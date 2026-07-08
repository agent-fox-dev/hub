//go:build integration

// Package integration contains smoke tests for the afc CLI binary.
// These tests exercise the full CLI binary through end-to-end execution paths
// defined in spec 03 (TS-03-SMOKE-1 through TS-03-SMOKE-4).
//
// Run with: go test -tags integration ./internal/integration/ -count=1 -run TestCLISmoke -v
package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// CLI smoke test helpers
// ---------------------------------------------------------------------------

// cliStubServer is a lightweight HTTP test server that records requests and
// serves canned responses. It supports registering routes by method + path
// and capturing request bodies for assertion.
type cliStubServer struct {
	Server   *httptest.Server
	mu       sync.Mutex
	requests []cliRecordedRequest
	routes   map[string]cliRouteHandler
}

type cliRecordedRequest struct {
	Method string
	Path   string
	Body   string
	Header http.Header
}

type cliRouteHandler struct {
	StatusCode  int
	Body        string
	ContentType string
}

// newCLIStubServer creates and starts a new stub HTTP server.
// The server is automatically cleaned up when the test completes.
func newCLIStubServer(t *testing.T) *cliStubServer {
	t.Helper()

	s := &cliStubServer{
		routes: make(map[string]cliRouteHandler),
	}

	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, cliRecordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   string(bodyBytes),
			Header: r.Header.Clone(),
		})
		s.mu.Unlock()

		key := r.Method + " " + r.URL.Path
		s.mu.Lock()
		handler, ok := s.routes[key]
		s.mu.Unlock()

		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"error":{"code":"404","message":"no stub for %s"}}`, key)
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

// onRoute registers a canned response for a given method and path.
func (s *cliStubServer) onRoute(method, path string, status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[method+" "+path] = cliRouteHandler{StatusCode: status, Body: body}
}

// receivedRequest checks if a request with the given method and path was
// received by the stub server.
func (s *cliStubServer) receivedRequest(method, path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.requests {
		if r.Method == method && r.Path == path {
			return true
		}
	}
	return false
}

// lastRequestFor returns the most recent recorded request matching the
// given method and path, or nil if none found.
func (s *cliStubServer) lastRequestFor(method, path string) *cliRecordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.requests) - 1; i >= 0; i-- {
		if s.requests[i].Method == method && s.requests[i].Path == path {
			return &s.requests[i]
		}
	}
	return nil
}

// requestCount returns the total number of requests received.
func (s *cliStubServer) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// ---------------------------------------------------------------------------
// Binary build and execution helpers
// ---------------------------------------------------------------------------

// afcBinaryPath builds the afc binary and returns its path. The binary is
// built once per test run and cached in a temp directory.
var (
	afcBinaryOnce sync.Once
	afcBinaryVal  string
	afcBinaryErr  error
)

func buildAfcBinary(t *testing.T) string {
	t.Helper()

	afcBinaryOnce.Do(func() {
		root := findCLIRepoRoot(t)
		tmpDir, err := os.MkdirTemp("", "afc-smoke-*")
		if err != nil {
			afcBinaryErr = fmt.Errorf("failed to create temp dir: %w", err)
			return
		}

		afcBinaryVal = filepath.Join(tmpDir, "afc")
		buildCmd := exec.Command("go", "build", "-o", afcBinaryVal, "./cmd/afc")
		buildCmd.Dir = root
		buildCmd.Stderr = os.Stderr
		if err := buildCmd.Run(); err != nil {
			afcBinaryErr = fmt.Errorf("failed to build afc binary: %w", err)
			return
		}
	})

	if afcBinaryErr != nil {
		t.Skipf("could not build afc binary: %v", afcBinaryErr)
	}
	return afcBinaryVal
}

// findCLIRepoRoot walks up from cwd to find the go.mod file.
func findCLIRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("could not get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repository root (go.mod)")
		}
		dir = parent
	}
}

// execCapture runs the afc binary synchronously with the given arguments and
// environment overrides, capturing stdout, stderr, and exit code separately.
func execCapture(t *testing.T, binary string, args []string, env ...string) (stdout, stderr string, exitCode int) {
	t.Helper()

	cmd := exec.Command(binary, args...)

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

// execCaptureAsync starts the afc binary asynchronously and returns the
// command, stdout/stderr pipes, and a done channel.
func execCaptureAsync(t *testing.T, binary string, args []string, env ...string) (cmd *exec.Cmd, stdoutPipe io.ReadCloser, stderrPipe io.ReadCloser, done chan error) {
	t.Helper()

	cmd = exec.Command(binary, args...)

	cleanEnv := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	cleanEnv = append(cleanEnv, env...)
	cmd.Env = cleanEnv

	var err error
	stdoutPipe, err = cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("failed to create stdout pipe: %v", err)
	}
	stderrPipe, err = cmd.StderrPipe()
	if err != nil {
		t.Fatalf("failed to create stderr pipe: %v", err)
	}

	if runtime.GOOS != "windows" {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}

	done = make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start afc: %v", err)
	}

	go func() {
		done <- cmd.Wait()
	}()

	return cmd, stdoutPipe, stderrPipe, done
}

// waitForCompletion waits for an async process to finish and returns
// stdout, stderr, and exit code.
func waitForCompletion(t *testing.T, stdoutPipe, stderrPipe io.ReadCloser, done chan error, timeout time.Duration) (stdout, stderr string, exitCode int) {
	t.Helper()

	stdoutCh := make(chan string, 1)
	stderrCh := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(stdoutPipe)
		stdoutCh <- string(b)
	}()
	go func() {
		b, _ := io.ReadAll(stderrPipe)
		stderrCh <- string(b)
	}()

	select {
	case err := <-done:
		stdout = <-stdoutCh
		stderr = <-stderrCh
		exitCode = 0
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if err != nil {
			t.Fatalf("unexpected error waiting for afc: %v", err)
		}
	case <-time.After(timeout):
		t.Fatalf("afc process did not exit within %v", timeout)
	}
	return
}

// getCallbackPort scans stderr output from an async login process to find
// the callback server port. Returns 0 if no port is found within the deadline.
func getCallbackPort(t *testing.T, stderrPipe io.ReadCloser, deadline time.Duration) (port int, stderrBuf []byte) {
	t.Helper()

	re := regexp.MustCompile(`http://(?:localhost|127\.0\.0\.1):(\d+)`)
	dl := time.Now().Add(deadline)
	readBuf := make([]byte, 256)
	stderrBuf = make([]byte, 0, 4096)

	for time.Now().Before(dl) && port == 0 {
		n, _ := stderrPipe.Read(readBuf)
		if n > 0 {
			stderrBuf = append(stderrBuf, readBuf[:n]...)
		}
		matches := re.FindStringSubmatch(string(stderrBuf))
		if len(matches) > 1 {
			p, err := strconv.Atoi(matches[1])
			if err == nil && p > 0 {
				port = p
			}
		}
		if port == 0 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	return port, stderrBuf
}

