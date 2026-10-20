package sandboxes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/hub/internal/audit"
)

// apikitParse reads a lease timestamp.
func apikitParse(t *testing.T, value string) (time.Time, error) {
	t.Helper()
	ts, err := apikit.ParseUTC(value)
	if err != nil {
		t.Fatalf("reading the time %q: %v", value, err)
	}
	return ts, err
}

// TS-27-41: Capabilities is the provider's, with the network modes cut down to
// those the deployment allows.
// Requirement: 27-REQ-7.1
func TestBackendCapabilities_NarrowsNetworkModes_TS_27_41(t *testing.T) {
	cfg := defaultLeaseConfig()
	cfg.NetworkModes = []string{"none", "unrestricted"}
	env := newBackendEnv(t, leaseEnvOptions{config: cfg})

	got := env.backend.Capabilities()
	if want := []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkUnrestricted}; !reflect.DeepEqual(got.Network, want) {
		t.Errorf("Network = %v; want %v: the provider's modes that network_modes allows", got.Network, want)
	}
	rest, provided := got, env.prov.Capabilities()
	rest.Network, provided.Network = nil, nil
	if !reflect.DeepEqual(rest, provided) {
		t.Errorf("Capabilities = %+v; want the provider's %+v apart from the network modes", rest, provided)
	}
	if modes := env.prov.Capabilities().Network; len(modes) != 3 {
		t.Errorf("the provider's own capabilities were narrowed: %v", modes)
	}
}

// TS-27-42: A lease holder lists its own box and no other, and a lease that has no
// box yet lists nothing. The gateway answers List from the boxes it holds for
// each principal, and the principal is the lease.
// Requirement: 27-REQ-7.2
func TestGateway_ListsOnlyTheLeasesBox_TS_27_42(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	idA, tokenA := env.issueToken(`{"profile":"tools"}`)
	_, tokenB := env.issueToken(`{"profile":"tools"}`)
	_, tokenC := env.issueToken(`{"profile":"tools"}`)
	gw := env.startGateway()

	a, b := createVia(t, gw.dial(t, tokenA)), createVia(t, gw.dial(t, tokenB))
	if live := env.prov.live(); len(live) != 2 {
		t.Fatalf("live boxes = %v; want two", live)
	}

	infos, err := gw.dial(t, tokenA).List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 1 || infos[0].ID != a.ID() {
		t.Fatalf("List under lease A = %+v; want exactly its box %s, not %s", infos, a.ID(), b.ID())
	}
	if infos[0].Labels["hub.lease"] != idA {
		t.Errorf("labels = %v; want hub.lease = %s", infos[0].Labels, idA)
	}

	if infos, err := gw.dial(t, tokenC).List(context.Background()); err != nil || len(infos) != 0 {
		t.Errorf("List under a lease with no box = (%+v, %v); want an empty list", infos, err)
	}
}

// TS-27-43: Close through the gateway destroys the box, ends the lease with the
// reason client and emits one destroy event; a box that is already gone at the
// provider is closed all the same.
// Requirements: 27-REQ-7.3, 27-REQ-7.5
func TestGateway_CloseDestroysTheBox_TS_27_43(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	first, firstToken := env.issueToken(`{"profile":"tools"}`)
	second, secondToken := env.issueToken(`{"profile":"tools"}`)
	gw := env.startGateway()
	sbFirst, sbSecond := createVia(t, gw.dial(t, firstToken)), createVia(t, gw.dial(t, secondToken))

	// The provider lost the second box before the holder closed it.
	var gone *recordedBox
	for _, box := range env.prov.boxes {
		if box.ID() == sbSecond.ID() {
			gone = box
		}
	}
	if err := gone.Close(context.Background()); err != nil {
		t.Fatalf("closing the second box at the provider: %v", err)
	}

	for _, c := range []struct {
		id  string
		box sandbox.Sandbox
	}{{first, sbFirst}, {second, sbSecond}} {
		if err := c.box.Close(context.Background()); err != nil {
			t.Fatalf("Close of %s: %v", c.box.ID(), err)
		}
		lease := env.lease(c.id)
		if lease.State != "destroyed" || lease.EndReason == nil || *lease.EndReason != "client" || lease.EndedAt == nil {
			t.Errorf("lease %s: state %q, end_reason %v, ended_at %v; want destroyed by the client", c.id, lease.State, lease.EndReason, lease.EndedAt)
		}
		var found []audit.HubEvent
		for _, ev := range env.eventsOf(audit.EventSandboxDestroy) {
			if ev.ResourceID == c.id {
				found = append(found, ev)
			}
		}
		want := map[string]any{"lease_id": c.id, "sandbox_id": c.box.ID(), "reason": "client"}
		if len(found) != 1 || !reflect.DeepEqual(found[0].Metadata, want) {
			t.Errorf("destroy events of %s = %+v; want exactly one with %v", c.id, found, want)
		}
	}
	if live := env.prov.live(); len(live) != 0 {
		t.Errorf("live boxes = %v; want none", live)
	}
}

