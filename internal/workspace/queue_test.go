package workspace

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
)

// ========================================================================
// Spec 05 Task 2.1: Job queue construction and worker lifecycle
// (TS-05-10, TS-05-11, TS-05-12)
// ========================================================================

// TS-05-10: On server start, an in-memory FIFO job queue is initialized and
// the configured number of worker goroutines (default 4) are started.
// Requirement: 05-REQ-4.1
func TestJobQueue_Initialization(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q := NewJobQueue(ctx, db, wsRoot, 4)
	if q == nil {
		t.Fatal("NewJobQueue returned nil; job queue not implemented")
	}

	// Worker count must match the configured value.
	if got := q.WorkerCount(); got != 4 {
		t.Errorf("WorkerCount() = %d; want 4", got)
	}

	// The queue should be backed by a buffered Go channel (non-nil).
	if q.jobs == nil {
		t.Error("jobs channel is nil; queue should be backed by a buffered channel")
	}
}

// TestJobQueue_CustomWorkerCount verifies that NewJobQueue respects the
// configured worker count rather than always defaulting to 4.
// Requirement: 05-REQ-4.1
func TestJobQueue_CustomWorkerCount(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	q := NewJobQueue(ctx, db, wsRoot, 2)
	if q == nil {
		t.Fatal("NewJobQueue returned nil; job queue not implemented")
	}

	if got := q.WorkerCount(); got != 2 {
		t.Errorf("WorkerCount() = %d; want 2", got)
	}
}

// TestJobQueue_ConcurrentWorkerLimit verifies that the number of concurrently
// processing workers never exceeds the configured count.
// Requirement: 05-REQ-4.1
func TestJobQueue_ConcurrentWorkerLimit(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const workerCount = 4
	const totalJobs = 8

	q := NewJobQueue(ctx, db, wsRoot, workerCount)
	if q == nil {
		t.Fatal("NewJobQueue returned nil; job queue not implemented")
	}

	// Track concurrency via the injectable clone function.
	var maxConcurrent int32
	var current int32
	var processed int32

	oldFn := cloneFn
	cloneFn = func(_ context.Context, _ string, _ string, _ int, _ bool, _ string, _ transport.AuthMethod) (string, error) {
		c := atomic.AddInt32(&current, 1)
		defer atomic.AddInt32(&current, -1)

		// Update max concurrent using CAS loop.
		for {
			old := atomic.LoadInt32(&maxConcurrent)
			if c <= old || atomic.CompareAndSwapInt32(&maxConcurrent, old, c) {
				break
			}
		}

		// Simulate work to keep workers busy so concurrency can be observed.
		time.Sleep(50 * time.Millisecond)
		atomic.AddInt32(&processed, 1)
		return "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", nil
	}
	defer func() { cloneFn = oldFn }()

	// Seed workspaces and enqueue jobs.
	for i := 0; i < totalJobs; i++ {
		slug := fmt.Sprintf("ws-conc-%d", i)
		ws := &Workspace{
			Slug:    slug,
			GitURL:  "https://github.com/example/repo.git",
			OwnerID: "user-1",
			Status:  "active",
		}
		if err := insertWorkspace(db, ws); err != nil {
			t.Fatalf("seed workspace %q: %v", slug, err)
		}
		q.Enqueue(CloneJob{Slug: slug, GitURL: ws.GitURL})
	}

	// Wait for all jobs to complete with a timeout.
	deadline := time.After(5 * time.Second)
	for atomic.LoadInt32(&processed) < totalJobs {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for jobs; processed=%d/%d",
				atomic.LoadInt32(&processed), totalJobs)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Max concurrent workers must not exceed the configured count.
	if mc := atomic.LoadInt32(&maxConcurrent); mc > workerCount {
		t.Errorf("max concurrent workers = %d; want <= %d", mc, workerCount)
	}

	// All jobs must have been processed.
	if p := atomic.LoadInt32(&processed); p != totalJobs {
		t.Errorf("processed = %d; want %d", p, totalJobs)
	}
}

