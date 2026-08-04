package gitcmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// ========================================================================
// Group 2 test helpers: repo setup for LsRemote, MergeTree, Rebase tests
// ========================================================================

// runGit executes a git command in the specified directory and returns
// trimmed stdout. It fails the test on non-zero exit.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (dir=%s) failed: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// runGitMayFail executes a git command in the specified directory. Unlike
// runGit, it does not fail the test on error. Returns stdout and error.
func runGitMayFail(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// writeTestFile creates or overwrites a file with the given content.
func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// configGitUser sets user.name and user.email in the given repo directory.
func configGitUser(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "config", "user.email", "test@example.com")
}

// initBareTestRepo creates a bare git repository with a single commit on
// the "main" branch. Returns the absolute path to the bare repo directory.
// Used as a remote for LsRemote tests.
func initBareTestRepo(t *testing.T) string {
	t.Helper()

	// Create a regular repo with one commit on "main".
	sourceDir := t.TempDir()
	runGit(t, "", "init", "-b", "main", sourceDir)
	configGitUser(t, sourceDir)
	writeTestFile(t, filepath.Join(sourceDir, "README.md"), "hello")
	runGit(t, sourceDir, "add", ".")
	runGit(t, sourceDir, "commit", "-m", "initial")

	// Create a bare clone.
	bareDir := filepath.Join(t.TempDir(), "bare.git")
	runGit(t, "", "clone", "--bare", sourceDir, bareDir)

	return bareDir
}

// setupMergeableRepo creates a repository with diverged "main" and "feature"
// branches that can be merged cleanly (they modify different files).
// Returns (repoDir, mainSHA, featureSHA).
func setupMergeableRepo(t *testing.T) (string, string, string) {
	t.Helper()

	dir := t.TempDir()
	runGit(t, "", "init", "-b", "main", dir)
	configGitUser(t, dir)

	// Base commit on main.
	writeTestFile(t, filepath.Join(dir, "base.txt"), "base content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "base")

	// Feature branch: add a non-conflicting file.
	runGit(t, dir, "checkout", "-b", "feature")
	writeTestFile(t, filepath.Join(dir, "feature.txt"), "feature content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "feature change")
	featureSHA := runGit(t, dir, "rev-parse", "HEAD")

	// Back to main: add a different non-conflicting file.
	runGit(t, dir, "checkout", "main")
	writeTestFile(t, filepath.Join(dir, "main-only.txt"), "main content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "main change")
	mainSHA := runGit(t, dir, "rev-parse", "HEAD")

	return dir, mainSHA, featureSHA
}

// setupConflictingRepo creates a repository with diverged "main" and
// "feature" branches that conflict on "conflict.txt".
// Returns (repoDir, mainSHA, featureSHA).
func setupConflictingRepo(t *testing.T) (string, string, string) {
	t.Helper()

	dir := t.TempDir()
	runGit(t, "", "init", "-b", "main", dir)
	configGitUser(t, dir)

	// Base commit with conflict.txt.
	writeTestFile(t, filepath.Join(dir, "conflict.txt"), "base content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "base")

	// Feature branch: modify conflict.txt differently.
	runGit(t, dir, "checkout", "-b", "feature")
	writeTestFile(t, filepath.Join(dir, "conflict.txt"), "feature modification")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "feature change to conflict.txt")
	featureSHA := runGit(t, dir, "rev-parse", "HEAD")

	// Back to main: modify conflict.txt differently.
	runGit(t, dir, "checkout", "main")
	writeTestFile(t, filepath.Join(dir, "conflict.txt"), "main modification")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "main change to conflict.txt")
	mainSHA := runGit(t, dir, "rev-parse", "HEAD")

	return dir, mainSHA, featureSHA
}

// setupCleanRebaseRepo creates a repository where "feature" can be cleanly
// rebased onto "main" (they modify different files). Leaves the working
// directory on the "feature" branch. Returns the repo directory.
func setupCleanRebaseRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	runGit(t, "", "init", "-b", "main", dir)
	configGitUser(t, dir)

	// Base commit.
	writeTestFile(t, filepath.Join(dir, "base.txt"), "base content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "base")

	// Feature branch: add a non-conflicting file.
	runGit(t, dir, "checkout", "-b", "feature")
	writeTestFile(t, filepath.Join(dir, "feature.txt"), "feature content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "feature change")

	// Back to main: add a different non-conflicting file.
	runGit(t, dir, "checkout", "main")
	writeTestFile(t, filepath.Join(dir, "main-only.txt"), "main content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "main change")

	// Switch back to feature for the rebase.
	runGit(t, dir, "checkout", "feature")

	return dir
}

// setupConflictingRebaseRepo creates a repository where "feature" conflicts
// when rebased onto "main" (both modify "conflict.txt"). Leaves the working
// directory on the "feature" branch. Returns the repo directory.
func setupConflictingRebaseRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	runGit(t, "", "init", "-b", "main", dir)
	configGitUser(t, dir)

	// Base commit with conflict.txt.
	writeTestFile(t, filepath.Join(dir, "conflict.txt"), "base content")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "base")

	// Feature branch: modify conflict.txt.
	runGit(t, dir, "checkout", "-b", "feature")
	writeTestFile(t, filepath.Join(dir, "conflict.txt"), "feature modification")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "feature change")

	// Back to main: modify conflict.txt differently.
	runGit(t, dir, "checkout", "main")
	writeTestFile(t, filepath.Join(dir, "conflict.txt"), "main modification")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "main change")

	// Switch back to feature for the rebase.
	runGit(t, dir, "checkout", "feature")

	return dir
}

// dirExists returns true if the given path exists and is a directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}
