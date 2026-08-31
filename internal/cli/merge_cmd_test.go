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

// --- Test helpers for merge CLI tests ---

// mergeMockServer creates a test server that simulates the merge REST API.
// It records all incoming requests and returns appropriate responses for
// each merge endpoint.
//
// The mock server handles:
//   - POST /api/v1/workspaces/:slug/merges — returns 202 with a merge job record
//   - GET  /api/v1/workspaces/:slug/merges — returns 200 with a list of merge jobs
//   - GET  /api/v1/workspaces/:slug/merges/:id — returns 200 with a single merge job
//   - DELETE /api/v1/workspaces/:slug/merges/:id — returns 200 with confirmation
//
// failPaths maps URL paths to HTTP status codes that should trigger error
// responses. Paths not in failPaths receive successful responses.
func mergeMockServer(t *testing.T, failPaths map[string]int) (*httptest.Server, *[]mergeRequestRecord) {
	t.Helper()
	var mu sync.Mutex
	records := &[]mergeRequestRecord{}
	if failPaths == nil {
		failPaths = map[string]int{}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		mu.Lock()
		*records = append(*records, mergeRequestRecord{
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
			w.Header().Set("Content-Type", "application/json")
			if strings.HasSuffix(r.URL.Path, "/requeue") {
				// POST /api/v1/workspaces/:slug/merges/:id/requeue — requeue merge job.
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
					"id":            "job-uuid-1",
					"workspace_slug": "ws1",
					"target_branch": "main",
					"source_ref":    "feature/a",
					"status":        "queued",
					"submitted_by":  "test-user",
					"retry_count":   0,
					"created_at":    "2025-01-01T00:00:00Z",
					"updated_at":    "2025-01-01T00:00:00Z",
				})
			} else {
				// POST /api/v1/workspaces/:slug/merges — submit merge job.
				w.WriteHeader(http.StatusAccepted)
				var reqBody map[string]any
				if err := json.Unmarshal(bodyBytes, &reqBody); err == nil {
					resp := map[string]any{
						"id":            "job-uuid-1",
						"workspace_slug": "ws1",
						"target_branch": reqBody["target_branch"],
						"source_ref":    reqBody["source_ref"],
						"status":        "queued",
						"submitted_by":  "test-user",
						"retry_count":   0,
						"created_at":    "2025-01-01T00:00:00Z",
						"updated_at":    "2025-01-01T00:00:00Z",
					}
					json.NewEncoder(w).Encode(resp) //nolint:errcheck
				} else {
					json.NewEncoder(w).Encode(map[string]any{"id": "job-uuid-1", "status": "queued"}) //nolint:errcheck
				}
			}

		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)

			// Distinguish between list (ends with /merges) and get (ends with /merges/:id).
			if strings.HasSuffix(r.URL.Path, "/merges") {
				// GET /api/v1/workspaces/:slug/merges — list merge jobs.
				json.NewEncoder(w).Encode([]map[string]any{ //nolint:errcheck
					{
						"id":            "job-uuid-1",
						"workspace_slug": "ws1",
						"target_branch": "main",
						"source_ref":    "feature/a",
						"status":        "queued",
						"submitted_by":  "test-user",
						"retry_count":   0,
						"created_at":    "2025-01-01T00:00:00Z",
						"updated_at":    "2025-01-01T00:00:00Z",
					},
					{
						"id":            "job-uuid-2",
						"workspace_slug": "ws1",
						"target_branch": "main",
						"source_ref":    "feature/b",
						"status":        "completed",
						"submitted_by":  "test-user",
						"retry_count":   0,
						"created_at":    "2025-01-01T00:00:00Z",
						"updated_at":    "2025-01-02T00:00:00Z",
					},
				})
			} else {
				// GET /api/v1/workspaces/:slug/merges/:id — get single merge job.
				// Extract the merge ID from the path for inclusion in the response.
				parts := strings.Split(r.URL.Path, "/")
				mergeID := parts[len(parts)-1]
				json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
					"id":            mergeID,
					"workspace_slug": "ws1",
					"target_branch": "main",
					"source_ref":    "feature/a",
					"status":        "queued",
					"submitted_by":  "test-user",
					"retry_count":   0,
					"created_at":    "2025-01-01T00:00:00Z",
					"updated_at":    "2025-01-01T00:00:00Z",
				})
			}

		case http.MethodDelete:
			// DELETE /api/v1/workspaces/:slug/merges/:id — cancel merge job.
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))

	return server, records
}

