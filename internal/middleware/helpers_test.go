package middleware_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// setupTestDB creates a temporary SQLite database for testing.
// Returns a *sql.DB with WAL, foreign_keys, and busy_timeout pragmas applied.
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}

	// Apply pragmas as InitDatabase would.
	if _, err := database.Exec("PRAGMA journal_mode = WAL"); err != nil {
		t.Fatalf("failed to set WAL: %v", err)
	}
	if _, err := database.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("failed to set foreign_keys: %v", err)
	}
	if _, err := database.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		t.Fatalf("failed to set busy_timeout: %v", err)
	}

	t.Cleanup(func() { database.Close() })
	return database
}

// setupBrokenDB returns a *sql.DB that is closed, so queries will fail.
// Used to verify that structurally invalid tokens are rejected without
// any DB queries — if a DB query is attempted, it will produce a 503
// or error instead of the expected 401.
func setupBrokenDB(t *testing.T) *sql.DB {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "broken.db")

	database, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}

	// Close immediately so any subsequent queries fail.
	database.Close()

	// Remove the file to ensure even a reconnect attempt fails.
	os.Remove(dbPath)

	return database
}
