package jobqueue

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TS-10-19: Backoff delay is computed as min(base * multiplier^retry_count, cap)
// with retry_count already incremented; first failure produces 4s, second 8s,
// third 16s with default policy.
// Requirement: 10-REQ-6.1
// Property: 10-PROP-5 (backoff delay is non-decreasing and capped)
// ---------------------------------------------------------------------------

func TestBackoff_DefaultPolicyDelays(t *testing.T) {
	policy := RetryPolicy{
		Base:       2 * time.Second,
		Multiplier: 2,
		Cap:        7200 * time.Second,
		MaxRetries: 20,
	}

	tests := []struct {
		retryCount int
		expected   time.Duration
	}{
		{1, 4 * time.Second},     // 2s * 2^1 = 4s
		{2, 8 * time.Second},     // 2s * 2^2 = 8s
		{3, 16 * time.Second},    // 2s * 2^3 = 16s
		{4, 32 * time.Second},    // 2s * 2^4 = 32s
		{5, 64 * time.Second},    // 2s * 2^5 = 64s
		{11, 4096 * time.Second}, // 2s * 2^11 = 4096s (still under cap)
		{12, 7200 * time.Second}, // 2s * 2^12 = 8192s, capped at 7200s
		{20, 7200 * time.Second}, // capped at 7200s
	}

	for _, tt := range tests {
		got := computeBackoff(policy, tt.retryCount)
		if got != tt.expected {
			t.Errorf("computeBackoff(default, retryCount=%d) = %v, want %v",
				tt.retryCount, got, tt.expected)
		}
	}

	// Property check: delays are non-decreasing.
	prev := computeBackoff(policy, 1)
	for rc := 2; rc <= 25; rc++ {
		curr := computeBackoff(policy, rc)
		if curr < prev {
			t.Errorf("backoff at retryCount=%d (%v) < retryCount=%d (%v); "+
				"delays must be non-decreasing", rc, curr, rc-1, prev)
		}
		if curr > policy.Cap {
			t.Errorf("backoff at retryCount=%d (%v) exceeds cap (%v)",
				rc, curr, policy.Cap)
		}
		prev = curr
	}
}

// ---------------------------------------------------------------------------
// TS-10-20: Per-type retry policy overrides are applied when registered;
// unspecified fields fall back to defaults.
// Requirement: 10-REQ-6.2
// ---------------------------------------------------------------------------

func TestBackoff_PerTypePolicyOverrides(t *testing.T) {
	q, _ := newTestQueue(t)

	handler := func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		return nil, false, nil
	}

	// Register with only Base specified; other fields should use defaults.
	if err := q.Register("custom", handler, &RetryPolicy{Base: 10 * time.Second}); err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	policy := q.getPolicy("custom")

	if policy.Base != 10*time.Second {
		t.Errorf("expected Base=10s, got %v", policy.Base)
	}
	if policy.Multiplier != 2.0 {
		t.Errorf("expected Multiplier=2 (default), got %v", policy.Multiplier)
	}
	if policy.Cap != 7200*time.Second {
		t.Errorf("expected Cap=7200s (default), got %v", policy.Cap)
	}
	if policy.MaxRetries != 20 {
		t.Errorf("expected MaxRetries=20 (default), got %d", policy.MaxRetries)
	}

	// Verify the computed delays use the overridden Base.
	// retryCount=1: min(10s * 2^1, 7200s) = 20s
	delay := computeBackoff(policy, 1)
	if delay != 20*time.Second {
		t.Errorf("computeBackoff(custom, 1) = %v, want 20s", delay)
	}
}

// ---------------------------------------------------------------------------
// TS-10-21: The promote step transitions a job from failed to queued when its
// available_at has passed.
// Requirement: 10-REQ-6.3
// ---------------------------------------------------------------------------

func TestBackoff_PromoteFailedToQueued(t *testing.T) {
	q, db := newTestQueueWithOpts(t,
		WithWorkers(1),
		WithPollInterval(50*time.Millisecond),
	)

	// Register a handler that succeeds on execution.
	handler := func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		return map[string]string{"ok": "true"}, false, nil
	}
	if err := q.Register("sync", handler, nil); err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	// Seed a failed job whose available_at has already elapsed (5s in the past).
	// The promote step should transition it back to queued, then the worker
	// should claim and complete it.
	now := time.Now()
	seedJobFull(t, db, "j1", "sync", "k1", "n1", "failed",
		1, now.Add(-5*time.Second), now.Add(-10*time.Second))

	// Verify the initial status is failed.
	var initialStatus string
	if err := db.QueryRow("SELECT status FROM jobs WHERE id=?", "j1").Scan(&initialStatus); err != nil {
		t.Fatalf("query initial status failed: %v", err)
	}
	if initialStatus != "failed" {
		t.Fatalf("expected initial status='failed', got %q", initialStatus)
	}

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer q.Stop()

	// The failed job should be promoted to queued, then claimed and completed.
	waitForStatus(t, db, "j1", "completed", 5*time.Second)

	var status string
	if err := db.QueryRow("SELECT status FROM jobs WHERE id=?", "j1").Scan(&status); err != nil {
		t.Fatalf("query job status failed: %v", err)
	}
	if status != "completed" {
		t.Errorf("expected status='completed' after promote+execute, got %q", status)
	}
}

