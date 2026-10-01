package carrypatch

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ===========================================================================
// Spec 01, task 8: startup cleanup of stale rebuild worktrees and the per-run
// cleanup of orphaned directories.
// ===========================================================================

// addRegisteredWorktree registers a detached linked worktree at path in the
// trunk, as a crashed rebuild would have left behind.
func addRegisteredWorktree(t *testing.T, trunk, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, trunk, "worktree", "add", "--detach", path, "HEAD")
}

func worktreeLines(t *testing.T, trunk string) int {
	t.Helper()
	return len(splitNonEmpty(runGitCmd(t, trunk, "worktree", "list")))
}

func pathGone(path string) bool {
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

// TS-01-56: every <root>/*/rebuild directory is removed, pruned in the
// matching trunk and logged; main.go calls the function before
// mergeQueue.Start(). Requirement: 01-REQ-8.3
func TestStaleCleanup_TS_01_56_RemovesPrunesLogsAndMainCallsItFirst(t *testing.T) {
	root := t.TempDir()
	logs := &capturedLog{}
	type ws struct{ slug, trunk, rebuild, wt string }
	var all []ws
	for _, slug := range []string{"ws-a", "ws-b"} {
		trunk, _ := setupCarryPatchRepo(t, root, slug)
		rebuild := filepath.Join(root, slug, "rebuild")
		wt := filepath.Join(rebuild, "job-1")
		addRegisteredWorktree(t, trunk, wt)
		if n := worktreeLines(t, trunk); n != 2 {
			t.Fatalf("fixture: %s has %d worktrees, want 2", slug, n)
		}
		all = append(all, ws{slug, trunk, rebuild, wt})
	}

	CleanupStaleRebuildWorktrees(context.Background(), root, slog.New(logs))

	for _, w := range all {
		if !pathGone(w.rebuild) {
			t.Errorf("%s: rebuild directory still exists", w.slug)
		}
		if n := worktreeLines(t, w.trunk); n != 1 {
			t.Errorf("%s: git worktree list shows %d entries, want only the trunk:\n%s", w.slug, n,
				runGitCmd(t, w.trunk, "worktree", "list"))
		}
		if !logs.has(slog.LevelInfo, w.slug, w.rebuild) {
			t.Errorf("%s: no info log naming slug and path %s", w.slug, w.rebuild)
		}
	}

	src, err := os.ReadFile(filepath.Join("..", "..", "cmd", "af-hub", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	call := strings.Index(s, "CleanupStaleRebuildWorktrees")
	start := strings.Index(s, "mergeQueue.Start()")
	if call < 0 || start < 0 || call > start {
		t.Errorf("main.go must call CleanupStaleRebuildWorktrees before mergeQueue.Start() (call=%d start=%d)", call, start)
	}
}

// TS-01-57: a workspace without a trunk is skipped for prune with a warning,
// the others are still cleaned. Requirement: 01-REQ-8.4
func TestStaleCleanup_TS_01_57_MissingTrunkSkipped(t *testing.T) {
	root := t.TempDir()
	logs := &capturedLog{}

	aRebuild := filepath.Join(root, "ws-a", "rebuild")
	if err := os.MkdirAll(filepath.Join(aRebuild, "job-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	bTrunk, _ := setupCarryPatchRepo(t, root, "ws-b")
	bRebuild := filepath.Join(root, "ws-b", "rebuild")
	addRegisteredWorktree(t, bTrunk, filepath.Join(bRebuild, "job-1"))

	CleanupStaleRebuildWorktrees(context.Background(), root, slog.New(logs))

	if !logs.has(slog.LevelWarn, "ws-a") {
		t.Error("expected a warning naming workspace ws-a")
	}
	if !pathGone(aRebuild) {
		t.Error("ws-a rebuild directory should be removed even without a trunk")
	}
	if !pathGone(bRebuild) {
		t.Error("ws-b rebuild directory should be removed")
	}
	if n := worktreeLines(t, bTrunk); n != 1 {
		t.Errorf("ws-b: %d worktrees registered after cleanup, want 1", n)
	}
}

// TS-01-58: removal or prune failures are logged and never stop the
// cleanup. Requirement: 01-REQ-8.5
func TestStaleCleanup_TS_01_58_FailuresLoggedAndContinue(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission failures cannot be provoked as root")
	}
	root := t.TempDir()
	logs := &capturedLog{}

	// ws-1-ro: its rebuild directory cannot be removed (read-only parent).
	roTrunk, _ := setupCarryPatchRepo(t, root, "ws-1-ro")
	roRebuild := filepath.Join(root, "ws-1-ro", "rebuild")
	addRegisteredWorktree(t, roTrunk, filepath.Join(roRebuild, "job-1"))
	roParent := filepath.Join(root, "ws-1-ro")
	if err := os.Chmod(roParent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roParent, 0o755) })

	// ws-2-noprune: the trunk is not a repository, so prune fails.
	npRebuild := filepath.Join(root, "ws-2-noprune", "rebuild")
	if err := os.MkdirAll(filepath.Join(npRebuild, "job-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "ws-2-noprune", "trunk"), 0o755); err != nil {
		t.Fatal(err)
	}

	// ws-3-ok: processed normally after the failures.
	okTrunk, _ := setupCarryPatchRepo(t, root, "ws-3-ok")
	okRebuild := filepath.Join(root, "ws-3-ok", "rebuild")
	addRegisteredWorktree(t, okTrunk, filepath.Join(okRebuild, "job-1"))

	CleanupStaleRebuildWorktrees(context.Background(), root, slog.New(logs))

	atLeastWarn := func(slug, path string) bool {
		return logs.has(slog.LevelWarn, slug, path) || logs.has(slog.LevelError, slug, path)
	}
	if !atLeastWarn("ws-1-ro", roRebuild) {
		t.Error("expected a warn/error entry naming ws-1-ro and its rebuild path")
	}
	if !atLeastWarn("ws-2-noprune", npRebuild) {
		t.Error("expected a warn/error entry naming ws-2-noprune and its rebuild path")
	}
	if !pathGone(npRebuild) {
		t.Error("ws-2-noprune: the directory should still be removed when prune fails")
	}
	if !pathGone(okRebuild) {
		t.Error("ws-3-ok must still be cleaned after earlier failures")
	}
	if n := worktreeLines(t, okTrunk); n != 1 {
		t.Errorf("ws-3-ok: %d worktrees registered, want 1", n)
	}
}

// A root that does not exist, or that holds no rebuild directories, is not an
// error.
func TestStaleCleanup_MissingRootIsNoop(t *testing.T) {
	CleanupStaleRebuildWorktrees(context.Background(), filepath.Join(t.TempDir(), "absent"), nil)
	CleanupStaleRebuildWorktrees(context.Background(), t.TempDir(), nil)
}

// TS-01-59: a registered-but-orphaned worktree, a registered worktree whose
// directory is gone and a plain stale directory are all removed before the
// next rebuild, which succeeds. Requirement: 01-REQ-8.6
func TestStaleCleanup_TS_01_59_OrphansRemovedBeforeNextRebuild(t *testing.T) {
	e := newRealEnv(t)
	rebuild := filepath.Join(e.root, e.slug, "rebuild")

	orphan := filepath.Join(rebuild, "orphan")
	addRegisteredWorktree(t, e.trunk, orphan)
	deleted := filepath.Join(rebuild, "deleted")
	addRegisteredWorktree(t, e.trunk, deleted)
	if err := os.RemoveAll(deleted); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(rebuild, "plain")
	writeFileHelper(t, filepath.Join(plain, "junk.txt"), "stale")
	if n := e.worktreeCount(); n != 3 {
		t.Fatalf("fixture: %d registrations, want trunk + 2", n)
	}

	e.setPatches("feature/patch-a", "feature/patch-b")
	res, _, err := e.run(context.Background(), "job-59", StrategyRebase, FailModeFailFast)
	if err != nil {
		t.Fatalf("rebuild after a crash must succeed: %v", err)
	}
	if got := res.(*RebuildResult).PatchesApplied; got != 2 {
		t.Errorf("PatchesApplied = %d, want 2", got)
	}
	if n := e.worktreeCount(); n != 1 {
		t.Errorf("git worktree list shows %d entries, want only the trunk:\n%s", n,
			runGitCmd(t, e.trunk, "worktree", "list"))
	}
	entries, err := os.ReadDir(rebuild)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("rebuild/ still holds %d entries", len(entries))
	}
	for _, p := range []string{orphan, plain} {
		if !pathGone(p) {
			t.Errorf("%s still exists", p)
		}
	}
	if !e.logs.has(slog.LevelInfo, e.slug, orphan) {
		t.Error("expected an info log for the removal of the orphaned worktree directory")
	}
}
