package workspace

import (
	"context"
	"errors"
	"fmt"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// UpstreamRemote is the name of the upstream remote in carry-patch workspaces.
const UpstreamRemote = "upstream"

// UpstreamHeadRef is the local ref that tracks the upstream repository's HEAD
// (its default branch). It is written by FetchUpstream and is the canonical
// "upstream HEAD" every carry-patch operation resolves, so that rebuilds,
// syncs and previews all agree on the same base commit.
const UpstreamHeadRef = "refs/remotes/upstream/HEAD"

// upstreamRefSpecs fetches every upstream branch into refs/remotes/upstream/*
// and additionally stores the commit the remote's HEAD points at under
// UpstreamHeadRef.
//
// Without the HEAD refspec the only handle on "what did we just fetch" is
// FETCH_HEAD, whose first line after a plain `git fetch upstream` is the
// alphabetically first branch of the remote, not its default branch, so a
// multi-branch upstream would silently rebase the patch stack onto the wrong
// base.
var upstreamRefSpecs = []config.RefSpec{
	config.RefSpec("+refs/heads/*:refs/remotes/upstream/*"),
	config.RefSpec("+HEAD:" + UpstreamHeadRef),
}

// FetchUpstream fetches the upstream remote of the repository at repoPath
// using the given credentials (nil for public upstreams). It updates the
// refs/remotes/upstream/* tracking refs and UpstreamHeadRef. An
// already-up-to-date remote is not an error.
func FetchUpstream(ctx context.Context, repoPath string, auth transport.AuthMethod) error {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return fmt.Errorf("upstream: open repository at %s: %w", repoPath, err)
	}
	remote, err := repo.Remote(UpstreamRemote)
	if err != nil {
		return fmt.Errorf("upstream: remote %q: %w", UpstreamRemote, err)
	}
	err = remote.FetchContext(ctx, &git.FetchOptions{
		RemoteName: UpstreamRemote,
		RefSpecs:   upstreamRefSpecs,
		Auth:       auth,
		Tags:       git.NoTags,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("upstream: fetch %q: %w", UpstreamRemote, err)
	}
	return nil
}
