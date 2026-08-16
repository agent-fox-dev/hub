package gitcmd

import "context"

// Log runs `git log <args...>` with the standard safety environment
// variables and returns the raw stdout output.
//
// Returns (rawStdout, nil) on success. Returns ("", *GitError) if git log
// exits with a non-zero code. Returns ("", ctx.Err()) if the context is
// cancelled before the subprocess completes.
//
// When called with no args, Log runs `git log` with no additional arguments.
func (r *GitRunner) Log(ctx context.Context, args ...string) (string, error) {
	// TODO: implement in task group 9
	return "", nil
}
