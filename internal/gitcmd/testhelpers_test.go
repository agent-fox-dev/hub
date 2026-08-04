package gitcmd

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// initTestRepo creates a temporary directory, runs `git init` inside it,
// and registers cleanup. Returns the path to the repo directory.
func initTestRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	cmd := exec.Command("git", "init", dir)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}

	// Configure user for commits in tests.
	for _, kv := range [][2]string{
		{"user.name", "Test User"},
		{"user.email", "test@example.com"},
	} {
		cfg := exec.Command("git", "-C", dir, "config", kv[0], kv[1])
		cfg.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cfg.CombinedOutput(); err != nil {
			t.Fatalf("git config %s failed: %v\n%s", kv[0], err, out)
		}
	}

	return dir
}

// requireGitMinVersion skips the test if the host git version is below the
// specified minimum. This helper uses its own standalone version parsing logic
// so it does not depend on the parseGitVersion function under test.
func requireGitMinVersion(t *testing.T, minMajor, minMinor int) {
	t.Helper()

	cmd := exec.Command("git", "--version")
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("git --version failed: %v", err)
	}

	major, minor, parseErr := parseHostGitVersion(strings.TrimSpace(string(out)))
	if parseErr != nil {
		t.Skipf("could not parse host git version: %v", parseErr)
	}

	if major < minMajor || (major == minMajor && minor < minMinor) {
		t.Skipf("host git %d.%d is below required %d.%d", major, minor, minMajor, minMinor)
	}
}

// parseHostGitVersion is a standalone version parser used only by test helpers.
// It does NOT depend on the parseGitVersion function under test, ensuring that
// test precondition checks work even before the production code is implemented.
func parseHostGitVersion(raw string) (int, int, error) {
	// Expected format: "git version X.Y[.Z] [suffix]"
	const prefix = "git version "
	if !strings.HasPrefix(raw, prefix) {
		return 0, 0, fmt.Errorf("unexpected git version format: %q", raw)
	}
	versionPart := strings.TrimPrefix(raw, prefix)
	// Take only the first space-delimited token (drop platform suffixes).
	if idx := strings.IndexByte(versionPart, ' '); idx >= 0 {
		versionPart = versionPart[:idx]
	}
	parts := strings.SplitN(versionPart, ".", 3)
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("need at least major.minor: %q", raw)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid major: %w", err)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid minor: %w", err)
	}
	return major, minor, nil
}

// envSliceContains checks whether a slice of "KEY=VALUE" strings contains the
// given exact entry.
func envSliceContains(env []string, entry string) bool {
	return slices.Contains(env, entry)
}

// lastEnvValue returns the value of the last occurrence of the given key in a
// "KEY=VALUE" environment slice. Returns ("", false) if the key is not found.
func lastEnvValue(env []string, key string) (string, bool) {
	prefix := key + "="
	var val string
	found := false
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			val = e[len(prefix):]
			found = true
		}
	}
	return val, found
}
