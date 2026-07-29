package secrets

import (
	"database/sql"
	"net/http"

	"github.com/labstack/echo/v4"
)

// authInfoKey is the echo context key for storing AuthInfo in test environments.
const authInfoKey = "secrets.auth"

// CredentialType identifies the type of credential used to authenticate a request.
type CredentialType string

const (
	// CredentialAdmin represents an admin token.
	CredentialAdmin CredentialType = "admin"
	// CredentialAPIKey represents a user API key.
	CredentialAPIKey CredentialType = "apikey"
	// CredentialPAT represents a personal access token.
	CredentialPAT CredentialType = "pat"
)

// AuthInfo holds the authenticated identity and permissions for a request.
type AuthInfo struct {
	CredType    CredentialType `json:"cred_type"`
	UserID      string         `json:"user_id"`
	Permissions []string       `json:"permissions"`
}

// getAuth retrieves the AuthInfo from the echo context.
func getAuth(c echo.Context) (*AuthInfo, error) {
	val := c.Get(authInfoKey)
	if val != nil {
		info, ok := val.(*AuthInfo)
		if ok {
			return info, nil
		}
	}
	return nil, echo.NewHTTPError(http.StatusUnauthorized, "authentication required")
}

// hasPermission checks whether the AuthInfo contains a specific permission scope.
func (a *AuthInfo) hasPermission(perm string) bool {
	for _, p := range a.Permissions {
		if p == perm {
			return true
		}
	}
	return false
}

// hasSecretsManage reports whether the credential can perform all secrets operations.
func (a *AuthInfo) hasSecretsManage() bool {
	return a.hasPermission("secrets:manage")
}

// hasSecretsList reports whether the credential can list secrets.
// secrets:manage implies secrets:list.
func (a *AuthInfo) hasSecretsList() bool {
	return a.hasPermission("secrets:list") || a.hasPermission("secrets:manage")
}

// hasSecretsWrite reports whether the credential can update secrets.
// secrets:manage implies secrets:write.
func (a *AuthInfo) hasSecretsWrite() bool {
	return a.hasPermission("secrets:write") || a.hasPermission("secrets:manage")
}

// hasSecretsDelete reports whether the credential can delete secrets.
// secrets:manage implies secrets:delete.
func (a *AuthInfo) hasSecretsDelete() bool {
	return a.hasPermission("secrets:delete") || a.hasPermission("secrets:manage")
}

// hasVarsManage reports whether the credential can perform all variables operations.
func (a *AuthInfo) hasVarsManage() bool {
	return a.hasPermission("vars:manage")
}

// hasVarsRead reports whether the credential can list and read variables.
// vars:manage and vars:write both imply vars:read.
func (a *AuthInfo) hasVarsRead() bool {
	return a.hasPermission("vars:read") ||
		a.hasPermission("vars:write") ||
		a.hasPermission("vars:manage")
}

// hasVarsWrite reports whether the credential can update variables.
// vars:manage implies vars:write.
func (a *AuthInfo) hasVarsWrite() bool {
	return a.hasPermission("vars:write") || a.hasPermission("vars:manage")
}

// hasVarsDelete reports whether the credential can delete variables.
// vars:manage implies vars:delete.
func (a *AuthInfo) hasVarsDelete() bool {
	return a.hasPermission("vars:delete") || a.hasPermission("vars:manage")
}

// respondError writes a JSON error envelope and sets the HTTP status code.
func respondError(c echo.Context, code int, message string) error {
	return c.JSON(code, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

// checkOrgMembership verifies that the org exists and the user is a member.
// Returns the orgID and (0, "") if the check passes, or ("", httpCode, message) on failure.
func checkOrgMembership(db *sql.DB, userID, orgSlug string) (string, int, string) {
	var orgID string
	err := db.QueryRow("SELECT id FROM orgs WHERE slug = ?", orgSlug).Scan(&orgID)
	if err != nil {
		return "", http.StatusNotFound, "not found"
	}

	var isMember int
	err = db.QueryRow("SELECT COUNT(*) FROM org_members WHERE org_id = ? AND user_id = ?", orgID, userID).Scan(&isMember)
	if err != nil || isMember == 0 {
		return "", http.StatusNotFound, "not found"
	}

	return orgID, 0, ""
}

// lookupWorkspaceOwner retrieves a workspace and checks ownership.
// Returns the workspace owner_id or an error code for anti-enumeration.
func lookupWorkspaceOwner(db *sql.DB, slug, userID string, isAdmin bool) (string, int, string) {
	var ownerID string
	err := db.QueryRow("SELECT owner_id FROM workspaces WHERE slug = ?", slug).Scan(&ownerID)
	if err != nil {
		return "", http.StatusNotFound, "not found"
	}
	if !isAdmin && ownerID != userID {
		return "", http.StatusNotFound, "not found"
	}
	return ownerID, 0, ""
}

// --- User-scoped secret handlers ---

func handleCreateUserSecrets(store *Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondError(c, http.StatusNotImplemented, "not implemented")
	}
}

func handleListUserSecrets(store *Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondError(c, http.StatusNotImplemented, "not implemented")
	}
}

func handleUpdateUserSecret(store *Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondError(c, http.StatusNotImplemented, "not implemented")
	}
}

func handleDeleteUserSecret(store *Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondError(c, http.StatusNotImplemented, "not implemented")
	}
}

// --- Org-scoped secret handlers ---

func handleCreateOrgSecrets(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondError(c, http.StatusNotImplemented, "not implemented")
	}
}

func handleListOrgSecrets(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondError(c, http.StatusNotImplemented, "not implemented")
	}
}

func handleUpdateOrgSecret(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondError(c, http.StatusNotImplemented, "not implemented")
	}
}

func handleDeleteOrgSecret(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondError(c, http.StatusNotImplemented, "not implemented")
	}
}

// --- Workspace-scoped secret handlers ---

func handleCreateWorkspaceSecrets(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondError(c, http.StatusNotImplemented, "not implemented")
	}
}

func handleListWorkspaceSecrets(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondError(c, http.StatusNotImplemented, "not implemented")
	}
}

func handleUpdateWorkspaceSecret(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondError(c, http.StatusNotImplemented, "not implemented")
	}
}

func handleDeleteWorkspaceSecret(store *Store, db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return respondError(c, http.StatusNotImplemented, "not implemented")
	}
}
