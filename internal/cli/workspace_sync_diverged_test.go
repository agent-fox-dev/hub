package cli

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/txsvc/apikit"
)

// assertCLIErrorCode checks that the error wraps a CLIError with the given code.
func assertCLIErrorCode(t *testing.T, err error, wantCode int) {
	t.Helper()
	var ce *apikit.CLIError
	if !errors.As(err, &ce) {
		t.Errorf("error is not a CLIError: %v", err)
		return
	}
	if ce.ErrorCode() != wantCode {
		t.Errorf("CLIError code = %d; want %d", ce.ErrorCode(), wantCode)
	}
}

// decodeJSONValues decodes every JSON value in s. It fails the test when s is
// not a sequence of JSON documents.
func decodeJSONValues(t *testing.T, s string) []any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	var values []any
	for {
		var v any
		err := dec.Decode(&v)
		if errors.Is(err, io.EOF) {
			return values
		}
		if err != nil {
			t.Fatalf("stdout is not a sequence of JSON documents: %v\nstdout: %s", err, s)
		}
		values = append(values, v)
	}
}

// =========================================================================
// TS-20-38: afc workspace sync prints the new fields and exits 0 even with
// diverged branches when the flag is absent.
//
// Verifies: 20-REQ-6.4
// =========================================================================

func TestCLI_WorkspaceSync_PrintsNewFields_ExitsZero_TS2038(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/sync") {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"patches_merged":    []string{},
				"rebuild_triggered": false,
				"origin_fetched":    true,
				"patches_synced": []map[string]any{
					{
						"branch_name": "feat",
						"action":      "none",
						"state":       "diverged",
						"local_sha":   "aaa",
						"origin_sha":  "bbb",
					},
				},
				"patches_diverged": []string{"feat"},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	stdout, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"sync", "my-workspace")

	// Exit code must be 0 (no --fail-on-diverged flag).
	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	// Verify the printed JSON contains origin_fetched, patches_synced, patches_diverged
	// and all existing fields.
	var resp map[string]any
	if jsonErr := json.Unmarshal([]byte(stdout), &resp); jsonErr != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", jsonErr, stdout)
	}

	// origin_fetched
	of, ok := resp["origin_fetched"].(bool)
	if !ok || !of {
		t.Errorf("origin_fetched = %v; want true", resp["origin_fetched"])
	}

	// patches_synced
	ps, ok := resp["patches_synced"].([]any)
	if !ok || len(ps) != 1 {
		t.Errorf("patches_synced = %v; want array of length 1", resp["patches_synced"])
	}

	// patches_diverged
	pd, ok := resp["patches_diverged"].([]any)
	if !ok || len(pd) != 1 {
		t.Errorf("patches_diverged = %v; want [\"feat\"]", resp["patches_diverged"])
	}
	if len(pd) == 1 {
		if pd[0] != "feat" {
			t.Errorf("patches_diverged[0] = %v; want \"feat\"", pd[0])
		}
	}

	// Existing fields
	if _, ok := resp["patches_merged"]; !ok {
		t.Error("response missing existing field patches_merged")
	}
	if _, ok := resp["rebuild_triggered"]; !ok {
		t.Error("response missing existing field rebuild_triggered")
	}

	// stderr should be empty (no diverged message without the flag)
	if stderr != "" {
		t.Errorf("expected empty stderr; got: %s", stderr)
	}
}

// =========================================================================
// TS-20-39: --fail-on-diverged prints the JSON and a message naming the
// branches and exits 3 only when diverged is non-empty.
//
// Verifies: 20-REQ-6.5
// =========================================================================

func TestCLI_WorkspaceSync_FailOnDiverged_NonEmpty_TS2039(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/sync") {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"patches_merged":    []string{},
				"rebuild_triggered": false,
				"origin_fetched":    true,
				"patches_synced":    []map[string]any{},
				"patches_diverged":  []string{"a", "b"},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	stdout, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"sync", "my-workspace", "--fail-on-diverged")

	// Exit code must be 3: the CLIError code and the code the afc process
	// exits with (ExitCode), which apikit would otherwise collapse to 2.
	if err == nil {
		t.Fatal("expected non-zero exit; got nil error")
	}
	assertCLIErrorCode(t, err, 3)
	if got := ExitCode(err); got != 3 {
		t.Errorf("ExitCode = %d; want 3", got)
	}
	if !IsDivergedError(err) {
		t.Error("IsDivergedError = false; want true")
	}

	// stdout holds exactly one JSON document, the sync response; the diverged
	// failure must not add an {"error": ...} envelope after it.
	docs := decodeJSONValues(t, stdout)
	if len(docs) != 1 {
		t.Fatalf("stdout holds %d JSON documents; want exactly 1\nstdout: %s", len(docs), stdout)
	}
	resp, _ := docs[0].(map[string]any)
	if _, ok := resp["origin_fetched"]; !ok {
		t.Errorf("stdout should contain the response JSON; got: %s", stdout)
	}
	if _, hasErr := resp["error"]; hasErr {
		t.Errorf("stdout carries an error envelope; got: %s", stdout)
	}

	// stderr should contain a message naming the diverged branches.
	if !strings.Contains(stderr, "diverged patch branches: a, b") {
		t.Errorf("stderr should name diverged branches a and b; got: %s", stderr)
	}
}

