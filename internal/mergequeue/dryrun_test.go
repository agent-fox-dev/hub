package mergequeue

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/gitcmd"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Mock GitOps implementation for dry-run conflict check tests
// ---------------------------------------------------------------------------

// mockGitCall records a single method invocation on mockGitOps.
type mockGitCall struct {
	Method string
	Args   []string
}

// mockGitOps implements GitOps with configurable callbacks and call recording.
// All calls are recorded in order so tests can assert on invocation sequence.
type mockGitOps struct {
	mu    sync.Mutex
	calls []mockGitCall

	// onRun is called for each Run invocation. If nil, returns (nil, nil, nil).
	onRun func(ctx context.Context, args ...string) ([]byte, []byte, error)
	// onRunExitCode is called for each RunExitCode invocation. If nil, returns (nil, nil, 0, nil).
	onRunExitCode func(ctx context.Context, args ...string) ([]byte, []byte, int, error)
}

func (m *mockGitOps) Run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	m.mu.Lock()
	argsCopy := make([]string, len(args))
	copy(argsCopy, args)
	m.calls = append(m.calls, mockGitCall{Method: "Run", Args: argsCopy})
	m.mu.Unlock()
	if m.onRun != nil {
		return m.onRun(ctx, args...)
	}
	return nil, nil, nil
}

func (m *mockGitOps) RunExitCode(ctx context.Context, args ...string) ([]byte, []byte, int, error) {
	m.mu.Lock()
	argsCopy := make([]string, len(args))
	copy(argsCopy, args)
	m.calls = append(m.calls, mockGitCall{Method: "RunExitCode", Args: argsCopy})
	m.mu.Unlock()
	if m.onRunExitCode != nil {
		return m.onRunExitCode(ctx, args...)
	}
	return nil, nil, 0, nil
}

func (m *mockGitOps) recordedCalls() []mockGitCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]mockGitCall, len(m.calls))
	copy(result, m.calls)
	return result
}

// ---------------------------------------------------------------------------
// Mock BranchLocker implementation for dry-run conflict check tests
// ---------------------------------------------------------------------------

// mockBranchLocker implements BranchLocker with event recording and an
// optional shared event log to correlate lock timing with git operations.
type mockBranchLocker struct {
	mu     sync.Mutex
	events []string
	locked map[string]bool

	// sharedLog is an optional event log shared with other mocks.
	// When set, Lock/Unlock events are also appended here.
	sharedLog *eventLog
}

func newMockBranchLocker() *mockBranchLocker {
	return &mockBranchLocker{locked: make(map[string]bool)}
}

func (m *mockBranchLocker) Lock(branch string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	event := "mutex-lock:" + branch
	m.events = append(m.events, event)
	m.locked[branch] = true
	if m.sharedLog != nil {
		m.sharedLog.record(event)
	}
}

func (m *mockBranchLocker) Unlock(branch string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	event := "mutex-unlock:" + branch
	m.events = append(m.events, event)
	m.locked[branch] = false
	if m.sharedLog != nil {
		m.sharedLog.record(event)
	}
}

func (m *mockBranchLocker) wasLocked() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.events) > 0
}

// eventLog provides a shared, ordered record of events across mocks.
type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) record(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	result := make([]string, len(l.events))
	copy(result, l.events)
	return result
}

// ---------------------------------------------------------------------------
// Test DB helpers specific to dry-run tests
// ---------------------------------------------------------------------------

// testSHAs used by dry-run tests.
const (
	testTargetHead = "aaa1111111111111111111111111111111111111"
	testSourceHead = "bbb2222222222222222222222222222222222222"
)

// openDryRunTestDB opens an in-memory SQLite database with the merge_jobs
// table created directly via SQL (bypasses InitSchema stub).
func openDryRunTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDBNoSchema(t)
	setupMergeJobsTable(t, db)
	return db
}

