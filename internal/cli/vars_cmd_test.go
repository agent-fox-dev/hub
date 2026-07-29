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

// --- Test helpers for vars CLI tests ---

// varsMockServer creates a test server that records all incoming requests
// and returns appropriate responses for vars API endpoints.
//
// Unlike secrets, vars responses include a "value" field in entries.
// GET requests to paths ending in "/vars/resolved" return resolved variable
// sets with an "origin" field.
//
// failPaths maps URL paths to HTTP status codes that should trigger error
// responses. Paths not in failPaths receive successful responses.
func varsMockServer(t *testing.T, failPaths map[string]int) (*httptest.Server, *[]requestRecord) {
	t.Helper()
	var mu sync.Mutex
	records := &[]requestRecord{}
	if failPaths == nil {
		failPaths = map[string]int{}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		mu.Lock()
		*records = append(*records, requestRecord{
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
			w.WriteHeader(http.StatusCreated)
			// Echo back entries with values and timestamps.
			var reqBody map[string]any
			if err := json.Unmarshal(bodyBytes, &reqBody); err == nil {
				if entries, ok := reqBody["entries"].([]any); ok {
					var resp []map[string]any
					for _, e := range entries {
						entry, _ := e.(map[string]any)
						resp = append(resp, map[string]any{
							"key":        entry["key"],
							"value":      entry["value"],
							"created_at": "2024-01-01T00:00:00Z",
							"updated_at": "2024-01-01T00:00:00Z",
						})
					}
					json.NewEncoder(w).Encode(resp) //nolint:errcheck
					return
				}
			}
			json.NewEncoder(w).Encode([]map[string]any{}) //nolint:errcheck

		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// Check if this is a resolved vars request.
			if strings.HasSuffix(r.URL.Path, "/vars/resolved") {
				json.NewEncoder(w).Encode([]map[string]any{ //nolint:errcheck
					{
						"key":        "DB_URL",
						"value":      "postgres://localhost/db",
						"origin":     "workspace",
						"created_at": "2024-01-01T00:00:00Z",
						"updated_at": "2024-01-01T00:00:00Z",
					},
					{
						"key":        "LOG_LEVEL",
						"value":      "info",
						"origin":     "user",
						"created_at": "2024-01-01T00:00:00Z",
						"updated_at": "2024-01-01T00:00:00Z",
					},
				})
				return
			}
			// Regular vars list response.
			json.NewEncoder(w).Encode([]map[string]any{ //nolint:errcheck
				{
					"key":        "MY_VAR",
					"value":      "myval",
					"created_at": "2024-01-01T00:00:00Z",
					"updated_at": "2024-01-01T00:00:00Z",
				},
			})

		case http.MethodPatch:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			parts := strings.Split(r.URL.Path, "/")
			key := parts[len(parts)-1]
			var reqBody map[string]any
			val := "updated"
			if err := json.Unmarshal(bodyBytes, &reqBody); err == nil {
				if v, ok := reqBody["value"].(string); ok {
					val = v
				}
			}
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"key":        key,
				"value":      val,
				"created_at": "2024-01-01T00:00:00Z",
				"updated_at": "2024-01-02T00:00:00Z",
			})

		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))

	return server, records
}

// runVarsCmd executes a vars subcommand through the full root command
// tree (with PersistentPreRunE credential resolution) and captures
// stdout/stderr.
func runVarsCmd(t *testing.T, baseURL, apiKey string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	setupTestEnv(t)
	root := BuildRootCommand()
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	fullArgs := append([]string{"--endpoint-url", baseURL, "--api-key", apiKey, "vars"}, args...)
	root.SetArgs(fullArgs)
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

// =========================================================================
// TS-08-14: afc vars create KEY=val with no flags defaults to user scope
// and POSTs to /api/v1/user/vars with body {"entries":[{"key":"KEY","value":"val"}]}.
// Requirement: 08-REQ-6.1
// =========================================================================

func TestCLI_VarsCreate_DefaultUserScope(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runVarsCmd(t, server.URL, "test-api-key",
		"create", "KEY=val")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	reqs := getRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}

	req := reqs[0]
	if req.Method != "POST" {
		t.Errorf("method = %q; want POST", req.Method)
	}
	if req.Path != "/api/v1/user/vars" {
		t.Errorf("path = %q; want /api/v1/user/vars", req.Path)
	}

	// Verify the request body has the correct entries with values.
	entries := parseBodyEntries(t, req.Body)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry; got %d", len(entries))
	}
	if entries[0]["key"] != "KEY" {
		t.Errorf("entry key = %q; want KEY", entries[0]["key"])
	}
	if entries[0]["value"] != "val" {
		t.Errorf("entry value = %q; want val", entries[0]["value"])
	}

	// Response should be printed to stdout including the value field.
	if !strings.Contains(stdout, "KEY") {
		t.Errorf("stdout should contain KEY; got: %s", stdout)
	}
	if !strings.Contains(stdout, "val") {
		t.Errorf("stdout should contain val; got: %s", stdout)
	}
}

