package sandboxes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/hub/internal/audit"
)

// deleteLease sends DELETE for a lease of the workspace "widgets" as the owner.
// A DELETE that does not return within a minute fails the test.
func (e *backendEnv) deleteLease(id string) (code int, body string) {
	e.t.Helper()
	rec := e.deleteRecorded(id)
	return rec.Code, rec.Body.String()
}

// deleteRecorded is deleteLease with the whole response.
func (e *backendEnv) deleteRecorded(id string) *httptest.ResponseRecorder {
	e.t.Helper()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- e.do(http.MethodDelete, "/api/v1/workspaces/widgets/sandboxes/"+id, "", apiKeyAuth())
	}()
	select {
	case rec := <-done:
		return rec
	case <-time.After(time.Minute):
		e.t.Fatalf("DELETE of %s did not return", id)
		return nil
	}
}

// shortenDestroyTimeout sets the deadline of a box's destruction for the test.
func shortenDestroyTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := destroyTimeout
	destroyTimeout = d
	t.Cleanup(func() { destroyTimeout = prev })
}

// TS-27-15: POST, GET one, the workspace list and the admin list all carry
// gateway_url, the same on every lease in every state.
// Requirement: 27-REQ-2.7
func TestLeaseResponses_CarryTheGatewayURL_TS_27_15(t *testing.T) {
	const url = "http://127.0.0.1:4711"
	env := newBackendEnv(t, leaseEnvOptions{})
	env.svc.gatewayURL = url

	leased := env.issue(`{"profile":"tools"}`)
	active := env.issue(`{"profile":"tools"}`)
	destroyed := env.issue(`{"profile":"tools"}`)
	failed := env.issue(`{"profile":"tools"}`)
	if changed, err := env.store.MarkActive(active, "box"); err != nil || !changed {
		t.Fatalf("MarkActive = (%v, %v)", changed, err)
	}
	if changed, err := env.store.DestroyLease(destroyed, "leased", "api"); err != nil || !changed {
		t.Fatalf("DestroyLease = (%v, %v)", changed, err)
	}
	if changed, err := env.store.FailLease(failed, "boom"); err != nil || !changed {
		t.Fatalf("FailLease = (%v, %v)", changed, err)
	}

	posted := env.post("widgets", `{"profile":"tools"}`, apiKeyAuth())
	if posted.Code != http.StatusCreated {
		t.Fatalf("POST answered %d: %s", posted.Code, posted.Body)
	}
	if got := jsonObject(t, posted)["gateway_url"]; got != url {
		t.Errorf("POST: gateway_url = %v; want %q", got, url)
	}

	for _, id := range []string{leased, active, destroyed, failed} {
		rec := env.do(http.MethodGet, "/api/v1/workspaces/widgets/sandboxes/"+id, "", apiKeyAuth())
		if got := jsonObject(t, rec)["gateway_url"]; got != url {
			t.Errorf("GET one %s: gateway_url = %v; want %q", id, got, url)
		}
	}
	for what, path := range map[string]string{
		"workspace list": "/api/v1/workspaces/widgets/sandboxes?all=true",
		"admin list":     "/api/v1/sandboxes?all=true",
	} {
		auth := apiKeyAuth()
		if strings.HasPrefix(path, "/api/v1/sandboxes") {
			auth = adminAuth()
		}
		rec := env.do(http.MethodGet, path, "", auth)
		var list []map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) < 5 {
			t.Fatalf("%s: %d leases, %v; want at least 5 in every state", what, len(list), err)
		}
		states := map[string]bool{}
		for _, l := range list {
			states[l["state"].(string)] = true
			if l["gateway_url"] != url {
				t.Errorf("%s: gateway_url of %v = %v; want %q", what, l["id"], l["gateway_url"], url)
			}
		}
		for _, state := range []string{"leased", "active", "destroyed", "failed"} {
			if !states[state] {
				t.Errorf("%s: no lease in state %s was listed", what, state)
			}
		}
	}
}