// insertQueuedMergeJob inserts a merge job in "queued" status for dry-run
// testing and returns its ID.
func insertQueuedMergeJob(t *testing.T, db *sql.DB, suffix string) *MergeJob {
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
	insertTestMergeJobFull(t, db, job)
	return job
}

// getJobRetryCount reads the current retry_count from the database.
func getJobRetryCount(t *testing.T, db *sql.DB, id string) int {
	t.Helper()
	var count int
	err := db.QueryRow("SELECT retry_count FROM merge_jobs WHERE id = ?", id).Scan(&count)
	if err != nil {
		t.Fatalf("getJobRetryCount(%q) failed: %v", id, err)
	}
	return count
}

// getJobAvailableAt reads the available_at timestamp from the database.
func getJobAvailableAt(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var availableAt string
	err := db.QueryRow("SELECT available_at FROM merge_jobs WHERE id = ?", id).Scan(&availableAt)
	if err != nil {
		t.Fatalf("getJobAvailableAt(%q) failed: %v", id, err)
	}
	return availableAt
}

// getJobConflictDetails reads the conflict_details from the database.
func getJobConflictDetails(t *testing.T, db *sql.DB, id string) sql.NullString {
	t.Helper()
	var details sql.NullString
	err := db.QueryRow("SELECT conflict_details FROM merge_jobs WHERE id = ?", id).Scan(&details)
	if err != nil {
		t.Fatalf("getJobConflictDetails(%q) failed: %v", id, err)
	}
	return details
}

// newDefaultMockGitOps creates a mockGitOps that returns fixed SHAs for
// rev-parse calls and configurable responses for everything else.
// It dispatches Run calls based on the first git argument.
func newDefaultMockGitOps(exitCode int, mergeTreeStderr string, mergeTreeErr error) *mockGitOps {
	return &mockGitOps{
		onRun: func(ctx context.Context, args ...string) ([]byte, []byte, error) {
			if len(args) >= 1 && args[0] == "rev-parse" {
				// Return targetHead for origin/ refs, sourceHead otherwise.
				for _, a := range args {
					if strings.HasPrefix(a, "origin/") {
						return []byte(testTargetHead + "\n"), nil, nil
					}
				}
				return []byte(testSourceHead + "\n"), nil, nil
			}
			// Default: succeed with no output.
			return nil, nil, nil
		},
		onRunExitCode: func(ctx context.Context, args ...string) ([]byte, []byte, int, error) {
			return nil, []byte(mergeTreeStderr), exitCode, mergeTreeErr
		},
	}
}

// ---------------------------------------------------------------------------
// TS-11-19: processMergeJob calls GitRunner.RunExitCode with merge-tree
// --write-tree args before acquiring the mutex.
// Requirement: 11-REQ-4.1
// ---------------------------------------------------------------------------

func TestDryRun_MergeTreeCalledWithCorrectArgs(t *testing.T) {
	db := openDryRunTestDB(t)
	job := insertQueuedMergeJob(t, db, "dryrun1")

	// Mock returns exit code 0 (no conflicts) for merge-tree.
	mockGit := newDefaultMockGitOps(0, "", nil)
	mockMu := newMockBranchLocker()

	err := processMergeJob(context.Background(), db, job, MergeDeps{Git: mockGit, Locker: mockMu})
	if err != nil {
		t.Fatalf("processMergeJob() returned error: %v", err)
	}

	// Find the RunExitCode call with "merge-tree" args.
	calls := mockGit.recordedCalls()
	var mergeTreeCall *mockGitCall
	for i := range calls {
		if calls[i].Method == "RunExitCode" && len(calls[i].Args) >= 1 && calls[i].Args[0] == "merge-tree" {
			mergeTreeCall = &calls[i]
			break
		}
	}

	if mergeTreeCall == nil {
		t.Fatal("RunExitCode was never called with 'merge-tree' arg; want RunExitCode('merge-tree', '--write-tree', targetHead, sourceHead)")
	}

	// Verify args: merge-tree --write-tree <targetHead> <sourceHead>
	if len(mergeTreeCall.Args) < 4 {
		t.Fatalf("merge-tree call has %d args; want at least 4: %v", len(mergeTreeCall.Args), mergeTreeCall.Args)
	}
	if mergeTreeCall.Args[0] != "merge-tree" {
		t.Errorf("arg[0] = %q; want 'merge-tree'", mergeTreeCall.Args[0])
	}
	if mergeTreeCall.Args[1] != "--write-tree" {
		t.Errorf("arg[1] = %q; want '--write-tree'", mergeTreeCall.Args[1])
	}
	if mergeTreeCall.Args[2] != testTargetHead {
		t.Errorf("arg[2] (targetHead) = %q; want %q", mergeTreeCall.Args[2], testTargetHead)
	}
	if mergeTreeCall.Args[3] != testSourceHead {
		t.Errorf("arg[3] (sourceHead) = %q; want %q", mergeTreeCall.Args[3], testSourceHead)
	}
}

