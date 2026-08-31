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

// --- Test helpers for rebase CLI tests ---

// rebaseRequestRecord captures an HTTP request received by the rebase mock server.
type rebaseRequestRecord struct {
	Method string
	Path   string
	Body   string
}

// rebaseMockServer creates a test server that simulates the rebase REST API.
// It records all incoming requests and returns appropriate responses.
//
// The mock server handles:
//   - POST /api/v1/workspaces/:slug/rebase — returns 200 with per-branch results
//
// failPaths maps URL paths to HTTP status codes that should trigger error
// responses. Paths not in failPaths receive successful responses.
func rebaseMockServer(t *testing.T, failPaths map[string]int) (*httptest.Server, *[]rebaseRequestRecord) {
	t.Helper()
	var mu sync.Mutex
	records := &[]rebaseRequestRecord{}
	if failPaths == nil {
		failPaths = map[string]int{}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		mu.Lock()
		*records = append(*records, rebaseRequestRecord{
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
			w.WriteHeader(http.StatusOK)

			var reqBody map[string]any
			if err := json.Unmarshal(bodyBytes, &reqBody); err == nil {
				branches, _ := reqBody["branches"].([]any)
				var results []map[string]any
				for _, b := range branches {
					branchName, _ := b.(string)
					results = append(results, map[string]any{
						"branch":   branchName,
						"status":   "ok",
						"new_sha":  "abc123",
						"conflict": false,
					})
				}
				json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
					"results": results,
				})
			} else {
				json.NewEncoder(w).Encode(map[string]any{"results": []any{}}) //nolint:errcheck
			}

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))

	return server, records
}

// getRebaseRecords returns a snapshot of the recorded requests (thread-safe copy).
func getRebaseRecords(records *[]rebaseRequestRecord) []rebaseRequestRecord {
	return append([]rebaseRequestRecord{}, *records...)
}

// runRebaseCmd executes a rebase subcommand through the full root command
// tree (with PersistentPreRunE credential resolution) and captures
// stdout/stderr.
func runRebaseCmd(t *testing.T, baseURL, apiKey string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	setupTestEnv(t)
	root := BuildRootCommand()
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	fullArgs := append([]string{"--endpoint-url", baseURL, "--api-key", apiKey, "rebase"}, args...)
	root.SetArgs(fullArgs)
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

// =========================================================================
// TS-NS-1: afc rebase submit calls the correct API endpoint with correct
// JSON body.
// Requirement: NS-REQ-1
// =========================================================================

func TestRebaseCLI_Submit_Success(t *testing.T) {
	server, records := rebaseMockServer(t, nil)
	defer server.Close()

	stdout, stderr, err := runRebaseCmd(t, server.URL, "test-api-key",
		"submit", "my-ws", "--target-ref", "main", "--branches", "patch/a,patch/b")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}
	if stderr != "" {
		t.Errorf("expected empty stderr; got: %s", stderr)
	}

	// Verify the correct API call was made.
	reqs := getRebaseRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}

	req := reqs[0]
	if req.Method != "POST" {
		t.Errorf("method = %q; want POST", req.Method)
	}
	if req.Path != "/api/v1/workspaces/my-ws/rebase" {
		t.Errorf("path = %q; want /api/v1/workspaces/my-ws/rebase", req.Path)
	}

	// Verify the request body contains target_ref and branches.
	var body map[string]any
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}
	if body["target_ref"] != "main" {
		t.Errorf("target_ref = %v; want main", body["target_ref"])
	}
	branchesRaw, ok := body["branches"].([]any)
	if !ok {
		t.Fatalf("branches is not an array; got %T: %v", body["branches"], body["branches"])
	}
	if len(branchesRaw) != 2 {
		t.Fatalf("expected 2 branches; got %d", len(branchesRaw))
	}
	if branchesRaw[0] != "patch/a" {
		t.Errorf("branches[0] = %v; want patch/a", branchesRaw[0])
	}
	if branchesRaw[1] != "patch/b" {
		t.Errorf("branches[1] = %v; want patch/b", branchesRaw[1])
	}

	// Verify stdout contains JSON response from the server.
	if !strings.Contains(stdout, "results") {
		t.Errorf("stdout should contain 'results'; got: %s", stdout)
	}

	// Verify stdout is valid JSON.
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\nstdout: %s", err, stdout)
	}
}

// =========================================================================
// TS-NS-2: Missing required flags exit with code 2 and print an error to
// stderr.
// Requirement: NS-REQ-2
// =========================================================================

