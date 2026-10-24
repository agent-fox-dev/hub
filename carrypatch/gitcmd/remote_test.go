package gitcmd

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ========================================================================
// Spec 14 Task 3.3: RemoteAdd method tests
// (TS-14-12, TS-14-13)
// Requirements: 14-REQ-6.1, 14-REQ-6.2
// ========================================================================

// TestRemoteAdd_Success verifies that RemoteAdd adds a named remote to the
// repository and returns nil. After the call, the remote should appear in
// git remote -v output.
//
// Preconditions:
// - A real git repository is initialised in a temp directory
// - No remote named 'upstream' exists
// - GitRunner is constructed with workDir pointing to the temp repo
//
// TS-14-12
// Requirement: 14-REQ-6.1
func TestRemoteAdd_Success(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	dir := initTestRepoWithCommit(t)

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	err = runner.RemoteAdd(ctx, "upstream", "https://example.com/repo.git")
	if err != nil {
		t.Fatalf("RemoteAdd(ctx, %q, %q) returned unexpected error: %v",
			"upstream", "https://example.com/repo.git", err)
	}

	// Verify the remote exists via git remote -v.
	remotes := runGit(t, dir, "remote", "-v")
	if !strings.Contains(remotes, "upstream") {
		t.Errorf("after RemoteAdd, 'upstream' not found in git remote -v output: %q", remotes)
	}
	if !strings.Contains(remotes, "https://example.com/repo.git") {
		t.Errorf("after RemoteAdd, URL not found in git remote -v output: %q", remotes)
	}
}

// TestRemoteAdd_AlreadyExists verifies that RemoteAdd returns a non-nil
// *GitError when a remote with the given name already exists.
//
// Preconditions:
// - A real git repository is initialised in a temp directory
// - Remote 'origin' already exists
// - GitRunner is constructed with workDir pointing to the temp repo
//
// TS-14-13
// Requirement: 14-REQ-6.2
func TestRemoteAdd_AlreadyExists(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	dir := initTestRepoWithCommit(t)

	// Add a remote named 'origin' first.
	runGit(t, dir, "remote", "add", "origin", "https://example.com/first.git")

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	err = runner.RemoteAdd(ctx, "origin", "https://example.com/repo.git")
	if err == nil {
		t.Fatal("RemoteAdd with existing remote name should return non-nil error")
	}

	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("error should be *GitError, got %T: %v", err, err)
	}
	if ge.ExitCode == 0 {
		t.Error("GitError.ExitCode should be non-zero")
	}
}

// TestRemoteAdd_EmptyName verifies that RemoteAdd returns a *GitError
// without invoking the git subprocess when the name is an empty string.
//
// Edge case: 14-REQ-6.E1
func TestRemoteAdd_EmptyName(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	dir := initTestRepoWithCommit(t)

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	err = runner.RemoteAdd(ctx, "", "https://example.com/repo.git")
	if err == nil {
		t.Fatal("RemoteAdd with empty name should return non-nil error")
	}

	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("error should be *GitError, got %T: %v", err, err)
	}
}

// TestRemoteAdd_EmptyURL verifies that RemoteAdd returns a *GitError
// without invoking the git subprocess when the URL is an empty string.
//
// Edge case: 14-REQ-6.E1
func TestRemoteAdd_EmptyURL(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	dir := initTestRepoWithCommit(t)

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	err = runner.RemoteAdd(ctx, "upstream", "")
	if err == nil {
		t.Fatal("RemoteAdd with empty URL should return non-nil error")
	}

	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("error should be *GitError, got %T: %v", err, err)
	}
}
