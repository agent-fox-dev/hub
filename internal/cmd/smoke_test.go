package cmd_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/cmd"
	"github.com/agent-fox-dev/hub/internal/config"
	"github.com/agent-fox-dev/hub/internal/login"
)

// ---------------------------------------------------------------------------
// Smoke Tests — end-to-end flows exercising the full Cobra command tree
// against mock HTTP servers. These verify wiring: PersistentPreRunE ->
// RunE -> client functions -> config.Save chains are unbroken.
// ---------------------------------------------------------------------------

// TestSmokeLoginAndWorkspaceCreate verifies the end-to-end flow:
// 1. Operator logs in via OAuth (mock providers + callback exchange)
// 2. Operator creates a workspace with team association
//
// TS-05-SMOKE-1
func TestSmokeLoginAndWorkspaceCreate(t *testing.T) {
	// Track all incoming requests so we can verify call chains.
	var requests []capturedRequest

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, capturedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(body),
			Header: r.Header.Clone(),
		})

		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/auth/providers":
			fmt.Fprint(w, `{"providers":[{"name":"github","authorize_url":"https://github.com/login/oauth/authorize","scopes":"user:email"}]}`)

		case r.Method == "POST" && r.URL.Path == "/api/v1/auth/callback":
			fmt.Fprint(w, `{"user":{"id":"u-1"},"api_key":{"key_id":"k-1","secret":"secret123","token":"af_k-1_secret123"}}`)

		case r.Method == "GET" && r.URL.Path == "/api/v1/teams":
			fmt.Fprint(w, `[{"slug":"my-team","id":"team-uuid","name":"My Team"}]`)

		case r.Method == "POST" && r.URL.Path == "/api/v1/workspaces":
			fmt.Fprint(w, `{"id":"ws-1","slug":"my-workspace","git_url":"https://github.com/org/repo"}`)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockServer.Close()

	// Step 1: Login
	configPath := setupConfigHome(t, config.Config{
		HubURL: mockServer.URL,
	})

	// Stub browser open to auto-trigger callback with correct state.
	origOpen := login.BrowserOpenFunc
	login.BrowserOpenFunc = func(authURL string) error {
		// Extract state and port from the authorization URL.
		// authURL looks like: https://github.com/login/oauth/authorize?redirect_uri=http%3A%2F%2Flocalhost%3A<port>%2Fcallback&state=<state>
		// We need to parse redirect_uri and state from the authURL,
		// then hit the callback server directly.
		go func() {
			time.Sleep(50 * time.Millisecond) // Let the server start listening
			// Parse the redirect_uri from the URL
			parts := strings.Split(authURL, "redirect_uri=")
			if len(parts) < 2 {
				return
			}
			// Extract state
			stateParts := strings.Split(authURL, "state=")
			if len(stateParts) < 2 {
				return
			}
			state := stateParts[len(stateParts)-1]
			// state might have trailing params
			if idx := strings.Index(state, "&"); idx != -1 {
				state = state[:idx]
			}

			// Find the redirect_uri (the callback URL)
			redirectPart := parts[1]
			if idx := strings.Index(redirectPart, "&"); idx != -1 {
				redirectPart = redirectPart[:idx]
			}
			// URL-decode it
			redirectURI := strings.ReplaceAll(redirectPart, "%3A", ":")
			redirectURI = strings.ReplaceAll(redirectURI, "%2F", "/")

			// Hit the callback
			callbackURL := fmt.Sprintf("%s?code=test-code&state=%s", redirectURI, state)
			http.Get(callbackURL) //nolint:errcheck
		}()
		return nil
	}
	defer func() { login.BrowserOpenFunc = origOpen }()

	// Override login timeout to be short.
	origTimeout := cmd.LoginTimeout
	cmd.LoginTimeout = 10 * time.Second
	defer func() { cmd.LoginTimeout = origTimeout }()

	result := runCLI(t, []string{"login", "--provider", "github", "--expires", "90"})

	if result.ExitCode != 0 {
		t.Fatalf("login exit code = %d, want 0; stderr: %s", result.ExitCode, result.Stderr)
	}

	// Verify config was written with credentials.
	cfg := readConfig(t, configPath)
	if cfg.UserID != "u-1" {
		t.Errorf("config user_id = %q, want 'u-1'", cfg.UserID)
	}
	if cfg.APIKey != "af_k-1_secret123" {
		t.Errorf("config api_key = %q, want 'af_k-1_secret123'", cfg.APIKey)
	}
	if cfg.KeyID != "k-1" {
		t.Errorf("config key_id = %q, want 'k-1'", cfg.KeyID)
	}

	// Verify POST /api/v1/auth/callback was called with correct payload.
	var callbackReq *capturedRequest
	for i := range requests {
		if requests[i].Method == "POST" && requests[i].Path == "/api/v1/auth/callback" {
			callbackReq = &requests[i]
			break
		}
	}
	if callbackReq == nil {
		t.Fatal("POST /api/v1/auth/callback was not called")
	}

	var cbPayload map[string]interface{}
	if err := json.Unmarshal([]byte(callbackReq.Body), &cbPayload); err != nil {
		t.Fatalf("failed to parse callback payload: %v", err)
	}
	if cbPayload["provider"] != "github" {
		t.Errorf("callback provider = %v, want 'github'", cbPayload["provider"])
	}
	if cbPayload["code"] != "test-code" {
		t.Errorf("callback code = %v, want 'test-code'", cbPayload["code"])
	}
	if int(cbPayload["expires"].(float64)) != 90 {
		t.Errorf("callback expires = %v, want 90", cbPayload["expires"])
	}

	// Verify stderr contains authorization URL.
	if !strings.Contains(result.Stderr, "http") {
		t.Errorf("stderr should contain authorization URL, got: %q", result.Stderr)
	}

	// Step 2: Workspace Create (now that we have credentials)
	requests = nil // Reset request tracker

	result = runCLI(t, []string{
		"workspace", "create",
		"--git-url", "https://github.com/org/repo",
		"--slug", "my-workspace",
		"--branch", "main",
		"--team", "my-team",
	})

	if result.ExitCode != 0 {
		t.Fatalf("workspace create exit code = %d, want 0; stderr: %s", result.ExitCode, result.Stderr)
	}

	// Verify GET /api/v1/teams was called with Bearer auth.
	var teamsReq *capturedRequest
	for i := range requests {
		if requests[i].Method == "GET" && requests[i].Path == "/api/v1/teams" {
			teamsReq = &requests[i]
			break
		}
	}
	if teamsReq == nil {
		t.Fatal("GET /api/v1/teams was not called")
	}
	if auth := teamsReq.Header.Get("Authorization"); !strings.HasPrefix(auth, "Bearer ") {
		t.Errorf("teams request auth header = %q, want 'Bearer ...'", auth)
	}

	// Verify POST /api/v1/workspaces payload.
	var wsReq *capturedRequest
	for i := range requests {
		if requests[i].Method == "POST" && requests[i].Path == "/api/v1/workspaces" {
			wsReq = &requests[i]
			break
		}
	}
	if wsReq == nil {
		t.Fatal("POST /api/v1/workspaces was not called")
	}

	var wsPayload map[string]interface{}
	if err := json.Unmarshal([]byte(wsReq.Body), &wsPayload); err != nil {
		t.Fatalf("failed to parse workspace payload: %v", err)
	}
	if wsPayload["git_url"] != "https://github.com/org/repo" {
		t.Errorf("workspace git_url = %v, want 'https://github.com/org/repo'", wsPayload["git_url"])
	}
	if wsPayload["slug"] != "my-workspace" {
		t.Errorf("workspace slug = %v, want 'my-workspace'", wsPayload["slug"])
	}
	if wsPayload["branch"] != "main" {
		t.Errorf("workspace branch = %v, want 'main'", wsPayload["branch"])
	}
	if wsPayload["team_id"] != "team-uuid" {
		t.Errorf("workspace team_id = %v, want 'team-uuid'", wsPayload["team_id"])
	}

	// Verify pretty-printed JSON on stdout.
	var wsOutput map[string]interface{}
	if err := json.Unmarshal([]byte(result.Stdout), &wsOutput); err != nil {
		t.Fatalf("stdout is not valid JSON: %v", err)
	}
	if wsOutput["slug"] != "my-workspace" {
		t.Errorf("stdout slug = %v, want 'my-workspace'", wsOutput["slug"])
	}
}