func TestRebaseCLI_Submit_MissingTargetRef(t *testing.T) {
	server, records := rebaseMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runRebaseCmd(t, server.URL, "test-api-key",
		"submit", "my-ws", "--branches", "patch/a,patch/b")

	if err == nil {
		t.Fatal("expected non-zero exit; got nil error")
	}

	// No API call should have been made.
	reqs := getRebaseRecords(records)
	if len(reqs) != 0 {
		t.Errorf("expected 0 API requests when --target-ref missing; got %d", len(reqs))
	}

	// Error output should mention --target-ref.
	if !strings.Contains(stdout, "target-ref") {
		t.Errorf("error output should mention --target-ref; stdout: %s", stdout)
	}
}

func TestRebaseCLI_Submit_MissingBranches(t *testing.T) {
	server, records := rebaseMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runRebaseCmd(t, server.URL, "test-api-key",
		"submit", "my-ws", "--target-ref", "main")

	if err == nil {
		t.Fatal("expected non-zero exit; got nil error")
	}

	// No API call should have been made.
	reqs := getRebaseRecords(records)
	if len(reqs) != 0 {
		t.Errorf("expected 0 API requests when --branches missing; got %d", len(reqs))
	}

	// Error output should mention --branches.
	if !strings.Contains(stdout, "branches") {
		t.Errorf("error output should mention --branches; stdout: %s", stdout)
	}
}

func TestRebaseCLI_Submit_MissingBothFlags(t *testing.T) {
	server, records := rebaseMockServer(t, nil)
	defer server.Close()

	_, _, err := runRebaseCmd(t, server.URL, "test-api-key",
		"submit", "my-ws")

	if err == nil {
		t.Fatal("expected non-zero exit; got nil error")
	}

	// No API call should have been made.
	reqs := getRebaseRecords(records)
	if len(reqs) != 0 {
		t.Errorf("expected 0 API requests when flags missing; got %d", len(reqs))
	}
}

// =========================================================================
// TS-NS-3: API error responses are propagated as exit code 1.
// Requirement: NS-REQ-3
// =========================================================================

func TestRebaseCLI_Submit_APIError(t *testing.T) {
	failPaths := map[string]int{
		"/api/v1/workspaces/my-ws/rebase": http.StatusUnprocessableEntity,
	}
	server, _ := rebaseMockServer(t, failPaths)
	defer server.Close()

	_, _, err := runRebaseCmd(t, server.URL, "test-api-key",
		"submit", "my-ws", "--target-ref", "main", "--branches", "patch/a,patch/b")

	if err == nil {
		t.Fatal("expected non-zero exit on API error; got nil error")
	}
}

func TestRebaseCLI_Submit_APIError500(t *testing.T) {
	failPaths := map[string]int{
		"/api/v1/workspaces/my-ws/rebase": http.StatusInternalServerError,
	}
	server, _ := rebaseMockServer(t, failPaths)
	defer server.Close()

	_, _, err := runRebaseCmd(t, server.URL, "test-api-key",
		"submit", "my-ws", "--target-ref", "main", "--branches", "patch/a")

	if err == nil {
		t.Fatal("expected non-zero exit on API error; got nil error")
	}
}

// =========================================================================
// TS-NS-4: RebaseCmd is registered in BuildRootCommand.
// Requirement: NS-REQ-4
// =========================================================================

func TestRebaseCmd_RegisteredInRoot(t *testing.T) {
	root := BuildRootCommand()
	found := false
	for _, sub := range root.Commands() {
		if sub.Use == "rebase" {
			found = true
			break
		}
	}
	if !found {
		t.Error("BuildRootCommand() should have a 'rebase' top-level command")
	}
}

func TestRebaseCmd_HasSubmitSubcommand(t *testing.T) {
	cmd := RebaseCmd()
	found := false
	for _, sub := range cmd.Commands() {
		if strings.HasPrefix(sub.Use, "submit") {
			found = true
			break
		}
	}
	if !found {
		t.Error("RebaseCmd() should have a 'submit' subcommand")
	}
}

// =========================================================================
// Missing workspace-slug argument — cobra.ExactArgs should reject.
// =========================================================================

func TestRebaseCLI_Submit_MissingSlug(t *testing.T) {
	server, records := rebaseMockServer(t, nil)
	defer server.Close()

	_, _, err := runRebaseCmd(t, server.URL, "test-api-key",
		"submit", "--target-ref", "main", "--branches", "patch/a")

	if err == nil {
		t.Fatal("expected non-zero exit when workspace-slug is missing; got nil error")
	}

	// No API call should have been made.
	reqs := getRebaseRecords(records)
	if len(reqs) != 0 {
		t.Errorf("expected 0 API requests; got %d", len(reqs))
	}
}
