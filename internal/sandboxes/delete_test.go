package sandboxes

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/testutil"
)

func shortenDestroyTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := destroyTimeout
	destroyTimeout = d
	t.Cleanup(func() { destroyTimeout = prev })
}

// DELETE of a leased lease ends it as destroyed/api with the token hash cleared
// and one destroy event naming the HTTP credential and no box; a terminal lease
// is 204 and unchanged; a malformed, unknown or foreign id is 404 and unchanged.
func TestDelete_EndsALiveLeaseOnce(t *testing.T) {
	emitter := &recordingEmitter{}
	env := newLeaseEnv(t, leaseEnvOptions{emitter: emitter})
	env.addWorkspace("other", "", "ready")
	id, token := env.issueToken("")
	foreign := env.insertLeaseAt(1, "other", "leased", env.clock.Now())
	expired := env.insertLeaseAt(2, "widgets", "expired", env.clock.Now())
	env.clock.Advance(5 * time.Minute)

	if rec := env.delete(id); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("DELETE: %d %q", rec.Code, rec.Body)
	}
	if s := env.readState(id); s.state != "destroyed" || s.endReason != "api" || s.endedAt != "2030-01-02T03:09:05Z" || s.updatedAt != s.endedAt || s.hash.Valid {
		t.Errorf("after DELETE: %+v", s)
	}
	if _, err := env.store.LookupByToken(token); err != ErrLeaseNotFound {
		t.Errorf("the token still works: %v", err)
	}
	events := eventsOf(emitter, audit.EventSandboxDestroy)
	if len(events) != 1 || events[0].ActorType != "api_key" || events[0].ActorID != owner || events[0].ResourceID != id || events[0].Workspace != "widgets" ||
		!reflect.DeepEqual(events[0].Metadata, map[string]any{"lease_id": id, "sandbox_id": nil, "reason": "api"}) {
		t.Errorf("destroy events = %+v", events)
	}

	before := map[string]leaseState{id: env.readState(id), expired: env.readState(expired), foreign: env.readState(foreign)}
	env.clock.Advance(time.Hour)
	for _, lid := range []string{id, expired} {
		if rec := env.delete(lid); rec.Code != http.StatusNoContent {
			t.Errorf("DELETE of a terminal lease: %d", rec.Code)
		}
	}
	for name, lid := range map[string]string{"malformed": "xyz", "unknown": leaseID(999), "foreign": foreign} {
		if rec := env.delete(lid); rec.Code != http.StatusNotFound || message(t, rec) != "sandbox lease not found" {
			t.Errorf("%s: %d %q", name, rec.Code, message(t, rec))
		}
	}
	for lid, s := range before {
		if env.readState(lid) != s {
			t.Errorf("lease %s changed: %+v -> %+v", lid, s, env.readState(lid))
		}
	}
	if len(emitter.recorded()) != 2 { // the lease event and the one destroy
		t.Errorf("events = %+v; want no second destroy", emitter.recorded())
	}
}

// DELETE of an active lease destroys the box under a 30s deadline through the
// gateway, then ends the lease and revokes the token on the very next RPC, the
// relayed calls included; a box the gateway no longer holds counts as destroyed.
func TestDelete_ActiveLeaseDestroysTheBox(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	id, token := env.issueToken("")
	gone := env.issue("")
	env.store.MarkActive(gone, "a-box-that-is-gone")
	gw := env.startGateway()
	client := gw.dial(t, token)
	sb := createVia(t, client)

	if rec := env.delete(id); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: %d %s", rec.Code, rec.Body)
	}
	if live := env.prov.live(); len(live) != 0 {
		t.Errorf("live boxes = %v", live)
	}
	if left := env.prov.allBoxes()[0].deadlines[0]; left <= 0 || left > 30*time.Second {
		t.Errorf("the box was closed with %v left; want a deadline within 30s", left)
	}
	if l := env.lease(id); l.State != "destroyed" || *l.EndReason != "api" {
		t.Errorf("lease = %s/%v", l.State, l.EndReason)
	}
	if _, err := client.List(context.Background()); !errors.Is(err, gateway.ErrUnauthenticated) {
		t.Errorf("List with the revoked token = %v", err)
	}
	if _, err := sb.Tools(context.Background(), sandbox.ToolOptions{}); err == nil {
		t.Error("a relayed call on the box of a deleted lease succeeded")
	}
	events := env.eventsOf(audit.EventSandboxDestroy)
	if len(events) != 1 || events[0].Metadata["sandbox_id"] != sb.ID() || events[0].Metadata["reason"] != "api" || events[0].ActorType != "api_key" {
		t.Errorf("destroy events = %+v", events)
	}
	if rec := env.delete(gone); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE of a lease whose box is gone: %d %s", rec.Code, rec.Body)
	}
	if l := env.lease(gone); l.State != "destroyed" || len(env.eventsOf(audit.EventSandboxDestroy)) != 2 {
		t.Errorf("a box the gateway does not hold: lease %s, %d events", l.State, len(env.eventsOf(audit.EventSandboxDestroy)))
	}
}

