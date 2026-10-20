package sandboxes

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/hub/internal/audit"
)

// fakeTickers makes tickers a test fires by hand.
type fakeTickers struct {
	mu      sync.Mutex
	made    []chan time.Time
	stopped int
}

func (f *fakeTickers) make(time.Duration) (<-chan time.Time, func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan time.Time, 8)
	f.made = append(f.made, ch)
	return ch, func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.stopped++
	}
}

// fire sends one tick to the latest ticker made, without waiting for anyone to
// take it.
func (f *fakeTickers) fire() {
	f.mu.Lock()
	defer f.mu.Unlock()
	select {
	case f.made[len(f.made)-1] <- time.Now():
	default:
	}
}

func (f *fakeTickers) stoppedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

// withTickers makes the environment's sweeper reap on the given tickers.
func (e *sweepEnv) withTickers(tickers *fakeTickers) {
	e.sweeper = NewSweeper(SweeperConfig{
		DB: e.db, Provider: e.prov, Boxes: e.boxes, Audit: e.sink, Metrics: e.metrics,
		Now: e.clock.Now, CallTimeout: time.Second, Interval: time.Minute, Tickers: tickers.make,
	})
}

// waitFor polls until cond holds, and fails the test when it does not within ten
// seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// TS-28-24: Restart recovery destroys the box of every active lease and ends it
// as destroyed with end_reason restart, and leaves a leased lease alone.
// Requirement: 28-REQ-5.1
func TestRecoverActive_TS_28_24(t *testing.T) {
	env := newSweepEnv(t)
	now := env.clock.Now()
	a := env.seed(1, "active", seedFields{boxID: "box-1", expires: now.Add(time.Hour)})
	b := env.seed(2, "active", seedFields{workspace: "gadgets", boxID: "box-2", expires: now.Add(time.Hour)})
	leased := env.seed(3, "leased", seedFields{leaseExpires: timePtr(now.Add(time.Minute))})
	leasedBefore := strings.Join(env.rowText(leased), "|")
	reaps := env.prov.reapCount()

	if err := env.sweeper.RecoverActive(context.Background()); err != nil {
		t.Fatalf("RecoverActive: %v", err)
	}

	if got := env.boxes.destroyedIDs(); len(got) != 2 || got[0] != "box-1" || got[1] != "box-2" {
		t.Errorf("destroyed %v; want box-1 and box-2", got)
	}
	for _, id := range []string{a, b} {
		if s := env.readState(id); s.state != "destroyed" || s.endReason != "restart" || s.hash.Valid {
			t.Errorf("lease %s reads %+v; want destroyed/restart with no token hash", id, s)
		}
	}
	if got := strings.Join(env.rowText(leased), "|"); got != leasedBefore {
		t.Errorf("the leased lease changed:\n%s\n->\n%s", leasedBefore, got)
	}
	events := eventsOf(env.emitter, audit.EventSandboxDestroy)
	if len(events) != 2 {
		t.Fatalf("%d hub.sandbox.destroy events; want one per lease: %v", len(events), events)
	}
	for _, e := range events {
		if e.Metadata["reason"] != "restart" || e.ActorType != "system" || e.ActorID != "" {
			t.Errorf("event %+v; want reason restart by the system with no actor id", e)
		}
	}
	if got := len(eventsOf(env.emitter, audit.EventSandboxExpire)); got != 0 {
		t.Errorf("%d expire events; want none", got)
	}
	if got := env.prov.reapCount() - reaps; got != 0 {
		t.Errorf("recovery called the provider's Reap %d times; that is the sweep's step", got)
	}
}

