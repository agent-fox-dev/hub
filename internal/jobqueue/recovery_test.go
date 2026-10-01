package jobqueue

import (
	"bytes"
	"database/sql"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newTestQueueWithLogCapture is like newTestQueue but also captures log output
// into a buffer so tests can assert on log content (e.g., WARN messages).
func newTestQueueWithLogCapture(t *testing.T, opts ...Option) (*Queue, *sql.DB, *bytes.Buffer) {
	t.Helper()
	db := openTestDB(t)
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	q, err := New(db, logger, opts...)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	return q, db, &buf
}

// ---------------------------------------------------------------------------
// TS-10-25: On startup, the queue resets all jobs in running status to queued
// with available_at=now() and logs each reset at WARN level.
// Requirement: 10-REQ-8.1
// Property: 10-PROP-6 (crash recovery resets all running jobs)
// ---------------------------------------------------------------------------

func TestRecovery_ResetsRunningJobsOnStartup(t *testing.T) {
	q, db, logBuf := newTestQueueWithLogCapture(t)

	// Seed two jobs in running status (simulating a crash).
	now := time.Now()
	seedJobFull(t, db, "j1", "merge", "main", "n1", "running",
		0, now.Add(-5*time.Minute), now.Add(-10*time.Minute))
	seedJobFull(t, db, "j2", "sync", "repo-42", "n2", "running",
		1, now.Add(-3*time.Minute), now.Add(-8*time.Minute))

	// Also seed a completed job to verify it's NOT affected.
	seedJob(t, db, "j3", "merge", "dev", "n3", "completed")

	// Record every status transition. Start launches its workers right
	// after crash recovery, and they may claim a recovered job (and
	// dead-letter it, as no handler is registered) before the queries below
	// run, so a job's current status after Start is not deterministic.
	// Recovery runs inside Start before any worker exists, so each job's
	// first recorded transition is the one recovery made.
	recordStatusTransitions(t, db)

	if err := q.Start(); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	defer q.Stop()

	// Verify j1 was reset to queued.
	j1Old, j1Status, j1AvailableAt := firstStatusTransition(t, db, "j1")
	if j1Old != "running" || j1Status != "queued" {
		t.Errorf("expected j1 status 'running' -> 'queued' after crash recovery, got %q -> %q",
			j1Old, j1Status)
	}
	j1Avail, parseErr := time.Parse(time.RFC3339, j1AvailableAt)
	if parseErr != nil {
		t.Fatalf("failed to parse j1 available_at %q: %v", j1AvailableAt, parseErr)
	}
	if time.Since(j1Avail) > 5*time.Second {
		t.Errorf("j1 available_at should be approximately now, got %v (age=%v)",
			j1Avail, time.Since(j1Avail))
	}

	// Verify j2 was reset to queued.
	j2Old, j2Status, j2AvailableAt := firstStatusTransition(t, db, "j2")
	if j2Old != "running" || j2Status != "queued" {
		t.Errorf("expected j2 status 'running' -> 'queued' after crash recovery, got %q -> %q",
			j2Old, j2Status)
	}
	j2Avail, parseErr := time.Parse(time.RFC3339, j2AvailableAt)
	if parseErr != nil {
		t.Fatalf("failed to parse j2 available_at %q: %v", j2AvailableAt, parseErr)
	}
	if time.Since(j2Avail) > 5*time.Second {
		t.Errorf("j2 available_at should be approximately now, got %v (age=%v)",
			j2Avail, time.Since(j2Avail))
	}

	// Verify j3 (completed) was NOT affected.
	var j3Status string
	if err := db.QueryRow("SELECT status FROM jobs WHERE id=?", "j3").Scan(&j3Status); err != nil {
		t.Fatalf("query j3 failed: %v", err)
	}
	if j3Status != "completed" {
		t.Errorf("expected j3 (completed) to be unchanged, got %q", j3Status)
	}
	var j3Transitions int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM status_transitions WHERE job_id=?", "j3",
	).Scan(&j3Transitions); err != nil {
		t.Fatalf("query j3 transitions failed: %v", err)
	}
	if j3Transitions != 0 {
		t.Errorf("expected no status transitions for j3 (completed), got %d", j3Transitions)
	}

	// Verify WARN log lines were emitted for each reset job.
	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "WARN") {
		t.Error("expected WARN log lines for crash recovery resets")
	}
	if !strings.Contains(logOutput, "j1") {
		t.Errorf("expected WARN log to contain job_id 'j1', log output:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "j2") {
		t.Errorf("expected WARN log to contain job_id 'j2', log output:\n%s", logOutput)
	}
}

