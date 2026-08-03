package campaign

import (
	"context"
	"fmt"
)

// RebaseResult contains the outcome of rebasing a single spec branch.
type RebaseResult struct {
	SpecID        string
	Success       bool
	NewSHA        string
	ConflictFiles []string
}

// RebaseEngine performs cascading rebases of spec branches after merges.
type RebaseEngine struct {
	store  *Store
	gitOps GitOps
	authz  *Authz
}

// NewRebaseEngine creates a new RebaseEngine.
func NewRebaseEngine(store *Store, gitOps GitOps, authz *Authz) *RebaseEngine {
	return &RebaseEngine{store: store, gitOps: gitOps, authz: authz}
}

// CascadeRebase rebases all active spec branches onto the new integration
// branch HEAD in topological DAG order. Branches that conflict are set to
// blocked and their downstream dependents are skipped (per-subtree
// conflict-stop semantics).
func (r *RebaseEngine) CascadeRebase(ctx context.Context, campaignID, mergeJobID, integrationBranch, repoPath string) ([]RebaseResult, error) {
	// Get campaign for the DAG.
	campaign, err := r.store.GetCampaign(ctx, campaignID)
	if err != nil {
		return nil, fmt.Errorf("cascade rebase: get campaign: %w", err)
	}
	if campaign == nil {
		return nil, fmt.Errorf("cascade rebase: campaign %q not found", campaignID)
	}

	// Get all specs for this campaign.
	specs, err := r.store.GetCampaignSpecs(ctx, campaignID)
	if err != nil {
		return nil, fmt.Errorf("cascade rebase: get specs: %w", err)
	}

	// Build map of active specs by spec ID.
	activeSpecs := make(map[string]*CampaignSpec, len(specs))
	for i := range specs {
		if specs[i].Status == "active" {
			activeSpecs[specs[i].SpecID] = &specs[i]
		}
	}

	// Get topological order (roots first, leaves last).
	topoOrder := TopologicalOrder(campaign.DAG)

	// Track specs to skip (downstream dependents of conflicted branches).
	skippedSpecs := make(map[string]bool)

	var results []RebaseResult

	for _, specID := range topoOrder {
		// Skip downstream dependents of a conflicted branch.
		if skippedSpecs[specID] {
			continue
		}

		// Only rebase active spec branches.
		spec, isActive := activeSpecs[specID]
		if !isActive {
			continue
		}

		// Perform rebase via GitOps.
		newSHA, conflictFiles, rebaseErr := r.gitOps.Rebase(ctx, repoPath, spec.BranchName, integrationBranch)

		if rebaseErr != nil {
			// Unexpected error (12-REQ-8.E2): treat the branch as blocked.
			conflictInfo := []string{rebaseErr.Error()}
			_ = r.store.SetSpecBlocked(ctx, campaignID, specID, conflictInfo, mergeJobID)
			if r.authz != nil {
				r.authz.BlockBranch(spec.BranchName)
			}
			markDownstreamSkipped(campaign.DAG, specID, skippedSpecs)
			results = append(results, RebaseResult{SpecID: specID, Success: false, ConflictFiles: conflictInfo})
			continue
		}

		if len(conflictFiles) > 0 {
			// Rebase conflict (12-REQ-8.3): set spec to blocked, revoke push
			// access, record conflict details, skip downstream dependents.
			_ = r.store.SetSpecBlocked(ctx, campaignID, specID, conflictFiles, mergeJobID)
			if r.authz != nil {
				r.authz.BlockBranch(spec.BranchName)
			}
			markDownstreamSkipped(campaign.DAG, specID, skippedSpecs)
			results = append(results, RebaseResult{SpecID: specID, Success: false, ConflictFiles: conflictFiles})
			continue
		}

		// Clean rebase (12-REQ-8.2): update branch_sha in campaign_specs.
		_ = r.store.UpdateSpecBranchSHA(ctx, campaignID, specID, newSHA)
		results = append(results, RebaseResult{SpecID: specID, Success: true, NewSHA: newSHA})
	}

	return results, nil
}

// RebaseBranch rebases a single spec branch onto the current integration
// branch HEAD. Returns the result including new SHA for clean rebases or
// conflict file list for conflicts.
func (r *RebaseEngine) RebaseBranch(ctx context.Context, campaignID, specID, integrationBranch, repoPath string) (*RebaseResult, error) {
	spec, err := r.store.GetCampaignSpec(ctx, campaignID, specID)
	if err != nil {
		return nil, fmt.Errorf("rebase branch: get spec: %w", err)
	}
	if spec == nil {
		return nil, fmt.Errorf("rebase branch: spec %q/%q not found", campaignID, specID)
	}

	newSHA, conflictFiles, rebaseErr := r.gitOps.Rebase(ctx, repoPath, spec.BranchName, integrationBranch)
	if rebaseErr != nil {
		return &RebaseResult{SpecID: specID, Success: false, ConflictFiles: []string{rebaseErr.Error()}}, nil
	}
	if len(conflictFiles) > 0 {
		return &RebaseResult{SpecID: specID, Success: false, ConflictFiles: conflictFiles}, nil
	}

	if err := r.store.UpdateSpecBranchSHA(ctx, campaignID, specID, newSHA); err != nil {
		return nil, fmt.Errorf("rebase branch: update SHA: %w", err)
	}
	return &RebaseResult{SpecID: specID, Success: true, NewSHA: newSHA}, nil
}

// markDownstreamSkipped adds all transitive downstream dependents of specID
// to the skippedSpecs set. This implements per-subtree conflict-stop:
// when a branch conflicts, only its downstream dependents are skipped;
// branches in unrelated parts of the DAG are unaffected.
func markDownstreamSkipped(dag *DAG, specID string, skippedSpecs map[string]bool) {
	for _, e := range dag.Edges {
		if e.From == specID && !skippedSpecs[e.To] {
			skippedSpecs[e.To] = true
			markDownstreamSkipped(dag, e.To, skippedSpecs)
		}
	}
}
