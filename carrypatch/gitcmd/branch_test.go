package gitcmd

import (
	"context"
	"testing"
)

func TestCreateBranch_CreatesAtStartPoint(t *testing.T) {
	dir, _, featureSHA := divergedRepo(t, false)
	if err := newRunner(t, dir).CreateBranch(context.Background(), "topic", featureSHA); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if got := runGit(t, dir, "rev-parse", "refs/heads/topic"); got != featureSHA {
		t.Errorf("topic = %s, want %s", got, featureSHA)
	}
}

// An existing name or an empty argument is a GitError and the branches are
// unchanged.
func TestCreateBranch_RejectsExistingNameAndEmptyArguments(t *testing.T) {
	dir, mainSHA, featureSHA := divergedRepo(t, false)
	r := newRunner(t, dir)
	for _, c := range [][2]string{{"feature", mainSHA}, {"", mainSHA}, {"other", ""}} {
		asGitError(t, r.CreateBranch(context.Background(), c[0], c[1]))
	}
	if got := runGit(t, dir, "rev-parse", "feature"); got != featureSHA {
		t.Errorf("feature = %s after rejected CreateBranch, want %s", got, featureSHA)
	}
}
