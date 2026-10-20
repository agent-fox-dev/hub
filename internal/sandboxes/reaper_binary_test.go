package sandboxes

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/audit"
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

// TS-28-57 (smoke): af-hub, started with sandboxes on over the leases of a crashed
// process, ends the active lease as destroyed with end_reason restart, leaves the
// leased one alone, prunes the lease that ended long ago and keeps the recent one,
// and reports no active box on /metrics, all before it answers its first request.
// Path: 28-PATH-5. Requirements: 28-REQ-5.1, 28-REQ-6.2, 28-REQ-9.3
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

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", env.port))
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	metrics, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(metrics), "# TYPE afhub_sandboxes_active gauge") {
		t.Errorf("/metrics does not list afhub_sandboxes_active:\n%s", metrics)
	}
	for _, line := range strings.Split(string(metrics), "\n") {
		if strings.HasPrefix(line, "afhub_sandboxes_active{") && !strings.HasSuffix(line, " 0") {
			t.Errorf("/metrics has %q after recovery; want no active box", line)
		}
	}

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

// TS-28-56 (smoke): When the server's Start returns, af-hub destroys the box of every
// active lease and ends it with end_reason shutdown, records the event, leaves a
// leased lease alone, and does so while both databases are still open, within twenty
// seconds. A box cannot be made by the fake engine of the binary tests, so the lease
// of the box is written into the database while the hub runs, as the gateway would
// have written it, and the hub is stopped with SIGTERM.
// Path: 28-PATH-4. Requirements: 28-REQ-7.1, 28-REQ-7.3, 28-REQ-7.4
func TestAfHubBinary_ShutdownEndsActiveLeases_TS_28_56(t *testing.T) {
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
	cmd, output, exited := startAfHubProcess(t, bin, env)

	db, err := sql.Open("sqlite", filepath.Join(env.dataDir, "afhub.db")+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatalf("opening the hub's database: %v", err)
	}
	defer db.Close()
	insertRawLease(t, db, leaseID(1), "widgets", "active", HashToken(testToken(1)))
	insertRawLease(t, db, leaseID(2), "widgets", "leased", HashToken(testToken(2)))
	if _, err := db.Exec(`UPDATE sandbox_leases SET sandbox_id = 'box-1' WHERE id = ?`, leaseID(1)); err != nil {
		t.Fatal(err)
	}
	leasedBefore := wholeRowsOf(t, db, leaseID(2))

	start := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signalling af-hub: %v", err)
	}
	select {
	case <-exited:
	case <-time.After(30 * time.Second):
		t.Fatalf("af-hub did not stop within 30 seconds of SIGTERM:\n%s", output)
	}
	if elapsed := time.Since(start); elapsed > ShutdownLimit+5*time.Second {
		t.Errorf("af-hub took %v to stop; want it inside the shutdown limit", elapsed)
	}

	var state, reason string
	if err := db.QueryRow(`SELECT state, COALESCE(end_reason, '') FROM sandbox_leases WHERE id = ?`, leaseID(1)).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "destroyed" || reason != "shutdown" {
		t.Errorf("the active lease reads %s/%s after SIGTERM; want destroyed/shutdown\nhub output:\n%s", state, reason, output)
	}
	if after := wholeRowsOf(t, db, leaseID(2)); after != leasedBefore {
		t.Errorf("the leased lease changed:\n%s\n->\n%s", leasedBefore, after)
	}

	auditDB, err := audit.OpenDB(filepath.Join(env.dataDir, "audit.duckdb"))
	if err != nil {
		t.Fatalf("opening the audit database: %v", err)
	}
	defer auditDB.Close()
	var actorType, actorID, metadata string
	if err := auditDB.QueryRow(`SELECT actor_type, actor_id, metadata FROM hub_audit_events WHERE event_type = ? AND resource_id = ?`,
		audit.EventSandboxDestroy, leaseID(1)).Scan(&actorType, &actorID, &metadata); err != nil {
		t.Fatalf("reading the destroy event: %v\nhub output:\n%s", err, output)
	}
	if actorType != "system" || actorID != "" || !strings.Contains(metadata, `"reason":"shutdown"`) {
		t.Errorf("event by %q/%q with metadata %s; want the system, no actor id and reason shutdown", actorType, actorID, metadata)
	}
}

// wholeRowsOf returns every column of one lease row as a string.
func wholeRowsOf(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	return strings.Join(rowTextOf(t, db, id), "|")
}
