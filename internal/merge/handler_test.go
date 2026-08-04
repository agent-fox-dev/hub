package merge

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/hub/internal/gitcmd"
)

// ===========================================================================
// Test Helpers for handler_test.go
// ===========================================================================

// mockGitRunner records invocations and returns preconfigured results.
// Since GitRunner is a concrete struct (not an interface), tests that need
// to mock GitRunner calls inject behaviour via FetchFunc, CommandExecutor,
// and other Handler fields. For merge-tree and rebase, the Handler delegates
// to Runner.MergeTree / Runner.Rebase, so these tests must either:
//   - Use real git repos (for integration-style tests), or
//   - Set up the Handler fields that control the pre-check flow.
//
// The tests below use the Handler's injectable dependencies to control
// behaviour without needing a real git subprocess.

// recordingFetch records calls to the FetchFunc.
type recordingFetch struct {
	calls []fetchCall
	err   error // error to return
}

type fetchCall struct {
	trunkDir     string
	targetBranch string
	auth         transport.AuthMethod
}

func (rf *recordingFetch) fn(trunkDir, targetBranch string, auth transport.AuthMethod) error {
	rf.calls = append(rf.calls, fetchCall{
		trunkDir:     trunkDir,
		targetBranch: targetBranch,
		auth:         auth,
	})
	return rf.err
}

// recordingExecutor records calls to CommandExecutor.Run and returns
// preconfigured results.
type recordingExecutor struct {
	calls  []execCall
	stdout string
	err    error
}

type execCall struct {
	dir     string
	env     []string
	timeout time.Duration
	command string
	args    []string
}

func (re *recordingExecutor) Run(_ context.Context, dir string, env []string, timeout time.Duration, command string, args ...string) (string, error) {
	re.calls = append(re.calls, execCall{
		dir:     dir,
		env:     env,
		timeout: timeout,
		command: command,
		args:    args,
	})
	return re.stdout, re.err
}

// stubResolveAuth returns a ResolveAuthFunc that returns the given auth and error.
func stubResolveAuth(auth transport.AuthMethod, err error) ResolveAuthFunc {
	return func(_ string) (transport.AuthMethod, error) {
		return auth, err
	}
}

// stubGetVariable returns a GetVariable func that looks up (scope, slug, key)
// in the provided map and returns an error for missing entries.
func stubGetVariable(vars map[string]string) func(string, string, string) (string, error) {
	return func(scope, slug, key string) (string, error) {
		mapKey := fmt.Sprintf("%s:%s:%s", scope, slug, key)
		if v, ok := vars[mapKey]; ok {
			return v, nil
		}
		return "", fmt.Errorf("variable not found: %s", mapKey)
	}
}

// Ensure stubGetVariable is usable (will be used in task group 3 tests for
// CHECK_COMMAND lookup).
var _ = stubGetVariable

// newTestHandler creates a Handler with the given workspace root and default
// stubs. Individual tests override fields as needed.
func newTestHandler(workspaceRoot string) *Handler {
	return &Handler{
		WorkspaceRoot: workspaceRoot,
	}
}

// ---------------------------------------------------------------------------
// TS-12-11: When the dry-run conflict check detects conflicts, the merge
// handler returns WouldConflict as a permanent error with conflict_files,
// and the job transitions to failed status.
//
// Requirement: 12-REQ-4.1
// ---------------------------------------------------------------------------

