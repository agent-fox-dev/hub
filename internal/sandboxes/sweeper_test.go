package sandboxes

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// sweepEnv is a backend environment with a sweeper over fake boxes and metrics.
type sweepEnv struct {
	*backendEnv
	metrics *audit.Metrics
	boxes   *fakeBoxes
	sweeper *Sweeper
}

func newSweepEnv(t *testing.T) *sweepEnv {
	t.Helper()
	m := audit.NewMetrics()
	env := newBackendEnv(t, leaseEnvOptions{metrics: m, config: bigLeaseConfig()})
	boxes := &fakeBoxes{}
	sweeper := NewSweeper(SweeperConfig{
		DB: env.db, Provider: env.prov, Boxes: boxes, Audit: env.sink, Metrics: m,
		Now: env.clock.Now, CallTimeout: time.Second,
	})
	return &sweepEnv{backendEnv: env, metrics: m, boxes: boxes, sweeper: sweeper}
}

// seedFields are the times and the box of a lease the sweep tests seed.
type seedFields struct {
	workspace string
	created   time.Time
	// expires is the lease's expires_at; an hour after created when zero.
	expires time.Time
	// leaseExpires is lease_expires_at; NULL when nil.
	leaseExpires *time.Time
	// ended is ended_at of a terminal lease; NULL when nil. updated is updated_at;
	// created when zero.
	ended   *time.Time
	updated time.Time
	// boxID is the sandbox_id; none when empty. The box exists in the fake boxes.
	boxID string
}

func timePtr(t time.Time) *time.Time { return &t }

// seed inserts a lease straight through SQL in the given state and returns its
// id. A live lease keeps its token hash, a terminal one has none.
func (e *sweepEnv) seed(n int, state string, f seedFields) string {
	e.t.Helper()
	if f.workspace == "" {
		f.workspace = "widgets"
	}
	if f.created.IsZero() {
		f.created = e.clock.Now()
	}
	if f.expires.IsZero() {
		f.expires = f.created.Add(time.Hour)
	}
	if f.updated.IsZero() {
		f.updated = f.created
	}
	id := e.insertLeaseAt(n, f.workspace, state, f.created)

	var leaseExpires, ended, boxID, reason any
	if f.leaseExpires != nil {
		leaseExpires = apikit.FormatUTC(*f.leaseExpires)
	}
	if f.ended != nil {
		ended = apikit.FormatUTC(*f.ended)
		reason = "api"
	}
	if f.boxID != "" {
		boxID = f.boxID
		e.boxes.add(f.boxID, map[string]string{"hub.lease": id})
	}
	var hash any = HashToken(testToken(n))
	if state != leaseLeased && state != leaseActive {
		hash = nil
	}
	if _, err := e.db.Exec(`UPDATE sandbox_leases SET expires_at = ?, lease_expires_at = ?, ended_at = ?, end_reason = ?,
		updated_at = ?, sandbox_id = ?, token_hash = ? WHERE id = ?`,
		apikit.FormatUTC(f.expires), leaseExpires, ended, reason, apikit.FormatUTC(f.updated), boxID, hash, id); err != nil {
		e.t.Fatalf("seeding lease %d: %v", n, err)
	}
	return id
}

// sweep runs one sweep and fails the test on its error.
func (e *sweepEnv) sweep() {
	e.t.Helper()
	if err := e.sweeper.Sweep(context.Background()); err != nil {
		e.t.Fatalf("Sweep returned error: %v", err)
	}
}

// tokenWorks reports whether the lease authenticator accepts the token of seed n.
func (e *sweepEnv) tokenWorks(n int) bool {
	_, ok := NewLeaseAuthenticator(e.store, e.clock.Now)(context.Background(), testToken(n))
	return ok
}

