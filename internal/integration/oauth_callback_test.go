package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/agent-fox/af-hub/internal/config"
	"github.com/agent-fox/af-hub/internal/store"
)

// TS-02-6: Verify that POST /api/v1/auth/callback exchanges the authorization
// code, upserts the user, and returns the user object.
func TestOAuthCallback_CreatesNewUser(t *testing.T) {
	// Set up mock GitHub server with valid token and user info responses.
	mockGitHub := setupMockGitHubServer(t,
		`{"access_token": "mock_access_token", "token_type": "bearer"}`,
		`{"id": 99999, "login": "newghuser", "email": "newghuser@example.com", "name": "New GH User"}`,
		http.StatusOK,
		http.StatusOK,
	)
	defer mockGitHub.Close()

	env := setupTestEnv(t, []config.OAuthProviderConfig{
		{
			Provider:     "github",
			ClientID:     "test_client_id",
			ClientSecret: "test_client_secret",
			TokenURL:     mockGitHub.URL + "/login/oauth/access_token",
			UserinfoURL:  mockGitHub.URL + "/api/user",
		},
	})

	body := `{"provider": "github", "code": "validcode123", "redirect_uri": "http://localhost:9999/callback"}`
	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/auth/callback", body, nil)

	// Should return HTTP 200
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/auth/callback status = %d, want %d\nBody: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	// Parse the user object from the response
	var user userResponse
	parseJSON(t, rec, &user)

	// Verify user fields
	if user.Username == "" {
		t.Error("response user.username is empty")
	}
	if user.Email == "" {
		t.Error("response user.email is empty")
	}
	if user.Provider != "github" {
		t.Errorf("response user.provider = %q, want %q", user.Provider, "github")
	}
	if user.Status != "active" {
		t.Errorf("response user.status = %q, want %q", user.Status, "active")
	}
	if user.ProviderID == "" {
		t.Error("response user.provider_id is empty")
	}

	// Verify the user record was created in the database
	dbUser, err := env.Store.GetUserByProviderID("github", user.ProviderID)
	if err != nil {
		t.Fatalf("user not found in database: %v", err)
	}
	if dbUser == nil {
		t.Fatal("user record is nil in database")
	}
	if dbUser.Status != "active" {
		t.Errorf("db user status = %q, want 'active'", dbUser.Status)
	}
}

// TS-02-6: Verify that calling OAuth callback again for the same user
// updates existing fields (upsert behavior).
func TestOAuthCallback_UpsertsExistingUser(t *testing.T) {
	mockGitHub := setupMockGitHubServer(t,
		`{"access_token": "mock_access_token", "token_type": "bearer"}`,
		`{"id": 55555, "login": "existinguser", "email": "updated@example.com", "name": "Updated Name"}`,
		http.StatusOK,
		http.StatusOK,
	)
	defer mockGitHub.Close()

	env := setupTestEnv(t, []config.OAuthProviderConfig{
		{
			Provider:     "github",
			ClientID:     "test_client_id",
			ClientSecret: "test_client_secret",
			TokenURL:     mockGitHub.URL + "/login/oauth/access_token",
			UserinfoURL:  mockGitHub.URL + "/api/user",
		},
	})

	// Pre-create the user in the database
	existingUser := &store.User{
		Username:   "existinguser",
		Email:      "original@example.com",
		Provider:   "github",
		ProviderID: "55555",
		Status:     "active",
	}
	_, err := env.Store.CreateUser(existingUser)
	if err != nil {
		t.Fatalf("failed to create existing user: %v", err)
	}

	// Call the callback — should upsert (update) the existing user
	body := `{"provider": "github", "code": "validcode456", "redirect_uri": "http://localhost:9999/callback"}`
	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/auth/callback", body, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d\nBody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var user userResponse
	parseJSON(t, rec, &user)

	// Email should be updated
	if user.Email != "updated@example.com" {
		t.Errorf("user.email = %q, want %q", user.Email, "updated@example.com")
	}

	// Status should remain active
	if user.Status != "active" {
		t.Errorf("user.status = %q, want 'active'", user.Status)
	}
}