// TS-27-44: A lease cannot destroy a box that is not its own: gatewayd answers
// as it does for an id it never held, and nothing at the provider or on either
// lease changes.
// Requirement: 27-REQ-7.4
func TestGateway_DestroyOfAnotherLeasesBox_TS_27_44(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	idA, tokenA := env.issueToken(`{"profile":"tools"}`)
	idB, tokenB := env.issueToken(`{"profile":"tools"}`)
	gw := env.startGateway()
	a, _ := createVia(t, gw.dial(t, tokenA)), createVia(t, gw.dial(t, tokenB))
	beforeA, beforeB := *env.lease(idA), *env.lease(idB)

	if err := gw.dial(t, tokenB).Destroy(context.Background(), a.ID()); err != nil {
		t.Fatalf("Destroy of a box that is not the lease's own: %v; want the idempotent nil gatewayd gives", err)
	}
	if live := env.prov.live(); len(live) != 2 {
		t.Errorf("live boxes = %v; want both: lease B's token destroyed lease A's box", live)
	}
	if afterA, afterB := *env.lease(idA), *env.lease(idB); !reflect.DeepEqual(beforeA, afterA) || !reflect.DeepEqual(beforeB, afterB) {
		t.Errorf("a lease changed: A %+v -> %+v, B %+v -> %+v", beforeA, afterA, beforeB, afterB)
	}
	if n := len(env.eventsOf(audit.EventSandboxDestroy)); n != 0 {
		t.Errorf("hub.sandbox.destroy events = %d; want 0", n)
	}
}

// TS-27-45: When the provider cannot destroy the box, Close returns the error
// scrubbed of every secret, the lease stays active and no destroy event is
// emitted.
// Requirement: 27-REQ-7.6
func TestGateway_CloseFailureIsScrubbedAndLeavesTheLeaseActive_TS_27_45(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	id, token := env.issueToken(`{"profile":"tools"}`)
	gw := env.startGateway()
	sb := createVia(t, gw.dial(t, token))

	env.prov.setCloseErr(fmt.Errorf("podman rm failed: run -e GIT_PAT=%s -e SECRET_ENV=envval", plantedSecret))
	err := sb.Close(context.Background())
	if err == nil {
		t.Fatal("Close returned nil although the provider could not destroy the box")
	}
	if strings.Contains(err.Error(), plantedSecret) || strings.Contains(err.Error(), "envval") || !strings.Contains(err.Error(), "[redacted]") {
		t.Errorf("Close error = %q; want the text with both values redacted", err)
	}
	if lease := env.lease(id); lease.State != "active" || lease.EndReason != nil {
		t.Errorf("lease state %q, end_reason %v; want active and not ended", lease.State, lease.EndReason)
	}
	if n := len(env.eventsOf(audit.EventSandboxDestroy)); n != 0 {
		t.Errorf("hub.sandbox.destroy events = %d; want 0", n)
	}
	if _, ok := lookupToken(env, token); !ok {
		t.Error("the token stopped working although the lease is still active")
	}
	env.prov.setCloseErr(nil)
}

// lookupToken reports whether a token still finds a live lease.
func lookupToken(env *backendEnv, token string) (*Lease, bool) {
	l, err := env.store.LookupByToken(token)
	return l, err == nil
}

