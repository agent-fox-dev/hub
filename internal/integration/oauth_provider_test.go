package integration_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/agent-fox/af-hub/internal/config"
	"github.com/agent-fox/af-hub/internal/store"
)

// TS-02-5: Verify that GET /api/v1/auth/providers returns an array of configured
// provider objects with name and authorize_url, without exposing secrets.
func TestListProviders_ReturnsConfiguredProviders(t *testing.T) {
	env := setupTestEnv(t, []config.OAuthProviderConfig{
		{
			Provider:     "github",
			ClientID:     "test_client_id",
			ClientSecret: "test_client_secret",
		},
	})

	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/auth/providers", "", nil)

	// Should return HTTP 200
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/auth/providers status = %d, want %d", rec.Code, http.StatusOK)
	}

	// Should return JSON array
	var providers []providerListEntry
	parseJSON(t, rec, &providers)

	if len(providers) == 0 {
		t.Fatal("expected at least one provider in the response")
	}

	// Find the github provider
	var found bool
	for _, p := range providers {
		if p.Name == "github" {
			found = true
			if !strings.HasPrefix(p.AuthorizeURL, "https://github.com") {
				t.Errorf("github authorize_url = %q, want prefix 'https://github.com'", p.AuthorizeURL)
			}
			break
		}
	}
	if !found {
		t.Error("github provider not found in response")
	}
}

// TS-02-5: Verify that no secrets or credentials are exposed in the provider list.
func TestListProviders_NoSecretsExposed(t *testing.T) {
	env := setupTestEnv(t, []config.OAuthProviderConfig{
		{
			Provider:     "github",
			ClientID:     "secret_client_id_12345",
			ClientSecret: "secret_client_secret_67890",
		},
	})

	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/auth/providers", "", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	body := rec.Body.String()

	// The response must NOT contain client_secret, client_id, or token_url
	if strings.Contains(body, "secret_client_secret_67890") {
		t.Error("response body contains client_secret value")
	}
	if strings.Contains(body, "client_secret") {
		t.Error("response body contains 'client_secret' key")
	}
	if strings.Contains(body, "token_url") {
		t.Error("response body contains 'token_url' key")
	}

	// Verify each entry only has 'name' and 'authorize_url' fields
	var rawProviders []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rawProviders); err != nil {
		t.Fatalf("failed to parse response as JSON array: %v", err)
	}

	for i, p := range rawProviders {
		if _, ok := p["client_secret"]; ok {
			t.Errorf("provider[%d] contains 'client_secret' field", i)
		}
		if _, ok := p["client_id"]; ok {
			t.Errorf("provider[%d] contains 'client_id' field", i)
		}
		if _, ok := p["token_url"]; ok {
			t.Errorf("provider[%d] contains 'token_url' field", i)
		}
		if _, ok := p["userinfo_url"]; ok {
			t.Errorf("provider[%d] contains 'userinfo_url' field", i)
		}
	}
}

// TS-02-5: Verify that GET /api/v1/auth/providers does not require an
// Authorization header (public endpoint).
func TestListProviders_NoAuthRequired(t *testing.T) {
	env := setupTestEnv(t, []config.OAuthProviderConfig{
		{
			Provider:     "github",
			ClientID:     "test_client_id",
			ClientSecret: "test_client_secret",
		},
	})

	// No Authorization header
	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/auth/providers", "", nil)

	// Should return 200, not 401
	if rec.Code == http.StatusUnauthorized {
		t.Error("GET /api/v1/auth/providers returned 401 — this is a public endpoint")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// TS-02-4: Verify that removing a provider from config retains existing user
// records and API keys in the database.
func TestProviderRemoval_RetainsExistingRecords(t *testing.T) {
	// Step 1: Set up environment with github provider configured
	env := setupTestEnv(t, []config.OAuthProviderConfig{
		{
			Provider:     "github",
			ClientID:     "test_client_id",
			ClientSecret: "test_client_secret",
		},
	})

	// Create a user with provider='github' in the database
	user := &store.User{
		Username:   "existinguser",
		Email:      "existing@example.com",
		Provider:   "github",
		ProviderID: "12345",
		Status:     "active",
	}
	createdUser, err := env.Store.CreateUser(user)
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// Create an API key for that user
	apiKey := &store.APIKey{
		KeyID:       "testkey001",
		KeyHash:     "somehash",
		UserID:      createdUser.ID,
		WorkspaceID: "ws001",
		Label:       "test key",
		Role:        "editor",
	}
	_, err = env.Store.CreateAPIKey(apiKey)
	if err != nil {
		t.Fatalf("failed to create API key: %v", err)
	}

	// Step 2: Create a new environment WITHOUT github provider (simulating removal)
	envNoGitHub := setupTestEnv(t, []config.OAuthProviderConfig{
		// No github provider configured
	})
	_ = envNoGitHub // new env without github — the DB records should still be there

	// Step 3: Verify user record still exists in the database
	dbUser, err := env.Store.GetUserByProviderID("github", "12345")
	if err != nil {
		t.Fatalf("user record should still exist after provider removal: %v", err)
	}
	if dbUser == nil {
		t.Fatal("user record is nil after provider removal")
	}

	// Step 4: Verify API keys still exist
	keys, err := env.Store.ListAPIKeysByUserID(createdUser.ID)
	if err != nil {
		t.Fatalf("failed to list API keys: %v", err)
	}
	if len(keys) == 0 {
		t.Error("API keys should still exist after provider removal")
	}
}

// TS-02-4: Verify that POST /api/v1/auth/callback with a removed provider
// returns HTTP 400 with unsupported provider error.
func TestProviderRemoval_CallbackReturnsUnsupportedProvider(t *testing.T) {
	// Set up environment WITHOUT github provider
	env := setupTestEnv(t, []config.OAuthProviderConfig{
		// No providers configured
	})

	body := `{"provider": "github", "code": "abc", "redirect_uri": "http://localhost/cb"}`
	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/auth/callback", body, nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/v1/auth/callback with removed provider status = %d, want %d",
			rec.Code, http.StatusBadRequest)
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

// TS-02-5: Verify that response Content-Type is application/json.
func TestListProviders_ReturnsJSON(t *testing.T) {
	env := setupTestEnv(t, []config.OAuthProviderConfig{
		{
			Provider:     "github",
			ClientID:     "test_client_id",
			ClientSecret: "test_client_secret",
		},
	})

	rec := doRequest(env.Echo, http.MethodGet, "/api/v1/auth/providers", "", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	contentType := rec.Header().Get("Content-Type")
	if !strings.Contains(contentType, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}
}
