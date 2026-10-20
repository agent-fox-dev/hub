package sandboxes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/hub/internal/audit"
)

// TS-27-56: A planted secret and a fixed environment value, in the errors of a
// provider that echoes them, reach no HTTP body, audit event, log line, gateway
// error, lease error field or box spec; and no lease token or token hash is
// logged.
// Requirements: 27-REQ-9.4, 27-REQ-9.7
func TestNothingSecretLeavesHub_TS_27_56(t *testing.T) {
	logs := captureDefaultLog(t)
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	gw := env.startGateway()
	ctx := context.Background()
	echo := func(what string) error {
		return fmt.Errorf("engine %s: run -e GIT_PAT=%s -e SECRET_ENV=envval https://user:pw@registry.example/tools", what, plantedSecret)
	}

	// Everything an HTTP caller, a gateway caller or an operator could see.
	var seen []string
	keep := func(v any) { seen = append(seen, fmt.Sprint(v)) }
	var tokens, ids []string

	// A Create the provider refuses with a message that echoes the values.
	idFail, tokenFail := env.issueToken(`{"profile":"tools"}`)
	tokens, ids = append(tokens, tokenFail), append(ids, idFail)
	env.prov.mu.Lock()
	env.prov.createErr = echo("create")
	env.prov.mu.Unlock()
	_, err := gw.dial(t, tokenFail).Create(ctx, sandbox.Spec{Image: "x"})
	if err == nil {
		t.Fatal("Create with a failing provider succeeded")
	}
	keep(err)
	env.prov.mu.Lock()
	env.prov.createErr = nil
	env.prov.mu.Unlock()

	// A box that is made, reported, listed and closed, with a Close that fails.
	idOK, tokenOK := env.issueToken(`{"profile":"tools"}`)
	tokens, ids = append(tokens, tokenOK), append(ids, idOK)
	client := gw.dial(t, tokenOK)
	sb := createVia(t, client)
	keep(fmt.Sprintf("%+v", sb.Spec()))
	infos, _ := client.List(ctx)
	keep(infos)
	env.prov.setCloseErr(echo("rm"))
	closeErr := sb.Close(ctx)
	if closeErr == nil {
		t.Fatal("Close with a failing provider succeeded")
	}
	keep(closeErr)
	env.prov.setCloseErr(nil)
	keep(sb.Close(ctx))

	// A DELETE the provider fails, and one it does not.
	idDel, tokenDel := env.issueToken(`{"profile":"tools"}`)
	tokens, ids = append(tokens, tokenDel), append(ids, idDel)
	createVia(t, gw.dial(t, tokenDel))
	env.prov.setCloseErr(echo("rm"))
	failed := env.deleteRecorded(idDel)
	if failed.Code != http.StatusBadGateway {
		t.Fatalf("DELETE with a failing provider answered %d: %s", failed.Code, failed.Body)
	}
	keep(failed.Body)
	env.prov.setCloseErr(nil)
	keep(env.deleteRecorded(idDel).Body)

	// What the lease routes show afterwards, and what is stored. The lease keeps its
	// fixed environment (spec 26 stores it for the gateway), but never a secret,
	// and the error it records is scrubbed.
	for _, id := range ids {
		keep(env.do(http.MethodGet, "/api/v1/workspaces/widgets/sandboxes/"+id, "", apiKeyAuth()).Body)
		if lease := env.lease(id); lease.Error != nil {
			keep(*lease.Error)
		}
		if row := strings.Join(env.rowText(id), "\n"); strings.Contains(row, plantedSecret) {
			t.Errorf("a column of lease %s holds the secret:\n%s", id, row)
		}
	}
	keep(env.do(http.MethodGet, "/api/v1/workspaces/widgets/sandboxes?all=true", "", apiKeyAuth()).Body)
	events, err := json.Marshal(env.emitter.recorded())
	if err != nil {
		t.Fatal(err)
	}
	keep(events)

	for i, out := range seen {
		for _, secret := range []string{plantedSecret, "envval", "user:pw"} {
			if strings.Contains(out, secret) {
				t.Errorf("output %d holds %q:\n%s", i, secret, out)
			}
		}
	}
	logged := logs.String()
	for _, secret := range []string{plantedSecret, "envval", "user:pw"} {
		if strings.Contains(logged, secret) {
			t.Errorf("a log line holds %q:\n%s", secret, logged)
		}
	}
	for _, token := range tokens {
		for _, secret := range []string{token, HashToken(token)} {
			if strings.Contains(logged, secret) {
				t.Errorf("a log line holds a token or its hash %q", secret)
			}
		}
	}
	if !strings.Contains(closeErr.Error(), "[redacted]") {
		t.Errorf("the Close error %q shows no sign of scrubbing", closeErr)
	}
	if lease := env.lease(idFail); lease.State != "failed" || lease.Error == nil || !strings.Contains(*lease.Error, "[redacted]") {
		t.Errorf("lease %s: state %q, error %v; want failed with the scrubbed message", idFail, lease.State, lease.Error)
	}
}

