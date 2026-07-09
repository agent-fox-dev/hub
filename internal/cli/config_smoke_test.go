package cli_test

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/agent-fox/af-hub/internal/cliconfig"
)

// ---------------------------------------------------------------------------
// Helpers for config smoke tests
// ---------------------------------------------------------------------------

// execAfcWithHome runs the afc binary with HOME set to the given directory.
// Other environment overrides can be provided in extraEnv.
func execAfcWithHome(t *testing.T, homeDir string, args []string, extraEnv ...string) (stdout, stderr string, exitCode int) {
	t.Helper()

	cmd := exec.Command(afcBinary, args...)

	cmdEnv := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + homeDir,
	}
	for _, e := range extraEnv {
		if !strings.HasPrefix(e, "HOME=") {
			cmdEnv = append(cmdEnv, e)
		}
	}
	cmd.Env = cmdEnv

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

// execAfcAsyncWithHome starts the afc binary asynchronously with HOME set
// to the given directory. Returns the command, stdout/stderr pipes, and a
// done channel.
func execAfcAsyncWithHome(t *testing.T, homeDir string, args []string, extraEnv ...string) (*exec.Cmd, io.ReadCloser, io.ReadCloser, chan error) {
	t.Helper()

	cmd := exec.Command(afcBinary, args...)

	cmdEnv := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + homeDir,
	}
	for _, e := range extraEnv {
		if !strings.HasPrefix(e, "HOME=") {
			cmdEnv = append(cmdEnv, e)
		}
	}
	cmd.Env = cmdEnv

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}

	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start afc: %v", err)
	}
	go func() {
		done <- cmd.Wait()
	}()

	return cmd, stdoutPipe, stderrPipe, done
}

// waitForDoneCfg waits for an async process to complete and returns
// stdout, stderr, and exit code.
func waitForDoneCfg(t *testing.T, stdoutPipe, stderrPipe io.ReadCloser, done chan error, timeout time.Duration) (stdout, stderr string, exitCode int) {
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
			t.Fatalf("error waiting for process: %v", err)
		}
	case <-time.After(timeout):
		t.Fatal("process did not exit within timeout")
	}
	return
}

