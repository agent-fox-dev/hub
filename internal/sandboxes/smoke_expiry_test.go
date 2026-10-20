package sandboxes

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/hub/internal/audit"
)

// getLease reads a lease of widgets through the REST API.
func (e *expiryBackendEnv) getLease(id string) map[string]any {
	e.t.Helper()
	rec := e.get("/api/v1/workspaces/widgets/sandboxes/"+id, apiKeyAuth())
	if rec.Code != http.StatusOK {
		e.t.Fatalf("GET lease answered %d: %s", rec.Code, rec.Body)
	}
	return jsonObject(e.t, rec)
}

// tick fires the reaper's ticker and waits until the sweep it starts has ended: the
// sweep runs under the lock Sweep takes, so a second sweep returns after the first.
func (e *expiryBackendEnv) tick(tickers *fakeTickers, g *Gateway) {
	e.t.Helper()
	before := e.prov.reapCount()
	tickers.fire()
	waitFor(e.t, "the sweep of the tick", func() bool { return e.prov.reapCount() > before })
	if err := g.sweeper.Sweep(context.Background()); err != nil {
		e.t.Fatalf("Sweep: %v", err)
	}
}

// TS-28-53 (smoke): An unused lease lapses at its deadline, the reaper expires it,
// and its token stops working.
// Path: 28-PATH-1. Requirements: 28-REQ-2.1, 28-REQ-4.3, 28-REQ-3.3
func TestSmoke_UnusedLeaseLapses_TS_28_53(t *testing.T) {
	env := newExpiryBackendEnv(t)
	tickers := &fakeTickers{}
	g := env.runGateway(tickers)

	rec := env.post("widgets", `{"profile":"tools"}`, apiKeyAuth())
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST answered %d: %s", rec.Code, rec.Body)
	}
	issued := jsonObject(t, rec)
	id, token := issued["id"].(string), issued["token"].(string)
	if deadline, _ := issued["lease_expires_at"].(string); deadline == "" || deadline == issued["expires_at"] {
		t.Fatalf("lease_expires_at = %q, expires_at = %v; want a deadline of five minutes, before expires_at", deadline, issued["expires_at"])
	}
	client := gateway2(t, g.URL(), token) // the token works while the lease is leased

	env.pastLeaseDeadline()
	env.tick(tickers, g)

	got := env.getLease(id)
	if got["state"] != "expired" || got["end_reason"] != "expired" {
		t.Errorf("GET reads %v/%v; want expired/expired", got["state"], got["end_reason"])
	}
	events := eventsOf(env.emitter, audit.EventSandboxExpire)
	if len(events) != 1 || events[0].ResourceID != id || events[0].Metadata["state"] != "expired" {
		t.Errorf("expire events = %v; want exactly one with state expired", events)
	}
	if err := rawCall(t, &testGateway{g}, token, rpcGatewayCreate); status.Code(err) != codes.Unauthenticated {
		t.Errorf("the token of the expired lease: Create = %v; want Unauthenticated", err)
	}
	if _, err := client.Create(context.Background(), sandbox.Spec{Image: "ignored"}); err == nil {
		t.Error("the client of an expired lease could still create a box")
	}
	if n := env.prov.creates(); n != 0 {
		t.Errorf("provider Create calls = %d; want 0", n)
	}
}

