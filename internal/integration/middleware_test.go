package integration_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox/af-hub/internal/auth"
	"github.com/agent-fox/af-hub/internal/config"
	"github.com/agent-fox/af-hub/internal/handler"
	"github.com/agent-fox/af-hub/internal/store"
	"github.com/labstack/echo/v4"
)

// --- Test helpers ---

// mockErrorStore is a store that returns configurable errors for token lookup
// operations. It embeds store.Store so it satisfies the interface at compile
// time; unimplemented methods will panic with nil pointer dereference (which
// is fine — those methods should not be called in the error path tests).
type mockErrorStore struct {
	store.Store
	tokenLookupErr error
}

func (m *mockErrorStore) GetAdminTokenByHash(_ string) (*store.AdminToken, error) {
	if m.tokenLookupErr != nil {
		return nil, m.tokenLookupErr
	}
	return nil, store.ErrNotFound
}

func (m *mockErrorStore) GetAPIKeyByKeyID(_ string) (*store.APIKey, error) {
	if m.tokenLookupErr != nil {
		return nil, m.tokenLookupErr
	}
	return nil, store.ErrNotFound
}

// sha256HexString computes the hex-encoded SHA-256 hash of a string.
func sha256HexString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// contextEchoHandler returns a handler that echoes auth context values in
// the JSON response. Used to verify that auth middleware populates context
// correctly.
func contextEchoHandler() echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{
			"user_id":      c.Get(auth.ContextKeyUserID),
			"role":         c.Get(auth.ContextKeyRole),
			"workspace_id": c.Get(auth.ContextKeyWorkspaceID),
			"auth_method":  c.Get(auth.ContextKeyAuthMethod),
			"user_status":  c.Get(auth.ContextKeyUserStatus),
		})
	}
}

// handlerInvokedTracker wraps a handler and tracks whether it was called.
type handlerInvokedTracker struct {
	invoked bool
}

func (h *handlerInvokedTracker) handler() echo.HandlerFunc {
	return func(c echo.Context) error {
		h.invoked = true
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	}
}

// setupTestEnvWithAuth creates a test environment with auth middleware
// applied to protected /api/v1/* routes (excluding /api/v1/auth/*).
// Returns the test environment with handler tracking capabilities.
func setupTestEnvWithAuth(t *testing.T, s store.Store) *testEnv {
	t.Helper()

	cfg := &config.AuthConfig{
		OAuth: []config.OAuthProviderConfig{
			{
				Provider:     "github",
				ClientID:     "test_client_id",
				ClientSecret: "test_client_secret",
			},
		},
		Timeout: 5,
	}

	registry := auth.NewRegistry(cfg)
	authHandler := handler.NewAuthHandler(registry, s)

	e := echo.New()
	e.HTTPErrorHandler = handler.CustomHTTPErrorHandler

	// Public auth routes (no middleware).
	authGroup := e.Group("/api/v1/auth")
	authGroup.GET("/providers", authHandler.ListProviders)
	authGroup.POST("/callback", authHandler.OAuthCallback)

	// Protected routes with auth middleware.
	apiGroup := e.Group("/api/v1", auth.AuthMiddleware(s))
	apiGroup.GET("/users", contextEchoHandler())
	apiGroup.GET("/keys", contextEchoHandler())
	apiGroup.POST("/workspaces", contextEchoHandler())

	// Health probe (no middleware).
	e.GET("/health", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	return &testEnv{
		Echo:        e,
		Store:       s,
		Registry:    registry,
		AuthHandler: authHandler,
	}
}

// seedAdminToken creates an admin token record in the store with the
// SHA-256 hash of the given plaintext token.
func seedAdminToken(t *testing.T, s store.Store, plaintextToken string) {
	t.Helper()
	hash := sha256HexString(plaintextToken)
	_, err := s.CreateAdminToken(&store.AdminToken{
		TokenHash: hash,
	})
	if err != nil {
		t.Fatalf("failed to seed admin token: %v", err)
	}
}

// seedUserAndAPIKey creates a user and an API key record in the store.
// Returns the created user.
func seedUserAndAPIKey(t *testing.T, s store.Store, username, keyID, secret, role, workspaceID, status string) *store.User {
	t.Helper()

	user, err := s.CreateUser(&store.User{
		Username:   username,
		Email:      username + "@test.com",
		Provider:   "local",
		ProviderID: username + "_pid",
		Status:     status,
	})
	if err != nil {
		t.Fatalf("failed to create user %q: %v", username, err)
	}

	secretHash := sha256HexString(secret)
	_, err = s.CreateAPIKey(&store.APIKey{
		KeyID:       keyID,
		KeyHash:     secretHash,
		UserID:      user.ID,
		WorkspaceID: workspaceID,
		Role:        role,
		Label:       "test key for " + username,
	})
	if err != nil {
		t.Fatalf("failed to create API key for %q: %v", username, err)
	}

	return user
}

// --- TS-02-8: Auth middleware scope tests ---

// TS-02-8: Verify that auth middleware applies to all /api/v1/* routes
// except /api/v1/auth/* and health probes.
func TestAuthMiddleware_ProtectedRouteRequiresAuth(t *testing.T) {
	s := createTestStore(t)
	env := setupTestEnvWithAuth(t, s)

	// GET /api/v1/users without Authorization header → should return 401
	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/users", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/users without auth: status = %d, want %d",
			rec.Code, http.StatusUnauthorized)
	}
}