// TS-27-48: DELETE of an active lease destroys the box under a deadline, ends the
// lease with the reason api, emits one destroy event for the HTTP credential and
// revokes the token.
// Requirements: 27-REQ-8.1, 27-REQ-8.6
func TestDelete_ActiveLeaseDestroysTheBox_TS_27_48(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	id, token := env.issueToken(`{"profile":"tools"}`)
	gw := env.startGateway()
	client := gw.dial(t, token)
	sb := createVia(t, client)

	code, body := env.deleteLease(id)
	if code != http.StatusNoContent {
		t.Fatalf("DELETE answered %d: %s", code, body)
	}

	if live := env.prov.live(); len(live) != 0 {
		t.Errorf("live boxes = %v; want none", live)
	}
	box := env.prov.boxes[0]
	if len(box.deadlines) == 0 || box.deadlines[0] <= 0 || box.deadlines[0] > 30*time.Second {
		t.Errorf("the box was closed with %v left on its deadline; want a deadline of at most 30s", box.deadlines)
	}
	lease := env.lease(id)
	if lease.State != "destroyed" || lease.EndReason == nil || *lease.EndReason != "api" {
		t.Errorf("lease state %q, end_reason %v; want destroyed by the api", lease.State, lease.EndReason)
	}

	if _, err := client.List(context.Background()); !errors.Is(err, gateway.ErrUnauthenticated) {
		t.Errorf("List with the revoked token = %v; want ErrUnauthenticated", err)
	}
	if _, err := sb.Tools(context.Background(), sandbox.ToolOptions{}); err == nil {
		t.Error("a relayed call on the box of a deleted lease succeeded")
	}
	if _, err := gateway.Dial(context.Background(), gw.URL(), gateway.Options{Token: token}); !errors.Is(err, gateway.ErrUnauthenticated) {
		t.Errorf("Dial with the revoked token = %v; want ErrUnauthenticated", err)
	}

	events := env.eventsOf(audit.EventSandboxDestroy)
	if len(events) != 1 {
		t.Fatalf("hub.sandbox.destroy events = %d; want 1", len(events))
	}
	ev := events[0]
	if ev.Metadata["lease_id"] != id || ev.Metadata["sandbox_id"] != sb.ID() || ev.Metadata["reason"] != "api" {
		t.Errorf("event metadata = %v; want the lease, the box and the reason api", ev.Metadata)
	}
	if ev.ActorType != "api_key" || ev.ActorID != "user-1" || ev.ResourceType != "sandbox" || ev.ResourceID != id || ev.Workspace != "widgets" {
		t.Errorf("event = %+v; want the actor of the HTTP credential on the lease's resource", ev)
	}
}

// TS-27-49: A box the gateway no longer holds counts as destroyed.
// Requirement: 27-REQ-8.2
func TestDelete_BoxNotHeldCountsAsDestroyed_TS_27_49(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	id := env.issue(`{"profile":"tools"}`)
	if changed, err := env.store.MarkActive(id, "a-box-that-is-gone"); err != nil || !changed {
		t.Fatalf("MarkActive = (%v, %v)", changed, err)
	}
	env.startGateway()

	code, body := env.deleteLease(id)
	if code != http.StatusNoContent {
		t.Fatalf("DELETE answered %d: %s", code, body)
	}
	lease := env.lease(id)
	if lease.State != "destroyed" || lease.EndReason == nil || *lease.EndReason != "api" {
		t.Errorf("lease state %q, end_reason %v; want destroyed by the api", lease.State, lease.EndReason)
	}
	events := env.eventsOf(audit.EventSandboxDestroy)
	if len(events) != 1 || events[0].Metadata["sandbox_id"] != "a-box-that-is-gone" {
		t.Errorf("destroy events = %+v; want one for the box", events)
	}
}

