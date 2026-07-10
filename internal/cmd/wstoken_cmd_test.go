package cmd_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/config"
)

// ---------------------------------------------------------------------------
// 11.1 — Workspace Token Create Command Tests (REQ-12)
// ---------------------------------------------------------------------------

// TestWorkspaceTokenCreateCmdSuccess verifies that "afc workspace token create"
// sends POST /api/v1/workspaces/:slug/tokens with expires as integer, prints
// pretty-printed JSON to stdout, and does NOT persist the token to config.
// TS-05-35 (command level)
func TestWorkspaceTokenCreateCmdSuccess(t *testing.T) {
	var requests []capturedRequest
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, capturedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(body),
			Header: r.Header.Clone(),
		})

		if r.Method == "POST" && r.URL.Path == "/api/v1/workspaces/my-ws/tokens" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"id":"tok-1","token":"secret-val","label":"ci","expires_at":"2024-04-01T00:00:00Z"}`)
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

	result := runCLI(t, []string{
		"workspace", "token", "create",
		"--workspace", "my-ws",
		"--expires", "30",
	})

	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %s", result.ExitCode, result.Stderr)
	}

	// Verify POST /api/v1/workspaces/my-ws/tokens was called with Bearer auth.
	postFound := false
	for _, req := range requests {
		if req.Method == "POST" && req.Path == "/api/v1/workspaces/my-ws/tokens" {
			postFound = true
			authHeader := req.Header.Get("Authorization")
			if authHeader != "Bearer k" {
				t.Errorf("Authorization header = %q, want 'Bearer k'", authHeader)
			}

			// Verify expires is an integer in the request body.
			var reqBody map[string]any
			if err := json.Unmarshal([]byte(req.Body), &reqBody); err != nil {
				t.Fatalf("failed to parse POST body: %v", err)
			}
			expiresVal, ok := reqBody["expires"]
			if !ok {
				t.Error("payload should contain 'expires' key")
			} else {
				expiresFloat, ok := expiresVal.(float64)
				if !ok {
					t.Errorf("expires should be a number, got %T", expiresVal)
				} else if int(expiresFloat) != 30 {
					t.Errorf("expires = %v, want 30", expiresFloat)
				}
			}
			break
		}
	}
	if !postFound {
		t.Error("POST /api/v1/workspaces/my-ws/tokens was not called")
	}

	// Verify stdout contains pretty-printed JSON with 2-space indent.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result.Stdout), &parsed); err != nil {
		t.Fatalf("stdout is not valid JSON: %v; stdout: %q", err, result.Stdout)
	}
	if parsed["token"] != "secret-val" {
		t.Errorf("stdout token = %v, want 'secret-val'", parsed["token"])
	}
	if !strings.Contains(result.Stdout, "  ") {
		t.Error("stdout should be pretty-printed with 2-space indentation")
	}

	// CRITICAL: Verify the token secret is NOT in config.
	configContents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}
	if strings.Contains(string(configContents), "secret-val") {
		t.Error("config file should NOT contain the token secret value")
	}
}

// TestWorkspaceTokenCreateCmdLabelOmitted verifies that label is omitted from
// the POST payload when --label is not provided, and expires is always present.
// TS-05-36 (command level)
func TestWorkspaceTokenCreateCmdLabelOmitted(t *testing.T) {
	var requests []capturedRequest
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, capturedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(body),
			Header: r.Header.Clone(),
		})

		if r.Method == "POST" && r.URL.Path == "/api/v1/workspaces/ws1/tokens" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"id":"t1","token":"s"}`)
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

	result := runCLI(t, []string{
		"workspace", "token", "create",
		"--workspace", "ws1",
		// No --label flag provided.
	})

	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %s", result.ExitCode, result.Stderr)
	}

	// Verify POST body omits label and includes expires as integer.
	for _, req := range requests {
		if req.Method == "POST" && req.Path == "/api/v1/workspaces/ws1/tokens" {
			var reqBody map[string]any
			if err := json.Unmarshal([]byte(req.Body), &reqBody); err != nil {
				t.Fatalf("failed to parse POST body: %v", err)
			}
			if _, exists := reqBody["label"]; exists {
				t.Error("payload should NOT contain 'label' key when --label is not provided")
			}
			expiresVal, ok := reqBody["expires"]
			if !ok {
				t.Error("payload should contain 'expires' key")
			} else if _, ok := expiresVal.(float64); !ok {
				t.Errorf("expires should be a number, got %T", expiresVal)
			}
			return
		}
	}
	t.Error("POST /api/v1/workspaces/ws1/tokens was not called")
}