// TS-02-7: Verify that OAuth callback for a blocked user returns HTTP 200
// with user object but does NOT change the user status to active.
func TestOAuthCallback_BlockedUserStatusNotChanged(t *testing.T) {
	mockGitHub := setupMockGitHubServer(t,
		`{"access_token": "mock_access_token", "token_type": "bearer"}`,
		`{"id": 77777, "login": "blockeduser", "email": "blocked@example.com", "name": "Blocked User"}`,
		http.StatusOK,
		http.StatusOK,
	)
	defer mockGitHub.Close()

	env := setupTestEnv(t, []config.OAuthProviderConfig{
		{
			Provider:     "github",
			ClientID:     "test_client_id",
			ClientSecret: "test_client_secret",
			TokenURL:     mockGitHub.URL + "/login/oauth/access_token",
			UserinfoURL:  mockGitHub.URL + "/api/user",
		},
	})

	// Pre-create a blocked user in the database
	blockedUser := &store.User{
		Username:   "blockeduser",
		Email:      "blocked@example.com",
		Provider:   "github",
		ProviderID: "77777",
		Status:     "blocked",
	}
	_, err := env.Store.CreateUser(blockedUser)
	if err != nil {
		t.Fatalf("failed to create blocked user: %v", err)
	}

	// Call the OAuth callback
	body := `{"provider": "github", "code": "validcode_blocked", "redirect_uri": "http://localhost:9999/callback"}`
	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/auth/callback", body, nil)

	// Should return HTTP 200 (the spec says it returns the user, not an error)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d\nBody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var user userResponse
	parseJSON(t, rec, &user)

	// User status in the response should still be 'blocked'
	if user.Status != "blocked" {
		t.Errorf("response user.status = %q, want 'blocked'", user.Status)
	}

	// Verify the database record still has status='blocked'
	dbUser, err := env.Store.GetUserByProviderID("github", "77777")
	if err != nil {
		t.Fatalf("failed to get user from DB: %v", err)
	}
	if dbUser.Status != "blocked" {
		t.Errorf("db user status = %q, want 'blocked' — OAuth callback must never re-activate a blocked user", dbUser.Status)
	}
}

// TS-02-7: Verify that multiple OAuth callbacks for a blocked user
// never change the status.
func TestOAuthCallback_BlockedUserMultipleCalls(t *testing.T) {
	mockGitHub := setupMockGitHubServer(t,
		`{"access_token": "mock_access_token", "token_type": "bearer"}`,
		`{"id": 88888, "login": "multiblocked", "email": "multi@example.com", "name": "Multi Blocked"}`,
		http.StatusOK,
		http.StatusOK,
	)
	defer mockGitHub.Close()

	env := setupTestEnv(t, []config.OAuthProviderConfig{
		{
			Provider:     "github",
			ClientID:     "test_client_id",
			ClientSecret: "test_client_secret",
			TokenURL:     mockGitHub.URL + "/login/oauth/access_token",
			UserinfoURL:  mockGitHub.URL + "/api/user",
		},
	})

	// Pre-create a blocked user
	_, err := env.Store.CreateUser(&store.User{
		Username:   "multiblocked",
		Email:      "multi@example.com",
		Provider:   "github",
		ProviderID: "88888",
		Status:     "blocked",
	})
	if err != nil {
		t.Fatalf("failed to create blocked user: %v", err)
	}

	// Call the callback multiple times
	for i := range 3 {
		body := fmt.Sprintf(`{"provider": "github", "code": "code_%d", "redirect_uri": "http://localhost:9999/callback"}`, i)
		rec := doRequest(env.Echo, http.MethodPost, "/api/v1/auth/callback", body, nil)

		if rec.Code != http.StatusOK {
			t.Fatalf("call %d: status = %d, want %d", i, rec.Code, http.StatusOK)
		}

		var user userResponse
		parseJSON(t, rec, &user)

		if user.Status != "blocked" {
			t.Errorf("call %d: user.status = %q, want 'blocked'", i, user.Status)
		}
	}

	// Final DB check
	dbUser, err := env.Store.GetUserByProviderID("github", "88888")
	if err != nil {
		t.Fatalf("failed to get user from DB: %v", err)
	}
	if dbUser.Status != "blocked" {
		t.Errorf("after multiple calls, db user status = %q, want 'blocked'", dbUser.Status)
	}
}

// --- Edge Case Tests (TS-02-E1 through TS-02-E5) ---

// TS-02-E1: Verify that referencing an unknown provider name during OAuth
// callback returns HTTP 400 with unsupported provider error.
func TestOAuthCallback_UnknownProvider(t *testing.T) {
	env := setupTestEnv(t, []config.OAuthProviderConfig{
		{
			Provider:     "github",
			ClientID:     "test_client_id",
			ClientSecret: "test_client_secret",
		},
	})

	body := `{"provider": "unknownprovider", "code": "anycode", "redirect_uri": "http://localhost:9999/callback"}`
	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/auth/callback", body, nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d\nBody: %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var errResp errorResponse
	parseJSON(t, rec, &errResp)

	if errResp.Error.Code != "400" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "400")
	}
	if !strings.Contains(errResp.Error.Message, "unsupported provider") {
		t.Errorf("error message = %q, want it to contain 'unsupported provider'",
			errResp.Error.Message)
	}
}

