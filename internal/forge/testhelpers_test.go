package forge

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
	_ "modernc.org/sqlite"

	"github.com/agent-fox-dev/hub/internal/secrets"
)

const (
	ownerID = "user-owner"
	otherID = "user-other"
)

// ---------------------------------------------------------------------------
// Credentials
// ---------------------------------------------------------------------------

func apiKeyAuth() *apikit.AuthInfo {
	return &apikit.AuthInfo{UserID: ownerID, CredentialType: "api_key"}
}

func adminAuth() *apikit.AuthInfo {
	return &apikit.AuthInfo{UserID: otherID, CredentialType: "admin_token"}
}

func patAuth(perms ...string) *apikit.AuthInfo {
	return &apikit.AuthInfo{UserID: ownerID, CredentialType: "pat", Permissions: perms}
}

// ---------------------------------------------------------------------------
// Database
// ---------------------------------------------------------------------------

// openTestDB opens an in-memory SQLite database.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory database: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

// createWorkspacesTable creates the workspaces table with the columns the forge
// routes and wsaccess read. workspace_mode is left nullable on purpose: the routes
// treat a NULL mode as a standard workspace.
func createWorkspacesTable(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS workspaces (
			slug           TEXT PRIMARY KEY,
			git_url        TEXT NOT NULL,
			owner_id       TEXT NOT NULL,
			status         TEXT NOT NULL DEFAULT 'active',
			clone_status   TEXT NOT NULL DEFAULT 'pending',
			workspace_mode TEXT,
			upstream_url   TEXT
		)`)
	if err != nil {
		t.Fatalf("create workspaces table: %v", err)
	}
}

// workspaceRow is a row of the workspaces table; a nil mode or upstream is NULL.
type workspaceRow struct {
	slug        string
	owner       string
	gitURL      string
	mode        *string
	upstream    *string
	status      string
	cloneStatus string
}

func ptr(s string) *string { return &s }

// addWorkspace inserts a workspace: owned by ownerID, active and cloned, a standard
// workspace on github.com/acme/widgets unless a modifier says otherwise.
func addWorkspace(t *testing.T, db *sql.DB, slug string, mods ...func(*workspaceRow)) {
	t.Helper()
	row := workspaceRow{
		slug: slug, owner: ownerID, gitURL: "https://github.com/acme/widgets",
		mode: ptr("standard"), status: "active", cloneStatus: "ready",
	}
	for _, m := range mods {
		m(&row)
	}
	_, err := db.Exec(
		`INSERT INTO workspaces (slug, git_url, owner_id, status, clone_status, workspace_mode, upstream_url)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		row.slug, row.gitURL, row.owner, row.status, row.cloneStatus, row.mode, row.upstream)
	if err != nil {
		t.Fatalf("insert workspace %s: %v", slug, err)
	}
}

// carryPatch makes the workspace a carry_patch fork of upstream.
func carryPatch(upstream string) func(*workspaceRow) {
	return func(r *workspaceRow) {
		r.mode = ptr("carry_patch")
		r.upstream = ptr(upstream)
	}
}

// ---------------------------------------------------------------------------
// The forge stub
// ---------------------------------------------------------------------------

// recordedRequest is a request the forge stub received.
type recordedRequest struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   string
}

// forgeStub is a TLS server standing in for GitHub or GitLab. It records every
// request it receives and answers with its handler, 200 and an empty JSON object
// by default.
type forgeStub struct {
	*httptest.Server

	mu       sync.Mutex
	requests []recordedRequest
	handler  http.HandlerFunc
}

func newForgeStub(t *testing.T) *forgeStub {
	t.Helper()
	s := &forgeStub{}
	s.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "{}")
	}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, recordedRequest{
			Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
			Header: r.Header.Clone(), Body: string(body),
		})
		h := s.handler
		s.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

// setHandler replaces what the stub answers.
func (s *forgeStub) setHandler(h http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handler = h
}

// received returns the requests recorded so far.
func (s *forgeStub) received() []recordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recordedRequest(nil), s.requests...)
}

func (s *forgeStub) count() int { return len(s.received()) }

// ---------------------------------------------------------------------------
// The test environment
// ---------------------------------------------------------------------------

// env is the forge routes mounted on a router, with a workspaces table, a stub for
// the forge and resolvers and a variable lookup that count their calls.
type env struct {
	t    *testing.T
	db   *sql.DB
	echo *echo.Echo
	stub *forgeStub

	originAuth   transport.AuthMethod
	upstreamAuth transport.AuthMethod
	originErr    error
	upstreamErr  error

	mu     sync.Mutex
	vars   map[string]string // "slug/KEY" -> value
	varErr error

	originCalls   atomic.Int64
	upstreamCalls atomic.Int64
	varCalls      atomic.Int64
}

