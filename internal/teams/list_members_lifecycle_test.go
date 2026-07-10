package teams_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/auth"
	"github.com/agent-fox-dev/hub/internal/teams"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// ---------------------------------------------------------------------------
// Test Helpers (list members, lifecycle, admin middleware, response format)
// ---------------------------------------------------------------------------

// setupListMembersTest initializes a test database, Echo instance, and
// registers team routes. Returns the Echo instance and database.
func setupListMembersTest(t *testing.T) (*echo.Echo, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	createStubUsersTable(t, db)
	if err := teams.InitSchema(db); err != nil {
		t.Fatalf("InitSchema failed: %v", err)
	}

	store := teams.NewStore(db)
	handler := teams.NewHandler(store)

	e := echo.New()
	g := e.Group("/api/v1/teams")
	handler.RegisterRoutes(g)

	return e, db
}

// setTestAuthContext is a test helper middleware that injects an AuthContext
// into the Echo context, simulating the auth middleware from server_foundation.
func setTestAuthContext(ac *auth.AuthContext) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if ac != nil {
				c.Set(string(auth.AuthContextKey), *ac)
			}
			return next(c)
		}
	}
}

// adminAuthCtx returns an AuthContext representing an admin caller.
func adminAuthCtx() *auth.AuthContext {
	return &auth.AuthContext{
		CredentialType: auth.CredentialTypeAdmin,
		IsAdmin:        true,
	}
}

// nonAdminAuthCtx returns an AuthContext representing a non-admin caller.
func nonAdminAuthCtx() *auth.AuthContext {
	return &auth.AuthContext{
		CredentialType: auth.CredentialTypeAPIKey,
		UserID:         uuid.New().String(),
		IsAdmin:        false,
	}
}

// setupWithAdminMiddlewareAndAuth sets up Echo with AdminRequired middleware
// AND injects the given auth context. This simulates the full auth + admin
// middleware chain.
func setupWithAdminMiddlewareAndAuth(t *testing.T, ac *auth.AuthContext) (*echo.Echo, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	createStubUsersTable(t, db)
	if err := teams.InitSchema(db); err != nil {
		t.Fatalf("InitSchema failed: %v", err)
	}

	store := teams.NewStore(db)
	handler := teams.NewHandler(store)

	e := echo.New()
	// Apply auth context injection first, then admin middleware.
	g := e.Group("/api/v1/teams", setTestAuthContext(ac), teams.AdminRequired())
	handler.RegisterRoutes(g)

	return e, db
}

// parseMemberListResponse unmarshals a JSON array of member objects.
func parseMemberListResponse(t *testing.T, rec *httptest.ResponseRecorder) []memberResponse {
	t.Helper()
	var members []memberResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &members); err != nil {
		t.Fatalf("failed to parse member list response: %v\nbody: %s", err, rec.Body.String())
	}
	return members
}

// seedMemberWithTime inserts a team_members row with a specific created_at timestamp.
func seedMemberWithTime(t *testing.T, db *sql.DB, teamID, userID string, createdAt time.Time) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO team_members (team_id, user_id, created_at) VALUES (?, ?, ?)`,
		teamID, userID, teams.FormatTime(createdAt),
	)
	if err != nil {
		t.Fatalf("failed to seed member with time (team=%s, user=%s): %v", teamID, userID, err)
	}
}

// rfc3339MicroRe matches timestamps in format YYYY-MM-DDTHH:MM:SS.ffffffZ.
var rfc3339MicroRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{6}Z$`)

// ===========================================================================
// 7.1: List team members endpoint tests (TS-03-44, TS-03-45, TS-03-46, TS-03-47)
// Requirements: 03-REQ-9.1, 03-REQ-9.2, 03-REQ-9.3, 03-REQ-9.4
// ===========================================================================

// ---------------------------------------------------------------------------
// TS-03-44: GET /api/v1/teams/:id/members returns members ordered by joined_at
// Requirement: 03-REQ-9.1
// ---------------------------------------------------------------------------

