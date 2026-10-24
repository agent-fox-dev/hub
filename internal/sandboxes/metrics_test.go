package sandboxes

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// afhub_sandboxes_active goes up when a lease becomes active and down when it
// ends; a lease that becomes destroyed is observed once with its duration and
// a lapsed one is not; a second ending records nothing.
func TestMetrics_GaugeAndLeaseSeconds(t *testing.T) {
	env := newSweepEnv(t)
	id := env.issue("")
	if activeGauge(t, env.metrics, "widgets") != 0 {
		t.Fatal("a leased lease counts as active")
	}
	env.mustCreate(id)
	if activeGauge(t, env.metrics, "widgets") != 1 || activeGauge(t, env.metrics, "gadgets") != 0 {
		t.Errorf("gauge after Create: widgets %v, gadgets %v", activeGauge(t, env.metrics, "widgets"), activeGauge(t, env.metrics, "gadgets"))
	}
	env.clock.Advance(90 * time.Second)
	env.delete(id)
	env.delete(id)
	if count, sum := leaseSeconds(t, env.metrics); activeGauge(t, env.metrics, "widgets") != 0 || count != 1 || sum != 90 {
		t.Errorf("after DELETE twice: gauge %v, lease_seconds (%d, %v); want 0 and (1, 90)", activeGauge(t, env.metrics, "widgets"), count, sum)
	}
	lapsed := env.issue("")
	env.clock.Advance(6 * time.Minute)
	env.sweep()
	if count, _ := leaseSeconds(t, env.metrics); env.readState(lapsed).state != "expired" || count != 1 {
		t.Errorf("a lapsed lease was observed: count %d", count)
	}
}

// Recalibration sets the gauge from the table, drops stale labels and keeps the
// "" placeholder when no workspace has an active lease.
func TestMetrics_Recalibrate(t *testing.T) {
	env := newSweepEnv(t)
	now := env.clock.Now()
	for n, ws := range []string{"widgets", "widgets", "gadgets"} {
		env.seed(n+1, "active", seedFields{workspace: ws, boxID: fmt.Sprintf("b%d", n), expires: now.Add(time.Hour)})
	}
	env.metrics.SandboxesActive.WithLabelValues("stale").Set(9)
	env.metrics.SandboxesActive.WithLabelValues("widgets").Set(7)
	if err := env.sweeper.recalibrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	series := func(ws string) (float64, bool) {
		return metricSample(t, env.metrics, "afhub_sandboxes_active", map[string]string{"workspace": ws})
	}
	if v, ok := series("widgets"); !ok || v != 2 {
		t.Errorf("widgets = (%v, %v); want 2", v, ok)
	}
	if v, ok := series("gadgets"); !ok || v != 1 {
		t.Errorf("gadgets = (%v, %v); want 1", v, ok)
	}
	if _, ok := series("stale"); ok {
		t.Error("the stale label survived")
	}
	if _, ok := series(""); ok {
		t.Error("the placeholder is set although workspaces have active leases")
	}
	if err := env.sweeper.RecoverActive(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v, ok := series(""); !ok || v != 0 {
		t.Errorf("the placeholder = (%v, %v) after recovery ended every lease; want 0 so the metric stays listed", v, ok)
	}
	if _, ok := series("widgets"); ok {
		t.Error("a workspace without active leases keeps a series")
	}
	// A failed read leaves the gauge alone.
	env.metrics.SandboxesActive.WithLabelValues("kept").Set(3)
	testutil.Exec(t, env.db, `ALTER TABLE sandbox_leases RENAME TO away`)
	if err := env.sweeper.recalibrate(context.Background()); err == nil {
		t.Error("recalibrate without the table returned nil")
	}
	if v, ok := series("kept"); !ok || v != 3 {
		t.Error("a failed read changed the gauge")
	}
}
