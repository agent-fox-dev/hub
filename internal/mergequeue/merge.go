package mergequeue

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/agent-fox-dev/hub/internal/gitcmd"
	"github.com/txsvc/apikit"
)

// GitOps defines the git operations used by processMergeJob.
// In production, *gitcmd.GitRunner satisfies this interface.
type GitOps interface {
	Run(ctx context.Context, args ...string) (stdout []byte, stderr []byte, err error)
	RunExitCode(ctx context.Context, args ...string) (stdout []byte, stderr []byte, exitCode int, err error)
}

// BranchLocker provides per-target-branch mutual exclusion for merge
// operations. Lock blocks until the branch lock is acquired; Unlock releases
// it. Implementations must be safe for concurrent use.
type BranchLocker interface {
	Lock(branch string)
	Unlock(branch string)
}

// VariableGetter retrieves workspace variables by key.
// In production, this wraps the secrets.Store to look up CHECK_COMMAND
// and CHECK_TIMEOUT for a given workspace.
type VariableGetter interface {
	GetVariable(ctx context.Context, workspaceSlug, key string) (value string, found bool, err error)
}

// PostMergeHook is a function called after a successful merge when
// campaign_id is non-NULL. It notifies the campaign scheduler of the
// completed integration. Hook errors are logged but do not change
// the job status from merged.
type PostMergeHook func(ctx context.Context, job MergeJob) error

// CheckRunnerFunc executes a check command in the given directory with
// the specified timeout. It returns the combined stdout+stderr output
// and an error if the command failed or timed out.
// In production, this wraps os/exec.CommandContext with sh -c.
type CheckRunnerFunc func(ctx context.Context, dir string, command string, timeout time.Duration) (output []byte, err error)

// MergeDeps bundles the dependencies required by processMergeJob.
// All fields except Git and Locker are optional; when nil/zero the
// corresponding feature is skipped (e.g. no check command, no hook).
type MergeDeps struct {
	Git           GitOps
	Locker        BranchLocker
	Variables     VariableGetter
	Hook          PostMergeHook
	RunCheck      CheckRunnerFunc
	WorkspaceRoot string
}

// defaultCheckTimeout is used when CHECK_TIMEOUT is not configured.
const defaultCheckTimeout = 10 * time.Minute

