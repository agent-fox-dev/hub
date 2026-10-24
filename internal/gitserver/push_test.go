package gitserver

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/wslock"
)

// rejecting is a pre-receive hook refusing the named refs.
func rejecting(refs ...string) PreReceiveHookFunc {
	return func(_ context.Context, _ *sql.DB, _ string, _ *apikit.AuthInfo, upd RefUpdate) error {
		for _, r := range refs {
			if string(upd.Name) == r {
				return errors.New(r + " is blocked")
			}
		}
		return nil
	}
}

// emptyPack is a packfile of zero objects, as a client sends when every
// pushed commit already exists on the server.
func emptyPack() []byte {
	header := []byte{'P', 'A', 'C', 'K', 0, 0, 0, 2, 0, 0, 0, 0}
	sum := sha1.Sum(header)
	return append(header, sum[:]...)
}

// pushSetup is a workspace with its owner's remote and clone, the trunk's
// branch and a push spy running hook.
func pushSetup(t *testing.T, hook PreReceiveHookFunc) (v *env, s *pushSpy, trunk, local, branch string) {
	t.Helper()
	v = newEnv(t)
	s = spyPushes(t, hook)
	trunk = v.trunk("ws")
	local = clone(t, v.remote(t, v.key(t, "owner")))
	return v, s, trunk, local, gitRun(t, trunk, "symbolic-ref", "--short", "HEAD")
}

// A push updates the trunk's ref and worktree, records head_sha, runs the
// post-push hook with the branch and emits one audit event naming the
// actor, the ref and the new head.
func TestPush_UpdatesRepositoryAndRecordsIt(t *testing.T) {
	v, s, trunk, local, branch := pushSetup(t, nil)
	sha := addCommit(t, local, "new.txt", "new")
	gitRun(t, local, "push", "-q", "origin", branch)
	if got := refValue(trunk, "refs/heads/"+branch); got != sha {
		t.Errorf("trunk %s = %s; want %s", branch, got, sha)
	}
	if got := v.headSHA(t, "ws"); got != sha {
		t.Errorf("head_sha = %s; want %s", got, sha)
	}
	if _, err := os.Stat(filepath.Join(trunk, "new.txt")); err != nil {
		t.Error("the trunk worktree was not reset to the pushed commit")
	}
	if got := s.Pushed(t); !reflect.DeepEqual(got, []string{branch}) {
		t.Errorf("post-push branches %v; want [%s]", got, branch)
	}
	events := s.Events()
	if len(events) != 1 {
		t.Fatalf("%d audit events; want 1", len(events))
	}
	e := events[0]
	want := audit.HubEvent{EventType: "hub.git.push", ResourceType: "workspace", Action: "push", Workspace: "ws", ActorID: "owner", ActorType: "api_key"}
	e.Metadata = nil
	if !reflect.DeepEqual(e, want) {
		t.Errorf("event %+v; want %+v", e, want)
	}
	if refs := refsUpdated(t, events[0]); events[0].Metadata["head_sha"] != sha || !reflect.DeepEqual(refs, []string{"refs/heads/" + branch}) {
		t.Errorf("metadata %v; want head_sha %s and refs [refs/heads/%s]", events[0].Metadata, sha, branch)
	}
}

// While a rebuild, merge or sync holds the workspace, a push still updates
// the ref and head_sha but leaves the worktree to the running operation.
func TestPush_LeavesWorktreeToBusyOperation(t *testing.T) {
	v, _, trunk, local, branch := pushSetup(t, nil)
	unlock := wslock.Lock("ws")
	defer unlock()
	sha := addCommit(t, local, "new.txt", "new")
	gitRun(t, local, "push", "-q", "origin", branch)
	if got := refValue(trunk, "refs/heads/"+branch); got != sha || v.headSHA(t, "ws") != sha {
		t.Errorf("trunk %s = %s, head_sha %s; want %s", branch, got, v.headSHA(t, "ws"), sha)
	}
	if _, err := os.Stat(filepath.Join(trunk, "new.txt")); err == nil {
		t.Error("the trunk worktree was reset while the workspace was busy")
	}
}

// An empty receive-pack body is a successful no-op that refreshes head_sha
// from the trunk without consulting the pre-receive hook.
func TestPush_EmptyBodyIsNoOp(t *testing.T) {
	v := newEnv(t)
	calls := 0
	s := spyPushes(t, func(context.Context, *sql.DB, string, *apikit.AuthInfo, RefUpdate) error { calls++; return nil })
	rec := v.do(t, http.MethodPost, wsPath+"/git-receive-pack", "", v.key(t, "owner"))
	if want := string(encodePktLine("unpack ok\n")) + "0000"; rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Errorf("(%d, %q); want 200 %q", rec.Code, rec.Body, want)
	}
	if head := gitRun(t, v.trunk("ws"), "rev-parse", "HEAD"); v.headSHA(t, "ws") != head {
		t.Errorf("head_sha %s; want %s", v.headSHA(t, "ws"), head)
	}
	if calls != 0 || len(s.Events()) != 0 {
		t.Errorf("%d hook calls, %d audit events; want none", calls, len(s.Events()))
	}
}

