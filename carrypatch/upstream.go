package carrypatch

import (
	"context"
	"errors"
	"fmt"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// UpstreamRemote is the name of the upstream remote in a carry-patch
// workspace's clone. The host's clone setup creates it.
const UpstreamRemote = "upstream"

// UpstreamHeadRef is the local ref that tracks the upstream repository's HEAD
// (its default branch). fetchUpstream writes it, and every rebuild, sync and
// preview resolves it as "upstream HEAD", so the module and the host's clone
// setup agree on one base commit.
const UpstreamHeadRef = "refs/remotes/upstream/HEAD"

// upstreamRefSpecs fetches every upstream branch into refs/remotes/upstream/*
// and stores the commit the remote's HEAD points at under UpstreamHeadRef.
// Without the HEAD refspec the only handle on "what did we just fetch" is
// FETCH_HEAD, whose first line after a plain fetch is the alphabetically first
// branch of the remote, not its default branch.
var upstreamRefSpecs = []config.RefSpec{
	config.RefSpec("+refs/heads/*:refs/remotes/upstream/*"),
	config.RefSpec("+HEAD:" + UpstreamHeadRef),
}

// fetchUpstream fetches the upstream remote of the repository at repoPath with
// the given credentials (nil for a public upstream). An already-up-to-date
// remote is not an error.
func fetchUpstream(ctx context.Context, repoPath string, auth transport.AuthMethod) error {
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