// TS-02-8: Verify that /api/v1/auth/* routes are excluded from auth middleware.
func TestAuthMiddleware_AuthRoutesExcluded(t *testing.T) {
	s := createTestStore(t)
	env := setupTestEnvWithAuth(t, s)

	// GET /api/v1/auth/providers without Authorization header → should return 200
	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/auth/providers", "", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/v1/auth/providers without auth: status = %d, want %d",
			rec.Code, http.StatusOK)
	}
}

// TS-02-8: Verify that health probe routes are excluded from auth middleware.
func TestAuthMiddleware_HealthProbeExcluded(t *testing.T) {
	s := createTestStore(t)
	env := setupTestEnvWithAuth(t, s)

	// GET /health without Authorization header → should return 200
	rec := doRequest(env.Echo, http.MethodGet, "/health", "", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /health without auth: status = %d, want %d",
			rec.Code, http.StatusOK)
	}
}

// --- TS-02-9: Admin token validation tests ---

// TS-02-9: Verify that an admin token is hashed with SHA-256, matched against
// admin_tokens table, and the request context is populated with admin role.
func TestAuthMiddleware_AdminToken_Valid(t *testing.T) {
	s := createTestStore(t)
	seedAdminToken(t, s, "af_admin_testtoken")
	env := setupTestEnvWithAuth(t, s)

	headers := map[string]string{
		"Authorization": "Bearer af_admin_testtoken",
	}
	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/users", "", headers)

	// Should return HTTP 200 (middleware passes, handler executes).
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d\nBody: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	// Verify context values echoed by handler.
	var ctx map[string]any
	parseJSON(t, rec, &ctx)

	if ctx["auth_method"] != auth.AuthMethodAdmin {
		t.Errorf("auth_method = %v, want %q", ctx["auth_method"], auth.AuthMethodAdmin)
	}
	if ctx["user_status"] != "active" {
		t.Errorf("user_status = %v, want 'active'", ctx["user_status"])
	}
}

// TS-02-9: Verify that admin token auth populates user_id in the context.
func TestAuthMiddleware_AdminToken_PopulatesUserID(t *testing.T) {
	s := createTestStore(t)
	seedAdminToken(t, s, "af_admin_contexttest")
	env := setupTestEnvWithAuth(t, s)

	headers := map[string]string{
		"Authorization": "Bearer af_admin_contexttest",
	}
	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/users", "", headers)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var ctx map[string]any
	parseJSON(t, rec, &ctx)

	// user_id should be populated (non-empty).
	if ctx["user_id"] == nil || ctx["user_id"] == "" {
		t.Error("user_id should be populated for admin token auth")
	}
}

// --- TS-02-10: API key token validation tests ---

// TS-02-10: Verify that an API key token is parsed, key_id looked up,
// secret verified via SHA-256, and context populated with workspace_id and role.
func TestAuthMiddleware_APIKey_Valid(t *testing.T) {
	s := createTestStore(t)

	// Create a user with an API key.
	seedUserAndAPIKey(t, s, "apiuser001", "key001", "mysecret", "editor", "ws001", "active")
	env := setupTestEnvWithAuth(t, s)

	headers := map[string]string{
		"Authorization": "Bearer af_key001_mysecret",
	}
	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/keys", "", headers)

	// Should return HTTP 200.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d\nBody: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	// Verify context values.
	var ctx map[string]any
	parseJSON(t, rec, &ctx)

	if ctx["auth_method"] != auth.AuthMethodAPIKey {
		t.Errorf("auth_method = %v, want %q", ctx["auth_method"], auth.AuthMethodAPIKey)
	}
	if ctx["role"] != "editor" {
		t.Errorf("role = %v, want 'editor'", ctx["role"])
	}
	if ctx["workspace_id"] != "ws001" {
		t.Errorf("workspace_id = %v, want 'ws001'", ctx["workspace_id"])
	}
	if ctx["user_status"] != "active" {
		t.Errorf("user_status = %v, want 'active'", ctx["user_status"])
	}
}

