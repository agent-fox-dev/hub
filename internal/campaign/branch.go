package campaign

import (
	"context"
	"fmt"
	"strings"
)

// DeriveSpecBranchName derives the git branch name from a spec directory name.
// The spec directory follows the convention NN_snake_case_name (e.g. "07_secrets_variables").
// The branch name is spec/<NN>-<kebab-case-name> (e.g. "spec/07-secrets-variables").
func DeriveSpecBranchName(specDir string) string {
	// Replace underscores with hyphens to convert from snake_case to kebab-case.
	kebab := strings.ReplaceAll(specDir, "_", "-")
	return "spec/" + kebab
}

// CreateSpecBranch creates a new spec branch from the given integration branch
// ref using GitOps. It returns the SHA of the newly created branch head.
func CreateSpecBranch(ctx context.Context, ops GitOps, repoPath, branchName, integrationBranch string) (string, error) {
	sha, err := ops.CreateBranch(ctx, repoPath, branchName, integrationBranch)
	if err != nil {
		return "", fmt.Errorf("create spec branch %q: %w", branchName, err)
	}
	return sha, nil
}

// DeleteSpecBranch deletes a spec branch from the repository.
// Used during rollback when campaign creation fails partway through.
func DeleteSpecBranch(ctx context.Context, ops GitOps, repoPath, branchName string) error {
	if err := ops.DeleteBranch(ctx, repoPath, branchName); err != nil {
		return fmt.Errorf("delete spec branch %q: %w", branchName, err)
	}
	return nil
}
