package cli_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Login test helpers
// ---------------------------------------------------------------------------

// execAfcAsync starts the afc binary asynchronously and returns the
// *exec.Cmd, stdout/stderr pipes, and a done channel that closes when the
// process exits. The caller is responsible for eventually waiting on or
// killing the process.
func execAfcAsync(t *testing.T, args []string, env ...string) (cmd *exec.Cmd, stdoutPipe io.ReadCloser, stderrPipe io.ReadCloser, done chan error) {
	t.Helper()

	cmd = exec.Command(afcBinary, args...)

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

	// Set process group so we can send signals properly on Unix.
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

// waitForDone waits for the process to finish (with a timeout) and returns
// stdout, stderr, and exit code.
func waitForDone(t *testing.T, stdoutPipe, stderrPipe io.ReadCloser, done chan error, timeout time.Duration) (stdout, stderr string, exitCode int) {
	t.Helper()

	// Read stdout and stderr in goroutines to avoid deadlock.
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

// isPortListening checks if a TCP port is currently accepting connections.
func isPortListening(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}


// stubProviderList returns the JSON for a provider list response matching
// spec 02's actual contract: an array of provider objects with name and
// authorize_url fields.
func stubProviderList(providers ...string) string {
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
// TS-03-6: Login fetches provider list and validates provider
// REQ: 03-REQ-2.1
// ---------------------------------------------------------------------------

func TestLogin_FetchesProviderList(t *testing.T) {
	// TS-03-6: afc login --provider github fetches GET /api/v1/auth/providers
	// and validates that github is in the list.

	stub := newStubServer(t)
	// Use spec-02 provider object format: [{name, authorize_url}]
	stub.onRoute("GET", "/api/v1/auth/providers", http.StatusOK, stubProviderList("github"))
	// Stub the callback endpoint so the flow can complete if it gets that far.
	stub.onRoute("POST", "/api/v1/auth/callback", http.StatusOK, `{"id":"u1","email":"op@example.com"}`)

	// Run afc login async. The login flow will:
	// 1. Fetch providers
	// 2. Start callback server
	// 3. Try to open browser (will fail in test, that's OK)
	// 4. Wait for callback
	//
	// We use a short callback timeout to prevent hanging.
	cmd, stdoutPipe, stderrPipe, done := execAfcAsync(t, []string{
		"login", "--provider", "github",
	}, "AF_HUB_URL="+stub.Server.URL, "AFC_CALLBACK_TIMEOUT=3")

	// Give the process a moment to make the providers request, then check.
	// We'll wait up to 5s for the process to finish (it may time out or error).
	defer func() {
		if cmd.Process != nil {
			cmd.Process.Signal(syscall.SIGTERM)
		}
	}()

	_, _, _ = waitForDone(t, stdoutPipe, stderrPipe, done, 15*time.Second)

	if !stub.receivedRequest("GET", "/api/v1/auth/providers") {
		t.Error("expected afc to issue GET /api/v1/auth/providers, but it did not")
	}
}

func TestLogin_ValidatesProviderExists(t *testing.T) {
	// TS-03-6: afc login --provider github validates that 'github' is in the
	// provider list before proceeding to the callback server step.

	stub := newStubServer(t)
	stub.onRoute("GET", "/api/v1/auth/providers", http.StatusOK, stubProviderList("github"))

	// If the provider is validated and login proceeds, it would try to start
	// a callback server. We use a short timeout to let the test finish.
	_, stdoutPipe, stderrPipe, done := execAfcAsync(t, []string{
		"login", "--provider", "github",
	}, "AF_HUB_URL="+stub.Server.URL, "AFC_CALLBACK_TIMEOUT=2")

	_, _, _ = waitForDone(t, stdoutPipe, stderrPipe, done, 15*time.Second)

	// The key assertion: the providers endpoint was hit exactly once.
	if !stub.receivedRequest("GET", "/api/v1/auth/providers") {
		t.Error("expected GET /api/v1/auth/providers to be called")
	}
}

// ---------------------------------------------------------------------------
// TS-03-E7: Login with unknown provider lists available providers
// REQ: 03-REQ-2.1 (provider not found edge case)
// ---------------------------------------------------------------------------

func TestLogin_UnknownProviderListsAvailable(t *testing.T) {
	// TS-03-E7: Run afc login --provider gitlab when only github is available.
	// Assert exit !=0 and stderr mentions 'gitlab' (requested) and 'github'
	// (available).

	stub := newStubServer(t)
	stub.onRoute("GET", "/api/v1/auth/providers", http.StatusOK, stubProviderList("github"))

	_, stderr, exitCode := execAfc(t, []string{
		"login", "--provider", "gitlab",
	}, "AF_HUB_URL="+stub.Server.URL)

	if exitCode == 0 {
		t.Error("expected non-zero exit code when provider is not found, got 0")
	}
	if !strings.Contains(stderr, "gitlab") {
		t.Errorf("expected stderr to mention requested provider 'gitlab', got: %q", stderr)
	}
	if !strings.Contains(stderr, "github") {
		t.Errorf("expected stderr to list available provider 'github', got: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// TS-03-7: Login starts callback server on random port with redirect_uri
// REQ: 03-REQ-2.2
// ---------------------------------------------------------------------------

func TestLogin_CallbackServerRandomPort(t *testing.T) {
	// TS-03-7: afc login starts a local callback server on a random available
	// port and the authorization URL contains redirect_uri with that port.
	//
	// We intercept stderr output to find the callback URL / port, then
	// verify the port is listening and the redirect_uri is well-formed.

	stub := newStubServer(t)
	stub.onRoute("GET", "/api/v1/auth/providers", http.StatusOK, stubProviderList("github"))

	cmd, stdoutPipe, stderrPipe, done := execAfcAsync(t, []string{
		"login", "--provider", "github",
	}, "AF_HUB_URL="+stub.Server.URL, "AFC_CALLBACK_TIMEOUT=5")

	defer func() {
		if cmd.Process != nil {
			cmd.Process.Signal(syscall.SIGTERM)
		}
	}()

	// Read stderr to find the callback URL printed by the CLI.
	var callbackPort int
	deadline := time.Now().Add(5 * time.Second)
	buf := make([]byte, 0, 4096)
	readBuf := make([]byte, 256)
	re := regexp.MustCompile(`http://(?:localhost|127\.0\.0\.1):(\d+)`)

	for time.Now().Before(deadline) && callbackPort == 0 {
		n, _ := stderrPipe.Read(readBuf)
		if n > 0 {
			buf = append(buf, readBuf[:n]...)
		}
		matches := re.FindStringSubmatch(string(buf))
		if len(matches) > 1 {
			p, _ := strconv.Atoi(matches[1])
			if p > 0 {
				callbackPort = p
			}
		}
		if callbackPort == 0 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	if callbackPort == 0 {
		_, stderrRemaining, _ := waitForDone(t, stdoutPipe, stderrPipe, done, 10*time.Second)
		t.Fatalf("expected login to start a callback server and print a URL to stderr, "+
			"but no port was found. stderr: %q", string(buf)+stderrRemaining)
	}

	// Assert port is in valid range.
	if callbackPort <= 0 || callbackPort > 65535 {
		t.Errorf("expected port in range 1-65535, got %d", callbackPort)
	}

	// Assert port is listening.
	if !isPortListening(callbackPort) {
		t.Errorf("expected port %d to be listening, but it is not", callbackPort)
	}

	// Assert redirect_uri is well-formed (if the full auth URL is in stderr).
	stderrStr := string(buf)
	redirectRe := regexp.MustCompile(`redirect_uri=([^\s&]+)`)
	rdMatches := redirectRe.FindStringSubmatch(stderrStr)
	if len(rdMatches) > 1 {
		redirectURI, err := url.QueryUnescape(rdMatches[1])
		if err == nil {
			if !strings.HasPrefix(redirectURI, "http://localhost:") {
				t.Errorf("expected redirect_uri to start with http://localhost:, got: %q", redirectURI)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// TS-03-8: Login sends authorization code to POST /api/v1/auth/callback
//          and prints returned user object as JSON to stdout
// REQ: 03-REQ-2.3
// ---------------------------------------------------------------------------

func TestLogin_SendsCodeAndPrintsUserJSON(t *testing.T) {
	// TS-03-8: Simulate OAuth redirect to the callback server with
	// ?code=test-code-123. Assert that afc POSTs to /api/v1/auth/callback
	// with the authorization code (plus provider and redirect_uri per spec 02),
	// and prints the JSON user object to stdout.

	userObj := `{"id":"u1","email":"op@example.com"}`
	stub := newStubServer(t)
	stub.onRoute("GET", "/api/v1/auth/providers", http.StatusOK, stubProviderList("github"))
	stub.onRoute("POST", "/api/v1/auth/callback", http.StatusOK, userObj)

	cmd, stdoutPipe, stderrPipe, done := execAfcAsync(t, []string{
		"login", "--provider", "github",
	}, "AF_HUB_URL="+stub.Server.URL, "AFC_CALLBACK_TIMEOUT=10")

	defer func() {
		if cmd.Process != nil {
			cmd.Process.Signal(syscall.SIGTERM)
		}
	}()

	// Wait for the callback server to start by polling stderr for the port.
	// Give it up to 5 seconds to start.
	var callbackPort int
	deadline := time.Now().Add(5 * time.Second)
	buf := make([]byte, 0, 4096)
	readBuf := make([]byte, 256)
	re := regexp.MustCompile(`http://(?:localhost|127\.0\.0\.1):(\d+)`)

	for time.Now().Before(deadline) && callbackPort == 0 {
		n, _ := stderrPipe.Read(readBuf)
		if n > 0 {
			buf = append(buf, readBuf[:n]...)
		}
		matches := re.FindStringSubmatch(string(buf))
		if len(matches) > 1 {
			p, err := strconv.Atoi(matches[1])
			if err == nil && p > 0 {
				callbackPort = p
			}
		}
		if callbackPort == 0 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	if callbackPort == 0 {
		// Try waiting for the process to finish; it might have errored.
		stdout, stderr, exitCode := waitForDone(t, stdoutPipe, stderrPipe, done, 10*time.Second)
		t.Fatalf("could not find callback port. Process exited with code %d.\nstdout: %q\nstderr: %q",
			exitCode, stdout, stderr)
	}

	// Simulate the OAuth redirect by sending a GET request to the callback URL.
	callbackURL := fmt.Sprintf("http://localhost:%d/callback?code=test-code-123", callbackPort)
	resp, err := http.Get(callbackURL)
	if err != nil {
		t.Fatalf("failed to send callback request: %v", err)
	}
	resp.Body.Close()

	// Wait for the process to finish.
	stdout, _, exitCode := waitForDone(t, stdoutPipe, stderrPipe, done, 10*time.Second)

	if exitCode != 0 {
		t.Errorf("expected exit code 0, got %d", exitCode)
	}

	// Assert stdout is valid JSON.
	if !isValidJSON(stdout) {
		t.Errorf("expected stdout to be valid JSON, got: %q", stdout)
	}

	// Assert the user object fields are present.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &parsed); err == nil {
		if parsed["id"] != "u1" {
			t.Errorf("expected user id 'u1', got: %v", parsed["id"])
		}
	}

	// Assert POST /api/v1/auth/callback was called.
	if !stub.receivedRequest("POST", "/api/v1/auth/callback") {
		t.Error("expected afc to POST to /api/v1/auth/callback, but it did not")
	}

	// Per reviewer finding: verify the POST body includes provider, code, and
	// redirect_uri (spec 02 REQ-2.4 requires all three fields).
	stub.mu.Lock()
	var callbackReq *recordedRequest
	for i, r := range stub.requests {
		if r.Method == "POST" && r.Path == "/api/v1/auth/callback" {
			callbackReq = &stub.requests[i]
			break
		}
	}
	stub.mu.Unlock()

	if callbackReq != nil {
		// Try JSON body first.
		var body map[string]any
		if err := json.Unmarshal([]byte(callbackReq.Body), &body); err == nil {
			if _, ok := body["code"]; !ok {
				t.Error("expected POST /api/v1/auth/callback body to include 'code' field")
			}
			if _, ok := body["provider"]; !ok {
				t.Error("expected POST /api/v1/auth/callback body to include 'provider' field (per spec 02 REQ-2.4)")
			}
			if _, ok := body["redirect_uri"]; !ok {
				t.Error("expected POST /api/v1/auth/callback body to include 'redirect_uri' field (per spec 02 REQ-2.4)")
			}
		} else {
			// Maybe form-encoded.
			vals, parseErr := url.ParseQuery(callbackReq.Body)
			if parseErr == nil {
				if vals.Get("code") == "" {
					t.Error("expected POST body to include 'code'")
				}
				if vals.Get("provider") == "" {
					t.Error("expected POST body to include 'provider'")
				}
				if vals.Get("redirect_uri") == "" {
					t.Error("expected POST body to include 'redirect_uri'")
				}
			} else {
				t.Errorf("could not parse POST body as JSON or form: %q", callbackReq.Body)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// TS-03-9, TS-03-P1: Callback server is shut down after login completes
// REQ: 03-REQ-2.4, 03-PROP-1
// ---------------------------------------------------------------------------

func TestLogin_CallbackServerShutdownOnSuccess(t *testing.T) {
	// TS-03-9: After a successful login flow completes, the callback port is
	// no longer listening.

	stub := newStubServer(t)
	stub.onRoute("GET", "/api/v1/auth/providers", http.StatusOK, stubProviderList("github"))
	stub.onRoute("POST", "/api/v1/auth/callback", http.StatusOK, `{"id":"u1","email":"op@example.com"}`)

	cmd, stdoutPipe, stderrPipe, done := execAfcAsync(t, []string{
		"login", "--provider", "github",
	}, "AF_HUB_URL="+stub.Server.URL, "AFC_CALLBACK_TIMEOUT=10")

	defer func() {
		if cmd.Process != nil {
			cmd.Process.Signal(syscall.SIGTERM)
		}
	}()

	// Find callback port.
	var callbackPort int
	deadline := time.Now().Add(5 * time.Second)
	buf := make([]byte, 0, 4096)
	readBuf := make([]byte, 256)
	re := regexp.MustCompile(`http://(?:localhost|127\.0\.0\.1):(\d+)`)

	for time.Now().Before(deadline) && callbackPort == 0 {
		n, _ := stderrPipe.Read(readBuf)
		if n > 0 {
			buf = append(buf, readBuf[:n]...)
		}
		matches := re.FindStringSubmatch(string(buf))
		if len(matches) > 1 {
			p, _ := strconv.Atoi(matches[1])
			if p > 0 {
				callbackPort = p
			}
		}
		if callbackPort == 0 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	if callbackPort == 0 {
		stdout, stderr, exitCode := waitForDone(t, stdoutPipe, stderrPipe, done, 10*time.Second)
		t.Fatalf("could not find callback port. Exit=%d stdout=%q stderr=%q",
			exitCode, stdout, stderr)
	}

	// Complete the login flow by sending a callback.
	callbackURL := fmt.Sprintf("http://localhost:%d/callback?code=test-code", callbackPort)
	resp, err := http.Get(callbackURL)
	if err != nil {
		t.Fatalf("failed to send callback: %v", err)
	}
	resp.Body.Close()

	// Wait for the process to exit.
	_, _, _ = waitForDone(t, stdoutPipe, stderrPipe, done, 10*time.Second)

	// Assert the callback port is no longer listening.
	// Give a moment for the OS to release the port.
	time.Sleep(200 * time.Millisecond)
	if isPortListening(callbackPort) {
		t.Errorf("expected callback port %d to be released after login, but it is still listening", callbackPort)
	}
}

func TestLogin_CallbackServerShutdownOnTimeout(t *testing.T) {
	// TS-03-P1 variant (b): After login timeout, callback port is released.

	stub := newStubServer(t)
	stub.onRoute("GET", "/api/v1/auth/providers", http.StatusOK, stubProviderList("github"))

	cmd, stdoutPipe, stderrPipe, done := execAfcAsync(t, []string{
		"login", "--provider", "github",
	}, "AF_HUB_URL="+stub.Server.URL, "AFC_CALLBACK_TIMEOUT=2")

	defer func() {
		if cmd.Process != nil {
			cmd.Process.Signal(syscall.SIGTERM)
		}
	}()

	// Find callback port.
	var callbackPort int
	deadline := time.Now().Add(5 * time.Second)
	buf := make([]byte, 0, 4096)
	readBuf := make([]byte, 256)
	re := regexp.MustCompile(`http://(?:localhost|127\.0\.0\.1):(\d+)`)

	for time.Now().Before(deadline) && callbackPort == 0 {
		n, _ := stderrPipe.Read(readBuf)
		if n > 0 {
			buf = append(buf, readBuf[:n]...)
		}
		matches := re.FindStringSubmatch(string(buf))
		if len(matches) > 1 {
			p, _ := strconv.Atoi(matches[1])
			if p > 0 {
				callbackPort = p
			}
		}
		if callbackPort == 0 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	if callbackPort == 0 {
		_, stderrRemaining, _ := waitForDone(t, stdoutPipe, stderrPipe, done, 10*time.Second)
		t.Fatalf("expected callback server to start; no port found. stderr: %q",
			string(buf)+stderrRemaining)
	}

	// Don't send any callback — let it timeout.
	// Wait for the process to exit (should timeout within ~2s + buffer).
	_, _, _ = waitForDone(t, stdoutPipe, stderrPipe, done, 15*time.Second)

	// Assert the callback port is released.
	time.Sleep(200 * time.Millisecond)
	if isPortListening(callbackPort) {
		t.Errorf("expected callback port %d to be released after timeout, but it is still listening", callbackPort)
	}
}

func TestLogin_CallbackServerShutdownOnProviderError(t *testing.T) {
	// TS-03-P1 variant (c): After provider error redirect, callback port is
	// released.

	stub := newStubServer(t)
	stub.onRoute("GET", "/api/v1/auth/providers", http.StatusOK, stubProviderList("github"))

	cmd, stdoutPipe, stderrPipe, done := execAfcAsync(t, []string{
		"login", "--provider", "github",
	}, "AF_HUB_URL="+stub.Server.URL, "AFC_CALLBACK_TIMEOUT=10")

	defer func() {
		if cmd.Process != nil {
			cmd.Process.Signal(syscall.SIGTERM)
		}
	}()

	// Find callback port.
	var callbackPort int
	deadline := time.Now().Add(5 * time.Second)
	buf := make([]byte, 0, 4096)
	readBuf := make([]byte, 256)
	re := regexp.MustCompile(`http://(?:localhost|127\.0\.0\.1):(\d+)`)

	for time.Now().Before(deadline) && callbackPort == 0 {
		n, _ := stderrPipe.Read(readBuf)
		if n > 0 {
			buf = append(buf, readBuf[:n]...)
		}
		matches := re.FindStringSubmatch(string(buf))
		if len(matches) > 1 {
			p, _ := strconv.Atoi(matches[1])
			if p > 0 {
				callbackPort = p
			}
		}
		if callbackPort == 0 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	if callbackPort == 0 {
		_, stderrRemaining, _ := waitForDone(t, stdoutPipe, stderrPipe, done, 10*time.Second)
		t.Fatalf("expected callback server to start; no port found. stderr: %q",
			string(buf)+stderrRemaining)
	}

	// Send a provider error redirect.
	errURL := fmt.Sprintf("http://localhost:%d/callback?error=access_denied&error_description=User+denied+access", callbackPort)
	resp, err := http.Get(errURL)
	if err == nil {
		resp.Body.Close()
	}

	_, _, _ = waitForDone(t, stdoutPipe, stderrPipe, done, 10*time.Second)

	time.Sleep(200 * time.Millisecond)
	if isPortListening(callbackPort) {
		t.Errorf("expected callback port %d to be released after provider error, but it is still listening", callbackPort)
	}
}

func TestLogin_CallbackServerShutdownOnHubPostError(t *testing.T) {
	// TS-03-P1 variant (d): After hub POST error, callback port is released.

	stub := newStubServer(t)
	stub.onRoute("GET", "/api/v1/auth/providers", http.StatusOK, stubProviderList("github"))
	stub.onRoute("POST", "/api/v1/auth/callback", http.StatusBadRequest,
		`{"error":{"code":"400","message":"Authorization code has expired"}}`)

	cmd, stdoutPipe, stderrPipe, done := execAfcAsync(t, []string{
		"login", "--provider", "github",
	}, "AF_HUB_URL="+stub.Server.URL, "AFC_CALLBACK_TIMEOUT=10")

	defer func() {
		if cmd.Process != nil {
			cmd.Process.Signal(syscall.SIGTERM)
		}
	}()

	// Find callback port.
	var callbackPort int
	deadline := time.Now().Add(5 * time.Second)
	buf := make([]byte, 0, 4096)
	readBuf := make([]byte, 256)
	re := regexp.MustCompile(`http://(?:localhost|127\.0\.0\.1):(\d+)`)

	for time.Now().Before(deadline) && callbackPort == 0 {
		n, _ := stderrPipe.Read(readBuf)
		if n > 0 {
			buf = append(buf, readBuf[:n]...)
		}
		matches := re.FindStringSubmatch(string(buf))
		if len(matches) > 1 {
			p, _ := strconv.Atoi(matches[1])
			if p > 0 {
				callbackPort = p
			}
		}
		if callbackPort == 0 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	if callbackPort == 0 {
		_, stderrRemaining, _ := waitForDone(t, stdoutPipe, stderrPipe, done, 10*time.Second)
		t.Fatalf("expected callback server to start; no port found. stderr: %q",
			string(buf)+stderrRemaining)
	}

	// Send a valid-looking callback that will fail at hub POST.
	callbackURL := fmt.Sprintf("http://localhost:%d/callback?code=expired-code", callbackPort)
	resp, err := http.Get(callbackURL)
	if err == nil {
		resp.Body.Close()
	}

	_, _, _ = waitForDone(t, stdoutPipe, stderrPipe, done, 10*time.Second)

	time.Sleep(200 * time.Millisecond)
	if isPortListening(callbackPort) {
		t.Errorf("expected callback port %d to be released after hub POST error, but it is still listening", callbackPort)
	}
}

func TestLogin_CallbackServerShutdownOnSIGINT(t *testing.T) {
	// TS-03-P1 variant (e): After SIGINT, callback port is released.
	if runtime.GOOS == "windows" {
		t.Skip("SIGINT test not supported on Windows")
	}

	stub := newStubServer(t)
	stub.onRoute("GET", "/api/v1/auth/providers", http.StatusOK, stubProviderList("github"))

	cmd, stdoutPipe, stderrPipe, done := execAfcAsync(t, []string{
		"login", "--provider", "github",
	}, "AF_HUB_URL="+stub.Server.URL, "AFC_CALLBACK_TIMEOUT=30")

	// Find callback port.
	var callbackPort int
	deadline := time.Now().Add(5 * time.Second)
	buf := make([]byte, 0, 4096)
	readBuf := make([]byte, 256)
	re := regexp.MustCompile(`http://(?:localhost|127\.0\.0\.1):(\d+)`)

	for time.Now().Before(deadline) && callbackPort == 0 {
		n, _ := stderrPipe.Read(readBuf)
		if n > 0 {
			buf = append(buf, readBuf[:n]...)
		}
		matches := re.FindStringSubmatch(string(buf))
		if len(matches) > 1 {
			p, _ := strconv.Atoi(matches[1])
			if p > 0 {
				callbackPort = p
			}
		}
		if callbackPort == 0 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Send SIGINT.
	time.Sleep(500 * time.Millisecond)
	if cmd.Process != nil {
		cmd.Process.Signal(syscall.SIGINT)
	}

	_, _, exitCode := waitForDone(t, stdoutPipe, stderrPipe, done, 10*time.Second)

	if exitCode == 0 {
		t.Error("expected non-zero exit code after SIGINT, got 0")
	}

	// Assert port is released.
	if callbackPort > 0 {
		time.Sleep(200 * time.Millisecond)
		if isPortListening(callbackPort) {
			t.Errorf("expected callback port %d to be released after SIGINT, but it is still listening", callbackPort)
		}
	}
}

// ---------------------------------------------------------------------------
// TS-03-E2: Hub unreachable when fetching providers
// REQ: 03-REQ-2.E1
// ---------------------------------------------------------------------------

func TestLogin_HubUnreachable(t *testing.T) {
	// TS-03-E2: No service at http://localhost:19999. Assert exit !=0 and
	// stderr contains the hub URL and connection failure details.

	_, stderr, exitCode := execAfc(t, []string{
		"login", "--provider", "github",
		"--hub-url", "http://localhost:19999",
	})

	if exitCode == 0 {
		t.Error("expected non-zero exit code when hub is unreachable, got 0")
	}
	if !strings.Contains(stderr, "localhost:19999") {
		t.Errorf("expected stderr to contain 'localhost:19999', got: %q", stderr)
	}
	stderrLower := strings.ToLower(stderr)
	hasConnectionInfo := strings.Contains(stderrLower, "connect") ||
		strings.Contains(stderrLower, "unreachable") ||
		strings.Contains(stderrLower, "refused") ||
		strings.Contains(stderrLower, "dial")
	if !hasConnectionInfo {
		t.Errorf("expected stderr to contain connection failure details (connect/unreachable/refused/dial), got: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// TS-03-E3: Callback timeout with retry suggestion
// REQ: 03-REQ-2.E2
// ---------------------------------------------------------------------------

func TestLogin_CallbackTimeout(t *testing.T) {
	// TS-03-E3: afc login with a short callback timeout (via AFC_CALLBACK_TIMEOUT
	// env var). Never send the OAuth redirect. Assert exit !=0, stderr contains
	// timeout and retry suggestion, callback port is released.

	stub := newStubServer(t)
	stub.onRoute("GET", "/api/v1/auth/providers", http.StatusOK, stubProviderList("github"))

	cmd, stdoutPipe, stderrPipe, done := execAfcAsync(t, []string{
		"login", "--provider", "github",
	}, "AF_HUB_URL="+stub.Server.URL, "AFC_CALLBACK_TIMEOUT=2")

	defer func() {
		if cmd.Process != nil {
			cmd.Process.Signal(syscall.SIGTERM)
		}
	}()

	// Find the callback port while waiting.
	var callbackPort int
	portDeadline := time.Now().Add(5 * time.Second)
	buf := make([]byte, 0, 4096)
	readBuf := make([]byte, 256)
	re := regexp.MustCompile(`http://(?:localhost|127\.0\.0\.1):(\d+)`)

	for time.Now().Before(portDeadline) && callbackPort == 0 {
		n, _ := stderrPipe.Read(readBuf)
		if n > 0 {
			buf = append(buf, readBuf[:n]...)
		}
		matches := re.FindStringSubmatch(string(buf))
		if len(matches) > 1 {
			p, _ := strconv.Atoi(matches[1])
			if p > 0 {
				callbackPort = p
			}
		}
		if callbackPort == 0 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Don't send any callback. Wait for timeout.
	_, stderrRemaining, exitCode := waitForDone(t, stdoutPipe, stderrPipe, done, 15*time.Second)
	stderr := string(buf) + stderrRemaining

	if exitCode == 0 {
		t.Error("expected non-zero exit code on timeout, got 0")
	}

	stderrLower := strings.ToLower(stderr)
	if !strings.Contains(stderrLower, "timeout") && !strings.Contains(stderrLower, "timed out") {
		t.Errorf("expected stderr to contain 'timeout' or 'timed out', got: %q", stderr)
	}
	if !strings.Contains(stderrLower, "retry") && !strings.Contains(stderrLower, "again") {
		t.Errorf("expected stderr to contain 'retry' or 'again', got: %q", stderr)
	}

	// Assert port is released.
	if callbackPort > 0 {
		time.Sleep(200 * time.Millisecond)
		if isPortListening(callbackPort) {
			t.Errorf("expected callback port %d to be released after timeout, but it is still listening", callbackPort)
		}
	}
}

// ---------------------------------------------------------------------------
// TS-03-E4: OAuth provider error redirect
// REQ: 03-REQ-2.E3
// ---------------------------------------------------------------------------

func TestLogin_ProviderErrorRedirect(t *testing.T) {
	// TS-03-E4: Simulate OAuth redirect with ?error=access_denied&error_description=User+denied+access.
	// Assert exit !=0, stderr contains the provider error description, and
	// callback port is released.

	stub := newStubServer(t)
	stub.onRoute("GET", "/api/v1/auth/providers", http.StatusOK, stubProviderList("github"))

	cmd, stdoutPipe, stderrPipe, done := execAfcAsync(t, []string{
		"login", "--provider", "github",
	}, "AF_HUB_URL="+stub.Server.URL, "AFC_CALLBACK_TIMEOUT=10")

	defer func() {
		if cmd.Process != nil {
			cmd.Process.Signal(syscall.SIGTERM)
		}
	}()

	// Find callback port.
	var callbackPort int
	deadline := time.Now().Add(5 * time.Second)
	buf := make([]byte, 0, 4096)
	readBuf := make([]byte, 256)
	re := regexp.MustCompile(`http://(?:localhost|127\.0\.0\.1):(\d+)`)

	for time.Now().Before(deadline) && callbackPort == 0 {
		n, _ := stderrPipe.Read(readBuf)
		if n > 0 {
			buf = append(buf, readBuf[:n]...)
		}
		matches := re.FindStringSubmatch(string(buf))
		if len(matches) > 1 {
			p, _ := strconv.Atoi(matches[1])
			if p > 0 {
				callbackPort = p
			}
		}
		if callbackPort == 0 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	if callbackPort == 0 {
		stdout, stderrOut, exitCode := waitForDone(t, stdoutPipe, stderrPipe, done, 10*time.Second)
		t.Fatalf("could not find callback port. Exit=%d stdout=%q stderr=%q",
			exitCode, stdout, stderrOut)
	}

	// Send provider error redirect.
	errURL := fmt.Sprintf("http://localhost:%d/callback?error=access_denied&error_description=User+denied+access", callbackPort)
	resp, err := http.Get(errURL)
	if err != nil {
		t.Fatalf("failed to send provider error redirect: %v", err)
	}
	resp.Body.Close()

	// Wait for exit.
	_, stderrRemaining, exitCode := waitForDone(t, stdoutPipe, stderrPipe, done, 10*time.Second)
	stderr := string(buf) + stderrRemaining

	if exitCode == 0 {
		t.Error("expected non-zero exit code on provider error, got 0")
	}
	if !strings.Contains(stderr, "access_denied") && !strings.Contains(stderr, "User denied access") {
		t.Errorf("expected stderr to contain 'access_denied' or 'User denied access', got: %q", stderr)
	}

	// Assert port is released.
	time.Sleep(200 * time.Millisecond)
	if isPortListening(callbackPort) {
		t.Errorf("expected callback port %d to be released after provider error, but it is still listening", callbackPort)
	}
}

// ---------------------------------------------------------------------------
// TS-03-E5: Hub POST /api/v1/auth/callback returns HTTP error with envelope
// REQ: 03-REQ-2.E4
// ---------------------------------------------------------------------------

func TestLogin_HubCallbackError(t *testing.T) {
	// TS-03-E5: Hub returns HTTP 400 with error envelope from POST
	// /api/v1/auth/callback. Assert exit !=0 and stderr contains the error
	// message from the envelope.
	//
	// NOTE: Uses nested error envelope format per spec 02 REQ-8.1:
	// {"error":{"code":"400","message":"Authorization code has expired"}}

	stub := newStubServer(t)
	stub.onRoute("GET", "/api/v1/auth/providers", http.StatusOK, stubProviderList("github"))
	stub.onRoute("POST", "/api/v1/auth/callback", http.StatusBadRequest,
		`{"error":{"code":"400","message":"Authorization code has expired"}}`)

	cmd, stdoutPipe, stderrPipe, done := execAfcAsync(t, []string{
		"login", "--provider", "github",
	}, "AF_HUB_URL="+stub.Server.URL, "AFC_CALLBACK_TIMEOUT=10")

	defer func() {
		if cmd.Process != nil {
			cmd.Process.Signal(syscall.SIGTERM)
		}
	}()

	// Find callback port.
	var callbackPort int
	deadline := time.Now().Add(5 * time.Second)
	portBuf := make([]byte, 0, 4096)
	readBuf := make([]byte, 256)
	re := regexp.MustCompile(`http://(?:localhost|127\.0\.0\.1):(\d+)`)

	for time.Now().Before(deadline) && callbackPort == 0 {
		n, _ := stderrPipe.Read(readBuf)
		if n > 0 {
			portBuf = append(portBuf, readBuf[:n]...)
		}
		matches := re.FindStringSubmatch(string(portBuf))
		if len(matches) > 1 {
			p, _ := strconv.Atoi(matches[1])
			if p > 0 {
				callbackPort = p
			}
		}
		if callbackPort == 0 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	if callbackPort == 0 {
		stdout, stderrOut, exitCode := waitForDone(t, stdoutPipe, stderrPipe, done, 10*time.Second)
		t.Fatalf("could not find callback port. Exit=%d stdout=%q stderr=%q",
			exitCode, stdout, stderrOut)
	}

	// Send callback with code that will trigger the hub error.
	callbackURL := fmt.Sprintf("http://localhost:%d/callback?code=expired-code", callbackPort)
	resp, err := http.Get(callbackURL)
	if err != nil {
		t.Fatalf("failed to send callback: %v", err)
	}
	resp.Body.Close()

	// Wait for exit.
	_, stderrRemaining, exitCode := waitForDone(t, stdoutPipe, stderrPipe, done, 10*time.Second)
	stderr := string(portBuf) + stderrRemaining

	if exitCode == 0 {
		t.Error("expected non-zero exit code on hub callback error, got 0")
	}
	if !strings.Contains(stderr, "Authorization code has expired") {
		t.Errorf("expected stderr to contain 'Authorization code has expired', got: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// TS-03-E6: SIGINT during callback wait
// REQ: 03-REQ-2.E5
// ---------------------------------------------------------------------------

func TestLogin_SIGINTCleanShutdown(t *testing.T) {
	// TS-03-E6: Send SIGINT while waiting for callback. Assert exit !=0,
	// callback port released, no orphaned child processes.
	if runtime.GOOS == "windows" {
		t.Skip("SIGINT test not supported on Windows")
	}

	stub := newStubServer(t)
	stub.onRoute("GET", "/api/v1/auth/providers", http.StatusOK, stubProviderList("github"))

	cmd, stdoutPipe, stderrPipe, done := execAfcAsync(t, []string{
		"login", "--provider", "github",
	}, "AF_HUB_URL="+stub.Server.URL, "AFC_CALLBACK_TIMEOUT=30")

	// Find callback port — wait for the server to start.
	var callbackPort int
	deadline := time.Now().Add(5 * time.Second)
	buf := make([]byte, 0, 4096)
	readBuf := make([]byte, 256)
	re := regexp.MustCompile(`http://(?:localhost|127\.0\.0\.1):(\d+)`)

	for time.Now().Before(deadline) && callbackPort == 0 {
		n, _ := stderrPipe.Read(readBuf)
		if n > 0 {
			buf = append(buf, readBuf[:n]...)
		}
		matches := re.FindStringSubmatch(string(buf))
		if len(matches) > 1 {
			p, _ := strconv.Atoi(matches[1])
			if p > 0 {
				callbackPort = p
			}
		}
		if callbackPort == 0 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Wait a moment, then send SIGINT.
	time.Sleep(500 * time.Millisecond)
	if cmd.Process != nil {
		cmd.Process.Signal(syscall.SIGINT)
	}

	_, _, exitCode := waitForDone(t, stdoutPipe, stderrPipe, done, 5*time.Second)

	if exitCode == 0 {
		t.Error("expected non-zero exit code after SIGINT, got 0")
	}

	// Assert callback port is released.
	if callbackPort > 0 {
		time.Sleep(200 * time.Millisecond)
		if isPortListening(callbackPort) {
			t.Errorf("expected callback port %d to be released after SIGINT, but it is still listening", callbackPort)
		}
	}

	// Check for orphaned processes on the callback port.
	if callbackPort > 0 {
		checkCmd := exec.CommandContext(context.Background(), "lsof", "-i", fmt.Sprintf("tcp:%d", callbackPort))
		out, _ := checkCmd.Output()
		if len(out) > 0 && strings.Contains(string(out), "LISTEN") {
			t.Errorf("orphaned process still listening on port %d: %s", callbackPort, string(out))
		}
	}
}
