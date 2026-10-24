package sandboxes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// A sweep lapses the leased leases past their deadline (expires_at for a row
// from before the column), destroys the box of the active ones past expires_at
// (the moment itself included) and ends them as destroyed, each with one expire
// event, the token revoked and no provider call for a lease without a box.
func TestSweep_EndsOverdueLeases(t *testing.T) {
	env := newSweepEnv(t)
	t0 := env.clock.Now()
	lapsed := env.seed(1, "leased", seedFields{leaseExpires: timePtr(t0.Add(5 * time.Minute))})
	legacy := env.seed(2, "leased", seedFields{created: t0.Add(-2 * time.Hour)})
	ahead := env.seed(3, "leased", seedFields{leaseExpires: timePtr(t0.Add(30 * time.Minute))})
	overdue := env.seed(4, "active", seedFields{boxID: "box-4", expires: t0.Add(10 * time.Minute)})
	fresh := env.seed(5, "active", seedFields{boxID: "box-5", expires: t0.Add(10*time.Minute + time.Second)})
	env.clock.Advance(10 * time.Minute)

	env.sweep()

	for id, state := range map[string]string{lapsed: "expired", legacy: "expired", overdue: "destroyed"} {
		if s := env.readState(id); s.state != state || s.endReason != "expired" || s.hash.Valid {
			t.Errorf("lease %s reads %+v; want %s/expired without a hash", id, s, state)
		}
	}
	for _, id := range []string{ahead, fresh} {
		if s := env.readState(id); !s.hash.Valid || (s.state != "leased" && s.state != "active") {
			t.Errorf("lease %s with time left reads %+v", id, s)
		}
	}
	if got := env.boxes.ids(); !slices.Equal(got, []string{"box-5"}) || !slices.Equal(env.boxes.destroyedIDs(), []string{"box-4"}) {
		t.Errorf("boxes %v, destroyed %v; want box-5 kept and box-4 destroyed", got, env.boxes.destroyedIDs())
	}
	events := env.eventsOf(audit.EventSandboxExpire)
	if len(events) != 3 || len(env.eventsOf(audit.EventSandboxDestroy)) != 0 {
		t.Errorf("%d expire events, %d destroy events; want 3 and 0", len(events), len(env.eventsOf(audit.EventSandboxDestroy)))
	}
	for n, want := range map[int]bool{1: false, 2: false, 3: true, 4: false, 5: true} {
		if env.tokenWorks(n) != want {
			t.Errorf("token %d works = %v; want %v", n, !want, want)
		}
	}
}

// The steps run in order, Reap before List, the overdue boxes destroyed before
// the orphans are looked for, the orphans before the prune, and the gauge
// recalibrated last; a failing step is logged and returned and the rest still
// run; a hung call is cancelled at the call limit and logged as a timeout.
func TestSweep_StepOrderFailuresAndTimeouts(t *testing.T) {
	env := newSweepEnv(t)
	calls := &callLog{}
	env.prov.set(func(p *recordingProvider) { p.calls = calls })
	env.boxes.calls = calls
	t0 := env.clock.Now()
	env.seed(1, "active", seedFields{boxID: "box-1", expires: t0.Add(30 * time.Minute)})
	lapsed := env.seed(2, "leased", seedFields{leaseExpires: timePtr(t0.Add(5 * time.Minute))})
	old := env.seed(3, "destroyed", seedFields{created: t0.Add(-40 * time.Hour), ended: timePtr(t0.Add(-30 * time.Hour)), boxID: "box-old"})
	env.metrics.SandboxesActive.WithLabelValues("widgets").Set(7)
	env.clock.Advance(time.Hour)
	presentAtCall := map[string]bool{}
	gaugeAtDestroy := -1.0
	env.boxes.before = func(call string) {
		presentAtCall[call] = env.rowExists(old)
		if call == "Destroy box-1" {
			gaugeAtDestroy = activeGauge(t, env.metrics, "widgets")
		}
	}
	env.sweep()
	log := calls.all()
	pos := func(call string) int { return slices.Index(log, call) }
	if pos("Reap") < 0 || pos("Reap") > pos("List") || pos("Destroy box-1") < 0 || pos("Destroy box-1") > pos("List") || pos("Destroy box-old") < pos("List") {
		t.Errorf("calls = %v; want Reap, the overdue destroy, List, the orphan destroy", log)
	}
	if !presentAtCall["Destroy box-old"] || env.rowExists(old) || env.readState(lapsed).state != "expired" || gaugeAtDestroy != 7 || activeGauge(t, env.metrics, "widgets") != 0 {
		t.Errorf("old present at the orphan destroy %v, pruned %v, lapsed %s, gauge at destroy %v, after %v", presentAtCall["Destroy box-old"], !env.rowExists(old), env.readState(lapsed).state, gaugeAtDestroy, activeGauge(t, env.metrics, "widgets"))
	}

	env = newSweepEnv(t)
	env.prov.setReap(nil, errors.New("reap failed at https://user:pw@host/x"))
	env.boxes.listErr = errors.New("list: engine unreachable")
	t0 = env.clock.Now()
	lapsed = env.seed(1, "leased", seedFields{leaseExpires: timePtr(t0.Add(time.Minute))})
	overdue := env.seed(2, "active", seedFields{boxID: "box-2", expires: t0.Add(time.Minute)})
	old = env.seed(3, "destroyed", seedFields{created: t0.Add(-40 * time.Hour), ended: timePtr(t0.Add(-30 * time.Hour))})
	env.clock.Advance(time.Hour)
	logs := captureDefaultLog(t)
	err := env.sweeper.Sweep(context.Background())
	if err == nil || !strings.Contains(err.Error(), "reap failed") || !strings.Contains(err.Error(), "list:") || strings.Contains(err.Error(), "user:pw") {
		t.Errorf("Sweep = %v; want both failures without the userinfo", err)
	}
	if env.readState(lapsed).state != "expired" || env.readState(overdue).state != "destroyed" || env.rowExists(old) || strings.Count(logs.String(), `"level":"ERROR"`) < 2 {
		t.Error("a failing step stopped the rest, or was not logged")
	}

	env = newSweepEnv(t)
	cfg := env.sweeperConfig()
	cfg.CallTimeout = 50 * time.Millisecond
	env.sweeper = NewSweeper(cfg)
	env.prov.set(func(p *recordingProvider) { p.reapHang = true })
	env.boxes.listHang = true
	env.boxes.destroyHang = map[string]bool{"box-2": true, "box-old": true}
	t0 = env.clock.Now()
	overdue = env.seed(2, "active", seedFields{boxID: "box-2", expires: t0.Add(time.Minute)})
	old = env.seed(3, "destroyed", seedFields{created: t0.Add(-40 * time.Hour), ended: timePtr(t0.Add(-30 * time.Hour)), boxID: "box-old"})
	env.clock.Advance(time.Hour)
	logs = captureDefaultLog(t)
	start := time.Now()
	err = env.sweeper.Sweep(context.Background())
	if time.Since(start) > 2*time.Second || err == nil || strings.Count(logs.String(), "timed out") < 3 {
		t.Errorf("a sweep of hung calls took %v, returned %v and logged %d timeouts", time.Since(start), err, strings.Count(logs.String(), "timed out"))
	}
	if s := env.readState(overdue); s.state != "destroyed" || env.rowExists(old) {
		t.Errorf("the overdue lease reads %+v, old pruned %v", s, !env.rowExists(old))
	}
}

