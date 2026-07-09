// workspace_v2_handler_test.go — integration tests for POST /api/v1/workspaces
// per the spec 07 workspace entity schema (git_url, branch, owner_id, team_id).
//
// These tests exercise the V2 handler through the full Echo stack including
// auth middleware. They are expected to FAIL until task group 10 implements the
// real handler logic (the stub returns 501 Not Implemented).
//
// Test IDs map to the test specification:
//   TS-07-7:  Happy path — 201 with all fields
//   TS-07-8:  Missing slug/git_url — 400
//   TS-07-9:  Invalid slug format — 400
//   TS-07-10: Invalid git_url format — 400
//   TS-07-11: Duplicate slug — 409
//   TS-07-12: Non-existent team_id — 404
//   TS-07-13: User not team member — 403
//   TS-07-14: Admin token — 403
//   TS-07-15: owner_id from auth, not body — 201
//   TS-07-16: Null optional fields — 201
//   TS-07-E5: Malformed JSON — 400
//   TS-07-E6: Timeout — 500
//   TS-07-E7: No auth header — 401
package handler_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox/af-hub/internal/auth"
	"github.com/agent-fox/af-hub/internal/handler"
	"github.com/agent-fox/af-hub/internal/store"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Test infrastructure
// ---------------------------------------------------------------------------

// v2HandlerTestEnv holds the test infrastructure for workspace V2 handler tests.
type v2HandlerTestEnv struct {
	Echo  *echo.Echo
	Store store.Store
	DB    *sql.DB // direct SQL for seeding teams, workspaces, etc.
}

// newV2HandlerTestEnv creates a fresh test environment with the spec 07 schema,
// auth middleware, and the V2 handler wired to POST /api/v1/workspaces.
//
// The schema includes:
//   - users, api_keys, admin_tokens — for auth middleware
//   - teams, team_members — for team existence/membership checks
//   - workspaces — spec 07 schema (id, slug, git_url, branch, owner_id, team_id, status, created_at)
//
// FK enforcement is NOT enabled (intentional) — these tests focus on handler
// logic, not DB-level FK validation. Store-level FK tests live in
// internal/store/workspace_store_test.go (task group 2).
func newV2HandlerTestEnv(t *testing.T) *v2HandlerTestEnv {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("newV2HandlerTestEnv: open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("newV2HandlerTestEnv: enable WAL: %v", err)
	}

	// Create all tables needed for auth + spec 07 handler.
	schema := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			username TEXT UNIQUE NOT NULL,
			email TEXT,
			full_name TEXT,
			provider TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			status TEXT DEFAULT 'active',
			created_at TEXT,
			updated_at TEXT,
			UNIQUE(provider, provider_id)
		)`,
		// teams table — renamed from old workspaces by spec 06.
		`CREATE TABLE IF NOT EXISTS teams (
			id TEXT PRIMARY KEY,
			name TEXT UNIQUE NOT NULL,
			slug TEXT UNIQUE NOT NULL,
			status TEXT DEFAULT 'active',
			created_at TEXT,
			created_by TEXT
		)`,
		// team_members — membership association for team access checks.
		`CREATE TABLE IF NOT EXISTS team_members (
			user_id TEXT,
			team_id TEXT,
			role TEXT NOT NULL,
			created_at TEXT,
			granted_by TEXT,
			PRIMARY KEY (user_id, team_id)
		)`,
		// New workspaces table per spec 07-REQ-1.1.
		`CREATE TABLE IF NOT EXISTS workspaces (
			id TEXT PRIMARY KEY,
			slug TEXT UNIQUE NOT NULL,
			git_url TEXT NOT NULL,
			branch TEXT,
			owner_id TEXT NOT NULL,
			team_id TEXT,
			status TEXT NOT NULL DEFAULT 'active',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		// api_keys — needed for auth middleware.
		`CREATE TABLE IF NOT EXISTS api_keys (
			id TEXT PRIMARY KEY,
			key_id TEXT UNIQUE,
			key_hash TEXT,
			user_id TEXT,
			workspace_id TEXT,
			role TEXT,
			label TEXT,
			expires_at TEXT,
			revoked_at TEXT,
			created_at TEXT
		)`,
		// admin_tokens — needed for auth middleware.
		`CREATE TABLE IF NOT EXISTS admin_tokens (
			id TEXT PRIMARY KEY,
			token_hash TEXT,
			created_at TEXT
		)`,
	}
	for _, stmt := range schema {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("newV2HandlerTestEnv: schema exec: %v\nStatement: %s", err, stmt)
		}
	}

	s := store.NewStore(db)

	// Wire Echo with auth middleware and V2 handler.
	e := echo.New()
	e.HTTPErrorHandler = handler.CustomHTTPErrorHandler

	wsHandler := handler.NewWorkspaceV2Handler(s)
	apiGroup := e.Group("/api/v1", auth.AuthMiddleware(s))
	apiGroup.POST("/workspaces", wsHandler.CreateWorkspaceV2)

	return &v2HandlerTestEnv{Echo: e, Store: s, DB: db}
}

