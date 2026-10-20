package sandboxes

import (
	"database/sql"
	"testing"
	"time"

	"github.com/txsvc/apikit"
)

// leaseRow is the columns the transitions write.
type leaseRow struct {
	state, sandboxID, errText, endedAt, endReason string
	updatedAt                                     string
	hash                                          sql.NullString
}

func readLeaseRow(t *testing.T, db *sql.DB, id string) leaseRow {
	t.Helper()
	var r leaseRow
	var box, errText, ended, reason sql.NullString
	if err := db.QueryRow(`SELECT state, sandbox_id, error, ended_at, end_reason, updated_at, token_hash FROM sandbox_leases WHERE id = ?`, id).
		Scan(&r.state, &box, &errText, &ended, &reason, &r.updatedAt, &r.hash); err != nil {
		t.Fatalf("reading lease %s: %v", id, err)
	}
	r.sandboxID, r.errText, r.endedAt, r.endReason = box.String, errText.String, ended.String, reason.String
	return r
}

// TS-27-38: Every state change is a conditional UPDATE on the state hub expects
// to find; ended_at is set on entering failed or destroyed, the token hash is
// cleared, and destroyed and failed are terminal.
// Requirement: 27-REQ-6.4
func TestLeaseTransitions_Conditional_TS_27_38(t *testing.T) {
	now := time.Date(2030, 1, 2, 5, 0, 0, 0, time.UTC)
	stamp := apikit.FormatUTC(now)
	store, db := newLeaseStore(t, now)

	mustChange := func(what string, changed bool, err error) {
		t.Helper()
		if err != nil || !changed {
			t.Fatalf("%s = (%v, %v); want (true, nil)", what, changed, err)
		}
	}
	mustNotChange := func(what string, changed bool, err error) {
		t.Helper()
		if err != nil || changed {
			t.Errorf("%s = (%v, %v); want (false, nil): zero rows", what, changed, err)
		}
	}

	t.Run("leased to active records the box and keeps the token", func(t *testing.T) {
		id := leaseID(1)
		insertRawLease(t, db, id, "widgets", "leased", HashToken(testToken(1)))
		changed, err := store.MarkActive(id, "box-1")
		mustChange("MarkActive", changed, err)
		if r := readLeaseRow(t, db, id); r.state != "active" || r.sandboxID != "box-1" || r.endedAt != "" || !r.hash.Valid || r.updatedAt != stamp {
			t.Errorf("after MarkActive: %+v; want active with box-1, no ended_at, the token hash kept and updated_at %s", r, stamp)
		}
		if l, err := store.LookupByToken(testToken(1)); err != nil || l.ID != id {
			t.Errorf("LookupByToken after MarkActive = (%v, %v); want the lease", l, err)
		}
		again, err := store.MarkActive(id, "box-2")
		mustNotChange("MarkActive on an active lease", again, err)
		if r := readLeaseRow(t, db, id); r.sandboxID != "box-1" {
			t.Errorf("a second MarkActive changed sandbox_id to %q", r.sandboxID)
		}
		wrong, err := store.FailLease(id, "late failure")
		mustNotChange("FailLease on an active lease", wrong, err)
	})

	t.Run("leased to failed records the message, ends the lease and revokes the token", func(t *testing.T) {
		id := leaseID(2)
		insertRawLease(t, db, id, "widgets", "leased", HashToken(testToken(2)))
		changed, err := store.FailLease(id, "secret_missing: GIT_PAT")
		mustChange("FailLease", changed, err)
		r := readLeaseRow(t, db, id)
		if r.state != "failed" || r.errText != "secret_missing: GIT_PAT" || r.endedAt != stamp || r.updatedAt != stamp || r.endReason != "" || r.hash.Valid {
			t.Errorf("after FailLease: %+v; want failed, the message, ended_at %s, no end_reason, the token hash cleared", r, stamp)
		}
		if l, err := store.LookupByToken(testToken(2)); l != nil || err != ErrLeaseNotFound {
			t.Errorf("LookupByToken of a failed lease = (%v, %v); want (nil, ErrLeaseNotFound)", l, err)
		}
	})

	t.Run("leased to destroyed and active to destroyed record the reason", func(t *testing.T) {
		for _, c := range []struct {
			n            int
			from, reason string
		}{{3, "leased", "api"}, {4, "active", "client"}} {
			id := leaseID(c.n)
			insertRawLease(t, db, id, "widgets", c.from, HashToken(testToken(c.n)))
			changed, err := store.DestroyLease(id, c.from, c.reason)
			mustChange("DestroyLease from "+c.from, changed, err)
			r := readLeaseRow(t, db, id)
			if r.state != "destroyed" || r.endReason != c.reason || r.endedAt != stamp || r.errText != "" || r.hash.Valid {
				t.Errorf("after DestroyLease from %s: %+v; want destroyed with %q, ended_at %s, no error, the token hash cleared", c.from, r, c.reason, stamp)
			}
		}
	})

	t.Run("a wrong expected state updates zero rows", func(t *testing.T) {
		leased, active := leaseID(5), leaseID(6)
		insertRawLease(t, db, leased, "widgets", "leased", HashToken(testToken(5)))
		insertRawLease(t, db, active, "widgets", "active", HashToken(testToken(6)))
		changed, err := store.DestroyLease(leased, "active", "client")
		mustNotChange("DestroyLease(leased lease, from active)", changed, err)
		changed, err = store.DestroyLease(active, "leased", "api")
		mustNotChange("DestroyLease(active lease, from leased)", changed, err)
		for id, state := range map[string]string{leased: "leased", active: "active"} {
			if r := readLeaseRow(t, db, id); r.state != state || r.endedAt != "" || !r.hash.Valid {
				t.Errorf("a refused transition changed %s: %+v", id, r)
			}
		}
	})

	t.Run("no transition leaves a terminal state", func(t *testing.T) {
		for _, c := range []struct {
			n     int
			state string
		}{{7, "destroyed"}, {8, "failed"}, {9, "expired"}} {
			id := leaseID(c.n)
			insertRawLease(t, db, id, "widgets", c.state, nil)
			before := readLeaseRow(t, db, id)

			changed, err := store.MarkActive(id, "box")
			mustNotChange("MarkActive from "+c.state, changed, err)
			changed, err = store.FailLease(id, "x")
			mustNotChange("FailLease from "+c.state, changed, err)
			for _, from := range []string{"leased", "active"} {
				changed, err = store.DestroyLease(id, from, "api")
				mustNotChange("DestroyLease from "+from+" on a "+c.state+" lease", changed, err)
			}
			if after := readLeaseRow(t, db, id); after != before {
				t.Errorf("a %s lease changed: %+v -> %+v", c.state, before, after)
			}
		}
	})

	t.Run("a state that is not live is no state to leave", func(t *testing.T) {
		id := leaseID(10)
		insertRawLease(t, db, id, "widgets", "leased", HashToken(testToken(10)))
		for _, from := range []string{"destroyed", "failed", "expired", "bogus", ""} {
			if changed, err := store.DestroyLease(id, from, "api"); err == nil || changed {
				t.Errorf("DestroyLease from %q = (%v, %v); want an error and no change", from, changed, err)
			}
		}
		if r := readLeaseRow(t, db, id); r.state != "leased" {
			t.Errorf("a refused DestroyLease changed the lease to %q", r.state)
		}
	})
}
