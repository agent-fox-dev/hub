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

// --- Test helpers for rebuild CLI tests ---

// rebuildMockServer creates a test server that simulates the rebuild REST API.
// It records all incoming requests and returns appropriate responses for each
// rebuild endpoint.
//
// The mock server handles:
//   - POST /api/v1/workspaces/:slug/rebuild  -- returns 202 with a job record
//   - GET  /api/v1/workspaces/:slug/rebuilds -- returns 200 with a job list
//   - GET  /api/v1/workspaces/:slug/rebuilds/:id -- returns 200 with job details
//
// failPaths maps URL paths to HTTP status codes that should trigger error
// responses. Paths not in failPaths receive successful responses.
func rebuildCLIMockServer(t *testing.T, failPaths map[string]int) (*httptest.Server, *[]rebuildCLIRequestRecord) {
	t.Helper()
	var mu sync.Mutex
	records := &[]rebuildCLIRequestRecord{}
	if failPaths == nil {
		failPaths = map[string]int{}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		mu.Lock()
		*records = append(*records, rebuildCLIRequestRecord{
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
		case http.MethodPost:
			// POST /api/v1/workspaces/:slug/rebuild -- submit rebuild job.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"id":        "job-uuid-1",
				"type":      "rebuild",
				"key":       "my-workspace",
				"group_key": "my-workspace:integration",
				"status":    "queued",
			})

		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)

			// Handle rebuild-preview endpoint.
			if strings.HasSuffix(r.URL.Path, "/rebuild-preview") {
				json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
					"patch_results": []map[string]any{
						{
							"patch_id":       "p1",
							"branch_name":    "feature/a",
							"position":       1,
							"status":         "would_succeed",
							"tree_sha":       "abc123",
							"conflict_files": []string{},
						},
						{
							"patch_id":       "p2",
							"branch_name":    "feature/b",
							"position":       2,
							"status":         "would_conflict",
							"conflict_files": []string{"base.txt"},
						},
					},
				})
				return
			}

			// Distinguish between list (ends with /rebuilds) and get (has /:id).
			if strings.HasSuffix(r.URL.Path, "/rebuilds") {
				// GET /api/v1/workspaces/:slug/rebuilds -- list rebuild jobs.
				json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
					"jobs": []map[string]any{
						{
							"id":         "job-uuid-1",
							"status":     "completed",
							"strategy":   "rebase",
							"created_at": "2025-01-01T00:00:00Z",
						},
						{
							"id":         "job-uuid-2",
							"status":     "queued",
							"strategy":   "rebase",
							"created_at": "2025-01-02T00:00:00Z",
						},
					},
				})
			} else {
				// GET /api/v1/workspaces/:slug/rebuilds/:id -- get single rebuild.
				parts := strings.Split(r.URL.Path, "/")
				rebuildID := parts[len(parts)-1]
				json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
					"id":       rebuildID,
					"status":   "completed",
					"strategy": "rebase",
					"patch_results": []map[string]any{
						{
							"patch_id":    "p1",
							"branch_name": "feature/a",
							"position":    1,
							"status":      "success",
						},
					},
					"created_at": "2025-01-01T00:00:00Z",
				})
			}

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))

	return server, records
}

// rebuildCLIRequestRecord captures an HTTP request received by the rebuild
// mock server.
type rebuildCLIRequestRecord struct {
	Method string
	Path   string
	Body   string
}

// getRebuildCLIRecords returns a snapshot of the recorded requests (thread-safe copy).
func getRebuildCLIRecords(records *[]rebuildCLIRequestRecord) []rebuildCLIRequestRecord {
	return append([]rebuildCLIRequestRecord{}, *records...)
}

// runRebuildCmd executes a rebuild subcommand through the full root command
// tree (with PersistentPreRunE credential resolution) and captures
// stdout/stderr.
func runRebuildCmd(t *testing.T, baseURL, apiKey string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	setupTestEnv(t)
	root := BuildRootCommand()
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	fullArgs := append([]string{"--endpoint-url", baseURL, "--api-key", apiKey, "rebuild"}, args...)
	root.SetArgs(fullArgs)
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

// =========================================================================
// TS-16-22: 'afc rebuild submit <workspace-slug>' sends POST
// /api/v1/workspaces/:slug/rebuild, prints the job id and status 'queued'
// to stdout, and exits with code 0.
// Requirement: 16-REQ-7.1
// =========================================================================

func TestRebuildCLI_Submit_Success(t *testing.T) {
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

	// Verify the correct API call was made.
	reqs := getRebuildCLIRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}

	req := reqs[0]
	if req.Method != "POST" {
		t.Errorf("method = %q; want POST", req.Method)
	}
	if req.Path != "/api/v1/workspaces/my-workspace/rebuild" {
		t.Errorf("path = %q; want /api/v1/workspaces/my-workspace/rebuild", req.Path)
	}

	// Verify stdout contains the job status 'queued'.
	if !strings.Contains(stdout, "queued") {
		t.Errorf("stdout should contain 'queued'; got: %s", stdout)
	}
}

