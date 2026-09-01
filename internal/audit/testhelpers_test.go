package audit

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// auditTestEnv holds a test HTTP server, DuckDB database, and optional SQLite
// database for integration tests.
type auditTestEnv struct {
	echo     *echo.Echo
	duckDB   *sql.DB
	sqliteDB *sql.DB
	store    Store
}

// newAuditTestEnv creates an Echo server with session routes mounted for
// testing. Uses a real DuckDB database in t.TempDir() initialized with
// the agent_sessions and token_usage schema.
func newAuditTestEnv(t *testing.T) *auditTestEnv {
	t.Helper()
	duckDB := openTestAuditDB(t)
	store := NewStore(duckDB)

	e := echo.New()
	e.HTTPErrorHandler = apikit.HTTPErrorHandler
	api := e.Group("/api/v1")

	// Apply test auth middleware.
	api.Use(testAuthMiddleware())

	// Register session routes.
	RegisterSessionRoutes(api, store, nil)

	return &auditTestEnv{
		echo:   e,
		duckDB: duckDB,
		store:  store,
	}
}

// newAuditTestEnvWithSQLite creates an environment that includes a SQLite
// database for workspace access checks.
func newAuditTestEnvWithSQLite(t *testing.T) *auditTestEnv {
	t.Helper()
	duckDB := openTestAuditDB(t)
	sqliteDB := openTestSQLiteDB(t)
	store := NewStore(duckDB)

	e := echo.New()
	e.HTTPErrorHandler = apikit.HTTPErrorHandler
	api := e.Group("/api/v1")

	api.Use(testAuthMiddleware())

	RegisterSessionRoutes(api, store, sqliteDB)

	return &auditTestEnv{
		echo:     e,
		duckDB:   duckDB,
		sqliteDB: sqliteDB,
		store:    store,
	}
}

// openTestAuditDB opens a DuckDB database in a temporary directory
// and initializes the schema.
func openTestAuditDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "testdata", "audit.duckdb")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatalf("OpenDB(%q): %v", path, err)
	}
	t.Cleanup(func() { db.Close() })

	if err := InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	return db
}

// openTestAuditDBWithSchema opens a DuckDB database and initializes the
// schema. Returns the *sql.DB for direct queries.
func openTestAuditDBWithSchema(t *testing.T) *sql.DB {
	t.Helper()
	return openTestAuditDB(t)
}

// tableExists checks whether a table exists in the DuckDB database.
func tableExists(t *testing.T, db *sql.DB, tableName string) bool {
	t.Helper()
	var count int
	err := db.QueryRow(
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_name = ?",
		tableName,
	).Scan(&count)
	if err != nil {
		t.Fatalf("query for table %s: %v", tableName, err)
	}
	return count > 0
}

// allNineTables returns the names of all nine audit tables.
func allNineTables() []string {
	return []string{
		"agent_audit_events",
		"hub_audit_events",
		"session_outcomes",
		"tool_calls",
		"tool_errors",
		"agent_traces",
		"postmortems",
		"agent_sessions",
		"token_usage",
	}
}

// openTestSQLiteDB opens an in-memory SQLite database with workspaces table.
func openTestSQLiteDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })

	// Create a minimal workspaces table for access checks.
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS workspaces (
		slug     TEXT PRIMARY KEY,
		owner_id TEXT NOT NULL,
		status   TEXT NOT NULL DEFAULT 'active'
	)`)
	if err != nil {
		t.Fatalf("create workspaces table: %v", err)
	}
	return db
}

// testAuthMiddleware returns middleware that reads apikit.AuthInfo from the
// X-Test-Auth JSON header and injects it via apikit.SetAuthInfo.
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
func (env *auditTestEnv) doRequest(t *testing.T, method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
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
		UserID:         "admin",
	}
}

// apiKeyAuth returns an apikit.AuthInfo representing an API key credential.
// The userID parameter maps to credential_id in session records.
func apiKeyAuth(userID string) *apikit.AuthInfo {
	return &apikit.AuthInfo{
		CredentialType: "api_key",
		UserID:         userID,
	}
}

// patAuth returns an apikit.AuthInfo representing a PAT with the given scopes.
func patAuth(userID string, permissions ...string) *apikit.AuthInfo {
	return &apikit.AuthInfo{
		CredentialType: "pat",
		UserID:         userID,
		Permissions:    permissions,
	}
}

// seedSession inserts a session directly into DuckDB for test setup.
func (env *auditTestEnv) seedSession(t *testing.T, s *Session) {
	t.Helper()
	if s.StartedAt == "" {
		s.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := env.duckDB.Exec(
		`INSERT INTO agent_sessions (id, run_id, workspace_slug, node_id, archetype,
			status, started_at, model, credential_id, credential_type, error_message,
			ended_at, duration_ms, cache_creation_input_tokens, metadata, ingested_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.RunID, s.WorkspaceSlug, s.NodeID, s.Archetype,
		s.Status, s.StartedAt, s.Model, s.CredentialID, s.CredentialType,
		nilIfEmpty(s.ErrorMessage), s.EndedAt, s.DurationMs,
		s.CacheCreationInputTokens, nil, now,
	)
	if err != nil {
		t.Fatalf("seedSession(%q): %v", s.ID, err)
	}
}

