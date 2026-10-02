package workspace

import (
	"database/sql"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// WorkspaceAccess is an alias for wsaccess.WorkspaceAccess so that existing
// callers (internal/merge, tests, etc.) continue to compile without changes.
type WorkspaceAccess = wsaccess.WorkspaceAccess

// AuthorizeWorkspace delegates to wsaccess.AuthorizeWorkspace. It is kept as
// a thin wrapper so that callers inside and outside the workspace package do
// not need to change their import paths.
func AuthorizeWorkspace(db *sql.DB, auth *apikit.AuthInfo, slug string) (*WorkspaceAccess, int, string) {
	return wsaccess.AuthorizeWorkspace(db, auth, slug)
}
