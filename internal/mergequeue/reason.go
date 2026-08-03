// Package mergequeue provides a FIFO merge queue with rebase-then-fast-forward
// semantics for serializing branch integration operations.
package mergequeue

// CantMergeReason is a typed enum indicating why a merge cannot proceed.
type CantMergeReason string

const (
	// BeforeDependency indicates an upstream spec has not yet been merged.
	BeforeDependency CantMergeReason = "BeforeDependency"
	// WouldConflict indicates a dry-run detected merge conflicts.
	WouldConflict CantMergeReason = "WouldConflict"
	// AlreadyMerged indicates the source branch is already integrated into the target.
	AlreadyMerged CantMergeReason = "AlreadyMerged"
	// BranchNotReady indicates no new commits exist on the source branch.
	BranchNotReady CantMergeReason = "BranchNotReady"
	// SpecBlocked indicates the spec is in blocked status in the campaign.
	SpecBlocked CantMergeReason = "SpecBlocked"
)