// TS-28-18: A sweep lapses a leased lease past lease_expires_at, or past
// expires_at when the column is NULL, without calling the provider.
// Requirement: 28-REQ-4.3
func TestSweep_LapsesUnusedLeases_TS_28_18(t *testing.T) {
	env := newSweepEnv(t)
	t0 := env.clock.Now()
	// 1: the deadline for a Create has passed. 2: a row from before the column,
	// whose expires_at has passed. 3: the deadline is ahead. 4: the deadline is
	// exactly now.
	a := env.seed(1, "leased", seedFields{leaseExpires: timePtr(t0.Add(5 * time.Minute))})
	b := env.seed(2, "leased", seedFields{created: t0.Add(-2 * time.Hour)})
	c := env.seed(3, "leased", seedFields{leaseExpires: timePtr(t0.Add(30 * time.Minute))})
	d := env.seed(4, "leased", seedFields{leaseExpires: timePtr(t0.Add(10 * time.Minute))})
	env.clock.Advance(10 * time.Minute)

	env.sweep()

	for _, id := range []string{a, b, d} {
		if s := env.readState(id); s.state != "expired" || s.endReason != "expired" || s.hash.Valid {
			t.Errorf("lease %s reads %+v; want expired/expired with no token hash", id, s)
		}
	}
	if s := env.readState(c); s.state != "leased" || !s.hash.Valid {
		t.Errorf("the lease with a deadline ahead reads %+v; want it left leased", s)
	}
	events := eventsOf(env.emitter, audit.EventSandboxExpire)
	if len(events) != 3 {
		t.Fatalf("%d hub.sandbox.expire events; want one per lapsed lease: %v", len(events), events)
	}
	for _, e := range events {
		if e.Metadata["state"] != "expired" || e.ActorType != "system" {
			t.Errorf("event %+v; want state expired by the system", e)
		}
	}
	if got := env.boxes.destroyedIDs(); len(got) != 0 {
		t.Errorf("the provider destroyed %v; want no call for leases that never had a box", got)
	}
	for n, want := range map[int]bool{1: false, 2: false, 3: true, 4: false} {
		if got := env.tokenWorks(n); got != want {
			t.Errorf("token %d works = %v; want %v", n, got, want)
		}
	}
}

// TS-28-19: A sweep destroys the box of an active lease past expires_at, the
// moment itself included, and ends the lease as destroyed with end_reason
// expired.
// Requirement: 28-REQ-4.4
func TestSweep_DestroysOverdueBoxes_TS_28_19(t *testing.T) {
	env := newSweepEnv(t)
	t0 := env.clock.Now()
	overdue := env.seed(1, "active", seedFields{boxID: "box-1", expires: t0.Add(30 * time.Minute)})
	fresh := env.seed(2, "active", seedFields{boxID: "box-2", expires: t0.Add(3 * time.Hour)})
	other := env.seed(3, "active", seedFields{workspace: "gadgets", boxID: "box-3", expires: t0.Add(2 * time.Hour)})
	atNow := env.seed(4, "active", seedFields{boxID: "box-4", expires: t0.Add(time.Hour)})
	justAhead := env.seed(5, "active", seedFields{boxID: "box-5", expires: t0.Add(time.Hour + time.Second)})
	env.clock.Advance(time.Hour)

	env.sweep()

	var left []string
	boxes, _ := env.boxes.List(context.Background())
	for _, b := range boxes {
		left = append(left, b.ID)
	}
	if want := []string{"box-2", "box-3", "box-5"}; !slices.Equal(left, want) {
		t.Errorf("the provider lists %v; want %v: the boxes of the leases with time left", left, want)
	}
	for _, id := range []string{overdue, atNow} {
		if s := env.readState(id); s.state != "destroyed" || s.endReason != "expired" || s.hash.Valid {
			t.Errorf("overdue lease %s reads %+v; want destroyed/expired with no token hash", id, s)
		}
	}
	for _, id := range []string{fresh, other, justAhead} {
		if s := env.readState(id); s.state != "active" || !s.hash.Valid {
			t.Errorf("lease %s with time left reads %+v; want it left active", id, s)
		}
	}
	events := eventsOf(env.emitter, audit.EventSandboxExpire)
	if len(events) != 2 || events[0].ResourceID != overdue || events[1].ResourceID != atNow ||
		events[0].Metadata["state"] != "destroyed" || events[0].Metadata["sandbox_id"] != "box-1" ||
		events[1].Metadata["sandbox_id"] != "box-4" {
		t.Errorf("expire events = %v; want one each for box-1 and box-4 with state destroyed", events)
	}
	if got := eventsOf(env.emitter, audit.EventSandboxDestroy); len(got) != 0 {
		t.Errorf("%d hub.sandbox.destroy events; want none", len(got))
	}
	for n, want := range map[int]bool{1: false, 2: true, 4: false, 5: true} {
		if got := env.tokenWorks(n); got != want {
			t.Errorf("token %d works = %v; want %v", n, got, want)
		}
	}
}

