package merge

import (
	"database/sql"
	"log/slog"
	"testing"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
	_ "modernc.org/sqlite"
)

// openTestDB opens an in-memory SQLite database configured with WAL mode
// and busy_timeout=5000.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("failed to set WAL mode: %v", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		t.Fatalf("failed to set busy_timeout: %v", err)
	}
	return db
}

// newTestQueue opens a test database, initializes the jobqueue schema
// (including the group_key migration), and returns a new Queue along
// with the underlying *sql.DB for direct queries.
func newTestQueue(t *testing.T) (*jobqueue.Queue, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	if err := jobqueue.InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}
	if err := jobqueue.MigrateGroupKey(db); err != nil {
		t.Fatalf("MigrateGroupKey() returned error: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(testWriter{t}, nil))
	q, err := jobqueue.New(db, logger)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	return q, db
}

// testWriter adapts *testing.T to io.Writer for slog output in tests.
type testWriter struct {
	t *testing.T
}

func (tw testWriter) Write(p []byte) (int, error) {
	tw.t.Helper()
	tw.t.Log(string(p))
	return len(p), nil
}

// rollbackCall records a call to the RollbackFunc.
type rollbackCall struct {
	trunkDir string
	branch   string
	sha      string
}
