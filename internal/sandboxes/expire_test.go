package sandboxes

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// seedActive inserts a lease and moves it to active with a box, as the gateway
// does, so that the gauge counts it.
func (e *sweepEnv) seedActive(n int, boxID string) *Lease {
	e.t.Helper()
	id := e.insertLeaseAt(n, "widgets", "leased", e.clock.Now())
	if changed, err := e.store.MarkActive(id, boxID); err != nil || !changed {
		e.t.Fatalf("MarkActive = (%v, %v)", changed, err)
	}
	e.boxes.add(boxID, map[string]string{"hub.lease": id})
	return e.lease(id)
}

// The expiry path lapses a leased lease and destroys the box of an active one,
// both with the reason expired, the token revoked and one hub.sandbox.expire by
// the system naming the box only when there was one; another state is an error.
func TestEndExpired_EndsLiveLeasesWithOneEvent(t *testing.T) {
	env := newSweepEnv(t)
	leased := env.lease(env.seed(1, "leased", seedFields{}))
	active := env.seedActive(2, "box-2")
	env.clock.Advance(2 * time.Hour)
	for _, l := range []*Lease{leased, active} {
		if changed, err := env.ender.endExpired(context.Background(), l); err != nil || !changed {
			t.Fatalf("endExpired(%s) = (%v, %v)", l.State, changed, err)
		}
	}
	if got := env.boxes.destroyedIDs(); !slices.Equal(got, []string{"box-2"}) {
		t.Errorf("destroyed %v; want only box-2", got)
	}
	for id, state := range map[string]string{leased.ID: "expired", active.ID: "destroyed"} {
		if s := env.readState(id); s.state != state || s.endReason != "expired" || s.hash.Valid {
			t.Errorf("lease %s reads %+v; want %s/expired without a hash", id, s, state)
		}
	}
	if n := len(env.eventsOf(audit.EventSandboxDestroy)); n != 0 {
		t.Errorf("%d destroy events; want none", n)
	}
	events := env.eventsOf(audit.EventSandboxExpire)
	if len(events) != 2 {
		t.Fatalf("expire events = %+v; want one per lease", events)
	}
	for i, want := range []map[string]any{{"lease_id": leased.ID, "state": "expired"}, {"lease_id": active.ID, "state": "destroyed", "sandbox_id": "box-2"}} {
		if e := events[i]; e.ResourceType != "sandbox" || e.Action != "expire" || e.ActorType != "system" || e.ActorID != "" || e.Workspace != "widgets" || !reflect.DeepEqual(e.Metadata, want) {
			t.Errorf("event %d = %+v; want metadata %v by the system", i, e, want)
		}
	}
	if count, _ := leaseSeconds(t, env.metrics); count != 1 {
		t.Errorf("lease_seconds observed %d leases; want only the destroyed one", count)
	}
	if _, err := env.ender.endExpired(context.Background(), env.lease(leased.ID)); err == nil {
		t.Error("ending an expired lease by time returned no error")
	}
}

// The destroy-with-reason ending (shutdown, restart) destroys the box, ends the
// lease with the reason and emits one hub.sandbox.destroy by the system.
func TestEndDestroyed_WithReason(t *testing.T) {
	env := newSweepEnv(t)
	active := env.seedActive(1, "box-1")
	env.clock.Advance(10 * time.Minute)
	if changed, err := env.ender.endDestroyed(context.Background(), active, "restart"); err != nil || !changed {
		t.Fatalf("endDestroyed = (%v, %v)", changed, err)
	}
	if s := env.readState(active.ID); s.state != "destroyed" || s.endReason != "restart" || s.hash.Valid || !slices.Equal(env.boxes.destroyedIDs(), []string{"box-1"}) {
		t.Errorf("lease %+v, destroyed %v", s, env.boxes.destroyedIDs())
	}
	events := env.eventsOf(audit.EventSandboxDestroy)
	want := map[string]any{"lease_id": active.ID, "sandbox_id": "box-1", "reason": "restart"}
	if len(events) != 1 || events[0].ActorType != "system" || events[0].ActorID != "" || !reflect.DeepEqual(events[0].Metadata, want) || len(env.eventsOf(audit.EventSandboxExpire)) != 0 {
		t.Errorf("events = %+v; want one destroy by the system with %v", events, want)
	}
	if count, sum := leaseSeconds(t, env.metrics); count != 1 || sum != 600 || activeGauge(t, env.metrics, "widgets") != 0 {
		t.Errorf("lease_seconds (%d, %v), gauge %v; want one observation of 600 and 0", count, sum, activeGauge(t, env.metrics, "widgets"))
	}
}

