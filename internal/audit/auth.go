package audit

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// hasScope reports whether the credential has any of the given scopes.
// Admin and api_key credentials are implicitly granted all scopes.
func hasScope(auth *apikit.AuthInfo, scopes ...string) bool {
	if wsaccess.IsAdmin(auth) || auth.CredentialType == "api_key" {
		return true
	}
	return wsaccess.HasScope(auth, scopes...)
}

// hasSessionsWrite reports whether the credential has sessions:write scope.
// Admin and api_key credentials are implicitly granted all scopes.
// PAT credentials require explicit scope grants.
func hasSessionsWrite(auth *apikit.AuthInfo) bool {
	return hasScope(auth, "sessions:write")
}

// hasSessionsRead reports whether the credential has sessions:read scope,
// which sessions:write implies.
func hasSessionsRead(auth *apikit.AuthInfo) bool {
	return hasScope(auth, "sessions:read", "sessions:write")
}

// hasAuditWrite reports whether the credential has audit:write scope.
func hasAuditWrite(auth *apikit.AuthInfo) bool {
	return hasScope(auth, "audit:write")
}

// hasAuditRead reports whether the credential has audit:read scope, which
// audit:write implies.
func hasAuditRead(auth *apikit.AuthInfo) bool {
	return hasScope(auth, "audit:read", "audit:write")
}

// requireAuth extracts and validates authentication from the request context.
// Returns the AuthInfo if authenticated, or writes a 401 error and returns nil.
func requireAuth(c echo.Context) *apikit.AuthInfo {
	auth := apikit.GetAuthInfo(c)
	if auth == nil {
		_ = apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		return nil
	}
	return auth
}

// requireAuditWrite checks that the request has audit:write scope.
// Returns the AuthInfo if authorized, or writes an error and returns nil.
func requireAuditWrite(c echo.Context) *apikit.AuthInfo {
	auth := requireAuth(c)
	if auth == nil {
		return nil
	}
	if !hasAuditWrite(auth) {
		_ = apikit.WriteAPIError(c, http.StatusForbidden, "insufficient scope: audit:write required")
		return nil
	}
	return auth
}

// requireAuditRead checks that the request has audit:read scope.
// Returns the AuthInfo if authorized, or writes an error and returns nil.
func requireAuditRead(c echo.Context) *apikit.AuthInfo {
	auth := requireAuth(c)
	if auth == nil {
		return nil
	}
	if !hasAuditRead(auth) {
		_ = apikit.WriteAPIError(c, http.StatusForbidden, "insufficient scope: audit:read required")
		return nil
	}
	return auth
}

// requireWorkspaceRead decides whether the caller may read the workspace
// slug on the per-run audit reads and the transcript: an admin token, the
// workspace's owner and a member of its org may, whatever the workspace's
// status. A workspace that does not exist or that the caller may not read is
// 404 workspace not found, so the endpoints cannot be used to enumerate
// slugs; a failed lookup is 500.
//
// When sqliteDB is nil (route registration without a SQLite handle, used by
// unit tests) the check is skipped; the server binary always wires it.
// Returns false after writing the error response.
func requireWorkspaceRead(c echo.Context, auth *apikit.AuthInfo, slug string, sqliteDB *sql.DB) bool {
	if sqliteDB == nil {
		return true
	}
	return authorizeWorkspace(c, auth, slug, sqliteDB, wsaccess.NeedRead)
}

// requireWorkspaceWrite decides whether the caller may change the workspace
// slug (open a session in it): the owner and admin tokens may, a member of
// the workspace's org is refused with 403 forbidden, and everyone else, like
// an unknown slug, with 404 workspace not found. The workspace's status is
// not checked. When sqliteDB is nil the check is skipped. Returns false after
// writing the refusal.
func requireWorkspaceWrite(c echo.Context, auth *apikit.AuthInfo, slug string, sqliteDB *sql.DB) bool {
	if sqliteDB == nil {
		return true
	}
	return authorizeWorkspace(c, auth, slug, sqliteDB, wsaccess.NeedWrite)
}

// authorizeWorkspace applies the workspace access rule for need, binding
// first: a PAT bound to another workspace is refused with 404 and reported
// once with the request's method, route and status, and then
// wsaccess.AuthorizeWorkspace decides, its own binding check passing
// silently. A failed lookup is 500. Returns false after writing the refusal.
func authorizeWorkspace(c echo.Context, auth *apikit.AuthInfo, slug string, sqliteDB *sql.DB, need wsaccess.Need) bool {
	denied, err := wsaccess.DeniesWorkspace(sqliteDB, auth, slug, wsaccess.SiteOf(c, http.StatusNotFound))
	if err != nil {
		_ = apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		return false
	}
	if denied {
		_ = apikit.WriteAPIError(c, http.StatusNotFound, "workspace not found")
		return false
	}
	if _, status, msg := wsaccess.AuthorizeWorkspace(sqliteDB, auth, slug, need); status != 0 {
		_ = wsaccess.WriteRefusal(c, status, msg)
		return false
	}
	return true
}

