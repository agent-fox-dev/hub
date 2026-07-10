package auth_test

import (
	"context"
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
	_ "modernc.org/sqlite"

	"github.com/agent-fox-dev/hub/internal/auth"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// callbackTestDB opens an in-memory SQLite DB, applies schema, and returns it.
func callbackTestDB(t *testing.T) *sql.DB {
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

	// Minimal schema required for callback tests.
	schema := `
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
	CREATE TABLE IF NOT EXISTS api_keys (
		id          TEXT PRIMARY KEY,
		key_id      TEXT NOT NULL UNIQUE,
		secret_hash TEXT NOT NULL,
		user_id     TEXT NOT NULL REFERENCES users(id),
		expires_at  TEXT,
		created_at  TEXT NOT NULL,
		revoked_at  TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_api_keys_key_id ON api_keys(key_id);
	`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("failed to apply schema: %v", err)
	}

	return db
}

// sha256hex returns the hex-encoded SHA-256 hash of s.
func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// mockGitHubServer creates an httptest.Server that serves both token exchange
// and user info endpoints. Returns the server and cleanup function.
func mockGitHubServer(t *testing.T, login, email, name string, providerID int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"access_token": "mock_access_token_12345",
		})
	})

	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":    providerID,
			"login": login,
			"email": email,
			"name":  name,
		})
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// mockGitHubErrorServer returns a server that responds with an error to code exchange.
func mockGitHubErrorServer(t *testing.T, statusCode int) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(statusCode)
		w.Write([]byte("server error"))
	}))
	t.Cleanup(ts.Close)
	return ts
}

// mockGitHubUserInfoServer creates a server where token exchange succeeds
// but user info returns configurable data (possibly with empty email).
func mockGitHubUserInfoServer(t *testing.T, login, email string, providerID int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"access_token": "mock_access_token",
		})
	})

	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]interface{}{
			"id":    providerID,
			"login": login,
		}
		if email != "" {
			resp["email"] = email
		}
		json.NewEncoder(w).Encode(resp)
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// setupCallbackEcho creates an Echo instance with the callback route registered.
func setupCallbackEcho(t *testing.T, db *sql.DB, registry *auth.Registry, allowlist *auth.Allowlist) *echo.Echo {
	t.Helper()
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
	e.POST("/api/v1/auth/callback", auth.CallbackHandler(db, registry, allowlist))
	return e
}

// registerGitHubProvider creates a registry with a GitHub provider pointing
// to the given mock server URLs.
func registerGitHubProvider(t *testing.T, mockServerURL string) *auth.Registry {
	t.Helper()
	registry := auth.NewRegistry()
	cfg := auth.ProviderConfig{
		Name:         "github",
		TokenURL:     mockServerURL + "/token",
		UserInfoURL:  mockServerURL + "/userinfo",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
	}
	provider := auth.NewGitHubProvider(cfg)
	registry.Register("github", provider, cfg)
	return registry
}

// doCallback sends a POST /api/v1/auth/callback request.
func doCallback(e *echo.Echo, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/callback", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// parseCallbackResponse parses the callback response JSON.
func parseCallbackResponse(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse callback response: %v\nbody: %s", err, rec.Body.String())
	}
	return resp
}

// isUUIDv4 checks if a string looks like a UUID v4 (lowercase, hyphenated).
func isUUIDv4(s string) bool {
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	return re.MatchString(s)
}

// base62Re matches base62 strings.
var base62Re = regexp.MustCompile(`^[0-9A-Za-z]+$`)

// insertTestUser inserts a user directly into the DB for testing.
// Uses a past timestamp to ensure updated_at comparisons can detect changes.
func insertTestUser(t *testing.T, db *sql.DB, id, username, email, status, provider, providerID string) {
	t.Helper()
	past := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO users (id, username, email, full_name, status, provider, provider_id, created_at, updated_at)
		 VALUES (?, ?, ?, '', ?, ?, ?, ?, ?)`,
		id, username, email, status, provider, providerID, past, past,
	)
	if err != nil {
		t.Fatalf("insertTestUser(%s): %v", id, err)
	}
}

// insertTestKey inserts an API key directly into the DB for testing.
func insertTestKey(t *testing.T, db *sql.DB, keyID, userID, secret string, expiresAt, revokedAt *string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	secretHash := sha256hex(secret)
	_, err := db.Exec(
		`INSERT INTO api_keys (id, key_id, secret_hash, user_id, expires_at, created_at, revoked_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"apikey-"+keyID, keyID, secretHash, userID, expiresAt, now, revokedAt,
	)
	if err != nil {
		t.Fatalf("insertTestKey(%s): %v", keyID, err)
	}
}

