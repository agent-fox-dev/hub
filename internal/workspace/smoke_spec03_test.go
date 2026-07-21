package workspace

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// TS-03-SMOKE-1: End-to-end smoke test: PAT with workspaces:write updates
// workspace display_name via API (matching the CLI→API path).
//
// Real components: echo HTTP server, test auth middleware, workspace update
// handler (PATCH /api/v1/workspaces/:slug), SQLite workspaces table.
//
// Validates: 03-REQ-4.1, 03-REQ-2.1, 03-REQ-7.1, 03-PATH-1
func TestSmoke03_PATWriteUpdatesDisplayName(t *testing.T) {
	env := newTestEnv(t)

	// Seed an active workspace owned by alice.
	env.seedWorkspace(t, &Workspace{
		Slug:        "my-project",
		GitURL:      "https://github.com/org/repo",
		OwnerID:     "alice-user-id",
		Status:      "active",
		DisplayName: "my-project",
		Description: "",
	})

	// Record pre-update state.
	dbBefore, err := getWorkspaceBySlug(env.db, "my-project")
	if err != nil {
		t.Fatalf("getWorkspaceBySlug before update: %v", err)
	}
	beforeUpdatedAt := dbBefore.UpdatedAt

	// Small sleep to ensure updated_at advances (nano precision).
	time.Sleep(10 * time.Millisecond)

	// PAT with workspaces:write scope.
	auth := patAuth("alice-user-id", "workspaces:write")

	// Send PATCH with display_name update.
	body := `{"display_name": "My Project"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/my-project", body, auth)

	// Expect HTTP 200 with full workspace JSON.
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH returned %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	ws := parseWorkspaceJSON(t, rec)

	// Verify display_name was updated in the response.
	if ws.DisplayName != "My Project" {
		t.Errorf("response display_name = %q; want %q", ws.DisplayName, "My Project")
	}

	// Verify slug is unchanged (immutable).
	if ws.Slug != "my-project" {
		t.Errorf("response slug = %q; want %q", ws.Slug, "my-project")
	}

	// Verify updated_at advanced.
	if ws.UpdatedAt <= beforeUpdatedAt {
		t.Errorf("updated_at %q did not advance past %q", ws.UpdatedAt, beforeUpdatedAt)
	}

	// Verify the DB row was updated.
	dbAfter, err := getWorkspaceBySlug(env.db, "my-project")
	if err != nil {
		t.Fatalf("getWorkspaceBySlug after update: %v", err)
	}
	if dbAfter.DisplayName != "My Project" {
		t.Errorf("DB display_name = %q; want %q", dbAfter.DisplayName, "My Project")
	}
	if dbAfter.UpdatedAt <= beforeUpdatedAt {
		t.Errorf("DB updated_at %q did not advance past %q", dbAfter.UpdatedAt, beforeUpdatedAt)
	}

	// Verify the full response contains all expected fields by re-fetching.
	getRec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/my-project", "",
		patAuth("alice-user-id", "workspaces:write"))
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET after update returned %d; want %d", getRec.Code, http.StatusOK)
	}
	var raw map[string]any
	if err := json.Unmarshal(getRec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("failed to parse GET response as JSON: %v", err)
	}
	requiredFields := []string{"slug", "git_url", "owner_id", "status", "display_name", "description", "created_at", "updated_at"}
	for _, f := range requiredFields {
		if _, ok := raw[f]; !ok {
			t.Errorf("response missing required field %q", f)
		}
	}
	if dn, ok := raw["display_name"].(string); !ok || dn != "My Project" {
		t.Errorf("GET display_name = %v; want %q", raw["display_name"], "My Project")
	}
}

// TS-03-SMOKE-2: End-to-end smoke test: PAT with workspaces:delete deletes an
// archived workspace by knowing the slug directly (no workspaces:read needed).
//
// Real components: echo HTTP server, test auth middleware, workspace delete
// handler (DELETE /api/v1/workspaces/:slug), SQLite workspaces table.
//
// Validates: 03-REQ-3.5, 03-PATH-2
func TestSmoke03_PATDeleteArchived(t *testing.T) {
	env := newTestEnv(t)

	// Seed an archived workspace owned by alice.
	env.seedWorkspace(t, &Workspace{
		Slug:        "old-project",
		GitURL:      "https://github.com/org/old-repo",
		OwnerID:     "alice-user-id",
		Status:      "archived",
		DisplayName: "old-project",
		Description: "",
	})

	// PAT with ONLY workspaces:delete scope (no read scope).
	auth := patAuth("alice-user-id", "workspaces:delete")

	// Delete the archived workspace.
	rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/old-project", "", auth)

	// Expect HTTP 204 No Content (errata: spec says 200, actual implementation returns 204).
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE returned %d; want %d\nbody: %s", rec.Code, http.StatusNoContent, rec.Body.String())
	}

	// Verify workspace row is removed from the database.
	dbWs, err := getWorkspaceBySlug(env.db, "old-project")
	if err != nil {
		t.Fatalf("getWorkspaceBySlug after delete: %v", err)
	}
	if dbWs != nil {
		t.Error("workspace row still exists in DB after delete; want no row")
	}

	// Verify that the PAT with only workspaces:delete CANNOT list workspaces
	// (anti-enumeration: delete scope does not imply read).
	rec = env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", auth)
	if rec.Code != http.StatusNotFound {
		t.Errorf("list with delete-only PAT returned %d; want %d (anti-enumeration)",
			rec.Code, http.StatusNotFound)
	}
}

// TS-03-SMOKE-3: End-to-end smoke test: Workspace creation with display_name and
// description fields provided in the POST body.
//
// Real components: echo HTTP server, test auth middleware, workspace creation
// handler (POST /api/v1/workspaces), SQLite workspaces table.
//
// Validates: 03-REQ-6.1, 03-PATH-3
func TestSmoke03_CreateWithDisplayNameAndDescription(t *testing.T) {
	env := newTestEnv(t)

	// PAT with workspaces:create scope.
	auth := patAuth("alice-user-id", "workspaces:create")

	// Create workspace with display_name and description.
	body := `{"slug":"new-project","git_url":"https://github.com/org/new-repo","display_name":"My Project","description":"A test workspace"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	// Expect HTTP 201.
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST returned %d; want %d\nbody: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	ws := parseWorkspaceJSON(t, rec)

	// Verify display_name and description in response.
	if ws.DisplayName != "My Project" {
		t.Errorf("display_name = %q; want %q", ws.DisplayName, "My Project")
	}
	if ws.Description != "A test workspace" {
		t.Errorf("description = %q; want %q", ws.Description, "A test workspace")
	}

	// Verify slug.
	if ws.Slug != "new-project" {
		t.Errorf("slug = %q; want %q", ws.Slug, "new-project")
	}

	// Verify status.
	if ws.Status != "active" {
		t.Errorf("status = %q; want %q", ws.Status, "active")
	}

	// Verify the DB row was created with the correct fields.
	dbWs, err := getWorkspaceBySlug(env.db, "new-project")
	if err != nil {
		t.Fatalf("getWorkspaceBySlug: %v", err)
	}
	if dbWs == nil {
		t.Fatal("workspace not found in database after creation")
	}
	if dbWs.DisplayName != "My Project" {
		t.Errorf("DB display_name = %q; want %q", dbWs.DisplayName, "My Project")
	}
	if dbWs.Description != "A test workspace" {
		t.Errorf("DB description = %q; want %q", dbWs.Description, "A test workspace")
	}
}

// TS-03-SMOKE-4: End-to-end smoke test: PATCH on an archived workspace is rejected
// with HTTP 400, and no changes are written to the database.
//
// Real components: echo HTTP server, test auth middleware, workspace update
// handler (PATCH /api/v1/workspaces/:slug), SQLite workspaces table.
//
// Validates: 03-REQ-4.E2, 03-PATH-4
func TestSmoke03_PatchArchivedRejected(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-user-id")

	// Seed an archived workspace.
	env.seedWorkspace(t, &Workspace{
		Slug:        "archived-ws",
		GitURL:      "https://github.com/org/repo",
		OwnerID:     "alice-user-id",
		Status:      "archived",
		DisplayName: "archived-ws",
		Description: "original description",
	})

	// Record pre-update state.
	dbBefore, err := getWorkspaceBySlug(env.db, "archived-ws")
	if err != nil {
		t.Fatalf("getWorkspaceBySlug before PATCH: %v", err)
	}

	// Attempt to update the archived workspace.
	body := `{"description": "new text"}`
	rec := env.doRequest(t, http.MethodPatch, "/api/v1/workspaces/archived-ws", body, auth)

	// Expect HTTP 400.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PATCH on archived returned %d; want %d\nbody: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	// Verify error envelope.
	errResp := parseErrorEnvelope(t, rec)
	if errResp.Error.Code != http.StatusBadRequest {
		t.Errorf("error.code = %d; want %d", errResp.Error.Code, http.StatusBadRequest)
	}

	// Verify no changes were written to the database.
	dbAfter, err := getWorkspaceBySlug(env.db, "archived-ws")
	if err != nil {
		t.Fatalf("getWorkspaceBySlug after PATCH: %v", err)
	}
	if dbAfter == nil {
		t.Fatal("workspace disappeared from DB after rejected PATCH")
	}
	if dbAfter.Description != dbBefore.Description {
		t.Errorf("description changed from %q to %q; expected no change",
			dbBefore.Description, dbAfter.Description)
	}
	if dbAfter.UpdatedAt != dbBefore.UpdatedAt {
		t.Errorf("updated_at changed from %q to %q; expected no change",
			dbBefore.UpdatedAt, dbAfter.UpdatedAt)
	}
	if dbAfter.Status != "archived" {
		t.Errorf("status changed to %q; expected 'archived'", dbAfter.Status)
	}
}
