package mergequeue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/gitcmd"

	_ "modernc.org/sqlite"
)

// testMergedHead is the SHA returned by rev-parse HEAD after a successful
// rebase, used as the expected merged_sha value in merge pipeline tests.
const testMergedHead = "ccc3333333333333333333333333333333333333"

// ---------------------------------------------------------------------------
// Mock VariableGetter for merge pipeline tests
// ---------------------------------------------------------------------------

// mockVariableGetter implements VariableGetter with a fixed map of variables.
type mockVariableGetter struct {
	vars map[string]string
	err  error
}

func (m *mockVariableGetter) GetVariable(_ context.Context, _, key string) (string, bool, error) {
	if m.err != nil {
		return "", false, m.err
	}
	val, ok := m.vars[key]
	return val, ok, nil
}

// ---------------------------------------------------------------------------
// Helpers for merge pipeline tests
// ---------------------------------------------------------------------------

// newHappyPathMockGitOps creates a mockGitOps that returns success for all
// operations: rev-parse returns known SHAs, merge-tree dry-run passes (exit 0),
// and fetch/rebase/push all succeed.
func newHappyPathMockGitOps() *mockGitOps {
	return &mockGitOps{
		onRun: func(_ context.Context, args ...string) ([]byte, []byte, error) {
			if len(args) >= 1 {
				switch args[0] {
				case "rev-parse":
					for _, a := range args {
						if strings.HasPrefix(a, "origin/") {
							return []byte(testTargetHead + "\n"), nil, nil
						}
					}
					// After rebase, rev-parse HEAD returns the merged SHA.
					return []byte(testMergedHead + "\n"), nil, nil
				}
			}
			return nil, nil, nil
		},
		onRunExitCode: func(_ context.Context, _ ...string) ([]byte, []byte, int, error) {
			return nil, nil, 0, nil
		},
	}
}

// openMergeTestDB opens an in-memory SQLite database with the merge_jobs
// table for merge pipeline tests. Bypasses InitSchema stub.
func openMergeTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDBNoSchema(t)
	setupMergeJobsTable(t, db)
	return db
}