// A lost conditional update emits and records nothing; a failing Destroy still
// ends the lease and is logged without URL userinfo; an ended context leaves the
// lease active for the next start; a nil or failing emitter changes nothing.
func TestEndExpired_LostUpdateDestroyFailureAndEndedContext(t *testing.T) {
	env := newSweepEnv(t)
	snapshot := env.seedActive(1, "box-1")
	env.clock.Advance(90 * time.Second)
	if env.delete(snapshot.ID).Code != 204 {
		t.Fatal("DELETE failed")
	}
	events, count := len(env.emitter.recorded()), func() uint64 { c, _ := leaseSeconds(t, env.metrics); return c }()
	if changed, err := env.ender.endExpired(context.Background(), snapshot); err != nil || changed {
		t.Fatalf("endExpired on an ended lease = (%v, %v); want (false, nil)", changed, err)
	}
	if len(env.emitter.recorded()) != events || activeGauge(t, env.metrics, "widgets") != 0 || func() uint64 { c, _ := leaseSeconds(t, env.metrics); return c }() != count {
		t.Error("the lost ending emitted an event or recorded a metric")
	}
	if s := env.readState(snapshot.ID); s.endReason != "api" {
		t.Errorf("the lease reads %+v; want DELETE's api kept", s)
	}

	logs := captureDefaultLog(t)
	failing := env.seedActive(2, "box-2")
	env.boxes.setDestroyErr(errors.New("GET https://user:pw@host/x failed"))
	if changed, err := env.ender.endExpired(context.Background(), failing); err != nil || !changed {
		t.Fatalf("endExpired with a failing Destroy = (%v, %v)", changed, err)
	}
	if s := env.readState(failing.ID); s.state != "destroyed" || s.hash.Valid {
		t.Errorf("the lease reads %+v; want it ended all the same", s)
	}
	if out := logs.String(); !containsAll(out, []string{failing.ID, "box-2", "https://host/x"}) || strings.Contains(out, "user:pw") {
		t.Errorf("the log:\n%s", out)
	}
	env.boxes.setDestroyErr(nil)

	stays := env.seedActive(3, "box-3")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if changed, err := env.ender.endExpired(ctx, stays); changed || err == nil {
		t.Errorf("endExpired with an ended context = (%v, %v)", changed, err)
	}
	if _, err := env.ender.endDestroyed(ctx, stays, "shutdown"); err == nil {
		t.Error("endDestroyed with an ended context returned nil")
	}
	if s := env.readState(stays.ID); s.state != "active" || !s.hash.Valid {
		t.Errorf("the lease reads %+v; want it left active", s)
	}

	for i, em := range []audit.Emitter{nil, &recordingEmitter{err: errors.New("the audit store is down")}} {
		lease := env.lease(env.seed(10+i, "leased", seedFields{}))
		env.clock.Advance(2 * time.Hour)
		if changed, err := newEnder(env.store, em, env.boxes, time.Second).endExpired(context.Background(), lease); err != nil || !changed || env.readState(lease.ID).state != "expired" {
			t.Errorf("emitter %d: endExpired = (%v, %v)", i, changed, err)
		}
	}
	if n := strings.Count(logs.String(), "failed to emit"); n != 1 {
		t.Errorf("%d failed emits logged; want only the failing emitter's", n)
	}
}
