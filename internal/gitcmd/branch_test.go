package gitcmd

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// ========================================================================
// Spec 14 Task 1.3–1.4: CreateBranch method tests
// (TS-14-3, TS-14-4)
// Requirements: 14-REQ-2.1, 14-REQ-2.2
// ========================================================================

// TestCreateBranch_Success verifies that CreateBranch creates a new local
// branch at the specified startPoint and returns nil. After the call, the
// new branch should exist in the repository.
//
// Preconditions:
// - A real git repository with at least one commit
// - Branch 'new-branch' does not exist
//
// TS-14-3
// Requirement: 14-REQ-2.1
func TestCreateBranch_Success(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	dir := initTestRepoWithCommit(t)

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	err = runner.CreateBranch(ctx, "new-branch", "HEAD")
	if err != nil {
		t.Fatalf("CreateBranch(ctx, %q, %q) returned unexpected error: %v",
			"new-branch", "HEAD", err)
	}

	// Verify the branch exists via git branch --list.
	branches := runGit(t, dir, "branch", "--list", "new-branch")
	if !strings.Contains(branches, "new-branch") {
		t.Errorf("after CreateBranch, 'new-branch' not found in branch list: %q", branches)
	}
}

// TestCreateBranch_AlreadyExists verifies that CreateBranch returns a
// non-nil *GitError with non-zero ExitCode when the branch already exists.
//
// Preconditions:
// - A real git repository with at least one commit
// - Branch 'existing-branch' already exists
//
// TS-14-4
// Requirement: 14-REQ-2.2
func TestCreateBranch_AlreadyExists(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	dir := initTestRepoWithCommit(t)

	// Create a branch that already exists.
	runGit(t, dir, "branch", "existing-branch")

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	err = runner.CreateBranch(ctx, "existing-branch", "HEAD")
	if err == nil {
		t.Fatal("CreateBranch for existing branch should return non-nil error")
	}

	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("error should be *GitError, got %T: %v", err, err)
	}
	if ge.ExitCode == 0 {
		t.Error("GitError.ExitCode should be non-zero")
	}
}

// ========================================================================
// Spec 14 Task 1.5–1.6: DeleteBranch method tests
// (TS-14-5, TS-14-6)
// Requirements: 14-REQ-3.1, 14-REQ-3.2
// ========================================================================

// TestDeleteBranch_Success verifies that DeleteBranch force-deletes an
// existing local branch and returns nil. After the call, the branch should
// no longer exist in the repository.
//
// Preconditions:
// - A real git repository with at least one commit
// - Branch 'to-delete' exists locally
// - Current HEAD is NOT on 'to-delete'
//
// TS-14-5
// Requirement: 14-REQ-3.1
func TestDeleteBranch_Success(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	// Create a repo with a commit on 'main'.
	dir := t.TempDir()
	runGit(t, "", "init", "-b", "main", dir)
	configGitUser(t, dir)
	writeTestFile(t, filepath.Join(dir, "file.txt"), "initial content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial commit")

	// Create a branch 'to-delete' (not fully merged, to exercise -D).
	runGit(t, dir, "checkout", "-b", "to-delete")
	writeTestFile(t, filepath.Join(dir, "extra.txt"), "extra content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "unmerged commit")

	// Switch back to 'main' so we can delete 'to-delete'.
	runGit(t, dir, "checkout", "main")

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	err = runner.DeleteBranch(ctx, "to-delete")
	if err != nil {
		t.Fatalf("DeleteBranch(ctx, %q) returned unexpected error: %v", "to-delete", err)
	}

	// Verify the branch no longer exists.
	branches := runGit(t, dir, "branch", "--list", "to-delete")
	if strings.TrimSpace(branches) != "" {
		t.Errorf("after DeleteBranch, 'to-delete' should not exist, got: %q", branches)
	}
}

// TestDeleteBranch_NonExistent verifies that DeleteBranch returns a non-nil
// *GitError with non-zero ExitCode when the branch does not exist.
//
// Preconditions:
// - A real git repository initialised in a temp directory
// - Branch 'ghost-branch' does not exist
//
// TS-14-6
// Requirement: 14-REQ-3.2
func TestDeleteBranch_NonExistent(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	dir := initTestRepoWithCommit(t)

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	err = runner.DeleteBranch(ctx, "ghost-branch")
	if err == nil {
		t.Fatal("DeleteBranch for non-existent branch should return non-nil error")
	}

	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("error should be *GitError, got %T: %v", err, err)
	}
	if ge.ExitCode == 0 {
		t.Error("GitError.ExitCode should be non-zero")
	}
}