// ---------------------------------------------------------------------------
// TS-11-20: When merge-tree returns a non-zero exit code, processMergeJob
// parses conflict file paths from stderr, sets status to conflict, and stores
// paths in conflict_details.
// Requirement: 11-REQ-4.2
// ---------------------------------------------------------------------------

func TestDryRun_NonZeroExitParsesConflictsAndSetsStatus(t *testing.T) {
	db := openDryRunTestDB(t)
	job := insertQueuedMergeJob(t, db, "dryrun2")

	// Merge-tree reports two conflicting files via stderr.
	conflictStderr := "CONFLICT (content): Merge conflict in file1.go\nCONFLICT (content): Merge conflict in file2.go\n"
	mockGit := newDefaultMockGitOps(1, conflictStderr, nil)
	mockMu := newMockBranchLocker()

	err := processMergeJob(context.Background(), db, job, MergeDeps{Git: mockGit, Locker: mockMu})
	if err != nil {
		t.Fatalf("processMergeJob() returned error: %v", err)
	}

	// Job status must be 'conflict'.
	status := getJobStatus(t, db, job.ID)
	if status != "conflict" {
		t.Errorf("job status = %q; want 'conflict'", status)
	}

	// conflict_details must contain the two file paths as a JSON array.
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
}

// ---------------------------------------------------------------------------
// TS-11-21: The dry-run conflict check is performed outside the per-target-
// branch mutex as a best-effort early exit. When the dry-run detects
// conflicts, the mutex is never acquired.
// Requirement: 11-REQ-4.3
// ---------------------------------------------------------------------------

func TestDryRun_ConflictCheckOutsideMutex(t *testing.T) {
	db := openDryRunTestDB(t)
	job := insertQueuedMergeJob(t, db, "dryrun3")

	// Use a shared event log to correlate git and mutex operations.
	log := &eventLog{}

	conflictStderr := "CONFLICT (content): Merge conflict in config.yaml\n"
	mockGit := &mockGitOps{
		onRun: func(ctx context.Context, args ...string) ([]byte, []byte, error) {
			if len(args) >= 1 && args[0] == "rev-parse" {
				for _, a := range args {
					if strings.HasPrefix(a, "origin/") {
						return []byte(testTargetHead + "\n"), nil, nil
					}
				}
				return []byte(testSourceHead + "\n"), nil, nil
			}
			return nil, nil, nil
		},
		onRunExitCode: func(ctx context.Context, args ...string) ([]byte, []byte, int, error) {
			log.record("dry-run")
			return nil, []byte(conflictStderr), 1, nil
		},
	}

	mockMu := newMockBranchLocker()
	mockMu.sharedLog = log

	err := processMergeJob(context.Background(), db, job, MergeDeps{Git: mockGit, Locker: mockMu})
	if err != nil {
		t.Fatalf("processMergeJob() returned error: %v", err)
	}

	events := log.snapshot()

	// Verify dry-run was recorded.
	dryRunSeen := false
	for _, e := range events {
		if e == "dry-run" {
			dryRunSeen = true
		}
	}
	if !dryRunSeen {
		t.Fatal("'dry-run' event not recorded; want RunExitCode (merge-tree) to be called")
	}

	// Verify mutex was NOT acquired when dry-run detects conflicts.
	if mockMu.wasLocked() {
		t.Error("mutex was acquired during dry-run conflict; want mutex to be skipped when dry-run detects conflicts")
	}

	// If events contain both, verify dry-run comes before any mutex event.
	for _, e := range events {
		if strings.HasPrefix(e, "mutex-lock:") {
			t.Errorf("mutex-lock event found in log (%v); want no mutex acquisition on dry-run conflict", events)
			break
		}
	}
}

