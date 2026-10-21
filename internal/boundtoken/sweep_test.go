package boundtoken

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// eventually polls cond until it holds or d passes.
func eventually(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

func countBindings(db *sql.DB) int {
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM token_workspace_bindings`).Scan(&n)
	return n
}

func shortenSweep(t *testing.T, d time.Duration) {
	t.Helper()
	old := sweepInterval
	sweepInterval = d
	t.Cleanup(func() { sweepInterval = old })
}

// TS-34-35: StartBindingSweep runs a pass at once, then one per interval, and
// returns after cancel. Requirement: 34-REQ-6.1
func TestSweep_Loop_TS_34_35(t *testing.T) {
	shortenSweep(t, 300*time.Millisecond)
	db := openTestDB(t)
	seedPAT(t, db, patRow{id: "o1"})
	bind(t, db, "o1", "gone")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	start := time.Now()
	go func() { StartBindingSweep(ctx, db); close(done) }()
	if !eventually(150*time.Millisecond, func() bool { return countBindings(db) == 0 }) || time.Since(start) >= 300*time.Millisecond {
		t.Fatal("the first orphan was not removed before the first interval elapsed")
	}
	seedPAT(t, db, patRow{id: "o2"})
	bind(t, db, "o2", "gone-too")
	if !eventually(time.Second, func() bool { return countBindings(db) == 0 }) {
		t.Fatal("a later pass did not remove the second orphan")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("StartBindingSweep did not return within one interval after cancel")
	}
}

// TS-34-36: A pass revokes and unbinds the tokens of bindings whose workspace
// is gone and deletes bindings whose pats row is gone. Requirement: 34-REQ-6.2
func TestSweep_Pass_TS_34_36(t *testing.T) {
	db := openTestDB(t)
	seedWorkspace(t, db, "here", "u1", "active")
	seedPAT(t, db, patRow{id: "a"})
	seedPAT(t, db, patRow{id: "b", revokedAt: "2026-01-01T00:00:00Z"})
	seedPAT(t, db, patRow{id: "d"})
	bind(t, db, "a", "gone")
	bind(t, db, "b", "gone")
	bind(t, db, "c", "here") // no pats row
	bind(t, db, "d", "here")

	if err := wsaccess.SweepBindings(context.Background(), db); err != nil {
		t.Fatalf("SweepBindings = %v", err)
	}
	var left []string
	rows, _ := db.Query(`SELECT token_id FROM token_workspace_bindings ORDER BY token_id`)
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		left = append(left, id)
	}
	rows.Close()
	if strings.Join(left, ",") != "d" {
		t.Errorf("bindings left = %v; want only d", left)
	}
	if !isRevoked(t, db, "a") || isRevoked(t, db, "d") {
		t.Error("want a revoked and d live")
	}
	var b string
	_ = db.QueryRow(`SELECT revoked_at FROM pats WHERE token_id = 'b'`).Scan(&b)
	if b != "2026-01-01T00:00:00Z" {
		t.Errorf("b's revoked_at = %q; want it unchanged", b)
	}
}

// TS-34-37: Bindings of revoked or expired tokens whose workspace exists
// survive the sweep and stay listed. Requirement: 34-REQ-6.3
func TestSweep_KeepsRevokedAndExpired_TS_34_37(t *testing.T) {
	env := newTestEnv(t, Config{})
	seedWorkspace(t, env.db, "s", "u1", "active")
	seedPAT(t, env.db, patRow{id: "revoked", revokedAt: "2026-01-01T00:00:00Z", createdAt: "2026-09-01T10:00:00Z"})
	seedPAT(t, env.db, patRow{id: "expired", expiresAt: "2026-01-01T00:00:00Z", createdAt: "2026-09-01T10:00:01Z"})
	bind(t, env.db, "revoked", "s")
	bind(t, env.db, "expired", "s")
	if err := wsaccess.SweepBindings(context.Background(), env.db); err != nil {
		t.Fatal(err)
	}
	if n := countBindings(env.db); n != 2 {
		t.Fatalf("bindings = %d; want 2", n)
	}
	list := listOf(t, env, "s", nil)
	if len(list) != 2 || list[0]["token_id"] != "revoked" || list[0]["revoked_at"] == nil || list[1]["token_id"] != "expired" || list[1]["expires_at"] == nil {
		t.Errorf("listing %v; want both tokens with their revoked_at and expires_at", list)
	}
}

// TS-34-38: A failing pass rolls back, is logged at error level, and the loop
// keeps running. Requirement: 34-REQ-6.4
func TestSweep_FailedPassRetried_TS_34_38(t *testing.T) {
	shortenSweep(t, 30*time.Millisecond)
	logs := captureLogs(t)
	db := openTestDB(t)
	seedPAT(t, db, patRow{id: "a"})
	bind(t, db, "a", "gone")
	if _, err := db.Exec(`CREATE TRIGGER block BEFORE UPDATE ON pats BEGIN SELECT RAISE(ABORT, 'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go StartBindingSweep(ctx, db)

	if !eventually(time.Second, func() bool { return strings.Contains(logs.String(), "level=ERROR") }) {
		t.Fatalf("no error-level log line for the failed pass: %q", logs)
	}
	if countBindings(db) != 1 || isRevoked(t, db, "a") {
		t.Fatal("the failed pass committed part of its work")
	}
	if _, err := db.Exec(`DROP TRIGGER block`); err != nil {
		t.Fatal(err)
	}
	if !eventually(time.Second, func() bool { return countBindings(db) == 0 }) || !isRevoked(t, db, "a") {
		t.Error("a later pass did not remove the binding and revoke the token")
	}
}

// TS-34-39: Cancelling the context while a pass runs is not logged as a
// failure: the pass is held at its transaction (the single connection is
// taken), the context is cancelled, and the sweep returns quietly.
// Requirement: 34-REQ-6.5
func TestSweep_CancelNotAFailure_TS_34_39(t *testing.T) {
	logs := captureLogs(t)
	db := openTestDB(t) // one connection
	seedPAT(t, db, patRow{id: "a"})
	bind(t, db, "a", "gone")
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { StartBindingSweep(ctx, db); close(done) }()
	time.Sleep(50 * time.Millisecond) // the first pass is now waiting for the connection
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the sweep did not return")
	}
	conn.Close()
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("the interrupted pass was logged as a failure: %q", logs)
	}
	if countBindings(db) != 1 || isRevoked(t, db, "a") {
		t.Error("the interrupted pass committed work")
	}
}
