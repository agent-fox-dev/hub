package gitcmd

import "context"

// CreateBranch creates a new local branch at the specified startPoint by
// running `git branch <name> <startPoint>` with the standard safety
// environment variables.
//
// Returns nil on success. Returns a *GitError if git branch exits with a
// non-zero code (e.g. branch already exists). Returns ctx.Err() if the
// context is cancelled before the subprocess completes.
//
// If name or startPoint is empty, CreateBranch returns a *GitError without
// invoking the git subprocess.
func (r *GitRunner) CreateBranch(ctx context.Context, name, startPoint string) error {
	if name == "" || startPoint == "" {
		return &GitError{
			Args:     []string{"branch", name, startPoint},
			ExitCode: -1,
			Stderr:   "name and startPoint must not be empty",
		}
	}
	_, err := r.Run(ctx, "branch", endOfOptions, name, startPoint)
	return err
}