// TS-16-22 (continued): Verify the returned job id and status are printed
// as valid JSON.
func TestRebuildCLI_Submit_PrintsJobRecord(t *testing.T) {
	server, _ := rebuildCLIMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runRebuildCmd(t, server.URL, "test-api-key",
		"submit", "my-workspace")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	// Parse stdout as JSON and verify key fields.
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
	}
	if _, ok := result["id"]; !ok {
		t.Error("response JSON missing 'id' field")
	}
	if _, ok := result["status"]; !ok {
		t.Error("response JSON missing 'status' field")
	}
}

// 16-REQ-7.E1: IF the API returns an error response for the rebuild submit
// command, THEN the CLI SHALL print the error message to stderr and exit
// with a non-zero exit code.
func TestRebuildCLI_Submit_APIError(t *testing.T) {
	failPaths := map[string]int{
		"/api/v1/workspaces/my-workspace/rebuild": http.StatusBadRequest,
	}
	server, _ := rebuildCLIMockServer(t, failPaths)
	defer server.Close()

	_, _, err := runRebuildCmd(t, server.URL, "test-api-key",
		"submit", "my-workspace")

	if err == nil {
		t.Fatal("expected non-zero exit on API error; got nil error")
	}
}

// 16-REQ-7.E2: IF the workspace-slug argument is missing, THEN the CLI
// SHALL print usage information and exit with a non-zero exit code.
func TestRebuildCLI_Submit_MissingSlug(t *testing.T) {
	server, records := rebuildCLIMockServer(t, nil)
	defer server.Close()

	_, _, err := runRebuildCmd(t, server.URL, "test-api-key",
		"submit")

	if err == nil {
		t.Fatal("expected non-zero exit when workspace-slug is missing; got nil error")
	}

	// No API call should have been made.
	reqs := getRebuildCLIRecords(records)
	if len(reqs) != 0 {
		t.Errorf("expected 0 API requests; got %d", len(reqs))
	}
}

// =========================================================================
// TS-16-23: 'afc rebuild list <workspace-slug>' sends GET
// /api/v1/workspaces/:slug/rebuilds and prints the list of rebuild jobs to
// stdout, exiting with code 0.
// Requirement: 16-REQ-7.2
// =========================================================================

func TestRebuildCLI_List_Success(t *testing.T) {
	server, records := rebuildCLIMockServer(t, nil)
	defer server.Close()

	stdout, stderr, err := runRebuildCmd(t, server.URL, "test-api-key",
		"list", "my-workspace")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}
	if stderr != "" {
		t.Errorf("expected empty stderr; got: %s", stderr)
	}

	// Verify the correct API call was made.
	reqs := getRebuildCLIRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}

	req := reqs[0]
	if req.Method != "GET" {
		t.Errorf("method = %q; want GET", req.Method)
	}
	if req.Path != "/api/v1/workspaces/my-workspace/rebuilds" {
		t.Errorf("path = %q; want /api/v1/workspaces/my-workspace/rebuilds", req.Path)
	}

	// Verify stdout contains non-empty job records.
	if len(stdout) == 0 {
		t.Error("expected non-empty stdout with rebuild job list")
	}

	// Verify the response is valid JSON.
	var result any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
	}
}

func TestRebuildCLI_List_APIError(t *testing.T) {
	failPaths := map[string]int{
		"/api/v1/workspaces/my-workspace/rebuilds": http.StatusInternalServerError,
	}
	server, _ := rebuildCLIMockServer(t, failPaths)
	defer server.Close()

	_, _, err := runRebuildCmd(t, server.URL, "test-api-key",
		"list", "my-workspace")

	if err == nil {
		t.Fatal("expected non-zero exit on API error; got nil error")
	}
}

func TestRebuildCLI_List_MissingSlug(t *testing.T) {
	server, _ := rebuildCLIMockServer(t, nil)
	defer server.Close()

	_, _, err := runRebuildCmd(t, server.URL, "test-api-key",
		"list")

	if err == nil {
		t.Fatal("expected non-zero exit when workspace-slug is missing; got nil error")
	}
}