// =========================================================================
// afc vars create KEY=val --org myorg POSTs to /api/v1/orgs/myorg/vars.
// Requirement: 08-REQ-6.1 (org scope variant)
// =========================================================================

func TestCLI_VarsCreate_OrgScope(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runVarsCmd(t, server.URL, "test-api-key",
		"create", "KEY=val", "--org", "myorg")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	reqs := getRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}

	if reqs[0].Method != "POST" {
		t.Errorf("method = %q; want POST", reqs[0].Method)
	}
	if reqs[0].Path != "/api/v1/orgs/myorg/vars" {
		t.Errorf("path = %q; want /api/v1/orgs/myorg/vars", reqs[0].Path)
	}

	if !strings.Contains(stdout, "KEY") {
		t.Errorf("stdout should contain KEY; got: %s", stdout)
	}
}

// =========================================================================
// afc vars create KEY=val --workspace myws POSTs to
// /api/v1/workspaces/myws/vars.
// Requirement: 08-REQ-6.1 (workspace scope variant)
// =========================================================================

func TestCLI_VarsCreate_WorkspaceScope(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runVarsCmd(t, server.URL, "test-api-key",
		"create", "KEY=val", "--workspace", "myws")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	reqs := getRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}

	if reqs[0].Path != "/api/v1/workspaces/myws/vars" {
		t.Errorf("path = %q; want /api/v1/workspaces/myws/vars", reqs[0].Path)
	}

	if !strings.Contains(stdout, "KEY") {
		t.Errorf("stdout should contain KEY; got: %s", stdout)
	}
}

// =========================================================================
// TS-08-15: afc vars create KEY=val --user --org myorg makes two sequential
// POST calls in user->org order and prints both responses.
// Requirement: 08-REQ-6.2
// =========================================================================

func TestCLI_VarsCreate_MultiScopeUserOrg(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runVarsCmd(t, server.URL, "test-api-key",
		"create", "KEY=val", "--user", "--org", "myorg")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	reqs := getRecords(records)
	if len(reqs) != 2 {
		t.Fatalf("expected exactly 2 requests; got %d", len(reqs))
	}

	// Verify order: user -> org.
	if reqs[0].Path != "/api/v1/user/vars" {
		t.Errorf("request[0].path = %q; want /api/v1/user/vars", reqs[0].Path)
	}
	if reqs[1].Path != "/api/v1/orgs/myorg/vars" {
		t.Errorf("request[1].path = %q; want /api/v1/orgs/myorg/vars", reqs[1].Path)
	}

	// Both responses should be in stdout.
	if !strings.Contains(stdout, "KEY") {
		t.Errorf("stdout should contain KEY from responses; got: %s", stdout)
	}
}

// =========================================================================
// afc vars create --user --org myorg --workspace myws makes three sequential
// POST calls in user -> org -> workspace order.
// Requirement: 08-REQ-12.E2, 08-PROP-1
// =========================================================================

func TestCLI_VarsCreate_MultiScopeAllThree(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runVarsCmd(t, server.URL, "test-api-key",
		"create", "KEY=val", "--user", "--org", "myorg", "--workspace", "myws")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	reqs := getRecords(records)
	if len(reqs) != 3 {
		t.Fatalf("expected exactly 3 requests; got %d", len(reqs))
	}

	// Verify order: user -> org -> workspace.
	expectedPaths := []string{
		"/api/v1/user/vars",
		"/api/v1/orgs/myorg/vars",
		"/api/v1/workspaces/myws/vars",
	}
	for i, expected := range expectedPaths {
		if reqs[i].Path != expected {
			t.Errorf("request[%d].path = %q; want %q", i, reqs[i].Path, expected)
		}
		if reqs[i].Method != "POST" {
			t.Errorf("request[%d].method = %q; want POST", i, reqs[i].Method)
		}
	}

	if !strings.Contains(stdout, "KEY") {
		t.Errorf("stdout should contain KEY from all responses; got: %s", stdout)
	}
}

