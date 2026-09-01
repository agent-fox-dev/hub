package audit

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// openTestAuditDB opens a DuckDB database in a temporary directory
// and registers cleanup. The database file is placed in a subdirectory
// to also exercise the MkdirAll path.
func openTestAuditDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "testdata", "audit.duckdb")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatalf("OpenDB(%q): %v", path, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// openTestAuditDBWithSchema opens a DuckDB database and initializes the
// schema. Returns the *sql.DB for direct queries.
func openTestAuditDBWithSchema(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestAuditDB(t)
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	return db
}

// tableExists checks whether a table exists in the DuckDB database.
func tableExists(t *testing.T, db *sql.DB, tableName string) bool {
	t.Helper()
	var count int
	err := db.QueryRow(
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_name = ?",
		tableName,
	).Scan(&count)
	if err != nil {
		t.Fatalf("query for table %s: %v", tableName, err)
	}
	return count > 0
}

// allNineTables returns the names of all nine audit tables.
func allNineTables() []string {
	return []string{
		"agent_audit_events",
		"hub_audit_events",
		"session_outcomes",
		"tool_calls",
		"tool_errors",
		"agent_traces",
		"postmortems",
		"agent_sessions",
		"token_usage",
	}
}