// TS-28-54 (smoke): A box past expires_at is destroyed by the sweep and the metrics
// follow.
// Path: 28-PATH-2. Requirements: 28-REQ-9.2, 28-REQ-9.6
func TestSmoke_BoxPastExpiresAtIsDestroyed_TS_28_54(t *testing.T) {
	env := newExpiryBackendEnv(t)
	tickers := &fakeTickers{}
	g := env.runGateway(tickers)
	id, token := env.issueToken(`{"profile":"tools"}`)
	box := createVia(t, gateway2(t, g.URL(), token))

	if env.getLease(id)["state"] != "active" || activeGauge(t, env.metrics, "widgets") != 1 || createsCounter(t, env.metrics, "created") != 1 {
		t.Fatalf("after Create: lease %v, gauge %v, created %v; want active, 1 and 1",
			env.getLease(id)["state"], activeGauge(t, env.metrics, "widgets"), createsCounter(t, env.metrics, "created"))
	}
	if live := env.prov.live(); len(live) != 1 || live[0] != box.ID() {
		t.Fatalf("live boxes %v; want %s", live, box.ID())
	}

	env.clock.Advance(90 * time.Minute) // past the hour of expires_at
	env.tick(tickers, g)

	if live := env.prov.live(); len(live) != 0 {
		t.Errorf("live boxes %v; want the box destroyed", live)
	}
	got := env.getLease(id)
	if got["state"] != "destroyed" || got["end_reason"] != "expired" || got["sandbox_id"] != box.ID() {
		t.Errorf("GET reads %v/%v with box %v; want destroyed/expired with box %s", got["state"], got["end_reason"], got["sandbox_id"], box.ID())
	}
	events := eventsOf(env.emitter, audit.EventSandboxExpire)
	if len(events) != 1 || events[0].Metadata["state"] != "destroyed" || events[0].Metadata["sandbox_id"] != box.ID() {
		t.Errorf("expire events = %v; want exactly one with state destroyed and the box", events)
	}
	if got := eventsOf(env.emitter, audit.EventSandboxDestroy); len(got) != 0 {
		t.Errorf("%d hub.sandbox.destroy events; want none", len(got))
	}
	if got := activeGauge(t, env.metrics, "widgets"); got != 0 {
		t.Errorf("afhub_sandboxes_active = %v; want 0", got)
	}
	if got := createsCounter(t, env.metrics, "created"); got != 1 {
		t.Errorf("afhub_sandbox_creates_total{created} = %v; want 1", got)
	}
	if count, sum := leaseSeconds(t, env.metrics); count != 1 || sum != 5400 {
		t.Errorf("afhub_sandbox_lease_seconds = (%d, %v); want one observation of 5400 seconds", count, sum)
	}
}

// TS-28-55 (smoke): A late Create through the gateway expires the lease on the spot,
// without a sweep.
// Path: 28-PATH-3. Requirement: 28-REQ-3.1
func TestSmoke_LateCreateExpiresTheLease_TS_28_55(t *testing.T) {
	env := newExpiryBackendEnv(t)
	gw := env.startGateway()
	id, token := env.issueToken(`{"profile":"tools"}`)
	client := gw.dial(t, token)
	sweeps := env.prov.reapCount()
	env.pastLeaseDeadline()

	_, err := client.Create(context.Background(), sandbox.Spec{Image: "ignored"})

	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("Create = %v; want the refusal of an expired lease", err)
	}
	if n := env.prov.creates(); n != 0 {
		t.Errorf("provider Create calls = %d; want 0", n)
	}
	if got := env.getLease(id); got["state"] != "expired" || got["end_reason"] != "expired" {
		t.Errorf("GET reads %v/%v; want expired/expired", got["state"], got["end_reason"])
	}
	if env.prov.reapCount() != sweeps {
		t.Error("a sweep ran; the Create expired the lease on its own")
	}
}

// TS-28-58 (smoke): A deleted lease is visible for 24 hours and then pruned by a
// sweep.
// Path: 28-PATH-6. Requirements: 28-REQ-6.1, 28-REQ-6.2
func TestSmoke_DeletedLeaseIsRetainedThenPruned_TS_28_58(t *testing.T) {
	env := newExpiryBackendEnv(t)
	id := env.issue(`{"profile":"tools"}`)
	if rec := env.do(http.MethodDelete, "/api/v1/workspaces/widgets/sandboxes/"+id, "", apiKeyAuth()); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE answered %d: %s", rec.Code, rec.Body)
	}
	listed := func(path string) bool {
		for _, l := range leaseList(t, env.get(path, apiKeyAuth())) {
			if l["id"] == id {
				return true
			}
		}
		return false
	}

	env.clock.Advance(23 * time.Hour)
	if err := env.sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if got := env.getLease(id); got["state"] != "destroyed" {
		t.Errorf("after 23 hours GET reads %v; want destroyed", got["state"])
	}
	if !listed("/api/v1/workspaces/widgets/sandboxes?all=true") {
		t.Error("after 23 hours ?all=true does not list the lease")
	}

	env.clock.Advance(time.Hour + time.Second) // past 24 hours after ended_at
	if err := env.sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if rec := env.get("/api/v1/workspaces/widgets/sandboxes/"+id, apiKeyAuth()); rec.Code != http.StatusNotFound {
		t.Errorf("after the sweep GET answered %d; want 404", rec.Code)
	}
	if listed("/api/v1/workspaces/widgets/sandboxes?all=true") {
		t.Error("after the sweep ?all=true still lists the lease")
	}
}
