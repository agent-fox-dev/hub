package gitcmd

import (
	"context"
	"errors"
	"testing"
)

// ========================================================================
// Spec 11 Task 3.1: RevParse and UpdateRef round-trip tests
// (TS-11-24, TS-11-25, TS-11-26, TS-11-27)
// Requirements: 11-REQ-8, 11-REQ-9
// ========================================================================

// TestRevParse_Success verifies that RevParse returns the trimmed SHA string
// (40 hex characters) and nil error when git resolves the ref successfully.
//
// TS-11-24
// Requirement: 11-REQ-8.1
func TestRevParse_Success(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	repoDir := initTestRepoWithCommit(t)
	runner, err := New(repoDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	sha, err := runner.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatalf("RevParse(HEAD) returned error: %v", err)
	}
	if len(sha) != 40 {
		t.Errorf("SHA length = %d, want 40: %q", len(sha), sha)
	}
	if !shaRegexp.MatchString(sha) {
		t.Errorf("SHA %q does not match /^[0-9a-f]{40}$/", sha)
	}
}

// TestRevParse_UnknownRef verifies that RevParse returns ("", *GitError) with
// a non-zero ExitCode when the ref cannot be resolved by git.
//
// TS-11-25
// Requirement: 11-REQ-8.2
func TestRevParse_UnknownRef(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	repoDir := initTestRepoWithCommit(t)
	runner, err := New(repoDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	out, err := runner.RevParse(ctx, "refs/heads/nonexistent-branch-xyz")
	if out != "" {
		t.Errorf("RevParse should return empty string for unknown ref, got %q", out)
	}
	if err == nil {
		t.Fatal("RevParse should return non-nil error for unknown ref")
	}

	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("error should be *GitError, got %T: %v", err, err)
	}
	if ge.ExitCode == 0 {
		t.Error("GitError.ExitCode should be non-zero")
	}
}

// TestRevParse_EmptyRef verifies that RevParse passes an empty ref string to
// git as-is, which returns a non-zero exit code as a *GitError.
//
// Edge case: 11-REQ-8.E1
func TestRevParse_EmptyRef(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	repoDir := initTestRepoWithCommit(t)
	runner, err := New(repoDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	out, err := runner.RevParse(ctx, "")
	if out != "" {
		t.Errorf("RevParse('') should return empty string, got %q", out)
	}
	if err == nil {
		t.Fatal("RevParse('') should return non-nil error")
	}

	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("error should be *GitError, got %T: %v", err, err)
	}
}

// TestUpdateRef_Success verifies that UpdateRef runs 'git update-ref <ref> <sha>'
// and returns nil when git updates the reference successfully. A subsequent
// RevParse call confirms the ref points to the expected SHA.
//
// TS-11-26
// Requirement: 11-REQ-9.1
func TestUpdateRef_Success(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	repoDir := initTestRepoWithCommit(t)

	// Get the commit SHA using raw git (independent of RevParse).
	commitSHA := runGit(t, repoDir, "rev-parse", "HEAD")

	runner, err := New(repoDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	err = runner.UpdateRef(ctx, "refs/heads/test-branch", commitSHA)
	if err != nil {
		t.Fatalf("UpdateRef returned error: %v", err)
	}

	// Verify the ref was updated using RevParse.
	resolved, err := runner.RevParse(ctx, "refs/heads/test-branch")
	if err != nil {
		t.Fatalf("RevParse after UpdateRef failed: %v", err)
	}
	if resolved != commitSHA {
		t.Errorf("RevParse after UpdateRef = %q, want %q", resolved, commitSHA)
	}
}

// TestUpdateRef_InvalidSHA verifies that UpdateRef returns *GitError with
// ExitCode != 0 and non-empty Stderr when given an invalid SHA.
//
// TS-11-27
// Requirement: 11-REQ-9.2
func TestUpdateRef_InvalidSHA(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	repoDir := initTestRepoWithCommit(t)
	runner, err := New(repoDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	err = runner.UpdateRef(ctx, "refs/heads/test-branch", "not-a-valid-sha")
	if err == nil {
		t.Fatal("UpdateRef should return error for invalid SHA")
	}

	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("error should be *GitError, got %T: %v", err, err)
	}
	if ge.ExitCode == 0 {
		t.Error("GitError.ExitCode should be non-zero")
	}
}

// TestUpdateRef_EmptyRefOrSHA verifies that UpdateRef passes empty ref/sha
// strings to git, which returns a non-zero exit code as a *GitError.
//
// Edge case: 11-REQ-9.E1
func TestUpdateRef_EmptyRefOrSHA(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	repoDir := initTestRepoWithCommit(t)
	runner, err := New(repoDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()

	// Empty ref
	err = runner.UpdateRef(ctx, "", "abc123")
	if err == nil {
		t.Error("UpdateRef with empty ref should return error")
	}

	// Empty sha
	err = runner.UpdateRef(ctx, "refs/heads/test-branch", "")
	if err == nil {
		t.Error("UpdateRef with empty sha should return error")
	}
}

// TestRevParseUpdateRef_RoundTrip performs a round-trip test: create a commit,
// capture its SHA via RevParse, update a custom ref via UpdateRef, then
// re-resolve via RevParse and assert equality.
//
// Requirements: 11-REQ-8.1, 11-REQ-9.1
func TestRevParseUpdateRef_RoundTrip(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	repoDir := initTestRepoWithCommit(t)
	runner, err := New(repoDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()

	// Step 1: Capture HEAD SHA via RevParse.
	headSHA, err := runner.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatalf("RevParse(HEAD) failed: %v", err)
	}
	if len(headSHA) != 40 {
		t.Fatalf("HEAD SHA length = %d, want 40: %q", len(headSHA), headSHA)
	}

	// Step 2: Create a new ref pointing to the same commit.
	customRef := "refs/heads/roundtrip-test"
	err = runner.UpdateRef(ctx, customRef, headSHA)
	if err != nil {
		t.Fatalf("UpdateRef(%s, %s) failed: %v", customRef, headSHA, err)
	}

	// Step 3: Resolve the custom ref via RevParse.
	resolved, err := runner.RevParse(ctx, customRef)
	if err != nil {
		t.Fatalf("RevParse(%s) failed: %v", customRef, err)
	}

	// Step 4: Assert equality.
	if resolved != headSHA {
		t.Errorf("round-trip SHA mismatch: RevParse(%s) = %q, expected %q", customRef, resolved, headSHA)
	}
}
