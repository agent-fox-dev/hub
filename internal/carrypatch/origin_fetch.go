package carrypatch

import (
	"context"
	"errors"
	"fmt"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// OriginRemoteName is the name of the origin (fork) remote in carry-patch workspaces.
const OriginRemoteName = "origin"

// OriginRefSpecs fetches every origin branch into refs/remotes/origin/*.
var OriginRefSpecs = []config.RefSpec{
	config.RefSpec("+refs/heads/*:refs/remotes/origin/*"),
}

// DefaultOriginFetchFunc returns a FetchFunc that fetches the 'origin' remote
// of the repository at repoPath using the given credentials (nil for public forks).
// It updates the refs/remotes/origin/* tracking refs. An already-up-to-date
// remote is not an error.
func DefaultOriginFetchFunc() FetchFunc {
	return func(ctx context.Context, repoPath string, auth transport.AuthMethod) error {
		repo, err := git.PlainOpen(repoPath)
		if err != nil {
			return fmt.Errorf("origin: open repository at %s: %w", repoPath, err)
		}
		remote, err := repo.Remote(OriginRemoteName)
		if err != nil {
			return fmt.Errorf("origin: remote %q: %w", OriginRemoteName, err)
		}
		err = remote.FetchContext(ctx, &git.FetchOptions{
			RemoteName: OriginRemoteName,
			RefSpecs:   OriginRefSpecs,
			Auth:       auth,
			Tags:       git.NoTags,
		})
		if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
			return fmt.Errorf("origin: fetch %q: %w", OriginRemoteName, err)
		}
		return nil
	}
}
