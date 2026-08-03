package campaign

import (
	"context"
	"fmt"
	"sync"
)

// Authz manages push access control for campaign spec branches.
// It tracks blocked branches (due to rebase conflicts) and protected
// integration branches (where direct pushes are not allowed).
type Authz struct {
	mu                  sync.RWMutex
	blockedBranches     map[string]bool
	integrationBranches map[string]bool
}

// NewAuthz creates a new Authz instance.
func NewAuthz() *Authz {
	return &Authz{
		blockedBranches:     make(map[string]bool),
		integrationBranches: make(map[string]bool),
	}
}

// BlockBranch revokes push access to the given branch.
func (a *Authz) BlockBranch(branch string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.blockedBranches[branch] = true
}

// UnblockBranch restores push access to the given branch.
func (a *Authz) UnblockBranch(branch string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.blockedBranches, branch)
}

// ProtectIntegrationBranch marks a branch as a campaign integration branch,
// preventing direct pushes.
func (a *Authz) ProtectIntegrationBranch(branch string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.integrationBranches[branch] = true
}

// IsBlocked reports whether push access to the given branch is revoked.
func (a *Authz) IsBlocked(branch string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.blockedBranches[branch]
}

// AuthorizePush checks whether a push to the given branch is allowed.
// Returns nil if allowed, or a non-nil error if the push should be rejected.
func (a *Authz) AuthorizePush(_ context.Context, branch string) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.integrationBranches[branch] {
		return fmt.Errorf("direct pushes to integration branch are not allowed; use the merge queue")
	}
	if a.blockedBranches[branch] {
		return fmt.Errorf("branch is blocked due to rebase conflict")
	}
	return nil
}
