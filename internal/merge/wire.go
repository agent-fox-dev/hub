package merge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/hub/internal/gitcmd"
)

// DefaultFetchFunc returns a FetchFunc that uses go-git to fetch from the
// upstream remote. This is the production implementation used when the merge
// handler is wired in the server bootstrap.
func DefaultFetchFunc() FetchFunc {
	return func(trunkDir string, targetBranch string, auth transport.AuthMethod) error {
		repo, err := git.PlainOpen(trunkDir)
		if err != nil {
			return fmt.Errorf("merge: open repository at %s: %w", trunkDir, err)
		}

		fetchOpts := &git.FetchOptions{
			RemoteName: "origin",
			Auth:       auth,
		}

		err = repo.Fetch(fetchOpts)
		if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
			return fmt.Errorf("merge: fetch from upstream: %w", err)
		}
		return nil
	}
}

// DefaultRollbackFunc returns a RollbackFunc that uses GitRunner to restore
// a branch to a previous SHA via 'git checkout <branch> && git reset --hard <sha>'.
// This is the production implementation for rolling back a failed check command.
func DefaultRollbackFunc() RollbackFunc {
	return func(ctx context.Context, trunkDir, branch, sha string) error {
		runner, err := gitcmd.New(trunkDir, nil)
		if err != nil {
			return fmt.Errorf("rollback: create runner: %w", err)
		}
		if _, err := runner.Run(ctx, "checkout", branch); err != nil {
			return fmt.Errorf("rollback checkout %q: %w", branch, err)
		}
		if _, err := runner.Run(ctx, "reset", "--hard", sha); err != nil {
			return fmt.Errorf("rollback reset --hard %s: %w", sha, err)
		}
		return nil
	}
}

// ShellExecutor implements CommandExecutor using os/exec. It runs shell
// commands with environment variables and timeout enforcement. This is
// the production implementation for executing workspace CHECK_COMMAND.
type ShellExecutor struct{}

// Run executes a command in the given directory with the provided
// environment variables and timeout.
func (e *ShellExecutor) Run(ctx context.Context, dir string, env []string, timeout time.Duration, command string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), err
	}
	return string(output), nil
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