func TestListMembers_Success(t *testing.T) {
	e, db := setupListMembersTest(t)

	teamID := seedTeamWithStatus(t, db, "Active Team", "active-members", "active")

	// Seed 3 users and add them as members with staggered timestamps.
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	u1 := seedUser(t, db, "user1-lm@example.com", "User One")
	u2 := seedUser(t, db, "user2-lm@example.com", "User Two")
	u3 := seedUser(t, db, "user3-lm@example.com", "User Three")

	seedMemberWithTime(t, db, teamID, u1, baseTime)
	seedMemberWithTime(t, db, teamID, u2, baseTime.Add(1*time.Hour))
	seedMemberWithTime(t, db, teamID, u3, baseTime.Add(2*time.Hour))

	rec := doRequest(t, e, http.MethodGet, "/api/v1/teams/"+teamID+"/members", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	members := parseMemberListResponse(t, rec)
	if len(members) != 3 {
		t.Fatalf("expected 3 members, got %d", len(members))
	}

	// Verify ordering by joined_at ascending.
	if members[0].UserID != u1 {
		t.Errorf("expected first member user_id %q, got %q", u1, members[0].UserID)
	}
	if members[1].UserID != u2 {
		t.Errorf("expected second member user_id %q, got %q", u2, members[1].UserID)
	}
	if members[2].UserID != u3 {
		t.Errorf("expected third member user_id %q, got %q", u3, members[2].UserID)
	}

	// Verify each member has the expected fields.
	for i, m := range members {
		if m.TeamID != teamID {
			t.Errorf("member[%d]: expected team_id %q, got %q", i, teamID, m.TeamID)
		}
		if m.Email == "" {
			t.Errorf("member[%d]: expected non-empty email", i)
		}
		if m.Name == "" {
			t.Errorf("member[%d]: expected non-empty name", i)
		}
		if !rfc3339MicroRe.MatchString(m.JoinedAt) {
			t.Errorf("member[%d]: joined_at does not match RFC3339 microsecond format: %q", i, m.JoinedAt)
		}
	}

	// Verify ordering by timestamp values.
	for i := 0; i < len(members)-1; i++ {
		t1, err1 := time.Parse("2006-01-02T15:04:05.000000Z", members[i].JoinedAt)
		t2, err2 := time.Parse("2006-01-02T15:04:05.000000Z", members[i+1].JoinedAt)
		if err1 != nil || err2 != nil {
			t.Logf("skipping timestamp comparison due to parse error")
			continue
		}
		if !t1.Before(t2) && !t1.Equal(t2) {
			t.Errorf("members not ordered: [%d].joined_at=%s >= [%d].joined_at=%s",
				i, members[i].JoinedAt, i+1, members[i+1].JoinedAt)
		}
	}
}

// TestListMembers_ArchivedTeam verifies that listing members of an archived team
// still works (returns HTTP 200 with member data).
func TestListMembers_ArchivedTeam(t *testing.T) {
	e, db := setupListMembersTest(t)

	teamID := seedTeamWithStatus(t, db, "Archived Team", "archived-members", "archived")
	u1 := seedUser(t, db, "archived-member@example.com", "Archived Member")
	seedMember(t, db, teamID, u1)

	rec := doRequest(t, e, http.MethodGet, "/api/v1/teams/"+teamID+"/members", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	members := parseMemberListResponse(t, rec)
	if len(members) != 1 {
		t.Fatalf("expected 1 member for archived team, got %d", len(members))
	}
}

// TestListMembers_EmptyTeam verifies that listing members of a team with no
// members returns HTTP 200 with an empty JSON array.
func TestListMembers_EmptyTeam(t *testing.T) {
	e, db := setupListMembersTest(t)

	teamID := seedTeamWithStatus(t, db, "Empty Team", "empty-members", "active")

	rec := doRequest(t, e, http.MethodGet, "/api/v1/teams/"+teamID+"/members", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	members := parseMemberListResponse(t, rec)
	if len(members) != 0 {
		t.Errorf("expected 0 members, got %d", len(members))
	}

	// Verify it's an empty array, not null.
	body := strings.TrimSpace(rec.Body.String())
	if body != "[]" && body != "[]\n" {
		t.Errorf("expected empty JSON array [], got %q", body)
	}
}

// ---------------------------------------------------------------------------
// TS-03-45: GET /api/v1/teams/:id/members returns 404 for deleted/nonexistent team
// Requirement: 03-REQ-9.2
// ---------------------------------------------------------------------------

func TestListMembers_TeamNotFound(t *testing.T) {
	e, db := setupListMembersTest(t)

	t.Run("nonexistent_team", func(t *testing.T) {
		nonexistentID := uuid.New().String()
		rec := doRequest(t, e, http.MethodGet, "/api/v1/teams/"+nonexistentID+"/members", "")

		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected status 404, got %d: %s", rec.Code, rec.Body.String())
		}

		resp := parseErrorResponse(t, rec)
		if resp.Error.Code != 404 {
			t.Errorf("expected error code 404, got %d", resp.Error.Code)
		}
		if resp.Error.Message != "team not found" {
			t.Errorf("expected message %q, got %q", "team not found", resp.Error.Message)
		}
	})

	t.Run("deleted_team", func(t *testing.T) {
		deletedID := seedTeamWithStatus(t, db, "Deleted Team LM", "deleted-lm", "deleted")
		rec := doRequest(t, e, http.MethodGet, "/api/v1/teams/"+deletedID+"/members", "")

		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected status 404, got %d: %s", rec.Code, rec.Body.String())
		}

		resp := parseErrorResponse(t, rec)
		if resp.Error.Code != 404 {
			t.Errorf("expected error code 404, got %d", resp.Error.Code)
		}
		if resp.Error.Message != "team not found" {
			t.Errorf("expected message %q, got %q", "team not found", resp.Error.Message)
		}
	})
}

// ---------------------------------------------------------------------------
// TS-03-46: GET /api/v1/teams/:id/members with invalid UUID → HTTP 400
// Requirement: 03-REQ-9.3
// ---------------------------------------------------------------------------

func TestListMembers_InvalidUUID(t *testing.T) {
	e, _ := setupListMembersTest(t)

	rec := doRequest(t, e, http.MethodGet, "/api/v1/teams/bad-uuid/members", "")

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
}

// ---------------------------------------------------------------------------
// TS-03-47: GET /api/v1/teams/:id/members returns all 20 members, no pagination
// Requirement: 03-REQ-9.4
// ---------------------------------------------------------------------------

func TestListMembers_NoPagination(t *testing.T) {
	e, db := setupListMembersTest(t)

	teamID := seedTeamWithStatus(t, db, "Big Team", "big-team", "active")

	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	userIDs := make([]string, 20)
	for i := range 20 {
		uid := seedUser(t, db, fmt.Sprintf("bigteam-user-%d@example.com", i), fmt.Sprintf("User %d", i))
		userIDs[i] = uid
		seedMemberWithTime(t, db, teamID, uid, baseTime.Add(time.Duration(i)*time.Minute))
	}

	rec := doRequest(t, e, http.MethodGet, "/api/v1/teams/"+teamID+"/members", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	members := parseMemberListResponse(t, rec)
	if len(members) != 20 {
		t.Fatalf("expected 20 members (no pagination), got %d", len(members))
	}

	// Verify ordering by joined_at ascending.
	for i := 0; i < len(members)-1; i++ {
		t1, err1 := time.Parse("2006-01-02T15:04:05.000000Z", members[i].JoinedAt)
		t2, err2 := time.Parse("2006-01-02T15:04:05.000000Z", members[i+1].JoinedAt)
		if err1 != nil || err2 != nil {
			continue
		}
		if t1.After(t2) {
			t.Errorf("members not ordered at index %d: %s > %s",
				i, members[i].JoinedAt, members[i+1].JoinedAt)
		}
	}
}

// ===========================================================================
// 7.2: Admin middleware enforcement tests (TS-03-48, TS-03-49)
// Requirements: 03-REQ-10.1, 03-REQ-10.2
// ===========================================================================

// ---------------------------------------------------------------------------
// TS-03-48: Admin middleware returns HTTP 403 for non-admin users
// Requirement: 03-REQ-10.1
// ---------------------------------------------------------------------------

func TestAdminMiddleware_NonAdminForbidden(t *testing.T) {
	e, _ := setupWithAdminMiddlewareAndAuth(t, nonAdminAuthCtx())

	rec := doRequest(t, e, http.MethodGet, "/api/v1/teams", "")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status 403 for non-admin, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify error envelope format.
	resp := parseErrorResponse(t, rec)
	if resp.Error.Code != 403 {
		t.Errorf("expected error code 403, got %d", resp.Error.Code)
	}
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message")
	}
}

// TestAdminMiddleware_AdminAllowed verifies that admin callers pass through the
// middleware and reach the handler.
func TestAdminMiddleware_AdminAllowed(t *testing.T) {
	e, _ := setupWithAdminMiddlewareAndAuth(t, adminAuthCtx())

	// GET /api/v1/teams should return 200 (empty list) for an admin.
	rec := doRequest(t, e, http.MethodGet, "/api/v1/teams", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200 for admin, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// TS-03-49: All 8 team endpoints return HTTP 403 for non-admin
// Requirement: 03-REQ-10.2
// ---------------------------------------------------------------------------

func TestAdminMiddleware_AllEndpoints(t *testing.T) {
	e, _ := setupWithAdminMiddlewareAndAuth(t, nonAdminAuthCtx())

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
				t.Errorf("expected status 403 for %s %s with non-admin, got %d: %s",
					ep.method, ep.path, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestAdminMiddleware_NoAuthContext verifies that requests without any auth
// context are rejected with HTTP 403.
func TestAdminMiddleware_NoAuthContext(t *testing.T) {
	db := openTestDB(t)
	createStubUsersTable(t, db)
	if err := teams.InitSchema(db); err != nil {
		t.Fatalf("InitSchema failed: %v", err)
	}

	store := teams.NewStore(db)
	handler := teams.NewHandler(store)

	e := echo.New()
	// Apply AdminRequired middleware WITHOUT auth context injection.
	g := e.Group("/api/v1/teams", teams.AdminRequired())
	handler.RegisterRoutes(g)

	rec := doRequest(t, e, http.MethodGet, "/api/v1/teams", "")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status 403 without auth context, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ===========================================================================
// 7.3: Lifecycle state machine and updated_at semantics (TS-03-50–53)
// Requirements: 03-REQ-11.1, 03-REQ-11.2, 03-REQ-11.3, 03-REQ-11.4
// ===========================================================================

// ---------------------------------------------------------------------------
// TS-03-50: Active team: reactivation rejected with HTTP 409
// Requirement: 03-REQ-11.1
// ---------------------------------------------------------------------------

func TestLifecycle_ActiveCannotReactivate(t *testing.T) {
	e, db := setupListMembersTest(t)

	teamID := seedTeamWithStatus(t, db, "Lifecycle Active", "lifecycle-active", "active")

	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/reactivate", "")

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseErrorResponse(t, rec)
	if resp.Error.Message != "team is already active" {
		t.Errorf("expected message %q, got %q", "team is already active", resp.Error.Message)
	}

	// Team remains active.
	var status string
	if err := db.QueryRow("SELECT status FROM teams WHERE id = ?", teamID).Scan(&status); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if status != "active" {
		t.Errorf("expected status 'active', got %q", status)
	}
}

// TestLifecycle_ActiveCanArchive verifies the active→archived transition works.
func TestLifecycle_ActiveCanArchive(t *testing.T) {
	e, db := setupListMembersTest(t)

	teamID := seedTeamWithStatus(t, db, "Can Archive", "can-archive", "active")

	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/archive", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseTeamResponse(t, rec)
	if resp.Status != "archived" {
		t.Errorf("expected status 'archived', got %q", resp.Status)
	}
}

// ---------------------------------------------------------------------------
// TS-03-51: Archived team: re-archive rejected with HTTP 409
// Requirement: 03-REQ-11.2
// ---------------------------------------------------------------------------

func TestLifecycle_ArchivedCannotArchive(t *testing.T) {
	e, db := setupListMembersTest(t)

	teamID := seedTeamWithStatus(t, db, "Lifecycle Archived", "lifecycle-archived", "archived")

	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/archive", "")

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseErrorResponse(t, rec)
	if resp.Error.Message != "team is already archived" {
		t.Errorf("expected message %q, got %q", "team is already archived", resp.Error.Message)
	}

	// Team remains archived.
	var status string
	if err := db.QueryRow("SELECT status FROM teams WHERE id = ?", teamID).Scan(&status); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if status != "archived" {
		t.Errorf("expected status 'archived', got %q", status)
	}
}

// TestLifecycle_ArchivedCanReactivate verifies the archived→active transition works.
func TestLifecycle_ArchivedCanReactivate(t *testing.T) {
	e, db := setupListMembersTest(t)

	teamID := seedTeamWithStatus(t, db, "Can React", "can-react", "archived")

	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/reactivate", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseTeamResponse(t, rec)
	if resp.Status != "active" {
		t.Errorf("expected status 'active', got %q", resp.Status)
	}
}

// TestLifecycle_ArchivedCanDelete verifies archived→deleted transition works.
func TestLifecycle_ArchivedCanDelete(t *testing.T) {
	e, db := setupListMembersTest(t)

	teamID := seedTeamWithStatus(t, db, "Can Delete", "can-delete", "archived")

	rec := doRequest(t, e, http.MethodDelete, "/api/v1/teams/"+teamID, "")

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// Team is physically removed.
	var count int
	if err := db.QueryRow("SELECT count(*) FROM teams WHERE id = ?", teamID).Scan(&count); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected team to be deleted, got count %d", count)
	}
}

// TestLifecycle_ActiveCannotDeleteDirectly verifies active→deleted is rejected.
func TestLifecycle_ActiveCannotDeleteDirectly(t *testing.T) {
	e, db := setupListMembersTest(t)

	teamID := seedTeamWithStatus(t, db, "No Direct Del", "no-direct-del", "active")

	rec := doRequest(t, e, http.MethodDelete, "/api/v1/teams/"+teamID, "")

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseErrorResponse(t, rec)
	if resp.Error.Message != "team must be archived before deletion" {
		t.Errorf("expected message %q, got %q", "team must be archived before deletion", resp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// TS-03-52: All endpoints return HTTP 404 for deleted team's UUID
// Requirement: 03-REQ-11.3
// ---------------------------------------------------------------------------

func TestLifecycle_DeletedTeamInaccessible(t *testing.T) {
	e, _ := setupListMembersTest(t)

	// Use a UUID that doesn't exist in the database (simulates a deleted team).
	deletedID := uuid.New().String()

	endpoints := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/teams/" + deletedID, ""},
		{http.MethodPost, "/api/v1/teams/" + deletedID + "/archive", ""},
		{http.MethodPost, "/api/v1/teams/" + deletedID + "/reactivate", ""},
		{http.MethodDelete, "/api/v1/teams/" + deletedID, ""},
		{http.MethodPost, "/api/v1/teams/" + deletedID + "/members", `{"user_id":"` + uuid.New().String() + `"}`},
		{http.MethodGet, "/api/v1/teams/" + deletedID + "/members", ""},
	}

	for _, ep := range endpoints {
		name := ep.method + "_" + strings.ReplaceAll(ep.path, deletedID, "ID")
		t.Run(name, func(t *testing.T) {
			rec := doRequest(t, e, ep.method, ep.path, ep.body)

			if rec.Code != http.StatusNotFound {
				t.Errorf("expected status 404 for %s %s, got %d: %s",
					ep.method, ep.path, rec.Code, rec.Body.String())
				return
			}

			resp := parseErrorResponse(t, rec)
			if resp.Error.Message != "team not found" {
				t.Errorf("expected message %q for %s %s, got %q",
					"team not found", ep.method, ep.path, resp.Error.Message)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TS-03-53: updated_at refreshed on archive and reactivate
// Requirement: 03-REQ-11.4
// ---------------------------------------------------------------------------

func TestLifecycle_UpdatedAtRefreshed(t *testing.T) {
	e, db := setupListMembersTest(t)

	// Seed a team with a deliberately old updated_at.
	oldTime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	teamID := uuid.New().String()
	_, err := db.Exec(
		`INSERT INTO teams (id, name, slug, url, status, created_at, updated_at) VALUES (?, ?, ?, NULL, ?, ?, ?)`,
		teamID, "UpdatedAt Team", "updatedat-team", "active",
		teams.FormatTime(oldTime), teams.FormatTime(oldTime),
	)
	if err != nil {
		t.Fatalf("failed to seed team: %v", err)
	}

	// Archive: updated_at should be refreshed.
	rec1 := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/archive", "")
	if rec1.Code != http.StatusOK {
		t.Fatalf("archive: expected 200, got %d: %s", rec1.Code, rec1.Body.String())
	}
	resp1 := parseTeamResponse(t, rec1)
	t1, err := time.Parse("2006-01-02T15:04:05.000000Z", resp1.UpdatedAt)
	if err != nil {
		t.Fatalf("failed to parse updated_at after archive: %v", err)
	}
	if !t1.After(oldTime) {
		t.Errorf("expected updated_at after archive (%s) to be after seed time (%s)",
			resp1.UpdatedAt, teams.FormatTime(oldTime))
	}

	// Reactivate: updated_at should be refreshed again.
	rec2 := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/reactivate", "")
	if rec2.Code != http.StatusOK {
		t.Fatalf("reactivate: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	resp2 := parseTeamResponse(t, rec2)
	t2, err := time.Parse("2006-01-02T15:04:05.000000Z", resp2.UpdatedAt)
	if err != nil {
		t.Fatalf("failed to parse updated_at after reactivate: %v", err)
	}
	if t2.Before(t1) {
		t.Errorf("expected updated_at after reactivate (%s) to be >= archive time (%s)",
			resp2.UpdatedAt, resp1.UpdatedAt)
	}
}

// TestLifecycle_MemberAddDoesNotChangeUpdatedAt verifies that adding a member
// does not change the team's updated_at timestamp.
func TestLifecycle_MemberAddDoesNotChangeUpdatedAt(t *testing.T) {
	e, db := setupListMembersTest(t)

	teamID := seedTeamWithStatus(t, db, "No Update Team", "no-update-team", "active")

	// Record the team's updated_at before member add.
	var beforeUpdatedAt string
	if err := db.QueryRow("SELECT updated_at FROM teams WHERE id = ?", teamID).Scan(&beforeUpdatedAt); err != nil {
		t.Fatalf("query error: %v", err)
	}

	// Add a member.
	userID := seedUser(t, db, "noupdate@example.com", "No Update User")
	body := fmt.Sprintf(`{"user_id": "%s"}`, userID)
	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/members", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Check updated_at is unchanged.
	var afterUpdatedAt string
	if err := db.QueryRow("SELECT updated_at FROM teams WHERE id = ?", teamID).Scan(&afterUpdatedAt); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if beforeUpdatedAt != afterUpdatedAt {
		t.Errorf("expected updated_at unchanged after member add: before=%q, after=%q",
			beforeUpdatedAt, afterUpdatedAt)
	}
}

// ===========================================================================
// 7.4: Response format tests (TS-03-54, TS-03-55, TS-03-56)
// Requirements: 03-REQ-12.1, 03-REQ-12.2, 03-REQ-12.3
// ===========================================================================

// ---------------------------------------------------------------------------
// TS-03-54: Content-Type: application/json on all successful responses
// Requirement: 03-REQ-12.1
// ---------------------------------------------------------------------------

func TestResponseFormat_ContentType(t *testing.T) {
	e, db := setupListMembersTest(t)

	teamID := seedTeamWithStatus(t, db, "CT Team", "ct-team", "active")

	successEndpoints := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/teams"},
		{http.MethodGet, "/api/v1/teams/" + teamID},
	}

	for _, ep := range successEndpoints {
		t.Run(ep.method+"_"+ep.path, func(t *testing.T) {
			rec := doRequest(t, e, ep.method, ep.path, "")

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
			}

			ct := rec.Header().Get("Content-Type")
			if !strings.Contains(ct, "application/json") {
				t.Errorf("expected Content-Type containing 'application/json', got %q", ct)
			}
		})
	}
}

// TestResponseFormat_ContentTypeOnCreate verifies Content-Type on POST create.
func TestResponseFormat_ContentTypeOnCreate(t *testing.T) {
	e, _ := setupListMembersTest(t)

	body := `{"name": "CT Create Team", "slug": "ct-create-team"}`
	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams", body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rec.Code, rec.Body.String())
	}

	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected Content-Type containing 'application/json', got %q", ct)
	}
}

// TestResponseFormat_ContentTypeOnListMembers verifies Content-Type on member list.
func TestResponseFormat_ContentTypeOnListMembers(t *testing.T) {
	e, db := setupListMembersTest(t)

	teamID := seedTeamWithStatus(t, db, "CT Members Team", "ct-members", "active")

	rec := doRequest(t, e, http.MethodGet, "/api/v1/teams/"+teamID+"/members", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected Content-Type containing 'application/json', got %q", ct)
	}
}

// ---------------------------------------------------------------------------
// TS-03-55: Error responses use standard error envelope
// Requirement: 03-REQ-12.2
// ---------------------------------------------------------------------------

func TestResponseFormat_ErrorEnvelope(t *testing.T) {
	e, _ := setupListMembersTest(t)

	// Trigger a 404 error.
	nonexistentID := uuid.New().String()
	rec := doRequest(t, e, http.MethodGet, "/api/v1/teams/"+nonexistentID, "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d: %s", rec.Code, rec.Body.String())
	}

	// Parse raw JSON to verify structure.
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	// Verify nested error envelope structure: {"error": {"code": <int>, "message": <string>}}
	errorObj, ok := raw["error"]
	if !ok {
		t.Fatal("expected 'error' key in response")
	}

	errorMap, ok := errorObj.(map[string]any)
	if !ok {
		t.Fatal("expected 'error' to be an object")
	}

	code, ok := errorMap["code"]
	if !ok {
		t.Fatal("expected 'code' in error object")
	}
	codeFloat, ok := code.(float64)
	if !ok {
		t.Fatalf("expected 'code' to be a number, got %T", code)
	}
	if int(codeFloat) != 404 {
		t.Errorf("expected error code 404, got %v", code)
	}

	message, ok := errorMap["message"]
	if !ok {
		t.Fatal("expected 'message' in error object")
	}
	messageStr, ok := message.(string)
	if !ok {
		t.Fatalf("expected 'message' to be a string, got %T", message)
	}
	if messageStr != "team not found" {
		t.Errorf("expected message %q, got %q", "team not found", messageStr)
	}
}

// TestResponseFormat_ErrorEnvelopeVariousStatuses verifies error envelope
// structure for different HTTP status codes.
func TestResponseFormat_ErrorEnvelopeVariousStatuses(t *testing.T) {
	e, db := setupListMembersTest(t)

	cases := []struct {
		name           string
		method         string
		path           string
		body           string
		expectedStatus int
		expectedMsg    string
	}{
		{
			name:           "400_invalid_id",
			method:         http.MethodGet,
			path:           "/api/v1/teams/not-a-uuid",
			expectedStatus: 400,
			expectedMsg:    "invalid id format",
		},
		{
			name:           "422_invalid_slug",
			method:         http.MethodPost,
			path:           "/api/v1/teams",
			body:           `{"name": "Test", "slug": "-bad"}`,
			expectedStatus: 422,
			expectedMsg:    "invalid slug format",
		},
	}

	// Seed a team for 409 test.
	teamID := seedTeamWithStatus(t, db, "Envelope Team", "envelope-team", "archived")
	cases = append(cases, struct {
		name           string
		method         string
		path           string
		body           string
		expectedStatus int
		expectedMsg    string
	}{
		name:           "409_already_archived",
		method:         http.MethodPost,
		path:           "/api/v1/teams/" + teamID + "/archive",
		expectedStatus: 409,
		expectedMsg:    "team is already archived",
	})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRequest(t, e, tc.method, tc.path, tc.body)

			if rec.Code != tc.expectedStatus {
				t.Fatalf("expected status %d, got %d: %s", tc.expectedStatus, rec.Code, rec.Body.String())
			}

			resp := parseErrorResponse(t, rec)
			if resp.Error.Code != tc.expectedStatus {
				t.Errorf("expected error code %d, got %d", tc.expectedStatus, resp.Error.Code)
			}
			if resp.Error.Message != tc.expectedMsg {
				t.Errorf("expected message %q, got %q", tc.expectedMsg, resp.Error.Message)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TS-03-56: Timestamps in RFC3339 UTC with microsecond precision
// Requirement: 03-REQ-12.3
// ---------------------------------------------------------------------------

func TestResponseFormat_TimestampFormat(t *testing.T) {
	e, db := setupListMembersTest(t)

	// Create a team via API to test timestamps.
	body := `{"name": "TS Format Team", "slug": "ts-format-team"}`
	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams", body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rec.Code, rec.Body.String())
	}

	teamResp := parseTeamResponse(t, rec)

	// Verify team timestamps match RFC3339 UTC microsecond pattern.
	if !rfc3339MicroRe.MatchString(teamResp.CreatedAt) {
		t.Errorf("team created_at does not match RFC3339 microsecond format: %q", teamResp.CreatedAt)
	}
	if !rfc3339MicroRe.MatchString(teamResp.UpdatedAt) {
		t.Errorf("team updated_at does not match RFC3339 microsecond format: %q", teamResp.UpdatedAt)
	}

	// Add a member and verify joined_at timestamp format.
	userID := seedUser(t, db, "tsformat@example.com", "TS Format User")
	memberBody := fmt.Sprintf(`{"user_id": "%s"}`, userID)
	rec2 := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamResp.ID+"/members", memberBody)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	var memberResp memberResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &memberResp); err != nil {
		t.Fatalf("failed to parse member response: %v", err)
	}

	if !rfc3339MicroRe.MatchString(memberResp.JoinedAt) {
		t.Errorf("member joined_at does not match RFC3339 microsecond format: %q", memberResp.JoinedAt)
	}
}

// TestResponseFormat_TimestampFormatOnListMembers verifies joined_at timestamps
// in the list members response match RFC3339 UTC microsecond format.
func TestResponseFormat_TimestampFormatOnListMembers(t *testing.T) {
	e, db := setupListMembersTest(t)

	teamID := seedTeamWithStatus(t, db, "TS List Team", "ts-list-team", "active")
	u1 := seedUser(t, db, "tslist1@example.com", "TS List User 1")
	u2 := seedUser(t, db, "tslist2@example.com", "TS List User 2")
	seedMember(t, db, teamID, u1)
	seedMember(t, db, teamID, u2)

	rec := doRequest(t, e, http.MethodGet, "/api/v1/teams/"+teamID+"/members", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	members := parseMemberListResponse(t, rec)
	for i, m := range members {
		if !rfc3339MicroRe.MatchString(m.JoinedAt) {
			t.Errorf("member[%d] joined_at does not match RFC3339 microsecond format: %q", i, m.JoinedAt)
		}
	}
}

// TestResponseFormat_TimestampFormatOnListTeams verifies team timestamps
// in the list teams response match RFC3339 UTC microsecond format.
func TestResponseFormat_TimestampFormatOnListTeams(t *testing.T) {
	e, db := setupListMembersTest(t)

	seedTeamWithStatus(t, db, "TS LT1", "ts-lt-one", "active")
	seedTeamWithStatus(t, db, "TS LT2", "ts-lt-two", "active")

	rec := doRequest(t, e, http.MethodGet, "/api/v1/teams", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var teamsList []teamResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &teamsList); err != nil {
		t.Fatalf("failed to parse team list response: %v", err)
	}

	for i, tm := range teamsList {
		if !rfc3339MicroRe.MatchString(tm.CreatedAt) {
			t.Errorf("team[%d] created_at does not match RFC3339 microsecond format: %q", i, tm.CreatedAt)
		}
		if !rfc3339MicroRe.MatchString(tm.UpdatedAt) {
			t.Errorf("team[%d] updated_at does not match RFC3339 microsecond format: %q", i, tm.UpdatedAt)
		}
	}
}
