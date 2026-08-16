package gitcmd

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"testing"
)

// ========================================================================
// Spec 14 Task 3.4: Log method tests
// (TS-14-14, TS-14-15)
// Requirements: 14-REQ-7.1, 14-REQ-7.2
// ========================================================================

// TestLog_Success verifies that Log runs git log with the given args and
// returns the raw stdout output and nil error. The output should contain
// commit SHAs when using a format that emits them.
//
// Preconditions:
// - A real git repository is initialised in a temp directory with at least
//   two commits
// - GitRunner is constructed with workDir pointing to the temp repo
//
// TS-14-14
// Requirement: 14-REQ-7.1
func TestLog_Success(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	// Create a repo with two commits so log output is non-trivial.
	dir := initTestRepoWithCommit(t)
	writeTestFile(t, filepath.Join(dir, "second.txt"), "second content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "second commit")

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	out, err := runner.Log(ctx, "--oneline", "--format=%H")
	if err != nil {
		t.Fatalf("Log(ctx, %q, %q) returned unexpected error: %v",
			"--oneline", "--format=%H", err)
	}

	if len(out) == 0 {
		t.Fatal("Log output should be non-empty")
	}

	// Verify output contains a 40-character hex string (a full SHA).
	sha40 := regexp.MustCompile(`[0-9a-f]{40}`)
	if !sha40.MatchString(out) {
		t.Errorf("Log output should contain a 40-char hex SHA, got: %q", out)
	}
}

// TestLog_InvalidFlag verifies that Log returns ("", *GitError) when git
// log exits non-zero (e.g. due to an invalid flag).
//
// Preconditions:
// - A real git repository is initialised in a temp directory
// - GitRunner is constructed with workDir pointing to the temp repo
//
// TS-14-15
// Requirement: 14-REQ-7.2
func TestLog_InvalidFlag(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	dir := initTestRepoWithCommit(t)

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	out, err := runner.Log(ctx, "--invalid-flag-xyz")
	if out != "" {
		t.Errorf("Log with invalid flag should return empty string, got: %q", out)
	}
	if err == nil {
		t.Fatal("Log with invalid flag should return non-nil error")
	}

	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("error should be *GitError, got %T: %v", err, err)
	}
	if ge.ExitCode == 0 {
		t.Error("GitError.ExitCode should be non-zero")
	}
}

// TestLog_NoArgs verifies that Log with no arguments runs `git log` and
// returns raw stdout without error.
//
// Edge case: 14-REQ-7.E1
func TestLog_NoArgs(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	dir := initTestRepoWithCommit(t)

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	out, err := runner.Log(ctx)
	if err != nil {
		t.Fatalf("Log(ctx) with no args returned unexpected error: %v", err)
	}

	// A repository with at least one commit should produce non-empty log output.
	if len(out) == 0 {
		t.Error("Log with no args should return non-empty output for a repo with commits")
	}
}
