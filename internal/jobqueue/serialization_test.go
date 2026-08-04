package jobqueue

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TS-10-37: The polling query ensures at most one job with a given (type, key)
// is in running status at any time by excluding keys with a running job.
// Requirement: 10-REQ-12.1
// Property: 10-PROP-3 (per-key serial execution)
// ---------------------------------------------------------------------------

func TestSerialization_RunningBlocksSameKey(t *testing.T) {
	q, db := newTestQueueWithOpts(t,
		WithWorkers(2),
		WithPollInterval(50*time.Millisecond),
	)

	// Handler that blocks until explicitly released, so we can observe
	// the running state.
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

	// Seed j1 and j2 with the same (type, key). j1 is queued and should
	// be claimed first (earlier created_at).
	now := time.Now()
	seedJobFull(t, db, "j1", "merge", "main", "n1", "queued",
		0, now.Add(-2*time.Second), now.Add(-2*time.Second))
	seedJobFull(t, db, "j2", "merge", "main", "n2", "queued",
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
		t.Fatal("j1 handler was not called; workers may not be running")
	}

	// Give workers a chance to attempt claiming j2.
	time.Sleep(200 * time.Millisecond)

	// j2 must still be queued: per-key serialization prevents claiming it
	// while j1 (same type+key) is running.
	var j2Status string
	if err := db.QueryRow("SELECT status FROM jobs WHERE id=?", "j2").Scan(&j2Status); err != nil {
		t.Fatalf("query j2 status failed: %v", err)
	}
	if j2Status != "queued" {
		t.Errorf("expected j2 status='queued' (blocked by running j1 with same key), got %q", j2Status)
	}
}

// ---------------------------------------------------------------------------
// TS-10-38: While a job with (type=T, key=K) is running, all other queued
// jobs with the same (type, key) are skipped during polling, but jobs with
// different keys run concurrently.
// Requirement: 10-REQ-12.2
// Property: 10-PROP-3
// ---------------------------------------------------------------------------

func TestSerialization_DifferentKeysRunConcurrently(t *testing.T) {
	q, db := newTestQueueWithOpts(t,
		WithWorkers(2),
		WithPollInterval(50*time.Millisecond),
	)

	// Handler that blocks until released. All jobs use the same handler
	// since they share the same type.
	handlerStarted := make(chan struct{}, 4) // buffer for all potential calls
	blockCh := make(chan struct{})
	handler := func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		handlerStarted <- struct{}{}
		<-blockCh
		return nil, false, nil
	}
	if err := q.Register("merge", handler, nil); err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	// j1 is already running (pre-seeded). j2, j3 share j1's key.
	// j4 has a different key and should run concurrently.
	now := time.Now()
	seedJobFull(t, db, "j1", "merge", "main", "n1", "running",
		0, now.Add(-4*time.Second), now.Add(-4*time.Second))
	seedJobFull(t, db, "j2", "merge", "main", "n2", "queued",
		0, now.Add(-3*time.Second), now.Add(-3*time.Second))
	seedJobFull(t, db, "j3", "merge", "main", "n3", "queued",
		0, now.Add(-2*time.Second), now.Add(-2*time.Second))
	seedJobFull(t, db, "j4", "merge", "dev", "n4", "queued",
		0, now.Add(-1*time.Second), now.Add(-1*time.Second))

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer func() {
		close(blockCh)
		q.Stop()
	}()

	// Wait for at least one handler to start (should be j4).
	select {
	case <-handlerStarted:
		// A worker claimed and started a job.
	case <-time.After(5 * time.Second):
		t.Fatal("no handler was called; workers may not be running")
	}

	// Give workers time to settle.
	time.Sleep(200 * time.Millisecond)

	// j4 (key='dev') should be running: different key from j1.
	var j4Status string
	if err := db.QueryRow("SELECT status FROM jobs WHERE id=?", "j4").Scan(&j4Status); err != nil {
		t.Fatalf("query j4 status failed: %v", err)
	}
	if j4Status != "running" {
		t.Errorf("expected j4 (key='dev') status='running' (should run concurrently), got %q", j4Status)
	}

	// j2 and j3 (key='main') should remain queued: j1 (same key) is running.
	var j2Status, j3Status string
	if err := db.QueryRow("SELECT status FROM jobs WHERE id=?", "j2").Scan(&j2Status); err != nil {
		t.Fatalf("query j2 status failed: %v", err)
	}
	if err := db.QueryRow("SELECT status FROM jobs WHERE id=?", "j3").Scan(&j3Status); err != nil {
		t.Fatalf("query j3 status failed: %v", err)
	}
	if j2Status != "queued" {
		t.Errorf("expected j2 (key='main') status='queued' (blocked by running j1), got %q", j2Status)
	}
	if j3Status != "queued" {
		t.Errorf("expected j3 (key='main') status='queued' (blocked by running j1), got %q", j3Status)
	}
}

// ---------------------------------------------------------------------------
// TS-10-39: After a running job with (type=T, key=K) completes, the next poll
// cycle picks up the next queued job with the same (type, key).
// Requirement: 10-REQ-12.3
// ---------------------------------------------------------------------------

func TestSerialization_NextJobAfterCompletion(t *testing.T) {
	q, db := newTestQueueWithOpts(t,
		WithWorkers(1),
		WithPollInterval(50*time.Millisecond),
	)

	// Handler that completes immediately.
	handler := func(_ context.Context, _ json.RawMessage) (any, bool, error) {
		return nil, false, nil
	}
	if err := q.Register("merge", handler, nil); err != nil {
		t.Fatalf("Register() failed: %v", err)
	}

	// Seed two queued jobs with the same (type, key). j1 has an earlier
	// created_at so it should be picked up first.
	now := time.Now()
	seedJobFull(t, db, "j1", "merge", "main", "n1", "queued",
		0, now.Add(-2*time.Second), now.Add(-2*time.Second))
	seedJobFull(t, db, "j2", "merge", "main", "n2", "queued",
		0, now.Add(-1*time.Second), now.Add(-1*time.Second))

	if err := q.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer q.Stop()

	// j1 should be picked up and completed first.
	waitForStatus(t, db, "j1", "completed", 5*time.Second)

	// After j1 completes, the per-key lock is released. The next poll cycle
	// should claim j2 and complete it.
	waitForStatus(t, db, "j2", "completed", 5*time.Second)
}
