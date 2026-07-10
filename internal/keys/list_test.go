package keys_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	_ "modernc.org/sqlite"

	"github.com/agent-fox-dev/hub/internal/keys"
)

// ---------------------------------------------------------------------------
// Test Helpers (shared across key management test files)
// ---------------------------------------------------------------------------

// openTestDB opens an in-memory SQLite database with production pragmas.
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

// initUsersTable creates the users table matching spec 01 DDL.
func initUsersTable(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS users (
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
	)`)
	if err != nil {
		t.Fatalf("failed to create users table: %v", err)
	}
}

// initAPIKeysTable creates the api_keys table matching spec 01 DDL.
func initAPIKeysTable(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS api_keys (
		id              TEXT PRIMARY KEY,
		key_id          TEXT NOT NULL UNIQUE,
		secret_hash     TEXT NOT NULL,
		user_id         TEXT NOT NULL REFERENCES users(id),
		expires_at      TEXT,
		created_at      TEXT NOT NULL,
		revoked_at      TEXT,
		expires_in_days INTEGER
	)`)
	if err != nil {
		t.Fatalf("failed to create api_keys table: %v", err)
	}
}

// initAllTables creates all tables needed for key management tests.
func initAllTables(t *testing.T, db *sql.DB) {
	t.Helper()
	initUsersTable(t, db)
	initAPIKeysTable(t, db)
}

// setupEcho creates an Echo instance with the custom error handler that
// returns the nested error envelope: {"error": {"code": N, "message": "..."}}.
func setupEcho() *echo.Echo {
	e := echo.New()
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		if c.Response().Committed {
			return
		}
		he, ok := err.(*echo.HTTPError)
		if !ok {
			he = echo.NewHTTPError(http.StatusInternalServerError, "internal server error")
		}
		msg, ok := he.Message.(string)
		if !ok {
			msg = "internal server error"
		}
		_ = c.JSON(he.Code, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    he.Code,
				"message": msg,
			},
		})
	}
	return e
}

// setAuthContext returns Echo middleware that injects the given AuthContext.
func setAuthContext(ac *keys.AuthContext) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			c.Set(string(keys.AuthContextKey), ac)
			return next(c)
		}
	}
}

// adminAuthContext returns an AuthContext for an admin token.
func adminAuthContext() *keys.AuthContext {
	return &keys.AuthContext{
		CredentialType: keys.CredentialTypeAdmin,
		IsAdmin:        true,
	}
}

// userAuthContext returns an AuthContext for a regular user API key.
func userAuthContext(userID string) *keys.AuthContext {
	return &keys.AuthContext{
		CredentialType: keys.CredentialTypeAPIKey,
		UserID:         userID,
		IsAdmin:        false,
	}
}

// workspaceAuthContext returns an AuthContext for a workspace token.
func workspaceAuthContext(wsID string) *keys.AuthContext {
	return &keys.AuthContext{
		CredentialType: keys.CredentialTypeWorkspaceToken,
		WorkspaceID:    wsID,
		IsAdmin:        false,
	}
}

// sha256Hex returns the hex-encoded SHA-256 hash of the input string.
func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// insertTestUser inserts a user row directly into the database for test setup.
func insertTestUser(t *testing.T, db *sql.DB, id, username, email, status, provider, providerID, createdAt string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO users (id, username, email, full_name, status, provider, provider_id, created_at, updated_at)
		 VALUES (?, ?, ?, '', ?, ?, ?, ?, ?)`,
		id, username, email, status, provider, providerID, createdAt, createdAt,
	)
	if err != nil {
		t.Fatalf("failed to insert test user %s: %v", username, err)
	}
}

// insertTestAPIKey inserts an API key row directly into the database.
// expiresInDays stores the original expiry duration for refresh operations.
func insertTestAPIKey(t *testing.T, db *sql.DB, keyID, userID, secret, createdAt string, expiresAt, revokedAt *string, expiresInDays *int) {
	t.Helper()
	secretHash := sha256Hex(secret)
	_, err := db.Exec(
		`INSERT INTO api_keys (id, key_id, secret_hash, user_id, expires_at, created_at, revoked_at, expires_in_days)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"apikey-"+keyID, keyID, secretHash, userID, expiresAt, createdAt, revokedAt, expiresInDays,
	)
	if err != nil {
		t.Fatalf("failed to insert test api key %s: %v", keyID, err)
	}
}

// nowISO returns an ISO 8601 timestamp for the current time in UTC.
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

// strPtr returns a pointer to the given string (for nullable DB fields).
func strPtr(s string) *string {
	return &s
}

// intPtr returns a pointer to the given int (for nullable integer DB fields).
func intPtr(i int) *int {
	return &i
}

// ---------------------------------------------------------------------------
// TS-02-24: Admin GET /api/v1/keys returns all API keys across all users
// ordered by created_at ASC; no secret or token field in entries.
// Requirement: 02-REQ-8.1
// ---------------------------------------------------------------------------