// doV2Request sends an HTTP request against the test Echo server.
func doV2Request(e *echo.Echo, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// sha256Hex computes the hex-encoded SHA-256 hash of a string.
func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// v2APIKeyHeaders returns Authorization headers for an API key token.
// Token format: "af_<keyID>_<secret>".
func v2APIKeyHeaders(keyID, secret string) map[string]string {
	return map[string]string{
		"Authorization": "Bearer af_" + keyID + "_" + secret,
	}
}

// v2AdminHeaders returns Authorization headers for an admin token.
func v2AdminHeaders(fullToken string) map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + fullToken,
	}
}

// seedV2User creates a user in the test database and returns the User struct.
func seedV2User(t *testing.T, s store.Store, username string) *store.User {
	t.Helper()
	user, err := s.CreateUser(&store.User{
		Username:   username,
		Email:      username + "@test.com",
		Provider:   "local",
		ProviderID: username + "_pid",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("seedV2User(%q): %v", username, err)
	}
	return user
}

// seedV2APIKey creates an API key for the given user.
func seedV2APIKey(t *testing.T, s store.Store, userID, keyID, secret, role, wsID string) {
	t.Helper()
	_, err := s.CreateAPIKey(&store.APIKey{
		KeyID:       keyID,
		KeyHash:     sha256Hex(secret),
		UserID:      userID,
		WorkspaceID: wsID,
		Role:        role,
		Label:       "test key " + keyID,
	})
	if err != nil {
		t.Fatalf("seedV2APIKey(%q): %v", keyID, err)
	}
}

// seedV2AdminToken creates an admin token in the store.
// fullToken should include the "af_admin_" prefix.
func seedV2AdminToken(t *testing.T, s store.Store, fullToken string) {
	t.Helper()
	_, err := s.CreateAdminToken(&store.AdminToken{
		TokenHash: sha256Hex(fullToken),
	})
	if err != nil {
		t.Fatalf("seedV2AdminToken: %v", err)
	}
}

// seedV2Team creates a team in the test database and returns its UUID.
func seedV2Team(t *testing.T, db *sql.DB, slug, createdBy string) string {
	t.Helper()
	id := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO teams (id, name, slug, status, created_at, created_by)
		 VALUES (?, ?, ?, 'active', ?, ?)`,
		id, "team-"+slug, slug, now, createdBy,
	)
	if err != nil {
		t.Fatalf("seedV2Team(%q): %v", slug, err)
	}
	return id
}

// seedV2TeamMember adds a user as a member of a team with the given role.
func seedV2TeamMember(t *testing.T, db *sql.DB, userID, teamID, role string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO team_members (user_id, team_id, role, created_at)
		 VALUES (?, ?, ?, ?)`,
		userID, teamID, role, now,
	)
	if err != nil {
		t.Fatalf("seedV2TeamMember(user=%s, team=%s): %v", userID, teamID, err)
	}
}

// seedV2Workspace inserts a workspace row using the spec 07 schema.
func seedV2Workspace(t *testing.T, db *sql.DB, slug, gitURL, ownerID string) string {
	t.Helper()
	id := uuid.New().String()
	_, err := db.Exec(
		`INSERT INTO workspaces (id, slug, git_url, owner_id, status, created_at)
		 VALUES (?, ?, ?, ?, 'active', datetime('now'))`,
		id, slug, gitURL, ownerID,
	)
	if err != nil {
		t.Fatalf("seedV2Workspace(%q): %v", slug, err)
	}
	return id
}

