package campaign

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/agent-fox-dev/hub/internal/mergequeue"
)

// mutexEntry holds a per-campaign mutex used to serialize PostMergeHook
// invocations for the same campaign.
type mutexEntry struct {
	mu sync.Mutex
}

// Scheduler manages campaign lifecycle transitions including completion
// checking, failure propagation, and post-merge hook processing.
type Scheduler struct {
	store        *Store
	gitOps       GitOps
	authz        *Authz
	rebaseEngine *RebaseEngine
	repoPath     string
	mutexes      sync.Map // per-campaign mutexes keyed by campaign ID → *mutexEntry
	frontiers    sync.Map // per-campaign frontier cache keyed by campaign ID → []string
}

// NewScheduler creates a new campaign Scheduler.
func NewScheduler(store *Store) *Scheduler {
	return &Scheduler{store: store}
}

// CheckCompletion checks if all specs in the campaign have merged status
// and transitions the campaign to completed if so.
func (s *Scheduler) CheckCompletion(ctx context.Context, campaignID string) error {
	specs, err := s.store.GetCampaignSpecs(ctx, campaignID)
	if err != nil {
		return err
	}

	allMerged := true
	for _, spec := range specs {
		if spec.Status != "merged" {
			allMerged = false
			break
		}
	}

	if allMerged {
		return s.store.UpdateCampaignStatus(ctx, campaignID, "completed")
	}
	return nil
}

// PropagateSpecFailure marks a spec as failed and immediately transitions
// the campaign to failed status.
func (s *Scheduler) PropagateSpecFailure(ctx context.Context, campaignID, specID string) error {
	if err := s.store.UpdateSpecStatus(ctx, campaignID, specID, "failed"); err != nil {
		return err
	}
	return s.store.UpdateCampaignStatus(ctx, campaignID, "failed")
}

// HandlePostMerge processes a completed merge job. For successful merges
// (status="merged"), it acquires the per-campaign mutex, performs cascading
// rebase of active sibling branches, advances the DAG frontier, and checks
// for campaign completion. For dead-letter jobs (status="dead_letter"), it
// sets the spec and campaign to failed status without performing rebase.
// Jobs with NULL campaign_id are treated as no-ops.
func (s *Scheduler) HandlePostMerge(ctx context.Context, job mergequeue.MergeJob) error {
	// No-op for standalone merges (no campaign).
	if !job.CampaignID.Valid {
		return nil
	}
	campaignID := job.CampaignID.String

	// Acquire per-campaign mutex to serialize hook processing.
	entry, _ := s.mutexes.LoadOrStore(campaignID, &mutexEntry{})
	mu := entry.(*mutexEntry)
	mu.mu.Lock()
	defer mu.mu.Unlock()

	// Load the campaign.
	campaign, err := s.store.GetCampaign(ctx, campaignID)
	if err != nil {
		return fmt.Errorf("handle post merge: get campaign: %w", err)
	}
	if campaign == nil {
		return nil
	}

	// Skip if campaign is not active (already failed, completed, or cancelled).
	if campaign.Status != "active" {
		return nil
	}

	// Handle dead-letter: set spec and campaign to failed, no rebase.
	if job.Status == "dead_letter" {
		if job.SpecID.Valid {
			// Best-effort: ignore error if spec not found.
			_ = s.store.UpdateSpecStatus(ctx, campaignID, job.SpecID.String, "failed")
		}
		return s.store.UpdateCampaignStatus(ctx, campaignID, "failed")
	}

	// Handle successful merge.
	if job.Status == "merged" && job.SpecID.Valid {
		specID := job.SpecID.String

		// Mark the merged spec as merged.
		if err := s.store.UpdateSpecStatus(ctx, campaignID, specID, "merged"); err != nil {
			return fmt.Errorf("handle post merge: mark spec merged: %w", err)
		}

		// Cascade rebase of remaining active branches.
		if s.rebaseEngine != nil {
			if _, err := s.rebaseEngine.CascadeRebase(ctx, campaignID, job.ID, campaign.IntegrationBranch, s.repoPath); err != nil {
				return fmt.Errorf("handle post merge: cascade rebase: %w", err)
			}
		}

		// Advance frontier: activate pending specs whose dependencies are satisfied.
		if err := s.advanceFrontier(ctx, campaign); err != nil {
			return fmt.Errorf("handle post merge: advance frontier: %w", err)
		}

		// Check if all specs are merged → complete campaign.
		return s.CheckCompletion(ctx, campaignID)
	}

	return nil
}