// ---------------------------------------------------------------------------
// TS-02-5: POST /api/v1/auth/callback with valid provider, expires, and
// allowlisted redirect_uri completes the full login flow and returns user
// object plus API key.
// Requirement: 02-REQ-2.1
// ---------------------------------------------------------------------------

func TestCallback_FullLoginFlow(t *testing.T) {
	db := callbackTestDB(t)
	mock := mockGitHubServer(t, "testuser", "test@example.com", "Test User", 12345)
	registry := registerGitHubProvider(t, mock.URL)
	allowlist := auth.NewAllowlist("", true) // dev mode
	e := setupCallbackEcho(t, db, registry, allowlist)

	body := `{"provider":"github","code":"valid-code","redirect_uri":"http://localhost:3000/callback","expires":30}`
	rec := doCallback(e, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseCallbackResponse(t, rec)

	// Check user object.
	user, ok := resp["user"].(map[string]interface{})
	if !ok {
		t.Fatal("expected 'user' object in response")
	}
	if !isUUIDv4(user["id"].(string)) {
		t.Errorf("expected UUID v4 id, got %v", user["id"])
	}
	if user["username"] != "testuser" {
		t.Errorf("expected username 'testuser', got %v", user["username"])
	}
	if user["email"] != "test@example.com" {
		t.Errorf("expected email 'test@example.com', got %v", user["email"])
	}
	if user["status"] != "active" {
		t.Errorf("expected status 'active', got %v", user["status"])
	}
	if user["provider"] != "github" {
		t.Errorf("expected provider 'github', got %v", user["provider"])
	}

	// Check api_key object.
	apiKey, ok := resp["api_key"].(map[string]interface{})
	if !ok {
		t.Fatal("expected 'api_key' object in response")
	}
	keyID, _ := apiKey["key_id"].(string)
	secret, _ := apiKey["secret"].(string)
	token, _ := apiKey["token"].(string)

	if len(keyID) != 8 || !base62Re.MatchString(keyID) {
		t.Errorf("key_id should be 8 alphanumeric chars, got %q", keyID)
	}
	if len(secret) != 32 || !base62Re.MatchString(secret) {
		t.Errorf("secret should be 32 alphanumeric chars, got %q", secret)
	}
	if token != "af_"+keyID+"_"+secret {
		t.Errorf("token format mismatch: %q", token)
	}
	if apiKey["user_id"] != user["id"] {
		t.Errorf("api_key.user_id should match user.id")
	}

	// Verify expires_at is approximately 30 days from now.
	if expiresAt, ok := apiKey["expires_at"].(string); ok {
		parsed, err := time.Parse(time.RFC3339, expiresAt)
		if err != nil {
			t.Fatalf("failed to parse expires_at: %v", err)
		}
		diffDays := time.Until(parsed).Hours() / 24
		if diffDays < 29.9 || diffDays > 30.1 {
			t.Errorf("expires_at should be ~30 days from now, got %.1f", diffDays)
		}
	} else {
		t.Error("expected expires_at in api_key response")
	}

	// Verify DB state.
	var dbUserID string
	err := db.QueryRow("SELECT id FROM users WHERE id = ?", user["id"]).Scan(&dbUserID)
	if err != nil {
		t.Fatalf("user not found in DB: %v", err)
	}

	var dbSecretHash string
	err = db.QueryRow("SELECT secret_hash FROM api_keys WHERE key_id = ?", keyID).Scan(&dbSecretHash)
	if err != nil {
		t.Fatalf("key not found in DB: %v", err)
	}
	if dbSecretHash != sha256hex(secret) {
		t.Error("DB secret_hash should be SHA-256 of returned secret")
	}
}

// ---------------------------------------------------------------------------
// TS-02-6: Callback creates a new user with status 'active' and UUID v4 id
// when no user exists for (provider, provider_id).
// Requirement: 02-REQ-2.2
// ---------------------------------------------------------------------------

func TestCallback_NewUserCreation(t *testing.T) {
	db := callbackTestDB(t)
	mock := mockGitHubServer(t, "newuser", "new@example.com", "New User", 99999)
	registry := registerGitHubProvider(t, mock.URL)
	allowlist := auth.NewAllowlist("", true)
	e := setupCallbackEcho(t, db, registry, allowlist)

	body := `{"provider":"github","code":"new-code","redirect_uri":"http://localhost:3000/cb"}`
	rec := doCallback(e, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseCallbackResponse(t, rec)
	user := resp["user"].(map[string]interface{})

	if !isUUIDv4(user["id"].(string)) {
		t.Errorf("expected UUID v4 id, got %v", user["id"])
	}
	if user["status"] != "active" {
		t.Errorf("expected status 'active', got %v", user["status"])
	}

	// Verify DB.
	var dbProviderID, dbStatus string
	err := db.QueryRow("SELECT provider_id, status FROM users WHERE id = ?", user["id"]).
		Scan(&dbProviderID, &dbStatus)
	if err != nil {
		t.Fatalf("user not found in DB: %v", err)
	}
	if dbProviderID != "99999" {
		t.Errorf("expected provider_id '99999', got %q", dbProviderID)
	}
	if dbStatus != "active" {
		t.Errorf("expected status 'active', got %q", dbStatus)
	}
}

// ---------------------------------------------------------------------------
// TS-02-7: Callback updates username and email from provider response for
// existing user without changing provider_id; updated_at bumped only if
// value changes.
// Requirement: 02-REQ-2.3
// ---------------------------------------------------------------------------

func TestCallback_ExistingUserUpdate(t *testing.T) {
	db := callbackTestDB(t)

	// Create existing user.
	insertTestUser(t, db, "user-uuid-1", "olduser", "old@example.com", "active", "github", "12345")

	// Record the original updated_at (set in the past by insertTestUser).
	var t0 string
	db.QueryRow("SELECT updated_at FROM users WHERE id = ?", "user-uuid-1").Scan(&t0)

	// Mock returns new username and email but same provider_id.
	mock := mockGitHubServer(t, "newuser", "new@example.com", "", 12345)
	registry := registerGitHubProvider(t, mock.URL)
	allowlist := auth.NewAllowlist("", true)
	e := setupCallbackEcho(t, db, registry, allowlist)

	body := `{"provider":"github","code":"code-123","redirect_uri":"http://localhost:3000/cb"}`
	rec := doCallback(e, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify DB.
	var dbUsername, dbEmail, dbProviderID, dbUpdatedAt string
	db.QueryRow("SELECT username, email, provider_id, updated_at FROM users WHERE id = ?", "user-uuid-1").
		Scan(&dbUsername, &dbEmail, &dbProviderID, &dbUpdatedAt)

	if dbUsername != "newuser" {
		t.Errorf("expected username 'newuser', got %q", dbUsername)
	}
	if dbEmail != "new@example.com" {
		t.Errorf("expected email 'new@example.com', got %q", dbEmail)
	}
	if dbProviderID != "12345" {
		t.Errorf("provider_id should remain '12345', got %q", dbProviderID)
	}
	if dbUpdatedAt <= t0 {
		t.Errorf("updated_at should be bumped since username and email changed")
	}
}

// ---------------------------------------------------------------------------
// TS-02-8: New API key is generated with 8-char base62 key_id, 32-char base62
// secret from crypto/rand; SHA-256 hash stored; expires_at computed correctly;
// token format is af_<key_id>_<secret>.
// Requirement: 02-REQ-2.4
// ---------------------------------------------------------------------------

func TestCallback_KeyGeneration(t *testing.T) {
	db := callbackTestDB(t)
	mock := mockGitHubServer(t, "keyuser", "key@example.com", "", 55555)
	registry := registerGitHubProvider(t, mock.URL)
	allowlist := auth.NewAllowlist("", true)
	e := setupCallbackEcho(t, db, registry, allowlist)

	body := `{"provider":"github","code":"code-x","redirect_uri":"http://localhost:3000/cb","expires":60}`
	rec := doCallback(e, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseCallbackResponse(t, rec)
	apiKey := resp["api_key"].(map[string]interface{})

	keyID := apiKey["key_id"].(string)
	secret := apiKey["secret"].(string)

	keyIDRe := regexp.MustCompile(`^[0-9A-Za-z]{8}$`)
	secretRe := regexp.MustCompile(`^[0-9A-Za-z]{32}$`)

	if !keyIDRe.MatchString(keyID) {
		t.Errorf("key_id should match ^[0-9A-Za-z]{8}$, got %q", keyID)
	}
	if !secretRe.MatchString(secret) {
		t.Errorf("secret should match ^[0-9A-Za-z]{32}$, got %q", secret)
	}
	if apiKey["token"] != "af_"+keyID+"_"+secret {
		t.Errorf("token format mismatch")
	}

	// Verify SHA-256 hash in DB.
	var dbSecretHash string
	db.QueryRow("SELECT secret_hash FROM api_keys WHERE key_id = ?", keyID).Scan(&dbSecretHash)
	if dbSecretHash != sha256hex(secret) {
		t.Error("DB secret_hash should equal SHA-256 of returned secret")
	}

	// Verify expires_at is approximately 60 days.
	expiresAt := apiKey["expires_at"].(string)
	parsed, _ := time.Parse(time.RFC3339, expiresAt)
	diffDays := time.Until(parsed).Hours() / 24
	if diffDays < 59.9 || diffDays > 60.1 {
		t.Errorf("expires_at should be ~60 days from now, got %.1f", diffDays)
	}
}

// ---------------------------------------------------------------------------
// TS-02-9: Login revokes the existing active key before creating a new key.
// Requirement: 02-REQ-2.5
// ---------------------------------------------------------------------------

func TestCallback_RevokesExistingKey(t *testing.T) {
	db := callbackTestDB(t)

	// Create existing user with an active key.
	insertTestUser(t, db, "user-revoke-1", "revokeuser", "r@example.com", "active", "github", "77777")
	insertTestKey(t, db, "oldkey01", "user-revoke-1", "oldsecret12345678901234567890ab", nil, nil)

	mock := mockGitHubServer(t, "revokeuser", "r@example.com", "", 77777)
	registry := registerGitHubProvider(t, mock.URL)
	allowlist := auth.NewAllowlist("", true)
	e := setupCallbackEcho(t, db, registry, allowlist)

	body := `{"provider":"github","code":"code-r","redirect_uri":"http://localhost:3000/cb"}`
	rec := doCallback(e, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseCallbackResponse(t, rec)
	newKeyID := resp["api_key"].(map[string]interface{})["key_id"].(string)

	if newKeyID == "oldkey01" {
		t.Error("new key_id should differ from old key_id")
	}

	// Old key should be revoked.
	var oldRevokedAt sql.NullString
	db.QueryRow("SELECT revoked_at FROM api_keys WHERE key_id = ?", "oldkey01").Scan(&oldRevokedAt)
	if !oldRevokedAt.Valid || oldRevokedAt.String == "" {
		t.Error("old key should have revoked_at set")
	}

	// New key should not be revoked.
	var newRevokedAt sql.NullString
	db.QueryRow("SELECT revoked_at FROM api_keys WHERE key_id = ?", newKeyID).Scan(&newRevokedAt)
	if newRevokedAt.Valid && newRevokedAt.String != "" {
		t.Error("new key should not have revoked_at set")
	}
}

// ---------------------------------------------------------------------------
// TS-02-E2: POST /api/v1/auth/callback with unrecognized provider returns
// HTTP 400 with structured error body.
// Requirement: 02-REQ-2.E1
// ---------------------------------------------------------------------------

func TestCallback_UnrecognizedProvider(t *testing.T) {
	db := callbackTestDB(t)
	registry := auth.NewRegistry() // empty: no providers
	cfg := auth.ProviderConfig{Name: "github", ClientID: "id"}
	registry.Register("github", auth.NewGitHubProvider(cfg), cfg)

	allowlist := auth.NewAllowlist("", true)
	e := setupCallbackEcho(t, db, registry, allowlist)

	body := `{"provider":"fakeprovider","code":"code123","redirect_uri":"http://localhost:3000/cb"}`
	rec := doCallback(e, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify structured error body.
	resp := parseCallbackResponse(t, rec)
	errObj, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatal("expected structured error body")
	}
	if errObj["message"] == nil || errObj["message"] == "" {
		t.Error("error should contain a message")
	}
}

// ---------------------------------------------------------------------------
// TS-02-E3: POST /api/v1/auth/callback with invalid expires returns HTTP 400.
// Requirement: 02-REQ-2.E2
// ---------------------------------------------------------------------------

func TestCallback_InvalidExpires(t *testing.T) {
	db := callbackTestDB(t)
	mock := mockGitHubServer(t, "user1", "e@e.com", "", 1)
	registry := registerGitHubProvider(t, mock.URL)
	allowlist := auth.NewAllowlist("", true)
	e := setupCallbackEcho(t, db, registry, allowlist)

	tests := []struct {
		name string
		body string
	}{
		{"expires=45", `{"provider":"github","code":"c","redirect_uri":"http://localhost:3000/cb","expires":45}`},
		{"expires=-1", `{"provider":"github","code":"c","redirect_uri":"http://localhost:3000/cb","expires":-1}`},
		{"expires=7", `{"provider":"github","code":"c","redirect_uri":"http://localhost:3000/cb","expires":7}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doCallback(e, tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("expected HTTP 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TS-02-E4: POST /api/v1/auth/callback with non-allowlisted redirect_uri
// returns HTTP 400.
// Requirement: 02-REQ-2.E3
// ---------------------------------------------------------------------------

func TestCallback_NonAllowlistedRedirectURI(t *testing.T) {
	db := callbackTestDB(t)
	mock := mockGitHubServer(t, "user1", "e@e.com", "", 1)
	registry := registerGitHubProvider(t, mock.URL)
	// Production mode with specific external_url.
	allowlist := auth.NewAllowlist("https://app.example.com", false)
	e := setupCallbackEcho(t, db, registry, allowlist)

	body := `{"provider":"github","code":"c","redirect_uri":"https://attacker.com/cb"}`
	rec := doCallback(e, body)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected HTTP 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// TS-02-E5: POST /api/v1/auth/callback returns HTTP 502 when OAuth provider
// code exchange fails.
// Requirement: 02-REQ-2.E4
// ---------------------------------------------------------------------------

func TestCallback_CodeExchangeFailure(t *testing.T) {
	db := callbackTestDB(t)
	errorServer := mockGitHubErrorServer(t, http.StatusInternalServerError)
	registry := registerGitHubProvider(t, errorServer.URL)
	allowlist := auth.NewAllowlist("", true)
	e := setupCallbackEcho(t, db, registry, allowlist)

	body := `{"provider":"github","code":"bad-code","redirect_uri":"http://localhost:3000/cb"}`
	rec := doCallback(e, body)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected HTTP 502, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify no DB writes.
	var userCount int
	db.QueryRow("SELECT COUNT(*) FROM users").Scan(&userCount)
	if userCount != 0 {
		t.Errorf("expected 0 users, got %d", userCount)
	}
}

// ---------------------------------------------------------------------------
// TS-02-E6: POST /api/v1/auth/callback returns HTTP 400 when provider returns
// null or empty email.
// Requirement: 02-REQ-2.E5
// ---------------------------------------------------------------------------

func TestCallback_EmptyEmail(t *testing.T) {
	db := callbackTestDB(t)
	mock := mockGitHubUserInfoServer(t, "testuser", "", 12345) // empty email
	registry := registerGitHubProvider(t, mock.URL)
	allowlist := auth.NewAllowlist("", true)
	e := setupCallbackEcho(t, db, registry, allowlist)

	body := `{"provider":"github","code":"empty-email-code","redirect_uri":"http://localhost:3000/cb"}`
	rec := doCallback(e, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify no user created.
	var userCount int
	db.QueryRow("SELECT COUNT(*) FROM users").Scan(&userCount)
	if userCount != 0 {
		t.Errorf("expected 0 users, got %d", userCount)
	}
}

// ---------------------------------------------------------------------------
// TS-02-E7: POST /api/v1/auth/callback returns HTTP 400 when derived username
// from GitHub fails validation rules.
// Requirement: 02-REQ-2.E6
// ---------------------------------------------------------------------------

func TestCallback_InvalidUsername(t *testing.T) {
	db := callbackTestDB(t)
	mock := mockGitHubUserInfoServer(t, "invalid user!", "e@e.com", 12345) // space + exclamation
	registry := registerGitHubProvider(t, mock.URL)
	allowlist := auth.NewAllowlist("", true)
	e := setupCallbackEcho(t, db, registry, allowlist)

	body := `{"provider":"github","code":"invalid-login","redirect_uri":"http://localhost:3000/cb"}`
	rec := doCallback(e, body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify no user created.
	var userCount int
	db.QueryRow("SELECT COUNT(*) FROM users").Scan(&userCount)
	if userCount != 0 {
		t.Errorf("expected 0 users, got %d", userCount)
	}
}

// ---------------------------------------------------------------------------
// TS-02-E8: POST /api/v1/auth/callback returns HTTP 409 when derived username
// conflicts case-insensitively with a different (provider, provider_id).
// Requirement: 02-REQ-2.E7
// ---------------------------------------------------------------------------

func TestCallback_UsernameConflict(t *testing.T) {
	db := callbackTestDB(t)

	// Existing user "Alice" with a different provider_id.
	insertTestUser(t, db, "user-conflict-1", "Alice", "alice@example.com", "active", "github", "other-id-999")

	// Provider returns login="alice" (same case-insensitively) with a new provider_id.
	mock := mockGitHubServer(t, "alice", "alice2@example.com", "", 111)
	registry := registerGitHubProvider(t, mock.URL)
	allowlist := auth.NewAllowlist("", true)
	e := setupCallbackEcho(t, db, registry, allowlist)

	body := `{"provider":"github","code":"conflict-code","redirect_uri":"http://localhost:3000/cb"}`
	rec := doCallback(e, body)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected HTTP 409, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify no new user created for provider_id=111.
	var count int
	db.QueryRow("SELECT COUNT(*) FROM users WHERE provider_id = ?", "111").Scan(&count)
	if count != 0 {
		t.Error("no new user should be created for the conflicting provider_id")
	}
}

// ---------------------------------------------------------------------------
// TS-02-E9: POST /api/v1/auth/callback returns HTTP 403 for a blocked user.
// Requirement: 02-REQ-2.E8
// ---------------------------------------------------------------------------

func TestCallback_BlockedUser(t *testing.T) {
	db := callbackTestDB(t)

	// Existing blocked user with a numeric provider_id (GitHub returns
	// user IDs as JSON numbers, so we use a numeric string).
	insertTestUser(t, db, "user-blocked-1", "blockeduser", "b@e.com", "blocked", "github", "333333")

	mock := mockGitHubServer(t, "blockeduser", "b@e.com", "", 333333)
	registry := registerGitHubProvider(t, mock.URL)
	allowlist := auth.NewAllowlist("", true)
	e := setupCallbackEcho(t, db, registry, allowlist)

	body := `{"provider":"github","code":"blocked-code","redirect_uri":"http://localhost:3000/cb"}`
	rec := doCallback(e, body)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected HTTP 403, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify no new key created.
	var keyCount int
	db.QueryRow("SELECT COUNT(*) FROM api_keys WHERE user_id = ?", "user-blocked-1").Scan(&keyCount)
	if keyCount != 0 {
		t.Errorf("expected 0 keys for blocked user, got %d", keyCount)
	}

	// Verify status remains blocked.
	var status string
	db.QueryRow("SELECT status FROM users WHERE id = ?", "user-blocked-1").Scan(&status)
	if status != "blocked" {
		t.Errorf("status should remain 'blocked', got %q", status)
	}
}

// ---------------------------------------------------------------------------
// TS-02-E10: POST /api/v1/auth/callback returns HTTP 500 (or 400) when
// external_url is absent in production mode.
// Requirement: 02-REQ-2.E9
//
// NOTE: Per reviewer finding, 02-REQ-2.E9 is logically unreachable since
// dev mode IS when external_url is absent. This test verifies that the
// allowlist correctly returns an error when in production mode with no
// external_url configured — which is really a configuration error.
// ---------------------------------------------------------------------------

func TestCallback_ProductionModeNoExternalURL(t *testing.T) {
	db := callbackTestDB(t)
	mock := mockGitHubServer(t, "user1", "e@e.com", "", 1)
	registry := registerGitHubProvider(t, mock.URL)
	// Production mode with empty external_url = config error.
	allowlist := auth.NewAllowlist("", false)
	e := setupCallbackEcho(t, db, registry, allowlist)

	body := `{"provider":"github","code":"c","redirect_uri":"http://localhost:3000/cb"}`
	rec := doCallback(e, body)

	// The allowlist returns an error for this configuration; the callback
	// handler wraps it as HTTP 400 (redirect_uri not allowed).
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400 for config error, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// TS-02-E12: POST /api/v1/auth/callback returns HTTP 502 when provider code
// exchange times out.
// Requirement: 02-REQ-2.E11
// ---------------------------------------------------------------------------

func TestCallback_ProviderTimeout(t *testing.T) {
	db := callbackTestDB(t)

	// Use a channel to unblock the slow server handler when the test finishes.
	done := make(chan struct{})

	slowServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until either the test signals done or a generous timeout.
		select {
		case <-done:
		case <-time.After(30 * time.Second):
		}
	}))
	t.Cleanup(func() {
		close(done)
		slowServer.Close()
	})

	registry := auth.NewRegistry()
	cfg := auth.ProviderConfig{
		Name:         "github",
		TokenURL:     slowServer.URL + "/token",
		UserInfoURL:  slowServer.URL + "/userinfo",
		ClientID:     "id",
		ClientSecret: "secret",
	}
	provider := auth.NewGitHubProvider(cfg)
	registry.Register("github", provider, cfg)
	allowlist := auth.NewAllowlist("", true)
	e := setupCallbackEcho(t, db, registry, allowlist)

	// The callback handler creates a 10s child context timeout. We set a
	// much shorter parent context (50ms) to make the test fast. The child
	// context inherits the parent deadline.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/callback",
		strings.NewReader(`{"provider":"github","code":"timeout-code","redirect_uri":"http://localhost:3000/cb"}`))
	req.Header.Set("Content-Type", "application/json")

	ctx, cancel := context.WithTimeout(req.Context(), 50*time.Millisecond)
	defer cancel()
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected HTTP 502 for timeout, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// TS-02-10 (through callback): In dev mode, http://localhost with various
// ports is allowed; non-localhost is rejected.
// Requirement: 02-REQ-3.1
// ---------------------------------------------------------------------------

func TestCallback_DevModeRedirectURI(t *testing.T) {
	db := callbackTestDB(t)
	mock := mockGitHubServer(t, "devuser", "dev@example.com", "", 10001)
	registry := registerGitHubProvider(t, mock.URL)
	allowlist := auth.NewAllowlist("", true) // dev mode
	e := setupCallbackEcho(t, db, registry, allowlist)

	// localhost:8080 should work.
	rec1 := doCallback(e, `{"provider":"github","code":"c1","redirect_uri":"http://localhost:8080/cb"}`)
	if rec1.Code != http.StatusOK {
		t.Errorf("localhost:8080 should be allowed in dev mode, got %d", rec1.Code)
	}

	// Non-localhost should fail.
	// Use a different provider_id since user was already created.
	mock2 := mockGitHubServer(t, "devuser2", "dev2@example.com", "", 10002)
	registry2 := registerGitHubProvider(t, mock2.URL)
	e2 := setupCallbackEcho(t, db, registry2, allowlist)

	rec2 := doCallback(e2, `{"provider":"github","code":"c2","redirect_uri":"http://otherhost:3000/cb"}`)
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("non-localhost should be rejected in dev mode, got %d", rec2.Code)
	}
}

// ---------------------------------------------------------------------------
// TS-02-11 (through callback): In production mode, only matching external_url
// origin is allowed.
// Requirement: 02-REQ-3.2
// ---------------------------------------------------------------------------

func TestCallback_ProductionModeRedirectURI(t *testing.T) {
	db := callbackTestDB(t)
	mock := mockGitHubServer(t, "produser", "prod@example.com", "", 20001)
	registry := registerGitHubProvider(t, mock.URL)
	allowlist := auth.NewAllowlist("https://app.example.com", false) // production
	e := setupCallbackEcho(t, db, registry, allowlist)

	// Matching origin should work.
	rec1 := doCallback(e, `{"provider":"github","code":"c1","redirect_uri":"https://app.example.com/callback"}`)
	if rec1.Code != http.StatusOK {
		t.Errorf("matching origin should be allowed, got %d: %s", rec1.Code, rec1.Body.String())
	}

	// Non-matching port should fail.
	rec2 := doCallback(e, `{"provider":"github","code":"c2","redirect_uri":"https://app.example.com:8443/callback"}`)
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("non-matching port should be rejected, got %d", rec2.Code)
	}
}

// ---------------------------------------------------------------------------
// TS-02-12 (through callback): Path component is not considered in allowlist.
// Requirement: 02-REQ-3.3
// ---------------------------------------------------------------------------

func TestCallback_PathIgnoredInAllowlist(t *testing.T) {
	db := callbackTestDB(t)
	mock := mockGitHubServer(t, "pathuser", "path@example.com", "", 30001)
	registry := registerGitHubProvider(t, mock.URL)
	allowlist := auth.NewAllowlist("https://app.example.com", false)
	e := setupCallbackEcho(t, db, registry, allowlist)

	// Matching origin with path should work.
	rec1 := doCallback(e, `{"provider":"github","code":"c1","redirect_uri":"https://app.example.com/some/path?foo=bar"}`)
	if rec1.Code != http.StatusOK {
		t.Errorf("matching origin with path should be allowed, got %d: %s", rec1.Code, rec1.Body.String())
	}

	// Different host with same path should be rejected.
	rec2 := doCallback(e, `{"provider":"github","code":"c2","redirect_uri":"https://evil.example.com/some/path?foo=bar"}`)
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("different host should be rejected, got %d", rec2.Code)
	}
}

// ---------------------------------------------------------------------------
// Default expires: omitting the expires field defaults to 90 days.
// Requirement: 02-REQ-2.1
// ---------------------------------------------------------------------------

func TestCallback_DefaultExpires90Days(t *testing.T) {
	db := callbackTestDB(t)
	mock := mockGitHubServer(t, "defuser", "def@example.com", "", 40001)
	registry := registerGitHubProvider(t, mock.URL)
	allowlist := auth.NewAllowlist("", true)
	e := setupCallbackEcho(t, db, registry, allowlist)

	// Omit expires field.
	body := `{"provider":"github","code":"c","redirect_uri":"http://localhost:3000/cb"}`
	rec := doCallback(e, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseCallbackResponse(t, rec)
	apiKey := resp["api_key"].(map[string]interface{})

	expiresAt, ok := apiKey["expires_at"].(string)
	if !ok || expiresAt == "" {
		t.Fatal("expected non-null expires_at when expires defaults to 90")
	}

	parsed, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		t.Fatalf("failed to parse expires_at: %v", err)
	}
	diffDays := time.Until(parsed).Hours() / 24
	if diffDays < 89.9 || diffDays > 90.1 {
		t.Errorf("default expires should be ~90 days, got %.1f", diffDays)
	}
}

// ---------------------------------------------------------------------------
// expires=0 means indefinite (no expiry).
// Requirement: 02-REQ-2.4
// ---------------------------------------------------------------------------

func TestCallback_ExpiresZeroIndefinite(t *testing.T) {
	db := callbackTestDB(t)
	mock := mockGitHubServer(t, "noexpuser", "noexp@example.com", "", 50001)
	registry := registerGitHubProvider(t, mock.URL)
	allowlist := auth.NewAllowlist("", true)
	e := setupCallbackEcho(t, db, registry, allowlist)

	body := `{"provider":"github","code":"c","redirect_uri":"http://localhost:3000/cb","expires":0}`
	rec := doCallback(e, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseCallbackResponse(t, rec)
	apiKey := resp["api_key"].(map[string]interface{})

	if apiKey["expires_at"] != nil {
		t.Errorf("expected null expires_at for expires=0, got %v", apiKey["expires_at"])
	}
}

// ---------------------------------------------------------------------------
// TS-02-E13 (through callback): localhost.evil.com rejected in dev mode.
// Requirement: 02-REQ-3.E1
// ---------------------------------------------------------------------------

func TestCallback_LocalhostEvilRejected(t *testing.T) {
	db := callbackTestDB(t)
	mock := mockGitHubServer(t, "eviluser", "evil@example.com", "", 60001)
	registry := registerGitHubProvider(t, mock.URL)
	allowlist := auth.NewAllowlist("", true)
	e := setupCallbackEcho(t, db, registry, allowlist)

	body := `{"provider":"github","code":"c","redirect_uri":"http://localhost.evil.com:3000/cb"}`
	rec := doCallback(e, body)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected HTTP 400 for localhost.evil.com, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// TS-02-35 (through callback): Only SHA-256 hash stored; no plaintext in DB.
// Requirement: 02-REQ-11.2
// ---------------------------------------------------------------------------

func TestCallback_SecretNotStoredInPlaintext(t *testing.T) {
	db := callbackTestDB(t)
	mock := mockGitHubServer(t, "secuser", "sec@example.com", "", 70001)
	registry := registerGitHubProvider(t, mock.URL)
	allowlist := auth.NewAllowlist("", true)
	e := setupCallbackEcho(t, db, registry, allowlist)

	body := `{"provider":"github","code":"c","redirect_uri":"http://localhost:3000/cb"}`
	rec := doCallback(e, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseCallbackResponse(t, rec)
	secret := resp["api_key"].(map[string]interface{})["secret"].(string)
	keyID := resp["api_key"].(map[string]interface{})["key_id"].(string)

	// Verify DB has the hash.
	var dbHash string
	db.QueryRow("SELECT secret_hash FROM api_keys WHERE key_id = ?", keyID).Scan(&dbHash)
	if dbHash != sha256hex(secret) {
		t.Error("secret_hash should be SHA-256 of returned secret")
	}

	// Verify plaintext not in any column.
	rows, err := db.Query("SELECT id, key_id, secret_hash, user_id, expires_at, created_at, revoked_at FROM api_keys WHERE key_id = ?", keyID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	if rows.Next() {
		var id, kid, hash, uid string
		var expiresAt, createdAt, revokedAt sql.NullString
		rows.Scan(&id, &kid, &hash, &uid, &expiresAt, &createdAt, &revokedAt)

		// Check each column value doesn't contain the plaintext secret
		// (except secret_hash which is a hash, not the plaintext).
		for _, col := range []string{id, kid, uid} {
			if strings.Contains(col, secret) {
				t.Errorf("plaintext secret found in column value %q", col)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Existing user with no value changes: updated_at NOT bumped.
// Requirement: 02-REQ-2.3, 02-REQ-13.1
// ---------------------------------------------------------------------------

func TestCallback_ExistingUserNoChange_UpdatedAtNotBumped(t *testing.T) {
	db := callbackTestDB(t)

	// Create existing user with same values the provider will return.
	insertTestUser(t, db, "user-nochange-1", "sameuser", "same@example.com", "active", "github", "88888")

	var t0 string
	db.QueryRow("SELECT updated_at FROM users WHERE id = ?", "user-nochange-1").Scan(&t0)

	time.Sleep(10 * time.Millisecond)

	// Provider returns same username and email.
	mock := mockGitHubServer(t, "sameuser", "same@example.com", "", 88888)
	registry := registerGitHubProvider(t, mock.URL)
	allowlist := auth.NewAllowlist("", true)
	e := setupCallbackEcho(t, db, registry, allowlist)

	body := `{"provider":"github","code":"c","redirect_uri":"http://localhost:3000/cb"}`
	rec := doCallback(e, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var t1 string
	db.QueryRow("SELECT updated_at FROM users WHERE id = ?", "user-nochange-1").Scan(&t1)

	if t1 != t0 {
		t.Errorf("updated_at should not be bumped when no values changed; was %q now %q", t0, t1)
	}
}
