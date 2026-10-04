package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
)

// ========================================================================
// Spec 23 Task 5: CLI afc patch reset-to-origin
// (TS-23-33, TS-23-34, TS-23-35, TS-23-36)
// Requirements: 23-REQ-5
// ========================================================================

// TS-23-33: afc patch reset-to-origin posts to the endpoint with no body
// and prints the JSON result.
// Verifies: 23-REQ-5.1
func TestCLI_PatchResetToOrigin_Success_TS2333(t *testing.T) {
	var (
		capturedMethod string
		capturedPath   string
		capturedBody   string
	)

	responseJSON := map[string]any{
		"id":                "7",
		"workspace_slug":    "ws",
		"branch_name":       "feature/x",
		"status":            "active",
		"replaced_sha":      "abc123def456abc123def456abc123def456abc1",
		"rebuild_triggered": true,
		"rebuild_job_id":    "job-42",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/workspaces/{slug}/patches/{id}/reset-to-origin", func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedPath = r.URL.Path
		bodyBytes, _ := io.ReadAll(r.Body)
		capturedBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(responseJSON)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	stdout, _, err := runPatchCmd(t, server.URL, "test-api-key",
		"reset-to-origin", "ws", "7")

	if err != nil {
		t.Fatalf("command returned error: %v", err)
	}

	// Verify the stub received the correct method and path.
	if capturedMethod != "POST" {
		t.Errorf("expected method POST, got %s", capturedMethod)
	}
	if capturedPath != "/api/v1/workspaces/ws/patches/7/reset-to-origin" {
		t.Errorf("expected path /api/v1/workspaces/ws/patches/7/reset-to-origin, got %s", capturedPath)
	}

	// Verify the request body was empty.
	if strings.TrimSpace(capturedBody) != "" {
		t.Errorf("expected empty request body, got %q", capturedBody)
	}

	// Verify stdout holds the response JSON.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
	}
	if parsed["replaced_sha"] != "abc123def456abc123def456abc123def456abc1" {
		t.Errorf("expected replaced_sha in output, got %v", parsed["replaced_sha"])
	}
	if parsed["rebuild_triggered"] != true {
		t.Errorf("expected rebuild_triggered=true in output, got %v", parsed["rebuild_triggered"])
	}
	if parsed["rebuild_job_id"] != "job-42" {
		t.Errorf("expected rebuild_job_id=job-42 in output, got %v", parsed["rebuild_job_id"])
	}
}

// TS-23-34: The command rejects any argument count other than two and sends
// no request.
// Verifies: 23-REQ-5.2
func TestCLI_PatchResetToOrigin_WrongArgCount_TS2334(t *testing.T) {
	var requestCount atomic.Int64

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	cases := []struct {
		name string
		args []string
	}{
		{"zero args", []string{"reset-to-origin"}},
		{"one arg", []string{"reset-to-origin", "ws"}},
		{"three args", []string{"reset-to-origin", "ws", "1", "x"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requestCount.Store(0)
			_, _, err := runPatchCmd(t, server.URL, "test-api-key", tc.args...)
			if err == nil {
				t.Errorf("expected error for args %v, got nil", tc.args)
			}
			if requestCount.Load() != 0 {
				t.Errorf("expected zero requests for args %v, got %d", tc.args, requestCount.Load())
			}
		})
	}
}

// TS-23-35: A server error status propagates through CLIHandleError with a
// non-zero exit.
// Verifies: 23-REQ-5.3
func TestCLI_PatchResetToOrigin_ServerError_TS2335(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		response   map[string]any
		wantMsg    string
	}{
		{
			name:       "409 status conflict",
			statusCode: http.StatusConflict,
			response: map[string]any{
				"error": map[string]any{
					"code":    409,
					"message": "patch status deleted cannot be reset",
				},
			},
			wantMsg: "cannot be reset",
		},
		{
			name:       "502 origin fetch failed",
			statusCode: http.StatusBadGateway,
			response: map[string]any{
				"error": map[string]any{
					"code":       502,
					"message":    "origin fetch failed",
					"error_type": "origin_fetch_failed",
				},
			},
			wantMsg: "origin fetch failed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("POST /api/v1/workspaces/{slug}/patches/{id}/reset-to-origin", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.statusCode)
				json.NewEncoder(w).Encode(tc.response)
			})
			server := httptest.NewServer(mux)
			defer server.Close()

			stdout, stderr, err := runPatchCmd(t, server.URL, "test-api-key",
				"reset-to-origin", "ws", "7")

			if err == nil {
				t.Fatal("expected error for server error response, got nil")
			}

			// The server message should be reported somewhere in the output.
			combined := stdout + stderr + err.Error()
			if !strings.Contains(combined, tc.wantMsg) {
				t.Errorf("expected output to contain %q; got stdout=%q stderr=%q err=%v", tc.wantMsg, stdout, stderr, err)
			}

			// SilenceErrors and SilenceUsage should prevent usage text.
			if strings.Contains(stdout, "Usage:") || strings.Contains(stderr, "Usage:") {
				t.Error("usage text should not be printed on server error")
			}
		})
	}

	// Verify SilenceErrors and SilenceUsage are set on the command.
	patchCmd := PatchCmd()
	var resetCmd *cobra.Command
	for _, c := range patchCmd.Commands() {
		if c.Name() == "reset-to-origin" {
			resetCmd = c
			break
		}
	}
	if resetCmd == nil {
		t.Fatal("reset-to-origin subcommand not found")
	}
	if !resetCmd.SilenceErrors {
		t.Error("SilenceErrors should be true")
	}
	if !resetCmd.SilenceUsage {
		t.Error("SilenceUsage should be true")
	}
}

// TS-23-36: Help text names the backup ref and replaced_sha and the command
// defines no flags.
// Verifies: 23-REQ-5.4
func TestCLI_PatchResetToOrigin_HelpText_TS2336(t *testing.T) {
	patchCmd := PatchCmd()
	var resetCmd *cobra.Command
	for _, c := range patchCmd.Commands() {
		if c.Name() == "reset-to-origin" {
			resetCmd = c
			break
		}
	}
	if resetCmd == nil {
		t.Fatal("reset-to-origin subcommand not found")
	}

	// Render help text (Long description is the primary help text).
	help := resetCmd.Long
	if help == "" {
		help = resetCmd.Short
	}

	if !strings.Contains(help, "refs/hub/replaced/") {
		t.Errorf("help text should mention refs/hub/replaced/; got: %s", help)
	}
	if !strings.Contains(help, "replaced_sha") {
		t.Errorf("help text should mention replaced_sha; got: %s", help)
	}

	// The command should define no local flags and no --force flag.
	if resetCmd.Flags().Lookup("force") != nil {
		t.Error("reset-to-origin should not define a --force flag")
	}

	// The command should have no local flags.
	if resetCmd.HasLocalFlags() {
		t.Error("reset-to-origin should not define any local flags")
	}
}
