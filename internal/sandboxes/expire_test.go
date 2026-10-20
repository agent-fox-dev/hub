package sandboxes

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/hub/internal/audit"
)

// expiryEnv is a backend environment with metrics on and an ender over a set of
// fake boxes.
type expiryEnv struct {
	*backendEnv
	metrics *audit.Metrics
	boxes   *fakeBoxes
	ender   *ender
}

func newExpiryEnv(t *testing.T) *expiryEnv {
	t.Helper()
	m := audit.NewMetrics()
	env := newBackendEnv(t, leaseEnvOptions{metrics: m})
	boxes := &fakeBoxes{}
	return &expiryEnv{backendEnv: env, metrics: m, boxes: boxes, ender: newEnder(env.store, env.sink, boxes, time.Second)}
}

// seedLeased inserts a leased lease and returns it.
func (e *expiryEnv) seedLeased(n int) *Lease {
	e.t.Helper()
	return e.lease(e.insertLeaseAt(n, "widgets", "leased", e.clock.Now()))
}

// seedActive inserts a lease, moves it to active with a box of this id, as the
// gateway does, and returns it.
func (e *expiryEnv) seedActive(n int, boxID string) *Lease {
	e.t.Helper()
	id := e.insertLeaseAt(n, "widgets", "leased", e.clock.Now())
	if changed, err := e.store.MarkActive(id, boxID); err != nil || !changed {
		e.t.Fatalf("MarkActive = (%v, %v)", changed, err)
	}
	e.boxes.add(boxID, map[string]string{"hub.lease": id})
	return e.lease(id)
}

// TS-28-40: The expiry path emits one hub.sandbox.expire event with the
// specified fields, and no hub.sandbox.destroy.
// Requirement: 28-REQ-8.2
func TestEndExpired_EmitsOneEvent_TS_28_40(t *testing.T) {
	env := newExpiryEnv(t)
	leased := env.seedLeased(1)
	active := env.seedActive(2, "box-2")
	env.clock.Advance(2 * time.Hour)

	for i, l := range []*Lease{leased, active} {
		changed, err := env.ender.endExpired(context.Background(), l)
		if err != nil || !changed {
			t.Fatalf("endExpired(%s lease) = (%v, %v); want (true, nil)", l.State, changed, err)
		}
		// Only a lease that becomes destroyed is observed: one that lapses is not.
		if count, _ := leaseSeconds(t, env.metrics); count != uint64(i) {
			t.Errorf("after ending the %s lease the lease_seconds count is %d; want %d", l.State, count, i)
		}
	}

	if got := eventsOf(env.emitter, audit.EventSandboxDestroy); len(got) != 0 {
		t.Errorf("%d hub.sandbox.destroy events; want none for the expiry path: %v", len(got), got)
	}
	events := eventsOf(env.emitter, audit.EventSandboxExpire)
	if len(events) != 2 {
		t.Fatalf("%d hub.sandbox.expire events; want one per lease: %v", len(events), events)
	}
	for i, tc := range []struct {
		lease    *Lease
		metadata map[string]any
	}{
		{leased, map[string]any{"lease_id": leased.ID, "state": "expired"}},
		{active, map[string]any{"lease_id": active.ID, "state": "destroyed", "sandbox_id": "box-2"}},
	} {
		e := events[i]
		if e.ResourceType != "sandbox" || e.ResourceID != tc.lease.ID || e.Action != "expire" ||
			e.ActorType != "system" || e.ActorID != "" || e.Workspace != "widgets" {
			t.Errorf("event %d = %+v; want sandbox/%s/expire by the system with no actor id in workspace widgets", i, e, tc.lease.ID)
		}
		if !reflect.DeepEqual(e.Metadata, tc.metadata) {
			t.Errorf("event %d metadata = %v; want exactly %v", i, e.Metadata, tc.metadata)
		}
	}

	// The leased lease had no box to destroy; the active one's box was destroyed.
	if got := env.boxes.destroyedIDs(); !slices.Equal(got, []string{"box-2"}) {
		t.Errorf("destroyed %v; want only box-2", got)
	}
	for _, tc := range []struct {
		id, state string
	}{{leased.ID, "expired"}, {active.ID, "destroyed"}} {
		if s := env.readState(tc.id); s.state != tc.state || s.endReason != "expired" || s.hash.Valid {
			t.Errorf("lease %s reads %+v; want %s with end_reason expired and no token hash", tc.id, s, tc.state)
		}
	}
}

