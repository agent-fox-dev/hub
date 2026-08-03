package mergequeue

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/gitcmd"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// TS-11-33: When the nonce in the database does not match the job's nonce at
// validation time, the job is discarded without executing the merge.
// Requirement: 11-REQ-5.E4
// ---------------------------------------------------------------------------

func TestMergePushFail_NonceMismatch_DiscardsJob(t *testing.T) {
	db := openMergeTestDB(t)
	job := insertMergeTestJob(t, db, "nonce1", "")
	workspaceRoot := t.TempDir()

	// Change the nonce in the database so it no longer matches the job's nonce.
	// The job struct retains the original nonce, creating a mismatch that the
	// implementation must detect inside the per-target-branch mutex.
	_, err := db.Exec("UPDATE merge_jobs SET nonce = ? WHERE id = ?", "different-nonce-value", job.ID)
	if err != nil {
		t.Fatalf("failed to update nonce in DB: %v", err)
	}

	mockGit := newHappyPathMockGitOps()
	mockMu := newMockBranchLocker()

	deps := MergeDeps{
		Git:           mockGit,
		Locker:        mockMu,
		WorkspaceRoot: workspaceRoot,
	}

	_ = processMergeJob(context.Background(), db, job, deps)

	// Verify no merge git operations (fetch, rebase, push) were executed.
	// Rev-parse and merge-tree may occur before the nonce check (dry-run phase),
	// but the actual merge pipeline must not start.
	calls := mockGit.recordedCalls()
	for _, c := range calls {
		if c.Method == "Run" && len(c.Args) >= 1 {
			switch c.Args[0] {
			case "fetch", "rebase", "push":
				t.Errorf("git %s was called; want no merge operations when nonce mismatches", c.Args[0])
			}
		}
	}

	// Verify job status is NOT 'running' — the job was discarded before
	// transitioning to running.
	status := getJobStatus(t, db, job.ID)
	if status == "running" {
		t.Error("job status is 'running'; want job discarded (not started) when nonce mismatches")
	}

	// Verify the per-target-branch mutex was acquired. The nonce check
	// happens inside the mutex per REQ-5.1, so the mutex must be locked
	// before the nonce is validated.
	if !mockMu.wasLocked() {
		t.Error("mutex was not acquired; nonce validation must happen inside the per-target-branch mutex")
	}

	// Verify the mutex was released after discarding the job.
	assertMutexReleased(t, mockMu)
}

// TestMergePushFail_NonceMismatch_StatusUnchanged verifies that a nonce
// mismatch leaves the job's database status completely untouched.
func TestMergePushFail_NonceMismatch_StatusUnchanged(t *testing.T) {
	db := openMergeTestDB(t)
	job := insertMergeTestJob(t, db, "nonce2", "")
	workspaceRoot := t.TempDir()

	// Change the nonce in the database to create a mismatch.
	_, err := db.Exec("UPDATE merge_jobs SET nonce = ? WHERE id = ?", "tampered-nonce", job.ID)
	if err != nil {
		t.Fatalf("failed to update nonce in DB: %v", err)
	}

	mockGit := newHappyPathMockGitOps()
	mockMu := newMockBranchLocker()

	deps := MergeDeps{
		Git:           mockGit,
		Locker:        mockMu,
		WorkspaceRoot: workspaceRoot,
	}

	_ = processMergeJob(context.Background(), db, job, deps)

	// Verify the status remains 'queued' (the original status from insertion).
	status := getJobStatus(t, db, job.ID)
	if status != "queued" {
		t.Errorf("job status = %q; want 'queued' (unchanged after nonce mismatch discard)", status)
	}

	// Verify base_sha was NOT set (job never transitioned to running).
	baseSHA := getJobBaseSHA(t, db, job.ID)
	if baseSHA.Valid && baseSHA.String != "" {
		t.Errorf("base_sha = %q; want empty (job should not have started)", baseSHA.String)
	}

	// Verify the mutex was acquired (nonce check happens inside the mutex)
	// and then released.
	if !mockMu.wasLocked() {
		t.Error("mutex was not acquired; nonce validation must happen inside the per-target-branch mutex")
	}
	assertMutexReleased(t, mockMu)
}

// ---------------------------------------------------------------------------
// TS-11-34: When the check command exceeds CHECK_TIMEOUT, the subprocess is
// killed and job status is set to check_failed with timeout error in
// check_output.
// Requirement: 11-REQ-5.E5
// ---------------------------------------------------------------------------

