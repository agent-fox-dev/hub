package sandboxes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests run the real af-hub binary (built from cmd/af-hub) as a
// subprocess, because the order of start-up and the process exit are decided
// in cmd/af-hub/main.go, which in-process tests cannot see.

// buildAfHub compiles the real af-hub binary into a temp dir and returns its
// path. The test is skipped under -short or without a go tool, since
// compiling the binary (it links DuckDB through cgo) dominates the runtime.
func buildAfHub(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping real-binary test in -short mode")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not available; cannot build the af-hub binary")
	}
	bin := filepath.Join(t.TempDir(), "af-hub")
	build := exec.Command(goBin, "build", "-o", bin, "github.com/agent-fox-dev/hub/cmd/af-hub")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build af-hub failed: %v\n%s", err, out)
	}
	return bin
}

// afHubEnv is the directories and the port one af-hub process uses.
type afHubEnv struct {
	configDir string // XDG_CONFIG_HOME: config.toml and admin_token
	dataDir   string // database, workspaces, audit database
	port      int
}

// newAfHubEnv writes a config.toml with the given [sandbox] section (empty
// for none) and returns the environment to run af-hub in.
func newAfHubEnv(t *testing.T, sandboxSection string) *afHubEnv {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	env := &afHubEnv{configDir: t.TempDir(), dataDir: t.TempDir(), port: port}
	config := fmt.Sprintf(`[server]
port = %d
bind = "127.0.0.1"

[database]
path = %q

[workspace]
path = %q

%s`, port, filepath.Join(env.dataDir, "afhub.db"), filepath.Join(env.dataDir, "workspaces"), sandboxSection)
	writeConfigFile(t, env.configDir, config)
	return env
}

// run starts the binary with args and waits for it, killing it after limit.
// It returns what the process wrote and its exit status; a process that had to
// be killed has exit status -1.
func (e *afHubEnv) run(t *testing.T, bin string, limit time.Duration, args ...string) (output string, exit int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = e.dataDir
	cmd.Env = append(os.Environ(),
		"XDG_CONFIG_HOME="+e.configDir,
		"XDG_DATA_HOME="+e.dataDir,
		"HOME="+e.dataDir,
		"AF_AUDIT_DB_PATH="+filepath.Join(e.dataDir, "audit.duckdb"),
	)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return buf.String(), 0
	case errors.As(err, &exitErr):
		return buf.String(), exitErr.ExitCode()
	default:
		t.Fatalf("running af-hub: %v", err)
		return "", 0
	}
}

// exists reports whether the path exists.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// listening reports whether something accepts connections on the hub's port.
func (e *afHubEnv) listening() bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", e.port), time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// TS-25-8: With an invalid sandbox section af-hub still completes the
// admin-token bootstrap, and otherwise exits before a workspace root, a queue,
// a worker or a listener exists.
//
// The bootstrap run ends in log.Fatal with apikit's "admin token generated"
// message, as it does without a [sandbox] section (docs/configuration.md): the
// test compares it with such a run instead of expecting exit status 0.
// Requirement: 25-REQ-1.7
func TestAfHubBinary_SandboxValidatedAfterBootstrap_TS_25_8(t *testing.T) {
	bin := buildAfHub(t)
	const invalidSection = `[sandbox]
provider = "podman"
listenx  = "an unknown key"
`

	control := newAfHubEnv(t, "")
	controlOutput, controlExit := control.run(t, bin, time.Minute, "--admin-email", "admin@example.com")
	if !exists(filepath.Join(control.configDir, "admin_token")) {
		t.Fatalf("control run without [sandbox] wrote no admin_token (exit %d):\n%s", controlExit, controlOutput)
	}

	env := newAfHubEnv(t, invalidSection)
	output, exit := env.run(t, bin, time.Minute, "--admin-email", "admin@example.com")
	if !exists(filepath.Join(env.configDir, "admin_token")) {
		t.Fatalf("bootstrap with an invalid [sandbox] section wrote no admin_token (exit %d):\n%s", exit, output)
	}
	if exit != controlExit {
		t.Errorf("bootstrap exit status = %d; want %d, the status of a run without [sandbox]\n%s", exit, controlExit, output)
	}
	if !strings.Contains(output, "admin token generated") {
		t.Errorf("bootstrap output does not report the generated token:\n%s", output)
	}
	if strings.Contains(output, "sandbox") {
		t.Errorf("the bootstrap run reported a sandbox error:\n%s", output)
	}

	// The operator saves the token and deletes the file; the next start gets
	// to the sandbox section.
	if err := os.Remove(filepath.Join(env.configDir, "admin_token")); err != nil {
		t.Fatalf("removing admin_token: %v", err)
	}
	output, exit = env.run(t, bin, 30*time.Second)
	if exit <= 0 {
		t.Fatalf("af-hub with an invalid [sandbox] section exited with status %d; want a refusal\n%s", exit, output)
	}
	if !strings.Contains(output, "sandbox.listenx") {
		t.Errorf("output does not name the unknown key sandbox.listenx:\n%s", output)
	}
	if exists(filepath.Join(env.dataDir, "workspaces")) {
		t.Error("the workspace root was created before the sandbox section was validated")
	}
	if exists(filepath.Join(env.dataDir, "audit.duckdb")) {
		t.Error("the audit database was opened before the sandbox section was validated")
	}
	if env.listening() {
		t.Error("something listens on the hub's port after the refusal")
	}
}
