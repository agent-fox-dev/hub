package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ========================================================================
// Spec 13 Task 9.2: CLI workspace sync and reclone commands
// Requirements: 13-REQ-7.3, 13-REQ-7.E4, 13-REQ-8
// ========================================================================

// TS-13-25 (CLI sync): Verifies that 'afc workspace sync <slug>' calls
// POST /api/v1/workspaces/:slug/sync and prints the workspace JSON.
func TestCLI_WorkspaceSync_Success(t *testing.T) {
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.RequestURI()
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/workspaces/") &&
			strings.HasSuffix(r.URL.Path, "/sync") {
			resp := map[string]any{
				"slug":        "sync-ws",
				"git_url":     "https://github.com/example/repo.git",
				"status":      "active",
				"sync_mode":   "pull_only",
				"sync_status": "idle",
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}
		writeJSON(w, http.StatusNotFound, errorResp{})
	}))
	defer server.Close()

	stdout, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"sync", "sync-ws")

	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}

	// Verify the correct endpoint was called.
	if capturedPath != "/api/v1/workspaces/sync-ws/sync" {
		t.Errorf("captured path = %q; want %q", capturedPath, "/api/v1/workspaces/sync-ws/sync")
	}

	// Verify the output is valid JSON with sync fields.
	var resp map[string]any
	if jsonErr := json.Unmarshal([]byte(stdout), &resp); jsonErr != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", jsonErr, stdout)
	}
	if resp["sync_status"] != "idle" {
		t.Errorf("sync_status = %v; want %q", resp["sync_status"], "idle")
	}
}

// Verifies that 'afc workspace sync <slug> --reset-to-upstream' appends
// ?reset_to_upstream=true to the API request.
func TestCLI_WorkspaceSync_ResetToUpstream(t *testing.T) {
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.RequestURI()
		resp := map[string]any{
			"slug":        "sync-ws",
			"git_url":     "https://github.com/example/repo.git",
			"status":      "active",
			"sync_mode":   "pull_only",
			"sync_status": "idle",
		}
		writeJSON(w, http.StatusOK, resp)
	}))
	defer server.Close()

	stdout, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"sync", "sync-ws", "--reset-to-upstream")

	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}

	if capturedPath != "/api/v1/workspaces/sync-ws/sync?reset_to_upstream=true" {
		t.Errorf("captured path = %q; want %q",
			capturedPath, "/api/v1/workspaces/sync-ws/sync?reset_to_upstream=true")
	}

	if stdout == "" {
		t.Fatal("stdout is empty")
	}
}

// Verifies that 'afc workspace sync <slug>' handles API errors correctly.
func TestCLI_WorkspaceSync_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e := errorResp{}
		e.Error.Code = http.StatusBadRequest
		e.Error.Message = "sync is disabled for this workspace"
		writeJSON(w, http.StatusBadRequest, e)
	}))
	defer server.Close()

	stdout, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"sync", "disabled-ws")

	if err == nil {
		t.Error("expected error for disabled sync; got nil")
	}
	if !hasErrorEnvelope(stdout) {
		t.Errorf("stdout should contain error envelope; got: %s", stdout)
	}
}

// 13-REQ-7.3: Verifies that 'afc workspace reclone <slug> --confirm' calls
// POST /api/v1/workspaces/:slug/reclone and prints the workspace JSON with
// clone_status='pending'.
func TestCLI_WorkspaceReclone_Success(t *testing.T) {
	var capturedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.RequestURI()
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reclone") {
			resp := map[string]any{
				"slug":         "reclone-ws",
				"git_url":      "https://github.com/example/repo.git",
				"status":       "active",
				"clone_status": "pending",
				"sync_status":  "idle",
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}
		writeJSON(w, http.StatusNotFound, errorResp{})
	}))
	defer server.Close()

	stdout, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"reclone", "reclone-ws", "--confirm")

	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}

	// Verify the correct endpoint was called.
	if capturedPath != "/api/v1/workspaces/reclone-ws/reclone" {
		t.Errorf("captured path = %q; want %q",
			capturedPath, "/api/v1/workspaces/reclone-ws/reclone")
	}

	// Verify the output contains clone_status='pending'.
	var resp map[string]any
	if jsonErr := json.Unmarshal([]byte(stdout), &resp); jsonErr != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", jsonErr, stdout)
	}
	if resp["clone_status"] != "pending" {
		t.Errorf("clone_status = %v; want %q", resp["clone_status"], "pending")
	}
	if resp["status"] != "active" {
		t.Errorf("status = %v; want %q", resp["status"], "active")
	}
}

// 13-REQ-7.E4: Verifies that 'afc workspace reclone <slug>' without --confirm
// rejects the command with a non-zero exit code before making any API call.
func TestCLI_WorkspaceReclone_NoConfirm(t *testing.T) {
	apiCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalled = true
		writeJSON(w, http.StatusOK, map[string]any{})
	}))
	defer server.Close()

	stdout, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"reclone", "reclone-ws")

	if err == nil {
		t.Error("expected error when --confirm is omitted; got nil")
	}

	if apiCalled {
		t.Error("API was called despite missing --confirm; want client-side rejection")
	}

	msg := errorMessage(stdout)
	if !strings.Contains(msg, "confirm") && !strings.Contains(msg, "--confirm") {
		t.Errorf("error message should mention --confirm; got: %s", msg)
	}
}

// Verifies that 'afc workspace reclone <slug> --confirm' handles API errors.
func TestCLI_WorkspaceReclone_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e := errorResp{}
		e.Error.Code = http.StatusConflict
		e.Error.Message = "clone operation already in progress"
		writeJSON(w, http.StatusConflict, e)
	}))
	defer server.Close()

	stdout, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"reclone", "reclone-ws", "--confirm")

	if err == nil {
		t.Error("expected error for conflicting reclone; got nil")
	}
	if !hasErrorEnvelope(stdout) {
		t.Errorf("stdout should contain error envelope; got: %s", stdout)
	}
}

// Verifies that the workspace command tree includes sync and reclone subcommands.
func TestCLI_CommandTree_SyncAndReclone(t *testing.T) {
	stdout, _, err := runRootCmd(t, "workspace", "--help")

	if err != nil {
		t.Fatalf("workspace --help returned error: %v", err)
	}

	for _, sub := range []string{"sync", "reclone"} {
		if !strings.Contains(stdout, sub) {
			t.Errorf("workspace help output missing subcommand %q", sub)
		}
	}
}
