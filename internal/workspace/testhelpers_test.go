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
	"github.com/txsvc/apikit"
)

// testEnv holds a test HTTP server and database for integration tests.
type testEnv struct {
	echo *echo.Echo
	db   *sql.DB
}

// newTestEnv creates an echo server with workspace routes mounted for testing.
// Uses an in-memory SQLite database initialised with the workspaces schema.
//
// Spec 04 requires every workspace to have an org_id. When org_id is omitted
// from a create request, the handler auto-defaults to the user's personal org.
// To keep existing tests working without individual modifications, newTestEnv
// seeds personal orgs for all user IDs commonly used across the test suite.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	db := openTestDB(t)
	e := echo.New()
	api := e.Group("/api/v1")

	// Apply test auth middleware that injects apikit.AuthInfo from X-Test-Auth header.
	api.Use(testAuthMiddleware())

	// Register workspace routes.
	if err := RegisterRoutes(api, db); err != nil {
		t.Fatalf("RegisterRoutes() returned error: %v", err)
	}

	env := &testEnv{echo: e, db: db}

	// Seed personal orgs for all common test user IDs so that workspace
	// creation without explicit org_id succeeds (04-REQ-8.1).
	seedDefaultPersonalOrgs(t, db)

	return env
}

// testAuthMiddleware returns middleware that reads apikit.AuthInfo from the
// X-Test-Auth JSON header and injects it via apikit.SetAuthInfo. If absent,
// auth context remains unset (simulates an unauthenticated request).
func testAuthMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			authHeader := c.Request().Header.Get("X-Test-Auth")
			if authHeader != "" {
				var info apikit.AuthInfo
				if err := json.Unmarshal([]byte(authHeader), &info); err != nil {
					return echo.NewHTTPError(http.StatusBadRequest, "invalid X-Test-Auth header")
				}
				apikit.SetAuthInfo(c, &info)
			}
			return next(c)
		}
	}
}

// doRequest performs an HTTP request against the test server with optional auth.
func (env *testEnv) doRequest(t *testing.T, method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
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

// adminAuth returns an apikit.AuthInfo representing an admin token.
func adminAuth() *apikit.AuthInfo {
	return &apikit.AuthInfo{
		CredentialType: "admin_token",
	}
}

// userAuth returns an apikit.AuthInfo representing a user API key.
func userAuth(userID string) *apikit.AuthInfo {
	return &apikit.AuthInfo{
		CredentialType: "api_key",
		UserID:         userID,
	}
}

// patAuth returns an apikit.AuthInfo representing a PAT with the given permission scopes.
func patAuth(userID string, permissions ...string) *apikit.AuthInfo {
	return &apikit.AuthInfo{
		CredentialType: "pat",
		UserID:         userID,
		Permissions:    permissions,
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

// seedDefaultPersonalOrgs inserts personal orgs for all user IDs commonly
// used across the workspace test suite. This centralised setup avoids
// modifying each individual test after spec 04 made org_id auto-default
// mandatory. Tests that explicitly test "no personal org" behaviour use
// newAutoOrgTestEnv instead and seed their own orgs as needed.
func seedDefaultPersonalOrgs(t *testing.T, db *sql.DB) {
	t.Helper()
	// Comprehensive list of user IDs used in workspace tests that create
	// workspaces without explicit org_id.
	userIDs := []string{
		"alice-id", "alice-user-id", "u1-id", "u2-id", "user-1",
		"user-spec05", "prop-u1", "prop-u3", "prop-u4",
		"prop-user-1", "prop-user-2", "user-defaults",
		"empty-user-id", "del-pat-user", "scope-user",
		"user-a", "user-b", "user-c",
		"clone-user-001",
		// Dynamically generated user IDs used in property tests.
		"user-0", "user-2", "user-3", "user-4",
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, uid := range userIDs {
		orgID := "personal-org-" + uid
		name := "Personal " + uid
		slug := "personal-" + uid
		_, err := db.Exec(
			`INSERT INTO orgs (id, name, slug, owner_id, status, created_at, updated_at)
			 VALUES (?, ?, ?, ?, 'active', ?, ?)`,
			orgID, name, slug, uid, now, now,
		)
		if err != nil {
			t.Fatalf("seedDefaultPersonalOrgs(%q): %v", uid, err)
		}
		_, err = db.Exec(
			`INSERT INTO org_members (org_id, user_id, created_at) VALUES (?, ?, ?)`,
			orgID, uid, now,
		)
		if err != nil {
			t.Fatalf("seedDefaultPersonalOrgs membership(%q): %v", uid, err)
		}
	}
}

// seedPersonalOrg creates a personal org owned by the given user and adds
// them as a member. The org's id is "personal-org-<userID>" and the slug is
// "personal-<userID>". This is needed since spec 04 requires every workspace
// to have an org_id: when no org_id is provided in the create request, the
// handler auto-populates it from the user's personal org.
func (env *testEnv) seedPersonalOrg(t *testing.T, userID string) {
	t.Helper()
	orgID := "personal-org-" + userID
	name := "Personal " + userID
	slug := "personal-" + userID
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := env.db.Exec(
		`INSERT INTO orgs (id, name, slug, owner_id, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'active', ?, ?)`,
		orgID, name, slug, userID, now, now,
	)
	if err != nil {
		t.Fatalf("seedPersonalOrg(%q) returned error: %v", userID, err)
	}
	env.seedOrgMember(t, orgID, userID)
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
	HubURL      *string `json:"hub_url"`
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