// TS-05-11: Workers start on queue initialization and stop when context is
// cancelled.
// Requirement: 05-REQ-4.1, 05-REQ-4.E1
func TestJobQueue_WorkerLifecycle(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())

	q := NewJobQueue(ctx, db, wsRoot, 2)
	if q == nil {
		cancel()
		t.Fatal("NewJobQueue returned nil; job queue not implemented")
	}

	// Verify workers are running by processing a job.
	var processed int32
	oldFn := cloneFn
	cloneFn = func(_ context.Context, _ string, _ string, _ int, _ bool, _ string, _ transport.AuthMethod) (string, error) {
		atomic.AddInt32(&processed, 1)
		return "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", nil
	}
	defer func() { cloneFn = oldFn }()

	// Seed a workspace and enqueue a job.
	ws := &Workspace{
		Slug:    "ws-lifecycle",
		GitURL:  "https://github.com/example/repo.git",
		OwnerID: "user-1",
		Status:  "active",
	}
	if err := insertWorkspace(db, ws); err != nil {
		cancel()
		t.Fatalf("seed workspace: %v", err)
	}
	q.Enqueue(CloneJob{Slug: "ws-lifecycle", GitURL: ws.GitURL})

	// Wait briefly for the job to be picked up.
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&processed) < 1 {
		select {
		case <-deadline:
			cancel()
			t.Fatal("timed out waiting for job to be processed; workers may not be running")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	if p := atomic.LoadInt32(&processed); p != 1 {
		t.Errorf("processed = %d; want 1", p)
	}

	// Cancel context to stop workers.
	cancel()

	// Wait for workers to exit with a timeout.
	done := make(chan struct{})
	go func() {
		q.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Workers exited successfully.
	case <-time.After(3 * time.Second):
		t.Fatal("workers did not exit within 3 seconds after context cancellation")
	}
}

// TS-05-12: Pending jobs are discarded on context cancellation (graceful
// shutdown). In-progress PlainCloneContext calls are interrupted.
// Requirement: 05-REQ-4.E1, 05-REQ-4.E2
func TestJobQueue_GracefulShutdown(t *testing.T) {
	db := openTestDB(t)
	wsRoot := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())

	// Use 1 worker so additional enqueued jobs stay pending in the channel.
	q := NewJobQueue(ctx, db, wsRoot, 1)
	if q == nil {
		cancel()
		t.Fatal("NewJobQueue returned nil; job queue not implemented")
	}

	// Clone function that blocks until context is cancelled, simulating
	// a slow network clone.
	var started int32
	oldFn := cloneFn
	cloneFn = func(fnCtx context.Context, _ string, _ string, _ int, _ bool, _ string, _ transport.AuthMethod) (string, error) {
		atomic.AddInt32(&started, 1)
		// Block until context is cancelled (simulating a hang).
		<-fnCtx.Done()
		return "", fnCtx.Err()
	}
	defer func() { cloneFn = oldFn }()

	// Seed workspaces and enqueue multiple jobs. With 1 worker, the first
	// job will block (in the clone function) and the rest will be pending.
	for i := 0; i < 5; i++ {
		slug := fmt.Sprintf("ws-shutdown-%d", i)
		ws := &Workspace{
			Slug:    slug,
			GitURL:  "https://github.com/example/repo.git",
			OwnerID: "user-1",
			Status:  "active",
		}
		if err := insertWorkspace(db, ws); err != nil {
			cancel()
			t.Fatalf("seed workspace %q: %v", slug, err)
		}
		q.Enqueue(CloneJob{Slug: slug, GitURL: ws.GitURL})
	}

	// Wait briefly for the first job to start processing.
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&started) < 1 {
		select {
		case <-deadline:
			cancel()
			t.Fatal("timed out waiting for first job to start")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Cancel context to trigger graceful shutdown.
	cancel()

	// Workers should exit promptly; pending jobs should be discarded.
	done := make(chan struct{})
	go func() {
		q.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Workers exited successfully.
	case <-time.After(3 * time.Second):
		t.Fatal("workers did not exit within 3 seconds after context cancellation")
	}

	// Only the first job should have been picked up by the worker.
	// Pending jobs (2-5) should have been discarded without processing.
	if s := atomic.LoadInt32(&started); s > 1 {
		t.Errorf("started = %d; want 1 (only the in-progress job should have been picked up)", s)
	}
}
