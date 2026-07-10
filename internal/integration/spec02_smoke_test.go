package integration

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/auth"
	hubkeys "github.com/agent-fox-dev/hub/internal/keys"
	"github.com/agent-fox-dev/hub/internal/users"
)

// ---------------------------------------------------------------------------
// Smoke Test Helpers (spec 02 specific)
// ---------------------------------------------------------------------------

// setupSpec02Server creates an Echo server with all spec 02 routes registered.
// Uses real auth middleware, real SQLite DB, and real handler constructors
// (which are stubs for now — tests will fail until implementation completes).
func setupSpec02Server(t *testing.T, db *sql.DB) *echo.Echo {
	t.Helper()
	e := echo.New()

	// Custom error handler matching server_foundation format.
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

	// Set up routes with real auth middleware.
	apiGroup := e.Group("/api/v1")
	protectedGroup := apiGroup.Group("", auth.Middleware(db))

	// Public endpoint (no auth).
	// Note: GET /api/v1/auth/providers is not yet implemented as a handler
	// in the auth package — it will be added in task group 4.

	// User management routes (admin-protected).
	registry := &smokeProviderRegistry{providers: map[string]bool{"github": true}}
	protectedGroup.POST("/users", users.CreateUserHandler(db, registry))
	protectedGroup.GET("/users", users.ListUsersHandler(db))
	protectedGroup.GET("/users/:id", users.GetUserHandler(db))
	protectedGroup.PUT("/users/:id", users.UpdateUserHandler(db))

	// Key management routes (mixed auth).
	protectedGroup.GET("/keys", hubkeys.ListKeysHandler(db))
	protectedGroup.POST("/keys/:key_id/refresh", hubkeys.RefreshKeyHandler(db))
	protectedGroup.DELETE("/keys/:key_id", hubkeys.RevokeKeyHandler(db))

	return e
}

// smokeProviderRegistry implements users.ProviderRegistry for smoke tests.
type smokeProviderRegistry struct {
	providers map[string]bool
}

func (r *smokeProviderRegistry) IsRegistered(name string) bool {
	return r.providers[name]
}