// mergeRequestRecord captures an HTTP request received by the merge mock server.
type mergeRequestRecord struct {
	Method string
	Path   string
	Body   string
}

// getMergeRecords returns a snapshot of the recorded requests (thread-safe copy).
func getMergeRecords(records *[]mergeRequestRecord) []mergeRequestRecord {
	return append([]mergeRequestRecord{}, *records...)
}

// runMergeCmd executes a merge subcommand through the full root command
// tree (with PersistentPreRunE credential resolution) and captures
// stdout/stderr.
func runMergeCmd(t *testing.T, baseURL, apiKey string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	setupTestEnv(t)
	root := BuildRootCommand()
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	fullArgs := append([]string{"--endpoint-url", baseURL, "--api-key", apiKey, "merge"}, args...)
	root.SetArgs(fullArgs)
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

// =========================================================================
// TS-12-46: 'afc merge submit <workspace-slug> --target <branch> --source
// <branch>' calls POST /api/v1/workspaces/:slug/merges and prints the merge
// job record to stdout, exiting 0.
// Requirement: 12-REQ-13.1
// =========================================================================

func TestMergeCLI_Submit_Success(t *testing.T) {
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

	// Verify the correct API call was made.
	reqs := getMergeRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}

	req := reqs[0]
	if req.Method != "POST" {
		t.Errorf("method = %q; want POST", req.Method)
	}
	if req.Path != "/api/v1/workspaces/ws1/merges" {
		t.Errorf("path = %q; want /api/v1/workspaces/ws1/merges", req.Path)
	}

	// Verify the request body contains target_branch and source_ref.
	var body map[string]any
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}
	if body["target_branch"] != "main" {
		t.Errorf("target_branch = %v; want main", body["target_branch"])
	}
	if body["source_ref"] != "feature/a" {
		t.Errorf("source_ref = %v; want feature/a", body["source_ref"])
	}

	// Verify stdout contains the merge job record with expected fields.
	if !strings.Contains(stdout, "job-uuid-1") {
		t.Errorf("stdout should contain job ID 'job-uuid-1'; got: %s", stdout)
	}
	if !strings.Contains(stdout, "queued") {
		t.Errorf("stdout should contain status 'queued'; got: %s", stdout)
	}
	if !strings.Contains(stdout, "id") {
		t.Errorf("stdout should contain field 'id'; got: %s", stdout)
	}
	if !strings.Contains(stdout, "status") {
		t.Errorf("stdout should contain field 'status'; got: %s", stdout)
	}
}

// =========================================================================
// TS-12-46 (continued): Verify the returned merge job ID is printed.
// Requirement: 12-REQ-13.1
// =========================================================================

func TestMergeCLI_Submit_PrintsJobID(t *testing.T) {
	server, _ := mergeMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runMergeCmd(t, server.URL, "test-api-key",
		"submit", "ws1", "--target", "main", "--source", "feature/a")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	// Parse stdout as JSON and verify the 'id' field is present.
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
	}
	if _, ok := result["id"]; !ok {
		t.Errorf("response JSON missing 'id' field; got: %v", result)
	}
	if id, ok := result["id"].(string); !ok || id == "" {
		t.Errorf("response 'id' should be a non-empty string; got: %v", result["id"])
	}
}

// =========================================================================
// 12-REQ-13.E1: IF --target or --source flag is missing, THEN the afc CLI
// merge submit SHALL print usage error and exit with non-zero code without
// making any API call.
// =========================================================================

