package carrypatch

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/gitcmd"
)

// ===========================================================================
// TS-23-44 (integration): RemoveBackup deletes refs/hub/replaced/<branch>
// ===========================================================================

func TestRecoveryRemoveBackup_DeletesRef_TS2344(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "rm-backup-int"

	_, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	branch := "feature/rm-int"
	runGitCmd(t, trunkDir, "checkout", "-b", branch)
	commitFile(t, trunkDir, "rm.txt", "rm content", "rm commit")
	commitSHA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "update-ref", "refs/hub/replaced/"+branch, commitSHA)
	runGitCmd(t, trunkDir, "checkout", "main")

	// Verify the backup ref exists.
	before := revParse(t, trunkDir, "refs/hub/replaced/"+branch)
	if before == "" {
		t.Fatal("backup ref should exist before removal")
	}

	svc := &RecoveryService{
		NewGitRunner:  NewGitRunnerFactory(),
		WorkspaceRoot: workspaceRoot,
	}

	ctx := context.Background()
	err := svc.RemoveBackup(ctx, slug, branch)
	if err != nil {
		t.Fatalf("RemoveBackup error: %v", err)
	}

	// Verify the backup ref is gone.
	after := revParse(t, trunkDir, "refs/hub/replaced/"+branch)
	if after != "" {
		t.Errorf("backup ref should be deleted, got %q", after)
	}
}

// ===========================================================================
// TS-23-45 (integration): RemoveBackup with no backup ref is a silent success
// ===========================================================================

func TestRecoveryRemoveBackup_NoRef_SilentSuccess_TS2345(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "rm-no-ref"

	_, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	// No backup ref exists.
	before := revParse(t, trunkDir, "refs/hub/replaced/feature/no-ref")
	if before != "" {
		t.Fatal("backup ref should not exist")
	}

	// Capture log output.
	var logBuf strings.Builder
	handler := slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	svc := &RecoveryService{
		NewGitRunner:  NewGitRunnerFactory(),
		WorkspaceRoot: workspaceRoot,
	}

	ctx := context.Background()
	err := svc.RemoveBackup(ctx, slug, "feature/no-ref")
	if err != nil {
		t.Fatalf("RemoveBackup error: %v; want nil for missing ref", err)
	}

	// No warn-level log.
	logStr := logBuf.String()
	if strings.Contains(logStr, "level=WARN") {
		t.Errorf("expected no WARN log, got: %s", logStr)
	}
}

// ===========================================================================
// TS-23-46 (integration): RemoveBackup with missing trunk succeeds with info log
// ===========================================================================

func TestRecoveryRemoveBackup_MissingTrunk_InfoLog_TS2346(t *testing.T) {
	workspaceRoot := t.TempDir()

	// Capture log output.
	var logBuf strings.Builder
	handler := slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	svc := &RecoveryService{
		NewGitRunner:  NewGitRunnerFactory(),
		WorkspaceRoot: workspaceRoot,
	}

	ctx := context.Background()
	err := svc.RemoveBackup(ctx, "nonexistent-slug", "feature/missing")
	if err != nil {
		t.Fatalf("RemoveBackup error: %v; want nil for missing trunk", err)
	}

	// Verify one info-level log entry about the missing trunk.
	logStr := logBuf.String()
	if !strings.Contains(logStr, "level=INFO") {
		t.Error("expected an INFO log entry for missing trunk")
	}
	if !strings.Contains(logStr, "trunk") {
		t.Error("expected INFO log to mention 'trunk'")
	}

	// No warn-level log.
	if strings.Contains(logStr, "level=WARN") {
		t.Errorf("expected no WARN log, got: %s", logStr)
	}
}

// ===========================================================================
// TS-23-48 (integration): Removal during a held wslock returns 204
// (verified at the carrypatch level: RemoveBackup does not take wslock)
// ===========================================================================