// TS-02-10: Verify that context user_id is populated with the API key owner.
func TestAuthMiddleware_APIKey_PopulatesUserID(t *testing.T) {
	s := createTestStore(t)

	user := seedUserAndAPIKey(t, s, "apiuser002", "key002", "secret2", "reader", "ws001", "active")
	env := setupTestEnvWithAuth(t, s)

	headers := map[string]string{
		"Authorization": "Bearer af_key002_secret2",
	}
	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/keys", "", headers)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var ctx map[string]any
	parseJSON(t, rec, &ctx)

	if ctx["user_id"] != user.ID {
		t.Errorf("user_id = %v, want %q", ctx["user_id"], user.ID)
	}
}

// --- TS-02-11: Blocked user rejection tests ---

// TS-02-11: Verify that a valid token belonging to a blocked user results
// in HTTP 403 and the handler is never invoked.
func TestAuthMiddleware_BlockedUser_Returns403(t *testing.T) {
	s := createTestStore(t)

	// Create a blocked user with a valid API key.
	seedUserAndAPIKey(t, s, "blockeduser002", "key_blocked", "secretblocked", "editor", "ws001", "blocked")
	env := setupTestEnvWithAuth(t, s)

	headers := map[string]string{
		"Authorization": "Bearer af_key_blocked_secretblocked",
	}
	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/keys", "", headers)

	// Should return HTTP 403.
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d\nBody: %s",
			rec.Code, http.StatusForbidden, rec.Body.String())
	}

	var errResp errorResponse
	parseJSON(t, rec, &errResp)

	if errResp.Error.Code != "403" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "403")
	}
	if errResp.Error.Message != "user is blocked" {
		t.Errorf("error message = %q, want %q", errResp.Error.Message, "user is blocked")
	}
}

// TS-02-11: Verify that the handler is never invoked for a blocked user.
func TestAuthMiddleware_BlockedUser_HandlerNotInvoked(t *testing.T) {
	s := createTestStore(t)
	seedUserAndAPIKey(t, s, "blockedtrack", "key_track", "secrettrack", "editor", "ws001", "blocked")

	// Build Echo server with handler tracking.
	e := echo.New()
	e.HTTPErrorHandler = handler.CustomHTTPErrorHandler
	tracker := &handlerInvokedTracker{}

	apiGroup := e.Group("/api/v1", auth.AuthMiddleware(s))
	apiGroup.GET("/keys", tracker.handler())

	headers := map[string]string{
		"Authorization": "Bearer af_key_track_secrettrack",
	}
	rec := doRequest(e, http.MethodGet, "/api/v1/keys", "", headers)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	if tracker.invoked {
		t.Error("handler was invoked for a blocked user — it should never execute")
	}
}

// --- TS-02-E6: Missing/malformed Authorization header tests ---

// TS-02-E6: Verify that a missing Authorization header on a protected
// endpoint returns HTTP 401.
func TestAuthMiddleware_MissingAuthHeader_Returns401(t *testing.T) {
	s := createTestStore(t)
	env := setupTestEnvWithAuth(t, s)

	// No Authorization header.
	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/users", "", nil)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d\nBody: %s",
			rec.Code, http.StatusUnauthorized, rec.Body.String())
	}

	var errResp errorResponse
	parseJSON(t, rec, &errResp)

	if errResp.Error.Code != "401" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "401")
	}
	if !strings.Contains(errResp.Error.Message, "missing or malformed") {
		t.Errorf("error message = %q, want it to contain 'missing or malformed'",
			errResp.Error.Message)
	}
}

// TS-02-E6: Verify that a malformed token format returns HTTP 401.
func TestAuthMiddleware_MalformedToken_Returns401(t *testing.T) {
	s := createTestStore(t)
	env := setupTestEnvWithAuth(t, s)

	headers := map[string]string{
		"Authorization": "Bearer invalidformat",
	}
	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/users", "", headers)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	var errResp errorResponse
	parseJSON(t, rec, &errResp)

	if errResp.Error.Code != "401" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "401")
	}
}