// TestSmokeKeysRefresh verifies the keys refresh happy path:
// operator refreshes their API key and config is updated with new credentials.
//
// TS-05-SMOKE-2
func TestSmokeKeysRefresh(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == "POST" && r.URL.Path == "/api/v1/keys/old-kid/refresh":
			// Verify Bearer auth.
			if auth := r.Header.Get("Authorization"); auth != "Bearer old-key" {
				t.Errorf("refresh auth = %q, want 'Bearer old-key'", auth)
			}
			fmt.Fprint(w, `{"key_id":"new-kid","user_id":"u-1","secret":"new-secret","token":"af_new-kid_new-secret"}`)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockServer.Close()

	configPath := setupConfigHome(t, config.Config{
		HubURL: mockServer.URL,
		UserID: "u-1",
		APIKey: "old-key",
		KeyID:  "old-kid",
	})

	result := runCLI(t, []string{"keys", "refresh"})

	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", result.ExitCode, result.Stderr)
	}

	// Verify config was updated.
	cfg := readConfig(t, configPath)
	if cfg.APIKey != "af_new-kid_new-secret" {
		t.Errorf("config api_key = %q, want 'af_new-kid_new-secret'", cfg.APIKey)
	}
	if cfg.KeyID != "new-kid" {
		t.Errorf("config key_id = %q, want 'new-kid'", cfg.KeyID)
	}

	// Verify stdout has pretty-printed JSON.
	var output map[string]interface{}
	if err := json.Unmarshal([]byte(result.Stdout), &output); err != nil {
		t.Fatalf("stdout is not valid JSON: %v; stdout: %q", err, result.Stdout)
	}
	if output["key_id"] != "new-kid" {
		t.Errorf("stdout key_id = %v, want 'new-kid'", output["key_id"])
	}
}