// TS-28-20: A failing Destroy still ends an overdue active lease and revokes its
// token, and logs the lease, the box and the error without URL userinfo.
// Requirement: 28-REQ-4.5
func TestSweep_FailedDestroyStillEnds_TS_28_20(t *testing.T) {
	env := newSweepEnv(t)
	overdue := env.seed(1, "active", seedFields{boxID: "box-1", expires: env.clock.Now().Add(time.Minute)})
	env.clock.Advance(time.Hour)
	env.boxes.setDestroyErr(fmt.Errorf("GET https://user:pw@host/x failed"))
	logs := captureDefaultLog(t)

	env.sweep()

	if s := env.readState(overdue); s.state != "destroyed" || s.endReason != "expired" || s.hash.Valid {
		t.Errorf("the lease reads %+v; want destroyed/expired with no token hash", s)
	}
	if env.tokenWorks(1) {
		t.Error("the token of the overdue lease still works")
	}
	var found map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec["lease_id"] == overdue {
			found = rec
		}
	}
	if found == nil {
		t.Fatalf("no log record names the lease:\n%s", logs)
	}
	if found["sandbox_id"] != "box-1" || !strings.Contains(fmt.Sprint(found["error"]), "https://host/x") ||
		strings.Contains(logs.String(), "user:pw") {
		t.Errorf("record %v; want sandbox_id box-1 and the error without the userinfo", found)
	}
}

// getLeaseStatus is the status of GET one for a lease of widgets.
func (e *sweepEnv) getLeaseStatus(id string) int {
	e.t.Helper()
	return e.get("/api/v1/workspaces/widgets/sandboxes/"+id, apiKeyAuth()).Code
}

// listed reports whether the all=true list of the workspace and the admin list
// both hold the lease.
func (e *sweepEnv) listed(id string) (inWorkspace, inAdmin bool) {
	e.t.Helper()
	has := func(path string, auth *apikit.AuthInfo) bool {
		for _, l := range leaseList(e.t, e.get(path, auth)) {
			if l["id"] == id {
				return true
			}
		}
		return false
	}
	return has("/api/v1/workspaces/widgets/sandboxes?all=true", apiKeyAuth()), has("/api/v1/sandboxes?all=true", adminAuth())
}