// Response types for assertions.

// workspaceV2Response represents a workspace object from the spec 07 schema.
type workspaceV2Response struct {
	ID        string  `json:"id"`
	Slug      string  `json:"slug"`
	GitURL    string  `json:"git_url"`
	Branch    *string `json:"branch"`
	OwnerID   string  `json:"owner_id"`
	TeamID    *string `json:"team_id"`
	Status    string  `json:"status"`
	CreatedAt string  `json:"created_at"`
}

// v2ErrorResponse is the standard error envelope for assertions.
type v2ErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// assertV2Error parses the response body and asserts the error envelope contents.
func assertV2Error(t *testing.T, rec *httptest.ResponseRecorder, wantCode int, wantMessage string) {
	t.Helper()
	if rec.Code != wantCode {
		t.Fatalf("status = %d, want %d\nBody: %s", rec.Code, wantCode, rec.Body.String())
	}
	var errResp v2ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to parse error JSON: %v\nBody: %s", err, rec.Body.String())
	}
	wantCodeStr := http.StatusText(wantCode)
	// Use numeric code string (e.g. "400", "409") per the standard error envelope.
	wantCodeStr = strings.TrimSpace(errResp.Error.Code)
	if wantCodeStr == "" {
		t.Error("error.code is empty")
	}
	if errResp.Error.Message != wantMessage {
		t.Errorf("error.message = %q, want %q", errResp.Error.Message, wantMessage)
	}
}

// ---------------------------------------------------------------------------
// Subtask 3.1: Happy path and optional fields (TS-07-7, TS-07-15, TS-07-16)
// ---------------------------------------------------------------------------

