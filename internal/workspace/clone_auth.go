package workspace

import (
	"fmt"
	"log"

	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

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
	// Step 1: Look up GIT_PAT (09-REQ-6.1).
	pat, err := store.GetSecretValue("workspace", slug, "GIT_PAT")
	if err == nil {
		return &githttp.BasicAuth{
			Username: "x-token-auth",
			Password: pat,
		}, nil
	}
	if !isNotFoundError(err) {
		return nil, fmt.Errorf("lookup GIT_PAT for workspace %q: %w", slug, err)
	}

	// Step 2: GIT_PAT not found — try GIT_USERNAME + GIT_PASSWORD (09-REQ-6.2).
	username, err := store.GetSecretValue("workspace", slug, "GIT_USERNAME")
	if err != nil {
		if !isNotFoundError(err) {
			return nil, fmt.Errorf("lookup GIT_USERNAME for workspace %q: %w", slug, err)
		}
		// Neither GIT_PAT nor GIT_USERNAME found — no credentials (09-REQ-6.3).
		return nil, nil
	}

	// Step 3: GIT_USERNAME found — look up GIT_PASSWORD.
	password, err := store.GetSecretValue("workspace", slug, "GIT_PASSWORD")
	if err != nil {
		if !isNotFoundError(err) {
			return nil, fmt.Errorf("lookup GIT_PASSWORD for workspace %q: %w", slug, err)
		}
		// GIT_USERNAME found but GIT_PASSWORD missing — inconsistent (09-REQ-6.E2).
		log.Printf("WARNING: workspace %q has GIT_USERNAME but no GIT_PASSWORD; treating as no credentials", slug)
		return nil, nil
	}

	return &githttp.BasicAuth{
		Username: username,
		Password: password,
	}, nil
}

// ResolveCloneAuth is the exported version of resolveCloneAuth, provided for
// use by other packages (e.g., the merge handler) that need to resolve git
// credentials for a workspace.
func ResolveCloneAuth(store *secrets.Store, slug string) (transport.AuthMethod, error) {
	return resolveCloneAuth(store, slug)
}

// resolveUpstreamAuth looks up upstream-specific credentials for a carry_patch
// workspace. Falls back to resolveCloneAuth if no upstream-specific secrets
// are found (15-REQ-5.1).
//
// Lookup order:
//  1. UPSTREAM_GIT_PAT → &BasicAuth{Username: "x-token-auth", Password: <pat>}
//  2. UPSTREAM_GIT_USERNAME + UPSTREAM_GIT_PASSWORD → &BasicAuth{...}
//  3. Fall back to resolveCloneAuth (origin credentials or nil)
func resolveUpstreamAuth(store *secrets.Store, slug string) (transport.AuthMethod, error) {
	// TODO: implement in task group 6
	return nil, nil
}

// isNotFoundError checks whether err is a *secrets.NotFoundError.
func isNotFoundError(err error) bool {
	_, ok := err.(*secrets.NotFoundError)
	return ok
}