func TestMergeFetchRebase_CheckTimeout_SetsCheckFailed(t *testing.T) {
	db := openMergeTestDB(t)
	job := insertMergeTestJob(t, db, "timeout1", "")
	workspaceRoot := t.TempDir()

	mockGit := newHappyPathMockGitOps()
	mockMu := newMockBranchLocker()

	// Configure CHECK_COMMAND='sleep 999' and CHECK_TIMEOUT='1s'.
	mockVars := &mockVariableGetter{
		vars: map[string]string{
			"CHECK_COMMAND": "sleep 999",
			"CHECK_TIMEOUT": "1s",
		},
	}

	var capturedTimeout time.Duration

	deps := MergeDeps{
		Git:           mockGit,
		Locker:        mockMu,
		Variables:     mockVars,
		WorkspaceRoot: workspaceRoot,
		RunCheck: func(_ context.Context, _, _ string, timeout time.Duration) ([]byte, error) {
			capturedTimeout = timeout
			// Simulate a timeout: the check command was killed because it
			// exceeded the configured CHECK_TIMEOUT.
			return []byte("signal: killed"), context.DeadlineExceeded
		},
	}

	err := processMergeJob(context.Background(), db, job, deps)
	if err != nil {
		t.Fatalf("processMergeJob() returned error: %v", err)
	}

	// Verify the parsed CHECK_TIMEOUT (1s) was passed to the check runner.
	if capturedTimeout != 1*time.Second {
		t.Errorf("check timeout = %v; want 1s", capturedTimeout)
	}

	// Verify job status is 'check_failed'.
	status := getJobStatus(t, db, job.ID)
	if status != "check_failed" {
		t.Errorf("job status = %q; want 'check_failed'", status)
	}

	// Verify check_output contains a timeout-related error message.
	checkOutput := getJobCheckOutput(t, db, job.ID)
	if !checkOutput.Valid {
		t.Fatal("check_output is NULL; want timeout error message")
	}
	output := strings.ToLower(checkOutput.String)
	if !strings.Contains(output, "timeout") &&
		!strings.Contains(output, "deadline exceeded") &&
		!strings.Contains(output, "killed") &&
		!strings.Contains(output, "signal") {
		t.Errorf("check_output = %q; want to contain 'timeout', 'deadline exceeded', 'killed', or 'signal'", checkOutput.String)
	}

	// Verify mutex was released after check failure.
	assertMutexReleased(t, mockMu)
}

// ---------------------------------------------------------------------------
// TS-11-35: When fetching CHECK_COMMAND from the workspace variables store
// fails, processMergeJob sets status to check_failed and records the error
// in check_output.
// Requirement: 11-REQ-5.E6
// ---------------------------------------------------------------------------

func TestMergeFetchRebase_VariableStoreFails_SetsCheckFailed(t *testing.T) {
	db := openMergeTestDB(t)
	job := insertMergeTestJob(t, db, "varfail1", "")
	workspaceRoot := t.TempDir()

	mockGit := newHappyPathMockGitOps()
	mockMu := newMockBranchLocker()

	// Workspace variables store returns an error when queried for CHECK_COMMAND.
	mockVars := &mockVariableGetter{
		err: errors.New("store unavailable"),
	}

	deps := MergeDeps{
		Git:           mockGit,
		Locker:        mockMu,
		Variables:     mockVars,
		WorkspaceRoot: workspaceRoot,
	}

	err := processMergeJob(context.Background(), db, job, deps)
	if err != nil {
		t.Fatalf("processMergeJob() returned error: %v", err)
	}

	// Verify job status is 'check_failed'.
	status := getJobStatus(t, db, job.ID)
	if status != "check_failed" {
		t.Errorf("job status = %q; want 'check_failed'", status)
	}

	// Verify check_output contains the store error message.
	checkOutput := getJobCheckOutput(t, db, job.ID)
	if !checkOutput.Valid {
		t.Fatal("check_output is NULL; want variable store error message")
	}
	if !strings.Contains(checkOutput.String, "store unavailable") {
		t.Errorf("check_output = %q; want to contain 'store unavailable'", checkOutput.String)
	}

	// Verify mutex was released.
	assertMutexReleased(t, mockMu)
}

// ---------------------------------------------------------------------------
// TS-11-36: When the git fetch subprocess hangs and the context is cancelled,
// the job is re-enqueued with backoff and the mutex is released.
// Requirement: 11-REQ-5.E7
// ---------------------------------------------------------------------------