// =========================================================================
// Multi-scope create with partial failure: org scope fails, user scope
// succeeds. Command should continue to all scopes and exit with error.
// Requirement: 08-REQ-6.E3, 08-PROP-2
// =========================================================================

func TestCLI_VarsCreate_MultiScopePartialFailure(t *testing.T) {
	server, records := varsMockServer(t, map[string]int{
		"/api/v1/orgs/myorg/vars": http.StatusInternalServerError,
	})
	defer server.Close()

	stdout, _, err := runVarsCmd(t, server.URL, "test-api-key",
		"create", "KEY=val", "--user", "--org", "myorg")

	// Expect error because org scope failed.
	if err == nil {
		t.Fatal("expected error for partial multi-scope failure; got nil")
	}

	reqs := getRecords(records)
	if len(reqs) != 2 {
		t.Fatalf("expected 2 requests (user + org); got %d", len(reqs))
	}

	// First request should be to user scope.
	if reqs[0].Path != "/api/v1/user/vars" {
		t.Errorf("first request path = %q; want /api/v1/user/vars", reqs[0].Path)
	}
	// Second request should be to org scope.
	if reqs[1].Path != "/api/v1/orgs/myorg/vars" {
		t.Errorf("second request path = %q; want /api/v1/orgs/myorg/vars", reqs[1].Path)
	}

	// User scope response should still appear in stdout.
	if !strings.Contains(stdout, "KEY") {
		t.Error("stdout should contain user-scope response even when org scope fails")
	}

	// Error envelope for org failure should appear in stdout
	// (CLIHandleError writes to stdout, not stderr -- see errata).
	if !hasErrorEnvelope(stdout) {
		t.Errorf("stdout should contain error envelope for org scope failure; got: %s", stdout)
	}
}

// =========================================================================
// TS-08-16: afc vars create KEY1=val1,KEY2=val2 --workspace myws sends all
// entries in a single POST to /api/v1/workspaces/myws/vars.
// Requirement: 08-REQ-6.3
// =========================================================================

func TestCLI_VarsCreate_CommaSeparatedWorkspace(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runVarsCmd(t, server.URL, "test-api-key",
		"create", "KEY1=val1,KEY2=val2", "--workspace", "myws")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	reqs := getRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request; got %d", len(reqs))
	}

	if reqs[0].Path != "/api/v1/workspaces/myws/vars" {
		t.Errorf("path = %q; want /api/v1/workspaces/myws/vars", reqs[0].Path)
	}

	entries := parseBodyEntries(t, reqs[0].Body)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries for comma-separated input; got %d", len(entries))
	}
	if entries[0]["key"] != "KEY1" {
		t.Errorf("entries[0].key = %q; want KEY1", entries[0]["key"])
	}
	if entries[0]["value"] != "val1" {
		t.Errorf("entries[0].value = %q; want val1", entries[0]["value"])
	}
	if entries[1]["key"] != "KEY2" {
		t.Errorf("entries[1].key = %q; want KEY2", entries[1]["key"])
	}
	if entries[1]["value"] != "val2" {
		t.Errorf("entries[1].value = %q; want val2", entries[1]["value"])
	}

	if !strings.Contains(stdout, "KEY1") || !strings.Contains(stdout, "KEY2") {
		t.Errorf("stdout should contain both keys; got: %s", stdout)
	}
}

// =========================================================================
// TS-08-26: The key=value parser correctly splits a comma-separated argument
// into multiple key=value pairs for vars create commands.
// Requirement: 08-REQ-11.2
// =========================================================================

