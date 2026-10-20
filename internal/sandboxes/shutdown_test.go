package sandboxes

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/hub/internal/audit"
)

// runGateway builds the gateway over the environment, with a reaper on tickers the
// test fires, runs its start-up work and lets it serve. It hands the lease service
// the gateway as startGateway does, and shuts the gateway down when the test ends.
func (e *expiryBackendEnv) runGateway(tickers *fakeTickers) *Gateway {
	e.t.Helper()
	g, err := newGateway(e.profile, GatewayDeps{DB: e.db, Secrets: e.secrets, Audit: e.sink, Now: e.clock.Now, Metrics: e.metrics}, gatewaySeams{})
	if err != nil {
		e.t.Fatalf("newGateway: %v", err)
	}
	g.sweeper.tickers = tickers.make
	stopAfter(e.t, g)
	if err := g.Run(context.Background()); err != nil {
		e.t.Fatalf("Run: %v", err)
	}
	e.svc.gatewayURL = g.URL()
	e.svc.boxes = g
	return g
}

// TS-28-33: The sandbox shutdown runs after the server's Start returns, and is
// limited to twenty seconds. (The order against the databases' Close is that of
// main's deferred calls, which run after the body that calls the shutdown; the
// real binary is started and stopped by TS-28-56.)
// Requirement: 28-REQ-7.1
func TestShutdownLimit_TS_28_33(t *testing.T) {
	if ShutdownLimit != 20*time.Second {
		t.Errorf("ShutdownLimit = %v; want 20s", ShutdownLimit)
	}
}

