package users_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/agent-fox-dev/hub/internal/users"
)

// ---------------------------------------------------------------------------
// TS-02-17: Admin GET /api/v1/users returns HTTP 200 with all user records
// ordered by created_at ASC; provider_id omitted from each entry.
// Requirement: 02-REQ-5.1
// ---------------------------------------------------------------------------

func TestListUsers_AdminListsAll(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	// Insert two users with different created_at timestamps.
	insertTestUser(t, db, "user-a-uuid", "userA", "a@example.com", "User A",
		"active", "github", "ext-a", "2025-01-01T00:00:00Z")
	insertTestUser(t, db, "user-b-uuid", "userB", "b@example.com", "User B",
		"active", "github", "ext-b", "2025-02-01T00:00:00Z")

	e := setupEcho()
	e.GET("/api/v1/users", users.ListUsersHandler(db), setAuthContext(adminAuthContext()))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Users []map[string]any `json:"users"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if len(resp.Users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(resp.Users))
	}

	// Ordered by created_at ASC: userA first, then userB.
	if resp.Users[0]["username"] != "userA" {
		t.Errorf("expected first user to be 'userA', got %v", resp.Users[0]["username"])
	}
	if resp.Users[1]["username"] != "userB" {
		t.Errorf("expected second user to be 'userB', got %v", resp.Users[1]["username"])
	}

	// Verify each entry has required fields but no provider_id.
	requiredFields := []string{"id", "username", "email", "full_name", "status", "provider", "created_at", "updated_at"}
	for i, u := range resp.Users {
		for _, field := range requiredFields {
			if _, ok := u[field]; !ok {
				t.Errorf("user[%d] missing required field %q", i, field)
			}
		}
		if _, ok := u["provider_id"]; ok {
			t.Errorf("user[%d] should NOT include provider_id in list response", i)
		}
	}
}

// ---------------------------------------------------------------------------
// TS-02-18: Admin GET /api/v1/users returns HTTP 200 with empty users array
// when no users exist.
// Requirement: 02-REQ-5.2
// ---------------------------------------------------------------------------

func TestListUsers_EmptyList(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	e := setupEcho()
	e.GET("/api/v1/users", users.ListUsersHandler(db), setAuthContext(adminAuthContext()))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Users []map[string]any `json:"users"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp.Users == nil {
		t.Error("expected users to be an empty array, got nil")
	}
	if len(resp.Users) != 0 {
		t.Errorf("expected 0 users, got %d", len(resp.Users))
	}
}

// ---------------------------------------------------------------------------
// TS-02-19: Admin GET /api/v1/users/:id returns full user object with
// provider_id and team_memberships array.
// Requirement: 02-REQ-6.1
//
// NOTE: Reviewer finding (critical): the team_members table has no `role`
// column. Spec 03 lists roles as deferred/out-of-scope. This test checks
// the shape of the response; the `role` field value may need errata.
// See docs/errata/02_user_management_divergences.md.
// ---------------------------------------------------------------------------

func TestGetUser_AdminGetsFullUser(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	// Insert user with known provider_id.
	insertTestUser(t, db, "user-uuid-1", "detailuser", "detail@example.com", "Detail User",
		"active", "github", "ext-111", "2025-01-01T00:00:00Z")

	// Insert a team and team membership.
	_, err := db.Exec(`INSERT INTO teams (id, name, slug, status, created_at, updated_at)
		VALUES ('team-1', 'Alpha', 'alpha', 'active', '2025-01-01T00:00:00Z', '2025-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("failed to insert team: %v", err)
	}
	_, err = db.Exec(`INSERT INTO team_members (team_id, user_id, created_at)
		VALUES ('team-1', 'user-uuid-1', '2025-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("failed to insert team membership: %v", err)
	}

	e := setupEcho()
	e.GET("/api/v1/users/:id", users.GetUserHandler(db), setAuthContext(adminAuthContext()))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/user-uuid-1", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// Verify user fields including provider_id.
	if resp["id"] != "user-uuid-1" {
		t.Errorf("expected id 'user-uuid-1', got %v", resp["id"])
	}
	if resp["provider_id"] != "ext-111" {
		t.Errorf("expected provider_id 'ext-111', got %v", resp["provider_id"])
	}

	// Verify team_memberships array.
	memberships, ok := resp["team_memberships"]
	if !ok {
		t.Fatal("expected team_memberships field in response")
	}

	memberList, ok := memberships.([]any)
	if !ok {
		t.Fatalf("expected team_memberships to be an array, got %T", memberships)
	}
	if len(memberList) != 1 {
		t.Fatalf("expected 1 team membership, got %d", len(memberList))
	}

	member, ok := memberList[0].(map[string]any)
	if !ok {
		t.Fatalf("expected membership entry to be an object, got %T", memberList[0])
	}
	if member["team_id"] != "team-1" {
		t.Errorf("expected team_id 'team-1', got %v", member["team_id"])
	}
	if member["team_name"] != "Alpha" {
		t.Errorf("expected team_name 'Alpha', got %v", member["team_name"])
	}
	// NOTE: role field is expected per spec but team_members table lacks a role column.
	// See errata doc. The handler should provide a default value or derive it.
	if _, hasRole := member["role"]; !hasRole {
		t.Error("expected role field in team membership entry")
	}
}

// ---------------------------------------------------------------------------
// TS-02-E17: GET /api/v1/users returns HTTP 403 when called by a non-admin
// authenticated user.
// Requirement: 02-REQ-5.E1
// ---------------------------------------------------------------------------

func TestListUsers_EdgeCase_NonAdminForbidden(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	insertTestUser(t, db, "user-nonadmin-1", "nonadmin", "na@example.com", "",
		"active", "github", "na-001", "2025-01-01T00:00:00Z")

	e := setupEcho()
	e.GET("/api/v1/users", users.ListUsersHandler(db), setAuthContext(userAuthContext("user-nonadmin-1")))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected HTTP 403 for non-admin, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// TS-02-E18: GET /api/v1/users/:id returns HTTP 404 with structured error
// body when user ID does not exist.
// Requirement: 02-REQ-6.E1
// ---------------------------------------------------------------------------

func TestGetUser_EdgeCase_NotFound(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	e := setupEcho()
	e.GET("/api/v1/users/:id", users.GetUserHandler(db), setAuthContext(adminAuthContext()))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/non-existent-uuid", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected HTTP 404, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify structured error body.
	var resp struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse error body: %v", err)
	}
	if resp.Error.Code != http.StatusNotFound {
		t.Errorf("expected error code 404, got %d", resp.Error.Code)
	}
	if resp.Error.Message == "" {
		t.Error("expected non-empty error message")
	}
}

// ---------------------------------------------------------------------------
// TS-02-E19: GET /api/v1/users/:id returns HTTP 403 when called by a
// non-admin authenticated user.
// Requirement: 02-REQ-6.E2
// ---------------------------------------------------------------------------

func TestGetUser_EdgeCase_NonAdminForbidden(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	// Target user exists.
	insertTestUser(t, db, "user-uuid-target", "targetuser", "t@example.com", "",
		"active", "github", "t-001", "2025-01-01T00:00:00Z")
	// Calling user exists.
	insertTestUser(t, db, "user-uuid-caller", "calleruser", "c@example.com", "",
		"active", "github", "c-001", "2025-01-02T00:00:00Z")

	e := setupEcho()
	e.GET("/api/v1/users/:id", users.GetUserHandler(db), setAuthContext(userAuthContext("user-uuid-caller")))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/user-uuid-target", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected HTTP 403 for non-admin, got %d: %s", rec.Code, rec.Body.String())
	}
}
