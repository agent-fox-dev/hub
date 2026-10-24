package gitcmd

import (
	"context"
	"errors"
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
