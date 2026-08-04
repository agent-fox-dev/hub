package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ========================================================================
// Spec 13 Task 5.1: CLI workspace create --sync-mode flag
// (TS-13-6, 13-REQ-2.E3)
// Requirements: 13-REQ-2.3
// ========================================================================

// TS-13-6: Verifies that 'afc workspace create --sync-mode disabled' passes
// sync_mode in the API request body and the workspace is created with the
// specified mode.
// Requirement: 13-REQ-2.3
func TestCLI_WorkspaceCreate_SyncMode(t *testing.T) {
	var capturedSyncMode string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/workspaces" {
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, errorResp{})
				return
			}
			if sm, ok := req["sync_mode"].(string); ok {
				capturedSyncMode = sm
			}
			resp := map[string]any{
				"slug":      req["slug"],
				"git_url":   req["git_url"],
				"owner_id":  "test-user-id",
				"status":    "active",
				"sync_mode": capturedSyncMode,
			}
			writeJSON(w, http.StatusCreated, resp)
			return
		}
		writeJSON(w, http.StatusNotFound, errorResp{})
	}))
	defer server.Close()

	stdout, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--git-url", "https://github.com/example/repo.git",
		"--slug", "cli-ws", "--sync-mode", "disabled")

	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}

	// Verify sync_mode was sent in the request body.
	if capturedSyncMode != "disabled" {
		t.Errorf("captured sync_mode = %q; want %q", capturedSyncMode, "disabled")
	}

	// Verify the output contains "disabled".
	if stdout == "" {
		t.Fatal("stdout is empty")
	}
	var resp map[string]any
	if jsonErr := json.Unmarshal([]byte(stdout), &resp); jsonErr != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", jsonErr, stdout)
	}
	if sm, ok := resp["sync_mode"].(string); !ok || sm != "disabled" {
		t.Errorf("response sync_mode = %v; want %q", resp["sync_mode"], "disabled")
	}
}

// 13-REQ-2.E3: Verifies that 'afc workspace create --sync-mode <invalid>'
// rejects the command with a non-zero exit code before making any API call.
// Requirement: 13-REQ-2.3
func TestCLI_WorkspaceCreate_InvalidSyncMode(t *testing.T) {
	apiCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalled = true
		writeJSON(w, http.StatusOK, map[string]any{})
	}))
	defer server.Close()

	stdout, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--git-url", "https://github.com/example/repo.git",
		"--slug", "bad-mode-ws", "--sync-mode", "full_sync")

	if err == nil {
		t.Fatal("expected error for invalid sync_mode, got nil")
	}

	if apiCalled {
		t.Error("API was called despite invalid sync_mode; want client-side rejection")
	}

	// Verify the output contains an error envelope (CLIHandleError writes to stdout).
	if !hasErrorEnvelope(stdout) {
		t.Errorf("expected error envelope in stdout; got: %s", stdout)
	}
}

// Verifies that 'afc workspace create' without --sync-mode does NOT include
// sync_mode in the request body (server defaults to 'pull_only').
func TestCLI_WorkspaceCreate_NoSyncModeOmitted(t *testing.T) {
	var hasSyncMode bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/workspaces" {
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, errorResp{})
				return
			}
			_, hasSyncMode = req["sync_mode"]
			resp := map[string]any{
				"slug":      req["slug"],
				"git_url":   req["git_url"],
				"owner_id":  "test-user-id",
				"status":    "active",
				"sync_mode": "pull_only",
			}
			writeJSON(w, http.StatusCreated, resp)
			return
		}
		writeJSON(w, http.StatusNotFound, errorResp{})
	}))
	defer server.Close()

	_, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--git-url", "https://github.com/example/repo.git",
		"--slug", "default-mode-ws")

	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}

	if hasSyncMode {
		t.Error("sync_mode was included in request body when --sync-mode flag was not set; want omitted")
	}
}