// TS-27-50: A destroy that fails, or that outlasts its deadline, answers 502
// sandbox_provider_error with the message scrubbed and changes nothing, so the
// DELETE can be repeated and succeeds when the provider does.
// Requirement: 27-REQ-8.3
func TestDelete_ProviderFailureIsRetryable_TS_27_50(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	id, token := env.issueToken(`{"profile":"tools"}`)
	gw := env.startGateway()
	createVia(t, gw.dial(t, token))

	unchanged := func(step string) {
		t.Helper()
		if lease := env.lease(id); lease.State != "active" || lease.EndReason != nil || lease.EndedAt != nil {
			t.Errorf("%s: lease state %q, end_reason %v; want active and not ended", step, lease.State, lease.EndReason)
		}
		if _, ok := lookupToken(env, token); !ok {
			t.Errorf("%s: the token stopped working", step)
		}
		if n := len(env.eventsOf(audit.EventSandboxDestroy)); n != 0 {
			t.Errorf("%s: hub.sandbox.destroy events = %d; want 0", step, n)
		}
	}
	wantProviderError := func(step string) {
		t.Helper()
		rec := env.deleteRecorded(id)
		if rec.Code != http.StatusBadGateway || errorType(t, rec) != "sandbox_provider_error" {
			t.Fatalf("%s: DELETE answered %d (%s); want 502 sandbox_provider_error: %s", step, rec.Code, errorType(t, rec), rec.Body)
		}
		if body := rec.Body.String(); strings.Contains(body, plantedSecret) || strings.Contains(body, "envval") {
			t.Errorf("%s: the response holds a secret: %s", step, body)
		}
		unchanged(step)
	}

	// The provider fails with an error that echoes the secret.
	env.prov.setCloseErr(fmt.Errorf("podman rm failed: -e GIT_PAT=%s -e SECRET_ENV=envval", plantedSecret))
	wantProviderError("a failing provider")
	rec := env.deleteRecorded(id)
	if !strings.Contains(rec.Body.String(), "[redacted]") {
		t.Errorf("the message is not the scrubbed text: %s", rec.Body)
	}

	// The provider does not answer before the deadline.
	shortenDestroyTimeout(t, 100*time.Millisecond)
	env.prov.setCloseErr(nil)
	env.prov.setCloseBlock(make(chan struct{}))
	started := time.Now()
	wantProviderError("a provider past the deadline")
	if took := time.Since(started); took > 5*time.Second {
		t.Errorf("DELETE took %v; want it to give up at the deadline", took)
	}

	// The provider works again.
	env.prov.setCloseBlock(nil)
	if code, body := env.deleteLease(id); code != http.StatusNoContent {
		t.Fatalf("the retried DELETE answered %d: %s", code, body)
	}
	if lease := env.lease(id); lease.State != "destroyed" {
		t.Errorf("lease state %q after the retry; want destroyed", lease.State)
	}
	if live := env.prov.live(); len(live) != 0 {
		t.Errorf("live boxes = %v; want none", live)
	}
	if n := len(env.eventsOf(audit.EventSandboxDestroy)); n != 1 {
		t.Errorf("hub.sandbox.destroy events = %d; want 1, from the DELETE that worked", n)
	}
}

// TS-27-51: DELETE of a leased lease ends it without a provider call, emits one
// event with no box, and the token no longer opens the gateway.
// Requirements: 27-REQ-8.4, 27-REQ-8.6
func TestDelete_LeasedLeaseHasNoBox_TS_27_51(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	id, token := env.issueToken(`{"profile":"tools"}`)
	gw := env.startGateway()

	if code, body := env.deleteLease(id); code != http.StatusNoContent {
		t.Fatalf("DELETE answered %d: %s", code, body)
	}
	if n := len(env.prov.boxes); n != 0 {
		t.Errorf("the provider made %d boxes; want none", n)
	}
	lease := env.lease(id)
	if lease.State != "destroyed" || lease.EndReason == nil || *lease.EndReason != "api" || lease.SandboxID != nil {
		t.Errorf("lease state %q, end_reason %v, sandbox_id %v; want destroyed by the api with no box", lease.State, lease.EndReason, lease.SandboxID)
	}
	events := env.eventsOf(audit.EventSandboxDestroy)
	if len(events) != 1 {
		t.Fatalf("hub.sandbox.destroy events = %d; want 1", len(events))
	}
	if box, present := events[0].Metadata["sandbox_id"]; !present || box != nil || events[0].Metadata["reason"] != "api" {
		t.Errorf("event metadata = %v; want sandbox_id null and reason api", events[0].Metadata)
	}
	if _, err := gateway.Dial(context.Background(), gw.URL(), gateway.Options{Token: token}); !errors.Is(err, gateway.ErrUnauthenticated) {
		t.Errorf("Dial with the revoked token = %v; want ErrUnauthenticated", err)
	}
}

// TS-27-52: A lease that gets its box between DELETE's read and its update is read
// again once, and the box is destroyed.
// Requirement: 27-REQ-8.5
func TestDelete_LeaseChangedUnderIt_TS_27_52(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	id, token := env.issueToken(`{"profile":"tools"}`)
	gw := env.startGateway()
	var created sandbox.Sandbox
	hooked := 0
	env.svc.afterRead = func() {
		hooked++
		env.svc.afterRead = nil
		created = createVia(t, gw.dial(t, token))
	}

	if code, body := env.deleteLease(id); code != http.StatusNoContent {
		t.Fatalf("DELETE answered %d: %s", code, body)
	}
	if hooked != 1 || created == nil {
		t.Fatalf("the lease was not changed between the read and the update (hook calls: %d)", hooked)
	}
	if live := env.prov.live(); len(live) != 0 {
		t.Errorf("live boxes = %v; want none: the box that appeared must be destroyed", live)
	}
	lease := env.lease(id)
	if lease.State != "destroyed" || lease.EndReason == nil || *lease.EndReason != "api" {
		t.Errorf("lease state %q, end_reason %v; want destroyed by the api", lease.State, lease.EndReason)
	}
	events := env.eventsOf(audit.EventSandboxDestroy)
	if len(events) != 1 || events[0].Metadata["sandbox_id"] != created.ID() {
		t.Errorf("destroy events = %+v; want exactly one, for the box %s", events, created.ID())
	}
}

