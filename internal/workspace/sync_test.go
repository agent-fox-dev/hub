package workspace

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/wslock"
)

const upstreamSHA = "1111111111111111111111111111111111111111"

func syncReq(t *testing.T, v *env, slug, query string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	t.Helper()
	return v.do(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync"+query, "", auth)
}

// TestSync_UpToDate: an up-to-date fetch records the upstream HEAD and the
// time, keeps head_sha, passes the stored credential and the branch to the
// fetch, and records the event.
func TestSync_UpToDate(t *testing.T) {
	v := newEnv(t)
	v.seed(t, &Workspace{Slug: "ws", OwnerID: owner, CloneStatus: "ready", Branch: strPtr("dev"), HeadSHA: strPtr(headSHA), SyncError: strPtr("old")})
	storeSecrets(t, v.db, "ws", "GIT_USERNAME", "bob", "GIT_PASSWORD", "pw")
	var gotPath, gotBranch, gotLocal string
	var gotAuth transport.AuthMethod
	swap(t, &syncFetchAndCompareFn, func(_ context.Context, repoPath string, auth transport.AuthMethod, branch *string, local string) (string, string, error) {
		gotPath, gotAuth, gotBranch, gotLocal = repoPath, auth, *branch, local
		return upstreamSHA, "up_to_date", nil
	})
	rec := syncReq(t, v, "ws", "", testutil.User(owner))
	expectStatus(t, rec, http.StatusOK)
	got := testutil.Object(t, rec)
	if got["sync_status"] != "idle" || got["upstream_head_sha"] != upstreamSHA || got["head_sha"] != headSHA || got["last_sync_at"] == nil || got["sync_error"] != nil {
		t.Errorf("response %v", got)
	}
	if gotPath != v.trunk("ws") || gotBranch != "dev" || gotLocal != headSHA || *gotAuth.(*githttp.BasicAuth) != (githttp.BasicAuth{Username: "bob", Password: "pw"}) {
		t.Errorf("fetch got %s %s %s %+v", gotPath, gotBranch, gotLocal, gotAuth)
	}
	if ev := v.events.one(t, "hub.workspace.sync"); ev.Metadata["result"] != "up_to_date" || ev.Workspace != "ws" {
		t.Errorf("event %+v", ev)
	}
}

// TestSync_FastForward: a fast-forward moves the local ref and head_sha to the
// upstream HEAD.
func TestSync_FastForward(t *testing.T) {
	v := newEnv(t)
	v.seed(t, &Workspace{Slug: "ws", OwnerID: owner, CloneStatus: "ready", HeadSHA: strPtr(headSHA)})
	updated := stubSync(t, upstreamSHA, "fast_forward", nil, nil)
	rec := syncReq(t, v, "ws", "", testutil.Admin())
	expectStatus(t, rec, http.StatusOK)
	if got := testutil.Object(t, rec); got["head_sha"] != upstreamSHA || got["upstream_head_sha"] != upstreamSHA || got["sync_status"] != "idle" || got["last_sync_at"] == nil {
		t.Errorf("response %v", got)
	}
	if len(*updated) != 1 || (*updated)[0] != upstreamSHA {
		t.Errorf("ref updates %v", *updated)
	}
	if ev := v.events.one(t, "hub.workspace.sync"); ev.Metadata["result"] != "fast_forward" {
		t.Errorf("event %+v", ev)
	}
}

// TestSync_Failures: each failure of the git steps leaves sync_status error
// with the reason, head_sha unchanged and no event; a divergence is reported
// with the upstream HEAD and the way out.
func TestSync_Failures(t *testing.T) {
	v := newEnv(t)
	cases := []struct {
		name    string
		sha     string
		outcome string
		err     error
		refErr  error
		code    int
		msg     string
		errText string
	}{
		{"diverged", upstreamSHA, "diverged", nil, nil, http.StatusConflict, "diverged; use --reset-to-upstream", "upstream history has diverged"},
		{"fetch error", "", "", errors.New("dial tcp: refused"), nil, http.StatusBadGateway, "upstream fetch error", "upstream fetch failed: dial tcp: refused"},
		{"ref update error", upstreamSHA, "fast_forward", nil, errors.New("locked ref"), http.StatusBadGateway, "could not fast-forward", "ref update failed: locked ref"},
		{"unknown outcome", upstreamSHA, "sideways", nil, nil, http.StatusInternalServerError, "unexpected sync outcome", "unexpected sync outcome: sideways"},
	}
	for i, c := range cases {
		slug := "ws" + string(rune('a'+i))
		v.seed(t, &Workspace{Slug: slug, OwnerID: owner, CloneStatus: "ready", HeadSHA: strPtr(headSHA)})
		stubSync(t, c.sha, c.outcome, c.err, c.refErr)
		expectError(t, syncReq(t, v, slug, "", testutil.User(owner)), c.code, c.msg, "")
		if v.column(t, slug, "sync_status") != "error" || !strings.Contains(v.column(t, slug, "sync_error"), c.errText) || v.column(t, slug, "head_sha") != headSHA {
			t.Errorf("%s: status %s, error %q, head %s", c.name, v.column(t, slug, "sync_status"), v.column(t, slug, "sync_error"), v.column(t, slug, "head_sha"))
		}
		if c.name == "diverged" && v.column(t, slug, "upstream_head_sha") != upstreamSHA {
			t.Error("the divergence did not record the upstream HEAD")
		}
	}
	if len(v.events.ofType("hub.workspace.sync")) != 0 {
		t.Error("a failed sync emitted an event")
	}

	// Without the sync functions, or without a readable credential, the sync
	// fails before fetching.
	v.seed(t, &Workspace{Slug: "uninit", OwnerID: owner, CloneStatus: "ready"})
	swap(t, &syncFetchAndCompareFn, nil)
	expectError(t, syncReq(t, v, "uninit", "", testutil.User(owner)), http.StatusBadGateway, "sync system not initialized", "")
	expectError(t, syncReq(t, v, "uninit", "?reset_to_upstream=true", testutil.User(owner)), http.StatusBadGateway, "sync system not initialized", "")
	if v.column(t, "uninit", "sync_status") != "error" {
		t.Error("the uninitialized sync left sync_status " + v.column(t, "uninit", "sync_status"))
	}
	v.seed(t, &Workspace{Slug: "no-cred", OwnerID: owner, CloneStatus: "ready"})
	testutil.Exec(t, v.db, `DROP TABLE secrets`)
	expectError(t, syncReq(t, v, "no-cred", "", testutil.User(owner)), http.StatusBadGateway, "credential resolution error", "")
	if !strings.Contains(v.column(t, "no-cred", "sync_error"), "credential resolution failed") {
		t.Errorf("sync_error %q", v.column(t, "no-cred", "sync_error"))
	}
}

// TestSync_Preconditions: the workspace must be active with a ready clone and
// sync enabled and not syncing; the caller must have write access.
func TestSync_Preconditions(t *testing.T) {
	v := newEnv(t)
	stubSync(t, upstreamSHA, "up_to_date", nil, nil)
	v.orgWorkspace(t, "team")
	v.seed(t, &Workspace{Slug: "archived", OwnerID: owner, Status: "archived", CloneStatus: "archived"})
	v.seed(t, &Workspace{Slug: "pending", OwnerID: owner, CloneStatus: "pending"})
	v.seed(t, &Workspace{Slug: "disabled", OwnerID: owner, CloneStatus: "ready", SyncMode: "disabled"})
	v.seed(t, &Workspace{Slug: "syncing", OwnerID: owner, CloneStatus: "ready", SyncStatus: "syncing"})
	v.ready(t, "busy", owner)
	unlock, _ := wslock.TryLock("busy")
	defer unlock()
	o := testutil.User(owner)
	for _, c := range []struct {
		name, slug string
		auth       *apikit.AuthInfo
		code       int
		msg        string
	}{
		{"archived", "archived", o, http.StatusBadRequest, "not active"},
		{"clone not ready", "pending", o, http.StatusBadRequest, "clone is not ready"},
		{"disabled", "disabled", o, http.StatusBadRequest, "sync is disabled"},
		{"already syncing", "syncing", o, http.StatusConflict, "already in progress"},
		{"busy", "busy", o, http.StatusConflict, "another operation is running"},
		{"unauthenticated", "team", nil, http.StatusUnauthorized, "authentication required"},
		{"read PAT", "team", testutil.PAT(owner, "workspaces:read"), http.StatusForbidden, "workspaces:write scope"},
		{"member", "team", testutil.User(member), http.StatusForbidden, "read-only access"},
		{"stranger", "team", testutil.User(stranger), http.StatusNotFound, "workspace not found"},
		{"unknown", "nope", o, http.StatusNotFound, "workspace not found"},
	} {
		if msg := expectStatus(t, syncReq(t, v, c.slug, "", c.auth), c.code); !strings.Contains(msg, c.msg) {
			t.Errorf("%s: %q; want %q", c.name, msg, c.msg)
		}
	}
	if v.column(t, "syncing", "sync_status") != "syncing" || v.column(t, "busy", "sync_status") != "idle" {
		t.Error("a refused sync changed sync_status")
	}
	// A rebuild in progress does not block the sync, and a member's org-mate
	// owner syncs a carry_patch workspace the standard way when no extension
	// is registered.
	end, _ := wslock.BeginRebuild("team")
	defer end()
	expectStatus(t, syncReq(t, v, "team", "", o), http.StatusOK)
}

// TestSync_ClaimIsAtomic: two syncs that both passed the preconditions cannot
// both run; the second finds the status claimed.
func TestSync_ClaimIsAtomic(t *testing.T) {
	v := newEnv(t)
	v.ready(t, "ws", owner)
	if ok, err := claimSyncStatus(v.db, "ws"); err != nil || !ok {
		t.Fatalf("first claim = %v, %v", ok, err)
	}
	if ok, err := claimSyncStatus(v.db, "ws"); err != nil || ok {
		t.Fatalf("second claim = %v, %v; want refused", ok, err)
	}
	// A claim the database refuses is a 500.
	v.ready(t, "race", owner)
	stubSync(t, upstreamSHA, "up_to_date", nil, nil)
	testutil.Exec(t, v.db, `CREATE TRIGGER no_claim BEFORE UPDATE ON workspaces WHEN NEW.sync_status = 'syncing' BEGIN SELECT RAISE(FAIL, 'down'); END`)
	expectError(t, syncReq(t, v, "race", "", testutil.User(owner)), http.StatusInternalServerError, "failed to update sync status", "")
}

// TestSync_CancelledRequest: a request whose context is cancelled while the
// fetch runs ends with sync_status error naming the interruption.
func TestSync_CancelledRequest(t *testing.T) {
	v := newEnv(t)
	v.ready(t, "ws", owner)
	started := make(chan struct{})
	swap(t, &syncFetchAndCompareFn, func(ctx context.Context, _ string, _ transport.AuthMethod, _ *string, _ string) (string, string, error) {
		close(started)
		<-ctx.Done()
		return "", "", ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { v.e.ServeHTTP(rec, request(ctx, http.MethodPost, "/api/v1/workspaces/ws/sync", testutil.User(owner))); close(done) }()
	<-started
	cancel()
	<-done
	if rec.Code != http.StatusGatewayTimeout || !strings.Contains(v.column(t, "ws", "sync_error"), "sync interrupted: context canceled") || v.column(t, "ws", "sync_status") != "error" {
		t.Errorf("status %d, sync_status %s, sync_error %q", rec.Code, v.column(t, "ws", "sync_status"), v.column(t, "ws", "sync_error"))
	}
}

// TestSync_ResetToUpstream: reset_to_upstream forces the local ref to the
// upstream HEAD whatever the ancestry, and records it as head_sha; a failed
// fetch or ref update leaves head_sha alone.
func TestSync_ResetToUpstream(t *testing.T) {
	v := newEnv(t)
	v.seed(t, &Workspace{Slug: "ws", OwnerID: owner, CloneStatus: "ready", HeadSHA: strPtr(headSHA), SyncStatus: "error", SyncError: strPtr("diverged")})
	updated := stubSync(t, upstreamSHA, "diverged", nil, nil)
	rec := syncReq(t, v, "ws", "?reset_to_upstream=true", testutil.User(owner))
	expectStatus(t, rec, http.StatusOK)
	if got := testutil.Object(t, rec); got["head_sha"] != upstreamSHA || got["upstream_head_sha"] != upstreamSHA || got["sync_status"] != "idle" || got["sync_error"] != nil || got["last_sync_at"] == nil {
		t.Errorf("response %v", got)
	}
	if len(*updated) != 1 || (*updated)[0] != upstreamSHA {
		t.Errorf("ref updates %v", *updated)
	}
	if ev := v.events.one(t, "hub.workspace.sync"); ev.Metadata["result"] != "reset_to_upstream" {
		t.Errorf("event %+v", ev)
	}

	stubSync(t, "", "", errors.New("no route"), nil)
	expectError(t, syncReq(t, v, "ws", "?reset_to_upstream=true", testutil.User(owner)), http.StatusBadGateway, "reset-to-upstream failed: upstream fetch error", "")
	stubSync(t, headSHA, "diverged", nil, errors.New("locked"))
	expectError(t, syncReq(t, v, "ws", "?reset_to_upstream=true", testutil.User(owner)), http.StatusBadGateway, "could not update integration branch", "")
	if v.column(t, "ws", "head_sha") != upstreamSHA || v.column(t, "ws", "sync_status") != "error" || !strings.Contains(v.column(t, "ws", "sync_error"), "reset-to-upstream ref update failed: locked") {
		t.Errorf("after the failures: head %s, status %s, error %q", v.column(t, "ws", "head_sha"), v.column(t, "ws", "sync_status"), v.column(t, "ws", "sync_error"))
	}
}

// TestSync_CarryPatchExtension: a carry_patch workspace is synced by the
// registered extension, whose fields are merged into the re-read workspace;
// an extension that declines falls through to the standard sync and one that
// fails has answered already.
func TestSync_CarryPatchExtension(t *testing.T) {
	v := newEnv(t)
	v.orgWorkspace(t, "team")
	var gotSlug, gotPath string
	swap(t, &syncExtension, SyncExtension(func(_ echo.Context, slug, repoPath string) (map[string]any, bool, error) {
		gotSlug, gotPath = slug, repoPath
		testutil.Exec(t, v.db, `UPDATE workspaces SET upstream_head_sha = ? WHERE slug = ?`, upstreamSHA, slug)
		return map[string]any{"patches_merged": []string{"feature/a"}, "rebuild_triggered": true, "status": "shadowed"}, true, nil
	}))
	rec := syncReq(t, v, "team", "", testutil.User(owner))
	expectStatus(t, rec, http.StatusOK)
	got := testutil.Object(t, rec)
	if got["slug"] != "team" || got["upstream_head_sha"] != upstreamSHA || got["rebuild_triggered"] != true || got["status"] != "shadowed" || got["access"] != "owner" {
		t.Errorf("response %v", got)
	}
	if merged, _ := got["patches_merged"].([]any); len(merged) != 1 || merged[0] != "feature/a" || gotSlug != "team" || gotPath != v.trunk("team") {
		t.Errorf("extension got %s %s; merged %v", gotSlug, gotPath, got["patches_merged"])
	}

	stubSync(t, upstreamSHA, "up_to_date", nil, nil)
	RegisterSyncExtension(func(echo.Context, string, string) (map[string]any, bool, error) { return nil, false, nil })
	rec = syncReq(t, v, "team", "", testutil.User(owner))
	expectStatus(t, rec, http.StatusOK)
	if got := testutil.Object(t, rec); got["patches_merged"] != nil || got["last_sync_at"] == nil {
		t.Errorf("fallthrough response %v", got)
	}

	RegisterSyncExtension(func(c echo.Context, _, _ string) (map[string]any, bool, error) {
		return nil, false, c.JSON(http.StatusTeapot, map[string]string{"extension": "failed"})
	})
	if rec := syncReq(t, v, "team", "", testutil.User(owner)); rec.Code != http.StatusTeapot {
		t.Errorf("extension failure: %d %s", rec.Code, rec.Body)
	}
	// A standard workspace never sees the extension.
	v.ready(t, "std", owner)
	RegisterSyncExtension(func(echo.Context, string, string) (map[string]any, bool, error) { t.Error("called"); return nil, true, nil })
	expectStatus(t, syncReq(t, v, "std", "", testutil.User(owner)), http.StatusOK)
}

// TestSync_GitFetchAndCompare: against real repositories the fetch reports
// up_to_date, fast_forward and diverged, and the ref update moves the branch
// and the working tree.
func TestSync_GitFetchAndCompare(t *testing.T) {
	trunk := filepath.Join(t.TempDir(), "trunk")
	bare := newTrunk(t, trunk)
	gitRun(t, trunk, "push", "-q", "origin", "main")
	base := gitRun(t, trunk, "rev-parse", "HEAD")

	sha, outcome, err := defaultSyncFetchAndCompareFn(context.Background(), trunk, nil, nil, base)
	if err != nil || sha != base || outcome != "up_to_date" {
		t.Fatalf("up to date: %s %s %v", sha, outcome, err)
	}
	if _, outcome, _ := defaultSyncFetchAndCompareFn(context.Background(), trunk, nil, strPtr("main"), ""); outcome != "up_to_date" {
		t.Errorf("without a local HEAD: %s; want up_to_date", outcome)
	}

	// Origin advances: fast-forward, then the ref update brings the file in.
	work := filepath.Join(t.TempDir(), "work")
	gitRun(t, filepath.Dir(work), "clone", "-q", bare, work)
	next := commit(t, work, "next.txt", "next")
	gitRun(t, work, "push", "-q", "origin", "main")
	sha, outcome, err = defaultSyncFetchAndCompareFn(context.Background(), trunk, nil, nil, base)
	if err != nil || sha != next || outcome != "fast_forward" {
		t.Fatalf("after origin advanced: %s %s %v; want %s fast_forward", sha, outcome, err, next)
	}
	if err := defaultSyncUpdateLocalRefFn(trunk, nil, next); err != nil {
		t.Fatal(err)
	}
	if gitRun(t, trunk, "rev-parse", "main") != next || gitRun(t, trunk, "status", "--porcelain") != "" {
		t.Error("the ref update did not move main and reset the working tree")
	}
	if _, err := os.Stat(filepath.Join(trunk, "next.txt")); err != nil {
		t.Error("the working tree lacks the fetched file")
	}

	// Origin is rewritten: diverged, and the local commit unknown to the
	// trunk is diverged as well.
	gitRun(t, work, "reset", "-q", "--hard", base)
	rewritten := commit(t, work, "other.txt", "other")
	gitRun(t, work, "push", "-q", "--force", "origin", "main")
	if sha, outcome, err := defaultSyncFetchAndCompareFn(context.Background(), trunk, nil, strPtr("main"), next); err != nil || sha != rewritten || outcome != "diverged" {
		t.Errorf("after a force push: %s %s %v; want %s diverged", sha, outcome, err, rewritten)
	}
	if _, outcome, _ := defaultSyncFetchAndCompareFn(context.Background(), trunk, nil, nil, upstreamSHA); outcome != "diverged" {
		t.Errorf("unknown local commit: %s; want diverged", outcome)
	}
	// A failed ref update's working tree reset is not an error.
	if err := defaultSyncUpdateLocalRefFn(trunk, strPtr("main"), rewritten); err != nil {
		t.Errorf("ref update to the rewritten tip: %v", err)
	}
	if _, _, err := defaultSyncFetchAndCompareFn(context.Background(), t.TempDir(), nil, nil, ""); err == nil || !strings.Contains(err.Error(), "open repository") {
		t.Errorf("no repository: %v", err)
	}
	if err := defaultSyncUpdateLocalRefFn(t.TempDir(), nil, next); err == nil {
		t.Error("ref update without a repository succeeded")
	}
	if !isShallowError(errors.New("object not found")) || isShallowError(errors.New("refused")) || isShallowError(nil) {
		t.Error("isShallowError")
	}
}

// TestSync_ReconcileStuck: at start every workspace left syncing is set to
// error with the reason, the others untouched; a failing database is an error.
func TestSync_ReconcileStuck(t *testing.T) {
	v := newEnv(t)
	v.seed(t, &Workspace{Slug: "stuck", OwnerID: owner, SyncStatus: "syncing"})
	v.seed(t, &Workspace{Slug: "fine", OwnerID: owner, SyncStatus: "idle"})
	if err := ReconcileStuckSyncs(v.db); err != nil {
		t.Fatal(err)
	}
	if v.column(t, "stuck", "sync_status") != "error" || v.column(t, "stuck", "sync_error") != "sync interrupted by server restart" || v.column(t, "fine", "sync_status") != "idle" {
		t.Errorf("stuck: %s %q; fine: %s", v.column(t, "stuck", "sync_status"), v.column(t, "stuck", "sync_error"), v.column(t, "fine", "sync_status"))
	}
	testutil.Exec(t, v.db, `DROP TABLE workspaces`)
	if err := ReconcileStuckSyncs(v.db); err == nil {
		t.Error("no error without the table")
	}
}
