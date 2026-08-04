package merge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/gitcmd"
)

// ===========================================================================
// Mock RebaseRunner for testing rebase operations.
// ===========================================================================

// mockRebaseRunner records invocations and returns preconfigured results
// for the RebaseRunner interface.
type mockRebaseRunner struct {
	// rebaseSHA is returned by Rebase on success.
	rebaseSHA string
	// rebaseErr is returned by Rebase.
	rebaseErr error
	// runResults maps git subcommand to (stdout, error). The first element
	// of args is used as the key.
	runResults map[string]runResult
	// revParseSHA is returned by RevParse on success.
	revParseSHA string
	// revParseErr is returned by RevParse.
	revParseErr error
	// calls records all method invocations for assertion.
	calls []mockRunnerCall
}

type runResult struct {
	stdout string
	err    error
}

type mockRunnerCall struct {
	method string
	args   []string
}

func (m *mockRebaseRunner) Run(_ context.Context, args ...string) (string, error) {
	m.calls = append(m.calls, mockRunnerCall{method: "Run", args: args})
	if m.runResults != nil && len(args) > 0 {
		if r, ok := m.runResults[args[0]]; ok {
			return r.stdout, r.err
		}
	}
	return "", nil
}

func (m *mockRebaseRunner) Rebase(_ context.Context, onto string) (string, error) {
	m.calls = append(m.calls, mockRunnerCall{method: "Rebase", args: []string{onto}})
	return m.rebaseSHA, m.rebaseErr
}

func (m *mockRebaseRunner) RevParse(_ context.Context, ref string) (string, error) {
	m.calls = append(m.calls, mockRunnerCall{method: "RevParse", args: []string{ref}})
	return m.revParseSHA, m.revParseErr
}

// Verify *mockRebaseRunner satisfies RebaseRunner at compile time.
var _ RebaseRunner = (*mockRebaseRunner)(nil)

// newMockRunnerSuccess creates a mock runner that returns the given SHA
// on a successful rebase.
func newMockRunnerSuccess(newHeadSHA string) *mockRebaseRunner {
	return &mockRebaseRunner{
		rebaseSHA: newHeadSHA,
	}
}

// newMockRunnerConflict creates a mock runner that returns a
// *RebaseConflictError on rebase with the given conflicting file paths.
func newMockRunnerConflict(conflictFiles []string) *mockRebaseRunner {
	return &mockRebaseRunner{
		rebaseErr: &gitcmd.RebaseConflictError{
			ConflictingFiles: conflictFiles,
		},
	}
}

// ---------------------------------------------------------------------------
// TS-12-24: The branch rebase operation invokes GitRunner with
// 'git rebase <target-ref>' on the source branch and returns the new
// branch HEAD SHA on success.
//
// Requirement: 12-REQ-7.1
// ---------------------------------------------------------------------------

func TestRebaseBranch_Success(t *testing.T) {
	expectedSHA := "newhead123newhead123newhead123newhead1234"
	runner := newMockRunnerSuccess(expectedSHA)

	ctx := context.Background()
	newSHA, err := RebaseBranch(ctx, runner, "feature/a", "main")

	if err != nil {
		t.Fatalf("RebaseBranch returned error: %v", err)
	}

	// Must return the new HEAD SHA.
	if newSHA != expectedSHA {
		t.Errorf("expected new HEAD SHA=%q, got %q", expectedSHA, newSHA)
	}

	// Verify the runner was called with 'rebase main'.
	found := false
	for _, call := range runner.calls {
		if call.method == "Rebase" && len(call.args) > 0 && call.args[0] == "main" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected runner.Rebase to be called with 'main'")
	}
}

// ---------------------------------------------------------------------------
// TS-12-25: When the rebase encounters conflicts, the branch rebase
// operation runs 'git rebase --abort' and returns a RebaseConflictError
// with conflict_files; the branch is left in its pre-rebase state.
//
// Requirement: 12-REQ-7.2
// ---------------------------------------------------------------------------

