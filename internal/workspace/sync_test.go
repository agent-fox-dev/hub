package workspace

import (
	"bytes"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ========================================================================
// Spec 14 Task 5.1: Sync working tree reset after fast-forward
// (TS-14-30)
// Requirements: 14-REQ-14.1
// ========================================================================

// TS-14-30: After sync fast-forward advances the local branch ref,
// worktree.Reset with HardReset updates on-disk files to match the new HEAD.
//
// Preconditions:
//   - A real git repository is initialised in a temp directory with two commits
//   - The local ref points to the first commit
//   - defaultSyncUpdateLocalRefFn is accessible for testing (same package)
//
// Requirement: 14-REQ-14.1
func TestSyncWorktreeReset_FastForwardUpdatesOnDiskFiles(t *testing.T) {
	// Create a real git repo with two commits.
	dir := t.TempDir()
	syncGit(t, "", "init", "-b", "main", dir)
	syncGit(t, dir, "config", "user.name", "Test User")
	syncGit(t, dir, "config", "user.email", "test@example.com")

	// First commit: tracked file with initial content.
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("initial content"), 0o644); err != nil {
		t.Fatalf("write tracked.txt: %v", err)
	}
	syncGit(t, dir, "add", ".")
	syncGit(t, dir, "commit", "-m", "first commit")
	firstSHA := syncGit(t, dir, "rev-parse", "HEAD")

	// Second commit: update the tracked file.
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("updated content"), 0o644); err != nil {
		t.Fatalf("write tracked.txt: %v", err)
	}
	syncGit(t, dir, "add", ".")
	syncGit(t, dir, "commit", "-m", "second commit")
	secondSHA := syncGit(t, dir, "rev-parse", "HEAD")

	if firstSHA == secondSHA {
		t.Fatalf("first and second commit SHAs must differ; both are %s", firstSHA)
	}

	// Reset HEAD back to first commit so working tree reflects old state.
	syncGit(t, dir, "reset", "--hard", firstSHA)

	// Sanity: verify working tree is at first commit.
	content, err := os.ReadFile(filepath.Join(dir, "tracked.txt"))
	if err != nil {
		t.Fatalf("read tracked.txt after reset: %v", err)
	}
	if string(content) != "initial content" {
		t.Fatalf("tracked.txt = %q after reset; want %q", string(content), "initial content")
	}

	// Act: call defaultSyncUpdateLocalRefFn to advance ref to second commit.
	err = defaultSyncUpdateLocalRefFn(dir, nil, secondSHA)
	if err != nil {
		t.Fatalf("defaultSyncUpdateLocalRefFn returned error: %v", err)
	}

	// Assert: on-disk file content must match the second commit (14-REQ-14.1).
	// The function must call worktree.Reset with HardReset after advancing the
	// ref, so on-disk files reflect the new HEAD immediately.
	content, err = os.ReadFile(filepath.Join(dir, "tracked.txt"))
	if err != nil {
		t.Fatalf("read tracked.txt after sync: %v", err)
	}
	if string(content) != "updated content" {
		t.Errorf("tracked.txt = %q; want %q (working tree must match new HEAD after sync fast-forward)",
			string(content), "updated content")
	}

	// Assert: git status --porcelain shows clean working tree.
	status := syncGit(t, dir, "status", "--porcelain")
	if status != "" {
		t.Errorf("git status --porcelain = %q; want empty (clean working tree after reset)", status)
	}

	// Assert: HEAD now points to the second commit.
	head := syncGit(t, dir, "rev-parse", "HEAD")
	if head != secondSHA {
		t.Errorf("HEAD = %q; want %q", head, secondSHA)
	}
}

// ========================================================================
// Spec 14 Task 5.2: Sync working tree reset failure (best-effort)
// (TS-14-31)
// Requirements: 14-REQ-14.2
// Property: 14-PROP-4
// ========================================================================

// TS-14-31: When worktree.Reset fails after a successful ref update,
// defaultSyncUpdateLocalRefFn logs the error and returns nil.
//
// The test makes the working tree directory read-only to prevent file
// creation during worktree.Reset, while keeping .git writable for the ref
// update. The function must:
//   - return nil (the ref update is already committed; 14-PROP-4)
//   - emit an error-level log about the reset failure (14-REQ-14.2)
//
// Requirement: 14-REQ-14.2
// Property: 14-PROP-4 (worktree.Reset failure never fails the sync)
func TestSyncWorktreeReset_FailureDoesNotFailSync(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-based reset failure test not reliable on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("cannot test permission denial as root")
	}

	// Create a real git repo with two commits.
	dir := t.TempDir()
	syncGit(t, "", "init", "-b", "main", dir)
	syncGit(t, dir, "config", "user.name", "Test User")
	syncGit(t, dir, "config", "user.email", "test@example.com")

	// First commit.
	if err := os.WriteFile(filepath.Join(dir, "existing.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatalf("write existing.txt: %v", err)
	}
	syncGit(t, dir, "add", ".")
	syncGit(t, dir, "commit", "-m", "first commit")

	// Second commit adds a NEW file (worktree.Reset must create it on disk).
	if err := os.WriteFile(filepath.Join(dir, "existing.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatalf("write existing.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "newfile.txt"), []byte("new content"), 0o644); err != nil {
		t.Fatalf("write newfile.txt: %v", err)
	}
	syncGit(t, dir, "add", ".")
	syncGit(t, dir, "commit", "-m", "second commit")
	secondSHA := syncGit(t, dir, "rev-parse", "HEAD")

	// Reset to first commit (removes newfile.txt from working tree).
	syncGit(t, dir, "reset", "--hard", "HEAD~1")

	// Make root directory read-only to cause worktree.Reset failure.
	// The .git subdirectory remains writable (Unix permissions are per-directory),
	// so PlainOpen and SetReference succeed. Creating newfile.txt in the
	// read-only root directory will fail during worktree.Reset.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	// Capture log output to verify error logging.
	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	defer log.SetOutput(os.Stderr)

	// Act: call defaultSyncUpdateLocalRefFn.
	err := defaultSyncUpdateLocalRefFn(dir, nil, secondSHA)

	// Assert: function must return nil (14-REQ-14.2, 14-PROP-4).
	// A worktree.Reset failure after a successful ref update must NOT
	// cause the sync operation to fail.
	if err != nil {
		t.Errorf("defaultSyncUpdateLocalRefFn returned %v; want nil "+
			"(worktree.Reset failure must not fail sync — 14-PROP-4)", err)
	}

	// Assert: an error-level log was emitted about the reset failure (14-REQ-14.2).
	logOutput := logBuf.String()
	if logOutput == "" {
		t.Error("no log output emitted; want error-level log about worktree reset failure (14-REQ-14.2)")
	}
}

// syncGit runs a git command and returns trimmed stdout. Fails the test on error.
func syncGit(t *testing.T, dir string, args ...string) string {
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
