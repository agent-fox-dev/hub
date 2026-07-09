package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox/af-hub/internal/store"
)

// ---------------------------------------------------------------------------
// Test constants for spec-06 team endpoint rename tests.
// ---------------------------------------------------------------------------

const testAdminTokenTeamEndpoint = "af_admin_team_endpoint_test"

// ---------------------------------------------------------------------------
// TS-06-9: Verifies that POST /api/v1/teams creates a new team and returns
// HTTP 201 with a JSON body containing `team_id` (or `id`).
// Requirement: 06-REQ-3.1
// ---------------------------------------------------------------------------

func TestTeamEndpoint_CreateTeam_ReturnsCreated(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenTeamEndpoint)

	body := `{
		"name": "Alpha Team",
		"slug": "alpha-team",
		"url": "https://alpha.example.com"
	}`

	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/teams", body,
		adminHeaders(testAdminTokenTeamEndpoint))

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/teams: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	// Parse the response and verify team fields.
	var result map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse JSON response: %v\nBody: %s", err, rec.Body.String())
	}

	// Must have a non-empty id.
	id, _ := result["id"].(string)
	if id == "" {
		t.Error("response should contain non-empty 'id' field")
	}

	// Must NOT contain workspace_id.
	if _, has := result["workspace_id"]; has {
		t.Error("response should not contain 'workspace_id' field")
	}
}

func TestTeamEndpoint_CreateTeam_PersistsInDB(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenTeamEndpoint)

	body := `{
		"name": "Persist Team",
		"slug": "persist-team",
		"url": "https://persist-team.example.com"
	}`

	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/teams", body,
		adminHeaders(testAdminTokenTeamEndpoint))

	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/teams: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var result map[string]any
	parseJSON(t, rec, &result)

	teamID, _ := result["id"].(string)
	if teamID == "" {
		t.Fatal("response id is empty; cannot verify DB persistence")
	}

	// After the rename, the store method is GetTeamByID. For now, use the
	// existing store method to verify persistence. The test will fail because
	// the /api/v1/teams route doesn't exist yet.
	dbTeam, err := env.Store.GetWorkspaceByID(teamID)
	if err != nil {
		t.Fatalf("GetWorkspaceByID(%q) failed: %v", teamID, err)
	}
	if dbTeam.Name != "Persist Team" {
		t.Errorf("DB name = %q, want %q", dbTeam.Name, "Persist Team")
	}
}

// ---------------------------------------------------------------------------
// TS-06-10: Verifies that GET /api/v1/teams returns HTTP 200 with a JSON
// array of team objects.
// Requirement: 06-REQ-3.2
// ---------------------------------------------------------------------------

func TestTeamEndpoint_ListTeams_ReturnsOK(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenTeamEndpoint)

	// Pre-populate at least one team.
	_, err := env.Store.CreateWorkspace(&store.Workspace{
		Name:   "List Team 1",
		Slug:   "list-team-one",
		URL:    "https://list1.example.com",
		Status: "active",
	})
	if err != nil {
		t.Fatalf("failed to seed team: %v", err)
	}

	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/teams", "",
		adminHeaders(testAdminTokenTeamEndpoint))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/teams: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	// Response should be a JSON array.
	var teams []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &teams); err != nil {
		t.Fatalf("response should be a JSON array: %v\nBody: %s", err, rec.Body.String())
	}

	if len(teams) < 1 {
		t.Error("GET /api/v1/teams should return at least 1 team")
	}

	// No element should contain workspace_id.
	for i, team := range teams {
		if _, has := team["workspace_id"]; has {
			t.Errorf("teams[%d] should not contain 'workspace_id'", i)
		}
	}
}

// ---------------------------------------------------------------------------
// TS-06-11: Verifies that GET /api/v1/teams/:id returns HTTP 200 with the
// matching team JSON object, or HTTP 404 if not found.
// Requirement: 06-REQ-3.3
//
// NOTE: Per reviewer finding, GET /api/v1/workspaces/:id does not exist in
// the current codebase. This endpoint is new functionality, not a rename.
// The test is included for spec compliance but documents this divergence.
// See docs/errata/06_team_rename.md for details.
// ---------------------------------------------------------------------------

