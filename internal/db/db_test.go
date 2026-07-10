package db_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/db"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// 2.1 — SQLite Initialization, Pragmas, and Schema
// ---------------------------------------------------------------------------

// TestSpec01_DBParentDirCreation verifies that InitDatabase creates the
// parent directories for the database path automatically if they do not
// exist before opening the database (mkdir -p equivalent).
// TS-01-8, REQ: 01-REQ-3.1
func TestSpec01_DBParentDirCreation(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "nested", "deep", "af-hub.db")

	// Parent directories do not exist yet.
	parentDir := filepath.Dir(dbPath)
	if _, err := os.Stat(parentDir); !os.IsNotExist(err) {
		t.Fatal("precondition failed: parent directory should not exist yet")
	}

	database, err := db.InitDatabase(dbPath)
	if err != nil {
		t.Fatalf("InitDatabase should succeed and create parent dirs: %v", err)
	}
	if database == nil {
		t.Fatal("InitDatabase returned nil *sql.DB, want non-nil")
	}
	defer database.Close()

	// Verify parent directory was created.
	if _, err := os.Stat(parentDir); os.IsNotExist(err) {
		t.Error("parent directory should have been created by InitDatabase")
	}

	// Verify database file was created.
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("database file should exist after InitDatabase")
	}
}

// TestSpec01_DBPragmasApplied verifies that WAL, foreign_keys, and
// busy_timeout pragmas are applied correctly after opening the database.
// After initialization: journal_mode=wal, foreign_keys=1, busy_timeout=5000.
// TS-01-9, REQ: 01-REQ-3.2
func TestSpec01_DBPragmasApplied(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := db.InitDatabase(dbPath)
	if err != nil {
		t.Fatalf("InitDatabase failed: %v", err)
	}
	if database == nil {
		t.Fatal("InitDatabase returned nil *sql.DB, want non-nil")
	}
	defer database.Close()

	// Check journal_mode = WAL.
	var journalMode string
	if err := database.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("failed to query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want %q", journalMode, "wal")
	}

	// Check foreign_keys = ON (1).
	var foreignKeys int
	if err := database.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("failed to query foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}

	// Check busy_timeout = 5000.
	var busyTimeout int
	if err := database.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("failed to query busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", busyTimeout)
	}
}

// TestSpec01_DBSchemaTablesAndIndexes verifies that all 7 tables and 5
// indexes exist after InitDatabase and that initialization is idempotent
// (no error on second boot).
//
// Note: REQ-3.3 text says "eight tables" but lists only 7 table names.
// This is a known spec error (see reviewer findings). The correct count
// is 7 tables: users, admin_tokens, api_keys, teams, team_members,
// workspaces, workspace_tokens.
//
// TS-01-10, REQ: 01-REQ-3.3
func TestSpec01_DBSchemaTablesAndIndexes(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := db.InitDatabase(dbPath)
	if err != nil {
		t.Fatalf("InitDatabase failed: %v", err)
	}
	if database == nil {
		t.Fatal("InitDatabase returned nil *sql.DB, want non-nil")
	}
	defer database.Close()

	// Expected tables (7 — see note above about spec count error).
	expectedTables := []string{
		"users",
		"admin_tokens",
		"api_keys",
		"teams",
		"team_members",
		"workspaces",
		"workspace_tokens",
	}
	for _, tbl := range expectedTables {
		var name string
		err := database.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", tbl,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q should exist in database: %v", tbl, err)
		}
	}

	// Expected indexes (5).
	expectedIndexes := []string{
		"idx_api_keys_key_id",
		"idx_workspace_tokens_token_id",
		"idx_users_provider",
		"idx_workspaces_slug",
		"idx_teams_slug",
	}
	for _, idx := range expectedIndexes {
		var name string
		err := database.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='index' AND name=?", idx,
		).Scan(&name)
		if err != nil {
			t.Errorf("index %q should exist in database: %v", idx, err)
		}
	}

	// Idempotency check: a second InitDatabase call on the same path
	// should not error (CREATE TABLE/INDEX IF NOT EXISTS).
	database2, err := db.InitDatabase(dbPath)
	if err != nil {
		t.Fatalf("second InitDatabase (idempotency) failed: %v", err)
	}
	if database2 == nil {
		t.Fatal("second InitDatabase returned nil *sql.DB")
	}
	database2.Close()
}