// =========================================================================
// TS-16-24: 'afc rebuild status <workspace-slug> <rebuild-id>' sends GET
// /api/v1/workspaces/:slug/rebuilds/:id and prints job details including
// patch_results to stdout, exiting with code 0; exits non-zero if job not
// found.
// Requirement: 16-REQ-7.3
// =========================================================================

func TestRebuildCLI_Status_Success(t *testing.T) {
	server, records := rebuildCLIMockServer(t, nil)
	defer server.Close()

	stdout, stderr, err := runRebuildCmd(t, server.URL, "test-api-key",
		"status", "my-workspace", "job-uuid-1")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}
	if stderr != "" {
		t.Errorf("expected empty stderr; got: %s", stderr)
	}

	// Verify the correct API call was made.
	reqs := getRebuildCLIRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}

	req := reqs[0]
	if req.Method != "GET" {
		t.Errorf("method = %q; want GET", req.Method)
	}
	if req.Path != "/api/v1/workspaces/my-workspace/rebuilds/job-uuid-1" {
		t.Errorf("path = %q; want /api/v1/workspaces/my-workspace/rebuilds/job-uuid-1", req.Path)
	}

	// Verify stdout contains the job ID and status.
	if !strings.Contains(stdout, "job-uuid-1") {
		t.Errorf("stdout should contain job ID 'job-uuid-1'; got: %s", stdout)
	}
	if !strings.Contains(stdout, "completed") {
		t.Errorf("stdout should contain status 'completed'; got: %s", stdout)
	}

	// Verify the response is valid JSON with expected fields.
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
	}
	if result["id"] != "job-uuid-1" {
		t.Errorf("expected id='job-uuid-1'; got %v", result["id"])
	}
}

// Exits non-zero if job not found (HTTP 404).
func TestRebuildCLI_Status_NotFound(t *testing.T) {
	failPaths := map[string]int{
		"/api/v1/workspaces/my-workspace/rebuilds/nonexistent-id": http.StatusNotFound,
	}
	server, _ := rebuildCLIMockServer(t, failPaths)
	defer server.Close()

	_, _, err := runRebuildCmd(t, server.URL, "test-api-key",
		"status", "my-workspace", "nonexistent-id")

	if err == nil {
		t.Fatal("expected non-zero exit when job not found; got nil error")
	}
}

func TestRebuildCLI_Status_MissingArgs(t *testing.T) {
	server, _ := rebuildCLIMockServer(t, nil)
	defer server.Close()

	// Missing rebuild-id.
	_, _, err := runRebuildCmd(t, server.URL, "test-api-key",
		"status", "my-workspace")

	if err == nil {
		t.Fatal("expected non-zero exit when rebuild-id is missing; got nil error")
	}
}

func TestRebuildCLI_Status_MissingAllArgs(t *testing.T) {
	server, _ := rebuildCLIMockServer(t, nil)
	defer server.Close()

	// Missing both workspace-slug and rebuild-id.
	_, _, err := runRebuildCmd(t, server.URL, "test-api-key",
		"status")

	if err == nil {
		t.Fatal("expected non-zero exit when all args are missing; got nil error")
	}
}

// =========================================================================
// TS-NS-4: 'afc rebuild preview <workspace-slug>' sends GET
// /api/v1/workspaces/:slug/rebuild-preview and prints the JSON result
// to stdout, exiting with code 0.
// Requirement: NS-REQ-4
// =========================================================================

func TestRebuildCLI_Preview_Success(t *testing.T) {
	server, records := rebuildCLIMockServer(t, nil)
	defer server.Close()

	stdout, stderr, err := runRebuildCmd(t, server.URL, "test-api-key",
		"preview", "my-workspace")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}
	if stderr != "" {
		t.Errorf("expected empty stderr; got: %s", stderr)
	}

	// Verify the correct API call was made.
	reqs := getRebuildCLIRecords(records)
	found := false
	for _, req := range reqs {
		if req.Method == "GET" && req.Path == "/api/v1/workspaces/my-workspace/rebuild-preview" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected GET /api/v1/workspaces/my-workspace/rebuild-preview request")
	}

	// Verify stdout is valid JSON containing patch_results.
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
	}
	if _, ok := result["patch_results"]; !ok {
		t.Error("response JSON missing 'patch_results' field")
	}

	// Verify it contains status values.
	if !strings.Contains(stdout, "would_succeed") {
		t.Errorf("stdout should contain 'would_succeed'; got: %s", stdout)
	}
	if !strings.Contains(stdout, "would_conflict") {
		t.Errorf("stdout should contain 'would_conflict'; got: %s", stdout)
	}
}