// readableWorkspaces is the workspace restriction of the cross-workspace
// audit queries (GET /audit and GET /events): the workspaces, of any status,
// that wsaccess.ReadableSlugs says the caller may read (owned and member
// workspaces; for a PAT bound to a workspace at most that one), or nil for
// no restriction (admin tokens, or no SQLite handle). A failed lookup is 500.
// A requested workspace outside the set answers 404 workspace not found, and
// is reported as a binding refusal when the caller's binding is the cause:
// ReadableSlugs has already confined a bound token, so that
// second lookup only decides whether to report, and its error leaves the
// refusal as it is. It returns false after writing an answer.
func readableWorkspaces(c echo.Context, auth *apikit.AuthInfo, sqliteDB *sql.DB, requested string) (map[string]bool, bool) {
	if sqliteDB == nil {
		return nil, true
	}
	slugs, all, err := wsaccess.ReadableSlugs(sqliteDB, auth)
	if err != nil {
		_ = apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		return nil, false
	}
	if all {
		return nil, true
	}
	readable := make(map[string]bool, len(slugs))
	for slug := range slugs {
		readable[slug] = true
	}
	if requested != "" && !readable[requested] {
		_, _ = wsaccess.DeniesWorkspace(sqliteDB, auth, requested, wsaccess.SiteOf(c, http.StatusNotFound))
		_ = apikit.WriteAPIError(c, http.StatusNotFound, "workspace not found")
		return nil, false
	}
	return readable, true
}

// errWorkspaceAccessDenied is returned by checkWorkspaceAccess once it has
// written a refusal, so that the ingestion handler stops instead of storing
// the event.
var errWorkspaceAccessDenied = errors.New("workspace access denied")

// refused turns the result of writing a refusal into the non-nil error that
// stops the ingestion handler.
func refused(writeErr error) error {
	if writeErr != nil {
		return writeErr
	}
	return errWorkspaceAccessDenied
}

// checkWorkspaceAccess enforces workspace-level access control for audit
// ingestion endpoints. It checks:
// 1. Workspace exists (404)
// 2. A PAT bound to another workspace sees it as missing (404)
// 3. Workspace is not archived (409 workspace_archived)
// 4. Generic PAT/API key owner has write access to the workspace (403 workspace_access_denied)
//
// Admin tokens bypass all workspace restriction checks.
// If sqliteDB is nil, workspace existence and archive checks are skipped.
// After writing a refusal it returns a non-nil error, and the caller must
// stop.
func checkWorkspaceAccess(c echo.Context, auth *apikit.AuthInfo, slug string, sqliteDB *sql.DB) error {
	// Admin tokens bypass all workspace checks.
	if wsaccess.IsAdmin(auth) {
		return nil
	}

	// For generic PATs and API keys, verify workspace access via SQLite.
	if sqliteDB != nil {
		var ownerID string
		var status string
		err := sqliteDB.QueryRow(
			"SELECT owner_id, status FROM workspaces WHERE slug = ?", slug,
		).Scan(&ownerID, &status)
		if err == sql.ErrNoRows {
			return refused(apikit.WriteAPIError(c, http.StatusNotFound,
				"workspace not found"))
		}
		if err != nil {
			return refused(apikit.WriteAPIError(c, http.StatusInternalServerError,
				"failed to check workspace access"))
		}

		// To a bound token every other workspace does not exist.
		denied, err := wsaccess.DeniesWorkspace(sqliteDB, auth, slug, wsaccess.SiteOf(c, http.StatusNotFound))
		if err != nil {
			return refused(apikit.WriteAPIError(c, http.StatusInternalServerError,
				"internal server error"))
		}
		if denied {
			return refused(apikit.WriteAPIError(c, http.StatusNotFound,
				"workspace not found"))
		}

		// Check archived status.
		if status == "archived" {
			return refused(apikit.WriteAPIErrorWithType(c, http.StatusConflict,
				"workspace is archived",
				"workspace_archived"))
		}

		// Generic tokens: check owner has write access to the workspace.
		// For API keys and generic PATs, the UserID must match the workspace owner.
		if auth.UserID != ownerID {
			return refused(apikit.WriteAPIErrorWithType(c, http.StatusForbidden,
				"token owner does not have write access to this workspace",
				"workspace_access_denied"))
		}
	} else {
		// No SQLite DB available — deny access for generic PATs without
		// workspace scope since we cannot verify workspace membership.
		if wsaccess.IsPAT(auth) {
			return refused(apikit.WriteAPIErrorWithType(c, http.StatusForbidden,
				"token owner does not have write access to this workspace",
				"workspace_access_denied"))
		}
	}

	return nil
}
