package gitcmd

import (
	"context"
	"errors"
	"testing"
)

// ========================================================================
// Spec 14 Task 3.1–3.2: ConfigSet method tests
// (TS-14-10, TS-14-11)
// Requirements: 14-REQ-5.1, 14-REQ-5.2
// ========================================================================

// TestConfigSet_Success verifies that ConfigSet sets a repository-local git
// config value and returns nil. After the call, the config key should read
// back the expected value.
//
// Preconditions:
// - A real git repository is initialised in a temp directory
// - GitRunner is constructed with workDir pointing to the temp repo
//
// TS-14-10
// Requirement: 14-REQ-5.1
func TestConfigSet_Success(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	dir := initTestRepoWithCommit(t)

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	err = runner.ConfigSet(ctx, "rerere.enabled", "true")
	if err != nil {
		t.Fatalf("ConfigSet(ctx, %q, %q) returned unexpected error: %v",
			"rerere.enabled", "true", err)
	}

	// Verify the config value was set by reading it back via git config.
	val := runGit(t, dir, "config", "rerere.enabled")
	if val != "true" {
		t.Errorf("after ConfigSet, git config rerere.enabled = %q, want %q", val, "true")
	}
}

// TestConfigSet_InvalidKey verifies that ConfigSet returns a non-nil *GitError
// when git config exits non-zero (e.g. invalid key format).
//
// Preconditions:
// - A real git repository is initialised in a temp directory
// - GitRunner is constructed with workDir pointing to the temp repo
//
// TS-14-11
// Requirement: 14-REQ-5.2
func TestConfigSet_InvalidKey(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	dir := initTestRepoWithCommit(t)

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	err = runner.ConfigSet(ctx, "--invalid-key", "val")
	if err == nil {
		t.Fatal("ConfigSet with invalid key should return non-nil error")
	}

	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("error should be *GitError, got %T: %v", err, err)
	}
	if ge.ExitCode == 0 {
		t.Error("GitError.ExitCode should be non-zero")
	}
}

// TestConfigSet_EmptyKey verifies that ConfigSet returns a *GitError
// without invoking the git subprocess when the key is an empty string.
//
// Edge case: 14-REQ-5.E1
func TestConfigSet_EmptyKey(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	dir := initTestRepoWithCommit(t)

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	err = runner.ConfigSet(ctx, "", "value")
	if err == nil {
		t.Fatal("ConfigSet with empty key should return non-nil error")
	}

	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("error should be *GitError, got %T: %v", err, err)
	}
}

// TestConfigSet_EmptyValue verifies that ConfigSet returns a *GitError
// without invoking the git subprocess when the value is an empty string.
//
// Edge case: 14-REQ-5.E1
func TestConfigSet_EmptyValue(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	dir := initTestRepoWithCommit(t)

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	err = runner.ConfigSet(ctx, "rerere.enabled", "")
	if err == nil {
		t.Fatal("ConfigSet with empty value should return non-nil error")
	}

	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("error should be *GitError, got %T: %v", err, err)
	}
}