// processMergeJob executes the full merge algorithm for a single job:
// resolve SHAs, dry-run conflict check (outside mutex), acquire mutex,
// validate nonce, set running, fetch, rebase, check, push.
// It updates job status in the database at each stage transition.
func processMergeJob(ctx context.Context, db *sql.DB, job *MergeJob, deps MergeDeps) error {
	// Step 1: Resolve target and source SHAs using rev-parse.
	targetHead, err := resolveRef(ctx, deps.Git, "origin/"+job.TargetBranch)
	if err != nil {
		return fmt.Errorf("resolve target head: %w", err)
	}

	sourceHead, err := resolveRef(ctx, deps.Git, job.SourceRef)
	if err != nil {
		return fmt.Errorf("resolve source head: %w", err)
	}

	// Step 2: Dry-run conflict check via merge-tree (OUTSIDE mutex).
	_, stderr, exitCode, mergeTreeErr := deps.Git.RunExitCode(ctx,
		"merge-tree", "--write-tree", targetHead, sourceHead)
	if mergeTreeErr != nil {
		// Subprocess failure (not a conflict exit code) — re-enqueue with
		// backoff rather than marking as conflict (11-REQ-4.E2, 11-REQ-4.E3).
		slog.Error("merge-tree dry-run subprocess error",
			"merge_job_id", job.ID,
			"workspace_slug", job.WorkspaceSlug,
			"error", mergeTreeErr,
		)
		return reEnqueueWithBackoff(db, job, "")
	}
	if exitCode != 0 {
		// Dry-run detected conflicts. Set status to conflict without acquiring mutex.
		conflictFiles := parseConflictPaths(string(stderr))
		return setConflictStatus(db, job.ID, conflictFiles)
	}

	// Step 3: Acquire per-target-branch mutex.
	if deps.Locker != nil {
		deps.Locker.Lock(job.TargetBranch)
		defer deps.Locker.Unlock(job.TargetBranch)
	}

	// Step 4: Validate nonce — re-read job from DB to confirm it hasn't changed.
	dbJob, err := GetMergeJob(db, job.ID)
	if err != nil {
		return fmt.Errorf("re-read job for nonce check: %w", err)
	}
	if dbJob == nil {
		// Job was deleted (transaction rollback). Discard.
		return nil
	}
	if dbJob.Nonce != job.Nonce {
		// Nonce mismatch — another submission took over. Discard.
		slog.Warn("nonce mismatch; discarding job",
			"merge_job_id", job.ID,
			"workspace_slug", job.WorkspaceSlug,
			"expected_nonce", job.Nonce,
			"db_nonce", dbJob.Nonce,
		)
		return nil
	}
	// If status has moved past queued, skip.
	if dbJob.Status == "running" || isTerminal(dbJob.Status) {
		return nil
	}

	// Step 5: Transition to running, record base_sha.
	now := apikit.NowUTC()
	_, err = db.Exec(
		`UPDATE merge_jobs SET status = 'running', base_sha = ?, updated_at = ? WHERE id = ?`,
		targetHead, now, job.ID,
	)
	if err != nil {
		return fmt.Errorf("transition to running: %w", err)
	}
	slog.Info("job status transition",
		"merge_job_id", job.ID,
		"workspace_slug", job.WorkspaceSlug,
		"status", "running",
	)

	// Step 6: git fetch origin <targetBranch>.
	_, _, err = deps.Git.Run(ctx, "fetch", "origin", job.TargetBranch)
	if err != nil {
		// Fetch failed (e.g. subprocess hang killed by context cancellation).
		// Re-enqueue the job with backoff rather than leaving it in running state.
		slog.Error("git fetch failed; re-enqueuing with backoff",
			"merge_job_id", job.ID,
			"workspace_slug", job.WorkspaceSlug,
			"error", err,
		)
		return reEnqueueWithBackoff(db, job, "")
	}

	// Step 7: git rebase origin/<targetBranch>.
	_, _, err = deps.Git.Run(ctx, "rebase", "origin/"+job.TargetBranch)
	if err != nil {
		// Rebase failed — likely a conflict (TOCTOU).
		return handleRebaseConflict(ctx, db, job, deps)
	}

	// Step 8: Execute CHECK_COMMAND if configured.
	if deps.Variables != nil {
		checkCmd, found, varErr := deps.Variables.GetVariable(ctx, job.WorkspaceSlug, "CHECK_COMMAND")
		if varErr != nil {
			// Variable store failure — set check_failed with the error message
			// (11-REQ-5.E6).
			return setCheckFailed(db, job.ID, varErr.Error())
		}
		if found && checkCmd != "" && deps.RunCheck != nil {
			// Get CHECK_TIMEOUT.
			timeout := defaultCheckTimeout
			timeoutStr, timeoutFound, _ := deps.Variables.GetVariable(ctx, job.WorkspaceSlug, "CHECK_TIMEOUT")
			if timeoutFound && timeoutStr != "" {
				if parsed, parseErr := time.ParseDuration(timeoutStr); parseErr == nil {
					timeout = parsed
				}
			}

			workDir := filepath.Join(deps.WorkspaceRoot, job.WorkspaceSlug, "trunk")
			output, checkErr := deps.RunCheck(ctx, workDir, checkCmd, timeout)
			if checkErr != nil {
				// Check failed — set status to check_failed and store output.
				return setCheckFailed(db, job.ID, string(output))
			}
		}
	}

	// Step 9: git push origin <targetBranch>.
	_, _, err = deps.Git.Run(ctx, "push", "origin", job.TargetBranch)
	if err != nil {
		// Push failed.
		return setPushFailed(db, job.ID)
	}

	// Step 10: Resolve merged SHA (HEAD after push).
	mergedSHA, err := resolveRef(ctx, deps.Git, "HEAD")
	if err != nil {
		return fmt.Errorf("resolve merged SHA: %w", err)
	}

	// Step 11: Set status to merged and record merged_sha.
	now = apikit.NowUTC()
	_, err = db.Exec(
		`UPDATE merge_jobs SET status = 'merged', merged_sha = ?, updated_at = ? WHERE id = ?`,
		mergedSHA, now, job.ID,
	)
	if err != nil {
		return fmt.Errorf("transition to merged: %w", err)
	}
	slog.Info("job status transition",
		"merge_job_id", job.ID,
		"workspace_slug", job.WorkspaceSlug,
		"status", "merged",
	)

	// Step 12: Invoke PostMergeHook for campaign merges.
	if job.CampaignID.Valid && job.CampaignID.String != "" && deps.Hook != nil {
		// Re-read the job to get the full merged state.
		mergedJob, readErr := GetMergeJob(db, job.ID)
		if readErr != nil {
			slog.Error("failed to re-read merged job for hook",
				"merge_job_id", job.ID,
				"workspace_slug", job.WorkspaceSlug,
				"error", readErr,
			)
		} else if mergedJob != nil {
			if hookErr := deps.Hook(ctx, *mergedJob); hookErr != nil {
				slog.Error("PostMergeHook failed",
					"merge_job_id", job.ID,
					"workspace_slug", job.WorkspaceSlug,
					"error", hookErr,
				)
				// Hook errors are logged but do not change job status.
			}
		}
	}

	return nil
}