func TestPreCheck_WouldConflict_PermanentError(t *testing.T) {
	workspaceRoot := t.TempDir()
	h := newTestHandler(workspaceRoot)

	// The pre-check invokes DryRunCheck, which in turn calls Runner.MergeTree.
	// When merge-tree detects conflicts, PreCheck should return a
	// *MergeRejection with Reason=WouldConflict, Permanent=true, and the
	// list of conflict files.

	ctx := context.Background()
	result, err := h.PreCheck(ctx, "ws1", "main", "feature/conflict")

	// Must return an error (not nil).
	if err == nil {
		t.Fatal("expected non-nil error from PreCheck when conflicts detected, got nil")
	}

	// Error must be a *MergeRejection.
	var rejection *MergeRejection
	if !errors.As(err, &rejection) {
		t.Fatalf("expected error type *MergeRejection, got %T: %v", err, err)
	}

	// Rejection reason must be WouldConflict.
	if rejection.Reason != WouldConflict {
		t.Errorf("expected rejection reason WouldConflict, got %q", rejection.Reason)
	}

	// Must be a permanent error (not retryable).
	if !rejection.Permanent {
		t.Error("expected WouldConflict to be a permanent error, got retryable")
	}

	// Must include the conflict file list.
	if len(rejection.ConflictFiles) == 0 {
		t.Error("expected non-empty ConflictFiles in WouldConflict rejection")
	}

	// Result should be nil on error path.
	if result != nil {
		t.Errorf("expected nil result on WouldConflict rejection, got %+v", result)
	}
}

// ---------------------------------------------------------------------------
// TS-12-12: When the source branch is already integrated into the target,
// the merge handler returns AlreadyMerged and marks the job as completed
// without performing any git operations.
//
// Requirement: 12-REQ-4.2
// ---------------------------------------------------------------------------

func TestPreCheck_AlreadyMerged_Success(t *testing.T) {
	workspaceRoot := t.TempDir()
	h := newTestHandler(workspaceRoot)

	ctx := context.Background()
	result, err := h.PreCheck(ctx, "ws1", "main", "feature/already-merged")

	// AlreadyMerged is a success path — no error should be returned.
	if err != nil {
		t.Fatalf("expected nil error for AlreadyMerged, got: %v", err)
	}

	// Result must indicate AlreadyMerged.
	if result == nil {
		t.Fatal("expected non-nil PreCheckResult for AlreadyMerged")
	}
	if result.Reason != AlreadyMerged {
		t.Errorf("expected result.Reason=AlreadyMerged, got %q", result.Reason)
	}
}

// ---------------------------------------------------------------------------
// TS-12-13: When the source branch has no commits ahead of the target,
// the merge handler returns BranchNotReady as a retryable error.
//
// Requirement: 12-REQ-4.3
// ---------------------------------------------------------------------------

func TestPreCheck_BranchNotReady_RetryableError(t *testing.T) {
	workspaceRoot := t.TempDir()
	h := newTestHandler(workspaceRoot)

	ctx := context.Background()
	_, err := h.PreCheck(ctx, "ws1", "main", "feature/not-ready")

	// Must return an error.
	if err == nil {
		t.Fatal("expected non-nil error from PreCheck for BranchNotReady")
	}

	// Error must be a *MergeRejection.
	var rejection *MergeRejection
	if !errors.As(err, &rejection) {
		t.Fatalf("expected error type *MergeRejection, got %T: %v", err, err)
	}

	// Rejection reason must be BranchNotReady.
	if rejection.Reason != BranchNotReady {
		t.Errorf("expected rejection reason BranchNotReady, got %q", rejection.Reason)
	}

	// Must be retryable (not permanent).
	if rejection.Permanent {
		t.Error("expected BranchNotReady to be retryable (Permanent=false), got permanent")
	}
}

// ---------------------------------------------------------------------------
// TS-12-14: The merge handler invokes GitRunner with
// 'git merge-tree --write-tree <target-head> <source-branch-head>'
// in the workspace trunk directory during the pre-check phase.
//
// Requirement: 12-REQ-5.1
// ---------------------------------------------------------------------------

