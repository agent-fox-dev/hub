package teams_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/teams"
)

// ---------------------------------------------------------------------------
// Test Helpers (list teams and edge case tests)
// ---------------------------------------------------------------------------

// setupListTeamTest initializes a test database, Echo instance, and registers
// team routes. Returns the Echo instance and database for assertions.
func setupListTeamTest(t *testing.T) (*echo.Echo, *sql.DB) {
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

// seedTeamWithTime inserts a team with a specific created_at timestamp.
func seedTeamWithTime(t *testing.T, db *sql.DB, name, slug, status string, createdAt time.Time) string {
	t.Helper()
	id := uuid.New().String()
	created := teams.FormatTime(createdAt)
	updated := teams.FormatTime(createdAt)
	_, err := db.Exec(
		`INSERT INTO teams (id, name, slug, url, status, created_at, updated_at) VALUES (?, ?, ?, NULL, ?, ?, ?)`,
		id, name, slug, status, created, updated,
	)
	if err != nil {
		t.Fatalf("failed to seed team %q: %v", name, err)
	}
	return id
}

// parseTeamListResponse unmarshals a JSON array of team objects.
func parseTeamListResponse(t *testing.T, body []byte) []teamResponse {
	t.Helper()
	var list []teamResponse
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("failed to parse team list response: %v\nbody: %s", err, string(body))
	}
	return list
}

// ---------------------------------------------------------------------------
// TS-03-E2: Concurrent create requests with same slug
// Requirement: 03-REQ-2.E1
//
// Verifies that concurrent POST /api/v1/teams requests with the same slug
// result in at most one successfully created team. SQLite serialises
// concurrent writes, so one request always succeeds (HTTP 201) and the
// other fails — typically HTTP 409 (uniqueness conflict caught by the
// app-layer check or partial UNIQUE index) or, under heavy lock
// contention, possibly HTTP 500 (lock timeout). The key invariant is that
// exactly one non-deleted team row exists with the slug in the database.
// ---------------------------------------------------------------------------

func TestCreateTeamEdge_ConcurrentSameSlug(t *testing.T) {
	e, db := setupListTeamTest(t)
	// SQLite :memory: databases are per-connection. Without this limit,
	// goroutines may open separate connections to different in-memory DBs,
	// causing "no such table" errors. Constraining to 1 connection ensures
	// all goroutines share the same in-memory database.
	db.SetMaxOpenConns(1)

	var wg sync.WaitGroup
	results := make([]int, 2)

	// Use a start channel so both goroutines fire at roughly the same time.
	start := make(chan struct{})

	wg.Add(2)
	for i := range 2 {
		go func(idx int) {
			defer wg.Done()
			<-start
			name := fmt.Sprintf("Team %c", 'A'+idx)
			body := fmt.Sprintf(`{"name": "%s", "slug": "concurrent-slug"}`, name)
			rec := doRequest(t, e, http.MethodPost, "/api/v1/teams", body)
			results[idx] = rec.Code
		}(i)
	}
	close(start)
	wg.Wait()

	got201 := 0
	gotNon201 := 0
	for _, code := range results {
		if code == 201 {
			got201++
		} else {
			gotNon201++
		}
	}

	if got201 != 1 {
		t.Errorf("expected exactly one HTTP 201, got %d (statuses: %v)", got201, results)
	}
	if gotNon201 != 1 {
		t.Errorf("expected exactly one non-201, got %d (statuses: %v)", gotNon201, results)
	}

	// Key invariant: exactly one team with this slug exists in the DB.
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM teams WHERE slug = ? AND status != 'deleted'`, "concurrent-slug").Scan(&count); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 team with slug 'concurrent-slug', got %d", count)
	}
}

// ---------------------------------------------------------------------------
// TS-03-E3: Name/slug reuse after deletion
// Requirement: 03-REQ-2.E2
//
// Verifies that a name or slug previously belonging to a deleted team can
// be reused by a new team.
// ---------------------------------------------------------------------------

func TestCreateTeamEdge_ReuseDeletedNameSlug(t *testing.T) {
	e, db := setupListTeamTest(t)

	// Step 1: Create a team via the API.
	body := `{"name": "Reusable", "slug": "reusable-slug"}`
	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	origResp := parseTeamResponse(t, rec)
	origID := origResp.ID

	// Step 2: Archive the team (directly in DB since archive handler is a stub).
	_, err := db.Exec(`UPDATE teams SET status = 'archived', updated_at = ? WHERE id = ?`,
		teams.FormatTime(time.Now()), origID)
	if err != nil {
		t.Fatalf("failed to archive team: %v", err)
	}

	// Step 3: Delete the team (directly in DB since delete handler is a stub).
	_, err = db.Exec(`DELETE FROM team_members WHERE team_id = ?`, origID)
	if err != nil {
		t.Fatalf("failed to delete team members: %v", err)
	}
	_, err = db.Exec(`DELETE FROM teams WHERE id = ?`, origID)
	if err != nil {
		t.Fatalf("failed to delete team: %v", err)
	}

	// Verify the team is gone.
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM teams WHERE id = ?`, origID).Scan(&count); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if count != 0 {
		t.Fatal("original team should be deleted from DB")
	}

	// Step 4: Reuse the same name and slug — should succeed.
	rec = doRequest(t, e, http.MethodPost, "/api/v1/teams", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 on reuse of deleted name/slug, got %d: %s", rec.Code, rec.Body.String())
	}

	newResp := parseTeamResponse(t, rec)
	if newResp.Name != "Reusable" {
		t.Errorf("expected name %q, got %q", "Reusable", newResp.Name)
	}
	if newResp.Slug != "reusable-slug" {
		t.Errorf("expected slug %q, got %q", "reusable-slug", newResp.Slug)
	}
	if newResp.ID == origID {
		t.Error("new team should have a different ID from the deleted team")
	}
}

