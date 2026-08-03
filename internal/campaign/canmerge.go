package campaign

import (
	"context"
	"database/sql"

	"github.com/agent-fox-dev/hub/internal/mergequeue"
)

// CheckCanMerge checks whether a merge job for a campaign spec is allowed
// to proceed. It looks up the spec's status in campaign_specs and returns
// a non-empty CantMergeReason if the spec is cancelled or blocked.
func CheckCanMerge(_ context.Context, _ *sql.DB, _ mergequeue.MergeJob) (mergequeue.CantMergeReason, error) {
	return "", nil // stub
}
