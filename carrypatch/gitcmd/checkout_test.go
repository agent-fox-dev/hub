package gitcmd

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// ========================================================================
// Spec 14 Task 1.1–1.2: Checkout method tests
// (TS-14-1, TS-14-2)
// Requirements: 14-REQ-1.1, 14-REQ-1.2
// ========================================================================

// TestCheckout_Success verifies that Checkout switches the working tree to
// an existing branch and returns nil. After the call, HEAD should point to
// the checked-out branch.
//
// Preconditions:
// - A real git repository with at least one commit on 'main'
// - A second branch 'other' exists
// - Working tree starts on 'other' (not 'main')
//
// TS-14-1
// Requirement: 14-REQ-1.1
func TestCheckout_Success(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	// Create a repo with an initial commit on 'main'.
	dir := t.TempDir()
	runGit(t, "", "init", "-b", "main", dir)
	configGitUser(t, dir)
	writeTestFile(t, filepath.Join(dir, "file.txt"), "initial content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial commit")

	// Create a second branch and switch to it so HEAD is NOT on 'main'.
	runGit(t, dir, "checkout", "-b", "other")

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	err = runner.Checkout(ctx, "main")
	if err != nil {
		t.Fatalf("Checkout(ctx, %q) returned unexpected error: %v", "main", err)
	}

	// Verify HEAD points to 'main'.
	headRef := runGit(t, dir, "symbolic-ref", "HEAD")
	if headRef != "refs/heads/main" {
		t.Errorf("after Checkout, HEAD = %q, want %q", headRef, "refs/heads/main")
	}
}

// TestCheckout_NonExistentRef verifies that Checkout returns a non-nil
// *GitError with a non-zero ExitCode and non-empty Stderr when the
// requested ref does not exist in the repository.
//
// TS-14-2
// Requirement: 14-REQ-1.2
func TestCheckout_NonExistentRef(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	// Create a repo with at least one commit.
	dir := t.TempDir()
	runGit(t, "", "init", "-b", "main", dir)
	configGitUser(t, dir)
	writeTestFile(t, filepath.Join(dir, "file.txt"), "initial content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial commit")

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	err = runner.Checkout(ctx, "nonexistent-branch-xyz")
	if err == nil {
		t.Fatal("Checkout(ctx, \"nonexistent-branch-xyz\") should return non-nil error")
	}

	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("error should be *GitError, got %T: %v", err, err)
	}
	if ge.ExitCode == 0 {
		t.Error("GitError.ExitCode should be non-zero")
	}
	if ge.Stderr == "" {
		t.Error("GitError.Stderr should be non-empty")
	}
}
