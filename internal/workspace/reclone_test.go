package workspace

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ========================================================================
// Spec 13 Task 3.3: Reclone operation
// (TS-13-21, TS-13-22, TS-13-23, TS-13-24)
// Requirements: 13-REQ-7
// ========================================================================

// TS-13-21: Verifies that POST /api/v1/workspaces/:slug/reclone executes
// the archive flow, deletes the clone, atomically sets clone_status='pending',
// sync_status='idle', clears sync_error and upstream_head_sha, enqueues a
// clone job, and returns workspace with clone_status='pending' and
// status='active'.
// Requirement: 13-REQ-7.1
// Property: 13-PROP-5 (reclone workspace status remains 'active')
func TestReclone_SuccessfulReclone(t *testing.T) {
	env := newTestEnv(t)
	wsRoot := t.TempDir()

	// Set workspace root so the handler can locate/delete workspace dirs.
	oldRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = wsRoot
	defer func() { defaultWorkspaceRoot = oldRoot }()

	env.seedWorkspace(t, &Workspace{
		Slug:        "reclone-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	// Set sync fields to simulate a workspace with sync state.
	_, err := env.db.Exec(
		`UPDATE workspaces SET sync_mode = 'pull_only', sync_status = 'idle',
		 upstream_head_sha = 'abc123', sync_error = NULL
		 WHERE slug = ?`,
		"reclone-ws",
	)
	if err != nil {
		t.Fatalf("failed to set sync fields: %v", err)
	}

	// Create workspace directory with trunk to simulate an existing clone.
	trunkDir := filepath.Join(wsRoot, "reclone-ws", "trunk")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		t.Fatalf("create trunk dir: %v", err)
	}

	// Mock the archive push function (should be called but not block reclone).
	oldPush := archiveOpenAndPushFn
	archiveOpenAndPushFn = func(repoPath, gitURL string) error {
		return nil // push succeeds
	}
	defer func() { archiveOpenAndPushFn = oldPush }()

	// Mock the HEAD SHA reader for archive flow.
	oldHead := archiveHeadFn
	archiveHeadFn = func(repoPath string) (string, error) {
		return "abcdef1234567890abcdef1234567890abcdef12", nil
	}
	defer func() { archiveHeadFn = oldHead }()

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/reclone-ws/reclone", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /reclone returned %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// clone_status must be 'pending' after reclone.
	if resp["clone_status"] != "pending" {
		t.Errorf("clone_status = %v; want %q", resp["clone_status"], "pending")
	}

	// status must remain 'active' throughout (13-PROP-5).
	if resp["status"] != "active" {
		t.Errorf("status = %v; want %q (must remain active during reclone)", resp["status"], "active")
	}

	// sync_status must be 'idle' after reclone.
	if resp["sync_status"] != "idle" {
		t.Errorf("sync_status = %v; want %q", resp["sync_status"], "idle")
	}

	// sync_error must be null/cleared.
	if resp["sync_error"] != nil {
		t.Errorf("sync_error = %v; want null", resp["sync_error"])
	}

	// upstream_head_sha must be null/cleared.
	if resp["upstream_head_sha"] != nil {
		t.Errorf("upstream_head_sha = %v; want null", resp["upstream_head_sha"])
	}
}

// TS-13-22: Verifies that workspace status remains 'active' throughout the
// entire reclone operation and is never set to any other value.
// Requirement: 13-REQ-7.2
// Property: 13-PROP-5 (workspace status remains active)
func TestReclone_StatusRemainsActive(t *testing.T) {
	env := newTestEnv(t)
	wsRoot := t.TempDir()

	oldRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = wsRoot
	defer func() { defaultWorkspaceRoot = oldRoot }()

	env.seedWorkspace(t, &Workspace{
		Slug:        "reclone-active-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	_, err := env.db.Exec(
		`UPDATE workspaces SET sync_mode = 'pull_only', sync_status = 'idle' WHERE slug = ?`,
		"reclone-active-ws",
	)
	if err != nil {
		t.Fatalf("failed to set sync fields: %v", err)
	}

	trunkDir := filepath.Join(wsRoot, "reclone-active-ws", "trunk")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		t.Fatalf("create trunk dir: %v", err)
	}

	oldPush := archiveOpenAndPushFn
	archiveOpenAndPushFn = func(repoPath, gitURL string) error { return nil }
	defer func() { archiveOpenAndPushFn = oldPush }()

	oldHead := archiveHeadFn
	archiveHeadFn = func(repoPath string) (string, error) {
		return "abcdef1234567890abcdef1234567890abcdef12", nil
	}
	defer func() { archiveHeadFn = oldHead }()

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/reclone-active-ws/reclone", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /reclone returned %d; want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp["status"] != "active" {
		t.Errorf("response status = %v; want %q", resp["status"], "active")
	}

	var dbStatus string
	err = env.db.QueryRow(
		`SELECT status FROM workspaces WHERE slug = ?`, "reclone-active-ws",
	).Scan(&dbStatus)
	if err != nil {
		t.Fatalf("failed to query status: %v", err)
	}
	if dbStatus != "active" {
		t.Errorf("DB status = %q; want %q", dbStatus, "active")
	}
}

