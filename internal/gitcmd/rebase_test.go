package gitcmd

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// ========================================================================
// Spec 11 Task 2.3: Rebase success, conflict, and abort tests
// (TS-11-19, TS-11-20, TS-11-21, TS-11-22, TS-11-23)
// Requirements: 11-REQ-6.1, 11-REQ-6.2, 11-REQ-6.3, 11-REQ-7.1, 11-REQ-7.2
// ========================================================================

// TestRebase_Success verifies that Rebase returns the new HEAD SHA (a
// 40-character hex string) and nil error when the rebase completes
// without conflicts.
//
// TS-11-19
// Requirement: 11-REQ-6.1
func TestRebase_Success(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	repoDir := setupCleanRebaseRepo(t)
	runner, err := New(repoDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	sha, err := runner.Rebase(ctx, "main")
	if err != nil {
		t.Fatalf("Rebase returned unexpected error: %v", err)
	}
	if sha == "" {
		t.Fatal("Rebase returned empty SHA for successful rebase")
	}
	if len(sha) != 40 {
		t.Errorf("SHA length = %d, want 40: %q", len(sha), sha)
	}
	if !shaRegexp.MatchString(sha) {
		t.Errorf("SHA %q does not match /^[0-9a-f]{40}$/", sha)
	}

	// Verify the returned SHA matches the current HEAD via RevParse.
	revParseSHA, err := runner.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatalf("RevParse(HEAD) after rebase failed: %v", err)
	}
	if sha != revParseSHA {
		t.Errorf("Rebase SHA %q != RevParse HEAD %q", sha, revParseSHA)
	}
}

// TestRebase_ConflictWithAutoAbort verifies that Rebase returns a
// *RebaseConflictError with non-empty ConflictingFiles when the rebase
// encounters a conflict, AND that the repository is NOT left in rebase
// state (git rebase --abort is called internally).
//
// TS-11-20
// Requirement: 11-REQ-6.2
// Correctness property: 11-PROP-3
func TestRebase_ConflictWithAutoAbort(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	repoDir := setupConflictingRebaseRepo(t)
	runner, err := New(repoDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	out, err := runner.Rebase(ctx, "main")
	if out != "" {
		t.Errorf("Rebase should return empty string on conflict, got %q", out)
	}
	if err == nil {
		t.Fatal("Rebase should return non-nil error on conflict")
	}

	// Extract *RebaseConflictError using errors.As.
	var ce *RebaseConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("error should be *RebaseConflictError, got %T: %v", err, err)
	}
	if len(ce.ConflictingFiles) < 1 {
		t.Fatal("RebaseConflictError.ConflictingFiles should have at least 1 entry")
	}

	// 11-PROP-3: After Rebase returns *RebaseConflictError, the repository
	// must NOT be in rebase state. Neither .git/rebase-merge nor
	// .git/rebase-apply should exist.
	rebaseMerge := filepath.Join(repoDir, ".git", "rebase-merge")
	rebaseApply := filepath.Join(repoDir, ".git", "rebase-apply")
	if dirExists(rebaseMerge) {
		t.Errorf(".git/rebase-merge should not exist after Rebase conflict auto-abort")
	}
	if dirExists(rebaseApply) {
		t.Errorf(".git/rebase-apply should not exist after Rebase conflict auto-abort")
	}
}

