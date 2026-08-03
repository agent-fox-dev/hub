package campaign

import (
	"context"
	"sync"

	"github.com/agent-fox-dev/hub/internal/mergequeue"
)

// Scheduler manages campaign lifecycle transitions including completion
// checking, failure propagation, and post-merge hook processing.
type Scheduler struct {
	store        *Store
	gitOps       GitOps
	authz        *Authz
	rebaseEngine *RebaseEngine
	repoPath     string
	mutexes      sync.Map // per-campaign mutexes keyed by campaign ID
}

// NewScheduler creates a new campaign Scheduler.
func NewScheduler(store *Store) *Scheduler {
	return &Scheduler{store: store}
}

// CheckCompletion checks if all specs in the campaign have merged status
// and transitions the campaign to completed if so.
func (s *Scheduler) CheckCompletion(_ context.Context, _ string) error {
	return nil // stub
}

// PropagateSpecFailure marks a spec as failed and immediately transitions
// the campaign to failed status.
func (s *Scheduler) PropagateSpecFailure(_ context.Context, _, _ string) error {
	return nil // stub
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
