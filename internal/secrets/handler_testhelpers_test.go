package secrets

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

// handlerTestEnv holds a test HTTP server and database for integration tests.
type handlerTestEnv struct {
	echo *echo.Echo
	db   *sql.DB
}

// newHandlerTestEnv creates an echo server with secrets routes mounted for testing.
// Uses an in-memory SQLite database initialised with the secrets/variables schema
// plus org/org_members/workspaces tables needed for auth checks.
func newHandlerTestEnv(t *testing.T) *handlerTestEnv {
	t.Helper()
	db := openTestDB(t)

	// Create the apikit orgs, org_members, and workspaces tables so that
	// org membership and workspace ownership checks in handlers work correctly.
	schemaSQL := []string{
		`CREATE TABLE IF NOT EXISTS orgs (
			id TEXT NOT NULL PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			slug TEXT NOT NULL UNIQUE,
			url TEXT,
			owner_id TEXT,
			status TEXT NOT NULL DEFAULT 'active',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS org_members (
			org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (org_id, user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS workspaces (
			slug TEXT NOT NULL PRIMARY KEY,
			git_url TEXT NOT NULL,
			branch TEXT,
			owner_id TEXT NOT NULL,
			org_id TEXT,
			status TEXT NOT NULL DEFAULT 'active',
			display_name TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			clone_status TEXT NOT NULL DEFAULT 'pending',
			head_sha TEXT,
			clone_error TEXT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
	}
	for _, stmt := range schemaSQL {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("failed to create supporting schema: %v", err)
		}
	}

	e := echo.New()
	api := e.Group("/api/v1")

	// Apply test auth middleware that reads AuthInfo from X-Test-Auth header.
	api.Use(testAuthMiddleware())

	// Register secrets routes.
	if err := RegisterRoutes(api, db); err != nil {
		t.Fatalf("RegisterRoutes() returned error: %v", err)
	}

	return &handlerTestEnv{echo: e, db: db}
}

// testAuthMiddleware returns middleware that reads AuthInfo from the
// X-Test-Auth JSON header. If absent, auth context remains unset
// (simulates an unauthenticated request).
func testAuthMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			authHeader := c.Request().Header.Get("X-Test-Auth")
			if authHeader != "" {
				var info AuthInfo
				if err := json.Unmarshal([]byte(authHeader), &info); err != nil {
					return echo.NewHTTPError(http.StatusBadRequest, "invalid X-Test-Auth header")
				}
				c.Set(authInfoKey, &info)
			}
			return next(c)
		}
	}
}

// doRequest performs an HTTP request against the test server with optional auth.
func (env *handlerTestEnv) doRequest(t *testing.T, method, path, body string, auth *AuthInfo) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if body != "" {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	if auth != nil {
		authJSON, err := json.Marshal(auth)
		if err != nil {
			t.Fatalf("failed to marshal auth info: %v", err)
		}
		req.Header.Set("X-Test-Auth", string(authJSON))
	}
	rec := httptest.NewRecorder()
	env.echo.ServeHTTP(rec, req)
	return rec
}

// adminAuth returns an AuthInfo representing an admin token.
func adminAuth() *AuthInfo {
	return &AuthInfo{
		CredType: CredentialAdmin,
	}
}

// userAuth returns an AuthInfo representing a user API key.
func userAuth(userID string) *AuthInfo {
	return &AuthInfo{
		CredType: CredentialAPIKey,
		UserID:   userID,
	}
}

// patAuth returns an AuthInfo representing a PAT with the given permission scopes.
func patAuth(userID string, permissions ...string) *AuthInfo {
	return &AuthInfo{
		CredType:    CredentialPAT,
		UserID:      userID,
		Permissions: permissions,
	}
}

// seedOrg inserts an organization into the orgs table for test setup.
func (env *handlerTestEnv) seedOrg(t *testing.T, orgID, name, slug string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := env.db.Exec(
		`INSERT INTO orgs (id, name, slug, status, created_at, updated_at) VALUES (?, ?, ?, 'active', ?, ?)`,
		orgID, name, slug, now, now,
	)
	if err != nil {
		t.Fatalf("seedOrg(%q) returned error: %v", orgID, err)
	}
}

// seedOrgMember adds a user as a member of an organization.
func (env *handlerTestEnv) seedOrgMember(t *testing.T, orgID, userID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := env.db.Exec(
		`INSERT INTO org_members (org_id, user_id, created_at) VALUES (?, ?, ?)`,
		orgID, userID, now,
	)
	if err != nil {
		t.Fatalf("seedOrgMember(%q, %q) returned error: %v", orgID, userID, err)
	}
}

// seedWorkspaceForTest inserts a workspace row into the database for test setup.
func (env *handlerTestEnv) seedWorkspaceForTest(t *testing.T, slug, ownerID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := env.db.Exec(
		`INSERT INTO workspaces (slug, git_url, owner_id, status, display_name, description, clone_status, created_at, updated_at)
		 VALUES (?, ?, ?, 'active', ?, '', 'pending', ?, ?)`,
		slug, "https://github.com/org/repo", ownerID, slug, now, now,
	)
	if err != nil {
		t.Fatalf("seedWorkspaceForTest(%q) returned error: %v", slug, err)
	}
}

// errorEnvelope represents the JSON error response envelope.
type errorEnvelope struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// parseErrorEnvelope parses the response body as a JSON error envelope.
func parseErrorEnvelope(t *testing.T, rec *httptest.ResponseRecorder) errorEnvelope {
	t.Helper()
	var resp errorEnvelope
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	return resp
}

// secretJSON represents a secret entry in API responses.
type secretJSON struct {
	Key       string `json:"key"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// parseSecretJSON parses the response body as a single secret JSON object.
func parseSecretJSON(t *testing.T, rec *httptest.ResponseRecorder) secretJSON {
	t.Helper()
	var resp secretJSON
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode secret response: %v", err)
	}
	return resp
}

// parseSecretListJSON parses the response body as a JSON array of secrets.
func parseSecretListJSON(t *testing.T, rec *httptest.ResponseRecorder) []secretJSON {
	t.Helper()
	var resp []secretJSON
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode secret list response: %v", err)
	}
	return resp
}

// parseRawJSON parses the response body as a generic map for field-presence checks.
func parseRawJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	return resp
}

// parseRawJSONArray parses the response body as a generic array of maps.
func parseRawJSONArray(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var resp []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode JSON array response: %v", err)
	}
	return resp
}

// seedWorkspaceWithOrg inserts a workspace row with an org_id for resolved variable tests.
func (env *handlerTestEnv) seedWorkspaceWithOrg(t *testing.T, slug, ownerID, orgID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := env.db.Exec(
		`INSERT INTO workspaces (slug, git_url, owner_id, org_id, status, display_name, description, clone_status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'active', ?, '', 'pending', ?, ?)`,
		slug, "https://github.com/org/repo", ownerID, orgID, slug, now, now,
	)
	if err != nil {
		t.Fatalf("seedWorkspaceWithOrg(%q) returned error: %v", slug, err)
	}
}
