package sandboxes

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// withCallTimeout makes the environment's sweeper limit every provider call to d.
func (e *sweepEnv) withCallTimeout(d time.Duration) {
	cfg := e.sweeperConfig()
	cfg.CallTimeout = d
	e.sweeper = NewSweeper(cfg)
}

// rowExists reports whether the lease has a row.
func (e *sweepEnv) rowExists(id string) bool {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM sandbox_leases WHERE id = ?`, id).Scan(&n); err != nil {
		e.t.Fatalf("counting rows of %s: %v", id, err)
	}
	return n == 1
}

// TS-28-16: A sweep runs Reap before List, destroys the boxes of overdue leases
// before it looks for orphans, and prunes and recalibrates after the endings.
// Requirement: 28-REQ-4.1
func TestSweep_StepOrder_TS_28_16(t *testing.T) {
	env := newSweepEnv(t)
	calls := &callLog{}
	env.prov.calls, env.boxes.calls = calls, calls
	t0 := env.clock.Now()
	env.seed(1, "active", seedFields{boxID: "box-1", expires: t0.Add(30 * time.Minute)})
	lapsed := env.seed(2, "leased", seedFields{leaseExpires: timePtr(t0.Add(5 * time.Minute))})
	// A lease that ended long ago and whose box is still there. The sweep finds the
	// box through the lease, so it must look before it prunes the lease.
	old := env.seed(3, "destroyed", seedFields{created: t0.Add(-40 * time.Hour), ended: timePtr(t0.Add(-30 * time.Hour)), boxID: "box-old"})
	env.metrics.SandboxesActive.WithLabelValues("widgets").Set(7)
	env.clock.Advance(time.Hour)

	presentAtCall := map[string]bool{}
	env.boxes.before = func(call string) { presentAtCall[call] = env.rowExists(old) }

	env.sweep()

	log := calls.all()
	pos := func(call string) int { return slices.Index(log, call) }
	if pos("Reap") < 0 || pos("List") < 0 || pos("Reap") > pos("List") {
		t.Errorf("calls = %v; want Reap before List", log)
	}
	if pos("Destroy box-1") < 0 || pos("Destroy box-1") > pos("List") {
		t.Errorf("calls = %v; want the overdue box destroyed before the listing", log)
	}
	if pos("Destroy box-old") < pos("List") || pos("Destroy box-old") < 0 {
		t.Errorf("calls = %v; want the orphan destroyed after the listing", log)
	}
	if !presentAtCall["Destroy box-old"] {
		t.Error("the old lease was pruned before its orphaned box was destroyed")
	}
	if env.rowExists(old) {
		t.Error("the old lease was not pruned")
	}
	if s := env.readState(lapsed); s.state != "expired" {
		t.Errorf("the lapsed lease reads %s; want expired", s.state)
	}
	if got := activeGauge(t, env.metrics, "widgets"); got != 0 {
		t.Errorf("the gauge reads %v when the sweep returns; want the table's 0 active leases", got)
	}
}

// TS-28-17: A failing step is logged and every later step still runs.
// Requirement: 28-REQ-4.2
func TestSweep_FailingStepsAreLoggedAndTheRestRuns_TS_28_17(t *testing.T) {
	env := newSweepEnv(t)
	env.prov.setReap(nil, fmt.Errorf("reap: engine unreachable"))
	env.boxes.listErr = fmt.Errorf("list: engine unreachable")
	t0 := env.clock.Now()
	lapsed := env.seed(1, "leased", seedFields{leaseExpires: timePtr(t0.Add(time.Minute))})
	overdue := env.seed(2, "active", seedFields{boxID: "box-2", expires: t0.Add(time.Minute)})
	old := env.seed(3, "destroyed", seedFields{created: t0.Add(-40 * time.Hour), ended: timePtr(t0.Add(-30 * time.Hour))})
	env.metrics.SandboxesActive.WithLabelValues("widgets").Set(7)
	env.clock.Advance(time.Hour)
	logs := captureDefaultLog(t)

	err := env.sweeper.Sweep(context.Background())

	if err == nil || !strings.Contains(err.Error(), "reap") || !strings.Contains(err.Error(), "list") {
		t.Errorf("Sweep returned %v; want both failures", err)
	}
	if s := env.readState(lapsed); s.state != "expired" {
		t.Errorf("the lapsed lease reads %s; want expired", s.state)
	}
	if s := env.readState(overdue); s.state != "destroyed" {
		t.Errorf("the overdue lease reads %s; want destroyed", s.state)
	}
	if env.rowExists(old) {
		t.Error("the old terminal lease was not pruned")
	}
	if got := activeGauge(t, env.metrics, "widgets"); got != 0 {
		t.Errorf("the gauge reads %v; want the recalibrated 0", got)
	}
	if n := strings.Count(logs.String(), `"level":"ERROR"`); n < 2 {
		t.Errorf("%d error records; want at least one for each failed step:\n%s", n, logs)
	}
}

// TS-28-21: The orphan step destroys exactly the boxes matched to a terminal
// lease, by box id or by the hub.lease label, and no others.
// Requirement: 28-REQ-4.6
func TestSweep_OrphanStepMatchesTerminalLeasesOnly_TS_28_21(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels bool
		want   []string
	}{
		{"the listing shows labels", true, []string{"box-1", "box-2"}},
		{"the listing shows no labels", false, []string{"box-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newSweepEnv(t)
			t0 := env.clock.Now()
			future := t0.Add(time.Hour)
			// B1: its id is the sandbox_id of a destroyed lease.
			env.seed(1, "destroyed", seedFields{boxID: "box-1", ended: timePtr(t0.Add(-time.Hour))})
			// B2: its hub.lease label names an expired lease, which never had a box.
			expired := env.seed(2, "expired", seedFields{ended: timePtr(t0.Add(-time.Hour))})
			env.boxes.add("box-2", map[string]string{"hub.lease": expired})
			// B3: the box of an active lease. B4: no hub.lease label. B5: a lease this hub
			// does not know.
			env.seed(3, "active", seedFields{boxID: "box-3", expires: future})
			env.boxes.add("box-4", map[string]string{"other": "label"})
			env.boxes.add("box-5", map[string]string{"hub.lease": leaseID(99)})
			// A leased lease's box label: a lease that is still leased has no box, and
			// a box that claims one is not hub's to destroy.
			leased := env.seed(6, "leased", seedFields{leaseExpires: timePtr(future)})
			env.boxes.add("box-6", map[string]string{"hub.lease": leased})
			if !tc.labels {
				env.boxes.clearLabels()
			}

			env.sweep()

			got := env.boxes.destroyedIDs()
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Errorf("destroyed %v; want exactly %v", got, tc.want)
			}
			for _, id := range []string{"box-3", "box-4", "box-5", "box-6"} {
				if slices.Contains(got, id) {
					t.Errorf("%s was destroyed although no terminal lease owns it", id)
				}
			}
		})
	}
}

// TS-28-22: Concurrent sweeps are serialised and no lease gets a second ending
// event.
// Requirement: 28-REQ-4.7
func TestSweep_ConcurrentSweepsAreSerialised_TS_28_22(t *testing.T) {
	env := newSweepEnv(t)
	env.prov.mu.Lock()
	env.prov.reapDelay = 10 * time.Millisecond
	env.prov.mu.Unlock()
	t0 := env.clock.Now()
	var overdue []string
	for n := 1; n <= 4; n++ {
		overdue = append(overdue, env.seed(n, "active", seedFields{boxID: fmt.Sprintf("box-%d", n), expires: t0.Add(time.Minute)}))
	}
	for n := 5; n <= 7; n++ {
		overdue = append(overdue, env.seed(n, "leased", seedFields{leaseExpires: timePtr(t0.Add(time.Minute))}))
	}
	// A lease another path ended before any sweep got to it.
	byDelete := env.seed(8, "active", seedFields{boxID: "box-8", expires: t0.Add(time.Minute)})
	if rec := env.do(http.MethodDelete, "/api/v1/workspaces/widgets/sandboxes/"+byDelete, "", apiKeyAuth()); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE answered %d: %s", rec.Code, rec.Body)
	}
	env.clock.Advance(time.Hour)
	reapsBefore := env.prov.reapCount() // the start-up probe has called Reap already

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if err := env.sweeper.Sweep(context.Background()); err != nil {
				t.Errorf("Sweep returned error: %v", err)
			}
		})
	}
	wg.Wait()

	env.prov.mu.Lock()
	maxInflight := env.prov.maxInflight
	env.prov.mu.Unlock()
	if reaps := env.prov.reapCount() - reapsBefore; maxInflight != 1 || reaps != 10 {
		t.Errorf("at most %d Reap calls at once over %d sweeps; want 1 and 10", maxInflight, reaps)
	}
	perLease := map[string]int{}
	for _, e := range env.emitter.recorded() {
		perLease[e.ResourceID]++
	}
	for _, id := range overdue {
		if perLease[id] != 1 {
			t.Errorf("lease %s has %d events; want exactly one ending event", id, perLease[id])
		}
	}
	if len(eventsOf(env.emitter, audit.EventSandboxExpire)) != len(overdue) {
		t.Errorf("%d expire events; want %d", len(eventsOf(env.emitter, audit.EventSandboxExpire)), len(overdue))
	}
	if perLease[byDelete] != 1 || len(eventsOf(env.emitter, audit.EventSandboxDestroy)) != 1 {
		t.Errorf("the deleted lease has %d events; want only DELETE's destroy event", perLease[byDelete])
	}
}

// TS-28-23: A provider call that hangs is cancelled at the per-call limit, logged
// as a timeout, and the sweep goes on.
// Requirement: 28-REQ-4.8
func TestSweep_HungCallsAreCancelled_TS_28_23(t *testing.T) {
	env := newSweepEnv(t)
	env.withCallTimeout(50 * time.Millisecond)
	env.prov.mu.Lock()
	env.prov.reapHang = true
	env.prov.mu.Unlock()
	env.boxes.listHang = true
	env.boxes.destroyHang = map[string]bool{"box-2": true}
	t0 := env.clock.Now()
	lapsed := env.seed(1, "leased", seedFields{leaseExpires: timePtr(t0.Add(time.Minute))})
	overdue := env.seed(2, "active", seedFields{boxID: "box-2", expires: t0.Add(time.Minute)})
	old := env.seed(3, "destroyed", seedFields{created: t0.Add(-40 * time.Hour), ended: timePtr(t0.Add(-30 * time.Hour))})
	env.clock.Advance(time.Hour)
	logs := captureDefaultLog(t)

	start := time.Now()
	err := env.sweeper.Sweep(context.Background())
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("the sweep took %v with every call limited to 50ms", elapsed)
	}
	if err == nil {
		t.Error("Sweep returned nil although two of its steps timed out")
	}
	if n := strings.Count(logs.String(), "timed out"); n < 3 {
		t.Errorf("%d timeout records; want one for Reap, the Destroy and List:\n%s", n, logs)
	}
	if s := env.readState(lapsed); s.state != "expired" {
		t.Errorf("the lapsed lease reads %s; want expired", s.state)
	}
	if s := env.readState(overdue); s.state != "destroyed" || s.endReason != "expired" {
		t.Errorf("the overdue lease reads %+v; want destroyed/expired although its Destroy hung", s)
	}
	if env.rowExists(old) {
		t.Error("the old terminal lease was not pruned")
	}
}

// A Destroy of an orphaned box that hangs is cancelled at the limit too, and the
// sweep goes on to prune.
// Requirement: 28-REQ-4.8
func TestSweep_HungOrphanDestroyIsCancelled(t *testing.T) {
	env := newSweepEnv(t)
	env.withCallTimeout(50 * time.Millisecond)
	env.boxes.destroyHang = map[string]bool{"box-old": true}
	t0 := env.clock.Now()
	old := env.seed(1, "destroyed", seedFields{created: t0.Add(-40 * time.Hour), ended: timePtr(t0.Add(-30 * time.Hour)), boxID: "box-old"})
	recent := env.seed(2, "destroyed", seedFields{created: t0.Add(-4 * time.Hour), ended: timePtr(t0.Add(-time.Hour)), boxID: "box-recent"})
	logs := captureDefaultLog(t)

	start := time.Now()
	if err := env.sweeper.Sweep(context.Background()); err != nil {
		t.Errorf("Sweep returned %v; a box that cannot be destroyed is logged, not a failed step", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("the sweep took %v with every call limited to 50ms", elapsed)
	}
	if got := env.boxes.destroyedIDs(); !slices.Equal(got, []string{"box-old", "box-recent"}) {
		t.Errorf("destroyed %v; want both boxes tried, the hung one first", got)
	}
	if !strings.Contains(logs.String(), "timed out") || !strings.Contains(logs.String(), "box-old") {
		t.Errorf("the timeout of the hung Destroy was not logged:\n%s", logs)
	}
	if env.rowExists(old) || !env.rowExists(recent) {
		t.Errorf("old row present %v, recent row present %v; want the old one pruned and the recent one kept", env.rowExists(old), env.rowExists(recent))
	}
}