// seedTokenUsage inserts a token_usage record directly into DuckDB.
func (env *auditTestEnv) seedTokenUsage(t *testing.T, u *TokenUsage) {
	t.Helper()
	if u.ReportedAt == "" {
		u.ReportedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := env.duckDB.Exec(
		`INSERT INTO token_usage (id, session_id, workspace_slug, model,
			input_tokens, output_tokens, cache_read_tokens, reported_at, ingested_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.SessionID, u.WorkspaceSlug, u.Model,
		u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.ReportedAt, now,
	)
	if err != nil {
		t.Fatalf("seedTokenUsage(%q): %v", u.ID, err)
	}
}

// seedWorkspace inserts a workspace into the SQLite workspaces table.
func (env *auditTestEnv) seedWorkspace(t *testing.T, slug, ownerID string) {
	t.Helper()
	if env.sqliteDB == nil {
		t.Fatal("seedWorkspace requires SQLite DB; use newAuditTestEnvWithSQLite")
	}
	_, err := env.sqliteDB.Exec(
		`INSERT INTO workspaces (slug, owner_id) VALUES (?, ?)`,
		slug, ownerID,
	)
	if err != nil {
		t.Fatalf("seedWorkspace(%q): %v", slug, err)
	}
}

// countSessions returns the number of sessions in agent_sessions with the
// given ID.
func (env *auditTestEnv) countSessions(t *testing.T, id string) int {
	t.Helper()
	var count int
	err := env.duckDB.QueryRow(
		"SELECT COUNT(*) FROM agent_sessions WHERE id = ?", id,
	).Scan(&count)
	if err != nil {
		t.Fatalf("countSessions(%q): %v", id, err)
	}
	return count
}

// getSessionStatus returns the status of a session from DuckDB.
func (env *auditTestEnv) getSessionStatus(t *testing.T, id string) string {
	t.Helper()
	var status string
	err := env.duckDB.QueryRow(
		"SELECT status FROM agent_sessions WHERE id = ?", id,
	).Scan(&status)
	if err != nil {
		t.Fatalf("getSessionStatus(%q): %v", id, err)
	}
	return status
}

// parseSessionJSON parses the response body as a Session.
func parseSessionJSON(t *testing.T, rec *httptest.ResponseRecorder) Session {
	t.Helper()
	var s Session
	if err := json.NewDecoder(rec.Body).Decode(&s); err != nil {
		t.Fatalf("failed to decode session response: %v\nbody: %s", err, rec.Body.String())
	}
	return s
}

// parseSessionListJSON parses the response body as a SessionListResponse.
func parseSessionListJSON(t *testing.T, rec *httptest.ResponseRecorder) SessionListResponse {
	t.Helper()
	var resp SessionListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode session list response: %v\nbody: %s", err, rec.Body.String())
	}
	return resp
}

// parseUsageJSON parses the response body as a TokenUsage.
func parseUsageJSON(t *testing.T, rec *httptest.ResponseRecorder) TokenUsage {
	t.Helper()
	var u TokenUsage
	if err := json.NewDecoder(rec.Body).Decode(&u); err != nil {
		t.Fatalf("failed to decode usage response: %v\nbody: %s", err, rec.Body.String())
	}
	return u
}

// parseUsageListJSON parses the response body as a UsageListResponse.
func parseUsageListJSON(t *testing.T, rec *httptest.ResponseRecorder) UsageListResponse {
	t.Helper()
	var resp UsageListResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode usage list response: %v\nbody: %s", err, rec.Body.String())
	}
	return resp
}

// parseErrorJSON parses the apikit error envelope from the response body.
type testErrorEnvelope struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func parseErrorJSON(t *testing.T, rec *httptest.ResponseRecorder) testErrorEnvelope {
	t.Helper()
	var resp testErrorEnvelope
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode error response: %v\nbody: %s", err, rec.Body.String())
	}
	return resp
}

// nilIfEmpty returns nil if s is empty, otherwise returns s.
func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// sessionID generates a sequential test session ID.
func sessionID(n int) string {
	return fmt.Sprintf("sess-%03d", n)
}

// usageID generates a sequential test usage record ID.
func usageID(n int) string {
	return fmt.Sprintf("usage-%03d", n)
}