func TestCLI_VarsCreate_CommaSeparatedThreePairs(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runVarsCmd(t, server.URL, "test-api-key",
		"create", "A=1,B=2,C=3")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	reqs := getRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request; got %d", len(reqs))
	}

	entries := parseBodyEntries(t, reqs[0].Body)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries for A=1,B=2,C=3; got %d", len(entries))
	}

	expectedKeys := []string{"A", "B", "C"}
	expectedVals := []string{"1", "2", "3"}
	for i, ek := range expectedKeys {
		if entries[i]["key"] != ek {
			t.Errorf("entries[%d].key = %q; want %q", i, entries[i]["key"], ek)
		}
		if entries[i]["value"] != expectedVals[i] {
			t.Errorf("entries[%d].value = %q; want %q", i, entries[i]["value"], expectedVals[i])
		}
	}

	if !strings.Contains(stdout, "A") {
		t.Errorf("stdout should contain entry keys; got: %s", stdout)
	}
}

// =========================================================================
// Single-scope vars create API error: DoRequest returns an error.
// Requirement: 08-REQ-6.E3 (single scope variant)
// =========================================================================

func TestCLI_VarsCreate_SingleScopeAPIError(t *testing.T) {
	server, records := varsMockServer(t, map[string]int{
		"/api/v1/user/vars": http.StatusForbidden,
	})
	defer server.Close()

	stdout, _, err := runVarsCmd(t, server.URL, "test-api-key",
		"create", "KEY=val")

	if err == nil {
		t.Fatal("expected error for API failure; got nil")
	}

	reqs := getRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request; got %d", len(reqs))
	}

	// Error envelope should be in stdout (CLIHandleError writes to stdout).
	if !hasErrorEnvelope(stdout) {
		t.Errorf("stdout should contain error envelope; got: %s", stdout)
	}
}

// =========================================================================
// Argument validation: missing '=' character in vars create.
// Requirement: 08-REQ-6.E1, 08-REQ-11.E1
// =========================================================================

func TestCLI_VarsCreate_MissingEquals(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	_, _, err := runVarsCmd(t, server.URL, "test-api-key",
		"create", "NOEQUALS")

	if err == nil {
		t.Fatal("expected error for argument with no '='; got nil")
	}

	// No API call should have been made.
	reqs := getRecords(records)
	if len(reqs) != 0 {
		t.Errorf("expected 0 requests when argument is invalid; got %d", len(reqs))
	}
}

// =========================================================================
// Argument validation: empty key after comma-split in vars create.
// Requirement: 08-REQ-6.E2, 08-REQ-11.E2
// =========================================================================

func TestCLI_VarsCreate_EmptyKeyInCommaSplit(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	_, _, err := runVarsCmd(t, server.URL, "test-api-key",
		"create", "KEY=val,,KEY2=val2")

	if err == nil {
		t.Fatal("expected error for empty key between commas; got nil")
	}

	// No API call should have been made.
	reqs := getRecords(records)
	if len(reqs) != 0 {
		t.Errorf("expected 0 requests when argument is invalid; got %d", len(reqs))
	}
}

// =========================================================================
// Argument validation: no positional argument provided to vars create.
// Requirement: 08-REQ-6.E4
// =========================================================================

func TestCLI_VarsCreate_NoArgument(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	_, _, err := runVarsCmd(t, server.URL, "test-api-key", "create")

	if err == nil {
		t.Fatal("expected error when no argument is provided; got nil")
	}

	// No API call should have been made.
	reqs := getRecords(records)
	if len(reqs) != 0 {
		t.Errorf("expected 0 requests when no argument is given; got %d", len(reqs))
	}
}

// =========================================================================
// TS-08-17: afc vars list with no flags defaults to user scope and GETs
// /api/v1/user/vars, printing the full response including values.
// Requirement: 08-REQ-7.1
// =========================================================================

func TestCLI_VarsList_DefaultUserScope(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runVarsCmd(t, server.URL, "test-api-key", "list")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	reqs := getRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}
	if reqs[0].Method != "GET" {
		t.Errorf("method = %q; want GET", reqs[0].Method)
	}
	if reqs[0].Path != "/api/v1/user/vars" {
		t.Errorf("path = %q; want /api/v1/user/vars", reqs[0].Path)
	}

	// Stdout should contain key AND value (vars include values unlike secrets).
	if !strings.Contains(stdout, "MY_VAR") {
		t.Errorf("stdout should contain MY_VAR; got: %s", stdout)
	}
	if !strings.Contains(stdout, "myval") {
		t.Errorf("stdout should contain myval; got: %s", stdout)
	}
}

// =========================================================================
// TS-08-18: afc vars list --org myorg GETs /api/v1/orgs/myorg/vars.
// Requirement: 08-REQ-7.2
// =========================================================================

