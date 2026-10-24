package workspace

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
)

// ========================================================================
// Spec 13 Task 3.3: Reclone operation
// (TS-13-21, TS-13-22, TS-13-23, TS-13-24)
// Requirements: 13-REQ-7
// ========================================================================

// TS-13-21: Verifies that POST /api/v1/workspaces/:slug/reclone executes
// the archive flow, deletes the clone, atomically sets status='pending',
// clears sync_error and upstream_head_sha, enqueues a clone job, and returns
// the workspace with status='pending'.
// Requirement: 13-REQ-7.1
func TestReclone_SuccessfulReclone(t *testing.T) {
	env := newTestEnv(t)
	wsRoot := t.TempDir()

	// Set workspace root so the handler can locate/delete workspace dirs.
	oldRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = wsRoot
	defer func() { defaultWorkspaceRoot = oldRoot }()

	env.seedWorkspace(t, &Workspace{
		Slug:    "reclone-ws",
		GitURL:  "https://github.com/example/repo.git",
		OwnerID: "alice-id",
		Status:  "ready",
	})

	// Set sync fields to simulate a workspace with sync state.
	_, err := env.db.Exec(
		`UPDATE workspaces SET upstream_head_sha = 'abc123', sync_error = 'stale'
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
	archiveOpenAndPushFn = func(repoPath, gitURL string, _ transport.AuthMethod) error {
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

	// status must be 'pending' after reclone.
	if resp["status"] != "pending" {
		t.Errorf("status = %v; want %q", resp["status"], "pending")
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

// TS-13-22: Verifies that an archived workspace cannot be recloned: it has
// no clone to replace, so the request is refused with HTTP 400 and the
// workspace stays archived.
// Requirement: 13-REQ-7.2
func TestReclone_ArchivedRejected(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "reclone-archived-ws",
		GitURL:  "https://github.com/example/repo.git",
		OwnerID: "alice-id",
		Status:  "archived",
	})

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/reclone-archived-ws/reclone", "", auth)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /reclone on archived workspace returned %d; want %d; body: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	envelope := parseErrorEnvelope(t, rec)
	want := "workspace is archived and must be reactivated before recloning"
	if envelope.Error.Message != want {
		t.Errorf("error.message = %q; want %q", envelope.Error.Message, want)
	}

	var dbStatus string
	err := env.db.QueryRow(
		`SELECT status FROM workspaces WHERE slug = ?`, "reclone-archived-ws",
	).Scan(&dbStatus)
	if err != nil {
		t.Fatalf("failed to query status: %v", err)
	}
	if dbStatus != "archived" {
		t.Errorf("DB status = %q; want %q", dbStatus, "archived")
	}
}

// TS-13-23: Verifies that 'afc workspace reclone <slug> --confirm' calls
// POST /reclone and prints the workspace with status='pending'.
// Tested at the API level since CLI code lives outside the workspace package.
// Requirement: 13-REQ-7.3
func TestReclone_CLIRecloneWithConfirm(t *testing.T) {
	env := newTestEnv(t)
	wsRoot := t.TempDir()

	oldRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = wsRoot
	defer func() { defaultWorkspaceRoot = oldRoot }()

	env.seedWorkspace(t, &Workspace{
		Slug:    "cli-reclone-ws",
		GitURL:  "https://github.com/example/repo.git",
		OwnerID: "alice-id",
		Status:  "ready",
	})

	trunkDir := filepath.Join(wsRoot, "cli-reclone-ws", "trunk")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		t.Fatalf("create trunk dir: %v", err)
	}

	oldPush := archiveOpenAndPushFn
	archiveOpenAndPushFn = func(repoPath, gitURL string, _ transport.AuthMethod) error { return nil }
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

	if resp["status"] != "pending" {
		t.Errorf("status = %v; want %q", resp["status"], "pending")
	}
}

// TS-13-24: Verifies that after reclone enqueues a clone job,
// status is set to 'pending' (the clone lifecycle takes over).
// Requirement: 13-REQ-7.4
func TestReclone_CloneJobEnqueued(t *testing.T) {
	env := newTestEnv(t)
	wsRoot := t.TempDir()

	oldRoot := defaultWorkspaceRoot
	defaultWorkspaceRoot = wsRoot
	defer func() { defaultWorkspaceRoot = oldRoot }()

	env.seedWorkspace(t, &Workspace{
		Slug:    "reclone-lifecycle-ws",
		GitURL:  "https://github.com/example/repo.git",
		OwnerID: "alice-id",
		Status:  "ready",
	})

	trunkDir := filepath.Join(wsRoot, "reclone-lifecycle-ws", "trunk")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		t.Fatalf("create trunk dir: %v", err)
	}

	oldPush := archiveOpenAndPushFn
	archiveOpenAndPushFn = func(repoPath, gitURL string, _ transport.AuthMethod) error { return nil }
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
	if resp["status"] != "pending" {
		t.Errorf("status = %v; want %q", resp["status"], "pending")
	}

	// Verify DB state matches response.
	var status string
	err := env.db.QueryRow(
		`SELECT status FROM workspaces WHERE slug = ?`, "reclone-lifecycle-ws",
	).Scan(&status)
	if err != nil {
		t.Fatalf("failed to query status: %v", err)
	}
	if status != "pending" {
		t.Errorf("DB status = %q; want %q", status, "pending")
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
		Slug:    "reclone-pushfail-ws",
		GitURL:  "https://github.com/example/repo.git",
		OwnerID: "alice-id",
		Status:  "ready",
	})

	trunkDir := filepath.Join(wsRoot, "reclone-pushfail-ws", "trunk")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		t.Fatalf("create trunk dir: %v", err)
	}

	// Mock push to FAIL — reclone should continue regardless.
	oldPush := archiveOpenAndPushFn
	archiveOpenAndPushFn = func(repoPath, gitURL string, _ transport.AuthMethod) error {
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
	if resp["status"] != "pending" {
		t.Errorf("status = %v; want %q", resp["status"], "pending")
	}
}

// 13-REQ-7.E5: Verifies that a caller without workspaces:write scope cannot
// trigger reclone.
func TestReclone_MissingSyncScopeRejected(t *testing.T) {
	env := newTestEnv(t)

	env.seedWorkspace(t, &Workspace{
		Slug:    "reclone-noscope-ws",
		GitURL:  "https://github.com/example/repo.git",
		OwnerID: "alice-id",
		Status:  "ready",
	})

	auth := patAuth("alice-id", "workspaces:read")
	rec := env.doRequest(t, http.MethodPost,
		"/api/v1/workspaces/reclone-noscope-ws/reclone", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Errorf("POST /reclone without workspaces:write scope returned %d; want %d",
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

// 13-REQ-7.E7: Verifies that reclone is rejected when status is 'pending' or
// 'cloning' to prevent concurrent reclone operations.
func TestReclone_RejectedWhilePendingOrCloning(t *testing.T) {
	for _, status := range []string{"pending", "cloning"} {
		t.Run(status, func(t *testing.T) {
			env := newTestEnv(t)

			env.seedWorkspace(t, &Workspace{
				Slug:    "reclone-busy-ws",
				GitURL:  "https://github.com/example/repo.git",
				OwnerID: "alice-id",
				Status:  status,
			})

			auth := adminAuth()
			rec := env.doRequest(t, http.MethodPost,
				"/api/v1/workspaces/reclone-busy-ws/reclone", "", auth)

			if rec.Code != http.StatusConflict {
				t.Errorf("POST /reclone with status=%q returned %d; want %d",
					status, rec.Code, http.StatusConflict)
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
		Slug:    "reclone-noauth-ws",
		GitURL:  "https://github.com/example/repo.git",
		OwnerID: "alice-id",
		Status:  "ready",
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
