package mergequeue

import (
	"context"
	"database/sql"
)

// CanMerge evaluates whether a merge job is eligible to proceed.
// It queries the database directly for dependency status and branch readiness.
// Returns (true, "", nil) when the merge may proceed.
// Returns (false, reason, nil) when the merge should be deferred or rejected.
// Returns (false, "", err) on unexpected database or context errors.
func CanMerge(ctx context.Context, db *sql.DB, job MergeJob) (bool, CantMergeReason, error) {
	// TODO: implement CanMerge pre-check
	return false, "", nil
}