// smokeUserSHA256 is a helper for computing SHA-256 in smoke tests.
func smokeUserSHA256(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// ---------------------------------------------------------------------------
// TS-02-SMOKE-1: End-to-end GitHub OAuth login flow.
// Execution Path: 02-PATH-1
//
// NOTE: POST /api/v1/auth/callback is not yet implemented (task group 6).
// This smoke test is structured to test the full flow once the callback
// handler exists. Until then, the test verifies the components that are
// available: user creation, key management, and DB state verification.
// The callback portion will fail with 404/501 until implementation.
// ---------------------------------------------------------------------------

func TestSmoke_GitHubOAuthLoginFlow(t *testing.T) {
	db := openTestDB(t)
	initTestSchema(t, db)

	adminToken := createTestAdminToken(t, db)
	e := setupSpec02Server(t, db)

	// Step 1: Verify user management works by pre-creating a user via admin API.
	createBody := `{"username":"smokeuser1","email":"smoke1@example.com","provider":"github","provider_id":"gh-smoke-001"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearer(adminToken))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("SMOKE-1: expected HTTP 201 for user creation, got %d: %s", rec.Code, rec.Body.String())
	}

	var createResp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("SMOKE-1: failed to parse user creation response: %v", err)
	}

	userID, ok := createResp["id"].(string)
	if !ok || userID == "" {
		t.Fatal("SMOKE-1: user creation should return a non-empty id")
	}

	// Verify user exists in DB.
	var dbUserID string
	err := db.QueryRow("SELECT id FROM users WHERE username = ?", "smokeuser1").Scan(&dbUserID)
	if err != nil {
		t.Fatalf("SMOKE-1: user not found in DB: %v", err)
	}
	if dbUserID != userID {
		t.Errorf("SMOKE-1: DB user ID mismatch: %s vs %s", dbUserID, userID)
	}

	// Verify no API key was created for admin-created user.
	var keyCount int
	db.QueryRow("SELECT COUNT(*) FROM api_keys WHERE user_id = ?", userID).Scan(&keyCount)
	if keyCount != 0 {
		t.Errorf("SMOKE-1: expected 0 api_keys for admin-created user, got %d", keyCount)
	}

	// Step 2: Verify user listing includes the new user.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	req2.Header.Set("Authorization", bearer(adminToken))
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("SMOKE-1: expected HTTP 200 for user listing, got %d: %s", rec2.Code, rec2.Body.String())
	}
}

// ---------------------------------------------------------------------------
// TS-02-SMOKE-2: Admin pre-provisions a user via POST /api/v1/users, then
// that user logs in via OAuth callback and receives an API key.
// Execution Path: 02-PATH-2
//
// NOTE: The callback portion is a stub. This test verifies the admin
// pre-provisioning flow and DB state. The callback login portion will be
// tested once task group 6 implements the callback handler.
// ---------------------------------------------------------------------------

func TestSmoke_AdminPreProvisionThenLogin(t *testing.T) {
	db := openTestDB(t)
	initTestSchema(t, db)

	adminToken := createTestAdminToken(t, db)
	e := setupSpec02Server(t, db)

	// Step 1: Admin pre-provisions a user.
	body := `{"username":"preprovuser","email":"preprov@example.com","full_name":"Pre Provisioned","provider":"github","provider_id":"gh-preprov-001"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearer(adminToken))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("SMOKE-2: expected HTTP 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var createResp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &createResp)

	// Verify provider_id is excluded from response.
	if _, exists := createResp["provider_id"]; exists {
		t.Error("SMOKE-2: provider_id should be excluded from POST /api/v1/users response")
	}

	// Verify no API key created.
	userID := createResp["id"].(string)
	var keyCount int
	db.QueryRow("SELECT COUNT(*) FROM api_keys WHERE user_id = ?", userID).Scan(&keyCount)
	if keyCount != 0 {
		t.Errorf("SMOKE-2: expected 0 api_keys after admin creation, got %d", keyCount)
	}

	// Step 2: Verify the user can be found by admin GET.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/users/"+userID, nil)
	req2.Header.Set("Authorization", bearer(adminToken))
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("SMOKE-2: expected HTTP 200 for GET user, got %d: %s", rec2.Code, rec2.Body.String())
	}

	var getResp map[string]interface{}
	json.Unmarshal(rec2.Body.Bytes(), &getResp)

	// Full GET should include provider_id.
	if _, exists := getResp["provider_id"]; !exists {
		t.Error("SMOKE-2: GET /api/v1/users/:id should include provider_id")
	}

	// Verify users table has exactly one row for this user.
	var dbCount int
	db.QueryRow("SELECT COUNT(*) FROM users WHERE id = ?", userID).Scan(&dbCount)
	if dbCount != 1 {
		t.Errorf("SMOKE-2: expected 1 user row, got %d", dbCount)
	}
}

// ---------------------------------------------------------------------------
// TS-02-SMOKE-3: Authenticated user refreshes their own expired API key
// and receives a new secret without re-authenticating via OAuth.
// Execution Path: 02-PATH-3
//
// NOTE: This test uses direct DB setup and the key refresh handler.
// It will fail until the refresh handler is implemented (task group 8).
// ---------------------------------------------------------------------------

func TestSmoke_UserRefreshesExpiredKey(t *testing.T) {
	db := openTestDB(t)
	initTestSchema(t, db)

	// Create user and an expired key.
	createTestUser(t, db, "user-smoke3", "smokerefresh")

	// Insert an expired key with 30-day original expiry.
	createdAt := pastISO(60 * 24 * time.Hour)
	expiredAt := pastISO(30 * 24 * time.Hour)
	secret := "abcdefghABCDEFGH0123456789aaaaaa"
	secretHash := smokeUserSHA256(secret)
	_, err := db.Exec(`INSERT INTO api_keys (id, key_id, secret_hash, user_id, expires_at, created_at, revoked_at, expires_in_days)
		VALUES (?, ?, ?, ?, ?, ?, NULL, 30)`,
		"apikey-smk3key1", "smk3key1", secretHash, "user-smoke3", expiredAt, createdAt)
	if err != nil {
		t.Fatalf("SMOKE-3: failed to insert expired key: %v", err)
	}

	// Create an active API key for auth.
	apiKeyToken := createTestAPIKey(t, db, "user-smoke3", "smk3auth")

	e := setupSpec02Server(t, db)

	// Refresh the expired key.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/keys/smk3key1/refresh", nil)
	req.Header.Set("Authorization", bearer(apiKeyToken))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("SMOKE-3: expected HTTP 200 for refresh, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("SMOKE-3: failed to parse refresh response: %v", err)
	}

	// Verify key_id unchanged.
	if resp["key_id"] != "smk3key1" {
		t.Errorf("SMOKE-3: expected key_id 'smk3key1', got %v", resp["key_id"])
	}

	// Verify new secret is 32 alphanumeric chars.
	newSecret, ok := resp["secret"].(string)
	if !ok {
		t.Fatal("SMOKE-3: expected 'secret' in response")
	}
	base62Re := regexp.MustCompile(`^[0-9A-Za-z]{32}$`)
	if !base62Re.MatchString(newSecret) {
		t.Errorf("SMOKE-3: secret should match base62 pattern, got %q", newSecret)
	}

	// Token format.
	expectedToken := "af_smk3key1_" + newSecret
	if resp["token"] != expectedToken {
		t.Errorf("SMOKE-3: expected token %q, got %v", expectedToken, resp["token"])
	}

	// revoked_at should be null.
	if resp["revoked_at"] != nil {
		t.Errorf("SMOKE-3: revoked_at should be null after refresh, got %v", resp["revoked_at"])
	}

	// DB: secret_hash should be updated.
	var dbHash string
	db.QueryRow("SELECT secret_hash FROM api_keys WHERE key_id = ?", "smk3key1").Scan(&dbHash)
	if dbHash != smokeUserSHA256(newSecret) {
		t.Error("SMOKE-3: secret_hash in DB should match SHA-256 of new secret")
	}

	// DB: expires_at should be approximately 30 days from now.
	var dbExpiresAt string
	db.QueryRow("SELECT expires_at FROM api_keys WHERE key_id = ?", "smk3key1").Scan(&dbExpiresAt)
	parsed, parseErr := time.Parse(time.RFC3339, dbExpiresAt)
	if parseErr != nil {
		t.Fatalf("SMOKE-3: failed to parse expires_at: %v", parseErr)
	}
	diffDays := time.Until(parsed).Hours() / 24
	if diffDays < 29.0 || diffDays > 31.0 {
		t.Errorf("SMOKE-3: expires_at should be ~30 days from now, got %.1f days", diffDays)
	}

	// Old secret should no longer match.
	if dbHash == secretHash {
		t.Error("SMOKE-3: secret_hash should have changed from the old value")
	}
}

// ---------------------------------------------------------------------------
// TS-02-SMOKE-4: Admin blocks a user and revokes their API key; blocked user
// cannot subsequently log in via OAuth.
// Execution Path: 02-PATH-4
//
// NOTE: The OAuth callback portion (blocked user login attempt) requires
// the callback handler from task group 6. The admin block + key revoke
// portions are tested here with real handlers.
// ---------------------------------------------------------------------------

func TestSmoke_AdminBlocksUserAndRevokesKey(t *testing.T) {
	db := openTestDB(t)
	initTestSchema(t, db)

	adminToken := createTestAdminToken(t, db)
	createTestUser(t, db, "user-smoke4", "smokeblock")
	createTestAPIKey(t, db, "user-smoke4", "smk4key1")

	e := setupSpec02Server(t, db)

	// Step 1: Admin blocks the user.
	blockBody := `{"status":"blocked"}`
	req1 := httptest.NewRequest(http.MethodPut, "/api/v1/users/user-smoke4", strings.NewReader(blockBody))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("Authorization", bearer(adminToken))
	rec1 := httptest.NewRecorder()
	e.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("SMOKE-4: expected HTTP 200 for blocking user, got %d: %s", rec1.Code, rec1.Body.String())
	}

	var blockResp map[string]interface{}
	json.Unmarshal(rec1.Body.Bytes(), &blockResp)
	if blockResp["status"] != "blocked" {
		t.Errorf("SMOKE-4: expected status 'blocked', got %v", blockResp["status"])
	}

	// Verify DB status.
	var dbStatus string
	db.QueryRow("SELECT status FROM users WHERE id = ?", "user-smoke4").Scan(&dbStatus)
	if dbStatus != "blocked" {
		t.Errorf("SMOKE-4: DB status should be 'blocked', got %q", dbStatus)
	}

	// Step 2: Admin revokes the key.
	req2 := httptest.NewRequest(http.MethodDelete, "/api/v1/keys/smk4key1", nil)
	req2.Header.Set("Authorization", bearer(adminToken))
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusNoContent {
		t.Fatalf("SMOKE-4: expected HTTP 204 for key revocation, got %d: %s", rec2.Code, rec2.Body.String())
	}

	// Verify key is revoked in DB.
	var revokedAt *string
	db.QueryRow("SELECT revoked_at FROM api_keys WHERE key_id = ?", "smk4key1").Scan(&revokedAt)
	if revokedAt == nil {
		t.Error("SMOKE-4: key should have revoked_at set after revocation")
	}

	// Verify user status remains 'blocked' (not accidentally reactivated).
	db.QueryRow("SELECT status FROM users WHERE id = ?", "user-smoke4").Scan(&dbStatus)
	if dbStatus != "blocked" {
		t.Errorf("SMOKE-4: user status should remain 'blocked', got %q", dbStatus)
	}
}
