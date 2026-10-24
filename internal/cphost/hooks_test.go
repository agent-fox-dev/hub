package cphost

import (
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// In origin mode the module's pre-receive hook, adapted to the git server, refuses
// a push to a registered patch branch; an unregistered branch pushed with it lands.
func TestPreReceive_RejectsRegisteredBranchInOriginMode(t *testing.T) {
	h := newHub(t)
	clone := h.clone(t, variables{"PATCH_BRANCH_SOURCE": "origin"})
	branchWithCommit(t, clone, "p1")
	runGit(t, clone, "checkout", "-q", "main")
	other := branchWithCommit(t, clone, "other")

	out, err := tryGit(clone, "push", "origin", "p1", "other")
	if err == nil || !strings.Contains(out, "branch is synced from origin") {
		t.Fatalf("push = %v; want p1 rejected with the module's message:\n%s", err, out)
	}
	if _, err := tryGit(h.trunk, "rev-parse", "--verify", "refs/heads/p1"); err == nil {
		t.Error("rejected p1 was written to the hub")
	}
	if got := runGit(t, h.trunk, "rev-parse", "refs/heads/other"); got != other {
		t.Errorf("other = %s on the hub, want %s", got, other)
	}
}

// In hub mode with PUSH_PATCHES_TO_ORIGIN the module's post-push hook, adapted to
// the git server, runs after the push: it enqueues a rebuild and mirrors the branch.
func TestPostPush_EnqueuesRebuildAndMirrorsToOrigin(t *testing.T) {
	h := newHub(t)
	clone := h.clone(t, variables{"PUSH_PATCHES_TO_ORIGIN": "true"})
	p1 := branchWithCommit(t, clone, "p1")

	if out, err := tryGit(clone, "push", "origin", "p1"); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if got := runGit(t, h.trunk, "rev-parse", "refs/heads/p1"); got != p1 {
		t.Errorf("hub p1 = %s, want %s", got, p1)
	}
	// The hook runs after the response; the mirror follows the enqueue.
	waitFor(t, "p1 on the fork", func() bool {
		got, err := tryGit(h.origin, "rev-parse", "refs/heads/p1")
		return err == nil && got == p1
	})
	if n := testutil.Count(t, h.db, `SELECT COUNT(*) FROM jobs WHERE type = 'rebuild' AND submitted_by = 'system:push-hook'`); n != 1 {
		t.Errorf("%d rebuild jobs enqueued by the push hook, want 1", n)
	}
}
