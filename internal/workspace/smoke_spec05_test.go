package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
)

// TS-05-SMOKE-1: Happy path: workspace is created via POST /api/v1/workspaces,
// returns HTTP 201 with clone_status='pending', and the job queue worker clones
// the repository and sets clone_status='ready' with a valid head_sha.
//
// Real components: workspace create handler, job queue, job queue worker,
// SQLite database, WORKSPACE_ROOT filesystem.
// Mock: cloneFn (simulates PlainCloneContext).
//
// Validates: 05-REQ-5.1, 05-PATH-1
func TestSmoke05_CreateAndCloneReady(t *testing.T) {
	env := newTestEnv(t)
	wsRoot := t.TempDir()

	// Set up a real job queue with 1 worker and a mock clone function.
	const fakeSHA = "aabbccddee00112233445566778899aabbccddee"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	origCloneFn := cloneFn
	cloneFn = func(_ context.Context, path, url string, depth int, singleBranch bool, refName string, _ transport.AuthMethod) (string, error) {
		// Create a fake trunk dir to simulate go-git behaviour.
		_ = os.MkdirAll(path, 0o755)
		return fakeSHA, nil
	}
	defer func() { cloneFn = origCloneFn }()

	origRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = wsRoot
	defer func() { defaultWorkspaceRoot = origRoot }()

	origQueue := defaultQueue
	defaultQueue = NewJobQueue(ctx, env.db, wsRoot, 1)
	defer func() { defaultQueue = origQueue }()

	auth := userAuth("alice-user-id")

	// POST /api/v1/workspaces — must return 201 with clone_status='pending'.
	body := `{"slug":"smoke-ws","git_url":"https://github.com/example/repo.git"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d; want %d\nbody: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	var createResp spec05WorkspaceJSON
	if err := json.NewDecoder(rec.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if createResp.CloneStatus == nil || *createResp.CloneStatus != "pending" {
		t.Errorf("create clone_status = %v; want 'pending'", createResp.CloneStatus)
	}
	if createResp.HeadSHA != nil {
		t.Errorf("create head_sha = %v; want nil", createResp.HeadSHA)
	}
	if createResp.CloneError != nil {
		t.Errorf("create clone_error = %v; want nil", createResp.CloneError)
	}

	// Wait for the worker to process the clone job.
	waitForCloneStatus(t, env.db, "smoke-ws", "ready", 5*time.Second)

	// GET /api/v1/workspaces/:slug — verify clone_status='ready' and valid head_sha.
	rec = env.doRequest(t, http.MethodGet, "/api/v1/workspaces/smoke-ws", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var getResp spec05WorkspaceJSON
	if err := json.NewDecoder(rec.Body).Decode(&getResp); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if getResp.CloneStatus == nil || *getResp.CloneStatus != "ready" {
		t.Errorf("get clone_status = %v; want 'ready'", getResp.CloneStatus)
	}
	if getResp.HeadSHA == nil || *getResp.HeadSHA != fakeSHA {
		t.Errorf("get head_sha = %v; want %q", getResp.HeadSHA, fakeSHA)
	}
	if getResp.CloneError != nil {
		t.Errorf("get clone_error = %v; want nil", getResp.CloneError)
	}

	// Verify the trunk directory was created on disk.
	trunkDir := filepath.Join(wsRoot, "smoke-ws", "trunk")
	if _, err := os.Stat(trunkDir); os.IsNotExist(err) {
		t.Errorf("trunk directory %q does not exist", trunkDir)
	}
}

// TS-05-SMOKE-2: Happy path: archiving a workspace with clone_status='ready'
// pushes local commits to upstream, records head_sha, deletes the local clone
// directory, and returns HTTP 200 with status='archived'.
//
// Validates: 05-REQ-6.1, 05-PATH-2
func TestSmoke05_ArchiveReadyWorkspace(t *testing.T) {
	env := newTestEnv(t)
	wsRoot := t.TempDir()

	const fakeSHA = "1234567890abcdef1234567890abcdef12345678"

	// Set up workspace directory on disk.
	slug := "archive-ready-ws"
	trunkDir := filepath.Join(wsRoot, slug, "trunk")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		t.Fatalf("create trunk dir: %v", err)
	}

	// Seed workspace with clone_status='ready'.
	env.seedWorkspace(t, &Workspace{
		Slug:        slug,
		GitURL:      "https://github.com/org/repo",
		OwnerID:     "alice-user-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	// Inject mock git functions.
	origHead := archiveHeadFn
	archiveHeadFn = func(repoPath string) (string, error) {
		return fakeSHA, nil
	}
	defer func() { archiveHeadFn = origHead }()

	origRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = wsRoot
	defer func() { defaultWorkspaceRoot = origRoot }()

	auth := userAuth("alice-user-id")

	// POST /api/v1/workspaces/:slug/archive
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/archive", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("archive status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp spec05WorkspaceJSON
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode archive response: %v", err)
	}

	if resp.Status != "archived" {
		t.Errorf("status = %q; want 'archived'", resp.Status)
	}
	if resp.CloneStatus == nil || *resp.CloneStatus != "archived" {
		t.Errorf("clone_status = %v; want 'archived'", resp.CloneStatus)
	}
	if resp.HeadSHA == nil || *resp.HeadSHA != fakeSHA {
		t.Errorf("head_sha = %v; want %q", resp.HeadSHA, fakeSHA)
	}
	// Verify workspace directory was deleted.
	wsDir := filepath.Join(wsRoot, slug)
	if _, err := os.Stat(wsDir); !os.IsNotExist(err) {
		t.Errorf("workspace directory %q still exists after archive", wsDir)
	}
}

// TS-05-SMOKE-3: Happy path: reactivating an archived workspace sets
// status='active', clone_status='pending', enqueues a reclone job, and the
// worker re-clones the repository setting clone_status='ready'.
//
// Validates: 05-REQ-7.1, 05-PATH-3
func TestSmoke05_ReactivateAndReclone(t *testing.T) {
	env := newTestEnv(t)
	wsRoot := t.TempDir()
	const fakeSHA = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	origCloneFn := cloneFn
	cloneFn = func(_ context.Context, path, url string, depth int, singleBranch bool, refName string, _ transport.AuthMethod) (string, error) {
		_ = os.MkdirAll(path, 0o755)
		return fakeSHA, nil
	}
	defer func() { cloneFn = origCloneFn }()

	origRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = wsRoot
	defer func() { defaultWorkspaceRoot = origRoot }()

	origQueue := defaultQueue
	defaultQueue = NewJobQueue(ctx, env.db, wsRoot, 1)
	defer func() { defaultQueue = origQueue }()

	// Seed an archived workspace.
	env.seedWorkspace(t, &Workspace{
		Slug:        "reactivate-ws",
		GitURL:      "https://github.com/org/repo",
		OwnerID:     "alice-user-id",
		Status:      "archived",
		CloneStatus: "archived",
	})

	auth := userAuth("alice-user-id")

	// POST /api/v1/workspaces/:slug/reactivate
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/reactivate-ws/reactivate", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("reactivate status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp spec05WorkspaceJSON
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode reactivate response: %v", err)
	}

	if resp.Status != "active" {
		t.Errorf("status = %q; want 'active'", resp.Status)
	}
	if resp.CloneStatus == nil || *resp.CloneStatus != "pending" {
		t.Errorf("clone_status = %v; want 'pending'", resp.CloneStatus)
	}
	if resp.CloneError != nil {
		t.Errorf("clone_error = %v; want nil", resp.CloneError)
	}

	// Wait for the worker to process the reclone job.
	waitForCloneStatus(t, env.db, "reactivate-ws", "ready", 5*time.Second)

	// GET — verify clone_status='ready' and updated head_sha.
	rec = env.doRequest(t, http.MethodGet, "/api/v1/workspaces/reactivate-ws", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d; want %d", rec.Code, http.StatusOK)
	}

	var getResp spec05WorkspaceJSON
	if err := json.NewDecoder(rec.Body).Decode(&getResp); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if getResp.CloneStatus == nil || *getResp.CloneStatus != "ready" {
		t.Errorf("after reclone: clone_status = %v; want 'ready'", getResp.CloneStatus)
	}
	if getResp.HeadSHA == nil || *getResp.HeadSHA != fakeSHA {
		t.Errorf("after reclone: head_sha = %v; want %q", getResp.HeadSHA, fakeSHA)
	}

	// Verify trunk directory exists.
	trunkDir := filepath.Join(wsRoot, "reactivate-ws", "trunk")
	if _, err := os.Stat(trunkDir); os.IsNotExist(err) {
		t.Errorf("trunk directory %q does not exist after reclone", trunkDir)
	}
}

// TS-05-SMOKE-4: Happy path: deleting an archived workspace removes any
// remaining workspace directory and deletes the database row, returning HTTP 204.
//
// Validates: 05-REQ-8.1, 05-PATH-4
func TestSmoke05_DeleteArchivedCleanup(t *testing.T) {
	env := newTestEnv(t)
	wsRoot := t.TempDir()

	slug := "delete-ws"

	// Create a workspace directory on disk (simulating leftover from archive failure).
	wsDir := filepath.Join(wsRoot, slug)
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatalf("create workspace dir: %v", err)
	}

	// Seed an archived workspace.
	env.seedWorkspace(t, &Workspace{
		Slug:        slug,
		GitURL:      "https://github.com/org/repo",
		OwnerID:     "alice-user-id",
		Status:      "archived",
		CloneStatus: "archived",
	})

	origRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = wsRoot
	defer func() { defaultWorkspaceRoot = origRoot }()

	auth := userAuth("alice-user-id")

	// DELETE /api/v1/workspaces/:slug
	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/"+slug, "", auth)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d; want %d\nbody: %s", rec.Code, http.StatusNoContent, rec.Body.String())
	}

	// Verify workspace directory was deleted.
	if _, err := os.Stat(wsDir); !os.IsNotExist(err) {
		t.Errorf("workspace directory %q still exists after delete", wsDir)
	}

	// Verify database row was deleted.
	dbWs, err := getWorkspaceBySlug(env.db, slug)
	if err != nil {
		t.Fatalf("getWorkspaceBySlug after delete: %v", err)
	}
	if dbWs != nil {
		t.Error("workspace row still exists in DB after delete")
	}
}

// TS-05-SMOKE-5: End-to-end full workspace lifecycle: create → clone → archive
// → reactivate → reclone → archive → delete, verifying correct state transitions
// and HTTP responses at each step.
//
// Validates: 05-PATH-5
func TestSmoke05_FullLifecycle(t *testing.T) {
	env := newTestEnv(t)
	wsRoot := t.TempDir()

	const sha1 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const sha2 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	cloneCount := 0

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	origCloneFn := cloneFn
	cloneFn = func(_ context.Context, path, url string, depth int, singleBranch bool, refName string, _ transport.AuthMethod) (string, error) {
		_ = os.MkdirAll(path, 0o755)
		cloneCount++
		if cloneCount == 1 {
			return sha1, nil
		}
		return sha2, nil
	}
	defer func() { cloneFn = origCloneFn }()

	origHead := archiveHeadFn
	archiveHeadFn = func(repoPath string) (string, error) {
		return sha1, nil
	}
	defer func() { archiveHeadFn = origHead }()

	origRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = wsRoot
	defer func() { defaultWorkspaceRoot = origRoot }()

	origQueue := defaultQueue
	defaultQueue = NewJobQueue(ctx, env.db, wsRoot, 1)
	defer func() { defaultQueue = origQueue }()

	auth := userAuth("alice-user-id")
	slug := "lifecycle-spec05"
	branch := "main"

	// 1. Create workspace — HTTP 201 with clone_status='pending'.
	body := fmt.Sprintf(`{"slug":%q,"git_url":"https://github.com/org/repo","branch":%q}`, slug, branch)
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("step 1 create: status = %d; want %d\nbody: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	assertCloneStatus(t, rec, "pending", "step 1 create")

	// 2. Wait for clone worker to complete.
	waitForCloneStatus(t, env.db, slug, "ready", 5*time.Second)

	// 3. GET — clone_status='ready', valid head_sha.
	rec = env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug, "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("step 3 get: status = %d", rec.Code)
	}
	ws := assertCloneStatus(t, rec, "ready", "step 3 get")
	if ws.HeadSHA == nil || len(*ws.HeadSHA) != 40 {
		t.Errorf("step 3 get: head_sha = %v; want 40-char hex string", ws.HeadSHA)
	}

	// 4. Archive — HTTP 200 with clone_status='archived'; directory deleted.
	rec = env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/archive", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("step 4 archive: status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	ws = assertCloneStatus(t, rec, "archived", "step 4 archive")
	if ws.Status != "archived" {
		t.Errorf("step 4 archive: status = %q; want 'archived'", ws.Status)
	}
	wsDir := filepath.Join(wsRoot, slug)
	if _, err := os.Stat(wsDir); !os.IsNotExist(err) {
		t.Errorf("step 4: workspace directory %q still exists", wsDir)
	}

	// 5. Reactivate — HTTP 200 with clone_status='pending'.
	rec = env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/reactivate", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("step 5 reactivate: status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	assertCloneStatus(t, rec, "pending", "step 5 reactivate")

	// 6. Wait for reclone worker.
	waitForCloneStatus(t, env.db, slug, "ready", 5*time.Second)

	// Verify reclone completed — clone_status='ready'.
	rec = env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug, "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("step 6 get: status = %d", rec.Code)
	}
	assertCloneStatus(t, rec, "ready", "step 6 after reclone")

	// 7. Archive again.
	rec = env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/archive", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("step 7 re-archive: status = %d", rec.Code)
	}
	assertCloneStatus(t, rec, "archived", "step 7 re-archive")

	// 8. Delete — HTTP 204; DB row gone; no directory remains.
	rec = env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/"+slug, "", auth)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("step 8 delete: status = %d; want %d", rec.Code, http.StatusNoContent)
	}
	dbWs, err := getWorkspaceBySlug(env.db, slug)
	if err != nil {
		t.Fatalf("step 8 db lookup: %v", err)
	}
	if dbWs != nil {
		t.Error("step 8: workspace row still exists after delete")
	}
	if _, err := os.Stat(wsDir); !os.IsNotExist(err) {
		t.Errorf("step 8: workspace directory %q still exists", wsDir)
	}
}

// TS-05-SMOKE-6: Failure path: workspace is created with an invalid git URL,
// returns HTTP 201 with clone_status='pending', and the worker sets
// clone_status='failed' with the error in clone_error after PlainCloneContext fails.
//
// Validates: 05-PATH-6
func TestSmoke05_CloneFailure(t *testing.T) {
	env := newTestEnv(t)
	wsRoot := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	origCloneFn := cloneFn
	cloneFn = func(_ context.Context, path, url string, depth int, singleBranch bool, refName string, _ transport.AuthMethod) (string, error) {
		return "", fmt.Errorf("repository not found")
	}
	defer func() { cloneFn = origCloneFn }()

	origRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = wsRoot
	defer func() { defaultWorkspaceRoot = origRoot }()

	origQueue := defaultQueue
	defaultQueue = NewJobQueue(ctx, env.db, wsRoot, 1)
	defer func() { defaultQueue = origQueue }()

	auth := userAuth("alice-user-id")

	// Create workspace — returns 201 with clone_status='pending'.
	body := `{"slug":"fail-ws","git_url":"https://github.com/invalid/repo.git"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d; want %d\nbody: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	assertCloneStatus(t, rec, "pending", "create")

	// Wait for worker to process the job (will fail the clone).
	waitForCloneStatus(t, env.db, "fail-ws", "failed", 5*time.Second)

	// GET — clone_status='failed', clone_error non-null.
	rec = env.doRequest(t, http.MethodGet, "/api/v1/workspaces/fail-ws", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d", rec.Code)
	}

	ws := assertCloneStatus(t, rec, "failed", "after clone failure")
	if ws.CloneError == nil {
		t.Error("clone_error is nil; want non-null error message")
	} else if *ws.CloneError == "" {
		t.Error("clone_error is empty; want non-empty error message")
	}
	if ws.HeadSHA != nil {
		t.Errorf("head_sha = %v; want nil after failed clone", ws.HeadSHA)
	}

	// Verify partial directory was cleaned up.
	wsDir := filepath.Join(wsRoot, "fail-ws")
	if _, err := os.Stat(wsDir); !os.IsNotExist(err) {
		t.Errorf("partial workspace directory %q still exists after failed clone", wsDir)
	}
}

// TS-05-SMOKE-EXTRA: Verify all workspace endpoints include clone fields in responses.
// This covers TS-05-8 (05-REQ-3.2) at the smoke test level.
func TestSmoke05_AllEndpointsIncludeCloneFields(t *testing.T) {
	env := newTestEnv(t)

	// Inject mock functions so archive/reactivate work without real git.
	origHead := archiveHeadFn
	archiveHeadFn = func(repoPath string) (string, error) {
		return "abcdef0123456789abcdef0123456789abcdef01", nil
	}
	defer func() { archiveHeadFn = origHead }()

	origRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = t.TempDir()
	defer func() { defaultWorkspaceRoot = origRoot }()

	auth := userAuth("alice-user-id")
	slug := "clone-fields-ws"

	// Verify each endpoint returns clone fields.
	cloneFields := []string{"clone_status", "head_sha", "clone_error"}

	t.Run("POST_create", func(t *testing.T) {
		body := fmt.Sprintf(`{"slug":%q,"git_url":"https://github.com/org/repo"}`, slug)
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d; want %d", rec.Code, http.StatusCreated)
		}
		assertJSONHasFields(t, rec, cloneFields)
	})

	t.Run("GET_list", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		var list []map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(list) == 0 {
			t.Fatal("empty list")
		}
		for _, f := range cloneFields {
			if _, ok := list[0][f]; !ok {
				t.Errorf("list response missing %q", f)
			}
		}
	})

	t.Run("GET_by_slug", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/"+slug, "", auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		assertJSONHasFields(t, rec, cloneFields)
	})

	t.Run("PATCH_update", func(t *testing.T) {
		body := `{"display_name":"Updated Name"}`
		rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/"+slug, body, auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		assertJSONHasFields(t, rec, cloneFields)
	})

	// Set clone_status to 'ready' so archive can push.
	_ = updateCloneStatus(env.db, slug, "ready", nil, nil)
	trunkDir := filepath.Join(defaultWorkspaceRoot, slug, "trunk")
	_ = os.MkdirAll(trunkDir, 0o755)

	t.Run("POST_archive", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/archive", "", auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; body = %s", rec.Code, rec.Body.String())
		}
		assertJSONHasFields(t, rec, cloneFields)
	})

	t.Run("POST_reactivate", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/"+slug+"/reactivate", "", auth)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; body = %s", rec.Code, rec.Body.String())
		}
		assertJSONHasFields(t, rec, cloneFields)
	})
}

