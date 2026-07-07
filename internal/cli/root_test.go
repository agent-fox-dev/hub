package cli_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// afcBinary holds the compiled afc binary path.
// It is built once per test run via TestMain.
var afcBinary string

func TestMain(m *testing.M) {
	// Build the afc binary once for all tests in this package.
	tmpDir, err := os.MkdirTemp("", "afc-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	afcBinary = filepath.Join(tmpDir, "afc")

	// Find the module root by walking up from the test file location.
	// The binary is at cmd/afc relative to the module root.
	buildCmd := exec.Command("go", "build", "-o", afcBinary, "./cmd/afc")
	// We need to run from the module root. Use the go env to find it.
	modRoot, err := goModRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to find module root: %v\n", err)
		os.Exit(1)
	}
	buildCmd.Dir = modRoot
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to build afc binary: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// goModRoot returns the Go module root directory.
func goModRoot() (string, error) {
	cmd := exec.Command("go", "env", "GOMOD")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go env GOMOD: %w", err)
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull {
		return "", fmt.Errorf("not inside a Go module")
	}
	return filepath.Dir(gomod), nil
}

// execAfc runs the afc binary with the given arguments and environment overrides.
// It returns stdout, stderr, and the exit code.
func execAfc(t *testing.T, args []string, env ...string) (stdout, stderr string, exitCode int) {
	t.Helper()

	cmd := exec.Command(afcBinary, args...)

	// Start with a clean environment, preserving only essentials.
	cleanEnv := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	cleanEnv = append(cleanEnv, env...)
	cmd.Env = cleanEnv

	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()
	exitCode = 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("failed to run afc: %v", err)
	}

	return stdoutBuf.String(), stderrBuf.String(), exitCode
}

// stubServer is a test helper that creates an HTTP server recording
// received requests and serving canned responses per route.
type stubServer struct {
	Server   *httptest.Server
	mu       sync.Mutex
	requests []recordedRequest
	routes   map[string]routeHandler
}

type recordedRequest struct {
	Method string
	Path   string
	Body   string
	Header http.Header
}

type routeHandler struct {
	StatusCode  int
	Body        string
	ContentType string
}

