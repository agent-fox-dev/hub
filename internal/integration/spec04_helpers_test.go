// Package integration contains integration tests for af-hub specs.
// These tests exercise the full HTTP handler stack including middleware,
// database access, and response formatting.
package integration

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	_ "modernc.org/sqlite"

	"github.com/agent-fox-dev/hub/internal/auth"
	"github.com/agent-fox-dev/hub/internal/workspace"
)

// ---------------------------------------------------------------------------
// Database helpers
// ---------------------------------------------------------------------------

// openTestDB opens an in-memory SQLite database with foreign keys enabled.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory SQLite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			t.Fatalf("failed to set %s: %v", pragma, err)
		}
	}
	return db
}

// schemaDDL contains the full database schema from spec 01.
const schemaDDL = `
CREATE TABLE IF NOT EXISTS users (
    id          TEXT PRIMARY KEY,
    username    TEXT NOT NULL UNIQUE,
    email       TEXT NOT NULL,
    full_name   TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'active',
    provider    TEXT NOT NULL,
    provider_id TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    UNIQUE (provider, provider_id)
);

CREATE TABLE IF NOT EXISTS admin_tokens (
    id          TEXT PRIMARY KEY,
    token_hash  TEXT NOT NULL,
    created_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS api_keys (
    id              TEXT PRIMARY KEY,
    key_id          TEXT NOT NULL UNIQUE,
    secret_hash     TEXT NOT NULL,
    user_id         TEXT NOT NULL REFERENCES users(id),
    expires_at      TEXT,
    created_at      TEXT NOT NULL,
    revoked_at      TEXT,
    expires_in_days INTEGER
);

CREATE TABLE IF NOT EXISTS teams (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    slug        TEXT NOT NULL UNIQUE,
    url         TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'active',
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS team_members (
    team_id    TEXT NOT NULL REFERENCES teams(id),
    user_id    TEXT NOT NULL REFERENCES users(id),
    created_at TEXT NOT NULL,
    PRIMARY KEY (team_id, user_id)
);

CREATE TABLE IF NOT EXISTS workspaces (
    id         TEXT PRIMARY KEY,
    slug       TEXT NOT NULL UNIQUE,
    git_url    TEXT NOT NULL,
    branch     TEXT,
    owner_id   TEXT NOT NULL REFERENCES users(id),
    team_id    TEXT REFERENCES teams(id),
    status     TEXT NOT NULL DEFAULT 'active',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS workspace_tokens (
    id           TEXT PRIMARY KEY,
    token_id     TEXT NOT NULL UNIQUE,
    secret_hash  TEXT NOT NULL,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id),
    user_id      TEXT NOT NULL REFERENCES users(id),
    label        TEXT,
    expires_at   TEXT,
    created_at   TEXT NOT NULL,
    revoked_at   TEXT
);

CREATE INDEX IF NOT EXISTS idx_api_keys_key_id ON api_keys(key_id);
CREATE INDEX IF NOT EXISTS idx_workspace_tokens_token_id ON workspace_tokens(token_id);
CREATE INDEX IF NOT EXISTS idx_users_provider ON users(provider, provider_id);
CREATE INDEX IF NOT EXISTS idx_workspaces_slug ON workspaces(slug);
CREATE INDEX IF NOT EXISTS idx_teams_slug ON teams(slug);
`

