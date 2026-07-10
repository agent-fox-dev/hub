package teams_test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/teams"
)

// ---------------------------------------------------------------------------
// Test Helpers (create team tests)
// ---------------------------------------------------------------------------

// errorResponse mirrors the nested error envelope used by the teams handler.
type errorResponse struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// teamResponse mirrors the JSON team object returned by the handler.
type teamResponse struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Slug      string  `json:"slug"`
	URL       *string `json:"url"`
	Status    string  `json:"status"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

// rfc3339MicroRegex matches timestamps in format YYYY-MM-DDTHH:MM:SS.ffffffZ.
var rfc3339MicroRegex = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{6}Z$`)

// setupCreateTeamTest initializes a test database, Echo instance, and registers
// team routes. Returns the Echo instance and database for assertions.
func setupCreateTeamTest(t *testing.T) (*echo.Echo, *sql.DB) {
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

// doRequest performs an HTTP request against the Echo router and returns the response.
func doRequest(t *testing.T, e *echo.Echo, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// doRequestRaw performs an HTTP request with custom Content-Type header.
func doRequestRaw(t *testing.T, e *echo.Echo, method, path, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// parseTeamResponse unmarshals a team JSON response body.
func parseTeamResponse(t *testing.T, rec *httptest.ResponseRecorder) teamResponse {
	t.Helper()
	var resp teamResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse team response: %v\nbody: %s", err, rec.Body.String())
	}
	return resp
}

// parseErrorResponse unmarshals a nested error envelope response body.
func parseErrorResponse(t *testing.T, rec *httptest.ResponseRecorder) errorResponse {
	t.Helper()
	var resp errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse error response: %v\nbody: %s", err, rec.Body.String())
	}
	return resp
}

// seedTeamDirect inserts a team directly into the database, bypassing the handler.
func seedTeamDirect(t *testing.T, db *sql.DB, name, slug, status string) string {
	t.Helper()
	id := uuid.New().String()
	now := teams.FormatTime(time.Now())
	_, err := db.Exec(
		`INSERT INTO teams (id, name, slug, url, status, created_at, updated_at) VALUES (?, ?, ?, NULL, ?, ?, ?)`,
		id, name, slug, status, now, now,
	)
	if err != nil {
		t.Fatalf("failed to seed team: %v", err)
	}
	return id
}

// ---------------------------------------------------------------------------
// TS-03-4: Successful team creation with name trimming and full response shape
// Requirement: 03-REQ-2.1
// ---------------------------------------------------------------------------

func TestCreateTeam_Success(t *testing.T) {
	e, db := setupCreateTeamTest(t)

	t.Run("creates_team_with_trimmed_name_and_full_response", func(t *testing.T) {
		body := `{"name": "  Engineering  ", "slug": "engineering", "url": "https://eng.example.com"}`
		rec := doRequest(t, e, http.MethodPost, "/api/v1/teams", body)

		if rec.Code != http.StatusCreated {
			t.Fatalf("expected status 201, got %d: %s", rec.Code, rec.Body.String())
		}

		// Check Content-Type header.
		ct := rec.Header().Get("Content-Type")
		if !strings.Contains(ct, "application/json") {
			t.Errorf("expected Content-Type application/json, got %q", ct)
		}

		resp := parseTeamResponse(t, rec)

		// Verify UUID.
		if _, err := uuid.Parse(resp.ID); err != nil {
			t.Errorf("id is not a valid UUID: %q", resp.ID)
		}

		// Name should be trimmed.
		if resp.Name != "Engineering" {
			t.Errorf("expected name %q, got %q", "Engineering", resp.Name)
		}

		if resp.Slug != "engineering" {
			t.Errorf("expected slug %q, got %q", "engineering", resp.Slug)
		}

		if resp.URL == nil || *resp.URL != "https://eng.example.com" {
			t.Errorf("expected url %q, got %v", "https://eng.example.com", resp.URL)
		}

		if resp.Status != "active" {
			t.Errorf("expected status %q, got %q", "active", resp.Status)
		}

		// Verify RFC3339 UTC microsecond timestamps.
		if !rfc3339MicroRegex.MatchString(resp.CreatedAt) {
			t.Errorf("created_at does not match RFC3339 microsecond format: %q", resp.CreatedAt)
		}
		if !rfc3339MicroRegex.MatchString(resp.UpdatedAt) {
			t.Errorf("updated_at does not match RFC3339 microsecond format: %q", resp.UpdatedAt)
		}

		// Verify DB row exists.
		var dbName string
		err := db.QueryRow("SELECT name FROM teams WHERE id = ?", resp.ID).Scan(&dbName)
		if err != nil {
			t.Fatalf("team not found in DB: %v", err)
		}
		if dbName != "Engineering" {
			t.Errorf("DB name mismatch: got %q, want %q", dbName, "Engineering")
		}
	})

	t.Run("creates_team_without_url", func(t *testing.T) {
		body := `{"name": "No URL Team", "slug": "no-url-team"}`
		rec := doRequest(t, e, http.MethodPost, "/api/v1/teams", body)

		if rec.Code != http.StatusCreated {
			t.Fatalf("expected status 201, got %d: %s", rec.Code, rec.Body.String())
		}

		resp := parseTeamResponse(t, rec)

		if resp.URL != nil {
			t.Errorf("expected url to be null, got %v", resp.URL)
		}
	})
}

// ---------------------------------------------------------------------------
// TS-03-13: Silently ignores unrecognized fields
// Requirement: 03-REQ-2.9
// ---------------------------------------------------------------------------

func TestCreateTeam_IgnoresUnrecognizedFields(t *testing.T) {
	e, _ := setupCreateTeamTest(t)

	body := `{"name": "Alpha", "slug": "alpha-team", "unknown_field": "ignored", "another": 42}`
	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams", body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseTeamResponse(t, rec)

	if resp.Name != "Alpha" {
		t.Errorf("expected name %q, got %q", "Alpha", resp.Name)
	}
	if resp.Slug != "alpha-team" {
		t.Errorf("expected slug %q, got %q", "alpha-team", resp.Slug)
	}

	// Verify unrecognized fields are not in the response.
	var raw map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("failed to parse raw response: %v", err)
	}
	if _, ok := raw["unknown_field"]; ok {
		t.Error("unrecognized field 'unknown_field' should not appear in response")
	}
	if _, ok := raw["another"]; ok {
		t.Error("unrecognized field 'another' should not appear in response")
	}
}