// cliIsPortListening checks if a TCP port is currently accepting connections.
func cliIsPortListening(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// isValidJSONCLI checks if a string is valid JSON (object or array).
func isValidJSONCLI(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	var js json.RawMessage
	return json.Unmarshal([]byte(s), &js) == nil
}

// stubCLIProviderList returns a JSON provider list in the spec-02 object
// format: [{name, authorize_url}].
func stubCLIProviderList(providers ...string) string {
	type provider struct {
		Name         string `json:"name"`
		AuthorizeURL string `json:"authorize_url"`
	}
	var list []provider
	for _, p := range providers {
		list = append(list, provider{
			Name:         p,
			AuthorizeURL: fmt.Sprintf("https://%s.example.com/login/oauth/authorize", p),
		})
	}
	b, _ := json.Marshal(list)
	return string(b)
}

// ---------------------------------------------------------------------------
// TS-03-SMOKE-1: End-to-end smoke test for the successful OAuth login flow
// Execution Path: 03-PATH-1
// ---------------------------------------------------------------------------

func TestCLISmoke_OAuthLoginFlow(t *testing.T) {
	// TS-03-SMOKE-1: Full OAuth login flow against a stub hub server.
	//
	// Steps:
	// 1. afc issues GET /api/v1/auth/providers and receives provider list
	// 2. afc starts a local callback server on a randomly chosen available port
	// 3. afc invokes the browser-open command (mocked via missing binary — OK)
	// 4. Simulated OAuth redirect delivers ?code=test-auth-code to callback
	// 5. afc issues POST /api/v1/auth/callback with the authorization code
	// 6. afc prints the returned user object as valid JSON to stdout
	// 7. afc exits with exit code 0
	// 8. The callback server port is no longer listening after exit

	binary := buildAfcBinary(t)

	stub := newCLIStubServer(t)
	stub.onRoute("GET", "/api/v1/auth/providers", http.StatusOK, stubCLIProviderList("github"))
	userObj := `{"id":"u1","email":"op@example.com","username":"testuser"}`
	stub.onRoute("POST", "/api/v1/auth/callback", http.StatusOK, userObj)

	cmd, stdoutPipe, stderrPipe, done := execCaptureAsync(t, binary, []string{
		"login", "--provider", "github",
	}, "AF_HUB_URL="+stub.Server.URL, "AFC_CALLBACK_TIMEOUT=10", "AFC_SKIP_BROWSER=1")

	defer func() {
		if cmd.Process != nil {
			cmd.Process.Signal(syscall.SIGTERM)
		}
	}()

	// Step 2: Find the callback port from stderr.
	callbackPort, stderrBuf := getCallbackPort(t, stderrPipe, 5*time.Second)
	if callbackPort == 0 {
		stdout, stderrRest, exitCode := waitForCompletion(t, stdoutPipe, stderrPipe, done, 10*time.Second)
		t.Fatalf("could not find callback port. Exit=%d stdout=%q stderr=%q",
			exitCode, stdout, string(stderrBuf)+stderrRest)
	}

	// Step 1: Assert GET /api/v1/auth/providers was called.
	if !stub.receivedRequest("GET", "/api/v1/auth/providers") {
		t.Error("expected GET /api/v1/auth/providers to be called")
	}

	// Step 4: Simulate OAuth redirect.
	callbackURL := fmt.Sprintf("http://localhost:%d/callback?code=test-auth-code", callbackPort)
	resp, err := http.Get(callbackURL)
	if err != nil {
		t.Fatalf("failed to send OAuth callback: %v", err)
	}
	resp.Body.Close()

	// Wait for the process to finish.
	stdout, _, exitCode := waitForCompletion(t, stdoutPipe, stderrPipe, done, 10*time.Second)

	// Step 5: Assert POST /api/v1/auth/callback was called.
	if !stub.receivedRequest("POST", "/api/v1/auth/callback") {
		t.Error("expected POST /api/v1/auth/callback to be called")
	}

	// Step 6: Assert stdout is valid JSON containing user object.
	if !isValidJSONCLI(stdout) {
		t.Errorf("expected stdout to be valid JSON, got: %q", stdout)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &parsed); err == nil {
		if parsed["id"] != "u1" {
			t.Errorf("expected user id 'u1', got: %v", parsed["id"])
		}
	}

	// Step 7: Assert exit code 0.
	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}

	// Step 8: Assert callback port is released.
	time.Sleep(200 * time.Millisecond)
	if cliIsPortListening(callbackPort) {
		t.Errorf("callback port %d is still listening after exit", callbackPort)
	}
}