// ---------- helpers ----------

// assertCloneStatus decodes a spec05WorkspaceJSON from the recorder and checks
// that clone_status matches the expected value. Returns the decoded struct.
func assertCloneStatus(t *testing.T, rec *httptest.ResponseRecorder, want, label string) spec05WorkspaceJSON {
	t.Helper()
	var ws spec05WorkspaceJSON
	if err := json.NewDecoder(rec.Body).Decode(&ws); err != nil {
		t.Fatalf("%s: decode: %v", label, err)
	}
	if ws.CloneStatus == nil {
		t.Fatalf("%s: clone_status is nil; want %q", label, want)
	}
	if *ws.CloneStatus != want {
		t.Errorf("%s: clone_status = %q; want %q", label, *ws.CloneStatus, want)
	}
	return ws
}

// waitForCloneStatus polls the database until the workspace's clone_status
// matches the expected value, or times out.
func waitForCloneStatus(t *testing.T, db *sql.DB, slug, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, _, _, err := getCloneFields(db, slug)
		if err == nil && status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, _, _, _ := getCloneFields(db, slug)
	t.Fatalf("timeout waiting for clone_status=%q on %q; current=%q", want, slug, status)
}

// assertJSONHasFields decodes the response as a JSON object and verifies that
// all specified fields are present.
func assertJSONHasFields(t *testing.T, rec *httptest.ResponseRecorder, fields []string) {
	t.Helper()
	var raw map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&raw); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	for _, f := range fields {
		if _, ok := raw[f]; !ok {
			t.Errorf("response missing field %q", f)
		}
	}
}

