package jobqueue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TS-10-SMOKE-1: End-to-end smoke test: a consumer enqueues a merge job, a
// worker claims and executes it successfully, and the consumer reads the
// completed result via GetByID.
//
// Execution Path: 10-PATH-1
// Requirements: 10-REQ-1.1, 10-REQ-2.1, 10-REQ-3.1, 10-REQ-4.1, 10-REQ-4.2,
//               10-REQ-5.1, 10-REQ-11.1, 10-REQ-14.1, 10-REQ-15.1, 10-REQ-15.2
// ---------------------------------------------------------------------------

func TestSMOKE_EnqueueClaimCompleteGetByID(t *testing.T) {
	// Create a Queue with an in-memory SQLite DB, 1 worker, short poll interval.
	q, db := newTestQueueWithOpts(t,
		WithWorkers(1),
		WithPollInterval(50*time.Millisecond),
	)

	// Register a merge handler that returns a result.
	mergeResult := map[string]string{"branch": "main", "merged": "true"}
	handler := func(ctx context.Context, payload json.RawMessage) (any, bool, error) {
		if ctx == nil {
			t.Error("handler received nil context")
		}
		// Verify the payload received matches what was enqueued.
		var p map[string]string
		if err := json.Unmarshal(payload, &p); err != nil {
			t.Errorf("handler failed to unmarshal payload: %v", err)
		}
		if p["branch"] != "main" {
			t.Errorf("expected payload branch='main', got %q", p["branch"])
		}
		return mergeResult, false, nil
	}
	if err := q.Register("merge", handler, nil); err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	// Start the queue (runs crash recovery, launches workers).
	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer q.Stop()

	// Enqueue a job.
	payload := json.RawMessage(`{"branch":"main"}`)
	jobID, duplicate, err := q.Enqueue(EnqueueParams{
		Type:        "merge",
		Key:         "main",
		Nonce:       "smoke-1-abc123",
		Payload:     payload,
		SubmittedBy: "user-1",
	})
	if err != nil {
		t.Fatalf("Enqueue() returned error: %v", err)
	}
	if jobID == "" {
		t.Fatal("Enqueue() returned empty job ID")
	}
	if duplicate {
		t.Error("Enqueue() returned duplicate=true for a fresh job")
	}

	// Verify a row exists with status='queued' immediately after Enqueue
	// (or the worker may have already claimed it — accept queued or running).
	var statusAfterEnqueue string
	if err := db.QueryRow("SELECT status FROM jobs WHERE id=?", jobID).Scan(&statusAfterEnqueue); err != nil {
		t.Fatalf("query job status after enqueue failed: %v", err)
	}
	if statusAfterEnqueue != "queued" && statusAfterEnqueue != "running" && statusAfterEnqueue != "completed" {
		t.Errorf("expected status queued/running/completed immediately after enqueue, got %q", statusAfterEnqueue)
	}

	// Wait for the job to complete.
	waitForStatus(t, db, jobID, "completed", 5*time.Second)

	// Use GetByID to read the completed job record.
	job, err := q.GetByID(jobID)
	if err != nil {
		t.Fatalf("GetByID() returned error: %v", err)
	}
	if job == nil {
		t.Fatal("GetByID() returned nil job")
	}
	if job.Status != "completed" {
		t.Errorf("expected status='completed', got %q", job.Status)
	}
	if job.Type != "merge" {
		t.Errorf("expected type='merge', got %q", job.Type)
	}
	if job.Key != "main" {
		t.Errorf("expected key='main', got %q", job.Key)
	}
	if job.SubmittedBy != "user-1" {
		t.Errorf("expected submitted_by='user-1', got %q", job.SubmittedBy)
	}

	// Verify the result column contains the serialized handler output.
	if job.Result == nil {
		t.Fatal("expected non-nil result")
	}
	var resultData map[string]string
	if err := json.Unmarshal(job.Result, &resultData); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if resultData["merged"] != "true" {
		t.Errorf("expected result merged='true', got %q", resultData["merged"])
	}
	if resultData["branch"] != "main" {
		t.Errorf("expected result branch='main', got %q", resultData["branch"])
	}

	// Verify the payload is stored correctly.
	if job.Payload == nil {
		t.Fatal("expected non-nil payload")
	}
	var payloadData map[string]string
	if err := json.Unmarshal(job.Payload, &payloadData); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if payloadData["branch"] != "main" {
		t.Errorf("expected payload branch='main', got %q", payloadData["branch"])
	}
}