// TS-28-42: An ending that loses the conditional update emits no event and
// records no metric.
// Requirements: 28-REQ-8.4, 28-REQ-9.2
func TestEndExpired_LostUpdateRecordsNothing_TS_28_42(t *testing.T) {
	for _, state := range []string{"leased", "active"} {
		t.Run(state, func(t *testing.T) {
			env := newExpiryEnv(t)
			var snapshot *Lease
			if state == "leased" {
				snapshot = env.seedLeased(1)
			} else {
				snapshot = env.seedActive(1, "box-1")
			}
			wantGauge := 0.0
			if state == "active" {
				wantGauge = 1
			}
			if got := activeGauge(t, env.metrics, "widgets"); got != wantGauge {
				t.Fatalf("gauge after seeding = %v; want %v", got, wantGauge)
			}

			// DELETE ends the lease before the sweep, which holds the snapshot, gets to it.
			env.clock.Advance(90 * time.Second)
			if rec := env.do(http.MethodDelete, "/api/v1/workspaces/widgets/sandboxes/"+snapshot.ID, "", apiKeyAuth()); rec.Code != http.StatusNoContent {
				t.Fatalf("DELETE answered %d: %s", rec.Code, rec.Body)
			}
			events, gauge := len(env.emitter.recorded()), activeGauge(t, env.metrics, "widgets")
			count, sum := leaseSeconds(t, env.metrics)
			if gauge != 0 || count != 1 || sum != 90 {
				t.Fatalf("after DELETE: gauge %v, histogram (%d, %v); want 0 and (1, 90)", gauge, count, sum)
			}

			changed, err := env.ender.endExpired(context.Background(), snapshot)
			if err != nil || changed {
				t.Fatalf("endExpired on an ended lease = (%v, %v); want (false, nil)", changed, err)
			}
			if got := len(env.emitter.recorded()); got != events {
				t.Errorf("the lost ending emitted %d events", got-events)
			}
			if got := activeGauge(t, env.metrics, "widgets"); got != gauge {
				t.Errorf("the gauge went from %v to %v on a lost ending", gauge, got)
			}
			if c, s := leaseSeconds(t, env.metrics); c != count || s != sum {
				t.Errorf("the histogram went from (%d, %v) to (%d, %v) on a lost ending", count, sum, c, s)
			}
			if s := env.readState(snapshot.ID); s.state != "destroyed" || s.endReason != "api" {
				t.Errorf("the lease reads %s/%s; want the DELETE's destroyed/api", s.state, s.endReason)
			}
		})
	}
}

// TS-28-43: A nil emitter or a failing Emit does not prevent or alter the ending.
// Requirement: 28-REQ-8.5
func TestEndExpired_EmitterFailures_TS_28_43(t *testing.T) {
	env := newExpiryEnv(t)
	failing := &recordingEmitter{err: fmt.Errorf("the audit store is down")}
	working := &recordingEmitter{}

	type result struct {
		changed bool
		err     error
	}
	var results []result
	var logs []int
	for i, em := range []audit.Emitter{working, nil, failing} {
		lease := env.seedLeased(i + 1)
		buf := captureDefaultLog(t)
		ender := newEnder(env.store, em, env.boxes, time.Second)
		changed, err := ender.endExpired(context.Background(), lease)
		results = append(results, result{changed, err})
		logs = append(logs, strings.Count(buf.String(), `"level":"ERROR"`))

		if s := env.readState(lease.ID); s.state != "expired" || s.endReason != "expired" {
			t.Errorf("setup %d: the lease reads %s/%s; want expired/expired", i, s.state, s.endReason)
		}
	}
	for i, r := range results {
		if r != results[0] || r.err != nil || !r.changed {
			t.Errorf("setup %d returned %+v; want the same (true, nil) as a working emitter", i, r)
		}
	}
	if len(working.recorded()) != 1 {
		t.Errorf("the working emitter recorded %d events; want 1", len(working.recorded()))
	}
	if !reflect.DeepEqual(logs, []int{0, 0, 1}) {
		t.Errorf("error records per setup = %v; want [0 0 1]: only the failing Emit is logged", logs)
	}
}