func TestTeamEndpoint_GetTeamByID_ReturnsOK(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenTeamEndpoint)

	// Create a team to fetch.
	ws, err := env.Store.CreateWorkspace(&store.Workspace{
		Name:   "Fetch Team",
		Slug:   "fetch-team",
		URL:    "https://fetch.example.com",
		Status: "active",
	})
	if err != nil {
		t.Fatalf("failed to seed team: %v", err)
	}

	rec := doRequest(env.Echo, http.MethodGet,
		fmt.Sprintf("/api/v1/teams/%s", ws.ID), "",
		adminHeaders(testAdminTokenTeamEndpoint))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/teams/%s: status = %d, want %d\nBody: %s",
			ws.ID, rec.Code, http.StatusOK, rec.Body.String())
	}

	var result map[string]any
	parseJSON(t, rec, &result)

	id, _ := result["id"].(string)
	if id != ws.ID {
		t.Errorf("response id = %q, want %q", id, ws.ID)
	}
}

func TestTeamEndpoint_GetTeamByID_NotFound(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenTeamEndpoint)

	rec := doRequest(env.Echo, http.MethodGet,
		"/api/v1/teams/nonexistent-team-id", "",
		adminHeaders(testAdminTokenTeamEndpoint))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /api/v1/teams/nonexistent-team-id: status = %d, want %d\nBody: %s",
			rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// TS-06-12: Verifies that POST /api/v1/teams/:id/archive archives the team
// and returns HTTP 200.
// Requirement: 06-REQ-3.4
//
// NOTE: The spec says PUT for archive, but the codebase uses POST. Using
// POST to match the existing behavior (zero behavioral change guarantee,
// 06-REQ-7). See docs/errata/06_team_rename.md for details.
// ---------------------------------------------------------------------------

func TestTeamEndpoint_ArchiveTeam_ReturnsOK(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenTeamEndpoint)

	ws, err := env.Store.CreateWorkspace(&store.Workspace{
		Name:   "Archive Team",
		Slug:   "archive-team",
		URL:    "https://archive-team.example.com",
		Status: "active",
	})
	if err != nil {
		t.Fatalf("failed to seed team: %v", err)
	}

	rec := doRequest(env.Echo, http.MethodPost,
		fmt.Sprintf("/api/v1/teams/%s/archive", ws.ID), "",
		adminHeaders(testAdminTokenTeamEndpoint))

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/teams/%s/archive: status = %d, want %d\nBody: %s",
			ws.ID, rec.Code, http.StatusOK, rec.Body.String())
	}

	// Verify DB state shows archived.
	dbWs, err := env.Store.GetWorkspaceByID(ws.ID)
	if err != nil {
		t.Fatalf("GetWorkspaceByID failed: %v", err)
	}
	if dbWs.Status != "archived" {
		t.Errorf("DB status = %q, want %q", dbWs.Status, "archived")
	}
}

// ---------------------------------------------------------------------------
// TS-06-13: Verifies that POST /api/v1/teams/:id/reactivate reactivates an
// archived team and returns HTTP 200.
// Requirement: 06-REQ-3.5
//
// NOTE: Same POST vs PUT divergence as TS-06-12.
// ---------------------------------------------------------------------------

func TestTeamEndpoint_ReactivateTeam_ReturnsOK(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenTeamEndpoint)

	ws, err := env.Store.CreateWorkspace(&store.Workspace{
		Name:   "Reactivate Team",
		Slug:   "reactivate-team",
		URL:    "https://reactivate-team.example.com",
		Status: "archived",
	})
	if err != nil {
		t.Fatalf("failed to seed team: %v", err)
	}

	rec := doRequest(env.Echo, http.MethodPost,
		fmt.Sprintf("/api/v1/teams/%s/reactivate", ws.ID), "",
		adminHeaders(testAdminTokenTeamEndpoint))

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/teams/%s/reactivate: status = %d, want %d\nBody: %s",
			ws.ID, rec.Code, http.StatusOK, rec.Body.String())
	}

	// Verify DB state shows active.
	dbWs, err := env.Store.GetWorkspaceByID(ws.ID)
	if err != nil {
		t.Fatalf("GetWorkspaceByID failed: %v", err)
	}
	if dbWs.Status != "active" {
		t.Errorf("DB status = %q, want %q", dbWs.Status, "active")
	}
}

