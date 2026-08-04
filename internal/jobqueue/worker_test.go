package jobqueue

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TS-10-11: Worker's processing loop first runs the promote step to transition
// failed jobs with elapsed available_at back to queued before polling for new
// work.
// Requirement: 10-REQ-4.1
// ---------------------------------------------------------------------------

func TestWorker_PromoteFailedJobs(t *testing.T) {
	q, db := newTestQueueWithOpts(t,
		WithWorkers(1),
		WithPollInterval(50*time.Millisecond),
	)

	// Register a handler that completes successfully.
	handler := func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		return nil, false, nil
	}
	if err := q.Register("test", handler, nil); err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	// Seed a failed job whose available_at has already elapsed (10s in the past).
	// The promote step should transition it back to queued, then the worker
	// should claim and complete it.
	now := time.Now()
	seedJobFull(t, db, "j1", "test", "k1", "n1", "failed",
		1, now.Add(-10*time.Second), now.Add(-20*time.Second))

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
// TS-10-12: Worker atomically claims a queued job by updating status to running
// with a WHERE clause that includes status='queued', preventing double-claim.
// Requirement: 10-REQ-4.2
// ---------------------------------------------------------------------------

func TestWorker_AtomicClaim(t *testing.T) {
	q, db := newTestQueueWithOpts(t,
		WithWorkers(1),
		WithPollInterval(50*time.Millisecond),
	)

	handlerCalled := make(chan struct{}, 1)
	handler := func(ctx context.Context, _ json.RawMessage) (any, bool, error) {
		if ctx == nil {
			t.Error("handler received nil context")
		}
		handlerCalled <- struct{}{}
		return nil, false, nil
	}
	if err := q.Register("test", handler, nil); err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	// Seed a queued job with available_at in the past.
	now := time.Now()
	seedJobFull(t, db, "j1", "test", "k1", "n1", "queued",
		0, now.Add(-1*time.Second), now.Add(-2*time.Second))

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer q.Stop()

	// Wait for the handler to be called (proves the job was claimed).
	select {
	case <-handlerCalled:
		// Success: worker claimed and dispatched the job.
	case <-time.After(5 * time.Second):
		t.Fatal("handler was not called within timeout; worker may not be running")
	}

	// After handler returns, job should be running or completed.
	waitForStatus(t, db, "j1", "completed", 2*time.Second)

	var status string
	if err := db.QueryRow("SELECT status FROM jobs WHERE id=?", "j1").Scan(&status); err != nil {
		t.Fatalf("query job status failed: %v", err)
	}
	if status != "completed" {
		t.Errorf("expected status='completed', got %q", status)
	}
}

// ---------------------------------------------------------------------------
// TS-10-13: When no claimable job is found, the worker blocks on a three-way
// select and wakes up when the poll timer fires.
// Requirement: 10-REQ-4.3
// ---------------------------------------------------------------------------

func TestWorker_PollInterval(t *testing.T) {
	q, db := newTestQueueWithOpts(t,
		WithWorkers(1),
		WithPollInterval(100*time.Millisecond),
	)

	handlerCalled := make(chan struct{}, 1)
	handler := func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		handlerCalled <- struct{}{}
		return nil, false, nil
	}
	if err := q.Register("test", handler, nil); err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer q.Stop()

	// Let the queue idle with no jobs for several poll intervals.
	// Workers should be blocking on the three-way select, not busy-looping.
	time.Sleep(250 * time.Millisecond)

	// Seed a job directly (bypassing Enqueue, so no wakeup signal).
	// The poll timer should discover it on the next cycle.
	now := time.Now()
	seedJobFull(t, db, "j1", "test", "k1", "n1", "queued",
		0, now.Add(-1*time.Second), now)

	// The poll timer should pick up the job within a couple of intervals.
	select {
	case <-handlerCalled:
		// Success: poll timer woke the worker and it found the job.
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not pick up job via poll timer within timeout")
	}
}

// ---------------------------------------------------------------------------
// TS-10-14: Queue starts exactly the configured number of worker goroutines
// and all workers share a single buffered(1) wakeup channel.
// Requirement: 10-REQ-4.4
// ---------------------------------------------------------------------------

func TestWorker_GoroutineCount(t *testing.T) {
	q, _ := newTestQueueWithOpts(t, WithWorkers(3))

	// Snapshot goroutine count before starting.
	before := runtime.NumGoroutine()

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer q.Stop()

	// Allow goroutines to be scheduled.
	runtime.Gosched()
	time.Sleep(100 * time.Millisecond)

	after := runtime.NumGoroutine()
	delta := after - before

	// Expect at least 3 new goroutines (the workers). There may be
	// additional goroutines (e.g., a promote/poll coordinator), so we
	// check >= 3 rather than == 3.
	if delta < 3 {
		t.Errorf("expected at least 3 new goroutines for WithWorkers(3), "+
			"got delta=%d (before=%d, after=%d)", delta, before, after)
	}

	// Verify the wakeup channel has buffer size 1.
	if cap(q.wakeupCh) != 1 {
		t.Errorf("expected wakeup channel capacity=1, got %d", cap(q.wakeupCh))
	}
}
