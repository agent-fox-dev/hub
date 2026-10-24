package gitcmd

import "context"

// Checkout switches the working tree to the specified ref (branch name or
// commit SHA). It runs `git checkout <ref>` with the standard safety
// environment variables.
//
// Returns nil on success. Returns a *GitError if git checkout exits with a
// non-zero code. Returns ctx.Err() if the context is cancelled before the
// subprocess completes.
//
// If ref is empty, Checkout returns a *GitError without invoking the git
// subprocess.
func (r *GitRunner) Checkout(ctx context.Context, ref string) error {
	if ref == "" {
		return &GitError{
			Args:     []string{"checkout"},
			ExitCode: -1,
			Stderr:   "ref must not be empty",
		}
	}
	// git checkout does not accept --end-of-options; reject option-like refs
	// outright and terminate the ref list with "--" so it is never parsed as
	// a pathspec either.
	if err := ValidateRefName(ref); err != nil {
		return &GitError{Args: []string{"checkout", ref}, ExitCode: -1, Stderr: err.Error()}
	}
	_, err := r.Run(ctx, "checkout", ref, "--")
	return err
}
