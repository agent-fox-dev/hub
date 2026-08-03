package campaign

import "context"

// GitOps defines git operations needed by the campaign package.
// The campaign package does not depend on internal/gitcmd directly;
// instead, this interface is satisfied by an adapter wrapping GitRunner.
type GitOps interface {
	// CreateBranch creates a new branch from the given ref and returns its SHA.
	CreateBranch(ctx context.Context, repoPath, branchName, fromRef string) (sha string, err error)

	// DeleteBranch deletes a branch from the repository.
	DeleteBranch(ctx context.Context, repoPath, branchName string) error

	// BranchExists checks whether a branch exists in the repository.
	BranchExists(ctx context.Context, repoPath, branchName string) (bool, error)

	// ResolveRef returns the SHA for the given ref (branch name, HEAD, etc.).
	ResolveRef(ctx context.Context, repoPath, ref string) (sha string, err error)
}
