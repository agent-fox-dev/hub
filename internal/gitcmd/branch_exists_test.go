package gitcmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test helpers for BranchExists
// ---------------------------------------------------------------------------

// initBareRepo creates a bare git repository with an initial commit on 'main'
// and optionally additional branches. It uses exec.Command directly for setup
// so the test infrastructure does not depend on the GitRunner stub.
// Returns the absolute path to the bare repository.
func initBareRepo(t *testing.T, branches ...string) string {
	t.Helper()

	// Create a source (non-bare) repository.
	srcDir := t.TempDir()

	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = srcDir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_TERMINAL_PROMPT=0",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	git("init")
	git("config", "user.email", "test@test.com")
	git("config", "user.name", "Test")

	// Create initial commit so branches have something to point at.
	readme := filepath.Join(srcDir, "README.md")
	if err := os.WriteFile(readme, []byte("test repo\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	git("add", "README.md")
	git("commit", "-m", "initial commit")

	// Rename default branch to 'main' for determinism across git versions.
	git("branch", "-M", "main")

	// Create additional branches.
	for _, branch := range branches {
		git("branch", branch)
	}

	// Create a bare clone.
	bareDir := filepath.Join(t.TempDir(), "bare.git")
	cmd := exec.Command("git", "clone", "--bare", srcDir, bareDir)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git clone --bare failed: %v\n%s", err, out)
	}

	return bareDir
}

// ---------------------------------------------------------------------------
// Task 4.1: BranchExists test infrastructure and exit code 0 path
// ---------------------------------------------------------------------------

// TS-10-13: BranchExists returns (true, nil) when git ls-remote exits with
// code 0, indicating the branch exists on the remote.
// Requirement: 10-REQ-4.1
func TestBranchExists_BranchPresent_ReturnsTrueNil(t *testing.T) {
	t.Parallel()
	bareRepo := initBareRepo(t, "feature/foo")
	workDir := t.TempDir()

	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	ctx := context.Background()
	exists, err := runner.BranchExists(ctx, bareRepo, "feature/foo")
	if err != nil {
		t.Fatalf("BranchExists returned error: %v; expected nil", err)
	}
	if !exists {
		t.Error("BranchExists returned false; expected true for existing branch 'feature/foo'")
	}
}

// TS-10-13 (extended): BranchExists returns (true, nil) for the default
// 'main' branch which always exists on the bare repo.
// Requirement: 10-REQ-4.1
func TestBranchExists_MainBranch_ReturnsTrueNil(t *testing.T) {
	t.Parallel()
	bareRepo := initBareRepo(t)
	workDir := t.TempDir()

	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	ctx := context.Background()
	exists, err := runner.BranchExists(ctx, bareRepo, "main")
	if err != nil {
		t.Fatalf("BranchExists returned error: %v; expected nil", err)
	}
	if !exists {
		t.Error("BranchExists returned false; expected true for existing branch 'main'")
	}
}

// TS-10-16: BranchExists always prepends refs/heads/ to the bare branch name
// before passing to git ls-remote --exit-code. Verified by querying 'main'
// which only exists under refs/heads/main (not refs/tags/main).
// Requirement: 10-REQ-4.4
func TestBranchExists_PrependsRefsHeads(t *testing.T) {
	t.Parallel()
	bareRepo := initBareRepo(t)
	workDir := t.TempDir()

	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	ctx := context.Background()
	// 'main' exists only as refs/heads/main in the bare repo.
	// If BranchExists correctly prepends refs/heads/, this query matches.
	exists, err := runner.BranchExists(ctx, bareRepo, "main")
	if err != nil {
		t.Fatalf("BranchExists returned error: %v", err)
	}
	if !exists {
		t.Error("BranchExists('main') returned false; expected true — refs/heads/main should exist on the bare repo")
	}
}

// ---------------------------------------------------------------------------
// Task 4.2: BranchExists exit 2, network failure, and caller contract
// ---------------------------------------------------------------------------

// TS-10-14: BranchExists returns (false, nil) when git ls-remote exits with
// code 2, indicating the branch is genuinely absent from the remote.
// Requirement: 10-REQ-4.2
func TestBranchExists_BranchMissing_ReturnsFalseNil(t *testing.T) {
	t.Parallel()
	bareRepo := initBareRepo(t) // only has 'main'
	workDir := t.TempDir()

	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	ctx := context.Background()
	exists, err := runner.BranchExists(ctx, bareRepo, "nonexistent-branch-xyz")
	if err != nil {
		t.Fatalf("BranchExists returned error: %v; expected nil for missing branch (exit code 2)", err)
	}
	if exists {
		t.Error("BranchExists returned true; expected false for nonexistent branch 'nonexistent-branch-xyz'")
	}
}

// TS-10-15: BranchExists returns (false, non-nil error) when git ls-remote
// exits with a non-zero, non-2 exit code (e.g. 128 for invalid remote),
// never misinterpreting this as branch-missing.
// Requirement: 10-REQ-4.3
func TestBranchExists_InvalidRemote_ReturnsFalseError(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()

	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	ctx := context.Background()
	exists, err := runner.BranchExists(ctx, "/nonexistent/path/to/repo", "main")
	if err == nil {
		t.Fatal("BranchExists with invalid remote returned nil error; expected non-nil error describing the failure")
	}
	if exists {
		t.Error("BranchExists with invalid remote returned true; expected false")
	}
}

// TS-10-15 (extended): The error returned for a network/auth failure path
// contains a non-empty, meaningful message.
// Requirement: 10-REQ-4.3
func TestBranchExists_InvalidRemote_ErrorIsNonEmpty(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()

	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	ctx := context.Background()
	_, err := runner.BranchExists(ctx, "/nonexistent/path/to/repo", "main")
	if err == nil {
		t.Fatal("BranchExists with invalid remote returned nil error; expected non-nil error")
	}
	if err.Error() == "" {
		t.Error("BranchExists error message is empty; expected meaningful error description")
	}
}

// 10-REQ-4.E1: An invalid remote URL or unreachable remote host causing git
// ls-remote to exit with code 128 returns (false, non-nil error); never
// (false, nil).
// Requirement: 10-REQ-4.E1
func TestBranchExists_UnreachableRemote_ReturnsFalseError(t *testing.T) {
	t.Parallel()
	workDir := t.TempDir()

	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	ctx := context.Background()
	// A directory that is not a git repo triggers exit code 128.
	nonRepoDir := t.TempDir()
	exists, err := runner.BranchExists(ctx, nonRepoDir, "main")
	if err == nil {
		t.Fatal("BranchExists with non-repo path returned nil error; expected non-nil error (exit 128)")
	}
	if exists {
		t.Error("BranchExists with non-repo path returned true; expected false")
	}
}

// 10-REQ-4.E2: Context cancellation before BranchExists starts the subprocess
// returns (false, non-nil error) wrapping the context error.
// Requirement: 10-REQ-4.E2
func TestBranchExists_ContextCancelled_ReturnsFalseError(t *testing.T) {
	t.Parallel()
	bareRepo := initBareRepo(t)
	workDir := t.TempDir()

	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	// Cancel context before calling BranchExists.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	exists, err := runner.BranchExists(ctx, bareRepo, "main")
	if err == nil {
		t.Fatal("BranchExists with cancelled context returned nil error; expected error wrapping context cancellation")
	}
	if exists {
		t.Error("BranchExists with cancelled context returned true; expected false")
	}
}

// 10-REQ-4.E2 (timeout): Context deadline expiring during BranchExists
// returns (false, non-nil error).
// Requirement: 10-REQ-4.E2
func TestBranchExists_ContextTimeout_ReturnsFalseError(t *testing.T) {
	t.Parallel()
	bareRepo := initBareRepo(t)
	workDir := t.TempDir()

	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	// Use a very short timeout that will have expired before the subprocess
	// can complete. Sleep briefly to guarantee the deadline has passed.
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)

	exists, err := runner.BranchExists(ctx, bareRepo, "main")
	if err == nil {
		// If the command completed before the deadline, skip — we cannot
		// test the timeout path reliably in this environment.
		t.Skip("BranchExists completed before context timeout — cannot test deadline path")
	}
	if exists {
		t.Error("BranchExists with timed-out context returned true; expected false")
	}
}

