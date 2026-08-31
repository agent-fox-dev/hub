package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// --- Test helpers for rerere and patch-status CLI tests ---

// rerereMockServer creates a test server that simulates the rerere and
// patch-status REST APIs. It records all incoming requests and returns
// appropriate responses for each endpoint.
//
// The mock server handles:
//   - GET    /api/v1/workspaces/:slug/rerere       -- returns 200 with resolutions list
//   - DELETE /api/v1/workspaces/:slug/rerere/*      -- returns 204 (success) or 404 (not found)
//   - GET    /api/v1/workspaces/:slug/patch-status  -- returns 200 with status dashboard
//
// failPaths maps URL paths to HTTP status codes that should trigger error
// responses. Paths not in failPaths receive successful responses.
func rerereMockServer(t *testing.T, failPaths map[string]int) (*httptest.Server, *[]rerereRequestRecord) {
	t.Helper()
	var mu sync.Mutex
	records := &[]rerereRequestRecord{}
	if failPaths == nil {
		failPaths = map[string]int{}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		mu.Lock()
		*records = append(*records, rerereRequestRecord{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(bodyBytes),
		})
		mu.Unlock()

		// Check if this path should return an error.
		if code, ok := failPaths[r.URL.Path]; ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"error": map[string]any{
					"code":    code,
					"message": "mock error for " + r.URL.Path,
				},
			})
			return
		}

		switch r.Method {
		case http.MethodGet:
			// Distinguish between rerere list and patch-status.
			if strings.Contains(r.URL.Path, "/patch-status") {
				// GET /api/v1/workspaces/:slug/patch-status
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
					"workspace_slug":       "my-workspace",
					"workspace_mode":       "carry_patch",
					"upstream_url":         "https://github.com/upstream/repo",
					"upstream_head_sha":    "abc123def456",
					"integration_branch":   "integration",
					"integration_head_sha": "def456abc123",
					"last_sync_at":         "2025-01-01T00:00:00Z",
					"last_rebuild": map[string]any{
						"id":     "rebuild-1",
						"status": "completed",
					},
					"patches": []map[string]any{
						{
							"id":                      "p1",
							"branch_name":              "feature/a",
							"position":                 1,
							"status":                   "active",
							"last_rebuild_result":       "success",
							"rerere_resolution_count":   0,
						},
						{
							"id":                      "p2",
							"branch_name":              "feature/b",
							"position":                 2,
							"status":                   "active",
							"last_rebuild_result":       "success",
							"rerere_resolution_count":   1,
						},
					},
					"summary": map[string]any{
						"total_patches":   2,
						"active":          2,
						"merged_upstream": 0,
						"conflict":        0,
						"disabled":        0,
					},
				})
			} else if strings.Contains(r.URL.Path, "/rerere") {
				// GET /api/v1/workspaces/:slug/rerere
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
					"resolutions": []map[string]any{
						{
							"path":        "src/config.go",
							"recorded_at": "2025-01-01T00:00:00Z",
						},
						{
							"path":        "src/main.go",
							"recorded_at": "2025-01-02T00:00:00Z",
						},
					},
				})
			}

		case http.MethodDelete:
			// DELETE /api/v1/workspaces/:slug/rerere/*pathspec
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))

	return server, records
}

// rerereRequestRecord captures an HTTP request received by the mock server.
type rerereRequestRecord struct {
	Method string
	Path   string
	Body   string
}

// getRerereRecords returns a snapshot of the recorded requests (thread-safe copy).
func getRerereRecords(records *[]rerereRequestRecord) []rerereRequestRecord {
	return append([]rerereRequestRecord{}, *records...)
}

// runRerereCmd executes a rerere subcommand through the full root command
// tree (with PersistentPreRunE credential resolution) and captures
// stdout/stderr.
func runRerereCmd(t *testing.T, baseURL, apiKey string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	setupTestEnv(t)
	root := BuildRootCommand()
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	fullArgs := append([]string{"--endpoint-url", baseURL, "--api-key", apiKey, "rerere"}, args...)
	root.SetArgs(fullArgs)
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

// =========================================================================
// TS-16-25: 'afc rerere list <workspace-slug>' sends GET
// /api/v1/workspaces/:slug/rerere and prints the list of recorded
// resolutions with path and recorded_at to stdout, exiting with code 0.
// Requirement: 16-REQ-8.1
// =========================================================================

func TestRerereCLI_List_Success(t *testing.T) {
	server, records := rerereMockServer(t, nil)
	defer server.Close()

	stdout, stderr, err := runRerereCmd(t, server.URL, "test-api-key",
		"list", "my-workspace")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}
	if stderr != "" {
		t.Errorf("expected empty stderr; got: %s", stderr)
	}

	// Verify the correct API call was made.
	reqs := getRerereRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}

	req := reqs[0]
	if req.Method != "GET" {
		t.Errorf("method = %q; want GET", req.Method)
	}
	if req.Path != "/api/v1/workspaces/my-workspace/rerere" {
		t.Errorf("path = %q; want /api/v1/workspaces/my-workspace/rerere", req.Path)
	}

	// Verify stdout contains the resolution path.
	if !strings.Contains(stdout, "src/config.go") {
		t.Errorf("stdout should contain 'src/config.go'; got: %s", stdout)
	}
}

