package campaign

import "context"

// Scheduler manages campaign lifecycle transitions including completion
// checking and failure propagation.
type Scheduler struct {
	store *Store
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
