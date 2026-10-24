package workspace

import (
	"net/http"
	"strings"
	"testing"
)

// ========================================================================
// Spec 13 Task 1.3: Sync precondition validation
// (TS-13-7, TS-13-8, TS-13-9, TS-13-10, TS-13-11, 13-REQ-3.E1)
// Requirements: 13-REQ-3
// ========================================================================

// TS-13-7: Verifies that POST /sync on a workspace with status='archived'
// returns HTTP 400 without performing git operations.
// Requirement: 13-REQ-3.1
func TestSyncPrecondition_ArchivedWorkspace(t *testing.T) {
	env := newTestEnv(t)

	// Seed an archived workspace.
	env.seedWorkspace(t, &Workspace{
		Slug:    "archived-ws",
		GitURL:  "https://github.com/example/repo.git",
		OwnerID: "alice-id",
		Status:  "archived",
	})

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/archived-ws/sync", "", auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST /sync on archived workspace returned %d; want %d",
			rec.Code, http.StatusBadRequest)
	}

	envelope := parseErrorEnvelope(t, rec)
	want := "workspace is not ready; sync requires a ready workspace"
	if envelope.Error.Message != want {
		t.Errorf("error.message = %q; want %q", envelope.Error.Message, want)
	}

	// Verify the sync fields were NOT touched by the rejected request.
	assertSyncUntouched(t, env, "archived-ws")
}

// assertSyncUntouched checks that no sync outcome was recorded on the
// workspace: last_sync_at and sync_error are still NULL.
func assertSyncUntouched(t *testing.T, env *testEnv, slug string) {
	t.Helper()
	var lastSyncAt, syncError *string
	err := env.db.QueryRow(
		`SELECT last_sync_at, sync_error FROM workspaces WHERE slug = ?`, slug,
	).Scan(&lastSyncAt, &syncError)
	if err != nil {
		t.Fatalf("failed to query sync fields: %v", err)
	}
	if lastSyncAt != nil {
		t.Errorf("last_sync_at = %q; want NULL (should not change on rejected request)", *lastSyncAt)
	}
	if syncError != nil {
		t.Errorf("sync_error = %q; want NULL (should not change on rejected request)", *syncError)
	}
}

// TS-13-8: Verifies that POST /sync on a workspace with status='cloning'
// returns HTTP 400 without performing git operations.
// Requirement: 13-REQ-3.2
func TestSyncPrecondition_CloningWorkspace(t *testing.T) {
	env := newTestEnv(t)

	// Seed a workspace with status='cloning' (reclone in progress).
	env.seedWorkspace(t, &Workspace{
		Slug:    "cloning-ws",
		GitURL:  "https://github.com/example/repo.git",
		OwnerID: "alice-id",
		Status:  "cloning",
	})

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/cloning-ws/sync", "", auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST /sync on cloning workspace returned %d; want %d",
			rec.Code, http.StatusBadRequest)
	}

	envelope := parseErrorEnvelope(t, rec)
	want := "workspace is not ready; sync requires a ready workspace"
	if envelope.Error.Message != want {
		t.Errorf("error.message = %q; want %q", envelope.Error.Message, want)
	}
	assertSyncUntouched(t, env, "cloning-ws")
}