// ---------------------------------------------------------------------------
// TS-11-22: When dry-run passes but real rebase detects a conflict (TOCTOU),
// job transitions to conflict status and integration branch is not left dirty.
// Requirement: 11-REQ-4.E1
// ---------------------------------------------------------------------------

func TestDryRun_TOCTOURebaseConflictAfterPassingDryRun(t *testing.T) {
	db := openDryRunTestDB(t)
	job := insertQueuedMergeJob(t, db, "dryrun4")

	rebaseAbortCalled := false
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

				case "rebase":
					// Check if this is a rebase --abort call.
					for _, a := range args {
						if a == "--abort" {
							rebaseAbortCalled = true
							return nil, nil, nil
						}
					}
					// Real rebase fails with conflict (TOCTOU window).
					return nil, []byte("CONFLICT: merge conflict\n"), &gitcmd.GitError{
						Command:  strings.Join(args, " "),
						ExitCode: 1,
						Stderr:   "CONFLICT (content): Merge conflict in file1.go",
					}

				case "diff":
					// git diff --name-only --diff-filter=U returns conflicting files.
					return []byte("file1.go\n"), nil, nil

				case "fetch":
					return nil, nil, nil
				}
			}
			return nil, nil, nil
		},
		onRunExitCode: func(ctx context.Context, args ...string) ([]byte, []byte, int, error) {
			// Dry-run passes (exit code 0).
			return nil, nil, 0, nil
		},
	}

	mockMu := newMockBranchLocker()

	err := processMergeJob(context.Background(), db, job, MergeDeps{Git: mockGit, Locker: mockMu})
	if err != nil {
		t.Fatalf("processMergeJob() returned error: %v", err)
	}

	// Job status must be 'conflict' (from the real rebase failure).
	status := getJobStatus(t, db, job.ID)
	if status != "conflict" {
		t.Errorf("job status = %q; want 'conflict' after TOCTOU rebase conflict", status)
	}

	// rebase --abort must have been called to clean up the dirty state.
	if !rebaseAbortCalled {
		t.Error("git rebase --abort was not called; want rebase --abort to clean integration branch")
	}

	// conflict_details should be populated from the real rebase conflict.
	details := getJobConflictDetails(t, db, job.ID)
	if !details.Valid {
		t.Fatal("conflict_details is NULL; want file paths from real rebase conflict")
	}

	var files []string
	if err := json.Unmarshal([]byte(details.String), &files); err != nil {
		t.Fatalf("conflict_details is not valid JSON: %v (raw: %q)", err, details.String)
	}

	foundFile1 := false
	for _, f := range files {
		if f == "file1.go" {
			foundFile1 = true
		}
	}
	if !foundFile1 {
		t.Errorf("conflict_details does not contain 'file1.go'; got %v", files)
	}
}

// ---------------------------------------------------------------------------
// TS-11-23: When RunExitCode returns a non-nil error (subprocess failure)
// during the merge-tree dry-run, the job is re-enqueued with backoff rather
// than marked as conflict.
// Requirement: 11-REQ-4.E2
// ---------------------------------------------------------------------------

