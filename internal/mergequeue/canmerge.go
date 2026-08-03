package mergequeue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// canMergeHook is an optional additional pre-check function. When non-nil,
// CanMerge calls it after the built-in checks pass. Set via SetCanMergeHook
// at application startup.
var canMergeHook CanMergeFunc

// SetCanMergeHook registers a CanMergeFunc that CanMerge calls after
// built-in checks pass. Call this at startup before the worker begins
// processing jobs. The campaign package uses this to inject additional
// spec-status checks (e.g. cancelled specs).
func SetCanMergeHook(fn CanMergeFunc) {
	canMergeHook = fn
}

// CanMerge evaluates whether a merge job is eligible to proceed.
// It queries the database for dependency status and branch readiness.
//
// For campaign merges (campaign_id is set):
//   - Returns (false, AlreadyMerged, nil) if the spec is already merged.
//   - Returns (false, SpecBlocked, nil) if the spec is blocked.
//   - Returns (false, BranchNotReady, nil) if branch_sha is NULL (no commits).
//   - Returns (false, BeforeDependency, nil) if an upstream spec is not merged.
//
// For standalone merges (campaign_id is NULL/empty):
//   - Skips BeforeDependency and SpecBlocked checks.
//   - Returns (true, "", nil) immediately.
//
// Returns (false, "", err) on unexpected database or context errors.
func CanMerge(ctx context.Context, db *sql.DB, job MergeJob) (bool, CantMergeReason, error) {
	// Check context first — return immediately if already cancelled.
	if err := ctx.Err(); err != nil {
		return false, "", err
	}

	// Standalone merges skip all campaign-specific checks.
	if !job.CampaignID.Valid || job.CampaignID.String == "" {
		return true, "", nil
	}

	// Campaign merge — spec_id is required for campaign checks.
	if !job.SpecID.Valid || job.SpecID.String == "" {
		return true, "", nil
	}

	// Query campaign_specs for the spec's status and branch_sha.
	var specStatus string
	var branchSHA sql.NullString
	err := db.QueryRowContext(ctx,
		`SELECT status, branch_sha FROM campaign_specs WHERE campaign_id = ? AND spec_id = ?`,
		job.CampaignID.String, job.SpecID.String,
	).Scan(&specStatus, &branchSHA)
	if errors.Is(err, sql.ErrNoRows) {
		// Spec not found in campaign_specs — no campaign constraint, allow merge.
		return true, "", nil
	}
	if err != nil {
		return false, "", err
	}

	// Check spec status.
	if specStatus == "merged" {
		return false, AlreadyMerged, nil
	}
	if specStatus == "blocked" {
		return false, SpecBlocked, nil
	}

	// Check branch readiness — branch_sha must be non-NULL.
	if !branchSHA.Valid {
		return false, BranchNotReady, nil
	}

	// Check upstream dependencies via campaign DAG.
	reason, depErr := checkUpstreamDependencies(ctx, db, job.CampaignID.String, job.SpecID.String)
	if depErr != nil {
		return false, "", depErr
	}
	if reason != "" {
		return false, reason, nil
	}

	// Built-in checks passed. Run the hook for additional checks if registered.
	if canMergeHook != nil {
		return canMergeHook(ctx, db, job)
	}

	return true, "", nil
}

// dagSchema represents the JSON structure of a campaign DAG.
type dagSchema struct {
	Specs []string  `json:"specs"`
	Edges []dagEdge `json:"edges"`
}

// dagEdge represents a dependency edge in the campaign DAG.
type dagEdge struct {
	From         string `json:"from"`
	To           string `json:"to"`
	Relationship string `json:"relationship"`
}

// checkUpstreamDependencies parses the campaign DAG and verifies that all
// upstream specs that the given spec depends on are in "merged" status.
// Returns (BeforeDependency, nil) if any upstream spec is not yet merged.
func checkUpstreamDependencies(ctx context.Context, db *sql.DB, campaignID, specID string) (CantMergeReason, error) {
	var dagJSON string
	err := db.QueryRowContext(ctx,
		`SELECT dag FROM campaigns WHERE id = ?`, campaignID,
	).Scan(&dagJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil // Campaign not found — no dependency constraint.
	}
	if err != nil {
		return "", fmt.Errorf("query campaign DAG: %w", err)
	}

	var dag dagSchema
	if err := json.Unmarshal([]byte(dagJSON), &dag); err != nil {
		return "", fmt.Errorf("parse campaign DAG: %w", err)
	}

	// Find all upstream specs that this spec depends on.
	for _, edge := range dag.Edges {
		if edge.To == specID && edge.Relationship == "depends_on" {
			var upstreamStatus string
			err := db.QueryRowContext(ctx,
				`SELECT status FROM campaign_specs WHERE campaign_id = ? AND spec_id = ?`,
				campaignID, edge.From,
			).Scan(&upstreamStatus)
			if errors.Is(err, sql.ErrNoRows) {
				continue // Upstream spec not tracked — skip.
			}
			if err != nil {
				return "", err
			}
			if upstreamStatus != "merged" {
				return BeforeDependency, nil
			}
		}
	}

	return "", nil
}
