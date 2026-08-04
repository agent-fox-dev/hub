package jobqueue

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TS-10-27: Calling Shutdown closes stopCh, causing worker goroutines to stop
// claiming new jobs and complete their current work. Wait() returns within the
// grace period.
// Requirement: 10-REQ-9.1
// Property: 10-PROP-9 (graceful shutdown completes within grace period)
// ---------------------------------------------------------------------------

func TestShutdown_StopsWorkers(t *testing.T) {
	q, _ := newTestQueueWithOpts(t,
		WithWorkers(2),
		WithPollInterval(50*time.Millisecond),
	)
	registerTestHandler(t, q, "test")

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	q.Shutdown()

	done := make(chan struct{})
	go func() {
		q.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Wait() returned within grace period.
	case <-time.After(35 * time.Second):
		t.Fatal("Wait() did not return within grace period (35s)")
	}
}

// ---------------------------------------------------------------------------
// TS-10-28: Wait() returns immediately after all workers exit before the grace
// period expires. Returns nil.
// Requirement: 10-REQ-9.2
// ---------------------------------------------------------------------------

func TestShutdown_WaitReturnsQuickly(t *testing.T) {
	q, _ := newTestQueueWithOpts(t,
		WithWorkers(1),
		WithGracePeriod(30*time.Second),
		WithPollInterval(50*time.Millisecond),
	)
	registerTestHandler(t, q, "test")

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	q.Shutdown()

	start := time.Now()
	err := q.Wait()
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("Wait() returned error: %v", err)
	}

	// With no long-running jobs, Wait() should return well before the 30s grace period.
	if elapsed > 5*time.Second {
		t.Errorf("Wait() took %v; expected < 5s (well before 30s grace period)", elapsed)
	}
}

// ---------------------------------------------------------------------------
// TS-10-29: When the grace period expires before all workers finish, the queue
// cancels in-flight handler contexts, logs a WARN per interrupted job, and
// Wait() returns.
// Requirement: 10-REQ-9.3
// Edge Case: 10-REQ-14.E1 (WARN per interrupted job)
// ---------------------------------------------------------------------------

func TestShutdown_GracePeriodExpiry(t *testing.T) {
	var ctxCancelled bool
	var mu sync.Mutex

	q, db, logBuf := newTestQueueWithLogCapture(t,
		WithGracePeriod(100*time.Millisecond),
		WithWorkers(1),
		WithPollInterval(50*time.Millisecond),
	)

	// Register a handler that blocks until its context is cancelled.
	handler := func(ctx context.Context, _ json.RawMessage) (any, bool, error) {
		<-ctx.Done()
		mu.Lock()
		ctxCancelled = true
		mu.Unlock()
		return nil, false, ctx.Err()
	}
	if err := q.Register("test", handler, nil); err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	// Seed a queued job.
	now := time.Now()
	seedJobFull(t, db, "j1", "test", "k1", "n1", "queued",
		0, now.Add(-1*time.Second), now.Add(-2*time.Second))

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Wait for the handler to start executing.
	time.Sleep(200 * time.Millisecond)

	q.Shutdown()

	start := time.Now()
	q.Wait()
	elapsed := time.Since(start)

	// Wait() should return within ~500ms (grace period is 100ms + some tolerance).
	if elapsed > 500*time.Millisecond {
		t.Errorf("Wait() took %v; expected < 500ms (grace period is 100ms)", elapsed)
	}

	// Verify the handler's context was cancelled.
	mu.Lock()
	cancelled := ctxCancelled
	mu.Unlock()
	if !cancelled {
		t.Error("expected handler's context to be cancelled after grace period expiry")
	}

	// Verify a WARN log line was emitted containing the interrupted job's ID.
	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "WARN") {
		t.Errorf("expected WARN log for grace period interruption, log output:\n%s", logOutput)
	}
	if !strings.Contains(logOutput, "j1") {
		t.Errorf("expected WARN log to contain job_id 'j1', log output:\n%s", logOutput)
	}
}

// ---------------------------------------------------------------------------
// TS-10-E27: If Shutdown is called before the queue has been started, it
// returns without error and without blocking; Wait() returns immediately.
// Requirement: 10-REQ-9.E1
// ---------------------------------------------------------------------------

func TestShutdown_BeforeStart(t *testing.T) {
	q, _ := newTestQueueWithOpts(t, WithWorkers(1))

	// Shutdown before Start: should not panic or block.
	q.Shutdown()

	done := make(chan struct{})
	go func() {
		q.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Wait() returned immediately.
	case <-time.After(2 * time.Second):
		t.Fatal("Wait() blocked after Shutdown before Start; expected immediate return")
	}
}

// ---------------------------------------------------------------------------
// TS-10-E28: If Shutdown is called multiple times concurrently, subsequent
// calls are no-ops; closing an already-closed stopCh is protected against
// panic (e.g., via sync.Once). Wait() returns normally.
// Requirement: 10-REQ-9.E2
// ---------------------------------------------------------------------------

func TestShutdown_MultipleCalls(t *testing.T) {
	q, _ := newTestQueueWithOpts(t,
		WithWorkers(2),
		WithPollInterval(50*time.Millisecond),
	)
	registerTestHandler(t, q, "test")

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Call Shutdown concurrently from multiple goroutines.
	// This must not panic (e.g., from closing an already-closed channel).
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q.Shutdown()
		}()
	}
	wg.Wait()

	// Wait() should still return normally.
	done := make(chan struct{})
	go func() {
		q.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success.
	case <-time.After(35 * time.Second):
		t.Fatal("Wait() did not return after multiple concurrent Shutdown calls")
	}
}

// ---------------------------------------------------------------------------
// TS-10-E29: If a handler ignores context cancellation and runs indefinitely
// after the grace period expires, the queue logs the interruption at WARN
// level and exits anyway; Wait() returns after the grace period.
// Requirement: 10-REQ-9.E3
// ---------------------------------------------------------------------------

func TestShutdown_HandlerIgnoresContext(t *testing.T) {
	q, db, logBuf := newTestQueueWithLogCapture(t,
		WithGracePeriod(100*time.Millisecond),
		WithWorkers(1),
		WithPollInterval(50*time.Millisecond),
	)

	// Register a handler that never returns (ignores ctx.Done()).
	blocker := make(chan struct{})
	t.Cleanup(func() { close(blocker) })

	handler := func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		<-blocker // blocks forever until test cleanup
		return nil, false, nil
	}
	if err := q.Register("test", handler, nil); err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	// Seed a queued job.
	now := time.Now()
	seedJobFull(t, db, "j1", "test", "k1", "n1", "queued",
		0, now.Add(-1*time.Second), now.Add(-2*time.Second))

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Wait for the handler to start.
	time.Sleep(200 * time.Millisecond)

	q.Shutdown()

	start := time.Now()
	q.Wait()
	elapsed := time.Since(start)

	// Wait() must return after the grace period regardless of the hung handler.
	if elapsed > 1*time.Second {
		t.Errorf("Wait() took %v; expected to return after grace period (~100ms)", elapsed)
	}

	// Verify WARN log for the interrupted job.
	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "WARN") {
		t.Errorf("expected WARN log for interrupted handler, log output:\n%s", logOutput)
	}
}
