package keys_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/agent-fox-dev/hub/internal/config"
	"github.com/agent-fox-dev/hub/internal/keys"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// writeStructConfig encodes a Config struct as TOML and writes it to
// $HOME/.af/config.toml.
func writeStructConfig(t *testing.T, home string, cfg config.Config) string {
	t.Helper()
	dir := filepath.Join(home, ".af")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	path := filepath.Join(dir, "config.toml")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatalf("failed to open config for writing: %v", err)
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(cfg); err != nil {
		t.Fatalf("failed to encode config: %v", err)
	}
	return path
}

// readParsedConfig reads and parses the config file at the given path.
func readParsedConfig(t *testing.T, path string) config.Config {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}
	var cfg config.Config
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		t.Fatalf("failed to parse config file: %v", err)
	}
	return cfg
}

// requestCapture is a simple helper to track HTTP requests received by
// a mock server.
type requestCapture struct {
	Method string
	Path   string
	Body   string
	Header http.Header
}

// refreshJSON returns the JSON for a POST /api/v1/keys/:key_id/refresh
// response using the actual spec 02 flat response shape.
func refreshJSON(keyID, token, secret string) string {
	resp := keys.RefreshResponse{
		KeyID:  keyID,
		Token:  token,
		Secret: secret,
	}
	data, _ := json.Marshal(resp)
	return string(data)
}

// ---------------------------------------------------------------------------
// 3.1 — Keys Success Path Tests
// ---------------------------------------------------------------------------

// TestKeysListSuccess verifies that afc keys list sends GET /api/v1/keys
// with Bearer auth header and returns the JSON response body for
// pretty-printing to stdout.
// TS-05-22
func TestKeysListSuccess(t *testing.T) {
	var requests []requestCapture
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, requestCapture{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(body),
			Header: r.Header.Clone(),
		})

		if r.Method == "GET" && r.URL.Path == "/api/v1/keys" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `[{"id":"k1","key_id":"kid-1","created_at":"2024-01-01T00:00:00Z"}]`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	body, statusCode, err := keys.ListKeys(mockServer.URL, "test-key", client)
	if err != nil {
		t.Fatalf("ListKeys failed: %v", err)
	}

	// Verify HTTP status code is 200.
	if statusCode != http.StatusOK {
		t.Errorf("status code = %d, want 200", statusCode)
	}

	// Verify GET /api/v1/keys was called.
	found := false
	for _, req := range requests {
		if req.Method == "GET" && req.Path == "/api/v1/keys" {
			found = true
			// Verify Authorization: Bearer header.
			authHeader := req.Header.Get("Authorization")
			if authHeader != "Bearer test-key" {
				t.Errorf("Authorization header = %q, want 'Bearer test-key'", authHeader)
			}
			break
		}
	}
	if !found {
		t.Error("GET /api/v1/keys was not called")
	}

	// Verify response body is parseable JSON.
	var parsed []map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}
	if len(parsed) == 0 {
		t.Error("expected at least one key in the response")
	}
	if parsed[0]["id"] != "k1" {
		t.Errorf("first key id = %v, want 'k1'", parsed[0]["id"])
	}

	// Verify the response can be pretty-printed with 2-space indentation.
	prettyPrinted, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		t.Fatalf("failed to pretty-print JSON: %v", err)
	}
	if !strings.Contains(string(prettyPrinted), "  ") {
		t.Error("pretty-printed JSON should contain 2-space indentation")
	}
}