func TestListKeys_AdminReturnsAllKeys(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	// Create two users with keys at different times.
	t1 := "2025-01-01T00:00:00Z"
	t2 := "2025-02-01T00:00:00Z"
	insertTestUser(t, db, "user-a", "usera", "a@e.com", "active", "github", "ext-a", t1)
	insertTestUser(t, db, "user-b", "userb", "b@e.com", "active", "github", "ext-b", t1)
	insertTestAPIKey(t, db, "keyAAAA01", "user-a", "secret-a-32-chars-padding-12345", t1, nil, nil, nil)
	insertTestAPIKey(t, db, "keyBBBB01", "user-b", "secret-b-32-chars-padding-12345", t2, nil, nil, nil)

	e := setupEcho()
	e.GET("/api/v1/keys", keys.ListKeysHandler(db), setAuthContext(adminAuthContext()))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/keys", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if len(resp.Keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(resp.Keys))
	}

	// Ordered by created_at ASC: keyAAAA01 first (T1), then keyBBBB01 (T2).
	if resp.Keys[0]["key_id"] != "keyAAAA01" {
		t.Errorf("expected first key to be 'keyAAAA01', got %v", resp.Keys[0]["key_id"])
	}
	if resp.Keys[1]["key_id"] != "keyBBBB01" {
		t.Errorf("expected second key to be 'keyBBBB01', got %v", resp.Keys[1]["key_id"])
	}

	// Verify each entry has required fields but no secret or token.
	requiredFields := []string{"key_id", "user_id", "created_at", "expires_at", "revoked_at"}
	for i, k := range resp.Keys {
		for _, field := range requiredFields {
			if _, ok := k[field]; !ok {
				t.Errorf("keys[%d] missing required field %q", i, field)
			}
		}
		if _, ok := k["secret"]; ok {
			t.Errorf("keys[%d] should NOT include 'secret'", i)
		}
		if _, ok := k["token"]; ok {
			t.Errorf("keys[%d] should NOT include 'token'", i)
		}
	}
}

// ---------------------------------------------------------------------------
// TS-02-25: User GET /api/v1/keys returns all historical keys for the
// requesting user (including expired and revoked) ordered by created_at ASC;
// no secret.
// Requirement: 02-REQ-8.2
// ---------------------------------------------------------------------------

func TestListKeys_UserReturnsOwnKeysOnly(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	t1 := "2025-01-01T00:00:00Z"
	t2 := "2025-02-01T00:00:00Z"
	t3 := "2025-03-01T00:00:00Z"
	revokedTime := "2025-01-15T00:00:00Z"
	expiredTime := "2024-12-01T00:00:00Z"

	insertTestUser(t, db, "user-uuid-5", "user5", "u5@e.com", "active", "github", "ext-5", t1)
	insertTestUser(t, db, "user-other", "otheruser", "other@e.com", "active", "github", "ext-other", t1)

	// User 5 has 3 keys: one active, one expired, one revoked.
	insertTestAPIKey(t, db, "key5actv", "user-uuid-5", "secret-active-32-chars-pad-1234", t1, nil, nil, nil)                       // active
	insertTestAPIKey(t, db, "key5expd", "user-uuid-5", "secret-expired-32-chars-pad-123", t2, &expiredTime, nil, nil)               // expired
	insertTestAPIKey(t, db, "key5rvkd", "user-uuid-5", "secret-revoked-32-chars-pad-123", t3, nil, &revokedTime, nil)               // revoked

	// Other user has a key — should NOT be returned.
	insertTestAPIKey(t, db, "keyOther", "user-other", "secret-other-32-chars-padding-1", t2, nil, nil, nil)

	e := setupEcho()
	e.GET("/api/v1/keys", keys.ListKeysHandler(db), setAuthContext(userAuthContext("user-uuid-5")))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/keys", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	// Only user-uuid-5's keys should be returned (3 keys).
	if len(resp.Keys) != 3 {
		t.Fatalf("expected 3 keys for user-uuid-5, got %d", len(resp.Keys))
	}

	for _, k := range resp.Keys {
		if k["user_id"] != "user-uuid-5" {
			t.Errorf("expected all keys to belong to user-uuid-5, got user_id=%v", k["user_id"])
		}
		if _, ok := k["secret"]; ok {
			t.Error("secret should NOT be included in key listing")
		}
	}

	// Verify ordered by created_at ASC.
	for i := 1; i < len(resp.Keys); i++ {
		prev, _ := resp.Keys[i-1]["created_at"].(string)
		curr, _ := resp.Keys[i]["created_at"].(string)
		if prev > curr {
			t.Errorf("keys not ordered by created_at ASC: keys[%d]=%s > keys[%d]=%s", i-1, prev, i, curr)
		}
	}
}

// ---------------------------------------------------------------------------
// TS-02-26: Key listing responses never include a computed status or is_active
// field; consumers derive validity from expires_at and revoked_at.
// Requirement: 02-REQ-8.3
// ---------------------------------------------------------------------------

func TestListKeys_NoComputedStatusFields(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	insertTestUser(t, db, "user-status", "statususer", "s@e.com", "active", "github", "ext-s", "2025-01-01T00:00:00Z")
	insertTestAPIKey(t, db, "keyStatA", "user-status", "secret-status-32-chars-pad-1234", "2025-01-01T00:00:00Z", nil, nil, nil)

	e := setupEcho()
	e.GET("/api/v1/keys", keys.ListKeysHandler(db), setAuthContext(adminAuthContext()))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/keys", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	for i, k := range resp.Keys {
		if _, ok := k["status"]; ok {
			t.Errorf("keys[%d] should NOT include 'status' field", i)
		}
		if _, ok := k["is_active"]; ok {
			t.Errorf("keys[%d] should NOT include 'is_active' field", i)
		}
		if _, ok := k["is_valid"]; ok {
			t.Errorf("keys[%d] should NOT include 'is_valid' field", i)
		}
	}
}

// ---------------------------------------------------------------------------
// TS-02-E23: GET /api/v1/keys returns HTTP 403 when called with a workspace
// token.
// Requirement: 02-REQ-8.E1
// ---------------------------------------------------------------------------

func TestListKeys_EdgeCase_WorkspaceTokenForbidden(t *testing.T) {
	db := openTestDB(t)
	initAllTables(t, db)

	e := setupEcho()
	e.GET("/api/v1/keys", keys.ListKeysHandler(db), setAuthContext(workspaceAuthContext("ws-uuid-1")))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/keys", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected HTTP 403 for workspace token, got %d: %s", rec.Code, rec.Body.String())
	}
}
