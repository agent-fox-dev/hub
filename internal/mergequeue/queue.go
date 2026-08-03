package mergequeue

import (
	"context"
	"database/sql"
	"time"
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
	// TODO: implement exponential backoff calculation
	return 0
}

// reEnqueueWithBackoff re-enqueues a retriable failed job by updating
// available_at to now() + backoff delay and incrementing retry_count.
// The job's status remains "queued". Returns an error if the database
// update fails.
func reEnqueueWithBackoff(db *sql.DB, job *MergeJob, reason CantMergeReason) error {
	// TODO: implement backoff re-enqueue
	return nil
}

// PollEligibleJobs returns queued jobs with available_at <= now(),
// using the idx_merge_jobs_available index. Only jobs with status='queued'
// and available_at in the past are returned.
func PollEligibleJobs(db *sql.DB) ([]*MergeJob, error) {
	// TODO: implement polling query
	return nil, nil
}

// processJobByID looks up a merge job by ID and processes it through the
// worker pipeline. It validates the nonce, calls the canMerge pre-check,
// and either re-enqueues with backoff (retriable failures), transitions to
// dead_letter (after max retries), or executes the full merge pipeline.
//
// Returns nil if the job doesn't exist (transaction rollback case) or if
// the job has already been processed (status is running/merged/terminal).
func processJobByID(ctx context.Context, db *sql.DB, jobID string, deps MergeDeps, canMerge CanMergeFunc) error {
	// TODO: implement worker job processing
	return nil
}

// isRetriable returns true if the CantMergeReason indicates a transient
// condition that should be retried with backoff: BeforeDependency,
// BranchNotReady, or SpecBlocked.
func isRetriable(reason CantMergeReason) bool {
	// TODO: implement
	return false
}
