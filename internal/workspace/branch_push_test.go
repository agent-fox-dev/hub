package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/wslock"
)

// push sends the branch push of name (the segment as sent) on slug.
func (v *env) push(t *testing.T, slug, name, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	t.Helper()
	return v.do(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/branches/"+name+"/push", body, auth)
}

// pushed decodes a 200 answer of the push.
func pushed(t *testing.T, rec *httptest.ResponseRecorder) branchPushAnswer {
	t.Helper()
	expectStatus(t, rec, http.StatusOK)
	var a branchPushAnswer
	testutil.Decode(t, rec, &a)
	return a
}

// pushEvents are the hub.branch.push events recorded so far.
func (v *env) pushEvents() []audit.HubEvent { return v.events.ofType(audit.EventBranchPush) }

// TestBranchPush_Owner: the owner's push of a branch answers its tip and
// origin's URL, moves origin's branch, and records one event; a repeat is
// up_to_date, a forced push of an overwritten branch is forced.
func TestBranchPush_Owner(t *testing.T) {
	v := newEnv(t)
	bare := v.seedPushWorkspace(t, &Workspace{Slug: "ws", OwnerID: owner}, "fix")
	tip := gitRun(t, v.trunk("ws"), "rev-parse", "fix")

	a := pushed(t, v.push(t, "ws", "fix", "", testutil.User(owner)))
	if a.Branch != "fix" || a.SHA != tip || a.Remote != "origin" || a.RemoteURL != bare || a.Result != "pushed" || bareRef(t, bare, "refs/heads/fix") != tip {
		t.Errorf("answer %+v; origin's fix %s", a, bareRef(t, bare, "refs/heads/fix"))
	}
	if a := pushed(t, v.push(t, "ws", "fix", `{"force":true}`, testutil.User(owner))); a.Result != "up_to_date" {
		t.Errorf("repeat: %s", a.Result)
	}
	// Origin gains a commit the trunk lacks: a plain push is refused with
	// origin's commit and the hint to force, and force overwrites it.
	work := filepath.Join(t.TempDir(), "w")
	gitRun(t, filepath.Dir(work), "clone", "-q", "-b", "fix", bare, work)
	forge := commit(t, work, "forge.txt", "made on the forge")
	gitRun(t, work, "push", "-q", "origin", "fix")
	expectError(t, v.push(t, "ws", "fix", "", testutil.User(owner)), http.StatusConflict, "non_fast_forward: origin's fix is at "+forge+", which is not in the workspace's branch; push with force to overwrite it", "non_fast_forward")
	if a := pushed(t, v.push(t, "ws", "fix", `{"force":true}`, testutil.User(owner))); a.Result != "forced" || bareRef(t, bare, "refs/heads/fix") != tip {
		t.Errorf("forced: %+v, origin's fix %s", a, bareRef(t, bare, "refs/heads/fix"))
	}
	calls := *v.pushes
	if len(calls) != 4 || calls[0].refSpecs[0] != "refs/heads/fix:refs/heads/fix" || calls[0].force || calls[3].refSpecs[0] != "+refs/heads/fix:refs/heads/fix" || !calls[3].force || !calls[3].deadline {
		t.Errorf("pushes %+v", calls)
	}
	evs := v.pushEvents()
	if len(evs) != 4 || evs[0].Metadata["result"] != "pushed" || evs[0].Metadata["sha"] != tip || evs[0].Metadata["ok"] != true || evs[0].Metadata["remote_url"] != bare ||
		evs[0].ResourceType != "branch" || evs[0].ResourceID != "fix" || evs[0].Workspace != "ws" || evs[0].ActorID != owner ||
		evs[2].Metadata["ok"] != false || evs[2].Metadata["error_type"] != "non_fast_forward" || evs[3].Metadata["result"] != "forced" || evs[3].Metadata["force"] != true {
		t.Errorf("events %+v", evs)
	}
	// The own branch is never forced and its non-fast-forward hint is to sync.
	pushed(t, v.push(t, "ws", "main", "", testutil.User(owner)))
	gitRun(t, work, "fetch", "-q", "origin")
	gitRun(t, work, "checkout", "-q", "main")
	commit(t, work, "forge2.txt", "x")
	gitRun(t, work, "push", "-q", "origin", "main")
	expectError(t, v.push(t, "ws", "main", "", testutil.User(owner)), http.StatusConflict, "sync the workspace to bring it up to date", "non_fast_forward")
	expectError(t, v.push(t, "ws", "main", `{"force":true}`, testutil.User(owner)), http.StatusBadRequest, `protected_branch: "main" is the workspace's own branch`, "protected_branch")
}

// TestBranchPush_Names: the name is the path segment decoded once, so every
// branch name git accepts can be pushed; an invalid name and a branch the
// trunk lacks are 404 branch_not_found.
func TestBranchPush_Names(t *testing.T) {
	v := newEnv(t)
	bare := v.seedPushWorkspace(t, &Workspace{Slug: "ws", OwnerID: owner})
	for _, name := range []string{"fix/42-nil-map", "a%25b", "100%", "x%2Fy", "release/v1.0"} {
		gitRun(t, v.trunk("ws"), "branch", name)
		a := pushed(t, v.push(t, "ws", url.PathEscape(name), "", testutil.User(owner)))
		if a.Branch != name || bareRef(t, bare, "refs/heads/"+name) != a.SHA {
			t.Errorf("%s: answer %+v, origin %s", name, a, bareRef(t, bare, "refs/heads/"+name))
		}
	}
	if bareRef(t, bare, "refs/heads/a%b") != "" {
		t.Error("a%2525b was decoded twice")
	}
	expectError(t, v.push(t, "ws", "..", "", testutil.User(owner)), http.StatusNotFound, "branch_not_found", "branch_not_found")
	expectError(t, v.push(t, "ws", "-flag", "", testutil.User(owner)), http.StatusNotFound, `"-flag" is not a valid branch name`, "branch_not_found")
	expectError(t, v.push(t, "ws", "ghost", "", testutil.User(owner)), http.StatusNotFound, `branch "ghost" does not exist in the workspace`, "branch_not_found")
	if len(*v.pushes) != 5 || len(v.pushEvents()) != 5 {
		t.Error("a refusal pushed or emitted")
	}
}

// TestBranchPush_Body: the body is optional; an object without force or with
// unknown fields pushes non-forced; anything else is 400.
func TestBranchPush_Body(t *testing.T) {
	for body, want := range map[string]struct {
		force bool
		err   string
	}{
		"": {false, ""}, "  \n": {false, ""}, "{}": {false, ""}, `{"colour":"red"}`: {false, ""}, `{"force":false}`: {false, ""}, `{"force":true}`: {true, ""},
		"[]": {false, "the body must be a JSON object"}, "null": {false, "the body must be a JSON object"}, "nope": {false, "the body must be a JSON object"},
		`{"force":"yes"}`: {false, "force must be a boolean"}, `{"force":null}`: {false, "force must be a boolean"},
	} {
		force, err := parseBranchPushBody(strings.NewReader(body))
		if force != want.force || (err == nil) != (want.err == "") || err != nil && err.Error() != want.err {
			t.Errorf("%q: %v %v; want %v %q", body, force, err, want.force, want.err)
		}
	}
	v := newEnv(t)
	v.seedPushWorkspace(t, &Workspace{Slug: "ws", OwnerID: owner}, "fix")
	expectError(t, v.push(t, "ws", "fix", `{"force":"yes"}`, testutil.User(owner)), http.StatusBadRequest, "invalid request body: force must be a boolean", "")
	if len(*v.pushes) != 0 {
		t.Error("a bad body pushed")
	}
}

// TestBranchPush_Refusals: the branches hub owns are refused from the row and
// the patches table, an inactive or unready workspace, a busy one, and a
// failure of hub's own answer 500 with no event; a rebuild does not block.
func TestBranchPush_Refusals(t *testing.T) {
	v := newEnv(t)
	logs := captureLog(t)
	v.seedPushWorkspace(t, &Workspace{Slug: "cp", OwnerID: owner, WorkspaceMode: "carry_patch", UpstreamURL: strPtr("https://u/p"), IntegrationBranch: strPtr("integration")}, "integration", "feature/a", "feature/gone", "feat")
	v.seedPatch(t, "cp", "feature/a", 1)
	testutil.Exec(t, v.db, `INSERT INTO patches (id, workspace_slug, branch_name, position, status, added_at, updated_at) VALUES ('d', 'cp', 'feature/gone', 2, 'deleted', '', '')`)
	v.seedPushWorkspace(t, &Workspace{Slug: "cp-default", OwnerID: owner, WorkspaceMode: "carry_patch", UpstreamURL: strPtr("https://u/p")}, "deploy")
	v.seedPushWorkspace(t, &Workspace{Slug: "col", OwnerID: owner, Branch: strPtr("dev")}, "dev", "feat")
	v.seed(t, &Workspace{Slug: "archived", OwnerID: owner, Status: "archived"})
	v.seed(t, &Workspace{Slug: "pending", OwnerID: owner, Status: "pending"})
	v.seedPushWorkspace(t, &Workspace{Slug: "busy", OwnerID: owner}, "feat")
	v.seedPushWorkspace(t, &Workspace{Slug: "rebuilding", OwnerID: owner}, "feat")
	v.seed(t, &Workspace{Slug: "no-trunk", OwnerID: owner, Status: "ready"})
	v.seedPushWorkspace(t, &Workspace{Slug: "no-origin", OwnerID: owner}, "feat")
	gitRun(t, v.trunk("no-origin"), "remote", "remove", "origin")
	v.seedPushWorkspace(t, &Workspace{Slug: "headless", OwnerID: owner}, "feat")
	os.WriteFile(filepath.Join(v.trunk("headless"), ".git", "HEAD"), []byte("ref: refs/heads/missing\n"), 0o644)
	unlock, _ := wslock.TryLock("busy")
	defer unlock()
	end, _ := wslock.BeginRebuild("rebuilding")
	defer end()
	o := testutil.User(owner)
	for _, c := range []struct {
		slug, name, body string
		code             int
		msg, typ         string
	}{
		{"cp", "integration", "", 400, `integration_branch: hub builds "integration"`, "integration_branch"},
		{"cp-default", "deploy", "", 400, `integration_branch: hub builds "deploy"`, "integration_branch"},
		{"cp", "feature/a", "", 400, `patch_branch: "feature/a" is a registered patch`, "patch_branch"},
		{"col", "dev", `{"force":true}`, 400, `protected_branch: "dev" is the workspace's own branch`, "protected_branch"},
		{"archived", "feat", "", 400, "workspace is not ready", ""},
		{"pending", "feat", "", 400, "workspace is not ready", ""},
		{"busy", "feat", "", 409, "another operation is running", "workspace_busy"},
		{"no-trunk", "feat", "", 500, "internal server error", ""},
		{"no-origin", "feat", "", 500, "internal server error", ""},
		{"headless", "feat", `{"force":true}`, 500, "internal server error", ""},
	} {
		expectError(t, v.push(t, c.slug, url.PathEscape(c.name), c.body, o), c.code, c.msg, c.typ)
	}
	// Only the trunk without origin reached the shared push, which found no
	// origin: hub's own failure, not a push attempt.
	if len(*v.pushes) != 1 || len(v.pushEvents()) != 0 || strings.Contains(logs.String(), `"error_type"`) {
		t.Errorf("a refusal pushed, emitted or logged a push attempt: %d pushes, %d events, logs %s", len(*v.pushes), len(v.pushEvents()), logs)
	}
	for _, slug := range []string{"no-trunk", "no-origin", "headless"} {
		if unlock, ok := wslock.TryLock(slug); !ok {
			t.Errorf("the lock of %s is still held", slug)
		} else {
			unlock()
		}
	}
	// What is allowed: a deleted patch's branch, the own branch without force,
	// a forced push of another branch of the column workspace, and a push
	// while a rebuild is in progress.
	pushed(t, v.push(t, "cp", url.PathEscape("feature/gone"), "", o))
	pushed(t, v.push(t, "cp", "feat", `{"force":true}`, o))
	pushed(t, v.push(t, "col", "dev", "", o))
	pushed(t, v.push(t, "col", "feat", `{"force":true}`, o))
	pushed(t, v.push(t, "rebuilding", "feat", "", o))
	// A failing patches lookup is hub's own failure.
	testutil.Exec(t, v.db, `DROP TABLE patches`)
	expectError(t, v.push(t, "cp", "feat", "", o), http.StatusInternalServerError, "internal server error", "")
}

// TestBranchPush_Credential: the push uses the workspace's stored credential;
// a secrets store that fails answers 500 before anything is sent; no answer
// or log holds the secret.
func TestBranchPush_Credential(t *testing.T) {
	v := newEnv(t)
	logs := captureLog(t)
	var seen []transport.AuthMethod
	stubPush(t, func(_ context.Context, auth transport.AuthMethod) error {
		seen = append(seen, auth)
		if auth != nil {
			return fmt.Errorf("%w: rejected tok-12345", transport.ErrAuthenticationRequired)
		}
		return nil
	})
	v.seedPushWorkspace(t, &Workspace{Slug: "pat", OwnerID: owner}, "feat")
	v.seedPushWorkspace(t, &Workspace{Slug: "basic", OwnerID: owner}, "feat")
	v.seedPushWorkspace(t, &Workspace{Slug: "none", OwnerID: owner}, "feat")
	storeSecrets(t, v.db, "pat", "GIT_PAT", "tok-12345")
	storeSecrets(t, v.db, "basic", "GIT_USERNAME", "bob", "GIT_PASSWORD", "tok-12345")
	expectError(t, v.push(t, "pat", "feat", "", testutil.User(owner)), http.StatusBadGateway, "forge_unauthenticated: origin rejected the workspace's GIT_PAT", "forge_unauthenticated")
	expectError(t, v.push(t, "basic", "feat", "", testutil.User(owner)), http.StatusBadGateway, "origin rejected the workspace's GIT_USERNAME/GIT_PASSWORD", "forge_unauthenticated")
	pushed(t, v.push(t, "none", "feat", "", testutil.User(owner)))
	if len(seen) != 3 || *seen[0].(*githttp.BasicAuth) != (githttp.BasicAuth{Username: "x-token-auth", Password: "tok-12345"}) || *seen[1].(*githttp.BasicAuth) != (githttp.BasicAuth{Username: "bob", Password: "tok-12345"}) || seen[2] != nil {
		t.Errorf("credentials %+v", seen)
	}
	for _, out := range []string{logs.String()} {
		if strings.Contains(out, "tok-12345") || strings.Contains(out, "bob") {
			t.Errorf("the logs hold the credential: %s", out)
		}
	}
	if evs := v.pushEvents(); len(evs) != 3 || evs[0].Metadata["error_type"] != "forge_unauthenticated" || !strings.Contains(logs.String(), "push to origin failed") {
		t.Errorf("events %+v, logs %s", evs, logs)
	}
	testutil.Exec(t, v.db, `DROP TABLE secrets`)
	expectError(t, v.push(t, "none", "feat", "", testutil.User(owner)), http.StatusInternalServerError, "internal server error", "")
	if len(seen) != 3 {
		t.Error("a push was attempted without a credential decision")
	}
}

// TestBranchPush_ForgeAnswers: each failure of the push to origin is mapped to
// its status and error_type, in the answer and in the event; a rejected ref
// carries the forge's reason, scrubbed.
func TestBranchPush_ForgeAnswers(t *testing.T) {
	v := newEnv(t)
	swap(t, &branchPushDeadline, 200*time.Millisecond)
	bare := v.seedPushWorkspace(t, &Workspace{Slug: "ws", OwnerID: owner}, "feat")
	outcomes := []struct {
		err       error
		code      int
		errorType string
		msg       string
	}{
		{errors.New("non-fast-forward update: refs/heads/feat"), 409, "non_fast_forward", "non_fast_forward: origin's feat"},
		{fmt.Errorf("%w: not found", transport.ErrRepositoryNotFound), 404, "forge_not_found", "origin has no repository"},
		{errors.New("command error on refs/heads/feat: pre-receive hook declined"), 422, "forge_rejected", "forge_rejected: origin refused the update: pre-receive hook declined"},
		{transport.ErrAuthenticationRequired, 502, "forge_unauthenticated", "the workspace has no stored git credential"},
		{transport.ErrAuthorizationFailed, 502, "forge_forbidden", "does not let the workspace's git credential push"},
		{&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, 502, "forge_unreachable", "could not be reached"},
		{errors.New("x509: certificate signed by unknown authority"), 502, "forge_unreachable", "could not be reached"},
		{errors.New("unexpected EOF"), 502, "forge_error", "the push to origin failed"},
		{context.DeadlineExceeded, 504, "forge_timeout", "did not finish the push within 200ms"},
	}
	for i, c := range outcomes {
		stubPush(t, func(context.Context, transport.AuthMethod) error { return c.err })
		expectError(t, v.push(t, "ws", "feat", "", testutil.User(owner)), c.code, c.msg, c.errorType)
		if evs := v.pushEvents(); len(evs) != i+1 || evs[i].Metadata["error_type"] != c.errorType || evs[i].Metadata["ok"] != false {
			t.Errorf("%s: events %+v", c.errorType, evs)
		}
	}
	// A push that outlives the deadline is cut off.
	stubPush(t, func(ctx context.Context, _ transport.AuthMethod) error { <-ctx.Done(); return ctx.Err() })
	expectError(t, v.push(t, "ws", "feat", "", testutil.User(owner)), http.StatusGatewayTimeout, "forge_timeout", "forge_timeout")

	// Through the real transport: a hook that declines, with userinfo in its
	// reason scrubbed; a forge that answers 401, 403 and 404; a port nobody
	// listens on.
	swap(t, &pushToOriginFn, pushToOrigin)
	hook := "#!/bin/sh\necho 'declined: protected ref, see https://user:pw@forge.example/rules' >&2\nexit 1\n"
	os.WriteFile(filepath.Join(bare, "hooks", "pre-receive"), []byte(hook), 0o755)
	rec := v.push(t, "ws", "feat", "", testutil.User(owner))
	expectError(t, rec, http.StatusUnprocessableEntity, "https://forge.example/rules", "forge_rejected")
	if strings.Contains(rec.Body.String(), "user:pw@") || bareRef(t, bare, "refs/heads/feat") != "" {
		t.Errorf("answer %s; origin's feat %s", rec.Body, bareRef(t, bare, "refs/heads/feat"))
	}
	status := http.StatusUnauthorized
	forge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
	defer forge.Close()
	testutil.Exec(t, v.db, `UPDATE workspaces SET git_url = ? WHERE slug = 'ws'`, forge.URL+"/r.git")
	expectError(t, v.push(t, "ws", "feat", "", testutil.User(owner)), http.StatusBadGateway, "forge_unauthenticated", "forge_unauthenticated")
	status = http.StatusForbidden
	expectError(t, v.push(t, "ws", "feat", "", testutil.User(owner)), http.StatusBadGateway, "forge_forbidden", "forge_forbidden")
	status = http.StatusNotFound
	expectError(t, v.push(t, "ws", "feat", "", testutil.User(owner)), http.StatusNotFound, "forge_not_found", "forge_not_found")
	testutil.Exec(t, v.db, `UPDATE workspaces SET git_url = 'http://127.0.0.1:1/r.git' WHERE slug = 'ws'`)
	expectError(t, v.push(t, "ws", "feat", "", testutil.User(owner)), http.StatusBadGateway, "forge_unreachable", "forge_unreachable")
	if evs := v.pushEvents(); evs[len(evs)-1].Metadata["remote_url"] != "http://127.0.0.1:1/r.git" {
		t.Errorf("remote_url %v", evs[len(evs)-1].Metadata["remote_url"])
	}
}

// TestBranchPush_Lock: the lock is held while the push runs and released
// after it, the caller's disconnect does not stop the push or its event, and
// a HEAD moved during the push gets its working tree restored.
func TestBranchPush_Lock(t *testing.T) {
	v := newEnv(t)
	logs := captureLog(t)
	bare := v.seedPushWorkspace(t, &Workspace{Slug: "ws", OwnerID: owner}, "feat")
	trunk := v.trunk("ws")
	gitRun(t, trunk, "checkout", "-q", "-b", "side")
	side := commit(t, trunk, "README.md", "# moved by a push to hub")
	gitRun(t, trunk, "checkout", "-q", "main")

	started := make(chan struct{})
	gate := make(chan struct{})
	orig := pushToOrigin
	stubPush(t, func(ctx context.Context, auth transport.AuthMethod) error {
		close(started)
		<-gate
		// A push to hub's git server moved the checked-out branch meanwhile.
		gitRun(t, trunk, "update-ref", "refs/heads/main", side)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return orig(ctx, trunk, bare, auth, []config.RefSpec{"refs/heads/feat:refs/heads/feat"}, false, nil)
	})
	ctx, cancel := context.WithCancel(context.Background())
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		v.e.ServeHTTP(rec, request(ctx, http.MethodPost, "/api/v1/workspaces/ws/branches/feat/push", testutil.User(owner)))
		close(done)
	}()
	<-started
	if unlock, ok := wslock.TryLock("ws"); ok {
		unlock()
		t.Error("the lock was free while the push ran")
	}
	cancel()
	close(gate)
	<-done
	if a := pushed(t, rec); a.Result != "pushed" || bareRef(t, bare, "refs/heads/feat") != a.SHA {
		t.Errorf("after the disconnect: %+v", a)
	}
	if unlock, ok := wslock.TryLock("ws"); !ok {
		t.Error("the lock is still held")
	} else {
		unlock()
	}
	if evs := v.pushEvents(); len(evs) != 1 || evs[0].Metadata["ok"] != true {
		t.Errorf("events %+v", evs)
	}
	if content, _ := os.ReadFile(filepath.Join(trunk, "README.md")); string(content) != "# moved by a push to hub" || gitRun(t, trunk, "status", "--porcelain") != "" {
		t.Errorf("the working tree was not restored to the moved HEAD: %q", content)
	}
	// A failing restore is one warning without the secret; an unmoved HEAD
	// leaves a local change alone.
	swap(t, &pushToOriginFn, pushToOrigin)
	storeSecrets(t, v.db, "ws", "GIT_PAT", "tok-12345")
	swap(t, &resetTrunkWorktreeFn, func(*git.Repository, plumbing.Hash) error { return errors.New("reset failed near tok-12345") })
	gitRun(t, trunk, "update-ref", "refs/heads/main", side)
	moved := gitRun(t, trunk, "rev-parse", "main")
	gitRun(t, trunk, "update-ref", "refs/heads/main", gitRun(t, trunk, "rev-parse", "main~1"))
	swap(t, &pushToOriginFn, func(ctx context.Context, p, u string, a transport.AuthMethod, r []config.RefSpec, f bool, w io.Writer) error {
		gitRun(t, trunk, "update-ref", "refs/heads/main", moved)
		return pushToOrigin(ctx, p, u, a, r, f, w)
	})
	pushed(t, v.push(t, "ws", "feat", "", testutil.User(owner)))
	if out := logs.String(); strings.Count(out, "restoring the trunk's worktree failed") != 1 || strings.Contains(out, "tok-12345") {
		t.Errorf("logs %s", out)
	}
	swap(t, &pushToOriginFn, pushToOrigin)
	os.WriteFile(filepath.Join(trunk, "README.md"), []byte("local change"), 0o644)
	pushed(t, v.push(t, "ws", "feat", "", testutil.User(owner)))
	if content, _ := os.ReadFile(filepath.Join(trunk, "README.md")); string(content) != "local change" {
		t.Error("an unmoved HEAD's working tree was reset")
	}
}

