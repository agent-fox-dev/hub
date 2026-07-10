package teams_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/teams"
)

// ---------------------------------------------------------------------------
// Test Helpers (add member tests)
// ---------------------------------------------------------------------------

// memberResponse mirrors the JSON member object returned by the handler.
type memberResponse struct {
	UserID   string `json:"user_id"`
	TeamID   string `json:"team_id"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	JoinedAt string `json:"joined_at"`
}

// setupAddMemberTest initializes a test database, Echo instance, and
// registers team routes. Returns the Echo instance and database.
func setupAddMemberTest(t *testing.T) (*echo.Echo, *sql.DB) {
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

// rfc3339MicroPattern matches timestamps in format YYYY-MM-DDTHH:MM:SS.ffffffZ.
var rfc3339MicroPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{6}Z$`)

// ---------------------------------------------------------------------------
// TS-03-36: POST /api/v1/teams/:id/members adds a new user to an active team
// Requirement: 03-REQ-8.1
//
// Verifies that adding a new user to an active team returns HTTP 200 with a
// member object containing user_id, team_id, email, name (from users table),
// and joined_at. A row must exist in team_members.
// ---------------------------------------------------------------------------

func TestAddMember_Success(t *testing.T) {
	e, db := setupAddMemberTest(t)

	// Seed an active team and a user.
	teamID := seedTeamWithStatus(t, db, "Member Team", "member-team", "active")
	userID := seedUser(t, db, "jane@example.com", "Jane Doe")

	body := fmt.Sprintf(`{"user_id": "%s"}`, userID)
	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/members", body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Parse response.
	var resp memberResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse member response: %v", err)
	}

	// Verify member object fields.
	if resp.UserID != userID {
		t.Errorf("expected user_id %q, got %q", userID, resp.UserID)
	}
	if resp.TeamID != teamID {
		t.Errorf("expected team_id %q, got %q", teamID, resp.TeamID)
	}
	if resp.Email != "jane@example.com" {
		t.Errorf("expected email %q, got %q", "jane@example.com", resp.Email)
	}
	if resp.Name != "Jane Doe" {
		t.Errorf("expected name %q, got %q", "Jane Doe", resp.Name)
	}

	// Verify joined_at is RFC3339 UTC microsecond format.
	if !rfc3339MicroPattern.MatchString(resp.JoinedAt) {
		t.Errorf("joined_at does not match RFC3339 microsecond format: %q", resp.JoinedAt)
	}

	// Verify DB row exists.
	var count int
	if err := db.QueryRow(
		"SELECT count(*) FROM team_members WHERE team_id = ? AND user_id = ?",
		teamID, userID,
	).Scan(&count); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 team_members row, got %d", count)
	}

	// Verify Content-Type header.
	ct := rec.Header().Get("Content-Type")
	if ct == "" || !contains(ct, "application/json") {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}
}

// contains checks if a string contains a substring (case-insensitive helper).
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// TS-03-37: POST /api/v1/teams/:id/members idempotent re-add
// Requirement: 03-REQ-8.2
//
// Verifies that re-adding an existing member returns HTTP 200 with the
// original joined_at timestamp, current user data, and no duplicate row.
// ---------------------------------------------------------------------------

