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
// TS-NS-2 (rebuild submit --wait with failed job): the final record is
// printed and the command exits non-zero so scripts can detect failure.
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

	stdout, stderr, err := runRebuildCmd(t, server.URL, "test-api-key",
		"submit", "my-workspace", "--wait", "--poll-interval", "100ms")

	if err == nil {
		t.Fatal("expected a non-zero exit for a failed job")
	}

	if !strings.Contains(stdout, "something went wrong") {
		t.Errorf("stdout should contain the final job record; got: %s", stdout)
	}
	if !strings.Contains(stdout, "job ended with status failed") {
		t.Errorf("stdout should contain the error envelope; got: %s", stdout)
	}
	if !strings.Contains(stderr, "failed") {
		t.Errorf("stderr should mention the failed status; got: %s", stderr)
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
