package merge

import (
	"context"
	"errors"
	"fmt"

	"github.com/agent-fox-dev/hub/internal/gitcmd"
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
// *RebaseConflictError with the conflicting file paths. On ref-not-found,
// returns ErrRefNotFound without invoking Rebase.
func RebaseBranch(ctx context.Context, runner RebaseRunner, sourceBranch, targetRef string) (string, error) {
	// Validate source branch exists before attempting any git operations.
	if _, err := runner.RevParse(ctx, sourceBranch); err != nil {
		return "", err
	}

	// Checkout the source branch.
	if _, err := runner.Run(ctx, "checkout", sourceBranch); err != nil {
		return "", fmt.Errorf("merge: checkout %q: %w", sourceBranch, err)
	}

	// Rebase onto the target ref. On conflict, the runner auto-aborts and
	// returns *RebaseConflictError. On timeout, the context error is
	// returned directly (preserving errors.Is compatibility).
	newSHA, err := runner.Rebase(ctx, targetRef)
	if err != nil {
		return "", err
	}

	return newSHA, nil
}

// BatchRebase rebases each source branch in the branches slice onto the
// target ref sequentially. A conflict on one branch does not prevent
// rebasing of subsequent branches. Returns a per-branch result list.
func BatchRebase(ctx context.Context, runner RebaseRunner, targetRef string, branches []string) ([]RebaseResult, error) {
	if len(branches) == 0 {
		return nil, fmt.Errorf("merge: empty branches list")
	}

	results := make([]RebaseResult, 0, len(branches))
	for _, branch := range branches {
		newSHA, err := RebaseBranch(ctx, runner, branch, targetRef)
		if err != nil {
			result := RebaseResult{
				Branch: branch,
				Status: "error",
			}
			var conflictErr *gitcmd.RebaseConflictError
			if errors.As(err, &conflictErr) {
				result.Status = "conflict"
				result.ConflictFiles = conflictErr.ConflictingFiles
			}
			results = append(results, result)
			continue
		}
		results = append(results, RebaseResult{
			Branch:  branch,
			Status:  "ok",
			NewHead: newSHA,
		})
	}

	return results, nil
}
