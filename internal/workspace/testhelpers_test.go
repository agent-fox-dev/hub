package workspace

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

// testEnv holds a test HTTP server and database for integration tests.
type testEnv struct {
	echo *echo.Echo
	db   *sql.DB
}

// newTestEnv creates an echo server with workspace routes mounted for testing.
// Uses an in-memory SQLite database initialised with the workspaces schema.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db := openTestDB(t)
	e := echo.New()
	api := e.Group("/api/v1")

	// Apply test auth middleware that reads AuthInfo from X-Test-Auth header.
	api.Use(testAuthMiddleware())

	// Register workspace routes.
	if err := RegisterRoutes(api, db); err != nil {
		t.Fatalf("RegisterRoutes() returned error: %v", err)
	}

	return &testEnv{echo: e, db: db}
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
func (env *testEnv) doRequest(t *testing.T, method, path, body string, auth *AuthInfo) *httptest.ResponseRecorder {
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

// seedWorkspace inserts a workspace directly into the database for test setup.
func (env *testEnv) seedWorkspace(t *testing.T, ws *Workspace) {
	t.Helper()
	if err := insertWorkspace(env.db, ws); err != nil {
		t.Fatalf("seedWorkspace(%q) returned error: %v", ws.Slug, err)
	}
}

// seedOrg inserts an organization into the orgs table for test setup.
// It does NOT add any members — use seedOrgMember for that.
func (env *testEnv) seedOrg(t *testing.T, orgID, name, slug string) {
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
func (env *testEnv) seedOrgMember(t *testing.T, orgID, userID string) {
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

// deleteWorkspaceBySlug removes a workspace row directly from the database.
func (env *testEnv) deleteWorkspaceBySlug(t *testing.T, slug string) {
	t.Helper()
	_, err := env.db.Exec("DELETE FROM workspaces WHERE slug = ?", slug)
	if err != nil {
		t.Fatalf("deleteWorkspaceBySlug(%q) returned error: %v", slug, err)
	}
}

// errorEnvelope represents the JSON error response envelope.
type errorEnvelope struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// workspaceJSON represents the JSON workspace object in API responses.
type workspaceJSON struct {
	Slug        string  `json:"slug"`
	GitURL      string  `json:"git_url"`
	Branch      *string `json:"branch"`
	DisplayName string  `json:"display_name"`
	Description string  `json:"description"`
	OwnerID     string  `json:"owner_id"`
	OrgID       *string `json:"org_id"`
	Status      string  `json:"status"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
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

// parseWorkspaceJSON parses the response body as a workspace JSON object.
func parseWorkspaceJSON(t *testing.T, rec *httptest.ResponseRecorder) workspaceJSON {
	t.Helper()
	var resp workspaceJSON
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode workspace response: %v", err)
	}
	return resp
}

// parseWorkspaceListJSON parses the response body as a JSON array of workspaces.
func parseWorkspaceListJSON(t *testing.T, rec *httptest.ResponseRecorder) []workspaceJSON {
	t.Helper()
	var resp []workspaceJSON
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode workspace list response: %v", err)
	}
	return resp
}
