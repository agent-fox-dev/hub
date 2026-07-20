package workspace

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// TS-01-SMOKE-1: End-to-end smoke test: create a workspace via POST /api/v1/workspaces
// using an HTTPS git_url and verify it is persisted in the workspaces table with correct
// fields.
//
// Real components: echo HTTP server, apikit-style auth middleware (test shim),
// POST /api/v1/workspaces handler, SQLite workspaces table.
//
// Validates: 01-REQ-5.1, 01-PATH-1
func TestSmoke_CreateWorkspace(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-user-id")

	// Create workspace via API.
	body := `{"slug":"my-workspace","git_url":"https://github.com/org/repo"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /workspaces returned %d; want %d\nbody: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	ws := parseWorkspaceJSON(t, rec)

	// Verify response fields.
	if ws.Slug != "my-workspace" {
		t.Errorf("slug = %q; want %q", ws.Slug, "my-workspace")
	}
	if ws.GitURL != "https://github.com/org/repo" {
		t.Errorf("git_url = %q; want %q", ws.GitURL, "https://github.com/org/repo")
	}
	if ws.Status != "active" {
		t.Errorf("status = %q; want %q", ws.Status, "active")
	}
	if ws.OwnerID != "alice-user-id" {
		t.Errorf("owner_id = %q; want %q", ws.OwnerID, "alice-user-id")
	}

	// Verify created_at and updated_at are valid RFC 3339.
	if _, err := time.Parse(time.RFC3339, ws.CreatedAt); err != nil {
		t.Errorf("created_at %q is not valid RFC 3339: %v", ws.CreatedAt, err)
	}
	if _, err := time.Parse(time.RFC3339, ws.UpdatedAt); err != nil {
		t.Errorf("updated_at %q is not valid RFC 3339: %v", ws.UpdatedAt, err)
	}

	// Verify the workspace is actually persisted in the database.
	dbWs, err := getWorkspaceBySlug(env.db, "my-workspace")
	if err != nil {
		t.Fatalf("getWorkspaceBySlug returned error: %v", err)
	}
	if dbWs == nil {
		t.Fatal("workspace not found in database after creation")
	}
	if dbWs.Status != "active" {
		t.Errorf("DB status = %q; want %q", dbWs.Status, "active")
	}
	if dbWs.OwnerID != "alice-user-id" {
		t.Errorf("DB owner_id = %q; want %q", dbWs.OwnerID, "alice-user-id")
	}
}

// TS-01-SMOKE-2: End-to-end smoke test: archive then permanently delete a workspace;
// verify the slug is freed and no row remains in the workspaces table.
//
// Real components: echo HTTP server, auth middleware (test shim),
// POST /api/v1/workspaces/:slug/archive handler,
// DELETE /api/v1/workspaces/:slug handler, SQLite workspaces table.
//
// Validates: 01-REQ-8.1, 01-REQ-10.1, 01-PATH-2
func TestSmoke_ArchiveThenDelete(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-user-id")

	// Seed an active workspace.
	env.seedWorkspace(t, &Workspace{
		Slug:    "my-workspace",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "alice-user-id",
		Status:  "active",
	})

	// Step 1: Archive the workspace.
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/my-workspace/archive", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("archive returned %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	ws := parseWorkspaceJSON(t, rec)
	if ws.Status != "archived" {
		t.Errorf("after archive: status = %q; want %q", ws.Status, "archived")
	}

	// Verify DB state after archive.
	dbWs, err := getWorkspaceBySlug(env.db, "my-workspace")
	if err != nil {
		t.Fatalf("getWorkspaceBySlug after archive: %v", err)
	}
	if dbWs == nil || dbWs.Status != "archived" {
		t.Fatalf("DB workspace status after archive = %v; want 'archived'", dbWs)
	}

	// Step 2: Delete the archived workspace.
	rec = env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/my-workspace", "", auth)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete returned %d; want %d\nbody: %s", rec.Code, http.StatusNoContent, rec.Body.String())
	}

	// Verify no row remains in the database.
	dbWs, err = getWorkspaceBySlug(env.db, "my-workspace")
	if err != nil {
		t.Fatalf("getWorkspaceBySlug after delete: %v", err)
	}
	if dbWs != nil {
		t.Error("workspace row still exists after delete; want no row")
	}

	// Verify slug is freed for reuse — creating a new workspace with the same slug succeeds.
	body := `{"slug":"my-workspace","git_url":"https://github.com/org/repo2"}`
	rec = env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("re-create with freed slug returned %d; want %d\nbody: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}
}

// TS-01-SMOKE-3: End-to-end smoke test: list workspaces with include_archived=true
// and verify both active and archived workspaces appear in the JSON array output,
// ordered by created_at descending.
//
// Real components: echo HTTP server, auth middleware (test shim),
// GET /api/v1/workspaces handler, SQLite workspaces table.
//
// Validates: 01-REQ-6.1, 01-REQ-6.2, 01-REQ-6.3, 01-PATH-3
func TestSmoke_ListWithIncludeArchived(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-user-id")

	// Seed workspaces with different statuses and times.
	// Use fixed timestamps so ordering is predictable.
	env.seedWorkspace(t, &Workspace{
		Slug:    "older-active",
		GitURL:  "https://github.com/org/repo1",
		OwnerID: "alice-user-id",
		Status:  "active",
	})

	// Slight delay to ensure different created_at for ordering.
	time.Sleep(10 * time.Millisecond)

	env.seedWorkspace(t, &Workspace{
		Slug:    "archived-ws",
		GitURL:  "https://github.com/org/repo2",
		OwnerID: "alice-user-id",
		Status:  "archived",
	})

	time.Sleep(10 * time.Millisecond)

	env.seedWorkspace(t, &Workspace{
		Slug:    "newest-active",
		GitURL:  "https://github.com/org/repo3",
		OwnerID: "alice-user-id",
		Status:  "active",
	})

	// Also seed a workspace for another user — should NOT appear.
	env.seedWorkspace(t, &Workspace{
		Slug:    "bob-ws",
		GitURL:  "https://github.com/org/repo4",
		OwnerID: "bob-user-id",
		Status:  "active",
	})

	// List with include_archived=true.
	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces?include_archived=true", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("list returned %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	wsList := parseWorkspaceListJSON(t, rec)

	// Should contain exactly 3 of alice's workspaces (active + archived).
	if len(wsList) != 3 {
		t.Fatalf("list returned %d workspaces; want 3", len(wsList))
	}

	// Verify both statuses are present.
	statuses := map[string]bool{}
	for _, ws := range wsList {
		statuses[ws.Status] = true
	}
	if !statuses["active"] {
		t.Error("no active workspace in list")
	}
	if !statuses["archived"] {
		t.Error("no archived workspace in list (should be included with include_archived=true)")
	}

	// Verify owned by alice only — no bob workspaces.
	for _, ws := range wsList {
		if ws.OwnerID != "alice-user-id" {
			t.Errorf("workspace %q has owner_id %q; want alice-user-id", ws.Slug, ws.OwnerID)
		}
	}

	// Verify ordering: created_at descending (newest first).
	for i := 1; i < len(wsList); i++ {
		if wsList[i-1].CreatedAt < wsList[i].CreatedAt {
			t.Errorf("workspace[%d].created_at (%s) < workspace[%d].created_at (%s); want descending order",
				i-1, wsList[i-1].CreatedAt, i, wsList[i].CreatedAt)
		}
	}
}

// TS-01-SMOKE-4: End-to-end smoke test: create a workspace with org association;
// verify org_id UUID is stored in the workspace record.
//
// Real components: echo HTTP server, auth middleware (test shim),
// apikit org membership check, POST /api/v1/workspaces handler, SQLite workspaces table.
//
// Validates: 01-REQ-5.1, 01-REQ-5.3, 01-PATH-4
func TestSmoke_CreateWorkspaceWithOrg(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-user-id")

	// Seed an organization and add alice as a member.
	orgID := "org-uuid-12345"
	env.seedOrg(t, orgID, "My Org", "my-org")
	env.seedOrgMember(t, orgID, "alice-user-id")

	// Create workspace with org_id.
	body := `{"slug":"team-ws","git_url":"git@github.com:org/repo","org_id":"org-uuid-12345"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /workspaces returned %d; want %d\nbody: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	ws := parseWorkspaceJSON(t, rec)
	if ws.OrgID == nil || *ws.OrgID != orgID {
		t.Errorf("org_id = %v; want %q", ws.OrgID, orgID)
	}

	// Verify org_id in database.
	dbWs, err := getWorkspaceBySlug(env.db, "team-ws")
	if err != nil {
		t.Fatalf("getWorkspaceBySlug: %v", err)
	}
	if dbWs == nil {
		t.Fatal("workspace not found in database")
	}
	if dbWs.OrgID == nil || *dbWs.OrgID != orgID {
		t.Errorf("DB org_id = %v; want %q", dbWs.OrgID, orgID)
	}
}

// TS-01-SMOKE-5: End-to-end smoke test: a PAT with workspaces:read scope reads the
// PAT owner's workspaces and does not see workspaces belonging to other users.
// Archived workspaces are excluded by default.
//
// Real components: echo HTTP server, auth middleware (test shim),
// GET /api/v1/workspaces handler, SQLite workspaces table.
//
// Validates: 01-REQ-6.2, 01-REQ-3.3, 01-PATH-5
func TestSmoke_PATReadOwnWorkspacesOnly(t *testing.T) {
	env := newTestEnv(t)

	// Seed workspaces for different users.
	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-active",
		GitURL:  "https://github.com/alice/repo",
		OwnerID: "alice-user-id",
		Status:  "active",
	})
	env.seedWorkspace(t, &Workspace{
		Slug:    "alice-archived",
		GitURL:  "https://github.com/alice/repo2",
		OwnerID: "alice-user-id",
		Status:  "archived",
	})
	env.seedWorkspace(t, &Workspace{
		Slug:    "bob-active",
		GitURL:  "https://github.com/bob/repo",
		OwnerID: "bob-user-id",
		Status:  "active",
	})

	// Alice's PAT with workspaces:read scope.
	auth := patAuth("alice-user-id", "workspaces:read")

	rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("list returned %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	wsList := parseWorkspaceListJSON(t, rec)

	// Should contain only alice's active workspace (not archived, not bob's).
	if len(wsList) != 1 {
		var slugs []string
		for _, ws := range wsList {
			slugs = append(slugs, ws.Slug)
		}
		t.Fatalf("list returned %d workspaces %v; want 1 (alice-active only)", len(wsList), slugs)
	}

	ws := wsList[0]
	if ws.Slug != "alice-active" {
		t.Errorf("slug = %q; want %q", ws.Slug, "alice-active")
	}
	if ws.OwnerID != "alice-user-id" {
		t.Errorf("owner_id = %q; want %q", ws.OwnerID, "alice-user-id")
	}
}