// TS-28-46: afhub_sandboxes_active goes up when Create activates a lease and
// down when DELETE ends an active lease; the lease's duration is observed once.
// Requirement: 28-REQ-9.2
func TestGauge_ActivationAndDelete_TS_28_46(t *testing.T) {
	env := newExpiryEnv(t)
	id := env.issue(`{"profile":"tools"}`)
	if got := activeGauge(t, env.metrics, "widgets"); got != 0 {
		t.Fatalf("gauge after issuing = %v; want 0: a leased lease has no box", got)
	}

	if _, err := env.create(id, sandbox.Spec{}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := activeGauge(t, env.metrics, "widgets"); got != 1 {
		t.Errorf("gauge after Create = %v; want 1", got)
	}
	if got := activeGauge(t, env.metrics, "gadgets"); got != 0 {
		t.Errorf("gauge of another workspace = %v; want 0", got)
	}

	env.clock.Advance(90 * time.Second)
	if rec := env.do(http.MethodDelete, "/api/v1/workspaces/widgets/sandboxes/"+id, "", apiKeyAuth()); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE answered %d: %s", rec.Code, rec.Body)
	}
	if got := activeGauge(t, env.metrics, "widgets"); got != 0 {
		t.Errorf("gauge after DELETE = %v; want 0", got)
	}
	if count, sum := leaseSeconds(t, env.metrics); count != 1 || sum != 90 {
		t.Errorf("lease_seconds = (%d, %v); want one observation of 90 seconds", count, sum)
	}

	// DELETE again changes nothing, so records nothing.
	env.do(http.MethodDelete, "/api/v1/workspaces/widgets/sandboxes/"+id, "", apiKeyAuth())
	if got := activeGauge(t, env.metrics, "widgets"); got != 0 {
		t.Errorf("gauge after a second DELETE = %v; want 0", got)
	}
	if count, _ := leaseSeconds(t, env.metrics); count != 1 {
		t.Errorf("lease_seconds count = %d after a second DELETE; want 1", count)
	}
}

// TS-28-51: A nil *audit.Metrics records nothing and every result is the same.
// Requirement: 28-REQ-9.7
func TestNilMetrics_SameResults_TS_28_51(t *testing.T) {
	scenario := func(t *testing.T, m *audit.Metrics) []string {
		env := newBackendEnv(t, leaseEnvOptions{metrics: m})
		boxes := &fakeBoxes{}
		ender := newEnder(env.store, env.sink, boxes, time.Second)

		a, b, c := env.issue(`{"profile":"tools"}`), env.issue(`{"profile":"tools"}`), env.issue(`{"profile":"tools"}`)
		for _, id := range []string{a, c} {
			if _, err := env.create(id, sandbox.Spec{}); err != nil {
				t.Fatalf("Create: %v", err)
			}
		}
		env.clock.Advance(2 * time.Hour)
		for _, id := range []string{a, b} {
			if _, err := ender.endExpired(context.Background(), env.lease(id)); err != nil {
				t.Fatalf("endExpired: %v", err)
			}
		}
		if rec := env.do(http.MethodDelete, "/api/v1/workspaces/widgets/sandboxes/"+c, "", apiKeyAuth()); rec.Code != http.StatusNoContent {
			t.Fatalf("DELETE answered %d", rec.Code)
		}
		// A sweep (which recalibrates the gauge, prunes and reaps) and a recovery run
		// with the same metrics, or none.
		d := env.issue(`{"profile":"tools"}`)
		env.clock.Advance(6 * time.Minute)
		sweeper := NewSweeper(SweeperConfig{
			DB: env.db, Provider: env.prov, Boxes: boxes, Audit: env.sink, Metrics: m, Now: env.clock.Now, CallTimeout: time.Second,
		})
		if err := sweeper.Sweep(context.Background()); err != nil {
			t.Fatalf("Sweep: %v", err)
		}
		if err := sweeper.RecoverActive(context.Background()); err != nil {
			t.Fatalf("RecoverActive: %v", err)
		}

		names := map[string]string{a: "A", b: "B", c: "C", d: "D"}
		var out []string
		for _, id := range []string{a, b, c, d} {
			l := env.lease(id)
			out = append(out, fmt.Sprintf("lease %s: %s %s", names[id], l.State, ptrText(l.EndReason)))
		}
		for _, e := range env.emitter.recorded() {
			meta := maps.Clone(e.Metadata)
			delete(meta, "sandbox_id")
			delete(meta, "ttl")
			if id, ok := meta["lease_id"].(string); ok {
				meta["lease_id"] = names[id]
			}
			out = append(out, fmt.Sprintf("event %s %s %s %v", e.EventType, e.Action, e.ActorType, meta))
		}
		return out
	}

	withMetrics := scenario(t, audit.NewMetrics())
	withoutMetrics := scenario(t, nil)
	if !slices.Equal(withMetrics, withoutMetrics) {
		t.Errorf("with a nil *audit.Metrics the results are\n%s\nwant the same as with metrics\n%s",
			strings.Join(withoutMetrics, "\n"), strings.Join(withMetrics, "\n"))
	}
	if len(withMetrics) < 8 {
		t.Errorf("the scenario produced only %d results: %v", len(withMetrics), withMetrics)
	}
}