// 28-PATH-5: the gateway accepts connections only after recovery has ended the
// leases an earlier process left active.
// Requirement: 28-REQ-5.1
func TestGatewayRun_RecoversBeforeItServes_TS_28_24(t *testing.T) {
	env, deps := gatewayTestEnv(t, nil)
	var ids []string
	for n := 1; n <= 2; n++ {
		id := env.insertLeaseAt(n, "widgets", "leased", env.clock.Now())
		if changed, err := env.store.MarkActive(id, fmt.Sprintf("box-%d", n)); err != nil || !changed {
			t.Fatalf("MarkActive = (%v, %v)", changed, err)
		}
		ids = append(ids, id)
	}

	statesWhenServing := make(chan []string, 1)
	seams := gatewaySeams{
		serve: func(s *gatewayd.Server, l net.Listener) error {
			var states []string
			for _, id := range ids {
				st := env.readState(id)
				states = append(states, st.state+"/"+st.endReason)
			}
			statesWhenServing <- states
			return s.Serve(l)
		},
		exit: func(int) { panic("the gateway must not exit") },
	}
	gw, err := newGateway(env.profile, deps, seams)
	if err != nil {
		t.Fatalf("newGateway: %v", err)
	}
	stopAfter(t, gw)
	select {
	case <-statesWhenServing:
		t.Fatal("the gateway served before Run was called")
	case <-time.After(50 * time.Millisecond):
	}

	if err := gw.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	select {
	case states := <-statesWhenServing:
		for i, st := range states {
			if st != "destroyed/restart" {
				t.Errorf("lease %d read %s when the gateway began to serve; want destroyed/restart", i+1, st)
			}
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the gateway never began to serve")
	}
}

// TS-28-25: A Destroy that fails during recovery still ends the lease, and a
// later sweep's orphan step destroys the box.
// Requirement: 28-REQ-5.2
func TestRecoverActive_DestroyFailure_TS_28_25(t *testing.T) {
	env := newSweepEnv(t)
	id := env.seed(1, "active", seedFields{boxID: "box-1", expires: env.clock.Now().Add(time.Hour)})
	env.boxes.failNext = fmt.Errorf("podman rm failed at https://user:pw@registry.example/x")
	logs := captureDefaultLog(t)

	if err := env.sweeper.RecoverActive(context.Background()); err != nil {
		t.Fatalf("RecoverActive: %v; a Destroy that fails is logged, not returned", err)
	}
	if s := env.readState(id); s.state != "destroyed" || s.endReason != "restart" {
		t.Errorf("the lease reads %s/%s; want destroyed/restart", s.state, s.endReason)
	}
	if out := logs.String(); !strings.Contains(out, "podman rm failed") || strings.Contains(out, "user:pw") {
		t.Errorf("the failure is not in the log scrubbed:\n%s", out)
	}
	if boxes, _ := env.boxes.List(context.Background()); len(boxes) != 1 {
		t.Fatalf("boxes %v; want the box still there", boxes)
	}

	env.sweep()

	if got := env.boxes.destroyedIDs(); len(got) != 2 || got[1] != "box-1" {
		t.Errorf("destroyed %v; want box-1 tried again by the sweep", got)
	}
	if boxes, _ := env.boxes.List(context.Background()); len(boxes) != 0 {
		t.Errorf("boxes %v after the sweep; want none", boxes)
	}
}

// TS-28-26: After the first sweep the reaper sweeps once per tick, and not after
// its context is cancelled.
// Requirement: 28-REQ-5.3
func TestStartReaper_TS_28_26(t *testing.T) {
	env := newSweepEnv(t)
	tickers := &fakeTickers{}
	env.withTickers(tickers)
	base := env.prov.reapCount()
	reaps := func() int { return env.prov.reapCount() - base }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reaper, err := env.sweeper.StartReaper(ctx)
	if err != nil {
		t.Fatalf("StartReaper: %v", err)
	}
	if got := reaps(); got != 1 {
		t.Fatalf("sweeps after start = %d; want the first one already done", got)
	}

	for range 3 {
		tickers.fire()
	}
	waitFor(t, "three sweeps on three ticks", func() bool { return reaps() == 4 })

	cancel()
	reaper.Stop()
	tickers.fire()
	time.Sleep(50 * time.Millisecond)
	if got := reaps(); got != 4 {
		t.Errorf("sweeps after the context was cancelled = %d; want still 4", got)
	}
	if tickers.stoppedCount() != 1 {
		t.Errorf("the ticker was stopped %d times; want once", tickers.stoppedCount())
	}
}

// TS-28-27: Errors from recovery or the first sweep are returned and logged and
// do not stop the reaper.
// Requirement: 28-REQ-5.4
func TestRunStartup_ErrorsDoNotStopStartup_TS_28_27(t *testing.T) {
	env := newSweepEnv(t)
	tickers := &fakeTickers{}
	env.withTickers(tickers)
	env.prov.setReap(nil, errors.New("reap: engine down"))
	env.boxes.listErr = errors.New("list: engine down")
	env.boxes.setDestroyErr(errors.New("destroy: engine down"))
	env.seed(1, "active", seedFields{boxID: "box-1", expires: env.clock.Now().Add(time.Hour)})
	base := env.prov.reapCount()
	logs := captureDefaultLog(t)

	err := env.sweeper.RunStartup(context.Background())

	if err == nil || !strings.Contains(err.Error(), "reap") || !strings.Contains(err.Error(), "list") {
		t.Errorf("RunStartup returned %v; want the failures of the first sweep", err)
	}
	for _, want := range []string{"reap: engine down", "list: engine down", "destroy: engine down"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the log does not contain %q:\n%s", want, logs)
		}
	}
	if s := env.readState(leaseID(1)); s.state != "destroyed" || s.endReason != "restart" {
		t.Errorf("the lease reads %s/%s; want destroyed/restart although every call failed", s.state, s.endReason)
	}
	// The reaper runs all the same.
	tickers.fire()
	waitFor(t, "a sweep on the tick", func() bool { return env.prov.reapCount()-base == 2 })
	env.sweeper.StopReaper()
}

