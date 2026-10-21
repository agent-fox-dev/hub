package gitserver

import (
	"context"
	"database/sql"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// Spec 37 — members cannot push.

// git runs git in dir and returns its trimmed output.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(runGitCmd(t, dir, args...))
}

// receivePackBody builds a receive-pack request that pushes a new commit on
// top of the head of workspace slug's repository to its current branch:
// the ref update command followed by the pack.
func receivePackBody(t *testing.T, env *gitTestEnv, slug string) string {
	t.Helper()
	trunk := filepath.Join(env.workspaceRoot, slug, "trunk")
	clone := t.TempDir()
	runGitCmd(t, "", "clone", trunk, clone)
	addCommit(t, clone, "member.txt", "member commit")
	oldSHA, newSHA := gitOut(t, trunk, "rev-parse", "HEAD"), gitOut(t, clone, "rev-parse", "HEAD")
	ref := gitOut(t, trunk, "symbolic-ref", "HEAD")
	cmd := exec.Command("git", "-C", clone, "pack-objects", "--stdout", "--revs")
	cmd.Stdin = strings.NewReader(newSHA + "\n^" + oldSHA + "\n")
	pack, err := cmd.Output()
	if err != nil {
		t.Fatalf("pack-objects: %v", err)
	}
	return string(encodePktLine(oldSHA+" "+newSHA+" "+ref+"\x00report-status\n")) + "0000" + string(pack)
}

// repoState is what a push could change in workspace slug: its refs, its
// objects, the recorded head_sha and the worktree status.
func repoState(t *testing.T, env *gitTestEnv, slug string) string {
	t.Helper()
	trunk := filepath.Join(env.workspaceRoot, slug, "trunk")
	var headSHA sql.NullString
	if err := env.db.QueryRow(`SELECT head_sha FROM workspaces WHERE slug = ?`, slug).Scan(&headSHA); err != nil {
		t.Fatal(err)
	}
	return strings.Join([]string{
		gitOut(t, trunk, "for-each-ref"),
		gitOut(t, trunk, "rev-list", "--all", "--objects"),
		gitOut(t, trunk, "status", "--porcelain"),
		headSHA.String,
	}, "\n--\n")
}

// countHooks installs counting pre-receive and post-push hooks for the rest
// of the test.
func countHooks(t *testing.T) (pre, post *atomic.Int32) {
	t.Helper()
	origPre, origPost := preReceiveHook, postPushHook
	t.Cleanup(func() { preReceiveHook, postPushHook = origPre, origPost })
	pre, post = &atomic.Int32{}, &atomic.Int32{}
	RegisterPreReceiveHook(func(context.Context, *sql.DB, string, *apikit.AuthInfo, RefUpdate) error {
		pre.Add(1)
		return nil
	})
	RegisterPostPushHook(func(*sql.DB, string, []string) { post.Add(1) })
	return pre, post
}

// TS-37-13: a member's push discovery and receive-pack are refused with the
// read-only 403 whatever the credential's scopes. Requirement: 37-REQ-4.1
func TestMemberPush_Refused_TS_37_13(t *testing.T) {
	env := newMemberGitEnv(t)
	seedGitPAT(t, env.db, "memberNoScope", "user-b")
	for _, cred := range []string{"af_key_userB", "af_pat_userB_write", "af_pat_userB_read", "af_pat_memberNoScope"} {
		for _, r := range []struct{ method, path string }{
			{http.MethodGet, "/git/myorg/myws.git/info/refs?service=git-receive-pack"},
			{http.MethodPost, "/git/myorg/myws.git/git-receive-pack"},
		} {
			rec := env.doRequest(t, r.method, r.path, "", withBasicAuth("x", cred))
			if rec.Code != http.StatusForbidden || rec.Header().Get("Content-Type") != "text/plain" || rec.Body.String() != readOnlyBody ||
				strings.Contains(rec.Body.String(), "insufficient git permissions") {
				t.Errorf("%s %s as %s: (%d, %q, %q); want text/plain 403 %q", r.method, r.path, cred, rec.Code, rec.Header().Get("Content-Type"), rec.Body, readOnlyBody)
			}
		}
	}
}

// TS-37-14: a member's refused receive-pack with a valid pack changes no
// ref, object, head_sha or worktree, calls no hook and emits no audit
// event; the owner's identical push succeeds. Requirement: 37-REQ-4.2
func TestMemberPush_NoSideEffects_TS_37_14(t *testing.T) {
	env := newMemberGitEnv(t)
	trunk := filepath.Join(env.workspaceRoot, "myws", "trunk")
	setHeadSHAInDB(t, env, "myws", gitOut(t, trunk, "rev-parse", "HEAD"))
	pre, post := countHooks(t)
	emitter := newGitAuditEmitter()
	origEmitter := defaultAuditEmitter
	defaultAuditEmitter = emitter
	t.Cleanup(func() { defaultAuditEmitter = origEmitter })

	body := receivePackBody(t, env, "myws")
	before := repoState(t, env, "myws")
	rec := env.doRequest(t, http.MethodPost, "/git/myorg/myws.git/git-receive-pack", body, withBasicAuth("x", "af_key_userB"))
	if rec.Code != http.StatusForbidden || rec.Body.String() != readOnlyBody {
		t.Fatalf("member push: (%d, %q); want 403 %q", rec.Code, rec.Body, readOnlyBody)
	}
	time.Sleep(200 * time.Millisecond) // the post-push hook runs asynchronously
	if after := repoState(t, env, "myws"); after != before {
		t.Errorf("repository changed:\nbefore %s\nafter  %s", before, after)
	}
	if pre.Load() != 0 || post.Load() != 0 || len(emitter.Events()) != 0 {
		t.Errorf("pre-receive %d, post-push %d, audit events %d; want 0, 0, 0", pre.Load(), post.Load(), len(emitter.Events()))
	}

	// The same body from the owner is a valid push.
	if rec := env.doRequest(t, http.MethodPost, "/git/myorg/myws.git/git-receive-pack", body, withBasicAuth("x", "af_key_userA")); rec.Code != http.StatusOK || repoState(t, env, "myws") == before {
		t.Errorf("owner push of the same body: (%d, %q); want 200 and a changed repository", rec.Code, rec.Body)
	}
}

// TS-37-15: refused member push requests write no audit event to the audit
// store. Requirement: 37-REQ-4.4
func TestMemberPush_NoAuditEvent_TS_37_15(t *testing.T) {
	env := newMemberGitEnv(t)
	duck, err := audit.OpenDB(filepath.Join(t.TempDir(), "audit.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { duck.Close() })
	if err := audit.InitSchema(duck); err != nil {
		t.Fatal(err)
	}
	origEmitter := defaultAuditEmitter
	SetAuditEmitter(audit.NewEmitter(audit.NewStore(duck)))
	t.Cleanup(func() { defaultAuditEmitter = origEmitter })
	count := func() int {
		var n int
		if err := duck.QueryRow(`SELECT COUNT(*) FROM hub_audit_events`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	for _, r := range []struct{ method, path, body string }{
		{http.MethodGet, "/git/myorg/myws.git/info/refs?service=git-receive-pack", ""},
		{http.MethodPost, "/git/myorg/myws.git/git-receive-pack", receivePackBody(t, env, "myws")},
	} {
		if rec := env.doRequest(t, r.method, r.path, r.body, withBasicAuth("x", "af_key_userB")); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: %d; want 403", r.method, r.path, rec.Code)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if after := count(); after != before {
		t.Errorf("hub_audit_events %d -> %d; want unchanged", before, after)
	}
}
