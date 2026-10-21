package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/secrets"
	"github.com/agent-fox-dev/hub/internal/wslock"
)

// TS-38-15: The owner with an API key, the owner with a scoped PAT, a PAT
// bound to this slug and an admin token all get past access.
// Requirement: 38-REQ-2.7
func TestBranchPushAllowedCallers_TS_38_15(t *testing.T) {
	env := newBranchPushEnv(t)
	for i, auth := range []*apikit.AuthInfo{userAuth("user-a"), patAuth("user-a", "workspaces:write"), adminAuth()} {
		slug := []string{"allow-key", "allow-pat", "allow-admin"}[i]
		env.seedPushWorkspace(t, &Workspace{Slug: slug, OwnerID: "user-a"}, "fix")
		if a := pushedOK(t, env.push(t, slug, "fix", "", auth)); a.Result != "pushed" {
			t.Errorf("%s: %+v; want pushed", slug, a)
		}
	}

	// A PAT bound to the slug, through the real hub and its binding guard.
	hub := newRealHub(t)
	if err := secrets.InitSchema(hub.db.SqlDB); err != nil {
		t.Fatal(err)
	}
	trunk := filepath.Join(env.root, "allow-bound", "trunk")
	bare := newPushTrunk(t, trunk, "fix")
	if err := insertWorkspace(hub.db.SqlDB, &Workspace{Slug: "allow-bound", GitURL: bare, OwnerID: "u1", Status: "active", CloneStatus: "ready"}); err != nil {
		t.Fatal(err)
	}
	pat, patID := hub.mintPAT(t, "workspaces:write")
	bindToken(t, hub.db.SqlDB, patID, "allow-bound")
	rec := hub.do(http.MethodPost, "/api/v1/workspaces/allow-bound/branches/fix/push", pat, "")
	if a := pushedOK(t, rec); a.Result != "pushed" || bareRef(t, bare, "refs/heads/fix") != a.SHA {
		t.Errorf("bound PAT: %+v", a)
	}
}

// blockPush makes the shared push wait until the returned function is called,
// after signalling on started, and then push.
func blockPush(t *testing.T) (started <-chan struct{}, release func()) {
	t.Helper()
	orig := pushToOriginFn
	start := make(chan struct{}, 1)
	gate := make(chan struct{})
	pushToOriginFn = func(ctx context.Context, p, u string, a transport.AuthMethod, r []config.RefSpec, f bool, w io.Writer) error {
		select {
		case start <- struct{}{}:
		default:
		}
		<-gate
		return orig(ctx, p, u, a, r, f, w)
	}
	t.Cleanup(func() { pushToOriginFn = orig })
	var released bool
	return start, func() {
		if !released {
			released = true
			close(gate)
		}
	}
}

// TS-38-26: sha is the trunk tip read under the lock and the lock is released
// after every outcome.
// Requirement: 38-REQ-5.4, 38-REQ-5.5
func TestBranchPushLockLifetime_TS_38_26(t *testing.T) {
	env := newBranchPushEnv(t)
	env.seedPushWorkspace(t, &Workspace{Slug: "held", OwnerID: "user-a"}, "feat")
	tip := env.trunkTip(t, "held", "feat")

	for _, disconnect := range []bool{false, true} {
		started, release := blockPush(t)
		ctx, cancel := context.WithCancel(context.Background())
		rec := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/held/branches/feat/push", nil).WithContext(ctx)
			authJSON, _ := json.Marshal(userAuth("user-a"))
			req.Header.Set("X-Test-Auth", string(authJSON))
			env.echo.ServeHTTP(rec, req)
			close(done)
		}()
		select {
		case <-started:
		case <-time.After(10 * time.Second):
			t.Fatal("the push did not start")
		}
		if disconnect {
			cancel()
		}
		if unlock, ok := wslock.TryLock("held"); ok {
			unlock()
			t.Errorf("disconnect %v: the lock was free while the push was in flight", disconnect)
		}
		release()
		<-done
		cancel()
		assertLockFree(t, "held")
		if rec.Code != http.StatusOK {
			t.Fatalf("disconnect %v: status %d: %s", disconnect, rec.Code, rec.Body)
		}
	}
	evs := env.branchEvents()
	if a := pushedOK(t, env.push(t, "held", "feat", "", userAuth("user-a"))); a.SHA != tip {
		t.Errorf("sha %s; want the trunk's tip %s", a.SHA, tip)
	}
	for _, ev := range evs {
		if ev.Metadata["sha"] != tip {
			t.Errorf("event sha %v; want %s", ev.Metadata["sha"], tip)
		}
	}

	// A failed push and a refusal release the lock as well.
	stubPush(t, errors.New("unexpected EOF"))
	refusedWithType(t, env.push(t, "held", "feat", "", userAuth("user-a")), http.StatusBadGateway, "forge_error")
	assertLockFree(t, "held")
	assertRefusal(t, env.push(t, "held", "ghost", "", userAuth("user-a")), http.StatusNotFound, `branch_not_found: branch "ghost" does not exist in the workspace`, "branch_not_found")
	assertLockFree(t, "held")
}