func TestDryRun_SubprocessErrorReenqueuesWithBackoff(t *testing.T) {
	db := openDryRunTestDB(t)
	job := insertQueuedMergeJob(t, db, "dryrun5")
	beforeAvailableAt := job.AvailableAt

	// RunExitCode returns a non-nil error (not a non-zero exit code).
	mockGit := newDefaultMockGitOps(0, "", fmt.Errorf("subprocess error: signal killed"))
	mockMu := newMockBranchLocker()

	// processMergeJob should handle the error and re-enqueue.
	_ = processMergeJob(context.Background(), db, job, MergeDeps{Git: mockGit, Locker: mockMu})

	// Job status must remain 'queued' (not 'conflict').
	status := getJobStatus(t, db, job.ID)
	if status != "queued" {
		t.Errorf("job status = %q; want 'queued' (re-enqueued after subprocess error)", status)
	}

	// retry_count must be incremented.
	retryCount := getJobRetryCount(t, db, job.ID)
	if retryCount < 1 {
		t.Errorf("retry_count = %d; want >= 1", retryCount)
	}

	// available_at must be set to a future timestamp (backoff).
	availableAt := getJobAvailableAt(t, db, job.ID)
	if availableAt <= beforeAvailableAt {
		t.Errorf("available_at = %q; want later than original %q (backoff)", availableAt, beforeAvailableAt)
	}

	// Mutex must NOT have been acquired.
	if mockMu.wasLocked() {
		t.Error("mutex was acquired despite subprocess error; want no mutex acquisition on error")
	}
}

// ---------------------------------------------------------------------------
// TS-11-24: When the merge-tree subprocess hangs and the context is
// cancelled, the job is re-enqueued with backoff and not marked as conflict.
// Requirement: 11-REQ-4.E3
// ---------------------------------------------------------------------------

func TestDryRun_ContextCancelledReenqueuesWithBackoff(t *testing.T) {
	db := openDryRunTestDB(t)
	job := insertQueuedMergeJob(t, db, "dryrun6")
	beforeAvailableAt := job.AvailableAt

	// Create a context with a very short deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	// RunExitCode blocks until context is cancelled, then returns context error.
	mockGit := &mockGitOps{
		onRun: func(ctx context.Context, args ...string) ([]byte, []byte, error) {
			if len(args) >= 1 && args[0] == "rev-parse" {
				for _, a := range args {
					if strings.HasPrefix(a, "origin/") {
						return []byte(testTargetHead + "\n"), nil, nil
					}
				}
				return []byte(testSourceHead + "\n"), nil, nil
			}
			return nil, nil, nil
		},
		onRunExitCode: func(ctx context.Context, args ...string) ([]byte, []byte, int, error) {
			// Block until context is cancelled.
			<-ctx.Done()
			return nil, nil, 0, ctx.Err()
		},
	}
	mockMu := newMockBranchLocker()

	// processMergeJob should handle context cancellation gracefully.
	_ = processMergeJob(ctx, db, job, MergeDeps{Git: mockGit, Locker: mockMu})

	// Job status must remain 'queued' (not 'conflict').
	status := getJobStatus(t, db, job.ID)
	if status != "queued" {
		t.Errorf("job status = %q; want 'queued' (re-enqueued after context timeout)", status)
	}

	// retry_count must be incremented.
	retryCount := getJobRetryCount(t, db, job.ID)
	if retryCount < 1 {
		t.Errorf("retry_count = %d; want >= 1", retryCount)
	}

	// available_at must be set to a future timestamp (backoff).
	availableAt := getJobAvailableAt(t, db, job.ID)
	if availableAt <= beforeAvailableAt {
		t.Errorf("available_at = %q; want later than original %q (backoff)", availableAt, beforeAvailableAt)
	}
}