// insertMergeTestJob inserts a merge job in "queued" status for merge
// pipeline tests and returns the MergeJob struct. If campaignID is non-empty,
// it sets the CampaignID field.
func insertMergeTestJob(t *testing.T, db *sql.DB, suffix string, campaignID string) *MergeJob {
	t.Helper()
	id := newTestUUID(suffix)
	nonce := newTestUUID("n" + suffix)
	now := time.Now().UTC().Format(time.RFC3339)
	job := &MergeJob{
		ID:            id,
		Nonce:         nonce,
		WorkspaceSlug: "test-ws",
		TargetBranch:  "main",
		SourceRef:     "spec/07-secrets-variables",
		Status:        "queued",
		RetryCount:    0,
		AvailableAt:   now,
		SubmittedBy:   newTestUUID("user"),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if campaignID != "" {
		job.CampaignID = sql.NullString{String: campaignID, Valid: true}
	}
	insertTestMergeJobFull(t, db, job)
	return job
}

// assertMockGitCalled checks that the mockGitOps recorded a Run call with
// the given leading args. Returns true if found.
func assertMockGitCalled(t *testing.T, mockGit *mockGitOps, wantArgs ...string) bool {
	t.Helper()
	calls := mockGit.recordedCalls()
	for _, c := range calls {
		if c.Method == "Run" && len(c.Args) >= len(wantArgs) {
			match := true
			for i, want := range wantArgs {
				if c.Args[i] != want {
					match = false
					break
				}
			}
			if match {
				return true
			}
		}
	}
	t.Errorf("git Run(%v) was not called; recorded calls: %v", wantArgs, calls)
	return false
}

// assertMutexReleased checks that the mock branch locker acquired and
// then released the mutex, or was never locked (both acceptable for the
// "released" assertion).
func assertMutexReleased(t *testing.T, mockMu *mockBranchLocker) {
	t.Helper()
	mockMu.mu.Lock()
	defer mockMu.mu.Unlock()
	lockCount, unlockCount := 0, 0
	for _, e := range mockMu.events {
		if strings.HasPrefix(e, "mutex-lock:") {
			lockCount++
		}
		if strings.HasPrefix(e, "mutex-unlock:") {
			unlockCount++
		}
	}
	if lockCount > 0 && unlockCount == 0 {
		t.Error("mutex was locked but never unlocked; want mutex released")
	}
}

// ---------------------------------------------------------------------------
// TS-11-25: processMergeJob acquires the per-target-branch mutex, validates
// the nonce, sets status to running, records base_sha, and calls git fetch
// origin <targetBranch>.
// Requirement: 11-REQ-5.1
// ---------------------------------------------------------------------------

func TestMerge_AcquiresMutex_ValidatesNonce_SetsRunning_Fetches(t *testing.T) {
	db := openMergeTestDB(t)
	job := insertMergeTestJob(t, db, "merge1", "")
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

	// Verify per-target-branch mutex was acquired.
	if !mockMu.wasLocked() {
		t.Error("per-target-branch mutex was never acquired; want Lock('main') called")
	}

	// Verify base_sha was recorded (non-empty) when status transitioned to running.
	baseSHA := getJobBaseSHA(t, db, job.ID)
	if !baseSHA.Valid || baseSHA.String == "" {
		t.Error("base_sha is empty; want non-empty SHA recorded when status transitions to running")
	}

	// Verify git fetch was called with the correct args.
	assertMockGitCalled(t, mockGit, "fetch", "origin", "main")
}

// ---------------------------------------------------------------------------
// TS-11-26: After a successful fetch, processMergeJob calls git rebase
// origin/<targetBranch> to rebase source_ref onto the freshly fetched
// remote tracking ref.
// Requirement: 11-REQ-5.2
// ---------------------------------------------------------------------------

func TestMerge_RebaseCalledAfterFetch(t *testing.T) {
	db := openMergeTestDB(t)
	job := insertMergeTestJob(t, db, "merge2", "")
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

	// Verify git rebase origin/main was called.
	assertMockGitCalled(t, mockGit, "rebase", "origin/main")

	// Verify ordering: rebase must come AFTER fetch.
	calls := mockGit.recordedCalls()
	fetchIdx, rebaseIdx := -1, -1
	for i, c := range calls {
		if c.Method == "Run" && len(c.Args) >= 1 {
			if c.Args[0] == "fetch" && fetchIdx == -1 {
				fetchIdx = i
			}
			if c.Args[0] == "rebase" && rebaseIdx == -1 {
				rebaseIdx = i
			}
		}
	}
	if fetchIdx >= 0 && rebaseIdx >= 0 && rebaseIdx <= fetchIdx {
		t.Errorf("rebase (call %d) called before or at same index as fetch (call %d); want rebase after fetch",
			rebaseIdx, fetchIdx)
	}
}

// ---------------------------------------------------------------------------
// TS-11-27: When CHECK_COMMAND is configured, processMergeJob executes it
// via sh -c with the correct working directory and CHECK_TIMEOUT.
// Requirement: 11-REQ-5.3
// ---------------------------------------------------------------------------

func TestMerge_CheckCommand_ExecutedWithCorrectParams(t *testing.T) {
	db := openMergeTestDB(t)
	job := insertMergeTestJob(t, db, "merge3", "")
	workspaceRoot := t.TempDir()

	mockGit := newHappyPathMockGitOps()
	mockMu := newMockBranchLocker()

	// Configure CHECK_COMMAND and CHECK_TIMEOUT workspace variables.
	mockVars := &mockVariableGetter{
		vars: map[string]string{
			"CHECK_COMMAND": "make test",
			"CHECK_TIMEOUT": "5m",
		},
	}

	// Capture the check runner invocation parameters.
	var capturedDir, capturedCommand string
	var capturedTimeout time.Duration
	checkCalled := false

	deps := MergeDeps{
		Git:           mockGit,
		Locker:        mockMu,
		Variables:     mockVars,
		WorkspaceRoot: workspaceRoot,
		RunCheck: func(_ context.Context, dir, command string, timeout time.Duration) ([]byte, error) {
			checkCalled = true
			capturedDir = dir
			capturedCommand = command
			capturedTimeout = timeout
			return nil, nil
		},
	}

	err := processMergeJob(context.Background(), db, job, deps)
	if err != nil {
		t.Fatalf("processMergeJob() returned error: %v", err)
	}

	if !checkCalled {
		t.Fatal("check runner was not called; want CHECK_COMMAND to be executed after rebase")
	}

	// Verify working directory is <WORKSPACE_ROOT>/<slug>/trunk.
	expectedDir := filepath.Join(workspaceRoot, "test-ws", "trunk")
	if capturedDir != expectedDir {
		t.Errorf("check dir = %q; want %q", capturedDir, expectedDir)
	}

	// Verify command matches the CHECK_COMMAND variable value.
	if capturedCommand != "make test" {
		t.Errorf("check command = %q; want 'make test'", capturedCommand)
	}

	// Verify timeout is 5 minutes (from CHECK_TIMEOUT variable).
	if capturedTimeout != 5*time.Minute {
		t.Errorf("check timeout = %v; want 5m0s", capturedTimeout)
	}
}

// TestMerge_CheckCommand_DefaultTimeout verifies that CHECK_TIMEOUT defaults
// to 10 minutes when the workspace variable is not configured.
func TestMerge_CheckCommand_DefaultTimeout(t *testing.T) {
	db := openMergeTestDB(t)
	job := insertMergeTestJob(t, db, "merge3b", "")
	workspaceRoot := t.TempDir()

	mockGit := newHappyPathMockGitOps()
	mockMu := newMockBranchLocker()

	// Configure CHECK_COMMAND but NOT CHECK_TIMEOUT.
	mockVars := &mockVariableGetter{
		vars: map[string]string{
			"CHECK_COMMAND": "make lint",
		},
	}

	var capturedTimeout time.Duration
	checkCalled := false

	deps := MergeDeps{
		Git:           mockGit,
		Locker:        mockMu,
		Variables:     mockVars,
		WorkspaceRoot: workspaceRoot,
		RunCheck: func(_ context.Context, _, _ string, timeout time.Duration) ([]byte, error) {
			checkCalled = true
			capturedTimeout = timeout
			return nil, nil
		},
	}

	err := processMergeJob(context.Background(), db, job, deps)
	if err != nil {
		t.Fatalf("processMergeJob() returned error: %v", err)
	}

	if !checkCalled {
		t.Fatal("check runner was not called; want CHECK_COMMAND to be executed")
	}

	// Verify timeout defaults to 10 minutes.
	if capturedTimeout != 10*time.Minute {
		t.Errorf("check timeout = %v; want 10m0s (default)", capturedTimeout)
	}
}

// ---------------------------------------------------------------------------
// TS-11-28: After the check step passes (or is skipped), processMergeJob
// calls git push origin <targetBranch> and sets status to merged with
// merged_sha recorded.
// Requirement: 11-REQ-5.4
// ---------------------------------------------------------------------------

func TestMerge_PushAndMergedStatus_WhenCheckSkipped(t *testing.T) {
	db := openMergeTestDB(t)
	job := insertMergeTestJob(t, db, "merge4", "")
	workspaceRoot := t.TempDir()

	mockGit := newHappyPathMockGitOps()
	mockMu := newMockBranchLocker()

	// No CHECK_COMMAND configured (Variables is nil) - check step should be skipped.
	deps := MergeDeps{
		Git:           mockGit,
		Locker:        mockMu,
		WorkspaceRoot: workspaceRoot,
	}

	err := processMergeJob(context.Background(), db, job, deps)
	if err != nil {
		t.Fatalf("processMergeJob() returned error: %v", err)
	}

	// Verify git push origin main was called.
	assertMockGitCalled(t, mockGit, "push", "origin", "main")

	// Verify job status is 'merged'.
	status := getJobStatus(t, db, job.ID)
	if status != "merged" {
		t.Errorf("job status = %q; want 'merged'", status)
	}

	// Verify merged_sha is recorded (non-empty).
	mergedSHA := getJobMergedSHA(t, db, job.ID)
	if !mergedSHA.Valid || mergedSHA.String == "" {
		t.Error("merged_sha is empty; want non-empty SHA recorded after successful push")
	}
}

// TestMerge_PushAndMergedStatus_WhenCheckPasses verifies the push and merged
// status when the check step is configured and passes successfully.
func TestMerge_PushAndMergedStatus_WhenCheckPasses(t *testing.T) {
	db := openMergeTestDB(t)
	job := insertMergeTestJob(t, db, "merge4b", "")
	workspaceRoot := t.TempDir()

	mockGit := newHappyPathMockGitOps()
	mockMu := newMockBranchLocker()

	mockVars := &mockVariableGetter{
		vars: map[string]string{
			"CHECK_COMMAND": "make test",
		},
	}

	deps := MergeDeps{
		Git:           mockGit,
		Locker:        mockMu,
		Variables:     mockVars,
		WorkspaceRoot: workspaceRoot,
		RunCheck: func(_ context.Context, _, _ string, _ time.Duration) ([]byte, error) {
			return []byte("ok"), nil
		},
	}

	err := processMergeJob(context.Background(), db, job, deps)
	if err != nil {
		t.Fatalf("processMergeJob() returned error: %v", err)
	}

	// Verify git push was called.
	assertMockGitCalled(t, mockGit, "push", "origin", "main")

	// Verify final status is merged.
	status := getJobStatus(t, db, job.ID)
	if status != "merged" {
		t.Errorf("job status = %q; want 'merged'", status)
	}

	// Verify merged_sha is recorded.
	mergedSHA := getJobMergedSHA(t, db, job.ID)
	if !mergedSHA.Valid || mergedSHA.String == "" {
		t.Error("merged_sha is empty; want non-empty SHA recorded after successful push")
	}
}

// ---------------------------------------------------------------------------
// TS-11-29: PostMergeHook is invoked synchronously after merged status is
// set for campaign merges; hook errors are logged but do not change job status.
// Requirement: 11-REQ-5.5
// ---------------------------------------------------------------------------

func TestMerge_PostMergeHook_CalledAndErrorIgnored(t *testing.T) {
	db := openMergeTestDB(t)
	// Use a non-empty campaign_id to trigger hook invocation.
	campaignID := newTestUUID("campaign1")
	job := insertMergeTestJob(t, db, "merge5", campaignID)
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
			return errors.New("hook error: notification failed")
		},
	}

	err := processMergeJob(context.Background(), db, job, deps)
	if err != nil {
		t.Fatalf("processMergeJob() returned error: %v", err)
	}

	// Verify PostMergeHook was called.
	if !hookCalled {
		t.Fatal("PostMergeHook was not called; want hook invoked for campaign merge")
	}

	// Verify hook received the correct job.
	if hookReceivedJob.ID != job.ID {
		t.Errorf("hook received job ID %q; want %q", hookReceivedJob.ID, job.ID)
	}

	// Verify job status remains 'merged' despite hook error.
	status := getJobStatus(t, db, job.ID)
	if status != "merged" {
		t.Errorf("job status = %q; want 'merged' (hook errors must not change status)", status)
	}
}