// movedHeadTrunk seeds a workspace whose trunk has a commit on a side branch
// that changes README.md, and makes the shared push move refs/heads/main (the
// checked-out branch) to that commit before pushing, as a push to hub's git
// server does while the lock is held: refs only, the worktree untouched. It
// returns the trunk's path and the side commit.
func movedHeadTrunk(t *testing.T, env *branchPushEnv, slug string) (string, string) {
	t.Helper()
	env.seedPushWorkspace(t, &Workspace{Slug: slug, OwnerID: "user-a"}, "feat")
	trunk := filepath.Join(env.root, slug, "trunk")
	pushGit(t, trunk, "checkout", "-q", "-b", "side")
	side := pushCommit(t, trunk, "README.md", "# moved by a push to hub")
	pushGit(t, trunk, "checkout", "-q", "main")
	orig := pushToOriginFn
	pushToOriginFn = func(ctx context.Context, p, u string, a transport.AuthMethod, r []config.RefSpec, f bool, w io.Writer) error {
		pushGit(t, trunk, "update-ref", "refs/heads/main", side)
		return orig(ctx, p, u, a, r, f, w)
	}
	t.Cleanup(func() { pushToOriginFn = orig })
	return trunk, side
}

// TS-38-27: When HEAD moved during the lock, the worktree is hard-reset to the
// new HEAD commit.
// Requirement: 38-REQ-5.6
func TestBranchPushRestoresWorktree_TS_38_27(t *testing.T) {
	env := newBranchPushEnv(t)
	trunk, side := movedHeadTrunk(t, env, "moved")

	pushedOK(t, env.push(t, "moved", "feat", "", userAuth("user-a")))
	if got := pushGit(t, trunk, "rev-parse", "HEAD"); got != side {
		t.Fatalf("HEAD = %s; want %s", got, side)
	}
	content, err := os.ReadFile(filepath.Join(trunk, "README.md"))
	if err != nil || string(content) != "# moved by a push to hub" {
		t.Errorf("README.md = %q (%v); want the new HEAD's content", content, err)
	}
	if status := pushGit(t, trunk, "status", "--porcelain"); status != "" {
		t.Errorf("git status is not clean:\n%s", status)
	}
}

// worktreeWarnings counts the warnings about restoring the worktree.
func worktreeWarnings(t *testing.T, logs *lockedBuffer) int {
	t.Helper()
	n := 0
	for _, rec := range logRecords(t, logs) {
		if msg, _ := rec["msg"].(string); rec["level"] == "WARN" && strings.Contains(msg, "worktree") {
			n++
		}
	}
	return n
}

// TS-38-28: A failed reset, an unmoved HEAD or an unreadable HEAD leaves the
// answer unchanged and logs at most one warning.
// Requirement: 38-REQ-5.7
func TestBranchPushWorktreeLeftAlone_TS_38_28(t *testing.T) {
	env := newBranchPushEnv(t)
	logs := captureBranchPushLog(t)
	storeGitSecrets(t, env, "reset-fails", "GIT_PAT", "tok-12345")

	// A failing reset: the answer stands, one warning without the secret.
	movedHeadTrunk(t, env, "reset-fails")
	oldReset := resetTrunkWorktreeFn
	resetTrunkWorktreeFn = func(*git.Repository, plumbing.Hash) error {
		return errors.New("reset failed near tok-12345")
	}
	t.Cleanup(func() { resetTrunkWorktreeFn = oldReset })
	pushedOK(t, env.push(t, "reset-fails", "feat", "", userAuth("user-a")))
	if n := worktreeWarnings(t, logs); n != 1 {
		t.Errorf("%d worktree warnings; want 1: %s", n, logs)
	}
	if strings.Contains(logs.String(), "tok-12345") {
		t.Errorf("the warning holds the secret: %s", logs)
	}
	resetTrunkWorktreeFn = oldReset

	// HEAD that does not move, and HEAD that cannot be read when the lock is
	// taken: the worktree is not touched (a local change survives).
	env.seedPushWorkspace(t, &Workspace{Slug: "unmoved", OwnerID: "user-a"}, "feat")
	env.seedPushWorkspace(t, &Workspace{Slug: "headless", OwnerID: "user-a"}, "feat")
	if err := os.WriteFile(filepath.Join(env.root, "headless", "trunk", ".git", "HEAD"), []byte("ref: refs/heads/missing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"unmoved", "headless"} {
		readme := filepath.Join(env.root, slug, "trunk", "README.md")
		if err := os.WriteFile(readme, []byte("local change"), 0o644); err != nil {
			t.Fatal(err)
		}
		pushedOK(t, env.push(t, slug, "feat", "", userAuth("user-a")))
		if content, _ := os.ReadFile(readme); string(content) != "local change" {
			t.Errorf("%s: the worktree was reset: README.md = %q", slug, content)
		}
	}
	if n := worktreeWarnings(t, logs); n != 1 {
		t.Errorf("%d worktree warnings in all; want only the failed reset's", n)
	}
}