func TestRebuildCLI_Preview_PrintsValidJSON(t *testing.T) {
	server, _ := rebuildCLIMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runRebuildCmd(t, server.URL, "test-api-key",
		"preview", "my-workspace")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
	}
	patchResults, ok := result["patch_results"].([]any)
	if !ok {
		t.Fatal("patch_results is not an array")
	}
	if len(patchResults) != 2 {
		t.Fatalf("expected 2 patch_results; got %d", len(patchResults))
	}
}

func TestRebuildCLI_Preview_APIError(t *testing.T) {
	failPaths := map[string]int{
		"/api/v1/workspaces/my-workspace/rebuild-preview": http.StatusBadRequest,
	}
	server, _ := rebuildCLIMockServer(t, failPaths)
	defer server.Close()

	_, _, err := runRebuildCmd(t, server.URL, "test-api-key",
		"preview", "my-workspace")

	if err == nil {
		t.Fatal("expected non-zero exit on API error; got nil error")
	}
}

func TestRebuildCLI_Preview_MissingSlug(t *testing.T) {
	server, records := rebuildCLIMockServer(t, nil)
	defer server.Close()

	_, _, err := runRebuildCmd(t, server.URL, "test-api-key",
		"preview")

	if err == nil {
		t.Fatal("expected non-zero exit when workspace-slug is missing; got nil error")
	}

	// No API call should have been made.
	reqs := getRebuildCLIRecords(records)
	if len(reqs) != 0 {
		t.Errorf("expected 0 API requests; got %d", len(reqs))
	}
}

// =========================================================================
// TS-NS-4: 'afc rebuild submit <slug> --strategy merge' sends a POST body
// containing {"strategy": "merge"} to the API.
// Requirement: NS-REQ-4
// =========================================================================

func TestRebuildCLI_Submit_WithStrategyFlag(t *testing.T) {
	server, records := rebuildCLIMockServer(t, nil)
	defer server.Close()

	stdout, stderr, err := runRebuildCmd(t, server.URL, "test-api-key",
		"submit", "my-workspace", "--strategy", "merge")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}
	if stderr != "" {
		t.Errorf("expected empty stderr; got: %s", stderr)
	}

	// Verify stdout contains the job status 'queued'.
	if !strings.Contains(stdout, "queued") {
		t.Errorf("stdout should contain 'queued'; got: %s", stdout)
	}

	// Verify the request body contains {"strategy":"merge"}.
	reqs := getRebuildCLIRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}

	req := reqs[0]
	if req.Method != "POST" {
		t.Errorf("method = %q; want POST", req.Method)
	}

	var body map[string]any
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		t.Fatalf("request body is not valid JSON: %v\nbody: %s", err, req.Body)
	}
	if body["strategy"] != "merge" {
		t.Errorf("expected body strategy='merge'; got %v", body["strategy"])
	}
}

func TestRebuildCLI_Submit_WithStrategyRebase(t *testing.T) {
	server, records := rebuildCLIMockServer(t, nil)
	defer server.Close()

	_, _, err := runRebuildCmd(t, server.URL, "test-api-key",
		"submit", "my-workspace", "--strategy", "rebase")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	reqs := getRebuildCLIRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}

	var body map[string]any
	if err := json.Unmarshal([]byte(reqs[0].Body), &body); err != nil {
		t.Fatalf("request body is not valid JSON: %v\nbody: %s", err, reqs[0].Body)
	}
	if body["strategy"] != "rebase" {
		t.Errorf("expected body strategy='rebase'; got %v", body["strategy"])
	}
}

// TS-NS-5: 'afc rebuild submit <slug>' with no --strategy flag sends a POST
// with no body (or omits the strategy field).
// Requirement: NS-REQ-5
func TestRebuildCLI_Submit_NoStrategyFlag_EmptyBody(t *testing.T) {
	server, records := rebuildCLIMockServer(t, nil)
	defer server.Close()

	_, _, err := runRebuildCmd(t, server.URL, "test-api-key",
		"submit", "my-workspace")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	reqs := getRebuildCLIRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}

	// Body should be empty (nil body) when --strategy is not supplied.
	if reqs[0].Body != "" {
		t.Errorf("expected empty request body when --strategy not supplied; got: %s", reqs[0].Body)
	}
}

// TS-NS-4: Verify RebuildCmd() has a 'preview' subcommand.
func TestRebuildCmd_HasPreviewSubcommand(t *testing.T) {
	cmd := RebuildCmd()
	found := false
	for _, sub := range cmd.Commands() {
		if strings.HasPrefix(sub.Use, "preview") {
			found = true
			break
		}
	}
	if !found {
		t.Error("RebuildCmd() should have a 'preview' subcommand")
	}
}