// ---------------------------------------------------------------------------
// TS-06-14: Verifies that DELETE /api/v1/teams/:id deletes the team and
// returns HTTP 200 (existing behavior; spec says 204 but the current
// implementation returns 200 with a JSON body).
// Requirement: 06-REQ-3.6
//
// NOTE: Using HTTP 200 (not 204) to match existing behavior.
// ---------------------------------------------------------------------------

func TestTeamEndpoint_DeleteTeam_Success(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenTeamEndpoint)

	// Create an archived team (delete requires archived status).
	ws, err := env.Store.CreateWorkspace(&store.Workspace{
		Name:   "Delete Team",
		Slug:   "delete-team",
		URL:    "https://delete-team.example.com",
		Status: "archived",
	})
	if err != nil {
		t.Fatalf("failed to seed team: %v", err)
	}

	rec := doRequest(env.Echo, http.MethodDelete,
		fmt.Sprintf("/api/v1/teams/%s", ws.ID), "",
		adminHeaders(testAdminTokenTeamEndpoint))

	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE /api/v1/teams/%s: status = %d, want %d\nBody: %s",
			ws.ID, rec.Code, http.StatusOK, rec.Body.String())
	}

	// Verify team is deleted from DB.
	_, err = env.Store.GetWorkspaceByID(ws.ID)
	if err == nil {
		t.Error("team should be deleted from DB")
	}
}

// ---------------------------------------------------------------------------
// TS-06-15: Verifies that POST /api/v1/teams/:id/members adds a member to
// the team and returns HTTP 200.
// Requirement: 06-REQ-3.7
//
// NOTE: The existing handler (AddOrUpdateMember) returns HTTP 200, not 201.
// Using 200 to match existing behavior. Also, the handler uses upsert
// semantics, not a separate add endpoint.
// ---------------------------------------------------------------------------

func TestTeamEndpoint_AddMember_ReturnsOK(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenTeamEndpoint)

	// Create a team and a user.
	ws, err := env.Store.CreateWorkspace(&store.Workspace{
		Name: "Member Team", Slug: "member-team",
		URL: "https://member-team.example.com", Status: "active",
	})
	if err != nil {
		t.Fatalf("failed to seed team: %v", err)
	}

	user, err := env.Store.CreateUser(&store.User{
		Username:   "teamuser001",
		Email:      "teamuser001@example.com",
		Provider:   "github",
		ProviderID: "gh_team_member_001",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("failed to seed user: %v", err)
	}

	body := fmt.Sprintf(`{"user_id": "%s", "role": "editor"}`, user.ID)

	rec := doRequest(env.Echo, http.MethodPost,
		fmt.Sprintf("/api/v1/teams/%s/members", ws.ID), body,
		adminHeaders(testAdminTokenTeamEndpoint))

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/teams/%s/members: status = %d, want %d\nBody: %s",
			ws.ID, rec.Code, http.StatusOK, rec.Body.String())
	}

	// Parse response and verify team_id field is present (not workspace_id).
	var result map[string]any
	parseJSON(t, rec, &result)

	if _, has := result["workspace_id"]; has {
		t.Error("member response should not contain 'workspace_id'")
	}

	// After the rename, the JSON field should be 'team_id'.
	teamID, _ := result["team_id"].(string)
	if teamID == "" {
		t.Error("member response should contain non-empty 'team_id' field")
	}

	// Verify DB state.
	dbMember, err := env.Store.GetWorkspaceMember(user.ID, ws.ID)
	if err != nil {
		t.Fatalf("GetWorkspaceMember failed: %v", err)
	}
	if dbMember.Role != "editor" {
		t.Errorf("DB role = %q, want %q", dbMember.Role, "editor")
	}
}

// ---------------------------------------------------------------------------
// TS-06-16: Verifies that DELETE /api/v1/teams/:id/members/:user_id removes
// a member from the team and returns HTTP 204.
// Requirement: 06-REQ-3.8
//
// NOTE: Per reviewer finding, no DELETE /workspaces/:id/members/:user_id
// endpoint exists in the codebase. This is new functionality, not a rename.
// The spec claims zero behavioral change (06-REQ-7) but this adds new
// functionality. Test included for spec compliance.
// See docs/errata/06_team_rename.md.
// ---------------------------------------------------------------------------