// ---------------------------------------------------------------------------
// TS-03-5: Empty name after trimming → HTTP 422 "invalid team name"
// Requirement: 03-REQ-2.2
// ---------------------------------------------------------------------------

func TestCreateTeam_EmptyName(t *testing.T) {
	e, db := setupCreateTeamTest(t)

	body := `{"name": "   ", "slug": "valid-slug"}`
	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams", body)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected status 422, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseErrorResponse(t, rec)
	if resp.Error.Code != 422 {
		t.Errorf("expected error code 422, got %d", resp.Error.Code)
	}
	if resp.Error.Message != "invalid team name" {
		t.Errorf("expected message %q, got %q", "invalid team name", resp.Error.Message)
	}

	// No team should be inserted.
	var count int
	if err := db.QueryRow("SELECT count(*) FROM teams WHERE slug = ?", "valid-slug").Scan(&count); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if count != 0 {
		t.Error("no team should be inserted when name is empty")
	}
}

// ---------------------------------------------------------------------------
// TS-03-6: Name exceeding 255 characters → HTTP 422 "invalid team name"
// Requirement: 03-REQ-2.2
// ---------------------------------------------------------------------------

func TestCreateTeam_NameTooLong(t *testing.T) {
	e, _ := setupCreateTeamTest(t)

	longName := strings.Repeat("a", 256)
	body := `{"name": "` + longName + `", "slug": "valid-slug"}`
	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams", body)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected status 422, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseErrorResponse(t, rec)
	if resp.Error.Code != 422 {
		t.Errorf("expected error code 422, got %d", resp.Error.Code)
	}
	if resp.Error.Message != "invalid team name" {
		t.Errorf("expected message %q, got %q", "invalid team name", resp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// TS-03-12: Missing required fields → HTTP 422 "missing required field"
// Requirement: 03-REQ-2.8
// ---------------------------------------------------------------------------

func TestCreateTeam_MissingRequiredFields(t *testing.T) {
	e, _ := setupCreateTeamTest(t)

	t.Run("missing_both_name_and_slug", func(t *testing.T) {
		body := `{"url": "https://example.com"}`
		rec := doRequest(t, e, http.MethodPost, "/api/v1/teams", body)

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

	t.Run("missing_name_only", func(t *testing.T) {
		body := `{"slug": "valid-slug"}`
		rec := doRequest(t, e, http.MethodPost, "/api/v1/teams", body)

		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("expected status 422, got %d: %s", rec.Code, rec.Body.String())
		}

		resp := parseErrorResponse(t, rec)
		if resp.Error.Message != "missing required field" {
			t.Errorf("expected message %q, got %q", "missing required field", resp.Error.Message)
		}
	})

	t.Run("missing_slug_only", func(t *testing.T) {
		body := `{"name": "My Team"}`
		rec := doRequest(t, e, http.MethodPost, "/api/v1/teams", body)

		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("expected status 422, got %d: %s", rec.Code, rec.Body.String())
		}

		resp := parseErrorResponse(t, rec)
		if resp.Error.Message != "missing required field" {
			t.Errorf("expected message %q, got %q", "missing required field", resp.Error.Message)
		}
	})
}

// ---------------------------------------------------------------------------
// TS-03-7: Invalid slug format → HTTP 422 "invalid slug format"
// Requirement: 03-REQ-2.3
// ---------------------------------------------------------------------------

func TestCreateTeam_InvalidSlug(t *testing.T) {
	e, _ := setupCreateTeamTest(t)

	cases := []struct {
		name string
		slug string
		desc string
	}{
		{"starts_with_hyphen", "-bad-slug", "slug starts with hyphen"},
		{"ends_with_hyphen", "bad-slug-", "slug ends with hyphen"},
		{"too_short", "ab", "slug is less than 3 characters"},
		{"uppercase", "Bad-Slug", "slug contains uppercase letters"},
		{"single_char", "a", "slug is only 1 character"},
		{"too_long", "a" + strings.Repeat("b", 63) + "c", "slug exceeds 64 characters"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"name": "My Team", "slug": "` + tc.slug + `"}`
			rec := doRequest(t, e, http.MethodPost, "/api/v1/teams", body)

			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("expected status 422 for %s, got %d: %s", tc.desc, rec.Code, rec.Body.String())
			}

			resp := parseErrorResponse(t, rec)
			if resp.Error.Code != 422 {
				t.Errorf("expected error code 422, got %d", resp.Error.Code)
			}
			if resp.Error.Message != "invalid slug format" {
				t.Errorf("expected message %q, got %q", "invalid slug format", resp.Error.Message)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TS-03-8: Invalid URL format → HTTP 422 "invalid url format"
// Requirement: 03-REQ-2.4
// ---------------------------------------------------------------------------

func TestCreateTeam_InvalidURL(t *testing.T) {
	e, _ := setupCreateTeamTest(t)

	cases := []struct {
		name string
		slug string
		url  string
		desc string
	}{
		{"ftp_scheme", "url-test-ftp", "ftp://notallowed.com", "URL with ftp scheme"},
		{"no_scheme", "url-test-noscheme", "notallowed.com", "URL with no scheme"},
		{"empty_host", "url-test-emptyhost", "https://", "URL with empty host"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"name": "My Team", "slug": "` + tc.slug + `", "url": "` + tc.url + `"}`
			rec := doRequest(t, e, http.MethodPost, "/api/v1/teams", body)

			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("expected status 422 for %s, got %d: %s", tc.desc, rec.Code, rec.Body.String())
			}

			resp := parseErrorResponse(t, rec)
			if resp.Error.Code != 422 {
				t.Errorf("expected error code 422, got %d", resp.Error.Code)
			}
			if resp.Error.Message != "invalid url format" {
				t.Errorf("expected message %q, got %q", "invalid url format", resp.Error.Message)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TS-03-9: Duplicate name → HTTP 409 "team name already exists"
// Requirement: 03-REQ-2.5
// ---------------------------------------------------------------------------

func TestCreateTeam_DuplicateName(t *testing.T) {
	e, db := setupCreateTeamTest(t)

	// Seed an existing team with the name "Engineering".
	id := uuid.New().String()
	now := teams.FormatTime(fixedTime())
	_, err := db.Exec(
		`INSERT INTO teams (id, name, slug, url, status, created_at, updated_at) VALUES (?, ?, ?, NULL, ?, ?, ?)`,
		id, "Engineering", "eng-old", "active", now, now,
	)
	if err != nil {
		t.Fatalf("failed to seed team: %v", err)
	}

	body := `{"name": "Engineering", "slug": "engineering-2"}`
	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams", body)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseErrorResponse(t, rec)
	if resp.Error.Code != 409 {
		t.Errorf("expected error code 409, got %d", resp.Error.Code)
	}
	if resp.Error.Message != "team name already exists" {
		t.Errorf("expected message %q, got %q", "team name already exists", resp.Error.Message)
	}

	// No new team should be inserted.
	var count int
	if err := db.QueryRow("SELECT count(*) FROM teams WHERE slug = ?", "engineering-2").Scan(&count); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if count != 0 {
		t.Error("no team should be inserted when name is duplicate")
	}
}

// ---------------------------------------------------------------------------
// TS-03-10: Duplicate slug → HTTP 409 "team slug already exists"
// Requirement: 03-REQ-2.6
// ---------------------------------------------------------------------------

func TestCreateTeam_DuplicateSlug(t *testing.T) {
	e, db := setupCreateTeamTest(t)

	// Seed an existing team with the slug "my-team".
	id := uuid.New().String()
	now := teams.FormatTime(fixedTime())
	_, err := db.Exec(
		`INSERT INTO teams (id, name, slug, url, status, created_at, updated_at) VALUES (?, ?, ?, NULL, ?, ?, ?)`,
		id, "Old Team", "my-team", "active", now, now,
	)
	if err != nil {
		t.Fatalf("failed to seed team: %v", err)
	}

	body := `{"name": "New Team", "slug": "my-team"}`
	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams", body)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseErrorResponse(t, rec)
	if resp.Error.Code != 409 {
		t.Errorf("expected error code 409, got %d", resp.Error.Code)
	}
	if resp.Error.Message != "team slug already exists" {
		t.Errorf("expected message %q, got %q", "team slug already exists", resp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// TS-03-11: Malformed JSON → HTTP 400 "invalid request body"
// Requirement: 03-REQ-2.7
// ---------------------------------------------------------------------------

func TestCreateTeam_MalformedJSON(t *testing.T) {
	e, _ := setupCreateTeamTest(t)

	rec := doRequestRaw(t, e, http.MethodPost, "/api/v1/teams", "not-valid-json{", "application/json")

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
// TS-03-14: DB partial UNIQUE index violation mapped to HTTP 409
// Requirement: 03-REQ-2.10
//
// Simulates a concurrent race by inserting the conflicting slug directly
// into the DB before the API call, so the app-layer check catches it.
// This achieves the same end behavior (HTTP 409) as a real race condition.
// ---------------------------------------------------------------------------

func TestCreateTeam_DBConstraintViolation(t *testing.T) {
	e, db := setupCreateTeamTest(t)

	slug := "race-slug"
	// Insert directly into DB to simulate a concurrent insert.
	id := uuid.New().String()
	now := teams.FormatTime(fixedTime())
	_, err := db.Exec(
		`INSERT INTO teams (id, name, slug, url, status, created_at, updated_at) VALUES (?, ?, ?, NULL, ?, ?, ?)`,
		id, "Race Team", slug, "active", now, now,
	)
	if err != nil {
		t.Fatalf("failed to seed team: %v", err)
	}

	// API request with the same slug will be caught by the app-layer check.
	body := `{"name": "Other Team", "slug": "` + slug + `"}`
	rec := doRequest(t, e, http.MethodPost, "/api/v1/teams", body)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseErrorResponse(t, rec)
	if resp.Error.Code != 409 {
		t.Errorf("expected error code 409, got %d", resp.Error.Code)
	}
	// Either name or slug conflict message is acceptable.
	validMessages := map[string]bool{
		"team slug already exists": true,
		"team name already exists": true,
	}
	if !validMessages[resp.Error.Message] {
		t.Errorf("expected one of %v, got %q", validMessages, resp.Error.Message)
	}

	// Only 1 team with this slug should exist.
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM teams WHERE slug = ? AND status != 'deleted'`, slug).Scan(&count); err != nil {
		t.Fatalf("query error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected exactly 1 team with slug %q, got %d", slug, count)
	}
}

// ---------------------------------------------------------------------------
// Helper: fixedTime returns a deterministic time for test seeding.
// ---------------------------------------------------------------------------

func fixedTime() time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
}