// ---------------------------------------------------------------------------
// TS-03-SMOKE-2: End-to-end smoke test: create API key then list keys
// Execution Path: 03-PATH-2
// ---------------------------------------------------------------------------

func TestCLISmoke_CreateAndListKeys(t *testing.T) {
	// TS-03-SMOKE-2: Create an API key then list all keys.
	//
	// Steps:
	// 1. afc keys create sends authenticated POST with workspace_id, label, expires
	// 2. stdout contains valid JSON key object including plaintext secret
	// 3. keys create exits with exit code 0
	// 4. afc keys list sends authenticated GET
	// 5. stdout is valid JSON array with required fields
	// 6. keys list exits with exit code 0

	binary := buildAfcBinary(t)

	stub := newCLIStubServer(t)
	keyObj := `{"key":"af_k1_smoke-secret-123","key_id":"k1","workspace_id":"ws-123","label":"ci-bot","expires_at":"2026-08-01T00:00:00Z","role":"member","created_at":"2026-01-01T00:00:00Z"}`
	stub.onRoute("POST", "/api/v1/keys", http.StatusCreated, keyObj)

	// Step 1-3: keys create
	stdout, _, exitCode := execCapture(t, binary, []string{
		"keys", "create",
		"--workspace", "ws-123",
		"--label", "ci-bot",
		"--expires", "30",
		"--api-key", "testkey",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode != 0 {
		t.Errorf("keys create: expected exit code 0, got %d", exitCode)
	}

	if !isValidJSONCLI(stdout) {
		t.Errorf("keys create: expected valid JSON on stdout, got: %q", stdout)
	}

	// Assert plaintext secret is in the output.
	if !strings.Contains(stdout, "smoke-secret-123") {
		t.Errorf("keys create: expected stdout to contain plaintext secret 'smoke-secret-123', got: %q", stdout)
	}

	// Assert authenticated request was sent with correct fields.
	req := stub.lastRequestFor("POST", "/api/v1/keys")
	if req == nil {
		t.Fatal("keys create: expected POST /api/v1/keys to be called")
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(req.Body), &body); err == nil {
		if ws, ok := body["workspace_id"]; !ok || ws != "ws-123" {
			t.Errorf("keys create: expected workspace_id='ws-123' in body, got: %v", ws)
		}
		if label, ok := body["label"]; !ok || label != "ci-bot" {
			t.Errorf("keys create: expected label='ci-bot' in body, got: %v", label)
		}
	}

	// Step 4-6: keys list
	keysJSON := `[
		{"key_id":"k1","label":"ci-bot","workspace_id":"ws-123","expires_at":"2026-08-01T00:00:00Z","created_at":"2026-01-01T00:00:00Z","revoked":false},
		{"key_id":"k2","label":"deploy","workspace_id":"ws-456","expires_at":null,"created_at":"2026-02-01T00:00:00Z","revoked":true}
	]`
	stub.onRoute("GET", "/api/v1/keys", http.StatusOK, keysJSON)

	stdout2, _, exitCode2 := execCapture(t, binary, []string{
		"keys", "list",
		"--api-key", "testkey",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode2 != 0 {
		t.Errorf("keys list: expected exit code 0, got %d", exitCode2)
	}

	if !isValidJSONCLI(stdout2) {
		t.Errorf("keys list: expected valid JSON on stdout, got: %q", stdout2)
	}

	var arr []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout2)), &arr); err != nil {
		t.Fatalf("keys list: failed to parse JSON array: %v", err)
	}

	requiredFields := []string{"key_id", "label", "workspace_id", "expires_at", "created_at", "revoked"}
	for i, item := range arr {
		for _, field := range requiredFields {
			if _, ok := item[field]; !ok {
				t.Errorf("keys list: item[%d] missing required field %q", i, field)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// TS-03-SMOKE-3: End-to-end smoke test: refresh then revoke API key
// Execution Path: 03-PATH-3
// ---------------------------------------------------------------------------

func TestCLISmoke_RefreshThenRevokeKey(t *testing.T) {
	// TS-03-SMOKE-3: Refresh an existing key then revoke it.
	//
	// Steps:
	// 1. afc keys refresh sends authenticated request for key-abc
	// 2. stdout contains valid JSON with new plaintext secret
	// 3. keys refresh exits with exit code 0
	// 4. afc keys revoke sends authenticated revoke request for key-abc
	// 5. stderr contains a confirmation message
	// 6. stdout of revoke is empty
	// 7. keys revoke exits with exit code 0

	binary := buildAfcBinary(t)

	stub := newCLIStubServer(t)
	refreshedKey := `{"key":"af_key-abc_new-refreshed-secret","key_id":"key-abc","workspace_id":"ws-123","expires_at":"2026-08-01T00:00:00Z","created_at":"2026-01-01T00:00:00Z"}`
	stub.onRoute("POST", "/api/v1/keys/key-abc/refresh", http.StatusOK, refreshedKey)

	// Step 1-3: keys refresh
	stdout, _, exitCode := execCapture(t, binary, []string{
		"keys", "refresh", "key-abc",
		"--api-key", "testkey",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode != 0 {
		t.Errorf("keys refresh: expected exit code 0, got %d", exitCode)
	}

	if !isValidJSONCLI(stdout) {
		t.Errorf("keys refresh: expected valid JSON on stdout, got: %q", stdout)
	}

	if !strings.Contains(stdout, "new-refreshed-secret") {
		t.Errorf("keys refresh: expected stdout to contain 'new-refreshed-secret', got: %q", stdout)
	}

	if !stub.receivedRequest("POST", "/api/v1/keys/key-abc/refresh") {
		t.Error("keys refresh: expected POST /api/v1/keys/key-abc/refresh to be called")
	}

	// Step 4-7: keys revoke
	stub.onRoute("DELETE", "/api/v1/keys/key-abc", http.StatusOK, `{"message":"key revoked"}`)

	stdout2, stderr2, exitCode2 := execCapture(t, binary, []string{
		"keys", "revoke", "key-abc",
		"--api-key", "testkey",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode2 != 0 {
		t.Errorf("keys revoke: expected exit code 0, got %d", exitCode2)
	}

	// stderr should contain a confirmation message.
	if strings.TrimSpace(stderr2) == "" {
		t.Error("keys revoke: expected non-empty confirmation on stderr")
	}
	stderrLower := strings.ToLower(stderr2)
	hasConfirmation := strings.Contains(stderrLower, "revok") ||
		strings.Contains(stderrLower, "success") ||
		strings.Contains(stderr2, "key-abc")
	if !hasConfirmation {
		t.Errorf("keys revoke: expected stderr to contain 'revok', 'success', or 'key-abc', got: %q", stderr2)
	}

	// stdout should be empty.
	if strings.TrimSpace(stdout2) != "" {
		t.Errorf("keys revoke: expected empty stdout, got: %q", stdout2)
	}

	if !stub.receivedRequest("DELETE", "/api/v1/keys/key-abc") {
		t.Error("keys revoke: expected DELETE /api/v1/keys/key-abc to be called")
	}
}

// ---------------------------------------------------------------------------
// TS-03-SMOKE-4: End-to-end smoke test: OAuth callback timeout path
// Execution Path: 03-PATH-4
// ---------------------------------------------------------------------------

func TestCLISmoke_OAuthLoginTimeout(t *testing.T) {
	// TS-03-SMOKE-4: Login starts, waits for callback, times out, cleans up,
	// and exits non-zero with a retry suggestion.
	//
	// Steps:
	// 1. afc fetches and validates the provider list
	// 2. afc starts callback server and invokes browser open command
	// 3. afc waits for the callback and times out (short timeout via env var)
	// 4. stderr contains a timeout error message
	// 5. stderr contains a retry suggestion
	// 6. The callback server port is released after timeout
	// 7. Process exits with a non-zero exit code

	binary := buildAfcBinary(t)

	stub := newCLIStubServer(t)
	stub.onRoute("GET", "/api/v1/auth/providers", http.StatusOK, stubCLIProviderList("github"))

	cmd, stdoutPipe, stderrPipe, done := execCaptureAsync(t, binary, []string{
		"login", "--provider", "github",
	}, "AF_HUB_URL="+stub.Server.URL, "AFC_CALLBACK_TIMEOUT=2", "AFC_SKIP_BROWSER=1")

	defer func() {
		if cmd.Process != nil {
			cmd.Process.Signal(syscall.SIGTERM)
		}
	}()

	// Find callback port.
	callbackPort, stderrBuf := getCallbackPort(t, stderrPipe, 5*time.Second)

	// Step 1: Verify providers were fetched.
	if !stub.receivedRequest("GET", "/api/v1/auth/providers") {
		t.Error("expected GET /api/v1/auth/providers to be called")
	}

	// Do NOT send any callback — let it time out.
	_, stderrRemaining, exitCode := waitForCompletion(t, stdoutPipe, stderrPipe, done, 15*time.Second)
	stderr := string(stderrBuf) + stderrRemaining

	// Step 7: Assert non-zero exit code.
	if exitCode == 0 {
		t.Error("expected non-zero exit code on timeout, got 0")
	}

	// Step 4: Assert stderr contains timeout error.
	stderrLower := strings.ToLower(stderr)
	if !strings.Contains(stderrLower, "timeout") && !strings.Contains(stderrLower, "timed out") {
		t.Errorf("expected stderr to contain 'timeout' or 'timed out', got: %q", stderr)
	}

	// Step 5: Assert stderr contains retry suggestion.
	if !strings.Contains(stderrLower, "retry") && !strings.Contains(stderrLower, "again") {
		t.Errorf("expected stderr to contain 'retry' or 'again', got: %q", stderr)
	}

	// Step 6: Assert callback port is released.
	if callbackPort > 0 {
		time.Sleep(200 * time.Millisecond)
		if cliIsPortListening(callbackPort) {
			t.Errorf("callback port %d is still listening after timeout", callbackPort)
		}
	}
}