// testAuthMiddleware injects the AuthInfo carried as JSON in the X-Test-Auth header.
func testAuthMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if h := c.Request().Header.Get("X-Test-Auth"); h != "" {
				var info apikit.AuthInfo
				if err := json.Unmarshal([]byte(h), &info); err != nil {
					return echo.NewHTTPError(http.StatusBadRequest, "invalid X-Test-Auth header")
				}
				apikit.SetAuthInfo(c, &info)
			}
			return next(c)
		}
	}
}

// newEnv mounts the forge routes. The modifiers change the Config before it is used.
func newEnv(t *testing.T, mods ...func(*Config)) *env {
	t.Helper()
	e := &env{t: t, db: openTestDB(t), stub: newForgeStub(t), vars: map[string]string{}}
	createWorkspacesTable(t, e.db)
	e.originAuth = &githttp.BasicAuth{Username: "x-token-auth", Password: "origin-token"}

	cfg := Config{
		DB: e.db,
		ResolveOriginAuth: func(string) (transport.AuthMethod, error) {
			e.originCalls.Add(1)
			return e.originAuth, e.originErr
		},
		ResolveUpstreamAuth: func(string) (transport.AuthMethod, error) {
			e.upstreamCalls.Add(1)
			return e.upstreamAuth, e.upstreamErr
		},
		GetVariable: func(ownerType, owner, key string) (string, error) {
			e.varCalls.Add(1)
			e.mu.Lock()
			defer e.mu.Unlock()
			if e.varErr != nil {
				return "", e.varErr
			}
			if v, ok := e.vars[owner+"/"+key]; ok {
				return v, nil
			}
			return "", &secrets.NotFoundError{Key: key}
		},
		Transport: e.stub.Client().Transport,
	}
	for _, m := range mods {
		m(&cfg)
	}

	e.echo = echo.New()
	e.echo.HTTPErrorHandler = apikit.HTTPErrorHandler
	api := e.echo.Group("/api/v1")
	api.Use(testAuthMiddleware())
	RegisterRoutes(api, cfg)
	return e
}

// setVar sets a workspace variable the Config's GetVariable will find.
func (e *env) setVar(slug, key, value string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.vars[slug+"/"+key] = value
}

// get calls GET /api/v1<path> with the credential, or without one when auth is nil.
func (e *env) get(path string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1"+path, nil)
	if auth != nil {
		b, err := json.Marshal(auth)
		if err != nil {
			e.t.Fatalf("marshal auth: %v", err)
		}
		req.Header.Set("X-Test-Auth", string(b))
	}
	rec := httptest.NewRecorder()
	e.echo.ServeHTTP(rec, req)
	return rec
}

// apiError is the error envelope of the hub's API.
type apiError struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	ErrorType string `json:"error_type"`
}

// decodeError decodes the error envelope of a response; a body that is none stops
// the test.
func decodeError(t *testing.T, rec *httptest.ResponseRecorder) apiError {
	t.Helper()
	var env struct {
		Error *apiError `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Error == nil {
		t.Fatalf("status %d: the body is not an error envelope: %v\n%s", rec.Code, err, rec.Body.String())
	}
	return *env.Error
}

// forgeRoute is one of the ten forge routes; path holds %s where the slug goes and
// a sample number where a number is taken.
type forgeRoute struct {
	name string
	path string
	// takesTarget is false for the diagnostic, which does not take ?target=.
	takesTarget bool
}

var forgeRoutes = []forgeRoute{
	{"forge", "/workspaces/%s/forge", false},
	{"repository", "/workspaces/%s/forge/repository", true},
	{"issues", "/workspaces/%s/issues", true},
	{"issue", "/workspaces/%s/issues/1", true},
	{"comments", "/workspaces/%s/issues/1/comments", true},
	{"pull", "/workspaces/%s/pulls/1", true},
	{"state", "/workspaces/%s/pulls/1/state", true},
	{"files", "/workspaces/%s/pulls/1/files", true},
	{"checks", "/workspaces/%s/pulls/1/checks", true},
	{"reviews", "/workspaces/%s/pulls/1/reviews", true},
}

func (r forgeRoute) url(slug string) string {
	return strings.Replace(r.path, "%s", slug, 1)
}