// TS-28-34: In shutdown mode a Create is refused with Unavailable, counted
// nowhere, and every lease that ends does so with the reason shutdown.
// Requirement: 28-REQ-7.2
func TestShutdownMode_TS_28_34(t *testing.T) {
	env := newExpiryBackendEnv(t)
	leased := env.issue(`{"profile":"tools"}`)
	byDelete := env.issue(`{"profile":"tools"}`)
	byClose := env.issue(`{"profile":"tools"}`)
	if _, err := env.create(byDelete, sandbox.Spec{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	box, err := env.create(byClose, sandbox.Spec{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	creates, created, failed := env.prov.creates(), createsCounter(t, env.metrics, "created"), createsCounter(t, env.metrics, "failed")

	if env.profile.InShutdown() {
		t.Fatal("the service is in shutdown mode before it was entered")
	}
	env.profile.EnterShutdown()
	if !env.profile.InShutdown() {
		t.Fatal("the service is not in shutdown mode after it was entered")
	}

	_, err = env.create(leased, sandbox.Spec{})
	wantCode(t, err, codes.Unavailable)
	if got := env.prov.creates(); got != creates {
		t.Errorf("provider Create calls went from %d to %d; want none in shutdown mode", creates, got)
	}
	if createsCounter(t, env.metrics, "created") != created || createsCounter(t, env.metrics, "failed") != failed {
		t.Error("creates_total changed for a Create that shutdown mode refused")
	}
	if s := env.readState(leased); s.state != "leased" {
		t.Errorf("the refused lease reads %s; want it left leased", s.state)
	}

	// DELETE and the client's Close end their leases with the reason shutdown.
	if rec := env.do(http.MethodDelete, "/api/v1/workspaces/widgets/sandboxes/"+byDelete, "", apiKeyAuth()); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE answered %d: %s", rec.Code, rec.Body)
	}
	if err := box.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, id := range []string{byDelete, byClose} {
		if s := env.readState(id); s.state != "destroyed" || s.endReason != "shutdown" {
			t.Errorf("lease %s reads %s/%s; want destroyed/shutdown", id, s.state, s.endReason)
		}
	}
	for _, e := range eventsOf(env.emitter, audit.EventSandboxDestroy) {
		if e.Metadata["reason"] != "shutdown" {
			t.Errorf("event %+v; want the reason shutdown", e)
		}
	}
	// The sweep does not start either: a lease past its deadline stays as it is.
	env.pastLeaseDeadline()
	if err := env.sweeper.Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if s := env.readState(leased); s.state != "leased" {
		t.Errorf("a sweep in shutdown mode ended the lease: it reads %s", s.state)
	}
}

// TS-28-35: Shutdown stops the reaper, destroys every active box, ends each lease
// as destroyed with end_reason shutdown, and stops the gateway after that.
// Requirement: 28-REQ-7.3
func TestShutdown_TS_28_35(t *testing.T) {
	env := newExpiryBackendEnv(t)
	tickers := &fakeTickers{}
	base := env.prov.reapCount()
	g := env.runGateway(tickers)
	url := g.URL()

	var ids []string
	for range 3 {
		id, token := env.issueToken(`{"profile":"tools"}`)
		createVia(t, gateway2(t, url, token))
		ids = append(ids, id)
	}
	leased := env.issue(`{"profile":"tools"}`)
	// Run's first sweep, and the one gatewayd asks for when it begins to serve.
	waitFor(t, "the two first sweeps", func() bool { return env.prov.reapCount()-base >= 2 })
	swept := env.prov.reapCount()

	if err := g.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	for _, id := range ids {
		if s := env.readState(id); s.state != "destroyed" || s.endReason != "shutdown" || s.hash.Valid {
			t.Errorf("lease %s reads %+v; want destroyed/shutdown with no token hash", id, s)
		}
	}
	if s := env.readState(leased); s.state != "leased" || !s.hash.Valid {
		t.Errorf("the leased lease reads %+v; want it untouched", s)
	}
	if live := env.prov.live(); len(live) != 0 {
		t.Errorf("live boxes %v; want every box destroyed", live)
	}
	events := eventsOf(env.emitter, audit.EventSandboxDestroy)
	if len(events) != 3 {
		t.Fatalf("%d hub.sandbox.destroy events; want one per lease: %v", len(events), events)
	}
	for _, e := range events {
		// The gateway's own stop would have closed the boxes through the handles, with
		// the lease as the actor; the system ended them first.
		if e.Metadata["reason"] != "shutdown" || e.ActorType != "system" || e.ActorID != "" {
			t.Errorf("event %+v; want reason shutdown by the system with no actor id", e)
		}
	}
	if got := len(eventsOf(env.emitter, audit.EventSandboxExpire)); got != 0 {
		t.Errorf("%d expire events; want none", got)
	}
	if !env.profile.InShutdown() {
		t.Error("the service is not in shutdown mode after the shutdown")
	}

	// The reaper is stopped and no sweep starts, whatever fires.
	tickers.fire()
	time.Sleep(50 * time.Millisecond)
	if got := env.prov.reapCount(); got != swept {
		t.Errorf("provider Reap calls went from %d to %d after the shutdown; want no sweep to start", swept, got)
	}
	if _, err := gateway.Dial(context.Background(), url, gateway.Options{Token: "sbl_" + strings.Repeat("a", 44)}); err == nil {
		t.Error("the gateway still answers after the shutdown")
	}
	if got := createsCounter(t, env.metrics, "created"); got != 3 {
		t.Errorf("creates_total{created} = %v; want the 3 creates before the shutdown", got)
	}
}

// gateway2 dials the real client at url with a token.
func gateway2(t *testing.T, url, token string) *gateway.Provider {
	t.Helper()
	p, err := gateway.Dial(context.Background(), url, gateway.Options{Token: token})
	if err != nil {
		t.Fatalf("gateway.Dial: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// TS-28-36: Shutdown leaves every leased lease byte-identical, whatever else is in
// the table.
// Requirement: 28-REQ-7.4
func TestShutdown_LeavesLeasedRowsAlone_TS_28_36(t *testing.T) {
	for seed := range uint64(15) {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			r := rand.New(rand.NewPCG(28, 36+seed))
			env := newSweepEnv(t)
			now := env.clock.Now()
			var leased []string
			for n := 1; n <= 10; n++ {
				f := seedFields{workspace: []string{"widgets", "gadgets", "tools"}[r.IntN(3)], created: now.Add(-time.Duration(r.IntN(5000)) * time.Minute)}
				if r.IntN(2) == 0 {
					f.leaseExpires = timePtr(now.Add(time.Duration(r.IntN(600)-300) * time.Minute))
				}
				if r.IntN(3) == 0 {
					f.boxID = fmt.Sprintf("box-%d", n)
					env.seed(n, "active", f)
					continue
				}
				leased = append(leased, env.seed(n, "leased", f))
			}
			before := map[string]string{}
			for _, id := range leased {
				before[id] = strings.Join(env.rowText(id), "|")
			}

			if err := env.sweeper.Shutdown(context.Background()); err != nil {
				t.Fatalf("Shutdown: %v", err)
			}

			for _, id := range leased {
				if got := strings.Join(env.rowText(id), "|"); got != before[id] {
					t.Errorf("leased lease %s changed:\n%s\n->\n%s", id, before[id], got)
				}
			}
		})
	}
}

// TS-28-37: When a Destroy hangs, Shutdown returns when the limit runs out and
// leaves the lease whose ending did not complete active, for recovery at the next
// start; the others are ended.
// Requirement: 28-REQ-7.5
func TestShutdown_HungDestroy_TS_28_37(t *testing.T) {
	for _, tc := range []struct {
		name string
		hang func(env *sweepEnv, boxID string) (release func())
	}{
		{"a Destroy that honours its context", func(env *sweepEnv, boxID string) func() {
			env.boxes.destroyHang = map[string]bool{boxID: true}
			return func() {}
		}},
		{"a Destroy that ignores its context", func(env *sweepEnv, boxID string) func() {
			block := make(chan struct{})
			env.boxes.destroyBlock = map[string]chan struct{}{boxID: block}
			return func() { close(block) }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newSweepEnv(t)
			now := env.clock.Now()
			hung := env.seed(1, "active", seedFields{boxID: "box-hung", expires: now.Add(time.Hour)})
			ok := env.seed(2, "active", seedFields{boxID: "box-ok", expires: now.Add(time.Hour)})
			release := tc.hang(env, "box-hung")
			defer release()

			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			start := time.Now()
			err := env.sweeper.Shutdown(ctx)

			if elapsed := time.Since(start); elapsed > time.Second {
				t.Errorf("Shutdown took %v with a limit of 100ms", elapsed)
			}
			if err == nil {
				t.Error("Shutdown returned nil although its limit ran out")
			}
			if s := env.readState(hung); s.state != "active" || !s.hash.Valid {
				t.Errorf("the lease whose Destroy hung reads %+v; want it left active", s)
			}
			if s := env.readState(ok); s.state != "destroyed" || s.endReason != "shutdown" {
				t.Errorf("the other lease reads %s/%s; want destroyed/shutdown", s.state, s.endReason)
			}
			if got := len(eventsOf(env.emitter, audit.EventSandboxDestroy)); got != 1 {
				t.Errorf("%d destroy events; want one, for the lease that was ended", got)
			}
		})
	}
}

// TS-28-38: A second shutdown, and a shutdown of a disabled feature, do nothing.
// Requirement: 28-REQ-7.6
func TestShutdown_TwiceAndDisabled_TS_28_38(t *testing.T) {
	env := newExpiryBackendEnv(t)
	g := env.runGateway(&fakeTickers{})
	id, token := env.issueToken(`{"profile":"tools"}`)
	createVia(t, gateway2(t, g.URL(), token))
	if err := g.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	reaps, creates, closes := env.prov.reapCount(), env.prov.creates(), closeCount(env.prov.boxes)
	rows, events := wholeRows(t, env.db), len(env.emitter.recorded())

	if err := g.Shutdown(context.Background()); err != nil {
		t.Errorf("the second Shutdown returned %v; want nil", err)
	}
	if env.prov.reapCount() != reaps || env.prov.creates() != creates || closeCount(env.prov.boxes) != closes {
		t.Error("the second Shutdown called the provider")
	}
	if wholeRows(t, env.db) != rows || len(env.emitter.recorded()) != events {
		t.Error("the second Shutdown changed a lease or emitted an event")
	}
	if s := env.readState(id); s.state != "destroyed" || s.endReason != "shutdown" {
		t.Errorf("the lease reads %s/%s; want destroyed/shutdown", s.state, s.endReason)
	}

	// Nothing is done a second time: a lease that is active again is left alone.
	again := env.insertLeaseAt(40, "widgets", "leased", env.clock.Now())
	if changed, err := env.store.MarkActive(again, "box-again"); err != nil || !changed {
		t.Fatalf("MarkActive = (%v, %v)", changed, err)
	}
	if err := g.Shutdown(context.Background()); err != nil {
		t.Errorf("the third Shutdown returned %v; want nil", err)
	}
	if s := env.readState(again); s.state != "active" {
		t.Errorf("a lease active after the first shutdown reads %s after another; want it left alone", s.state)
	}

	var disabled *Gateway
	if err := disabled.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown of a disabled feature returned %v; want nil", err)
	}
}

// Shutdown waits for a sweep in progress, one the reaper did not start included,
// before it ends any lease, and when the limit runs out while it waits it ends
// none.
// Requirement: 28-REQ-7.3
func TestShutdown_WaitsForASweepInProgress(t *testing.T) {
	t.Run("the sweep ends in time", func(t *testing.T) {
		env := newSweepEnv(t)
		env.prov.mu.Lock()
		env.prov.reapDelay = 200 * time.Millisecond
		env.prov.mu.Unlock()
		id := env.seed(1, "active", seedFields{boxID: "box-1", expires: env.clock.Now().Add(time.Hour)})

		swept := make(chan struct{})
		go func() {
			defer close(swept)
			_ = env.sweeper.Sweep(context.Background())
		}()
		waitFor(t, "the sweep to reach the provider", func() bool {
			env.prov.mu.Lock()
			defer env.prov.mu.Unlock()
			return env.prov.inflight > 0
		})

		if err := env.sweeper.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
		select {
		case <-swept:
		default:
			t.Error("Shutdown returned while a sweep was still running")
		}
		if s := env.readState(id); s.state != "destroyed" || s.endReason != "shutdown" {
			t.Errorf("the lease reads %s/%s; want destroyed/shutdown", s.state, s.endReason)
		}
	})

	t.Run("the limit runs out first", func(t *testing.T) {
		env := newSweepEnv(t)
		env.prov.mu.Lock()
		env.prov.reapDelay = time.Second
		env.prov.mu.Unlock()
		id := env.seed(1, "active", seedFields{boxID: "box-1", expires: env.clock.Now().Add(time.Hour)})
		swept := make(chan struct{})
		go func() {
			defer close(swept)
			_ = env.sweeper.Sweep(context.Background())
		}()
		waitFor(t, "the sweep to reach the provider", func() bool {
			env.prov.mu.Lock()
			defer env.prov.mu.Unlock()
			return env.prov.inflight > 0
		})

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if err := env.sweeper.Shutdown(ctx); err == nil {
			t.Error("Shutdown returned nil although its limit ran out while it waited")
		}
		if s := env.readState(id); s.state != "active" {
			t.Errorf("the lease reads %s; want it left active for the next start", s.state)
		}
		<-swept
	})
}

// TS-28-49: A Create refused before the gateway accepts it counts nowhere.
// Requirement: 28-REQ-9.5
func TestCreatesTotal_RefusalsLeaveItAlone_TS_28_49(t *testing.T) {
	counters := func(env *expiryBackendEnv) []float64 {
		return []float64{createsCounter(t, env.metrics, "created"), createsCounter(t, env.metrics, "failed")}
	}
	for _, tc := range []struct {
		name string
		// setup prepares the refusal; refuse makes the Create that is refused.
		setup  func(env *expiryBackendEnv) (id string)
		refuse func(env *expiryBackendEnv, id string) error
		code   codes.Code
	}{
		{"unknown token", func(env *expiryBackendEnv) string { return "" }, func(env *expiryBackendEnv, _ string) error {
			return rawCall(t, env.startGateway(), "sbl_"+strings.Repeat("x", 44), rpcGatewayCreate)
		}, codes.Unauthenticated},
		{"second Create", func(env *expiryBackendEnv) string {
			id := env.issue(`{"profile":"tools"}`)
			if _, err := env.create(id, sandbox.Spec{}); err != nil {
				t.Fatalf("Create: %v", err)
			}
			return id
		}, func(env *expiryBackendEnv, id string) error {
			_, err := env.create(id, sandbox.Spec{})
			return err
		}, codes.FailedPrecondition},
		{"missed deadline", func(env *expiryBackendEnv) string {
			id := env.issue(`{"profile":"tools"}`)
			env.pastLeaseDeadline()
			return id
		}, func(env *expiryBackendEnv, id string) error {
			_, err := env.create(id, sandbox.Spec{})
			return err
		}, codes.FailedPrecondition},
		{"shutdown", func(env *expiryBackendEnv) string {
			id := env.issue(`{"profile":"tools"}`)
			env.profile.EnterShutdown()
			return id
		}, func(env *expiryBackendEnv, id string) error {
			_, err := env.create(id, sandbox.Spec{})
			return err
		}, codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newExpiryBackendEnv(t)
			id := tc.setup(env)
			before := counters(env)

			err := tc.refuse(env, id)

			if err == nil {
				t.Fatal("the Create was not refused")
			}
			if tc.code == codes.Unauthenticated {
				if status.Code(err) != codes.Unauthenticated {
					t.Errorf("Create = %v; want Unauthenticated", err)
				}
			} else {
				wantCode(t, err, tc.code)
			}
			if got := counters(env); !slices.Equal(got, before) {
				t.Errorf("creates_total went from %v to %v; want it unchanged by a refusal", before, got)
			}
		})
	}
}