func TestMergeCLI_Submit_MissingTarget(t *testing.T) {
	server, records := mergeMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runMergeCmd(t, server.URL, "test-api-key",
		"submit", "ws1", "--source", "feature/a")

	if err == nil {
		t.Fatal("expected non-zero exit; got nil error")
	}

	// No API call should have been made.
	reqs := getMergeRecords(records)
	if len(reqs) != 0 {
		t.Errorf("expected 0 API requests when --target missing; got %d", len(reqs))
	}

	// Error message should mention --target.
	if !strings.Contains(stdout, "target") {
		t.Errorf("error output should mention --target; stdout: %s", stdout)
	}
}

func TestMergeCLI_Submit_MissingSource(t *testing.T) {
	server, records := mergeMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runMergeCmd(t, server.URL, "test-api-key",
		"submit", "ws1", "--target", "main")

	if err == nil {
		t.Fatal("expected non-zero exit; got nil error")
	}

	// No API call should have been made.
	reqs := getMergeRecords(records)
	if len(reqs) != 0 {
		t.Errorf("expected 0 API requests when --source missing; got %d", len(reqs))
	}

	// Error message should mention --source.
	if !strings.Contains(stdout, "source") {
		t.Errorf("error output should mention --source; stdout: %s", stdout)
	}
}

func TestMergeCLI_Submit_MissingBothFlags(t *testing.T) {
	server, records := mergeMockServer(t, nil)
	defer server.Close()

	_, _, err := runMergeCmd(t, server.URL, "test-api-key",
		"submit", "ws1")

	if err == nil {
		t.Fatal("expected non-zero exit; got nil error")
	}

	// No API call should have been made.
	reqs := getMergeRecords(records)
	if len(reqs) != 0 {
		t.Errorf("expected 0 API requests when flags missing; got %d", len(reqs))
	}
}

// =========================================================================
// 12-REQ-13.E2: IF the API returns an error response, THEN the afc CLI
// SHALL print the error message to stderr and exit with non-zero code;
// library code must not call os.Exit directly.
// =========================================================================

func TestMergeCLI_Submit_APIError(t *testing.T) {
	failPaths := map[string]int{
		"/api/v1/workspaces/ws1/merges": http.StatusBadRequest,
	}
	server, _ := mergeMockServer(t, failPaths)
	defer server.Close()

	_, _, err := runMergeCmd(t, server.URL, "test-api-key",
		"submit", "ws1", "--target", "main", "--source", "feature/a")

	if err == nil {
		t.Fatal("expected non-zero exit on API error; got nil error")
	}
}

// =========================================================================
// TS-12-47: 'afc merge list <workspace-slug>' calls GET
// /api/v1/workspaces/:slug/merges and prints the list of merge jobs to
// stdout, exiting 0.
// Requirement: 12-REQ-13.2
// =========================================================================

func TestMergeCLI_List_Success(t *testing.T) {
	server, records := mergeMockServer(t, nil)
	defer server.Close()

	stdout, stderr, err := runMergeCmd(t, server.URL, "test-api-key",
		"list", "ws1")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}
	if stderr != "" {
		t.Errorf("expected empty stderr; got: %s", stderr)
	}

	// Verify the correct API call was made.
	reqs := getMergeRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}

	req := reqs[0]
	if req.Method != "GET" {
		t.Errorf("method = %q; want GET", req.Method)
	}
	if req.Path != "/api/v1/workspaces/ws1/merges" {
		t.Errorf("path = %q; want /api/v1/workspaces/ws1/merges", req.Path)
	}

	// Verify stdout contains a non-empty response.
	if len(stdout) == 0 {
		t.Error("expected non-empty stdout with merge job list")
	}

	// Verify the response is valid JSON.
	var result any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
	}

	// The result should be a list (array).
	jobs, ok := result.([]any)
	if !ok {
		t.Fatalf("expected JSON array; got: %T", result)
	}
	if len(jobs) == 0 {
		t.Error("expected at least one merge job in the list")
	}
}

func TestMergeCLI_List_APIError(t *testing.T) {
	failPaths := map[string]int{
		"/api/v1/workspaces/ws1/merges": http.StatusInternalServerError,
	}
	server, _ := mergeMockServer(t, failPaths)
	defer server.Close()

	_, _, err := runMergeCmd(t, server.URL, "test-api-key",
		"list", "ws1")

	if err == nil {
		t.Fatal("expected non-zero exit on API error; got nil error")
	}
}

