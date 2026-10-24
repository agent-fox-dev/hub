package sandboxes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/hub/internal/audit"
)

// notFoundForms are the ways a destroy says the box is gone; refusalForms the
// ways a call says the remote refused hub's token, each echoing a secret.
func notFoundForms() map[string]error {
	return map[string]error{
		"ErrNotHeld wrapped":       fmt.Errorf("gatewayd: destroy: %w", gatewayd.ErrNotHeld),
		"NotFound status":          status.Error(codes.NotFound, "no such sandbox"),
		"NotFound wrapped, joined": errors.Join(errors.New("also"), fmt.Errorf("again: %w", status.Error(codes.NotFound, "gone"))),
	}
}

func refusalForms() map[string]error {
	return map[string]error{
		"Unauthenticated status":     status.Error(codes.Unauthenticated, "refused "+plantedSecret),
		"PermissionDenied wrapped":   fmt.Errorf("gateway https://gw.example: destroy: %w", status.Error(codes.PermissionDenied, "refused "+plantedSecret)),
		"gateway.ErrUnauthenticated": fmt.Errorf("%w: gateway https://gw.example refused the token: %s", gateway.ErrUnauthenticated, plantedSecret),
	}
}

// The conformance table of the remote's errors: how hub reads them, and what a
// not-found or a refusal does at every site a box is destroyed or made. A
// not-found counts as destroyed for the caller's Close, DELETE and the sweep; a
// refusal reaches the caller as Unavailable with the message scrubbed and logs
// one error naming sandbox.gateway_token, a Create failing the lease, a destroy
// leaving it active and DELETE answering 502; a provider that is not remote and
// a remote that fails otherwise log nothing about the token.
func TestRemote_ErrorConformance(t *testing.T) {
	for name, err := range notFoundForms() {
		if !isNotFound(err) || isRemoteRefusal(err) {
			t.Errorf("%s: isNotFound %t, isRemoteRefusal %t", name, isNotFound(err), isRemoteRefusal(err))
		}
	}
	for name, err := range refusalForms() {
		if isNotFound(err) || !isRemoteRefusal(err) {
			t.Errorf("%s: isNotFound %t, isRemoteRefusal %t", name, isNotFound(err), isRemoteRefusal(err))
		}
	}
	for name, err := range map[string]error{"nil": nil, "plain": errors.New("boom"), "Unavailable": status.Error(codes.Unavailable, "down"), "context": context.DeadlineExceeded, "capacity": fmt.Errorf("%w: full", gateway.ErrCapacity)} {
		if isNotFound(err) || isRemoteRefusal(err) {
			t.Errorf("%s is read as not found or a refusal", name)
		}
	}
	if (*Service)(nil).isRemote() || (*Service)(nil).remoteFailure("create", errors.New("x"), "x").Error() != "x" {
		t.Error("a nil service is remote")
	}

	for name, form := range notFoundForms() {
		t.Run("not found: "+name, func(t *testing.T) {
			env := newScriptedEnv(t, nil)
			for site, end := range map[string]func(id string, box sandbox.Sandbox) string{
				"Close": func(_ string, box sandbox.Sandbox) string {
					return fmt.Sprint(box.Close(context.Background()), " client")
				},
				"DELETE": func(id string, _ sandbox.Sandbox) string { return fmt.Sprint(env.delete(id).Code, " api") },
				"sweep": func(string, sandbox.Sandbox) string {
					env.clock.Advance(2 * time.Hour)
					return fmt.Sprint(env.gw.sweeper.Sweep(context.Background()), " expired")
				},
			} {
				env.prov.setCloseErr(nil)
				id, token, box := env.activeLease()
				env.prov.setCloseErr(form)
				result := end(id, box)
				reason := result[strings.LastIndex(result, " ")+1:]
				if !strings.HasPrefix(result, "<nil> ") && !strings.HasPrefix(result, "204 ") {
					t.Errorf("%s answered %s; want success for a box that is gone", site, result)
				}
				if l := env.lease(id); l.State != "destroyed" || *l.EndReason != reason {
					t.Errorf("%s: lease %s/%v; want destroyed/%s", site, l.State, l.EndReason, reason)
				}
				if _, err := env.store.LookupByToken(token); err == nil {
					t.Errorf("%s: the token still works", site)
				}
			}
			if strings.Contains(env.logs.String(), "destroying a box failed") || refusalRecords(t, env.logs) != 0 {
				t.Errorf("a not-found was logged as a failure:\n%s", env.logs)
			}
		})
	}

	for name, form := range refusalForms() {
		t.Run("refusal: "+name, func(t *testing.T) {
			env := newScriptedEnv(t, nil)
			records := 0
			oneMore := func(step string) {
				t.Helper()
				if records++; refusalRecords(t, env.logs) != records {
					t.Errorf("after %s %d records name sandbox.gateway_token; want %d", step, refusalRecords(t, env.logs), records)
				}
			}
			env.prov.setCreateErr(form)
			id := env.issue("")
			_, err := env.create(id, sandbox.Spec{})
			wantCode(t, err, codes.Unavailable)
			if l := env.lease(id); l.State != "failed" || l.Error == nil || strings.Contains(*l.Error, plantedSecret) || strings.Contains(err.Error(), plantedSecret) {
				t.Errorf("a refused Create: %v, lease %+v", err, l)
			}
			oneMore("a refused Create")
			id, token := env.issueToken("")
			_, err = env.gw.dial(t, token).Create(context.Background(), sandbox.Spec{Image: "x"})
			wantCode(t, err, codes.Unavailable)
			if env.lease(id).State != "failed" || strings.Contains(err.Error(), plantedSecret) {
				t.Errorf("through the gateway: %v, lease %s", err, env.lease(id).State)
			}
			oneMore("a refused Create through the gateway")
			env.prov.setCreateErr(nil)
			id, token, _ = env.activeLease()
			env.prov.setCloseErr(form)
			rec := env.delete(id)
			if rec.Code != http.StatusBadGateway || errorType(t, rec) != "sandbox_provider_error" || strings.Contains(rec.Body.String(), plantedSecret) || env.lease(id).State != "active" {
				t.Errorf("DELETE: %d %s, lease %s", rec.Code, rec.Body, env.lease(id).State)
			}
			if _, err := env.store.LookupByToken(token); err != nil {
				t.Error("the token stopped working")
			}
			oneMore("a refused DELETE")
			env.prov.setCloseErr(nil)
			id, _, box := env.activeLease()
			env.prov.setCloseErr(form)
			if err := box.Close(context.Background()); err == nil || strings.Contains(err.Error(), plantedSecret) || env.lease(id).State != "active" {
				t.Errorf("Close: %v, lease %s", err, env.lease(id).State)
			}
			oneMore("a refused Close")
			env.prov.setCloseErr(nil)
			env.prov.setReap(nil, form)
			if err := env.gw.sweeper.Sweep(context.Background()); err == nil {
				t.Error("the sweep did not report the failing Reap")
			}
			oneMore("a refused Reap")
			containsNone(t, "the log", env.logs.String(), plantedSecret)
			if n := len(env.eventsOf(audit.EventSandboxDestroy)); n != 0 {
				t.Errorf("%d destroy events; want none", n)
			}
		})
	}

	logs := captureDefaultLog(t)
	podman := newBackendEnv(t, leaseEnvOptions{})
	podman.prov.setCreateErr(status.Error(codes.Unauthenticated, "the engine says no"))
	if _, err := podman.create(podman.issue(""), sandbox.Spec{}); err == nil || status.Code(err) == codes.Unavailable || refusalRecords(t, logs) != 0 {
		t.Errorf("a provider that is not remote: %v, %d records", err, refusalRecords(t, logs))
	}
	down := newScriptedEnv(t, nil)
	down.prov.setCreateErr(status.Error(codes.Unavailable, "remote down"))
	down.prov.setReap(nil, errors.New("the remote's reap failed"))
	down.create(down.issue(""), sandbox.Spec{})
	down.gw.sweeper.Sweep(context.Background())
	if refusalRecords(t, down.logs) != 0 {
		t.Errorf("a remote that fails otherwise logged %d records naming the token", refusalRecords(t, down.logs))
	}
}

