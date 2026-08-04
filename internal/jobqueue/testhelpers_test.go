package jobqueue

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openTestDB opens an in-memory SQLite database configured with WAL mode
// and busy_timeout=5000. It does NOT call InitSchema — callers that need the
// schema should call InitSchema explicitly or use newTestQueue.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	// In-memory databases are per-connection; cap the pool to keep them shared.
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

// newTestQueue opens a test database, calls InitSchema, and returns a
// new Queue instance along with the underlying *sql.DB for direct queries.
func newTestQueue(t *testing.T) (*Queue, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(testWriter{t}, nil))
	q := New(db, logger)
	return q, db
}

// registerTestHandler registers a no-op handler for the given type name.
func registerTestHandler(t *testing.T, q *Queue, typeName string) {
	t.Helper()
	handler := func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		return nil, false, nil
	}
	if err := q.Register(typeName, handler, nil); err != nil {
		t.Fatalf("Register(%q) returned error: %v", typeName, err)
	}
}

// seedJob inserts a job row directly into the database for test setup.
func seedJob(t *testing.T, db *sql.DB, id, typ, key, nonce, status string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO jobs (id, type, key, nonce, status, payload, result, error, retry_count, available_at, submitted_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, '{}', NULL, NULL, 0, ?, 'test', ?, ?)`,
		id, typ, key, nonce, status, now, now, now,
	)
	if err != nil {
		t.Fatalf("seedJob(%q, type=%q, key=%q, nonce=%q, status=%q) failed: %v",
			id, typ, key, nonce, status, err)
	}
}

// columnInfo holds metadata for a single table column from PRAGMA table_info.
type columnInfo struct {
	Name    string
	Type    string
	NotNull int
	PK      int
}

// queryTableInfo returns column metadata for the given table.
func queryTableInfo(t *testing.T, db *sql.DB, tableName string) []columnInfo {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info('%s')", tableName))
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s) failed: %v", tableName, err)
	}
	defer rows.Close()

	var cols []columnInfo
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dfltValue *string
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan column info: %v", err)
		}
		cols = append(cols, columnInfo{Name: name, Type: typ, NotNull: notnull, PK: pk})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration error: %v", err)
	}
	return cols
}

// newTestQueueWithOpts opens a test database, calls InitSchema, and returns a
// new Queue instance constructed with the provided options, along with the
// underlying *sql.DB for direct queries.
func newTestQueueWithOpts(t *testing.T, opts ...Option) (*Queue, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(testWriter{t}, nil))
	q := New(db, logger, opts...)
	return q, db
}

// seedJobFull inserts a job row with full control over retry_count, available_at,
// and created_at for test scenarios that need precise timing or retry state.
func seedJobFull(t *testing.T, db *sql.DB, id, typ, key, nonce, status string, retryCount int, availableAt, createdAt time.Time) {
	t.Helper()
	avail := availableAt.UTC().Format(time.RFC3339)
	created := createdAt.UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO jobs (id, type, key, nonce, status, payload, result, error, retry_count, available_at, submitted_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, '{}', NULL, NULL, ?, ?, 'test', ?, ?)`,
		id, typ, key, nonce, status, retryCount, avail, created, created,
	)
	if err != nil {
		t.Fatalf("seedJobFull(%q, type=%q, key=%q, status=%q, retry=%d) failed: %v",
			id, typ, key, status, retryCount, err)
	}
}

// waitForStatus polls the database until the job reaches the target status
// or the timeout expires. Fails the test on timeout.
func waitForStatus(t *testing.T, db *sql.DB, jobID, targetStatus string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastStatus string
	for time.Now().Before(deadline) {
		err := db.QueryRow("SELECT status FROM jobs WHERE id=?", jobID).Scan(&lastStatus)
		if err == nil && lastStatus == targetStatus {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("waitForStatus(%q, %q): timed out after %v; last status=%q",
		jobID, targetStatus, timeout, lastStatus)
}

// findColumn searches for a column by name in the column list.
func findColumn(cols []columnInfo, name string) (columnInfo, bool) {
	for _, c := range cols {
		if c.Name == name {
			return c, true
		}
	}
	return columnInfo{}, false
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