// TS-27-53: DELETE of a lease that has ended answers 204 as before, calls no
// provider and emits nothing.
// Requirement: 27-REQ-8.7
func TestDelete_EndedLeases_TS_27_53(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	env.startGateway()
	ids := map[string]string{"destroyed": leaseID(1), "expired": leaseID(2), "failed": leaseID(3)}
	for state, id := range ids {
		insertRawLease(t, env.db, id, "widgets", state, nil)
	}
	for state, id := range ids {
		if code, body := env.deleteLease(id); code != http.StatusNoContent {
			t.Errorf("DELETE of a %s lease answered %d: %s", state, code, body)
		}
		if lease := env.lease(id); lease.State != state {
			t.Errorf("a %s lease became %s", state, lease.State)
		}
	}
	if n := len(env.prov.boxes); n != 0 {
		t.Errorf("the provider made %d boxes; want none", n)
	}
	if n := len(env.eventsOf(audit.EventSandboxDestroy)); n != 0 {
		t.Errorf("hub.sandbox.destroy events = %d; want 0", n)
	}
}

// Several DELETEs of one active lease at once all answer 204 and emit one event
// between them: the conditional update lets one of them win.
func TestDelete_ConcurrentDeletesEmitOnce(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	id, token := env.issueToken(`{"profile":"tools"}`)
	gw := env.startGateway()
	createVia(t, gw.dial(t, token))

	var wg sync.WaitGroup
	codes := make([]int, 6)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], _ = env.deleteLease(id)
		}()
	}
	wg.Wait()
	for i, code := range codes {
		if code != http.StatusNoContent {
			t.Errorf("DELETE %d answered %d; want 204", i, code)
		}
	}
	if n := len(env.eventsOf(audit.EventSandboxDestroy)); n != 1 {
		t.Errorf("hub.sandbox.destroy events = %d; want 1", n)
	}
	if live := env.prov.live(); len(live) != 0 {
		t.Errorf("live boxes = %v; want none", live)
	}
}

// A client that closes its box and a DELETE that arrives afterwards (or the other
// way round) end the lease once, with one event.
func TestDelete_AfterTheClientClosed(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	id, token := env.issueToken(`{"profile":"tools"}`)
	gw := env.startGateway()
	sb := createVia(t, gw.dial(t, token))
	if err := sb.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if code, body := env.deleteLease(id); code != http.StatusNoContent {
		t.Fatalf("DELETE answered %d: %s", code, body)
	}
	lease := env.lease(id)
	if lease.State != "destroyed" || lease.EndReason == nil || *lease.EndReason != "client" {
		t.Errorf("lease state %q, end_reason %v; want destroyed by the client, as it was", lease.State, lease.EndReason)
	}
	if n := len(env.eventsOf(audit.EventSandboxDestroy)); n != 1 {
		t.Errorf("hub.sandbox.destroy events = %d; want 1", n)
	}
}

// failingBoxes is a box destroyer whose error is not scrubbed.
type failingBoxes struct{ err error }

func (f failingBoxes) DestroyBox(context.Context, *Lease) error { return f.err }

// The handler scrubs what a destroyer returns itself, with the lease's fixed
// environment, whatever the destroyer did to it.
func TestDelete_ProviderErrorIsScrubbedByTheHandler(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	id := env.issue(`{"profile":"tools"}`)
	if changed, err := env.store.MarkActive(id, "box"); err != nil || !changed {
		t.Fatalf("MarkActive = (%v, %v)", changed, err)
	}
	env.svc.boxes = failingBoxes{err: errors.New("engine said: SECRET_ENV=envval at https://u:tok@registry.example")}

	rec := env.deleteRecorded(id)
	if rec.Code != http.StatusBadGateway || errorType(t, rec) != "sandbox_provider_error" {
		t.Fatalf("DELETE answered %d (%s); want 502 sandbox_provider_error", rec.Code, errorType(t, rec))
	}
	if body := rec.Body.String(); strings.Contains(body, "envval") || strings.Contains(body, "u:tok") || !strings.Contains(body, "[redacted]") {
		t.Errorf("the message is not scrubbed: %s", body)
	}
	if lease := env.lease(id); lease.State != "active" {
		t.Errorf("lease state %q; want active", lease.State)
	}
}