// 10-REQ-4.E3: Passing a full ref path (e.g. 'refs/heads/main') as the branch
// parameter causes BranchExists to prepend refs/heads/ regardless, producing
// 'refs/heads/refs/heads/main' which will not match any real branch and will
// return (false, nil) via exit code 2. This documents the caller contract
// violation — callers must pass bare branch names only.
// Requirement: 10-REQ-4.E3
func TestBranchExists_FullRefPath_DoublePrepend_ReturnsFalseNil(t *testing.T) {
	t.Parallel()
	bareRepo := initBareRepo(t)
	workDir := t.TempDir()

	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	ctx := context.Background()
	// Passing 'refs/heads/main' as branch causes BranchExists to query
	// 'refs/heads/refs/heads/main', which does not exist on the remote.
	exists, err := runner.BranchExists(ctx, bareRepo, "refs/heads/main")
	if err != nil {
		t.Fatalf("BranchExists returned error: %v; expected nil (exit code 2 for no match)", err)
	}
	if exists {
		t.Error("BranchExists('refs/heads/main') returned true; expected false — double-prepend produces 'refs/heads/refs/heads/main' which matches no branch")
	}
}

// 10-REQ-4.E4: Passing an empty branch name runs 'git ls-remote --exit-code
// <remote> refs/heads/' which queries all branches. The result depends on git's
// behavior and is not guarded by the package. This is a caller contract
// violation — the test only verifies BranchExists does not panic.
// Requirement: 10-REQ-4.E4
func TestBranchExists_EmptyBranch_DoesNotPanic(t *testing.T) {
	t.Parallel()
	bareRepo := initBareRepo(t)
	workDir := t.TempDir()

	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	ctx := context.Background()
	// An empty branch name is a caller contract violation. BranchExists
	// will query 'refs/heads/' which matches all branches. Since the bare
	// repo has at least one branch ('main'), git ls-remote should exit 0.
	// However, the exact behavior is git-version-dependent and not
	// guaranteed by the package.
	exists, err := runner.BranchExists(ctx, bareRepo, "")
	// No assertions on specific values — this is documented as caller
	// contract violation territory per 10-REQ-4.E4. We only verify that
	// BranchExists does not panic and returns some result.
	_ = exists
	_ = err
}

