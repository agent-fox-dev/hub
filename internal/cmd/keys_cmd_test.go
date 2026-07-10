package cmd_test

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

	"github.com/BurntSushi/toml"
	"github.com/agent-fox-dev/hub/internal/config"
	"github.com/agent-fox-dev/hub/internal/keys"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// setupConfigHome creates a temp dir, writes a config.toml inside, and sets
// HOME to the temp dir for the duration of the test. Returns the config path.
func setupConfigHome(t *testing.T, cfg config.Config) string {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	dir := filepath.Join(tmpHome, ".af")
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

// readConfig parses the TOML config file at the given path.
func readConfig(t *testing.T, path string) config.Config {
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

// capturedRequest records the HTTP method, path, headers, and body of a request.
type capturedRequest struct {
	Method string
	Path   string
	Body   string
	Header http.Header
}

// ---------------------------------------------------------------------------
// 9.1 — Keys List Command Tests (REQ-7)
// ---------------------------------------------------------------------------

// TestKeysListCmdSuccess verifies that "afc keys list" sends GET /api/v1/keys
// with Bearer auth header and prints the pretty-printed JSON response to stdout.
// TS-05-22 (command level)
func TestKeysListCmdSuccess(t *testing.T) {
	var requests []capturedRequest
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, capturedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(body),
			Header: r.Header.Clone(),
		})

		if r.Method == "GET" && r.URL.Path == "/api/v1/keys" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `[{"key_id":"k1","created_at":"2024-01-01T00:00:00Z"}]`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	// Clear env vars that could interfere.
	t.Setenv("AF_HUB_URL", "")
	t.Setenv("AF_HUB_USER_ID", "")
	t.Setenv("AF_HUB_API_KEY", "")

	setupConfigHome(t, config.Config{
		HubURL: mockServer.URL,
		APIKey: "test-key",
		UserID: "test-user",
		KeyID:  "test-kid",
	})

	result := runCLI(t, []string{"keys", "list"})

	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %s", result.ExitCode, result.Stderr)
	}

	// Verify GET /api/v1/keys was called with Bearer auth.
	found := false
	for _, req := range requests {
		if req.Method == "GET" && req.Path == "/api/v1/keys" {
			found = true
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

	// Verify stdout contains pretty-printed JSON with 2-space indentation.
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(result.Stdout), &parsed); err != nil {
		t.Fatalf("stdout is not valid JSON: %v; stdout: %q", err, result.Stdout)
	}
	if len(parsed) == 0 || parsed[0]["key_id"] != "k1" {
		t.Errorf("unexpected JSON content: %s", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "  ") {
		t.Error("stdout should be pretty-printed with 2-space indentation")
	}
}

// TestKeysListCmdUnauthorized verifies that a non-2xx response from
// GET /api/v1/keys causes the CLI to print the JSON error message to stderr
// and exit with code 1; stdout should be empty.
// TS-05-E13 (command level)
func TestKeysListCmdUnauthorized(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/api/v1/keys" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"code":401,"message":"unauthorized"}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	t.Setenv("AF_HUB_URL", "")
	t.Setenv("AF_HUB_USER_ID", "")
	t.Setenv("AF_HUB_API_KEY", "")

	setupConfigHome(t, config.Config{
		HubURL: mockServer.URL,
		APIKey: "bad-key",
		UserID: "test-user",
		KeyID:  "test-kid",
	})

	result := runCLI(t, []string{"keys", "list"})

	if result.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", result.ExitCode)
	}

	// Error message should contain the server error.
	if !strings.Contains(result.Stderr, "unauthorized") {
		t.Errorf("stderr should contain 'unauthorized', got: %q", result.Stderr)
	}

	// Stdout should be empty (no JSON output on error).
	if strings.TrimSpace(result.Stdout) != "" {
		t.Errorf("stdout should be empty on error, got: %q", result.Stdout)
	}
}

// ---------------------------------------------------------------------------
// 9.2 — Keys Refresh Command Tests (REQ-8)
// ---------------------------------------------------------------------------

