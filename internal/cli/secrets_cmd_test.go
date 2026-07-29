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

// --- Test helpers for secrets CLI tests ---

// requestRecord captures an HTTP request received by the mock server.
type requestRecord struct {
	Method string
	Path   string
	Body   string
}

// secretsMockServer creates a test server that records all incoming requests
// and returns appropriate responses for secrets/vars API endpoints.
//
// failPaths maps URL paths to HTTP status codes that should trigger error
// responses. Paths not in failPaths receive successful responses.
func secretsMockServer(t *testing.T, failPaths map[string]int) (*httptest.Server, *[]requestRecord) {
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
			// Echo back entries with timestamps.
			var reqBody map[string]any
			if err := json.Unmarshal(bodyBytes, &reqBody); err == nil {
				if entries, ok := reqBody["entries"].([]any); ok {
					var resp []map[string]any
					for _, e := range entries {
						entry, _ := e.(map[string]any)
						resp = append(resp, map[string]any{
							"key":        entry["key"],
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
			json.NewEncoder(w).Encode([]map[string]any{ //nolint:errcheck
				{
					"key":        "MY_SECRET",
					"created_at": "2024-01-01T00:00:00Z",
					"updated_at": "2024-01-01T00:00:00Z",
				},
			})

		case http.MethodPatch:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			parts := strings.Split(r.URL.Path, "/")
			key := parts[len(parts)-1]
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"key":        key,
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

// runSecretsCmd executes a secrets subcommand through the full root command
// tree (with PersistentPreRunE credential resolution) and captures
// stdout/stderr.
func runSecretsCmd(t *testing.T, baseURL, apiKey string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	setupTestEnv(t)
	root := BuildRootCommand()
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	fullArgs := append([]string{"--endpoint-url", baseURL, "--api-key", apiKey, "secrets"}, args...)
	root.SetArgs(fullArgs)
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

// getRecords returns a snapshot of the recorded requests (thread-safe copy).
func getRecords(records *[]requestRecord) []requestRecord {
	return append([]requestRecord{}, *records...)
}

// parseBodyEntries extracts the "entries" array from a JSON request body.
func parseBodyEntries(t *testing.T, body string) []map[string]any {
	t.Helper()
	var reqBody map[string]any
	if err := json.Unmarshal([]byte(body), &reqBody); err != nil {
		t.Fatalf("failed to parse request body as JSON: %v\nbody: %s", err, body)
	}
	entriesRaw, ok := reqBody["entries"].([]any)
	if !ok {
		t.Fatalf("expected 'entries' array in request body; got: %s", body)
	}
	var entries []map[string]any
	for _, e := range entriesRaw {
		entry, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("entry is not a JSON object; got: %v", e)
		}
		entries = append(entries, entry)
	}
	return entries
}

// parseBodyValue extracts the "value" field from a JSON request body.
func parseBodyValue(t *testing.T, body string) string {
	t.Helper()
	var reqBody map[string]any
	if err := json.Unmarshal([]byte(body), &reqBody); err != nil {
		t.Fatalf("failed to parse request body as JSON: %v\nbody: %s", err, body)
	}
	val, ok := reqBody["value"].(string)
	if !ok {
		t.Fatalf("expected 'value' string in request body; got: %s", body)
	}
	return val
}

// =========================================================================
// TS-08-1: BuildRootCommand registers 'secrets' and 'vars' as top-level
// subcommands.
// Requirement: 08-REQ-1.1
// =========================================================================

func TestCLI_BuildRootCommand_RegistersSecretsAndVars(t *testing.T) {
	root := BuildRootCommand()

	// Collect top-level subcommand names.
	names := make(map[string]bool)
	for _, c := range root.Commands() {
		names[c.Name()] = true
	}

	if !names["secrets"] {
		t.Error("BuildRootCommand should register 'secrets' as a top-level subcommand")
	}
	if !names["vars"] {
		t.Error("BuildRootCommand should register 'vars' as a top-level subcommand")
	}

	// Verify 'secrets' has the expected subcommands.
	for _, c := range root.Commands() {
		if c.Name() == "secrets" {
			subNames := make(map[string]bool)
			for _, sub := range c.Commands() {
				subNames[sub.Name()] = true
			}
			for _, expected := range []string{"create", "list", "update", "delete"} {
				if !subNames[expected] {
					t.Errorf("'secrets' command is missing subcommand %q", expected)
				}
			}
		}
		if c.Name() == "vars" {
			subNames := make(map[string]bool)
			for _, sub := range c.Commands() {
				subNames[sub.Name()] = true
			}
			for _, expected := range []string{"create", "list", "update", "delete", "resolve"} {
				if !subNames[expected] {
					t.Errorf("'vars' command is missing subcommand %q", expected)
				}
			}
		}
	}
}

// =========================================================================
// TS-08-2: afc secrets create with no ownership flags defaults to user scope
// and POSTs to /api/v1/user/secrets with the correct entries body.
// Requirement: 08-REQ-2.1
// =========================================================================

func TestCLI_SecretsCreate_DefaultUserScope(t *testing.T) {
	server, records := secretsMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runSecretsCmd(t, server.URL, "test-api-key",
		"create", "API_KEY=abc123")

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
	if req.Path != "/api/v1/user/secrets" {
		t.Errorf("path = %q; want /api/v1/user/secrets", req.Path)
	}

	// Verify the request body has the correct entries.
	entries := parseBodyEntries(t, req.Body)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry; got %d", len(entries))
	}
	if entries[0]["key"] != "API_KEY" {
		t.Errorf("entry key = %q; want API_KEY", entries[0]["key"])
	}
	if entries[0]["value"] != "abc123" {
		t.Errorf("entry value = %q; want abc123", entries[0]["value"])
	}

	// Response should be printed to stdout.
	if !strings.Contains(stdout, "API_KEY") {
		t.Errorf("stdout should contain API_KEY; got: %s", stdout)
	}
}

// =========================================================================
// TS-08-3: afc secrets create --org myorg POSTs to
// /api/v1/orgs/myorg/secrets with the correct entries body.
// Requirement: 08-REQ-2.2
// =========================================================================

func TestCLI_SecretsCreate_OrgScope(t *testing.T) {
	server, records := secretsMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runSecretsCmd(t, server.URL, "test-api-key",
		"create", "KEY=val", "--org", "myorg")

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
	if req.Path != "/api/v1/orgs/myorg/secrets" {
		t.Errorf("path = %q; want /api/v1/orgs/myorg/secrets", req.Path)
	}

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

	if !strings.Contains(stdout, "KEY") {
		t.Errorf("stdout should contain KEY; got: %s", stdout)
	}
}

// =========================================================================
// TS-08-4: afc secrets create --workspace myws POSTs to
// /api/v1/workspaces/myws/secrets with the correct entries body.
// Requirement: 08-REQ-2.3
// =========================================================================

func TestCLI_SecretsCreate_WorkspaceScope(t *testing.T) {
	server, records := secretsMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runSecretsCmd(t, server.URL, "test-api-key",
		"create", "KEY=val", "--workspace", "myws")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	reqs := getRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}

	if reqs[0].Path != "/api/v1/workspaces/myws/secrets" {
		t.Errorf("path = %q; want /api/v1/workspaces/myws/secrets", reqs[0].Path)
	}

	if !strings.Contains(stdout, "KEY") {
		t.Errorf("stdout should contain KEY; got: %s", stdout)
	}
}

// =========================================================================
// TS-08-5: afc secrets create --user --org myorg --workspace myws makes
// three sequential POST calls in fixed user -> org -> workspace order and
// prints all three responses.
// Requirement: 08-REQ-2.4
// =========================================================================

func TestCLI_SecretsCreate_MultiScopeOrder(t *testing.T) {
	server, records := secretsMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runSecretsCmd(t, server.URL, "test-api-key",
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
		"/api/v1/user/secrets",
		"/api/v1/orgs/myorg/secrets",
		"/api/v1/workspaces/myws/secrets",
	}
	for i, expected := range expectedPaths {
		if reqs[i].Path != expected {
			t.Errorf("request[%d].path = %q; want %q", i, reqs[i].Path, expected)
		}
		if reqs[i].Method != "POST" {
			t.Errorf("request[%d].method = %q; want POST", i, reqs[i].Method)
		}
	}

	// All three responses should be in stdout.
	if !strings.Contains(stdout, "KEY") {
		t.Errorf("stdout should contain KEY from all responses; got: %s", stdout)
	}
}

// =========================================================================
// Multi-scope create with partial failure: org scope fails, user scope
// succeeds. Command should continue to all scopes and exit 1.
// Requirement: 08-REQ-2.E4
// =========================================================================

func TestCLI_SecretsCreate_MultiScopePartialFailure(t *testing.T) {
	server, records := secretsMockServer(t, map[string]int{
		"/api/v1/orgs/myorg/secrets": http.StatusInternalServerError,
	})
	defer server.Close()

	stdout, _, err := runSecretsCmd(t, server.URL, "test-api-key",
		"create", "TOKEN=xyz", "--user", "--org", "myorg")

	// Expect error because org scope failed.
	if err == nil {
		t.Fatal("expected error for partial multi-scope failure; got nil")
	}

	reqs := getRecords(records)
	if len(reqs) != 2 {
		t.Fatalf("expected 2 requests (user + org); got %d", len(reqs))
	}

	// First request should be to user scope.
	if reqs[0].Path != "/api/v1/user/secrets" {
		t.Errorf("first request path = %q; want /api/v1/user/secrets", reqs[0].Path)
	}
	// Second request should be to org scope.
	if reqs[1].Path != "/api/v1/orgs/myorg/secrets" {
		t.Errorf("second request path = %q; want /api/v1/orgs/myorg/secrets", reqs[1].Path)
	}

	// User scope response should still appear in stdout.
	if !strings.Contains(stdout, "TOKEN") {
		t.Error("stdout should contain user-scope response even when org scope fails")
	}

	// Error envelope for org failure should appear in stdout
	// (CLIHandleError writes to stdout, not stderr — see errata).
	if !hasErrorEnvelope(stdout) {
		t.Errorf("stdout should contain error envelope for org scope failure; got: %s", stdout)
	}
}

// =========================================================================
// Single-scope create API error: DoRequest returns an error.
// Requirement: 08-REQ-2.E5
// =========================================================================

func TestCLI_SecretsCreate_SingleScopeAPIError(t *testing.T) {
	server, records := secretsMockServer(t, map[string]int{
		"/api/v1/user/secrets": http.StatusForbidden,
	})
	defer server.Close()

	stdout, _, err := runSecretsCmd(t, server.URL, "test-api-key",
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
// TS-08-25: The key=value parser splits on the first '=' so that values
// containing '=' characters are preserved correctly.
// Requirement: 08-REQ-11.1
// =========================================================================

func TestCLI_SecretsCreate_ValueContainsEquals(t *testing.T) {
	server, records := secretsMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runSecretsCmd(t, server.URL, "test-api-key",
		"create", "KEY=val=with=equals")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	reqs := getRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request; got %d", len(reqs))
	}

	entries := parseBodyEntries(t, reqs[0].Body)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry; got %d", len(entries))
	}
	if entries[0]["key"] != "KEY" {
		t.Errorf("key = %q; want KEY", entries[0]["key"])
	}
	if entries[0]["value"] != "val=with=equals" {
		t.Errorf("value = %q; want val=with=equals", entries[0]["value"])
	}

	if !strings.Contains(stdout, "KEY") {
		t.Errorf("stdout should contain KEY; got: %s", stdout)
	}
}

// =========================================================================
// Comma-separated key=value pairs in create command.
// Requirement: 08-REQ-11.2, 08-REQ-2.5
// (Mirrors TS-08-26 but uses secrets create instead of vars create.)
// =========================================================================

func TestCLI_SecretsCreate_CommaSeparated(t *testing.T) {
	server, records := secretsMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runSecretsCmd(t, server.URL, "test-api-key",
		"create", "KEY1=val1,KEY2=val2")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	reqs := getRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request; got %d", len(reqs))
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
// Argument validation: missing '=' character.
// Requirement: 08-REQ-2.E1, 08-REQ-11.E1
// =========================================================================

func TestCLI_SecretsCreate_MissingEquals(t *testing.T) {
	server, records := secretsMockServer(t, nil)
	defer server.Close()

	_, _, err := runSecretsCmd(t, server.URL, "test-api-key",
		"create", "BADKEY")

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
// Argument validation: empty key after comma-split.
// Requirement: 08-REQ-2.E2, 08-REQ-11.E2
// =========================================================================

func TestCLI_SecretsCreate_EmptyKeyInCommaSplit(t *testing.T) {
	server, records := secretsMockServer(t, nil)
	defer server.Close()

	_, _, err := runSecretsCmd(t, server.URL, "test-api-key",
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
// Argument validation: no positional argument provided to create.
// Requirement: 08-REQ-2.E3
// =========================================================================

func TestCLI_SecretsCreate_NoArgument(t *testing.T) {
	server, records := secretsMockServer(t, nil)
	defer server.Close()

	_, _, err := runSecretsCmd(t, server.URL, "test-api-key", "create")

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
// TS-08-7: afc secrets list with no ownership flags defaults to user scope
// and GETs /api/v1/user/secrets.
// Requirement: 08-REQ-3.1, 08-REQ-12.1
// =========================================================================

func TestCLI_SecretsList_DefaultUserScope(t *testing.T) {
	server, records := secretsMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runSecretsCmd(t, server.URL, "test-api-key", "list")

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
	if reqs[0].Path != "/api/v1/user/secrets" {
		t.Errorf("path = %q; want /api/v1/user/secrets", reqs[0].Path)
	}

	if !strings.Contains(stdout, "MY_SECRET") {
		t.Errorf("stdout should contain MY_SECRET; got: %s", stdout)
	}
}

// =========================================================================
// TS-08-8: afc secrets list --org myorg GETs /api/v1/orgs/myorg/secrets.
// Requirement: 08-REQ-3.2
// =========================================================================

func TestCLI_SecretsList_OrgScope(t *testing.T) {
	server, records := secretsMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runSecretsCmd(t, server.URL, "test-api-key",
		"list", "--org", "myorg")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	reqs := getRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}
	if reqs[0].Path != "/api/v1/orgs/myorg/secrets" {
		t.Errorf("path = %q; want /api/v1/orgs/myorg/secrets", reqs[0].Path)
	}

	if !strings.Contains(stdout, "MY_SECRET") {
		t.Errorf("stdout should contain response content; got: %s", stdout)
	}
}

// =========================================================================
// TS-08-9: afc secrets list --workspace myws GETs
// /api/v1/workspaces/myws/secrets.
// Requirement: 08-REQ-3.3
// =========================================================================

func TestCLI_SecretsList_WorkspaceScope(t *testing.T) {
	server, records := secretsMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runSecretsCmd(t, server.URL, "test-api-key",
		"list", "--workspace", "myws")

	if err != nil {
		t.Fatalf("expected exit 0; got error: %v", err)
	}

	reqs := getRecords(records)
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request; got %d", len(reqs))
	}
	if reqs[0].Path != "/api/v1/workspaces/myws/secrets" {
		t.Errorf("path = %q; want /api/v1/workspaces/myws/secrets", reqs[0].Path)
	}

	if !strings.Contains(stdout, "MY_SECRET") {
		t.Errorf("stdout should contain response content; got: %s", stdout)
	}
}

// =========================================================================
// Multiple ownership flags on list command (single-scope command) should
// return an error.
// Requirement: 08-REQ-3.E1, 08-REQ-12.E1
// =========================================================================

func TestCLI_SecretsList_MultipleSelectors(t *testing.T) {
	server, records := secretsMockServer(t, nil)
	defer server.Close()

	_, _, err := runSecretsCmd(t, server.URL, "test-api-key",
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
// List API error.
// Requirement: 08-REQ-3.E2
// =========================================================================

func TestCLI_SecretsList_APIError(t *testing.T) {
	server, _ := secretsMockServer(t, map[string]int{
		"/api/v1/user/secrets": http.StatusForbidden,
	})
	defer server.Close()

	stdout, _, err := runSecretsCmd(t, server.URL, "test-api-key", "list")

	if err == nil {
		t.Fatal("expected error for API failure; got nil")
	}
	if !hasErrorEnvelope(stdout) {
		t.Errorf("stdout should contain error envelope; got: %s", stdout)
	}
}

// =========================================================================
// TS-08-10: afc secrets update KEY=newval with no flags defaults to user
// scope and PATCHes /api/v1/user/secrets/KEY with body {"value":"newval"}.
// Requirement: 08-REQ-4.1
// =========================================================================

func TestCLI_SecretsUpdate_DefaultUserScope(t *testing.T) {
	server, records := secretsMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runSecretsCmd(t, server.URL, "test-api-key",
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
	if req.Path != "/api/v1/user/secrets/KEY" {
		t.Errorf("path = %q; want /api/v1/user/secrets/KEY", req.Path)
	}

	val := parseBodyValue(t, req.Body)
	if val != "newval" {
		t.Errorf("body value = %q; want newval", val)
	}

	if !strings.Contains(stdout, "KEY") {
		t.Errorf("stdout should contain KEY; got: %s", stdout)
	}
}

// =========================================================================
// TS-08-11: afc secrets update KEY=newval --org myorg PATCHes
// /api/v1/orgs/myorg/secrets/KEY with the correct body.
// Requirement: 08-REQ-4.2
// =========================================================================

func TestCLI_SecretsUpdate_OrgScope(t *testing.T) {
	server, records := secretsMockServer(t, nil)
	defer server.Close()

	stdout, _, err := runSecretsCmd(t, server.URL, "test-api-key",
		"update", "KEY=newval", "--org", "myorg")

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
	if reqs[0].Path != "/api/v1/orgs/myorg/secrets/KEY" {
		t.Errorf("path = %q; want /api/v1/orgs/myorg/secrets/KEY", reqs[0].Path)
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
// Update with missing '=' in argument.
// Requirement: 08-REQ-4.E1
// =========================================================================

func TestCLI_SecretsUpdate_MissingEquals(t *testing.T) {
	server, records := secretsMockServer(t, nil)
	defer server.Close()

	_, _, err := runSecretsCmd(t, server.URL, "test-api-key",
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
// Requirement: 08-REQ-4.E2
// =========================================================================

func TestCLI_SecretsUpdate_MultipleSelectors(t *testing.T) {
	server, records := secretsMockServer(t, nil)
	defer server.Close()

	_, _, err := runSecretsCmd(t, server.URL, "test-api-key",
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
// Requirement: 08-REQ-4.E4
// =========================================================================

func TestCLI_SecretsUpdate_NoArgument(t *testing.T) {
	server, records := secretsMockServer(t, nil)
	defer server.Close()

	_, _, err := runSecretsCmd(t, server.URL, "test-api-key", "update")

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
// Requirement: 08-REQ-4.E3
// =========================================================================

func TestCLI_SecretsUpdate_NotFound(t *testing.T) {
	server, _ := secretsMockServer(t, map[string]int{
		"/api/v1/user/secrets/NOKEY": http.StatusNotFound,
	})
	defer server.Close()

	stdout, _, err := runSecretsCmd(t, server.URL, "test-api-key",
		"update", "NOKEY=newval")

	if err == nil {
		t.Fatal("expected error for 404 response; got nil")
	}
	if !hasErrorEnvelope(stdout) {
		t.Errorf("stdout should contain error envelope; got: %s", stdout)
	}
}

// =========================================================================
// TS-08-12: afc secrets delete MYKEY with no flags defaults to user scope,
// DELETEs /api/v1/user/secrets/MYKEY, stdout is empty, stderr has
// confirmation message.
// Requirement: 08-REQ-5.1
// =========================================================================

func TestCLI_SecretsDelete_DefaultUserScope(t *testing.T) {
	server, records := secretsMockServer(t, nil)
	defer server.Close()

	stdout, stderr, err := runSecretsCmd(t, server.URL, "test-api-key",
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
	if req.Path != "/api/v1/user/secrets/MYKEY" {
		t.Errorf("path = %q; want /api/v1/user/secrets/MYKEY", req.Path)
	}

	// stdout should be empty (CLIPrintResult is NOT called for DELETE).
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("stdout should be empty for DELETE; got: %s", stdout)
	}

	// Confirmation message should be on stderr.
	if !strings.Contains(stderr, "Secret 'MYKEY' has been deleted.") {
		t.Errorf("stderr should contain confirmation message; got: %s", stderr)
	}
}

// =========================================================================
// TS-08-13: afc secrets delete MYKEY --org myorg DELETEs
// /api/v1/orgs/myorg/secrets/MYKEY and prints confirmation to stderr.
// Requirement: 08-REQ-5.2
// =========================================================================

func TestCLI_SecretsDelete_OrgScope(t *testing.T) {
	server, records := secretsMockServer(t, nil)
	defer server.Close()

	stdout, stderr, err := runSecretsCmd(t, server.URL, "test-api-key",
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
	if reqs[0].Path != "/api/v1/orgs/myorg/secrets/MYKEY" {
		t.Errorf("path = %q; want /api/v1/orgs/myorg/secrets/MYKEY", reqs[0].Path)
	}

	if strings.TrimSpace(stdout) != "" {
		t.Errorf("stdout should be empty for DELETE; got: %s", stdout)
	}
	if !strings.Contains(stderr, "Secret 'MYKEY' has been deleted.") {
		t.Errorf("stderr should contain confirmation message; got: %s", stderr)
	}
}

// =========================================================================
// Delete with no positional argument.
// Requirement: 08-REQ-5.E1
// =========================================================================

func TestCLI_SecretsDelete_NoArgument(t *testing.T) {
	server, records := secretsMockServer(t, nil)
	defer server.Close()

	_, _, err := runSecretsCmd(t, server.URL, "test-api-key", "delete")

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
// Requirement: 08-REQ-5.E2
// =========================================================================

func TestCLI_SecretsDelete_MultipleSelectors(t *testing.T) {
	server, records := secretsMockServer(t, nil)
	defer server.Close()

	_, _, err := runSecretsCmd(t, server.URL, "test-api-key",
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
// Requirement: 08-REQ-5.E3
// =========================================================================

func TestCLI_SecretsDelete_NotFound(t *testing.T) {
	server, _ := secretsMockServer(t, map[string]int{
		"/api/v1/user/secrets/NOKEY": http.StatusNotFound,
	})
	defer server.Close()

	stdout, stderr, err := runSecretsCmd(t, server.URL, "test-api-key",
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
