package gitcmd

import (
	"context"
	"log"
	"os"
	"path/filepath"
)

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
	// 14-REQ-9.E1: empty branch returns *GitError without invoking subprocess.
	if branch == "" {
		return "", &GitError{
			Args:     []string{"merge", "--no-ff"},
			ExitCode: -1,
			Stderr:   "branch must not be empty",
		}
	}

	// Run git merge --no-ff <branch> via runWithExitCode for exit-code discrimination.
	args := []string{"merge", "--no-ff", branch}
	stdout, exitCode, stderr, err := r.runWithExitCode(ctx, args...)
	if err != nil {
		// 14-REQ-9.3, 14-REQ-9.E4: context cancellation or deadline exceeded —
		// return the context error without running git merge --abort.
		return "", err
	}

	if exitCode != 0 {
		// Determine whether this is a conflict or a different error.
		// Check for MERGE_HEAD which git creates on conflict.
		mergeHead := filepath.Join(r.workDir, ".git", "MERGE_HEAD")
		_, statErr := os.Stat(mergeHead)
		isConflict := statErr == nil

		// Also check combined output for CONFLICT lines.
		combinedOutput := stdout + "\n" + stderr
		conflictFiles := parseRebaseConflictFiles(combinedOutput)
		if len(conflictFiles) > 0 {
			isConflict = true
		}

		if isConflict {
			// 14-REQ-9.2: parse conflicting files with fallback (14-REQ-9.E2, 14-PROP-6).
			files := parseConflictFilesWithFallback(combinedOutput)

			// 14-PROP-2: auto-abort to restore clean state before returning.
			_, abortErr := r.Run(ctx, "merge", "--abort")
			if abortErr != nil {
				// 14-REQ-9.E3: log abort failure, still return MergeNoFFConflictError.
				log.Printf("gitcmd: merge --abort failed after conflict: %v", abortErr)
			}

			return "", &MergeNoFFConflictError{ConflictingFiles: files}
		}

		// Non-conflict error: return *GitError.
		return "", &GitError{
			Args:     args,
			ExitCode: exitCode,
			Stderr:   stderr,
		}
	}

	// 14-REQ-9.1: merge succeeded — read the new HEAD SHA (the merge commit).
	newSHA, err := r.RevParse(ctx, "HEAD")
	if err != nil {
		return "", err
	}

	return newSHA, nil
}

// MergeAbort runs `git merge --abort` to abort a merge that is in progress.
//
// Returns nil on success, or a *GitError wrapping the exit code and stderr
// on failure (including when no merge is in progress).
func (r *GitRunner) MergeAbort(ctx context.Context) error {
	_, err := r.Run(ctx, "merge", "--abort")
	return err
}
