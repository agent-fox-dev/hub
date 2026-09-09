// Package upstream fetches the upstream remote of a carry-patch workspace
// and records the upstream default branch so that rebuilds, syncs, and
// previews all agree on the same base commit.
package upstream

import (
	"context"
	"errors"
	"fmt"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// RemoteName is the name of the upstream remote in carry-patch workspaces.
const RemoteName = "upstream"

// HeadRef is the local ref that tracks the upstream repository's HEAD (its
// default branch). It is written by Fetch and is the canonical "upstream
// HEAD" every carry-patch operation resolves.
const HeadRef = "refs/remotes/upstream/HEAD"

// refSpecs fetches every upstream branch into refs/remotes/upstream/* and
// additionally stores the commit the remote's HEAD points at under HeadRef.
//
// Without the HEAD refspec the only handle on "what did we just fetch" is
// FETCH_HEAD, whose first line after a plain `git fetch upstream` is the
// alphabetically first branch of the remote, not its default branch, so a
// multi-branch upstream would silently rebase the patch stack onto the wrong
// base.
var refSpecs = []config.RefSpec{
	config.RefSpec("+refs/heads/*:refs/remotes/upstream/*"),
	config.RefSpec("+HEAD:" + HeadRef),
}

// Fetch fetches the upstream remote of the repository at repoPath using the
// given credentials (nil for public upstreams). It updates the
// refs/remotes/upstream/* tracking refs and HeadRef. An already-up-to-date
// remote is not an error.
func Fetch(ctx context.Context, repoPath string, auth transport.AuthMethod) error {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return fmt.Errorf("upstream: open repository at %s: %w", repoPath, err)
	}
	remote, err := repo.Remote(RemoteName)
	if err != nil {
		return fmt.Errorf("upstream: remote %q: %w", RemoteName, err)
	}
	err = remote.FetchContext(ctx, &git.FetchOptions{
		RemoteName: RemoteName,
		RefSpecs:   refSpecs,
		Auth:       auth,
		Tags:       git.NoTags,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("upstream: fetch %q: %w", RemoteName, err)
	}
	return nil
}
