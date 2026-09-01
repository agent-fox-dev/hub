package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
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
	db       *sql.DB
	sqliteDB *sql.DB
	store    Store
}

// newAuditTestEnv creates an Echo server with session and ingestion routes
// mounted for testing. Uses a real DuckDB database in t.TempDir() initialized
// with the agent_sessions and token_usage schema.
func newAuditTestEnv(t *testing.T) *auditTestEnv {
	t.Helper()
	duckDB := openTestAuditDB(t)
	// Ensure all audit tables exist for seeding and querying in handler tests.
	initHandlerTestSchema(t, duckDB)
	store := NewStore(duckDB)

	e := echo.New()
	e.HTTPErrorHandler = apikit.HTTPErrorHandler
	api := e.Group("/api/v1")

	// Apply test auth middleware.
	api.Use(testAuthMiddleware())

	// Register session routes.
	RegisterSessionRoutes(api, store, nil)

	// Register ingestion and query routes.
	RegisterRoutes(api, store, &nopEmitter{})

	return &auditTestEnv{
		echo: e,
		db:   duckDB,
		store: store,
	}
}

// newAuditTestEnvWithSQLite creates an environment that includes a SQLite
// database for workspace access checks.
func newAuditTestEnvWithSQLite(t *testing.T) *auditTestEnv {
	t.Helper()
	duckDB := openTestAuditDB(t)
	// Ensure all audit tables exist for seeding and querying in handler tests.
	initHandlerTestSchema(t, duckDB)
	sqliteDB := openTestSQLiteDB(t)
	store := NewStore(duckDB)

	e := echo.New()
	e.HTTPErrorHandler = apikit.HTTPErrorHandler
	api := e.Group("/api/v1")

	api.Use(testAuthMiddleware())

	RegisterSessionRoutes(api, store, sqliteDB)
	RegisterRoutes(api, store, &nopEmitter{})

	return &auditTestEnv{
		echo:     e,
		db:       duckDB,
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

// ---------------------------------------------------------------------------
// HTTP handler test infrastructure
// ---------------------------------------------------------------------------

// nopStore implements Store with no-op methods. Used when handler stubs
// don't call Store methods.
type nopStore struct{}

func (s *nopStore) InsertHubEvent(_ context.Context, _ HubEventRow) error {
	return nil
}

// nopEmitter implements Emitter with no-op methods.
type nopEmitter struct{}

func (e *nopEmitter) Emit(_ context.Context, _ HubEvent) error {
	return nil
}

// initHandlerTestSchema creates the DuckDB tables needed for handler tests.
// This is a test-only function separate from InitSchema so that handler
// tests do not depend on InitSchema being implemented.
func initHandlerTestSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	ddl := []string{
		`CREATE TABLE IF NOT EXISTS agent_audit_events (
			id VARCHAR PRIMARY KEY,
			run_id VARCHAR NOT NULL,
			workspace VARCHAR NOT NULL DEFAULT '',
			event_type VARCHAR NOT NULL,
			severity VARCHAR NOT NULL DEFAULT 'info',
			node_id VARCHAR NOT NULL DEFAULT '',
			session_id VARCHAR NOT NULL DEFAULT '',
			timestamp VARCHAR NOT NULL DEFAULT '',
			payload VARCHAR NOT NULL DEFAULT '{}',
			ingested_at VARCHAR NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS hub_audit_events (
			id VARCHAR PRIMARY KEY,
			event_type VARCHAR NOT NULL,
			actor_id VARCHAR NOT NULL DEFAULT '',
			actor_type VARCHAR NOT NULL DEFAULT '',
			resource_type VARCHAR NOT NULL DEFAULT '',
			resource_id VARCHAR NOT NULL DEFAULT '',
			action VARCHAR NOT NULL DEFAULT '',
			workspace VARCHAR NOT NULL DEFAULT '',
			metadata VARCHAR NOT NULL DEFAULT '{}',
			ingested_at VARCHAR NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS session_outcomes (
			id VARCHAR PRIMARY KEY,
			run_id VARCHAR NOT NULL,
			workspace VARCHAR NOT NULL DEFAULT '',
			session_id VARCHAR NOT NULL DEFAULT '',
			node_id VARCHAR NOT NULL DEFAULT '',
			status VARCHAR NOT NULL DEFAULT '',
			timestamp VARCHAR NOT NULL DEFAULT '',
			duration_ms INTEGER NOT NULL DEFAULT 0,
			token_usage VARCHAR NOT NULL DEFAULT '{}',
			ingested_at VARCHAR NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS tool_calls (
			id VARCHAR PRIMARY KEY,
			run_id VARCHAR NOT NULL,
			workspace VARCHAR NOT NULL DEFAULT '',
			tool_name VARCHAR NOT NULL DEFAULT '',
			node_id VARCHAR NOT NULL DEFAULT '',
			session_id VARCHAR NOT NULL DEFAULT '',
			timestamp VARCHAR NOT NULL DEFAULT '',
			duration_ms INTEGER NOT NULL DEFAULT 0,
			input VARCHAR NOT NULL DEFAULT '{}',
			output VARCHAR NOT NULL DEFAULT '{}',
			ingested_at VARCHAR NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS tool_errors (
			id VARCHAR PRIMARY KEY,
			run_id VARCHAR NOT NULL,
			workspace VARCHAR NOT NULL DEFAULT '',
			tool_name VARCHAR NOT NULL DEFAULT '',
			node_id VARCHAR NOT NULL DEFAULT '',
			session_id VARCHAR NOT NULL DEFAULT '',
			error_code VARCHAR NOT NULL DEFAULT '',
			error_msg VARCHAR NOT NULL DEFAULT '',
			timestamp VARCHAR NOT NULL DEFAULT '',
			ingested_at VARCHAR NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS agent_traces (
			id VARCHAR PRIMARY KEY,
			run_id VARCHAR NOT NULL,
			workspace VARCHAR NOT NULL DEFAULT '',
			event_type VARCHAR NOT NULL DEFAULT '',
			node_id VARCHAR NOT NULL DEFAULT '',
			session_id VARCHAR NOT NULL DEFAULT '',
			sequence INTEGER NOT NULL DEFAULT 0,
			timestamp VARCHAR NOT NULL DEFAULT '',
			data VARCHAR NOT NULL DEFAULT '{}',
			ingested_at VARCHAR NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS postmortems (
			run_id VARCHAR PRIMARY KEY,
			workspace VARCHAR NOT NULL DEFAULT '',
			schema_version INTEGER NOT NULL DEFAULT 1,
			run_status VARCHAR NOT NULL DEFAULT '',
			started_at VARCHAR NOT NULL DEFAULT '',
			completed_at VARCHAR NOT NULL DEFAULT '',
			task_summary VARCHAR NOT NULL DEFAULT '{}',
			cost_summary VARCHAR NOT NULL DEFAULT '{}',
			blocked_tasks VARCHAR NOT NULL DEFAULT '[]',
			session_history VARCHAR NOT NULL DEFAULT '[]',
			ingested_at VARCHAR NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS agent_sessions (
			id VARCHAR PRIMARY KEY,
			run_id VARCHAR NOT NULL DEFAULT '',
			workspace VARCHAR NOT NULL DEFAULT '',
			status VARCHAR NOT NULL DEFAULT '',
			created_at VARCHAR NOT NULL DEFAULT '',
			updated_at VARCHAR NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS token_usage (
			id VARCHAR PRIMARY KEY,
			session_id VARCHAR NOT NULL DEFAULT '',
			run_id VARCHAR NOT NULL DEFAULT '',
			workspace VARCHAR NOT NULL DEFAULT '',
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			total_cost_usd DOUBLE NOT NULL DEFAULT 0.0,
			recorded_at VARCHAR NOT NULL DEFAULT ''
		)`,
	}
	for _, stmt := range ddl {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("initHandlerTestSchema: %v\nSQL: %s", err, stmt)
		}
	}
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

// doJSON performs an HTTP request against the test server with a JSON body
// and optional auth. Returns the response recorder.
func (env *auditTestEnv) doJSON(t *testing.T, method, path, body string, auth *apikit.AuthInfo) *httptest.ResponseRecorder {
	return env.doRequest(t, method, path, body, auth)
}

// adminAuth returns an apikit.AuthInfo representing an admin token.
func adminAuth() *apikit.AuthInfo {
	return &apikit.AuthInfo{
		CredentialType: "admin_token",
		UserID:         "admin",
	}
}

// apiKeyAuth returns an apikit.AuthInfo representing an API key credential.
// The optional userID parameter maps to credential_id in session records.
// If omitted, defaults to "test-user-id".
func apiKeyAuth(userID ...string) *apikit.AuthInfo {
	uid := "test-user-id"
	if len(userID) > 0 {
		uid = userID[0]
	}
	return &apikit.AuthInfo{
		CredentialType: "api_key",
		UserID:         uid,
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
	_, err := env.db.Exec(
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
	_, err := env.db.Exec(
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
	err := env.db.QueryRow(
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
	err := env.db.QueryRow(
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

// apiErrorEnvelope represents the apikit JSON error response envelope.
type apiErrorEnvelope struct {
	Error struct {
		Code      int    `json:"code"`
		Message   string `json:"message"`
		ErrorType string `json:"error_type,omitempty"`
	} `json:"error"`
}

// parseJSON decodes the response body into the target struct.
func parseJSON(t *testing.T, rec *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.NewDecoder(rec.Body).Decode(target); err != nil {
		t.Fatalf("failed to decode JSON response: %v\nbody: %s", err, rec.Body.String())
	}
}

// parseJSONMap decodes the response body into a map[string]any.
func parseJSONMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	parseJSON(t, rec, &m)
	return m
}

// queryTableCount returns the number of rows in the given table.
func queryTableCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count)
	if err != nil {
		t.Fatalf("queryTableCount(%s): %v", table, err)
	}
	return count
}

// parseCostJSON parses the response body as a CostResponse.
func parseCostJSON(t *testing.T, rec *httptest.ResponseRecorder) CostResponse {
	t.Helper()
	var resp CostResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode cost response: %v\nbody: %s", err, rec.Body.String())
	}
	return resp
}

// newAuditTestEnvWithEmitter creates an environment that includes an Emitter
// for recording emitted hub audit events during force-close tests.
func newAuditTestEnvWithEmitter(t *testing.T) (*auditTestEnv, *testEmitter) {
	t.Helper()
	duckDB := openTestAuditDB(t)
	sqliteDB := openTestSQLiteDB(t)
	store := NewStore(duckDB)
	emitter := &testEmitter{}

	e := echo.New()
	e.HTTPErrorHandler = apikit.HTTPErrorHandler
	api := e.Group("/api/v1")

	api.Use(testAuthMiddleware())

	RegisterSessionRoutes(api, store, sqliteDB)

	return &auditTestEnv{
		echo:     e,
		db:       duckDB,
		sqliteDB: sqliteDB,
		store:    store,
	}, emitter
}

// testEmitter records emitted HubEvents for test assertions.
type testEmitter struct {
	events []HubEvent
}

func (te *testEmitter) Emit(_ context.Context, event HubEvent) error {
	te.events = append(te.events, event)
	return nil
}

// seedWorkspaceWithStatus inserts a workspace with a specific status.
func (env *auditTestEnv) seedWorkspaceWithStatus(t *testing.T, slug, ownerID, status string) {
	t.Helper()
	if env.sqliteDB == nil {
		t.Fatal("seedWorkspaceWithStatus requires SQLite DB; use newAuditTestEnvWithSQLite")
	}
	_, err := env.sqliteDB.Exec(
		`INSERT INTO workspaces (slug, owner_id, status) VALUES (?, ?, ?)`,
		slug, ownerID, status,
	)
	if err != nil {
		t.Fatalf("seedWorkspaceWithStatus(%q): %v", slug, err)
	}
}

// getSessionField returns a single string column value for a session.
func (env *auditTestEnv) getSessionField(t *testing.T, id, column string) string {
	t.Helper()
	var val sql.NullString
	err := env.db.QueryRow(
		fmt.Sprintf("SELECT %s FROM agent_sessions WHERE id = ?", column), id,
	).Scan(&val)
	if err != nil {
		t.Fatalf("getSessionField(%q, %q): %v", id, column, err)
	}
	return val.String
}

// countTokenUsageRows returns the number of token_usage rows for a workspace.
func (env *auditTestEnv) countTokenUsageRows(t *testing.T, workspaceSlug string) int {
	t.Helper()
	var count int
	err := env.db.QueryRow(
		"SELECT COUNT(*) FROM token_usage WHERE workspace_slug = ?", workspaceSlug,
	).Scan(&count)
	if err != nil {
		t.Fatalf("countTokenUsageRows(%q): %v", workspaceSlug, err)
	}
	return count
}

// uuidRegex matches standard UUID format (8-4-4-4-12 hex digits with dashes).
var uuidRegex = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// isValidUUID checks whether s matches the standard UUID format.
func isValidUUID(s string) bool {
	return uuidRegex.MatchString(s)
}

// Test constants for handler tests.
const (
	testRunID    = "20260704_143022_a1b2c3"
	testSlug     = "ws1"
	eventsPath   = "/api/v1/workspaces/ws1/runs/20260704_143022_a1b2c3/events"
	outcomesPath = "/api/v1/workspaces/ws1/runs/20260704_143022_a1b2c3/sessions/outcomes"
	callsPath    = "/api/v1/workspaces/ws1/runs/20260704_143022_a1b2c3/tools/calls"
	errorsPath   = "/api/v1/workspaces/ws1/runs/20260704_143022_a1b2c3/tools/errors"
	tracesPath       = "/api/v1/workspaces/ws1/runs/20260704_143022_a1b2c3/traces"
	tracesBatchPath  = "/api/v1/workspaces/ws1/runs/20260704_143022_a1b2c3/traces/batch"
	eventsBatchPath  = "/api/v1/workspaces/ws1/runs/20260704_143022_a1b2c3/events/batch"
	pmPath           = "/api/v1/workspaces/ws1/runs/20260704_143022_a1b2c3/postmortem"
)

// countSessionRows returns the number of sessions for a workspace.
func (env *auditTestEnv) countSessionRows(t *testing.T, workspaceSlug string) int {
	t.Helper()
	var count int
	err := env.db.QueryRow(
		"SELECT COUNT(*) FROM agent_sessions WHERE workspace_slug = ?", workspaceSlug,
	).Scan(&count)
	if err != nil {
		t.Fatalf("countSessionRows(%q): %v", workspaceSlug, err)
	}
	return count
}

// workspaceExistsInSQLite checks if a workspace exists in SQLite.
func (env *auditTestEnv) workspaceExistsInSQLite(t *testing.T, slug string) bool {
	t.Helper()
	if env.sqliteDB == nil {
		t.Fatal("workspaceExistsInSQLite requires SQLite DB")
	}
	var count int
	err := env.sqliteDB.QueryRow(
		"SELECT COUNT(*) FROM workspaces WHERE slug = ?", slug,
	).Scan(&count)
	if err != nil {
		t.Fatalf("workspaceExistsInSQLite(%q): %v", slug, err)
	}
	return count > 0
}

// queryTableCountWhere returns the number of rows in the given table matching
// a WHERE clause with the given args.
func queryTableCountWhere(t *testing.T, db *sql.DB, table, where string, args ...any) int {
	t.Helper()
	var count int
	q := "SELECT COUNT(*) FROM " + table
	if where != "" {
		q += " WHERE " + where
	}
	err := db.QueryRow(q, args...).Scan(&count)
	if err != nil {
		t.Fatalf("queryTableCountWhere(%s, %s): %v", table, where, err)
	}
	return count
}

// seedAuditEvent inserts an audit event directly into DuckDB for test setup.
func (env *auditTestEnv) seedAuditEvent(t *testing.T, id, runID, workspace, eventType, timestamp string) {
	t.Helper()
	if timestamp == "" {
		timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := env.db.Exec(
		`INSERT INTO agent_audit_events (id, run_id, workspace, event_type, severity, timestamp, ingested_at)
		 VALUES (?, ?, ?, ?, 'info', ?, ?)`,
		id, runID, workspace, eventType, timestamp, now,
	)
	if err != nil {
		t.Fatalf("seedAuditEvent(%q): %v", id, err)
	}
}

// seedSessionOutcome inserts a session outcome directly into DuckDB for test setup.
func (env *auditTestEnv) seedSessionOutcome(t *testing.T, id, runID, workspace, status, timestamp string) {
	t.Helper()
	if timestamp == "" {
		timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := env.db.Exec(
		`INSERT INTO session_outcomes (id, run_id, workspace, session_id, status, timestamp, ingested_at)
		 VALUES (?, ?, ?, 'sess-1', ?, ?, ?)`,
		id, runID, workspace, status, timestamp, now,
	)
	if err != nil {
		t.Fatalf("seedSessionOutcome(%q): %v", id, err)
	}
}

// seedToolCall inserts a tool call record directly into DuckDB for test setup.
func (env *auditTestEnv) seedToolCall(t *testing.T, id, runID, workspace, toolName, timestamp string) {
	t.Helper()
	if timestamp == "" {
		timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := env.db.Exec(
		`INSERT INTO tool_calls (id, run_id, workspace, tool_name, timestamp, ingested_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, runID, workspace, toolName, timestamp, now,
	)
	if err != nil {
		t.Fatalf("seedToolCall(%q): %v", id, err)
	}
}

// seedToolError inserts a tool error record directly into DuckDB for test setup.
func (env *auditTestEnv) seedToolError(t *testing.T, id, runID, workspace, toolName, timestamp string) {
	t.Helper()
	if timestamp == "" {
		timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := env.db.Exec(
		`INSERT INTO tool_errors (id, run_id, workspace, tool_name, error_msg, timestamp, ingested_at)
		 VALUES (?, ?, ?, ?, 'test error', ?, ?)`,
		id, runID, workspace, toolName, timestamp, now,
	)
	if err != nil {
		t.Fatalf("seedToolError(%q): %v", id, err)
	}
}

// seedTrace inserts a trace event directly into DuckDB for test setup.
func (env *auditTestEnv) seedTrace(t *testing.T, id, runID, workspace, eventType, timestamp string) {
	t.Helper()
	if timestamp == "" {
		timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := env.db.Exec(
		`INSERT INTO agent_traces (id, run_id, workspace, event_type, timestamp, ingested_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, runID, workspace, eventType, timestamp, now,
	)
	if err != nil {
		t.Fatalf("seedTrace(%q): %v", id, err)
	}
}

// workspacePatAuth returns an apikit.AuthInfo for a workspace-scoped PAT.
func workspacePatAuth(userID, workspaceSlug string, permissions ...string) *apikit.AuthInfo {
	return &apikit.AuthInfo{
		CredentialType: "pat",
		UserID:         userID,
		KeyID:          workspaceSlug, // KeyID stores workspace scope for workspace-scoped PATs
		Permissions:    permissions,
	}
}
