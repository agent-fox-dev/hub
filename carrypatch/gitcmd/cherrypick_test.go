package gitcmd

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestCherryPick_AppliesCommitAndReturnsHead(t *testing.T) {
	dir, mainSHA, featureSHA := divergedRepo(t, false)
	sha, err := newRunner(t, dir).CherryPick(context.Background(), featureSHA)
	if err != nil {
		t.Fatalf("CherryPick: %v", err)
	}
	if sha != runGit(t, dir, "rev-parse", "HEAD") || sha == featureSHA {
		t.Errorf("CherryPick returned %s; HEAD is %s, source %s", sha, runGit(t, dir, "rev-parse", "HEAD"), featureSHA)
	}
	if parent := runGit(t, dir, "rev-parse", "HEAD~1"); parent != mainSHA {
		t.Errorf("new commit sits on %s, want %s", parent, mainSHA)
	}
	if !exists(filepath.Join(dir, "feature.txt")) {
		t.Error("feature.txt was not applied")
	}
}

// A conflicting cherry-pick is aborted before returning: the error names the
// files and the repository is clean at the old HEAD.
func TestCherryPick_ConflictAbortsAndListsFiles(t *testing.T) {
	dir, mainSHA, featureSHA := divergedRepo(t, true)
	sha, err := newRunner(t, dir).CherryPick(context.Background(), featureSHA)
	var ce *CherryPickConflictError
	if sha != "" || !errors.As(err, &ce) {
		t.Fatalf("CherryPick = %q, %v; want \"\", *CherryPickConflictError", sha, err)
	}
	if got := slices.Sorted(slices.Values(ce.ConflictingFiles)); !reflect.DeepEqual(got, []string{"base.txt", "second.txt"}) {
		t.Errorf("ConflictingFiles = %v, want base.txt and second.txt", got)
	}
	if exists(filepath.Join(dir, ".git", "CHERRY_PICK_HEAD")) {
		t.Error("CHERRY_PICK_HEAD still exists")
	}
	if runGit(t, dir, "rev-parse", "HEAD") != mainSHA || runGit(t, dir, "status", "--porcelain") != "" {
		t.Error("repository not restored to a clean main after the conflict")
	}
}

// A cancelled context is returned as is; no abort runs on the caller's behalf.
func TestCherryPick_CancelledContextIsReturned(t *testing.T) {
	dir, _, featureSHA := divergedRepo(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sha, err := newRunner(t, dir).CherryPick(ctx, featureSHA)
	if sha != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("CherryPick = %q, %v; want \"\", context.Canceled", sha, err)
	}
}

// An empty or unknown SHA is a GitError, not a conflict.
func TestCherryPick_BadSHAIsGitError(t *testing.T) {
	r := newRunner(t, initRepo(t))
	for _, sha := range []string{"", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"} {
		_, err := r.CherryPick(context.Background(), sha)
		asGitError(t, err)
	}
}
