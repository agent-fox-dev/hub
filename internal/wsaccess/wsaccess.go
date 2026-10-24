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
	"errors"
	"net/http"
	"slices"

	"github.com/txsvc/apikit"
)

// WorkspaceAccess is the minimal workspace state returned by
// AuthorizeWorkspace: enough for per-workspace endpoints in other packages to
// apply their own precondition checks without a second lookup, and the
// caller's access. OrgID is "" when the workspace has no org.
type WorkspaceAccess struct {
	Slug        string
	OwnerID     string
	OrgID       string
	Status      string
	CloneStatus string
	Access      Access
}

// Access is a caller's level on one workspace, as AuthorizeWorkspace decides
// it: an admin token, the workspace's owner, a member of the workspace's org,
// or none of these.
type Access string

// Access levels.
const (
	AccessNone   Access = ""
	AccessMember Access = "member"
	AccessOwner  Access = "owner"
	AccessAdmin  Access = "admin"
)

// Need is what an endpoint requires of the caller's access. NeedRead admits
// members, owners and admin tokens. NeedWrite and NeedPrivate admit owners and
// admin tokens; a member is refused with 403 on NeedWrite and, as if the
// workspace did not exist, with 404 on NeedPrivate.
type Need string

// Needs.
const (
	NeedRead    Need = "read"
	NeedWrite   Need = "write"
	NeedPrivate Need = "private"
)

// AuthorizeWorkspace decides what the caller in auth may do with the
// workspace identified by slug, for an endpoint that needs need. It is
// AuthorizeWorkspaceAt for callers that have no request: a binding refusal is
// reported with status 404 and the API surface, without method or route.
//
// It is the shared entry point for every REST handler that addresses a
// workspace by slug, so that workspace, sync, reclone, patch, merge, rebuild,
// rerere, patch-status, sandbox lease, forge, secrets, variables and bound
// token endpoints apply one rule.
func AuthorizeWorkspace(db *sql.DB, auth *apikit.AuthInfo, slug string, need Need) (*WorkspaceAccess, int, string) {
	return AuthorizeWorkspaceAt(db, auth, slug, need, RefusalSite{Status: http.StatusNotFound, Surface: SurfaceAPI})
}

// AuthorizeWorkspaceAt applies hub's workspace access rule, in this order:
//
//  1. a nil auth is 401, without a query;
//  2. a slug that names no workspace is 404;
//  3. a caller with AccessNone (see accessOf) is 404, with no binding lookup
//     and no refusal report;
//  4. a PAT bound to another workspace is 404 (spec 33), and the refusal is
//     reported once with site (spec 34);
//  5. a member is 403 on NeedWrite and 404 on NeedPrivate.
//
// On success it returns the workspace access record, with the caller's
// Access, and (0, ""). On failure it returns nil and the status plus message
// the caller should respond with (see WriteRefusal). A workspace the caller
// may not see is 404, not 403, so that slugs are not disclosed
// (anti-enumeration). A database error is 500, and never a grant.
func AuthorizeWorkspaceAt(db *sql.DB, auth *apikit.AuthInfo, slug string, need Need, site RefusalSite) (*WorkspaceAccess, int, string) {
	if auth == nil {
		return nil, http.StatusUnauthorized, "authentication required"
	}
	var ws WorkspaceAccess
	err := db.QueryRow(
		`SELECT slug, owner_id, COALESCE(org_id, ''), status, clone_status FROM workspaces WHERE slug = ?`, slug,
	).Scan(&ws.Slug, &ws.OwnerID, &ws.OrgID, &ws.Status, &ws.CloneStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, http.StatusNotFound, "workspace not found"
	}
	if err != nil {
		return nil, http.StatusInternalServerError, "internal server error"
	}
	ws.Access, err = accessOf(db, auth, ws.OwnerID, ws.OrgID)
	if err != nil {
		return nil, http.StatusInternalServerError, "internal server error"
	}
	if ws.Access == AccessNone {
		return nil, http.StatusNotFound, "workspace not found"
	}
	// A PAT bound to another workspace must not see this one (spec 33),
	// whatever its access and need. The workspace row is closed by now, so
	// the lookup can run on a single-connection pool; a failed lookup denies
	// access.
	denied, err := DeniesWorkspace(db, auth, ws.Slug, site)
	if err != nil {
		return nil, http.StatusInternalServerError, "internal server error"
	}
	if denied {
		return nil, http.StatusNotFound, "workspace not found"
	}
	if ws.Access == AccessMember {
		switch need {
		case NeedWrite:
			return nil, http.StatusForbidden, "org members have read-only access to this workspace"
		case NeedPrivate:
			return nil, http.StatusNotFound, "workspace not found"
		}
	}
	return &ws, 0, ""
}

// accessOf decides the caller's access to a workspace owned by ownerID in the
// org orgID ("" for none): an admin token is AccessAdmin and the owner is
// AccessOwner, both without reading org_members; a caller with a user id is
// AccessMember when org_members has the row (orgID, user id), read on every
// call; everyone else is AccessNone.
func accessOf(db *sql.DB, auth *apikit.AuthInfo, ownerID, orgID string) (Access, error) {
	if IsAdmin(auth) {
		return AccessAdmin, nil
	}
	if auth.UserID == "" {
		return AccessNone, nil
	}
	if auth.UserID == ownerID {
		return AccessOwner, nil
	}
	if orgID == "" {
		return AccessNone, nil
	}
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM org_members WHERE org_id = ? AND user_id = ?`, orgID, auth.UserID,
	).Scan(&n); err != nil {
		return AccessNone, err
	}
	if n > 0 {
		return AccessMember, nil
	}
	return AccessNone, nil
}

// IsAdmin reports whether the credential is an admin token.
func IsAdmin(auth *apikit.AuthInfo) bool {
	return auth.CredentialType == "admin_token"
}

// IsPAT reports whether the credential is a personal access token, the one
// credential type whose scopes are checked.
func IsPAT(auth *apikit.AuthInfo) bool {
	return auth.CredentialType == "pat"
}

// HasScope reports whether the credential carries any of the scopes.
func HasScope(auth *apikit.AuthInfo, scopes ...string) bool {
	for _, s := range scopes {
		if slices.Contains(auth.Permissions, s) {
			return true
		}
	}
	return false
}
