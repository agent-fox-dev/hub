package workspace

import (
	"database/sql"
	"net/http"

	"github.com/txsvc/apikit"
)

// WorkspaceAccess is the minimal workspace state returned by
// AuthorizeWorkspace: enough for per-workspace endpoints in other packages to
// apply their own precondition checks without a second lookup.
type WorkspaceAccess struct {
	Slug        string
	OwnerID     string
	Status      string
	CloneStatus string
}

// AuthorizeWorkspace loads the workspace identified by slug and enforces the
// hub's ownership rule for every per-workspace endpoint: admin tokens may act
// on any workspace, every other credential only on workspaces it owns.
//
// It is the shared entry point for packages outside workspace (carrypatch,
// merge) that address workspaces by slug, so that sync, reclone, patch,
// merge, rebuild, rerere, and patch-status endpoints apply the same rule as
// the workspace CRUD handlers.
//
// On success it returns the workspace access record and (0, ""). On failure
// it returns nil and the HTTP status plus message the caller should respond
// with. A workspace the caller does not own is reported as 404 (not 403) to
// avoid disclosing which slugs exist (anti-enumeration).
func AuthorizeWorkspace(db *sql.DB, auth *apikit.AuthInfo, slug string) (*WorkspaceAccess, int, string) {
	if auth == nil {
		return nil, http.StatusUnauthorized, "authentication required"
	}
	var ws WorkspaceAccess
	err := db.QueryRow(
		`SELECT slug, owner_id, status, clone_status FROM workspaces WHERE slug = ?`, slug,
	).Scan(&ws.Slug, &ws.OwnerID, &ws.Status, &ws.CloneStatus)
	if err == sql.ErrNoRows {
		return nil, http.StatusNotFound, "workspace not found"
	}
	if err != nil {
		return nil, http.StatusInternalServerError, "internal server error"
	}
	if !isAdmin(auth) && ws.OwnerID != auth.UserID {
		return nil, http.StatusNotFound, "workspace not found"
	}
	return &ws, 0, ""
}
