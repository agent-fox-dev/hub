package workspace

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/hub/internal/wslock"
)

// guardTestSetup points the workspace handlers at a temp root, stubs the git
// helpers archive and reclone use, and seeds a ready workspace with a trunk
// directory holding a marker file. It returns the trunk directory.
func guardTestSetup(t *testing.T, env *testEnv, slug string) string {
	t.Helper()
	wsRoot := t.TempDir()

	oldRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = wsRoot
	t.Cleanup(func() { defaultWorkspaceRoot = oldRoot })

	oldPush := archiveOpenAndPushFn
	archiveOpenAndPushFn = func(string, string, transport.AuthMethod) error { return nil }
	t.Cleanup(func() { archiveOpenAndPushFn = oldPush })

	oldHead := archiveHeadFn
	archiveHeadFn = func(string) (string, error) {
		return "abcdef1234567890abcdef1234567890abcdef12", nil
	}
	t.Cleanup(func() { archiveHeadFn = oldHead })

	env.seedWorkspace(t, &Workspace{
		Slug:        slug,
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	trunkDir := filepath.Join(wsRoot, slug, "trunk")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		t.Fatalf("create trunk dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(trunkDir, "marker"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	return trunkDir
}

func assertWorkspaceBusy(t *testing.T, rec interface {
	Bytes() []byte
}, code int) {
	t.Helper()
	if code != http.StatusConflict {
		t.Fatalf("status = %d; want 409; body: %s", code, string(rec.Bytes()))
	}
	var body struct {
		Error struct {
			ErrorType string `json:"error_type"`
		} `json:"error"`
		ErrorType string `json:"error_type"`
	}
	if err := json.Unmarshal(rec.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	got := body.ErrorType
	if got == "" {
		got = body.Error.ErrorType
	}
	if got != "workspace_busy" {
		t.Errorf("error_type = %q; want workspace_busy; body: %s", got, string(rec.Bytes()))
	}
}

func assertLockFree(t *testing.T, slug string) {
	t.Helper()
	unlock, ok := wslock.TryLock(slug)
	if !ok {
		t.Fatalf("workspace lock for %q is still held", slug)
	}
	unlock()
}

// TS-01-35: archive of a ready workspace answers 409 workspace_busy while the
// rebuild guard is set and the lock is free.
// Requirement: 01-REQ-5.5
func TestArchive_RebuildGuardSet_Returns409_TS_01_35(t *testing.T) {
	env := newTestEnv(t)
	const slug = "guard-archive"
	trunkDir := guardTestSetup(t, env, slug)

	end, ok := wslock.BeginRebuild(slug)
	if !ok {
		t.Fatal("BeginRebuild failed")
	}
	defer end()

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/archive", "", adminAuth())
	assertWorkspaceBusy(t, rec.Body, rec.Code)

	var status string
	if err := env.db.QueryRow(`SELECT status FROM workspaces WHERE slug = ?`, slug).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status == "archived" {
		t.Error("workspace must not be archived while a rebuild is active")
	}
	if _, err := os.Stat(filepath.Join(trunkDir, "marker")); err != nil {
		t.Errorf("trunk must be untouched: %v", err)
	}
	assertLockFree(t, slug)
}

// TS-01-36: reclone answers 409 workspace_busy while the guard is set and
// leaves the clone directory untouched.
// Requirement: 01-REQ-5.6
func TestReclone_RebuildGuardSet_Returns409_TS_01_36(t *testing.T) {
	env := newTestEnv(t)
	const slug = "guard-reclone"
	trunkDir := guardTestSetup(t, env, slug)

	end, ok := wslock.BeginRebuild(slug)
	if !ok {
		t.Fatal("BeginRebuild failed")
	}
	defer end()

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/reclone", "", adminAuth())
	assertWorkspaceBusy(t, rec.Body, rec.Code)

	if _, err := os.Stat(filepath.Join(trunkDir, "marker")); err != nil {
		t.Errorf("clone directory must be untouched: %v", err)
	}
	var cloneStatus string
	if err := env.db.QueryRow(`SELECT clone_status FROM workspaces WHERE slug = ?`, slug).Scan(&cloneStatus); err != nil {
		t.Fatalf("query clone_status: %v", err)
	}
	if cloneStatus != "ready" {
		t.Errorf("clone_status = %q; want ready", cloneStatus)
	}
	assertLockFree(t, slug)
}

// TS-01-37: archive and reclone behave as before once the guard has ended.
// Requirement: 01-REQ-5.7
func TestArchiveAndReclone_GuardEnded_BehaveAsBefore_TS_01_37(t *testing.T) {
	env := newTestEnv(t)
	guardTestSetup(t, env, "guard-ended-a")
	// A second workspace under the same (already redirected) root.
	env.seedWorkspace(t, &Workspace{
		Slug:        "guard-ended-b",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})
	if err := os.MkdirAll(filepath.Join(defaultWorkspaceRoot, "guard-ended-b", "trunk"), 0o755); err != nil {
		t.Fatalf("create trunk dir: %v", err)
	}

	endA, ok := wslock.BeginRebuild("guard-ended-a")
	if !ok {
		t.Fatal("BeginRebuild(a) failed")
	}
	endA()
	endB, ok := wslock.BeginRebuild("guard-ended-b")
	if !ok {
		t.Fatal("BeginRebuild(b) failed")
	}
	endB()

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/guard-ended-a/archive", "", adminAuth())
	if rec.Code != http.StatusOK {
		t.Fatalf("archive status = %d; want 200; body: %s", rec.Code, rec.Body.String())
	}
	var status string
	if err := env.db.QueryRow(`SELECT status FROM workspaces WHERE slug = ?`, "guard-ended-a").Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "archived" {
		t.Errorf("status = %q; want archived", status)
	}

	rec = env.doRequest(t, http.MethodPost, "/api/v1/workspaces/guard-ended-b/reclone", "", adminAuth())
	if rec.Code != http.StatusOK {
		t.Fatalf("reclone status = %d; want 200; body: %s", rec.Code, rec.Body.String())
	}
}

// TS-01-38 (workspace half): the standard sync handler ignores the guard when
// the lock is free.
// Requirement: 01-REQ-5.8
func TestSync_RebuildGuardSet_NotRejected_TS_01_38(t *testing.T) {
	env := newTestEnv(t)
	const slug = "guard-sync"
	guardTestSetup(t, env, slug)
	stubSyncUpToDate(t, "abc1234567890abcdef1234567890abcdef123456")
	if _, err := env.db.Exec(
		`UPDATE workspaces SET sync_mode = 'pull_only', sync_status = 'idle' WHERE slug = ?`, slug); err != nil {
		t.Fatalf("set sync fields: %v", err)
	}

	end, ok := wslock.BeginRebuild(slug)
	if !ok {
		t.Fatal("BeginRebuild failed")
	}
	defer end()

	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/sync", "", adminAuth())
	if rec.Code != http.StatusOK {
		t.Fatalf("sync status = %d; want 200 (guard must not reject sync); body: %s", rec.Code, rec.Body.String())
	}
}