// detectCallbackPortCfg reads stderr from an async process to find the
// callback server port. Returns 0 if no port is found.
func detectCallbackPortCfg(t *testing.T, stderrPipe io.ReadCloser, timeout time.Duration) (port int, stderrBuf []byte) {
	t.Helper()

	re := regexp.MustCompile(`http://(?:localhost|127\.0\.0\.1):(\d+)`)
	dl := time.Now().Add(timeout)
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

// writeTestConfig creates $HOME/.af/config.toml with the given content.
func writeTestConfig(t *testing.T, homeDir, content string) {
	t.Helper()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(afDir, "config.toml"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

// loadConfigFromHome reads and parses $HOME/.af/config.toml into a Config struct.
func loadConfigFromHome(t *testing.T, homeDir string) *cliconfig.Config {
	t.Helper()
	configPath := filepath.Join(homeDir, ".af", "config.toml")
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config.toml: %v", err)
	}
	var cfg cliconfig.Config
	if _, err := toml.Decode(string(content), &cfg); err != nil {
		t.Fatalf("failed to decode config.toml: %v", err)
	}
	return &cfg
}

// ---------------------------------------------------------------------------
// TS-05-SMOKE-1: First-time user: login then run authenticated command
// Execution Path: 05-PATH-1
// REQ: 05-REQ-1.1, 05-REQ-5.1, 05-REQ-3.1, 05-REQ-3.2
// ---------------------------------------------------------------------------

func TestSpec05Smoke_FirstTimeLoginAndAuthenticatedCommand(t *testing.T) {
	// TS-05-SMOKE-1: From a clean temp home dir (no .af/ directory):
	// 1. Run afc login → config file auto-created, credentials stored
	// 2. Run keys list with no flags → uses config file credentials
	//
	// Expected (when implemented):
	// - $HOME/.af/ created with 0700
	// - $HOME/.af/config.toml created with 0600
	// - Config contains [keys._login] with key_id, token, label="login"
	// - Config has api_key="_login" and hub_url set
	// - keys list succeeds using config credentials

	homeDir := t.TempDir()

	stub := newStubServer(t)
	// Provider list for the login flow.
	stub.onRoute("GET", "/api/v1/auth/providers", http.StatusOK,
		`[{"name":"github","authorize_url":"https://github.com/login/oauth/authorize"}]`)
	// Callback response with api_key (per 05-REQ-10.1).
	callbackResp := `{"user":{"id":"u1","username":"testuser","email":"test@example.com","provider":"github","provider_id":"12345","status":"active"},"api_key":{"key":"af_0011aabb_secret","key_id":"0011aabb"}}`
	stub.onRoute("POST", "/api/v1/auth/callback", http.StatusOK, callbackResp)
	// Keys list response for the second command.
	stub.onRoute("GET", "/api/v1/keys", http.StatusOK, `[]`)

	// --- Part 1: Login ---

	cmd, stdoutPipe, stderrPipe, done := execAfcAsyncWithHome(t, homeDir,
		[]string{"login", "--provider", "github"},
		"AF_HUB_URL="+stub.Server.URL,
		"AFC_CALLBACK_TIMEOUT=10",
		"AFC_SKIP_BROWSER=1",
	)
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
	}()

	// Detect the callback port from stderr.
	callbackPort, earlyStderr := detectCallbackPortCfg(t, stderrPipe, 5*time.Second)
	if callbackPort == 0 {
		stdout, stderrRest, exitCode := waitForDoneCfg(t, stdoutPipe, stderrPipe, done, 10*time.Second)
		t.Fatalf("could not find callback port. Exit=%d stdout=%q stderr=%q",
			exitCode, stdout, string(earlyStderr)+stderrRest)
	}

	// Simulate OAuth redirect with an authorization code.
	callbackURL := fmt.Sprintf("http://localhost:%d/callback?code=test-auth-code", callbackPort)
	resp, err := http.Get(callbackURL)
	if err != nil {
		t.Fatalf("failed to send OAuth callback: %v", err)
	}
	resp.Body.Close()

	// Wait for login to complete.
	_, _, loginExitCode := waitForDoneCfg(t, stdoutPipe, stderrPipe, done, 10*time.Second)

	if loginExitCode != 0 {
		t.Errorf("login: expected exit code 0, got %d", loginExitCode)
	}

	// --- Part 2: Verify config file state ---

	// Check $HOME/.af/ directory was created with 0700.
	afDir := filepath.Join(homeDir, ".af")
	dirInfo, err := os.Stat(afDir)
	if err != nil {
		t.Fatalf("$HOME/.af/ was not created after login: %v", err)
	}
	if !dirInfo.IsDir() {
		t.Fatal("$HOME/.af is not a directory")
	}
	if dirInfo.Mode().Perm() != 0700 {
		t.Errorf("$HOME/.af/ permissions: got %04o, want 0700", dirInfo.Mode().Perm())
	}

	// Check config.toml was created with 0600.
	configPath := filepath.Join(afDir, "config.toml")
	fileInfo, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("config.toml was not created after login: %v", err)
	}
	if fileInfo.Mode().Perm() != 0600 {
		t.Errorf("config.toml permissions: got %04o, want 0600", fileInfo.Mode().Perm())
	}

	// Check config contents.
	cfg := loadConfigFromHome(t, homeDir)

	if cfg.APIKey != "_login" {
		t.Errorf("config api_key = %q, want %q", cfg.APIKey, "_login")
	}
	if cfg.HubURL != stub.Server.URL {
		t.Errorf("config hub_url = %q, want %q", cfg.HubURL, stub.Server.URL)
	}

	loginEntry, ok := cfg.Keys["_login"]
	if !ok {
		t.Fatal("config missing [keys._login] section after login")
	}
	if loginEntry.KeyID != "0011aabb" {
		t.Errorf("[keys._login].key_id = %q, want %q", loginEntry.KeyID, "0011aabb")
	}
	if loginEntry.Token != "af_0011aabb_secret" {
		t.Errorf("[keys._login].token = %q, want %q", loginEntry.Token, "af_0011aabb_secret")
	}
	if loginEntry.Label != "login" {
		t.Errorf("[keys._login].label = %q, want %q", loginEntry.Label, "login")
	}

	// --- Part 3: Run keys list with no flags ---

	_, _, listExitCode := execAfcWithHome(t, homeDir,
		[]string{"keys", "list"},
		// No AF_HUB_URL, no --hub-url, no --api-key → relies on config.
	)

	if listExitCode != 0 {
		t.Errorf("keys list (config-only): expected exit code 0, got %d", listExitCode)
	}
}

// ---------------------------------------------------------------------------
// TS-05-SMOKE-2: Keys create + keys default sequence
// Execution Path: 05-PATH-2
// REQ: 05-REQ-6.1, 05-REQ-9.1
// ---------------------------------------------------------------------------