// A destroy that fails or outlasts its deadline is 502 sandbox_provider_error
// with the message scrubbed and changes nothing, so the DELETE can be repeated;
// the handler scrubs what the destroyer returns itself.
func TestDelete_ProviderFailureIsRetryable(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	id, token := env.issueToken("")
	gw := env.startGateway()
	createVia(t, gw.dial(t, token))
	wantProviderError := func(step string) {
		t.Helper()
		rec := env.delete(id)
		if rec.Code != http.StatusBadGateway || errorType(t, rec) != "sandbox_provider_error" {
			t.Fatalf("%s: %d %s; want 502 sandbox_provider_error", step, rec.Code, rec.Body)
		}
		containsNone(t, step+" response", rec.Body.String(), plantedSecret, "envval", "u:tok")
		if l := env.lease(id); l.State != "active" || l.EndReason != nil {
			t.Errorf("%s: lease %s/%v; want active", step, l.State, l.EndReason)
		}
		if _, err := env.store.LookupByToken(token); err != nil {
			t.Errorf("%s: the token stopped working", step)
		}
	}
	env.prov.setCloseErr(errors.New("podman rm failed: -e GIT_PAT=" + plantedSecret + " -e SECRET_ENV=envval at https://u:tok@registry.example"))
	wantProviderError("a failing provider")
	if !strings.Contains(env.delete(id).Body.String(), "[redacted]") {
		t.Error("the message is not the scrubbed text")
	}
	shortenDestroyTimeout(t, 100*time.Millisecond)
	env.prov.setCloseErr(nil)
	env.prov.setCloseBlock(make(chan struct{}))
	started := time.Now()
	wantProviderError("a provider past the deadline")
	if time.Since(started) > 5*time.Second {
		t.Error("DELETE did not give up at the deadline")
	}
	env.prov.setCloseBlock(nil)
	if rec := env.delete(id); rec.Code != http.StatusNoContent {
		t.Fatalf("the retried DELETE: %d %s", rec.Code, rec.Body)
	}
	if l := env.lease(id); l.State != "destroyed" || len(env.prov.live()) != 0 || len(env.eventsOf(audit.EventSandboxDestroy)) != 1 {
		t.Errorf("after the retry: %s, live %v, %d events", l.State, env.prov.live(), len(env.eventsOf(audit.EventSandboxDestroy)))
	}

	other := env.issue("")
	env.store.MarkActive(other, "box")
	env.svc.boxes = failingDestroyer{errors.New("engine said: SECRET_ENV=envval at https://u:tok@registry.example")}
	rec := env.delete(other)
	if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "envval") || strings.Contains(rec.Body.String(), "u:tok") || !strings.Contains(rec.Body.String(), "[redacted]") {
		t.Errorf("a destroyer's raw text: %d %s", rec.Code, rec.Body)
	}
}

// failingDestroyer is a BoxDestroyer that fails with err as it is.
type failingDestroyer struct{ err error }

func (f failingDestroyer) DestroyBox(context.Context, *Lease) error { return f.err }

// A box appearing between DELETE's read and its update is destroyed; concurrent
// DELETEs, and a DELETE after the client closed, end the lease once with one
// event; DELETE of an ended lease calls no destroyer.
func TestDelete_RacesEndTheLeaseOnce(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	gw := env.startGateway()
	id, token := env.issueToken("")
	var created sandbox.Sandbox
	env.svc.afterRead = func() {
		env.svc.afterRead = nil
		created = createVia(t, gw.dial(t, token))
	}
	if rec := env.delete(id); rec.Code != http.StatusNoContent || created == nil {
		t.Fatalf("DELETE with a box appearing under it: %d, created %v", rec.Code, created != nil)
	}
	if events := env.eventsOf(audit.EventSandboxDestroy); len(env.prov.live()) != 0 || len(events) != 1 || events[0].Metadata["sandbox_id"] != created.ID() {
		t.Errorf("live %v, events %+v; want the box that appeared destroyed and one event", env.prov.live(), events)
	}

	concurrent, token := env.issueToken("")
	createVia(t, gw.dial(t, token))
	var wg sync.WaitGroup
	codes := make([]int, 6)
	for i := range codes {
		wg.Go(func() { codes[i] = env.delete(concurrent).Code })
	}
	wg.Wait()
	for i, code := range codes {
		if code != http.StatusNoContent {
			t.Errorf("DELETE %d answered %d", i, code)
		}
	}
	closed, token := env.issueToken("")
	sb := createVia(t, gw.dial(t, token))
	if err := sb.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rec := env.delete(closed); rec.Code != http.StatusNoContent || *env.lease(closed).EndReason != "client" {
		t.Errorf("DELETE after the client closed: %d, reason %v; want 204 and client kept", rec.Code, env.lease(closed).EndReason)
	}
	if n := len(env.eventsOf(audit.EventSandboxDestroy)); n != 3 || len(env.prov.live()) != 0 {
		t.Errorf("%d destroy events, live %v; want 3 and none", n, env.prov.live())
	}

	env.svc.boxes = failingDestroyer{errors.New("a destroyer that must not be called")}
	for state, lid := range map[string]string{"destroyed": leaseID(1), "expired": leaseID(2), "failed": leaseID(3)} {
		insertRawLease(t, env.db, lid, "widgets", state, nil)
		testutil.Exec(t, env.db, `UPDATE sandbox_leases SET sandbox_id = 'box', ended_at = '2030-01-02T03:30:00Z', end_reason = 'api' WHERE id = ?`, lid)
		before := *env.lease(lid)
		if rec := env.delete(lid); rec.Code != http.StatusNoContent || !reflect.DeepEqual(before, *env.lease(lid)) {
			t.Errorf("DELETE of a %s lease: %d, changed %v", state, rec.Code, !reflect.DeepEqual(before, *env.lease(lid)))
		}
	}
	if len(env.eventsOf(audit.EventSandboxDestroy)) != 3 {
		t.Errorf("ended leases: %d events", len(env.eventsOf(audit.EventSandboxDestroy)))
	}
}
