package gitcmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// shaHexRegexp matches a 40-character lowercase hex string (git SHA).
// Declared here to avoid collision with shaRegexp in mergetree_test.go.
var shaHexRegexp = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ========================================================================
// Spec 14 Task 2.1: CherryPick success test
// (TS-14-7)
// Requirements: 14-REQ-4.1
// ========================================================================

// TestCherryPick_Success verifies that CherryPick applies a commit cleanly
// and returns the new HEAD SHA and nil error.
//
// Preconditions:
// - A real git repository with two branches: 'main' (base) and 'feature'
//   (one commit ahead of main)
// - Current HEAD is on 'main'
// - GitRunner is constructed with workDir pointing to the temp repo
//
// TS-14-7
// Requirement: 14-REQ-4.1
func TestCherryPick_Success(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	// Create repo with initial commit on 'main'.
	dir := t.TempDir()
	runGit(t, "", "init", "-b", "main", dir)
	configGitUser(t, dir)
	writeTestFile(t, filepath.Join(dir, "base.txt"), "base content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "base commit")

	// Create feature branch with one extra commit.
	runGit(t, dir, "checkout", "-b", "feature")
	writeTestFile(t, filepath.Join(dir, "feature.txt"), "feature content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "feature commit")
	featureSHA := runGit(t, dir, "rev-parse", "HEAD")

	// Switch back to main.
	runGit(t, dir, "checkout", "main")
	originalHead := runGit(t, dir, "rev-parse", "HEAD")

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	newSHA, err := runner.CherryPick(ctx, featureSHA)
	if err != nil {
		t.Fatalf("CherryPick returned unexpected error: %v", err)
	}

	// newSHA must be a valid 40-char hex SHA.
	if len(newSHA) != 40 {
		t.Errorf("SHA length = %d, want 40: %q", len(newSHA), newSHA)
	}
	if !shaHexRegexp.MatchString(newSHA) {
		t.Errorf("SHA %q does not match /^[0-9a-f]{40}$/", newSHA)
	}

	// newSHA must differ from originalHead (a new commit was created).
	if newSHA == originalHead {
		t.Errorf("newSHA should differ from originalHead %q", originalHead)
	}

	// git rev-parse HEAD in workDir must match the returned SHA.
	headSHA := runGit(t, dir, "rev-parse", "HEAD")
	if headSHA != newSHA {
		t.Errorf("HEAD SHA %q != returned SHA %q", headSHA, newSHA)
	}
}

// ========================================================================
// Spec 14 Task 2.2: CherryPick conflict test
// (TS-14-8)
// Requirements: 14-REQ-4.2, 14-REQ-13.1
// ========================================================================

// TestCherryPick_Conflict verifies that CherryPick auto-aborts on conflict
// and returns a *CherryPickConflictError with non-empty ConflictingFiles.
// After the call, the repository must not be in cherry-pick state.
//
// Preconditions:
// - A real git repository with a conflicting commit on a side branch
// - Current HEAD is on 'main' with a conflicting change to the same file
//
// TS-14-8
// Requirements: 14-REQ-4.2, 14-REQ-13.1
// Correctness property: 14-PROP-1
func TestCherryPick_Conflict(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	// Create repo with a base commit containing conflict.txt.
	dir := t.TempDir()
	runGit(t, "", "init", "-b", "main", dir)
	configGitUser(t, dir)
	writeTestFile(t, filepath.Join(dir, "conflict.txt"), "base content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "base commit")

	// Feature branch: modify conflict.txt differently.
	runGit(t, dir, "checkout", "-b", "feature")
	writeTestFile(t, filepath.Join(dir, "conflict.txt"), "feature modification")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "feature change to conflict.txt")
	conflictingSHA := runGit(t, dir, "rev-parse", "HEAD")

	// Back to main: modify conflict.txt differently.
	runGit(t, dir, "checkout", "main")
	writeTestFile(t, filepath.Join(dir, "conflict.txt"), "main modification")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "main change to conflict.txt")

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	sha, err := runner.CherryPick(ctx, conflictingSHA)

	// Must return empty SHA.
	if sha != "" {
		t.Errorf("CherryPick should return empty string on conflict, got %q", sha)
	}

	// Must return non-nil error.
	if err == nil {
		t.Fatal("CherryPick should return non-nil error on conflict")
	}

	// Must be *CherryPickConflictError.
	var cpErr *CherryPickConflictError
	if !errors.As(err, &cpErr) {
		t.Fatalf("error should be *CherryPickConflictError, got %T: %v", err, err)
	}

	// ConflictingFiles must be non-empty.
	if len(cpErr.ConflictingFiles) == 0 {
		t.Fatal("CherryPickConflictError.ConflictingFiles should be non-empty")
	}

	// 14-PROP-1: Repository must not be in cherry-pick state (auto-abort ran).
	cherryPickHead := filepath.Join(dir, ".git", "CHERRY_PICK_HEAD")
	if _, statErr := os.Stat(cherryPickHead); statErr == nil {
		t.Error("CHERRY_PICK_HEAD should not exist after CherryPick conflict auto-abort")
	}
}

// ========================================================================
// Spec 14 Task 2.3: CherryPick context cancellation test
// (TS-14-9)
// Requirements: 14-REQ-4.3
// ========================================================================

// TestCherryPick_ContextCancelled verifies that CherryPick returns ctx.Err()
// (context.Canceled) without running git cherry-pick --abort when the context
// is already cancelled.
//
// Preconditions:
// - A real git repository with at least one commit
// - A context that is already cancelled is prepared
//
// TS-14-9
// Requirement: 14-REQ-4.3
func TestCherryPick_ContextCancelled(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	// Create a repo with a commit so we have a valid SHA to pass.
	dir := t.TempDir()
	runGit(t, "", "init", "-b", "main", dir)
	configGitUser(t, dir)
	writeTestFile(t, filepath.Join(dir, "file.txt"), "content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "initial commit")
	validSHA := runGit(t, dir, "rev-parse", "HEAD")

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Create an already-cancelled context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sha, err := runner.CherryPick(ctx, validSHA)

	// Must return empty SHA.
	if sha != "" {
		t.Errorf("CherryPick should return empty string with cancelled context, got %q", sha)
	}

	// Must return context.Canceled error.
	if err == nil {
		t.Fatal("CherryPick should return non-nil error with cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error should be context.Canceled, got %T: %v", err, err)
	}
}

// ========================================================================
// Spec 14 Task 2.5: CherryPick multi-file conflict and fallback tests
// (TS-14-29)
// Requirements: 14-REQ-13.2
// ========================================================================

// TestCherryPick_ConflictTwoFiles verifies that CherryPickConflictError
// contains exactly the conflicting file paths when a cherry-pick conflicts
// on multiple files.
//
// Preconditions:
// - A real git repository with a cherry-pick conflict involving two files
//
// TS-14-29
// Requirement: 14-REQ-13.2
func TestCherryPick_ConflictTwoFiles(t *testing.T) {
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
	conflictingSHA := runGit(t, dir, "rev-parse", "HEAD")

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
	_, cpErr := runner.CherryPick(ctx, conflictingSHA)
	if cpErr == nil {
		t.Fatal("CherryPick should return non-nil error on two-file conflict")
	}

	var ce *CherryPickConflictError
	if !errors.As(cpErr, &ce) {
		t.Fatalf("error should be *CherryPickConflictError, got %T: %v", cpErr, cpErr)
	}

	if len(ce.ConflictingFiles) != 2 {
		t.Fatalf("ConflictingFiles should have 2 entries, got %d: %v",
			len(ce.ConflictingFiles), ce.ConflictingFiles)
	}

	// Check that both file paths are present (order may vary).
	foundA, foundB := false, false
	for _, f := range ce.ConflictingFiles {
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

// TestCherryPick_EmptySHA verifies that CherryPick returns a *GitError
// without invoking the git subprocess when the sha argument is empty.
//
// Edge case: 14-REQ-4.E1
func TestCherryPick_EmptySHA(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	dir := initTestRepoWithCommit(t)

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	sha, err := runner.CherryPick(ctx, "")

	if sha != "" {
		t.Errorf("CherryPick should return empty string for empty SHA, got %q", sha)
	}
	if err == nil {
		t.Fatal("CherryPick should return non-nil error for empty SHA")
	}

	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("error should be *GitError, got %T: %v", err, err)
	}
}
