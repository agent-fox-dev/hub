// Package integration contains smoke tests for the af-hub server binary.
// These tests exercise the full binary through all five execution paths.
//
// Smoke tests are skipped by default until the real binary is built (task
// groups 3+). Run with: go test -run TestSmoke ./internal/integration/ -count=1
package integration

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// skipIfBinaryMissing skips the test if the af-hub binary is not available.
func skipIfBinaryMissing(t *testing.T) string {
	t.Helper()
	root := findRepoRoot(t)
	binPath := filepath.Join(root, "bin", "af-hub")
	if _, err := os.Stat(binPath); err != nil {
		t.Skipf("af-hub binary not found at %s; skipping smoke test (run 'make build' first)", binPath)
	}
	return binPath
}

// TS-01-SMOKE-1: Smoke test the full first-boot path: server starts with
// config.toml and no AF_HUB_ADMIN_TOKEN, bootstraps admin, writes admin_token
// file, and serves health probes.
func TestSmoke_FirstBoot(t *testing.T) {
	binPath := skipIfBinaryMissing(t)
	dir := t.TempDir()

	// Write a minimal config.toml.
	port := getFreePort(t)
	configContent := fmt.Sprintf(`[server]
port = %d
bind_address = "127.0.0.1"

[database]
path = "%s"
`, port, filepath.Join(dir, "data", "af-hub.db"))

	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config.toml: %v", err)
	}

	// Start the server (no AF_HUB_ADMIN_TOKEN set).
	cmd := exec.Command(binPath)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "AF_HUB_ADMIN_TOKEN=") // Ensure unset.

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
	})

	// Wait for the server to be ready.
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	waitForServer(t, addr, 10*time.Second)

	// Verify admin_token file was created.
	tokenPath := filepath.Join(dir, "admin_token")
	tokenData, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("admin_token file not created: %v", err)
	}
	token := string(tokenData)

	// Verify token format.
	if !strings.HasPrefix(token, "af_admin_") {
		t.Errorf("admin token should start with 'af_admin_', got prefix: %q", token[:min(len(token), 20)])
	}
	if len(token) != 73 {
		t.Errorf("expected token length 73, got %d", len(token))
	}

	// Verify file permissions (mode 0600).
	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatalf("failed to stat admin_token: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected admin_token mode 0600, got %04o", info.Mode().Perm())
	}

	// Verify GET /healthz returns 200 {"status": "ok"}.
	resp, err := http.Get(fmt.Sprintf("http://%s/healthz", addr))
	if err != nil {
		t.Fatalf("GET /healthz failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("GET /healthz: expected 200, got %d", resp.StatusCode)
	}
	var healthBody map[string]string
	json.NewDecoder(resp.Body).Decode(&healthBody)
	if healthBody["status"] != "ok" {
		t.Errorf("GET /healthz: expected status 'ok', got %q", healthBody["status"])
	}

	// Verify GET /readyz returns 200 {"status": "ready"}.
	resp2, err := http.Get(fmt.Sprintf("http://%s/readyz", addr))
	if err != nil {
		t.Fatalf("GET /readyz failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Errorf("GET /readyz: expected 200, got %d", resp2.StatusCode)
	}
	var readyBody map[string]string
	json.NewDecoder(resp2.Body).Decode(&readyBody)
	if readyBody["status"] != "ready" {
		t.Errorf("GET /readyz: expected status 'ready', got %q", readyBody["status"])
	}
}

// TS-01-SMOKE-2: Smoke test the subsequent boot path: server starts with
// AF_HUB_ADMIN_TOKEN set to the correct token and passes validation.
func TestSmoke_SubsequentBoot(t *testing.T) {
	binPath := skipIfBinaryMissing(t)
	dir := t.TempDir()

	port := getFreePort(t)
	configContent := fmt.Sprintf(`[server]
port = %d
bind_address = "127.0.0.1"

[database]
path = "%s"
`, port, filepath.Join(dir, "data", "af-hub.db"))

	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config.toml: %v", err)
	}

	// First boot to create admin token.
	cmd1 := exec.Command(binPath)
	cmd1.Dir = dir
	cmd1.Env = append(os.Environ(), "AF_HUB_ADMIN_TOKEN=")
	if err := cmd1.Start(); err != nil {
		t.Fatalf("first boot failed to start: %v", err)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	waitForServer(t, addr, 10*time.Second)

	// Read the admin token.
	tokenData, err := os.ReadFile(filepath.Join(dir, "admin_token"))
	if err != nil {
		t.Fatalf("admin_token not found after first boot: %v", err)
	}
	token := string(tokenData)

	// Stop the first instance.
	cmd1.Process.Signal(syscall.SIGTERM)
	cmd1.Wait()

	// Wait for port to be released.
	waitForPortFree(t, addr, 5*time.Second)

	// Second boot with correct token.
	port2 := getFreePort(t)
	configContent2 := fmt.Sprintf(`[server]
port = %d
bind_address = "127.0.0.1"

[database]
path = "%s"
`, port2, filepath.Join(dir, "data", "af-hub.db"))
	if err := os.WriteFile(cfgPath, []byte(configContent2), 0644); err != nil {
		t.Fatalf("failed to update config.toml: %v", err)
	}

	cmd2 := exec.Command(binPath)
	cmd2.Dir = dir
	cmd2.Env = append(os.Environ(), fmt.Sprintf("AF_HUB_ADMIN_TOKEN=%s", token))
	if err := cmd2.Start(); err != nil {
		t.Fatalf("second boot failed to start: %v", err)
	}
	t.Cleanup(func() {
		cmd2.Process.Signal(syscall.SIGTERM)
		cmd2.Wait()
	})

	addr2 := fmt.Sprintf("127.0.0.1:%d", port2)
	waitForServer(t, addr2, 10*time.Second)

	// Verify health probes.
	resp, err := http.Get(fmt.Sprintf("http://%s/healthz", addr2))
	if err != nil {
		t.Fatalf("GET /healthz failed on subsequent boot: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("GET /healthz: expected 200, got %d", resp.StatusCode)
	}

	// Verify no new admin_token file (same one should exist).
	newTokenData, err := os.ReadFile(filepath.Join(dir, "admin_token"))
	if err != nil {
		t.Fatalf("admin_token file missing after second boot: %v", err)
	}
	if string(newTokenData) != token {
		t.Error("admin_token file content should not change on subsequent boot")
	}
}

// TS-01-SMOKE-3: Smoke test the admin token rotation path.
func TestSmoke_TokenRotation(t *testing.T) {
	binPath := skipIfBinaryMissing(t)
	dir := t.TempDir()

	port := getFreePort(t)
	configContent := fmt.Sprintf(`[server]
port = %d
bind_address = "127.0.0.1"

[database]
path = "%s"
`, port, filepath.Join(dir, "data", "af-hub.db"))

	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config.toml: %v", err)
	}

	// First boot to create initial token.
	cmd1 := exec.Command(binPath)
	cmd1.Dir = dir
	cmd1.Env = append(os.Environ(), "AF_HUB_ADMIN_TOKEN=")
	if err := cmd1.Start(); err != nil {
		t.Fatalf("first boot failed: %v", err)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	waitForServer(t, addr, 10*time.Second)

	oldToken, err := os.ReadFile(filepath.Join(dir, "admin_token"))
	if err != nil {
		t.Fatalf("admin_token not found: %v", err)
	}

	cmd1.Process.Signal(syscall.SIGTERM)
	cmd1.Wait()
	waitForPortFree(t, addr, 5*time.Second)

	// Rotation boot.
	port2 := getFreePort(t)
	configContent2 := fmt.Sprintf(`[server]
port = %d
bind_address = "127.0.0.1"

[database]
path = "%s"
`, port2, filepath.Join(dir, "data", "af-hub.db"))
	if err := os.WriteFile(cfgPath, []byte(configContent2), 0644); err != nil {
		t.Fatalf("failed to update config.toml: %v", err)
	}

	cmd2 := exec.Command(binPath, "--reset-admin-token")
	cmd2.Dir = dir
	cmd2.Env = append(os.Environ(), "AF_HUB_ADMIN_TOKEN=")
	if err := cmd2.Start(); err != nil {
		t.Fatalf("rotation boot failed: %v", err)
	}
	t.Cleanup(func() {
		cmd2.Process.Signal(syscall.SIGTERM)
		cmd2.Wait()
	})

	addr2 := fmt.Sprintf("127.0.0.1:%d", port2)
	waitForServer(t, addr2, 10*time.Second)

	// Verify new token is different.
	newToken, err := os.ReadFile(filepath.Join(dir, "admin_token"))
	if err != nil {
		t.Fatalf("admin_token not found after rotation: %v", err)
	}
	if string(newToken) == string(oldToken) {
		t.Error("admin_token should differ after rotation")
	}
	if !strings.HasPrefix(string(newToken), "af_admin_") {
		t.Errorf("new token should start with 'af_admin_', got: %q", string(newToken)[:min(len(newToken), 20)])
	}

	// Verify file mode.
	info, err := os.Stat(filepath.Join(dir, "admin_token"))
	if err != nil {
		t.Fatalf("stat admin_token: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected mode 0600, got %04o", info.Mode().Perm())
	}

	// Verify /healthz returns 200.
	resp, err := http.Get(fmt.Sprintf("http://%s/healthz", addr2))
	if err != nil {
		t.Fatalf("GET /healthz after rotation: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("GET /healthz: expected 200, got %d", resp.StatusCode)
	}
}

// TS-01-SMOKE-4: Smoke test the graceful shutdown path.
func TestSmoke_GracefulShutdown(t *testing.T) {
	binPath := skipIfBinaryMissing(t)
	dir := t.TempDir()

	port := getFreePort(t)
	configContent := fmt.Sprintf(`[server]
port = %d
bind_address = "127.0.0.1"

[database]
path = "%s"
`, port, filepath.Join(dir, "data", "af-hub.db"))

	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config.toml: %v", err)
	}

	cmd := exec.Command(binPath)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "AF_HUB_ADMIN_TOKEN=")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	waitForServer(t, addr, 10*time.Second)

	// Send SIGTERM.
	start := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("failed to send SIGTERM: %v", err)
	}

	// Wait for exit.
	err := cmd.Wait()
	elapsed := time.Since(start)

	// Should exit within 16 seconds (15s drain + 1s buffer).
	if elapsed > 16*time.Second {
		t.Errorf("server took %v to exit after SIGTERM, expected within 16s", elapsed)
	}

	// Should exit with code 0 (no in-flight requests).
	if err != nil {
		t.Logf("server exited with: %v (may be acceptable if exit code is 0)", err)
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() != 0 {
				t.Errorf("expected exit code 0, got %d", exitErr.ExitCode())
			}
		}
	}
}

