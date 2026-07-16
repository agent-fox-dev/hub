package auth_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/auth"
)

// ---------------------------------------------------------------------------
// Mock helpers for Google OAuth smoke tests
// ---------------------------------------------------------------------------

// mockGoogleServer creates an httptest.Server that serves both token exchange
// and user info endpoints for Google OAuth. The id, email, and name parameters
// control the userinfo response. Returns the server (cleaned up via t.Cleanup).
func mockGoogleServer(t *testing.T, id, email, name string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"access_token": "mock_google_access_token",
		})
	})

	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":    id,
			"email": email,
			"name":  name,
		})
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// registerGoogleProvider creates a registry with a Google provider pointing
// to the given mock server URLs for token exchange and userinfo.
func registerGoogleProvider(t *testing.T, mockServerURL string) *auth.Registry {
	t.Helper()
	registry := auth.NewRegistry()
	cfg := auth.ProviderConfig{
		Name:         "google",
		TokenURL:     mockServerURL + "/token",
		UserInfoURL:  mockServerURL + "/userinfo",
		ClientID:     "test-google-client-id",
		ClientSecret: "test-google-client-secret",
	}
	provider := auth.NewGoogleProvider(cfg)
	registry.Register("google", provider, cfg)
	return registry
}

// ---------------------------------------------------------------------------
// TS-07-SMOKE-1: End-to-end smoke test of a successful Google OAuth login flow
// using mock HTTP servers for the Google token and userinfo endpoints.
//
// Execution Path: 07-PATH-1
// Requirements: 07-REQ-3.1, 07-REQ-4.1, 07-REQ-4.2, 07-REQ-5.1
//
// Verifies:
//   - ExchangeCode returns a non-empty access token
//   - GetUserInfo returns *UserInfo with correct field mapping
//   - User record is created in the database with provider_id, username,
//     email, full_name
//   - Session token (API key) is issued
//   - No errors from any step
// ---------------------------------------------------------------------------

func TestGoogleSmoke_FullLoginFlow(t *testing.T) {
	db := callbackTestDB(t)
	mock := mockGoogleServer(t,
		"117730543842840592312",
		"jane.doe+work@gmail.com",
		"Jane Doe",
	)
	registry := registerGoogleProvider(t, mock.URL)
	allowlist := auth.NewAllowlist("", true) // dev mode
	e := setupCallbackEcho(t, db, registry, allowlist)

	body := `{"provider":"google","code":"valid-google-code","redirect_uri":"http://localhost:3000/callback","expires":30}`
	rec := doCallback(e, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	resp := parseCallbackResponse(t, rec)

	// ---- Verify user object ----
	user, ok := resp["user"].(map[string]interface{})
	if !ok {
		t.Fatal("expected 'user' object in response")
	}
	if !isUUIDv4(user["id"].(string)) {
		t.Errorf("expected UUID v4 id, got %v", user["id"])
	}
	if user["username"] != "janedoework" {
		t.Errorf("expected username 'janedoework', got %v", user["username"])
	}
	if user["email"] != "jane.doe+work@gmail.com" {
		t.Errorf("expected email 'jane.doe+work@gmail.com', got %v", user["email"])
	}
	if user["full_name"] != "Jane Doe" {
		t.Errorf("expected full_name 'Jane Doe', got %v", user["full_name"])
	}
	if user["status"] != "active" {
		t.Errorf("expected status 'active', got %v", user["status"])
	}
	if user["provider"] != "google" {
		t.Errorf("expected provider 'google', got %v", user["provider"])
	}

	// ---- Verify API key object ----
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

	// ---- Verify database state ----
	var dbProviderID, dbUsername, dbEmail, dbFullName, dbProvider string
	err := db.QueryRow(
		"SELECT provider_id, username, email, full_name, provider FROM users WHERE id = ?",
		user["id"],
	).Scan(&dbProviderID, &dbUsername, &dbEmail, &dbFullName, &dbProvider)
	if err != nil {
		t.Fatalf("user not found in DB: %v", err)
	}
	if dbProviderID != "117730543842840592312" {
		t.Errorf("expected provider_id '117730543842840592312', got %q", dbProviderID)
	}
	if dbUsername != "janedoework" {
		t.Errorf("expected username 'janedoework' in DB, got %q", dbUsername)
	}
	if dbEmail != "jane.doe+work@gmail.com" {
		t.Errorf("expected email 'jane.doe+work@gmail.com' in DB, got %q", dbEmail)
	}
	if dbFullName != "Jane Doe" {
		t.Errorf("expected full_name 'Jane Doe' in DB, got %q", dbFullName)
	}
	if dbProvider != "google" {
		t.Errorf("expected provider 'google' in DB, got %q", dbProvider)
	}

	// Verify API key was stored.
	var dbSecretHash string
	err = db.QueryRow("SELECT secret_hash FROM api_keys WHERE key_id = ?", keyID).Scan(&dbSecretHash)
	if err != nil {
		t.Fatalf("api key not found in DB: %v", err)
	}
	if dbSecretHash != sha256hex(secret) {
		t.Error("DB secret_hash should be SHA-256 of returned secret")
	}
}

// ---------------------------------------------------------------------------
// TS-07-SMOKE-2: Smoke test of the error path where the Google user's email
// local part sanitizes to an empty string, resulting in GetUserInfo returning
// an error and the callback handler returning HTTP 502.
//
// Execution Path: 07-PATH-2
// Requirements: 07-REQ-5.2, 07-REQ-5.E2
//
// Verifies:
//   - ExchangeCode returns a non-empty access token without error
//   - GetUserInfo returns (nil, error) for unsanitizable email
//   - Callback handler responds with HTTP 502
//   - No user record is created in the database
// ---------------------------------------------------------------------------

func TestGoogleSmoke_EmptyUsernameError(t *testing.T) {
	db := callbackTestDB(t)
	// Email "...@gmail.com" — local part contains only dots, which are
	// stripped during sanitization, yielding an empty username.
	mock := mockGoogleServer(t, "123", "...@gmail.com", "Dot User")
	registry := registerGoogleProvider(t, mock.URL)
	allowlist := auth.NewAllowlist("", true) // dev mode
	e := setupCallbackEcho(t, db, registry, allowlist)

	body := `{"provider":"google","code":"dot-email-code","redirect_uri":"http://localhost:3000/callback"}`
	rec := doCallback(e, body)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected HTTP 502 for empty-sanitized username, got %d: %s",
			rec.Code, rec.Body.String())
	}

	// Verify the error message is descriptive.
	resp := parseCallbackResponse(t, rec)
	errObj, ok := resp["error"].(map[string]interface{})
	if !ok {
		t.Fatal("expected structured error body")
	}
	errMsg, _ := errObj["message"].(string)
	if errMsg == "" {
		t.Error("expected non-empty error message")
	}
	errLower := strings.ToLower(errMsg)
	if !strings.Contains(errLower, "username") && !strings.Contains(errLower, "deriv") && !strings.Contains(errLower, "empty") {
		t.Errorf("error message %q should reference username derivation failure", errMsg)
	}

	// Verify no user record was created.
	var userCount int
	db.QueryRow("SELECT COUNT(*) FROM users").Scan(&userCount)
	if userCount != 0 {
		t.Errorf("expected 0 users in DB, got %d", userCount)
	}
}

