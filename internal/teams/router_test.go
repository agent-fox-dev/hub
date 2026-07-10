package teams_test

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/auth"
	"github.com/agent-fox-dev/hub/internal/teams"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// ---------------------------------------------------------------------------
// Router-level integration tests
// Verifies that RegisterTeamRoutes wires admin middleware at the group level,
// so all routes are protected and non-admin callers receive HTTP 403.
// Requirements: 03-REQ-10.1, 03-REQ-10.2, 03-REQ-12.1, 03-REQ-12.2
// Test Spec: TS-03-48, TS-03-49
// ---------------------------------------------------------------------------

// setupRouterTest initializes the database and registers team routes using
// the production-ready RegisterTeamRoutes function (router.go). The auth
// context is injected via a test middleware to simulate the auth middleware
// from server_foundation.
func setupRouterTest(t *testing.T, ac *auth.AuthContext) (*echo.Echo, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	createStubUsersTable(t, db)
	if err := teams.InitSchema(db); err != nil {
		t.Fatalf("InitSchema failed: %v", err)
	}

	e := echo.New()
	// Simulate auth middleware by injecting the auth context, then delegate
	// to RegisterTeamRoutes which applies AdminRequired middleware internally.
	g := e.Group("/api/v1/teams", setTestAuthContext(ac))
	teams.RegisterTeamRoutes(g, db)

	return e, db
}

// TestRouter_AdminAllowed verifies that admin callers pass through
// RegisterTeamRoutes middleware and reach the handler successfully.
func TestRouter_AdminAllowed(t *testing.T) {
	e, _ := setupRouterTest(t, adminAuthCtx())

	// GET /api/v1/teams should return HTTP 200 (empty list) for an admin.
	rec := doRequest(t, e, http.MethodGet, "/api/v1/teams", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200 for admin via RegisterTeamRoutes, got %d: %s",
			rec.Code, rec.Body.String())
	}

	// Verify Content-Type: application/json is set.
	ct := rec.Header().Get("Content-Type")
	if ct == "" || !strings.Contains(ct, "application/json") {
		t.Errorf("expected Content-Type containing 'application/json', got %q", ct)
	}
}

// TestRouter_NonAdminForbidden verifies that non-admin callers are rejected
// with HTTP 403 by the admin middleware applied via RegisterTeamRoutes.
func TestRouter_NonAdminForbidden(t *testing.T) {
	e, _ := setupRouterTest(t, nonAdminAuthCtx())

	rec := doRequest(t, e, http.MethodGet, "/api/v1/teams", "")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status 403 for non-admin via RegisterTeamRoutes, got %d: %s",
			rec.Code, rec.Body.String())
	}

	// Verify error envelope structure.
	resp := parseErrorResponse(t, rec)
	if resp.Error.Code != 403 {
		t.Errorf("expected error code 403, got %d", resp.Error.Code)
	}
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message")
	}
}

// TestRouter_AllEndpointsForbiddenForNonAdmin verifies that every team
// endpoint returns HTTP 403 when accessed by a non-admin caller through
// the RegisterTeamRoutes wiring.
func TestRouter_AllEndpointsForbiddenForNonAdmin(t *testing.T) {
	e, _ := setupRouterTest(t, nonAdminAuthCtx())

	testUUID := uuid.New().String()

	endpoints := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/v1/teams", `{"name":"test","slug":"test-slug"}`},
		{http.MethodGet, "/api/v1/teams", ""},
		{http.MethodGet, "/api/v1/teams/" + testUUID, ""},
		{http.MethodPost, "/api/v1/teams/" + testUUID + "/archive", ""},
		{http.MethodPost, "/api/v1/teams/" + testUUID + "/reactivate", ""},
		{http.MethodDelete, "/api/v1/teams/" + testUUID, ""},
		{http.MethodPost, "/api/v1/teams/" + testUUID + "/members", `{"user_id":"` + uuid.New().String() + `"}`},
		{http.MethodGet, "/api/v1/teams/" + testUUID + "/members", ""},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+"_"+ep.path, func(t *testing.T) {
			rec := doRequest(t, e, ep.method, ep.path, ep.body)

			if rec.Code != http.StatusForbidden {
				t.Errorf("expected status 403 for %s %s with non-admin via RegisterTeamRoutes, got %d: %s",
					ep.method, ep.path, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestRouter_NoAuthContextForbidden verifies that requests with no auth
// context at all are rejected with HTTP 403 when wired through
// RegisterTeamRoutes.
func TestRouter_NoAuthContextForbidden(t *testing.T) {
	db := openTestDB(t)
	createStubUsersTable(t, db)
	if err := teams.InitSchema(db); err != nil {
		t.Fatalf("InitSchema failed: %v", err)
	}

	e := echo.New()
	// Do NOT inject any auth context — simulates missing auth middleware.
	g := e.Group("/api/v1/teams")
	teams.RegisterTeamRoutes(g, db)

	rec := doRequest(t, e, http.MethodGet, "/api/v1/teams", "")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status 403 without auth context via RegisterTeamRoutes, got %d: %s",
			rec.Code, rec.Body.String())
	}
}

// TestRouter_AdminCanCreateTeam verifies end-to-end that an admin can create
// a team through the production RegisterTeamRoutes wiring with correct
// response format (Content-Type, timestamp precision, team object).
func TestRouter_AdminCanCreateTeam(t *testing.T) {
	e, db := setupRouterTest(t, adminAuthCtx())

	body := `{"name": "  Router Test Team  ", "slug": "router-test-team", "url": "https://example.com"}`
	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams", body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify Content-Type.
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected Content-Type containing 'application/json', got %q", ct)
	}

	// Parse and verify team response.
	resp := parseTeamResponse(t, rec)
	if resp.Name != "Router Test Team" {
		t.Errorf("expected trimmed name %q, got %q", "Router Test Team", resp.Name)
	}
	if resp.Slug != "router-test-team" {
		t.Errorf("expected slug %q, got %q", "router-test-team", resp.Slug)
	}
	if resp.Status != "active" {
		t.Errorf("expected status 'active', got %q", resp.Status)
	}

	// Verify timestamps are RFC3339 UTC with microsecond precision.
	if !rfc3339MicroRe.MatchString(resp.CreatedAt) {
		t.Errorf("created_at does not match RFC3339 microsecond format: %q", resp.CreatedAt)
	}
	if !rfc3339MicroRe.MatchString(resp.UpdatedAt) {
		t.Errorf("updated_at does not match RFC3339 microsecond format: %q", resp.UpdatedAt)
	}

	// Verify DB row exists.
	var count int
	if err := db.QueryRow("SELECT count(*) FROM teams WHERE id = ?", resp.ID).Scan(&count); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 team row with id %q, got %d", resp.ID, count)
	}
}

