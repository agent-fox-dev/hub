package gitcmd

import "context"

// MergeNoFF merges a branch with --no-ff and returns the resulting merge
// commit SHA on success, or a typed error on conflict or failure.
//
// On success, returns (mergeCommitSHA, nil) where mergeCommitSHA is the
// 40-character hex SHA of the new merge commit.
//
// If the merge encounters a conflict, MergeNoFF automatically runs
// `git merge --abort` to restore the repository to a clean state, then
// returns ("", *MergeNoFFConflictError) with ConflictingFiles populated
// via parseRebaseConflictFiles.
//
// If the context is cancelled before git merge --no-ff completes, MergeNoFF
// returns ("", ctx.Err()) without running git merge --abort; the caller is
// responsible for cleanup.
//
// If branch is empty, MergeNoFF returns ("", *GitError) without invoking
// the git subprocess.
func (r *GitRunner) MergeNoFF(ctx context.Context, branch string) (string, error) {
	// TODO: implement in task group 10
	return "", nil
}

// MergeAbort runs `git merge --abort` to abort a merge that is in progress.
//
// Returns nil on success, or a *GitError wrapping the exit code and stderr
// on failure (including when no merge is in progress).
func (r *GitRunner) MergeAbort(ctx context.Context) error {
	// TODO: implement in task group 10
	return nil
}
