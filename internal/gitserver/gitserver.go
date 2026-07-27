// Package gitserver implements a git smart HTTP server for af-hub,
// exposing workspace repositories over HTTP at /git/<org-slug>/<workspace-slug>.git/
// on the same port as the REST API.
package gitserver

import (
	"database/sql"

	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// WorkspaceLoader implements the go-git server.Loader interface,
// resolving a transport.Endpoint to a repository storer for the
// requested workspace. It extracts the workspace slug from the
// endpoint path, verifies the workspace exists and has clone_status
// 'ready', opens the repository via PlainOpen, and returns its
// storer.Storer.
type WorkspaceLoader struct {
	db            *sql.DB
	workspaceRoot string
}

// NewWorkspaceLoader creates a new WorkspaceLoader that resolves
// workspace repositories under the given workspaceRoot directory.
func NewWorkspaceLoader(db *sql.DB, workspaceRoot string) *WorkspaceLoader {
	return &WorkspaceLoader{db: db, workspaceRoot: workspaceRoot}
}

// Load resolves a transport.Endpoint to a storer.Storer for the
// workspace repository. Returns transport.ErrRepositoryNotFound if the
// workspace is not found, not ready, or the org check fails.
func (l *WorkspaceLoader) Load(ep *transport.Endpoint) (storer.Storer, error) {
	// TODO: implement — resolve endpoint to workspace storer.
	return nil, nil
}