// recordStatusTransitions installs a trigger that appends every change of
// jobs.status (with the row's available_at at that moment) to a
// status_transitions table, in order.
func recordStatusTransitions(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, stmt := range []string{
		`CREATE TABLE status_transitions (
			seq          INTEGER PRIMARY KEY AUTOINCREMENT,
			job_id       TEXT NOT NULL,
			old_status   TEXT NOT NULL,
			new_status   TEXT NOT NULL,
			available_at TEXT NOT NULL
		)`,
		`CREATE TRIGGER record_status_transition
		AFTER UPDATE OF status ON jobs
		WHEN OLD.status IS NOT NEW.status
		BEGIN
			INSERT INTO status_transitions (job_id, old_status, new_status, available_at)
			VALUES (NEW.id, OLD.status, NEW.status, NEW.available_at);
		END`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("install status transition recorder: %v", err)
		}
	}
}

// firstStatusTransition returns the earliest status change recorded by
// recordStatusTransitions for the job, failing the test if there is none.
func firstStatusTransition(t *testing.T, db *sql.DB, jobID string) (oldStatus, newStatus, availableAt string) {
	t.Helper()
	if err := db.QueryRow(
		`SELECT old_status, new_status, available_at FROM status_transitions
		 WHERE job_id = ? ORDER BY seq LIMIT 1`, jobID,
	).Scan(&oldStatus, &newStatus, &availableAt); err != nil {
		t.Fatalf("query first status transition of %s failed: %v", jobID, err)
	}
	return oldStatus, newStatus, availableAt
}

// ---------------------------------------------------------------------------
// TS-10-26: On startup with no running jobs, the queue proceeds to start
// worker goroutines without any recovery actions.
// Requirement: 10-REQ-8.2
// ---------------------------------------------------------------------------

func TestRecovery_NoRunningJobsNoRecoveryActions(t *testing.T) {
	q, db, logBuf := newTestQueueWithLogCapture(t, WithWorkers(2))

	// Seed some jobs that are NOT running (should not trigger recovery).
	seedJob(t, db, "j1", "merge", "main", "n1", "completed")
	seedJob(t, db, "j2", "sync", "r42", "n2", "queued")

	// Count this queue's worker goroutines rather than the process-wide
	// runtime.NumGoroutine (see startedWorkerGoroutines).
	baseline := startedWorkerGoroutines(t)

	err := q.Start()
	if err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	defer q.Stop()

	// Verify worker goroutines started.
	if after := startedWorkerGoroutines(t); baseline != 0 || after != 2 {
		t.Errorf("expected exactly 2 worker goroutines for WithWorkers(2), "+
			"got before=%d, after=%d", baseline, after)
	}

	// Verify no WARN log lines about crash recovery were emitted.
	logOutput := logBuf.String()
	lines := strings.Split(logOutput, "\n")
	for _, line := range lines {
		if strings.Contains(line, "WARN") && strings.Contains(line, "recover") {
			t.Errorf("unexpected crash recovery WARN log line: %q", line)
		}
	}
}

// ---------------------------------------------------------------------------
// TS-10-E24: When the crash recovery database query fails on startup, Start
// returns a non-nil error and no worker goroutines are started.
// Requirement: 10-REQ-8.E1
// ---------------------------------------------------------------------------

func TestRecovery_DatabaseQueryFailure(t *testing.T) {
	// Open a database and initialize schema, then close it to simulate failure.
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("failed to set WAL mode: %v", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		t.Fatalf("failed to set busy_timeout: %v", err)
	}
	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema() failed: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	q, newErr := New(db, logger)
	if newErr != nil {
		t.Fatalf("New() returned error: %v", newErr)
	}

	// Close the database to cause the recovery query to fail.
	db.Close()

	baseline := runtime.NumGoroutine()

	startErr := q.Start()
	if startErr == nil {
		q.Stop()
		t.Fatal("expected Start() to return error with closed database, got nil")
	}

	// Verify no worker goroutines were started.
	runtime.Gosched()
	time.Sleep(100 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > baseline {
		t.Errorf("expected no new goroutines after failed Start, got delta=%d",
			after-baseline)
	}
}

// ---------------------------------------------------------------------------
// TS-10-E25: Handler idempotency requirement is documented in code comments;
// crash recovery may re-dispatch a job whose handler had already partially
// executed.
// Requirement: 10-REQ-8.E2
// ---------------------------------------------------------------------------

func TestRecovery_IdempotencyDocumented(t *testing.T) {
	// This test verifies that the crash recovery code or its surrounding
	// comments document the requirement for handler idempotency.
	//
	// Since crash recovery resets running jobs to queued, a handler that
	// was interrupted mid-execution will be re-invoked. The code must
	// document that handlers must be idempotent or tolerate re-execution.

	content, err := os.ReadFile("jobqueue.go")
	if err != nil {
		t.Fatalf("failed to read jobqueue.go: %v", err)
	}

	src := string(content)
	if !strings.Contains(strings.ToLower(src), "idempotent") {
		t.Error("expected source code to document handler idempotency requirement " +
			"(word 'idempotent' not found in jobqueue.go)")
	}
}
