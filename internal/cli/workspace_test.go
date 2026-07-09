package cli_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers for workspace CLI tests
// ---------------------------------------------------------------------------

// requestsForRoute returns all recorded requests matching the given method and path.
func (s *stubServer) requestsForRoute(method, path string) []recordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []recordedRequest
	for _, r := range s.requests {
		if r.Method == method && r.Path == path {
			result = append(result, r)
		}
	}
	return result
}

// newDelayServer creates an httptest.Server from a custom handler. The server
// is automatically closed when the test finishes.
func newDelayServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(func() { srv.Close() })
	return srv
}

// ---------------------------------------------------------------------------
// TS-07-17: Verifies the afc workspace create command is registered as a Cobra
// subcommand with required flags --git-url and --slug, and optional flags
// --branch and --team.
// Requirement: 07-REQ-4.1
// ---------------------------------------------------------------------------

func TestWorkspaceCreate_CommandRegistered(t *testing.T) {
	// TS-07-17: Run `afc workspace create --help` and assert the help output
	// contains --git-url, --slug, --branch, and --team flags. The command
	// should be listed under 'afc workspace'.

	stdout, stderr, exitCode := execAfc(t, []string{"workspace", "create", "--help"})
	combined := stdout + stderr // Cobra may write to either stream.

	if exitCode != 0 {
		t.Errorf("expected exit code 0 for --help, got %d; output: %s", exitCode, combined)
	}

	// Assert the command exists and shows expected flags.
	requiredFlags := []string{"--git-url", "--slug"}
	for _, flag := range requiredFlags {
		if !strings.Contains(combined, flag) {
			t.Errorf("expected help output to contain %q flag, got:\n%s", flag, combined)
		}
	}

	optionalFlags := []string{"--branch", "--team"}
	for _, flag := range optionalFlags {
		if !strings.Contains(combined, flag) {
			t.Errorf("expected help output to contain %q flag, got:\n%s", flag, combined)
		}
	}
}

func TestWorkspaceCreate_WorkspaceParentCommand(t *testing.T) {
	// TS-07-17 complement: Verify 'workspace' is a parent command under 'afc'.

	stdout, stderr, exitCode := execAfc(t, []string{"workspace", "--help"})
	combined := stdout + stderr

	if exitCode != 0 {
		t.Errorf("expected exit code 0 for 'workspace --help', got %d; output: %s", exitCode, combined)
	}

	if !strings.Contains(combined, "create") {
		t.Errorf("expected 'workspace --help' to show 'create' subcommand, got:\n%s", combined)
	}
}

// ---------------------------------------------------------------------------
// TS-07-22: Verifies Cobra prints usage help and error message to stderr and
// exits with code 1 when a required flag is missing, before any API calls.
// Requirement: 07-REQ-4.6
// ---------------------------------------------------------------------------

func TestWorkspaceCreate_MissingSlugFlag(t *testing.T) {
	// TS-07-22: Run without --slug; assert exit code != 0; stderr contains
	// 'required' or 'slug'; no HTTP requests are made.
	//
	// No stub server is started — if the command somehow tries to make HTTP
	// calls, it would fail with a connection error to the unreachable URL.

	_, stderr, exitCode := execAfc(t, []string{
		"workspace", "create",
		"--git-url", "https://github.com/org/repo.git",
	}, "AF_HUB_URL=http://localhost:19999")

	if exitCode == 0 {
		t.Error("expected non-zero exit code when --slug is missing, got 0")
	}

	stderrLower := strings.ToLower(stderr)
	if !strings.Contains(stderrLower, "required") && !strings.Contains(stderrLower, "slug") {
		t.Errorf("expected stderr to mention 'required' or 'slug', got: %q", stderr)
	}
}

func TestWorkspaceCreate_MissingGitURLFlag(t *testing.T) {
	// TS-07-22 complement: Run without --git-url; assert exit code != 0.

	_, stderr, exitCode := execAfc(t, []string{
		"workspace", "create",
		"--slug", "my-ws",
	}, "AF_HUB_URL=http://localhost:19999")

	if exitCode == 0 {
		t.Error("expected non-zero exit code when --git-url is missing, got 0")
	}

	stderrLower := strings.ToLower(stderr)
	if !strings.Contains(stderrLower, "required") && !strings.Contains(stderrLower, "git-url") {
		t.Errorf("expected stderr to mention 'required' or 'git-url', got: %q", stderr)
	}
}

