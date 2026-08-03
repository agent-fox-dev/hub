package campaign

import (
	"context"
	"fmt"
	"strings"

	"github.com/agent-fox-dev/hub/internal/gitcmd"
)

// GitRunnerAdapter adapts the gitcmd.GitRunner struct to the campaign
// GitOps interface. Each method creates a per-call GitRunner scoped to
// the given repoPath, so a single adapter handles multiple workspaces.
type GitRunnerAdapter struct{}

// NewGitRunnerAdapter creates a new GitRunnerAdapter.
func NewGitRunnerAdapter() *GitRunnerAdapter {
	return &GitRunnerAdapter{}
}

// CreateBranch creates a new branch from the given ref and returns the
// new branch HEAD SHA.
func (a *GitRunnerAdapter) CreateBranch(ctx context.Context, repoPath, branchName, fromRef string) (string, error) {
	runner := gitcmd.NewRunner(repoPath)

	// Create the branch from the given ref.
	_, stderr, err := runner.Run(ctx, "branch", branchName, fromRef)
	if err != nil {
		return "", fmt.Errorf("create branch %s: %s: %w", branchName, string(stderr), err)
	}

	// Resolve the new branch SHA.
	stdout, stderr, err := runner.Run(ctx, "rev-parse", branchName)
	if err != nil {
		return "", fmt.Errorf("resolve branch %s: %s: %w", branchName, string(stderr), err)
	}

	return strings.TrimSpace(string(stdout)), nil
}

// DeleteBranch deletes a branch from the repository.
func (a *GitRunnerAdapter) DeleteBranch(ctx context.Context, repoPath, branchName string) error {
	runner := gitcmd.NewRunner(repoPath)
	_, stderr, err := runner.Run(ctx, "branch", "-D", branchName)
	if err != nil {
		return fmt.Errorf("delete branch %s: %s: %w", branchName, string(stderr), err)
	}
	return nil
}

// BranchExists checks whether a branch exists in the repository.
func (a *GitRunnerAdapter) BranchExists(ctx context.Context, repoPath, branchName string) (bool, error) {
	runner := gitcmd.NewRunner(repoPath)
	_, _, exitCode, err := runner.RunExitCode(ctx, "rev-parse", "--verify", "refs/heads/"+branchName)
	if err != nil {
		return false, err
	}
	return exitCode == 0, nil
}

// ResolveRef returns the SHA for the given ref (branch name, HEAD, etc.).
func (a *GitRunnerAdapter) ResolveRef(ctx context.Context, repoPath, ref string) (string, error) {
	runner := gitcmd.NewRunner(repoPath)
	stdout, stderr, err := runner.Run(ctx, "rev-parse", ref)
	if err != nil {
		return "", fmt.Errorf("resolve ref %s: %s: %w", ref, string(stderr), err)
	}
	return strings.TrimSpace(string(stdout)), nil
}

// Rebase rebases branchName onto ontoRef. For a clean rebase, returns
// (newSHA, nil, nil). For a conflict, returns ("", conflictFiles, nil).
// For unexpected errors, returns ("", nil, err).
func (a *GitRunnerAdapter) Rebase(ctx context.Context, repoPath, branchName, ontoRef string) (string, []string, error) {
	runner := gitcmd.NewRunner(repoPath)

	// Checkout the branch to rebase.
	_, stderr, err := runner.Run(ctx, "checkout", branchName)
	if err != nil {
		return "", nil, fmt.Errorf("checkout %s: %s: %w", branchName, string(stderr), err)
	}

	// Attempt rebase onto the target ref.
	_, _, exitCode, err := runner.RunExitCode(ctx, "rebase", ontoRef)
	if err != nil {
		return "", nil, fmt.Errorf("rebase %s onto %s: %w", branchName, ontoRef, err)
	}

	if exitCode != 0 {
		// Rebase failed — collect conflicting file paths.
		stdout, _, _ := runner.Run(ctx, "diff", "--name-only", "--diff-filter=U")
		conflictFiles := parseConflictFiles(string(stdout))

		// Abort the failed rebase to clean up state.
		_, _, _ = runner.Run(ctx, "rebase", "--abort")

		if len(conflictFiles) > 0 {
			return "", conflictFiles, nil
		}
		// Non-zero exit but no conflict files — treat as unexpected error.
		return "", nil, fmt.Errorf("rebase %s onto %s failed with exit code %d", branchName, ontoRef, exitCode)
	}

	// Clean rebase — get the new HEAD SHA.
	stdout, _, err := runner.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", nil, fmt.Errorf("resolve HEAD after rebase: %w", err)
	}
	return strings.TrimSpace(string(stdout)), nil, nil
}

// parseConflictFiles extracts file paths from git diff --name-only output.
func parseConflictFiles(output string) []string {
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files
}
