package workspace

import (
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/hub/internal/secrets"
)

// resolveCloneAuth looks up stored git credentials for a workspace from the
// secrets store and returns the appropriate transport.AuthMethod for use in
// clone operations.
//
// Lookup order (09-REQ-6.1, 09-REQ-6.2, 09-REQ-6.3):
//  1. GIT_PAT → &BasicAuth{Username: "x-token-auth", Password: <pat>}
//  2. GIT_USERNAME + GIT_PASSWORD → &BasicAuth{Username: <user>, Password: <pass>}
//  3. No credentials → nil (public repo, pre-feature behavior preserved)
//
// Returns a non-nil error (other than NotFoundError) if the secrets store
// lookup fails unexpectedly (09-REQ-6.E1). Partial credentials
// (GIT_USERNAME without GIT_PASSWORD) are logged as a warning and treated
// as no credentials (09-REQ-6.E2).
func resolveCloneAuth(store *secrets.Store, slug string) (transport.AuthMethod, error) {
	// Stub: will be implemented in task group 6.
	_ = store
	_ = slug
	return nil, nil
}