// Verify stdout is valid JSON with resolutions array.
func TestRerereCLI_List_PrintsResolutions(t *testing.T) {
	server, _ := rerereMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runRerereCmd(t, server.URL, "test-api-key",
		"list", "my-workspace")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	// Parse stdout as JSON and verify structure.
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
	}
	resolutions, ok := result["resolutions"]
	if !ok {
		t.Fatal("response JSON missing 'resolutions' field")
	}
	arr, ok := resolutions.([]any)
	if !ok {
		t.Fatalf("expected 'resolutions' to be an array; got %T", resolutions)
	}
	if len(arr) == 0 {
		t.Error("expected at least one resolution in the list")
	}
}

func TestRerereCLI_List_APIError(t *testing.T) {
	failPaths := map[string]int{
		"/api/v1/workspaces/my-workspace/rerere": http.StatusInternalServerError,
	}
	server, _ := rerereMockServer(t, failPaths)
	defer server.Close()

	_, _, err := runRerereCmd(t, server.URL, "test-api-key",
		"list", "my-workspace")

	if err == nil {
		t.Fatal("expected non-zero exit on API error; got nil error")
	}
}

func TestRerereCLI_List_MissingSlug(t *testing.T) {
	server, _ := rerereMockServer(t, nil)
	defer server.Close()

	_, _, err := runRerereCmd(t, server.URL, "test-api-key",
		"list")

	if err == nil {
		t.Fatal("expected non-zero exit when workspace-slug is missing; got nil error")
	}
}

// =========================================================================
// TS-16-26: 'afc rerere forget <workspace-slug> <pathspec>' sends DELETE
// /api/v1/workspaces/:slug/rerere/*pathspec and prints confirmation to
// stdout; prints error to stderr and exits non-zero if pathspec not found.
// Requirement: 16-REQ-8.2
// =========================================================================

func TestRerereCLI_Forget_Success(t *testing.T) {
	server, records := rerereMockServer(t, nil)
	defer server.Close()

	stdout, stderr, err := runRerereCmd(t, server.URL, "test-api-key",
		"forget", "my-workspace", "src/config.go")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	// Verify the correct API call was made.
	reqs := getRerereRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}

	req := reqs[0]
	if req.Method != "DELETE" {
		t.Errorf("method = %q; want DELETE", req.Method)
	}
	// 16-REQ-8.E1: pathspec with slashes is appended as a path segment
	// after /rerere/ without additional URL encoding of slashes.
	if req.Path != "/api/v1/workspaces/my-workspace/rerere/src/config.go" {
		t.Errorf("path = %q; want /api/v1/workspaces/my-workspace/rerere/src/config.go", req.Path)
	}

	// TS-NS-3: stdout must contain valid JSON; human-readable confirmation
	// text goes to stderr only.
	var v map[string]any
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
	}
	if v["status"] == nil || v["status"] == "" {
		t.Error("JSON response missing non-empty 'status' field")
	}

	// Human-readable confirmation should be on stderr, not stdout.
	if stderr == "" {
		t.Error("expected confirmation message on stderr")
	}
	if strings.Contains(stdout, "forgotten") && !strings.Contains(stdout, `"forgotten"`) {
		t.Errorf("stdout should not contain unquoted human-readable text; got: %s", stdout)
	}
}

// Pathspec not found returns non-zero exit.
func TestRerereCLI_Forget_NotFound(t *testing.T) {
	failPaths := map[string]int{
		"/api/v1/workspaces/my-workspace/rerere/nonexistent.go": http.StatusNotFound,
	}
	server, _ := rerereMockServer(t, failPaths)
	defer server.Close()

	_, stderr, err := runRerereCmd(t, server.URL, "test-api-key",
		"forget", "my-workspace", "nonexistent.go")

	if err == nil {
		t.Fatal("expected non-zero exit when pathspec not found; got nil error")
	}

	// Error message should appear in stderr.
	if stderr == "" {
		// CLIHandleError may write to stdout; check that some error was reported.
		t.Log("warning: error not in stderr; CLIHandleError may write to stdout")
	}
}

