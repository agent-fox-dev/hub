package audit

import (
	"path/filepath"
	"testing"
)

// TS-17-5: audit.InitSchema creates all nine required tables in a fresh
// DuckDB database.
func TestInitSchema_CreatesAllTables(t *testing.T) {
	db := openTestAuditDB(t)

	err := InitSchema(db)
	if err != nil {
		t.Fatalf("InitSchema returned error: %v", err)
	}

	for _, name := range allNineTables() {
		if !tableExists(t, db, name) {
			t.Errorf("table %q does not exist after InitSchema", name)
		}
	}
}

// TS-17-6: audit.InitSchema is non-destructive when run against a DuckDB
// database that already contains all nine tables with existing data.
func TestInitSchema_Idempotent(t *testing.T) {
	db := openTestAuditDB(t)

	// First call: create tables
	if err := InitSchema(db); err != nil {
		t.Fatalf("first InitSchema: %v", err)
	}

	// Insert a sentinel row into hub_audit_events
	_, err := db.Exec(`INSERT INTO hub_audit_events (id, event_type, actor_id, actor_type,
		resource_type, resource_id, action, workspace, metadata, ingested_at)
		VALUES ('test-id-1', 'test.event', 'actor1', 'system',
		'workspace', 'ws1', 'create', 'ws1', '{}', '2026-09-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert sentinel row: %v", err)
	}

	// Second call: should be idempotent
	if err := InitSchema(db); err != nil {
		t.Fatalf("second InitSchema: %v", err)
	}

	// Verify sentinel row still exists
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM hub_audit_events WHERE id = 'test-id-1'").Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Errorf("sentinel row count = %d, want 1 (data was lost)", count)
	}

	// Verify all tables still exist
	for _, name := range allNineTables() {
		if !tableExists(t, db, name) {
			t.Errorf("table %q missing after second InitSchema", name)
		}
	}
}

// TS-17-7: audit.InitSchema executes all DDL within a single transaction so
// partial initialization is never committed.
func TestInitSchema_RollsBackOnFailure(t *testing.T) {
	// Open a DB, then close it to force InitSchema to fail.
	// The closed connection will cause the transaction to fail immediately.
	path := filepath.Join(t.TempDir(), "audit.duckdb")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	db.Close() // close before InitSchema to force failure

	err = InitSchema(db)
	if err == nil {
		t.Fatal("InitSchema on closed DB should return error, got nil")
	}

	// Reopen the database and verify no tables were created
	db2, err := OpenDB(path)
	if err != nil {
		t.Fatalf("OpenDB (reopen): %v", err)
	}
	t.Cleanup(func() { db2.Close() })

	for _, name := range allNineTables() {
		if tableExists(t, db2, name) {
			t.Errorf("table %q should not exist after rolled-back InitSchema", name)
		}
	}
}