// resolveRef runs git rev-parse on the given ref and returns the trimmed SHA.
func resolveRef(ctx context.Context, git GitOps, ref string) (string, error) {
	stdout, _, err := git.Run(ctx, "rev-parse", ref)
	if err != nil {
		return "", fmt.Errorf("rev-parse %s: %w", ref, err)
	}
	return strings.TrimSpace(string(stdout)), nil
}

// parseConflictPaths extracts file paths from merge-tree stderr output.
// It looks for lines matching "CONFLICT (content): Merge conflict in <path>".
func parseConflictPaths(stderr string) []string {
	var paths []string
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "CONFLICT") {
			// Extract the path after "Merge conflict in ".
			idx := strings.Index(line, "Merge conflict in ")
			if idx >= 0 {
				path := strings.TrimSpace(line[idx+len("Merge conflict in "):])
				if path != "" {
					paths = append(paths, path)
				}
			}
		}
	}
	return paths
}

// setConflictStatus sets a job to conflict status with conflict_details.
func setConflictStatus(db *sql.DB, jobID string, conflictFiles []string) error {
	detailsJSON, err := json.Marshal(conflictFiles)
	if err != nil {
		return fmt.Errorf("marshal conflict_details: %w", err)
	}
	now := apikit.NowUTC()
	_, err = db.Exec(
		`UPDATE merge_jobs SET status = 'conflict', conflict_details = ?, updated_at = ? WHERE id = ?`,
		string(detailsJSON), now, jobID,
	)
	if err == nil {
		slog.Info("job status transition",
			"merge_job_id", jobID,
			"status", "conflict",
		)
	}
	return err
}

// handleRebaseConflict handles a rebase conflict by collecting unmerged file
// paths, aborting the rebase, and setting the job to conflict status.
func handleRebaseConflict(ctx context.Context, db *sql.DB, job *MergeJob, deps MergeDeps) error {
	// Check if it's really a GitError (rebase conflict).
	// Collect unmerged file paths.
	stdout, _, _ := deps.Git.Run(ctx, "diff", "--name-only", "--diff-filter=U")
	conflictFiles := parseUnmergedPaths(string(stdout))

	// Abort the rebase to clean up.
	_, _, _ = deps.Git.Run(ctx, "rebase", "--abort")

	return setConflictStatus(db, job.ID, conflictFiles)
}

// parseUnmergedPaths parses the output of `git diff --name-only --diff-filter=U`
// into a list of file paths.
func parseUnmergedPaths(output string) []string {
	var paths []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths
}

// setCheckFailed sets a job to check_failed status with check_output.
func setCheckFailed(db *sql.DB, jobID, output string) error {
	now := apikit.NowUTC()
	_, err := db.Exec(
		`UPDATE merge_jobs SET status = 'check_failed', check_output = ?, updated_at = ? WHERE id = ?`,
		output, now, jobID,
	)
	if err == nil {
		slog.Info("job status transition",
			"merge_job_id", jobID,
			"status", "check_failed",
		)
	}
	return err
}

// setPushFailed sets a job to push_failed status.
func setPushFailed(db *sql.DB, jobID string) error {
	now := apikit.NowUTC()
	_, err := db.Exec(
		`UPDATE merge_jobs SET status = 'push_failed', updated_at = ? WHERE id = ?`,
		now, jobID,
	)
	if err == nil {
		slog.Info("job status transition",
			"merge_job_id", jobID,
			"status", "push_failed",
		)
	}
	return err
}

// ensure gitcmd import is used
var _ = gitcmd.GitError{}
