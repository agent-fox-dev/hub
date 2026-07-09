package integration_test

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/agent-fox/af-hub/internal/config"
)

// ---------------------------------------------------------------------------
// TS-05-21: POST /api/v1/auth/callback response includes api_key object
// REQ: 05-REQ-10.1
// ---------------------------------------------------------------------------

func TestAuthCallback_ReturnsAPIKey(t *testing.T) {
	// TS-05-21: Verify that the server's POST /api/v1/auth/callback response
	// includes both a user object and an api_key object with key and key_id
	// fields.
	//
	// The current handler returns only the user object (no api_key field).
	// This test is expected to FAIL (red) until task 7.3 extends the handler.

	mockGitHub := setupMockGitHubServer(t,
		`{"access_token": "mock_access_token", "token_type": "bearer"}`,
		`{"id": 12345, "login": "apikeyuser", "email": "apikey@example.com", "name": "API Key User"}`,
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

	body := `{"provider": "github", "code": "validcode_apikey", "redirect_uri": "http://localhost:9999/callback"}`
	rec := doRequest(env.Echo, http.MethodPost, "/api/v1/auth/callback", body, nil)

	// Should return HTTP 200.
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/auth/callback status = %d, want %d\nBody: %s",
			rec.Code, http.StatusOK, rec.Body.String())
	}

	// Parse the response as a generic map to check both user and api_key fields.
	var resp map[string]any
	parseJSON(t, rec, &resp)

	// The spec (05-REQ-10.1) requires the response to have the structure:
	// {"user": {...}, "api_key": {"key": "af_<key_id>_<secret>", "key_id": "<key_id>"}}
	//
	// Verify user object is present at the "user" key.
	userObj := resp["user"]
	if userObj == nil {
		// The current implementation returns user fields at the top level
		// (e.g. resp["id"], resp["username"]). The spec requires a nested
		// "user" key. If top-level user fields exist, the structure is wrong.
		if resp["id"] != nil || resp["username"] != nil {
			t.Error("response has user fields at top level; spec requires {\"user\": {...}, \"api_key\": {...}} structure")
		} else {
			t.Error("response does not contain a user object")
		}
	}

	// Verify api_key object is present.
	apiKey, ok := resp["api_key"]
	if !ok || apiKey == nil {
		t.Fatal("response does not contain an api_key object")
	}

	apiKeyMap, ok := apiKey.(map[string]any)
	if !ok {
		t.Fatalf("api_key is not an object, got type: %T", apiKey)
	}

	// Verify api_key.key matches the format "af_<hex>_<secret>".
	keyVal, ok := apiKeyMap["key"].(string)
	if !ok || keyVal == "" {
		t.Error("api_key.key is missing or empty")
	} else {
		keyPattern := regexp.MustCompile(`^af_[a-f0-9]+_`)
		if !keyPattern.MatchString(keyVal) {
			t.Errorf("api_key.key does not match pattern '^af_[a-f0-9]+_', got: %q", keyVal)
		}
	}

	// Verify api_key.key_id matches hex pattern.
	keyIDVal, ok := apiKeyMap["key_id"].(string)
	if !ok || keyIDVal == "" {
		t.Error("api_key.key_id is missing or empty")
	} else {
		keyIDPattern := regexp.MustCompile(`^[a-f0-9]+$`)
		if !keyIDPattern.MatchString(keyIDVal) {
			t.Errorf("api_key.key_id does not match pattern '^[a-f0-9]+$', got: %q", keyIDVal)
		}
	}
}
