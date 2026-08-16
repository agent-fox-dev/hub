package gitcmd

import (
	"context"
	"log"
	"os"
	"path/filepath"
)

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
	// 14-REQ-4.E1: empty SHA returns *GitError without invoking subprocess.
	if sha == "" {
		return "", &GitError{
			Args:     []string{"cherry-pick"},
			ExitCode: -1,
			Stderr:   "sha must not be empty",
		}
	}

	// Run git cherry-pick <sha> via runWithExitCode for exit-code discrimination.
	args := []string{"cherry-pick", sha}
	stdout, exitCode, stderr, err := r.runWithExitCode(ctx, args...)
	if err != nil {
		// 14-REQ-4.3, 14-REQ-4.E4: context cancellation or deadline exceeded —
		// return the context error without running cherry-pick --abort.
		return "", err
	}

	if exitCode != 0 {
		// Determine whether this is a conflict or a different error.
		// Check for CHERRY_PICK_HEAD which git creates on conflict.
		cherryPickHead := filepath.Join(r.workDir, ".git", "CHERRY_PICK_HEAD")
		_, statErr := os.Stat(cherryPickHead)
		isConflict := statErr == nil

		// Also check combined output for CONFLICT lines.
		combinedOutput := stdout + "\n" + stderr
		conflictFiles := parseRebaseConflictFiles(combinedOutput)
		if len(conflictFiles) > 0 {
			isConflict = true
		}

		if isConflict {
			// 14-REQ-4.2: parse conflicting files with fallback (14-REQ-4.E2, 14-PROP-6).
			files := parseConflictFilesWithFallback(combinedOutput)

			// 14-PROP-1: auto-abort to restore clean state before returning.
			_, abortErr := r.Run(ctx, "cherry-pick", "--abort")
			if abortErr != nil {
				// 14-REQ-4.E3: log abort failure, still return CherryPickConflictError.
				log.Printf("gitcmd: cherry-pick --abort failed after conflict: %v", abortErr)
			}

			return "", &CherryPickConflictError{ConflictingFiles: files}
		}

		// Non-conflict error: return *GitError.
		return "", &GitError{
			Args:     args,
			ExitCode: exitCode,
			Stderr:   stderr,
		}
	}

	// 14-REQ-4.1: cherry-pick succeeded — read the new HEAD SHA.
	newSHA, err := r.RevParse(ctx, "HEAD")
	if err != nil {
		return "", err
	}

	return newSHA, nil
}
