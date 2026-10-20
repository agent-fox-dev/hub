package sandboxes

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/hub/internal/audit"
)

// expiryBackendEnv is a backend environment with metrics on and a sweeper over
// the same database, provider and clock.
type expiryBackendEnv struct {
	*backendEnv
	metrics *audit.Metrics
	sweeper *Sweeper
}

func newExpiryBackendEnv(t *testing.T) *expiryBackendEnv {
	t.Helper()
	m := audit.NewMetrics()
	env := newBackendEnv(t, leaseEnvOptions{config: gatewayLeaseConfig(), metrics: m})
	sweeper := NewSweeper(SweeperConfig{
		DB: env.db, Provider: env.prov, Boxes: &fakeBoxes{}, Audit: env.sink, Metrics: m,
		Now: env.clock.Now, CallTimeout: time.Second, Service: env.profile,
	})
	return &expiryBackendEnv{backendEnv: env, metrics: m, sweeper: sweeper}
}

// pastLeaseDeadline moves the clock past the deadline for a Create (the default
// lease_timeout is five minutes) but not past expires_at.
func (e *expiryBackendEnv) pastLeaseDeadline() { e.clock.Advance(6 * time.Minute) }

// TS-28-11: A Create that arrives after the lease deadline is refused without
// calling the provider, and expires the lease on the spot.
// Requirement: 28-REQ-3.1
func TestBackendCreate_LateCreateExpiresTheLease_TS_28_11(t *testing.T) {
	t.Run("at the backend", func(t *testing.T) {
		env := newExpiryBackendEnv(t)
		id := env.issue(`{"profile":"tools"}`)
		env.pastLeaseDeadline()

		_, err := env.create(id, sandbox.Spec{})

		wantCode(t, err, codes.FailedPrecondition)
		if !strings.Contains(err.Error(), "expired") {
			t.Errorf("error %q does not name expired", err)
		}
		if n := env.prov.creates(); n != 0 {
			t.Errorf("provider Create calls = %d; want 0", n)
		}
		if s := env.readState(id); s.state != "expired" || s.endReason != "expired" || s.hash.Valid {
			t.Errorf("the lease reads %+v; want expired/expired with no token hash", s)
		}
		events := eventsOf(env.emitter, audit.EventSandboxExpire)
		if len(events) != 1 || events[0].Metadata["state"] != "expired" || events[0].ResourceID != id {
			t.Errorf("expire events = %v; want exactly one with state expired", events)
		}
		// A second Create finds the lease ended.
		_, err = env.create(id, sandbox.Spec{})
		wantCode(t, err, codes.FailedPrecondition)
		if got := len(eventsOf(env.emitter, audit.EventSandboxExpire)); got != 1 {
			t.Errorf("%d expire events after a second Create; want 1", got)
		}
	})

	t.Run("a lease from before the column lapses at expires_at", func(t *testing.T) {
		env := newExpiryBackendEnv(t)
		id := env.issue(`{"profile":"tools"}`)
		if _, err := env.db.Exec(`UPDATE sandbox_leases SET lease_expires_at = NULL WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
		env.pastLeaseDeadline() // past what the column held, not past expires_at
		if _, err := env.create(id, sandbox.Spec{}); err != nil {
			t.Fatalf("Create on a lease with no deadline of its own, before its expires_at: %v", err)
		}

		env2 := newExpiryBackendEnv(t)
		id2 := env2.issue(`{"profile":"tools"}`)
		if _, err := env2.db.Exec(`UPDATE sandbox_leases SET lease_expires_at = NULL WHERE id = ?`, id2); err != nil {
			t.Fatal(err)
		}
		env2.clock.Advance(2 * time.Hour)
		_, err := env2.create(id2, sandbox.Spec{})
		wantCode(t, err, codes.FailedPrecondition)
		if s := env2.readState(id2); s.state != "expired" {
			t.Errorf("the lease reads %s; want expired", s.state)
		}
	})

	t.Run("over gRPC", func(t *testing.T) {
		env := newExpiryBackendEnv(t)
		id, token := env.issueToken(`{"profile":"tools"}`)
		client := env.startGateway().dial(t, token)
		env.pastLeaseDeadline()

		// gatewayd answers a backend error that is no refusal as Unavailable with the
		// backend's text (survey D-6 of spec 27), so the client gets the text and
		// FailedPrecondition is asserted at the backend above.
		_, err := client.Create(context.Background(), sandbox.Spec{Image: "ignored"})
		if err == nil || !strings.Contains(err.Error(), "expired") || errors.Is(err, gateway.ErrUnauthenticated) {
			t.Errorf("Create = %v; want the refusal of an expired lease, not of the token", err)
		}
		if n := env.prov.creates(); n != 0 {
			t.Errorf("provider Create calls = %d; want 0", n)
		}
		if s := env.readState(id); s.state != "expired" || s.endReason != "expired" {
			t.Errorf("the lease reads %+v; want expired/expired right after the Create", s)
		}
	})
}

// TS-28-12: The authenticator rejects the token of any lease whose expires_at has
// passed, in any state, even one whose token hash is still stored.
// Requirement: 28-REQ-3.2
func TestAuthenticator_RejectsPastExpiresAt_TS_28_12(t *testing.T) {
	env := newExpiryBackendEnv(t)
	gw := env.startGateway()
	// Each lease expired an hour after it was created an hour ago: expires_at is now.
	created := env.clock.Now().Add(-time.Hour)
	for n, state := range []string{"leased", "active", "expired", "failed"} {
		env.insertLeaseAt(n+1, "widgets", state, created)
		token := testToken(n + 1)
		if err := rawCall(t, gw, token, rpcGatewayCreate); status.Code(err) != codes.Unauthenticated {
			t.Errorf("a %s lease at its expires_at: Create = %v; want Unauthenticated", state, err)
		}
		if _, err := gateway.Dial(context.Background(), gw.URL(), gateway.Options{Token: token}); !errors.Is(err, gateway.ErrUnauthenticated) {
			t.Errorf("a %s lease at its expires_at: Dial = %v; want ErrUnauthenticated", state, err)
		}
	}
	// One second earlier the same lease works.
	env.clock.Advance(-time.Second)
	if _, err := gateway.Dial(context.Background(), gw.URL(), gateway.Options{Token: testToken(1)}); err != nil {
		t.Errorf("a leased lease one second before its expires_at: Dial = %v; want it to work", err)
	}
}

// TS-28-13: After the expiry path ended a lease its token fails with
// Unauthenticated, because the ending cleared the hash.
// Requirement: 28-REQ-3.3
func TestAuthenticator_RejectsAfterExpiryPath_TS_28_13(t *testing.T) {
	env := newExpiryBackendEnv(t)
	id, token := env.issueToken(`{"profile":"tools"}`)
	gw := env.startGateway()
	env.pastLeaseDeadline()
	if err := env.sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	for _, method := range []string{rpcGatewayCreate, rpcAgentHello} {
		if err := rawCall(t, gw, token, method); status.Code(err) != codes.Unauthenticated {
			t.Errorf("%s with the token of an expired lease = %v; want Unauthenticated", method, err)
		}
	}
	if _, err := gateway.Dial(context.Background(), gw.URL(), gateway.Options{Token: token}); !errors.Is(err, gateway.ErrUnauthenticated) {
		t.Errorf("Dial = %v; want ErrUnauthenticated", err)
	}
	if s := env.readState(id); s.state != "expired" || s.hash.Valid {
		t.Errorf("the lease reads %+v; want expired with token_hash NULL", s)
	}
}

// TS-28-14: A lease that leaves leased while its box is being created has the box
// destroyed, keeps the state and end reason it got, and counts as a failed create.
// Requirement: 28-REQ-3.4, 28-REQ-9.4
func TestBackendCreate_LeaseEndsDuringCreate_TS_28_14(t *testing.T) {
	for _, tc := range []struct {
		name       string
		end        func(env *expiryBackendEnv, id string)
		state, why string
	}{
		{"deleted", func(env *expiryBackendEnv, id string) {
			if rec := env.do(http.MethodDelete, "/api/v1/workspaces/widgets/sandboxes/"+id, "", apiKeyAuth()); rec.Code != http.StatusNoContent {
				env.t.Fatalf("DELETE answered %d: %s", rec.Code, rec.Body)
			}
		}, "destroyed", "api"},
		{"expired by a sweep", func(env *expiryBackendEnv, id string) {
			env.pastLeaseDeadline()
			if err := env.sweeper.Sweep(context.Background()); err != nil {
				env.t.Fatalf("Sweep: %v", err)
			}
		}, "expired", "expired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newExpiryBackendEnv(t)
			id := env.issue(`{"profile":"tools"}`)
			release := make(chan struct{})
			env.prov.mu.Lock()
			env.prov.block = release
			env.prov.mu.Unlock()
			failedBefore := createsCounter(t, env.metrics, "failed")

			var wg sync.WaitGroup
			var createErr error
			wg.Go(func() { _, createErr = env.create(id, sandbox.Spec{}) })
			select {
			case <-env.prov.entered:
			case <-time.After(10 * time.Second):
				t.Fatal("the provider's Create was never entered")
			}
			tc.end(env, id)
			close(release)
			wg.Wait()

			wantCode(t, createErr, codes.FailedPrecondition)
			if !strings.Contains(createErr.Error(), tc.state) {
				t.Errorf("error %q does not name the state %s", createErr, tc.state)
			}
			if live := env.prov.live(); len(live) != 0 {
				t.Errorf("boxes still live: %v; want the new box destroyed", live)
			}
			boxes := env.prov.boxes
			if len(boxes) != 1 || boxes[0].closes != 1 {
				t.Errorf("the box was closed %d times over %d boxes; want once", closeCount(boxes), len(boxes))
			}
			if s := env.readState(id); s.state != tc.state || s.endReason != tc.why {
				t.Errorf("the lease reads %s/%s; want %s/%s kept", s.state, s.endReason, tc.state, tc.why)
			}
			if got := createsCounter(t, env.metrics, "failed") - failedBefore; got != 1 {
				t.Errorf("creates_total{failed} went up by %v; want 1", got)
			}
			if got := createsCounter(t, env.metrics, "created"); got != 0 {
				t.Errorf("creates_total{created} = %v; want 0", got)
			}
		})
	}
}

func closeCount(boxes []*recordedBox) int {
	n := 0
	for _, b := range boxes {
		n += b.closes
	}
	return n
}

// TS-28-15: A late Create and any number of sweeps racing on the same lapsed
// lease end it exactly once.
// Requirement: 28-REQ-3.5
func TestLateCreateAndSweepsRace_TS_28_15(t *testing.T) {
	env := newExpiryBackendEnv(t)
	r := rand.New(rand.NewPCG(28, 15))
	for round := range 40 {
		id := env.insertLeaseAt(round+1, "widgets", "leased", env.clock.Now())
		if _, err := env.db.Exec(`UPDATE sandbox_leases SET lease_expires_at = ? WHERE id = ?`,
			"2030-01-02T03:04:05Z", id); err != nil { // the clock's first reading: already due
			t.Fatal(err)
		}
		env.clock.Advance(0)

		actions := []func(){
			func() { _, _ = env.create(id, sandbox.Spec{}) },
			func() { _ = env.sweeper.Sweep(context.Background()) },
			func() { _ = env.sweeper.Sweep(context.Background()) },
		}
		r.Shuffle(len(actions), func(i, j int) { actions[i], actions[j] = actions[j], actions[i] })
		var wg sync.WaitGroup
		for _, action := range actions {
			delay := time.Duration(r.IntN(300)) * time.Microsecond
			wg.Go(func() {
				time.Sleep(delay)
				action()
			})
		}
		wg.Wait()

		n := 0
		for _, e := range eventsOf(env.emitter, audit.EventSandboxExpire) {
			if e.ResourceID == id {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("round %d: %d expire events for lease %s; want exactly 1", round, n, id)
		}
		if s := env.readState(id); s.state != "expired" || s.endReason != "expired" {
			t.Fatalf("round %d: the lease reads %s/%s; want expired/expired", round, s.state, s.endReason)
		}
	}
	if n := env.prov.creates(); n != 0 {
		t.Errorf("provider Create calls = %d; want 0: every lease was past its deadline", n)
	}
}

// countingSweeper counts the sweeps it is asked for.
type countingSweeper struct{ n atomic.Int32 }

func (c *countingSweeper) Sweep(context.Context) error {
	c.n.Add(1)
	return nil
}

// TS-28-28: Backend.Reap runs the sweep only when the context carries no lease
// principal.
// Requirement: 28-REQ-5.5
func TestBackendReap_RunsTheSweepWithoutAPrincipal_TS_28_28(t *testing.T) {
	env := newBackendEnv(t, leaseEnvOptions{})
	sweeper := &countingSweeper{}
	b := newBackend(backendConfig{Service: env.profile, Store: env.store, Now: env.clock.Now, Principal: testPrincipal, Sweeper: sweeper})
	before := env.prov.reapCount()

	if ids, err := b.Reap(context.Background()); err != nil || len(ids) != 0 {
		t.Errorf("Reap = (%v, %v); want nil", ids, err)
	}
	if got := sweeper.n.Load(); got != 1 {
		t.Errorf("sweeps = %d after a Reap without a principal; want 1", got)
	}

	if ids, err := b.Reap(withPrincipal(context.Background(), leaseID(1))); err != nil || len(ids) != 0 {
		t.Errorf("Reap with a principal = (%v, %v); want nil", ids, err)
	}
	if got := sweeper.n.Load(); got != 1 {
		t.Errorf("sweeps = %d after a Reap with a principal; want still 1", got)
	}
	if got := env.prov.reapCount() - before; got != 0 {
		t.Errorf("provider Reap calls = %d; the backend itself does not reap, the sweep does", got)
	}
}

// TS-28-48: A Create the gateway accepts for a leased lease counts once, as
// created when the lease becomes active and as failed otherwise.
// Requirement: 28-REQ-9.4
func TestCreatesTotal_OneResultPerAcceptedCreate_TS_28_48(t *testing.T) {
	counters := func(env *expiryBackendEnv) map[string]float64 {
		return map[string]float64{"created": createsCounter(t, env.metrics, "created"), "failed": createsCounter(t, env.metrics, "failed")}
	}
	for _, tc := range []struct {
		name string
		run  func(env *expiryBackendEnv)
		want string
	}{
		{"created", func(env *expiryBackendEnv) {
			if _, err := env.create(env.issue(`{"profile":"tools"}`), sandbox.Spec{}); err != nil {
				t.Fatalf("Create: %v", err)
			}
		}, "created"},
		{"provider error", func(env *expiryBackendEnv) {
			env.prov.mu.Lock()
			env.prov.createErr = errors.New("the engine refused")
			env.prov.mu.Unlock()
			_, _ = env.create(env.issue(`{"profile":"tools"}`), sandbox.Spec{})
		}, "failed"},
		{"secret missing", func(env *expiryBackendEnv) {
			id := env.issue(`{"profile":"tools"}`)
			if err := env.secrets.DeleteSecret("workspace", "widgets", "GIT_PAT"); err != nil {
				t.Fatalf("deleting the secret: %v", err)
			}
			_, _ = env.create(id, sandbox.Spec{})
		}, "failed"},
		{"profile no longer configured", func(env *expiryBackendEnv) {
			id := env.issue(`{"profile":"tools"}`)
			if _, err := env.db.Exec(`UPDATE sandbox_leases SET profile = 'gone' WHERE id = ?`, id); err != nil {
				t.Fatal(err)
			}
			_, _ = env.create(id, sandbox.Spec{})
		}, "failed"},
		{"lease left leased", func(env *expiryBackendEnv) {
			id := env.issue(`{"profile":"tools"}`)
			release := make(chan struct{})
			env.prov.mu.Lock()
			env.prov.block = release
			env.prov.mu.Unlock()
			var wg sync.WaitGroup
			wg.Go(func() { _, _ = env.create(id, sandbox.Spec{}) })
			<-env.prov.entered
			env.do(http.MethodDelete, "/api/v1/workspaces/widgets/sandboxes/"+id, "", apiKeyAuth())
			close(release)
			wg.Wait()
		}, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newExpiryBackendEnv(t)
			before := counters(env)
			tc.run(env)
			after := counters(env)
			for result := range before {
				want := 0.0
				if result == tc.want {
					want = 1
				}
				if got := after[result] - before[result]; got != want {
					t.Errorf("creates_total{%s} went up by %v; want %v", result, got, want)
				}
			}
		})
	}
}

// The backend's refusals before it accepts a Create are not counted: a lease
// that does not exist, one that is not leased, a second Create in flight and a
// missed deadline.
// Requirement: 28-REQ-9.5
func TestCreatesTotal_RefusalsAreNotCounted(t *testing.T) {
	env := newExpiryBackendEnv(t)
	active := env.issue(`{"profile":"tools"}`)
	if _, err := env.create(active, sandbox.Spec{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	late := env.issue(`{"profile":"tools"}`)
	env.pastLeaseDeadline()
	before := []float64{createsCounter(t, env.metrics, "created"), createsCounter(t, env.metrics, "failed")}

	for name, id := range map[string]string{"unknown lease": leaseID(77), "second Create": active, "late Create": late} {
		if _, err := env.create(id, sandbox.Spec{}); err == nil {
			t.Errorf("%s: Create returned no error", name)
		}
	}
	if _, err := env.backend.Create(context.Background(), sandbox.Spec{}); err == nil {
		t.Error("Create without a principal returned no error")
	}

	after := []float64{createsCounter(t, env.metrics, "created"), createsCounter(t, env.metrics, "failed")}
	if !slices.Equal(before, after) {
		t.Errorf("creates_total went from %v to %v; want it unchanged by refusals", before, after)
	}
}

// The gateway lists the boxes hub holds and destroys one by id the way DELETE
// does: the lease is left for the caller to end, a box that is gone counts as
// destroyed, and a box that cannot be destroyed stays listed to be tried again.
// Requirement: 28-REQ-4.6
func TestGateway_ListsAndDestroysTheBoxesItHolds(t *testing.T) {
	t.Run("a box the gateway holds", func(t *testing.T) {
		env := newExpiryBackendEnv(t)
		id, token := env.issueToken(`{"profile":"tools"}`)
		gw := env.startGateway()
		box := createVia(t, gw.dial(t, token))

		boxes, err := gw.List(context.Background())
		if err != nil || len(boxes) != 1 || boxes[0].ID != box.ID() || boxes[0].Labels["hub.lease"] != id {
			t.Fatalf("List = (%v, %v); want the box %s labelled hub.lease=%s", boxes, err, box.ID(), id)
		}
		if err := gw.Destroy(context.Background(), box.ID()); err != nil {
			t.Fatalf("Destroy: %v", err)
		}
		if live := env.prov.live(); len(live) != 0 {
			t.Errorf("live boxes %v; want the box destroyed", live)
		}
		if boxes, _ := gw.List(context.Background()); len(boxes) != 0 {
			t.Errorf("List after Destroy = %v; want none", boxes)
		}
		// The caller records the end: the Close of the box did not mark it as the client's.
		if s := env.readState(id); s.state != "active" || s.endReason != "" {
			t.Errorf("the lease reads %+v after Destroy; want it left active for the caller to end", s)
		}
		if err := gw.Destroy(context.Background(), box.ID()); err != nil {
			t.Errorf("Destroy of a box that is gone = %v; want nil", err)
		}
		if err := gw.Destroy(context.Background(), "never-existed"); err != nil {
			t.Errorf("Destroy of an unknown box = %v; want nil", err)
		}
	})

	t.Run("a box that cannot be destroyed is kept to be tried again", func(t *testing.T) {
		env := newExpiryBackendEnv(t)
		id, token := env.issueToken(`{"profile":"tools"}`)
		gw := env.startGateway()
		box := createVia(t, gw.dial(t, token))

		env.prov.setCloseErr(errors.New("podman rm failed at https://user:pw@registry.example/x"))
		err := gw.Destroy(context.Background(), box.ID())
		if err == nil || strings.Contains(err.Error(), "user:pw") {
			t.Errorf("Destroy with a failing provider = %v; want an error without the userinfo", err)
		}
		if boxes, _ := gw.List(context.Background()); len(boxes) != 1 {
			t.Errorf("List after a failed Destroy = %v; want the box still held", boxes)
		}

		// The gateway has forgotten the box; hub's handle closes it.
		env.prov.setCloseErr(nil)
		if err := gw.Destroy(context.Background(), box.ID()); err != nil {
			t.Fatalf("Destroy the second time: %v", err)
		}
		if live := env.prov.live(); len(live) != 0 {
			t.Errorf("live boxes %v; want the box destroyed", live)
		}
		if boxes, _ := gw.List(context.Background()); len(boxes) != 0 {
			t.Errorf("List after the retry = %v; want none", boxes)
		}
		if s := env.readState(id); s.state != "active" {
			t.Errorf("the lease reads %s; want it left active for the caller to end", s.state)
		}
	})
}