func TestCLI_VarsList_OrgScope(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runVarsCmd(t, server.URL, "test-api-key",
		"list", "--org", "myorg")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	reqs := getRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}
	if reqs[0].Path != "/api/v1/orgs/myorg/vars" {
		t.Errorf("path = %q; want /api/v1/orgs/myorg/vars", reqs[0].Path)
	}

	if !strings.Contains(stdout, "MY_VAR") {
		t.Errorf("stdout should contain response content; got: %s", stdout)
	}
}

// =========================================================================
// TS-08-19: afc vars list --workspace myws GETs
// /api/v1/workspaces/myws/vars.
// Requirement: 08-REQ-7.3
// =========================================================================

func TestCLI_VarsList_WorkspaceScope(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runVarsCmd(t, server.URL, "test-api-key",
		"list", "--workspace", "myws")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	reqs := getRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}
	if reqs[0].Path != "/api/v1/workspaces/myws/vars" {
		t.Errorf("path = %q; want /api/v1/workspaces/myws/vars", reqs[0].Path)
	}

	if !strings.Contains(stdout, "MY_VAR") {
		t.Errorf("stdout should contain response content; got: %s", stdout)
	}
}

// =========================================================================
// Multiple ownership flags on vars list command should return an error.
// Requirement: 08-REQ-7.E1, 08-REQ-12.E1
// =========================================================================

func TestCLI_VarsList_MultipleSelectors(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	_, _, err := runVarsCmd(t, server.URL, "test-api-key",
		"list", "--user", "--org", "myorg")

	if err == nil {
		t.Fatal("expected error when multiple ownership selectors provided; got nil")
	}

	// No API call should have been made.
	reqs := getRecords(records)
	if len(reqs) != 0 {
		t.Errorf("expected 0 requests when multiple selectors used; got %d", len(reqs))
	}
}

// =========================================================================
// Multiple ownership flags on vars list: --org and --workspace.
// Requirement: 08-REQ-7.E1
// =========================================================================

func TestCLI_VarsList_MultipleSelectorOrgWorkspace(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	_, _, err := runVarsCmd(t, server.URL, "test-api-key",
		"list", "--org", "myorg", "--workspace", "myws")

	if err == nil {
		t.Fatal("expected error when multiple ownership selectors provided; got nil")
	}

	reqs := getRecords(records)
	if len(reqs) != 0 {
		t.Errorf("expected 0 requests when multiple selectors used; got %d", len(reqs))
	}
}

// =========================================================================
// List API error.
// Requirement: 08-REQ-7.E2
// =========================================================================

func TestCLI_VarsList_APIError(t *testing.T) {
	server, _ := varsMockServer(t, map[string]int{
		"/api/v1/user/vars": http.StatusForbidden,
	})
	defer server.Close()

	stdout, _, err := runVarsCmd(t, server.URL, "test-api-key", "list")

	if err == nil {
		t.Fatal("expected error for API failure; got nil")
	}
	if !hasErrorEnvelope(stdout) {
		t.Errorf("stdout should contain error envelope; got: %s", stdout)
	}
}

// =========================================================================
// TS-08-20: afc vars update KEY=newval with no flags defaults to user scope
// and PATCHes /api/v1/user/vars/KEY with body {"value":"newval"}.
// Requirement: 08-REQ-8.1
// =========================================================================

func TestCLI_VarsUpdate_DefaultUserScope(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runVarsCmd(t, server.URL, "test-api-key",
		"update", "KEY=newval")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	reqs := getRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}

	req := reqs[0]
	if req.Method != "PATCH" {
		t.Errorf("method = %q; want PATCH", req.Method)
	}
	if req.Path != "/api/v1/user/vars/KEY" {
		t.Errorf("path = %q; want /api/v1/user/vars/KEY", req.Path)
	}

	val := parseBodyValue(t, req.Body)
	if val != "newval" {
		t.Errorf("body value = %q; want newval", val)
	}

	// Response should include the value field.
	if !strings.Contains(stdout, "newval") {
		t.Errorf("stdout should contain newval; got: %s", stdout)
	}
}

// =========================================================================
// TS-08-21: afc vars update KEY=newval --workspace myws PATCHes
// /api/v1/workspaces/myws/vars/KEY with body {"value":"newval"}.
// Requirement: 08-REQ-8.2
// =========================================================================

