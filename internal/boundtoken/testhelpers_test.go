package boundtoken

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
	_ "modernc.org/sqlite"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// openTestDB opens a single-connection in-memory database with a minimal
// workspaces table, apikit's pats table and the binding table.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })
	for _, stmt := range []string{
		`CREATE TABLE workspaces (
			slug TEXT PRIMARY KEY,
			owner_id TEXT NOT NULL,
			org_id TEXT,
			status TEXT NOT NULL DEFAULT 'active',
			clone_status TEXT NOT NULL DEFAULT 'ready'
		)`,
		`CREATE TABLE pats (
			token_id TEXT NOT NULL PRIMARY KEY,
			user_id TEXT NOT NULL,
			name TEXT NOT NULL,
			secret_hash TEXT NOT NULL,
			permissions TEXT NOT NULL,
			expires_days INTEGER NOT NULL,
			expires_at TEXT,
			revoked_at TEXT,
			created_at TEXT NOT NULL
		)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := wsaccess.InitBindingSchema(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedWorkspace(t *testing.T, db *sql.DB, slug, owner, status string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO workspaces (slug, owner_id, status) VALUES (?, ?, ?)`, slug, owner, status); err != nil {
		t.Fatal(err)
	}
}

// patRow is a pats row to seed; empty optional fields are NULL.
type patRow struct {
	id, user, name, perms, expiresAt, revokedAt, createdAt string
}

func seedPAT(t *testing.T, db *sql.DB, p patRow) {
	t.Helper()
	null := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	if p.perms == "" {
		p.perms = "[]"
	}
	if p.createdAt == "" {
		p.createdAt = apikit.NowUTC()
	}
	if p.user == "" {
		p.user = "u1"
	}
	if _, err := db.Exec(`INSERT INTO pats (token_id, user_id, name, secret_hash, permissions, expires_days, expires_at, revoked_at, created_at)
		VALUES (?, ?, ?, ?, ?, 90, ?, ?, ?)`, p.id, p.user, p.name, "hash-"+p.id, p.perms, null(p.expiresAt), null(p.revokedAt), p.createdAt); err != nil {
		t.Fatalf("seeding pats row %s: %v", p.id, err)
	}
}

func bind(t *testing.T, db *sql.DB, tokenID, slug string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO token_workspace_bindings (token_id, slug, created_at) VALUES (?, ?, ?)`, tokenID, slug, apikit.NowUTC()); err != nil {
		t.Fatalf("binding %s to %s: %v", tokenID, slug, err)
	}
}

// testAuthMiddleware injects the AuthInfo carried as JSON in X-Test-Auth.
func testAuthMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if raw := c.Request().Header.Get("X-Test-Auth"); raw != "" {
				var info apikit.AuthInfo
				if err := json.Unmarshal([]byte(raw), &info); err != nil {
					return echo.NewHTTPError(http.StatusBadRequest, "invalid X-Test-Auth header")
				}
				apikit.SetAuthInfo(c, &info)
			}
			return next(c)
		}
	}
}

// testEnv is the bound-token routes on an /api/v1 group behind the test auth
// middleware.
type testEnv struct {
	e  *echo.Echo
	db *sql.DB
}

func newTestEnv(t *testing.T, cfg Config) *testEnv {
	t.Helper()
	if cfg.DB == nil {
		cfg.DB = openTestDB(t)
	}
	e := echo.New()
	e.HTTPErrorHandler = apikit.HTTPErrorHandler
	api := e.Group("/api/v1")
	api.Use(testAuthMiddleware())
	RegisterRoutes(api, cfg)
	return &testEnv{e: e, db: cfg.DB}
}

func (env *testEnv) do(method, path string, auth *apikit.AuthInfo, body string, header http.Header) *httptest.ResponseRecorder {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	for k, v := range header {
		req.Header[k] = v
	}
	if body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != nil {
		raw, _ := json.Marshal(auth)
		req.Header.Set("X-Test-Auth", string(raw))
	}
	rec := httptest.NewRecorder()
	env.e.ServeHTTP(rec, req)
	return rec
}

type errorBody struct {
	Error struct {
		Code      int    `json:"code"`
		Message   string `json:"message"`
		ErrorType string `json:"error_type"`
	} `json:"error"`
}

func parseError(t *testing.T, rec *httptest.ResponseRecorder) errorBody {
	t.Helper()
	var b errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("not an error envelope: %v: %s", err, rec.Body)
	}
	return b
}

func apiKey(user string) *apikit.AuthInfo {
	return &apikit.AuthInfo{CredentialType: "api_key", UserID: user}
}

func admin() *apikit.AuthInfo { return &apikit.AuthInfo{CredentialType: "admin_token", Role: "admin"} }

func pat(user, tokenID string, scopes ...string) *apikit.AuthInfo {
	return &apikit.AuthInfo{CredentialType: "pat", UserID: user, TokenID: tokenID, Permissions: scopes}
}

// recordRefusals registers a refusal observer for the test and returns the
// reports it receives.
func recordRefusals(t *testing.T) *[]wsaccess.BoundRefusal {
	t.Helper()
	var got []wsaccess.BoundRefusal
	wsaccess.SetRefusalObserver(func(r wsaccess.BoundRefusal) { got = append(got, r) })
	t.Cleanup(func() { wsaccess.SetRefusalObserver(nil) })
	return &got
}

// recordingEmitter records the events it is given.
type recordingEmitter struct {
	mu   sync.Mutex
	evs  []audit.HubEvent
	ctxs []context.Context
	err  error
}

func (r *recordingEmitter) Emit(ctx context.Context, e audit.HubEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, e)
	r.ctxs = append(r.ctxs, ctx)
	return r.err
}

func (r *recordingEmitter) events() []audit.HubEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]audit.HubEvent(nil), r.evs...)
}