// The destroy-with-reason ending (shutdown and restart) destroys the box, ends
// the lease with the reason and emits hub.sandbox.destroy by the system.
// Requirements: 28-REQ-8.3, 28-REQ-9.2, 28-REQ-9.6
func TestEndDestroyed_WithReason(t *testing.T) {
	for _, reason := range []string{"shutdown", "restart"} {
		t.Run(reason, func(t *testing.T) {
			env := newExpiryEnv(t)
			active := env.seedActive(1, "box-1")
			env.clock.Advance(10 * time.Minute)

			changed, err := env.ender.endDestroyed(context.Background(), active, reason)
			if err != nil || !changed {
				t.Fatalf("endDestroyed = (%v, %v); want (true, nil)", changed, err)
			}
			if s := env.readState(active.ID); s.state != "destroyed" || s.endReason != reason || s.hash.Valid {
				t.Errorf("the lease reads %+v; want destroyed with end_reason %s and no token hash", s, reason)
			}
			if got := env.boxes.destroyedIDs(); !slices.Equal(got, []string{"box-1"}) {
				t.Errorf("destroyed %v; want box-1", got)
			}
			if got := eventsOf(env.emitter, audit.EventSandboxExpire); len(got) != 0 {
				t.Errorf("%d hub.sandbox.expire events; want none", len(got))
			}
			events := eventsOf(env.emitter, audit.EventSandboxDestroy)
			if len(events) != 1 {
				t.Fatalf("%d hub.sandbox.destroy events; want 1", len(events))
			}
			e := events[0]
			want := map[string]any{"lease_id": active.ID, "sandbox_id": "box-1", "reason": reason}
			if e.ResourceType != "sandbox" || e.ResourceID != active.ID || e.Action != "destroy" ||
				e.ActorType != "system" || e.ActorID != "" || e.Workspace != "widgets" || !reflect.DeepEqual(e.Metadata, want) {
				t.Errorf("event = %+v; want sandbox/%s/destroy by the system with metadata %v", e, active.ID, want)
			}
			if got := activeGauge(t, env.metrics, "widgets"); got != 0 {
				t.Errorf("gauge = %v; want 0", got)
			}
			if count, sum := leaseSeconds(t, env.metrics); count != 1 || sum != 600 {
				t.Errorf("lease_seconds = (%d, %v); want one observation of 600 seconds", count, sum)
			}
		})
	}
}

// A Destroy that fails does not stop the ending: the lease is ended, its token
// is revoked and the failure is logged with the lease, the box and the error
// text with the URL userinfo removed.
// Requirement: 28-REQ-4.5
func TestEndExpired_DestroyFailureStillEnds(t *testing.T) {
	env := newExpiryEnv(t)
	active := env.seedActive(1, "box-1")
	env.boxes.setDestroyErr(fmt.Errorf("GET https://user:pw@host/x failed"))
	logs := captureDefaultLog(t)

	changed, err := env.ender.endExpired(context.Background(), active)
	if err != nil || !changed {
		t.Fatalf("endExpired = (%v, %v); want (true, nil)", changed, err)
	}
	if s := env.readState(active.ID); s.state != "destroyed" || s.endReason != "expired" || s.hash.Valid {
		t.Errorf("the lease reads %+v; want destroyed/expired with no token hash", s)
	}
	out := logs.String()
	for _, want := range []string{active.ID, "box-1", "https://host/x"} {
		if !strings.Contains(out, want) {
			t.Errorf("the log does not contain %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "user:pw") {
		t.Errorf("the log contains URL userinfo:\n%s", out)
	}
}

// ptrText is the text a pointer to a string points to, or "<nil>".
func ptrText(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// An ending whose context has ended does not complete: hub is stopping, the box may
// still be there, and the lease stays active for the next start to recover. No
// event is emitted and no metric recorded.
// Requirement: 28-REQ-7.5
func TestEndExpired_EndedContextLeavesTheLeaseActive(t *testing.T) {
	env := newExpiryEnv(t)
	active := env.seedActive(1, "box-1")
	events, gauge := len(env.emitter.recorded()), activeGauge(t, env.metrics, "widgets")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	changed, err := env.ender.endExpired(ctx, active)

	if changed || err == nil {
		t.Errorf("endExpired with an ended context = (%v, %v); want (false, the context's error)", changed, err)
	}
	if s := env.readState(active.ID); s.state != "active" || !s.hash.Valid {
		t.Errorf("the lease reads %+v; want it left active", s)
	}
	if len(env.emitter.recorded()) != events || activeGauge(t, env.metrics, "widgets") != gauge {
		t.Error("an ending that did not complete emitted an event or changed the gauge")
	}
}