// =========================================================================
// TS-12-48: 'afc merge status <workspace-slug> <merge-id>' calls GET
// /api/v1/workspaces/:slug/merges/:id and prints the merge job status to
// stdout, exiting 0.
// Requirement: 12-REQ-13.3
// =========================================================================

func TestMergeCLI_Status_Success(t *testing.T) {
	server, records := mergeMockServer(t, nil)
	defer server.Close()

	stdout, stderr, err := runMergeCmd(t, server.URL, "test-api-key",
		"status", "ws1", "job-uuid-1")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}
	if stderr != "" {
		t.Errorf("expected empty stderr; got: %s", stderr)
	}

	// Verify the correct API call was made.
	reqs := getMergeRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}

	req := reqs[0]
	if req.Method != "GET" {
		t.Errorf("method = %q; want GET", req.Method)
	}
	if req.Path != "/api/v1/workspaces/ws1/merges/job-uuid-1" {
		t.Errorf("path = %q; want /api/v1/workspaces/ws1/merges/job-uuid-1", req.Path)
	}

	// Verify stdout contains the merge job ID.
	if !strings.Contains(stdout, "job-uuid-1") {
		t.Errorf("stdout should contain merge job ID 'job-uuid-1'; got: %s", stdout)
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

func TestMergeCLI_Status_APIError(t *testing.T) {
	failPaths := map[string]int{
		"/api/v1/workspaces/ws1/merges/job-uuid-1": http.StatusNotFound,
	}
	server, _ := mergeMockServer(t, failPaths)
	defer server.Close()

	_, _, err := runMergeCmd(t, server.URL, "test-api-key",
		"status", "ws1", "job-uuid-1")

	if err == nil {
		t.Fatal("expected non-zero exit on API error; got nil error")
	}
}

// =========================================================================
// TS-12-49: 'afc merge cancel <workspace-slug> <merge-id>' calls DELETE
// /api/v1/workspaces/:slug/merges/:id and prints confirmation to stdout,
// exiting 0.
// Requirement: 12-REQ-13.4
// =========================================================================

func TestMergeCLI_Cancel_Success(t *testing.T) {
	server, records := mergeMockServer(t, nil)
	defer server.Close()

	stdout, stderr, err := runMergeCmd(t, server.URL, "test-api-key",
		"cancel", "ws1", "job-uuid-1")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	// Verify the correct API call was made.
	reqs := getMergeRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}

	req := reqs[0]
	if req.Method != "DELETE" {
		t.Errorf("method = %q; want DELETE", req.Method)
	}
	if req.Path != "/api/v1/workspaces/ws1/merges/job-uuid-1" {
		t.Errorf("path = %q; want /api/v1/workspaces/ws1/merges/job-uuid-1", req.Path)
	}

	// TS-NS-4: stdout must contain valid JSON with at least a status field.
	var v map[string]any
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
	}
	if v["status"] == nil || v["status"] == "" {
		t.Error("JSON response missing non-empty 'status' field")
	}

	// Verify confirmation is printed (to stderr, following the workspace delete pattern).
	if !strings.Contains(stderr, "job-uuid-1") {
		t.Errorf("confirmation should mention merge ID 'job-uuid-1'; stderr: %s", stderr)
	}
}

func TestMergeCLI_Cancel_APIError(t *testing.T) {
	failPaths := map[string]int{
		"/api/v1/workspaces/ws1/merges/job-uuid-1": http.StatusConflict,
	}
	server, _ := mergeMockServer(t, failPaths)
	defer server.Close()

	_, _, err := runMergeCmd(t, server.URL, "test-api-key",
		"cancel", "ws1", "job-uuid-1")

	if err == nil {
		t.Fatal("expected non-zero exit on API error; got nil error")
	}
}

// =========================================================================
// Missing workspace-slug argument tests — cobra.ExactArgs should reject.
// =========================================================================

