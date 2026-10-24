package carrypatch

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
)

// CleanupStaleRebuildWorktrees removes the rebuild worktrees left behind by a
// hub that died mid-run (01-REQ-8.3). For every <workspaceRoot>/*/rebuild
// directory it deletes the directory and then runs `git worktree prune` in the
// matching <workspaceRoot>/<slug>/trunk, which drops the registrations whose
// directories are gone. Each removal is logged at info level with the slug
// and path.
//
// It is meant to run once at startup, before the job queue starts, so that no
// rebuild is using one of those directories. It never touches the trunk's
// checkout and never fails: a missing trunk is skipped with a warning
// (01-REQ-8.4), and removal or prune errors are logged and the remaining
// workspaces are still processed (01-REQ-8.5).
func CleanupStaleRebuildWorktrees(ctx context.Context, workspaceRoot string, logger *slog.Logger) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if workspaceRoot == "" {
		return
	}
	entries, err := os.ReadDir(workspaceRoot)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warn("stale rebuild worktree cleanup: cannot list workspace root",
				"path", workspaceRoot, "error", err)
		}
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		slug := entry.Name()
		rebuildDir := filepath.Join(workspaceRoot, slug, "rebuild")
		if info, err := os.Stat(rebuildDir); err != nil || !info.IsDir() {
			continue
		}

		if err := os.RemoveAll(rebuildDir); err != nil {
			logger.Warn("stale rebuild worktree cleanup: cannot remove rebuild directory",
				"slug", slug, "path", rebuildDir, "error", err)
		} else {
			logger.Info("removed stale rebuild worktree directory", "slug", slug, "path", rebuildDir)
		}

		trunk := filepath.Join(workspaceRoot, slug, "trunk")
		if info, err := os.Stat(trunk); err != nil || !info.IsDir() {
			logger.Warn("stale rebuild worktree cleanup: trunk is missing; skipping worktree prune",
				"slug", slug, "path", rebuildDir, "trunk", trunk)
			continue
		}
		runner, err := NewGitRunnerFactory()(trunk)
		if err != nil {
			logger.Warn("stale rebuild worktree cleanup: cannot create git runner; skipping worktree prune",
				"slug", slug, "path", rebuildDir, "error", err)
			continue
		}
		if err := runner.WorktreePrune(ctx); err != nil {
			logger.Warn("stale rebuild worktree cleanup: worktree prune failed",
				"slug", slug, "path", rebuildDir, "error", err)
			continue
		}
		logger.Info("pruned stale rebuild worktree registrations", "slug", slug, "path", trunk)
	}
}