// Terminal leases that ended 24 hours ago or less stay readable through GET
// one, ?all=true and the admin list; older ones are pruned; leased and active
// leases are never deleted whatever their age.
func TestSweep_PrunesOldTerminalLeasesOnly(t *testing.T) {
	env := newSweepEnv(t)
	now := env.clock.Now()
	old := now.Add(-40 * time.Hour)
	kept := map[string]string{
		"destroyed 23h ago":        env.seed(1, "destroyed", seedFields{created: old, ended: timePtr(now.Add(-23 * time.Hour))}),
		"expired exactly 24h ago":  env.seed(2, "expired", seedFields{created: old, ended: timePtr(now.Add(-24 * time.Hour))}),
		"no ended_at, updated 24h": env.seed(3, "failed", seedFields{created: old, updated: now.Add(-24 * time.Hour)}),
		"ancient leased":           env.seed(4, "leased", seedFields{created: old, expires: old.Add(time.Hour)}),
		"ancient active":           env.seed(5, "active", seedFields{created: old, expires: old.Add(time.Hour), boxID: "b5"}),
	}
	pruned := map[string]string{
		"destroyed 24h1s ago": env.seed(6, "destroyed", seedFields{created: old, ended: timePtr(now.Add(-24*time.Hour - time.Second))}),
		"failed 48h ago":      env.seed(7, "failed", seedFields{created: old, ended: timePtr(now.Add(-48 * time.Hour))}),
		"no ended_at, old":    env.seed(8, "expired", seedFields{created: old, updated: now.Add(-25 * time.Hour)}),
	}
	env.sweep()
	has := func(path string) bool {
		return slices.ContainsFunc(leaseList(t, env.get(path, adminAuth())), func(l map[string]any) bool { return l["id"] == kept["destroyed 23h ago"] })
	}
	if !has("/api/v1/workspaces/widgets/sandboxes?all=true") || !has("/api/v1/sandboxes?all=true") {
		t.Error("a recent terminal lease is not listed")
	}
	for name, id := range kept {
		if rec := env.get("/api/v1/workspaces/widgets/sandboxes/"+id, ownerKey()); rec.Code != http.StatusOK {
			t.Errorf("%s: GET one answered %d; want 200", name, rec.Code)
		}
	}
	for name, id := range pruned {
		if env.rowExists(id) {
			t.Errorf("%s was not pruned", name)
		}
	}
}