// An unreadable pack is reported in-band and changes nothing.
func TestPush_UnreadablePackIsAnError(t *testing.T) {
	v := newEnv(t)
	s := spyPushes(t, nil)
	testutil.Exec(t, v.db, `UPDATE workspaces SET head_sha = 'stale' WHERE slug = 'ws'`)
	rec := v.do(t, http.MethodPost, wsPath+"/git-receive-pack", "not a push", v.key(t, "owner"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ERR ") {
		t.Errorf("(%d, %q); want 200 with an ERR line", rec.Code, rec.Body)
	}
	if v.headSHA(t, "ws") != "stale" || len(s.Events()) != 0 {
		t.Errorf("head_sha %s, %d audit events; want stale and none", v.headSHA(t, "ws"), len(s.Events()))
	}
}

// The pre-receive hook sees every ref update of a push, after the objects
// are in the trunk and before the ref is written, with the workspace, the
// actor and the old and new hashes.
func TestPush_PreReceiveHookSeesEachRefUpdate(t *testing.T) {
	type call struct {
		slug, actor string
		upd         RefUpdate
		inTrunk     bool
		refBefore   string
	}
	var calls []call
	var trunk string
	v, _, trunk, local, _ := pushSetup(t, func(_ context.Context, db *sql.DB, slug string, actor *apikit.AuthInfo, upd RefUpdate) error {
		_, err := gitTry(trunk, "cat-file", "-e", upd.New.String())
		calls = append(calls, call{slug, actor.CredentialType + ":" + actor.UserID, upd, err == nil, refValue(trunk, string(upd.Name))})
		return nil
	})
	_ = v
	gitRun(t, local, "checkout", "-q", "-b", "b")
	oldB := addCommit(t, local, "b1.txt", "b1")
	gitRun(t, local, "push", "-q", "origin", "b")
	calls = nil
	gitRun(t, local, "checkout", "-q", "-b", "a")
	shaA := addCommit(t, local, "a.txt", "a")
	gitRun(t, local, "checkout", "-q", "b")
	shaB := addCommit(t, local, "b2.txt", "b2")
	gitRun(t, local, "push", "-q", "origin", "a", "b")
	sort.Slice(calls, func(i, j int) bool { return calls[i].upd.Name < calls[j].upd.Name })
	want := []call{
		{"ws", "api_key:owner", RefUpdate{Name: "refs/heads/a", Old: plumbing.ZeroHash, New: plumbing.NewHash(shaA)}, true, ""},
		{"ws", "api_key:owner", RefUpdate{Name: "refs/heads/b", Old: plumbing.NewHash(oldB), New: plumbing.NewHash(shaB)}, true, oldB},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("hook calls %+v; want %+v", calls, want)
	}
}

// A push mixing a rejected ref, an accepted branch and a delete writes only
// the accepted updates, tells the client which ref was rejected and why,
// and drives the post-push hook (branches, deletes excluded) and the audit
// event (every accepted ref) from the accepted updates only.
func TestPush_MixedPushAppliesAcceptedRefsOnly(t *testing.T) {
	_, s, trunk, local, _ := pushSetup(t, rejecting("refs/heads/p1"))
	gitRun(t, local, "checkout", "-q", "-b", "old")
	addCommit(t, local, "old.txt", "old")
	gitRun(t, local, "push", "-q", "origin", "old")
	s.Pushed(t)
	before := len(s.Events())
	gitRun(t, local, "checkout", "-q", "-b", "p1")
	addCommit(t, local, "p1.txt", "p1")
	gitRun(t, local, "checkout", "-q", "-b", "feature")
	sha := addCommit(t, local, "feature.txt", "feature")
	out, err := gitTry(local, "push", "origin", "p1", "feature", ":old")
	if err == nil || !strings.Contains(out, "remote rejected") || !strings.Contains(out, "refs/heads/p1 is blocked") {
		t.Errorf("push: %v\n%s\nwant a remote rejection naming refs/heads/p1", err, out)
	}
	if refValue(trunk, "refs/heads/feature") != sha || refValue(trunk, "refs/heads/p1") != "" || refValue(trunk, "refs/heads/old") != "" {
		t.Errorf("trunk: feature %s, p1 %q, old %q; want %s, none, none", refValue(trunk, "refs/heads/feature"), refValue(trunk, "refs/heads/p1"), refValue(trunk, "refs/heads/old"), sha)
	}
	if got := s.Pushed(t); !reflect.DeepEqual(got, []string{"feature"}) {
		t.Errorf("post-push branches %v; want [feature]", got)
	}
	events := s.Events()[before:]
	if len(events) != 1 {
		t.Fatalf("%d audit events; want 1", len(events))
	}
	refs := refsUpdated(t, events[0])
	sort.Strings(refs)
	if !reflect.DeepEqual(refs, []string{"refs/heads/feature", "refs/heads/old"}) || events[0].Metadata["head_sha"] != sha {
		t.Errorf("audit refs %v head_sha %v; want [feature old] and %s", refs, events[0].Metadata["head_sha"], sha)
	}
}

// A push with no accepted update emits no audit event and runs no post-push
// hook, but still refreshes head_sha from the trunk.
func TestPush_RejectedOnlyPushHasNoSideEffects(t *testing.T) {
	v, s, trunk, local, _ := pushSetup(t, rejecting("refs/heads/blocked"))
	testutil.Exec(t, v.db, `UPDATE workspaces SET head_sha = 'stale' WHERE slug = 'ws'`)
	gitRun(t, local, "checkout", "-q", "-b", "blocked")
	addCommit(t, local, "x.txt", "x")
	if _, err := gitTry(local, "push", "origin", "blocked"); err == nil {
		t.Fatal("the rejected push succeeded")
	}
	if n := len(s.Events()); n != 0 {
		t.Errorf("%d audit events; want none", n)
	}
	if head := gitRun(t, trunk, "rev-parse", "HEAD"); v.headSHA(t, "ws") != head {
		t.Errorf("head_sha %s; want the trunk HEAD %s", v.headSHA(t, "ws"), head)
	}
	// The next accepted push is the first post-push call.
	gitRun(t, local, "checkout", "-q", "-b", "open")
	gitRun(t, local, "push", "-q", "origin", "open")
	if got := s.Pushed(t); !reflect.DeepEqual(got, []string{"open"}) {
		t.Errorf("first post-push call %v; want [open]", got)
	}
}

// A client that does not ask for report-status still has a hook rejection
// enforced, gets no per-ref message and no ERR line, and the refs accepted
// in the same push keep their side effects; a failed unpack is still an ERR.
func TestPush_WithoutReportStatus(t *testing.T) {
	v, s, trunk, _, _ := pushSetup(t, rejecting("refs/heads/blocked"))
	owner := v.key(t, "owner")
	base, zero := gitRun(t, trunk, "rev-parse", "HEAD"), plumbing.ZeroHash.String()
	body := receivePackRequest([]string{zero + " " + base + " refs/heads/blocked", zero + " " + base + " refs/heads/open"}, "agent=test", emptyPack())
	rec := v.do(t, http.MethodPost, wsPath+"/git-receive-pack", body, owner)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "ERR") || strings.Contains(rec.Body.String(), "blocked") {
		t.Errorf("(%d, %q); want 200 without an ERR line or the hook's message", rec.Code, rec.Body)
	}
	if refValue(trunk, "refs/heads/blocked") != "" || refValue(trunk, "refs/heads/open") != base {
		t.Errorf("trunk: blocked %q, open %q; want none and %s", refValue(trunk, "refs/heads/blocked"), refValue(trunk, "refs/heads/open"), base)
	}
	if events := s.Events(); len(events) != 1 || !reflect.DeepEqual(refsUpdated(t, events[0]), []string{"refs/heads/open"}) {
		t.Errorf("audit events %+v; want one for refs/heads/open", events)
	}
	if got := s.Pushed(t); !reflect.DeepEqual(got, []string{"open"}) {
		t.Errorf("post-push branches %v; want [open]", got)
	}
	if v.headSHA(t, "ws") != base {
		t.Errorf("head_sha %s; want %s", v.headSHA(t, "ws"), base)
	}
	body = receivePackRequest([]string{zero + " " + base + " refs/heads/broken"}, "agent=test", []byte("not a packfile"))
	rec = v.do(t, http.MethodPost, wsPath+"/git-receive-pack", body, owner)
	if !strings.Contains(rec.Body.String(), "ERR") || refValue(trunk, "refs/heads/broken") != "" || len(s.Events()) != 1 {
		t.Errorf("unreadable pack: %q, ref %q, %d events; want an ERR line, no ref and no new event", rec.Body, refValue(trunk, "refs/heads/broken"), len(s.Events()))
	}
}

// failingEmitter refuses every event.
type failingEmitter struct{}

func (failingEmitter) Emit(context.Context, audit.HubEvent) error { return errors.New("audit down") }

// Audit is optional: without an emitter, or with one that fails, the push
// succeeds all the same.
func TestPush_AuditEmitterOptional(t *testing.T) {
	_, _, trunk, local, _ := pushSetup(t, nil)
	for branch, emitter := range map[string]audit.Emitter{"none": nil, "failing": failingEmitter{}} {
		defaultAuditEmitter = emitter
		gitRun(t, local, "checkout", "-q", "-b", branch)
		sha := addCommit(t, local, branch+".txt", branch)
		if out, err := gitTry(local, "push", "origin", branch); err != nil || refValue(trunk, "refs/heads/"+branch) != sha {
			t.Errorf("push with %s emitter: %v\n%s", branch, err, out)
		}
	}
}