// TestWorkspaceTokenCreateCmdWithLabel verifies that --label is included in the
// payload when provided.
func TestWorkspaceTokenCreateCmdWithLabel(t *testing.T) {
	var requests []capturedRequest
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, capturedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(body),
			Header: r.Header.Clone(),
		})

		if r.Method == "POST" && r.URL.Path == "/api/v1/workspaces/ws1/tokens" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"id":"t1","token":"s","label":"ci-token"}`)
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

	result := runCLI(t, []string{
		"workspace", "token", "create",
		"--workspace", "ws1",
		"--label", "ci-token",
		"--expires", "60",
	})

	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %s", result.ExitCode, result.Stderr)
	}

	for _, req := range requests {
		if req.Method == "POST" && req.Path == "/api/v1/workspaces/ws1/tokens" {
			var reqBody map[string]any
			if err := json.Unmarshal([]byte(req.Body), &reqBody); err != nil {
				t.Fatalf("failed to parse POST body: %v", err)
			}
			if reqBody["label"] != "ci-token" {
				t.Errorf("payload label = %v, want 'ci-token'", reqBody["label"])
			}
			if int(reqBody["expires"].(float64)) != 60 {
				t.Errorf("payload expires = %v, want 60", reqBody["expires"])
			}
			return
		}
	}
	t.Error("POST /api/v1/workspaces/ws1/tokens was not called")
}

// TestWorkspaceTokenCreateCmdForbidden verifies that a non-2xx response from
// POST /api/v1/workspaces/:slug/tokens prints the error to stderr and exits
// with code 1 without storing anything in config.
// TS-05-E19 (command level)
func TestWorkspaceTokenCreateCmdForbidden(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/workspaces/ws1/tokens" {
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
		APIKey: "k",
		UserID: "uid",
		KeyID:  "kid",
	})

	result := runCLI(t, []string{
		"workspace", "token", "create",
		"--workspace", "ws1",
		"--expires", "30",
	})

	if result.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", result.ExitCode)
	}

	if !strings.Contains(result.Stderr, "forbidden") {
		t.Errorf("stderr should contain 'forbidden', got: %q", result.Stderr)
	}

	// Config should be unchanged.
	cfg := readConfig(t, configPath)
	if cfg.APIKey != "k" {
		t.Errorf("config api_key should be unchanged, got %q", cfg.APIKey)
	}
}

// ---------------------------------------------------------------------------
// 11.2 — Workspace Token List and Revoke Command Tests (REQ-13, REQ-14)
// ---------------------------------------------------------------------------

// TestWorkspaceTokenListCmdSuccess verifies that "afc workspace token list"
// sends GET /api/v1/workspaces/:slug/tokens with Bearer auth and prints
// pretty-printed metadata JSON to stdout.
// TS-05-37 (command level)
func TestWorkspaceTokenListCmdSuccess(t *testing.T) {
	var requests []capturedRequest
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, capturedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(body),
			Header: r.Header.Clone(),
		})

		if r.Method == "GET" && r.URL.Path == "/api/v1/workspaces/ws1/tokens" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `[{"id":"t1","label":"ci","created_at":"2024-01-01T00:00:00Z"}]`)
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

	result := runCLI(t, []string{
		"workspace", "token", "list",
		"--workspace", "ws1",
	})

	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %s", result.ExitCode, result.Stderr)
	}

	// Verify GET was called with Bearer auth.
	found := false
	for _, req := range requests {
		if req.Method == "GET" && req.Path == "/api/v1/workspaces/ws1/tokens" {
			found = true
			authHeader := req.Header.Get("Authorization")
			if authHeader != "Bearer k" {
				t.Errorf("Authorization header = %q, want 'Bearer k'", authHeader)
			}
			break
		}
	}
	if !found {
		t.Error("GET /api/v1/workspaces/ws1/tokens was not called")
	}

	// Verify stdout contains pretty-printed JSON.
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(result.Stdout), &parsed); err != nil {
		t.Fatalf("stdout is not valid JSON: %v; stdout: %q", err, result.Stdout)
	}
	if len(parsed) == 0 || parsed[0]["id"] != "t1" {
		t.Errorf("unexpected JSON content: %s", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "  ") {
		t.Error("stdout should be pretty-printed with 2-space indentation")
	}
}

// TestWorkspaceTokenRevokeCmdSuccess verifies that "afc workspace token revoke"
// sends DELETE /api/v1/workspaces/:slug/tokens/:token_id and prints
// 'Token <token-id> revoked.' to stderr on success.
// TS-05-38 (command level)
func TestWorkspaceTokenRevokeCmdSuccess(t *testing.T) {
	var requests []capturedRequest
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, capturedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(body),
			Header: r.Header.Clone(),
		})

		if r.Method == "DELETE" && r.URL.Path == "/api/v1/workspaces/ws1/tokens/tok-abc" {
			w.WriteHeader(http.StatusNoContent) // 204
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

	result := runCLI(t, []string{
		"workspace", "token", "revoke",
		"--workspace", "ws1",
		"tok-abc",
	})

	if result.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %s", result.ExitCode, result.Stderr)
	}

	// Verify DELETE was called with Bearer auth.
	found := false
	for _, req := range requests {
		if req.Method == "DELETE" && req.Path == "/api/v1/workspaces/ws1/tokens/tok-abc" {
			found = true
			authHeader := req.Header.Get("Authorization")
			if authHeader != "Bearer k" {
				t.Errorf("Authorization header = %q, want 'Bearer k'", authHeader)
			}
			break
		}
	}
	if !found {
		t.Error("DELETE /api/v1/workspaces/ws1/tokens/tok-abc was not called")
	}

	// Verify stderr has the revoked message.
	if !strings.Contains(result.Stderr, "Token tok-abc revoked.") {
		t.Errorf("stderr should contain 'Token tok-abc revoked.', got: %q", result.Stderr)
	}

	// Stdout should be empty (no JSON on revoke).
	if strings.TrimSpace(result.Stdout) != "" {
		t.Errorf("stdout should be empty for token revoke, got: %q", result.Stdout)
	}
}

// TestWorkspaceTokenRevokeCmdNotFound verifies that a non-2xx response from
// DELETE /api/v1/workspaces/:slug/tokens/:token_id prints the error to stderr
// and exits with code 1.
// TS-05-E20 (command level)
func TestWorkspaceTokenRevokeCmdNotFound(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" && r.URL.Path == "/api/v1/workspaces/ws1/tokens/t1" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":{"code":404,"message":"token not found"}}`)
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

	result := runCLI(t, []string{
		"workspace", "token", "revoke",
		"--workspace", "ws1",
		"t1",
	})

	if result.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", result.ExitCode)
	}

	if !strings.Contains(result.Stderr, "token not found") {
		t.Errorf("stderr should contain 'token not found', got: %q", result.Stderr)
	}

	// Stdout should be empty.
	if strings.TrimSpace(result.Stdout) != "" {
		t.Errorf("stdout should be empty on error, got: %q", result.Stdout)
	}
}