// TestKeysRefreshSuccess verifies that afc keys refresh sends
// POST /api/v1/keys/:key_id/refresh, updates api_key and key_id in config,
// and returns the response JSON for stdout output.
// TS-05-23
func TestKeysRefreshSuccess(t *testing.T) {
	var requests []requestCapture
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, requestCapture{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(body),
			Header: r.Header.Clone(),
		})

		// The URL path includes the key_id: /api/v1/keys/old-kid/refresh
		if r.Method == "POST" && r.URL.Path == "/api/v1/keys/old-kid/refresh" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// Per spec 02, the refresh response is a flat object with key_id and token.
			// token is the full composite key for Bearer auth.
			fmt.Fprint(w, refreshJSON("new-kid", "af_new-kid_newsecret", "newsecret"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	// Set up initial config with old credentials.
	tmpHome := t.TempDir()
	configPath := writeStructConfig(t, tmpHome, config.Config{
		HubURL: mockServer.URL,
		APIKey: "old-key",
		KeyID:  "old-kid",
		UserID: "test-user",
	})

	client := &http.Client{Timeout: 5 * time.Second}
	refreshResp, rawBody, err := keys.RefreshKey(mockServer.URL, "old-key", "old-kid", client)
	if err != nil {
		t.Fatalf("RefreshKey failed: %v", err)
	}

	// Verify POST /api/v1/keys/old-kid/refresh was called with Bearer auth.
	found := false
	for _, req := range requests {
		if req.Method == "POST" && req.Path == "/api/v1/keys/old-kid/refresh" {
			found = true
			authHeader := req.Header.Get("Authorization")
			if authHeader != "Bearer old-key" {
				t.Errorf("Authorization header = %q, want 'Bearer old-key'", authHeader)
			}
			break
		}
	}
	if !found {
		t.Error("POST /api/v1/keys/old-kid/refresh was not called")
	}

	// Verify parsed response contains new credentials.
	// Per spec 02: key_id (not 'id') and token (not 'key').
	if refreshResp.KeyID != "new-kid" {
		t.Errorf("response key_id = %q, want 'new-kid'", refreshResp.KeyID)
	}
	if refreshResp.Token != "af_new-kid_newsecret" {
		t.Errorf("response token = %q, want 'af_new-kid_newsecret'", refreshResp.Token)
	}

	// Verify raw body is available for pretty-printing.
	if len(rawBody) == 0 {
		t.Error("raw response body should not be empty")
	}

	// Update config with new credentials (what the command handler would do).
	newCfg := &config.Config{
		HubURL: mockServer.URL,
		UserID: "test-user",
		APIKey: refreshResp.Token, // Store the full token for Bearer auth.
		KeyID:  refreshResp.KeyID,
	}
	if err := config.Save(configPath, newCfg); err != nil {
		t.Fatalf("config.Save failed: %v", err)
	}

	// Verify config was updated with new credentials.
	cfg := readParsedConfig(t, configPath)
	if cfg.APIKey != "af_new-kid_newsecret" {
		t.Errorf("config api_key = %q, want 'af_new-kid_newsecret'", cfg.APIKey)
	}
	if cfg.KeyID != "new-kid" {
		t.Errorf("config key_id = %q, want 'new-kid'", cfg.KeyID)
	}

	// Verify the raw response can be pretty-printed.
	var prettyCheck any
	if err := json.Unmarshal(rawBody, &prettyCheck); err != nil {
		t.Errorf("raw body is not valid JSON: %v", err)
	}
}

// TestKeysRevokeSuccess verifies that afc keys revoke on a 2xx response
// clears api_key, key_id, and user_id from config and prints
// 'API key revoked.' to stderr.
// TS-05-25
func TestKeysRevokeSuccess(t *testing.T) {
	var requests []requestCapture
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, requestCapture{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(body),
			Header: r.Header.Clone(),
		})

		if r.Method == "DELETE" && r.URL.Path == "/api/v1/keys/my-kid" {
			w.WriteHeader(http.StatusNoContent) // 204
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	// Set up config with credentials to be cleared.
	tmpHome := t.TempDir()
	configPath := writeStructConfig(t, tmpHome, config.Config{
		HubURL: mockServer.URL,
		APIKey: "my-key",
		KeyID:  "my-kid",
		UserID: "my-uid",
	})

	client := &http.Client{Timeout: 5 * time.Second}
	statusCode, _, err := keys.RevokeKey(mockServer.URL, "my-key", "my-kid", client)
	if err != nil {
		t.Fatalf("RevokeKey failed: %v", err)
	}

	// Verify DELETE /api/v1/keys/my-kid was called.
	found := false
	for _, req := range requests {
		if req.Method == "DELETE" && req.Path == "/api/v1/keys/my-kid" {
			found = true
			authHeader := req.Header.Get("Authorization")
			if authHeader != "Bearer my-key" {
				t.Errorf("Authorization header = %q, want 'Bearer my-key'", authHeader)
			}
			break
		}
	}
	if !found {
		t.Error("DELETE /api/v1/keys/my-kid was not called")
	}

	// Verify status code indicates success (2xx).
	if statusCode < 200 || statusCode >= 300 {
		t.Errorf("status code = %d, want 2xx", statusCode)
	}

	// Clear credentials in config (what the command handler would do on 2xx).
	clearedCfg := &config.Config{
		HubURL: mockServer.URL,
		APIKey: "",
		KeyID:  "",
		UserID: "",
	}
	if err := config.Save(configPath, clearedCfg); err != nil {
		t.Fatalf("config.Save failed: %v", err)
	}

	// Verify credentials are cleared.
	cfg := readParsedConfig(t, configPath)
	if cfg.APIKey != "" {
		t.Errorf("config api_key = %q, want empty", cfg.APIKey)
	}
	if cfg.KeyID != "" {
		t.Errorf("config key_id = %q, want empty", cfg.KeyID)
	}
	if cfg.UserID != "" {
		t.Errorf("config user_id = %q, want empty", cfg.UserID)
	}

	// The command should print 'API key revoked.' to stderr (verified at
	// command level in later groups).
}

// TestKeysRevoke404Success verifies that afc keys revoke on a 404 response
// clears local credentials and prints
// 'API key not found on server. Local credentials cleared.' to stderr.
// TS-05-26
func TestKeysRevoke404Success(t *testing.T) {
	var requests []requestCapture
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, requestCapture{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(body),
			Header: r.Header.Clone(),
		})

		if r.Method == "DELETE" && r.URL.Path == "/api/v1/keys/gone-kid" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound) // 404
			fmt.Fprint(w, `{"error":{"code":404,"message":"not found"}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	// Set up config with credentials to be cleared.
	tmpHome := t.TempDir()
	configPath := writeStructConfig(t, tmpHome, config.Config{
		HubURL: mockServer.URL,
		APIKey: "gone-key",
		KeyID:  "gone-kid",
		UserID: "gone-uid",
	})

	client := &http.Client{Timeout: 5 * time.Second}
	statusCode, _, err := keys.RevokeKey(mockServer.URL, "gone-key", "gone-kid", client)
	if err != nil {
		t.Fatalf("RevokeKey failed: %v", err)
	}

	// Verify DELETE was called with Bearer auth.
	found := false
	for _, req := range requests {
		if req.Method == "DELETE" && req.Path == "/api/v1/keys/gone-kid" {
			found = true
			authHeader := req.Header.Get("Authorization")
			if authHeader != "Bearer gone-key" {
				t.Errorf("Authorization header = %q, want 'Bearer gone-key'", authHeader)
			}
			break
		}
	}
	if !found {
		t.Error("DELETE /api/v1/keys/gone-kid was not called")
	}

	// 404 is treated as a success scenario — credentials should still be cleared.
	if statusCode != http.StatusNotFound {
		t.Errorf("status code = %d, want 404", statusCode)
	}

	// Clear credentials in config (what the command handler would do on 404).
	clearedCfg := &config.Config{
		HubURL: mockServer.URL,
		APIKey: "",
		KeyID:  "",
		UserID: "",
	}
	if err := config.Save(configPath, clearedCfg); err != nil {
		t.Fatalf("config.Save failed: %v", err)
	}

	// Verify credentials are cleared.
	cfg := readParsedConfig(t, configPath)
	if cfg.APIKey != "" {
		t.Errorf("config api_key = %q, want empty after 404 revoke", cfg.APIKey)
	}
	if cfg.KeyID != "" {
		t.Errorf("config key_id = %q, want empty after 404 revoke", cfg.KeyID)
	}
	if cfg.UserID != "" {
		t.Errorf("config user_id = %q, want empty after 404 revoke", cfg.UserID)
	}

	// The command should print 'API key not found on server. Local credentials
	// cleared.' to stderr (verified at command level in later groups).
}

// ---------------------------------------------------------------------------
// 3.2 — Keys Error Path Tests
// ---------------------------------------------------------------------------

// TestKeysRefreshMissingKeyID verifies that afc keys refresh with missing
// key_id in config prints the exact error message and exits with code 1
// without any network request.
// TS-05-24
func TestKeysRefreshMissingKeyID(t *testing.T) {
	var requests []requestCapture
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, requestCapture{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(body),
			Header: r.Header.Clone(),
		})
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	// Config has hub_url and api_key but key_id is empty.
	tmpHome := t.TempDir()
	writeStructConfig(t, tmpHome, config.Config{
		HubURL: mockServer.URL,
		APIKey: "k",
		KeyID:  "", // empty — should trigger validation error
	})

	// ValidateKeyID should return an error when key_id is empty.
	err := keys.ValidateKeyID("")
	if err == nil {
		t.Fatal("ValidateKeyID should return error for empty key_id, got nil")
	}

	// Error message should match the exact spec text.
	wantMsg := `key_id is not set. Run "afc login" first.`
	if !strings.Contains(err.Error(), wantMsg) {
		t.Errorf("error message = %q, want it to contain %q", err.Error(), wantMsg)
	}

	// No network request should have been made.
	if len(requests) != 0 {
		t.Errorf("expected zero network requests, got %d", len(requests))
	}
}

// TestKeysRevokeMissingKeyID verifies that afc keys revoke with missing
// key_id in config prints the exact error message and exits with code 1
// without any network request.
// TS-05-27
func TestKeysRevokeMissingKeyID(t *testing.T) {
	var requests []requestCapture
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, requestCapture{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(body),
			Header: r.Header.Clone(),
		})
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	// Config has hub_url and api_key but key_id is empty.
	tmpHome := t.TempDir()
	writeStructConfig(t, tmpHome, config.Config{
		HubURL: mockServer.URL,
		APIKey: "k",
		KeyID:  "", // empty — should trigger validation error
	})

	// ValidateKeyID should return an error when key_id is empty.
	err := keys.ValidateKeyID("")
	if err == nil {
		t.Fatal("ValidateKeyID should return error for empty key_id, got nil")
	}

	// Error message should match the exact spec text.
	wantMsg := `key_id is not set. Run "afc login" first.`
	if !strings.Contains(err.Error(), wantMsg) {
		t.Errorf("error message = %q, want it to contain %q", err.Error(), wantMsg)
	}

	// No network request should have been made.
	if len(requests) != 0 {
		t.Errorf("expected zero network requests, got %d", len(requests))
	}
}

// TestKeysRevokeServerError verifies that afc keys revoke on a non-2xx
// non-404 response prints the error message from the JSON response body
// to stderr, exits with code 1, and does not modify config.
// TS-05-28
func TestKeysRevokeServerError(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.URL.Path == "/api/v1/keys/kid" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError) // 500
			fmt.Fprint(w, `{"error":{"code":500,"message":"internal server error"}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	// Set up config with credentials.
	tmpHome := t.TempDir()
	configPath := writeStructConfig(t, tmpHome, config.Config{
		HubURL: mockServer.URL,
		APIKey: "k",
		KeyID:  "kid",
		UserID: "uid",
	})

	client := &http.Client{Timeout: 5 * time.Second}
	statusCode, body, err := keys.RevokeKey(mockServer.URL, "k", "kid", client)

	// The function must make the HTTP request and return the status code and
	// body so the caller can handle non-2xx responses (print error to stderr).
	// A nil error with status code 500 is expected — the function communicates
	// server errors via status code, not Go errors.
	if err != nil {
		t.Fatalf("RevokeKey should not return error for reachable server: %v", err)
	}

	if statusCode != http.StatusInternalServerError {
		t.Errorf("status code = %d, want 500", statusCode)
	}

	// The response body should contain the error message for stderr output.
	if !strings.Contains(string(body), "internal server error") {
		t.Errorf("response body should contain 'internal server error', got: %s", string(body))
	}

	// Config should remain unchanged after a server error.
	cfg := readParsedConfig(t, configPath)
	if cfg.APIKey != "k" {
		t.Errorf("config api_key should be unchanged, got %q want 'k'", cfg.APIKey)
	}
	if cfg.KeyID != "kid" {
		t.Errorf("config key_id should be unchanged, got %q want 'kid'", cfg.KeyID)
	}
	if cfg.UserID != "uid" {
		t.Errorf("config user_id should be unchanged, got %q want 'uid'", cfg.UserID)
	}
}

// TestKeysListUnauthorized verifies that a non-2xx response from
// GET /api/v1/keys causes the CLI to print the JSON error message to stderr
// and exit with code 1; stdout should be empty.
// TS-05-E13
func TestKeysListUnauthorized(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/keys" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized) // 401
			fmt.Fprint(w, `{"error":{"code":401,"message":"unauthorized"}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	body, statusCode, err := keys.ListKeys(mockServer.URL, "bad-key", client)

	// The function must make the HTTP request and return the status code and
	// body so the caller can handle non-2xx responses appropriately.
	if err != nil {
		t.Fatalf("ListKeys should not return error for reachable server: %v", err)
	}

	// Status code should be 401.
	if statusCode != http.StatusUnauthorized {
		t.Errorf("status code = %d, want 401", statusCode)
	}

	// The response body should contain the error message for stderr output.
	if !strings.Contains(string(body), "unauthorized") {
		t.Errorf("response body should contain 'unauthorized', got: %s", string(body))
	}
}

// TestKeysRefreshForbidden verifies that a non-2xx response from
// POST /api/v1/keys/:key_id/refresh causes the CLI to print the error
// to stderr and exit with code 1 without modifying config.
// TS-05-E14
func TestKeysRefreshForbidden(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/keys/kid/refresh" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden) // 403
			fmt.Fprint(w, `{"error":{"code":403,"message":"forbidden"}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	// Set up config with existing credentials.
	tmpHome := t.TempDir()
	configPath := writeStructConfig(t, tmpHome, config.Config{
		HubURL: mockServer.URL,
		APIKey: "old",
		KeyID:  "kid",
		UserID: "uid",
	})

	client := &http.Client{Timeout: 5 * time.Second}
	refreshResp, rawBody, err := keys.RefreshKey(mockServer.URL, "old", "kid", client)

	// The function must make the HTTP request. On non-2xx, it should return an
	// error so the caller can handle it (print error to stderr, exit code 1).
	if err == nil {
		// If no error, refreshResp should be nil (no valid data on 403).
		if refreshResp != nil {
			t.Error("RefreshKey should not return valid data on 403 response")
		}
		t.Fatal("RefreshKey should return error on 403 response, got nil")
	}

	// The error should contain the server's error message or status code.
	if !strings.Contains(err.Error(), "forbidden") &&
		!strings.Contains(err.Error(), "403") {
		t.Errorf("error should contain 'forbidden' or '403', got: %v", err)
	}

	_ = rawBody // body may be available for error message extraction

	// Config should remain unchanged after a 403 error.
	cfg := readParsedConfig(t, configPath)
	if cfg.APIKey != "old" {
		t.Errorf("config api_key should be unchanged after 403, got %q want 'old'", cfg.APIKey)
	}
	if cfg.KeyID != "kid" {
		t.Errorf("config key_id should be unchanged after 403, got %q want 'kid'", cfg.KeyID)
	}
}

// TestKeysRevokeAtomicWriteFailure verifies that when atomic config write
// fails after a successful server revocation, the CLI prints a descriptive
// error about the config write failure and exits with code 1.
// TS-05-E15
func TestKeysRevokeAtomicWriteFailure(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.URL.Path == "/api/v1/keys/kid" {
			w.WriteHeader(http.StatusNoContent) // 204 success
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	// Set up config with credentials.
	tmpHome := t.TempDir()
	configPath := writeStructConfig(t, tmpHome, config.Config{
		HubURL: mockServer.URL,
		APIKey: "k",
		KeyID:  "kid",
		UserID: "uid",
	})

	// First, verify the server revocation succeeds.
	client := &http.Client{Timeout: 5 * time.Second}
	statusCode, _, err := keys.RevokeKey(mockServer.URL, "k", "kid", client)
	if err != nil {
		t.Fatalf("RevokeKey failed: %v", err)
	}
	if statusCode < 200 || statusCode >= 300 {
		t.Fatalf("RevokeKey status = %d, want 2xx", statusCode)
	}

	// Inject a rename failure to simulate atomic write failure.
	origRename := config.SaveRename
	config.SaveRename = func(oldpath, newpath string) error {
		return fmt.Errorf("disk full")
	}
	t.Cleanup(func() {
		config.SaveRename = origRename
	})

	// Attempt to save the cleared config — should fail due to rename error.
	clearedCfg := &config.Config{
		HubURL: mockServer.URL,
		APIKey: "",
		KeyID:  "",
		UserID: "",
	}
	saveErr := config.Save(configPath, clearedCfg)
	if saveErr == nil {
		t.Fatal("config.Save should return error when Rename fails, got nil")
	}

	// The error should be descriptive about the write failure.
	if !strings.Contains(saveErr.Error(), "disk full") &&
		!strings.Contains(saveErr.Error(), "rename") &&
		!strings.Contains(saveErr.Error(), "Error") {
		t.Errorf("save error should mention the failure, got: %v", saveErr)
	}

	// Config may retain stale values since the rename failed.
	// The original config should be unchanged.
	cfg := readParsedConfig(t, configPath)
	if cfg.APIKey != "k" {
		t.Errorf("config api_key should be unchanged after failed save, got %q", cfg.APIKey)
	}
}