func TestMergeFetchRebase_FetchHangs_ReenqueuesWithBackoff(t *testing.T) {
	db := openMergeTestDB(t)
	job := insertMergeTestJob(t, db, "hang1", "")
	workspaceRoot := t.TempDir()
	beforeAvailableAt := job.AvailableAt

	// Create a context with a short timeout to simulate a fetch that hangs.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	mockGit := &mockGitOps{
		onRun: func(ctx context.Context, args ...string) ([]byte, []byte, error) {
			if len(args) >= 1 {
				switch args[0] {
				case "rev-parse":
					for _, a := range args {
						if strings.HasPrefix(a, "origin/") {
							return []byte(testTargetHead + "\n"), nil, nil
						}
					}
					return []byte(testSourceHead + "\n"), nil, nil
				case "fetch":
					// Block until context is cancelled, simulating a hanging
					// git fetch subprocess.
					<-ctx.Done()
					return nil, nil, ctx.Err()
				}
			}
			return nil, nil, nil
		},
		onRunExitCode: func(_ context.Context, _ ...string) ([]byte, []byte, int, error) {
			// Dry-run passes (no conflicts).
			return nil, nil, 0, nil
		},
	}

	mockMu := newMockBranchLocker()

	deps := MergeDeps{
		Git:           mockGit,
		Locker:        mockMu,
		WorkspaceRoot: workspaceRoot,
	}

	_ = processMergeJob(ctx, db, job, deps)

	// Verify job status is 'queued' (re-enqueued, not a terminal failure).
	status := getJobStatus(t, db, job.ID)
	if status != "queued" {
		t.Errorf("job status = %q; want 'queued' (re-enqueued after fetch hang)", status)
	}

	// Verify retry_count is incremented.
	retryCount := getJobRetryCount(t, db, job.ID)
	if retryCount < 1 {
		t.Errorf("retry_count = %d; want >= 1", retryCount)
	}

	// Verify available_at is set to a future timestamp (backoff delay).
	availableAt := getJobAvailableAt(t, db, job.ID)
	if availableAt <= beforeAvailableAt {
		t.Errorf("available_at = %q; want later than original %q (backoff)", availableAt, beforeAvailableAt)
	}

	// Verify mutex was released after the fetch hang.
	assertMutexReleased(t, mockMu)
}

// ---------------------------------------------------------------------------
// Additional: PostMergeHook must NOT be invoked when the merge fails at any
// step before push success — even for campaign merges with a valid hook.
// Requirement: 11-REQ-5.E3, 11-REQ-5.5 (negative case)
// ---------------------------------------------------------------------------

func TestMergeFetchRebase_PostMergeHook_NotCalledOnPushFailure(t *testing.T) {
	db := openMergeTestDB(t)
	// Use a campaign merge (non-empty campaign_id) that would normally trigger
	// the PostMergeHook on success.
	campaignID := newTestUUID("campaign2")
	job := insertMergeTestJob(t, db, "hookfail", campaignID)
	workspaceRoot := t.TempDir()

	// Push fails — the merge does not complete successfully.
	mockGit := &mockGitOps{
		onRun: func(_ context.Context, args ...string) ([]byte, []byte, error) {
			if len(args) >= 1 {
				switch args[0] {
				case "rev-parse":
					for _, a := range args {
						if strings.HasPrefix(a, "origin/") {
							return []byte(testTargetHead + "\n"), nil, nil
						}
					}
					return []byte(testMergedHead + "\n"), nil, nil
				case "push":
					return nil, []byte("rejected\n"), &gitcmd.GitError{
						Command:  strings.Join(args, " "),
						ExitCode: 1,
						Stderr:   "! [rejected] main -> main (non-fast-forward)",
					}
				}
			}
			return nil, nil, nil
		},
		onRunExitCode: func(_ context.Context, _ ...string) ([]byte, []byte, int, error) {
			// Dry-run passes.
			return nil, nil, 0, nil
		},
	}

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

	_ = processMergeJob(context.Background(), db, job, deps)

	// Verify PostMergeHook was NOT called — merge failed at push step.
	if hookCalled {
		t.Error("PostMergeHook was called after push failure; want hook skipped when merge does not succeed")
	}

	// Verify job status is 'push_failed' (confirms push failure was handled).
	status := getJobStatus(t, db, job.ID)
	if status != "push_failed" {
		t.Errorf("job status = %q; want 'push_failed'", status)
	}
}

// TestMergeFetchRebase_RebaseUsesRemoteTrackingRef verifies that the rebase
// command uses 'origin/<targetBranch>' (the remote tracking ref) rather than
// a bare local branch name, ensuring the rebase uses the freshly fetched
// state.
func TestMergeFetchRebase_RebaseUsesRemoteTrackingRef(t *testing.T) {
	db := openMergeTestDB(t)
	job := insertMergeTestJob(t, db, "refchk1", "")
	workspaceRoot := t.TempDir()

	mockGit := newHappyPathMockGitOps()
	mockMu := newMockBranchLocker()

	deps := MergeDeps{
		Git:           mockGit,
		Locker:        mockMu,
		WorkspaceRoot: workspaceRoot,
	}

	err := processMergeJob(context.Background(), db, job, deps)
	if err != nil {
		t.Fatalf("processMergeJob() returned error: %v", err)
	}

	// Verify rebase uses 'origin/main' (remote tracking ref), NOT 'main'
	// (local branch ref).
	calls := mockGit.recordedCalls()
	rebaseFound := false
	for _, c := range calls {
		if c.Method == "Run" && len(c.Args) >= 2 && c.Args[0] == "rebase" {
			rebaseFound = true
			rebaseRef := c.Args[1]
			if rebaseRef != "origin/main" {
				t.Errorf("rebase ref = %q; want 'origin/main' (remote tracking ref)", rebaseRef)
			}
			if rebaseRef == "main" {
				t.Error("rebase used bare 'main' (local ref); want 'origin/main' to use freshly fetched state")
			}
		}
	}
	if !rebaseFound {
		t.Error("git rebase was not called; want rebase after fetch")
	}
}
