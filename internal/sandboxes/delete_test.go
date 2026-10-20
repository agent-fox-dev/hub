package sandboxes

import (
	"database/sql"
	"net/http"
	"testing"
	"time"
)

// leaseState is the part of a lease row that ending it changes.
type leaseState struct {
	state, endedAt, updatedAt, endReason string
	hash                                 sql.NullString
}

// readState reads the ending-related columns of a lease row.
func (e *leaseEnv) readState(id string) leaseState {
	e.t.Helper()
	var s leaseState
	var ended, reason sql.NullString
	if err := e.db.QueryRow(`SELECT state, ended_at, updated_at, end_reason, token_hash FROM sandbox_leases WHERE id = ?`, id).
		Scan(&s.state, &ended, &s.updatedAt, &reason, &s.hash); err != nil {
		e.t.Fatalf("reading lease %s: %v", id, err)
	}
	s.endedAt, s.endReason = ended.String, reason.String
	return s
}

// TS-26-49: DELETE ends a live lease, revokes its token and answers 204.
// Requirement: 26-REQ-8.1
func TestDelete_EndsLiveLease_TS_26_49(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	issued := env.mustIssue()
	id, token := issued["id"].(string), issued["token"].(string)
	if _, err := env.store.LookupByToken(token); err != nil {
		t.Fatalf("the token does not work before DELETE: %v", err)
	}

	env.clock.Advance(5 * time.Minute)
	rec := env.do(http.MethodDelete, "/api/v1/workspaces/widgets/sandboxes/"+id, "", apiKeyAuth())
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("DELETE: status %d, body %q; want 204 and an empty body", rec.Code, rec.Body)
	}

	got := jsonObject(t, env.get("/api/v1/workspaces/widgets/sandboxes/"+id, apiKeyAuth()))
	if got["state"] != "destroyed" || got["end_reason"] != "api" || got["ended_at"] != "2030-01-02T03:09:05Z" || got["updated_at"] != got["ended_at"] {
		t.Errorf("after DELETE: state %v, end_reason %v, ended_at %v, updated_at %v; want destroyed/api/03:09:05Z twice",
			got["state"], got["end_reason"], got["ended_at"], got["updated_at"])
	}
	if lease, err := env.store.LookupByToken(token); lease != nil || err != ErrLeaseNotFound {
		t.Errorf("LookupByToken after DELETE = (%v, %v); want (nil, ErrLeaseNotFound)", lease, err)
	}
	if s := env.readState(id); s.hash.Valid {
		t.Errorf("stored token hash after DELETE = %q; want NULL", s.hash.String)
	}
}

// TS-26-50: DELETE on a terminal lease answers 204 and changes nothing.
// Requirement: 26-REQ-8.2
func TestDelete_TerminalLeaseUnchanged_TS_26_50(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{config: bigLeaseConfig()})
	id := env.mustIssue()["id"].(string)
	url := "/api/v1/workspaces/widgets/sandboxes/"
	if rec := env.do(http.MethodDelete, url+id, "", apiKeyAuth()); rec.Code != http.StatusNoContent {
		t.Fatalf("first DELETE: status %d", rec.Code)
	}
	expired := env.insertLeaseAt(50, "widgets", "expired", env.clock.Now())
	failed := env.insertLeaseAt(51, "widgets", "failed", env.clock.Now())

	before := map[string]leaseState{id: env.readState(id), expired: env.readState(expired), failed: env.readState(failed)}
	env.clock.Advance(time.Hour)
	for name, lease := range map[string]string{"destroyed": id, "expired": expired, "failed": failed} {
		if rec := env.do(http.MethodDelete, url+lease, "", apiKeyAuth()); rec.Code != http.StatusNoContent {
			t.Errorf("%s: DELETE status %d; want 204", name, rec.Code)
		}
		if after := env.readState(lease); after != before[lease] {
			t.Errorf("%s: row changed by a DELETE: %+v -> %+v", name, before[lease], after)
		}
	}
}

// TS-26-51: DELETE with a malformed, unknown or foreign id answers 404 and
// changes nothing.
// Requirement: 26-REQ-8.3
func TestDelete_NotFound_TS_26_51(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{})
	env.addWorkspace("other", leaseOwner, "", "active")
	foreign := env.insertLeaseAt(1, "other", "leased", env.clock.Now())
	before := env.readState(foreign)

	for name, id := range map[string]string{"malformed": "xyz", "unknown": leaseID(999), "another workspace's": foreign} {
		rec := env.do(http.MethodDelete, "/api/v1/workspaces/widgets/sandboxes/"+id, "", apiKeyAuth())
		if rec.Code != http.StatusNotFound || message(t, rec) != "sandbox lease not found" {
			t.Errorf("%s: status %d, message %q; want 404 'sandbox lease not found'", name, rec.Code, message(t, rec))
		}
	}
	if after := env.readState(foreign); after != before || after.state != "leased" {
		t.Errorf("the other workspace's lease changed: %+v -> %+v", before, after)
	}
	if lease, err := env.store.LookupByToken(testToken(1)); err != nil || lease.ID != foreign {
		t.Errorf("its token no longer works: (%v, %v)", lease, err)
	}
}

// The DELETE handler is a thin wrapper of EndLease: ending a lease through it
// leaves the same row as calling EndLease directly.
// Requirement: 26-REQ-8.4
func TestDelete_IsEndLease_TS_26_52(t *testing.T) {
	env := newLeaseEnv(t, leaseEnvOptions{config: bigLeaseConfig()})
	viaHandler := env.mustIssue()["id"].(string)
	direct := env.mustIssue()["id"].(string)

	env.clock.Advance(time.Minute)
	if rec := env.do(http.MethodDelete, "/api/v1/workspaces/widgets/sandboxes/"+viaHandler, "", apiKeyAuth()); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: status %d", rec.Code)
	}
	if changed, err := env.store.EndLease(&Lease{ID: direct}, "destroyed", "api"); err != nil || !changed {
		t.Fatalf("EndLease = (%v, %v)", changed, err)
	}
	if a, b := env.readState(viaHandler), env.readState(direct); a != b {
		t.Errorf("DELETE left %+v; EndLease left %+v; want the same", a, b)
	}
}