func TestSpec05Smoke_KeysCreateAndDefault(t *testing.T) {
	// TS-05-SMOKE-2: User creates a workspace key and sets it as default.
	//
	// Steps:
	// 1. Pre-create config with _login credentials and hub_url
	// 2. Run keys create --workspace my-project → should add [keys.my-project]
	// 3. Assert [keys.my-project] section added to config
	// 4. Run keys default my-project → should set api_key="my-project"
	// 5. Assert api_key="my-project" in config

	homeDir := t.TempDir()

	// Pre-create config with login credentials.
	writeTestConfig(t, homeDir, `hub_url = "https://hub.example.com"
api_key = "_login"

[keys._login]
key_id = "aabbccdd"
token = "af_aabbccdd_secret"
label = "login"
`)

	stub := newStubServer(t)
	// Stub for keys create: returns the created key.
	keyCreateResp := `{"key":"af_11223344_newsecret","key_id":"11223344","role":"member","expires_at":"2027-01-01T00:00:00Z"}`
	stub.onRoute("POST", "/api/v1/keys", http.StatusCreated, keyCreateResp)

	// --- Step 2: keys create ---

	_, stderr, createExit := execAfcWithHome(t, homeDir,
		[]string{"keys", "create", "--workspace", "my-project", "--label", "dev-key", "--expires", "30"},
		"AF_HUB_URL="+stub.Server.URL,
		"AF_HUB_API_KEY=af_aabbccdd_secret",
	)

	if createExit != 0 {
		t.Errorf("keys create: expected exit code 0, got %d (stderr: %s)", createExit, stderr)
	}

	// --- Step 3: Verify config has [keys.my-project] ---

	cfg := loadConfigFromHome(t, homeDir)
	if _, ok := cfg.Keys["my-project"]; !ok {
		t.Fatal("keys create: expected [keys.my-project] section in config after create")
	}
	if cfg.Keys["my-project"].KeyID == "" {
		t.Error("keys create: [keys.my-project].key_id should not be empty")
	}
	if cfg.Keys["my-project"].Token == "" {
		t.Error("keys create: [keys.my-project].token should not be empty")
	}

	// --- Step 4: keys default ---

	stdout, stderr, defaultExit := execAfcWithHome(t, homeDir,
		[]string{"keys", "default", "my-project"},
	)

	if defaultExit != 0 {
		t.Errorf("keys default: expected exit code 0, got %d (stdout: %s, stderr: %s)", defaultExit, stdout, stderr)
	}

	// --- Step 5: Verify api_key updated ---

	cfg = loadConfigFromHome(t, homeDir)
	if cfg.APIKey != "my-project" {
		t.Errorf("keys default: config api_key = %q, want %q", cfg.APIKey, "my-project")
	}

	// Stdout should confirm the change.
	if !strings.Contains(stdout, "my-project") {
		t.Errorf("keys default: expected stdout to mention 'my-project', got: %q", stdout)
	}
}

// ---------------------------------------------------------------------------
// TS-05-SMOKE-3: Revoke the default key
// Execution Path: 05-PATH-3
// REQ: 05-REQ-8.1, 05-REQ-8.2
// ---------------------------------------------------------------------------

