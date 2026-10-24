package gitcmd

import (
	"context"
	"testing"
)

// Refs resolve to full SHAs; unknown, empty and option-like refs are errors
// instead of being echoed back.
func TestRevParse_ResolvesRefsAndRejectsBadOnes(t *testing.T) {
	dir, mainSHA, featureSHA := divergedRepo(t, false)
	r := newRunner(t, dir)
	for ref, want := range map[string]string{"HEAD": mainSHA, "main": mainSHA, "refs/heads/feature": featureSHA} {
		if got, err := r.RevParse(context.Background(), ref); err != nil || got != want {
			t.Errorf("RevParse(%q) = %q, %v; want %s", ref, got, err, want)
		}
	}
	for _, ref := range []string{"no-such-ref", "", "--detach"} {
		_, err := r.RevParse(context.Background(), ref)
		asGitError(t, err)
	}
}

func TestUpdateRef_PointsRefAtCommit(t *testing.T) {
	dir, mainSHA, featureSHA := divergedRepo(t, false)
	r := newRunner(t, dir)
	if err := r.UpdateRef(context.Background(), "refs/heads/pinned", featureSHA); err != nil {
		t.Fatalf("UpdateRef: %v", err)
	}
	if got := runGit(t, dir, "rev-parse", "refs/heads/pinned"); got != featureSHA {
		t.Errorf("refs/heads/pinned = %s, want %s", got, featureSHA)
	}
	// A bad SHA or an empty ref is a GitError and the ref is left alone.
	for _, c := range [][2]string{{"refs/heads/main", "not-a-sha"}, {"", mainSHA}} {
		asGitError(t, r.UpdateRef(context.Background(), c[0], c[1]))
	}
	if got := runGit(t, dir, "rev-parse", "main"); got != mainSHA {
		t.Errorf("main = %s after rejected updates, want %s", got, mainSHA)
	}
}