func TestRecoveryRemoveBackup_NoLockTaken_TS2348(t *testing.T) {
	workspaceRoot := t.TempDir()
	slug := "rm-no-lock"

	_, trunkDir, _ := setupForkAndTrunk(t, workspaceRoot, slug)

	branch := "feature/no-lock"
	runGitCmd(t, trunkDir, "checkout", "-b", branch)
	commitFile(t, trunkDir, "nolock.txt", "nolock", "nolock commit")
	commitSHA := runGitCmd(t, trunkDir, "rev-parse", "HEAD")
	runGitCmd(t, trunkDir, "update-ref", "refs/hub/replaced/"+branch, commitSHA)
	runGitCmd(t, trunkDir, "checkout", "main")

	svc := &RecoveryService{
		NewGitRunner:  NewGitRunnerFactory(),
		WorkspaceRoot: workspaceRoot,
		// Note: no LockFunc set — RemoveBackup should not need it.
	}

	ctx := context.Background()
	err := svc.RemoveBackup(ctx, slug, branch)
	if err != nil {
		t.Fatalf("RemoveBackup error: %v", err)
	}

	// Verify the backup ref is gone.
	after := revParse(t, trunkDir, "refs/hub/replaced/"+branch)
	if after != "" {
		t.Errorf("backup ref should be deleted, got %q", after)
	}
}

// ===========================================================================
// Issue #45 findings 3 and 8: one backup-ref removal behind patch deletion
// and the purge, and a log that says what actually happened
// ===========================================================================

// trunkWithBackup creates a workspace trunk with a branch and, when withBackup
// is set, a backup ref for it. It returns the trunk path and the backup SHA.
func trunkWithBackup(t *testing.T, workspaceRoot, slug, branch string, withBackup bool) (trunkDir, sha string) {
	t.Helper()
	_, trunkDir, _ = setupForkAndTrunk(t, workspaceRoot, slug)
	runGitCmd(t, trunkDir, "checkout", "-b", branch)
	sha = commitFile(t, trunkDir, "b.txt", "b", "b commit")
	if withBackup {
		runGitCmd(t, trunkDir, "update-ref", "refs/hub/replaced/"+branch, sha)
	}
	runGitCmd(t, trunkDir, "checkout", "main")
	return trunkDir, sha
}

func TestRemoveBackupRef_MissingRefLogsNoRemoval_Issue45(t *testing.T) {
	// git update-ref -d exits 0 for a ref that does not exist, so without a
	// lookup first a missing ref would be logged as removed.
	workspaceRoot := t.TempDir()
	const slug = "rm-missing"
	const branch = "feature/none"
	trunkDir, _ := trunkWithBackup(t, workspaceRoot, slug, branch, false)
	logs := captureLogs(t)

	svc := &RecoveryService{NewGitRunner: NewGitRunnerFactory(), WorkspaceRoot: workspaceRoot}
	if err := svc.RemoveBackup(context.Background(), slug, branch); err != nil {
		t.Fatalf("RemoveBackup error: %v", err)
	}

	if strings.Contains(logs.String(), "removed backup ref") {
		t.Errorf("a missing ref was logged as removed:\n%s", logs.String())
	}
	if got := revParse(t, trunkDir, "refs/hub/replaced/"+branch); got != "" {
		t.Errorf("backup ref = %q; want none", got)
	}
}

func TestRemoveBackupRef_PresentRefLogsSHAs_Issue45(t *testing.T) {
	// 23-REQ-10.7: the removal is logged at info level with slug, branch and
	// the SHA involved.
	workspaceRoot := t.TempDir()
	const slug = "rm-present"
	const branch = "feature/present"
	trunkDir, sha := trunkWithBackup(t, workspaceRoot, slug, branch, true)
	logs := captureLogs(t)

	svc := &RecoveryService{NewGitRunner: NewGitRunnerFactory(), WorkspaceRoot: workspaceRoot}
	if err := svc.RemoveBackup(context.Background(), slug, branch); err != nil {
		t.Fatalf("RemoveBackup error: %v", err)
	}

	if got := revParse(t, trunkDir, "refs/hub/replaced/"+branch); got != "" {
		t.Errorf("backup ref = %q; want it removed", got)
	}
	var line string
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, "removed backup ref") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no 'removed backup ref' log line:\n%s", logs.String())
	}
	if !strings.Contains(line, "level=INFO") {
		t.Errorf("removal not logged at info level: %s", line)
	}
	for _, want := range []string{slug, branch, "refs/hub/replaced/" + branch, sha} {
		if !strings.Contains(line, want) {
			t.Errorf("removal log should contain %q: %s", want, line)
		}
	}
}