// TestKeysRefreshCmdSuccess verifies that "afc keys refresh" sends
// POST /api/v1/keys/:key_id/refresh, updates api_key and key_id in config,
// and prints the response JSON to stdout.
// TS-05-23 (command level)
func TestKeysRefreshCmdSuccess(t *testing.T) {
	var requests []capturedRequest
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, capturedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(body),
			Header: r.Header.Clone(),
		})

		if r.Method == "POST" && r.URL.Path == "/api/v1/keys/old-kid/refresh" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			resp := keys.RefreshResponse{
				KeyID:  "new-kid",
				Token:  "af_new-kid_newsecret",
				Secret: "newsecret",
			}
			data, _ := json.Marshal(resp)
			w.Write(data)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	t.Setenv("AF_HUB_URL", "")
	t.Setenv("AF_HUB_USER_ID", "")
	t.Setenv("AF_HUB_API_KEY", "")

	configPath := setupConfigHome(t, config.Config{
		HubURL: mockServer.URL,
		APIKey: "old-key",
		UserID: "test-user",
		KeyID:  "old-kid",
	})

	result := runCLI(t, []string{"keys", "refresh"})

	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %s", result.ExitCode, result.Stderr)
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

	// Verify config was updated with new credentials.
	cfg := readConfig(t, configPath)
	if cfg.APIKey != "af_new-kid_newsecret" {
		t.Errorf("config api_key = %q, want 'af_new-kid_newsecret'", cfg.APIKey)
	}
	if cfg.KeyID != "new-kid" {
		t.Errorf("config key_id = %q, want 'new-kid'", cfg.KeyID)
	}

	// Verify stdout contains the pretty-printed response JSON.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result.Stdout), &parsed); err != nil {
		t.Fatalf("stdout is not valid JSON: %v; stdout: %q", err, result.Stdout)
	}
	if parsed["key_id"] != "new-kid" {
		t.Errorf("stdout key_id = %v, want 'new-kid'", parsed["key_id"])
	}
}

// TestKeysRefreshCmdMissingKeyID verifies that "afc keys refresh" with missing
// key_id in config prints the exact error message and exits with code 1
// without any network request.
// TS-05-24 (command level)
func TestKeysRefreshCmdMissingKeyID(t *testing.T) {
	var requests []capturedRequest
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, capturedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(body),
		})
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	t.Setenv("AF_HUB_URL", "")
	t.Setenv("AF_HUB_USER_ID", "")
	t.Setenv("AF_HUB_API_KEY", "")

	setupConfigHome(t, config.Config{
		HubURL: mockServer.URL,
		APIKey: "k",
		UserID: "test-user",
		KeyID:  "", // empty — should trigger validation error
	})

	result := runCLI(t, []string{"keys", "refresh"})

	if result.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", result.ExitCode)
	}

	wantMsg := `key_id is not set. Run "afc login" first.`
	if !strings.Contains(result.Stderr, wantMsg) {
		t.Errorf("stderr = %q, want it to contain %q", result.Stderr, wantMsg)
	}

	// No network request should have been made.
	if len(requests) != 0 {
		t.Errorf("expected zero network requests, got %d", len(requests))
	}
}

// TestKeysRefreshCmdForbidden verifies that a non-2xx response from
// POST /api/v1/keys/:key_id/refresh causes the CLI to print the error
// to stderr and exit with code 1 without modifying config.
// TS-05-E14 (command level)
func TestKeysRefreshCmdForbidden(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/keys/kid/refresh" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"error":{"code":403,"message":"forbidden"}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	t.Setenv("AF_HUB_URL", "")
	t.Setenv("AF_HUB_USER_ID", "")
	t.Setenv("AF_HUB_API_KEY", "")

	configPath := setupConfigHome(t, config.Config{
		HubURL: mockServer.URL,
		APIKey: "old",
		UserID: "uid",
		KeyID:  "kid",
	})

	result := runCLI(t, []string{"keys", "refresh"})

	if result.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", result.ExitCode)
	}

	if !strings.Contains(result.Stderr, "forbidden") {
		t.Errorf("stderr should contain 'forbidden', got: %q", result.Stderr)
	}

	// Config should remain unchanged.
	cfg := readConfig(t, configPath)
	if cfg.APIKey != "old" {
		t.Errorf("config api_key should be unchanged after 403, got %q", cfg.APIKey)
	}
}

