package merge

import (
	"context"
	"fmt"
)

// RebaseRunner abstracts the git operations needed for branch rebase
// operations. *gitcmd.GitRunner satisfies this interface.
type RebaseRunner interface {
	// Run executes an arbitrary git subcommand.
	Run(ctx context.Context, args ...string) (string, error)

	// Rebase executes git rebase <onto> with automatic abort on conflict.
	// Returns the new HEAD SHA on success or *RebaseConflictError on conflict.
	Rebase(ctx context.Context, onto string) (string, error)

	// RevParse resolves a ref to its full 40-character hex SHA.
	RevParse(ctx context.Context, ref string) (string, error)
}

// RebaseResult contains the per-branch outcome of a rebase operation.
type RebaseResult struct {
	// Branch is the name of the source branch that was rebased.
	Branch string `json:"branch"`

	// Status is "ok" on success or "conflict" on conflict.
	Status string `json:"status"`

	// NewHead is the 40-character hex SHA of the new branch HEAD after
	// a successful rebase. Empty on conflict.
	NewHead string `json:"new_head,omitempty"`

	// ConflictFiles lists the conflicting file paths when Status is
	// "conflict". Empty on success.
	ConflictFiles []string `json:"conflict_files,omitempty"`
}

// RebaseBranch rebases a single source branch onto a target ref using the
// provided RebaseRunner. It checks out the source branch, then runs
// 'git rebase <targetRef>'. Returns the new HEAD SHA on success.
//
// On conflict, the runner automatically aborts the rebase and returns a
// *RebaseConflictError with the conflicting file paths.
func RebaseBranch(_ context.Context, _ RebaseRunner, _, _ string) (string, error) {
	return "", fmt.Errorf("merge: RebaseBranch not implemented")
}

// BatchRebase rebases each source branch in the branches slice onto the
// target ref sequentially. A conflict on one branch does not prevent
// rebasing of subsequent branches. Returns a per-branch result list.
func BatchRebase(_ context.Context, _ RebaseRunner, _ string, _ []string) ([]RebaseResult, error) {
	return nil, fmt.Errorf("merge: BatchRebase not implemented")
}