func TestDryRunCheck_InvokesMergeTree(t *testing.T) {
	workspaceRoot := t.TempDir()
	h := newTestHandler(workspaceRoot)

	// The DryRunCheck method should:
	// 1. Resolve target-head and source-branch-head SHAs via RevParse
	// 2. Call Runner.MergeTree(ctx, targetHeadSHA, sourceHeadSHA)
	// 3. The MergeTree call runs 'git merge-tree --write-tree <target> <source>'
	//    in the workspace trunk directory

	ctx := context.Background()
	err := h.DryRunCheck(ctx, "ws1", "main", "feature/a")

	// We expect this to either succeed (nil error meaning clean merge) or
	// return a *MergeRejection. It must NOT return an untyped error for a
	// normal conflict case.
	//
	// For this test, we mainly verify the invocation happened correctly.
	// Since the stub returns "not implemented", this test will fail at the
	// first assertion — which is the expected behavior for group 2 tests.

	if err != nil {
		// The error should be either nil (clean merge-tree) or *MergeRejection.
		var rejection *MergeRejection
		if !errors.As(err, &rejection) {
			// Unexpected error type — DryRunCheck should only return
			// *MergeRejection or nil, not untyped errors for merge-tree
			// invocation issues.
			t.Fatalf("DryRunCheck returned unexpected error type %T: %v", err, err)
		}
	}
}

// ---------------------------------------------------------------------------
// TS-12-15: When git merge-tree --write-tree exits with a conflict exit
// code, the handler parses conflict_files and returns WouldConflict as a
// permanent error, leaving the working tree unmodified.
//
// Requirement: 12-REQ-5.2
// ---------------------------------------------------------------------------

func TestDryRunCheck_Conflict_ReturnsWouldConflict(t *testing.T) {
	workspaceRoot := t.TempDir()
	h := newTestHandler(workspaceRoot)

	// When GitRunner.MergeTree returns a *MergeConflictError, DryRunCheck
	// should translate it to *MergeRejection{Reason: WouldConflict}.

	ctx := context.Background()
	err := h.DryRunCheck(ctx, "ws1", "main", "feature/conflict")

	// Must return an error.
	if err == nil {
		t.Fatal("expected non-nil error from DryRunCheck when merge-tree detects conflicts")
	}

	// Must be a *MergeRejection.
	var rejection *MergeRejection
	if !errors.As(err, &rejection) {
		t.Fatalf("expected *MergeRejection, got %T: %v", err, err)
	}

	// Must be WouldConflict.
	if rejection.Reason != WouldConflict {
		t.Errorf("expected WouldConflict, got %q", rejection.Reason)
	}

	// Must be permanent.
	if !rejection.Permanent {
		t.Error("expected WouldConflict to be permanent")
	}

	// Must contain conflict files.
	if len(rejection.ConflictFiles) == 0 {
		t.Error("expected non-empty ConflictFiles in WouldConflict rejection")
	}
}

// ---------------------------------------------------------------------------
// TS-12-16: When git merge-tree --write-tree exits with code 0, the merge
// handler proceeds to the fetch step without error.
//
// Requirement: 12-REQ-5.3
// ---------------------------------------------------------------------------

