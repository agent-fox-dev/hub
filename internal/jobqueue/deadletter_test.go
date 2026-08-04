package jobqueue

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TS-10-22: RequeueDeadLetter on a dead_letter job with no active (type, key)
// job resets retry_count to 0, sets status=queued, available_at=now(), and
// sends a wakeup signal.
// Requirement: 10-REQ-7.1
// Property: 10-PROP-7 (only explicit RequeueDeadLetter moves dead_letter back)
// ---------------------------------------------------------------------------

func TestDeadLetter_RequeueSuccessful(t *testing.T) {
	q, db := newTestQueue(t)
	registerTestHandler(t, q, "merge")

	// Seed a dead-lettered job with retry_count=5.
	seedJobFull(t, db, "j1", "merge", "main", "n1", "dead_letter",
		5, time.Now().Add(-1*time.Hour), time.Now().Add(-2*time.Hour))

	// Capture updated_at before requeue.
	var updatedBefore string
	if err := db.QueryRow("SELECT updated_at FROM jobs WHERE id=?", "j1").Scan(&updatedBefore); err != nil {
		t.Fatalf("query updated_at failed: %v", err)
	}

	jobID, err := q.RequeueDeadLetter("j1")
	if err != nil {
		t.Fatalf("RequeueDeadLetter() returned error: %v", err)
	}
	if jobID != "j1" {
		t.Errorf("expected jobID='j1', got %q", jobID)
	}

	// Verify database state after requeue.
	var status string
	var retryCount int
	var availableAt, updatedAt string
	if err := db.QueryRow(
		"SELECT status, retry_count, available_at, updated_at FROM jobs WHERE id=?", "j1",
	).Scan(&status, &retryCount, &availableAt, &updatedAt); err != nil {
		t.Fatalf("query job after requeue failed: %v", err)
	}

	if status != "queued" {
		t.Errorf("expected status='queued', got %q", status)
	}
	if retryCount != 0 {
		t.Errorf("expected retry_count=0, got %d", retryCount)
	}

	// available_at should be approximately now (within 5s).
	avail, parseErr := time.Parse(time.RFC3339, availableAt)
	if parseErr != nil {
		t.Fatalf("failed to parse available_at %q: %v", availableAt, parseErr)
	}
	if time.Since(avail) > 5*time.Second {
		t.Errorf("expected available_at <= now, got %v (age=%v)", avail, time.Since(avail))
	}

	// updated_at should have changed.
	if updatedAt == updatedBefore {
		t.Error("expected updated_at to be refreshed after requeue")
	}
}

// ---------------------------------------------------------------------------
// TS-10-23: RequeueDeadLetter is rejected when an active (queued or running)
// job already exists for the same (type, key), returning the existing active
// job's ID and a non-nil error.
// Requirement: 10-REQ-7.2
// Property: 10-PROP-1 (at-most-one-active-per-key invariant)
// ---------------------------------------------------------------------------

func TestDeadLetter_RequeueRejectedActiveJobExists(t *testing.T) {
	q, db := newTestQueue(t)
	registerTestHandler(t, q, "merge")

	// Seed a dead-lettered job and an active queued job with the same (type, key).
	now := time.Now()
	seedJobFull(t, db, "j1", "merge", "main", "n1", "dead_letter",
		3, now.Add(-1*time.Hour), now.Add(-2*time.Hour))
	seedJobFull(t, db, "j2", "merge", "main", "n2", "queued",
		0, now, now)

	existingID, err := q.RequeueDeadLetter("j1")
	if err == nil {
		t.Fatal("expected error when active job exists for same (type, key), got nil")
	}

	if existingID != "j2" {
		t.Errorf("expected existingID='j2' (the active job), got %q", existingID)
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "active") && !strings.Contains(errMsg, "duplicate") {
		t.Errorf("error should mention 'active' or 'duplicate', got: %q", errMsg)
	}

	// j1 should remain in dead_letter status.
	var j1Status string
	if err := db.QueryRow("SELECT status FROM jobs WHERE id=?", "j1").Scan(&j1Status); err != nil {
		t.Fatalf("query j1 status failed: %v", err)
	}
	if j1Status != "dead_letter" {
		t.Errorf("j1 should remain in dead_letter, got %q", j1Status)
	}
}