// TS-28-47: The gauge is recalibrated from the table after restart recovery and at
// the end of every sweep: stale labels go and every workspace with active leases
// is set to its count.
// Requirement: 28-REQ-9.3
func TestGauge_RecalibratedAfterRecoveryAndSweeps_TS_28_47(t *testing.T) {
	setup := func(t *testing.T) *sweepEnv {
		env := newSweepEnv(t)
		now := env.clock.Now()
		env.seed(1, "active", seedFields{workspace: "widgets", boxID: "b1", expires: now.Add(time.Hour)})
		env.seed(2, "active", seedFields{workspace: "widgets", boxID: "b2", expires: now.Add(time.Hour)})
		env.seed(3, "active", seedFields{workspace: "gadgets", boxID: "b3", expires: now.Add(time.Hour)})
		env.metrics.SandboxesActive.WithLabelValues("stale").Set(9)
		env.metrics.SandboxesActive.WithLabelValues("widgets").Set(7)
		return env
	}
	series := func(env *sweepEnv, workspace string) (float64, bool) {
		return metricSample(t, env.metrics, "afhub_sandboxes_active", map[string]string{"workspace": workspace})
	}

	t.Run("after a sweep", func(t *testing.T) {
		env := setup(t)
		env.sweep()
		if v, ok := series(env, "widgets"); !ok || v != 2 {
			t.Errorf("widgets = (%v, %v); want 2", v, ok)
		}
		if v, ok := series(env, "gadgets"); !ok || v != 1 {
			t.Errorf("gadgets = (%v, %v); want 1", v, ok)
		}
		if _, ok := series(env, "stale"); ok {
			t.Error("the stale label survived the sweep")
		}
	})

	t.Run("after recovery", func(t *testing.T) {
		env := setup(t)
		if err := env.sweeper.RecoverActive(context.Background()); err != nil {
			t.Fatalf("RecoverActive: %v", err)
		}
		// Recovery ended every active lease, so no workspace has a count left, and the
		// stale values are gone.
		for _, ws := range []string{"widgets", "gadgets", "stale"} {
			if v, ok := series(env, ws); ok {
				t.Errorf("%s still has a series (%v) after recovery ended every active lease", ws, v)
			}
		}
		if count, _ := leaseSeconds(t, env.metrics); count != 3 {
			t.Errorf("lease_seconds count = %d; want the 3 destroyed leases observed", count)
		}
	})
}

// A lease that cannot be ended is logged and reported, and recovery still ends the
// others.
// Requirement: 28-REQ-5.4
func TestRecoverActive_OneLeaseFailsTheRestAreEnded(t *testing.T) {
	env := newSweepEnv(t)
	now := env.clock.Now()
	stuck := env.seed(1, "active", seedFields{boxID: "box-1", expires: now.Add(time.Hour)})
	other := env.seed(2, "active", seedFields{boxID: "box-2", expires: now.Add(time.Hour)})
	if _, err := env.db.Exec(fmt.Sprintf(`CREATE TRIGGER refuse_update BEFORE UPDATE ON sandbox_leases
		WHEN OLD.id = '%s' BEGIN SELECT RAISE(ABORT, 'database is locked'); END`, stuck)); err != nil {
		t.Fatal(err)
	}
	logs := captureDefaultLog(t)

	err := env.sweeper.RecoverActive(context.Background())

	if err == nil || !strings.Contains(err.Error(), stuck) || !strings.Contains(err.Error(), "database is locked") {
		t.Errorf("RecoverActive returned %v; want the failure of lease %s", err, stuck)
	}
	if !strings.Contains(logs.String(), stuck) {
		t.Errorf("the failure is not logged with the lease id:\n%s", logs)
	}
	if s := env.readState(stuck); s.state != "active" {
		t.Errorf("the lease that could not be ended reads %s; want active, to be recovered at the next start", s.state)
	}
	if s := env.readState(other); s.state != "destroyed" || s.endReason != "restart" {
		t.Errorf("the other lease reads %s/%s; want destroyed/restart", s.state, s.endReason)
	}
}