// initTestSchema applies the full database schema to the given DB.
func initTestSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(schemaDDL); err != nil {
		t.Fatalf("failed to initialize schema: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Hashing helpers
// ---------------------------------------------------------------------------

// sha256Hex returns the hex-encoded SHA-256 hash of the input string.
func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// ---------------------------------------------------------------------------
// Fixture factories
// ---------------------------------------------------------------------------

// now returns an ISO 8601 timestamp for the current time in UTC.
func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// pastISO returns an ISO 8601 timestamp for the given duration in the past.
func pastISO(d time.Duration) string {
	return time.Now().UTC().Add(-d).Format(time.RFC3339)
}

// futureISO returns an ISO 8601 timestamp for the given duration in the future.
func futureISO(d time.Duration) string {
	return time.Now().UTC().Add(d).Format(time.RFC3339)
}

// createTestUser inserts a user record into the database.
func createTestUser(t *testing.T, db *sql.DB, userID, username string) {
	t.Helper()
	now := nowISO()
	_, err := db.Exec(`INSERT INTO users (id, username, email, full_name, status, provider, provider_id, created_at, updated_at)
		VALUES (?, ?, ?, '', 'active', 'github', ?, ?, ?)`,
		userID, username, username+"@test.com", "gh_"+userID, now, now)
	if err != nil {
		t.Fatalf("createTestUser(%s): %v", userID, err)
	}
}

// createTestBlockedUser inserts a blocked user record into the database.
func createTestBlockedUser(t *testing.T, db *sql.DB, userID, username string) {
	t.Helper()
	now := nowISO()
	_, err := db.Exec(`INSERT INTO users (id, username, email, full_name, status, provider, provider_id, created_at, updated_at)
		VALUES (?, ?, ?, '', 'blocked', 'github', ?, ?, ?)`,
		userID, username, username+"@test.com", "gh_"+userID, now, now)
	if err != nil {
		t.Fatalf("createTestBlockedUser(%s): %v", userID, err)
	}
}

// createTestAdminToken inserts an admin token record and returns the full
// plaintext token string "af_admin_<secret>".
func createTestAdminToken(t *testing.T, db *sql.DB) string {
	t.Helper()
	// Use a fixed 64-hex-char secret for test determinism.
	secret := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tokenHash := sha256Hex(secret)
	now := nowISO()
	_, err := db.Exec(`INSERT INTO admin_tokens (id, token_hash, created_at) VALUES ('admin-tok-1', ?, ?)`,
		tokenHash, now)
	if err != nil {
		t.Fatalf("createTestAdminToken: %v", err)
	}
	return "af_admin_" + secret
}

// createTestAPIKey inserts an API key record and returns the full plaintext
// token string "af_<keyID>_<secret>".
func createTestAPIKey(t *testing.T, db *sql.DB, userID string, keyID string) string {
	t.Helper()
	// Fixed 32-char base62 secret for determinism.
	secret := "abcdefghABCDEFGH0123456789abcdef"
	secretHash := sha256Hex(secret)
	now := nowISO()
	_, err := db.Exec(`INSERT INTO api_keys (id, key_id, secret_hash, user_id, expires_at, created_at, revoked_at, expires_in_days)
		VALUES (?, ?, ?, ?, NULL, ?, NULL, NULL)`,
		"apikey-"+keyID, keyID, secretHash, userID, now)
	if err != nil {
		t.Fatalf("createTestAPIKey(%s, %s): %v", userID, keyID, err)
	}
	return "af_" + keyID + "_" + secret
}

// createTestAPIKeyWithSecret inserts an API key with a specific secret.
func createTestAPIKeyWithSecret(t *testing.T, db *sql.DB, userID, keyID, secret string) string {
	t.Helper()
	secretHash := sha256Hex(secret)
	now := nowISO()
	_, err := db.Exec(`INSERT INTO api_keys (id, key_id, secret_hash, user_id, expires_at, created_at, revoked_at, expires_in_days)
		VALUES (?, ?, ?, ?, NULL, ?, NULL, NULL)`,
		"apikey-"+keyID, keyID, secretHash, userID, now)
	if err != nil {
		t.Fatalf("createTestAPIKeyWithSecret(%s, %s): %v", userID, keyID, err)
	}
	return "af_" + keyID + "_" + secret
}

// createTestWorkspace inserts a workspace record into the database.
// Returns the workspace ID.
func createTestWorkspace(t *testing.T, db *sql.DB, wsID, slug, gitURL, ownerID string) string {
	t.Helper()
	now := nowISO()
	_, err := db.Exec(`INSERT INTO workspaces (id, slug, git_url, branch, owner_id, team_id, status, created_at, updated_at)
		VALUES (?, ?, ?, NULL, ?, NULL, 'active', ?, ?)`,
		wsID, slug, gitURL, ownerID, now, now)
	if err != nil {
		t.Fatalf("createTestWorkspace(%s, %s): %v", wsID, slug, err)
	}
	return wsID
}

// createTestWorkspaceAt inserts a workspace with a specific created_at timestamp.
func createTestWorkspaceAt(t *testing.T, db *sql.DB, wsID, slug, gitURL, ownerID, createdAt string) string {
	t.Helper()
	_, err := db.Exec(`INSERT INTO workspaces (id, slug, git_url, branch, owner_id, team_id, status, created_at, updated_at)
		VALUES (?, ?, ?, NULL, ?, NULL, 'active', ?, ?)`,
		wsID, slug, gitURL, ownerID, createdAt, createdAt)
	if err != nil {
		t.Fatalf("createTestWorkspaceAt(%s, %s): %v", wsID, slug, err)
	}
	return wsID
}

// testTokenRecord holds the info needed to create a workspace_tokens DB record.
type testTokenRecord struct {
	ID          string
	TokenID     string
	Secret      string // plaintext secret for hash computation
	WorkspaceID string
	UserID      string
	Label       *string
	ExpiresAt   *string
	CreatedAt   string
	RevokedAt   *string
}

// createTestWorkspaceToken inserts a workspace token record and returns
// the full plaintext token string "af_wt_<tokenID>_<secret>".
func createTestWorkspaceToken(t *testing.T, db *sql.DB, rec testTokenRecord) string {
	t.Helper()
	secretHash := sha256Hex(rec.Secret)
	_, err := db.Exec(`INSERT INTO workspace_tokens (id, token_id, secret_hash, workspace_id, user_id, label, expires_at, created_at, revoked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.TokenID, secretHash, rec.WorkspaceID, rec.UserID,
		rec.Label, rec.ExpiresAt, rec.CreatedAt, rec.RevokedAt)
	if err != nil {
		t.Fatalf("createTestWorkspaceToken(%s): %v", rec.TokenID, err)
	}
	return "af_wt_" + rec.TokenID + "_" + rec.Secret
}

// createTestTeam inserts an active team record.
func createTestTeam(t *testing.T, db *sql.DB, teamID, name, slug string) {
	t.Helper()
	now := nowISO()
	_, err := db.Exec(`INSERT INTO teams (id, name, slug, url, status, created_at, updated_at)
		VALUES (?, ?, ?, '', 'active', ?, ?)`,
		teamID, name, slug, now, now)
	if err != nil {
		t.Fatalf("createTestTeam(%s): %v", teamID, err)
	}
}

// ---------------------------------------------------------------------------
// HTTP request helpers
// ---------------------------------------------------------------------------

// setupTestServer creates an Echo instance with auth middleware and workspace
// routes registered, backed by the given database.
func setupTestServer(t *testing.T, db *sql.DB) *echo.Echo {
	t.Helper()
	e := echo.New()

	// Set up route groups matching spec 01 structure.
	apiGroup := e.Group("/api/v1")
	protectedGroup := apiGroup.Group("", auth.Middleware(db))

	// Register workspace routes.
	workspace.RegisterRoutes(protectedGroup, db)

	return e
}

// doRequest sends an HTTP request to the Echo server and returns the response.
func doRequest(e *echo.Echo, method, path string, body interface{}, authHeader string) *httptest.ResponseRecorder {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			panic(fmt.Sprintf("doRequest: failed to marshal body: %v", err))
		}
		bodyReader = bytes.NewReader(data)
	}

	req := httptest.NewRequest(method, path, bodyReader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// doRequestNoContentType sends an HTTP request without a Content-Type header.
func doRequestNoContentType(e *echo.Echo, method, path string, bodyJSON []byte, authHeader string) *httptest.ResponseRecorder {
	var bodyReader io.Reader
	if bodyJSON != nil {
		bodyReader = bytes.NewReader(bodyJSON)
	}

	req := httptest.NewRequest(method, path, bodyReader)
	// Explicitly no Content-Type header.
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// bearer returns a "Bearer <token>" string.
func bearer(token string) string {
	return "Bearer " + token
}

// ---------------------------------------------------------------------------
// Response parsing helpers
// ---------------------------------------------------------------------------

// parseJSON unmarshals the response body into the given target.
func parseJSON(t *testing.T, rec *httptest.ResponseRecorder, target interface{}) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), target); err != nil {
		t.Fatalf("failed to parse JSON response: %v\nbody: %s", err, rec.Body.String())
	}
}

// parseJSONMap unmarshals the response body into a map.
func parseJSONMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	parseJSON(t, rec, &m)
	return m
}

// parseJSONArray unmarshals the response body into a slice of maps.
func parseJSONArray(t *testing.T, rec *httptest.ResponseRecorder) []map[string]interface{} {
	t.Helper()
	var arr []map[string]interface{}
	parseJSON(t, rec, &arr)
	return arr
}

// ---------------------------------------------------------------------------
// Assertion helpers
// ---------------------------------------------------------------------------

// assertStatus checks that the response has the expected HTTP status code.
func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, expected int) {
	t.Helper()
	if rec.Code != expected {
		t.Errorf("HTTP status = %d, want %d\nbody: %s", rec.Code, expected, rec.Body.String())
	}
}

// assertErrorEnvelope checks that the response body is a valid error envelope
// with the expected code.
func assertErrorEnvelope(t *testing.T, rec *httptest.ResponseRecorder, expectedCode int) {
	t.Helper()
	body := parseJSONMap(t, rec)
	errObj, ok := body["error"]
	if !ok {
		t.Fatalf("response body missing 'error' key: %s", rec.Body.String())
	}
	errMap, ok := errObj.(map[string]interface{})
	if !ok {
		t.Fatalf("'error' is not an object: %v", errObj)
	}

	code, ok := errMap["code"]
	if !ok {
		t.Fatal("error envelope missing 'code' field")
	}
	// JSON numbers are float64.
	codeFloat, ok := code.(float64)
	if !ok || int(codeFloat) != expectedCode {
		t.Errorf("error.code = %v, want %d", code, expectedCode)
	}

	message, ok := errMap["message"]
	if !ok {
		t.Fatal("error envelope missing 'message' field")
	}
	msg, ok := message.(string)
	if !ok || msg == "" {
		t.Error("error.message should be a non-empty string")
	}
}

// assertEmptyBody checks that the response body is empty.
func assertEmptyBody(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Body.Len() > 0 {
		t.Errorf("expected empty body, got: %s", rec.Body.String())
	}
}

// workspaceSchemaFields is the set of exactly 8 fields in the workspace object.
var workspaceSchemaFields = []string{
	"id", "slug", "git_url", "branch", "team_id",
	"owner_user_id", "created_at", "updated_at",
}

// assertWorkspaceSchema checks that a response body map contains exactly
// the 8 workspace schema fields with correct types.
func assertWorkspaceSchema(t *testing.T, body map[string]interface{}) {
	t.Helper()

	// Check for exactly the expected fields.
	if len(body) != len(workspaceSchemaFields) {
		t.Errorf("workspace object has %d fields, want %d. Fields: %v",
			len(body), len(workspaceSchemaFields), keys(body))
	}

	for _, field := range workspaceSchemaFields {
		if _, ok := body[field]; !ok {
			t.Errorf("workspace object missing field %q", field)
		}
	}

	// Type checks.
	assertStringField(t, body, "id")
	assertStringField(t, body, "slug")
	assertStringField(t, body, "git_url")
	assertNullableStringField(t, body, "branch")
	assertNullableStringField(t, body, "team_id")
	assertStringField(t, body, "owner_user_id")
	assertISO8601Field(t, body, "created_at")
	assertISO8601Field(t, body, "updated_at")
}

// assertTokenListSchema checks that a token list item has the expected fields.
func assertTokenListSchema(t *testing.T, item map[string]interface{}) {
	t.Helper()
	requiredFields := []string{"token_id", "label", "created_at", "expires_at", "revoked_at"}
	for _, field := range requiredFields {
		if _, ok := item[field]; !ok {
			t.Errorf("token list item missing field %q", field)
		}
	}
	// The secret must never appear.
	forbiddenFields := []string{"token", "secret", "secret_hash"}
	for _, field := range forbiddenFields {
		if _, ok := item[field]; ok {
			t.Errorf("token list item must not contain field %q", field)
		}
	}
}

// ---------------------------------------------------------------------------
// Field type assertion helpers
// ---------------------------------------------------------------------------

func assertStringField(t *testing.T, m map[string]interface{}, key string) {
	t.Helper()
	v, ok := m[key]
	if !ok {
		return // already reported as missing
	}
	if _, ok := v.(string); !ok {
		t.Errorf("field %q should be a string, got %T", key, v)
	}
}

func assertNullableStringField(t *testing.T, m map[string]interface{}, key string) {
	t.Helper()
	v, ok := m[key]
	if !ok {
		return // already reported as missing
	}
	if v != nil {
		if _, ok := v.(string); !ok {
			t.Errorf("field %q should be string or null, got %T", key, v)
		}
	}
}

var iso8601Pattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}`)

func assertISO8601Field(t *testing.T, m map[string]interface{}, key string) {
	t.Helper()
	v, ok := m[key]
	if !ok {
		return
	}
	s, ok := v.(string)
	if !ok {
		t.Errorf("field %q should be an ISO 8601 string, got %T", key, v)
		return
	}
	if !iso8601Pattern.MatchString(s) {
		t.Errorf("field %q = %q is not ISO 8601 format", key, s)
	}
}

func isISO8601(s string) bool {
	return iso8601Pattern.MatchString(s)
}

var base62Pattern = regexp.MustCompile(`^[0-9A-Za-z]+$`)

func isBase62(s string) bool {
	return base62Pattern.MatchString(s)
}

// keys returns the keys of a map for diagnostic output.
func keys(m map[string]interface{}) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// strPtr returns a pointer to the given string.
func strPtr(s string) *string {
	return &s
}

// intPtr returns a pointer to the given int.
func intPtr(i int) *int {
	return &i
}

// ---------------------------------------------------------------------------
// Common test fixtures
// ---------------------------------------------------------------------------

// testEnv holds the common test environment: Echo server, database, and
// pre-created credential tokens.
type testEnv struct {
	E  *echo.Echo
	DB *sql.DB

	// Users
	OwnerUserID    string
	NonOwnerUserID string

	// Tokens
	OwnerAPIKey    string // Bearer-ready token for owner
	NonOwnerAPIKey string // Bearer-ready token for non-owner
	AdminToken     string // Bearer-ready admin token
}

// setupStandardEnv creates a standard test environment with:
//   - owner user (user-001) with API key
//   - non-owner user (user-002) with API key
//   - admin token
//   - Echo server with routes
func setupStandardEnv(t *testing.T) *testEnv {
	t.Helper()
	db := openTestDB(t)
	initTestSchema(t, db)

	createTestUser(t, db, "user-001", "owner")
	createTestUser(t, db, "user-002", "nonowner")

	ownerKey := createTestAPIKeyWithSecret(t, db, "user-001", "ownrkey1", "ABCDEFGHabcdefgh0123456789abcdef")
	nonOwnerKey := createTestAPIKeyWithSecret(t, db, "user-002", "nonokey1", "ZYXWVUTSzyxwvuts9876543210zyxwvu")
	adminToken := createTestAdminToken(t, db)

	e := setupTestServer(t, db)

	return &testEnv{
		E:              e,
		DB:             db,
		OwnerUserID:    "user-001",
		NonOwnerUserID: "user-002",
		OwnerAPIKey:    ownerKey,
		NonOwnerAPIKey: nonOwnerKey,
		AdminToken:     adminToken,
	}
}

// Ensure all imported packages are used.
var _ = http.StatusOK