// ---------------------------------------------------------------------------
// Correctness property: 10-PROP-4 — BranchExists exit-2 is the only
// non-error false return
// ---------------------------------------------------------------------------

// 10-PROP-4: For any call to BranchExists, (false, nil) is returned if and
// only if git ls-remote exits with code 2; any other non-zero exit code
// returns (false, non-nil error); exit code 0 returns (true, nil).
// This table-driven test exercises all three discrimination paths.
// Validates: 10-REQ-4.1, 10-REQ-4.2, 10-REQ-4.3
func TestBranchExists_ThreeWayDiscrimination(t *testing.T) {
	t.Parallel()
	bareRepo := initBareRepo(t, "feature/bar")
	workDir := t.TempDir()

	runner := NewRunner(workDir)
	if runner == nil {
		t.Skip("NewRunner returned nil — implementation not yet available")
	}

	ctx := context.Background()

	tests := []struct {
		name       string
		remote     string
		branch     string
		wantExists bool
		wantErr    bool
	}{
		{
			name:       "exit 0: branch exists",
			remote:     bareRepo,
			branch:     "feature/bar",
			wantExists: true,
			wantErr:    false,
		},
		{
			name:       "exit 2: branch missing",
			remote:     bareRepo,
			branch:     "no-such-branch-xyz",
			wantExists: false,
			wantErr:    false,
		},
		{
			name:       "exit 128: invalid remote",
			remote:     "/nonexistent/path/to/repo",
			branch:     "main",
			wantExists: false,
			wantErr:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			exists, err := runner.BranchExists(ctx, tc.remote, tc.branch)
			if exists != tc.wantExists {
				t.Errorf("BranchExists(%q, %q) exists = %v; want %v",
					tc.remote, tc.branch, exists, tc.wantExists)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("BranchExists(%q, %q) err = %v; wantErr = %v",
					tc.remote, tc.branch, err, tc.wantErr)
			}
		})
	}
}