// TestWorkspaceV2Handler_Create_HappyPath verifies POST /api/v1/workspaces
// returns HTTP 201 with a fully populated workspace JSON object.
// TS-07-7
func TestWorkspaceV2Handler_Create_HappyPath(t *testing.T) {
	env := newV2HandlerTestEnv(t)
	user := seedV2User(t, env.Store, "ws-user1")
	seedV2APIKey(t, env.Store, user.ID, "key07a", "secret07a", "editor", "dummy-ws")

	body := `{"slug":"my-api","git_url":"https://github.com/org/repo.git","branch":"main"}`
	rec := doV2Request(env.Echo, http.MethodPost, "/api/v1/workspaces", body,
		v2APIKeyHeaders("key07a", "secret07a"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d\nBody: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var ws workspaceV2Response
	if err := json.Unmarshal(rec.Body.Bytes(), &ws); err != nil {
		t.Fatalf("failed to parse response JSON: %v\nBody: %s", err, rec.Body.String())
	}

	// id must be a valid UUID.
	if _, err := uuid.Parse(ws.ID); err != nil {
		t.Errorf("id %q is not a valid UUID: %v", ws.ID, err)
	}
	if ws.Slug != "my-api" {
		t.Errorf("slug = %q, want %q", ws.Slug, "my-api")
	}
	if ws.GitURL != "https://github.com/org/repo.git" {
		t.Errorf("git_url = %q, want %q", ws.GitURL, "https://github.com/org/repo.git")
	}
	if ws.Branch == nil || *ws.Branch != "main" {
		t.Errorf("branch = %v, want %q", ws.Branch, "main")
	}
	if ws.OwnerID != user.ID {
		t.Errorf("owner_id = %q, want %q", ws.OwnerID, user.ID)
	}
	if ws.TeamID != nil {
		t.Errorf("team_id = %v, want nil", ws.TeamID)
	}
	if ws.Status != "active" {
		t.Errorf("status = %q, want %q", ws.Status, "active")
	}
	if ws.CreatedAt == "" {
		t.Error("created_at should not be empty")
	}
}

// TestWorkspaceV2Handler_Create_OwnerIDFromAuth verifies that owner_id is set
// from the authenticated user's identity and cannot be supplied by the request body.
// TS-07-15
func TestWorkspaceV2Handler_Create_OwnerIDFromAuth(t *testing.T) {
	env := newV2HandlerTestEnv(t)
	user := seedV2User(t, env.Store, "real-user")
	seedV2APIKey(t, env.Store, user.ID, "key07b", "secret07b", "editor", "dummy-ws")

	// Include owner_id in the body as an attacker would.
	body := `{"slug":"my-api","git_url":"https://github.com/org/repo.git","owner_id":"attacker-uuid"}`
	rec := doV2Request(env.Echo, http.MethodPost, "/api/v1/workspaces", body,
		v2APIKeyHeaders("key07b", "secret07b"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d\nBody: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var ws workspaceV2Response
	if err := json.Unmarshal(rec.Body.Bytes(), &ws); err != nil {
		t.Fatalf("failed to parse response JSON: %v", err)
	}

	// owner_id must come from the authenticated user, not the request body.
	if ws.OwnerID != user.ID {
		t.Errorf("owner_id = %q, want %q (the authenticated user)", ws.OwnerID, user.ID)
	}
	if ws.OwnerID == "attacker-uuid" {
		t.Error("owner_id should NOT be the attacker-supplied value")
	}
}

// TestWorkspaceV2Handler_Create_NullOptionalFields verifies that omitting
// branch and team_id stores NULL and reflects null in the response JSON.
// TS-07-16
func TestWorkspaceV2Handler_Create_NullOptionalFields(t *testing.T) {
	env := newV2HandlerTestEnv(t)
	user := seedV2User(t, env.Store, "null-fields-user")
	seedV2APIKey(t, env.Store, user.ID, "key07c", "secret07c", "editor", "dummy-ws")

	body := `{"slug":"no-branch-ws","git_url":"https://github.com/org/repo.git"}`
	rec := doV2Request(env.Echo, http.MethodPost, "/api/v1/workspaces", body,
		v2APIKeyHeaders("key07c", "secret07c"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d\nBody: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var ws workspaceV2Response
	if err := json.Unmarshal(rec.Body.Bytes(), &ws); err != nil {
		t.Fatalf("failed to parse response JSON: %v", err)
	}

	if ws.Branch != nil {
		t.Errorf("branch = %v, want nil", ws.Branch)
	}
	if ws.TeamID != nil {
		t.Errorf("team_id = %v, want nil", ws.TeamID)
	}

	// Also verify DB row has NULL for both columns.
	var branch, teamID sql.NullString
	err := env.DB.QueryRow(
		"SELECT branch, team_id FROM workspaces WHERE slug=?", "no-branch-ws",
	).Scan(&branch, &teamID)
	if err != nil {
		t.Fatalf("DB query for workspace row failed: %v", err)
	}
	if branch.Valid {
		t.Errorf("DB branch = %q, want NULL", branch.String)
	}
	if teamID.Valid {
		t.Errorf("DB team_id = %q, want NULL", teamID.String)
	}
}

// ---------------------------------------------------------------------------
// Subtask 3.2: 400 validation errors (TS-07-8, TS-07-9, TS-07-10)
// ---------------------------------------------------------------------------

// TestWorkspaceV2Handler_Create_MissingRequiredFields_Returns400 verifies
// HTTP 400 with "missing required fields" when slug or git_url is absent.
// TS-07-8
func TestWorkspaceV2Handler_Create_MissingRequiredFields_Returns400(t *testing.T) {
	env := newV2HandlerTestEnv(t)
	user := seedV2User(t, env.Store, "missing-fields-user")
	seedV2APIKey(t, env.Store, user.ID, "key07d", "secret07d", "editor", "dummy-ws")
	headers := v2APIKeyHeaders("key07d", "secret07d")

	t.Run("missing_slug", func(t *testing.T) {
		body := `{"git_url":"https://github.com/org/repo.git"}`
		rec := doV2Request(env.Echo, http.MethodPost, "/api/v1/workspaces", body, headers)
		assertV2Error(t, rec, http.StatusBadRequest, "missing required fields")
	})

	t.Run("missing_git_url", func(t *testing.T) {
		body := `{"slug":"my-ws"}`
		rec := doV2Request(env.Echo, http.MethodPost, "/api/v1/workspaces", body, headers)
		assertV2Error(t, rec, http.StatusBadRequest, "missing required fields")
	})

	t.Run("empty_body", func(t *testing.T) {
		body := `{}`
		rec := doV2Request(env.Echo, http.MethodPost, "/api/v1/workspaces", body, headers)
		assertV2Error(t, rec, http.StatusBadRequest, "missing required fields")
	})
}

// TestWorkspaceV2Handler_Create_InvalidSlug_Returns400 verifies HTTP 400
// with "invalid slug format" when the slug does not match the required format.
// TS-07-9
func TestWorkspaceV2Handler_Create_InvalidSlug_Returns400(t *testing.T) {
	env := newV2HandlerTestEnv(t)
	user := seedV2User(t, env.Store, "bad-slug-user")
	seedV2APIKey(t, env.Store, user.ID, "key07e", "secret07e", "editor", "dummy-ws")
	headers := v2APIKeyHeaders("key07e", "secret07e")

	invalidSlugs := []struct {
		name string
		slug string
	}{
		{"starts_with_digit", "1-bad-slug"},
		{"starts_with_hyphen", "-bad-slug"},
		{"uppercase", "MyApi"},
		{"consecutive_hyphens", "my--api"},
		{"trailing_hyphen", "my-api-"},
		{"too_short", "ab"},
	}

	for _, tc := range invalidSlugs {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"slug":"` + tc.slug + `","git_url":"https://github.com/org/repo.git"}`
			rec := doV2Request(env.Echo, http.MethodPost, "/api/v1/workspaces", body, headers)
			assertV2Error(t, rec, http.StatusBadRequest, "invalid slug format")
		})
	}
}

// TestWorkspaceV2Handler_Create_InvalidGitURL_Returns400 verifies HTTP 400
// with "invalid git_url format" when the git_url is not a valid HTTPS or SSH URL.
// TS-07-10
func TestWorkspaceV2Handler_Create_InvalidGitURL_Returns400(t *testing.T) {
	env := newV2HandlerTestEnv(t)
	user := seedV2User(t, env.Store, "bad-url-user")
	seedV2APIKey(t, env.Store, user.ID, "key07f", "secret07f", "editor", "dummy-ws")
	headers := v2APIKeyHeaders("key07f", "secret07f")

	invalidURLs := []struct {
		name   string
		gitURL string
	}{
		{"local_path", "/local/path/repo"},
		{"http_not_https", "http://github.com/repo.git"},
		{"git_protocol", "git://github.com/repo.git"},
		{"ssh_scheme", "ssh://git@github.com/repo.git"},
		{"ftp_scheme", "ftp://example.com/repo"},
	}

	for _, tc := range invalidURLs {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"slug":"my-api","git_url":"` + tc.gitURL + `"}`
			rec := doV2Request(env.Echo, http.MethodPost, "/api/v1/workspaces", body, headers)
			assertV2Error(t, rec, http.StatusBadRequest, "invalid git_url format")
		})
	}
}

// ---------------------------------------------------------------------------
// Subtask 3.3: 409 conflict and 404 team not found (TS-07-11, TS-07-12)
// ---------------------------------------------------------------------------

// TestWorkspaceV2Handler_Create_DuplicateSlug_Returns409 verifies HTTP 409
// with "workspace slug already exists" when the slug is already taken.
// TS-07-11
func TestWorkspaceV2Handler_Create_DuplicateSlug_Returns409(t *testing.T) {
	env := newV2HandlerTestEnv(t)
	user := seedV2User(t, env.Store, "dup-slug-user")
	seedV2APIKey(t, env.Store, user.ID, "key07g", "secret07g", "editor", "dummy-ws")

	// Pre-insert a workspace with the slug that will be duplicated.
	seedV2Workspace(t, env.DB, "existing-ws", "https://github.com/org/repo.git", user.ID)

	body := `{"slug":"existing-ws","git_url":"https://github.com/org/other.git"}`
	rec := doV2Request(env.Echo, http.MethodPost, "/api/v1/workspaces", body,
		v2APIKeyHeaders("key07g", "secret07g"))

	assertV2Error(t, rec, http.StatusConflict, "workspace slug already exists")
}

// TestWorkspaceV2Handler_Create_TeamNotFound_Returns404 verifies HTTP 404
// with "team not found" when team_id does not match any team.
// TS-07-12
func TestWorkspaceV2Handler_Create_TeamNotFound_Returns404(t *testing.T) {
	env := newV2HandlerTestEnv(t)
	user := seedV2User(t, env.Store, "team-404-user")
	seedV2APIKey(t, env.Store, user.ID, "key07h", "secret07h", "editor", "dummy-ws")

	body := `{"slug":"my-api","git_url":"https://github.com/org/repo.git","team_id":"nonexistent-team-uuid"}`
	rec := doV2Request(env.Echo, http.MethodPost, "/api/v1/workspaces", body,
		v2APIKeyHeaders("key07h", "secret07h"))

	assertV2Error(t, rec, http.StatusNotFound, "team not found")
}

// ---------------------------------------------------------------------------
// Subtask 3.4: 403 membership and admin token (TS-07-13, TS-07-14)
// ---------------------------------------------------------------------------

// TestWorkspaceV2Handler_Create_NotTeamMember_Returns403 verifies HTTP 403
// with "not a member of this team" when the user is not a member of the team.
// TS-07-13
func TestWorkspaceV2Handler_Create_NotTeamMember_Returns403(t *testing.T) {
	env := newV2HandlerTestEnv(t)
	user := seedV2User(t, env.Store, "non-member-user")
	teamCreator := seedV2User(t, env.Store, "team-creator")
	seedV2APIKey(t, env.Store, user.ID, "key07i", "secret07i", "editor", "dummy-ws")

	// Create team but do NOT add user as a member.
	teamID := seedV2Team(t, env.DB, "private-team", teamCreator.ID)

	body := `{"slug":"my-api","git_url":"https://github.com/org/repo.git","team_id":"` + teamID + `"}`
	rec := doV2Request(env.Echo, http.MethodPost, "/api/v1/workspaces", body,
		v2APIKeyHeaders("key07i", "secret07i"))

	assertV2Error(t, rec, http.StatusForbidden, "not a member of this team")
}

// TestWorkspaceV2Handler_Create_AdminToken_Returns403 verifies HTTP 403
// with "workspace creation requires user authentication" when an admin token is used.
// TS-07-14
func TestWorkspaceV2Handler_Create_AdminToken_Returns403(t *testing.T) {
	env := newV2HandlerTestEnv(t)
	adminToken := "af_admin_test07admin"
	seedV2AdminToken(t, env.Store, adminToken)

	body := `{"slug":"my-api","git_url":"https://github.com/org/repo.git"}`
	rec := doV2Request(env.Echo, http.MethodPost, "/api/v1/workspaces", body,
		v2AdminHeaders(adminToken))

	assertV2Error(t, rec, http.StatusForbidden, "workspace creation requires user authentication")
}

// ---------------------------------------------------------------------------
// Subtask 3.5: Edge cases (TS-07-E5, TS-07-E6, TS-07-E7)
// ---------------------------------------------------------------------------

// TestWorkspaceV2Handler_Create_MalformedJSON_Returns400 verifies HTTP 400
// with "missing required fields" when the request body is malformed JSON.
// TS-07-E5
func TestWorkspaceV2Handler_Create_MalformedJSON_Returns400(t *testing.T) {
	env := newV2HandlerTestEnv(t)
	user := seedV2User(t, env.Store, "malformed-user")
	seedV2APIKey(t, env.Store, user.ID, "key07j", "secret07j", "editor", "dummy-ws")

	rec := doV2Request(env.Echo, http.MethodPost, "/api/v1/workspaces",
		"{invalid-json}",
		v2APIKeyHeaders("key07j", "secret07j"))

	assertV2Error(t, rec, http.StatusBadRequest, "missing required fields")
}

// TestWorkspaceV2Handler_Create_Timeout_Returns500 verifies the handler returns
// HTTP 500 when the database write fails, and no goroutines leak.
// TS-07-E6
//
// This test uses SQLite write-locking to simulate a database failure: an
// uncommitted transaction holds the WAL write lock, so the handler's INSERT
// gets SQLITE_BUSY immediately (busy_timeout defaults to 0), which the
// handler maps to a 500 response.
func TestWorkspaceV2Handler_Create_Timeout_Returns500(t *testing.T) {
	env := newV2HandlerTestEnv(t)
	user := seedV2User(t, env.Store, "timeout-user")
	seedV2APIKey(t, env.Store, user.ID, "key07k", "secret07k", "editor", "dummy-ws")

	// Hold a write lock via an uncommitted transaction so the handler's
	// INSERT gets SQLITE_BUSY, which surfaces as a 500 error.
	tx, err := env.DB.Begin()
	if err != nil {
		t.Fatalf("begin blocking tx: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT INTO workspaces (id, slug, git_url, owner_id, status, created_at)
		 VALUES ('block-id', 'block-slug', 'https://block.example.com', ?, 'active', datetime('now'))`,
		user.ID,
	); err != nil {
		t.Fatalf("exec blocking write: %v", err)
	}

	goroutinesBefore := runtime.NumGoroutine()

	body := `{"slug":"timeout-ws","git_url":"https://github.com/org/repo.git"}`
	rec := doV2Request(env.Echo, http.MethodPost, "/api/v1/workspaces", body,
		v2APIKeyHeaders("key07k", "secret07k"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d\nBody: %s",
			rec.Code, http.StatusInternalServerError, rec.Body.String())
	}

	var errResp v2ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to parse error JSON: %v\nBody: %s", err, rec.Body.String())
	}
	if errResp.Error.Code == "" {
		t.Error("error.code should not be empty")
	}

	// Verify no goroutine leak.
	time.Sleep(200 * time.Millisecond)
	goroutinesAfter := runtime.NumGoroutine()
	if goroutinesAfter > goroutinesBefore+1 {
		t.Errorf("goroutine leak: before=%d, after=%d", goroutinesBefore, goroutinesAfter)
	}
}

// TestWorkspaceV2Handler_Create_NoAuth_Returns401 verifies HTTP 401 when no
// Authorization header is present — the auth middleware rejects the request
// before the handler logic runs.
// TS-07-E7
func TestWorkspaceV2Handler_Create_NoAuth_Returns401(t *testing.T) {
	env := newV2HandlerTestEnv(t)

	body := `{"slug":"my-ws","git_url":"https://github.com/org/repo.git"}`
	rec := doV2Request(env.Echo, http.MethodPost, "/api/v1/workspaces", body,
		nil) // no auth headers

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d\nBody: %s",
			rec.Code, http.StatusUnauthorized, rec.Body.String())
	}

	var errResp v2ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to parse error JSON: %v", err)
	}
	if errResp.Error.Code == "" {
		t.Error("error.code should not be empty")
	}
}

// ---------------------------------------------------------------------------
// Happy path with team association — validates team membership is checked
// when team_id is provided and user IS a member (should succeed).
// Supplements TS-07-7 / TS-07-SMOKE-1.
// ---------------------------------------------------------------------------

// TestWorkspaceV2Handler_Create_WithTeam_HappyPath verifies workspace creation
// succeeds when a valid team_id is provided and the user is a member.
func TestWorkspaceV2Handler_Create_WithTeam_HappyPath(t *testing.T) {
	env := newV2HandlerTestEnv(t)
	user := seedV2User(t, env.Store, "team-member-user")
	seedV2APIKey(t, env.Store, user.ID, "key07l", "secret07l", "editor", "dummy-ws")

	// Create team and add user as member.
	teamID := seedV2Team(t, env.DB, "my-team", user.ID)
	seedV2TeamMember(t, env.DB, user.ID, teamID, "editor")

	body := `{"slug":"team-ws","git_url":"git@github.com:org/repo.git","team_id":"` + teamID + `"}`
	rec := doV2Request(env.Echo, http.MethodPost, "/api/v1/workspaces", body,
		v2APIKeyHeaders("key07l", "secret07l"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d\nBody: %s",
			rec.Code, http.StatusCreated, rec.Body.String())
	}

	var ws workspaceV2Response
	if err := json.Unmarshal(rec.Body.Bytes(), &ws); err != nil {
		t.Fatalf("failed to parse response JSON: %v", err)
	}

	if ws.TeamID == nil || *ws.TeamID != teamID {
		t.Errorf("team_id = %v, want %q", ws.TeamID, teamID)
	}
	if ws.OwnerID != user.ID {
		t.Errorf("owner_id = %q, want %q", ws.OwnerID, user.ID)
	}
}