// Against a real remote gatewayd: a lease, Dial and Create make one box on the
// remote with the lease's image, TTL and merged env, through hub's own gateway
// (never the configured remote), with only hub's token shown to the remote and
// no Create ever retried; refusals and a token hub never minted cost the remote
// nothing; DELETE and the caller's Close destroy the box on the remote and
// revoke the lease token; no engine binary ever runs.
func TestRemote_LeaseLifecycleOnTheRemote(t *testing.T) {
	bin := t.TempDir()
	marker := bin + "/podman-ran"
	if err := os.WriteFile(bin+"/podman", []byte("#!/bin/sh\necho ran > '"+marker+"'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	env := newRemoteEnv(t, leaseEnvOptions{})
	first, second := env.mustIssue(""), env.mustIssue(`{"profile":"data"}`)
	if first["gateway_url"] != env.gw.URL() || strings.Contains(first["gateway_url"].(string), env.remote.host()) {
		t.Errorf("gateway_url = %v; want hub's own gateway, not the remote %s", first["gateway_url"], env.remote.url())
	}
	client := dialGateway(t, first["gateway_url"].(string), first["token"].(string))
	box := createVia(t, client)
	secondBox := createVia(t, env.gw.dial(t, second["token"].(string)))
	spec := env.prov.lastSpec()
	if env.prov.creates() != 2 || spec.Image != "registry.example/data:1" || spec.TTL != 30*time.Minute {
		t.Errorf("the remote saw %d Creates, last %+v", env.prov.creates(), spec)
	}
	if first := env.prov.allBoxes()[0].spec; first.Env["SECRET_ENV"] != "envval" || first.Env["GIT_PAT"] != plantedSecret || len(first.Env) != 2 {
		t.Errorf("the first box's env = %v", first.Env)
	}
	if live := env.remoteLive(); len(live) != 2 {
		t.Errorf("the remote holds %v", live)
	}
	for _, token := range env.remote.tokens() {
		if token != remoteToken {
			t.Errorf("the remote was shown a token of %d bytes that is not hub's", len(token))
		}
	}
	// The agent uses its box through both gateways.
	tools, err := box.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil || len(tools) == 0 {
		t.Errorf("Tools through the two gateways: %d tools, %v", len(tools), err)
	}

	creates, rpcs := env.prov.creates(), env.remote.rpcs()
	if _, err := client.Create(context.Background(), sandbox.Spec{Image: "x"}); err == nil || !strings.Contains(err.Error(), "only a leased lease") {
		t.Errorf("a second Create = %v", err)
	}
	if _, err := gateway.Dial(context.Background(), env.gw.URL(), gateway.Options{Token: "never-minted-token-0123456789-abcdef"}); !errors.Is(err, gateway.ErrUnauthenticated) {
		t.Errorf("Dial with a token hub never minted = %v", err)
	}
	if env.prov.creates() != creates || env.remote.rpcs() != rpcs {
		t.Error("the refusals reached the remote")
	}
	env.prov.setCreateErr(errors.New("the remote's backend failed"))
	id3, token3 := env.issueToken("")
	if _, err := env.gw.dial(t, token3).Create(context.Background(), sandbox.Spec{Image: "x"}); err == nil || env.lease(id3).State != "failed" {
		t.Errorf("a Create the remote fails: %v, lease %s", err, env.lease(id3).State)
	}
	time.Sleep(200 * time.Millisecond)
	if env.prov.creates() != creates+1 {
		t.Errorf("the remote saw %d Creates; want one more, never retried", env.prov.creates()-creates)
	}
	env.prov.setCreateErr(nil)

	if rec := env.delete(first["id"].(string)); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: %d %s", rec.Code, rec.Body)
	}
	if err := secondBox.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if live := env.remoteLive(); len(live) != 0 || len(env.prov.live()) != 0 {
		t.Errorf("after DELETE and Close the remote holds %v", live)
	}
	for name, token := range map[string]string{"DELETE": first["token"].(string), "Close": second["token"].(string)} {
		if _, err := gateway.Dial(context.Background(), env.gw.URL(), gateway.Options{Token: token}); !errors.Is(err, gateway.ErrUnauthenticated) {
			t.Errorf("after %s the lease token still works: %v", name, err)
		}
	}
	if l := env.lease(second["id"].(string)); *l.EndReason != "client" || *env.lease(first["id"].(string)).EndReason != "api" {
		t.Error("the end reasons are not api and client")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the sentinel podman binary was run")
	}
}

// While the remote is down, leases are issued and profiles listed; a Create
// fails the lease with a scrubbed Unavailable; DELETE answers 502 and changes
// nothing; Close fails and leaves the lease active; the sweep ends an overdue
// lease, logs the destroy it cannot make and tries the box again on the next
// run, which destroys it once the remote is back; the next Create then works
// without a redial. No surface names the token, the URL or the host.
func TestRemote_OutageAndRecovery(t *testing.T) {
	env := newRemoteEnv(t, leaseEnvOptions{metrics: audit.NewMetrics()})
	secrets := []string{remoteToken, env.remote.url(), env.remote.host()}
	active, activeToken, box := env.activeLease()
	overdue, _, _ := env.activeLease()
	env.remote.stop()

	down := env.mustIssue("")
	if rec := getProfiles(t, profilesEcho(env.profile), ownerKey()); rec.Code != http.StatusOK {
		t.Errorf("the profile listing answered %d while the remote is down", rec.Code)
	}
	began := time.Now()
	_, err := env.gw.dial(t, down["token"].(string)).Create(context.Background(), sandbox.Spec{Image: "x"})
	if err == nil || status.Code(err) != codes.Unavailable || time.Since(began) > 5*time.Second {
		t.Errorf("Create against a remote that is down = %v after %v", err, time.Since(began))
	}
	if l := env.lease(down["id"].(string)); l.State != "failed" || l.Error == nil || len(env.eventsOf(audit.EventSandboxFailed)) != 1 {
		t.Errorf("the lease = %+v", l)
	} else {
		containsNone(t, "the lease's error", *l.Error, secrets...)
	}
	containsNone(t, "the caller's error", err.Error(), secrets...)
	rec := env.delete(active)
	if rec.Code != http.StatusBadGateway || errorType(t, rec) != "sandbox_provider_error" || env.lease(active).State != "active" {
		t.Errorf("DELETE = %d %s, lease %s", rec.Code, rec.Body, env.lease(active).State)
	}
	containsNone(t, "the DELETE body", rec.Body.String(), secrets...)
	if p, err := gateway.Dial(context.Background(), env.gw.URL(), gateway.Options{Token: activeToken}); err != nil {
		t.Errorf("hub's gateway refused the lease token: %v", err)
	} else {
		p.Close()
	}
	if err := box.Close(context.Background()); err == nil || env.lease(active).State != "active" {
		t.Errorf("Close = %v, lease %s; want an error and active", err, env.lease(active).State)
	}
	for range 2 {
		if _, err := box.Tools(context.Background(), sandbox.ToolOptions{}); err != nil {
			containsNone(t, "a relayed call's error", err.Error(), secrets...)
		}
	}
	env.clock.Advance(2 * time.Hour)
	if err := env.gw.sweeper.Sweep(context.Background()); err == nil {
		t.Error("the sweep reported no failure")
	}
	if l := env.lease(overdue); l.State != "destroyed" || *l.EndReason != "expired" || len(env.gw.backend.heldBoxes()) != 2 {
		t.Errorf("after the sweep: lease %s/%v, held %v", l.State, l.EndReason, env.gw.backend.heldBoxes())
	}
	containsNone(t, "the log", env.logs.String(), secrets...)
	events, _ := json.Marshal(env.emitter.recorded())
	containsNone(t, "the events", string(events), secrets...)
	body := env.get("/api/v1/workspaces/widgets/sandboxes?all=true", ownerKey()).Body.String()
	containsNone(t, "the lease listing", body, secrets...)

	env.remote.restart()
	var sb sandbox.Sandbox
	waitLong(t, "a Create to succeed on the restarted remote", func() bool {
		lease := env.mustIssue(`{"profile":"data"}`)
		got, err := env.gw.dial(t, lease["token"].(string)).Create(context.Background(), sandbox.Spec{Image: "x"})
		sb = got
		return err == nil
	})
	waitLong(t, "a sweep to destroy the held boxes", func() bool {
		return env.gw.sweeper.Sweep(context.Background()) == nil && len(env.gw.backend.heldBoxes()) == 1
	})
	if live := env.remoteLive(); len(live) != 1 || live[0] != sb.ID() || env.factory.callCount() != 1 {
		t.Errorf("the remote holds %v, the factory was called %d times; want the new box only and one dial", live, env.factory.callCount())
	}
}

// waitLong polls for up to twenty seconds: gRPC reconnects with a backoff of a
// second or more after the remote comes back.
func waitLong(t *testing.T, what string, step func() bool) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); !step(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// The production dialer keeps the reason a dial failed and drops the address,
// so gRPC's status text, which gatewayd relays to a holder, does not name the
// remote; the token is in no response or /metrics.
func TestRemote_DialErrorsAndResponsesNameNothing(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := l.Addr().String()
	l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := dialWithoutAddress(ctx, closed); err == nil || strings.Contains(err.Error(), "127.0.0.1") || !strings.Contains(err.Error(), "refused") {
		t.Errorf("a closed port: %v", err)
	}
	if _, err := dialWithoutAddress(ctx, "no-such-host.invalid:443"); err == nil || strings.Contains(err.Error(), "no-such-host") {
		t.Errorf("an unknown host: %v", err)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := dialWithoutAddress(cancelled, closed); err == nil || strings.Contains(err.Error(), closed) {
		t.Errorf("a cancelled dial: %v", err)
	}
	if dialReason(context.DeadlineExceeded) != "timed out" || dialReason(errors.New("x")) != "failed" {
		t.Error("dialReason")
	}

	m := audit.NewMetrics()
	env := newRemoteEnv(t, leaseEnvOptions{metrics: m})
	_, token := env.issueToken("")
	createVia(t, env.gw.dial(t, token))
	rec := httptest.NewRecorder()
	m.MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for what, body := range map[string]string{
		"/metrics":         rec.Body.String(),
		"an error body":    env.post("no-such-workspace", "{}", ownerKey()).Body.String(),
		"a lease":          env.post("widgets", `{"profile":"data"}`, ownerKey()).Body.String(),
		"the listing":      env.get("/api/v1/workspaces/widgets/sandboxes", ownerKey()).Body.String(),
		"the profiles":     getProfiles(t, profilesEcho(env.profile), ownerKey()).Body.String(),
		"the capabilities": env.backend.Capabilities().Notes,
	} {
		if body == "" {
			t.Errorf("%s is empty", what)
		}
		containsNone(t, what, body, remoteToken, env.remote.url(), env.remote.host())
	}
}
