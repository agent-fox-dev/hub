package gitcmd

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// ========================================================================
// Spec 14 Task 4.6: IsAncestor returns (true, nil)
// (TS-14-25)
// Requirements: 14-REQ-12.1
// ========================================================================

// TestIsAncestor_True verifies that IsAncestor returns (true, nil) when
// commitA is an ancestor of commitB in a linear history.
//
// Preconditions:
// - A real git repository with a linear history: commit A then commit B
//
// TS-14-25
// Requirement: 14-REQ-12.1
func TestIsAncestor_True(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	// Create a repo with two commits in linear history.
	dir := t.TempDir()
	runGit(t, "", "init", "-b", "main", dir)
	configGitUser(t, dir)

	writeTestFile(t, filepath.Join(dir, "file.txt"), "content A")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "commit A")
	commitA := runGit(t, dir, "rev-parse", "HEAD")

	writeTestFile(t, filepath.Join(dir, "file.txt"), "content B")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "commit B")
	commitB := runGit(t, dir, "rev-parse", "HEAD")

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	result, err := runner.IsAncestor(ctx, commitA, commitB)
	if err != nil {
		t.Fatalf("IsAncestor returned unexpected error: %v", err)
	}
	if !result {
		t.Errorf("IsAncestor(%q, %q) = false, want true", commitA, commitB)
	}
}

// ========================================================================
// Spec 14 Task 4.6: IsAncestor returns (false, nil)
// (TS-14-26)
// Requirements: 14-REQ-12.2
// ========================================================================

// TestIsAncestor_False verifies that IsAncestor returns (false, nil) when
// commitA is NOT an ancestor of commitB (diverged branches).
//
// Preconditions:
// - A real git repository with two diverged branches; commitA is on branch-a,
//   commitB is on branch-b, neither is an ancestor of the other
//
// TS-14-26
// Requirement: 14-REQ-12.2
func TestIsAncestor_False(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	// Create a repo with two diverged branches.
	dir := t.TempDir()
	runGit(t, "", "init", "-b", "main", dir)
	configGitUser(t, dir)

	// Base commit.
	writeTestFile(t, filepath.Join(dir, "base.txt"), "base")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "base")

	// Branch-a: diverge from base.
	runGit(t, dir, "checkout", "-b", "branch-a")
	writeTestFile(t, filepath.Join(dir, "a.txt"), "a content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "commit on branch-a")
	commitA := runGit(t, dir, "rev-parse", "HEAD")

	// Branch-b: diverge from base (not from branch-a).
	runGit(t, dir, "checkout", "main")
	runGit(t, dir, "checkout", "-b", "branch-b")
	writeTestFile(t, filepath.Join(dir, "b.txt"), "b content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "commit on branch-b")
	commitB := runGit(t, dir, "rev-parse", "HEAD")

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	result, err := runner.IsAncestor(ctx, commitA, commitB)
	if err != nil {
		t.Fatalf("IsAncestor returned unexpected error: %v", err)
	}
	if result {
		t.Errorf("IsAncestor(%q, %q) = true, want false (diverged branches)", commitA, commitB)
	}
}

// ========================================================================
// Spec 14 Task 4.6: IsAncestor returns (false, *GitError) for invalid SHAs
// (TS-14-27)
// Requirements: 14-REQ-12.3
// Correctness property: 14-PROP-3
// ========================================================================

// TestIsAncestor_InvalidSHAs verifies that IsAncestor returns
// (false, *GitError) when git merge-base --is-ancestor exits with an
// exit code other than 0 or 1 (e.g. when SHAs don't exist).
//
// Preconditions:
// - A real git repository
// - Non-existent commit SHAs are provided
//
// TS-14-27
// Requirement: 14-REQ-12.3
// Correctness property: 14-PROP-3
func TestIsAncestor_InvalidSHAs(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	dir := initTestRepoWithCommit(t)

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	result, err := runner.IsAncestor(ctx,
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		"cafebabecafebabecafebabecafebabecafebabe")

	if result {
		t.Error("IsAncestor should return false for invalid SHAs")
	}
	if err == nil {
		t.Fatal("IsAncestor should return non-nil error for invalid SHAs")
	}

	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("error should be *GitError, got %T: %v", err, err)
	}
	// Exit code should not be 0 (ancestor) or 1 (not ancestor) for invalid SHAs.
	if ge.ExitCode == 0 || ge.ExitCode == 1 {
		t.Errorf("GitError.ExitCode should not be 0 or 1, got %d", ge.ExitCode)
	}
}

// ========================================================================
// Spec 14 Task 4.6 (edge case): IsAncestor with empty commit strings
// (14-REQ-12.E1)
// ========================================================================

// TestIsAncestor_EmptyCommitA verifies that IsAncestor returns
// (false, *GitError) without invoking the git subprocess when commitA
// is an empty string.
//
// Edge case: 14-REQ-12.E1
func TestIsAncestor_EmptyCommitA(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	dir := initTestRepoWithCommit(t)
	commitB := runGit(t, dir, "rev-parse", "HEAD")

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	result, err := runner.IsAncestor(ctx, "", commitB)

	if result {
		t.Error("IsAncestor should return false for empty commitA")
	}
	if err == nil {
		t.Fatal("IsAncestor should return non-nil error for empty commitA")
	}

	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("error should be *GitError, got %T: %v", err, err)
	}
}

// TestIsAncestor_EmptyCommitB verifies that IsAncestor returns
// (false, *GitError) without invoking the git subprocess when commitB
// is an empty string.
//
// Edge case: 14-REQ-12.E1
func TestIsAncestor_EmptyCommitB(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	dir := initTestRepoWithCommit(t)
	commitA := runGit(t, dir, "rev-parse", "HEAD")

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	result, err := runner.IsAncestor(ctx, commitA, "")

	if result {
		t.Error("IsAncestor should return false for empty commitB")
	}
	if err == nil {
		t.Fatal("IsAncestor should return non-nil error for empty commitB")
	}

	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("error should be *GitError, got %T: %v", err, err)
	}
}

// TestIsAncestor_ContextCancelled verifies that IsAncestor returns the
// context error when the context is already cancelled.
//
// Edge case: 14-REQ-12.E3
func TestIsAncestor_ContextCancelled(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	dir := initTestRepoWithCommit(t)
	sha := runGit(t, dir, "rev-parse", "HEAD")

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Create an already-cancelled context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := runner.IsAncestor(ctx, sha, sha)

	if result {
		t.Error("IsAncestor should return false with cancelled context")
	}
	if err == nil {
		t.Fatal("IsAncestor should return non-nil error with cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error should be context.Canceled, got %T: %v", err, err)
	}
}
