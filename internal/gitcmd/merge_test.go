package gitcmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// ========================================================================
// Spec 14 Task 4.1: MergeNoFF success test
// (TS-14-18)
// Requirements: 14-REQ-9.1
// ========================================================================

// TestMergeNoFF_Success verifies that MergeNoFF with a cleanly mergeable
// branch returns the merge commit SHA and nil error.
//
// Preconditions:
// - A real git repository with 'main' and 'feature' branches that merge cleanly
// - Current HEAD is on 'main'
// - GitRunner is constructed with workDir pointing to the temp repo
// - git user.email and user.name are configured in the repo
//
// TS-14-18
// Requirement: 14-REQ-9.1
func TestMergeNoFF_Success(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	// Set up a repo with diverged but non-conflicting 'main' and 'feature' branches.
	dir, _, _ := setupMergeableRepo(t)

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	sha, err := runner.MergeNoFF(ctx, "feature")
	if err != nil {
		t.Fatalf("MergeNoFF returned unexpected error: %v", err)
	}

	// sha must be a valid 40-char hex SHA.
	if len(sha) != 40 {
		t.Errorf("SHA length = %d, want 40: %q", len(sha), sha)
	}
	if !shaHexRegexp.MatchString(sha) {
		t.Errorf("SHA %q does not match /^[0-9a-f]{40}$/", sha)
	}

	// The returned SHA must match git rev-parse HEAD.
	headSHA := runGit(t, dir, "rev-parse", "HEAD")
	if headSHA != sha {
		t.Errorf("HEAD SHA %q != returned SHA %q", headSHA, sha)
	}

	// git log --oneline -1 should show a merge commit (contains "Merge branch").
	logOut := runGit(t, dir, "log", "--oneline", "-1")
	if len(logOut) == 0 {
		t.Fatal("git log --oneline -1 returned empty output")
	}
	// Verify it's actually a merge commit by checking for two parents.
	parentCount := runGit(t, dir, "rev-list", "--parents", "-1", "HEAD")
	// A merge commit has 3 fields: <commit> <parent1> <parent2>
	// A regular commit has 2 fields: <commit> <parent>
	fields := len(splitFields(parentCount))
	if fields < 3 {
		t.Errorf("expected merge commit with >= 2 parents (3 fields), got %d fields: %q",
			fields, parentCount)
	}
}

// splitFields splits a string by whitespace (helper for parent counting).
func splitFields(s string) []string {
	var fields []string
	for _, f := range splitByWhitespace(s) {
		if f != "" {
			fields = append(fields, f)
		}
	}
	return fields
}

// splitByWhitespace splits a string by any whitespace character.
func splitByWhitespace(s string) []string {
	result := make([]string, 0)
	current := ""
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if current != "" {
				result = append(result, current)
				current = ""
			}
		} else {
			current += string(r)
		}
	}
	if current != "" {
		result = append(result, current)
	}
	return result
}

// ========================================================================
// Spec 14 Task 4.2: MergeNoFF conflict test
// (TS-14-19)
// Requirements: 14-REQ-9.2, 14-REQ-13.2
// Correctness property: 14-PROP-2
// ========================================================================

// TestMergeNoFF_Conflict verifies that MergeNoFF auto-aborts on conflict
// and returns a *MergeNoFFConflictError with non-empty ConflictingFiles.
// After the call, the repository must be in a clean state (no merge in
// progress).
//
// Preconditions:
// - A real git repository with 'main' and 'feature' branches that have
//   conflicting changes to the same file
// - Current HEAD is on 'main'
//
// TS-14-19
// Requirements: 14-REQ-9.2, 14-REQ-13.2
// Correctness property: 14-PROP-2
func TestMergeNoFF_Conflict(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	// Set up a repo with conflicting 'main' and 'feature' branches.
	dir, _, _ := setupConflictingRepo(t)

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	sha, err := runner.MergeNoFF(ctx, "feature")

	// Must return empty SHA.
	if sha != "" {
		t.Errorf("MergeNoFF should return empty string on conflict, got %q", sha)
	}

	// Must return non-nil error.
	if err == nil {
		t.Fatal("MergeNoFF should return non-nil error on conflict")
	}

	// Must be *MergeNoFFConflictError.
	var mergeErr *MergeNoFFConflictError
	if !errors.As(err, &mergeErr) {
		t.Fatalf("error should be *MergeNoFFConflictError, got %T: %v", err, err)
	}

	// ConflictingFiles must be non-empty.
	if len(mergeErr.ConflictingFiles) == 0 {
		t.Fatal("MergeNoFFConflictError.ConflictingFiles should be non-empty")
	}

	// 14-PROP-2: Repository must not be in merge state (auto-abort ran).
	mergeHead := filepath.Join(dir, ".git", "MERGE_HEAD")
	if _, statErr := os.Stat(mergeHead); statErr == nil {
		t.Error("MERGE_HEAD should not exist after MergeNoFF conflict auto-abort")
	}
}

