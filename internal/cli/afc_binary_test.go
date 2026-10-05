package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These tests run the real afc binary (built from cmd/afc) as a subprocess,
// because the process exit status and the exact stdout/stderr contents are
// decided in cmd/afc/main.go, which the in-process command tests cannot see.

// buildAfcBinary compiles the real afc binary into a temp dir and returns its
// path. The test is skipped under -short or when the go tool is unavailable,
// since compiling the binary dominates the runtime.
func buildAfcBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping real-binary test in -short mode")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not available; cannot build the afc binary")
	}
	bin := filepath.Join(t.TempDir(), "afc")
	build := exec.Command(goBin, "build", "-o", bin, "github.com/agent-fox-dev/hub/cmd/afc")
	// afc has no cgo dependencies (see the Makefile); do not require a C
	// toolchain to build it for this test.
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build afc failed: %v\n%s", err, out)
	}
	return bin
}

// runAfcBinary runs the afc binary against a hub at baseURL with an isolated
// $HOME and returns stdout, stderr and the process exit code.
func runAfcBinary(t *testing.T, bin, baseURL string, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	home := t.TempDir()
	cfgDir := filepath.Join(home, ".ak")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"),
		[]byte("endpoint_url = \"\"\nuser_id = \"\"\napi_key = \"\"\n"), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}

	full := append([]string{"--endpoint-url", baseURL, "--api-key", "test-api-key"}, args...)
	cmd := exec.Command(bin, full...)
	cmd.Env = append(os.Environ(), "HOME="+home)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("running afc: %v", err)
		}
		exitCode = ee.ExitCode()
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// binarySyncStub returns a stub hub whose sync endpoint answers with the
// given patches_diverged list.
func binarySyncStub(diverged []string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/sync") {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"patches_merged":    []string{},
				"rebuild_triggered": false,
				"origin_fetched":    true,
				"patches_synced":    []map[string]any{},
				"patches_diverged":  diverged,
			})
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
}

// TestAfcBinary_SyncFailOnDiverged_ProcessExitsThree is the end-to-end check
// for 20-REQ-6.5 / TS-20-39: the real afc process, pointed at a stub hub whose
// sync response has a non-empty patches_diverged, must exit with status 3,
// print exactly one JSON document on stdout (the sync response, no error
// envelope) and name the branches on stderr.
func TestAfcBinary_SyncFailOnDiverged_ProcessExitsThree(t *testing.T) {
	bin := buildAfcBinary(t)
	server := binarySyncStub([]string{"feat-a", "feat-b"})
	defer server.Close()

	stdout, stderr, code := runAfcBinary(t, bin, server.URL,
		"workspace", "sync", "my-workspace", "--fail-on-diverged")

	if code != 3 {
		t.Errorf("exit code = %d; want 3\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	docs := decodeJSONValues(t, stdout)
	if len(docs) != 1 {
		t.Fatalf("stdout holds %d JSON documents; want exactly 1 (the sync response)\nstdout: %s",
			len(docs), stdout)
	}
	resp, ok := docs[0].(map[string]any)
	if !ok {
		t.Fatalf("stdout document is not an object: %s", stdout)
	}
	if _, hasErr := resp["error"]; hasErr {
		t.Errorf("stdout carries an error envelope; want only the sync response: %s", stdout)
	}
	if _, ok := resp["patches_diverged"]; !ok {
		t.Errorf("stdout document lacks patches_diverged: %s", stdout)
	}

	if !strings.Contains(stderr, "feat-a") || !strings.Contains(stderr, "feat-b") {
		t.Errorf("stderr should name the diverged branches; got: %q", stderr)
	}
	if n := strings.Count(stderr, "feat-a"); n != 1 {
		t.Errorf("stderr names feat-a %d times; want once (message printed once)\nstderr: %q", n, stderr)
	}
}

// TestAfcBinary_SyncFailOnDiverged_EmptyExitsZero pins the other half of
// 20-REQ-6.5: with an empty patches_diverged the process exits 0 even with
// the flag set.
func TestAfcBinary_SyncFailOnDiverged_EmptyExitsZero(t *testing.T) {
	bin := buildAfcBinary(t)
	server := binarySyncStub([]string{})
	defer server.Close()

	stdout, stderr, code := runAfcBinary(t, bin, server.URL,
		"workspace", "sync", "my-workspace", "--fail-on-diverged")

	if code != 0 {
		t.Errorf("exit code = %d; want 0\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if docs := decodeJSONValues(t, stdout); len(docs) != 1 {
		t.Errorf("stdout holds %d JSON documents; want 1\nstdout: %s", len(docs), stdout)
	}
}

// TestAfcBinary_SyncDiverged_WithoutFlagExitsZero pins 20-REQ-6.4 through the
// real binary: without --fail-on-diverged a diverged branch does not change
// the exit status.
func TestAfcBinary_SyncDiverged_WithoutFlagExitsZero(t *testing.T) {
	bin := buildAfcBinary(t)
	server := binarySyncStub([]string{"feat-a"})
	defer server.Close()

	stdout, stderr, code := runAfcBinary(t, bin, server.URL,
		"workspace", "sync", "my-workspace")

	if code != 0 {
		t.Errorf("exit code = %d; want 0\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
}

// TestAfcBinary_ErrorExitCodesUnchanged guards the existing exit-code
// contract (1 for API errors, 2 for client errors) alongside the new exit 3.
func TestAfcBinary_ErrorExitCodesUnchanged(t *testing.T) {
	bin := buildAfcBinary(t)

	// API error: the hub answers 400 → exit 1, with exactly one error envelope.
	apiErr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":400,"message":"bad request"}}`)
	}))
	defer apiErr.Close()
	stdout, stderr, code := runAfcBinary(t, bin, apiErr.URL,
		"workspace", "sync", "my-workspace", "--fail-on-diverged")
	if code != 1 {
		t.Errorf("API error exit code = %d; want 1\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if docs := decodeJSONValues(t, stdout); len(docs) != 1 {
		t.Errorf("API error: stdout holds %d JSON documents; want 1\nstdout: %s", len(docs), stdout)
	}

	// Client error: reclone without --confirm → exit 2.
	srv := binarySyncStub(nil)
	defer srv.Close()
	stdout, stderr, code = runAfcBinary(t, bin, srv.URL, "workspace", "reclone", "my-workspace")
	if code != 2 {
		t.Errorf("client error exit code = %d; want 2\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
}
