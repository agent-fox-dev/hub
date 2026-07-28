package workspace

import (
	"context"
	"errors"
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

// defaultArchiveOpenAndPushFn is the production implementation of
// ArchiveOpenAndPushFuncType. It opens an existing local repository via
// go-git PlainOpen and pushes to origin. Returns ErrAlreadyUpToDate when
// the remote already has all local commits (nothing to push).
func defaultArchiveOpenAndPushFn(repoPath, gitURL string) error {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return err
	}
	err = repo.Push(&git.PushOptions{
		RemoteName: "origin",
		RemoteURL:  gitURL,
	})
	if err != nil {
		if errors.Is(err, git.NoErrAlreadyUpToDate) {
			return ErrAlreadyUpToDate
		}
		return err
	}
	return nil
}

// defaultArchiveHeadFn is the production implementation of
// ArchiveHeadFuncType. It opens an existing local repository and returns
// the 40-character hex SHA of HEAD.
func defaultArchiveHeadFn(repoPath string) (string, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return "", err
	}
	head, err := repo.Head()
	if err != nil {
		return "", err
	}
	return head.Hash().String(), nil
}
