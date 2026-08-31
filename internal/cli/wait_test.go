package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// ===========================================================================
// TS-NS-2: 'afc rebuild submit --wait' blocks until the rebuild reaches a
// terminal state and exits 0 on success.
//
// Requirement: NS-REQ-2
// ===========================================================================

func TestRebuildCLI_Submit_Wait_Success(t *testing.T) {
	// Mock server: POST returns 202 with a queued job, GET returns "completed"
	// after the first poll.
	var pollCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"id":        "job-uuid-1",
				"type":      "rebuild",
				"key":       "my-workspace",
				"group_key": "my-workspace:integration",
				"status":    "queued",
			})
		case http.MethodGet:
			pollCount.Add(1)
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"id":           "job-uuid-1",
				"status":       "completed",
				"strategy":     "rebase",
				"created_at":   "2025-01-01T00:00:00Z",
				"completed_at": "2025-01-01T00:01:00Z",
			})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	stdout, stderr, err := runRebuildCmd(t, server.URL, "test-api-key",
		"submit", "my-workspace", "--wait", "--poll-interval", "100ms")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}
	if strings.Contains(stderr, "timeout") || strings.Contains(stderr, "Timed out") {
		t.Errorf("unexpected timeout message in stderr: %s", stderr)
	}

	// Verify stdout contains the completed status (from the polled result).
	if !strings.Contains(stdout, "completed") {
		t.Errorf("stdout should contain 'completed'; got: %s", stdout)
	}

	// Verify the status endpoint was polled at least once.
	if pollCount.Load() < 1 {
		t.Error("expected at least 1 poll request for job status")
	}
}

// ===========================================================================
// TS-NS-3: 'afc rebuild submit --wait --timeout <N>' exits non-zero with a
// timeout message when the rebuild exceeds the timeout.
//
// Requirement: NS-REQ-3
// ===========================================================================

func TestRebuildCLI_Submit_Wait_Timeout(t *testing.T) {
	// Mock server: POST returns 202, GET always returns "running" (never completes).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"id":        "job-uuid-1",
				"type":      "rebuild",
				"key":       "my-workspace",
				"group_key": "my-workspace:integration",
				"status":    "queued",
			})
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"id":         "job-uuid-1",
				"status":     "running",
				"strategy":   "rebase",
				"created_at": "2025-01-01T00:00:00Z",
			})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	_, stderr, err := runRebuildCmd(t, server.URL, "test-api-key",
		"submit", "my-workspace", "--wait", "--timeout", "500ms", "--poll-interval", "100ms")

	if err == nil {
		t.Fatal("expected non-zero exit on timeout; got nil error")
	}

	// Verify stderr contains a timeout message.
	if !strings.Contains(stderr, "Timed out") && !strings.Contains(stderr, "timeout") {
		t.Errorf("stderr should mention timeout; got: %s", stderr)
	}
}

// ===========================================================================
// TS-NS-2 (rebuild submit --wait with failed job): exits 0 because the job
// reached a terminal state.
// ===========================================================================

func TestRebuildCLI_Submit_Wait_FailedJob(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.Method {
		case http.MethodPost:
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"id":     "job-uuid-1",
				"status": "queued",
			})
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"id":     "job-uuid-1",
				"status": "failed",
				"error":  "something went wrong",
			})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	stdout, _, err := runRebuildCmd(t, server.URL, "test-api-key",
		"submit", "my-workspace", "--wait", "--poll-interval", "100ms")

	if err != nil {
		t.Fatalf("expected exit 0 (terminal state reached); got error: %v", err)
	}

	if !strings.Contains(stdout, "failed") {
		t.Errorf("stdout should contain 'failed'; got: %s", stdout)
	}
}

// ===========================================================================
// TS-NS-5 (rebuild submit without --wait): Existing behavior is unchanged.
// ===========================================================================

func TestRebuildCLI_Submit_NoWait_Unchanged(t *testing.T) {
	server, records := rebuildCLIMockServer(t, nil)
	defer server.Close()

	stdout, stderr, err := runRebuildCmd(t, server.URL, "test-api-key",
		"submit", "my-workspace")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}
	if stderr != "" {
		t.Errorf("expected empty stderr; got: %s", stderr)
	}

	// Only one request (the POST submit), no polling GETs.
	reqs := getRebuildCLIRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request without --wait; got %d", len(reqs))
	}
	if reqs[0].Method != "POST" {
		t.Errorf("method = %q; want POST", reqs[0].Method)
	}

	// Verify stdout is valid JSON with a "status" field.
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not valid JSON: %v", err)
	}
	if _, ok := result["status"]; !ok {
		t.Error("response JSON missing 'status' field")
	}
}

// ===========================================================================
// TS-NS-4: 'afc workspace sync --wait' blocks until the triggered rebuild
// reaches a terminal state.
//
// Requirement: NS-REQ-4
// ===========================================================================

func TestWorkspaceCLI_Sync_Wait_Success(t *testing.T) {
	// Mock server:
	// POST /sync returns sync response with rebuild_triggered=true and rebuild_job_id
	// GET /rebuilds/:id returns "completed" after first poll
	var pollCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/sync") {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"patches_merged":    []string{},
				"rebuild_triggered": true,
				"rebuild_job_id":    "rebuild-uuid-1",
			})
			return
		}

		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/rebuilds/") {
			pollCount.Add(1)
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"id":           "rebuild-uuid-1",
				"status":       "completed",
				"strategy":     "rebase",
				"created_at":   "2025-01-01T00:00:00Z",
				"completed_at": "2025-01-01T00:01:00Z",
			})
			return
		}

		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer server.Close()

	stdout, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"sync", "my-workspace", "--wait", "--poll-interval", "100ms")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	// Verify stdout includes both the sync response and the rebuild result.
	if !strings.Contains(stdout, "rebuild_triggered") {
		t.Errorf("stdout should contain sync response with 'rebuild_triggered'; got: %s", stdout)
	}
	if !strings.Contains(stdout, "completed") {
		t.Errorf("stdout should contain rebuild status 'completed'; got: %s", stdout)
	}

	if pollCount.Load() < 1 {
		t.Error("expected at least 1 poll request for rebuild status")
	}
}

