package gitcmd

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// TestCherry_AllApplied verifies that Cherry correctly identifies commits whose
// content has been applied upstream (squash-merge scenario).
func TestCherry_AllApplied(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	dir := t.TempDir()
	runGit(t, "", "init", "-b", "main", dir)
	configGitUser(t, dir)

	// Base commit.
	writeTestFile(t, filepath.Join(dir, "base.txt"), "base")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "base")

	// Create a feature branch with one commit.
	runGit(t, dir, "checkout", "-b", "feature")
	writeTestFile(t, filepath.Join(dir, "feature.txt"), "feature content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "add feature")

	// Go back to main and simulate a squash merge: apply the same diff as a
	// new commit (different SHA, same patch-id).
	runGit(t, dir, "checkout", "main")
	writeTestFile(t, filepath.Join(dir, "feature.txt"), "feature content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "squash: add feature (#1)")

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	applied, pending, err := runner.Cherry(ctx, "main", "feature")
	if err != nil {
		t.Fatalf("Cherry returned error: %v", err)
	}

	if len(pending) != 0 {
		t.Errorf("expected no pending commits, got %v", pending)
	}
	if len(applied) != 1 {
		t.Errorf("expected 1 applied commit, got %d: %v", len(applied), applied)
	}
}

// TestCherry_NoneApplied verifies that Cherry correctly identifies commits
// that have NOT been applied upstream.
func TestCherry_NoneApplied(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	dir := t.TempDir()
	runGit(t, "", "init", "-b", "main", dir)
	configGitUser(t, dir)

	writeTestFile(t, filepath.Join(dir, "base.txt"), "base")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "base")

	runGit(t, dir, "checkout", "-b", "feature")
	writeTestFile(t, filepath.Join(dir, "feature.txt"), "feature content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "add feature")

	// main has no equivalent commit.
	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	applied, pending, err := runner.Cherry(ctx, "main", "feature")
	if err != nil {
		t.Fatalf("Cherry returned error: %v", err)
	}

	if len(applied) != 0 {
		t.Errorf("expected no applied commits, got %v", applied)
	}
	if len(pending) != 1 {
		t.Errorf("expected 1 pending commit, got %d: %v", len(pending), pending)
	}
}

// TestCherry_PartialApplied verifies that Cherry correctly handles a case
// where some commits are applied and others are not.
func TestCherry_PartialApplied(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	dir := t.TempDir()
	runGit(t, "", "init", "-b", "main", dir)
	configGitUser(t, dir)

	writeTestFile(t, filepath.Join(dir, "base.txt"), "base")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "base")

	// Feature branch with 2 commits.
	runGit(t, dir, "checkout", "-b", "feature")
	writeTestFile(t, filepath.Join(dir, "a.txt"), "a content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "add a")

	writeTestFile(t, filepath.Join(dir, "b.txt"), "b content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "add b")

	// Squash only the first commit into main.
	runGit(t, dir, "checkout", "main")
	writeTestFile(t, filepath.Join(dir, "a.txt"), "a content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "squash: add a")

	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()
	applied, pending, err := runner.Cherry(ctx, "main", "feature")
	if err != nil {
		t.Fatalf("Cherry returned error: %v", err)
	}

	if len(applied) != 1 {
		t.Errorf("expected 1 applied commit, got %d: %v", len(applied), applied)
	}
	if len(pending) != 1 {
		t.Errorf("expected 1 pending commit, got %d: %v", len(pending), pending)
	}
}

// TestCherry_EmptyInputs verifies that Cherry returns a *GitError for empty
// upstream or head parameters.
func TestCherry_EmptyInputs(t *testing.T) {
	requireGitMinVersion(t, 2, 38)

	dir := initTestRepoWithCommit(t)
	runner, err := New(dir, nil)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	ctx := context.Background()

	_, _, err = runner.Cherry(ctx, "", "HEAD")
	if err == nil {
		t.Fatal("Cherry with empty upstream should return error")
	}
	var ge *GitError
	if !errors.As(err, &ge) {
		t.Fatalf("error should be *GitError, got %T: %v", err, err)
	}

	_, _, err = runner.Cherry(ctx, "HEAD", "")
	if err == nil {
		t.Fatal("Cherry with empty head should return error")
	}
	if !errors.As(err, &ge) {
		t.Fatalf("error should be *GitError, got %T: %v", err, err)
	}
}
