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

	// Seed an archived workspace with clone_status='ready'.
	env.seedWorkspace(t, &Workspace{
		Slug:        "archived-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "archived",
		CloneStatus: "ready",
	})

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/archived-ws/sync", "", auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST /sync on archived workspace returned %d; want %d",
			rec.Code, http.StatusBadRequest)
	}

	envelope := parseErrorEnvelope(t, rec)
	if envelope.Error.Message == "" {
		t.Error("error.message is empty; want non-empty message about inactive workspace")
	}

	// Verify sync_status was NOT changed (should remain 'idle').
	var syncStatus string
	err := env.db.QueryRow(
		`SELECT sync_status FROM workspaces WHERE slug = ?`, "archived-ws",
	).Scan(&syncStatus)
	if err != nil {
		t.Fatalf("failed to query sync_status: %v", err)
	}
	if syncStatus != "idle" {
		t.Errorf("sync_status = %q; want %q (should not change on rejected request)", syncStatus, "idle")
	}
}

// TS-13-8: Verifies that POST /sync on a workspace with clone_status='cloning'
// returns HTTP 400 without performing git operations.
// Requirement: 13-REQ-3.2
func TestSyncPrecondition_CloningWorkspace(t *testing.T) {
	env := newTestEnv(t)

	// Seed a workspace with clone_status='cloning' (reclone in progress).
	env.seedWorkspace(t, &Workspace{
		Slug:        "cloning-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "cloning",
	})

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/cloning-ws/sync", "", auth)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST /sync on cloning workspace returned %d; want %d",
			rec.Code, http.StatusBadRequest)
	}

	envelope := parseErrorEnvelope(t, rec)
	if envelope.Error.Message == "" {
		t.Error("error.message is empty; want non-empty message about clone not ready")
	}
}

// TS-13-9: Verifies that POST /sync on a workspace with sync_mode='disabled'
// returns HTTP 400 with a descriptive error and no git operations are performed.
// Requirement: 13-REQ-3.3
func TestSyncPrecondition_DisabledSyncMode(t *testing.T) {
	env := newTestEnv(t)

	// Seed workspace with status='active' and clone_status='ready'.
	env.seedWorkspace(t, &Workspace{
		Slug:        "disabled-sync-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	// Set sync_mode to 'disabled'. This will fail if the sync_mode column
	// does not exist yet (expected failure before schema migration).
	_, err := env.db.Exec(
		`UPDATE workspaces SET sync_mode = 'disabled' WHERE slug = ?`,
		"disabled-sync-ws",
	)
	if err != nil {
		t.Fatalf("failed to set sync_mode='disabled': %v", err)
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

	// Verify sync_status was NOT changed (should remain 'idle').
	var syncStatus string
	err = env.db.QueryRow(
		`SELECT sync_status FROM workspaces WHERE slug = ?`, "disabled-sync-ws",
	).Scan(&syncStatus)
	if err != nil {
		t.Fatalf("failed to query sync_status: %v", err)
	}
	if syncStatus != "idle" {
		t.Errorf("sync_status = %q; want %q (should not change on rejected request)", syncStatus, "idle")
	}
}

// TS-13-10: Verifies that POST /sync on a workspace already in
// sync_status='syncing' returns HTTP 409.
// Requirement: 13-REQ-3.4
func TestSyncPrecondition_AlreadySyncing(t *testing.T) {
	env := newTestEnv(t)

	// Seed workspace with status='active' and clone_status='ready'.
	env.seedWorkspace(t, &Workspace{
		Slug:        "syncing-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	// Set sync_status to 'syncing' to simulate an in-progress sync.
	// This will fail if the sync_status column does not exist yet.
	_, err := env.db.Exec(
		`UPDATE workspaces SET sync_status = 'syncing' WHERE slug = ?`,
		"syncing-ws",
	)
	if err != nil {
		t.Fatalf("failed to set sync_status='syncing': %v", err)
	}

	auth := adminAuth()
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/syncing-ws/sync", "", auth)

	if rec.Code != http.StatusConflict {
		t.Errorf("POST /sync on syncing workspace returned %d; want %d",
			rec.Code, http.StatusConflict)
	}

	envelope := parseErrorEnvelope(t, rec)
	if envelope.Error.Message == "" {
		t.Error("error.message is empty; want non-empty message about sync already in progress")
	}
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

// 13-REQ-3.E1: Verifies that a caller without workspaces:sync scope receives
// HTTP 403 before any workspace state is checked.
func TestSyncPrecondition_MissingSyncScope(t *testing.T) {
	env := newTestEnv(t)

	// Seed a valid workspace that would pass all preconditions if auth were OK.
	env.seedWorkspace(t, &Workspace{
		Slug:        "scope-test-ws",
		GitURL:      "https://github.com/example/repo.git",
		OwnerID:     "alice-id",
		Status:      "active",
		CloneStatus: "ready",
	})

	// Use a PAT that has workspaces:read but NOT workspaces:sync.
	auth := patAuth("alice-id", "workspaces:read")
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/scope-test-ws/sync", "", auth)

	if rec.Code != http.StatusForbidden {
		t.Errorf("POST /sync without workspaces:sync scope returned %d; want %d",
			rec.Code, http.StatusForbidden)
	}

	envelope := parseErrorEnvelope(t, rec)
	if envelope.Error.Message == "" {
		t.Error("error.message is empty; want non-empty message about missing scope")
	}
}
