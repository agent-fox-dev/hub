package carrypatch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runGitCmdR runs a git command with a test identity and a clean config, and
// fails the test when it fails.
func runGitCmdR(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitCmdOutR(dir, args...)
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return out
}

// gitCmdOutR runs a git command and returns combined output.
func gitCmdOutR(dir string, args ...string) (string, error) {
	full := append([]string{"-c", "user.name=test", "-c", "user.email=test@test.com"}, args...)
	cmd := exec.Command("git", full...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// addCommitR creates a file and commits it.
func addCommitR(t *testing.T, repoPath, filename, message string) {
	t.Helper()
	filePath := filepath.Join(repoPath, filename)
	if err := os.WriteFile(filePath, []byte(message+"\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", filePath, err)
	}
	runGitCmdR(t, repoPath, "add", filename)
	runGitCmdR(t, repoPath, "commit", "-m", message)
}
