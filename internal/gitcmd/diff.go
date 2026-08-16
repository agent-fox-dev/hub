package gitcmd

import "context"

// Diff runs `git diff <args...>` with the standard safety environment
// variables and returns the raw stdout output.
//
// Returns (rawStdout, nil) on success. Returns ("", *GitError) if git diff
// exits with a non-zero code. Returns ("", ctx.Err()) if the context is
// cancelled before the subprocess completes.
//
// When called with no args, Diff runs `git diff` with no additional arguments.
func (r *GitRunner) Diff(ctx context.Context, args ...string) (string, error) {
	cmdArgs := append([]string{"diff"}, args...)
	return r.Run(ctx, cmdArgs...)
}