// TS-13-9: Verifies that POST /sync on a workspace whose sync_enabled setting
// is false returns HTTP 400 with a descriptive error and no git operations
// are performed.
// Requirement: 13-REQ-3.3
func TestSyncPrecondition_SyncDisabled(t *testing.T) {
	env := newTestEnv(t)

	// Seed a ready workspace.
	env.seedWorkspace(t, &Workspace{
		Slug:    "disabled-sync-ws",
		GitURL:  "https://github.com/example/repo.git",
		OwnerID: "alice-id",
		Status:  "ready",
	})

	// Disable sync through the settings document.
	_, err := env.db.Exec(
		`UPDATE workspaces SET settings = json_set(settings, '$.sync_enabled', json('false')) WHERE slug = ?`,
		"disabled-sync-ws",
	)
	if err != nil {
		t.Fatalf("failed to set sync_enabled=false: %v", err)
	}

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/disabled-sync-ws/sync", "", auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST /sync on disabled workspace returned %d; want %d",
			rec.Code, http.StatusBadRequest)
	}

	envelope := parseErrorEnvelope(t, rec)
	if envelope.Error.Message == "" {
		t.Error("error.message is empty; want non-empty message about sync being disabled")
	}
	if !strings.Contains(strings.ToLower(envelope.Error.Message), "disabled") {
		t.Errorf("error message %q should mention 'disabled'", envelope.Error.Message)
	}
	want := "sync is disabled for this workspace; set the sync_enabled setting to true to enable"
	if envelope.Error.Message != want {
		t.Errorf("error.message = %q; want %q", envelope.Error.Message, want)
	}

	// Verify the sync fields were NOT touched by the rejected request.
	assertSyncUntouched(t, env, "disabled-sync-ws")
}

// TS-13-10: Verifies that POST /sync on a workspace whose lock is held by a
// sync in progress returns HTTP 409 with error_type workspace_busy.
// Requirement: 13-REQ-3.4
func TestSyncPrecondition_AlreadySyncing(t *testing.T) {
	env := newTestEnv(t)

	// Seed a ready workspace.
	env.seedWorkspace(t, &Workspace{
		Slug:    "syncing-ws",
		GitURL:  "https://github.com/example/repo.git",
		OwnerID: "alice-id",
		Status:  "ready",
	})

	// Hold the workspace lock to simulate an in-progress sync.
	holdLock(t, "syncing-ws")

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/syncing-ws/sync", "", auth)

	if rec.Code != http.StatusConflict {
		t.Errorf("POST /sync on syncing workspace returned %d; want %d",
			rec.Code, http.StatusConflict)
	}

	envelope := parseErrorEnvelope(t, rec)
	if envelope.Error.ErrorType != "workspace_busy" {
		t.Errorf("error.error_type = %q; want %q", envelope.Error.ErrorType, "workspace_busy")
	}
	if envelope.Error.Message == "" {
		t.Error("error.message is empty; want non-empty message about sync already in progress")
	}
	assertSyncUntouched(t, env, "syncing-ws")
}

// TS-13-11: Verifies that POST /sync for a non-existent workspace slug
// returns HTTP 404.
// Requirement: 13-REQ-3.5
func TestSyncPrecondition_NonexistentWorkspace(t *testing.T) {
	env := newTestEnv(t)

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/nonexistent-ws/sync", "", auth)

	if rec.Code != http.StatusNotFound {
		t.Errorf("POST /sync on nonexistent workspace returned %d; want %d",
			rec.Code, http.StatusNotFound)
	}

	// Verify the error response follows the apikit error envelope format.
	envelope := parseErrorEnvelope(t, rec)
	if envelope.Error.Code != http.StatusNotFound {
		t.Errorf("error.code = %d; want %d", envelope.Error.Code, http.StatusNotFound)
	}
	if envelope.Error.Message == "" {
		t.Error("error.message is empty; want non-empty message about workspace not found")
	}
}

// 13-REQ-3.E1: Verifies that a caller without workspaces:write scope receives
// HTTP 403 before any workspace state is checked.
func TestSyncPrecondition_MissingSyncScope(t *testing.T) {
	env := newTestEnv(t)

	// Seed a valid workspace that would pass all preconditions if auth were OK.
	env.seedWorkspace(t, &Workspace{
		Slug:    "scope-test-ws",
		GitURL:  "https://github.com/example/repo.git",
		OwnerID: "alice-id",
		Status:  "ready",
	})

	// Use a PAT that has workspaces:read but NOT workspaces:write.
	auth := patAuth("alice-id", "workspaces:read")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/scope-test-ws/sync", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Errorf("POST /sync without workspaces:write scope returned %d; want %d",
			rec.Code, http.StatusForbidden)
	}

	envelope := parseErrorEnvelope(t, rec)
	if envelope.Error.Message == "" {
		t.Error("error.message is empty; want non-empty message about missing scope")
	}
}