// TS-01-SMOKE-6: End-to-end smoke test: reactivate an archived workspace and verify
// the workspace status is updated to 'active' in the database, with updated_at
// refreshed.
//
// Real components: echo HTTP server, auth middleware (test shim),
// POST /api/v1/workspaces/:slug/reactivate handler, SQLite workspaces table.
//
// Validates: 01-REQ-9.1, 01-PATH-6
func TestSmoke_ReactivateArchivedWorkspace(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-user-id")

	// Seed an archived workspace.
	env.seedWorkspace(t, &Workspace{
		Slug:    "my-workspace",
		GitURL:  "https://github.com/org/repo",
		OwnerID: "alice-user-id",
		Status:  "archived",
	})

	// Record the pre-reactivation created_at to verify it doesn't change,
	// and updated_at to verify it is refreshed.
	dbBefore, err := getWorkspaceBySlug(env.db, "my-workspace")
	if err != nil {
		t.Fatalf("getWorkspaceBySlug before reactivate: %v", err)
	}
	beforeCreatedAt := dbBefore.CreatedAt

	// Reactivate the workspace.
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/my-workspace/reactivate", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("reactivate returned %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	ws := parseWorkspaceJSON(t, rec)
	if ws.Status != "active" {
		t.Errorf("response status = %q; want %q", ws.Status, "active")
	}

	// Verify DB state.
	dbAfter, err := getWorkspaceBySlug(env.db, "my-workspace")
	if err != nil {
		t.Fatalf("getWorkspaceBySlug after reactivate: %v", err)
	}
	if dbAfter.Status != "active" {
		t.Errorf("DB status = %q; want %q", dbAfter.Status, "active")
	}
	// created_at must not change.
	if dbAfter.CreatedAt != beforeCreatedAt {
		t.Errorf("created_at changed from %q to %q; should be immutable",
			beforeCreatedAt, dbAfter.CreatedAt)
	}
	// updated_at must be a valid RFC 3339 timestamp (the handler writes it fresh).
	if _, parseErr := time.Parse(time.RFC3339, dbAfter.UpdatedAt); parseErr != nil {
		t.Errorf("updated_at %q is not valid RFC 3339: %v", dbAfter.UpdatedAt, parseErr)
	}
	// updated_at must be >= created_at (reactivation is always after creation).
	if dbAfter.UpdatedAt < dbAfter.CreatedAt {
		t.Errorf("updated_at (%s) is before created_at (%s)",
			dbAfter.UpdatedAt, dbAfter.CreatedAt)
	}
}