// ---------------------------------------------------------------------------
// TS-03-15: GET /api/v1/teams without query params returns only active teams
// Requirement: 03-REQ-3.1
//
// Verifies that the list teams endpoint returns only active teams when no
// query parameters are provided, ordered by created_at ascending.
// ---------------------------------------------------------------------------

func TestListTeams_ActiveOnly(t *testing.T) {
	e, db := setupListTeamTest(t)

	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t1ID := seedTeamWithTime(t, db, "Alpha", "alpha", "active", baseTime)
	_ = seedTeamWithTime(t, db, "Beta", "beta", "archived", baseTime.Add(1*time.Hour))
	t3ID := seedTeamWithTime(t, db, "Gamma", "gamma", "active", baseTime.Add(2*time.Hour))

	rec := doRequest(t, e, http.MethodGet, "/api/v1/teams", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	list := parseTeamListResponse(t, rec.Body.Bytes())

	if len(list) != 2 {
		t.Fatalf("expected 2 teams, got %d", len(list))
	}

	// First should be Alpha (oldest active).
	if list[0].ID != t1ID {
		t.Errorf("expected first team ID %s, got %s", t1ID, list[0].ID)
	}
	// Second should be Gamma.
	if list[1].ID != t3ID {
		t.Errorf("expected second team ID %s, got %s", t3ID, list[1].ID)
	}

	// All returned teams should be active.
	for _, team := range list {
		if team.Status != "active" {
			t.Errorf("expected status 'active', got %q for team %s", team.Status, team.ID)
		}
	}
}

// ---------------------------------------------------------------------------
// TS-03-16: GET /api/v1/teams?include_archived=true returns active + archived
// Requirement: 03-REQ-3.2
//
// Verifies that the list teams endpoint returns both active and archived
// teams when include_archived=true is provided, ordered by created_at.
// ---------------------------------------------------------------------------

func TestListTeams_IncludeArchived(t *testing.T) {
	e, db := setupListTeamTest(t)

	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t1ID := seedTeamWithTime(t, db, "Alpha", "alpha", "active", baseTime)
	t2ID := seedTeamWithTime(t, db, "Beta", "beta", "archived", baseTime.Add(1*time.Hour))
	t3ID := seedTeamWithTime(t, db, "Gamma", "gamma", "active", baseTime.Add(2*time.Hour))

	rec := doRequest(t, e, http.MethodGet, "/api/v1/teams?include_archived=true", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	list := parseTeamListResponse(t, rec.Body.Bytes())

	if len(list) != 3 {
		t.Fatalf("expected 3 teams, got %d", len(list))
	}

	// Ordered by created_at ascending.
	if list[0].ID != t1ID {
		t.Errorf("expected first team ID %s, got %s", t1ID, list[0].ID)
	}
	if list[1].ID != t2ID {
		t.Errorf("expected second team ID %s, got %s", t2ID, list[1].ID)
	}
	if list[2].ID != t3ID {
		t.Errorf("expected third team ID %s, got %s", t3ID, list[2].ID)
	}

	// Verify at least one archived team is present.
	hasArchived := false
	for _, team := range list {
		if team.Status == "archived" {
			hasArchived = true
			break
		}
	}
	if !hasArchived {
		t.Error("expected at least one archived team in the response")
	}
}

// ---------------------------------------------------------------------------
// TS-03-17: GET /api/v1/teams never returns deleted teams
// Requirement: 03-REQ-3.3
//
// Verifies that deleted teams are never returned regardless of query params.
// ---------------------------------------------------------------------------

func TestListTeams_ExcludesDeleted(t *testing.T) {
	e, db := setupListTeamTest(t)

	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_ = seedTeamWithTime(t, db, "Active Team", "active-team", "active", baseTime)
	_ = seedTeamWithTime(t, db, "Deleted Team", "deleted-team", "deleted", baseTime.Add(1*time.Hour))

	t.Run("without_include_archived", func(t *testing.T) {
		rec := doRequest(t, e, http.MethodGet, "/api/v1/teams", "")

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
		}

		list := parseTeamListResponse(t, rec.Body.Bytes())

		for _, team := range list {
			if team.Status == "deleted" {
				t.Error("deleted team should not appear in response")
			}
			if team.Slug == "deleted-team" {
				t.Error("team with slug 'deleted-team' should not appear")
			}
		}
	})

	t.Run("with_include_archived", func(t *testing.T) {
		rec := doRequest(t, e, http.MethodGet, "/api/v1/teams?include_archived=true", "")

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
		}

		list := parseTeamListResponse(t, rec.Body.Bytes())

		for _, team := range list {
			if team.Status == "deleted" {
				t.Error("deleted team should not appear in response")
			}
			if team.Slug == "deleted-team" {
				t.Error("team with slug 'deleted-team' should not appear")
			}
		}
	})
}