// TestSmokeLoginCallbackTimeout verifies that when the operator starts login
// but does not complete the browser flow, the CLI exits with error after
// timeout and config remains unchanged.
//
// TS-05-SMOKE-3
func TestSmokeLoginCallbackTimeout(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method == "GET" && r.URL.Path == "/api/v1/auth/providers" {
			fmt.Fprint(w, `{"providers":[{"name":"github","authorize_url":"https://github.com/login/oauth/authorize","scopes":"user:email"}]}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	configPath := setupConfigHome(t, config.Config{
		HubURL: mockServer.URL,
	})

	// Stub browser open to do nothing (no callback will be sent).
	origOpen := login.BrowserOpenFunc
	login.BrowserOpenFunc = func(url string) error { return nil }
	defer func() { login.BrowserOpenFunc = origOpen }()

	// Use a very short timeout to avoid waiting 2 minutes.
	origTimeout := cmd.LoginTimeout
	cmd.LoginTimeout = 200 * time.Millisecond
	defer func() { cmd.LoginTimeout = origTimeout }()

	result := runCLI(t, []string{"login", "--provider", "github"})

	if result.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", result.ExitCode)
	}

	// Stderr should contain a timeout message.
	if !strings.Contains(result.Stderr, "timed out") && !strings.Contains(result.Stderr, "timeout") {
		t.Errorf("stderr should contain timeout message, got: %q", result.Stderr)
	}

	// Verify stderr contains authorization URL.
	if !strings.Contains(result.Stderr, "http") {
		t.Errorf("stderr should contain authorization URL, got: %q", result.Stderr)
	}

	// Config should be unchanged (no credentials written).
	cfg := readConfig(t, configPath)
	if cfg.APIKey != "" {
		t.Errorf("config api_key should be empty, got %q", cfg.APIKey)
	}
	if cfg.UserID != "" {
		t.Errorf("config user_id should be empty, got %q", cfg.UserID)
	}
}

// TestSmokeWorkspaceTokenRevoke verifies that revoking a workspace token
// prints confirmation to stderr and exits with code 0.
//
// TS-05-SMOKE-4
func TestSmokeWorkspaceTokenRevoke(t *testing.T) {
	var receivedMethod, receivedPath string
	var receivedAuth string
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedPath = r.URL.Path
		receivedAuth = r.Header.Get("Authorization")

		if r.Method == "DELETE" && r.URL.Path == "/api/v1/workspaces/my-workspace/tokens/abc-token-id" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	setupConfigHome(t, config.Config{
		HubURL: mockServer.URL,
		UserID: "u-1",
		APIKey: "my-key",
		KeyID:  "my-kid",
	})

	result := runCLI(t, []string{"workspace", "token", "revoke", "--workspace", "my-workspace", "abc-token-id"})

	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", result.ExitCode, result.Stderr)
	}

	// Verify the correct endpoint was called.
	if receivedMethod != "DELETE" {
		t.Errorf("method = %q, want DELETE", receivedMethod)
	}
	if receivedPath != "/api/v1/workspaces/my-workspace/tokens/abc-token-id" {
		t.Errorf("path = %q, want '/api/v1/workspaces/my-workspace/tokens/abc-token-id'", receivedPath)
	}
	if !strings.HasPrefix(receivedAuth, "Bearer ") {
		t.Errorf("auth = %q, want 'Bearer ...'", receivedAuth)
	}

	// Verify confirmation on stderr.
	if !strings.Contains(result.Stderr, "Token abc-token-id revoked.") {
		t.Errorf("stderr = %q, want it to contain 'Token abc-token-id revoked.'", result.Stderr)
	}

	// Stdout should be empty.
	if strings.TrimSpace(result.Stdout) != "" {
		t.Errorf("stdout should be empty, got: %q", result.Stdout)
	}
}

// TestSmokeKeysRevokeWith404 verifies that when keys revoke gets a 404
// (key already revoked), credentials are cleared and the correct message
// is printed.
//
// TS-05-SMOKE-5
func TestSmokeKeysRevokeWith404(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.URL.Path == "/api/v1/keys/gone-kid" {
			// Verify Bearer auth.
			if auth := r.Header.Get("Authorization"); auth != "Bearer gone-key" {
				t.Errorf("revoke auth = %q, want 'Bearer gone-key'", auth)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"code":404,"message":"not found"}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	configPath := setupConfigHome(t, config.Config{
		HubURL: mockServer.URL,
		UserID: "gone-uid",
		APIKey: "gone-key",
		KeyID:  "gone-kid",
	})

	result := runCLI(t, []string{"keys", "revoke"})

	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", result.ExitCode, result.Stderr)
	}

	// Verify credentials cleared in config.
	cfg := readConfig(t, configPath)
	if cfg.APIKey != "" {
		t.Errorf("config api_key should be empty, got %q", cfg.APIKey)
	}
	if cfg.KeyID != "" {
		t.Errorf("config key_id should be empty, got %q", cfg.KeyID)
	}
	if cfg.UserID != "" {
		t.Errorf("config user_id should be empty, got %q", cfg.UserID)
	}

	// Verify message on stderr.
	if !strings.Contains(result.Stderr, "API key not found on server. Local credentials cleared.") {
		t.Errorf("stderr = %q, want 'API key not found on server. Local credentials cleared.'", result.Stderr)
	}
}
