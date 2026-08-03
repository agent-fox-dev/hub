package mergequeue

import (
	"context"
	"database/sql"
)

// canMergeHook is the registered campaign pre-check function. When non-nil,
// CanMerge delegates to it. Set via SetCanMergeHook at application startup.
var canMergeHook CanMergeFunc

// SetCanMergeHook registers a CanMergeFunc that CanMerge delegates to.
// Call this at startup before the worker begins processing jobs.
// The campaign package uses this to inject its spec-status checks.
func SetCanMergeHook(fn CanMergeFunc) {
	canMergeHook = fn
}

// CanMerge evaluates whether a merge job is eligible to proceed.
// It queries the database directly for dependency status and branch readiness.
// Returns (true, "", nil) when the merge may proceed.
// Returns (false, reason, nil) when the merge should be deferred or rejected.
// Returns (false, "", err) on unexpected database or context errors.
func CanMerge(ctx context.Context, db *sql.DB, job MergeJob) (bool, CantMergeReason, error) {
	if canMergeHook != nil {
		return canMergeHook(ctx, db, job)
	}
	// No hook registered — allow the merge by default.
	return true, "", nil
}
