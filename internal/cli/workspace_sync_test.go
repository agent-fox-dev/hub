package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ========================================================================
// Spec 13 Task 5.1: CLI workspace create with sync disabled
// (TS-13-6)
// Requirements: 13-REQ-2.3
// ========================================================================

// TS-13-6: Verifies that 'afc workspace create --setting sync_enabled=false'
// passes the sync_enabled setting in the API request body and the workspace
// is created with sync disabled.
// Requirement: 13-REQ-2.3
func TestCLI_WorkspaceCreate_SyncDisabled(t *testing.T) {
	var capturedSettings any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/workspaces" {
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, errorResp{})
				return
			}
			capturedSettings = req["settings"]
			resp := map[string]any{
				"slug":     req["slug"],
				"git_url":  req["git_url"],
				"owner_id": "test-user-id",
				"status":   "pending",
				"settings": map[string]any{"sync_enabled": false},
			}
			writeJSON(w, http.StatusCreated, resp)
			return
		}
		writeJSON(w, http.StatusNotFound, errorResp{})
	}))
	defer server.Close()

	stdout, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--git-url", "https://github.com/example/repo.git",
		"--slug", "cli-ws", "--setting", "sync_enabled=false")

	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}

	// Verify the settings were sent in the request body as given.
	got, _ := json.Marshal(capturedSettings)
	if string(got) != `{"sync_enabled":false}` {
		t.Errorf("captured settings = %s; want %s", got, `{"sync_enabled":false}`)
	}

	// Verify the output carries the setting.
	if stdout == "" {
		t.Fatal("stdout is empty")
	}
	var resp map[string]any
	if jsonErr := json.Unmarshal([]byte(stdout), &resp); jsonErr != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", jsonErr, stdout)
	}
	settings, _ := resp["settings"].(map[string]any)
	if enabled, ok := settings["sync_enabled"].(bool); !ok || enabled {
		t.Errorf("response settings.sync_enabled = %v; want false", settings["sync_enabled"])
	}
}

// Verifies that 'afc workspace create' without --setting does NOT include
// settings in the request body (the server applies every default).
func TestCLI_WorkspaceCreate_NoSettingsOmitted(t *testing.T) {
	var hasSettings bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/workspaces" {
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, errorResp{})
				return
			}
			_, hasSettings = req["settings"]
			resp := map[string]any{
				"slug":     req["slug"],
				"git_url":  req["git_url"],
				"owner_id": "test-user-id",
				"status":   "pending",
				"settings": map[string]any{"sync_enabled": true},
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

	if hasSettings {
		t.Error("settings was included in request body when no --setting flag was set; want omitted")
	}
}