// TS-27-57: The three events carry the workspace, the resource and the right
// actor, and names only. A gateway event is the lease's, with the id of whoever
// issued the lease; a DELETE event is the HTTP credential's.
// Requirement: 27-REQ-9.5
func TestEventsCarryTheRightFields_TS_27_57(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	gw := env.startGateway()
	ctx := context.Background()

	// A lease an admin token issued: its events name "admin", not the owner.
	idA, tokenA := env.issueTokenAs(`{"profile":"tools"}`, adminAuth())
	sbA := createVia(t, gw.dial(t, tokenA))
	if err := sbA.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// A lease the owner issued, ended by an HTTP DELETE.
	idC, tokenC := env.issueToken(`{"profile":"tools"}`)
	createVia(t, gw.dial(t, tokenC))
	if code, body := env.deleteLease(idC); code != http.StatusNoContent {
		t.Fatalf("DELETE answered %d: %s", code, body)
	}
	// A lease that fails.
	idB, tokenB := env.issueToken(`{"profile":"tools"}`)
	if err := env.secrets.DeleteSecret("workspace", "widgets", "GIT_PAT"); err != nil {
		t.Fatal(err)
	}
	if _, err := gw.dial(t, tokenB).Create(ctx, sandbox.Spec{Image: "x"}); err == nil {
		t.Fatal("Create with a missing secret succeeded")
	}

	type want struct {
		eventType, resource, actorType, actorID string
	}
	cases := []want{
		{audit.EventSandboxCreate, idA, "lease", "admin"},
		{audit.EventSandboxDestroy, idA, "lease", "admin"},
		{audit.EventSandboxCreate, idC, "lease", leaseOwner},
		{audit.EventSandboxDestroy, idC, "api_key", leaseOwner},
		{audit.EventSandboxFailed, idB, "lease", leaseOwner},
	}
	for _, c := range cases {
		var found []audit.HubEvent
		for _, ev := range env.emitter.recorded() {
			if ev.EventType == c.eventType && ev.ResourceID == c.resource {
				found = append(found, ev)
			}
		}
		if len(found) != 1 {
			t.Errorf("%s events of %s = %d; want 1", c.eventType, c.resource, len(found))
			continue
		}
		ev := found[0]
		if ev.Workspace != "widgets" || ev.ResourceType != "sandbox" || ev.ResourceID != c.resource {
			t.Errorf("%s of %s: workspace %q, resource %s %s; want widgets, sandbox, the lease", c.eventType, c.resource, ev.Workspace, ev.ResourceType, ev.ResourceID)
		}
		if ev.ActorType != c.actorType || ev.ActorID != c.actorID {
			t.Errorf("%s of %s: actor %s %q; want %s %q", c.eventType, c.resource, ev.ActorType, ev.ActorID, c.actorType, c.actorID)
		}
	}

	raw, err := json.Marshal(env.emitter.recorded())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{plantedSecret, "envval"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("an event holds %q:\n%s", secret, raw)
		}
	}
	if n := len(env.emitter.recorded()); n != len(cases)+3 {
		t.Errorf("%d events; want the %d of the gateway and the DELETE plus the 3 lease events", n, len(cases))
	}
}

// outcome is what a caller sees of a run of the gateway, and the state it leaves.
type outcome struct {
	createErr, closeErr string
	deleteCode          int
	states              []string
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// runLifecycle creates and closes one box through the gateway, creates a second
// and deletes it over HTTP.
func runLifecycle(t *testing.T, opts leaseEnvOptions, failEmit bool) outcome {
	t.Helper()
	env := newBackendEnv(t, opts)
	if failEmit {
		env.emitter.err = errors.New("audit store down")
	}
	gw := env.startGateway()
	ctx := context.Background()

	var out outcome
	idA, tokenA := env.issueToken(`{"profile":"tools"}`)
	sb, err := gw.dial(t, tokenA).Create(ctx, sandbox.Spec{Image: "x"})
	out.createErr = errText(err)
	if err == nil {
		out.closeErr = errText(sb.Close(ctx))
	}
	idB, tokenB := env.issueToken(`{"profile":"tools"}`)
	createVia(t, gw.dial(t, tokenB))
	out.deleteCode, _ = env.deleteLease(idB)
	out.states = []string{env.lease(idA).State, env.lease(idB).State}
	return out
}

// TS-27-58: A missing or failing emitter changes the outcome of nothing; a
// failing one is logged.
// Requirement: 27-REQ-9.6
func TestEmitterNilOrFailingChangesNothing_TS_27_58(t *testing.T) {
	cfg := gatewayLeaseConfig()
	baseline := runLifecycle(t, leaseEnvOptions{config: cfg}, false)
	if baseline.createErr != "" || baseline.closeErr != "" || baseline.deleteCode != http.StatusNoContent ||
		!reflect.DeepEqual(baseline.states, []string{"destroyed", "destroyed"}) {
		t.Fatalf("the baseline run did not work: %+v", baseline)
	}

	if got := runLifecycle(t, leaseEnvOptions{config: gatewayLeaseConfig(), noEmitter: true}, false); !reflect.DeepEqual(got, baseline) {
		t.Errorf("with no emitter: %+v; want %+v", got, baseline)
	}

	logs := captureDefaultLog(t)
	if got := runLifecycle(t, leaseEnvOptions{config: gatewayLeaseConfig()}, true); !reflect.DeepEqual(got, baseline) {
		t.Errorf("with a failing emitter: %+v; want %+v", got, baseline)
	}
	out := logs.String()
	for _, eventType := range []string{audit.EventSandboxCreate, audit.EventSandboxDestroy} {
		if !strings.Contains(out, "audit: failed to emit "+eventType) {
			t.Errorf("the failed emit of %s was not logged:\n%s", eventType, out)
		}
	}
	if !strings.Contains(out, `"level":"ERROR"`) || !strings.Contains(out, "audit store down") {
		t.Errorf("the emit error was not logged as an error:\n%s", out)
	}
}