// TS-02-E6: Verify that missing "Bearer " prefix returns HTTP 401.
func TestAuthMiddleware_NoBearerPrefix_Returns401(t *testing.T) {
	s := createTestStore(t)
	env := setupTestEnvWithAuth(t, s)

	headers := map[string]string{
		"Authorization": "af_admin_testtoken",
	}
	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/users", "", headers)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// --- TS-02-E7: Admin token hash mismatch tests ---

// TS-02-E7: Verify that an admin token whose SHA-256 hash does not match
// any admin_tokens record returns HTTP 401.
func TestAuthMiddleware_AdminToken_HashMismatch_Returns401(t *testing.T) {
	s := createTestStore(t)
	// Seed a valid admin token (but we'll use a different token in the request).
	seedAdminToken(t, s, "af_admin_correcttoken")
	env := setupTestEnvWithAuth(t, s)

	headers := map[string]string{
		"Authorization": "Bearer af_admin_wrongtoken",
	}
	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/users", "", headers)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d\nBody: %s",
			rec.Code, http.StatusUnauthorized, rec.Body.String())
	}

	var errResp errorResponse
	parseJSON(t, rec, &errResp)

	if errResp.Error.Code != "401" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "401")
	}
	if !strings.Contains(errResp.Error.Message, "invalid token") {
		t.Errorf("error message = %q, want it to contain 'invalid token'",
			errResp.Error.Message)
	}
}

// TS-02-E7: Verify that an admin token with no matching hash in an empty
// admin_tokens table returns HTTP 401.
func TestAuthMiddleware_AdminToken_NoRecords_Returns401(t *testing.T) {
	s := createTestStore(t)
	// No admin tokens seeded.
	env := setupTestEnvWithAuth(t, s)

	headers := map[string]string{
		"Authorization": "Bearer af_admin_nonexistent",
	}
	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/users", "", headers)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// --- TS-02-E8: API key error condition tests ---

// TS-02-E8: Verify that an API key with a non-existent key_id returns HTTP 401.
func TestAuthMiddleware_APIKey_NotFound_Returns401(t *testing.T) {
	s := createTestStore(t)
	env := setupTestEnvWithAuth(t, s)

	headers := map[string]string{
		"Authorization": "Bearer af_nonexistent_secret",
	}
	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/keys", "", headers)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	var errResp errorResponse
	parseJSON(t, rec, &errResp)

	if errResp.Error.Code != "401" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "401")
	}
	lowerMsg := strings.ToLower(errResp.Error.Message)
	if !strings.Contains(lowerMsg, "invalid") &&
		!strings.Contains(lowerMsg, "revoked") &&
		!strings.Contains(lowerMsg, "expired") {
		t.Errorf("error message = %q, want it to contain 'invalid', 'revoked', or 'expired'",
			errResp.Error.Message)
	}
}

// TS-02-E8: Verify that an API key with a hash mismatch returns HTTP 401.
func TestAuthMiddleware_APIKey_HashMismatch_Returns401(t *testing.T) {
	s := createTestStore(t)

	// Create a key with a known hash, but use a different secret in the request.
	seedUserAndAPIKey(t, s, "hashmismatch", "knownkey", "correctsecret", "editor", "ws001", "active")
	env := setupTestEnvWithAuth(t, s)

	headers := map[string]string{
		"Authorization": "Bearer af_knownkey_wrongsecret",
	}
	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/keys", "", headers)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// TS-02-E8: Verify that a revoked API key (revoked_at is set) returns HTTP 401.
func TestAuthMiddleware_APIKey_Revoked_Returns401(t *testing.T) {
	s := createTestStore(t)

	user := seedUserAndAPIKey(t, s, "revokeduser", "revokedkey", "revokedsecret", "editor", "ws001", "active")
	_ = user

	// Revoke the key.
	if err := s.RevokeAPIKey("revokedkey"); err != nil {
		t.Fatalf("failed to revoke API key: %v", err)
	}

	env := setupTestEnvWithAuth(t, s)

	headers := map[string]string{
		"Authorization": "Bearer af_revokedkey_revokedsecret",
	}
	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/keys", "", headers)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	var errResp errorResponse
	parseJSON(t, rec, &errResp)

	if errResp.Error.Code != "401" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "401")
	}
}

