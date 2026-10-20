package sandboxes

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// The migration is called from cmd/af-hub/main.go, which in-process tests cannot
// see, so the real binary is started on a database whose lease table predates
// the column. Sandboxes are not configured: the migration runs all the same.
// Requirement: 28-REQ-2.2
func TestAfHubBinary_MigratesLegacyLeaseTable(t *testing.T) {
	bin := buildAfHub(t)
	env := newAfHubEnv(t, "")
	bootstrapAfHub(t, bin, env) // creates the database file, not yet the lease table

	dbPath := filepath.Join(env.dataDir, "afhub.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("opening %s: %v", dbPath, err)
	}
	defer db.Close()
	if _, err := db.Exec(legacyLeaseSchema(t)); err != nil {
		t.Fatalf("creating the legacy table: %v", err)
	}
	insertRawLease(t, db, leaseID(1), "widgets", "destroyed", nil)
	if n := leaseColumnCount(t, db, "lease_expires_at"); n != 0 {
		t.Fatalf("the legacy table has the column already (%d)", n)
	}

	startAfHub(t, bin, env)

	if n := leaseColumnCount(t, db, "lease_expires_at"); n != 1 {
		t.Errorf("after the start the lease table lists lease_expires_at %d times; want once", n)
	}
	if v, ok := storedLeaseExpiry(t, db, leaseID(1)); ok {
		t.Errorf("the old row reads lease_expires_at %q after the start; want NULL", v)
	}
}
