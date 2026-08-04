package workspace

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
)

// ========================================================================
// Spec 13 Task 9.3: Smoke tests for sync and reclone end-to-end flows
// Requirements: 13-REQ-3, 13-REQ-7, 13-REQ-8
// ========================================================================

// TS-13-SMOKE-1: End-to-end sync smoke test.
// Creates a workspace, waits for clone to complete, triggers sync via
// POST /api/v1/workspaces/:slug/sync, and verifies the response contains
// updated sync fields (sync_status, sync_mode, last_sync_at).
//
// Real components: workspace create handler, sync handler, job queue,
// SQLite database, WORKSPACE_ROOT filesystem.
// Mock: cloneFn (simulates PlainCloneContext), syncFetchAndCompareFn
// (returns up_to_date), syncUpdateLocalRefFn.
//
// Validates: 13-REQ-3, 13-REQ-8, 13-PATH-1
func TestSmoke13_SyncEndToEnd(t *testing.T) {
	env := newTestEnv(t)
	wsRoot := t.TempDir()

	const fakeSHA = "aabbccddee00112233445566778899aabbccddee"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Mock clone function.
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

	// Mock sync functions: return "up_to_date" (no new commits upstream).
	origFetchFn := syncFetchAndCompareFn
	syncFetchAndCompareFn = func(_ context.Context, repoPath string, _ transport.AuthMethod, branch *string, localHeadSHA string) (string, string, error) {
		return fakeSHA, "up_to_date", nil
	}
	defer func() { syncFetchAndCompareFn = origFetchFn }()

	origRefFn := syncUpdateLocalRefFn
	syncUpdateLocalRefFn = func(repoPath string, branch *string, newSHA string) error {
		return nil
	}
	defer func() { syncUpdateLocalRefFn = origRefFn }()

	auth := userAuth("alice-user-id")

	// Step 1: Create workspace.
	body := `{"slug":"smoke-sync-ws","git_url":"https://github.com/example/repo.git"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST create status = %d; want %d\nbody: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	// Step 2: Wait for clone to complete.
	waitForCloneStatus(t, env.db, "smoke-sync-ws", "ready", 5*1e9)

	// Step 3: Trigger sync via POST /api/v1/workspaces/:slug/sync.
	rec = env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/smoke-sync-ws/sync", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /sync status = %d; want %d\nbody: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	// Step 4: Verify response contains sync fields.
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode sync response: %v", err)
	}

	// Verify sync_status is 'idle' after successful sync.
	if resp["sync_status"] != "idle" {
		t.Errorf("sync_status = %v; want %q", resp["sync_status"], "idle")
	}

	// Verify sync_mode is present and correct (default 'pull_only').
	if resp["sync_mode"] != "pull_only" {
		t.Errorf("sync_mode = %v; want %q", resp["sync_mode"], "pull_only")
	}

	// Verify last_sync_at is set (non-null).
	if resp["last_sync_at"] == nil {
		t.Error("last_sync_at is nil; want non-null timestamp after sync")
	}

	// Verify upstream_head_sha is set.
	if resp["upstream_head_sha"] == nil {
		t.Error("upstream_head_sha is nil; want non-null after sync")
	}

	// Verify sync_error is null.
	if resp["sync_error"] != nil {
		t.Errorf("sync_error = %v; want nil", resp["sync_error"])
	}

	// Verify workspace is still active.
	if resp["status"] != "active" {
		t.Errorf("status = %v; want %q", resp["status"], "active")
	}
}

// TS-13-SMOKE-2: End-to-end reclone smoke test.
// Creates a workspace, waits for clone to complete, triggers reclone via
// POST /api/v1/workspaces/:slug/reclone, and verifies the response has
// clone_status='pending' and workspace status='active'.
//
// Real components: workspace create handler, reclone handler, job queue,
// SQLite database, WORKSPACE_ROOT filesystem.
// Mock: cloneFn (simulates PlainCloneContext), archiveHeadFn,
// archiveOpenAndPushFn.
//
// Validates: 13-REQ-7, 13-REQ-8, 13-PROP-5, 13-PATH-4
func TestSmoke13_RecloneEndToEnd(t *testing.T) {
	env := newTestEnv(t)
	wsRoot := t.TempDir()

	const fakeSHA = "1234567890abcdef1234567890abcdef12345678"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Mock clone function.
	origCloneFn := cloneFn
	cloneFn = func(_ context.Context, path, url string, depth int, singleBranch bool, refName string, _ transport.AuthMethod) (string, error) {
		_ = os.MkdirAll(path, 0o755)
		return fakeSHA, nil
	}
	defer func() { cloneFn = origCloneFn }()

	// Mock archive functions.
	origArchiveHead := archiveHeadFn
	archiveHeadFn = func(repoPath string) (string, error) {
		return fakeSHA, nil
	}
	defer func() { archiveHeadFn = origArchiveHead }()

	origArchivePush := archiveOpenAndPushFn
	archiveOpenAndPushFn = func(repoPath, gitURL string) error {
		return nil
	}
	defer func() { archiveOpenAndPushFn = origArchivePush }()

	origRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = wsRoot
	defer func() { defaultWorkspaceRoot = origRoot }()

	origQueue := defaultQueue
	defaultQueue = NewJobQueue(ctx, env.db, wsRoot, 1)
	defer func() { defaultQueue = origQueue }()

	auth := userAuth("alice-user-id")

	// Step 1: Create workspace.
	body := `{"slug":"smoke-reclone-ws","git_url":"https://github.com/example/repo.git"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST create status = %d; want %d\nbody: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	// Step 2: Wait for initial clone to complete.
	waitForCloneStatus(t, env.db, "smoke-reclone-ws", "ready", 5*1e9)

	// Verify trunk directory exists.
	trunkDir := filepath.Join(wsRoot, "smoke-reclone-ws", "trunk")
	if _, err := os.Stat(trunkDir); os.IsNotExist(err) {
		t.Fatalf("trunk directory %q does not exist after clone", trunkDir)
	}

	// Step 3: Trigger reclone via POST /api/v1/workspaces/:slug/reclone.
	rec = env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/smoke-reclone-ws/reclone", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /reclone status = %d; want %d\nbody: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	// Step 4: Verify response.
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode reclone response: %v", err)
	}

	// 13-REQ-7.1: clone_status='pending' in reclone response.
	if resp["clone_status"] != "pending" {
		t.Errorf("clone_status = %v; want %q", resp["clone_status"], "pending")
	}

	// 13-PROP-5: workspace status remains 'active' throughout.
	if resp["status"] != "active" {
		t.Errorf("status = %v; want %q", resp["status"], "active")
	}

	// 13-REQ-7.1: sync_status='idle' after reclone.
	if resp["sync_status"] != "idle" {
		t.Errorf("sync_status = %v; want %q", resp["sync_status"], "idle")
	}

	// 13-REQ-7.1: sync_error cleared.
	if resp["sync_error"] != nil {
		t.Errorf("sync_error = %v; want nil", resp["sync_error"])
	}

	// 13-REQ-7.1: upstream_head_sha cleared.
	if resp["upstream_head_sha"] != nil {
		t.Errorf("upstream_head_sha = %v; want nil", resp["upstream_head_sha"])
	}

	// The workspace directory is deleted by reclone but the clone worker
	// re-creates it asynchronously, so we just verify the response is correct.

	// Step 5: Wait for reclone job to re-clone the workspace.
	waitForCloneStatus(t, env.db, "smoke-reclone-ws", "ready", 5*1e9)

	// Step 6: Verify workspace is back to ready state.
	rec = env.doRequest(t, http.MethodGet,
		"/api/v1/workspaces/smoke-reclone-ws", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET after reclone status = %d; want %d",
			rec.Code, http.StatusOK)
	}

	var getResp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&getResp); err != nil {
		t.Fatalf("decode get response: %v", err)
	}

	if getResp["clone_status"] != "ready" {
		t.Errorf("after reclone: clone_status = %v; want %q",
			getResp["clone_status"], "ready")
	}
	if getResp["status"] != "active" {
		t.Errorf("after reclone: status = %v; want %q",
			getResp["status"], "active")
	}

	// Verify trunk directory exists again after reclone.
	if _, err := os.Stat(trunkDir); os.IsNotExist(err) {
		t.Errorf("trunk directory %q does not exist after reclone", trunkDir)
	}
}