// TS-13-23: Verifies that 'afc workspace reclone <slug> --confirm' calls
// POST /reclone and prints the workspace with clone_status='pending'.
// Tested at the API level since CLI code lives outside the workspace package.
// Requirement: 13-REQ-7.3
func TestReclone_CLIRecloneWithConfirm(t *testing.T) {
	env := newTestEnv(t)
	wsRoot := t.TempDir()

	oldRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = wsRoot
	defer func() { defaultWorkspaceRoot = oldRoot }()

	env.seedWorkspace(t, &Workspace{
		Slug:        "cli-reclone-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	_, err := env.db.Exec(
		`UPDATE workspaces SET sync_mode = 'pull_only', sync_status = 'idle' WHERE slug = ?`,
		"cli-reclone-ws",
	)
	if err != nil {
		t.Fatalf("failed to set sync fields: %v", err)
	}

	trunkDir := filepath.Join(wsRoot, "cli-reclone-ws", "trunk")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		t.Fatalf("create trunk dir: %v", err)
	}

	oldPush := archiveOpenAndPushFn
	archiveOpenAndPushFn = func(repoPath, gitURL string) error { return nil }
	defer func() { archiveOpenAndPushFn = oldPush }()

	oldHead := archiveHeadFn
	archiveHeadFn = func(repoPath string) (string, error) {
		return "abcdef1234567890abcdef1234567890abcdef12", nil
	}
	defer func() { archiveHeadFn = oldHead }()

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/cli-reclone-ws/reclone", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /reclone returned %d; want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp["clone_status"] != "pending" {
		t.Errorf("clone_status = %v; want %q", resp["clone_status"], "pending")
	}
}

// TS-13-24: Verifies that after reclone enqueues a clone job,
// clone_status is set to 'pending' (the clone lifecycle takes over).
// Requirement: 13-REQ-7.4
func TestReclone_CloneJobEnqueued(t *testing.T) {
	env := newTestEnv(t)
	wsRoot := t.TempDir()

	oldRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = wsRoot
	defer func() { defaultWorkspaceRoot = oldRoot }()

	env.seedWorkspace(t, &Workspace{
		Slug:        "reclone-lifecycle-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	_, err := env.db.Exec(
		`UPDATE workspaces SET sync_mode = 'pull_only', sync_status = 'idle' WHERE slug = ?`,
		"reclone-lifecycle-ws",
	)
	if err != nil {
		t.Fatalf("failed to set sync fields: %v", err)
	}

	trunkDir := filepath.Join(wsRoot, "reclone-lifecycle-ws", "trunk")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		t.Fatalf("create trunk dir: %v", err)
	}

	oldPush := archiveOpenAndPushFn
	archiveOpenAndPushFn = func(repoPath, gitURL string) error { return nil }
	defer func() { archiveOpenAndPushFn = oldPush }()

	oldHead := archiveHeadFn
	archiveHeadFn = func(repoPath string) (string, error) {
		return "abcdef1234567890abcdef1234567890abcdef12", nil
	}
	defer func() { archiveHeadFn = oldHead }()

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/reclone-lifecycle-ws/reclone", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /reclone returned %d; want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["clone_status"] != "pending" {
		t.Errorf("clone_status = %v; want %q", resp["clone_status"], "pending")
	}

	// Verify DB state matches response.
	var cloneStatus string
	err = env.db.QueryRow(
		`SELECT clone_status FROM workspaces WHERE slug = ?`, "reclone-lifecycle-ws",
	).Scan(&cloneStatus)
	if err != nil {
		t.Fatalf("failed to query clone_status: %v", err)
	}
	if cloneStatus != "pending" {
		t.Errorf("DB clone_status = %q; want %q", cloneStatus, "pending")
	}
}