func TestMergeCLI_Submit_MissingSlug(t *testing.T) {
	server, records := mergeMockServer(t, nil)
	defer server.Close()

	_, _, err := runMergeCmd(t, server.URL, "test-api-key",
		"submit", "--target", "main", "--source", "feature/a")

	if err == nil {
		t.Fatal("expected non-zero exit when workspace-slug is missing; got nil error")
	}

	// No API call should have been made.
	reqs := getMergeRecords(records)
	if len(reqs) != 0 {
		t.Errorf("expected 0 API requests; got %d", len(reqs))
	}
}

func TestMergeCLI_List_MissingSlug(t *testing.T) {
	server, _ := mergeMockServer(t, nil)
	defer server.Close()

	_, _, err := runMergeCmd(t, server.URL, "test-api-key",
		"list")

	if err == nil {
		t.Fatal("expected non-zero exit when workspace-slug is missing; got nil error")
	}
}

func TestMergeCLI_Status_MissingArgs(t *testing.T) {
	server, _ := mergeMockServer(t, nil)
	defer server.Close()

	_, _, err := runMergeCmd(t, server.URL, "test-api-key",
		"status", "ws1")

	if err == nil {
		t.Fatal("expected non-zero exit when merge-id is missing; got nil error")
	}
}

func TestMergeCLI_Cancel_MissingArgs(t *testing.T) {
	server, _ := mergeMockServer(t, nil)
	defer server.Close()

	_, _, err := runMergeCmd(t, server.URL, "test-api-key",
		"cancel", "ws1")

	if err == nil {
		t.Fatal("expected non-zero exit when merge-id is missing; got nil error")
	}
}

// =========================================================================
// Verify MergeCmd() has a 'requeue' subcommand.
// =========================================================================

func TestMergeCmd_HasRequeueSubcommand(t *testing.T) {
	cmd := MergeCmd()
	found := false
	for _, sub := range cmd.Commands() {
		if strings.HasPrefix(sub.Use, "requeue") {
			found = true
			break
		}
	}
	if !found {
		t.Error("MergeCmd() should have a 'requeue' subcommand")
	}
}

// =========================================================================
// 'afc merge requeue <workspace-slug> <merge-id>' sends POST
// /api/v1/workspaces/:slug/merges/:id/requeue and prints the result.
// =========================================================================

func TestMergeCLI_Requeue_Success(t *testing.T) {
	server, records := mergeMockServer(t, nil)
	defer server.Close()

	stdout, stderr, err := runMergeCmd(t, server.URL, "test-api-key",
		"requeue", "ws1", "job-uuid-1")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}
	if stderr != "" {
		t.Errorf("expected empty stderr; got: %s", stderr)
	}

	// Verify the correct API call was made.
	reqs := getMergeRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}

	req := reqs[0]
	if req.Method != "POST" {
		t.Errorf("method = %q; want POST", req.Method)
	}
	if req.Path != "/api/v1/workspaces/ws1/merges/job-uuid-1/requeue" {
		t.Errorf("path = %q; want /api/v1/workspaces/ws1/merges/job-uuid-1/requeue", req.Path)
	}

	// Verify stdout contains the requeued job status.
	if !strings.Contains(stdout, "queued") {
		t.Errorf("stdout should contain 'queued'; got: %s", stdout)
	}

	// Verify the response is valid JSON.
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
	}
}

func TestMergeCLI_Requeue_APIError(t *testing.T) {
	failPaths := map[string]int{
		"/api/v1/workspaces/ws1/merges/job-uuid-1/requeue": http.StatusConflict,
	}
	server, _ := mergeMockServer(t, failPaths)
	defer server.Close()

	_, _, err := runMergeCmd(t, server.URL, "test-api-key",
		"requeue", "ws1", "job-uuid-1")

	if err == nil {
		t.Fatal("expected non-zero exit on API error; got nil error")
	}
}

func TestMergeCLI_Requeue_MissingArgs(t *testing.T) {
	server, _ := mergeMockServer(t, nil)
	defer server.Close()

	_, _, err := runMergeCmd(t, server.URL, "test-api-key",
		"requeue", "ws1")

	if err == nil {
		t.Fatal("expected non-zero exit when merge-id is missing; got nil error")
	}
}
