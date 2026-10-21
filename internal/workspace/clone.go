package workspace

import (
	"context"
	"errors"
	"fmt"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// defaultCloneFn is the production implementation of CloneFuncType.
// It uses go-git's PlainCloneContext to perform a shallow clone with
// context cancellation support. The auth parameter is passed through
// to CloneOptions.Auth; when nil, the clone proceeds without
// authentication (public repo). Returns the 40-character hex SHA of
// the HEAD commit on success.
func defaultCloneFn(ctx context.Context, path string, url string, depth int, singleBranch bool, refName string, auth transport.AuthMethod) (string, error) {
	opts := &git.CloneOptions{
		URL:   url,
		Depth: depth,
		Auth:  auth,
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
// ArchiveOpenAndPushFuncType. It pushes every local branch of the repository
// at repoPath to origin through the push the branch push shares
// (pushToOriginFn), with no deadline. Returns ErrAlreadyUpToDate when the
// remote already has all local commits (nothing to push), and the error of
// opening the repository or finding origin as go-git gave it.
func defaultArchiveOpenAndPushFn(repoPath, gitURL string, auth transport.AuthMethod) error {
	err := pushToOriginFn(context.Background(), repoPath, gitURL, auth,
		[]config.RefSpec{"refs/heads/*:refs/heads/*"}, false, nil)
	var te *trunkError
	if errors.As(err, &te) {
		return te.err
	}
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
