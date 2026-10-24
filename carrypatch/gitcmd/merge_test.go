package gitcmd

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
)

// MergeNoFF always records a merge commit with both parents, with the given
// message or git's default.
func TestMergeNoFF_CreatesMergeCommit(t *testing.T) {
	tests := []struct{ message, wantSubject string }{
		{"merge feature", "merge feature"},
		{"", "Merge branch 'feature'"},
	}
	for _, tt := range tests {
		dir, mainSHA, featureSHA := divergedRepo(t, false)
		sha, err := newRunner(t, dir).MergeNoFF(context.Background(), "feature", tt.message)
		if err != nil {
			t.Fatalf("MergeNoFF(%q): %v", tt.message, err)
		}
		if got := runGit(t, dir, "rev-list", "--parents", "-n", "1", "HEAD"); got != sha+" "+mainSHA+" "+featureSHA {
			t.Errorf("HEAD with parents = %q, want %s %s %s", got, sha, mainSHA, featureSHA)
		}
		if got := runGit(t, dir, "log", "-1", "--format=%s"); got != tt.wantSubject {
			t.Errorf("merge subject = %q, want %q", got, tt.wantSubject)
		}
	}
}

// A conflicting merge is aborted before returning: the error names the files
// and the repository is clean at the old HEAD.
func TestMergeNoFF_ConflictAbortsAndListsFiles(t *testing.T) {
	dir, mainSHA, _ := divergedRepo(t, true)
	sha, err := newRunner(t, dir).MergeNoFF(context.Background(), "feature", "")
	var ce *MergeNoFFConflictError
	if sha != "" || !errors.As(err, &ce) {
		t.Fatalf("MergeNoFF = %q, %v; want \"\", *MergeNoFFConflictError", sha, err)
	}
	if got := slices.Sorted(slices.Values(ce.ConflictingFiles)); !reflect.DeepEqual(got, []string{"base.txt", "second.txt"}) {
		t.Errorf("ConflictingFiles = %v, want base.txt and second.txt", got)
	}
	if _, err := tryGit(dir, "rev-parse", "--verify", "MERGE_HEAD"); err == nil {
		t.Error("MERGE_HEAD still exists")
	}
	if runGit(t, dir, "rev-parse", "HEAD") != mainSHA || runGit(t, dir, "status", "--porcelain") != "" {
		t.Error("repository not restored to a clean main after the conflict")
	}
}

// A cancelled context is returned as is; no abort runs on the caller's behalf.
func TestMergeNoFF_CancelledContextIsReturned(t *testing.T) {
	dir, _, _ := divergedRepo(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sha, err := newRunner(t, dir).MergeNoFF(ctx, "feature", "")
	if sha != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("MergeNoFF = %q, %v; want \"\", context.Canceled", sha, err)
	}
}

// An empty or unknown branch is a GitError, not a conflict.
func TestMergeNoFF_BadBranchIsGitError(t *testing.T) {
	r := newRunner(t, initRepo(t))
	for _, branch := range []string{"", "no-such-branch"} {
		_, err := r.MergeNoFF(context.Background(), branch, "")
		asGitError(t, err)
	}
}
