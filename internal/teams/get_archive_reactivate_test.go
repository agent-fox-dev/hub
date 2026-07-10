package teams_test

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/teams"
)

// ---------------------------------------------------------------------------
// Test Helpers (get, archive, reactivate tests)
// ---------------------------------------------------------------------------

// setupTeamHandlerTest initializes a test database, Echo instance, and
// registers team routes. Returns the Echo instance and database.
func setupTeamHandlerTest(t *testing.T) (*echo.Echo, *sql.DB) {
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

// seedTeamWithStatus inserts a team with a given status. Returns the UUID.
func seedTeamWithStatus(t *testing.T, db *sql.DB, name, slug, status string) string {
	t.Helper()
	id := uuid.New().String()
	now := teams.FormatTime(time.Now().UTC())
	_, err := db.Exec(
		`INSERT INTO teams (id, name, slug, url, status, created_at, updated_at) VALUES (?, ?, ?, NULL, ?, ?, ?)`,
		id, name, slug, status, now, now,
	)
	if err != nil {
		t.Fatalf("failed to seed team %q: %v", name, err)
	}
	return id
}

// seedTeamWithStatusAndTimes inserts a team with specific created_at and updated_at.
func seedTeamWithStatusAndTimes(t *testing.T, db *sql.DB, name, slug, status string, createdAt, updatedAt time.Time) string {
	t.Helper()
	id := uuid.New().String()
	_, err := db.Exec(
		`INSERT INTO teams (id, name, slug, url, status, created_at, updated_at) VALUES (?, ?, ?, NULL, ?, ?, ?)`,
		id, name, slug, status, teams.FormatTime(createdAt), teams.FormatTime(updatedAt),
	)
	if err != nil {
		t.Fatalf("failed to seed team %q: %v", name, err)
	}
	return id
}

// ---------------------------------------------------------------------------
// TS-03-19: GET /api/v1/teams/:id returns HTTP 200 for active/archived teams
// Requirement: 03-REQ-4.1
// ---------------------------------------------------------------------------

func TestGetTeam_ActiveTeam(t *testing.T) {
	e, db := setupTeamHandlerTest(t)

	teamID := seedTeamWithStatus(t, db, "My Team", "my-team", "active")

	rec := doRequest(t, e, http.MethodGet, "/api/v1/teams/"+teamID, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseTeamResponse(t, rec)

	if resp.ID != teamID {
		t.Errorf("expected id %q, got %q", teamID, resp.ID)
	}
	if resp.Name != "My Team" {
		t.Errorf("expected name %q, got %q", "My Team", resp.Name)
	}
	if resp.Slug != "my-team" {
		t.Errorf("expected slug %q, got %q", "my-team", resp.Slug)
	}
	if resp.Status != "active" {
		t.Errorf("expected status %q, got %q", "active", resp.Status)
	}

	// Content-Type check.
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}
}

func TestGetTeam_ArchivedTeam(t *testing.T) {
	e, db := setupTeamHandlerTest(t)

	teamID := seedTeamWithStatus(t, db, "Archived Team", "archived-team", "archived")

	rec := doRequest(t, e, http.MethodGet, "/api/v1/teams/"+teamID, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseTeamResponse(t, rec)

	if resp.ID != teamID {
		t.Errorf("expected id %q, got %q", teamID, resp.ID)
	}
	if resp.Status != "archived" {
		t.Errorf("expected status %q, got %q", "archived", resp.Status)
	}
}

// ---------------------------------------------------------------------------
// TS-03-20: GET /api/v1/teams/:id returns HTTP 404 for deleted/nonexistent
// Requirement: 03-REQ-4.2
// ---------------------------------------------------------------------------

func TestGetTeam_NotFound(t *testing.T) {
	e, db := setupTeamHandlerTest(t)

	t.Run("nonexistent_uuid", func(t *testing.T) {
		nonexistentID := uuid.New().String()
		rec := doRequest(t, e, http.MethodGet, "/api/v1/teams/"+nonexistentID, "")

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
		// Insert a deleted team directly into DB.
		deletedID := seedTeamWithStatus(t, db, "Deleted Team", "deleted-team", "deleted")

		rec := doRequest(t, e, http.MethodGet, "/api/v1/teams/"+deletedID, "")

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
// TS-03-21: GET /api/v1/teams/:id returns HTTP 400 for invalid UUID
// Requirement: 03-REQ-4.3
// ---------------------------------------------------------------------------

func TestGetTeam_InvalidUUID(t *testing.T) {
	e, _ := setupTeamHandlerTest(t)

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
}

// ---------------------------------------------------------------------------
// TS-03-22: POST /api/v1/teams/:id/archive archives active team → HTTP 200
// Requirement: 03-REQ-5.1
// ---------------------------------------------------------------------------

func TestArchiveTeam_Success(t *testing.T) {
	e, db := setupTeamHandlerTest(t)

	oldTime := time.Now().UTC().Add(-1 * time.Hour)
	teamID := seedTeamWithStatusAndTimes(t, db, "Active Team", "active-team", "active", oldTime, oldTime)

	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/archive", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseTeamResponse(t, rec)

	if resp.ID != teamID {
		t.Errorf("expected id %q, got %q", teamID, resp.ID)
	}
	if resp.Status != "archived" {
		t.Errorf("expected status %q, got %q", "archived", resp.Status)
	}

	// Verify updated_at was refreshed.
	if !rfc3339MicroRegex.MatchString(resp.UpdatedAt) {
		t.Errorf("updated_at does not match RFC3339 microsecond format: %q", resp.UpdatedAt)
	}

	// Verify the DB row has been updated.
	var dbStatus string
	err := db.QueryRow("SELECT status FROM teams WHERE id = ?", teamID).Scan(&dbStatus)
	if err != nil {
		t.Fatalf("failed to query team status: %v", err)
	}
	if dbStatus != "archived" {
		t.Errorf("expected DB status %q, got %q", "archived", dbStatus)
	}
}

// ---------------------------------------------------------------------------
// TS-03-23: POST /api/v1/teams/:id/archive on already archived → HTTP 409
// Requirement: 03-REQ-5.2
// ---------------------------------------------------------------------------

func TestArchiveTeam_AlreadyArchived(t *testing.T) {
	e, db := setupTeamHandlerTest(t)

	teamID := seedTeamWithStatus(t, db, "Archived Team", "arch-team", "archived")

	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/archive", "")

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseErrorResponse(t, rec)
	if resp.Error.Code != 409 {
		t.Errorf("expected error code 409, got %d", resp.Error.Code)
	}
	if resp.Error.Message != "team is already archived" {
		t.Errorf("expected message %q, got %q", "team is already archived", resp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// TS-03-24: POST /api/v1/teams/:id/archive on nonexistent/deleted → HTTP 404
// Requirement: 03-REQ-5.3
// ---------------------------------------------------------------------------

func TestArchiveTeam_NotFound(t *testing.T) {
	e, _ := setupTeamHandlerTest(t)

	nonexistentID := uuid.New().String()
	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+nonexistentID+"/archive", "")

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
}

// ---------------------------------------------------------------------------
// TS-03-25: POST /api/v1/teams/:id/archive with invalid UUID → HTTP 400
// Requirement: 03-REQ-5.4
// ---------------------------------------------------------------------------

func TestArchiveTeam_InvalidUUID(t *testing.T) {
	e, _ := setupTeamHandlerTest(t)

	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/bad-id/archive", "")

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
// TS-03-26: POST /api/v1/teams/:id/archive ignores body and Content-Type
// Requirement: 03-REQ-5.5
// ---------------------------------------------------------------------------

func TestArchiveTeam_IgnoresBodyAndContentType(t *testing.T) {
	e, db := setupTeamHandlerTest(t)

	teamID := seedTeamWithStatus(t, db, "Ignorable Body Team", "ig-body-team", "active")

	// Send with a JSON body and non-JSON Content-Type.
	rec := doRequestRaw(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/archive",
		`{"unexpected":"data"}`, "text/plain")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseTeamResponse(t, rec)
	if resp.Status != "archived" {
		t.Errorf("expected status %q, got %q", "archived", resp.Status)
	}
}

// ---------------------------------------------------------------------------
// TS-03-27: POST /api/v1/teams/:id/reactivate on archived → HTTP 200
// Requirement: 03-REQ-6.1
// ---------------------------------------------------------------------------

func TestReactivateTeam_Success(t *testing.T) {
	e, db := setupTeamHandlerTest(t)

	teamID := seedTeamWithStatus(t, db, "Archived For React", "react-team", "archived")

	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/reactivate", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseTeamResponse(t, rec)

	if resp.ID != teamID {
		t.Errorf("expected id %q, got %q", teamID, resp.ID)
	}
	if resp.Status != "active" {
		t.Errorf("expected status %q, got %q", "active", resp.Status)
	}

	// Verify updated_at in RFC3339 UTC microsecond format.
	if !rfc3339MicroRegex.MatchString(resp.UpdatedAt) {
		t.Errorf("updated_at does not match RFC3339 microsecond format: %q", resp.UpdatedAt)
	}

	// Verify the DB row has been updated.
	var dbStatus string
	err := db.QueryRow("SELECT status FROM teams WHERE id = ?", teamID).Scan(&dbStatus)
	if err != nil {
		t.Fatalf("failed to query team status: %v", err)
	}
	if dbStatus != "active" {
		t.Errorf("expected DB status %q, got %q", "active", dbStatus)
	}
}

// ---------------------------------------------------------------------------
// TS-03-28: POST /api/v1/teams/:id/reactivate on active → HTTP 409
// Requirement: 03-REQ-6.2
// ---------------------------------------------------------------------------

func TestReactivateTeam_AlreadyActive(t *testing.T) {
	e, db := setupTeamHandlerTest(t)

	teamID := seedTeamWithStatus(t, db, "Active Team", "already-active", "active")

	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/reactivate", "")

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseErrorResponse(t, rec)
	if resp.Error.Code != 409 {
		t.Errorf("expected error code 409, got %d", resp.Error.Code)
	}
	if resp.Error.Message != "team is already active" {
		t.Errorf("expected message %q, got %q", "team is already active", resp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// TS-03-29: POST /api/v1/teams/:id/reactivate on nonexistent/deleted → 404
// Requirement: 03-REQ-6.3
// ---------------------------------------------------------------------------

func TestReactivateTeam_NotFound(t *testing.T) {
	e, _ := setupTeamHandlerTest(t)

	nonexistentID := uuid.New().String()
	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+nonexistentID+"/reactivate", "")

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
}

// ---------------------------------------------------------------------------
// TS-03-30: POST /api/v1/teams/:id/reactivate with invalid UUID → HTTP 400
// Requirement: 03-REQ-6.4
// ---------------------------------------------------------------------------

func TestReactivateTeam_InvalidUUID(t *testing.T) {
	e, _ := setupTeamHandlerTest(t)

	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/not-a-uuid/reactivate", "")

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
// TS-03-31: POST /api/v1/teams/:id/reactivate ignores body and Content-Type
// Requirement: 03-REQ-6.5
// ---------------------------------------------------------------------------

func TestReactivateTeam_IgnoresBodyAndContentType(t *testing.T) {
	e, db := setupTeamHandlerTest(t)

	teamID := seedTeamWithStatus(t, db, "React Body Team", "react-body-team", "archived")

	// Send with extra body fields and no Content-Type.
	rec := doRequestRaw(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/reactivate",
		`{"extra":"field"}`, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseTeamResponse(t, rec)
	if resp.Status != "active" {
		t.Errorf("expected status %q, got %q", "active", resp.Status)
	}
}

// ---------------------------------------------------------------------------
// TS-03-53: updated_at is refreshed on archive and reactivate
// Requirement: 03-REQ-11.4
//
// Additional test: verifies that updated_at is set to the current UTC
// timestamp on every successful lifecycle transition.
// ---------------------------------------------------------------------------

func TestArchiveReactivate_UpdatedAtRefreshed(t *testing.T) {
	e, db := setupTeamHandlerTest(t)

	// Seed a team with old timestamps.
	oldTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	teamID := seedTeamWithStatusAndTimes(t, db, "Lifecycle Team", "lifecycle-team", "active", oldTime, oldTime)

	// Archive the team.
	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/archive", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("archive: expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	archiveResp := parseTeamResponse(t, rec)

	// updated_at after archive should be newer than the original.
	if archiveResp.UpdatedAt <= teams.FormatTime(oldTime) {
		t.Errorf("expected updated_at after archive to be newer than %s, got %s",
			teams.FormatTime(oldTime), archiveResp.UpdatedAt)
	}

	// Reactivate the team.
	rec = doRequest(t, e, http.MethodPost, "/api/v1/teams/"+teamID+"/reactivate", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("reactivate: expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	reactResp := parseTeamResponse(t, rec)

	// updated_at after reactivate should be >= updated_at after archive.
	if reactResp.UpdatedAt < archiveResp.UpdatedAt {
		t.Errorf("expected updated_at after reactivate (%s) >= after archive (%s)",
			reactResp.UpdatedAt, archiveResp.UpdatedAt)
	}
}

// ---------------------------------------------------------------------------
// Additional: archive on deleted team returns 404
// Requirement: 03-REQ-5.3
// ---------------------------------------------------------------------------

func TestArchiveTeam_DeletedTeam(t *testing.T) {
	e, db := setupTeamHandlerTest(t)

	deletedID := seedTeamWithStatus(t, db, "Deleted For Arch", "deleted-arch", "deleted")

	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+deletedID+"/archive", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseErrorResponse(t, rec)
	if resp.Error.Message != "team not found" {
		t.Errorf("expected message %q, got %q", "team not found", resp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// Additional: reactivate on deleted team returns 404
// Requirement: 03-REQ-6.3
// ---------------------------------------------------------------------------

func TestReactivateTeam_DeletedTeam(t *testing.T) {
	e, db := setupTeamHandlerTest(t)

	deletedID := seedTeamWithStatus(t, db, "Deleted For React", "deleted-react", "deleted")

	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams/"+deletedID+"/reactivate", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseErrorResponse(t, rec)
	if resp.Error.Message != "team not found" {
		t.Errorf("expected message %q, got %q", "team not found", resp.Error.Message)
	}
}
