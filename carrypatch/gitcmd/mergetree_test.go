package gitcmd

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
)

// A clean dry-run merge yields the merged tree and leaves HEAD and the working
// tree alone.
func TestMergeTree_CleanMergeReturnsTree(t *testing.T) {
	dir, mainSHA, featureSHA := divergedRepo(t, false)
	tree, err := newRunner(t, dir).MergeTree(context.Background(), mainSHA, featureSHA)
	if err != nil {
		t.Fatalf("MergeTree: %v", err)
	}
	if typ := runGit(t, dir, "cat-file", "-t", tree); typ != "tree" {
		t.Errorf("MergeTree returned %q of type %q, want a tree", tree, typ)
	}
	if head := runGit(t, dir, "rev-parse", "HEAD"); head != mainSHA {
		t.Errorf("HEAD moved to %s", head)
	}
}

func TestMergeTree_ConflictListsFiles(t *testing.T) {
	dir, mainSHA, featureSHA := divergedRepo(t, true)
	out, err := newRunner(t, dir).MergeTree(context.Background(), mainSHA, featureSHA)
	var ce *MergeConflictError
	if out != "" || !errors.As(err, &ce) {
		t.Fatalf("MergeTree = %q, %v; want \"\", *MergeConflictError", out, err)
	}
	if got := slices.Sorted(slices.Values(ce.ConflictingFiles)); !reflect.DeepEqual(got, []string{"base.txt", "second.txt"}) {
		t.Errorf("ConflictingFiles = %v, want base.txt and second.txt", got)
	}
}

// An unknown or option-like revision is a plain GitError, not a conflict.
func TestMergeTree_BadRevisionIsGitError(t *testing.T) {
	r := newRunner(t, initRepo(t))
	for _, base := range []string{"no-such-rev", "--help"} {
		_, err := r.MergeTree(context.Background(), base, "main")
		ge := asGitError(t, err)
		if ge.Args[0] != "merge-tree" || ge.Args[1] != "--write-tree" {
			t.Errorf("Args = %v, want merge-tree --write-tree ...", ge.Args)
		}
	}
}
