package campaign

import (
	"context"
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
func (a *Authz) BlockBranch(_ string) {
	// stub
}

// UnblockBranch restores push access to the given branch.
func (a *Authz) UnblockBranch(_ string) {
	// stub
}

// ProtectIntegrationBranch marks a branch as a campaign integration branch,
// preventing direct pushes.
func (a *Authz) ProtectIntegrationBranch(_ string) {
	// stub
}

// IsBlocked reports whether push access to the given branch is revoked.
func (a *Authz) IsBlocked(_ string) bool {
	return false // stub
}

// AuthorizePush checks whether a push to the given branch is allowed.
// Returns nil if allowed, or a non-nil error if the push should be rejected.
func (a *Authz) AuthorizePush(_ context.Context, _ string) error {
	return nil // stub
}
