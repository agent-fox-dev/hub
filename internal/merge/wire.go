package merge

import (
	"context"
	"errors"
	"fmt"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/carrypatch/gitcmd"
)

// DefaultFetchFunc returns a FetchFunc that uses go-git to fetch from the
// upstream remote. This is the production implementation used when the merge
// handler is wired in the server bootstrap.
func DefaultFetchFunc() FetchFunc {
	return func(ctx context.Context, trunkDir string, targetBranch string, auth transport.AuthMethod) error {
		repo, err := git.PlainOpen(trunkDir)
		if err != nil {
			return fmt.Errorf("merge: open repository at %s: %w", trunkDir, err)
		}

		fetchOpts := &git.FetchOptions{
			RemoteName: "origin",
			Auth:       auth,
		}

		err = repo.FetchContext(ctx, fetchOpts)
		if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
			return fmt.Errorf("merge: fetch from upstream: %w", err)
		}
		return nil
	}
}

// DefaultBranchChecker returns a BranchChecker that uses GitRunner to verify
// branch existence via rev-parse. workspaceRoot is the WORKSPACE_ROOT directory.
func DefaultBranchChecker(workspaceRoot string) BranchChecker {
	return func(workspaceSlug, branch string) (bool, error) {
		trunkDir := fmt.Sprintf("%s/%s/trunk", workspaceRoot, workspaceSlug)
		runner, err := gitcmd.New(trunkDir, nil)
		if err != nil {
			return false, fmt.Errorf("merge: create runner for branch check: %w", err)
		}
		_, err = runner.RevParse(context.Background(), branch)
		if err != nil {
			// RevParse failure means branch does not exist (or repo error).
			// Treat as "does not exist" — the merge handler will report a
			// clear error if the repo is actually inaccessible.
			return false, nil
		}
		return true, nil
	}
}