func TestCLI_WorkspaceSync_FailOnDiverged_Empty_TS2039(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/sync") {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"patches_merged":    []string{},
				"rebuild_triggered": false,
				"origin_fetched":    true,
				"patches_synced":    []map[string]any{},
				"patches_diverged":  []string{},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"sync", "my-workspace", "--fail-on-diverged")

	// Exit code must be 0 when patches_diverged is empty.
	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}
}

// =========================================================================
// TS-20-40: With --wait the diverged check runs after the wait and a wait
// failure takes precedence.
//
// Verifies: 20-REQ-6.6
// =========================================================================

func TestCLI_WorkspaceSync_WaitThenDiverged_WaitOK_TS2040(t *testing.T) {
	// Stub hub: sync returns patches_diverged ["a"] and a rebuild job id.
	// Job status returns "completed" (wait succeeds).
	var pollCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/sync") {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"patches_merged":    []string{},
				"rebuild_triggered": true,
				"rebuild_job_id":    "rebuild-uuid-1",
				"origin_fetched":    true,
				"patches_synced":    []map[string]any{},
				"patches_diverged":  []string{"a"},
			})
			return
		}
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/rebuilds/") {
			pollCount.Add(1)
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"id":     "rebuild-uuid-1",
				"status": "completed",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"sync", "my-workspace", "--wait", "--fail-on-diverged", "--poll-interval", "100ms")

	// Wait succeeded, but patches_diverged is non-empty → exit 3.
	if err == nil {
		t.Fatal("expected non-zero exit; got nil error")
	}
	assertCLIErrorCode(t, err, 3)
	if got := ExitCode(err); got != 3 {
		t.Errorf("ExitCode = %d; want 3", got)
	}

	// The wait polling must have happened.
	if pollCount.Load() < 1 {
		t.Error("expected at least 1 poll request for rebuild status")
	}

	// stderr should mention the diverged branches.
	if !strings.Contains(stderr, "a") {
		t.Errorf("stderr should name diverged branch a; got: %s", stderr)
	}
}

func TestCLI_WorkspaceSync_WaitThenDiverged_WaitFail_TS2040(t *testing.T) {
	// Stub hub: sync returns patches_diverged ["a"] and a rebuild job id.
	// Job status returns "failed" (wait fails → exit 1 takes precedence).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/sync") {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"patches_merged":    []string{},
				"rebuild_triggered": true,
				"rebuild_job_id":    "rebuild-uuid-1",
				"origin_fetched":    true,
				"patches_synced":    []map[string]any{},
				"patches_diverged":  []string{"a"},
			})
			return
		}
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/rebuilds/") {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"id":     "rebuild-uuid-1",
				"status": "failed",
				"error":  "rebuild failed",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"sync", "my-workspace", "--wait", "--fail-on-diverged", "--poll-interval", "100ms")

	// Wait failure → exit 1 takes precedence over exit 3.
	if err == nil {
		t.Fatal("expected non-zero exit; got nil error")
	}
	exitCode := ExitCode(err)
	if exitCode != 1 {
		t.Errorf("ExitCode = %d; want 1 (wait failure takes precedence)", exitCode)
	}
	if IsDivergedError(err) {
		t.Error("IsDivergedError = true for a wait failure; want false")
	}
}

func TestCLI_WorkspaceSync_WaitThenDiverged_RequestError_TS2040(t *testing.T) {
	// Stub hub: sync POST returns an error (simulating a request error → exit 2 or 1).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/sync") {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"error": map[string]any{
					"message": "bad request",
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"sync", "my-workspace", "--wait", "--fail-on-diverged", "--poll-interval", "100ms")

	// Request error → non-zero exit, exit codes 1 and 2 keep their meaning.
	if err == nil {
		t.Fatal("expected non-zero exit; got nil error")
	}
	// The error from the API call should not be a diverged error.
	var ce *apikit.CLIError
	if errors.As(err, &ce) && ce.ErrorCode() == 3 {
		t.Error("error code should not be 3 for a request error")
	}
	if IsDivergedError(err) {
		t.Error("IsDivergedError = true for a request error; want false")
	}
	if got := ExitCode(err); got != 1 {
		t.Errorf("ExitCode = %d; want 1 for an API request error", got)
	}
}