func TestCLI_VarsUpdate_WorkspaceScope(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runVarsCmd(t, server.URL, "test-api-key",
		"update", "KEY=newval", "--workspace", "myws")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	reqs := getRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}

	if reqs[0].Method != "PATCH" {
		t.Errorf("method = %q; want PATCH", reqs[0].Method)
	}
	if reqs[0].Path != "/api/v1/workspaces/myws/vars/KEY" {
		t.Errorf("path = %q; want /api/v1/workspaces/myws/vars/KEY", reqs[0].Path)
	}

	val := parseBodyValue(t, reqs[0].Body)
	if val != "newval" {
		t.Errorf("body value = %q; want newval", val)
	}

	if !strings.Contains(stdout, "KEY") {
		t.Errorf("stdout should contain KEY; got: %s", stdout)
	}
}

// =========================================================================
// afc vars update KEY=newval --org myorg PATCHes
// /api/v1/orgs/myorg/vars/KEY.
// Requirement: 08-REQ-8.1 (org scope variant)
// =========================================================================

func TestCLI_VarsUpdate_OrgScope(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runVarsCmd(t, server.URL, "test-api-key",
		"update", "KEY=newval", "--org", "myorg")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	reqs := getRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}

	if reqs[0].Path != "/api/v1/orgs/myorg/vars/KEY" {
		t.Errorf("path = %q; want /api/v1/orgs/myorg/vars/KEY", reqs[0].Path)
	}

	if !strings.Contains(stdout, "KEY") {
		t.Errorf("stdout should contain KEY; got: %s", stdout)
	}
}

// =========================================================================
// Update with missing '=' in argument.
// Requirement: 08-REQ-8.E1
// =========================================================================

func TestCLI_VarsUpdate_MissingEquals(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	_, _, err := runVarsCmd(t, server.URL, "test-api-key",
		"update", "NOEQUALS")

	if err == nil {
		t.Fatal("expected error for argument with no '='; got nil")
	}

	reqs := getRecords(records)
	if len(reqs) != 0 {
		t.Errorf("expected 0 requests when argument is invalid; got %d", len(reqs))
	}
}

// =========================================================================
// Update with multiple ownership selectors.
// Requirement: 08-REQ-8.E2
// =========================================================================

func TestCLI_VarsUpdate_MultipleSelectors(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	_, _, err := runVarsCmd(t, server.URL, "test-api-key",
		"update", "KEY=val", "--user", "--org", "myorg")

	if err == nil {
		t.Fatal("expected error when multiple ownership selectors provided; got nil")
	}

	reqs := getRecords(records)
	if len(reqs) != 0 {
		t.Errorf("expected 0 requests when multiple selectors used; got %d", len(reqs))
	}
}

// =========================================================================
// Update with no positional argument.
// Requirement: 08-REQ-8.E1 (no arg variant)
// =========================================================================

func TestCLI_VarsUpdate_NoArgument(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	_, _, err := runVarsCmd(t, server.URL, "test-api-key", "update")

	if err == nil {
		t.Fatal("expected error when no argument is provided; got nil")
	}

	reqs := getRecords(records)
	if len(reqs) != 0 {
		t.Errorf("expected 0 requests when no argument given; got %d", len(reqs))
	}
}

// =========================================================================
// Update with 404 from API (key does not exist).
// Requirement: 08-REQ-8.E3
// =========================================================================

func TestCLI_VarsUpdate_NotFound(t *testing.T) {
	server, _ := varsMockServer(t, map[string]int{
		"/api/v1/user/vars/NOKEY": http.StatusNotFound,
	})
	defer server.Close()

	stdout, _, err := runVarsCmd(t, server.URL, "test-api-key",
		"update", "NOKEY=newval")

	if err == nil {
		t.Fatal("expected error for 404 response; got nil")
	}
	if !hasErrorEnvelope(stdout) {
		t.Errorf("stdout should contain error envelope; got: %s", stdout)
	}
}

// =========================================================================
// TS-08-22: afc vars delete MYKEY with no flags defaults to user scope,
// DELETEs /api/v1/user/vars/MYKEY, stdout is empty, stderr has
// confirmation message.
// Requirement: 08-REQ-9.1
// =========================================================================