// ---------------------------------------------------------------------------
// TS-10-24: RequeueDeadLetter called for a job not in dead_letter status
// returns an empty string and a non-nil error.
// Requirement: 10-REQ-7.3
// ---------------------------------------------------------------------------

func TestDeadLetter_RequeueNonDeadLetterJob(t *testing.T) {
	q, db := newTestQueue(t)
	registerTestHandler(t, q, "merge")

	// Seed a completed job.
	seedJob(t, db, "j1", "merge", "main", "n1", "completed")

	jobID, err := q.RequeueDeadLetter("j1")
	if err == nil {
		t.Fatal("expected error for non-dead_letter job, got nil")
	}
	if jobID != "" {
		t.Errorf("expected empty jobID, got %q", jobID)
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "not in dead_letter") && !strings.Contains(errMsg, "invalid status") {
		t.Errorf("error should mention 'not in dead_letter' or 'invalid status', got: %q", errMsg)
	}

	// Job should remain unchanged.
	var status string
	if err := db.QueryRow("SELECT status FROM jobs WHERE id=?", "j1").Scan(&status); err != nil {
		t.Fatalf("query job status failed: %v", err)
	}
	if status != "completed" {
		t.Errorf("job should remain in completed, got %q", status)
	}
}

// ---------------------------------------------------------------------------
// TS-10-E22: RequeueDeadLetter called with a non-existent job ID returns
// ("", non-nil error) with a not-found message.
// Requirement: 10-REQ-7.E1
// ---------------------------------------------------------------------------

func TestDeadLetter_RequeueNotFound(t *testing.T) {
	q, _ := newTestQueue(t)
	registerTestHandler(t, q, "merge")

	jobID, err := q.RequeueDeadLetter("nonexistent-id")
	if err == nil {
		t.Fatal("expected error for non-existent job ID, got nil")
	}
	if jobID != "" {
		t.Errorf("expected empty jobID, got %q", jobID)
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "not found") {
		t.Errorf("error should mention 'not found', got: %q", errMsg)
	}
}

// ---------------------------------------------------------------------------
// TS-10-E23: Two concurrent RequeueDeadLetter calls for the same dead-lettered
// job result in exactly one success; the other observes the newly queued job
// as a duplicate active job.
// Requirement: 10-REQ-7.E2
// Property: 10-PROP-1 (at-most-one-active-per-key invariant)
// ---------------------------------------------------------------------------

func TestDeadLetter_ConcurrentRequeue(t *testing.T) {
	q, db := newTestQueue(t)
	registerTestHandler(t, q, "merge")

	// Seed a dead-lettered job with no active job for the same (type, key).
	seedJobFull(t, db, "j1", "merge", "main", "n1", "dead_letter",
		3, time.Now().Add(-1*time.Hour), time.Now().Add(-2*time.Hour))

	type result struct {
		jobID string
		err   error
	}

	results := make([]result, 2)
	var wg sync.WaitGroup
	wg.Add(2)

	for i := 0; i < 2; i++ {
		go func(idx int) {
			defer wg.Done()
			id, err := q.RequeueDeadLetter("j1")
			results[idx] = result{jobID: id, err: err}
		}(i)
	}
	wg.Wait()

	var successes, failures int
	for _, r := range results {
		if r.err == nil {
			successes++
		} else {
			failures++
		}
	}

	if successes != 1 {
		t.Errorf("expected exactly 1 successful requeue, got %d", successes)
	}
	if failures != 1 {
		t.Errorf("expected exactly 1 failed requeue, got %d", failures)
	}

	// Verify exactly one queued job exists for (merge, main).
	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM jobs WHERE type='merge' AND key='main' AND status='queued'",
	).Scan(&count); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 queued job for (merge, main), got %d", count)
	}
}
