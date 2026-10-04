package carrypatch

import (
	"context"
	"log/slog"
	"strings"
	"testing"
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