// ---------------------------------------------------------------------------
// TS-03-18: GET /api/v1/teams returns all records with no pagination
// Requirement: 03-REQ-3.4
//
// Verifies that all matching records are returned without pagination,
// ordered by created_at ascending.
// ---------------------------------------------------------------------------

func TestListTeams_NoPagination(t *testing.T) {
	e, db := setupListTeamTest(t)

	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Seed 50 active teams with distinct created_at timestamps.
	expectedIDs := make([]string, 50)
	for i := 0; i < 50; i++ {
		name := fmt.Sprintf("Team %03d", i)
		slug := fmt.Sprintf("team-%03d", i)
		createdAt := baseTime.Add(time.Duration(i) * time.Second)
		expectedIDs[i] = seedTeamWithTime(t, db, name, slug, "active", createdAt)
	}

	rec := doRequest(t, e, http.MethodGet, "/api/v1/teams", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	list := parseTeamListResponse(t, rec.Body.Bytes())

	if len(list) != 50 {
		t.Fatalf("expected 50 teams, got %d", len(list))
	}

	// Verify ordering by created_at ascending.
	for i := 0; i < len(list)-1; i++ {
		if list[i].CreatedAt > list[i+1].CreatedAt {
			t.Errorf("teams not ordered by created_at ascending at index %d: %s > %s",
				i, list[i].CreatedAt, list[i+1].CreatedAt)
			break
		}
	}

	// Verify all expected IDs are present (in order).
	for i, id := range expectedIDs {
		if list[i].ID != id {
			t.Errorf("team at index %d: expected ID %s, got %s", i, id, list[i].ID)
		}
	}
}

// ---------------------------------------------------------------------------
// Additional edge case: empty list returns empty JSON array
// ---------------------------------------------------------------------------

func TestListTeams_EmptyListReturnsEmptyArray(t *testing.T) {
	e, _ := setupListTeamTest(t)

	rec := doRequest(t, e, http.MethodGet, "/api/v1/teams", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Should return an empty JSON array, not null.
	bodyStr := rec.Body.String()
	if bodyStr != "[]\n" {
		t.Errorf("expected empty JSON array '[]', got %q", bodyStr)
	}

	// Also verify Content-Type.
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("expected Content-Type containing 'application/json', got %q", ct)
	}
}
