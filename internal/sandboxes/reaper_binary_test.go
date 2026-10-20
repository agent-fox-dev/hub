package sandboxes

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// seedAfHubLeases creates the lease table in the hub's database, as hub does at
// start, and inserts the leases of an earlier process: an active one with a box,
// a leased one, a destroyed one that ended long ago and another that ended
// recently. All of them expire in 2030, so only a crash, not time, could have
// left the active one active.
func seedAfHubLeases(t *testing.T, env *afHubEnv) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(env.dataDir, "afhub.db"))
	if err != nil {
		t.Fatalf("opening the hub's database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	if err := MigrateLeaseExpiry(db); err != nil {
		t.Fatalf("MigrateLeaseExpiry: %v", err)
	}
	recent := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	insertRawLease(t, db, leaseID(1), "widgets", "active", HashToken(testToken(1)))
	insertRawLease(t, db, leaseID(2), "widgets", "leased", HashToken(testToken(2)))
	insertRawLease(t, db, leaseID(3), "widgets", "destroyed", nil)
	insertRawLease(t, db, leaseID(4), "widgets", "destroyed", nil)
	for _, stmt := range []struct {
		query string
		args  []any
	}{
		{`UPDATE sandbox_leases SET sandbox_id = 'box-1' WHERE id = ?`, []any{leaseID(1)}},
		{`UPDATE sandbox_leases SET ended_at = '2020-01-01T00:00:00Z', end_reason = 'api' WHERE id = ?`, []any{leaseID(3)}},
		{`UPDATE sandbox_leases SET ended_at = ?, end_reason = 'api' WHERE id = ?`, []any{recent, leaseID(4)}},
	} {
		if _, err := db.Exec(stmt.query, stmt.args...); err != nil {
			t.Fatalf("seeding: %v", err)
		}
	}
	return db
}

// TS-28-57 (part): af-hub, started with sandboxes on over the leases of a crashed
// process, ends the active lease as destroyed with end_reason restart, leaves the
// leased one alone, prunes the lease that ended long ago and keeps the recent one,
// all before it answers its first request.
// Requirements: 28-REQ-5.1, 28-REQ-6.2
func TestAfHubBinary_RecoversAtStart(t *testing.T) {
	bin := buildAfHub(t)
	engine, _ := writeFakeEngine(t)
	env := newAfHubEnv(t, fmt.Sprintf(`[sandbox]
provider      = "podman"
engine_binary = %q

[[sandbox.profiles]]
name  = "alpha"
image = "registry.example/alpha:1"
ttl   = "1h"
`, engine))
	bootstrapAfHub(t, bin, env)
	db := seedAfHubLeases(t, env)

	output := startAfHub(t, bin, env)

	states := map[string]string{}
	rows, err := db.Query(`SELECT id, state, COALESCE(end_reason, '') FROM sandbox_leases`)
	if err != nil {
		t.Fatalf("reading the leases: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, state, reason string
		if err := rows.Scan(&id, &state, &reason); err != nil {
			t.Fatal(err)
		}
		states[id] = state + "/" + reason
	}
	want := map[string]string{
		leaseID(1): "destroyed/restart",
		leaseID(2): "leased/",
		leaseID(4): "destroyed/api",
	}
	if len(states) != len(want) {
		t.Errorf("leases after the start = %v; want %v (the lease that ended long ago pruned)\nhub output:\n%s", states, want, output)
	}
	for id, w := range want {
		if states[id] != w {
			t.Errorf("lease %s reads %q; want %q\nhub output:\n%s", id, states[id], w, output)
		}
	}
}

// TS-28-29: With sandboxes off, af-hub does no recovery, no sweep and starts no
// reaper: the lease table is the same after the start as before it.
// Requirement: 28-REQ-5.6
func TestAfHubBinary_DisabledLeavesLeasesAlone_TS_28_29(t *testing.T) {
	bin := buildAfHub(t)
	env := newAfHubEnv(t, "")
	bootstrapAfHub(t, bin, env)
	db := seedAfHubLeases(t, env)
	before := wholeRows(t, db)

	startAfHub(t, bin, env)
	time.Sleep(2 * time.Second)

	if after := wholeRows(t, db); after != before {
		t.Errorf("the lease table changed although sandboxes are off:\n%s\nwant\n%s", after, before)
	}
}