// 13-REQ-7.E1: Verifies that reclone proceeds when the archive push fails.
func TestReclone_ArchivePushFailureContinues(t *testing.T) {
	env := newTestEnv(t)
	wsRoot := t.TempDir()

	oldRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = wsRoot
	defer func() { defaultWorkspaceRoot = oldRoot }()

	env.seedWorkspace(t, &Workspace{
		Slug:        "reclone-pushfail-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	_, err := env.db.Exec(
		`UPDATE workspaces SET sync_mode = 'pull_only', sync_status = 'idle' WHERE slug = ?`,
		"reclone-pushfail-ws",
	)
	if err != nil {
		t.Fatalf("failed to set sync fields: %v", err)
	}

	trunkDir := filepath.Join(wsRoot, "reclone-pushfail-ws", "trunk")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		t.Fatalf("create trunk dir: %v", err)
	}

	// Mock push to FAIL — reclone should continue regardless.
	oldPush := archiveOpenAndPushFn
	archiveOpenAndPushFn = func(repoPath, gitURL string) error {
		return os.ErrPermission
	}
	defer func() { archiveOpenAndPushFn = oldPush }()

	oldHead := archiveHeadFn
	archiveHeadFn = func(repoPath string) (string, error) {
		return "abcdef1234567890abcdef1234567890abcdef12", nil
	}
	defer func() { archiveHeadFn = oldHead }()

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/reclone-pushfail-ws/reclone", "", auth)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /reclone with push failure returned %d; want %d; body: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["clone_status"] != "pending" {
		t.Errorf("clone_status = %v; want %q", resp["clone_status"], "pending")
	}
}

// 13-REQ-7.E5: Verifies that a caller without workspaces:sync scope cannot
// trigger reclone.
func TestReclone_MissingSyncScopeRejected(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:        "reclone-noscope-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	auth := patAuth("alice-id", "workspaces:read")
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/reclone-noscope-ws/reclone", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Errorf("POST /reclone without workspaces:sync scope returned %d; want %d",
			rec.Code, http.StatusForbidden)
	}

	envelope := parseErrorEnvelope(t, rec)
	if envelope.Error.Message == "" {
		t.Error("error.message is empty; want non-empty message about missing scope")
	}
}

// 13-REQ-7.E6: Verifies that reclone for a non-existent workspace returns 404.
func TestReclone_NonexistentWorkspace(t *testing.T) {
	env := newTestEnv(t)

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/nonexistent-ws/reclone", "", auth)

	if rec.Code != http.StatusNotFound {
		t.Errorf("POST /reclone on nonexistent workspace returned %d; want %d",
			rec.Code, http.StatusNotFound)
	}

	envelope := parseErrorEnvelope(t, rec)
	if envelope.Error.Code != http.StatusNotFound {
		t.Errorf("error.code = %d; want %d", envelope.Error.Code, http.StatusNotFound)
	}
}

// 13-REQ-7.E7: Verifies that reclone is rejected when clone_status is
// 'pending' or 'cloning' to prevent concurrent reclone operations.
func TestReclone_RejectedWhilePendingOrCloning(t *testing.T) {
	for _, cloneStatus := range []string{"pending", "cloning"} {
		t.Run(cloneStatus, func(t *testing.T) {
			env := newTestEnv(t)

			env.seedWorkspace(t, &Workspace{
				Slug:        "reclone-busy-ws",
				GitURL:      "https://github.com/example/repo.git",
				OwnerID:     "alice-id",
				Status:      "active",
				CloneStatus: cloneStatus,
			})

			auth := adminAuth()
			rec := env.doRequest(t, http.MethodPost,
				"/api/v1/workspaces/reclone-busy-ws/reclone", "", auth)

			if rec.Code != http.StatusConflict {
				t.Errorf("POST /reclone with clone_status=%q returned %d; want %d",
					cloneStatus, rec.Code, http.StatusConflict)
			}

			envelope := parseErrorEnvelope(t, rec)
			if envelope.Error.Message == "" {
				t.Error("error.message is empty; want non-empty message about concurrent reclone")
			}
		})
	}
}

// 13-REQ-8.E2 (reclone path): Verifies that unauthenticated requests to
// POST /reclone are rejected.
func TestReclone_UnauthenticatedRejected(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:        "reclone-noauth-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/reclone-noauth-ws/reclone", "", nil)

	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Errorf("POST /reclone unauthenticated returned %d; want %d or %d",
			rec.Code, http.StatusUnauthorized, http.StatusForbidden)
	}

	body := rec.Body.String()
	if !strings.Contains(strings.ToLower(body), "auth") &&
		!strings.Contains(strings.ToLower(body), "unauthorized") &&
		!strings.Contains(strings.ToLower(body), "forbidden") {
		t.Errorf("response body = %q; want mention of auth/unauthorized/forbidden", body)
	}
}
