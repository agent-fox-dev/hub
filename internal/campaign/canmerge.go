package campaign

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/agent-fox-dev/hub/internal/mergequeue"
)

// CheckCanMerge checks whether a merge job for a campaign spec is allowed
// to proceed. It looks up the spec's status in campaign_specs and returns
// a non-empty CantMergeReason if the spec is cancelled or blocked.
// For non-campaign jobs (NULL campaign_id), the merge is always allowed.
func CheckCanMerge(_ context.Context, db *sql.DB, job mergequeue.MergeJob) (mergequeue.CantMergeReason, error) {
	// Non-campaign merge jobs are always allowed.
	if !job.CampaignID.Valid || !job.SpecID.Valid {
		return "", nil
	}

	var status string
	err := db.QueryRow(
		`SELECT status FROM campaign_specs WHERE campaign_id = ? AND spec_id = ?`,
		job.CampaignID.String, job.SpecID.String,
	).Scan(&status)
	if err == sql.ErrNoRows {
		// Spec not found in campaign_specs; allow the merge (no campaign constraint).
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("check can merge: %w", err)
	}

	switch status {
	case "cancelled":
		return mergequeue.CantMergeReason("spec is cancelled"), nil
	case "blocked":
		return mergequeue.CantMergeReason("spec is blocked due to rebase conflict"), nil
	default:
		return "", nil
	}
}
