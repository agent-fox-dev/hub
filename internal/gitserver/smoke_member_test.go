package gitserver

import (
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startMemberGitServer serves newMemberGitEnv over HTTP and returns the env
// and a remote URL of myws for a credential.
func startMemberGitServer(t *testing.T) (*gitTestEnv, func(credential string) string) {
	t.Helper()
	env := newMemberGitEnv(t)
	srv := httptest.NewServer(env.echo)
	t.Cleanup(srv.Close)
	return env, func(credential string) string {
		u, err := url.Parse(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		u.User = url.UserPassword("x-token-auth", credential)
		u.Path = "/git/myorg/myws.git"
		return u.String()
	}
}

// TS-37-36 (smoke, 37-PATH-1): a member clones and fetches a colleague's
// workspace with the git CLI and sees the owner's refs. Real: Echo server,
// GitAuthMiddleware, gitResolverMiddleware, authorizeGitAccess,
// wsaccess.AuthorizeWorkspaceAt, the git CLI, SQLite, a repository on disk.
func TestSmoke_MemberClonesAndFetches_TS_37_36(t *testing.T) {
	env, remote := startMemberGitServer(t)
	trunk := filepath.Join(env.workspaceRoot, "myws", "trunk")
	branch := strings.TrimPrefix(gitOut(t, trunk, "symbolic-ref", "HEAD"), "refs/heads/")

	clone := filepath.Join(t.TempDir(), "myws")
	runGitCmd(t, "", "clone", remote("af_key_userB"), clone)
	if got, want := gitOut(t, clone, "rev-parse", "HEAD"), gitOut(t, trunk, "rev-parse", "HEAD"); got != want {
		t.Errorf("cloned HEAD %s; want %s", got, want)
	}

	addCommit(t, trunk, "later.txt", "a commit after the clone")
	runGitCmd(t, clone, "fetch", "origin")
	newHead := gitOut(t, trunk, "rev-parse", "HEAD")
	if got := gitOut(t, clone, "rev-parse", "origin/"+branch); got != newHead {
		t.Errorf("fetched origin/%s = %s; want the new commit %s", branch, got, newHead)
	}
	if member, owner := gitOut(t, "", "ls-remote", remote("af_key_userB")), gitOut(t, "", "ls-remote", remote("af_key_userA")); member != owner {
		t.Errorf("member ls-remote %q; want the owner's %q", member, owner)
	}
}

// TS-37-37 (smoke, 37-PATH-2): a member's git push is refused with the
// read-only message and changes nothing, nor writes an audit event. Real:
// as TS-37-36, plus the audit store (useAuditStore).
func TestSmoke_MemberPushRefused_TS_37_37(t *testing.T) {
	env, remote := startMemberGitServer(t)
	trunk := filepath.Join(env.workspaceRoot, "myws", "trunk")
	setHeadSHAInDB(t, env, "myws", gitOut(t, trunk, "rev-parse", "HEAD"))
	duck := useAuditStore(t)
	events := func() (all, pushes int) {
		if err := duck.QueryRow(`SELECT COUNT(*), COUNT(*) FILTER (WHERE event_type = 'hub.git.push') FROM hub_audit_events`).Scan(&all, &pushes); err != nil {
			t.Fatal(err)
		}
		return all, pushes
	}

	clone := filepath.Join(t.TempDir(), "myws")
	runGitCmd(t, "", "clone", remote("af_key_userB"), clone)
	addCommit(t, clone, "member.txt", "a member's commit")
	before := repoState(t, env, "myws")
	allBefore, _ := events()

	out, err := gitCmdOutput(clone, "push", "origin", "HEAD")
	if err == nil {
		t.Fatalf("git push succeeded: %s; want it refused", out)
	}
	if !strings.Contains(out, "org members have read-only access to this workspace") {
		t.Errorf("git push output %q; want the read-only message", out)
	}
	time.Sleep(200 * time.Millisecond)
	if after := repoState(t, env, "myws"); after != before {
		t.Errorf("repository changed:\nbefore %s\nafter  %s", before, after)
	}
	if all, pushes := events(); all != allBefore || pushes != 0 {
		t.Errorf("audit events %d -> %d, hub.git.push %d; want unchanged and none", allBefore, all, pushes)
	}
}