// The orphan step destroys exactly the listed boxes that belong to a terminal
// lease, by sandbox_id or by the hub.lease label, and leaves every other box
// alone; a box that cannot be destroyed is logged and tried again next sweep.
func TestSweep_RemovesOrphansOfTerminalLeasesOnly(t *testing.T) {
	for _, labels := range []bool{true, false} {
		env := newSweepEnv(t)
		t0 := env.clock.Now()
		env.seed(1, "destroyed", seedFields{boxID: "box-1", ended: timePtr(t0.Add(-time.Hour))})
		expired := env.seed(2, "expired", seedFields{ended: timePtr(t0.Add(-time.Hour))})
		env.boxes.add("box-2", map[string]string{"hub.lease": expired})
		env.seed(3, "active", seedFields{boxID: "box-3", expires: t0.Add(time.Hour)})
		env.boxes.add("box-4", map[string]string{"other": "label"})
		env.boxes.add("box-5", map[string]string{"hub.lease": leaseID(99)})
		env.boxes.add("box-6", map[string]string{"hub.lease": env.seed(6, "leased", seedFields{leaseExpires: timePtr(t0.Add(time.Hour))})})
		if !labels {
			env.boxes.mu.Lock()
			for i := range env.boxes.boxes {
				env.boxes.boxes[i].Labels = nil
			}
			env.boxes.mu.Unlock()
		}
		env.boxes.failNext = errors.New("podman rm failed at https://user:pw@registry.example/x")
		logs := captureDefaultLog(t)
		env.sweep()
		want := []string{"box-1"}
		if labels {
			want = []string{"box-1", "box-2"}
		}
		got := env.boxes.destroyedIDs()
		slices.Sort(got)
		if !slices.Equal(got, want) || !strings.Contains(logs.String(), "podman rm failed") || strings.Contains(logs.String(), "user:pw") {
			t.Errorf("labels %v: destroyed %v; want %v, the failure logged without userinfo", labels, got, want)
		}
		env.sweep()
		if left := env.boxes.ids(); slices.Contains(left, "box-1") || (labels && slices.Contains(left, "box-2")) || len(left) < 3 {
			t.Errorf("labels %v: boxes left %v; want the orphans gone and the others kept", labels, left)
		}
	}
}

// Sweeps never overlap and never end a lease twice, also against a DELETE that
// gets there while the sweep destroys the box; a sweep in shutdown mode does
// nothing; a sweep whose context ends while it waits returns the context's error.
func TestSweep_SerialisedAndEndsEachLeaseOnce(t *testing.T) {
	env := newSweepEnv(t)
	env.prov.set(func(p *recordingProvider) { p.reapDelay = 10 * time.Millisecond })
	t0 := env.clock.Now()
	var overdue []string
	for n := 1; n <= 3; n++ {
		overdue = append(overdue, env.seed(n, "active", seedFields{boxID: fmt.Sprintf("box-%d", n), expires: t0.Add(time.Minute)}))
	}
	overdue = append(overdue, env.seed(4, "leased", seedFields{leaseExpires: timePtr(t0.Add(time.Minute))}))
	byDelete := env.seed(5, "active", seedFields{boxID: "box-5", expires: t0.Add(time.Minute)})
	deleted := false
	env.boxes.before = func(call string) {
		if call == "Destroy box-5" && !deleted {
			deleted = true
			env.delete(byDelete)
		}
	}
	env.clock.Advance(time.Hour)
	reaps := env.prov.reapCount()
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			if err := env.sweeper.Sweep(context.Background()); err != nil {
				t.Errorf("Sweep: %v", err)
			}
		})
	}
	wg.Wait()
	env.prov.mu.Lock()
	maxInflight := env.prov.maxInflight
	env.prov.mu.Unlock()
	if maxInflight != 1 || env.prov.reapCount()-reaps != 6 {
		t.Errorf("at most %d Reap at once over %d sweeps; want 1 and 6", maxInflight, env.prov.reapCount()-reaps)
	}
	perLease := map[string]int{}
	for _, e := range env.emitter.recorded() {
		perLease[e.ResourceID]++
	}
	for _, id := range overdue {
		if perLease[id] != 1 {
			t.Errorf("lease %s has %d events; want one", id, perLease[id])
		}
	}
	if !deleted || perLease[byDelete] != 1 || env.readState(byDelete).endReason != "api" || len(env.eventsOf(audit.EventSandboxDestroy)) != 1 {
		t.Errorf("the lease DELETE ended mid-sweep has %d events and reason %s", perLease[byDelete], env.readState(byDelete).endReason)
	}
	if count, _ := leaseSeconds(t, env.metrics); count != 4 {
		t.Errorf("lease_seconds observed %d leases; want the 4 destroyed", count)
	}

	env.profile.EnterShutdown()
	late := env.seed(9, "leased", seedFields{leaseExpires: timePtr(env.clock.Now().Add(-time.Minute))})
	if err := env.sweeper.Sweep(context.Background()); err != nil || env.readState(late).state != "leased" {
		t.Errorf("a sweep in shutdown mode: %v, lease %s; want nothing done", err, env.readState(late).state)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	env.sweeper.running <- struct{}{}
	if err := env.sweeper.Sweep(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("a sweep waiting with an ended context = %v", err)
	}
	<-env.sweeper.running
}