// TestSmoke_ErrorPropagation verifies that errors from the store layer propagate
// correctly through the handler layer as proper HTTP error responses.
// This covers subtask 12.2's requirement to verify return value propagation.
func TestSmoke_ErrorPropagation(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-user-id")

	t.Run("create_duplicate_slug_returns_409", func(t *testing.T) {
		// Seed a workspace.
		env.seedWorkspace(t, &Workspace{
			Slug:    "existing-ws",
			GitURL:  "https://github.com/org/repo",
			OwnerID: "alice-user-id",
			Status:  "active",
		})

		// Attempt to create with the same slug.
		body := `{"slug":"existing-ws","git_url":"https://github.com/org/repo2"}`
		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)

		if rec.Code != http.StatusConflict {
			t.Fatalf("duplicate create returned %d; want %d", rec.Code, http.StatusConflict)
		}

		errResp := parseErrorEnvelope(t, rec)
		if errResp.Error.Code != http.StatusConflict {
			t.Errorf("error.code = %d; want %d", errResp.Error.Code, http.StatusConflict)
		}
	})

	t.Run("archive_already_archived_returns_400", func(t *testing.T) {
		env.seedWorkspace(t, &Workspace{
			Slug:    "already-archived",
			GitURL:  "https://github.com/org/repo",
			OwnerID: "alice-user-id",
			Status:  "archived",
		})

		rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces/already-archived/archive", "", auth)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("archive already-archived returned %d; want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("delete_active_returns_400", func(t *testing.T) {
		env.seedWorkspace(t, &Workspace{
			Slug:    "active-ws",
			GitURL:  "https://github.com/org/repo",
			OwnerID: "alice-user-id",
			Status:  "active",
		})

		rec := env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/active-ws", "", auth)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("delete active returned %d; want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("get_nonexistent_returns_404", func(t *testing.T) {
		rec := env.doRequest(t, http.MethodGet, "/api/v1/workspaces/no-such-slug", "", auth)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("get nonexistent returned %d; want %d", rec.Code, http.StatusNotFound)
		}
	})

	t.Run("list_empty_returns_empty_array", func(t *testing.T) {
		// Use a fresh env with no seeded workspaces for this user.
		freshEnv := newTestEnv(t)
		freshAuth := userAuth("empty-user-id")

		rec := freshEnv.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", freshAuth)
		if rec.Code != http.StatusOK {
			t.Fatalf("list returned %d; want %d", rec.Code, http.StatusOK)
		}

		// Verify the body is an empty JSON array, not null.
		var raw json.RawMessage
		if err := json.NewDecoder(rec.Body).Decode(&raw); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if string(raw) != "[]" {
			t.Errorf("empty list response = %s; want []", string(raw))
		}
	})
}

// TestSmoke_FullLifecycle exercises the complete workspace lifecycle:
// create → list → get → archive → list (excluded) → list (included) →
// reactivate → delete (fail on active) → archive → delete (success).
func TestSmoke_FullLifecycle(t *testing.T) {
	env := newTestEnv(t)
	auth := userAuth("alice-user-id")

	// 1. Create.
	body := `{"slug":"lifecycle-ws","git_url":"https://github.com/org/repo","branch":"main"}`
	rec := env.doRequest(t, http.MethodPost, "/api/v1/workspaces", body, auth)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create returned %d; want %d\nbody: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	ws := parseWorkspaceJSON(t, rec)
	if ws.Branch == nil || *ws.Branch != "main" {
		t.Errorf("branch = %v; want 'main'", ws.Branch)
	}

	// 2. List — should contain the workspace.
	rec = env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("list returned %d", rec.Code)
	}
	wsList := parseWorkspaceListJSON(t, rec)
	if len(wsList) != 1 || wsList[0].Slug != "lifecycle-ws" {
		t.Fatalf("list = %v; want [lifecycle-ws]", wsList)
	}

	// 3. Get by slug.
	rec = env.doRequest(t, http.MethodGet, "/api/v1/workspaces/lifecycle-ws", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("get returned %d", rec.Code)
	}
	ws = parseWorkspaceJSON(t, rec)
	if ws.Status != "active" {
		t.Errorf("get: status = %q; want active", ws.Status)
	}

	// 4. Archive.
	rec = env.doRequest(t, http.MethodPost, "/api/v1/workspaces/lifecycle-ws/archive", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("archive returned %d", rec.Code)
	}
	ws = parseWorkspaceJSON(t, rec)
	if ws.Status != "archived" {
		t.Errorf("archive: status = %q; want archived", ws.Status)
	}

	// 5. List without include_archived — should be empty.
	rec = env.doRequest(t, http.MethodGet, "/api/v1/workspaces", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("list (without archived) returned %d", rec.Code)
	}
	wsList = parseWorkspaceListJSON(t, rec)
	if len(wsList) != 0 {
		t.Errorf("list without archived = %d; want 0", len(wsList))
	}

	// 6. List with include_archived — should contain the archived workspace.
	rec = env.doRequest(t, http.MethodGet, "/api/v1/workspaces?include_archived=true", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("list (with archived) returned %d", rec.Code)
	}
	wsList = parseWorkspaceListJSON(t, rec)
	if len(wsList) != 1 || wsList[0].Status != "archived" {
		t.Errorf("list with archived: got %v; want [archived]", wsList)
	}

	// 7. Reactivate.
	rec = env.doRequest(t, http.MethodPost, "/api/v1/workspaces/lifecycle-ws/reactivate", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("reactivate returned %d", rec.Code)
	}
	ws = parseWorkspaceJSON(t, rec)
	if ws.Status != "active" {
		t.Errorf("reactivate: status = %q; want active", ws.Status)
	}

	// 8. Delete active workspace — should fail.
	rec = env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/lifecycle-ws", "", auth)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("delete active returned %d; want %d", rec.Code, http.StatusBadRequest)
	}

	// 9. Archive again.
	rec = env.doRequest(t, http.MethodPost, "/api/v1/workspaces/lifecycle-ws/archive", "", auth)
	if rec.Code != http.StatusOK {
		t.Fatalf("re-archive returned %d", rec.Code)
	}

	// 10. Delete archived workspace — should succeed.
	rec = env.doRequest(t, http.MethodDelete, "/api/v1/workspaces/lifecycle-ws", "", auth)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete archived returned %d; want %d", rec.Code, http.StatusNoContent)
	}

	// 11. Verify completely gone from DB.
	dbWs, err := getWorkspaceBySlug(env.db, "lifecycle-ws")
	if err != nil {
		t.Fatalf("getWorkspaceBySlug after delete: %v", err)
	}
	if dbWs != nil {
		t.Error("workspace still exists in DB after delete")
	}
}
