package integration_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/agent-fox/af-hub/internal/store"
)

// --- Constants for workspace handler tests ---

const testAdminTokenWS = "af_admin_workspace_handler_test"

// --- TS-02-21: POST /api/v1/workspaces validates slug and URL, creates workspace ---

// TS-02-21: Verify that POST /api/v1/workspaces validates slug and URL
// formats, creates the workspace, and returns HTTP 201.
func TestWorkspaceHandler_CreateWorkspace_ReturnsCreated(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenWS)

	body := `{
		"name": "My Workspace",
		"slug": "my-workspace",
		"url": "https://myworkspace.example.com"
	}`

	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/workspaces", body,
		adminHeaders(testAdminTokenWS))

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/workspaces: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var ws workspaceResponse
	parseJSON(t, rec, &ws)

	if ws.Name != "My Workspace" {
		t.Errorf("name = %q, want %q", ws.Name, "My Workspace")
	}
	if ws.Slug != "my-workspace" {
		t.Errorf("slug = %q, want %q", ws.Slug, "my-workspace")
	}
	if ws.ID == "" {
		t.Error("id should be non-empty")
	}
}

// TS-02-21: Verify the workspace is persisted in the database.
func TestWorkspaceHandler_CreateWorkspace_PersistsInDB(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenWS)

	body := `{
		"name": "Persist WS",
		"slug": "persist-ws",
		"url": "https://persist.example.com"
	}`

	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/workspaces", body,
		adminHeaders(testAdminTokenWS))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d\nBody: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var ws workspaceResponse
	parseJSON(t, rec, &ws)

	dbWs, err := env.Store.GetWorkspaceByID(ws.ID)
	if err != nil {
		t.Fatalf("GetWorkspaceByID(%q) failed: %v", ws.ID, err)
	}
	if dbWs.Name != "Persist WS" {
		t.Errorf("DB name = %q, want %q", dbWs.Name, "Persist WS")
	}
	if dbWs.Slug != "persist-ws" {
		t.Errorf("DB slug = %q, want %q", dbWs.Slug, "persist-ws")
	}
}

// TS-02-21: Verify slug format with valid 3-character slug.
func TestWorkspaceHandler_CreateWorkspace_MinSlugLength(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenWS)

	body := `{
		"name": "Short Slug WS",
		"slug": "abc",
		"url": "https://short.example.com"
	}`

	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/workspaces", body,
		adminHeaders(testAdminTokenWS))

	if rec.Code != http.StatusCreated {
		t.Errorf("3-char slug: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}
}

// --- TS-02-22: GET /api/v1/workspaces excludes/includes archived ---

// TS-02-22: Verify that GET /api/v1/workspaces excludes archived workspaces
// by default and includes them when include_archived=true.
func TestWorkspaceHandler_ListWorkspaces_ExcludesArchivedByDefault(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenWS)

	// Create two active workspaces and one archived.
	_, err := env.Store.CreateWorkspace(&store.Workspace{
		Name:   "Active WS 1",
		Slug:   "active-ws-one",
		URL:    "https://active1.example.com",
		Status: "active",
	})
	if err != nil {
		t.Fatalf("failed to create workspace 1: %v", err)
	}

	_, err = env.Store.CreateWorkspace(&store.Workspace{
		Name:   "Active WS 2",
		Slug:   "active-ws-two",
		URL:    "https://active2.example.com",
		Status: "active",
	})
	if err != nil {
		t.Fatalf("failed to create workspace 2: %v", err)
	}

	_, err = env.Store.CreateWorkspace(&store.Workspace{
		Name:   "Archived WS",
		Slug:   "archived-ws",
		URL:    "https://archived.example.com",
		Status: "archived",
	})
	if err != nil {
		t.Fatalf("failed to create archived workspace: %v", err)
	}

	// Without include_archived — should return only active workspaces.
	rec1 := doRequest(env.Echo, http.MethodGet, "/api/v1/workspaces", "",
		adminHeaders(testAdminTokenWS))

	if rec1.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/workspaces: status = %d, want %d\nBody: %s",
			rec1.Code, http.StatusOK, rec1.Body.String())
	}

	var activeOnly []workspaceResponse
	parseJSON(t, rec1, &activeOnly)

	if len(activeOnly) != 2 {
		t.Errorf("without include_archived: len = %d, want 2", len(activeOnly))
	}

	// Verify none are archived.
	for _, ws := range activeOnly {
		if ws.Status == "archived" {
			t.Errorf("workspace %q should not be archived in default listing", ws.Name)
		}
	}
}

