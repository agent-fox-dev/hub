package gitcmd

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func noRebaseState(t *testing.T, dir string) {
	t.Helper()
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if exists(filepath.Join(dir, ".git", d)) {
			t.Errorf(".git/%s still exists", d)
		}
	}
}

func TestRebase_ReplaysOntoTargetAndReturnsHead(t *testing.T) {
	dir, mainSHA, featureSHA := divergedRepo(t, false)
	runGit(t, dir, "checkout", "-q", "feature")
	sha, err := newRunner(t, dir).Rebase(context.Background(), "main")
	if err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if sha != runGit(t, dir, "rev-parse", "HEAD") || sha == featureSHA {
		t.Errorf("Rebase returned %s; HEAD is %s, old tip %s", sha, runGit(t, dir, "rev-parse", "HEAD"), featureSHA)
	}
	if parent := runGit(t, dir, "rev-parse", "HEAD~1"); parent != mainSHA {
		t.Errorf("rebased commit sits on %s, want main %s", parent, mainSHA)
	}
}

// A conflicting rebase is aborted before returning, so the branch is back
// where it was and the error names the files.
func TestRebase_ConflictAbortsAndListsFiles(t *testing.T) {
	dir, _, featureSHA := divergedRepo(t, true)
	runGit(t, dir, "checkout", "-q", "feature")
	sha, err := newRunner(t, dir).Rebase(context.Background(), "main")
	var ce *RebaseConflictError
	if sha != "" || !errors.As(err, &ce) {
		t.Fatalf("Rebase = %q, %v; want \"\", *RebaseConflictError", sha, err)
	}
	if got := slices.Sorted(slices.Values(ce.ConflictingFiles)); !reflect.DeepEqual(got, []string{"base.txt", "second.txt"}) {
		t.Errorf("ConflictingFiles = %v, want base.txt and second.txt", got)
	}
	noRebaseState(t, dir)
	if head := runGit(t, dir, "rev-parse", "HEAD"); head != featureSHA {
		t.Errorf("HEAD = %s after abort, want %s", head, featureSHA)
	}
}

// An unknown or option-like target is a plain GitError and nothing happens.
func TestRebase_BadTargetIsGitError(t *testing.T) {
	dir, _, featureSHA := divergedRepo(t, false)
	runGit(t, dir, "checkout", "-q", "feature")
	r := newRunner(t, dir)
	for _, onto := range []string{"no-such-branch", "--exec=touch pwned"} {
		_, err := r.Rebase(context.Background(), onto)
		asGitError(t, err)
	}
	if runGit(t, dir, "rev-parse", "HEAD") != featureSHA || exists(filepath.Join(dir, "pwned")) {
		t.Error("a rejected rebase changed the repository")
	}
}

func TestRebaseAbort_LeavesInterruptedRebase(t *testing.T) {
	dir, _, featureSHA := divergedRepo(t, true)
	runGit(t, dir, "checkout", "-q", "feature")
	if _, err := tryGit(dir, "rebase", "main"); err == nil {
		t.Fatal("rebase did not conflict")
	}
	if err := newRunner(t, dir).RebaseAbort(context.Background()); err != nil {
		t.Fatalf("RebaseAbort: %v", err)
	}
	noRebaseState(t, dir)
	if head := runGit(t, dir, "rev-parse", "HEAD"); head != featureSHA {
		t.Errorf("HEAD = %s after abort, want %s", head, featureSHA)
	}
}

func TestRebaseAbort_WithoutRebaseIsGitError(t *testing.T) {
	err := newRunner(t, initRepo(t)).RebaseAbort(context.Background())
	if ge := asGitError(t, err); ge.Stderr == "" {
		t.Error("GitError.Stderr is empty")
	}
}