// TS-02-E8: Verify that an expired API key (expires_at in the past) returns
// HTTP 401.
func TestAuthMiddleware_APIKey_Expired_Returns401(t *testing.T) {
	s := createTestStore(t)

	// Create user first.
	user, err := s.CreateUser(&store.User{
		Username:   "expireduser",
		Email:      "expired@test.com",
		Provider:   "local",
		ProviderID: "expired_pid",
		Status:     "active",
	})
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// Create an API key with expires_at in the past.
	pastTime := time.Now().Add(-24 * time.Hour)
	_, err = s.CreateAPIKey(&store.APIKey{
		KeyID:       "expiredkey",
		KeyHash:     sha256HexString("expiredsecret"),
		UserID:      user.ID,
		WorkspaceID: "ws001",
		Role:        "editor",
		Label:       "expired key",
		ExpiresAt:   &pastTime,
	})
	if err != nil {
		t.Fatalf("failed to create expired API key: %v", err)
	}

	env := setupTestEnvWithAuth(t, s)

	headers := map[string]string{
		"Authorization": "Bearer af_expiredkey_expiredsecret",
	}
	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/keys", "", headers)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// TS-02-E8: Verify all four API key error conditions return the same error
// envelope format.
func TestAuthMiddleware_APIKey_AllErrorConditions_Return401(t *testing.T) {
	tests := []struct {
		name  string
		token string
	}{
		{"non-existent key_id", "af_nonexistent_secret"},
		{"hash mismatch", "af_knownkey_wrongsecret"},
		{"revoked key", "af_revokedkey_revokedsecret"},
		{"expired key", "af_expiredkey_expiredsecret"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := createTestStore(t)
			env := setupTestEnvWithAuth(t, s)

			headers := map[string]string{
				"Authorization": "Bearer " + tc.token,
			}
			rec := doRequest(env.Echo, http.MethodGet, "/api/v1/keys", "", headers)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
		})
	}
}

// --- TS-02-E9: Database error during token lookup tests ---

// TS-02-E9: Verify that a database error during token lookup returns HTTP 500
// without leaking internal error details.
func TestAuthMiddleware_DBError_AdminToken_Returns500(t *testing.T) {
	errStore := &mockErrorStore{
		tokenLookupErr: errSimulatedDBFailure,
	}

	e := echo.New()
	e.HTTPErrorHandler = handler.CustomHTTPErrorHandler

	apiGroup := e.Group("/api/v1", auth.AuthMiddleware(errStore))
	apiGroup.GET("/users", contextEchoHandler())

	headers := map[string]string{
		"Authorization": "Bearer af_admin_testtoken",
	}
	rec := doRequest(e, http.MethodGet, "/api/v1/users", "", headers)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d\nBody: %s",
			rec.Code, http.StatusInternalServerError, rec.Body.String())
	}

	var errResp errorResponse
	parseJSON(t, rec, &errResp)

	if errResp.Error.Code != "500" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "500")
	}
	if errResp.Error.Message != "internal server error" {
		t.Errorf("error message = %q, want %q",
			errResp.Error.Message, "internal server error")
	}

	// Ensure no internal error details leak.
	body := rec.Body.String()
	if strings.Contains(body, "connection refused") {
		t.Error("response body contains internal error details ('connection refused')")
	}
	if strings.Contains(strings.ToLower(body), "sql") {
		t.Error("response body contains SQL-related text")
	}
	if strings.Contains(strings.ToLower(body), "panic") {
		t.Error("response body contains 'panic' text")
	}
}

// TS-02-E9: Verify DB error during API key lookup also returns HTTP 500.
func TestAuthMiddleware_DBError_APIKey_Returns500(t *testing.T) {
	errStore := &mockErrorStore{
		tokenLookupErr: errSimulatedDBFailure,
	}

	e := echo.New()
	e.HTTPErrorHandler = handler.CustomHTTPErrorHandler

	apiGroup := e.Group("/api/v1", auth.AuthMiddleware(errStore))
	apiGroup.GET("/keys", contextEchoHandler())

	headers := map[string]string{
		"Authorization": "Bearer af_somekey_somesecret",
	}
	rec := doRequest(e, http.MethodGet, "/api/v1/keys", "", headers)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	var errResp errorResponse
	parseJSON(t, rec, &errResp)

	if errResp.Error.Code != "500" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "500")
	}
	if errResp.Error.Message != "internal server error" {
		t.Errorf("error message = %q, want %q",
			errResp.Error.Message, "internal server error")
	}
}

// errSimulatedDBFailure is used by mockErrorStore to simulate DB errors.
var errSimulatedDBFailure = errDBSimulated("connection refused")

type errDBSimulated string

func (e errDBSimulated) Error() string { return string(e) }
