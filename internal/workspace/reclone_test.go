package workspace

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/wslock"
)

func reclone(t *testing.T, v *env, slug string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	t.Helper()
	return v.do(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/reclone", "", auth)
}

// TestReclone_ReadyClone: a reclone pushes the local branches with the stored
// credential, removes the clone, resets the clone and sync state while the
// workspace stays active, and enqueues a clone job.
func TestReclone_ReadyClone(t *testing.T) {
	v := newEnv(t)
	jobs := captureQueue(t)
	v.seed(t, &Workspace{Slug: "ws", OwnerID: owner, CloneStatus: "ready", Branch: strPtr("dev"), HeadSHA: strPtr(headSHA),
		SyncStatus: "error", SyncError: strPtr("diverged"), UpstreamHeadSHA: strPtr(upstreamSHA)})
	marker := markerDir(t, v, "ws")
	storeSecrets(t, v.db, "ws", "GIT_PAT", "tok")
	var pushed []string
	swap(t, &archiveOpenAndPushFn, func(repoPath, gitURL string, auth transport.AuthMethod) error {
		pushed = append(pushed, repoPath+" "+gitURL+" "+auth.(*githttp.BasicAuth).Password)
		return nil
	})
	rec := reclone(t, v, "ws", testutil.PAT(owner, "workspaces:write"))
	expectStatus(t, rec, http.StatusOK)
	got := testutil.Object(t, rec)
	if got["status"] != "active" || got["clone_status"] != "pending" || got["sync_status"] != "idle" || got["sync_error"] != nil || got["upstream_head_sha"] != nil {
		t.Errorf("response %v", got)
	}
	if want := v.trunk("ws") + " https://github.com/org/ws tok"; len(pushed) != 1 || pushed[0] != want {
		t.Errorf("pushed %v; want %q", pushed, want)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("the clone survived")
	}
	if j := jobs(); len(j) != 1 || j[0].Slug != "ws" || *j[0].Branch != "dev" {
		t.Errorf("jobs %+v", j)
	}
	// A failed push and an unreadable HEAD are logged and the reclone goes on.
	logs := captureLog(t)
	v.ready(t, "push-fails", owner)
	swap(t, &archiveOpenAndPushFn, func(string, string, transport.AuthMethod) error { return errors.New("refused") })
	swap(t, &archiveHeadFn, func(string) (string, error) { return "", errors.New("no HEAD") })
	expectStatus(t, reclone(t, v, "push-fails", testutil.User(owner)), http.StatusOK)
	if out := logs.String(); !strings.Contains(out, "archive push failed") || !strings.Contains(out, "failed to read HEAD SHA") {
		t.Errorf("logs %s", out)
	}
	if v.column(t, "push-fails", "clone_status") != "pending" || len(jobs()) != 1 {
		t.Error("the reclone did not go on after the failed push")
	}
}

// TestReclone_Refusals: a clone in progress, a busy workspace, an active
// rebuild and callers without write access are refused; the admin is allowed.
func TestReclone_Refusals(t *testing.T) {
	v := newEnv(t)
	jobs := captureQueue(t)
	v.orgWorkspace(t, "team")
	v.seed(t, &Workspace{Slug: "pending", OwnerID: owner, CloneStatus: "pending"})
	v.seed(t, &Workspace{Slug: "cloning", OwnerID: owner, CloneStatus: "cloning"})
	v.ready(t, "busy", owner)
	v.ready(t, "rebuilding", owner)
	unlock, _ := wslock.TryLock("busy")
	defer unlock()
	end, _ := wslock.BeginRebuild("rebuilding")
	defer end()
	marker := markerDir(t, v, "rebuilding")
	o := testutil.User(owner)
	for _, c := range []struct {
		name, slug string
		auth       *apikit.AuthInfo
		code       int
		msg, typ   string
	}{
		{"pending", "pending", o, http.StatusConflict, "clone operation already in progress", ""},
		{"cloning", "cloning", o, http.StatusConflict, "clone_status is 'cloning'", ""},
		{"busy", "busy", o, http.StatusConflict, "another operation is running", "workspace_busy"},
		{"rebuilding", "rebuilding", o, http.StatusConflict, "another operation is running", "workspace_busy"},
		{"unauthenticated", "team", nil, http.StatusUnauthorized, "authentication required", ""},
		{"read PAT", "team", testutil.PAT(owner, "workspaces:read"), http.StatusForbidden, "workspaces:write scope", ""},
		{"member", "team", testutil.User(member), http.StatusForbidden, "read-only access", "forbidden"},
		{"stranger", "team", testutil.User(stranger), http.StatusNotFound, "workspace not found", ""},
		{"unknown", "nope", o, http.StatusNotFound, "workspace not found", ""},
	} {
		expectError(t, reclone(t, v, c.slug, c.auth), c.code, c.msg, c.typ)
	}
	if _, err := os.Stat(marker); err != nil || len(jobs()) != 0 || v.column(t, "rebuilding", "clone_status") != "ready" {
		t.Error("a refused reclone changed the workspace")
	}
	if unlock, ok := wslock.TryLock("rebuilding"); !ok {
		t.Error("the lock is still held")
	} else {
		unlock()
	}
	expectStatus(t, reclone(t, v, "team", testutil.Admin()), http.StatusOK)
	// A failing update is a 500 without a job.
	v.ready(t, "down", owner)
	testutil.Exec(t, v.db, `CREATE TRIGGER no_reclone BEFORE UPDATE ON workspaces WHEN NEW.clone_status = 'pending' BEGIN SELECT RAISE(FAIL, 'down'); END`)
	expectError(t, reclone(t, v, "down", o), http.StatusInternalServerError, "failed to update workspace state", "")
	if j := jobs(); len(j) != 1 || j[0].Slug != "team" {
		t.Errorf("jobs %+v; want team's only", j)
	}
}