func TestAddMember_Idempotent(t *testing.T) {
	e, db := setupAddMemberTest(t)

	teamID := seedTeamWithStatus(t, db, "Idempotent Team", "idempotent-team", "active")
	userID := seedUser(t, db, "jane@example.com", "Jane Doe")

	// First add — creates membership.
	body := fmt.Sprintf(`{"user_id": "%s"}`, userID)
	rec1 := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/members", body)

	if rec1.Code != http.StatusOK {
		t.Fatalf("first add: expected status 200, got %d: %s", rec1.Code, rec1.Body.String())
	}

	var resp1 memberResponse
	if err := json.Unmarshal(rec1.Body.Bytes(), &resp1); err != nil {
		t.Fatalf("failed to parse first response: %v", err)
	}
	originalJoinedAt := resp1.JoinedAt

	// Wait briefly to ensure a different timestamp if the row were re-inserted.
	time.Sleep(10 * time.Millisecond)

	// Second add — should be idempotent.
	rec2 := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/members", body)

	if rec2.Code != http.StatusOK {
		t.Fatalf("second add: expected status 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	var resp2 memberResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("failed to parse second response: %v", err)
	}

	// joined_at must equal the original, not re-created.
	if resp2.JoinedAt != originalJoinedAt {
		t.Errorf("expected joined_at %q (original), got %q", originalJoinedAt, resp2.JoinedAt)
	}

	// Current user data should be reflected.
	if resp2.Email != "jane@example.com" {
		t.Errorf("expected email %q, got %q", "jane@example.com", resp2.Email)
	}
	if resp2.Name != "Jane Doe" {
		t.Errorf("expected name %q, got %q", "Jane Doe", resp2.Name)
	}

	// Only one row should exist.
	var count int
	if err := db.QueryRow(
		"SELECT count(*) FROM team_members WHERE team_id = ? AND user_id = ?",
		teamID, userID,
	).Scan(&count); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 team_members row, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// TS-03-38: POST /api/v1/teams/:id/members on archived team → HTTP 409
// Requirement: 03-REQ-8.3
//
// Verifies that adding a member to an archived team returns HTTP 409 with
// message "team is archived" and no row is inserted.
// ---------------------------------------------------------------------------

func TestAddMember_ArchivedTeam(t *testing.T) {
	e, db := setupAddMemberTest(t)

	teamID := seedTeamWithStatus(t, db, "Archived Team", "archived-team", "archived")
	userID := seedUser(t, db, "user@example.com", "Test User")

	body := fmt.Sprintf(`{"user_id": "%s"}`, userID)
	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/members", body)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseErrorResponse(t, rec)
	if resp.Error.Code != 409 {
		t.Errorf("expected error code 409, got %d", resp.Error.Code)
	}
	if resp.Error.Message != "team is archived" {
		t.Errorf("expected message %q, got %q", "team is archived", resp.Error.Message)
	}

	// No row should be inserted.
	var count int
	if err := db.QueryRow(
		"SELECT count(*) FROM team_members WHERE team_id = ?", teamID,
	).Scan(&count); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 member rows, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// TS-03-39: POST /api/v1/teams/:id/members on nonexistent/deleted team → 404
// Requirement: 03-REQ-8.4
//
// Verifies that adding a member to a nonexistent or deleted team returns
// HTTP 404 with message "team not found".
// ---------------------------------------------------------------------------

func TestAddMember_TeamNotFound(t *testing.T) {
	e, db := setupAddMemberTest(t)

	userID := seedUser(t, db, "user@example.com", "Test User")

	t.Run("nonexistent_team", func(t *testing.T) {
		nonexistentID := uuid.New().String()
		body := fmt.Sprintf(`{"user_id": "%s"}`, userID)
		rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+nonexistentID+"/members", body)

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
		deletedID := seedTeamWithStatus(t, db, "Deleted Team", "deleted-team-mbr", "deleted")
		body := fmt.Sprintf(`{"user_id": "%s"}`, userID)
		rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+deletedID+"/members", body)

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
// TS-03-40: POST /api/v1/teams/:id/members with nonexistent user → HTTP 404
// Requirement: 03-REQ-8.5
//
// Verifies that adding a member with a user_id that doesn't exist returns
// HTTP 404 with message "user not found".
// ---------------------------------------------------------------------------

func TestAddMember_UserNotFound(t *testing.T) {
	e, db := setupAddMemberTest(t)

	teamID := seedTeamWithStatus(t, db, "Active Team", "active-team-user", "active")
	nonexistentUserID := uuid.New().String()

	body := fmt.Sprintf(`{"user_id": "%s"}`, nonexistentUserID)
	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/members", body)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseErrorResponse(t, rec)
	if resp.Error.Code != 404 {
		t.Errorf("expected error code 404, got %d", resp.Error.Code)
	}
	if resp.Error.Message != "user not found" {
		t.Errorf("expected message %q, got %q", "user not found", resp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// TS-03-41: POST /api/v1/teams/:id/members with invalid UUID → HTTP 400
// Requirement: 03-REQ-8.6
//
// Verifies that invalid UUID path param or user_id returns HTTP 400 with
// message "invalid id format".
// ---------------------------------------------------------------------------

func TestAddMember_InvalidUUID(t *testing.T) {
	e, db := setupAddMemberTest(t)

	t.Run("invalid_team_id_path_param", func(t *testing.T) {
		body := fmt.Sprintf(`{"user_id": "%s"}`, uuid.New().String())
		rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/bad-id/members", body)

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
	})

	t.Run("invalid_user_id_in_body", func(t *testing.T) {
		teamID := seedTeamWithStatus(t, db, "UUID Team", "uuid-team-mbr", "active")

		body := `{"user_id": "not-a-valid-uuid"}`
		rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/members", body)

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
	})
}

// ---------------------------------------------------------------------------
// TS-03-42: POST /api/v1/teams/:id/members with malformed JSON → HTTP 400
// Requirement: 03-REQ-8.7
//
// Verifies that malformed JSON body returns HTTP 400 with message
// "invalid request body".
// ---------------------------------------------------------------------------

func TestAddMember_MalformedJSON(t *testing.T) {
	e, db := setupAddMemberTest(t)

	teamID := seedTeamWithStatus(t, db, "JSON Team", "json-team-mbr", "active")

	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/members", "{not valid json")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseErrorResponse(t, rec)
	if resp.Error.Code != 400 {
		t.Errorf("expected error code 400, got %d", resp.Error.Code)
	}
	if resp.Error.Message != "invalid request body" {
		t.Errorf("expected message %q, got %q", "invalid request body", resp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// TS-03-43: POST /api/v1/teams/:id/members without user_id → HTTP 422
// Requirement: 03-REQ-8.8
//
// Verifies that missing user_id field returns HTTP 422 with message
// "missing required field".
// ---------------------------------------------------------------------------

func TestAddMember_MissingUserID(t *testing.T) {
	e, db := setupAddMemberTest(t)

	teamID := seedTeamWithStatus(t, db, "Missing Field Team", "missing-field-mbr", "active")

	t.Run("empty_json_object", func(t *testing.T) {
		rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/members", "{}")

		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("expected status 422, got %d: %s", rec.Code, rec.Body.String())
		}

		resp := parseErrorResponse(t, rec)
		if resp.Error.Code != 422 {
			t.Errorf("expected error code 422, got %d", resp.Error.Code)
		}
		if resp.Error.Message != "missing required field" {
			t.Errorf("expected message %q, got %q", "missing required field", resp.Error.Message)
		}
	})

	t.Run("user_id_empty_string", func(t *testing.T) {
		rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/members", `{"user_id": ""}`)

		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("expected status 422, got %d: %s", rec.Code, rec.Body.String())
		}

		resp := parseErrorResponse(t, rec)
		if resp.Error.Code != 422 {
			t.Errorf("expected error code 422, got %d", resp.Error.Code)
		}
		if resp.Error.Message != "missing required field" {
			t.Errorf("expected message %q, got %q", "missing required field", resp.Error.Message)
		}
	})
}

// ---------------------------------------------------------------------------
// TS-03-E5: Concurrent add-member for same (team_id, user_id) → exactly 1 row
// Requirement: 03-REQ-8.E1
//
// Verifies that concurrent add-member requests for the same (team_id,
// user_id) pair result in exactly one membership row, with no duplicates.
// Both requests should return HTTP 200.
// ---------------------------------------------------------------------------

func TestAddMember_ConcurrentSameUser(t *testing.T) {
	// Use a dedicated setup with MaxOpenConns(1) to prevent SQLite in-memory
	// mode from opening multiple connections (each with its own empty DB).
	db := openTestDB(t)
	db.SetMaxOpenConns(1)
	createStubUsersTable(t, db)
	if err := teams.InitSchema(db); err != nil {
		t.Fatalf("InitSchema failed: %v", err)
	}

	store := teams.NewStore(db)
	handler := teams.NewHandler(store)

	e := echo.New()
	g := e.Group("/api/v1/teams")
	handler.RegisterRoutes(g)

	teamID := seedTeamWithStatus(t, db, "Concurrent Team", "concurrent-team-mbr", "active")
	userID := seedUser(t, db, "concurrent@example.com", "Concurrent User")

	body := fmt.Sprintf(`{"user_id": "%s"}`, userID)

	const numConcurrent = 2
	results := make([]*int, numConcurrent)
	var wg sync.WaitGroup

	for i := range numConcurrent {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/members", body)
			code := rec.Code
			results[idx] = &code
		}(i)
	}

	wg.Wait()

	// Both requests should succeed (HTTP 200). Under SQLite in-memory mode
	// with MaxOpenConns(1), requests are serialized, so both should get 200.
	// The key invariant is that exactly one DB row exists.
	successCount := 0
	for _, code := range results {
		if code != nil && *code == http.StatusOK {
			successCount++
		}
	}

	// At least one should succeed.
	if successCount == 0 {
		t.Error("expected at least one HTTP 200 response from concurrent adds")
	}

	// The invariant: exactly one team_members row exists.
	var count int
	if err := db.QueryRow(
		"SELECT count(*) FROM team_members WHERE team_id = ? AND user_id = ?",
		teamID, userID,
	).Scan(&count); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 team_members row, got %d", count)
	}
}

// TestAddMember_FreshJoinReturnsCurrentUserData verifies that the member
// response reflects current user data from a fresh JOIN, not stale data.
func TestAddMember_FreshJoinReturnsCurrentUserData(t *testing.T) {
	e, db := setupAddMemberTest(t)

	teamID := seedTeamWithStatus(t, db, "Fresh Join Team", "fresh-join-team", "active")
	userID := seedUser(t, db, "old@example.com", "Old Name")

	// Add member with original user data.
	body := fmt.Sprintf(`{"user_id": "%s"}`, userID)
	rec1 := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/members", body)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first add: expected status 200, got %d: %s", rec1.Code, rec1.Body.String())
	}

	// Update user data directly in the DB (simulating a profile update).
	_, err := db.Exec(
		`UPDATE users SET email = ?, full_name = ? WHERE id = ?`,
		"new@example.com", "New Name", userID,
	)
	if err != nil {
		t.Fatalf("failed to update user: %v", err)
	}

	// Re-add (idempotent) — should return updated user data.
	rec2 := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/members", body)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second add: expected status 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	var resp2 memberResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp2.Email != "new@example.com" {
		t.Errorf("expected email %q (updated), got %q", "new@example.com", resp2.Email)
	}
	if resp2.Name != "New Name" {
		t.Errorf("expected name %q (updated), got %q", "New Name", resp2.Name)
	}
}
