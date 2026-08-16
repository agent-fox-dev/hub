package gitcmd

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// ========================================================================
// Spec 14 Task 3.5: Diff method tests
// (TS-14-16, TS-14-17)
// Requirements: 14-REQ-8.1, 14-REQ-8.2
// ========================================================================

// TestDiff_Success verifies that Diff runs git diff with the given args and
// returns raw stdout and nil error. The output should contain diff content
// reflecting the changes between the two commits.
//
// Preconditions:
// - A real git repository is initialised in a temp directory with at least
//   two commits
// - GitRunner is constructed with workDir pointing to the temp repo
//
// TS-14-16
// Requirement: 14-REQ-8.1
func TestDiff_Success(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	// Create a repo with two commits that change different content.
	dir := initTestRepoWithCommit(t)
	writeTestFile(t, filepath.Join(dir, "changed.txt"), "new content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "second commit with changed file")

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	out, err := runner.Diff(ctx, "HEAD~1", "HEAD")
	if err != nil {
		t.Fatalf("Diff(ctx, %q, %q) returned unexpected error: %v",
			"HEAD~1", "HEAD", err)
	}

	if len(out) == 0 {
		t.Fatal("Diff output should be non-empty when commits differ")
	}

	// The diff should reference the changed file.
	if !strings.Contains(out, "changed.txt") {
		t.Errorf("Diff output should reference 'changed.txt', got: %q", out)
	}
}

// TestDiff_InvalidFlag verifies that Diff returns ("", *GitError) when git
// diff exits non-zero (e.g. due to an invalid flag).
//
// Preconditions:
// - A real git repository is initialised in a temp directory
// - GitRunner is constructed with workDir pointing to the temp repo
//
// TS-14-17
// Requirement: 14-REQ-8.2
func TestDiff_InvalidFlag(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	dir := initTestRepoWithCommit(t)

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	out, err := runner.Diff(ctx, "--invalid-flag-xyz")
	if out != "" {
		t.Errorf("Diff with invalid flag should return empty string, got: %q", out)
	}
	if err == nil {
		t.Fatal("Diff with invalid flag should return non-nil error")
	}

	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("error should be *GitError, got %T: %v", err, err)
	}
	if ge.ExitCode == 0 {
		t.Error("GitError.ExitCode should be non-zero")
	}
}

// TestDiff_NoArgs verifies that Diff with no arguments runs `git diff` and
// returns raw stdout without error.
//
// Edge case: 14-REQ-8.E1
func TestDiff_NoArgs(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	dir := initTestRepoWithCommit(t)

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	out, err := runner.Diff(ctx)
	if err != nil {
		t.Fatalf("Diff(ctx) with no args returned unexpected error: %v", err)
	}

	// With a clean working tree and no args, git diff should produce empty output.
	if out != "" {
		t.Errorf("Diff with no args on clean working tree should return empty string, got: %q", out)
	}
}
