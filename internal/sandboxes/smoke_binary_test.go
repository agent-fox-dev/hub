package sandboxes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeFakeEngine writes an executable stand-in for podman that appends its
// arguments to a log, answers --version like podman and lists nothing. It
// returns the script and the log path; the log exists only once the engine has
// been invoked.
func writeFakeEngine(t *testing.T) (script, logPath string) {
	t.Helper()
	dir := t.TempDir()
	script = filepath.Join(dir, "fake-podman")
	logPath = filepath.Join(dir, "engine.log")
	body := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %q
case "$1" in
  --version) echo "podman version 5.0.0" ;;
esac
exit 0
`, logPath)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("writing the fake engine: %v", err)
	}
	return script, logPath
}

// bootstrapAfHub runs af-hub once with --admin-email, which writes the admin
// token and ends the process, and returns the token after removing its file as
// the next start requires.
func bootstrapAfHub(t *testing.T, bin string, env *afHubEnv) string {
	t.Helper()
	output, _ := env.run(t, bin, time.Minute, "--admin-email", "admin@example.com")
	tokenPath := filepath.Join(env.configDir, "admin_token")
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("the bootstrap run wrote no admin_token: %v\n%s", err, output)
	}
	if err := os.Remove(tokenPath); err != nil {
		t.Fatalf("removing admin_token: %v", err)
	}
	return strings.TrimSpace(string(token))
}

// syncBuffer is a bytes.Buffer that a running process and a test can share.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startAfHub starts af-hub in the background, waits until it answers its
// health probe and stops it when the test ends. It returns what the process
// writes.
func startAfHub(t *testing.T, bin string, env *afHubEnv) *syncBuffer {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Dir = env.dataDir
	cmd.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+env.configDir,
		"XDG_DATA_HOME="+env.dataDir,
		"HOME="+env.dataDir,
		"AF_AUDIT_DB_PATH="+filepath.Join(env.dataDir, "audit.duckdb"),
	)
	out := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting af-hub: %v", err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})

	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", env.port)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			t.Fatalf("af-hub exited while starting:\n%s", out)
		default:
		}
		if resp, err := http.Get(url); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return out
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("af-hub did not become healthy:\n%s", out)
	return nil
}

// TS-25-57 (smoke): An invalid sandbox section refuses start with every
// failure and no provider contact.
// Path: 25-PATH-3. Requirements: 25-REQ-1.4, 25-REQ-4.6
func TestAfHubBinary_InvalidSectionRefusesStart_TS_25_57(t *testing.T) {
	bin := buildAfHub(t)
	engine, engineLog := writeFakeEngine(t)
	env := newAfHubEnv(t, fmt.Sprintf(`[sandbox]
provider      = "podman"
engine_binary = %q
max_ttl       = "1h"

[[sandbox.profiles]]
name  = "dev"
image = "registry.example/dev:1"
ttl   = "30m"

[[sandbox.profiles]]
name      = "dev"
image     = "registry.example/dev2:1"
ttl       = "30m"
memroy_mb = 512

[[sandbox.profiles]]
name  = "slow"
image = "registry.example/slow:1"
ttl   = "3h"
`, engine))
	bootstrapAfHub(t, bin, env)

	output, exit := env.run(t, bin, 30*time.Second)
	if exit <= 0 {
		t.Fatalf("af-hub exited with status %d; want a refusal\n%s", exit, output)
	}
	for _, want := range []string{"sandbox.profiles[1].memroy_mb", `"dev"`, "already used", `"slow"`, "ttl"} {
		if !strings.Contains(output, want) {
			t.Errorf("output does not contain %q:\n%s", want, output)
		}
	}
	if exists(engineLog) {
		t.Errorf("the engine was invoked for an invalid section: %s", mustRead(t, engineLog))
	}
	if env.listening() {
		t.Error("something listens on the hub's port after the refusal")
	}
	if exists(filepath.Join(env.dataDir, "workspaces")) {
		t.Error("the workspace root was created before the sandbox section was validated")
	}
}

// mustRead returns the content of a file for an error message.
func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// A valid section starts the real af-hub with the real Podman provider driving
// a fake engine; the profile listing then answers over HTTP with the admin
// token, behind apikit's auth middleware. This runs the whole of 25-PATH-1
// through cmd/af-hub.
// Path: 25-PATH-1. Requirements: 25-REQ-5.1, 25-REQ-5.8, 25-REQ-7.4, 25-REQ-8.2
func TestAfHubBinary_ServesProfiles_PATH_1(t *testing.T) {
	bin := buildAfHub(t)
	engine, engineLog := writeFakeEngine(t)
	env := newAfHubEnv(t, fmt.Sprintf(`[sandbox]
provider      = "podman"
engine_binary = %q

[[sandbox.profiles]]
name             = "zeta"
image            = "registry.example/zeta:1"
ttl              = "30m"
secrets          = ["GIT_PAT"]
optional_secrets = ["GH_TOKEN"]

[sandbox.profiles.env]
LANG = "binary-env-value"

[[sandbox.profiles]]
name  = "alpha"
image = "registry.example/alpha:1"
ttl   = "1h"
`, engine))
	token := bootstrapAfHub(t, bin, env)
	output := startAfHub(t, bin, env)

	get := func(credential string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/v1/sandbox-profiles", env.port), nil)
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET /api/v1/sandbox-profiles: %v", err)
		}
		defer resp.Body.Close()
		var body strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			body.Write(buf[:n])
			if err != nil {
				break
			}
		}
		return resp.StatusCode, body.String()
	}

	status, body := get(token)
	if status != http.StatusOK {
		t.Fatalf("status = %d; want 200: %s\nhub output:\n%s", status, body, output)
	}
	var listing struct {
		Profiles []struct {
			Name    string   `json:"name"`
			EnvKeys []string `json:"env_keys"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(body), &listing); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, body)
	}
	if len(listing.Profiles) != 2 || listing.Profiles[0].Name != "alpha" || listing.Profiles[1].Name != "zeta" ||
		!slices.Equal(listing.Profiles[1].EnvKeys, []string{"LANG"}) {
		t.Errorf("listing = %+v; want alpha and zeta with zeta's env key LANG", listing.Profiles)
	}
	if strings.Contains(body+output.String(), "binary-env-value") {
		t.Errorf("an env value leaked into the listing or the hub's output:\n%s\n%s", body, output)
	}
	if status, _ := get(""); status != http.StatusUnauthorized {
		t.Errorf("without a credential: status = %d; want 401", status)
	}

	log := mustRead(t, engineLog)
	if !strings.Contains(log, "--version") || !strings.Contains(log, "ps") {
		t.Errorf("the engine was not asked for its version and a listing (the probe):\n%s", log)
	}
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		if first, _, _ := strings.Cut(line, " "); first == "run" || first == "create" || first == "volume" && strings.Contains(line, "create") {
			t.Errorf("the engine was asked to create something: %q", line)
		}
	}
	if !strings.Contains(output.String(), "sandboxes are enabled") {
		t.Errorf("the hub did not log that sandboxes are enabled:\n%s", output)
	}
}