// TS-28-30: A terminal lease that ended 24 hours or less ago stays readable
// after a sweep, through GET one, ?all=true and the admin list.
// Requirement: 28-REQ-6.1
func TestSweep_KeepsRecentTerminalLeases_TS_28_30(t *testing.T) {
	env := newSweepEnv(t)
	now := env.clock.Now()
	recent := map[string]string{
		"destroyed 23h ago":          env.seed(1, "destroyed", seedFields{created: now.Add(-30 * time.Hour), ended: timePtr(now.Add(-23 * time.Hour))}),
		"expired exactly 24h ago":    env.seed(2, "expired", seedFields{created: now.Add(-30 * time.Hour), ended: timePtr(now.Add(-24 * time.Hour))}),
		"failed 1s inside 24h":       env.seed(3, "failed", seedFields{created: now.Add(-30 * time.Hour), ended: timePtr(now.Add(-24*time.Hour + time.Second))}),
		"no ended_at, updated 23h":   env.seed(4, "destroyed", seedFields{created: now.Add(-30 * time.Hour), updated: now.Add(-23 * time.Hour)}),
		"no ended_at, updated 24h":   env.seed(5, "expired", seedFields{created: now.Add(-30 * time.Hour), updated: now.Add(-24 * time.Hour)}),
		"ended now (by this sweep)":  env.seed(6, "destroyed", seedFields{ended: timePtr(now)}),
		"ended in the future (skew)": env.seed(7, "destroyed", seedFields{ended: timePtr(now.Add(time.Minute))}),
	}

	env.sweep()

	for name, id := range recent {
		if got := env.getLeaseStatus(id); got != http.StatusOK {
			t.Errorf("%s: GET one answered %d; want 200", name, got)
		}
		if inWorkspace, inAdmin := env.listed(id); !inWorkspace || !inAdmin {
			t.Errorf("%s: listed in the workspace list %v and the admin list %v; want both", name, inWorkspace, inAdmin)
		}
	}
}

// TS-28-31: A sweep deletes a terminal lease that ended more than 24 hours ago.
// Requirement: 28-REQ-6.2
func TestSweep_PrunesOldTerminalLeases_TS_28_31(t *testing.T) {
	env := newSweepEnv(t)
	now := env.clock.Now()
	created := now.Add(-40 * time.Hour)
	old := map[string]string{
		"destroyed":            env.seed(1, "destroyed", seedFields{created: created, ended: timePtr(now.Add(-24*time.Hour - time.Second))}),
		"expired":              env.seed(2, "expired", seedFields{created: created, ended: timePtr(now.Add(-24*time.Hour - time.Second))}),
		"failed":               env.seed(3, "failed", seedFields{created: created, ended: timePtr(now.Add(-48 * time.Hour))}),
		"no ended_at, updated": env.seed(4, "destroyed", seedFields{created: created, updated: now.Add(-25 * time.Hour)}),
	}
	kept := env.seed(5, "destroyed", seedFields{created: created, ended: timePtr(now.Add(-time.Hour))})

	env.sweep()

	for name, id := range old {
		if got := env.getLeaseStatus(id); got != http.StatusNotFound {
			t.Errorf("%s: GET one answered %d; want 404", name, got)
		}
		if inWorkspace, inAdmin := env.listed(id); inWorkspace || inAdmin {
			t.Errorf("%s: still listed in the workspace list %v and the admin list %v", name, inWorkspace, inAdmin)
		}
	}
	if got := env.getLeaseStatus(kept); got != http.StatusOK {
		t.Errorf("a lease that ended an hour ago: GET one answered %d; want 200", got)
	}
}