// TS-02-22: Verify that include_archived=true includes archived workspaces.
func TestWorkspaceHandler_ListWorkspaces_IncludesArchivedWhenRequested(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenWS)

	// Create two active and one archived workspace.
	_, _ = env.Store.CreateWorkspace(&store.Workspace{
		Name: "Active A", Slug: "active-a", URL: "https://a.example.com", Status: "active",
	})
	_, _ = env.Store.CreateWorkspace(&store.Workspace{
		Name: "Active B", Slug: "active-b", URL: "https://b.example.com", Status: "active",
	})
	_, _ = env.Store.CreateWorkspace(&store.Workspace{
		Name: "Archived C", Slug: "archived-c", URL: "https://c.example.com", Status: "archived",
	})

	// With include_archived=true.
	rec2 := doRequest(env.Echo, http.MethodGet,
		"/api/v1/workspaces?include_archived=true", "",
		adminHeaders(testAdminTokenWS))

	if rec2.Code != http.StatusOK {
		t.Fatalf("GET with include_archived: status = %d, want %d\nBody: %s",
			rec2.Code, http.StatusOK, rec2.Body.String())
	}

	var allWS []workspaceResponse
	parseJSON(t, rec2, &allWS)

	if len(allWS) != 3 {
		t.Errorf("with include_archived=true: len = %d, want 3", len(allWS))
	}
}

// --- TS-02-23: POST /api/v1/workspaces/:id/archive ---

// TS-02-23: Verify that POST /api/v1/workspaces/:id/archive marks an active
// workspace as archived and returns HTTP 200.
func TestWorkspaceHandler_ArchiveWorkspace_Success(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenWS)

	ws, err := env.Store.CreateWorkspace(&store.Workspace{
		Name:   "To Archive",
		Slug:   "to-archive",
		URL:    "https://archive-me.example.com",
		Status: "active",
	})
	if err != nil {
		t.Fatalf("failed to create workspace: %v", err)
	}

	rec := doRequest(env.Echo, http.MethodPost,
		fmt.Sprintf("/api/v1/workspaces/%s/archive", ws.ID), "",
		adminHeaders(testAdminTokenWS))

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/workspaces/%s/archive: status = %d, want %d\nBody: %s",
			ws.ID, rec.Code, http.StatusOK, rec.Body.String())
	}

	var updatedWS workspaceResponse
	parseJSON(t, rec, &updatedWS)

	// Per reviewer finding: workspace archival uses status TEXT column.
	// We accept either status="archived" representation.
	if updatedWS.Status != "archived" {
		t.Errorf("status = %q, want %q", updatedWS.Status, "archived")
	}

	// Verify DB state.
	dbWs, err := env.Store.GetWorkspaceByID(ws.ID)
	if err != nil {
		t.Fatalf("GetWorkspaceByID failed: %v", err)
	}
	if dbWs.Status != "archived" {
		t.Errorf("DB status = %q, want %q", dbWs.Status, "archived")
	}
}

// --- TS-02-24: POST /api/v1/workspaces/:id/reactivate ---

// TS-02-24: Verify that POST /api/v1/workspaces/:id/reactivate marks an
// archived workspace as active and returns HTTP 200.
func TestWorkspaceHandler_ReactivateWorkspace_Success(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenWS)

	ws, err := env.Store.CreateWorkspace(&store.Workspace{
		Name:   "To Reactivate",
		Slug:   "to-reactivate",
		URL:    "https://reactivate-me.example.com",
		Status: "archived",
	})
	if err != nil {
		t.Fatalf("failed to create workspace: %v", err)
	}

	rec := doRequest(env.Echo, http.MethodPost,
		fmt.Sprintf("/api/v1/workspaces/%s/reactivate", ws.ID), "",
		adminHeaders(testAdminTokenWS))

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/workspaces/%s/reactivate: status = %d, want %d\nBody: %s",
			ws.ID, rec.Code, http.StatusOK, rec.Body.String())
	}

	var updatedWS workspaceResponse
	parseJSON(t, rec, &updatedWS)

	// Per reviewer finding: use status string representation.
	if updatedWS.Status != "active" {
		t.Errorf("status = %q, want %q", updatedWS.Status, "active")
	}

	// Verify DB state.
	dbWs, err := env.Store.GetWorkspaceByID(ws.ID)
	if err != nil {
		t.Fatalf("GetWorkspaceByID failed: %v", err)
	}
	if dbWs.Status != "active" {
		t.Errorf("DB status = %q, want %q", dbWs.Status, "active")
	}
}

// --- TS-02-25: DELETE /api/v1/workspaces/:id with cascade ---