// TS-02-E2: Verify that a timeout on the identity provider's token_url or
// userinfo_url causes HTTP 500 with no partial user state written.
func TestOAuthCallback_ProviderTimeout(t *testing.T) {
	// Set up a mock server that never responds (simulates timeout).
	// The mock server handler delays beyond the configured timeout.
	slowServer := setupMockGitHubServer(t,
		"", // Will not be reached — server handler will block
		"",
		http.StatusGatewayTimeout,
		http.StatusGatewayTimeout,
	)
	defer slowServer.Close()

	env := setupTestEnv(t, []config.OAuthProviderConfig{
		{
			Provider:     "github",
			ClientID:     "test_client_id",
			ClientSecret: "test_client_secret",
			TokenURL:     slowServer.URL + "/login/oauth/access_token",
			UserinfoURL:  slowServer.URL + "/api/user",
		},
	})

	// Count users before the call
	countBefore, err := env.Store.CountUsers()
	if err != nil {
		t.Fatalf("failed to count users: %v", err)
	}

	body := `{"provider": "github", "code": "timeout_code", "redirect_uri": "http://localhost:9999/callback"}`
	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/auth/callback", body, nil)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d\nBody: %s",
			rec.Code, http.StatusInternalServerError, rec.Body.String())
	}

	var errResp errorResponse
	parseJSON(t, rec, &errResp)

	if errResp.Error.Code != "500" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "500")
	}
	// The message should mention timeout
	if !strings.Contains(strings.ToLower(errResp.Error.Message), "timeout") {
		t.Errorf("error message = %q, want it to contain 'timeout'",
			errResp.Error.Message)
	}

	// No user record should have been written
	countAfter, err := env.Store.CountUsers()
	if err != nil {
		t.Fatalf("failed to count users after timeout: %v", err)
	}
	if countAfter != countBefore {
		t.Errorf("user count changed from %d to %d — no user record should be written on timeout",
			countBefore, countAfter)
	}
}