// TestRouter_AdminCanManageLifecycle verifies the full lifecycle through
// RegisterTeamRoutes: create → archive → reactivate → archive → delete.
func TestRouter_AdminCanManageLifecycle(t *testing.T) {
	e, db := setupRouterTest(t, adminAuthCtx())

	// Step 1: Create a team.
	createBody := `{"name": "Lifecycle Team", "slug": "lifecycle-router"}`
	createRec := doRequest(t, e, http.MethodPost, "/api/v1/teams", createBody)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", createRec.Code, createRec.Body.String())
	}
	createResp := parseTeamResponse(t, createRec)
	teamID := createResp.ID

	// Step 2: Archive the team.
	archiveRec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/archive", "")
	if archiveRec.Code != http.StatusOK {
		t.Fatalf("archive: expected 200, got %d: %s", archiveRec.Code, archiveRec.Body.String())
	}
	archiveResp := parseTeamResponse(t, archiveRec)
	if archiveResp.Status != "archived" {
		t.Errorf("archive: expected status 'archived', got %q", archiveResp.Status)
	}

	// Step 3: Reactivate.
	reactRec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/reactivate", "")
	if reactRec.Code != http.StatusOK {
		t.Fatalf("reactivate: expected 200, got %d: %s", reactRec.Code, reactRec.Body.String())
	}
	reactResp := parseTeamResponse(t, reactRec)
	if reactResp.Status != "active" {
		t.Errorf("reactivate: expected status 'active', got %q", reactResp.Status)
	}

	// Step 4: Archive again for deletion.
	archive2Rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/archive", "")
	if archive2Rec.Code != http.StatusOK {
		t.Fatalf("archive2: expected 200, got %d: %s", archive2Rec.Code, archive2Rec.Body.String())
	}

	// Step 5: Delete.
	deleteRec := doRequest(t, e, http.MethodDelete, "/api/v1/teams/"+teamID, "")
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d: %s", deleteRec.Code, deleteRec.Body.String())
	}

	// Step 6: Verify team is gone.
	var count int
	if err := db.QueryRow("SELECT count(*) FROM teams WHERE id = ?", teamID).Scan(&count); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected team to be deleted, got count %d", count)
	}

	// Step 7: GET on deleted team returns 404.
	getRec := doRequest(t, e, http.MethodGet, "/api/v1/teams/"+teamID, "")
	if getRec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for deleted team, got %d: %s", getRec.Code, getRec.Body.String())
	}
}

// TestRouter_ErrorEnvelopeFormat verifies that error responses through
// RegisterTeamRoutes use the nested error envelope format from spec 01.
func TestRouter_ErrorEnvelopeFormat(t *testing.T) {
	e, _ := setupRouterTest(t, adminAuthCtx())

	// Trigger a 400 error (invalid UUID).
	rec := doRequest(t, e, http.MethodGet, "/api/v1/teams/not-a-uuid", "")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseErrorResponse(t, rec)
	if resp.Error.Code != 400 {
		t.Errorf("expected error code 400, got %d", resp.Error.Code)
	}
	if resp.Error.Message != "invalid id format" {
		t.Errorf("expected message %q, got %q", "invalid id format", resp.Error.Message)
	}

	// Verify Content-Type on error response.
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected Content-Type containing 'application/json' on error, got %q", ct)
	}
}