// ---------------------------------------------------------------------------
// TS-10-SMOKE-2: End-to-end smoke test: a sync job fails with a retryable
// error, is backed off, promoted back to queued, and succeeds on the second
// attempt.
//
// Execution Path: 10-PATH-2
// Requirements: 10-REQ-3.1, 10-REQ-5.2, 10-REQ-6.1, 10-REQ-6.3, 10-REQ-5.1,
//               10-REQ-14.2
// ---------------------------------------------------------------------------

func TestSMOKE_RetryableErrorBackoffAndRecovery(t *testing.T) {
	q, db := newTestQueueWithOpts(t,
		WithWorkers(1),
		WithPollInterval(50*time.Millisecond),
	)

	// Channel-gated handler: signals after the first (failing) call so we can
	// inspect intermediate state while the backoff delay keeps the job in
	// "failed" status. The second call succeeds.
	var callCount atomic.Int32
	firstCallDone := make(chan struct{})
	handler := func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		n := callCount.Add(1)
		if n == 1 {
			defer func() { close(firstCallDone) }()
			return nil, true, errors.New("network timeout")
		}
		return map[string]string{"synced": "true"}, false, nil
	}

	// Register with a backoff long enough to observe the "failed" intermediate
	// state between the signal and promote. delay = base * multiplier^1 = 1s * 2 = 2s.
	policy := &RetryPolicy{
		Base:       1 * time.Second,
		Multiplier: 2,
		Cap:        5 * time.Second,
		MaxRetries: 5,
	}
	if err := q.Register("sync", handler, policy); err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer q.Stop()

	// Enqueue the job.
	jobID, _, err := q.Enqueue(EnqueueParams{
		Type:        "sync",
		Key:         "repo-42",
		Nonce:       "smoke-2-xyz789",
		Payload:     json.RawMessage(`{"repo":"42"}`),
		SubmittedBy: "system",
	})
	if err != nil {
		t.Fatalf("Enqueue() returned error: %v", err)
	}

	// Wait for the handler's first (failing) call to complete. The backoff
	// delay (2s) ensures the job stays in "failed" status while we inspect.
	select {
	case <-firstCallDone:
	case <-time.After(5 * time.Second):
		t.Fatal("handler first call did not complete within timeout")
	}

	// Small delay to let finalizeJob persist the status update.
	time.Sleep(100 * time.Millisecond)

	// Verify intermediate state: status=failed, retry_count=1, error set.
	var status string
	var retryCount int
	var errMsg sql.NullString
	if err := db.QueryRow(
		"SELECT status, retry_count, error FROM jobs WHERE id=?", jobID,
	).Scan(&status, &retryCount, &errMsg); err != nil {
		t.Fatalf("query job failed: %v", err)
	}
	if status != "failed" {
		t.Errorf("expected status='failed' after first attempt, got %q", status)
	}
	if retryCount != 1 {
		t.Errorf("expected retry_count=1 after first failure, got %d", retryCount)
	}
	if !errMsg.Valid || errMsg.String != "network timeout" {
		t.Errorf("expected error='network timeout', got %q", errMsg.String)
	}

	// Wait for the promote step to transition the job back to queued and the
	// worker to execute it successfully on the second attempt.
	waitForStatus(t, db, jobID, "completed", 10*time.Second)

	// Verify final state.
	job, err := q.GetByID(jobID)
	if err != nil {
		t.Fatalf("GetByID() returned error: %v", err)
	}
	if job.Status != "completed" {
		t.Errorf("expected status='completed', got %q", job.Status)
	}
	if job.RetryCount != 1 {
		t.Errorf("expected retry_count=1 (incremented once), got %d", job.RetryCount)
	}

	// Verify result was stored.
	if job.Result == nil {
		t.Fatal("expected non-nil result after successful retry")
	}
	var resultData map[string]string
	if err := json.Unmarshal(job.Result, &resultData); err != nil {
		t.Fatalf("failed to unmarshal result: %v", err)
	}
	if resultData["synced"] != "true" {
		t.Errorf("expected result synced='true', got %q", resultData["synced"])
	}

	// Verify handler was called exactly twice.
	if count := callCount.Load(); count != 2 {
		t.Errorf("expected handler called 2 times, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// TS-10-SMOKE-3: End-to-end smoke test: a job exhausts its retry limit, is
// dead-lettered, inspected via ListByType, and manually requeued via
// RequeueDeadLetter.
//
// Execution Path: 10-PATH-3
// Requirements: 10-REQ-3.1, 10-REQ-5.2, 10-REQ-5.3, 10-REQ-6.1, 10-REQ-6.E1,
//               10-REQ-7.1, 10-REQ-11.2, 10-REQ-14.2
// ---------------------------------------------------------------------------

func TestSMOKE_ExhaustRetriesDeadLetterAndRequeue(t *testing.T) {
	q, db := newTestQueueWithOpts(t,
		WithWorkers(1),
		WithPollInterval(50*time.Millisecond),
	)

	// Handler that always returns a retryable error, except after requeue.
	var callCount atomic.Int32
	handler := func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		n := callCount.Add(1)
		// Calls 1-3: retryable errors (will exhaust max_retries=2 by the 3rd call).
		// Call 4 (after requeue): success.
		if n <= 3 {
			return nil, true, errors.New("transient error")
		}
		return map[string]string{"ok": "true"}, false, nil
	}

	// Register with max_retries=2 and very short backoff for test speed.
	policy := &RetryPolicy{
		Base:       50 * time.Millisecond,
		Multiplier: 1, // constant delay for test predictability
		Cap:        100 * time.Millisecond,
		MaxRetries: 2,
	}
	if err := q.Register("flaky", handler, policy); err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer q.Stop()

	// Enqueue the job.
	jobID, _, err := q.Enqueue(EnqueueParams{
		Type:        "flaky",
		Key:         "target-1",
		Nonce:       "smoke-3-nonce1",
		Payload:     json.RawMessage(`{"action":"deploy"}`),
		SubmittedBy: "operator",
	})
	if err != nil {
		t.Fatalf("Enqueue() returned error: %v", err)
	}

	// The job should go through: queued → running → failed (retry_count=1) →
	// queued → running → failed (retry_count=2) → queued → running →
	// dead_letter (retry_count=3).
	waitForStatus(t, db, jobID, "dead_letter", 10*time.Second)

	// Verify retry_count was incremented to 3 (exceeds max_retries=2).
	var retryCount int
	if err := db.QueryRow(
		"SELECT retry_count FROM jobs WHERE id=?", jobID,
	).Scan(&retryCount); err != nil {
		t.Fatalf("query retry_count failed: %v", err)
	}
	if retryCount != 3 {
		t.Errorf("expected retry_count=3 after exhausting retries, got %d", retryCount)
	}

	// Inspect via ListByType with status filter.
	deadLettered, err := q.ListByType("flaky", ListOpts{Status: "dead_letter"})
	if err != nil {
		t.Fatalf("ListByType() returned error: %v", err)
	}
	if len(deadLettered) != 1 {
		t.Fatalf("expected 1 dead-lettered job, got %d", len(deadLettered))
	}
	if deadLettered[0].ID != jobID {
		t.Errorf("expected dead-lettered job ID=%q, got %q", jobID, deadLettered[0].ID)
	}
	if deadLettered[0].Error == "" {
		t.Error("expected non-empty error on dead-lettered job")
	}

	// Requeue the dead-lettered job.
	requeuedID, err := q.RequeueDeadLetter(jobID)
	if err != nil {
		t.Fatalf("RequeueDeadLetter() returned error: %v", err)
	}
	if requeuedID != jobID {
		t.Errorf("expected requeued job ID=%q, got %q", jobID, requeuedID)
	}

	// Verify retry_count was reset to 0 and status is queued.
	var statusAfterRequeue string
	var retryAfterRequeue int
	if err := db.QueryRow(
		"SELECT status, retry_count FROM jobs WHERE id=?", jobID,
	).Scan(&statusAfterRequeue, &retryAfterRequeue); err != nil {
		t.Fatalf("query after requeue failed: %v", err)
	}
	if statusAfterRequeue != "queued" && statusAfterRequeue != "running" && statusAfterRequeue != "completed" {
		t.Errorf("expected status queued/running/completed after requeue, got %q", statusAfterRequeue)
	}
	if retryAfterRequeue != 0 {
		t.Errorf("expected retry_count=0 after requeue, got %d", retryAfterRequeue)
	}

	// Wait for the worker to pick up the requeued job and complete it.
	waitForStatus(t, db, jobID, "completed", 5*time.Second)

	// Verify final state.
	job, err := q.GetByID(jobID)
	if err != nil {
		t.Fatalf("GetByID() returned error: %v", err)
	}
	if job.Status != "completed" {
		t.Errorf("expected final status='completed', got %q", job.Status)
	}
	if job.Result == nil {
		t.Fatal("expected non-nil result after successful requeue execution")
	}

	// Verify CountByStatus shows the expected distribution.
	counts, err := q.CountByStatus("flaky")
	if err != nil {
		t.Fatalf("CountByStatus() returned error: %v", err)
	}
	if counts["completed"] != 1 {
		t.Errorf("expected 1 completed job, got %d", counts["completed"])
	}
	// There should be no dead_letter jobs remaining.
	if counts["dead_letter"] != 0 {
		t.Errorf("expected 0 dead_letter jobs after requeue, got %d", counts["dead_letter"])
	}
}
