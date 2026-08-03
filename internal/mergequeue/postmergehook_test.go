package mergequeue

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ===========================================================================
// 10.2 — PostMergeHook Invocation and Error Handling Tests
// ===========================================================================

// ---------------------------------------------------------------------------
// TS-11-88: PostMergeHook is invoked synchronously after merged_sha is
// recorded for campaign merges; hook errors are logged but job status remains
// merged.
// Requirement: 11-REQ-16.1
// ---------------------------------------------------------------------------

func TestPostMergeHook_InvokedAfterMergedSHA_ErrorLogged_StatusRemainsMerged(t *testing.T) {
	db := openMergeTestDB(t)
	campaignID := newTestUUID("campaign88")
	job := insertMergeTestJob(t, db, "hook88", campaignID)
	workspaceRoot := t.TempDir()

	mockGit := newHappyPathMockGitOps()
	mockMu := newMockBranchLocker()

	hookCalled := false
	var hookReceivedJob MergeJob

	deps := MergeDeps{
		Git:           mockGit,
		Locker:        mockMu,
		WorkspaceRoot: workspaceRoot,
		Hook: func(_ context.Context, j MergeJob) error {
			hookCalled = true
			hookReceivedJob = j
			return errors.New("hook failed")
		},
	}

	err := processMergeJob(context.Background(), db, job, deps)
	if err != nil {
		t.Fatalf("processMergeJob() returned error: %v", err)
	}

	// PostMergeHook must be called for campaign merges.
	if !hookCalled {
		t.Fatal("PostMergeHook was not called; want hook invoked for campaign merge after merged_sha is set")
	}

	// The hook must receive the job after merged_sha is set.
	if hookReceivedJob.MergedSHA.String == "" || !hookReceivedJob.MergedSHA.Valid {
		t.Error("PostMergeHook received job with empty merged_sha; want merged_sha set before hook invocation")
	}

	// Job status must remain 'merged' despite hook error.
	status := getJobStatus(t, db, job.ID)
	if status != "merged" {
		t.Errorf("job status = %q; want 'merged' (hook errors must not change status)", status)
	}
}

// ---------------------------------------------------------------------------
// TS-11-89: PostMergeHook is not invoked for standalone merges
// (campaign_id=NULL) or when PostMergeHook is nil.
// Requirement: 11-REQ-16.2
// ---------------------------------------------------------------------------

func TestPostMergeHook_NotCalledForStandaloneMerge(t *testing.T) {
	db := openMergeTestDB(t)
	// Empty campaign_id = standalone merge.
	job := insertMergeTestJob(t, db, "hook89a", "")
	workspaceRoot := t.TempDir()

	mockGit := newHappyPathMockGitOps()
	mockMu := newMockBranchLocker()

	hookCalled := false

	deps := MergeDeps{
		Git:           mockGit,
		Locker:        mockMu,
		WorkspaceRoot: workspaceRoot,
		Hook: func(_ context.Context, _ MergeJob) error {
			hookCalled = true
			return nil
		},
	}

	err := processMergeJob(context.Background(), db, job, deps)
	if err != nil {
		t.Fatalf("processMergeJob() returned error: %v", err)
	}

	// PostMergeHook must NOT be called for standalone merges.
	if hookCalled {
		t.Error("PostMergeHook was called for standalone merge (campaign_id=NULL); want hook skipped")
	}

	// Job must still reach merged status.
	status := getJobStatus(t, db, job.ID)
	if status != "merged" {
		t.Errorf("job status = %q; want 'merged'", status)
	}
}