// TS-27-46: Reap goes straight to the provider, and every other method of the
// box the lease holds reaches the provider's box with the arguments it was given.
// Requirement: 27-REQ-7.7
func TestBackend_ReapAndHandleDelegate_TS_27_46(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	before := env.prov.reapCount()
	if _, err := env.backend.Reap(context.Background()); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if got := env.prov.reapCount() - before; got != 1 {
		t.Errorf("provider Reap calls = %d; want 1", got)
	}

	sb, err := env.create(env.issue(`{"profile":"tools"}`), sandbox.Spec{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	box := env.prov.boxes[0]
	if sb.ID() != box.ID() || !reflect.DeepEqual(sb.Agent(), box.Agent()) {
		t.Errorf("ID and Agent = %q, %+v; want the provider's box's %q, %+v", sb.ID(), sb.Agent(), box.ID(), box.Agent())
	}
	src := sandbox.Tar{R: bytes.NewReader(make([]byte, 1024)), Path: "."}
	if _, err := sb.Sync(context.Background(), src); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if err := sb.Export(context.Background(), ".", sandbox.TarOut{W: &bytes.Buffer{}}); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(box.syncs) != 1 || !reflect.DeepEqual(box.syncs[0], src) {
		t.Errorf("the provider's box was synced with %v; want %v", box.syncs, src)
	}
	if !reflect.DeepEqual(box.exports, []string{"."}) {
		t.Errorf("the provider's box exported %v; want [.]", box.exports)
	}
}

// TS-27-47: The spec the box reports keeps every environment key and none of the
// values; the rest is what was built.
// Requirement: 27-REQ-7.8
func TestHandle_SpecRedactsEnvValues_TS_27_47(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	sb, err := env.create(env.issue(`{"profile":"tools"}`), sandbox.Spec{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	built := env.prov.lastSpec()
	if built.Env["GIT_PAT"] != plantedSecret {
		t.Fatalf("the provider did not get the secret: Env = %v", built.Env)
	}

	got := sb.Spec()
	if len(got.Env) != len(built.Env) {
		t.Errorf("Env = %v; want the keys of %v", got.Env, built.Env)
	}
	for key := range built.Env {
		if value, ok := got.Env[key]; !ok || value != "[redacted]" {
			t.Errorf("Env[%s] = %q, %v; want [redacted]", key, value, ok)
		}
	}
	got.Env, built.Env = nil, nil
	if !reflect.DeepEqual(got, built) {
		t.Errorf("Spec = %+v; want what was built, %+v", got, built)
	}
	// Redacting must not change the box's own spec.
	if env.prov.boxes[0].spec.Env["GIT_PAT"] != plantedSecret {
		t.Error("the provider's spec was redacted in place")
	}
}

// The client destroy path: Close ends an active lease with the reason client and
// emits one event; a second Close is a no-op.
func TestHandleClose_ClientPath(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	id := env.issue(`{"profile":"tools"}`)
	sb, err := env.create(id, sandbox.Spec{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for range 2 {
		if err := sb.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	lease := env.lease(id)
	if lease.State != "destroyed" || lease.EndReason == nil || *lease.EndReason != "client" {
		t.Errorf("lease state %q, end_reason %v; want destroyed by the client", lease.State, lease.EndReason)
	}
	if n := len(env.eventsOf(audit.EventSandboxDestroy)); n != 1 {
		t.Errorf("hub.sandbox.destroy events = %d; want 1: the second Close changes nothing", n)
	}
	if live := env.prov.live(); len(live) != 0 {
		t.Errorf("live boxes = %v; want none", live)
	}
}

// A Close after the lease's time is up closes the box and changes nothing else:
// the gateway also closes a box at its TTL, and marking the lease expired is for
// the expiry sweep, not a client destroy.
func TestHandleClose_AfterExpiryChangesNothing(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	id := env.issue(`{"profile":"tools"}`)
	sb, err := env.create(id, sandbox.Spec{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	expires, _ := apikitParse(t, env.lease(id).ExpiresAt)
	env.clock.Advance(expires.Sub(env.clock.Now()) + time.Second)
	before := *env.lease(id)

	if err := sb.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if live := env.prov.live(); len(live) != 0 {
		t.Errorf("live boxes = %v; want none: the box is closed", live)
	}
	if after := *env.lease(id); !reflect.DeepEqual(before, after) || after.State != "active" {
		t.Errorf("the lease changed: %+v -> %+v", before, after)
	}
	if n := len(env.eventsOf(audit.EventSandboxDestroy)); n != 0 {
		t.Errorf("hub.sandbox.destroy events = %d; want 0", n)
	}
}

// gatewayd closes a box when its TTL ends. The TTL is the time the lease had left
// cut down to whole seconds, so that Close comes up to a second before the
// lease's expires_at, and it is no client's doing either.
func TestHandleClose_AtTheBoxsOwnTTLChangesNothing(t *testing.T) {
	for name, early := range map[string]time.Duration{
		"a second before": time.Second,
		"just before":     50 * time.Millisecond,
		"at the time":     0,
	} {
		t.Run(name, func(t *testing.T) {
			env := newBackendEnv(t, leaseEnvOptions{})
			id := env.issue(`{"profile":"tools"}`)
			sb, err := env.create(id, sandbox.Spec{})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			expires, _ := apikitParse(t, env.lease(id).ExpiresAt)
			env.clock.Advance(expires.Sub(env.clock.Now()) - early)
			before := *env.lease(id)

			if err := sb.Close(context.Background()); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if after := *env.lease(id); !reflect.DeepEqual(before, after) || after.State != "active" {
				t.Errorf("the lease changed: %+v -> %+v", before, after)
			}
			if n := len(env.eventsOf(audit.EventSandboxDestroy)); n != 0 {
				t.Errorf("hub.sandbox.destroy events = %d; want 0", n)
			}
		})
	}

	t.Run("well before is the client's", func(t *testing.T) {
		env := newBackendEnv(t, leaseEnvOptions{})
		id := env.issue(`{"profile":"tools"}`)
		sb, err := env.create(id, sandbox.Spec{})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		expires, _ := apikitParse(t, env.lease(id).ExpiresAt)
		env.clock.Advance(expires.Sub(env.clock.Now()) - 10*time.Second)
		if err := sb.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if lease := env.lease(id); lease.State != "destroyed" || lease.EndReason == nil || *lease.EndReason != "client" {
			t.Errorf("lease state %q, end_reason %v; want destroyed by the client", lease.State, lease.EndReason)
		}
	})
}

// A Close after a DELETE ended the lease closes the box and records nothing a
// second time.
func TestHandleClose_AfterTheLeaseWasEnded(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	id := env.issue(`{"profile":"tools"}`)
	sb, err := env.create(id, sandbox.Spec{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if changed, err := env.store.DestroyLease(id, "active", "api"); err != nil || !changed {
		t.Fatalf("DestroyLease = (%v, %v)", changed, err)
	}
	if err := sb.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if lease := env.lease(id); lease.State != "destroyed" || lease.EndReason == nil || *lease.EndReason != "api" {
		t.Errorf("lease state %q, end_reason %v; want destroyed by the api, as it was", lease.State, lease.EndReason)
	}
	if n := len(env.eventsOf(audit.EventSandboxDestroy)); n != 0 {
		t.Errorf("hub.sandbox.destroy events = %d; want 0", n)
	}
}

// The periodic reap of the gateway has no principal and runs the sweep, whose
// first step is the provider's Reap; the error text the provider gives back is
// scrubbed like any other, and goes to the log, since spec 28 has the sweep log its
// failures and Reap return nothing.
// Requirements: 27-REQ-7.7, 27-REQ-9.3, 28-REQ-5.5
func TestBackendReap_ScrubsTheProviderError(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	env.prov.setReap([]string{"left-over"}, errors.New("podman rm failed at https://user:pw@registry.example/x"))
	before := env.prov.reapCount()
	logs := captureDefaultLog(t)

	ids, err := env.backend.Reap(context.Background())
	if got := env.prov.reapCount() - before; got != 1 {
		t.Errorf("provider Reap calls = %d; want 1", got)
	}
	if len(ids) != 0 || err != nil {
		t.Errorf("Reap = (%v, %v); want nothing: the sweep logs what the provider says", ids, err)
	}
	if out := logs.String(); !strings.Contains(out, "podman rm failed") || strings.Contains(out, "user:pw") {
		t.Errorf("the log does not hold the provider's text with the user information removed:\n%s", out)
	}
}

// A lease holder can call the gateway's Reap RPC with its token, and that reaches
// the backend with the lease as the principal. Reaping the provider is the
// gateway's own work and not a lease holder's: the call does nothing, and no id
// of another lease's sandbox and no text of the provider's reaches the holder.
// Requirement: 27-REQ-9.7
func TestGateway_ReapByALeaseHolderDoesNothing(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig()})
	_, token := env.issueToken(`{"profile":"tools"}`)
	gw := env.startGateway()
	client := gw.dial(t, token)
	env.prov.setReap([]string{"someone-elses-box"}, errors.New("podman rm failed at https://user:pw@registry.example/x"))
	before := env.prov.reapCount()

	ids, err := client.Reap(context.Background())
	if err != nil || len(ids) != 0 {
		t.Errorf("Reap by a lease holder = (%v, %v); want nothing", ids, err)
	}
	if got := env.prov.reapCount() - before; got != 0 {
		t.Errorf("provider Reap calls = %d; want 0: a lease holder does not reap the provider", got)
	}
}
