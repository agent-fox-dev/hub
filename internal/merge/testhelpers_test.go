package merge

import (
	"database/sql"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
	_ "modernc.org/sqlite"
)

// openTestDB opens an in-memory SQLite database configured with WAL mode
// and busy_timeout=5000.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory database: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("failed to set WAL mode: %v", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		t.Fatalf("failed to set busy_timeout: %v", err)
	}
	return db
}

// newTestQueue opens a test database, initializes the jobqueue schema
// (including the group_key migration), and returns a new Queue along
// with the underlying *sql.DB for direct queries.
func newTestQueue(t *testing.T) (*jobqueue.Queue, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	if err := jobqueue.InitSchema(db); err != nil {
		t.Fatalf("InitSchema() returned error: %v", err)
	}
	if err := jobqueue.MigrateGroupKey(db); err != nil {
		t.Fatalf("MigrateGroupKey() returned error: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(testWriter{t}, nil))
	q, err := jobqueue.New(db, logger)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	return q, db
}

// testWriter adapts *testing.T to io.Writer for slog output in tests.
type testWriter struct {
	t *testing.T
}

func (tw testWriter) Write(p []byte) (int, error) {
	tw.t.Helper()
	tw.t.Log(string(p))
	return len(p), nil
}

// rollbackCall records a call to the RollbackFunc.
type rollbackCall struct {
	trunkDir string
	branch   string
	sha      string
}

// ===========================================================================
// Git repo setup helpers for handler tests
// ===========================================================================

// runGitCmd executes a git command in the specified directory and returns
// trimmed stdout. It fails the test on non-zero exit.
func runGitCmd(t *testing.T, dir string, args ...string) string {
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

// configGitUserCmd sets user.name and user.email in the given repo directory.
func configGitUserCmd(t *testing.T, dir string) {
	t.Helper()
	runGitCmd(t, dir, "config", "user.name", "Test User")
	runGitCmd(t, dir, "config", "user.email", "test@example.com")
}

// writeFileHelper creates or overwrites a file with the given content.
func writeFileHelper(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// setupWorkspaceRepo creates a git repository at <workspaceRoot>/<slug>/trunk/
// with these branches:
//   - main:                 has a base commit, a divergent commit, and the
//     merge commit of feature/already-merged
//   - feature/conflict:     modifies base.txt differently from main (conflicts)
//   - feature/already-merged: has a commit that was merged into main
//   - feature/not-ready:    at the same commit as the fork point (no work done)
//   - feature/clean:        adds a new file (clean merge with main)
//   - feature/a:            adds a new file (clean merge with main)
//
// Returns the trunk directory path.
func setupWorkspaceRepo(t *testing.T, workspaceRoot, slug string) string {
	t.Helper()
	trunkDir := filepath.Join(workspaceRoot, slug, "trunk")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		t.Fatalf("mkdir trunk: %v", err)
	}

	// Initialize the repo.
	runGitCmd(t, "", "init", "-b", "main", trunkDir)
	configGitUserCmd(t, trunkDir)

	// Base commit on main.
	writeFileHelper(t, filepath.Join(trunkDir, "base.txt"), "base content")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "base commit")

	// Create feature/conflict: modify base.txt to conflict with main.
	runGitCmd(t, trunkDir, "checkout", "-b", "feature/conflict")
	writeFileHelper(t, filepath.Join(trunkDir, "base.txt"), "conflict modification from feature")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "conflicting change")

	// Go back to main and add a divergent change to base.txt.
	runGitCmd(t, trunkDir, "checkout", "main")
	writeFileHelper(t, filepath.Join(trunkDir, "base.txt"), "main modification")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "main divergent change")

	// Create feature/already-merged: add a new file, then merge into main.
	runGitCmd(t, trunkDir, "checkout", "-b", "feature/already-merged")
	writeFileHelper(t, filepath.Join(trunkDir, "merged-feature.txt"), "already merged content")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "feature to be merged")
	runGitCmd(t, trunkDir, "checkout", "main")
	runGitCmd(t, trunkDir, "merge", "--no-ff", "feature/already-merged", "-m", "merge feature/already-merged")

	// Create feature/clean: non-conflicting change.
	runGitCmd(t, trunkDir, "checkout", "main")
	runGitCmd(t, trunkDir, "checkout", "-b", "feature/clean")
	writeFileHelper(t, filepath.Join(trunkDir, "clean-feature.txt"), "clean feature content")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "clean feature change")

	// Create feature/a: another non-conflicting branch.
	runGitCmd(t, trunkDir, "checkout", "main")
	runGitCmd(t, trunkDir, "checkout", "-b", "feature/a")
	writeFileHelper(t, filepath.Join(trunkDir, "feature-a.txt"), "feature a content")
	runGitCmd(t, trunkDir, "add", ".")
	runGitCmd(t, trunkDir, "commit", "-m", "feature a change")

	// Create feature/not-ready: at the current main HEAD with NO additional
	// commits. Source HEAD == target HEAD → BranchNotReady.
	runGitCmd(t, trunkDir, "checkout", "main")
	runGitCmd(t, trunkDir, "checkout", "-b", "feature/not-ready")
	// Don't add any commits — feature/not-ready points to the same commit
	// as main, representing a branch with no work done.

	// Return to main so the repo is in a known state.
	runGitCmd(t, trunkDir, "checkout", "main")

	return trunkDir
}