func TestPostMergeHook_NotCalledWhenHookIsNil(t *testing.T) {
	db := openMergeTestDB(t)
	campaignID := newTestUUID("campaign89b")
	job := insertMergeTestJob(t, db, "hook89b", campaignID)
	workspaceRoot := t.TempDir()

	mockGit := newHappyPathMockGitOps()
	mockMu := newMockBranchLocker()

	deps := MergeDeps{
		Git:           mockGit,
		Locker:        mockMu,
		WorkspaceRoot: workspaceRoot,
		// Hook is nil — should be handled gracefully.
	}

	err := processMergeJob(context.Background(), db, job, deps)
	if err != nil {
		t.Fatalf("processMergeJob() returned error: %v", err)
	}

	// Job must reach merged status even without a hook.
	status := getJobStatus(t, db, job.ID)
	if status != "merged" {
		t.Errorf("job status = %q; want 'merged'", status)
	}
}

// ---------------------------------------------------------------------------
// TS-11-90: NewQueue accepts PostMergeHook as a constructor parameter; the
// campaign package supplies the concrete implementation.
// Requirement: 11-REQ-16.3
// ---------------------------------------------------------------------------

func TestPostMergeHook_NewQueueAcceptsHookParameter(t *testing.T) {
	db := openMergeTestDB(t)

	hookCalled := false
	hook := PostMergeHook(func(_ context.Context, _ MergeJob) error {
		hookCalled = true
		return nil
	})

	deps := MergeDeps{
		Git:    newHappyPathMockGitOps(),
		Locker: newMockBranchLocker(),
		Hook:   hook,
	}

	q := NewQueue(db, deps, nil)
	if q == nil {
		t.Fatal("NewQueue returned nil; want a valid Queue instance")
	}

	// Verify the hook is stored and accessible via deps.
	if q.deps.Hook == nil {
		t.Error("Queue.deps.Hook is nil; want non-nil PostMergeHook stored from constructor")
	}

	// The hook being stored is the key assertion. We verify it's wired
	// by checking the deps struct holds a non-nil hook reference.
	// Full invocation testing is covered by TS-11-88.
	_ = hookCalled // prevent unused variable error
}

// ---------------------------------------------------------------------------
// TS-11-91: When PostMergeHook blocks and graceful shutdown cancels the
// context, the hook invocation is interrupted and the job remains in merged
// status.
// Requirement: 11-REQ-16.E1
// ---------------------------------------------------------------------------

func TestPostMergeHook_BlockingHook_CancelledByShutdown_StatusRemainsMerged(t *testing.T) {
	db := openMergeTestDB(t)
	campaignID := newTestUUID("campaign91")
	job := insertMergeTestJob(t, db, "hook91", campaignID)
	workspaceRoot := t.TempDir()

	mockGit := newHappyPathMockGitOps()
	mockMu := newMockBranchLocker()

	// Create a context that we can cancel to simulate graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())

	hookStarted := make(chan struct{})
	hookDone := make(chan struct{})

	deps := MergeDeps{
		Git:           mockGit,
		Locker:        mockMu,
		WorkspaceRoot: workspaceRoot,
		Hook: func(ctx context.Context, _ MergeJob) error {
			close(hookStarted)
			// Block until context is cancelled.
			<-ctx.Done()
			close(hookDone)
			return ctx.Err()
		},
	}

	// Run processMergeJob in a goroutine since the hook blocks.
	var processErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		processErr = processMergeJob(ctx, db, job, deps)
	}()

	// Wait for the hook to start blocking, then cancel the context.
	select {
	case <-hookStarted:
		cancel()
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timeout waiting for hook to start; processMergeJob may not have reached the hook invocation")
	}

	// Wait for processMergeJob to complete.
	wg.Wait()

	// processErr may or may not be nil depending on implementation,
	// but the job status must remain 'merged'.
	_ = processErr

	select {
	case <-hookDone:
		// Hook was interrupted by context cancellation — correct behavior.
	case <-time.After(2 * time.Second):
		t.Error("hook did not return after context cancellation; want hook interrupted by context.Done()")
	}

	// Job status must remain 'merged' regardless of hook error.
	status := getJobStatus(t, db, job.ID)
	if status != "merged" {
		t.Errorf("job status = %q; want 'merged' (hook interruption must not change terminal merged status)", status)
	}
}