// advanceFrontier computes the current DAG frontier and activates any pending
// specs that are newly eligible (all upstream dependencies merged).
func (s *Scheduler) advanceFrontier(ctx context.Context, campaign *Campaign) error {
	specs, err := s.store.GetCampaignSpecs(ctx, campaign.ID)
	if err != nil {
		return err
	}

	// Build the set of merged specs.
	mergedSpecs := make(map[string]bool)
	// Build a lookup of spec status.
	specStatus := make(map[string]string)
	for _, spec := range specs {
		specStatus[spec.SpecID] = spec.Status
		if spec.Status == "merged" {
			mergedSpecs[spec.SpecID] = true
		}
	}

	// Compute frontier: specs whose upstream dependencies are all merged.
	frontier := ComputeFrontier(campaign.DAG, mergedSpecs)

	for _, fSpecID := range frontier {
		// Only activate specs that are currently pending.
		if specStatus[fSpecID] != "pending" {
			continue
		}

		// Create a branch for the newly-activated spec.
		branchName := DeriveSpecBranchName(fSpecID)
		var sha string
		if s.gitOps != nil {
			sha, err = s.gitOps.CreateBranch(ctx, s.repoPath, branchName, campaign.IntegrationBranch)
			if err != nil {
				continue // Best effort: skip this spec if branch creation fails.
			}
		}

		// Transition spec to active with branch info.
		if err := s.store.ActivateSpec(ctx, campaign.ID, fSpecID, branchName, sha); err != nil {
			continue // Best effort.
		}
	}

	return nil
}

// RecoverFromDB re-evaluates all active campaigns by recomputing the
// frontier from the DAG and current campaign_specs status rows in the DB.
// It re-initializes the per-campaign mutex map as empty.
// This is called on hub restart to heal any partial state from crashes.
func (s *Scheduler) RecoverFromDB(ctx context.Context) error {
	// Re-initialize the per-campaign mutex map as empty (12-REQ-17.2).
	s.mutexes = sync.Map{}
	// Clear any cached frontiers from prior state.
	s.frontiers = sync.Map{}

	// List all active campaigns from the DB.
	campaigns, err := s.store.ListActiveCampaigns(ctx)
	if err != nil {
		return fmt.Errorf("recover from DB: %w", err)
	}

	for _, campaign := range campaigns {
		// Get all specs for this campaign.
		specs, err := s.store.GetCampaignSpecs(ctx, campaign.ID)
		if err != nil {
			log.Printf("campaign %s: failed to get specs during recovery: %v", campaign.ID, err)
			continue
		}

		// Build the set of merged specs for frontier computation.
		mergedSpecs := make(map[string]bool)
		for _, spec := range specs {
			if spec.Status == "merged" {
				mergedSpecs[spec.SpecID] = true
			}
		}

		// Compute the frontier: specs whose upstream deps are all merged.
		frontier := ComputeFrontier(campaign.DAG, mergedSpecs)

		// Cache the frontier for GetFrontier lookups.
		s.frontiers.Store(campaign.ID, frontier)

		// Active and blocked specs are left as-is (12-REQ-14.2, 12-REQ-14.3).
		// Only pending frontier specs need activation, but we do NOT activate
		// them here — activation requires branch creation which is done by
		// advanceFrontier during normal PostMergeHook processing.
		// Recovery only recomputes what the frontier should be.
	}

	return nil
}

// GetFrontier returns the spec IDs that are currently in the frontier
// for the given campaign — specs whose upstream dependencies are all
// satisfied and which should be active.
func (s *Scheduler) GetFrontier(campaignID string) []string {
	val, ok := s.frontiers.Load(campaignID)
	if !ok {
		return nil
	}
	frontier, _ := val.([]string)
	return frontier
}

// MutexMapSize returns the number of entries in the per-campaign mutex map.
// Used by tests to verify that the map is re-initialized as empty on restart.
func (s *Scheduler) MutexMapSize() int {
	count := 0
	s.mutexes.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}