func TestWorkspaceCreate_BothRequiredFlagsMissing(t *testing.T) {
	// TS-07-22 complement: Running workspace create with no flags should fail
	// before making any API calls.

	_, stderr, exitCode := execAfc(t, []string{
		"workspace", "create",
	}, "AF_HUB_URL=http://localhost:19999")

	if exitCode == 0 {
		t.Error("expected non-zero exit code when both required flags are missing, got 0")
	}

	stderrLower := strings.ToLower(stderr)
	if !strings.Contains(stderrLower, "required") &&
		!strings.Contains(stderrLower, "slug") &&
		!strings.Contains(stderrLower, "git-url") {
		t.Errorf("expected stderr to mention 'required', 'slug', or 'git-url', got: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// TS-07-23: Verifies the afc workspace create command handler code does not
// directly call os.Exit.
// Requirement: 07-REQ-4.7
// ---------------------------------------------------------------------------

func TestWorkspaceCreate_NoOsExitInHandlerCode(t *testing.T) {
	// TS-07-23: Static analysis — search source files for os.Exit and log.Fatal
	// in workspace command handler files. Assert zero matches.
	//
	// The workspace command handler lives in internal/cli/ (workspace*.go files).
	// The only allowed os.Exit is in root.go's Execute() function or
	// cmd/afc/main.go.

	modRoot, err := goModRoot()
	if err != nil {
		t.Fatalf("failed to find module root: %v", err)
	}

	// Search for workspace-related source files in internal/cli/ and cmd/.
	cliDir := filepath.Join(modRoot, "internal", "cli")
	entries, err := os.ReadDir(cliDir)
	if err != nil {
		t.Fatalf("failed to read internal/cli/ directory: %v", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		// Only check workspace-related source files (not test files).
		if !strings.HasPrefix(name, "workspace") ||
			!strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") {
			continue
		}

		content, err := os.ReadFile(filepath.Join(cliDir, name))
		if err != nil {
			t.Fatalf("failed to read %s: %v", name, err)
		}

		source := string(content)
		if strings.Contains(source, "os.Exit") {
			t.Errorf("found os.Exit in %s — workspace handler code must not call os.Exit directly", name)
		}
		if strings.Contains(source, "log.Fatal") {
			t.Errorf("found log.Fatal in %s — workspace handler code must not call log.Fatal directly", name)
		}
	}
}

// ---------------------------------------------------------------------------
// TS-07-18: Verifies that when --team is provided, the CLI calls
// GET /api/v1/teams with a slug filter to resolve to a UUID before calling
// POST /api/v1/workspaces.
// Requirement: 07-REQ-4.2
// ---------------------------------------------------------------------------

func TestWorkspaceCreate_TeamSlugResolution(t *testing.T) {
	// TS-07-18: Mock GET /api/v1/teams returning team with id='team-uuid-1';
	// mock POST /api/v1/workspaces capturing request; assert GET called first
	// with slug=my-team; POST body contains team_id='team-uuid-1'.

	stub := newStubServer(t)

	// GET /api/v1/teams returns a list with one matching team.
	teamsResp := `[{"id":"team-uuid-1","slug":"my-team","name":"My Team","status":"active"}]`
	stub.onRoute("GET", "/api/v1/teams", http.StatusOK, teamsResp)

	// POST /api/v1/workspaces returns a created workspace.
	wsResp := `{"id":"ws-uuid-1","slug":"my-ws","git_url":"https://github.com/org/repo.git","branch":null,"owner_id":"user-uuid-1","team_id":"team-uuid-1","status":"active","created_at":"2026-07-09T00:00:00Z"}`
	stub.onRoute("POST", "/api/v1/workspaces", http.StatusCreated, wsResp)

	stdout, _, exitCode := execAfc(t, []string{
		"workspace", "create",
		"--git-url", "https://github.com/org/repo.git",
		"--slug", "my-ws",
		"--team", "my-team",
		"--api-key", "test-api-key",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}

	// Assert GET /api/v1/teams was called.
	getReqs := stub.requestsForRoute("GET", "/api/v1/teams")
	if len(getReqs) == 0 {
		t.Fatal("expected GET /api/v1/teams to be called for team slug resolution, but it was not")
	}

	// Assert POST /api/v1/workspaces was called.
	if !stub.receivedRequest("POST", "/api/v1/workspaces") {
		t.Fatal("expected POST /api/v1/workspaces to be called, but it was not")
	}

	// Verify the POST request body contains team_id with the resolved UUID.
	postReq := stub.lastRequestFor("POST", "/api/v1/workspaces")
	if postReq == nil {
		t.Fatal("no POST /api/v1/workspaces request recorded")
	}

	var postBody map[string]any
	if err := json.Unmarshal([]byte(postReq.Body), &postBody); err != nil {
		t.Fatalf("failed to parse POST body: %v", err)
	}

	if teamID, ok := postBody["team_id"]; !ok || teamID != "team-uuid-1" {
		t.Errorf("expected POST body team_id='team-uuid-1', got: %v", teamID)
	}

	// Verify stdout contains valid JSON workspace object.
	if !isValidJSON(stdout) {
		t.Fatalf("expected stdout to be valid JSON, got: %q", stdout)
	}
}

// ---------------------------------------------------------------------------
// TS-07-19: Verifies the CLI prints the created workspace object as JSON to
// stdout and exits with code 0 on a successful POST /api/v1/workspaces call.
// Requirement: 07-REQ-4.3
// ---------------------------------------------------------------------------

func TestWorkspaceCreate_SuccessOutput(t *testing.T) {
	// TS-07-19: Mock POST /api/v1/workspaces returning 201 with valid workspace
	// JSON; run CLI; assert stdout is parseable JSON with expected fields;
	// exit code == 0.

	stub := newStubServer(t)
	wsResp := `{"id":"ws-uuid-1","slug":"my-ws","git_url":"https://github.com/org/repo.git","branch":null,"owner_id":"user-uuid-1","team_id":null,"status":"active","created_at":"2026-07-09T00:00:00Z"}`
	stub.onRoute("POST", "/api/v1/workspaces", http.StatusCreated, wsResp)

	stdout, _, exitCode := execAfc(t, []string{
		"workspace", "create",
		"--git-url", "https://github.com/org/repo.git",
		"--slug", "my-ws",
		"--api-key", "test-api-key",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}

	// Assert stdout is valid JSON.
	if !isValidJSON(stdout) {
		t.Fatalf("expected stdout to be valid JSON, got: %q", stdout)
	}

	// Parse and verify expected fields.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &parsed); err != nil {
		t.Fatalf("failed to parse stdout JSON: %v", err)
	}

	if slug, ok := parsed["slug"]; !ok || slug != "my-ws" {
		t.Errorf("expected slug='my-ws', got: %v", slug)
	}
	if gitURL, ok := parsed["git_url"]; !ok || gitURL != "https://github.com/org/repo.git" {
		t.Errorf("expected git_url='https://github.com/org/repo.git', got: %v", gitURL)
	}
	if id, ok := parsed["id"]; !ok {
		t.Error("expected 'id' field in response")
	} else if idStr, isStr := id.(string); !isStr || idStr == "" {
		t.Errorf("expected 'id' to be a non-empty string, got: %v", id)
	}
}

func TestWorkspaceCreate_SuccessWithBranch(t *testing.T) {
	// TS-07-19 complement: Verify that --branch flag is passed through to the
	// API request body.

	stub := newStubServer(t)
	wsResp := `{"id":"ws-uuid-1","slug":"my-ws","git_url":"https://github.com/org/repo.git","branch":"main","owner_id":"user-uuid-1","team_id":null,"status":"active","created_at":"2026-07-09T00:00:00Z"}`
	stub.onRoute("POST", "/api/v1/workspaces", http.StatusCreated, wsResp)

	stdout, _, exitCode := execAfc(t, []string{
		"workspace", "create",
		"--git-url", "https://github.com/org/repo.git",
		"--slug", "my-ws",
		"--branch", "main",
		"--api-key", "test-api-key",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}

	if !isValidJSON(stdout) {
		t.Fatalf("expected stdout to be valid JSON, got: %q", stdout)
	}

	// Verify the POST request body contains the branch field.
	postReq := stub.lastRequestFor("POST", "/api/v1/workspaces")
	if postReq == nil {
		t.Fatal("no POST /api/v1/workspaces request recorded")
	}

	var postBody map[string]any
	if err := json.Unmarshal([]byte(postReq.Body), &postBody); err != nil {
		t.Fatalf("failed to parse POST body: %v", err)
	}

	if branch, ok := postBody["branch"]; !ok || branch != "main" {
		t.Errorf("expected POST body branch='main', got: %v", branch)
	}
}

func TestWorkspaceCreate_RequestBodyContainsFields(t *testing.T) {
	// TS-07-19 complement: Verify the POST request body includes slug and
	// git_url from the flags, and that the request is authenticated.

	stub := newStubServer(t)
	wsResp := `{"id":"ws-uuid-1","slug":"my-ws","git_url":"https://github.com/org/repo.git","branch":null,"owner_id":"user-uuid-1","team_id":null,"status":"active","created_at":"2026-07-09T00:00:00Z"}`
	stub.onRoute("POST", "/api/v1/workspaces", http.StatusCreated, wsResp)

	_, _, exitCode := execAfc(t, []string{
		"workspace", "create",
		"--git-url", "https://github.com/org/repo.git",
		"--slug", "my-ws",
		"--api-key", "test-api-key",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}

	// Verify the request was authenticated.
	postReq := stub.lastRequestFor("POST", "/api/v1/workspaces")
	if postReq == nil {
		t.Fatal("no POST /api/v1/workspaces request recorded")
	}

	authHeader := postReq.Header.Get("Authorization")
	if !strings.Contains(authHeader, "test-api-key") {
		t.Errorf("expected Authorization header to contain 'test-api-key', got: %q", authHeader)
	}

	var body map[string]any
	if err := json.Unmarshal([]byte(postReq.Body), &body); err != nil {
		t.Fatalf("failed to parse POST body: %v", err)
	}

	if slug, ok := body["slug"]; !ok || slug != "my-ws" {
		t.Errorf("expected POST body slug='my-ws', got: %v", slug)
	}
	if gitURL, ok := body["git_url"]; !ok || gitURL != "https://github.com/org/repo.git" {
		t.Errorf("expected POST body git_url='https://github.com/org/repo.git', got: %v", gitURL)
	}
}

// ---------------------------------------------------------------------------
// TS-07-20: Verifies the CLI prints an error to stderr and exits with code 1
// (without calling POST /api/v1/workspaces) when the team slug cannot be
// resolved.
// Requirement: 07-REQ-4.4
// ---------------------------------------------------------------------------

func TestWorkspaceCreate_TeamSlugNotFound(t *testing.T) {
	// TS-07-20: Mock GET /api/v1/teams returning empty list; run CLI with
	// --team unknown-team; assert exit code != 0; stderr contains 'unknown-team';
	// POST /api/v1/workspaces never called.

	stub := newStubServer(t)

	// GET /api/v1/teams returns empty list (team not found).
	stub.onRoute("GET", "/api/v1/teams", http.StatusOK, `[]`)

	// POST /api/v1/workspaces should never be called.
	stub.onRoute("POST", "/api/v1/workspaces", http.StatusCreated,
		`{"id":"ws-uuid-1","slug":"my-ws","git_url":"https://github.com/org/repo.git","status":"active"}`)

	_, stderr, exitCode := execAfc(t, []string{
		"workspace", "create",
		"--git-url", "https://github.com/org/repo.git",
		"--slug", "my-ws",
		"--team", "unknown-team",
		"--api-key", "test-api-key",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode == 0 {
		t.Error("expected non-zero exit code when team slug cannot be resolved, got 0")
	}

	if !strings.Contains(stderr, "unknown-team") {
		t.Errorf("expected stderr to mention 'unknown-team', got: %q", stderr)
	}

	// Assert POST /api/v1/workspaces was NOT called.
	if stub.receivedRequest("POST", "/api/v1/workspaces") {
		t.Error("expected POST /api/v1/workspaces to NOT be called when team slug resolution fails")
	}
}

// ---------------------------------------------------------------------------
// TS-07-21: Verifies the CLI prints the error message from the standard error
// envelope to stderr and exits with code 1 when POST /api/v1/workspaces
// returns an error.
// Requirement: 07-REQ-4.5
// ---------------------------------------------------------------------------

func TestWorkspaceCreate_APIErrorEnvelope(t *testing.T) {
	// TS-07-21: Mock POST /api/v1/workspaces returning HTTP 409 with error
	// envelope; assert stderr contains 'workspace slug already exists';
	// exit code != 0.

	stub := newStubServer(t)
	stub.onRoute("POST", "/api/v1/workspaces", http.StatusConflict,
		`{"error":{"code":"409","message":"workspace slug already exists"}}`)

	_, stderr, exitCode := execAfc(t, []string{
		"workspace", "create",
		"--git-url", "https://github.com/org/repo.git",
		"--slug", "existing-ws",
		"--api-key", "test-api-key",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode == 0 {
		t.Error("expected non-zero exit code on 409 conflict, got 0")
	}

	if !strings.Contains(stderr, "workspace slug already exists") {
		t.Errorf("expected stderr to contain 'workspace slug already exists', got: %q", stderr)
	}
}

func TestWorkspaceCreate_APIError400(t *testing.T) {
	// TS-07-21 complement: Test with a 400 error response.

	stub := newStubServer(t)
	stub.onRoute("POST", "/api/v1/workspaces", http.StatusBadRequest,
		`{"error":{"code":"400","message":"invalid slug format"}}`)

	_, stderr, exitCode := execAfc(t, []string{
		"workspace", "create",
		"--git-url", "https://github.com/org/repo.git",
		"--slug", "my-ws",
		"--api-key", "test-api-key",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode == 0 {
		t.Error("expected non-zero exit code on 400 error, got 0")
	}

	if !strings.Contains(stderr, "invalid slug format") {
		t.Errorf("expected stderr to contain 'invalid slug format', got: %q", stderr)
	}
}

func TestWorkspaceCreate_APIError403(t *testing.T) {
	// TS-07-21 complement: Test with a 403 error response for admin token.

	stub := newStubServer(t)
	stub.onRoute("POST", "/api/v1/workspaces", http.StatusForbidden,
		`{"error":{"code":"403","message":"workspace creation requires user authentication"}}`)

	_, stderr, exitCode := execAfc(t, []string{
		"workspace", "create",
		"--git-url", "https://github.com/org/repo.git",
		"--slug", "my-ws",
		"--api-key", "admin-token",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode == 0 {
		t.Error("expected non-zero exit code on 403 error, got 0")
	}

	if !strings.Contains(stderr, "workspace creation requires user authentication") {
		t.Errorf("expected stderr to contain 'workspace creation requires user authentication', got: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// TS-07-E8: Verifies the CLI prints a human-readable network error to stderr
// and exits with code 1 without retrying when GET /api/v1/teams times out
// during team slug resolution.
// Requirement: 07-REQ-4.E1
// ---------------------------------------------------------------------------

func TestWorkspaceCreate_TeamResolutionTimeout(t *testing.T) {
	// TS-07-E8: Start a server that hangs on GET /api/v1/teams (simulating
	// timeout); CLI configured with a short HTTP timeout (AFC_HTTP_TIMEOUT=1);
	// assert exit code != 0; stderr mentions a network/timeout failure;
	// POST /api/v1/workspaces never called.

	if testing.Short() {
		t.Skip("skipping timeout test in short mode")
	}

	var postCalled bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/teams", func(w http.ResponseWriter, r *http.Request) {
		// Delay long enough for the CLI timeout to trigger.
		time.Sleep(10 * time.Second)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `[{"id":"team-uuid-1","slug":"some-team"}]`)
	})
	mux.HandleFunc("/api/v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		postCalled = true
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":"ws-1"}`)
	})

	srv := newDelayServer(t, mux)

	_, stderr, exitCode := execAfc(t, []string{
		"workspace", "create",
		"--git-url", "https://github.com/org/repo.git",
		"--slug", "my-ws",
		"--team", "some-team",
		"--api-key", "test-api-key",
	},
		"AF_HUB_URL="+srv.URL,
		"AFC_HTTP_TIMEOUT=1",
	)

	if exitCode == 0 {
		t.Error("expected non-zero exit code on team resolution timeout, got 0")
	}

	if strings.TrimSpace(stderr) == "" {
		t.Error("expected non-empty stderr describing the network failure")
	}

	// Verify the error is about a network/timeout failure, not just "unknown command".
	stderrLower := strings.ToLower(stderr)
	hasNetworkRef := strings.Contains(stderrLower, "timeout") ||
		strings.Contains(stderrLower, "deadline") ||
		strings.Contains(stderrLower, "connect") ||
		strings.Contains(stderrLower, "refused") ||
		strings.Contains(stderrLower, "failed")
	if !hasNetworkRef {
		t.Errorf("expected stderr to mention timeout/deadline/connect/refused/failed, got: %q", stderr)
	}

	if postCalled {
		t.Error("expected POST /api/v1/workspaces to NOT be called when team resolution times out")
	}
}

// ---------------------------------------------------------------------------
// TS-07-E9: Verifies the CLI prints a human-readable network error to stderr
// and exits with code 1 when POST /api/v1/workspaces times out.
// Requirement: 07-REQ-4.E2
// ---------------------------------------------------------------------------

func TestWorkspaceCreate_PostTimeout(t *testing.T) {
	// TS-07-E9: Start a server that hangs on POST /api/v1/workspaces; CLI
	// configured with a short HTTP timeout (AFC_HTTP_TIMEOUT=1); assert
	// exit code != 0; stderr describes POST failure with a network/timeout
	// error message.

	if testing.Short() {
		t.Skip("skipping timeout test in short mode")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		// Delay long enough for the CLI timeout to trigger.
		time.Sleep(10 * time.Second)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":"ws-1"}`)
	})

	srv := newDelayServer(t, mux)

	_, stderr, exitCode := execAfc(t, []string{
		"workspace", "create",
		"--git-url", "https://github.com/org/repo.git",
		"--slug", "my-ws",
		"--api-key", "test-api-key",
	},
		"AF_HUB_URL="+srv.URL,
		"AFC_HTTP_TIMEOUT=1",
	)

	if exitCode == 0 {
		t.Error("expected non-zero exit code on POST timeout, got 0")
	}

	if strings.TrimSpace(stderr) == "" {
		t.Error("expected non-empty stderr describing the POST failure")
	}

	// Verify the error is about a network/timeout failure, not just "unknown command".
	stderrLower := strings.ToLower(stderr)
	hasNetworkRef := strings.Contains(stderrLower, "timeout") ||
		strings.Contains(stderrLower, "deadline") ||
		strings.Contains(stderrLower, "connect") ||
		strings.Contains(stderrLower, "refused") ||
		strings.Contains(stderrLower, "failed")
	if !hasNetworkRef {
		t.Errorf("expected stderr to mention timeout/deadline/connect/refused/failed, got: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// TS-07-E10: Verifies the CLI prints a descriptive parse error to stderr and
// exits with code 1 when the API response body cannot be parsed as valid JSON.
// Requirement: 07-REQ-4.E3
// ---------------------------------------------------------------------------

func TestWorkspaceCreate_InvalidJSONResponse(t *testing.T) {
	// TS-07-E10: Mock POST /api/v1/workspaces returning HTTP 200 with body
	// 'not-json-at-all'; assert exit code != 0; stderr contains 'parse',
	// 'invalid', 'unexpected', or 'json'.

	stub := newStubServer(t)
	stub.onRouteWithContentType("POST", "/api/v1/workspaces", http.StatusOK,
		"not-json-at-all", "text/plain")

	_, stderr, exitCode := execAfc(t, []string{
		"workspace", "create",
		"--git-url", "https://github.com/org/repo.git",
		"--slug", "my-ws",
		"--api-key", "test-api-key",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode == 0 {
		t.Error("expected non-zero exit code on invalid JSON response, got 0")
	}

	stderrLower := strings.ToLower(stderr)
	hasParseRef := strings.Contains(stderrLower, "parse") ||
		strings.Contains(stderrLower, "invalid") ||
		strings.Contains(stderrLower, "unexpected") ||
		strings.Contains(stderrLower, "json") ||
		strings.Contains(stderrLower, "decode")
	if !hasParseRef {
		t.Errorf("expected stderr to contain 'parse', 'invalid', 'unexpected', 'json', or 'decode', got: %q", stderr)
	}
}

func TestWorkspaceCreate_InvalidJSONResponseOnSuccess(t *testing.T) {
	// TS-07-E10 complement: HTTP 201 (success status) but with invalid JSON
	// body. The CLI should still fail with a parse error.

	stub := newStubServer(t)
	stub.onRouteWithContentType("POST", "/api/v1/workspaces", http.StatusCreated,
		"<html>Internal Error</html>", "text/html")

	_, stderr, exitCode := execAfc(t, []string{
		"workspace", "create",
		"--git-url", "https://github.com/org/repo.git",
		"--slug", "my-ws",
		"--api-key", "test-api-key",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode == 0 {
		t.Error("expected non-zero exit code on unparseable response, got 0")
	}

	stderrLower := strings.ToLower(stderr)
	hasRef := strings.Contains(stderrLower, "parse") ||
		strings.Contains(stderrLower, "invalid") ||
		strings.Contains(stderrLower, "unexpected") ||
		strings.Contains(stderrLower, "json") ||
		strings.Contains(stderrLower, "decode")
	if !hasRef {
		t.Errorf("expected stderr to contain parse/invalid/unexpected/json/decode, got: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// Additional: Verify missing credentials error for workspace create.
// ---------------------------------------------------------------------------

func TestWorkspaceCreate_MissingCredentials(t *testing.T) {
	// Without --api-key and without AF_HUB_API_KEY, the CLI should fail with
	// an error about missing credentials.

	stub := newStubServer(t)
	stub.onRoute("POST", "/api/v1/workspaces", http.StatusCreated,
		`{"id":"ws-uuid-1","slug":"my-ws","status":"active"}`)

	_, stderr, exitCode := execAfc(t, []string{
		"workspace", "create",
		"--git-url", "https://github.com/org/repo.git",
		"--slug", "my-ws",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode == 0 {
		t.Error("expected non-zero exit code when credentials are missing, got 0")
	}

	stderrLower := strings.ToLower(stderr)
	hasCred := strings.Contains(stderrLower, "api-key") ||
		strings.Contains(stderrLower, "credential") ||
		strings.Contains(stderrLower, "auth") ||
		strings.Contains(stderr, "AF_HUB_API_KEY")
	if !hasCred {
		t.Errorf("expected stderr to mention api-key/credential/auth/AF_HUB_API_KEY, got: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// Additional: Verify server unreachable error for POST.
// ---------------------------------------------------------------------------

func TestWorkspaceCreate_ServerUnreachable(t *testing.T) {
	// When the server is unreachable, the CLI should print a network error
	// that mentions connection failure, and exit non-zero.

	_, stderr, exitCode := execAfc(t, []string{
		"workspace", "create",
		"--git-url", "https://github.com/org/repo.git",
		"--slug", "my-ws",
		"--api-key", "test-api-key",
	}, "AF_HUB_URL=http://localhost:19999")

	if exitCode == 0 {
		t.Error("expected non-zero exit code when server is unreachable, got 0")
	}

	if strings.TrimSpace(stderr) == "" {
		t.Error("expected non-empty stderr describing the network failure")
	}

	// Verify the error is about a network failure, not just "unknown command".
	stderrLower := strings.ToLower(stderr)
	hasNetworkRef := strings.Contains(stderrLower, "connect") ||
		strings.Contains(stderrLower, "refused") ||
		strings.Contains(stderrLower, "failed") ||
		strings.Contains(stderrLower, "hub")
	if !hasNetworkRef {
		t.Errorf("expected stderr to describe a connection failure, got: %q", stderr)
	}
}