func TestTeamEndpoint_RemoveMember_ReturnsNoContent(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenTeamEndpoint)

	ws, err := env.Store.CreateWorkspace(&store.Workspace{
		Name: "RemoveMember Team", Slug: "remove-member-team",
		URL: "https://remove-member.example.com", Status: "active",
	})
	if err != nil {
		t.Fatalf("failed to seed team: %v", err)
	}

	user, err := env.Store.CreateUser(&store.User{
		Username:   "rmuser001",
		Email:      "rmuser001@example.com",
		Provider:   "github",
		ProviderID: "gh_rm_member_001",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("failed to seed user: %v", err)
	}

	// Add the user as a member first.
	_, err = env.Store.UpsertWorkspaceMember(&store.WorkspaceMember{
		UserID: user.ID, WorkspaceID: ws.ID, Role: "editor",
	})
	if err != nil {
		t.Fatalf("failed to seed membership: %v", err)
	}

	rec := doRequest(env.Echo, http.MethodDelete,
		fmt.Sprintf("/api/v1/teams/%s/members/%s", ws.ID, user.ID), "",
		adminHeaders(testAdminTokenTeamEndpoint))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE /api/v1/teams/%s/members/%s: status = %d, want %d\nBody: %s",
			ws.ID, user.ID, rec.Code, http.StatusNoContent, rec.Body.String())
	}

	// Verify membership is removed from DB.
	_, err = env.Store.GetWorkspaceMember(user.ID, ws.ID)
	if err == nil {
		t.Error("membership should be removed from DB after DELETE")
	}
}

// ---------------------------------------------------------------------------
// TS-06-17: Verifies that all API JSON responses and request bodies use
// `team_id` and never `workspace_id` for the organizational-boundary
// identifier.
// Requirement: 06-REQ-3.9
// ---------------------------------------------------------------------------

func TestTeamEndpoint_JSONFieldTeamID_NotWorkspaceID(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenTeamEndpoint)

	// Create a team.
	createBody := `{
		"name": "JSON Field Team",
		"slug": "json-field-team",
		"url": "https://json-field.example.com"
	}`

	createRec := doRequest(env.Echo, http.MethodPost, "/api/v1/teams", createBody,
		adminHeaders(testAdminTokenTeamEndpoint))

	if createRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/teams: status = %d, want %d\nBody: %s",
			createRec.Code, http.StatusCreated, createRec.Body.String())
	}

	t.Run("create response has no workspace_id", func(t *testing.T) {
		respBody := createRec.Body.String()
		if strings.Contains(respBody, "workspace_id") {
			t.Error("POST /api/v1/teams response should not contain 'workspace_id'")
		}
	})

	// Get the team ID for subsequent tests.
	var createResult map[string]any
	if err := json.Unmarshal(createRec.Body.Bytes(), &createResult); err != nil {
		t.Fatalf("failed to parse create response: %v", err)
	}
	teamID, _ := createResult["id"].(string)

	// List teams and check JSON fields.
	t.Run("list response has no workspace_id", func(t *testing.T) {
		listRec := doRequest(env.Echo, http.MethodGet, "/api/v1/teams", "",
			adminHeaders(testAdminTokenTeamEndpoint))

		if listRec.Code != http.StatusOK {
			t.Skipf("GET /api/v1/teams returned %d, skipping field check", listRec.Code)
		}

		respBody := listRec.Body.String()
		if strings.Contains(respBody, "workspace_id") {
			t.Error("GET /api/v1/teams response should not contain 'workspace_id'")
		}
	})

	// Add a member and check JSON fields.
	t.Run("member response uses team_id", func(t *testing.T) {
		user, err := env.Store.CreateUser(&store.User{
			Username:   "jsonuser001",
			Email:      "jsonuser001@example.com",
			Provider:   "github",
			ProviderID: "gh_json_001",
			Status:     "active",
		})
		if err != nil {
			t.Fatalf("failed to seed user: %v", err)
		}

		memberBody := fmt.Sprintf(`{"user_id": "%s", "role": "editor"}`, user.ID)
		memberRec := doRequest(env.Echo, http.MethodPost,
			fmt.Sprintf("/api/v1/teams/%s/members", teamID), memberBody,
			adminHeaders(testAdminTokenTeamEndpoint))

		if memberRec.Code != http.StatusOK {
			t.Skipf("POST members returned %d, skipping field check", memberRec.Code)
		}

		respBody := memberRec.Body.String()
		if strings.Contains(respBody, "workspace_id") {
			t.Error("member response should not contain 'workspace_id'")
		}
		if !strings.Contains(respBody, "team_id") {
			t.Error("member response should contain 'team_id' field")
		}
	})
}

