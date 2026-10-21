package audit

import (
	"database/sql"
	"errors"
	"net/http"
	"slices"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// isAdmin reports whether the credential is an admin token.
func isAdmin(auth *apikit.AuthInfo) bool {
	return auth.CredentialType == "admin_token"
}

// isPAT reports whether the credential is a Personal Access Token.
func isPAT(auth *apikit.AuthInfo) bool {
	return auth.CredentialType == "pat"
}

// hasScope reports whether the credential has any of the given scopes.
// Admin and api_key credentials are implicitly granted all scopes.
func hasScope(auth *apikit.AuthInfo, scopes ...string) bool {
	if isAdmin(auth) || auth.CredentialType == "api_key" {
		return true
	}
	for _, s := range scopes {
		if slices.Contains(auth.Permissions, s) {
			return true
		}
	}
	return false
}

// hasSessionsWrite reports whether the credential has sessions:write scope.
// Admin and api_key credentials are implicitly granted all scopes.
// PAT credentials require explicit scope grants.
func hasSessionsWrite(auth *apikit.AuthInfo) bool {
	return hasScope(auth, "sessions:write")
}

// hasSessionsRead reports whether the credential has sessions:read scope.
func hasSessionsRead(auth *apikit.AuthInfo) bool {
	return hasScope(auth, "sessions:read")
}

// hasAuditWrite reports whether the credential has audit:write scope.
func hasAuditWrite(auth *apikit.AuthInfo) bool {
	return hasScope(auth, "audit:write")
}

// hasAuditRead reports whether the credential has audit:read scope.
func hasAuditRead(auth *apikit.AuthInfo) bool {
	return hasScope(auth, "audit:read")
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

// requireWorkspaceRead enforces workspace ownership on audit read endpoints:
// admin tokens may read any workspace, every other credential only
// workspaces it owns. Unknown and non-owned workspaces both yield 404 so the
// endpoint cannot be used to enumerate slugs.
//
// When sqliteDB is nil (route registration without a SQLite handle, used by
// unit tests) the check is skipped; the server binary always wires it.
// Returns false after writing the error response.
func requireWorkspaceRead(c echo.Context, auth *apikit.AuthInfo, slug string, sqliteDB *sql.DB) bool {
	if isAdmin(auth) || sqliteDB == nil {
		return true
	}
	var ownerID string
	err := sqliteDB.QueryRow("SELECT owner_id FROM workspaces WHERE slug = ?", slug).Scan(&ownerID)
	if err != nil || ownerID != auth.UserID {
		_ = apikit.WriteAPIError(c, http.StatusNotFound, "workspace not found")
		return false
	}
	// A PAT bound to another workspace treats this one as not its own
	// (spec 33).
	denied, err := wsaccess.DeniesWorkspace(sqliteDB, auth, slug, wsaccess.SiteOf(c, http.StatusNotFound))
	if err != nil {
		_ = apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		return false
	}
	if denied {
		_ = apikit.WriteAPIError(c, http.StatusNotFound, "workspace not found")
		return false
	}
	return true
}

// ownedWorkspaces returns the set of workspace slugs the caller may read for
// cross-workspace audit endpoints. It returns nil (unrestricted) for admin
// tokens or when sqliteDB is nil, and an empty set when the caller owns no
// workspaces. A PAT bound to a workspace (spec 33) may read at most that one;
// its binding is resolved before any rows are opened.
func ownedWorkspaces(auth *apikit.AuthInfo, sqliteDB *sql.DB) (map[string]bool, error) {
	if isAdmin(auth) || sqliteDB == nil {
		return nil, nil
	}
	boundSlug, bound, err := wsaccess.BoundSlug(sqliteDB, auth)
	if err != nil {
		return nil, err
	}
	rows, err := sqliteDB.Query("SELECT slug FROM workspaces WHERE owner_id = ?", auth.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	owned := make(map[string]bool)
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, err
		}
		if bound && slug != boundSlug {
			continue
		}
		owned[slug] = true
	}
	return owned, rows.Err()
}

// readableWorkspaces is the workspace restriction of the cross-workspace
// audit queries (GET /audit and GET /events): the workspaces the caller may
// read (nil for no restriction, see ownedWorkspaces). A requested workspace
// outside them answers 404 workspace not found, and is reported as a binding
// refusal when the caller's binding is the cause (spec 34): ownedWorkspaces
// has already confined a bound token, so that second lookup only decides
// whether to report, and its error leaves the refusal as it is. It returns
// false after writing an answer.
func readableWorkspaces(c echo.Context, auth *apikit.AuthInfo, sqliteDB *sql.DB, requested string) (map[string]bool, bool) {
	owned, err := ownedWorkspaces(auth, sqliteDB)
	if err != nil {
		_ = apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		return nil, false
	}
	if owned != nil && requested != "" && !owned[requested] {
		_, _ = wsaccess.DeniesWorkspace(sqliteDB, auth, requested, wsaccess.SiteOf(c, http.StatusNotFound))
		_ = apikit.WriteAPIError(c, http.StatusNotFound, "workspace not found")
		return nil, false
	}
	return owned, true
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
// 2. A PAT bound to another workspace (spec 33) sees it as missing (404)
// 3. Workspace is not archived (409 workspace_archived)
// 4. Generic PAT/API key owner has write access to the workspace (403 workspace_access_denied)
//
// Admin tokens bypass all workspace restriction checks.
// If sqliteDB is nil, workspace existence and archive checks are skipped.
// After writing a refusal it returns a non-nil error, and the caller must
// stop.
func checkWorkspaceAccess(c echo.Context, auth *apikit.AuthInfo, slug string, sqliteDB *sql.DB) error {
	// Admin tokens bypass all workspace checks.
	if isAdmin(auth) {
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
		if isPAT(auth) {
			return refused(apikit.WriteAPIErrorWithType(c, http.StatusForbidden,
				"token owner does not have write access to this workspace",
				"workspace_access_denied"))
		}
	}

	return nil
}