// TestRebase_SafetyEnvVars verifies that Rebase applies all safety
// environment variables (GIT_ALLOW_PROTOCOL, GIT_TERMINAL_PROMPT,
// GIT_CONFIG_NOSYSTEM) to every git subprocess invoked during the rebase
// sequence.
//
// This is verified by construction: GitRunner.Run always appends safety
// variables, and Rebase uses Run internally. The integration test confirms
// that no subprocess hangs on an interactive prompt (which would indicate
// GIT_TERMINAL_PROMPT was not set).
//
// TS-11-21
// Requirement: 11-REQ-6.3
func TestRebase_SafetyEnvVars(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	repoDir := setupCleanRebaseRepo(t)
	runner, err := New(repoDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Verify safety env vars are present in the runner's environment.
	expected := []string{
		"GIT_ALLOW_PROTOCOL=file:https:ssh",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
	}
	for _, want := range expected {
		if !envSliceContains(runner.env, want) {
			t.Errorf("runner.env does not contain %q", want)
		}
	}

	// Run the rebase — if safety vars are not applied, the subprocess
	// might hang on an interactive prompt or read system config.
	ctx := context.Background()
	_, err = runner.Rebase(ctx, "main")
	if err != nil {
		t.Fatalf("Rebase with safety env vars should succeed, got: %v", err)
	}
}

// TestRebaseAbort_Success verifies that RebaseAbort runs "git rebase --abort"
// and returns nil when the repository is in rebase state.
//
// TS-11-22
// Requirement: 11-REQ-7.1
func TestRebaseAbort_Success(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	repoDir := setupConflictingRebaseRepo(t)

	// Manually put the repository in rebase state by starting a conflicting
	// rebase using raw git (NOT GitRunner.Rebase, which auto-aborts).
	_, _ = runGitMayFail(t, repoDir, "rebase", "main")

	// Verify we are in rebase state before calling RebaseAbort.
	rebaseMerge := filepath.Join(repoDir, ".git", "rebase-merge")
	rebaseApply := filepath.Join(repoDir, ".git", "rebase-apply")
	if !dirExists(rebaseMerge) && !dirExists(rebaseApply) {
		t.Skip("repository is not in rebase state after manual rebase; cannot test RebaseAbort")
	}

	runner, err := New(repoDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	err = runner.RebaseAbort(ctx)
	if err != nil {
		t.Fatalf("RebaseAbort returned unexpected error: %v", err)
	}

	// After abort, rebase state directories should no longer exist.
	if dirExists(rebaseMerge) {
		t.Error(".git/rebase-merge should not exist after RebaseAbort")
	}
	if dirExists(rebaseApply) {
		t.Error(".git/rebase-apply should not exist after RebaseAbort")
	}
}

// TestRebaseAbort_NoRebaseState verifies that RebaseAbort returns a *GitError
// with non-zero ExitCode and non-empty Stderr when the repository is NOT
// in rebase state.
//
// TS-11-23
// Requirement: 11-REQ-7.2
// Edge case: 11-REQ-7.E1
func TestRebaseAbort_NoRebaseState(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	// Use a clean repo that is NOT in rebase state.
	repoDir := initTestRepo(t)

	// Make at least one commit so the repo is fully initialized.
	writeTestFile(t, filepath.Join(repoDir, "file.txt"), "content")
	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "commit", "-m", "initial")

	runner, err := New(repoDir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	err = runner.RebaseAbort(ctx)
	if err == nil {
		t.Fatal("RebaseAbort should return non-nil error when not in rebase state")
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

// ========================================================================
// Spec 14 Task 4.5: RebaseContinue success test
// (TS-14-23)
// Requirements: 14-REQ-11.1
// ========================================================================

// TestRebaseContinue_Success verifies that RebaseContinue while a rebase
// is paused returns the new HEAD SHA and nil error, and that the rebase
// is no longer in progress afterwards.
//
// Preconditions:
// - A real git repository with a rebase paused at a conflict that has been
//   manually resolved (all conflicts staged with `git add`)
// - git user.email and user.name are configured in the repo
//
// TS-14-23
// Requirement: 14-REQ-11.1
func TestRebaseContinue_Success(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	// Set up a repo where rebase will conflict, then manually resolve.
	dir := setupConflictingRebaseRepo(t)

	// Start the rebase (it will conflict).
	_, _ = runGitMayFail(t, dir, "rebase", "main")

	// Verify we are in rebase state.
	rebaseMerge := filepath.Join(dir, ".git", "rebase-merge")
	rebaseApply := filepath.Join(dir, ".git", "rebase-apply")
	if !dirExists(rebaseMerge) && !dirExists(rebaseApply) {
		t.Skip("repository is not in rebase state after manual rebase; cannot test RebaseContinue")
	}

	// Resolve the conflict by accepting "main" version and staging.
	writeTestFile(t, filepath.Join(dir, "conflict.txt"), "resolved content")
	runGit(t, dir, "add", "conflict.txt")

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	sha, err := runner.RebaseContinue(ctx)
	if err != nil {
		t.Fatalf("RebaseContinue returned unexpected error: %v", err)
	}

	// sha must be a valid 40-char hex SHA.
	if len(sha) != 40 {
		t.Errorf("SHA length = %d, want 40: %q", len(sha), sha)
	}
	if !shaRegexp.MatchString(sha) {
		t.Errorf("SHA %q does not match /^[0-9a-f]{40}$/", sha)
	}

	// Rebase must no longer be in progress.
	if dirExists(rebaseMerge) {
		t.Error(".git/rebase-merge should not exist after RebaseContinue success")
	}
	if dirExists(rebaseApply) {
		t.Error(".git/rebase-apply should not exist after RebaseContinue success")
	}
}

// ========================================================================
// Spec 14 Task 4.5: RebaseContinue failure test (no rebase in progress)
// (TS-14-24)
// Requirements: 14-REQ-11.2
// Edge case: 14-REQ-11.E1
// ========================================================================

// TestRebaseContinue_NoRebaseInProgress verifies that RebaseContinue returns
// ("", *GitError) with non-zero ExitCode when no rebase is in progress.
//
// Preconditions:
// - A real git repository with no rebase in progress
//
// TS-14-24
// Requirement: 14-REQ-11.2
// Edge case: 14-REQ-11.E1
func TestRebaseContinue_NoRebaseInProgress(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	// Use a clean repo that is NOT in rebase state.
	dir := initTestRepoWithCommit(t)

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	sha, err := runner.RebaseContinue(ctx)

	if sha != "" {
		t.Errorf("RebaseContinue should return empty string when no rebase in progress, got %q", sha)
	}
	if err == nil {
		t.Fatal("RebaseContinue should return non-nil error when no rebase in progress")
	}

	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("error should be *GitError, got %T: %v", err, err)
	}
	if ge.ExitCode == 0 {
		t.Error("GitError.ExitCode should be non-zero")
	}
}