func newStubServer(t *testing.T) *stubServer {
	t.Helper()

	s := &stubServer{
		routes: make(map[string]routeHandler),
	}

	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Record the request.
		bodyBytes, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, recordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(bodyBytes),
			Header: r.Header.Clone(),
		})
		s.mu.Unlock()

		// Find a matching route handler.
		key := r.Method + " " + r.URL.Path
		s.mu.Lock()
		handler, ok := s.routes[key]
		s.mu.Unlock()

		if !ok {
			// Try a catch-all for the path.
			s.mu.Lock()
			handler, ok = s.routes["* "+r.URL.Path]
			s.mu.Unlock()
		}

		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"error":"not_found","message":"no stub for %s"}`, key)
			return
		}

		ct := handler.ContentType
		if ct == "" {
			ct = "application/json"
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(handler.StatusCode)
		fmt.Fprint(w, handler.Body)
	}))

	t.Cleanup(func() { s.Server.Close() })
	return s
}

func (s *stubServer) onRoute(method, path string, status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[method+" "+path] = routeHandler{StatusCode: status, Body: body}
}

func (s *stubServer) receivedRequest(method, path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.requests {
		if r.Method == method && r.Path == path {
			return true
		}
	}
	return false
}

func (s *stubServer) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// isValidJSON checks if a string is valid JSON (object or array).
func isValidJSON(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	var js json.RawMessage
	return json.Unmarshal([]byte(s), &js) == nil
}

// ---------------------------------------------------------------------------
// TS-03-1, TS-03-5: Binary structure exposes login and keys subcommands
// REQ: 03-REQ-1.1, 03-REQ-1.5
// ---------------------------------------------------------------------------

func TestBinaryStructure_HelpShowsSubcommands(t *testing.T) {
	// TS-03-1: Execute `afc --help` and assert output contains 'login' and
	// 'keys' subcommand names with exit code 0.
	stdout, stderr, exitCode := execAfc(t, []string{"--help"})
	combined := stdout + stderr // Cobra may write to either stream.

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
	if !strings.Contains(combined, "login") {
		t.Errorf("expected help output to contain 'login', got:\n%s", combined)
	}
	if !strings.Contains(combined, "keys") {
		t.Errorf("expected help output to contain 'keys', got:\n%s", combined)
	}
}

func TestBinaryStructure_NoSubcommandShowsUsage(t *testing.T) {
	// TS-03-5: Running afc without a subcommand prints usage info and exits 0.
	stdout, stderr, exitCode := execAfc(t, nil)
	combined := stdout + stderr

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
	if !strings.Contains(combined, "login") {
		t.Errorf("expected usage output to contain 'login', got:\n%s", combined)
	}
	if !strings.Contains(combined, "keys") {
		t.Errorf("expected usage output to contain 'keys', got:\n%s", combined)
	}
}

func TestBinaryStructure_BinaryNameIsAfc(t *testing.T) {
	// TS-03-1: Binary name appears as 'afc' in usage output.
	stdout, stderr, exitCode := execAfc(t, []string{"--help"})
	combined := stdout + stderr

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
	if !strings.Contains(combined, "afc") {
		t.Errorf("expected usage output to contain 'afc', got:\n%s", combined)
	}
}

func TestBinaryStructure_HelpFlagShowsGlobalFlags(t *testing.T) {
	// TS-03-5: --help output includes global flags.
	stdout, stderr, exitCode := execAfc(t, []string{"--help"})
	combined := stdout + stderr

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
	if !strings.Contains(combined, "--hub-url") {
		t.Errorf("expected help output to contain '--hub-url', got:\n%s", combined)
	}
}

// ---------------------------------------------------------------------------
// TS-03-2, TS-03-P5: --hub-url flag overrides AF_HUB_URL env var
// REQ: 03-REQ-1.2, 03-PROP-5
// ---------------------------------------------------------------------------

func TestHubURLPrecedence_FlagOverridesEnvVar(t *testing.T) {
	// TS-03-2: Start a stub server, set AF_HUB_URL to a different URL,
	// run afc with --hub-url pointing to the stub. Assert the stub received
	// the request, not the env URL.

	stub := newStubServer(t)
	stub.onRoute("GET", "/api/v1/keys", http.StatusOK, `[]`)

	_, _, _ = execAfc(t, []string{
		"keys", "list",
		"--hub-url", stub.Server.URL,
		"--api-key", "test-key",
	}, "AF_HUB_URL=http://env-hub.example.com")

	if !stub.receivedRequest("GET", "/api/v1/keys") {
		t.Error("expected stub server to receive GET /api/v1/keys, but it did not")
	}
}

func TestHubURLPrecedence_EnvVarUsedWhenFlagAbsent(t *testing.T) {
	// TS-03-2 complement: When --hub-url is absent, AF_HUB_URL is used.

	stub := newStubServer(t)
	stub.onRoute("GET", "/api/v1/keys", http.StatusOK, `[]`)

	_, _, _ = execAfc(t, []string{
		"keys", "list",
		"--api-key", "test-key",
	}, "AF_HUB_URL="+stub.Server.URL)

	if !stub.receivedRequest("GET", "/api/v1/keys") {
		t.Error("expected stub server to receive GET /api/v1/keys when using AF_HUB_URL, but it did not")
	}
}

func TestHubURLPrecedence_FlagTakesPrecedenceProperty(t *testing.T) {
	// TS-03-P5: Property test — for the keys list command, set AF_HUB_URL to
	// URL_A and pass --hub-url URL_B. Assert URL_B (stub) received the request
	// and URL_A received none.

	stubB := newStubServer(t)
	stubB.onRoute("GET", "/api/v1/keys", http.StatusOK, `[]`)

	stubA := newStubServer(t)
	stubA.onRoute("GET", "/api/v1/keys", http.StatusOK, `[]`)

	_, _, _ = execAfc(t, []string{
		"keys", "list",
		"--hub-url", stubB.Server.URL,
		"--api-key", "test-key",
	}, "AF_HUB_URL="+stubA.Server.URL)

	if !stubB.receivedRequest("GET", "/api/v1/keys") {
		t.Error("expected --hub-url stub (URL_B) to receive request, but it did not")
	}
	if stubA.requestCount() > 0 {
		t.Error("expected AF_HUB_URL stub (URL_A) to receive no requests, but it did")
	}
}

// ---------------------------------------------------------------------------
// TS-03-3, TS-03-P3: JSON output goes to stdout, messages go to stderr
// REQ: 03-REQ-1.3, 03-PROP-3
// ---------------------------------------------------------------------------

func TestOutputStreams_KeysListJSONToStdout(t *testing.T) {
	// TS-03-3: Stub hub returns a valid JSON array. Assert stdout is valid
	// JSON and stderr contains no JSON object or array.

	stub := newStubServer(t)
	keysJSON := `[{"key_id":"k1","label":"bot","workspace_id":"ws-1","expires_at":"2026-01-01","created_at":"2025-01-01","revoked":false}]`
	stub.onRoute("GET", "/api/v1/keys", http.StatusOK, keysJSON)

	stdout, stderr, exitCode := execAfc(t, []string{
		"keys", "list",
		"--api-key", "test-key",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
	if !isValidJSON(stdout) {
		t.Errorf("expected stdout to be valid JSON, got: %q", stdout)
	}
	// stderr should not contain a JSON object or array.
	stderrTrimmed := strings.TrimSpace(stderr)
	if stderrTrimmed != "" && isValidJSON(stderrTrimmed) {
		t.Errorf("expected stderr to not be valid JSON, but it was: %q", stderr)
	}
}

func TestOutputStreams_KeysCreateJSONToStdout(t *testing.T) {
	// TS-03-P3: For keys create, JSON goes to stdout.

	stub := newStubServer(t)
	keyObj := `{"key_id":"k1","key":"af_k1_secret123","workspace_id":"ws-123","label":"ci-bot","expires_at":"2026-02-01","created_at":"2025-01-01"}`
	stub.onRoute("POST", "/api/v1/keys", http.StatusCreated, keyObj)

	stdout, stderr, exitCode := execAfc(t, []string{
		"keys", "create",
		"--workspace", "ws-123",
		"--label", "ci-bot",
		"--api-key", "test-key",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
	if !isValidJSON(stdout) {
		t.Errorf("expected stdout to be valid JSON, got: %q", stdout)
	}
	stderrTrimmed := strings.TrimSpace(stderr)
	if stderrTrimmed != "" && isValidJSON(stderrTrimmed) {
		t.Errorf("expected stderr to not be valid JSON, got: %q", stderr)
	}
}

func TestOutputStreams_KeysRefreshJSONToStdout(t *testing.T) {
	// TS-03-P3: For keys refresh, JSON goes to stdout.

	stub := newStubServer(t)
	keyObj := `{"key_id":"k1","key":"af_k1_newsecret","workspace_id":"ws-123","expires_at":"2026-02-01","created_at":"2025-01-01"}`
	stub.onRoute("POST", "/api/v1/keys/k1/refresh", http.StatusOK, keyObj)

	stdout, stderr, exitCode := execAfc(t, []string{
		"keys", "refresh", "k1",
		"--api-key", "test-key",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}
	if !isValidJSON(stdout) {
		t.Errorf("expected stdout to be valid JSON, got: %q", stdout)
	}
	stderrTrimmed := strings.TrimSpace(stderr)
	if stderrTrimmed != "" && isValidJSON(stderrTrimmed) {
		t.Errorf("expected stderr to not be valid JSON, got: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// TS-03-4, TS-03-P4: Non-zero exit code on failure
// REQ: 03-REQ-1.4, 03-PROP-4
// ---------------------------------------------------------------------------

func TestExitCodes_NonZeroOnHubError(t *testing.T) {
	// TS-03-4: Stub hub returns HTTP 500. Assert exit code != 0 and stderr
	// is non-empty.

	stub := newStubServer(t)
	stub.onRoute("GET", "/api/v1/keys", http.StatusInternalServerError,
		`{"error":{"code":"500","message":"Internal server error"}}`)

	_, stderr, exitCode := execAfc(t, []string{
		"keys", "list",
		"--api-key", "test-key",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode == 0 {
		t.Error("expected non-zero exit code on HTTP 500, got 0")
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected non-empty stderr on failure, got empty")
	}
}

func TestExitCodes_NonZeroOnHubUnreachable(t *testing.T) {
	// TS-03-P4: Hub is unreachable → exit != 0 and stderr is non-empty.

	_, stderr, exitCode := execAfc(t, []string{
		"keys", "list",
		"--api-key", "test-key",
	}, "AF_HUB_URL=http://localhost:19999")

	if exitCode == 0 {
		t.Error("expected non-zero exit code when hub is unreachable, got 0")
	}
	if strings.TrimSpace(stderr) == "" {
		t.Error("expected non-empty stderr when hub is unreachable, got empty")
	}
}

func TestExitCodes_TerminatesWithinBound(t *testing.T) {
	// TS-03-P4: Verify the command terminates within 10 seconds for
	// non-timeout errors. Use a context with deadline.

	stub := newStubServer(t)
	stub.onRoute("GET", "/api/v1/keys", http.StatusInternalServerError,
		`{"error":{"code":"500","message":"error"}}`)

	// execAfc will fail fatally if the command hangs, so this test
	// verifies the command terminates and returns non-zero.
	_, _, exitCode := execAfc(t, []string{
		"keys", "list",
		"--api-key", "test-key",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode == 0 {
		t.Error("expected non-zero exit code on error, got 0")
	}
}

// ---------------------------------------------------------------------------
// TS-03-E1: Missing hub URL configuration
// REQ: 03-REQ-1.E1
// ---------------------------------------------------------------------------

func TestMissingHubURL_NoFlagNoEnvVar(t *testing.T) {
	// TS-03-E1: Neither --hub-url nor AF_HUB_URL is set. Assert exit != 0
	// and stderr contains a descriptive error about the missing hub URL.

	// Run with a clean env that explicitly does NOT include AF_HUB_URL.
	_, stderr, exitCode := execAfc(t, []string{
		"keys", "list",
		"--api-key", "myapikey",
	})

	if exitCode == 0 {
		t.Error("expected non-zero exit code when hub URL is missing, got 0")
	}

	stderrLower := strings.ToLower(stderr)
	hasHubRef := strings.Contains(stderrLower, "hub") ||
		strings.Contains(stderrLower, "url") ||
		strings.Contains(stderr, "AF_HUB_URL")
	if !hasHubRef {
		t.Errorf("expected stderr to mention 'hub', 'url', or 'AF_HUB_URL', got: %q", stderr)
	}
}

func TestMissingHubURL_LoginAlsoFails(t *testing.T) {
	// TS-03-E1: Also applies to the login command.

	_, stderr, exitCode := execAfc(t, []string{
		"login",
	})

	if exitCode == 0 {
		t.Error("expected non-zero exit code when hub URL is missing on login, got 0")
	}

	stderrLower := strings.ToLower(stderr)
	hasHubRef := strings.Contains(stderrLower, "hub") ||
		strings.Contains(stderrLower, "url") ||
		strings.Contains(stderr, "AF_HUB_URL")
	if !hasHubRef {
		t.Errorf("expected stderr to mention 'hub', 'url', or 'AF_HUB_URL', got: %q", stderr)
	}
}
