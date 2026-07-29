package workspace

import (
	"context"

	"github.com/go-git/go-git/v5/plumbing/transport"
)

// ValidateCredentialsFuncType is the signature for the function that validates
// git credentials by performing an ls-remote operation against the upstream
// repository.
//
// Parameters:
//   - ctx: context for cancellation and timeout
//   - gitURL: the upstream repository URL
//   - auth: the transport.AuthMethod to authenticate with (BasicAuth)
//
// Returns nil on success, or an error on authentication failure / timeout.
type ValidateCredentialsFuncType func(ctx context.Context, gitURL string, auth transport.AuthMethod) error

// validateCredentialsFn is the injectable credential validation function used
// by handleCreateWorkspace. Tests replace it to capture arguments or simulate
// errors. The production default (set during server init) uses NewRemote +
// memory.NewStorage() + Remote.ListContext with a 10-second deadline.
var validateCredentialsFn ValidateCredentialsFuncType
