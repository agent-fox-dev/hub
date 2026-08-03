package campaign

import "context"

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
func (r *RebaseEngine) CascadeRebase(_ context.Context, _, _, _, _ string) ([]RebaseResult, error) {
	return nil, nil // stub
}

// RebaseBranch rebases a single spec branch onto the current integration
// branch HEAD. Returns the result including new SHA for clean rebases or
// conflict file list for conflicts.
func (r *RebaseEngine) RebaseBranch(_ context.Context, _, _, _, _ string) (*RebaseResult, error) {
	return nil, nil // stub
}