// TS-02-25: Verify that DELETE /api/v1/workspaces/:id on an archived
// workspace deletes the workspace, its memberships, and scoped API keys.
func TestWorkspaceHandler_DeleteWorkspace_CascadeDelete(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenWS)

	// Create an archived workspace.
	ws, err := env.Store.CreateWorkspace(&store.Workspace{
		Name:   "To Delete",
		Slug:   "to-delete",
		URL:    "https://delete-me.example.com",
		Status: "archived",
	})
	if err != nil {
		t.Fatalf("failed to create workspace: %v", err)
	}

	// Create users.
	user1, err := env.Store.CreateUser(&store.User{
		Username:   "deluser1",
		Email:      "deluser1@example.com",
		Provider:   "github",
		ProviderID: "gh_del_001",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("failed to create user 1: %v", err)
	}

	user2, err := env.Store.CreateUser(&store.User{
		Username:   "deluser2",
		Email:      "deluser2@example.com",
		Provider:   "github",
		ProviderID: "gh_del_002",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("failed to create user 2: %v", err)
	}

	// Create 2 memberships.
	_, err = env.Store.UpsertWorkspaceMember(&store.WorkspaceMember{
		UserID: user1.ID, WorkspaceID: ws.ID, Role: "editor",
	})
	if err != nil {
		t.Fatalf("failed to create membership 1: %v", err)
	}
	_, err = env.Store.UpsertWorkspaceMember(&store.WorkspaceMember{
		UserID: user2.ID, WorkspaceID: ws.ID, Role: "reader",
	})
	if err != nil {
		t.Fatalf("failed to create membership 2: %v", err)
	}

	// Create 3 API keys scoped to this workspace.
	for i := range 3 {
		_, err = env.Store.CreateAPIKey(&store.APIKey{
			KeyID:       fmt.Sprintf("delkey_%d", i),
			KeyHash:     sha256HexString(fmt.Sprintf("delsecret_%d", i)),
			UserID:      user1.ID,
			WorkspaceID: ws.ID,
			Role:        "editor",
			Label:       fmt.Sprintf("del key %d", i),
		})
		if err != nil {
			t.Fatalf("failed to create API key %d: %v", i, err)
		}
	}

	rec := doRequest(env.Echo, http.MethodDelete,
		fmt.Sprintf("/api/v1/workspaces/%s", ws.ID), "",
		adminHeaders(testAdminTokenWS))

	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE /api/v1/workspaces/%s: status = %d, want %d\nBody: %s",
			ws.ID, rec.Code, http.StatusOK, rec.Body.String())
	}

	// Verify response message.
	var resp map[string]string
	parseJSON(t, rec, &resp)
	if resp["message"] != "workspace deleted" {
		t.Errorf("message = %q, want %q", resp["message"], "workspace deleted")
	}

	// Verify workspace is deleted from DB.
	_, err = env.Store.GetWorkspaceByID(ws.ID)
	if err == nil {
		t.Error("workspace should be deleted from DB")
	}

	// Verify memberships are deleted.
	members, _ := env.Store.ListWorkspaceMembers(ws.ID)
	if len(members) != 0 {
		t.Errorf("memberships remaining = %d, want 0", len(members))
	}

	// Verify API keys are deleted.
	keyCount, _ := env.Store.CountAPIKeysByWorkspaceID(ws.ID)
	if keyCount != 0 {
		t.Errorf("API keys remaining = %d, want 0", keyCount)
	}
}

// --- TS-02-26: POST /api/v1/workspaces/:id/members ---