func TestCLI_VarsDelete_DefaultUserScope(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	stdout, stderr, err := runVarsCmd(t, server.URL, "test-api-key",
		"delete", "MYKEY")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	reqs := getRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}

	req := reqs[0]
	if req.Method != "DELETE" {
		t.Errorf("method = %q; want DELETE", req.Method)
	}
	if req.Path != "/api/v1/user/vars/MYKEY" {
		t.Errorf("path = %q; want /api/v1/user/vars/MYKEY", req.Path)
	}

	// stdout should be empty (CLIPrintResult is NOT called for DELETE).
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("stdout should be empty for DELETE; got: %s", stdout)
	}

	// Confirmation message should be on stderr.
	if !strings.Contains(stderr, "Variable 'MYKEY' has been deleted.") {
		t.Errorf("stderr should contain confirmation message; got: %s", stderr)
	}
}

// =========================================================================
// TS-08-23: afc vars delete MYKEY --org myorg DELETEs
// /api/v1/orgs/myorg/vars/MYKEY and prints confirmation to stderr.
// Requirement: 08-REQ-9.2
// =========================================================================

func TestCLI_VarsDelete_OrgScope(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	stdout, stderr, err := runVarsCmd(t, server.URL, "test-api-key",
		"delete", "MYKEY", "--org", "myorg")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	reqs := getRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}

	if reqs[0].Method != "DELETE" {
		t.Errorf("method = %q; want DELETE", reqs[0].Method)
	}
	if reqs[0].Path != "/api/v1/orgs/myorg/vars/MYKEY" {
		t.Errorf("path = %q; want /api/v1/orgs/myorg/vars/MYKEY", reqs[0].Path)
	}

	if strings.TrimSpace(stdout) != "" {
		t.Errorf("stdout should be empty for DELETE; got: %s", stdout)
	}
	if !strings.Contains(stderr, "Variable 'MYKEY' has been deleted.") {
		t.Errorf("stderr should contain confirmation message; got: %s", stderr)
	}
}

// =========================================================================
// Delete with no positional argument.
// Requirement: 08-REQ-9.E1
// =========================================================================

func TestCLI_VarsDelete_NoArgument(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	_, _, err := runVarsCmd(t, server.URL, "test-api-key", "delete")

	if err == nil {
		t.Fatal("expected error when no argument is provided; got nil")
	}

	reqs := getRecords(records)
	if len(reqs) != 0 {
		t.Errorf("expected 0 requests when no argument given; got %d", len(reqs))
	}
}

// =========================================================================
// Delete with multiple ownership selectors.
// Requirement: 08-REQ-9.E2
// =========================================================================

func TestCLI_VarsDelete_MultipleSelectors(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	_, _, err := runVarsCmd(t, server.URL, "test-api-key",
		"delete", "MYKEY", "--user", "--workspace", "myws")

	if err == nil {
		t.Fatal("expected error when multiple ownership selectors provided; got nil")
	}

	reqs := getRecords(records)
	if len(reqs) != 0 {
		t.Errorf("expected 0 requests when multiple selectors used; got %d", len(reqs))
	}
}

// =========================================================================
// Delete with 404 from API (key does not exist). No confirmation message
// should be printed.
// Requirement: 08-REQ-9.E3
// =========================================================================

func TestCLI_VarsDelete_NotFound(t *testing.T) {
	server, _ := varsMockServer(t, map[string]int{
		"/api/v1/user/vars/NOKEY": http.StatusNotFound,
	})
	defer server.Close()

	stdout, stderr, err := runVarsCmd(t, server.URL, "test-api-key",
		"delete", "NOKEY")

	if err == nil {
		t.Fatal("expected error for 404 response; got nil")
	}

	// Error envelope should be in stdout (CLIHandleError writes to stdout).
	if !hasErrorEnvelope(stdout) {
		t.Errorf("stdout should contain error envelope; got: %s", stdout)
	}

	// Confirmation message should NOT be printed when delete fails.
	if strings.Contains(stderr, "has been deleted") {
		t.Error("stderr should not contain confirmation message when delete fails")
	}
}

// =========================================================================
// TS-08-24: afc vars resolve myws GETs /api/v1/workspaces/myws/vars/resolved
// and prints the merged variable set including origin fields.
// Requirement: 08-REQ-10.1
// =========================================================================