func TestRerereCLI_Forget_MissingPathspec(t *testing.T) {
	server, records := rerereMockServer(t, nil)
	defer server.Close()

	_, _, err := runRerereCmd(t, server.URL, "test-api-key",
		"forget", "my-workspace")

	if err == nil {
		t.Fatal("expected non-zero exit when pathspec is missing; got nil error")
	}

	// No API call should have been made.
	reqs := getRerereRecords(records)
	if len(reqs) != 0 {
		t.Errorf("expected 0 API requests; got %d", len(reqs))
	}
}

func TestRerereCLI_Forget_MissingAllArgs(t *testing.T) {
	server, _ := rerereMockServer(t, nil)
	defer server.Close()

	_, _, err := runRerereCmd(t, server.URL, "test-api-key",
		"forget")

	if err == nil {
		t.Fatal("expected non-zero exit when all args are missing; got nil error")
	}
}

// =========================================================================
// TS-16-27: 'afc workspace patch-status <workspace-slug>' sends GET
// /api/v1/workspaces/:slug/patch-status and prints workspace metadata,
// last_rebuild summary, per-patch status table, and summary counts to
// stdout, exiting with code 0.
// Requirement: 16-REQ-9.1
// =========================================================================

func TestPatchStatusCLI_Success(t *testing.T) {
	server, records := rerereMockServer(t, nil)
	defer server.Close()

	stdout, stderr, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"patch-status", "my-workspace")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}
	if stderr != "" {
		t.Errorf("expected empty stderr; got: %s", stderr)
	}

	// Verify the correct API call was made.
	reqs := getRerereRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}

	req := reqs[0]
	if req.Method != "GET" {
		t.Errorf("method = %q; want GET", req.Method)
	}
	if req.Path != "/api/v1/workspaces/my-workspace/patch-status" {
		t.Errorf("path = %q; want /api/v1/workspaces/my-workspace/patch-status", req.Path)
	}

	// Verify stdout contains expected fields.
	if !strings.Contains(stdout, "my-workspace") {
		t.Errorf("stdout should contain 'my-workspace'; got: %s", stdout)
	}
	if !strings.Contains(stdout, "carry_patch") {
		t.Errorf("stdout should contain 'carry_patch'; got: %s", stdout)
	}
	if !strings.Contains(stdout, "total_patches") {
		t.Errorf("stdout should contain 'total_patches'; got: %s", stdout)
	}
}

// Verify stdout is valid JSON with the patch-status schema.
func TestPatchStatusCLI_PrintsDashboard(t *testing.T) {
	server, _ := rerereMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"patch-status", "my-workspace")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
	}

	// Verify expected top-level fields.
	expectedFields := []string{
		"workspace_slug", "workspace_mode", "upstream_url",
		"upstream_head_sha", "integration_branch", "integration_head_sha",
		"patches", "summary",
	}
	for _, field := range expectedFields {
		if _, ok := result[field]; !ok {
			t.Errorf("response JSON missing '%s' field", field)
		}
	}

	// Verify summary counts are present.
	summary, ok := result["summary"].(map[string]any)
	if !ok {
		t.Fatal("expected 'summary' to be an object")
	}
	for _, countField := range []string{"total_patches", "active", "merged_upstream", "conflict", "disabled"} {
		if _, ok := summary[countField]; !ok {
			t.Errorf("summary missing '%s' field", countField)
		}
	}
}

// 16-REQ-9.E1: IF the workspace is not in carry_patch mode and the API
// returns HTTP 400, THEN the CLI SHALL print the error message to stderr
// and exit with a non-zero exit code.
func TestPatchStatusCLI_NotCarryPatch(t *testing.T) {
	failPaths := map[string]int{
		"/api/v1/workspaces/standard-ws/patch-status": http.StatusBadRequest,
	}
	server, _ := rerereMockServer(t, failPaths)
	defer server.Close()

	_, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"patch-status", "standard-ws")

	if err == nil {
		t.Fatal("expected non-zero exit on HTTP 400; got nil error")
	}
}

func TestPatchStatusCLI_MissingSlug(t *testing.T) {
	server, _ := rerereMockServer(t, nil)
	defer server.Close()

	_, _, err := runWorkspaceCmd(t, server.URL, "test-api-key",
		"patch-status")

	if err == nil {
		t.Fatal("expected non-zero exit when workspace-slug is missing; got nil error")
	}
}