// TS-02-26: Verify that POST /api/v1/workspaces/:id/members adds or
// updates a membership with the given role and returns HTTP 200.
func TestWorkspaceHandler_AddMember_Success(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenWS)

	// Create a workspace and a user.
	ws, err := env.Store.CreateWorkspace(&store.Workspace{
		Name: "Member WS", Slug: "member-ws", URL: "https://member.example.com", Status: "active",
	})
	if err != nil {
		t.Fatalf("failed to create workspace: %v", err)
	}

	user, err := env.Store.CreateUser(&store.User{
		Username:   "memberuser006",
		Email:      "member006@example.com",
		Provider:   "github",
		ProviderID: "gh_member_006",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	body := fmt.Sprintf(`{"user_id": "%s", "role": "editor"}`, user.ID)

	rec := doRequest(env.Echo, http.MethodPost,
		fmt.Sprintf("/api/v1/workspaces/%s/members", ws.ID), body,
		adminHeaders(testAdminTokenWS))

	if rec.Code != http.StatusOK {
		t.Fatalf("POST members: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var membership membershipResponse
	parseJSON(t, rec, &membership)

	if membership.UserID != user.ID {
		t.Errorf("user_id = %q, want %q", membership.UserID, user.ID)
	}
	if membership.Role != "editor" {
		t.Errorf("role = %q, want %q", membership.Role, "editor")
	}
	if membership.WorkspaceID != ws.ID {
		t.Errorf("workspace_id = %q, want %q", membership.WorkspaceID, ws.ID)
	}

	// Verify DB state.
	dbMembership, err := env.Store.GetWorkspaceMember(user.ID, ws.ID)
	if err != nil {
		t.Fatalf("GetWorkspaceMember failed: %v", err)
	}
	if dbMembership.Role != "editor" {
		t.Errorf("DB role = %q, want %q", dbMembership.Role, "editor")
	}
}

// TS-02-26: Verify that a membership is updated (upserted) when re-adding
// a user with a different role.
func TestWorkspaceHandler_AddMember_UpsertUpdatesRole(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenWS)

	ws, err := env.Store.CreateWorkspace(&store.Workspace{
		Name: "Upsert WS", Slug: "upsert-ws", URL: "https://upsert.example.com", Status: "active",
	})
	if err != nil {
		t.Fatalf("failed to create workspace: %v", err)
	}

	user, err := env.Store.CreateUser(&store.User{
		Username:   "upsertuser",
		Email:      "upsert@example.com",
		Provider:   "github",
		ProviderID: "gh_upsert",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// First add as reader.
	body1 := fmt.Sprintf(`{"user_id": "%s", "role": "reader"}`, user.ID)
	rec1 := doRequest(env.Echo, http.MethodPost,
		fmt.Sprintf("/api/v1/workspaces/%s/members", ws.ID), body1,
		adminHeaders(testAdminTokenWS))

	if rec1.Code != http.StatusOK {
		t.Fatalf("first add: status = %d, want %d", rec1.Code, http.StatusOK)
	}

	// Update to editor.
	body2 := fmt.Sprintf(`{"user_id": "%s", "role": "editor"}`, user.ID)
	rec2 := doRequest(env.Echo, http.MethodPost,
		fmt.Sprintf("/api/v1/workspaces/%s/members", ws.ID), body2,
		adminHeaders(testAdminTokenWS))

	if rec2.Code != http.StatusOK {
		t.Fatalf("upsert: status = %d, want %d", rec2.Code, http.StatusOK)
	}

	var membership membershipResponse
	parseJSON(t, rec2, &membership)

	if membership.Role != "editor" {
		t.Errorf("role after upsert = %q, want %q", membership.Role, "editor")
	}

	// Verify only one membership exists.
	members, _ := env.Store.ListWorkspaceMembers(ws.ID)
	if len(members) != 1 {
		t.Errorf("expected 1 membership after upsert, got %d", len(members))
	}
}

// --- TS-02-27: GET /api/v1/workspaces/:id/members ---

// TS-02-27: Verify that GET /api/v1/workspaces/:id/members returns all
// member records for the workspace with HTTP 200.
func TestWorkspaceHandler_ListMembers_ReturnsAll(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenWS)

	ws, err := env.Store.CreateWorkspace(&store.Workspace{
		Name: "Members WS", Slug: "members-ws", URL: "https://members.example.com", Status: "active",
	})
	if err != nil {
		t.Fatalf("failed to create workspace: %v", err)
	}

	// Create 3 users and add as members.
	for i := range 3 {
		user, err := env.Store.CreateUser(&store.User{
			Username:   fmt.Sprintf("listmember_%d", i),
			Email:      fmt.Sprintf("listmember_%d@example.com", i),
			Provider:   "github",
			ProviderID: fmt.Sprintf("gh_listmember_%d", i),
			Status:     "active",
		})
		if err != nil {
			t.Fatalf("failed to create user %d: %v", i, err)
		}
		roles := []string{"admin", "editor", "reader"}
		_, err = env.Store.UpsertWorkspaceMember(&store.WorkspaceMember{
			UserID: user.ID, WorkspaceID: ws.ID, Role: roles[i],
		})
		if err != nil {
			t.Fatalf("failed to create membership %d: %v", i, err)
		}
	}

	rec := doRequest(env.Echo, http.MethodGet,
		fmt.Sprintf("/api/v1/workspaces/%s/members", ws.ID), "",
		adminHeaders(testAdminTokenWS))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET members: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	var members []membershipResponse
	parseJSON(t, rec, &members)

	if len(members) != 3 {
		t.Fatalf("len(members) = %d, want 3", len(members))
	}

	// Verify all have user_id and role.
	for i, m := range members {
		if m.UserID == "" {
			t.Errorf("member[%d].user_id is empty", i)
		}
		if m.Role == "" {
			t.Errorf("member[%d].role is empty", i)
		}
	}
}

// TS-02-27: Verify empty workspace returns empty array.
func TestWorkspaceHandler_ListMembers_EmptyWorkspace(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenWS)

	ws, err := env.Store.CreateWorkspace(&store.Workspace{
		Name: "Empty WS", Slug: "empty-ws", URL: "https://empty.example.com", Status: "active",
	})
	if err != nil {
		t.Fatalf("failed to create workspace: %v", err)
	}

	rec := doRequest(env.Echo, http.MethodGet,
		fmt.Sprintf("/api/v1/workspaces/%s/members", ws.ID), "",
		adminHeaders(testAdminTokenWS))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var members []membershipResponse
	parseJSON(t, rec, &members)

	if len(members) != 0 {
		t.Errorf("len(members) = %d, want 0 for empty workspace", len(members))
	}
}

// --- Workspace slug validation edge cases (TS-02-E15 partial) ---

// Verify that a valid slug with hyphens is accepted.
func TestWorkspaceHandler_CreateWorkspace_ValidSlugWithHyphens(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenWS)

	body := `{
		"name": "Hyphen WS",
		"slug": "my-hyphenated-slug",
		"url": "https://hyphen.example.com"
	}`

	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/workspaces", body,
		adminHeaders(testAdminTokenWS))

	if rec.Code != http.StatusCreated {
		t.Errorf("valid slug with hyphens: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}
}

// Verify that http:// URLs are accepted (not just https://).
func TestWorkspaceHandler_CreateWorkspace_HTTPUrlAccepted(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenWS)

	body := `{
		"name": "HTTP WS",
		"slug": "http-ws",
		"url": "http://httpws.example.com"
	}`

	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/workspaces", body,
		adminHeaders(testAdminTokenWS))

	if rec.Code != http.StatusCreated {
		t.Errorf("http URL: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}
}

// --- Additional edge case: workspace archive/reactivate verifies status change ---

// Verify archive changes status from "active" to "archived" in DB.
func TestWorkspaceHandler_ArchiveWorkspace_DBStateChange(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenWS)

	ws, _ := env.Store.CreateWorkspace(&store.Workspace{
		Name: "Archive DB Test", Slug: "archive-db-test",
		URL: "https://archivedb.example.com", Status: "active",
	})

	doRequest(env.Echo, http.MethodPost,
		fmt.Sprintf("/api/v1/workspaces/%s/archive", ws.ID), "",
		adminHeaders(testAdminTokenWS))

	dbWs, err := env.Store.GetWorkspaceByID(ws.ID)
	if err != nil {
		t.Fatalf("GetWorkspaceByID failed: %v", err)
	}
	if dbWs.Status != "archived" {
		t.Errorf("DB status = %q, want %q after archive", dbWs.Status, "archived")
	}
}

// Verify reactivate changes status from "archived" to "active" in DB.
func TestWorkspaceHandler_ReactivateWorkspace_DBStateChange(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenWS)

	ws, _ := env.Store.CreateWorkspace(&store.Workspace{
		Name: "Reactivate DB Test", Slug: "reactivate-db-test",
		URL: "https://reactivatedb.example.com", Status: "archived",
	})

	doRequest(env.Echo, http.MethodPost,
		fmt.Sprintf("/api/v1/workspaces/%s/reactivate", ws.ID), "",
		adminHeaders(testAdminTokenWS))

	dbWs, err := env.Store.GetWorkspaceByID(ws.ID)
	if err != nil {
		t.Fatalf("GetWorkspaceByID failed: %v", err)
	}
	if dbWs.Status != "active" {
		t.Errorf("DB status = %q, want %q after reactivate", dbWs.Status, "active")
	}
}

// Verify DELETE on non-existent workspace returns 404.
func TestWorkspaceHandler_DeleteWorkspace_NotFound_Returns404(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenWS)

	rec := doRequest(env.Echo, http.MethodDelete,
		"/api/v1/workspaces/nonexistent_ws_id", "",
		adminHeaders(testAdminTokenWS))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE non-existent workspace: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var errResp errorResponse
	parseJSON(t, rec, &errResp)

	if errResp.Error.Code != "404" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "404")
	}
	if !strings.Contains(errResp.Error.Message, "workspace not found") {
		t.Errorf("error message = %q, want 'workspace not found'",
			errResp.Error.Message)
	}
}