// ---------------------------------------------------------------------------
// 9.3 — Keys Revoke Command Tests (REQ-9)
// ---------------------------------------------------------------------------

// TestKeysRevokeCmdSuccess verifies that "afc keys revoke" on a 2xx response
// clears api_key, key_id, and user_id from config and prints
// 'API key revoked.' to stderr.
// TS-05-25 (command level)
func TestKeysRevokeCmdSuccess(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.URL.Path == "/api/v1/keys/my-kid" {
			w.WriteHeader(http.StatusNoContent) // 204
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	t.Setenv("AF_HUB_URL", "")
	t.Setenv("AF_HUB_USER_ID", "")
	t.Setenv("AF_HUB_API_KEY", "")

	configPath := setupConfigHome(t, config.Config{
		HubURL: mockServer.URL,
		APIKey: "my-key",
		UserID: "my-uid",
		KeyID:  "my-kid",
	})

	result := runCLI(t, []string{"keys", "revoke"})

	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %s", result.ExitCode, result.Stderr)
	}

	// Verify credentials are cleared.
	cfg := readConfig(t, configPath)
	if cfg.APIKey != "" {
		t.Errorf("config api_key = %q, want empty", cfg.APIKey)
	}
	if cfg.KeyID != "" {
		t.Errorf("config key_id = %q, want empty", cfg.KeyID)
	}
	if cfg.UserID != "" {
		t.Errorf("config user_id = %q, want empty", cfg.UserID)
	}

	// Verify status message on stderr.
	if !strings.Contains(result.Stderr, "API key revoked.") {
		t.Errorf("stderr should contain 'API key revoked.', got: %q", result.Stderr)
	}
}

// TestKeysRevokeCmdNot404 verifies that "afc keys revoke" on a 404 response
// clears local credentials and prints the correct message to stderr.
// TS-05-26 (command level)
func TestKeysRevokeCmdNot404(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.URL.Path == "/api/v1/keys/gone-kid" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"code":404,"message":"not found"}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	t.Setenv("AF_HUB_URL", "")
	t.Setenv("AF_HUB_USER_ID", "")
	t.Setenv("AF_HUB_API_KEY", "")

	configPath := setupConfigHome(t, config.Config{
		HubURL: mockServer.URL,
		APIKey: "gone-key",
		UserID: "gone-uid",
		KeyID:  "gone-kid",
	})

	result := runCLI(t, []string{"keys", "revoke"})

	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %s", result.ExitCode, result.Stderr)
	}

	// Verify credentials are cleared even on 404.
	cfg := readConfig(t, configPath)
	if cfg.APIKey != "" {
		t.Errorf("config api_key = %q, want empty after 404 revoke", cfg.APIKey)
	}
	if cfg.KeyID != "" {
		t.Errorf("config key_id = %q, want empty after 404 revoke", cfg.KeyID)
	}
	if cfg.UserID != "" {
		t.Errorf("config user_id = %q, want empty after 404 revoke", cfg.UserID)
	}

	// Verify correct status message on stderr.
	if !strings.Contains(result.Stderr, "API key not found on server. Local credentials cleared.") {
		t.Errorf("stderr should contain 404 message, got: %q", result.Stderr)
	}
}

// TestKeysRevokeCmdMissingKeyID verifies that "afc keys revoke" with missing
// key_id in config prints the exact error message and exits with code 1
// without any network request.
// TS-05-27 (command level)
func TestKeysRevokeCmdMissingKeyID(t *testing.T) {
	var requests []capturedRequest
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, capturedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(body),
		})
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	t.Setenv("AF_HUB_URL", "")
	t.Setenv("AF_HUB_USER_ID", "")
	t.Setenv("AF_HUB_API_KEY", "")

	setupConfigHome(t, config.Config{
		HubURL: mockServer.URL,
		APIKey: "k",
		UserID: "test-user",
		KeyID:  "", // empty — should trigger validation error
	})

	result := runCLI(t, []string{"keys", "revoke"})

	if result.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", result.ExitCode)
	}

	wantMsg := `key_id is not set. Run "afc login" first.`
	if !strings.Contains(result.Stderr, wantMsg) {
		t.Errorf("stderr = %q, want it to contain %q", result.Stderr, wantMsg)
	}

	// No network request should have been made.
	if len(requests) != 0 {
		t.Errorf("expected zero network requests, got %d", len(requests))
	}
}

