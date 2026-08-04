package jobqueue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TS-10-15: When a handler returns (result, false, nil), the worker updates
// the job to completed status, stores the serialized result, and logs at
// DEBUG level.
// Requirement: 10-REQ-5.1
// ---------------------------------------------------------------------------

func TestHandler_SuccessfulCompletion(t *testing.T) {
	q, db := newTestQueueWithOpts(t,
		WithWorkers(1),
		WithPollInterval(50*time.Millisecond),
	)

	handler := func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		return map[string]string{"merged": "true"}, false, nil
	}
	if err := q.Register("merge", handler, nil); err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	// Seed a queued job.
	now := time.Now()
	seedJobFull(t, db, "j1", "merge", "k", "n1", "queued",
		0, now.Add(-1*time.Second), now.Add(-2*time.Second))

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer q.Stop()

	waitForStatus(t, db, "j1", "completed", 5*time.Second)

	// Verify the result was stored as JSON.
	var status string
	var result sql.NullString
	var updatedAt string
	err := db.QueryRow(
		"SELECT status, result, updated_at FROM jobs WHERE id=?", "j1",
	).Scan(&status, &result, &updatedAt)
	if err != nil {
		t.Fatalf("query job failed: %v", err)
	}
	if status != "completed" {
		t.Errorf("expected status='completed', got %q", status)
	}
	if !result.Valid {
		t.Fatal("expected non-null result column")
	}
	if result.String != `{"merged":"true"}` {
		t.Errorf("expected result={\"merged\":\"true\"}, got %q", result.String)
	}
	if updatedAt == "" {
		t.Error("expected updated_at to be set")
	}
}

// ---------------------------------------------------------------------------
// TS-10-16: When a handler returns a retryable error and retry_count <
// max_retries, the worker increments retry_count, computes backoff delay,
// sets status=failed, and stores the error message.
// Requirement: 10-REQ-5.2
// ---------------------------------------------------------------------------

func TestHandler_RetryableError(t *testing.T) {
	q, db := newTestQueueWithOpts(t,
		WithWorkers(1),
		WithPollInterval(50*time.Millisecond),
	)

	policy := &RetryPolicy{
		Base:       2 * time.Second,
		Multiplier: 2,
		Cap:        7200 * time.Second,
		MaxRetries: 20,
	}
	handler := func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		return nil, true, errors.New("network timeout")
	}
	if err := q.Register("sync", handler, policy); err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	// Seed a queued job with retry_count=0.
	now := time.Now()
	seedJobFull(t, db, "j1", "sync", "r42", "n1", "queued",
		0, now.Add(-1*time.Second), now.Add(-2*time.Second))

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer q.Stop()

	waitForStatus(t, db, "j1", "failed", 5*time.Second)

	var status string
	var retryCount int
	var errMsg sql.NullString
	var availableAt string
	err := db.QueryRow(
		"SELECT status, retry_count, error, available_at FROM jobs WHERE id=?", "j1",
	).Scan(&status, &retryCount, &errMsg, &availableAt)
	if err != nil {
		t.Fatalf("query job failed: %v", err)
	}

	if status != "failed" {
		t.Errorf("expected status='failed', got %q", status)
	}
	if retryCount != 1 {
		t.Errorf("expected retry_count=1, got %d", retryCount)
	}
	if !errMsg.Valid || errMsg.String != "network timeout" {
		t.Errorf("expected error='network timeout', got %q (valid=%v)", errMsg.String, errMsg.Valid)
	}

	// Verify backoff: delay = min(base * multiplier^retry_count, cap)
	//                       = min(2s * 2^1, 7200s) = 4s
	avail, parseErr := time.Parse(time.RFC3339, availableAt)
	if parseErr != nil {
		t.Fatalf("failed to parse available_at %q: %v", availableAt, parseErr)
	}
	// available_at should be approximately now + 4s (within 2s tolerance).
	expectedMin := time.Now().Add(2 * time.Second)
	expectedMax := time.Now().Add(6 * time.Second)
	if avail.Before(expectedMin) || avail.After(expectedMax) {
		t.Errorf("expected available_at approximately now+4s, got %v (now=%v)",
			avail, time.Now())
	}
}

// ---------------------------------------------------------------------------
// TS-10-17: When a handler returns a retryable error and retry_count >=
// max_retries after increment, the worker transitions the job to dead_letter
// and logs at WARN level.
// Requirement: 10-REQ-5.3
// ---------------------------------------------------------------------------

func TestHandler_RetryableErrorExhaustsRetries(t *testing.T) {
	q, db := newTestQueueWithOpts(t,
		WithWorkers(1),
		WithPollInterval(50*time.Millisecond),
	)

	policy := &RetryPolicy{MaxRetries: 2}
	handler := func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		return nil, true, errors.New("still failing")
	}
	if err := q.Register("sync", handler, policy); err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	// Seed a queued job with retry_count already at max (2).
	// The next retryable error should push it to dead_letter.
	now := time.Now()
	seedJobFull(t, db, "j1", "sync", "r42", "n1", "queued",
		2, now.Add(-1*time.Second), now.Add(-10*time.Second))

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer q.Stop()

	waitForStatus(t, db, "j1", "dead_letter", 5*time.Second)

	var status string
	var errMsg sql.NullString
	err := db.QueryRow(
		"SELECT status, error FROM jobs WHERE id=?", "j1",
	).Scan(&status, &errMsg)
	if err != nil {
		t.Fatalf("query job failed: %v", err)
	}
	if status != "dead_letter" {
		t.Errorf("expected status='dead_letter', got %q", status)
	}
	if !errMsg.Valid || errMsg.String != "still failing" {
		t.Errorf("expected error='still failing', got %q (valid=%v)", errMsg.String, errMsg.Valid)
	}
}

// ---------------------------------------------------------------------------
// TS-10-18: When a handler returns a permanent (non-retryable) error, the
// worker transitions the job directly to dead_letter without incrementing
// retry_count.
// Requirement: 10-REQ-5.4
// ---------------------------------------------------------------------------

func TestHandler_PermanentError(t *testing.T) {
	q, db := newTestQueueWithOpts(t,
		WithWorkers(1),
		WithPollInterval(50*time.Millisecond),
	)

	handler := func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		return nil, false, errors.New("permanent failure")
	}
	if err := q.Register("merge", handler, nil); err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	// Seed a queued job with retry_count=0.
	now := time.Now()
	seedJobFull(t, db, "j1", "merge", "k", "n1", "queued",
		0, now.Add(-1*time.Second), now.Add(-2*time.Second))

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer q.Stop()

	waitForStatus(t, db, "j1", "dead_letter", 5*time.Second)

	var status string
	var retryCount int
	var errMsg sql.NullString
	err := db.QueryRow(
		"SELECT status, retry_count, error FROM jobs WHERE id=?", "j1",
	).Scan(&status, &retryCount, &errMsg)
	if err != nil {
		t.Fatalf("query job failed: %v", err)
	}
	if status != "dead_letter" {
		t.Errorf("expected status='dead_letter', got %q", status)
	}
	if retryCount != 0 {
		t.Errorf("expected retry_count=0 (not incremented for permanent error), got %d", retryCount)
	}
	if !errMsg.Valid || errMsg.String != "permanent failure" {
		t.Errorf("expected error='permanent failure', got %q (valid=%v)", errMsg.String, errMsg.Valid)
	}
}