// TS-28-32: A sweep never deletes a lease in leased or active state, whatever its
// age: it may end it, but the row stays.
// Requirement: 28-REQ-6.3
func TestSweep_NeverDeletesLiveLeases_TS_28_32(t *testing.T) {
	for seed := range uint64(20) {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			r := rand.New(rand.NewPCG(28, seed))
			env := newSweepEnv(t)
			now := env.clock.Now()
			ago := func() time.Duration { return time.Duration(r.IntN(24*365*3)) * time.Hour }
			var ids []string
			for n := 1; n <= 12; n++ {
				created := now.Add(-ago())
				f := seedFields{created: created, updated: created.Add(time.Duration(r.IntN(100)) * time.Minute)}
				switch r.IntN(3) {
				case 0: // ancient, long past every deadline
					f.expires = created.Add(time.Hour)
				case 1: // a long life still ahead
					f.expires = now.Add(time.Duration(1+r.IntN(1000)) * time.Hour)
				default:
					f.expires = created.Add(time.Duration(r.IntN(24*365)) * time.Hour)
				}
				if r.IntN(2) == 0 {
					f.leaseExpires = timePtr(created.Add(time.Duration(r.IntN(600)) * time.Minute))
				}
				state := "leased"
				if r.IntN(2) == 0 {
					state = "active"
					f.boxID = fmt.Sprintf("box-%d", n)
				}
				ids = append(ids, env.seed(n, state, f))
			}

			env.sweep()

			for _, id := range ids {
				var n int
				if err := env.db.QueryRow(`SELECT COUNT(*) FROM sandbox_leases WHERE id = ?`, id).Scan(&n); err != nil || n != 1 {
					t.Errorf("lease %s: %d rows after a sweep (%v); want the row kept", id, n, err)
				}
			}
		})
	}
}

// A sweep recalibrates the gauge from the table: stale labels go and every
// workspace with active leases gets its count.
// Requirement: 28-REQ-9.3
func TestSweep_RecalibratesGauge(t *testing.T) {
	env := newSweepEnv(t)
	now := env.clock.Now()
	env.seed(1, "active", seedFields{workspace: "widgets", boxID: "b1", expires: now.Add(time.Hour)})
	env.seed(2, "active", seedFields{workspace: "widgets", boxID: "b2", expires: now.Add(time.Hour)})
	env.seed(3, "active", seedFields{workspace: "gadgets", boxID: "b3", expires: now.Add(time.Hour)})
	env.seed(4, "leased", seedFields{workspace: "gadgets"})
	env.metrics.SandboxesActive.WithLabelValues("stale").Set(9)
	env.metrics.SandboxesActive.WithLabelValues("widgets").Set(5)

	env.sweep()

	for ws, want := range map[string]float64{"widgets": 2, "gadgets": 1} {
		if got, ok := metricSample(t, env.metrics, "afhub_sandboxes_active", map[string]string{"workspace": ws}); !ok || got != want {
			t.Errorf("gauge of %s = (%v, %v); want %v", ws, got, ok, want)
		}
	}
	if _, ok := metricSample(t, env.metrics, "afhub_sandboxes_active", map[string]string{"workspace": "stale"}); ok {
		t.Error("the stale workspace label survived the recalibration")
	}
}

// A failing step is logged and returned, and the later steps still run.
// Requirement: 28-REQ-4.2
func TestSweep_StepFailureDoesNotStopTheRest(t *testing.T) {
	env := newSweepEnv(t)
	env.prov.setReap(nil, fmt.Errorf("reap failed at https://user:pw@host/x"))
	t0 := env.clock.Now()
	lapsed := env.seed(1, "leased", seedFields{leaseExpires: timePtr(t0.Add(time.Minute))})
	old := env.seed(2, "destroyed", seedFields{created: t0.Add(-72 * time.Hour), ended: timePtr(t0.Add(-48 * time.Hour))})
	env.clock.Advance(time.Hour)
	logs := captureDefaultLog(t)

	err := env.sweeper.Sweep(context.Background())

	if err == nil || !strings.Contains(err.Error(), "reap failed") || strings.Contains(err.Error(), "user:pw") {
		t.Errorf("Sweep returned %v; want the Reap failure, without URL userinfo", err)
	}
	if s := env.readState(lapsed); s.state != "expired" {
		t.Errorf("the lapsed lease reads %s; want expired although Reap failed", s.state)
	}
	if got := env.getLeaseStatus(old); got != http.StatusNotFound {
		t.Errorf("the old lease: GET one answered %d; want 404 although Reap failed", got)
	}
	if !strings.Contains(logs.String(), `"level":"ERROR"`) || !strings.Contains(logs.String(), "reap failed") {
		t.Errorf("the failure was not logged:\n%s", logs)
	}
}