// ========================================================================
// Spec 14 Task 4.2 (additional): MergeNoFF conflict with multiple files
// (TS-14-29 — also validates via MergeNoFF path)
// Requirements: 14-REQ-13.2
// Correctness property: 14-PROP-6
// ========================================================================

// TestMergeNoFF_ConflictMultipleFiles verifies that MergeNoFFConflictError
// contains exactly the conflicting file paths when a merge --no-ff conflicts
// on multiple files.
//
// TS-14-29 (validated via MergeNoFF in addition to CherryPick)
// Requirement: 14-REQ-13.2
// Correctness property: 14-PROP-6
func TestMergeNoFF_ConflictMultipleFiles(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	// Create repo with base commit containing two files.
	dir := t.TempDir()
	runGit(t, "", "init", "-b", "main", dir)
	configGitUser(t, dir)
	writeTestFile(t, filepath.Join(dir, "file_a.txt"), "base a")
	writeTestFile(t, filepath.Join(dir, "file_b.txt"), "base b")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "base commit")

	// Feature branch: modify both files differently.
	runGit(t, dir, "checkout", "-b", "feature")
	writeTestFile(t, filepath.Join(dir, "file_a.txt"), "feature a")
	writeTestFile(t, filepath.Join(dir, "file_b.txt"), "feature b")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "feature changes both files")

	// Back to main: modify both files with different content.
	runGit(t, dir, "checkout", "main")
	writeTestFile(t, filepath.Join(dir, "file_a.txt"), "main a")
	writeTestFile(t, filepath.Join(dir, "file_b.txt"), "main b")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "main changes both files")

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	_, mergeErr := runner.MergeNoFF(ctx, "feature")
	if mergeErr == nil {
		t.Fatal("MergeNoFF should return non-nil error on two-file conflict")
	}

	var me *MergeNoFFConflictError
	if !errors.As(mergeErr, &me) {
		t.Fatalf("error should be *MergeNoFFConflictError, got %T: %v", mergeErr, mergeErr)
	}

	if len(me.ConflictingFiles) != 2 {
		t.Fatalf("ConflictingFiles should have 2 entries, got %d: %v",
			len(me.ConflictingFiles), me.ConflictingFiles)
	}

	// Check that both file paths are present (order may vary).
	foundA, foundB := false, false
	for _, f := range me.ConflictingFiles {
		switch f {
		case "file_a.txt":
			foundA = true
		case "file_b.txt":
			foundB = true
		}
	}
	if !foundA {
		t.Error("ConflictingFiles should contain 'file_a.txt'")
	}
	if !foundB {
		t.Error("ConflictingFiles should contain 'file_b.txt'")
	}
}

// ========================================================================
// Spec 14 Task 4.3: MergeNoFF context cancellation test
// (TS-14-20)
// Requirements: 14-REQ-9.3
// ========================================================================