// TestSpec01_DBDefaultConnectionPool verifies that sql.DB instance has
// default MaxOpenConnections=0 (no explicit SetMaxOpenConns override)
// after InitDatabase. The busy_timeout pragma handles write contention.
// TS-01-11, REQ: 01-REQ-3.4
func TestSpec01_DBDefaultConnectionPool(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := db.InitDatabase(dbPath)
	if err != nil {
		t.Fatalf("InitDatabase failed: %v", err)
	}
	if database == nil {
		t.Fatal("InitDatabase returned nil *sql.DB, want non-nil")
	}
	defer database.Close()

	// Default MaxOpenConnections is 0 (unlimited) when no explicit
	// SetMaxOpenConns override is applied.
	stats := database.Stats()
	if stats.MaxOpenConnections != 0 {
		t.Errorf("MaxOpenConnections = %d, want 0 (default, no explicit SetMaxOpenConns)",
			stats.MaxOpenConnections)
	}

	// Verify busy_timeout pragma is set (contention handled at SQLite layer).
	var busyTimeout int
	if err := database.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("failed to query busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", busyTimeout)
	}
}

// ---------------------------------------------------------------------------
// 2.2 — SQLite Fatal Error Paths
// ---------------------------------------------------------------------------

// TestSpec01_DBUnwritableParentDir verifies that InitDatabase returns an
// error when the parent directory for the database path cannot be created
// (e.g., permission denied). In the full server, this triggers a fatal exit.
// TS-01-E4, REQ: 01-REQ-3.E1
func TestSpec01_DBUnwritableParentDir(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("test requires non-root user to trigger permission denied")
	}

	// Use a path under /root (or another non-writable directory) to
	// trigger a directory creation failure.
	dbPath := "/root/no-write-permission/af-hub.db"

	_, err := db.InitDatabase(dbPath)
	if err == nil {
		t.Fatal("InitDatabase should return error when parent directory is unwritable, got nil")
	}
}

// TestSpec01_DBOpenFailure verifies that InitDatabase returns an error
// when the database file cannot be opened or created (e.g., path is an
// existing directory, not a file).
// TS-01-E5, REQ: 01-REQ-3.E2
func TestSpec01_DBOpenFailure(t *testing.T) {
	tmpDir := t.TempDir()
	// Create a directory at the expected database file path, so the
	// SQLite driver fails to open it as a file.
	dbPath := filepath.Join(tmpDir, "af-hub.db")
	if err := os.MkdirAll(dbPath, 0755); err != nil {
		t.Fatalf("failed to create directory at db path: %v", err)
	}

	_, err := db.InitDatabase(dbPath)
	if err == nil {
		t.Fatal("InitDatabase should return error when db path is a directory, got nil")
	}
}

