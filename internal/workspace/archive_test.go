package workspace

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/testutil"
	"github.com/agent-fox-dev/hub/internal/wslock"
)

func archive(t *testing.T, v *env, slug string) *httptestRecorder {
	t.Helper()
	return v.do(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/archive", "", testutil.User(owner))
}

// markerDir makes the workspace directory of slug with a marker file in its
// trunk and returns the marker's path.
func markerDir(t *testing.T, v *env, slug string) string {
	t.Helper()
	marker := filepath.Join(v.trunk(slug), "marker")
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return marker
}

// TestArchive_ReadyClone: archiving a ready workspace pushes every local
// branch to origin with the stored credential, records HEAD, removes the
// directory, force-closes its sessions and records the event.
func TestArchive_ReadyClone(t *testing.T) {
	v := newEnv(t)
	v.ready(t, "ws", owner)
	marker := markerDir(t, v, "ws")
	storeSecrets(t, v.db, "ws", "GIT_PAT", "tok")
	var pushed []string
	swap(t, &archiveOpenAndPushFn, func(repoPath, gitURL string, auth transport.AuthMethod) error {
		pushed = append(pushed, repoPath+" "+gitURL+" "+auth.(*githttp.BasicAuth).Password)
		return ErrAlreadyUpToDate
	})
	store := &fakeAuditStore{results: []audit.ForceCloseResult{{SessionID: "s1"}, {SessionID: "s2"}}}
	swap(t, &defaultAuditStore, audit.Store(store))

	rec := archive(t, v, "ws")
	expectStatus(t, rec, http.StatusOK)
	got := testutil.Object(t, rec)
	if got["status"] != "archived" || got["clone_status"] != "archived" || got["head_sha"] != headSHA || got["clone_error"] != nil {
		t.Errorf("response %v", got)
	}
	if want := v.trunk("ws") + " https://github.com/org/ws tok"; len(pushed) != 1 || pushed[0] != want {
		t.Errorf("pushed %v; want %q", pushed, want)
	}
	if _, err := os.Stat(filepath.Dir(filepath.Dir(marker))); !os.IsNotExist(err) {
		t.Error("the workspace directory survived the archive")
	}
	if len(store.calls) != 1 || store.calls[0] != "ws|workspace archived" {
		t.Errorf("force-close calls %v", store.calls)
	}
	if evs := v.events.ofType("hub.session.force_closed"); len(evs) != 2 || evs[0].ResourceID != "s1" || evs[0].Metadata["reason"] != "workspace archived" || evs[0].ActorType != "system" {
		t.Errorf("force-close events %+v", evs)
	}
	if ev := v.events.one(t, "hub.workspace.archive"); ev.Metadata["head_sha"] != headSHA || ev.Workspace != "ws" || ev.ActorID != owner {
		t.Errorf("event %+v", ev)
	}
}

// TestArchive_KeepsCloneWhenPushFails: a failed push, or a credential that
// cannot be resolved, archives the workspace but keeps the local clone for
// the reactivation.
func TestArchive_KeepsCloneWhenPushFails(t *testing.T) {
	v := newEnv(t)
	logs := captureLog(t)
	v.ready(t, "push-fails", owner)
	marker := markerDir(t, v, "push-fails")
	swap(t, &archiveOpenAndPushFn, func(string, string, transport.AuthMethod) error { return errors.New("remote refused") })
	expectStatus(t, archive(t, v, "push-fails"), http.StatusOK)
	if _, err := os.Stat(marker); err != nil || v.column(t, "push-fails", "status") != "archived" || !strings.Contains(logs.String(), "keeping local clone") {
		t.Errorf("marker %v, status %s, logs %s", err, v.column(t, "push-fails", "status"), logs)
	}

	v.ready(t, "no-creds", owner)
	marker = markerDir(t, v, "no-creds")
	testutil.Exec(t, v.db, `DROP TABLE secrets`)
	expectStatus(t, archive(t, v, "no-creds"), http.StatusOK)
	if _, err := os.Stat(marker); err != nil || v.column(t, "no-creds", "status") != "archived" {
		t.Errorf("marker %v, status %s", err, v.column(t, "no-creds", "status"))
	}
}

// TestArchive_HeadUnreadable: a HEAD that cannot be read aborts the archive
// and leaves the directory and the row alone.
func TestArchive_HeadUnreadable(t *testing.T) {
	v := newEnv(t)
	v.ready(t, "ws", owner)
	marker := markerDir(t, v, "ws")
	swap(t, &archiveHeadFn, func(string) (string, error) { return "", errors.New("reference not found") })
	expectError(t, archive(t, v, "ws"), http.StatusInternalServerError, "reference not found", "")
	if _, err := os.Stat(marker); err != nil || v.column(t, "ws", "status") != "active" {
		t.Error("the failed archive changed the workspace")
	}
}

// TestArchive_WithoutClone: a pending or failed clone is archived without a
// push and without a head_sha; its partial directory is removed.
func TestArchive_WithoutClone(t *testing.T) {
	v := newEnv(t)
	swap(t, &archiveOpenAndPushFn, func(string, string, transport.AuthMethod) error { t.Error("pushed"); return nil })
	for _, state := range []string{"pending", "failed"} {
		v.seed(t, &Workspace{Slug: state, OwnerID: owner, CloneStatus: state, CloneError: strPtr("boom")})
		marker := markerDir(t, v, state)
		rec := archive(t, v, state)
		expectStatus(t, rec, http.StatusOK)
		got := testutil.Object(t, rec)
		if got["status"] != "archived" || got["clone_status"] != "archived" || got["head_sha"] != nil || got["clone_error"] != nil {
			t.Errorf("%s: %v", state, got)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Errorf("%s: the directory survived", state)
		}
	}
	if evs := v.events.ofType("hub.workspace.archive"); len(evs) != 2 || evs[0].Metadata["head_sha"] != nil {
		t.Errorf("events %+v", evs)
	}
}

// TestArchive_Refusals: an archived workspace, a clone in progress, a busy
// workspace, an active rebuild and the callers without write access are
// refused and nothing changes.
func TestArchive_Refusals(t *testing.T) {
	v := newEnv(t)
	v.orgWorkspace(t, "team")
	v.seed(t, &Workspace{Slug: "done", OwnerID: owner, Status: "archived", CloneStatus: "archived"})
	v.seed(t, &Workspace{Slug: "cloning", OwnerID: owner, CloneStatus: "cloning"})
	v.ready(t, "busy", owner)
	v.ready(t, "rebuilding", owner)
	unlock, _ := wslock.TryLock("busy")
	defer unlock()
	end, _ := wslock.BeginRebuild("rebuilding")
	defer end()

	post := func(slug string, auth *testutilAuth) *httptestRecorder {
		return v.do(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/archive", "", auth)
	}
	expectError(t, post("done", testutil.User(owner)), http.StatusBadRequest, "already archived", "")
	expectError(t, post("cloning", testutil.User(owner)), http.StatusConflict, "clone in progress", "")
	expectError(t, post("busy", testutil.User(owner)), http.StatusConflict, "another operation is running", "workspace_busy")
	expectError(t, post("rebuilding", testutil.User(owner)), http.StatusConflict, "another operation is running", "workspace_busy")
	expectStatus(t, post("team", nil), http.StatusUnauthorized)
	expectError(t, post("team", testutil.PAT(owner, "workspaces:read")), http.StatusNotFound, "workspace not found", "")
	expectError(t, post("team", testutil.User(stranger)), http.StatusNotFound, "workspace not found", "")
	expectError(t, post("team", testutil.User(member)), http.StatusForbidden, "read-only access", "forbidden")
	expectError(t, post("nope", testutil.User(owner)), http.StatusNotFound, "workspace not found", "")
	for _, slug := range []string{"cloning", "busy", "rebuilding", "team"} {
		if v.column(t, slug, "status") != "active" {
			t.Errorf("%s was archived by a refused request", slug)
		}
	}
	if len(v.events.ofType("hub.workspace.archive")) != 0 {
		t.Error("a refused archive emitted an event")
	}
	// The lock is released after a refusal.
	if unlock, ok := wslock.TryLock("rebuilding"); !ok {
		t.Error("the lock of rebuilding is still held")
	} else {
		unlock()
	}
}

// TestArchive_StoreFailure: a database failure while archiving is a 500.
func TestArchive_StoreFailure(t *testing.T) {
	v := newEnv(t)
	v.ready(t, "ws", owner)
	testutil.Exec(t, v.db, `CREATE TRIGGER no_archive BEFORE UPDATE ON workspaces WHEN NEW.status = 'archived' BEGIN SELECT RAISE(FAIL, 'down'); END`)
	expectError(t, archive(t, v, "ws"), http.StatusInternalServerError, "failed to archive workspace", "")
}

// TestReactivate_Archived: reactivating an archived workspace makes it active
// with a pending clone, clears the clone error, enqueues the clone job and
// records the event; anything else is refused.
func TestReactivate_Archived(t *testing.T) {
	v := newEnv(t)
	jobs := captureQueue(t)
	v.orgWorkspace(t, "team")
	v.seed(t, &Workspace{Slug: "done", OwnerID: owner, Status: "archived", CloneStatus: "archived", Branch: strPtr("dev"), HeadSHA: strPtr(headSHA), CloneError: strPtr("old")})
	post := func(slug string, auth *testutilAuth) *httptestRecorder {
		return v.do(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/reactivate", "", auth)
	}
	expectError(t, post("team", testutil.User(owner)), http.StatusConflict, "not archived", "")
	expectStatus(t, post("done", nil), http.StatusUnauthorized)
	expectError(t, post("done", testutil.PAT(owner, "workspaces:read")), http.StatusNotFound, "workspace not found", "")
	expectError(t, post("done", testutil.User(stranger)), http.StatusNotFound, "workspace not found", "")
	expectError(t, post("nope", testutil.User(owner)), http.StatusNotFound, "workspace not found", "")
	if len(jobs()) != 0 {
		t.Error("a refused reactivation enqueued a job")
	}

	rec := post("done", testutil.PAT(owner, "workspaces:write"))
	expectStatus(t, rec, http.StatusOK)
	got := testutil.Object(t, rec)
	if got["status"] != "active" || got["clone_status"] != "pending" || got["clone_error"] != nil || got["head_sha"] != headSHA {
		t.Errorf("response %v", got)
	}
	if j := jobs(); len(j) != 1 || j[0].Slug != "done" || *j[0].Branch != "dev" {
		t.Errorf("jobs %+v; want done's clone job on dev", j)
	}
	if ev := v.events.one(t, "hub.workspace.reactivate"); ev.Workspace != "done" || ev.Action != "reactivate" || ev.ActorType != "pat" {
		t.Errorf("event %+v", ev)
	}

	// A database failure is a 500 without a job.
	v.seed(t, &Workspace{Slug: "stuck", OwnerID: owner, Status: "archived", CloneStatus: "archived"})
	testutil.Exec(t, v.db, `CREATE TRIGGER no_reactivate BEFORE UPDATE ON workspaces WHEN NEW.status = 'active' BEGIN SELECT RAISE(FAIL, 'down'); END`)
	expectError(t, post("stuck", testutil.User(owner)), http.StatusInternalServerError, "failed to reactivate", "")
	if len(jobs()) != 0 {
		t.Error("a failed reactivation enqueued a job")
	}
}
