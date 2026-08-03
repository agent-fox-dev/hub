package mergequeue

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/txsvc/apikit"
)

// CanMergeFunc is the function type for merge eligibility pre-checks.
// In production, the package-level CanMerge function is passed.
type CanMergeFunc func(ctx context.Context, db *sql.DB, job MergeJob) (bool, CantMergeReason, error)

const (
	// maxRetries is the maximum number of retry attempts before dead-lettering.
	maxRetries = 20

	// maxBackoffDuration is the maximum backoff delay per retry attempt (2 hours).
	maxBackoffDuration = 2 * time.Hour

	// backoffBase is the base delay for exponential backoff (2 seconds).
	backoffBase = 2 * time.Second
)

// calculateBackoff computes the retry delay for the given retry count:
// backoffBase * 2^retryCount, capped at maxBackoffDuration per attempt.
func calculateBackoff(retryCount int) time.Duration {
	delay := backoffBase
	for i := 0; i < retryCount; i++ {
		delay *= 2
		if delay > maxBackoffDuration {
			return maxBackoffDuration
		}
	}
	return delay
}

// reEnqueueWithBackoff re-enqueues a retriable failed job by updating
// available_at to now() + backoff delay and incrementing retry_count.
// The job's status remains "queued". Returns an error if the database
// update fails.
func reEnqueueWithBackoff(db *sql.DB, job *MergeJob, reason CantMergeReason) error {
	delay := calculateBackoff(job.RetryCount)
	// Round to nearest second to minimize truncation error from RFC3339 formatting.
	futureTime := apikit.FormatUTC(time.Now().UTC().Add(delay).Round(time.Second))
	newRetryCount := job.RetryCount + 1
	now := apikit.NowUTC()

	_, err := db.Exec(
		`UPDATE merge_jobs SET
			status = 'queued', retry_count = ?, available_at = ?, rejection_reason = ?, updated_at = ?
		WHERE id = ?`,
		newRetryCount, futureTime, string(reason), now, job.ID,
	)
	return err
}

// PollEligibleJobs returns queued jobs with available_at <= now(),
// using the idx_merge_jobs_available index. Only jobs with status='queued'
// and available_at in the past are returned.
func PollEligibleJobs(db *sql.DB) ([]*MergeJob, error) {
	now := apikit.NowUTC()
	rows, err := db.Query(`SELECT
		id, nonce, campaign_id, spec_id, workspace_slug, target_branch, source_ref,
		status, rejection_reason, retry_count, available_at, base_sha, merged_sha,
		conflict_details, check_output, submitted_by, created_at, updated_at
	FROM merge_jobs
	WHERE status = 'queued' AND available_at <= ?
	ORDER BY available_at ASC`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []*MergeJob
	for rows.Next() {
		var job MergeJob
		if err := rows.Scan(
			&job.ID, &job.Nonce, &job.CampaignID, &job.SpecID,
			&job.WorkspaceSlug, &job.TargetBranch, &job.SourceRef,
			&job.Status, &job.RejectionReason, &job.RetryCount,
			&job.AvailableAt, &job.BaseSHA, &job.MergedSHA,
			&job.ConflictDetails, &job.CheckOutput, &job.SubmittedBy,
			&job.CreatedAt, &job.UpdatedAt,
		); err != nil {
			return nil, err
		}
		jobs = append(jobs, &job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return jobs, nil
}

// processJobByID looks up a merge job by ID and processes it through the
// worker pipeline. It validates the nonce, calls the canMerge pre-check,
// and either re-enqueues with backoff (retriable failures), transitions to
// dead_letter (after max retries), or executes the full merge pipeline.
//
// Returns nil if the job doesn't exist (transaction rollback case) or if
// the job has already been processed (status is running/merged/terminal).
func processJobByID(ctx context.Context, db *sql.DB, jobID string, deps MergeDeps, canMerge CanMergeFunc) error {
	job, err := GetMergeJob(db, jobID)
	if err != nil {
		return fmt.Errorf("get merge job %s: %w", jobID, err)
	}
	if job == nil {
		return nil
	}

	// Skip if already in a terminal or running state.
	switch job.Status {
	case "merged", "conflict", "check_failed", "push_failed", "cancelled", "dead_letter", "running":
		slog.Info("skipping job in non-queued status",
			"merge_job_id", job.ID,
			"workspace_slug", job.WorkspaceSlug,
			"status", job.Status,
		)
		return nil
	}

	// If prepared, transition to queued first.
	if job.Status == "prepared" {
		if err := UpdateStatus(db, job.ID, "queued"); err != nil {
			return fmt.Errorf("transition prepared->queued: %w", err)
		}
		job.Status = "queued"
	}

	// Run the CanMerge pre-check.
	if canMerge != nil {
		ok, reason, checkErr := canMerge(ctx, db, *job)
		if checkErr != nil {
			return fmt.Errorf("canMerge check failed: %w", checkErr)
		}
		if !ok {
			if isRetriable(reason) {
				newCount := job.RetryCount + 1
				if newCount >= maxRetries {
					dlerr := DirectUpdateStatus(db, job.ID, "dead_letter",
						[]string{"rejection_reason"},
						[]interface{}{string(reason)},
					)
					if dlerr != nil {
						return fmt.Errorf("dead-letter job %s: %w", job.ID, dlerr)
					}
					slog.Info("job dead-lettered after max retries",
						"merge_job_id", job.ID,
						"workspace_slug", job.WorkspaceSlug,
						"status", "dead_letter",
						"rejection_reason", string(reason),
						"retry_count", newCount,
					)
					return nil
				}
				if berr := reEnqueueWithBackoff(db, job, reason); berr != nil {
					return fmt.Errorf("re-enqueue job %s: %w", job.ID, berr)
				}
				slog.Info("job re-enqueued with backoff",
					"merge_job_id", job.ID,
					"workspace_slug", job.WorkspaceSlug,
					"status", "queued",
					"rejection_reason", string(reason),
					"retry_count", newCount,
				)
				return nil
			}
			// Non-retriable (e.g. AlreadyMerged, WouldConflict).
			rerr := DirectUpdateStatus(db, job.ID, "conflict",
				[]string{"rejection_reason"},
				[]interface{}{string(reason)},
			)
			if rerr != nil {
				return fmt.Errorf("reject job %s: %w", job.ID, rerr)
			}
			return nil
		}
	}

	return processMergeJob(ctx, db, job, deps)
}

// isRetriable returns true if the CantMergeReason indicates a transient
// condition that should be retried with backoff.
func isRetriable(reason CantMergeReason) bool {
	switch reason {
	case BeforeDependency, BranchNotReady, SpecBlocked:
		return true
	}
	return false
}
