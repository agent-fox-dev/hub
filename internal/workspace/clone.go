package workspace

import (
	"context"
	"fmt"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// defaultCloneFn is the production implementation of CloneFuncType.
// It uses go-git's PlainCloneContext to perform a shallow clone with
// context cancellation support. Returns the 40-character hex SHA of
// the HEAD commit on success.
func defaultCloneFn(ctx context.Context, path string, url string, depth int, singleBranch bool, refName string) (string, error) {
	opts := &git.CloneOptions{
		URL:   url,
		Depth: depth,
	}
	if singleBranch {
		opts.SingleBranch = true
		opts.ReferenceName = plumbing.ReferenceName(refName)
	}

	repo, err := git.PlainCloneContext(ctx, path, false, opts)
	if err != nil {
		return "", fmt.Errorf("clone %q: %w", url, err)
	}

	head, err := repo.Head()
	if err != nil {
		return "", fmt.Errorf("read HEAD after clone: %w", err)
	}

	return head.Hash().String(), nil
}