// TestMergeNoFF_ContextCancelled verifies that MergeNoFF returns ctx.Err()
// (context.Canceled) without running git merge --abort when the context is
// already cancelled.
//
// Preconditions:
// - A real git repository initialised in a temp directory
// - GitRunner is constructed with workDir pointing to the temp repo
// - A context that is already cancelled is prepared
//
// TS-14-20
// Requirement: 14-REQ-9.3
func TestMergeNoFF_ContextCancelled(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	// Create a repo with at least one commit and a feature branch.
	dir, _, _ := setupMergeableRepo(t)

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Create an already-cancelled context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sha, err := runner.MergeNoFF(ctx, "feature")

	// Must return empty SHA.
	if sha != "" {
		t.Errorf("MergeNoFF should return empty string with cancelled context, got %q", sha)
	}

	// Must return context.Canceled error.
	if err == nil {
		t.Fatal("MergeNoFF should return non-nil error with cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error should be context.Canceled, got %T: %v", err, err)
	}
}

// ========================================================================
// Spec 14 Task 4.3 (edge case): MergeNoFF with empty branch name
// (14-REQ-9.E1)
// ========================================================================

// TestMergeNoFF_EmptyBranch verifies that MergeNoFF returns a *GitError
// without invoking the git subprocess when the branch argument is empty.
//
// Edge case: 14-REQ-9.E1
func TestMergeNoFF_EmptyBranch(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	dir := initTestRepoWithCommit(t)

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	sha, err := runner.MergeNoFF(ctx, "")

	if sha != "" {
		t.Errorf("MergeNoFF should return empty string for empty branch, got %q", sha)
	}
	if err == nil {
		t.Fatal("MergeNoFF should return non-nil error for empty branch")
	}

	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("error should be *GitError, got %T: %v", err, err)
	}
}

// ========================================================================
// Spec 14 Task 4.4: MergeAbort success test
// (TS-14-21)
// Requirements: 14-REQ-10.1
// ========================================================================

// TestMergeAbort_Success verifies that MergeAbort runs git merge --abort
// while a merge is in progress and returns nil. After the call, MERGE_HEAD
// must no longer exist.
//
// Preconditions:
// - A real git repository with a merge conflict in progress (MERGE_HEAD exists)
//
// TS-14-21
// Requirement: 14-REQ-10.1
func TestMergeAbort_Success(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	// Set up a conflicting repo.
	dir, _, _ := setupConflictingRepo(t)

	// Manually start a conflicting merge to put the repo in merge state.
	// Use raw git (NOT GitRunner.MergeNoFF, which auto-aborts).
	_, _ = runGitMayFail(t, dir, "merge", "--no-ff", "feature")

	// Verify MERGE_HEAD exists before calling MergeAbort.
	mergeHead := filepath.Join(dir, ".git", "MERGE_HEAD")
	if _, statErr := os.Stat(mergeHead); statErr != nil {
		t.Skip("MERGE_HEAD does not exist after manual merge; cannot test MergeAbort")
	}

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	err = runner.MergeAbort(ctx)
	if err != nil {
		t.Fatalf("MergeAbort returned unexpected error: %v", err)
	}

	// MERGE_HEAD must no longer exist after abort.
	if _, statErr := os.Stat(mergeHead); statErr == nil {
		t.Error("MERGE_HEAD should not exist after MergeAbort")
	}
}

// ========================================================================
// Spec 14 Task 4.4: MergeAbort failure test (no merge in progress)
// (TS-14-22)
// Requirements: 14-REQ-10.2
// Edge case: 14-REQ-10.E1
// ========================================================================

// TestMergeAbort_NoMergeInProgress verifies that MergeAbort returns a
// *GitError with non-zero ExitCode when no merge is in progress.
//
// Preconditions:
// - A real git repository with no merge in progress
//
// TS-14-22
// Requirement: 14-REQ-10.2
// Edge case: 14-REQ-10.E1
func TestMergeAbort_NoMergeInProgress(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	// Use a clean repo that is NOT in merge state.
	dir := initTestRepoWithCommit(t)

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	err = runner.MergeAbort(ctx)
	if err == nil {
		t.Fatal("MergeAbort should return non-nil error when no merge is in progress")
	}

	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("error should be *GitError, got %T: %v", err, err)
	}
	if ge.ExitCode == 0 {
		t.Error("GitError.ExitCode should be non-zero")
	}
}
