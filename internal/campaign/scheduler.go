package campaign

import (
	"context"
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
func (s *Scheduler) HandlePostMerge(_ context.Context, _ mergequeue.MergeJob) error {
	return nil // stub
}

// RecoverFromDB re-evaluates all active campaigns by recomputing the
// frontier from the DAG and current campaign_specs status rows in the DB.
// It re-initializes the per-campaign mutex map as empty.
// This is called on hub restart to heal any partial state from crashes.
func (s *Scheduler) RecoverFromDB(_ context.Context) error {
	return nil // stub
}

// GetFrontier returns the spec IDs that are currently in the frontier
// for the given campaign — specs whose upstream dependencies are all
// satisfied and which should be active.
func (s *Scheduler) GetFrontier(_ string) []string {
	return nil // stub
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
