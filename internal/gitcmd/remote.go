package gitcmd

import "context"

// RemoteAdd adds a named remote to the repository by running
// `git remote add <name> <url>` with the standard safety environment
// variables.
//
// Returns nil on success. Returns a *GitError if git remote add exits with
// a non-zero code (e.g. remote already exists). Returns ctx.Err() if the
// context is cancelled before the subprocess completes.
//
// If name or url is an empty string, RemoteAdd returns a *GitError without
// invoking the git subprocess.
func (r *GitRunner) RemoteAdd(ctx context.Context, name, url string) error {
	// TODO: implement in task group 9
	return nil
}
