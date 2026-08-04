package workspace

import (
	"context"
	"errors"
	"fmt"
	"log"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// defaultSyncFetchAndCompareFn is the production implementation of
// SyncFetchAndCompareFuncType. It uses go-git to:
//  1. Open the local repository at repoPath
//  2. Fetch from the "origin" remote with the provided auth
//  3. Read the upstream HEAD SHA from the remote tracking ref
//  4. Compare upstream HEAD against localHeadSHA using ancestry checks
//
// Returns the upstream HEAD SHA, the comparison outcome, and any error.
func defaultSyncFetchAndCompareFn(ctx context.Context, repoPath string, auth transport.AuthMethod, branch *string, localHeadSHA string) (string, string, error) {
	// Step 1: Open the local repository (13-REQ-4.E4).
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return "", "", fmt.Errorf("open repository at %s: %w", repoPath, err)
	}

	// Step 2: Fetch from upstream (13-REQ-4.1).
	remote, err := repo.Remote("origin")
	if err != nil {
		return "", "", fmt.Errorf("get remote 'origin': %w", err)
	}

	fetchOpts := &git.FetchOptions{
		RemoteName: "origin",
		Auth:       auth,
	}

	fetchErr := remote.FetchContext(ctx, fetchOpts)
	if fetchErr != nil && !errors.Is(fetchErr, git.NoErrAlreadyUpToDate) {
		// 13-REQ-4.E1: Check for shallow clone issues and retry with unshallow.
		if isShallowError(fetchErr) {
			log.Printf("WARNING: fetch failed due to shallow clone for %s, retrying with full history", repoPath)
			fetchOpts.Depth = 0 // full history (unshallow)
			fetchErr = remote.FetchContext(ctx, fetchOpts)
			if fetchErr != nil && !errors.Is(fetchErr, git.NoErrAlreadyUpToDate) {
				return "", "", fmt.Errorf("unshallow fetch failed: %w", fetchErr)
			}
		} else {
			return "", "", fmt.Errorf("fetch failed: %w", fetchErr)
		}
	}

	// Step 3: Read the upstream HEAD SHA from the remote tracking ref.
	upstreamRefName := resolveUpstreamRefName(repo, branch)
	upstreamRef, err := repo.Reference(upstreamRefName, true)
	if err != nil {
		return "", "", fmt.Errorf("read upstream ref %s: %w", upstreamRefName, err)
	}
	upstreamHeadSHA := upstreamRef.Hash().String()

	// Step 4: Compare upstream HEAD with local HEAD (13-REQ-4.2, 4.3, 4.4).
	if localHeadSHA == "" || localHeadSHA == upstreamHeadSHA {
		// No local HEAD or they're equal → up to date.
		return upstreamHeadSHA, "up_to_date", nil
	}

	// Check if local HEAD is an ancestor of upstream HEAD (fast-forward possible).
	localHash := plumbing.NewHash(localHeadSHA)
	upstreamHash := plumbing.NewHash(upstreamHeadSHA)

	localCommit, err := repo.CommitObject(localHash)
	if err != nil {
		return upstreamHeadSHA, "diverged", nil // Can't find local commit → treat as diverged
	}

	upstreamCommit, err := repo.CommitObject(upstreamHash)
	if err != nil {
		return upstreamHeadSHA, "diverged", nil // Can't find upstream commit → treat as diverged
	}

	isAncestor, err := localCommit.IsAncestor(upstreamCommit)
	if err != nil {
		// Ancestry check failed — conservative: treat as diverged.
		return upstreamHeadSHA, "diverged", nil
	}

	if isAncestor {
		return upstreamHeadSHA, "fast_forward", nil
	}

	// Local is not an ancestor of upstream → diverged (force-push detected).
	return upstreamHeadSHA, "diverged", nil
}

// defaultSyncUpdateLocalRefFn is the production implementation of
// SyncUpdateLocalRefFuncType. It opens the repo and updates the local
// integration branch ref to the given SHA (fast-forward).
func defaultSyncUpdateLocalRefFn(repoPath string, branch *string, newSHA string) error {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return fmt.Errorf("open repository at %s: %w", repoPath, err)
	}

	newHash := plumbing.NewHash(newSHA)

	// Determine the branch ref name.
	var refName plumbing.ReferenceName
	if branch != nil && *branch != "" {
		refName = plumbing.NewBranchReferenceName(*branch)
	} else {
		// Default branch: read HEAD to find out which branch it points to.
		headRef, err := repo.Head()
		if err != nil {
			return fmt.Errorf("read HEAD: %w", err)
		}
		refName = headRef.Name()
	}

	// Update the branch ref to the new SHA.
	newRef := plumbing.NewHashReference(refName, newHash)
	if err := repo.Storer.SetReference(newRef); err != nil {
		return fmt.Errorf("update ref %s: %w", refName, err)
	}

	return nil
}

// resolveUpstreamRefName determines the remote tracking ref name for the
// upstream branch. If branch is nil, it tries to determine the default
// branch from the repository's HEAD.
func resolveUpstreamRefName(repo *git.Repository, branch *string) plumbing.ReferenceName {
	if branch != nil && *branch != "" {
		return plumbing.NewRemoteReferenceName("origin", *branch)
	}

	// Try to determine default branch from HEAD.
	headRef, err := repo.Head()
	if err == nil {
		branchName := headRef.Name().Short()
		return plumbing.NewRemoteReferenceName("origin", branchName)
	}

	// Fallback to main.
	return plumbing.NewRemoteReferenceName("origin", "main")
}

// isShallowError checks whether a fetch error indicates a shallow clone
// issue that might be resolved by an unshallow fetch.
func isShallowError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	// go-git may report errors related to missing objects or shallow history.
	return contains(errStr, "shallow") ||
		contains(errStr, "missing object") ||
		contains(errStr, "object not found")
}

// contains checks if s contains substr (case-sensitive).
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
