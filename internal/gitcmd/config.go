package gitcmd

import "context"

// ConfigSet sets a repository-local git config value by running
// `git config <key> <value>` with the standard safety environment variables.
//
// Returns nil on success. Returns a *GitError if git config exits with a
// non-zero code. Returns ctx.Err() if the context is cancelled before the
// subprocess completes.
//
// If key or value is an empty string, ConfigSet returns a *GitError without
// invoking the git subprocess.
func (r *GitRunner) ConfigSet(ctx context.Context, key, value string) error {
	if key == "" || value == "" {
		return &GitError{
			Args:     []string{"config", key, value},
			ExitCode: -1,
			Stderr:   "key and value must not be empty",
		}
	}
	_, err := r.Run(ctx, "config", key, value)
	return err
}