func TestWorkspaceCLI_Sync_Wait_NoRebuild(t *testing.T) {
	// Mock server: sync response with rebuild_triggered=false (no rebuild_job_id).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/sync") {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"patches_merged":    []string{},
				"rebuild_triggered": false,
			})
			return
		}

		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer server.Close()

	stdout, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"sync", "my-workspace", "--wait", "--poll-interval", "100ms")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	// Should print the sync response and exit without polling.
	if !strings.Contains(stdout, "rebuild_triggered") {
		t.Errorf("stdout should contain sync response; got: %s", stdout)
	}
}

// ===========================================================================
// TS-NS-5 (workspace sync without --wait): Existing behavior is unchanged.
// ===========================================================================

func TestWorkspaceCLI_Sync_NoWait_Unchanged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/sync") {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"patches_merged":    []string{},
				"rebuild_triggered": true,
				"rebuild_job_id":    "rebuild-uuid-1",
			})
			return
		}

		// If a GET comes in for status polling, this test should not do that.
		t.Error("unexpected GET request — sync without --wait should not poll")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	stdout, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"sync", "my-workspace")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}
	if stderr != "" {
		t.Errorf("expected empty stderr; got: %s", stderr)
	}

	// Verify the response is valid JSON.
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
	}
}

// ===========================================================================
// TS-NS-5 (merge submit without --wait): Existing behavior is unchanged.
// ===========================================================================

func TestMergeCLI_Submit_NoWait_Unchanged(t *testing.T) {
	server, records := mergeMockServer(t, nil)
	defer server.Close()

	stdout, stderr, err := runMergeCmd(t, server.URL, "test-api-key",
		"submit", "ws1", "--target", "main", "--source", "feature/a")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}
	if stderr != "" {
		t.Errorf("expected empty stderr; got: %s", stderr)
	}

	// Only one request (the POST submit), no polling GETs.
	reqs := getMergeRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request without --wait; got %d", len(reqs))
	}
	if reqs[0].Method != "POST" {
		t.Errorf("method = %q; want POST", reqs[0].Method)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not valid JSON: %v", err)
	}
	if _, ok := result["status"]; !ok {
		t.Error("response JSON missing 'status' field")
	}
}

// ===========================================================================
// Merge submit --wait success
// ===========================================================================

func TestMergeCLI_Submit_Wait_Success(t *testing.T) {
	var pollCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"id":            "merge-uuid-1",
				"workspace_slug": "ws1",
				"target_branch": "main",
				"source_ref":    "feature/a",
				"status":        "queued",
			})
			return
		}

		if r.Method == http.MethodGet {
			pollCount.Add(1)
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"id":            "merge-uuid-1",
				"workspace_slug": "ws1",
				"target_branch": "main",
				"source_ref":    "feature/a",
				"status":        "completed",
			})
			return
		}

		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer server.Close()

	stdout, _, err := runMergeCmd(t, server.URL, "test-api-key",
		"submit", "ws1", "--target", "main", "--source", "feature/a",
		"--wait", "--poll-interval", "100ms")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	if !strings.Contains(stdout, "completed") {
		t.Errorf("stdout should contain 'completed'; got: %s", stdout)
	}

	if pollCount.Load() < 1 {
		t.Error("expected at least 1 poll request")
	}
}

// ===========================================================================
// Workspace create --wait success (AC-4)
// ===========================================================================

func TestWorkspaceCLI_Create_Wait_Success(t *testing.T) {
	var pollCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// POST /api/v1/workspaces — create workspace
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/workspaces" {
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"slug":         "new-ws",
				"git_url":      "https://github.com/org/repo",
				"status":       "active",
				"clone_status": "cloning",
			})
			return
		}

		// GET /api/v1/workspaces/new-ws — poll workspace status
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/workspaces/new-ws") {
			count := pollCount.Add(1)
			w.WriteHeader(http.StatusOK)
			cloneStatus := "cloning"
			if count >= 2 {
				cloneStatus = "ready"
			}
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"slug":         "new-ws",
				"git_url":      "https://github.com/org/repo",
				"status":       "active",
				"clone_status": cloneStatus,
			})
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	stdout, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"create", "--git-url", "https://github.com/org/repo", "--slug", "new-ws",
		"--wait", "--poll-interval", "100ms")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	// Verify stdout includes the final workspace with clone_status: ready.
	if !strings.Contains(stdout, "ready") {
		t.Errorf("stdout should contain clone_status 'ready'; got: %s", stdout)
	}

	if pollCount.Load() < 1 {
		t.Error("expected at least 1 poll request for workspace status")
	}
}

// ===========================================================================
// isTerminalStatus unit tests
// ===========================================================================

func TestIsTerminalStatus(t *testing.T) {
	tests := []struct {
		status   string
		terminal bool
	}{
		{"completed", true},
		{"failed", true},
		{"dead_letter", true},
		{"cancelled", true},
		{"queued", false},
		{"running", false},
		{"", false},
	}

	for _, tt := range tests {
		if got := isTerminalStatus(tt.status); got != tt.terminal {
			t.Errorf("isTerminalStatus(%q) = %v; want %v", tt.status, got, tt.terminal)
		}
	}
}