// TestKeysRevokeCmdServerError verifies that "afc keys revoke" on a non-2xx
// non-404 response prints the error to stderr, exits with code 1, and does
// not modify config.
// TS-05-28 (command level)
func TestKeysRevokeCmdServerError(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.URL.Path == "/api/v1/keys/kid" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"code":500,"message":"internal server error"}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	t.Setenv("AF_HUB_URL", "")
	t.Setenv("AF_HUB_USER_ID", "")
	t.Setenv("AF_HUB_API_KEY", "")

	configPath := setupConfigHome(t, config.Config{
		HubURL: mockServer.URL,
		APIKey: "k",
		UserID: "uid",
		KeyID:  "kid",
	})

	result := runCLI(t, []string{"keys", "revoke"})

	if result.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", result.ExitCode)
	}

	if !strings.Contains(result.Stderr, "internal server error") {
		t.Errorf("stderr should contain 'internal server error', got: %q", result.Stderr)
	}

	// Config should remain unchanged.
	cfg := readConfig(t, configPath)
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

// TestKeysRevokeCmdAtomicWriteFailure verifies that when atomic config write
// fails after a successful server revocation, the CLI exits with code 1 and
// prints a descriptive error.
// TS-05-E15 (command level)
func TestKeysRevokeCmdAtomicWriteFailure(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.URL.Path == "/api/v1/keys/kid" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	t.Setenv("AF_HUB_URL", "")
	t.Setenv("AF_HUB_USER_ID", "")
	t.Setenv("AF_HUB_API_KEY", "")

	setupConfigHome(t, config.Config{
		HubURL: mockServer.URL,
		APIKey: "k",
		UserID: "uid",
		KeyID:  "kid",
	})

	// Inject a rename failure to simulate atomic write failure.
	origRename := config.SaveRename
	config.SaveRename = func(oldpath, newpath string) error {
		return fmt.Errorf("disk full")
	}
	t.Cleanup(func() {
		config.SaveRename = origRename
	})

	result := runCLI(t, []string{"keys", "revoke"})

	if result.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", result.ExitCode)
	}

	// Error should mention the write failure.
	if !strings.Contains(result.Stderr, "disk full") &&
		!strings.Contains(result.Stderr, "rename") &&
		!strings.Contains(result.Stderr, "Error") {
		t.Errorf("stderr should mention config write failure, got: %q", result.Stderr)
	}
}

// TestKeysListCmdStdoutStderrSeparation verifies that "afc keys revoke" puts
// the status message ('API key revoked.') on stderr only and stdout is empty.
// TS-05-41 (partial command level)
func TestKeysListCmdStdoutStderrSeparation(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.URL.Path == "/api/v1/keys/kid" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	t.Setenv("AF_HUB_URL", "")
	t.Setenv("AF_HUB_USER_ID", "")
	t.Setenv("AF_HUB_API_KEY", "")

	setupConfigHome(t, config.Config{
		HubURL: mockServer.URL,
		APIKey: "k",
		UserID: "uid",
		KeyID:  "kid",
	})

	result := runCLI(t, []string{"keys", "revoke"})

	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %s", result.ExitCode, result.Stderr)
	}

	// 'API key revoked.' should be on stderr only.
	if !strings.Contains(result.Stderr, "API key revoked.") {
		t.Errorf("stderr should contain 'API key revoked.', got: %q", result.Stderr)
	}
	if strings.Contains(result.Stdout, "API key revoked.") {
		t.Errorf("stdout should NOT contain 'API key revoked.', got: %q", result.Stdout)
	}
	// Stdout should be empty for revoke (no JSON to output).
	if strings.TrimSpace(result.Stdout) != "" {
		t.Errorf("stdout should be empty for keys revoke, got: %q", result.Stdout)
	}
}