// TestSpec01_DBPragmaFailureIdentifiesName verifies that when a PRAGMA
// statement fails, the error identifies the specific failing PRAGMA.
// This is tested indirectly: if the DB is read-only, journal_mode=WAL
// should fail and the error should reference the pragma.
// TS-01-E6, REQ: 01-REQ-3.E3
func TestSpec01_DBPragmaFailureIdentifiesName(t *testing.T) {
	// This test verifies the error path when a PRAGMA fails.
	// We test this by creating a read-only database scenario.
	// The implementation must return an error that identifies the
	// specific failing PRAGMA. Since we can't easily make a specific
	// PRAGMA fail without the others, we verify the error contract
	// by checking that the stub returns a proper error.

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Create a minimal SQLite database file directly, then make it read-only.
	directDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to create test db: %v", err)
	}
	// Create something so the file exists.
	if _, err := directDB.Exec("CREATE TABLE IF NOT EXISTS test (id TEXT)"); err != nil {
		t.Fatalf("failed to create test table: %v", err)
	}
	directDB.Close()

	// Make the database file and its directory read-only.
	if err := os.Chmod(dbPath, 0444); err != nil {
		t.Fatalf("failed to chmod db file: %v", err)
	}
	if err := os.Chmod(tmpDir, 0555); err != nil {
		t.Fatalf("failed to chmod dir: %v", err)
	}

	// Restore permissions for cleanup.
	t.Cleanup(func() {
		os.Chmod(tmpDir, 0755)
		os.Chmod(dbPath, 0644)
	})

	if os.Getuid() == 0 {
		t.Skip("test requires non-root user to trigger read-only PRAGMA failure")
	}

	// InitDatabase should fail because PRAGMA journal_mode = WAL
	// requires write access.
	_, err = db.InitDatabase(dbPath)
	if err == nil {
		t.Fatal("InitDatabase should return error when PRAGMA journal_mode=WAL fails on read-only DB, got nil")
	}
	// The error MUST identify the specific failing PRAGMA name.
	// TS-01-E6 pseudocode: assert 'journal_mode' in fatal_log['msg']
	errMsg := err.Error()
	if !strings.Contains(errMsg, "journal_mode") {
		t.Errorf("error message = %q, want it to contain 'journal_mode' (the specific failing PRAGMA name)", errMsg)
	}
}

// TestSpec01_DBBusyTimeoutHTTP503 verifies that when a SQLite write
// operation exhausts the busy_timeout window (5 seconds), the error
// can be detected. The actual HTTP 503 response is produced by the
// handler/middleware layer, but this test confirms the DB layer
// correctly propagates the busy timeout error.
//
// Note: This test requires simulating write contention which is complex
// in a unit test. We verify the contract by ensuring InitDatabase
// sets the busy_timeout pragma correctly (tested in TestSpec01_DBPragmasApplied)
// and that the handler layer returns HTTP 503 (tested in handler tests).
// TS-01-E7, REQ: 01-REQ-3.E4
func TestSpec01_DBBusyTimeoutHTTP503(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := db.InitDatabase(dbPath)
	if err != nil {
		t.Fatalf("InitDatabase failed: %v", err)
	}
	if database == nil {
		t.Fatal("InitDatabase returned nil *sql.DB, want non-nil")
	}
	defer database.Close()

	// Verify busy_timeout is set — this is the prerequisite for the
	// write contention behavior. The HTTP 503 response is tested at
	// the handler/middleware level.
	var busyTimeout int
	if err := database.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("failed to query busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Errorf("busy_timeout = %d, want 5000 (prerequisite for HTTP 503 on contention)", busyTimeout)
	}

	// Open a second connection to simulate write contention.
	// First connection acquires an exclusive write lock.
	tx, err := database.Begin()
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	defer tx.Rollback()

	// Create a test table and perform a write to acquire the lock.
	if _, err := tx.Exec("CREATE TABLE IF NOT EXISTS contention_test (id TEXT)"); err != nil {
		t.Fatalf("failed to create contention table: %v", err)
	}
	if _, err := tx.Exec("INSERT INTO contention_test VALUES ('lock')"); err != nil {
		t.Fatalf("failed to insert: %v", err)
	}

	// Open a second connection with a very short busy_timeout to speed up test.
	db2, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open second connection: %v", err)
	}
	defer db2.Close()

	// Set a very short busy_timeout on the second connection.
	if _, err := db2.Exec("PRAGMA busy_timeout = 100"); err != nil {
		t.Fatalf("failed to set busy_timeout on db2: %v", err)
	}

	// Attempt a write on the second connection while the first holds the lock.
	// This should fail with a busy/locked error.
	_, writeErr := db2.Exec("INSERT INTO contention_test VALUES ('contested')")
	if writeErr == nil {
		t.Error("expected busy/locked error when writing with contention, got nil")
	}
	// The error should indicate database is busy/locked.
	// Implementation must detect this and return HTTP 503 at the handler level.
}
