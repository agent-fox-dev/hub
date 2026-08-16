package gitcmd

import "context"

// CherryPick applies a single commit onto the current HEAD by running
// `git cherry-pick <sha>` with the standard safety environment variables.
//
// On success, returns the new HEAD SHA and nil error.
//
// If the cherry-pick encounters a conflict, CherryPick automatically runs
// `git cherry-pick --abort` to restore the repository to a clean state,
// then returns ("", *CherryPickConflictError) with ConflictingFiles populated
// via parseRebaseConflictFiles.
//
// If the context is cancelled before git cherry-pick completes, CherryPick
// returns ("", ctx.Err()) without running git cherry-pick --abort; the caller
// is responsible for cleanup.
//
// If sha is empty, CherryPick returns ("", *GitError) without invoking the
// git subprocess.
func (r *GitRunner) CherryPick(ctx context.Context, sha string) (string, error) {
	// TODO: implement in task group 8
	return "", nil
}