func TestSpec05Smoke_RevokeDefaultKey(t *testing.T) {
	// TS-05-SMOKE-3: User revokes the default key, verifying config.toml is
	// cleaned up and a warning is issued.
	//
	// Steps:
	// 1. Pre-create config with api_key="my-project" and [keys.my-project]
	// 2. Run keys revoke a1b2c3d4e5f6 (matching key_id)
	// 3. Assert [keys.my-project] section removed
	// 4. Assert api_key set to empty string
	// 5. Assert warning on stderr mentions "afc keys default"

	homeDir := t.TempDir()

	writeTestConfig(t, homeDir, `hub_url = "https://hub.example.com"
api_key = "my-project"

[keys.my-project]
key_id = "a1b2c3d4e5f6"
token = "af_a1b2c3d4e5f6_secret"
label = "dev-key"

[keys._login]
key_id = "aabbccdd"
token = "af_aabbccdd_secret"
label = "login"
`)

	stub := newStubServer(t)
	stub.onRoute("DELETE", "/api/v1/keys/a1b2c3d4e5f6", http.StatusOK, `{"message":"key revoked"}`)

	// --- Step 2: keys revoke ---

	_, stderr, revokeExit := execAfcWithHome(t, homeDir,
		[]string{"keys", "revoke", "a1b2c3d4e5f6"},
		"AF_HUB_URL="+stub.Server.URL,
		"AF_HUB_API_KEY=af_aabbccdd_secret",
	)

	if revokeExit != 0 {
		t.Errorf("keys revoke: expected exit code 0, got %d", revokeExit)
	}

	// --- Step 3: Verify [keys.my-project] removed ---

	cfg := loadConfigFromHome(t, homeDir)
	if _, ok := cfg.Keys["my-project"]; ok {
		t.Error("keys revoke: expected [keys.my-project] section to be removed")
	}

	// --- Step 4: Verify api_key cleared ---

	if cfg.APIKey != "" {
		t.Errorf("keys revoke: config api_key = %q, want empty string (default key was revoked)", cfg.APIKey)
	}

	// --- Step 5: Verify warning on stderr ---

	if !strings.Contains(stderr, "afc keys default") {
		t.Errorf("keys revoke: expected stderr to mention 'afc keys default', got: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// TS-05-SMOKE-4: Backward compatibility — flags bypass config
// Execution Path: 05-PATH-4
// REQ: 05-REQ-11.1
// ---------------------------------------------------------------------------

func TestSpec05Smoke_BackwardCompatFlags(t *testing.T) {
	// TS-05-SMOKE-4: User passes --hub-url and --api-key flags. The command
	// executes using flag-provided credentials. Config file is not modified.
	//
	// Steps:
	// 1. Pre-create config with known content (may or may not be read)
	// 2. Run keys list with --hub-url and --api-key flags
	// 3. Assert exit code 0
	// 4. Assert config.toml is unchanged

	homeDir := t.TempDir()

	initialConfig := `hub_url = "https://config-hub.example.com"
api_key = "_login"

[keys._login]
key_id = "ccddee"
token = "af_ccddee_secret"
label = "login"
`
	writeTestConfig(t, homeDir, initialConfig)
	configPath := filepath.Join(homeDir, ".af", "config.toml")
	originalContent, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	stub := newStubServer(t)
	stub.onRoute("GET", "/api/v1/keys", http.StatusOK, `[]`)

	// --- Step 2: keys list with flags ---

	_, _, exitCode := execAfcWithHome(t, homeDir,
		[]string{"keys", "list",
			"--hub-url", stub.Server.URL,
			"--api-key", "af_flag_secret",
		},
	)

	// --- Step 3: Assert exit code 0 ---

	if exitCode != 0 {
		t.Errorf("keys list with flags: expected exit code 0, got %d", exitCode)
	}

	// --- Step 4: Assert config unchanged ---

	afterContent, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config after command: %v", err)
	}
	if string(afterContent) != string(originalContent) {
		t.Error("config.toml was modified during keys list with flags; expected no changes")
	}

	// Verify the stub received the request (command actually ran).
	if !stub.receivedRequest("GET", "/api/v1/keys") {
		t.Error("expected stub to receive GET /api/v1/keys")
	}
}

// ---------------------------------------------------------------------------
// TS-05-SMOKE-5: Malformed config causes immediate exit
// Execution Path: 05-PATH-5
// REQ: 05-REQ-2.E1
// ---------------------------------------------------------------------------

func TestSpec05Smoke_MalformedConfigExit(t *testing.T) {
	// TS-05-SMOKE-5: When config.toml contains invalid TOML, any afc command
	// should immediately exit with a non-zero status code and a descriptive
	// parse error on stderr. No API calls should be made.
	//
	// Steps:
	// 1. Write invalid TOML to config.toml
	// 2. Run any afc command (e.g. keys list)
	// 3. Assert exit code non-zero
	// 4. Assert stderr contains config.toml path and parse error
	// 5. Assert no API calls were made (stub receives no requests)

	homeDir := t.TempDir()

	// Write invalid TOML content.
	writeTestConfig(t, homeDir, `hub_url = [not valid TOML
this is broken content = {{{
`)

	stub := newStubServer(t)
	stub.onRoute("GET", "/api/v1/keys", http.StatusOK, `[]`)

	// --- Step 2: Run command with malformed config ---

	_, stderr, exitCode := execAfcWithHome(t, homeDir,
		[]string{"keys", "list",
			"--hub-url", stub.Server.URL,
			"--api-key", "af_test_secret",
		},
	)

	// --- Step 3: Assert non-zero exit code ---

	if exitCode == 0 {
		t.Error("expected non-zero exit code with malformed config, got 0")
	}

	// --- Step 4: Assert stderr mentions config file and parse error ---

	stderrLower := strings.ToLower(stderr)
	if !strings.Contains(stderrLower, "config") {
		t.Errorf("expected stderr to mention 'config', got: %q", stderr)
	}
	// Check for parse-related keywords.
	hasParseMention := strings.Contains(stderrLower, "parse") ||
		strings.Contains(stderrLower, "decode") ||
		strings.Contains(stderrLower, "toml") ||
		strings.Contains(stderrLower, "invalid") ||
		strings.Contains(stderrLower, "error")
	if !hasParseMention {
		t.Errorf("expected stderr to mention parse/decode error, got: %q", stderr)
	}

	// --- Step 5: Assert no API calls were made ---

	if stub.requestCount() > 0 {
		t.Errorf("expected no API calls with malformed config, but stub received %d request(s)", stub.requestCount())
	}
}
