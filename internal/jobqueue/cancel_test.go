package jobqueue

import (
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// TS-10-30: CancelJob on a queued job transitions it to cancelled status,
// sets updated_at to now(), logs at DEBUG level, and returns nil.
// Requirement: 10-REQ-10.1
// ---------------------------------------------------------------------------

func TestCancel_QueuedJob(t *testing.T) {
	q, db := newTestQueue(t)
	registerTestHandler(t, q, "merge")

	// Seed a queued job.
	seedJob(t, db, "j1", "merge", "main", "n1", "queued")

	err := q.CancelJob("j1")
	if err != nil {
		t.Fatalf("CancelJob() returned error: %v", err)
	}

	// Verify the job status was updated to cancelled.
	var status string
	if err := db.QueryRow("SELECT status FROM jobs WHERE id=?", "j1").Scan(&status); err != nil {
		t.Fatalf("query job status failed: %v", err)
	}
	if status != "cancelled" {
		t.Errorf("expected status='cancelled', got %q", status)
	}

	// Verify updated_at was refreshed.
	var updatedAt string
	if err := db.QueryRow("SELECT updated_at FROM jobs WHERE id=?", "j1").Scan(&updatedAt); err != nil {
		t.Fatalf("query updated_at failed: %v", err)
	}
	if updatedAt == "" {
		t.Error("expected updated_at to be set")
	}
}

// ---------------------------------------------------------------------------
// TS-10-31: CancelJob on an already-cancelled job succeeds silently without
// modifying the record (idempotent) and returns nil.
// Requirement: 10-REQ-10.2
// ---------------------------------------------------------------------------

func TestCancel_AlreadyCancelled(t *testing.T) {
	q, db := newTestQueue(t)
	registerTestHandler(t, q, "merge")

	// Seed a cancelled job.
	seedJob(t, db, "j1", "merge", "main", "n1", "cancelled")

	// Capture updated_at before the call.
	var updatedBefore string
	if err := db.QueryRow("SELECT updated_at FROM jobs WHERE id=?", "j1").Scan(&updatedBefore); err != nil {
		t.Fatalf("query updated_at failed: %v", err)
	}

	err := q.CancelJob("j1")
	if err != nil {
		t.Fatalf("CancelJob() returned error for already-cancelled job: %v", err)
	}

	// Verify the job status is still cancelled.
	var status string
	if err := db.QueryRow("SELECT status FROM jobs WHERE id=?", "j1").Scan(&status); err != nil {
		t.Fatalf("query job status failed: %v", err)
	}
	if status != "cancelled" {
		t.Errorf("expected status='cancelled', got %q", status)
	}
}

// ---------------------------------------------------------------------------
// TS-10-32: CancelJob on a job in running, completed, or dead_letter status
// returns ErrNotCancellable without modifying the record.
// Requirement: 10-REQ-10.3
// ---------------------------------------------------------------------------

func TestCancel_NotCancellableStatuses(t *testing.T) {
	q, db := newTestQueue(t)
	registerTestHandler(t, q, "merge")

	// Seed jobs in each non-cancellable status.
	seedJob(t, db, "j-running", "merge", "main", "n1", "running")
	seedJob(t, db, "j-completed", "merge", "dev", "n2", "completed")
	seedJob(t, db, "j-dead", "merge", "feat", "n3", "dead_letter")

	tests := []struct {
		name string
		id   string
	}{
		{"running", "j-running"},
		{"completed", "j-completed"},
		{"dead_letter", "j-dead"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := q.CancelJob(tc.id)
			if !errors.Is(err, ErrNotCancellable) {
				t.Errorf("CancelJob(%q) expected ErrNotCancellable, got: %v", tc.id, err)
			}

			// Verify the job record was not modified.
			var status string
			if err := db.QueryRow("SELECT status FROM jobs WHERE id=?", tc.id).Scan(&status); err != nil {
				t.Fatalf("query job status failed: %v", err)
			}
			if status != tc.name {
				t.Errorf("expected status=%q (unchanged), got %q", tc.name, status)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TS-10-E30: CancelJob called with a job ID that does not exist returns an
// error indicating the job was not found.
// Requirement: 10-REQ-10.E1
// ---------------------------------------------------------------------------

func TestCancel_NotFound(t *testing.T) {
	q, _ := newTestQueue(t)
	registerTestHandler(t, q, "merge")

	err := q.CancelJob("nonexistent-id")
	if err == nil {
		t.Fatal("expected error for non-existent job ID, got nil")
	}

	// Should NOT be ErrNotCancellable — it's a not-found error.
	if errors.Is(err, ErrNotCancellable) {
		t.Error("expected a not-found error, not ErrNotCancellable")
	}
}

// ---------------------------------------------------------------------------
// TS-10-E31: When CancelJob is called on a queued job and a worker
// simultaneously claims the same job (transitions to running), the cancel
// UPDATE with WHERE status='queued' finds zero rows affected; CancelJob
// returns ErrNotCancellable since the job is now running.
// Requirement: 10-REQ-10.E2
// ---------------------------------------------------------------------------

func TestCancel_RaceWithWorkerClaim(t *testing.T) {
	q, db := newTestQueue(t)
	registerTestHandler(t, q, "merge")

	// Seed a queued job.
	seedJob(t, db, "j1", "merge", "main", "n1", "queued")

	// Simulate a worker claiming the job before CancelJob runs
	// by changing status to running directly.
	_, err := db.Exec("UPDATE jobs SET status='running' WHERE id='j1'")
	if err != nil {
		t.Fatalf("failed to simulate worker claim: %v", err)
	}

	// CancelJob should find the job is now running and return ErrNotCancellable.
	err = q.CancelJob("j1")
	if !errors.Is(err, ErrNotCancellable) {
		t.Errorf("CancelJob() after worker claim expected ErrNotCancellable, got: %v", err)
	}

	// Verify the job remains in running status.
	var status string
	if err := db.QueryRow("SELECT status FROM jobs WHERE id=?", "j1").Scan(&status); err != nil {
		t.Fatalf("query job status failed: %v", err)
	}
	if status != "running" {
		t.Errorf("expected status='running', got %q", status)
	}
}
