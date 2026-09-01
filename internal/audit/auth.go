package audit

import (
	"database/sql"
	"net/http"
	"slices"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
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

// checkWorkspaceAccess enforces workspace-level access control for audit
// ingestion endpoints. It checks:
// 1. Workspace-scoped PAT workspace matches the URL :slug (403 workspace_mismatch)
// 2. Generic PAT/API key owner has write access to the workspace (403 workspace_access_denied)
// 3. Workspace exists (404)
// 4. Workspace is not archived (409 workspace_archived)
//
// Admin tokens bypass all workspace restriction checks.
// If sqliteDB is nil, workspace existence and archive checks are skipped.
func checkWorkspaceAccess(c echo.Context, auth *apikit.AuthInfo, slug string, sqliteDB *sql.DB) error {
	// Admin tokens bypass all workspace checks.
	if isAdmin(auth) {
		return nil
	}

	// Check workspace-scoped PAT: KeyID stores the workspace scope.
	if isPAT(auth) && auth.KeyID != "" {
		if auth.KeyID != slug {
			return apikit.WriteAPIErrorWithType(c, http.StatusForbidden,
				"token workspace scope does not match target workspace",
				"workspace_mismatch")
		}
		// Workspace-scoped PAT matches — skip further ownership checks.
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
			return apikit.WriteAPIError(c, http.StatusNotFound,
				"workspace not found")
		}
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError,
				"failed to check workspace access")
		}

		// Check archived status.
		if status == "archived" {
			return apikit.WriteAPIErrorWithType(c, http.StatusConflict,
				"workspace is archived",
				"workspace_archived")
		}

		// Generic tokens: check owner has write access to the workspace.
		// For API keys and generic PATs, the UserID must match the workspace owner.
		if auth.UserID != ownerID {
			return apikit.WriteAPIErrorWithType(c, http.StatusForbidden,
				"token owner does not have write access to this workspace",
				"workspace_access_denied")
		}
	} else {
		// No SQLite DB available — deny access for generic PATs without
		// workspace scope since we cannot verify workspace membership.
		if isPAT(auth) {
			return apikit.WriteAPIErrorWithType(c, http.StatusForbidden,
				"token owner does not have write access to this workspace",
				"workspace_access_denied")
		}
	}

	return nil
}