// TS-01-SMOKE-5: Smoke test the readiness probe failure path.
func TestSmoke_ReadyzFailure(t *testing.T) {
	binPath := skipIfBinaryMissing(t)
	dir := t.TempDir()

	port := getFreePort(t)
	dbPath := filepath.Join(dir, "data", "af-hub.db")
	configContent := fmt.Sprintf(`[server]
port = %d
bind_address = "127.0.0.1"

[database]
path = "%s"
`, port, dbPath)

	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("failed to write config.toml: %v", err)
	}

	cmd := exec.Command(binPath)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "AF_HUB_ADMIN_TOKEN=")
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
	})

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	waitForServer(t, addr, 10*time.Second)

	// Force a DB failure by removing the database file.
	// Note: This may not cause immediate failure since the connection
	// pool may still have open handles. In a real scenario, we'd need
	// a more robust way to simulate DB failure.
	os.Remove(dbPath)
	os.Remove(dbPath + "-wal")
	os.Remove(dbPath + "-shm")

	// Try /readyz — with the DB file removed, ping may fail.
	start := time.Now()
	resp, err := http.Get(fmt.Sprintf("http://%s/readyz", addr))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("GET /readyz failed: %v", err)
	}
	defer resp.Body.Close()

	// Response should arrive within 3 seconds (2-second ping deadline + buffer).
	if elapsed > 3*time.Second {
		t.Errorf("GET /readyz took %v, expected within 3 seconds", elapsed)
	}

	// We expect either 200 (if the connection is still cached) or 503.
	// The important thing is that the response arrives within the timeout.
	if resp.StatusCode != 200 && resp.StatusCode != 503 {
		t.Errorf("GET /readyz: expected 200 or 503, got %d", resp.StatusCode)
	}

	if resp.StatusCode == 503 {
		var body map[string]string
		json.NewDecoder(resp.Body).Decode(&body)
		if body["status"] != "not ready" {
			t.Errorf("expected status 'not ready', got %q", body["status"])
		}
	}
}

// --- Test helpers ---

// findRepoRoot walks up from the current directory to find go.mod.
func findRepoRoot(t *testing.T) string {
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

// getFreePort finds an available TCP port.
func getFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// waitForServer polls a server until it responds.
func waitForServer(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become ready within %v", addr, timeout)
}

// waitForPortFree waits until a port is no longer in use.
func waitForPortFree(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			return // Port is free.
		}
		conn.Close()
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("warning: port %s still in use after %v", addr, timeout)
}

