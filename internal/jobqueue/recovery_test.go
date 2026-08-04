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

	if err := q.Start(); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	defer q.Stop()

	// Verify j1 was reset to queued.
	var j1Status string
	var j1AvailableAt string
	if err := db.QueryRow(
		"SELECT status, available_at FROM jobs WHERE id=?", "j1",
	).Scan(&j1Status, &j1AvailableAt); err != nil {
		t.Fatalf("query j1 failed: %v", err)
	}
	if j1Status != "queued" {
		t.Errorf("expected j1 status='queued' after crash recovery, got %q", j1Status)
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
	var j2Status string
	var j2AvailableAt string
	if err := db.QueryRow(
		"SELECT status, available_at FROM jobs WHERE id=?", "j2",
	).Scan(&j2Status, &j2AvailableAt); err != nil {
		t.Fatalf("query j2 failed: %v", err)
	}
	if j2Status != "queued" {
		t.Errorf("expected j2 status='queued' after crash recovery, got %q", j2Status)
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

	baseline := runtime.NumGoroutine()

	err := q.Start()
	if err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	defer q.Stop()

	// Allow goroutines to be scheduled.
	runtime.Gosched()
	time.Sleep(100 * time.Millisecond)

	// Verify worker goroutines started.
	after := runtime.NumGoroutine()
	delta := after - baseline
	if delta < 2 {
		t.Errorf("expected at least 2 new goroutines for WithWorkers(2), "+
			"got delta=%d (before=%d, after=%d)", delta, baseline, after)
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

