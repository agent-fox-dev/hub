package gitserver

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/txsvc/apikit"
)

// ===========================================================================
// TS-23-73 (smoke): A client push mixing a backup ref and a normal branch
// is partly rejected end to end
//
// Verifies: 23-PATH-3, 23-REQ-6.1, 23-REQ-6.3
//
// Real components: git server receive-pack, trunk repository, real git
// client, auth middleware, database
// ===========================================================================

func TestSmoke_TS23_73_MixedPushPartlyRejected(t *testing.T) {
	// Save and restore global hooks.
	origPre := preReceiveHook
	origPost := postPushHook
	t.Cleanup(func() {
		preReceiveHook = origPre
		postPushHook = origPost
	})

	// Register a pre-receive hook that accepts everything — the protected
	// namespace rule must fire before the hook is consulted.
	RegisterPreReceiveHook(func(_ context.Context, _ *sql.DB, _ string, _ *apikit.AuthInfo, _ RefUpdate) error {
		return nil
	})

	env, trunk, remote := newGitClientServer(t)
	_ = env

	// Get the initial commit SHA from the trunk.
	initialSHA := runGitCmd(t, trunk, "rev-parse", "HEAD")

	// Create an existing backup ref in the trunk.
	createBackupRef(t, trunk, "feature", initialSHA)

	// Clone the repo.
	clone := t.TempDir()
	runGitCmd(t, "", "clone", remote, clone)

	// Add a commit so we have a different SHA to push.
	addCommit(t, clone, "mixed-smoke.txt", "mixed smoke commit")
	newSHA := runGitCmd(t, clone, "rev-parse", "HEAD")

	// Push both a normal branch and a protected ref in one push.
	out, _ := gitCmdOutput(clone, "push", "origin",
		newSHA+":refs/heads/newbranch",
		newSHA+":refs/hub/replaced/feature")

	// The client should see the protected-namespace rejection message.
	if !strings.Contains(out, "refs/hub/replaced/ is maintained by the hub and cannot be pushed to") {
		t.Errorf("expected protected-namespace message in output; got:\n%s", out)
	}

	// The normal branch should be written.
	trunkNewBranch := refValue(t, trunk, "refs/heads/newbranch")
	if trunkNewBranch != newSHA {
		t.Errorf("refs/heads/newbranch: got %s, want %s", trunkNewBranch, newSHA)
	}

	// The backup ref should keep its old value.
	trunkBackup := refValue(t, trunk, "refs/hub/replaced/feature")
	if trunkBackup != initialSHA {
		t.Errorf("refs/hub/replaced/feature changed; got %s, want %s", trunkBackup, initialSHA)
	}

	// Verify the backup ref is listed by ls-remote.
	lsOut := runGitCmd(t, "", "ls-remote", remote)
	if !strings.Contains(lsOut, "refs/hub/replaced/feature") {
		t.Errorf("ls-remote should list refs/hub/replaced/feature; got:\n%s", lsOut)
	}

	// Verify a client can fetch the backup ref by name.
	fetchDir := t.TempDir()
	runGitCmd(t, "", "init", fetchDir)
	runGitCmd(t, fetchDir, "remote", "add", "hub", remote)
	runGitCmd(t, fetchDir, "fetch", "hub", "refs/hub/replaced/feature")
	fetchHead := runGitCmd(t, fetchDir, "rev-parse", "FETCH_HEAD")
	if fetchHead != initialSHA {
		t.Errorf("FETCH_HEAD = %s; want %s", fetchHead, initialSHA)
	}
}

// newGitClientServerForSmoke starts a real HTTP server and returns the
// trunk path and an authenticated remote URL for the workspace "smokews"
// in org "myorg". This is a convenience wrapper for the smoke test.
func newGitClientServerForSmoke(t *testing.T) (*gitTestEnv, string, string) {
	t.Helper()
	env := newGitTestEnv(t)
	env.seedOrg(t, "org-1", "My Org", "myorg")
	env.seedOrgMember(t, "org-1", "user-1")
	env.seedWorkspace(t, "smokews", "https://github.com/org/repo", "user-1", "org-1", "active")
	env.setCloneStatus(t, "smokews", "ready")
	trunk := env.initWorkspaceRepo(t, "smokews")

	srv := httptest.NewServer(env.echo)
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("x-token-auth", "af_key_user1")
	u.Path = "/git/myorg/smokews.git"
	return env, trunk, u.String()
}
