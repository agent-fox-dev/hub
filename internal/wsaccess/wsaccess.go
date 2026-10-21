// Package wsaccess provides workspace authorization helpers and a per-request
// scope that can be imported by packages that must not depend on
// internal/workspace (such as internal/carrypatch). It breaks the import cycle
// that would otherwise arise when both packages need AuthorizeWorkspace, and
// it is the one leaf package both sides share, so a request handler in
// internal/workspace can hand request-scoped state (see WithRequestScope and
// RequestScoped) to a hook implemented in internal/carrypatch through the
// context alone.
package wsaccess

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
// avoid disclosing which slugs exist (anti-enumeration), and so is every
// workspace other than the one a bound PAT is bound to (see DeniesWorkspace).
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
	// A PAT bound to another workspace must not see this one (spec 33). The
	// workspace row is closed by now, so the lookup can run on a
	// single-connection pool; a failed lookup denies access.
	// It has no request, so its report carries no method or route.
	denied, err := DeniesWorkspace(db, auth, ws.Slug, RefusalSite{Status: http.StatusNotFound, Surface: SurfaceAPI})
	if err != nil {
		return nil, http.StatusInternalServerError, "internal server error"
	}
	if denied {
		return nil, http.StatusNotFound, "workspace not found"
	}
	return &ws, 0, ""
}

// isAdmin reports whether the credential is an admin token.
func isAdmin(auth *apikit.AuthInfo) bool {
	return auth.CredentialType == "admin_token"
}
