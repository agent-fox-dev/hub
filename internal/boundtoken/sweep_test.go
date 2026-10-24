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