// TestMerge_PostMergeHook_NotCalledForStandaloneMerge verifies that the
// PostMergeHook is not invoked when campaign_id is NULL (standalone merge).
func TestMerge_PostMergeHook_NotCalledForStandaloneMerge(t *testing.T) {
	db := openMergeTestDB(t)
	// Empty campaign_id = standalone merge.
	job := insertMergeTestJob(t, db, "merge5b", "")
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

	if hookCalled {
		t.Error("PostMergeHook was called for standalone merge; want hook skipped when campaign_id is NULL")
	}
}

// ---------------------------------------------------------------------------
// TS-11-30: When git rebase exits with a conflict, processMergeJob runs
// git diff --name-only --diff-filter=U, then git rebase --abort, sets
// status to conflict with conflict_details, and releases the mutex.
// Requirement: 11-REQ-5.E1
// ---------------------------------------------------------------------------

func TestMerge_RebaseConflict_CollectsPathsAndAborts(t *testing.T) {
	db := openMergeTestDB(t)
	job := insertMergeTestJob(t, db, "merge6", "")
	workspaceRoot := t.TempDir()

	rebaseAbortCalled := false

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
					return []byte(testSourceHead + "\n"), nil, nil
				case "fetch":
					return nil, nil, nil
				case "rebase":
					// Check if this is a rebase --abort call.
					for _, a := range args {
						if a == "--abort" {
							rebaseAbortCalled = true
							return nil, nil, nil
						}
					}
					// Real rebase fails with conflict.
					return nil, []byte("CONFLICT\n"), &gitcmd.GitError{
						Command:  strings.Join(args, " "),
						ExitCode: 1,
						Stderr:   "CONFLICT (content): Merge conflict in file1.go",
					}
				case "diff":
					// git diff --name-only --diff-filter=U returns unmerged file paths.
					return []byte("file1.go\nfile2.go\n"), nil, nil
				}
			}
			return nil, nil, nil
		},
		onRunExitCode: func(_ context.Context, _ ...string) ([]byte, []byte, int, error) {
			// Dry-run passes (no conflicts detected early).
			return nil, nil, 0, nil
		},
	}

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

	// Verify git diff --name-only --diff-filter=U was called to collect unmerged paths.
	assertMockGitCalled(t, mockGit, "diff", "--name-only", "--diff-filter=U")

	// Verify git rebase --abort was called to clean the integration branch.
	if !rebaseAbortCalled {
		t.Error("git rebase --abort was not called; want rebase aborted after conflict to clean integration branch")
	}

	// Verify job status is 'conflict'.
	status := getJobStatus(t, db, job.ID)
	if status != "conflict" {
		t.Errorf("job status = %q; want 'conflict'", status)
	}

	// Verify conflict_details contains the conflicting file paths as JSON array.
	details := getJobConflictDetails(t, db, job.ID)
	if !details.Valid {
		t.Fatal("conflict_details is NULL; want JSON array of conflicting file paths")
	}

	var files []string
	if err := json.Unmarshal([]byte(details.String), &files); err != nil {
		t.Fatalf("conflict_details is not valid JSON: %v (raw: %q)", err, details.String)
	}

	wantFiles := map[string]bool{"file1.go": false, "file2.go": false}
	for _, f := range files {
		if _, ok := wantFiles[f]; ok {
			wantFiles[f] = true
		}
	}
	for file, found := range wantFiles {
		if !found {
			t.Errorf("conflict_details does not contain %q; got %v", file, files)
		}
	}

	// Verify mutex was released after the conflict.
	assertMutexReleased(t, mockMu)
}