// ---------------------------------------------------------------------------
// TS-07-SMOKE-3: Smoke test of server startup with a Google provider entry,
// verifying it is registered in the Registry and available via the providers
// API.
//
// Execution Path: 07-PATH-3
// Requirements: 07-REQ-6.1, 07-REQ-6.2
//
// Verifies:
//   - Server starts without error or panic (no real Google servers contacted)
//   - Registry contains a GoogleProvider under the name "google"
//   - GET /api/v1/auth/providers returns a response including name=="google"
//   - The authorize_url contains the Google default endpoint
//   - The scopes include openid, email, profile
// ---------------------------------------------------------------------------

func TestGoogleSmoke_ProviderRegistration(t *testing.T) {
	// Simulate the startup sequence from main.go:
	// 1. Create registry
	// 2. Build ProviderConfig (mirroring main.go's conversion from OAuthProvider)
	// 3. Register GoogleProvider
	// 4. Wire up providers API handler
	// 5. Query providers endpoint

	registry := auth.NewRegistry()
	cfg := auth.ProviderConfig{
		Name:         "google",
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		// No URL overrides — defaults should apply.
	}
	provider := auth.NewGoogleProvider(cfg)
	registry.Register("google", provider, cfg)

	// Verify Lookup returns the provider.
	p, ok := registry.Lookup("google")
	if !ok {
		t.Fatal("expected 'google' to be registered in the registry")
	}
	if p == nil {
		t.Fatal("Lookup returned nil provider for 'google'")
	}

	// Set up an Echo instance with just the providers endpoint.
	e := echo.New()
	e.GET("/api/v1/auth/providers", auth.GetProvidersHandler(registry))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/providers", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Providers []struct {
			Name         string `json:"name"`
			AuthorizeURL string `json:"authorize_url"`
			Scopes       string `json:"scopes"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse providers response: %v\nbody: %s", err, rec.Body.String())
	}

	if len(resp.Providers) == 0 {
		t.Fatal("expected at least one provider, got 0")
	}

	// Find the Google provider entry.
	var found bool
	for _, p := range resp.Providers {
		if p.Name == "google" {
			found = true

			// Verify authorize_url starts with the Google default.
			if !strings.HasPrefix(p.AuthorizeURL, "https://accounts.google.com/o/oauth2/v2/auth") {
				t.Errorf("authorize_url = %q, want prefix 'https://accounts.google.com/o/oauth2/v2/auth'",
					p.AuthorizeURL)
			}
			// Verify client_id is included in authorize_url.
			if !strings.Contains(p.AuthorizeURL, "client_id=test-client-id") {
				t.Errorf("authorize_url = %q, should contain client_id=test-client-id", p.AuthorizeURL)
			}
			// Verify scopes include openid, email, profile.
			for _, scope := range []string{"openid", "email", "profile"} {
				if !strings.Contains(p.Scopes, scope) {
					t.Errorf("scopes = %q, missing required scope %q", p.Scopes, scope)
				}
			}
			break
		}
	}
	if !found {
		t.Errorf("providers response did not include a 'google' entry: %+v", resp.Providers)
	}
}
