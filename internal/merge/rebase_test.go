package merge

import (
	"context"
	"errors"
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
