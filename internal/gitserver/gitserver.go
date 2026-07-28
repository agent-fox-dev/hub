// Package gitserver implements a git smart HTTP server for af-hub,
// exposing workspace repositories over HTTP at /git/<org-slug>/<workspace-slug>.git/
// on the same port as the REST API.
package gitserver

import (
	"database/sql"
	"fmt"
	"path/filepath"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// WorkspaceLoader implements the go-git server.Loader interface,
// resolving a transport.Endpoint to a repository storer for the
// requested workspace. It extracts the org slug and workspace slug from
// the endpoint path, verifies the workspace exists, the org matches,
// and clone_status is 'ready', then opens the repository via PlainOpen
// and returns its storer.Storer.
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
// workspace is not found, not ready, or the org slug does not match.
//
// Returns a wrapped I/O error (not ErrRepositoryNotFound) when PlainOpen
// fails, so the HTTP handler can distinguish 404 from 500.
func (l *WorkspaceLoader) Load(ep *transport.Endpoint) (storer.Storer, error) {
	// Parse org and workspace slug from the endpoint path.
	orgSlug, slug, err := parseEndpointPath(ep.Path)
	if err != nil {
		return nil, transport.ErrRepositoryNotFound
	}

	// Resolve the workspace from the database using shared resolution logic.
	ws, err := resolveWorkspaceFromDB(l.db, orgSlug, slug)
	if err != nil {
		return nil, fmt.Errorf("workspace resolution: %w", err)
	}
	if ws == nil {
		return nil, transport.ErrRepositoryNotFound
	}

	// Verify clone_status is 'ready' (06-REQ-4.4).
	if ws.CloneStatus != "ready" {
		return nil, transport.ErrRepositoryNotFound
	}

	// Open the repository at <workspaceRoot>/<slug>/trunk/.
	repoPath := filepath.Join(l.workspaceRoot, slug, "trunk")
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("open repository at %s: %w", repoPath, err)
	}

	// Wrap the storer so it does NOT satisfy storer.PackfileWriter.
	// go-git's packfile.UpdateObjectStorage takes a raw-copy shortcut
	// when the storer implements PackfileWriter, but that path parses
	// the incoming pack without access to the existing object store,
	// so REF_DELTA objects in thin packs (sent by git push) fail with
	// "reference delta not found". The wrapper forces the parser-with-
	// storage path, which can resolve deltas against existing objects.
	return &thinPackSafeStorer{repo.Storer}, nil
}

// thinPackSafeStorer wraps a storer.Storer without implementing
// storer.PackfileWriter, forcing go-git to use the parser path
// that can resolve thin pack deltas against the existing object store.
type thinPackSafeStorer struct {
	storer.Storer
}
