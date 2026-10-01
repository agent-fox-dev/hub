package gitcmd

import (
	"context"
)

// WorktreeAdd creates a detached linked worktree at path, checked out at
// commit: `git worktree add --detach <path> <commit>`.
//
// git resolves a relative path against the runner's working directory, so
// callers should pass an absolute path.
func (r *GitRunner) WorktreeAdd(ctx context.Context, path, commit string) error {
	_, err := r.Run(ctx, "worktree", "add", "--detach", endOfOptions, path, commit)
	return err
}

// WorktreeRemove force-removes the linked worktree at path, discarding any
// uncommitted or in-progress (cherry-pick, merge) state:
// `git worktree remove --force <path>`.
func (r *GitRunner) WorktreeRemove(ctx context.Context, path string) error {
	_, err := r.Run(ctx, "worktree", "remove", "--force", endOfOptions, path)
	return err
}

// WorktreePrune drops registrations of linked worktrees whose directories no
// longer exist: `git worktree prune`.
func (r *GitRunner) WorktreePrune(ctx context.Context) error {
	_, err := r.Run(ctx, "worktree", "prune")
	return err
}
