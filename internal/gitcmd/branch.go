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
	_, err := r.Run(ctx, "branch", name, startPoint)
	return err
}

// DeleteBranch force-deletes a local branch regardless of merge status by
// running `git branch -D <name>` with the standard safety environment
// variables.
//
// Returns nil on success. Returns a *GitError if git branch -D exits with
// a non-zero code (e.g. branch does not exist). Returns ctx.Err() if the
// context is cancelled before the subprocess completes.
//
// If name is empty, DeleteBranch returns a *GitError without invoking the
// git subprocess.
func (r *GitRunner) DeleteBranch(ctx context.Context, name string) error {
	if name == "" {
		return &GitError{
			Args:     []string{"branch", "-D"},
			ExitCode: -1,
			Stderr:   "branch name must not be empty",
		}
	}
	_, err := r.Run(ctx, "branch", "-D", name)
	return err
}