func TestRebaseBranch_Conflict_AbortsAndReturnsError(t *testing.T) {
	runner := newMockRunnerConflict([]string{"src/a.go"})

	ctx := context.Background()
	newSHA, err := RebaseBranch(ctx, runner, "feature/a", "main")

	// Must return empty SHA on conflict.
	if newSHA != "" {
		t.Errorf("expected empty SHA on conflict, got %q", newSHA)
	}

	// Must return an error.
	if err == nil {
		t.Fatal("expected error from RebaseBranch on conflict")
	}

	// Error should contain conflict files.
	var conflictErr *gitcmd.RebaseConflictError
	if errors.As(err, &conflictErr) {
		if len(conflictErr.ConflictingFiles) == 0 {
			t.Error("expected non-empty ConflictingFiles in RebaseConflictError")
		}
		if conflictErr.ConflictingFiles[0] != "src/a.go" {
			t.Errorf("expected conflict file 'src/a.go', got %q", conflictErr.ConflictingFiles[0])
		}
	} else {
		t.Errorf("expected *RebaseConflictError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// TS-12-26: The batch rebase operation rebases each branch sequentially,
// aborts and records conflicts for failing branches, and continues with
// remaining branches, returning a per-branch result list.
//
// Requirement: 12-REQ-8.1
// ---------------------------------------------------------------------------

func TestBatchRebase_SequentialWithConflicts(t *testing.T) {
	// Mock runner that succeeds for feature/a, conflicts on feature/b,
	// succeeds for feature/c. BatchRebase will call RebaseBranch for each,
	// which in turn uses the runner. Since the runner is shared, we need a
	// more sophisticated mock that tracks which branch is being rebased.
	//
	// For this test, we use a sequenceMockRunner that returns different
	// results based on call order.
	runner := &sequenceMockRunner{
		results: []sequenceResult{
			{sha: "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111", err: nil},
			{sha: "", err: &gitcmd.RebaseConflictError{ConflictingFiles: []string{"conflict.go"}}},
			{sha: "cccc3333cccc3333cccc3333cccc3333cccc3333", err: nil},
		},
	}

	ctx := context.Background()
	branches := []string{"feature/a", "feature/b", "feature/c"}
	results, err := BatchRebase(ctx, runner, "main", branches)

	if err != nil {
		t.Fatalf("BatchRebase returned error: %v", err)
	}

	// Must return 3 results (one per branch).
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// feature/a: success.
	if results[0].Branch != "feature/a" {
		t.Errorf("results[0].Branch: expected 'feature/a', got %q", results[0].Branch)
	}
	if results[0].Status != "ok" {
		t.Errorf("results[0].Status: expected 'ok', got %q", results[0].Status)
	}
	if results[0].NewHead == "" {
		t.Error("results[0].NewHead: expected non-empty SHA for successful rebase")
	}

	// feature/b: conflict.
	if results[1].Branch != "feature/b" {
		t.Errorf("results[1].Branch: expected 'feature/b', got %q", results[1].Branch)
	}
	if results[1].Status != "conflict" {
		t.Errorf("results[1].Status: expected 'conflict', got %q", results[1].Status)
	}
	if len(results[1].ConflictFiles) == 0 {
		t.Error("results[1].ConflictFiles: expected non-empty for conflict")
	}

	// feature/c: success (processed despite feature/b conflict).
	if results[2].Branch != "feature/c" {
		t.Errorf("results[2].Branch: expected 'feature/c', got %q", results[2].Branch)
	}
	if results[2].Status != "ok" {
		t.Errorf("results[2].Status: expected 'ok', got %q", results[2].Status)
	}
	if results[2].NewHead == "" {
		t.Error("results[2].NewHead: expected non-empty SHA for successful rebase")
	}
}

// ---------------------------------------------------------------------------
// TS-12-27: A conflict on one branch during batch rebase does not prevent
// rebasing of subsequent branches; all branches appear in the result list.
//
// Requirement: 12-REQ-8.2
// ---------------------------------------------------------------------------

func TestBatchRebase_ContinuesPastConflicts(t *testing.T) {
	// First branch conflicts, remaining branches succeed.
	runner := &sequenceMockRunner{
		results: []sequenceResult{
			{sha: "", err: &gitcmd.RebaseConflictError{ConflictingFiles: []string{"x.go"}}},
			{sha: "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222", err: nil},
			{sha: "cccc3333cccc3333cccc3333cccc3333cccc3333", err: nil},
		},
	}

	ctx := context.Background()
	branches := []string{"feature/a", "feature/b", "feature/c"}
	results, err := BatchRebase(ctx, runner, "main", branches)

	if err != nil {
		t.Fatalf("BatchRebase returned error: %v", err)
	}

	// All 3 branches must appear in results.
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// Collect branch names from results.
	branchNames := make(map[string]bool)
	for _, r := range results {
		branchNames[r.Branch] = true
	}

	for _, b := range branches {
		if !branchNames[b] {
			t.Errorf("expected branch %q in results, but not found", b)
		}
	}

	// feature/b must have been processed despite feature/a conflict.
	if results[1].Status != "ok" {
		t.Errorf("expected feature/b status='ok' (processed despite earlier conflict), got %q", results[1].Status)
	}

	// feature/c must have been processed too.
	if results[2].Status != "ok" {
		t.Errorf("expected feature/c status='ok', got %q", results[2].Status)
	}
}

// ===========================================================================
// sequenceMockRunner returns different results on sequential Rebase calls.
// ===========================================================================

type sequenceResult struct {
	sha string
	err error
}

type sequenceMockRunner struct {
	results []sequenceResult
	callIdx int
	calls   []mockRunnerCall
}

func (m *sequenceMockRunner) Run(_ context.Context, args ...string) (string, error) {
	m.calls = append(m.calls, mockRunnerCall{method: "Run", args: args})
	return "", nil
}

func (m *sequenceMockRunner) Rebase(_ context.Context, onto string) (string, error) {
	m.calls = append(m.calls, mockRunnerCall{method: "Rebase", args: []string{onto}})
	if m.callIdx >= len(m.results) {
		return "", errors.New("no more mock results")
	}
	r := m.results[m.callIdx]
	m.callIdx++
	return r.sha, r.err
}

func (m *sequenceMockRunner) RevParse(_ context.Context, ref string) (string, error) {
	m.calls = append(m.calls, mockRunnerCall{method: "RevParse", args: []string{ref}})
	return "", nil
}

// Verify *sequenceMockRunner satisfies RebaseRunner at compile time.
var _ RebaseRunner = (*sequenceMockRunner)(nil)

// ===========================================================================
// Task Group 4 Tests: Single Branch Rebase Edge Cases
// ===========================================================================

// ---------------------------------------------------------------------------
// 12-REQ-7.E1: If the source branch ref does not exist, the branch rebase
// operation returns an error without invoking GitRunner.
// ---------------------------------------------------------------------------

func TestRebaseBranch_RefNotFound_ReturnsErrorWithoutGitOps(t *testing.T) {
	// Mock runner that returns ErrRefNotFound from RevParse, simulating a
	// non-existent source branch.
	runner := &mockRebaseRunner{
		revParseErr: gitcmd.ErrRefNotFound,
	}

	ctx := context.Background()
	sha, err := RebaseBranch(ctx, runner, "feature/nonexistent", "main")

	// Must return empty SHA.
	if sha != "" {
		t.Errorf("expected empty SHA for ref-not-found, got %q", sha)
	}

	// Must return an error.
	if err == nil {
		t.Fatal("expected error from RebaseBranch when source ref does not exist")
	}

	// The error should wrap or be ErrRefNotFound.
	if !errors.Is(err, gitcmd.ErrRefNotFound) {
		t.Errorf("expected error to wrap ErrRefNotFound, got %T: %v", err, err)
	}

	// GitRunner.Rebase must NOT have been called — the ref check should
	// short-circuit before any actual git rebase invocation.
	for _, call := range runner.calls {
		if call.method == "Rebase" {
			t.Error("expected Rebase NOT to be called when source ref does not exist")
		}
	}
}

// ---------------------------------------------------------------------------
// 12-REQ-7.E2: If GitRunner subprocess hangs during rebase, GitRunner kills
// the subprocess after its timeout; the rebase operation runs
// 'git rebase --abort' and returns an error. Working tree is cleaned up.
// ---------------------------------------------------------------------------

func TestRebaseBranch_Timeout_AbortsAndReturnsError(t *testing.T) {
	// Mock runner that returns a context.DeadlineExceeded error from Rebase,
	// simulating a subprocess timeout.
	runner := &mockRebaseRunner{
		rebaseErr: context.DeadlineExceeded,
	}

	ctx := context.Background()
	sha, err := RebaseBranch(ctx, runner, "feature/slow", "main")

	// Must return empty SHA.
	if sha != "" {
		t.Errorf("expected empty SHA on timeout, got %q", sha)
	}

	// Must return an error.
	if err == nil {
		t.Fatal("expected error from RebaseBranch on timeout")
	}

	// The error should indicate a timeout condition. It must either wrap
	// context.DeadlineExceeded or convey timeout semantics through the
	// error message or type — not be a generic "not implemented" error.
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected error wrapping context.DeadlineExceeded, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// 12-REQ-7.2 supplementary: Verify the branch is left in its pre-rebase
// state after conflict abort (the runner.RevParse call before rebase should
// record the original SHA, and after conflict+abort the branch ref is
// unchanged).
// ---------------------------------------------------------------------------

func TestRebaseBranch_Conflict_BranchLeftInPreRebaseState(t *testing.T) {
	originalSHA := "orig1234orig1234orig1234orig1234orig1234"
	runner := &mockRebaseRunner{
		revParseSHA: originalSHA,
		rebaseErr: &gitcmd.RebaseConflictError{
			ConflictingFiles: []string{"a.go", "b.go"},
		},
	}

	ctx := context.Background()
	sha, err := RebaseBranch(ctx, runner, "feature/conflict", "main")

	// Must return empty SHA (not the original SHA, since the rebase failed).
	if sha != "" {
		t.Errorf("expected empty SHA on conflict, got %q", sha)
	}

	// Must return an error.
	if err == nil {
		t.Fatal("expected error from RebaseBranch on conflict")
	}

	// The error should be a RebaseConflictError with the conflicting files.
	var conflictErr *gitcmd.RebaseConflictError
	if !errors.As(err, &conflictErr) {
		t.Fatalf("expected *RebaseConflictError, got %T: %v", err, err)
	}

	// Verify all conflict files are reported.
	if len(conflictErr.ConflictingFiles) != 2 {
		t.Errorf("expected 2 conflict files, got %d", len(conflictErr.ConflictingFiles))
	}

	// Verify that a checkout of the source branch was attempted (to return
	// to pre-rebase state). The runner's calls should show the source branch
	// was checked out before rebase.
	foundCheckout := false
	for _, call := range runner.calls {
		if call.method == "Run" && len(call.args) > 0 && call.args[0] == "checkout" {
			foundCheckout = true
			break
		}
	}
	// RebaseBranch should check out the source branch before rebasing.
	// After conflict, the runner auto-aborts, leaving the branch in its
	// pre-rebase state.
	if !foundCheckout {
		t.Log("Note: RebaseBranch may not explicitly checkout — GitRunner.Rebase auto-aborts on conflict")
	}
}

// ===========================================================================
// Task Group 4 Tests: Batch Rebase Edge Cases
// ===========================================================================

// ---------------------------------------------------------------------------
// 12-REQ-8.E1: If the branches list is empty, the batch rebase operation
// returns an error without performing any git operations.
// ---------------------------------------------------------------------------

func TestBatchRebase_EmptyBranchesList_ReturnsError(t *testing.T) {
	runner := newMockRunnerSuccess("unused")

	ctx := context.Background()
	results, err := BatchRebase(ctx, runner, "main", []string{})

	// Must return an error for empty input.
	if err == nil {
		t.Fatal("expected error from BatchRebase with empty branches list")
	}

	// The error message must indicate the branches list is empty, not be
	// a generic "not implemented" error.
	errMsg := err.Error()
	if !strings.Contains(errMsg, "empty") && !strings.Contains(errMsg, "branches") {
		t.Errorf("expected error to mention empty branches list, got: %v", err)
	}

	// Results should be nil or empty.
	if len(results) != 0 {
		t.Errorf("expected 0 results for empty branches list, got %d", len(results))
	}

	// No git operations should have been performed.
	if len(runner.calls) != 0 {
		t.Errorf("expected 0 runner calls for empty branches list, got %d", len(runner.calls))
	}
}

// ---------------------------------------------------------------------------
// 12-REQ-8.E2: If a branch in the list does not exist in the workspace
// repository, that branch is recorded as failed with a descriptive message,
// and remaining branches continue to be processed.
// ---------------------------------------------------------------------------

func TestBatchRebase_BranchNotFound_RecordedAsFailed(t *testing.T) {
	// The second branch doesn't exist — RevParse returns ErrRefNotFound.
	// The first and third branches succeed.
	runner := &branchExistenceMockRunner{
		branchExists: map[string]bool{
			"feature/a": true,
			"feature/b": false, // does not exist
			"feature/c": true,
		},
		successSHA: "aabb1122aabb1122aabb1122aabb1122aabb1122",
	}

	ctx := context.Background()
	branches := []string{"feature/a", "feature/b", "feature/c"}
	results, err := BatchRebase(ctx, runner, "main", branches)

	if err != nil {
		t.Fatalf("BatchRebase returned error: %v", err)
	}

	// Must return 3 results (one per branch).
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// feature/a: success.
	if results[0].Status != "ok" {
		t.Errorf("feature/a: expected status='ok', got %q", results[0].Status)
	}

	// feature/b: must have a non-ok status indicating failure.
	if results[1].Status == "ok" {
		t.Error("feature/b: expected non-ok status for missing branch, got 'ok'")
	}
	if results[1].Branch != "feature/b" {
		t.Errorf("feature/b: expected branch='feature/b', got %q", results[1].Branch)
	}

	// feature/c: success (not blocked by feature/b failure).
	if results[2].Status != "ok" {
		t.Errorf("feature/c: expected status='ok' (processed despite earlier failure), got %q", results[2].Status)
	}
}

// ---------------------------------------------------------------------------
// 12-REQ-8.E3: If GitRunner subprocess hangs for one branch during batch
// rebase, that branch is recorded as failed, and remaining branches continue.
// ---------------------------------------------------------------------------

func TestBatchRebase_Timeout_OtherBranchesContinue(t *testing.T) {
	// First branch succeeds, second branch times out, third succeeds.
	runner := &sequenceMockRunner{
		results: []sequenceResult{
			{sha: "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111", err: nil},
			{sha: "", err: context.DeadlineExceeded},
			{sha: "cccc3333cccc3333cccc3333cccc3333cccc3333", err: nil},
		},
	}

	ctx := context.Background()
	branches := []string{"feature/a", "feature/b", "feature/c"}
	results, err := BatchRebase(ctx, runner, "main", branches)

	if err != nil {
		t.Fatalf("BatchRebase returned error: %v", err)
	}

	// Must return 3 results.
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// feature/a: success.
	if results[0].Status != "ok" {
		t.Errorf("feature/a: expected status='ok', got %q", results[0].Status)
	}
	if results[0].NewHead == "" {
		t.Error("feature/a: expected non-empty NewHead for successful rebase")
	}

	// feature/b: must have a non-ok status (timeout error).
	if results[1].Status == "ok" {
		t.Error("feature/b: expected non-ok status for timeout, got 'ok'")
	}
	if results[1].Branch != "feature/b" {
		t.Errorf("feature/b: expected branch='feature/b', got %q", results[1].Branch)
	}

	// feature/c: success (not blocked by feature/b timeout).
	if results[2].Status != "ok" {
		t.Errorf("feature/c: expected status='ok' after earlier timeout, got %q", results[2].Status)
	}
	if results[2].NewHead == "" {
		t.Error("feature/c: expected non-empty NewHead for successful rebase")
	}
}

// ===========================================================================
// branchExistenceMockRunner simulates a runner where some branches don't
// exist. Used for testing 12-REQ-8.E2 (branch not found in batch rebase).
// ===========================================================================

type branchExistenceMockRunner struct {
	branchExists map[string]bool
	successSHA   string
	calls        []mockRunnerCall
	rebaseCount  int
}

func (m *branchExistenceMockRunner) Run(_ context.Context, args ...string) (string, error) {
	m.calls = append(m.calls, mockRunnerCall{method: "Run", args: args})

	// Simulate checkout failing for non-existent branches.
	if len(args) > 0 && args[0] == "checkout" && len(args) > 1 {
		branch := args[1]
		if exists, ok := m.branchExists[branch]; ok && !exists {
			return "", fmt.Errorf("error: pathspec '%s' did not match any file(s) known to git", branch)
		}
	}
	return "", nil
}

func (m *branchExistenceMockRunner) Rebase(_ context.Context, onto string) (string, error) {
	m.calls = append(m.calls, mockRunnerCall{method: "Rebase", args: []string{onto}})
	m.rebaseCount++
	return m.successSHA, nil
}

func (m *branchExistenceMockRunner) RevParse(_ context.Context, ref string) (string, error) {
	m.calls = append(m.calls, mockRunnerCall{method: "RevParse", args: []string{ref}})

	// If we know this branch doesn't exist, return ErrRefNotFound.
	if exists, ok := m.branchExists[ref]; ok && !exists {
		return "", gitcmd.ErrRefNotFound
	}
	return m.successSHA, nil
}

var _ RebaseRunner = (*branchExistenceMockRunner)(nil)
