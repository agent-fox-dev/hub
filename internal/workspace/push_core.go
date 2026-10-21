package workspace

import (
	"context"
	"io"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// trunkError is a failure to open the trunk or to find its origin remote: a
// failure of hub's own, before anything was handed to the transport.
type trunkError struct{ err error }

func (e *trunkError) Error() string { return "trunk unavailable: " + e.err.Error() }
func (e *trunkError) Unwrap() error { return e.err }

// pushToOrigin is the one push to origin that the archive, the reclone and the
// branch push share (38-REQ-10.1). It opens the trunk at repoPath and pushes
// refSpecs to its remote origin at gitURL with auth (nil for none), on ctx. force
// sets PushOptions.Force; progress, when not nil, receives what the remote reports
// while it runs, such as the reason a hook gives for refusing a ref.
//
// It returns go-git's error as it is, git.NoErrAlreadyUpToDate included, and a
// *trunkError when the trunk cannot be opened or has no origin.
func pushToOrigin(ctx context.Context, repoPath, gitURL string, auth transport.AuthMethod, refSpecs []config.RefSpec, force bool, progress io.Writer) error {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return &trunkError{err}
	}
	if _, err := repo.Remote("origin"); err != nil {
		return &trunkError{err}
	}
	return repo.PushContext(ctx, &git.PushOptions{
		RemoteName: "origin",
		RemoteURL:  gitURL,
		Auth:       auth,
		RefSpecs:   refSpecs,
		Force:      force,
		Progress:   progress,
	})
}

// pushToOriginFn is the shared push the archive and the branch push call. Tests
// replace it to count, observe or fail the pushes.
var pushToOriginFn = pushToOrigin
