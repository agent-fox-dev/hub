package mergequeue

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// campaignDAG represents the spec dependency graph stored in campaigns.dag.
type campaignDAG struct {
	Specs []string      `json:"specs"`
	Edges []dagEdge     `json:"edges"`
}

// dagEdge represents a single dependency edge in the campaign DAG.
type dagEdge struct {
	From         string `json:"from"`
	To           string `json:"to"`
	Relationship string `json:"relationship"`
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
	// Check for context cancellation before any DB work.
	select {
	case <-ctx.Done():
		return false, "", ctx.Err()
	default:
	}

	// Standalone merges skip all campaign-specific checks.
	if !job.CampaignID.Valid || job.CampaignID.String == "" {
		return true, "", nil
	}

	// Campaign merge: query campaign_specs for the spec's current state.
	var specStatus string
	var branchSHA sql.NullString
	err := db.QueryRowContext(ctx,
		"SELECT status, branch_sha FROM campaign_specs WHERE campaign_id = ? AND spec_id = ?",
		job.CampaignID.String, job.SpecID.String,
	).Scan(&specStatus, &branchSHA)
	if err != nil {
		return false, "", fmt.Errorf("query campaign_specs: %w", err)
	}

	// AlreadyMerged: spec is already integrated.
	if specStatus == "merged" {
		return false, AlreadyMerged, nil
	}

	// SpecBlocked: spec is blocked in the campaign.
	if specStatus == "blocked" {
		return false, SpecBlocked, nil
	}

	// BranchNotReady: no new commits on the source branch.
	if !branchSHA.Valid || branchSHA.String == "" {
		return false, BranchNotReady, nil
	}

	// BeforeDependency: check upstream specs via the campaign DAG.
	ok, err := checkUpstreamDependencies(ctx, db, job.CampaignID.String, job.SpecID.String)
	if err != nil {
		return false, "", err
	}
	if !ok {
		return false, BeforeDependency, nil
	}

	return true, "", nil
}

// checkUpstreamDependencies parses the campaign DAG and verifies that all
// upstream specs (those this spec depends on) are in "merged" status.
// Returns (true, nil) if all dependencies are satisfied.
func checkUpstreamDependencies(ctx context.Context, db *sql.DB, campaignID, specID string) (bool, error) {
	// Fetch the campaign DAG.
	var dagJSON string
	err := db.QueryRowContext(ctx,
		"SELECT dag FROM campaigns WHERE id = ?",
		campaignID,
	).Scan(&dagJSON)
	if err != nil {
		return false, fmt.Errorf("query campaign DAG: %w", err)
	}

	var dag campaignDAG
	if err := json.Unmarshal([]byte(dagJSON), &dag); err != nil {
		return false, fmt.Errorf("parse campaign DAG: %w", err)
	}

	// Find upstream specs: edges where this spec is the target (To).
	for _, edge := range dag.Edges {
		if edge.To == specID && edge.Relationship == "depends_on" {
			var upstreamStatus string
			err := db.QueryRowContext(ctx,
				"SELECT status FROM campaign_specs WHERE campaign_id = ? AND spec_id = ?",
				campaignID, edge.From,
			).Scan(&upstreamStatus)
			if err != nil {
				return false, fmt.Errorf("query upstream spec %s: %w", edge.From, err)
			}
			if upstreamStatus != "merged" {
				return false, nil
			}
		}
	}

	return true, nil
}
