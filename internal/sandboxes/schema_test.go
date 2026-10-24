package sandboxes

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// InitSchema creates the table once and leaves rows alone; the state CHECK
// accepts exactly the five states and token_hash is unique and nullable.
func TestInitSchema_IdempotentWithConstraints(t *testing.T) {
	_, db := newLeaseStore(t, time.Now())
	for i, state := range []string{"leased", "active", "destroyed", "expired", "failed"} {
		insertRawLease(t, db, leaseID(i), "widgets", state, testToken(i))
	}
	if _, err := db.Exec(rawLeaseInsert, leaseID(100), "widgets", "bogus", testToken(100)); err == nil {
		t.Error("state 'bogus' was accepted")
	}
	insertRawLease(t, db, leaseID(200), "widgets", "destroyed", nil)
	insertRawLease(t, db, leaseID(201), "widgets", "destroyed", nil)
	if _, err := db.Exec(rawLeaseInsert, leaseID(300), "widgets", "leased", testToken(0)); err == nil {
		t.Error("a second row with the same hash was accepted")
	}
	before := wholeRows(t, db)
	if err := InitSchema(db); err != nil {
		t.Fatalf("second InitSchema: %v", err)
	}
	if wholeRows(t, db) != before {
		t.Error("the second InitSchema changed the rows")
	}
}

// legacyLeaseSchema is the table before the column lease_expires_at.
func legacyLeaseSchema(t *testing.T) string {
	t.Helper()
	var kept []string
	for line := range strings.SplitSeq(leaseSchemaSQL, "\n") {
		if !strings.Contains(line, "lease_expires_at") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func leaseColumnCount(t *testing.T, db *sql.DB, column string) int {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info('sandbox_leases')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var cid, notNull, pk int
		var name, colType string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if name == column {
			n++
		}
	}
	return n
}

// The migration adds lease_expires_at to a table from before the column, once,
// leaves old rows NULL and a new table alone, tolerates a nil database and
// reports a database without the table.
func TestMigrateLeaseExpiry(t *testing.T) {
	db := testutil.OpenDB(t)
	testutil.Exec(t, db, legacyLeaseSchema(t))
	insertRawLease(t, db, leaseID(1), "widgets", "leased", testToken(1))
	if err := InitSchema(db); err != nil { // runs first at every start, changes an existing table nothing
		t.Fatal(err)
	}
	for run := range 2 {
		if err := MigrateLeaseExpiry(db); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
	}
	var v sql.NullString
	if err := db.QueryRow(`SELECT lease_expires_at FROM sandbox_leases WHERE id = ?`, leaseID(1)).Scan(&v); err != nil || v.Valid || leaseColumnCount(t, db, "lease_expires_at") != 1 {
		t.Errorf("after the migration: column count %d, old row %v (%v); want 1 and NULL", leaseColumnCount(t, db, "lease_expires_at"), v, err)
	}

	_, fresh := newLeaseStore(t, time.Now())
	insertRawLease(t, fresh, leaseID(1), "widgets", "leased", testToken(1))
	before := wholeRows(t, fresh)
	if err := MigrateLeaseExpiry(fresh); err != nil || wholeRows(t, fresh) != before || leaseColumnCount(t, fresh, "lease_expires_at") != 1 {
		t.Errorf("a new table: error %v, rows changed %v", err, wholeRows(t, fresh) != before)
	}
	if err := MigrateLeaseExpiry(nil); err != nil {
		t.Errorf("MigrateLeaseExpiry(nil) = %v", err)
	}
	if err := MigrateLeaseExpiry(testutil.OpenDB(t)); err == nil {
		t.Error("a database without the table returned nil")
	}
}
