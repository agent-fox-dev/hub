// Package testutil holds the test helpers the hub's packages share: a database
// with apikit's schema, apikit credentials, an Echo instance with a test
// authentication middleware, request and response helpers, and seeded users,
// organizations and workspace rows. It imports no hub package, so every
// package's tests can use it.
package testutil

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// AuthHeader is the header the test middleware reads a JSON apikit.AuthInfo
// from. A request without it is unauthenticated.
const AuthHeader = "X-Test-Auth"

// OpenDatabase opens a SQLite database in a temporary directory with apikit's
// schema (users, orgs, org_members, api_keys, pats, admin_config) applied, as
// the hub has at start. The caller applies the schemas of the packages under
// test (workspace.InitSchema, secrets.InitSchema, ...). It is closed when the
// test ends.
func OpenDatabase(t *testing.T) *apikit.DB {
	t.Helper()
	database, err := apikit.OpenDatabase(filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatalf("apikit.OpenDatabase: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

// OpenDB is OpenDatabase's *sql.DB.
func OpenDB(t *testing.T) *sql.DB {
	t.Helper()
	return OpenDatabase(t).SqlDB
}

// Server opens a real apikit server on a database of its own (see
// OpenDatabase). The caller mounts the routes under test with the package's
// mount function (workspace.MountWorkspaceHandlers and the like), which also
// mounts apikit's auth middleware and its user and token routes; a test that
// needs only apikit's routes calls server.MountHandlers(database). Requests
// go through Bearer (apikit's credentials), not through the test middleware.
func Server(t *testing.T) (*apikit.Server, *apikit.DB) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_DATA_HOME", dir)
	config := "[server]\nport = 0\n\n[database]\npath = \"" + filepath.Join(dir, "hub.db") + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	cfg, err := apikit.LoadConfig()
	if err != nil {
		t.Fatalf("apikit.LoadConfig: %v", err)
	}
	database, err := apikit.OpenDatabase(cfg.Database.Path)
	if err != nil {
		t.Fatalf("apikit.OpenDatabase: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return apikit.NewServer(cfg, func() error { return nil }), database
}

// Admin is an admin token's credential.
func Admin() *apikit.AuthInfo {
	return &apikit.AuthInfo{CredentialType: "admin_token"}
}

// User is the API key credential of userID.
func User(userID string) *apikit.AuthInfo {
	return &apikit.AuthInfo{CredentialType: "api_key", UserID: userID}
}

// PAT is a personal access token of userID with the given scopes.
func PAT(userID string, scopes ...string) *apikit.AuthInfo {
	return &apikit.AuthInfo{CredentialType: "pat", UserID: userID, Permissions: scopes}
}

// BoundPAT is PAT with the token id a workspace binding names.
func BoundPAT(userID, tokenID string, scopes ...string) *apikit.AuthInfo {
	return &apikit.AuthInfo{CredentialType: "pat", UserID: userID, TokenID: tokenID, Permissions: scopes}
}

// AuthMiddleware injects the apikit.AuthInfo a request carries as JSON in
// AuthHeader, as apikit's own middleware would after validating a credential;
// a request without the header stays unauthenticated.
func AuthMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if raw := c.Request().Header.Get(AuthHeader); raw != "" {
				var info apikit.AuthInfo
				if err := json.Unmarshal([]byte(raw), &info); err != nil {
					return echo.NewHTTPError(http.StatusBadRequest, "invalid "+AuthHeader+" header")
				}
				apikit.SetAuthInfo(c, &info)
			}
			return next(c)
		}
	}
}

// API is an Echo instance with the /api/v1 group the hub's packages mount
// their routes on, behind AuthMiddleware.
func API() (*echo.Echo, *echo.Group) {
	e := echo.New()
	api := e.Group("/api/v1")
	api.Use(AuthMiddleware())
	return e, api
}

// Request serves one request on e as auth (nil for unauthenticated). A
// non-empty body is sent as JSON.
func Request(t *testing.T, e *echo.Echo, method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	t.Helper()
	req := newRequest(method, path, body)
	if auth != nil {
		raw, err := json.Marshal(auth)
		if err != nil {
			t.Fatalf("marshal auth: %v", err)
		}
		req.Header.Set(AuthHeader, string(raw))
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// Bearer serves one request on e with token as the Bearer credential (empty
// for none), for a server from Server.
func Bearer(e *echo.Echo, method, path, body, token string) *httptest.ResponseRecorder {
	req := newRequest(method, path, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func newRequest(method, path, body string) *http.Request {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	return req
}

// ErrorEnvelope is apikit's error body.
type ErrorEnvelope struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	ErrorType string `json:"error_type"`
}

// Error decodes the error envelope of a response.
func Error(t *testing.T, rec *httptest.ResponseRecorder) ErrorEnvelope {
	t.Helper()
	var body struct {
		Error ErrorEnvelope `json:"error"`
	}
	Decode(t, rec, &body)
	return body.Error
}

// Decode decodes the JSON body of a response into v.
func Decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decoding response %d: %v: %s", rec.Code, err, rec.Body.String())
	}
}

// Object decodes the JSON object body of a response.
func Object(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	Decode(t, rec, &m)
	return m
}

// Objects decodes the JSON array-of-objects body of a response.
func Objects(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var list []map[string]any
	Decode(t, rec, &list)
	return list
}

// Count runs a COUNT query and returns its value.
func Count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// Exec runs a statement and fails the test on error.
func Exec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// SeedUser inserts an active user with id and returns it.
func SeedUser(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	now := apikit.NowUTC()
	Exec(t, db, `INSERT INTO users (id, username, email, full_name, role, status, provider, provider_id, created_at, updated_at)
		VALUES (?, ?, ?, '', 'user', 'active', 'github', ?, ?, ?)`, id, id, id+"@example.com", "p-"+id, now, now)
	return id
}

// APIKey creates an API key of userID (an existing user) and returns the
// full key to send as Bearer.
func APIKey(t *testing.T, db *sql.DB, userID string) string {
	t.Helper()
	key, err := apikit.GenerateAPIKey(db, userID, 0, echo.New().Logger)
	if err != nil {
		t.Fatalf("apikit.GenerateAPIKey(%s): %v", userID, err)
	}
	return key.FullKey
}

// MintPAT creates a PAT with the given scopes through apikit's
// POST /api/v1/user/tokens, as the user whose API key is apiKey, and returns
// the token and its id.
func MintPAT(t *testing.T, e *echo.Echo, apiKey string, scopes ...string) (token, tokenID string) {
	t.Helper()
	perms := []byte("[]")
	if scopes != nil {
		perms, _ = json.Marshal(scopes)
	}
	rec := Bearer(e, http.MethodPost, "/api/v1/user/tokens", `{"name":"t","permissions":`+string(perms)+`}`, apiKey)
	if rec.Code != http.StatusCreated {
		t.Fatalf("minting a PAT with %v: status %d: %s", scopes, rec.Code, rec.Body)
	}
	var created struct {
		Token   string `json:"token"`
		TokenID string `json:"token_id"`
	}
	Decode(t, rec, &created)
	if created.Token == "" || created.TokenID == "" {
		t.Fatalf("PAT response without token or id: %s", rec.Body)
	}
	return created.Token, created.TokenID
}

// SeedOrg inserts an active organization with id, slug and owner (which may
// be empty), and returns id.
func SeedOrg(t *testing.T, db *sql.DB, id, slug, ownerID string) string {
	t.Helper()
	now := apikit.NowUTC()
	var owner any
	if ownerID != "" {
		owner = ownerID
	}
	Exec(t, db, `INSERT INTO orgs (id, name, slug, url, owner_id, status, created_at, updated_at)
		VALUES (?, ?, ?, '', ?, 'active', ?, ?)`, id, slug, slug, owner, now, now)
	return id
}

// SeedOrgMember makes userID a member of orgID.
func SeedOrgMember(t *testing.T, db *sql.DB, orgID, userID string) {
	t.Helper()
	Exec(t, db, `INSERT INTO org_members (org_id, user_id, created_at) VALUES (?, ?, ?)`, orgID, userID, apikit.NowUTC())
}

// SeedPersonalOrg inserts the personal organization of userID (the org a
// workspace created without org_id is placed in) and returns its id.
func SeedPersonalOrg(t *testing.T, db *sql.DB, userID string) string {
	t.Helper()
	return SeedOrg(t, db, "personal-org-"+userID, "personal-"+userID, userID)
}

// Workspace is a row of the workspaces table to seed. Slug and OwnerID are
// required; GitURL defaults to https://github.com/org/<slug>, Status to ready,
// WorkspaceMode to standard. The table must exist (workspace.InitSchema).
type Workspace struct {
	Slug              string
	OwnerID           string
	OrgID             string
	Status            string
	GitURL            string
	Branch            string
	WorkspaceMode     string
	UpstreamURL       string
	IntegrationBranch string
	HeadSHA           string
}

// SeedWorkspace inserts ws and returns its slug.
func SeedWorkspace(t *testing.T, db *sql.DB, ws Workspace) string {
	t.Helper()
	if ws.GitURL == "" {
		ws.GitURL = "https://github.com/org/" + ws.Slug
	}
	if ws.Status == "" {
		ws.Status = "ready"
	}
	if ws.WorkspaceMode == "" {
		ws.WorkspaceMode = "standard"
	}
	now := apikit.NowUTC()
	Exec(t, db, `INSERT INTO workspaces (slug, git_url, branch, owner_id, org_id, status, head_sha, created_at, updated_at, workspace_mode, upstream_url, integration_branch, settings)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '{}')`,
		ws.Slug, ws.GitURL, nullable(ws.Branch), ws.OwnerID, nullable(ws.OrgID), ws.Status, nullable(ws.HeadSHA), now, now,
		ws.WorkspaceMode, nullable(ws.UpstreamURL), nullable(ws.IntegrationBranch))
	return ws.Slug
}

// BindToken binds tokenID to the workspace slug.
func BindToken(t *testing.T, db *sql.DB, tokenID, slug string) {
	t.Helper()
	Exec(t, db, `INSERT INTO token_workspace_bindings (token_id, slug, created_at) VALUES (?, ?, ?)`, tokenID, slug, apikit.NowUTC())
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
