package jobqueue

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test helpers for group_key extension tests (spec 12).
// ---------------------------------------------------------------------------

// newTestQueueWithMigration opens a test database, calls InitSchema and
// MigrateGroupKey, and returns a new Queue instance. This mirrors the expected
// production boot sequence after the merge_operations migration is applied.
func newTestQueueWithMigration(t *testing.T, opts ...Option) (*Queue, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}
	if err := MigrateGroupKey(db); err != nil {
		t.Fatalf("MigrateGroupKey() returned error: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(testWriter{t}, nil))
	q, err := New(db, logger, opts...)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	return q, db
}

// queryColumnMeta returns the type, NOT NULL flag, and default value for
// a column in the given table. Returns found=false if the column does not
// exist.
func queryColumnMeta(t *testing.T, db *sql.DB, table, column string) (colType string, notNull int, defaultVal string, found bool) {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info('%s')", table))
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s) failed: %v", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid, nn, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &nn, &dflt, &pk); err != nil {
			t.Fatalf("scan column info: %v", err)
		}
		if name == column {
			d := ""
			if dflt.Valid {
				d = dflt.String
			}
			return typ, nn, d, true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration error: %v", err)
	}
	return "", 0, "", false
}

// seedJobFullWithGroupKey inserts a job row with group_key set. When the
// group_key column does not exist in the schema, the INSERT fails and the
// test is failed — this is expected for tests that run before the migration
// is implemented.
func seedJobFullWithGroupKey(t *testing.T, db *sql.DB, id, typ, key, nonce, status, groupKey string, retryCount int, availableAt, createdAt time.Time) {
	t.Helper()
	avail := availableAt.UTC().Format(time.RFC3339)
	created := createdAt.UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO jobs (id, type, key, nonce, status, payload, result, error,
		  retry_count, available_at, submitted_by, group_key, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, '{}', NULL, NULL, ?, ?, 'test', ?, ?, ?)`,
		id, typ, key, nonce, status, retryCount, avail, groupKey, created, created,
	)
	if err != nil {
		t.Fatalf("seedJobFullWithGroupKey(%q, type=%q, key=%q, group_key=%q) failed: %v",
			id, typ, key, groupKey, err)
	}
}

// ---------------------------------------------------------------------------
// TS-12-1: Schema migration adds group_key TEXT NOT NULL DEFAULT '' column
// to the jobs table, and existing rows have group_key set to empty string.
//
// Requirement: 12-REQ-1.1
// ---------------------------------------------------------------------------

func TestMigrateGroupKey_AddsColumn(t *testing.T) {
	db := openTestDB(t)

	// Initialize the base schema (without group_key).
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}

	// Seed an existing job row before applying the migration.
	seedJob(t, db, "pre-existing-job", "clone", "ws1:clone", "n0", "queued")

	// Apply the group_key migration.
	if err := MigrateGroupKey(db); err != nil {
		t.Fatalf("MigrateGroupKey() returned error: %v", err)
	}

	// Verify group_key column exists with the correct type and constraints.
	colType, notNull, defaultVal, found := queryColumnMeta(t, db, "jobs", "group_key")
	if !found {
		t.Fatal("group_key column not found in jobs table after MigrateGroupKey()")
	}
	if colType != "TEXT" {
		t.Errorf("expected group_key column type='TEXT', got %q", colType)
	}
	if notNull != 1 {
		t.Error("expected group_key column to be NOT NULL")
	}
	if defaultVal != "''" {
		t.Errorf("expected group_key default value=\"''\", got %q", defaultVal)
	}

	// Verify the pre-existing row has group_key = '' (the DEFAULT value).
	var groupKey string
	err := db.QueryRow("SELECT group_key FROM jobs WHERE id = ?", "pre-existing-job").Scan(&groupKey)
	if err != nil {
		t.Fatalf("query group_key for pre-existing job failed: %v", err)
	}
	if groupKey != "" {
		t.Errorf("expected group_key='' for pre-existing row, got %q", groupKey)
	}
}

// ---------------------------------------------------------------------------
// TS-12-1 Edge Case (12-REQ-1.E1): If the jobs table already has a group_key
// column from a previous partial migration, MigrateGroupKey skips the column
// addition without error (idempotent).
// ---------------------------------------------------------------------------

func TestMigrateGroupKey_Idempotent(t *testing.T) {
	db := openTestDB(t)
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}

	// First migration call.
	if err := MigrateGroupKey(db); err != nil {
		t.Fatalf("first MigrateGroupKey() returned error: %v", err)
	}

	// Second migration call should succeed without error.
	if err := MigrateGroupKey(db); err != nil {
		t.Fatalf("second MigrateGroupKey() returned error: %v", err)
	}

	// Verify the column still exists after the second call.
	_, _, _, found := queryColumnMeta(t, db, "jobs", "group_key")
	if !found {
		t.Fatal("group_key column not found after second MigrateGroupKey() call")
	}
}

// ---------------------------------------------------------------------------
// TS-12-2: Enqueuing a job with a non-empty Group value stores that value
// in the group_key column of the inserted job row.
//
// Requirement: 12-REQ-1.2
// ---------------------------------------------------------------------------

func TestEnqueue_GroupValueStoredInGroupKey(t *testing.T) {
	q, db := newTestQueueWithMigration(t)
	registerTestHandler(t, q, "merge")

	jobID, _, err := q.Enqueue(EnqueueParams{
		Type:        "merge",
		Key:         "ws1:main:feature/a",
		Nonce:       "n-group-test",
		Payload:     json.RawMessage(`{}`),
		SubmittedBy: "test-user",
		Group:       "ws1:main",
	})
	if err != nil {
		t.Fatalf("Enqueue() returned error: %v", err)
	}
	if jobID == "" {
		t.Fatal("Enqueue() returned empty job ID")
	}

	// Verify the group_key column in the database row.
	var groupKey string
	queryErr := db.QueryRow("SELECT group_key FROM jobs WHERE id = ?", jobID).Scan(&groupKey)
	if queryErr != nil {
		t.Fatalf("query group_key from jobs failed: %v", queryErr)
	}
	if groupKey != "ws1:main" {
		t.Errorf("expected group_key='ws1:main', got %q", groupKey)
	}
}

// ---------------------------------------------------------------------------
// TS-12-3: Enqueuing a job without a Group field stores an empty string in
// group_key, preserving existing dedup behavior via (type, key).
//
// Requirement: 12-REQ-1.3
// Correctness Property: 12-PROP-7 (backward compatible)
// ---------------------------------------------------------------------------

func TestEnqueue_EmptyGroupStoresEmptyGroupKey(t *testing.T) {
	q, db := newTestQueueWithMigration(t)
	registerTestHandler(t, q, "clone")

	jobID, _, err := q.Enqueue(EnqueueParams{
		Type:        "clone",
		Key:         "ws1:clone",
		Nonce:       "n-no-group",
		Payload:     json.RawMessage(`{}`),
		SubmittedBy: "test-user",
		// Group is intentionally omitted (empty string).
	})
	if err != nil {
		t.Fatalf("Enqueue() returned error: %v", err)
	}
	if jobID == "" {
		t.Fatal("Enqueue() returned empty job ID")
	}

	// Verify the group_key column is empty string.
	var groupKey string
	queryErr := db.QueryRow("SELECT group_key FROM jobs WHERE id = ?", jobID).Scan(&groupKey)
	if queryErr != nil {
		t.Fatalf("query group_key from jobs failed: %v", queryErr)
	}
	if groupKey != "" {
		t.Errorf("expected group_key='', got %q", groupKey)
	}
}

// ---------------------------------------------------------------------------
// TS-12-4: Worker dispatch query uses CASE WHEN group_key != '' THEN
// group_key ELSE key END as the serialization key, blocking a second job
// of the same type and group_key while one is running.
//
// Requirement: 12-REQ-1.4
// Correctness Property: 12-PROP-1 (at most one merge per target branch)
// Edge Case: 12-REQ-1.E2
// ---------------------------------------------------------------------------

func TestDispatch_GroupKeyBlocksSameGroup(t *testing.T) {
	q, db := newTestQueueWithMigration(t,
		WithWorkers(2),
		WithPollInterval(50*time.Millisecond),
	)

	// Handler that blocks until released, so we can observe the running state.
	j1Running := make(chan struct{}, 1)
	blockCh := make(chan struct{})
	firstCall := true
	handler := func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		if firstCall {
			firstCall = false
			j1Running <- struct{}{}
			<-blockCh // Block j1's handler so it stays in running status.
		}
		return nil, false, nil
	}
	if err := q.Register("merge", handler, nil); err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	// Seed two merge jobs with DIFFERENT keys but the SAME group_key.
	// With group serialization, only one should run at a time.
	now := time.Now()
	seedJobFullWithGroupKey(t, db,
		"j1", "merge", "ws1:main:feature/a", "n1", "queued", "ws1:main",
		0, now.Add(-2*time.Second), now.Add(-2*time.Second))
	seedJobFullWithGroupKey(t, db,
		"j2", "merge", "ws1:main:feature/b", "n2", "queued", "ws1:main",
		0, now.Add(-1*time.Second), now.Add(-1*time.Second))

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer func() {
		close(blockCh) // Unblock j1's handler so workers can exit.
		q.Stop()
	}()

	// Wait for j1 to be claimed and its handler to start running.
	select {
	case <-j1Running:
		// j1 is now in running status.
	case <-time.After(5 * time.Second):
		t.Fatal("j1 handler was not called within timeout")
	}

	// Give workers a chance to attempt claiming j2.
	time.Sleep(200 * time.Millisecond)

	// j2 must still be queued: group serialization prevents claiming it
	// while j1 (same type + group_key) is running, even though j2 has a
	// different key.
	var j2Status string
	if err := db.QueryRow("SELECT status FROM jobs WHERE id=?", "j2").Scan(&j2Status); err != nil {
		t.Fatalf("query j2 status failed: %v", err)
	}
	if j2Status != "queued" {
		t.Errorf("expected j2 status='queued' (blocked by running j1 with same group_key), got %q", j2Status)
	}

	// Verify only one merge job with group_key='ws1:main' is running.
	var runningCount int
	err := db.QueryRow(
		"SELECT COUNT(*) FROM jobs WHERE type='merge' AND group_key='ws1:main' AND status='running'",
	).Scan(&runningCount)
	if err != nil {
		t.Fatalf("count running jobs failed: %v", err)
	}
	if runningCount != 1 {
		t.Errorf("expected exactly 1 running job with group_key='ws1:main', got %d", runningCount)
	}
}

// ---------------------------------------------------------------------------
// TS-12-5: Existing job types without group_key continue to serialize by
// (type, key) with no behavioral change after the migration.
//
// Requirement: 12-REQ-1.5
// Correctness Property: 12-PROP-7 (backward compatible)
// ---------------------------------------------------------------------------

func TestDispatch_BackwardCompatNoGroupKey(t *testing.T) {
	q, db := newTestQueueWithMigration(t,
		WithWorkers(2),
		WithPollInterval(50*time.Millisecond),
	)

	// Handler that blocks until released.
	j1Running := make(chan struct{}, 1)
	blockCh := make(chan struct{})
	firstCall := true
	handler := func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		if firstCall {
			firstCall = false
			j1Running <- struct{}{}
			<-blockCh
		}
		return nil, false, nil
	}
	if err := q.Register("clone", handler, nil); err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	// Seed two clone jobs with the SAME key and EMPTY group_key.
	// With the CASE WHEN expression, the effective serialization key
	// falls back to the key column (backward compatible).
	now := time.Now()
	seedJobFullWithGroupKey(t, db,
		"j1", "clone", "ws1:clone", "n1", "queued", "",
		0, now.Add(-2*time.Second), now.Add(-2*time.Second))
	seedJobFullWithGroupKey(t, db,
		"j2", "clone", "ws1:clone", "n2", "queued", "",
		0, now.Add(-1*time.Second), now.Add(-1*time.Second))

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer func() {
		close(blockCh)
		q.Stop()
	}()

	// Wait for j1 to be claimed.
	select {
	case <-j1Running:
	case <-time.After(5 * time.Second):
		t.Fatal("j1 handler was not called within timeout")
	}

	// Give workers a chance to attempt claiming j2.
	time.Sleep(200 * time.Millisecond)

	// j2 must still be queued: per-key serialization (fallback when
	// group_key='') blocks it while j1 (same type+key) is running.
	var j2Status string
	if err := db.QueryRow("SELECT status FROM jobs WHERE id=?", "j2").Scan(&j2Status); err != nil {
		t.Fatalf("query j2 status failed: %v", err)
	}
	if j2Status != "queued" {
		t.Errorf("expected j2 status='queued' (blocked by running j1 with same key), got %q", j2Status)
	}

	// Verify only one clone job is running.
	var runningCount int
	err := db.QueryRow(
		"SELECT COUNT(*) FROM jobs WHERE type='clone' AND key='ws1:clone' AND status='running'",
	).Scan(&runningCount)
	if err != nil {
		t.Fatalf("count running jobs failed: %v", err)
	}
	if runningCount != 1 {
		t.Errorf("expected exactly 1 running clone job, got %d", runningCount)
	}
}