func TestRemoveBackupRef_FailuresAreReturned_Issue45(t *testing.T) {
	workspaceRoot := t.TempDir()
	const slug = "rm-fail"
	if err := os.MkdirAll(filepath.Join(workspaceRoot, slug, "trunk"), 0o755); err != nil {
		t.Fatalf("mkdir trunk: %v", err)
	}
	const sha = "aabbccddee00112233445566778899aabbccddee"
	ctx := context.Background()

	factoryFor := func(run func(args ...string) (string, error)) (func(string) (GitRunner, error), *mockGitRunner) {
		m := newMockGitRunner()
		m.RunFunc = func(_ context.Context, args ...string) (string, error) { return run(args...) }
		return func(string) (GitRunner, error) { return m, nil }, m
	}
	deletes := func(m *mockGitRunner) int {
		n := 0
		for _, c := range m.RunCalls {
			if c.Args[0] == "update-ref" {
				n++
			}
		}
		return n
	}

	t.Run("lookup_failure", func(t *testing.T) {
		// A broken git must not be read as "nothing to remove".
		broken := &gitcmd.GitError{ExitCode: 128, Stderr: "fatal: not a git repository"}
		factory, m := factoryFor(func(...string) (string, error) { return "", broken })
		err := removeBackupRef(ctx, workspaceRoot, slug, "feature/x", factory)
		if !errors.Is(err, broken) {
			t.Fatalf("error = %v; want it to wrap the lookup failure", err)
		}
		if deletes(m) != 0 {
			t.Error("update-ref -d ran although the lookup failed")
		}
	})

	t.Run("delete_failure", func(t *testing.T) {
		denied := errors.New("permission denied")
		factory, _ := factoryFor(func(args ...string) (string, error) {
			if args[0] == "update-ref" {
				return "", denied
			}
			return sha, nil
		})
		logs := captureLogs(t)
		err := removeBackupRef(ctx, workspaceRoot, slug, "feature/x", factory)
		if !errors.Is(err, denied) {
			t.Fatalf("error = %v; want it to wrap the delete failure", err)
		}
		if strings.Contains(logs.String(), "removed backup ref") {
			t.Error("a failed delete was logged as a removal")
		}
	})

	t.Run("runner_construction_failure", func(t *testing.T) {
		err := removeBackupRef(ctx, workspaceRoot, slug, "feature/x",
			func(string) (GitRunner, error) { return nil, errRunnerBroken })
		if !errors.Is(err, errRunnerBroken) {
			t.Errorf("error = %v; want it to wrap %v", err, errRunnerBroken)
		}
	})

	t.Run("missing_ref_runs_no_delete", func(t *testing.T) {
		factory, m := factoryFor(func(...string) (string, error) { return "", &gitcmd.GitError{ExitCode: 1} })
		if err := removeBackupRef(ctx, workspaceRoot, slug, "feature/x", factory); err != nil {
			t.Fatalf("error = %v; want nil for a missing ref", err)
		}
		if deletes(m) != 0 {
			t.Error("update-ref -d ran for a ref that does not exist")
		}
	})
}

func TestPurge_UsesTheSharedBackupRemoval_Issue45(t *testing.T) {
	// The purge removes the ref through the same helper as patch deletion, so
	// it logs the removal with the removed SHA like RemoveBackup does.
	db := openTestDB(t)
	createPatchesTable(t, db)
	workspaceRoot := t.TempDir()
	const slug = "purge-shared"
	const branch = "feature/purge-shared"
	trunkDir, sha := trunkWithBackup(t, workspaceRoot, slug, branch, true)
	eightDaysAgo := time.Now().UTC().Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	seedPatchDeleted(t, db, "p-shared", slug, branch, -1, eightDaysAgo)
	logs := captureLogs(t)

	n, err := PurgeExpiredDeletedPatchesWithRefs(context.Background(), NewSQLPatchStore(db), workspaceRoot, NewGitRunnerFactory())
	if err != nil || n != 1 {
		t.Fatalf("purged = %d, err = %v; want 1, nil", n, err)
	}
	if got := revParse(t, trunkDir, "refs/hub/replaced/"+branch); got != "" {
		t.Errorf("backup ref = %q; want it removed", got)
	}
	if !strings.Contains(logs.String(), "removed backup ref") || !strings.Contains(logs.String(), sha) {
		t.Errorf("purge should log the removal with SHA %s:\n%s", sha, logs.String())
	}
}