// TS-02-E3: Verify that POST /api/v1/auth/callback with missing required
// fields returns HTTP 400.
func TestOAuthCallback_MissingRequiredFields(t *testing.T) {
	env := setupTestEnv(t, []config.OAuthProviderConfig{
		{
			Provider:     "github",
			ClientID:     "test_client_id",
			ClientSecret: "test_client_secret",
		},
	})

	tests := []struct {
		name string
		body string
	}{
		{
			name: "missing code and redirect_uri",
			body: `{"provider": "github"}`,
		},
		{
			name: "missing provider",
			body: `{"code": "abc", "redirect_uri": "http://localhost/cb"}`,
		},
		{
			name: "missing redirect_uri",
			body: `{"provider": "github", "code": "abc"}`,
		},
		{
			name: "missing code",
			body: `{"provider": "github", "redirect_uri": "http://localhost/cb"}`,
		},
		{
			name: "empty body",
			body: `{}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRequest(env.Echo, http.MethodPost, "/api/v1/auth/callback", tc.body, nil)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d\nBody: %s",
					rec.Code, http.StatusBadRequest, rec.Body.String())
			}

			var errResp errorResponse
			parseJSON(t, rec, &errResp)

			if errResp.Error.Code != "400" {
				t.Errorf("error code = %q, want %q", errResp.Error.Code, "400")
			}
			if !strings.Contains(strings.ToLower(errResp.Error.Message), "missing") {
				t.Errorf("error message = %q, want it to contain 'missing'",
					errResp.Error.Message)
			}
		})
	}
}

// TS-02-E4: Verify that when the identity provider rejects the authorization
// code, HTTP 400 is returned and no user record is written.
func TestOAuthCallback_RejectedAuthorizationCode(t *testing.T) {
	// Mock server returns error for the token exchange
	mockGitHub := setupMockGitHubServer(t,
		`{"error": "bad_verification_code", "error_description": "The code passed is incorrect or expired."}`,
		"",
		http.StatusOK, // GitHub returns 200 with error JSON, not 4xx
		http.StatusOK,
	)
	defer mockGitHub.Close()

	env := setupTestEnv(t, []config.OAuthProviderConfig{
		{
			Provider:     "github",
			ClientID:     "test_client_id",
			ClientSecret: "test_client_secret",
			TokenURL:     mockGitHub.URL + "/login/oauth/access_token",
			UserinfoURL:  mockGitHub.URL + "/api/user",
		},
	})

	countBefore, err := env.Store.CountUsers()
	if err != nil {
		t.Fatalf("failed to count users: %v", err)
	}

	body := `{"provider": "github", "code": "expired_code", "redirect_uri": "http://localhost:9999/callback"}`
	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/auth/callback", body, nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d\nBody: %s",
			rec.Code, http.StatusBadRequest, rec.Body.String())
	}

	var errResp errorResponse
	parseJSON(t, rec, &errResp)

	if errResp.Error.Code != "400" {
		t.Errorf("error code = %q, want %q", errResp.Error.Code, "400")
	}
	if !strings.Contains(strings.ToLower(errResp.Error.Message), "exchange failed") {
		t.Errorf("error message = %q, want it to contain 'exchange failed'",
			errResp.Error.Message)
	}

	// No user record should have been created
	countAfter, err := env.Store.CountUsers()
	if err != nil {
		t.Fatalf("failed to count users: %v", err)
	}
	if countAfter != countBefore {
		t.Errorf("user count changed from %d to %d — no user should be created on rejected code",
			countBefore, countAfter)
	}
}

// TS-02-E5: Verify that a request body exceeding the size limit on
// POST /api/v1/auth/callback returns HTTP 413.
func TestOAuthCallback_OversizedBody(t *testing.T) {
	env := setupTestEnv(t, []config.OAuthProviderConfig{
		{
			Provider:     "github",
			ClientID:     "test_client_id",
			ClientSecret: "test_client_secret",
		},
	})

	// Create a body that exceeds 1MB (a reasonable default body size limit)
	largePayload := strings.Repeat("x", 2*1024*1024) // 2MB
	body := fmt.Sprintf(`{"provider": "github", "code": "%s", "redirect_uri": "http://localhost/cb"}`, largePayload)

	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/auth/callback", body, nil)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d\nBody: %s",
			rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}

	var errResp errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err == nil {
		if errResp.Error.Code != "413" {
			t.Errorf("error code = %q, want %q", errResp.Error.Code, "413")
		}
		if !strings.Contains(strings.ToLower(errResp.Error.Message), "too large") {
			t.Errorf("error message = %q, want it to contain 'too large'",
				errResp.Error.Message)
		}
	}
}

// TS-02-E2: Verify that a timeout on the userinfo_url (not just token_url)
// also causes HTTP 500 with no user record written.
func TestOAuthCallback_UserInfoTimeout(t *testing.T) {
	// Token exchange succeeds, but userinfo times out
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"access_token": "valid_token", "token_type": "bearer"}`))
	})
	mux.HandleFunc("/api/user", func(w http.ResponseWriter, r *http.Request) {
		// Simulate timeout by returning 504
		w.WriteHeader(http.StatusGatewayTimeout)
	})
	mockServer := newTestServerFromMux(mux)
	defer mockServer.Close()

	env := setupTestEnv(t, []config.OAuthProviderConfig{
		{
			Provider:     "github",
			ClientID:     "test_client_id",
			ClientSecret: "test_client_secret",
			TokenURL:     mockServer.URL + "/login/oauth/access_token",
			UserinfoURL:  mockServer.URL + "/api/user",
		},
	})

	countBefore, err := env.Store.CountUsers()
	if err != nil {
		t.Fatalf("failed to count users: %v", err)
	}

	body := `{"provider": "github", "code": "userinfo_timeout_code", "redirect_uri": "http://localhost:9999/callback"}`
	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/auth/callback", body, nil)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d\nBody: %s",
			rec.Code, http.StatusInternalServerError, rec.Body.String())
	}

	// No user record should have been written
	countAfter, err := env.Store.CountUsers()
	if err != nil {
		t.Fatalf("failed to count users: %v", err)
	}
	if countAfter != countBefore {
		t.Errorf("user count changed from %d to %d — no user record should be written on userinfo timeout",
			countBefore, countAfter)
	}
}

// TS-02-6: Verify that the response Content-Type for the callback is JSON.
func TestOAuthCallback_ReturnsJSON(t *testing.T) {
	mockGitHub := setupMockGitHubServer(t,
		`{"access_token": "mock_token", "token_type": "bearer"}`,
		`{"id": 11111, "login": "jsonuser", "email": "json@example.com", "name": "JSON User"}`,
		http.StatusOK,
		http.StatusOK,
	)
	defer mockGitHub.Close()

	env := setupTestEnv(t, []config.OAuthProviderConfig{
		{
			Provider:     "github",
			ClientID:     "test_client_id",
			ClientSecret: "test_client_secret",
			TokenURL:     mockGitHub.URL + "/login/oauth/access_token",
			UserinfoURL:  mockGitHub.URL + "/api/user",
		},
	})

	body := `{"provider": "github", "code": "validcode", "redirect_uri": "http://localhost:9999/callback"}`
	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/auth/callback", body, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	contentType := rec.Header().Get("Content-Type")
	if !strings.Contains(contentType, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}
}