// ---------------------------------------------------------------------------
// TS-10-E19: When retry_count after increment equals max_retries+1, the job
// transitions to dead_letter instead of computing a backoff delay.
// Requirement: 10-REQ-6.E1
// Property: 10-PROP-4 (retry count monotonically increases until dead-letter)
// ---------------------------------------------------------------------------

func TestBackoff_ExhaustsRetriesTransitionsToDeadLetter(t *testing.T) {
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

	// Seed a queued job with retry_count already at max_retries (2).
	// The next retryable error increments to 3, which exceeds max_retries.
	now := time.Now()
	seedJobFull(t, db, "j1", "sync", "r42", "n1", "queued",
		2, now.Add(-1*time.Second), now.Add(-10*time.Second))

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer q.Stop()

	waitForStatus(t, db, "j1", "dead_letter", 5*time.Second)

	var status string
	var retryCount int
	if err := db.QueryRow(
		"SELECT status, retry_count FROM jobs WHERE id=?", "j1",
	).Scan(&status, &retryCount); err != nil {
		t.Fatalf("query job failed: %v", err)
	}
	if status != "dead_letter" {
		t.Errorf("expected status='dead_letter', got %q", status)
	}
	if retryCount != 3 {
		t.Errorf("expected retry_count=3 (incremented past max), got %d", retryCount)
	}
}

// ---------------------------------------------------------------------------
// TS-10-E20: When the computed backoff delay overflows a 64-bit integer before
// the cap is applied, the delay is clamped to the cap value without panicking
// or producing a negative delay.
// Requirement: 10-REQ-6.E2
// Property: 10-PROP-5
// ---------------------------------------------------------------------------

func TestBackoff_OverflowClampedToCap(t *testing.T) {
	policy := RetryPolicy{
		Base:       2 * time.Second,
		Multiplier: 1e18,
		Cap:        7200 * time.Second,
		MaxRetries: 200,
	}

	// retryCount=100 with multiplier=1e18 will overflow int64 before cap.
	delay := computeBackoff(policy, 100)

	if delay != 7200*time.Second {
		t.Errorf("expected delay=%v (capped), got %v", 7200*time.Second, delay)
	}
	if delay < 0 {
		t.Errorf("delay must be non-negative, got %v", delay)
	}
}

// ---------------------------------------------------------------------------
// TS-10-E21: When max_retries is configured as 0, the first failure
// immediately transitions the job to dead_letter without any retry attempt.
// Requirement: 10-REQ-6.E3
// ---------------------------------------------------------------------------

func TestBackoff_MaxRetriesZeroImmediateDeadLetter(t *testing.T) {
	q, db := newTestQueueWithOpts(t,
		WithWorkers(1),
		WithPollInterval(50*time.Millisecond),
	)

	policy := &RetryPolicy{MaxRetries: 0}
	handler := func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		return nil, true, errors.New("fail")
	}
	if err := q.Register("no-retry", handler, policy); err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	// Seed a queued job with retry_count=0.
	now := time.Now()
	seedJobFull(t, db, "j1", "no-retry", "k1", "n1", "queued",
		0, now.Add(-1*time.Second), now.Add(-2*time.Second))

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer q.Stop()

	// First failure should immediately dead-letter (retry_count increments
	// to 1, which exceeds max_retries=0).
	waitForStatus(t, db, "j1", "dead_letter", 5*time.Second)

	var status string
	var retryCount int
	if err := db.QueryRow(
		"SELECT status, retry_count FROM jobs WHERE id=?", "j1",
	).Scan(&status, &retryCount); err != nil {
		t.Fatalf("query job failed: %v", err)
	}
	if status != "dead_letter" {
		t.Errorf("expected status='dead_letter', got %q", status)
	}
	if retryCount != 1 {
		t.Errorf("expected retry_count=1 (incremented once before dead-letter), got %d", retryCount)
	}
}