// TestBranchPush_Access: through the real stack a PAT bound to the workspace
// pushes, and one bound elsewhere is refused by the guard.
func TestBranchPush_Access(t *testing.T) {
	h := newHub(t)
	swap(t, &defaultWorkspaceRoot, t.TempDir())
	swap(t, &defaultAuditEmitter, nil)
	trunk := filepath.Join(defaultWorkspaceRoot, "ws", "trunk")
	bare := newTrunk(t, trunk, "fix")
	insertWorkspace(h.db, &Workspace{Slug: "ws", GitURL: bare, OwnerID: "u1", Status: "ready"})
	insertWorkspace(h.db, &Workspace{Slug: "other", GitURL: bare, OwnerID: "u1", Status: "ready"})
	own := h.boundPAT(t, "ws", "workspaces:write")
	elsewhere := h.boundPAT(t, "other", "workspaces:write")
	if a := pushed(t, h.do(http.MethodPost, "/api/v1/workspaces/ws/branches/fix/push", own, "")); a.Result != "pushed" || bareRef(t, bare, "refs/heads/fix") != a.SHA {
		t.Errorf("bound PAT: %+v", a)
	}
	if rec := h.do(http.MethodPost, "/api/v1/workspaces/ws/branches/fix/push", elsewhere, ""); rec.Code != http.StatusNotFound {
		t.Errorf("PAT bound elsewhere: %d %s", rec.Code, rec.Body)
	}
}