// ---------------------------------------------------------------------------
// TS-06-18: Verifies that no routes are registered under the
// `/api/v1/workspaces` path prefix in the Echo router setup.
// Requirement: 06-REQ-3.10
// ---------------------------------------------------------------------------

func TestNoLegacyWorkspaceRoutes_Static(t *testing.T) {
	// Read the router registration source files to verify no /api/v1/workspaces
	// path strings are present.
	root := findIntegrationProjectRoot(t)

	routeFiles := []string{
		filepath.Join(root, "internal", "server", "server.go"),
		filepath.Join(root, "cmd", "af-hub", "main.go"),
	}

	for _, filePath := range routeFiles {
		content, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatalf("failed to read %s: %v", filePath, err)
		}

		relPath, _ := filepath.Rel(root, filePath)

		if strings.Contains(string(content), "/api/v1/workspaces") {
			t.Errorf("%s should not contain '/api/v1/workspaces' route paths", relPath)
		}

		// Also check for bare /workspaces route registrations.
		if strings.Contains(string(content), `"/workspaces"`) ||
			strings.Contains(string(content), `"/workspaces/`) {
			t.Errorf("%s should not contain '/workspaces' route paths", relPath)
		}
	}
}

func TestNoLegacyWorkspaceRoutes_Runtime(t *testing.T) {
	env := setupFullTestEnv(t)

	routes := env.Echo.Routes()
	for _, route := range routes {
		if strings.Contains(route.Path, "/workspaces") {
			t.Errorf("legacy route registered: %s %s — should use /teams instead",
				route.Method, route.Path)
		}
	}
}

func TestTeamRoutesRegistered_Runtime(t *testing.T) {
	env := setupFullTestEnv(t)

	routes := env.Echo.Routes()

	// Collect all paths containing /teams.
	teamPaths := make(map[string]bool)
	for _, route := range routes {
		if strings.Contains(route.Path, "/api/v1/teams") {
			key := route.Method + " " + route.Path
			teamPaths[key] = true
		}
	}

	// The minimum required team routes (based on existing workspace routes).
	// Note: Using POST for archive/reactivate to match existing behavior.
	requiredRoutes := []string{
		"POST /api/v1/teams",
		"GET /api/v1/teams",
		"POST /api/v1/teams/:id/archive",
		"POST /api/v1/teams/:id/reactivate",
		"DELETE /api/v1/teams/:id",
		"POST /api/v1/teams/:id/members",
		"GET /api/v1/teams/:id/members",
	}

	for _, required := range requiredRoutes {
		if !teamPaths[required] {
			t.Errorf("required team route not registered: %s", required)
		}
	}

	// Must have at least 7 team routes.
	if len(teamPaths) < 7 {
		t.Errorf("expected at least 7 team routes, got %d", len(teamPaths))
	}
}

// ---------------------------------------------------------------------------
// TS-06-E4: Verifies that any request to a legacy /api/v1/workspaces path
// returns HTTP 404 with no redirect.
// Requirement: 06-REQ-3.E1
// ---------------------------------------------------------------------------

