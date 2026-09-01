package audit

import (
	"os"
	"path/filepath"
	"testing"
)

// TS-17-1: audit.OpenDB calls os.MkdirAll on the parent directory and
// returns a valid *sql.DB on success.
func TestOpenDB_CreatesParentDirs(t *testing.T) {
	// Arrange: path inside a non-existent subdirectory of TempDir
	base := t.TempDir()
	path := filepath.Join(base, "subdir", "nested", "audit.duckdb")

	// Act
	db, err := OpenDB(path)
	if err != nil {
		t.Fatalf("OpenDB(%q) returned error: %v", path, err)
	}
	t.Cleanup(func() { db.Close() })

	// Assert: parent directory was created
	parentDir := filepath.Dir(path)
	info, statErr := os.Stat(parentDir)
	if statErr != nil {
		t.Fatalf("parent directory %q does not exist: %v", parentDir, statErr)
	}
	if !info.IsDir() {
		t.Fatalf("parent path %q is not a directory", parentDir)
	}

	// Assert: DB is not nil
	if db == nil {
		t.Fatal("OpenDB returned nil *sql.DB")
	}
}

// TS-17-1 (continued): db.Ping() succeeds on a valid connection.
func TestOpenDB_PingSucceeds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.duckdb")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatalf("OpenDB(%q): %v", path, err)
	}
	t.Cleanup(func() { db.Close() })

	if err := db.Ping(); err != nil {
		t.Fatalf("db.Ping() returned error: %v", err)
	}
}

// TS-17-1 edge case 17-REQ-1.E1: permission error on parent directory.
func TestOpenDB_ReturnsErrorOnPermissionDenied(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("test requires non-root user")
	}

	// Create a directory and make it unwritable
	base := t.TempDir()
	restrictedDir := filepath.Join(base, "restricted")
	if err := os.MkdirAll(restrictedDir, 0o555); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Cleanup(func() { os.Chmod(restrictedDir, 0o755) })

	path := filepath.Join(restrictedDir, "subdir", "audit.duckdb")
	db, err := OpenDB(path)
	if err == nil {
		db.Close()
		t.Fatal("OpenDB should return error for unwritable parent, got nil")
	}
}

// TS-17-1 edge case 17-REQ-1.E3: empty path returns error.
func TestOpenDB_ReturnsErrorOnEmptyPath(t *testing.T) {
	db, err := OpenDB("")
	if err == nil {
		db.Close()
		t.Fatal("OpenDB('') should return error, got nil")
	}
}

// TS-17-4: Connection is closed on cleanup (simulated by checking stats).
func TestOpenDB_CloseReducesOpenConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.duckdb")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatalf("OpenDB(%q): %v", path, err)
	}

	// Force a connection to be opened
	if err := db.Ping(); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	// Close the DB
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// After close, Ping should fail
	if err := db.Ping(); err == nil {
		t.Error("Ping after Close should return error")
	}
}