func TestCLI_VarsResolve_HappyPath(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runVarsCmd(t, server.URL, "test-api-key",
		"resolve", "myws")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	reqs := getRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}

	req := reqs[0]
	if req.Method != "GET" {
		t.Errorf("method = %q; want GET", req.Method)
	}
	if req.Path != "/api/v1/workspaces/myws/vars/resolved" {
		t.Errorf("path = %q; want /api/v1/workspaces/myws/vars/resolved", req.Path)
	}

	// Stdout should contain key, value, and origin fields.
	if !strings.Contains(stdout, "DB_URL") {
		t.Errorf("stdout should contain DB_URL; got: %s", stdout)
	}
	if !strings.Contains(stdout, "origin") {
		t.Errorf("stdout should contain origin field; got: %s", stdout)
	}
	if !strings.Contains(stdout, "workspace") {
		t.Errorf("stdout should contain 'workspace' origin; got: %s", stdout)
	}
}

// =========================================================================
// afc vars resolve with no positional argument.
// Requirement: 08-REQ-10.E1
// =========================================================================

func TestCLI_VarsResolve_NoArgument(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	_, _, err := runVarsCmd(t, server.URL, "test-api-key", "resolve")

	if err == nil {
		t.Fatal("expected error when no workspace slug is provided; got nil")
	}

	reqs := getRecords(records)
	if len(reqs) != 0 {
		t.Errorf("expected 0 requests when no argument given; got %d", len(reqs))
	}
}

// =========================================================================
// afc vars resolve with 404 from API (workspace does not exist).
// Requirement: 08-REQ-10.E2
// =========================================================================

func TestCLI_VarsResolve_NotFound(t *testing.T) {
	server, _ := varsMockServer(t, map[string]int{
		"/api/v1/workspaces/badws/vars/resolved": http.StatusNotFound,
	})
	defer server.Close()

	stdout, _, err := runVarsCmd(t, server.URL, "test-api-key",
		"resolve", "badws")

	if err == nil {
		t.Fatal("expected error for 404 response; got nil")
	}
	if !hasErrorEnvelope(stdout) {
		t.Errorf("stdout should contain error envelope; got: %s", stdout)
	}
}

// =========================================================================
// afc vars resolve with ownership flag should be rejected.
// Requirement: 08-REQ-10.E4
// =========================================================================

func TestCLI_VarsResolve_RejectsOwnershipFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"--user flag", []string{"resolve", "myws", "--user"}},
		{"--org flag", []string{"resolve", "myws", "--org", "myorg"}},
		{"--workspace flag", []string{"resolve", "myws", "--workspace", "otherws"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, records := varsMockServer(t, nil)
			defer server.Close()

			_, _, err := runVarsCmd(t, server.URL, "test-api-key", tt.args...)

			if err == nil {
				t.Fatal("expected error when ownership flags provided to resolve; got nil")
			}

			reqs := getRecords(records)
			if len(reqs) != 0 {
				t.Errorf("expected 0 requests when ownership flag used with resolve; got %d", len(reqs))
			}
		})
	}
}

// =========================================================================
// TS-08-28: The --user flag is defined as a BoolVar with default false;
// explicitly passing --user also targets user scope.
// Requirement: 08-REQ-12.2
// (Tests via secrets list --user to confirm flag works as explicit opt-in.)
// =========================================================================

func TestCLI_SecretsList_ExplicitUserFlag(t *testing.T) {
	server, records := secretsMockServer(t, nil)
	defer server.Close()

	_, _, err := runSecretsCmd(t, server.URL, "test-api-key",
		"list", "--user")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	reqs := getRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}
	if reqs[0].Path != "/api/v1/user/secrets" {
		t.Errorf("path = %q; want /api/v1/user/secrets", reqs[0].Path)
	}
}

// =========================================================================
// Explicit --user flag on vars list also targets user scope.
// Requirement: 08-REQ-12.2 (vars variant)
// =========================================================================

func TestCLI_VarsList_ExplicitUserFlag(t *testing.T) {
	server, records := varsMockServer(t, nil)
	defer server.Close()

	_, _, err := runVarsCmd(t, server.URL, "test-api-key",
		"list", "--user")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	reqs := getRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}
	if reqs[0].Path != "/api/v1/user/vars" {
		t.Errorf("path = %q; want /api/v1/user/vars", reqs[0].Path)
	}
}