// ---------------------------------------------------------------------------
// TS-11-31: When the check command exits with a non-zero exit code,
// processMergeJob sets status to check_failed and stores captured output
// in check_output.
// Requirement: 11-REQ-5.E2
// ---------------------------------------------------------------------------

func TestMerge_CheckCommandFailure_SetsCheckFailed(t *testing.T) {
	db := openMergeTestDB(t)
	job := insertMergeTestJob(t, db, "merge7", "")
	workspaceRoot := t.TempDir()

	mockGit := newHappyPathMockGitOps()
	mockMu := newMockBranchLocker()

	// Configure CHECK_COMMAND workspace variable.
	mockVars := &mockVariableGetter{
		vars: map[string]string{
			"CHECK_COMMAND": "make test",
		},
	}

	deps := MergeDeps{
		Git:           mockGit,
		Locker:        mockMu,
		Variables:     mockVars,
		WorkspaceRoot: workspaceRoot,
		RunCheck: func(_ context.Context, _, _ string, _ time.Duration) ([]byte, error) {
			return []byte("FAIL: test_foo"), errors.New("exit status 1")
		},
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

	// Verify check_output contains the captured stdout/stderr.
	checkOutput := getJobCheckOutput(t, db, job.ID)
	if !checkOutput.Valid {
		t.Fatal("check_output is NULL; want captured stdout/stderr from failed check command")
	}
	if !strings.Contains(checkOutput.String, "FAIL: test_foo") {
		t.Errorf("check_output = %q; want to contain 'FAIL: test_foo'", checkOutput.String)
	}

	// Verify mutex was released.
	assertMutexReleased(t, mockMu)
}

// ---------------------------------------------------------------------------
// TS-11-32: When the fast-forward push fails, processMergeJob sets status
// to push_failed and releases the mutex.
// Requirement: 11-REQ-5.E3
// ---------------------------------------------------------------------------

func TestMerge_PushFailure_SetsPushFailed(t *testing.T) {
	db := openMergeTestDB(t)
	job := insertMergeTestJob(t, db, "merge8", "")
	workspaceRoot := t.TempDir()

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
					return []byte(testSourceHead + "\n"), nil, nil
				case "fetch", "rebase":
					return nil, nil, nil
				case "push":
					// Push fails with a non-fast-forward rejection.
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

	deps := MergeDeps{
		Git:           mockGit,
		Locker:        mockMu,
		WorkspaceRoot: workspaceRoot,
	}

	err := processMergeJob(context.Background(), db, job, deps)
	if err != nil {
		t.Fatalf("processMergeJob() returned error: %v", err)
	}

	// Verify job status is 'push_failed'.
	status := getJobStatus(t, db, job.ID)
	if status != "push_failed" {
		t.Errorf("job status = %q; want 'push_failed'", status)
	}

	// Verify mutex was released after push failure.
	assertMutexReleased(t, mockMu)
}