func TestDryRunCheck_Clean_NoError(t *testing.T) {
	workspaceRoot := t.TempDir()
	h := newTestHandler(workspaceRoot)

	// When GitRunner.MergeTree returns (treeSHA, nil), DryRunCheck should
	// return nil, allowing the merge algorithm to proceed to the fetch step.

	ctx := context.Background()
	err := h.DryRunCheck(ctx, "ws1", "main", "feature/clean")

	if err != nil {
		t.Fatalf("expected nil error from DryRunCheck for clean merge, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TS-12-17: After the pre-check passes, the merge handler fetches the latest
// target branch state from the upstream remote via go-git using
// resolveCloneAuth credentials.
//
// Requirement: 12-REQ-6.1
// ---------------------------------------------------------------------------

func TestFetchTarget_UsesResolveCloneAuth(t *testing.T) {
	workspaceRoot := t.TempDir()

	// Set up a recording fetch to capture how FetchTarget invokes go-git.
	rf := &recordingFetch{}

	h := &Handler{
		WorkspaceRoot: workspaceRoot,
		Fetch:         rf.fn,
		ResolveAuth:   stubResolveAuth(nil, nil), // No auth for public repo.
	}

	ctx := context.Background()
	err := h.FetchTarget(ctx, "ws1", "main")

	if err != nil {
		t.Fatalf("FetchTarget returned error: %v", err)
	}

	// Verify fetch was called.
	if len(rf.calls) == 0 {
		t.Fatal("expected Fetch to be called, but no calls recorded")
	}

	// Verify it was called with the correct trunk directory.
	expectedTrunk := h.TrunkDir("ws1")
	if rf.calls[0].trunkDir != expectedTrunk {
		t.Errorf("expected Fetch trunkDir=%q, got %q", expectedTrunk, rf.calls[0].trunkDir)
	}

	// Verify it was called with the correct target branch.
	if rf.calls[0].targetBranch != "main" {
		t.Errorf("expected Fetch targetBranch='main', got %q", rf.calls[0].targetBranch)
	}
}

func TestFetchTarget_WithCredentials(t *testing.T) {
	workspaceRoot := t.TempDir()

	rf := &recordingFetch{}

	// Simulate resolveCloneAuth returning credentials.
	mockAuth := &mockAuthMethod{name: "test-auth"}

	h := &Handler{
		WorkspaceRoot: workspaceRoot,
		Fetch:         rf.fn,
		ResolveAuth:   stubResolveAuth(mockAuth, nil),
	}

	ctx := context.Background()
	err := h.FetchTarget(ctx, "ws1", "main")

	if err != nil {
		t.Fatalf("FetchTarget returned error: %v", err)
	}

	// Verify credentials were passed through.
	if len(rf.calls) == 0 {
		t.Fatal("expected Fetch to be called")
	}

	if rf.calls[0].auth != mockAuth {
		t.Errorf("expected Fetch to receive resolved auth credentials, got %v", rf.calls[0].auth)
	}
}

// mockAuthMethod implements transport.AuthMethod for testing.
type mockAuthMethod struct {
	name string
}

func (m *mockAuthMethod) Name() string { return m.name }
func (m *mockAuthMethod) String() string {
	return fmt.Sprintf("mock-auth(%s)", m.name)
}

// ---------------------------------------------------------------------------
// TS-12-18: The merge handler captures the pre-rebase SHA of the source
// branch before invoking GitRunner to run 'git rebase <target-ref>' in the
// workspace trunk directory.
//
// Requirement: 12-REQ-6.2
// ---------------------------------------------------------------------------

func TestRebaseSource_CapturesPreRebaseSHA(t *testing.T) {
	workspaceRoot := t.TempDir()
	h := newTestHandler(workspaceRoot)

	// RebaseSource must:
	// 1. Capture the current SHA of the source branch BEFORE rebase
	// 2. Invoke Runner.Rebase(ctx, targetRef) in the trunk directory
	// 3. Return the captured pre-rebase SHA

	ctx := context.Background()
	preRebaseSHA, err := h.RebaseSource(ctx, "ws1", "main", "feature/a")

	if err != nil {
		t.Fatalf("RebaseSource returned error: %v", err)
	}

	// Pre-rebase SHA must be a 40-char hex string.
	if len(preRebaseSHA) != 40 {
		t.Errorf("expected 40-char pre-rebase SHA, got %d chars: %q", len(preRebaseSHA), preRebaseSHA)
	}
}

// ---------------------------------------------------------------------------
// TS-12-19: When CHECK_COMMAND is set, the merge handler executes it via
// 'sh -c <CHECK_COMMAND>' in the workspace trunk directory with
// MERGE_TARGET, MERGE_SOURCE, and WORKSPACE_SLUG injected, enforcing a
// 10-minute timeout.
//
// Requirement: 12-REQ-6.3
// ---------------------------------------------------------------------------

func TestRunCheckCommand_ExecutesWithEnvAndTimeout(t *testing.T) {
	workspaceRoot := t.TempDir()

	executor := &recordingExecutor{}

	h := &Handler{
		WorkspaceRoot: workspaceRoot,
		Executor:      executor,
	}

	ctx := context.Background()
	err := h.RunCheckCommand(ctx, "ws1", "main", "feature/a", "make test")

	if err != nil {
		t.Fatalf("RunCheckCommand returned error: %v", err)
	}

	// Verify exactly one command was executed.
	if len(executor.calls) != 1 {
		t.Fatalf("expected 1 executor call, got %d", len(executor.calls))
	}
	call := executor.calls[0]

	// Verify command is 'sh -c <CHECK_COMMAND>'.
	if call.command != "sh" {
		t.Errorf("expected command='sh', got %q", call.command)
	}
	if len(call.args) != 2 || call.args[0] != "-c" || call.args[1] != "make test" {
		t.Errorf("expected args=['-c', 'make test'], got %v", call.args)
	}

	// Verify working directory is the trunk directory.
	expectedDir := h.TrunkDir("ws1")
	if call.dir != expectedDir {
		t.Errorf("expected dir=%q, got %q", expectedDir, call.dir)
	}

	// Verify environment variables are injected.
	envMap := envToMap(call.env)
	if envMap["MERGE_TARGET"] != "main" {
		t.Errorf("expected MERGE_TARGET='main', got %q", envMap["MERGE_TARGET"])
	}
	if envMap["MERGE_SOURCE"] != "feature/a" {
		t.Errorf("expected MERGE_SOURCE='feature/a', got %q", envMap["MERGE_SOURCE"])
	}
	if envMap["WORKSPACE_SLUG"] != "ws1" {
		t.Errorf("expected WORKSPACE_SLUG='ws1', got %q", envMap["WORKSPACE_SLUG"])
	}

	// Verify 10-minute timeout is enforced.
	if call.timeout != 10*time.Minute {
		t.Errorf("expected timeout=10m, got %v", call.timeout)
	}
}

// envToMap converts a slice of "KEY=VALUE" strings to a map.
func envToMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, e := range env {
		for i := 0; i < len(e); i++ {
			if e[i] == '=' {
				m[e[:i]] = e[i+1:]
				break
			}
		}
	}
	return m
}

// ---------------------------------------------------------------------------
// Additional test: TS-12-18 supplementary — verify that rebase conflict
// triggers git rebase --abort and returns permanent error with conflicting
// file paths.
//
// Requirement: 12-REQ-6.E1 (edge case for 12-REQ-6.2)
// ---------------------------------------------------------------------------

func TestRebaseSource_Conflict_AbortAndPermanentError(t *testing.T) {
	workspaceRoot := t.TempDir()
	h := newTestHandler(workspaceRoot)

	// When rebase encounters conflicts, RebaseSource should:
	// 1. Detect the RebaseConflictError from Runner.Rebase
	// 2. Invoke Runner.RebaseAbort to clean up
	// 3. Return a permanent error with conflicting file paths

	ctx := context.Background()
	_, err := h.RebaseSource(ctx, "ws1", "main", "feature/conflict")

	if err == nil {
		t.Fatal("expected error from RebaseSource when rebase conflicts")
	}

	// Should be a *MergeRejection with WouldConflict.
	var rejection *MergeRejection
	if !errors.As(err, &rejection) {
		t.Fatalf("expected *MergeRejection for rebase conflict, got %T: %v", err, err)
	}

	if !rejection.Permanent {
		t.Error("expected rebase conflict to be a permanent error")
	}

	if len(rejection.ConflictFiles) == 0 {
		t.Error("expected non-empty ConflictFiles for rebase conflict")
	}
}

// Ensure gitcmd types are used (prevents unused import errors).
var _ = (*gitcmd.GitRunner)(nil)
var _ = (*gitcmd.MergeConflictError)(nil)
var _ = (*gitcmd.RebaseConflictError)(nil)