func TestLegacyWorkspacePaths_Return404(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenTeamEndpoint)

	legacyPaths := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/workspaces"},
		{http.MethodPost, "/api/v1/workspaces"},
		{http.MethodGet, "/api/v1/workspaces/abc"},
		{http.MethodPost, "/api/v1/workspaces/abc/archive"},
		{http.MethodPost, "/api/v1/workspaces/abc/reactivate"},
		{http.MethodDelete, "/api/v1/workspaces/abc"},
		{http.MethodPost, "/api/v1/workspaces/abc/members"},
		{http.MethodGet, "/api/v1/workspaces/abc/members"},
	}

	for _, lp := range legacyPaths {
		t.Run(lp.method+"_"+lp.path, func(t *testing.T) {
			rec := doRequest(env.Echo, lp.method, lp.path, "",
				adminHeaders(testAdminTokenTeamEndpoint))

			// Must return 404 — no legacy workspace routes should be registered.
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s: status = %d, want %d",
					lp.method, lp.path, rec.Code, http.StatusNotFound)
			}

			// Must NOT be a redirect.
			if rec.Code == http.StatusMovedPermanently ||
				rec.Code == http.StatusFound ||
				rec.Code == http.StatusTemporaryRedirect ||
				rec.Code == http.StatusPermanentRedirect {
				t.Errorf("%s %s: got redirect %d, want 404 with no redirect",
					lp.method, lp.path, rec.Code)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TS-06-P3: Property test — for any route registered in the Echo router,
// no route path contains `/workspaces`. All organizational-boundary routes
// are under `/api/v1/teams`.
// Requirement: 06-REQ-3.10, 06-REQ-3.E1
// ---------------------------------------------------------------------------

func TestProperty_NoWorkspaceRoutes_AllTeamRoutesPresent(t *testing.T) {
	env := setupFullTestEnv(t)

	routes := env.Echo.Routes()

	// Property 1: No route contains /workspaces.
	for _, route := range routes {
		if strings.Contains(route.Path, "/workspaces") {
			t.Errorf("PROP-3 violated: route %s %s contains /workspaces",
				route.Method, route.Path)
		}
	}

	// Property 2: At least 7 team routes exist (the 7 existing workspace
	// routes renamed).
	teamRouteCount := 0
	for _, route := range routes {
		if strings.Contains(route.Path, "/api/v1/teams") {
			teamRouteCount++
		}
	}
	if teamRouteCount < 7 {
		t.Errorf("PROP-3 violated: expected at least 7 team routes, got %d", teamRouteCount)
	}
}

// ---------------------------------------------------------------------------
// TS-06-P4: Property test — for any JSON response body from a /api/v1/teams
// endpoint, the field `workspace_id` never appears. Where the organizational-
// boundary identifier is present, it is serialised as `team_id`.
// Requirement: 06-REQ-3.9
// ---------------------------------------------------------------------------

func TestProperty_JSONField_TeamID_NotWorkspaceID(t *testing.T) {
	env := setupFullTestEnv(t)
	seedAdminTokenFull(t, env.Store, testAdminTokenTeamEndpoint)

	// Create a team.
	createBody := `{
		"name": "Prop Team",
		"slug": "prop-team",
		"url": "https://prop.example.com"
	}`
	createRec := doRequest(env.Echo, http.MethodPost, "/api/v1/teams", createBody,
		adminHeaders(testAdminTokenTeamEndpoint))
	if createRec.Code != http.StatusCreated {
		t.Fatalf("could not create team for property test: status %d", createRec.Code)
	}

	var createResult map[string]any
	parseJSON(t, createRec, &createResult)
	teamID, _ := createResult["id"].(string)

	// Collect all responses from team endpoints.
	type endpointCall struct {
		method string
		path   string
		body   string
	}

	calls := []endpointCall{
		{http.MethodGet, "/api/v1/teams", ""},
		{http.MethodPost, fmt.Sprintf("/api/v1/teams/%s/archive", teamID), ""},
	}

	for _, call := range calls {
		t.Run(call.method+"_"+call.path, func(t *testing.T) {
			rec := doRequest(env.Echo, call.method, call.path, call.body,
				adminHeaders(testAdminTokenTeamEndpoint))

			if rec.Code >= 200 && rec.Code < 300 {
				respBody := rec.Body.String()
				if strings.Contains(respBody, `"workspace_id"`) {
					t.Errorf("PROP-4 violated: response from %s %s contains 'workspace_id'",
						call.method, call.path)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Helper: findIntegrationProjectRoot walks up from the current directory to
// find the directory containing go.mod.
// ---------------------------------------------------------------------------

func findIntegrationProjectRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find project root (no go.mod found)")
		}
		dir = parent
	}
}
