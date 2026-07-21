package workspace

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

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

// authInfoKey is the echo context key for storing AuthInfo in test environments.
const authInfoKey = "workspace.auth"

// AuthInfo holds the authenticated identity and permissions for a request.
type AuthInfo struct {
	CredType    CredentialType `json:"cred_type"`
	UserID      string         `json:"user_id"`
	Permissions []string       `json:"permissions"`
}

// getAuth retrieves the AuthInfo from the echo context.
// It checks two sources in order:
//  1. Echo context (c.Get) — used by test middleware that injects AuthInfo directly.
//  2. Apikit auth context (request context.Context) — used in production where
//     apikit's auth middleware injects AuthInfo via context.WithValue.
//
// Returns nil and an error if no auth information is present in either source.
func getAuth(c echo.Context) (*AuthInfo, error) {
	// Source 1: echo context (test environment).
	val := c.Get(authInfoKey)
	if val != nil {
		info, ok := val.(*AuthInfo)
		if ok {
			return info, nil
		}
	}

	// Source 2: apikit auth context (production environment).
	apikitInfo := apikit.GetAuthInfo(c)
	if apikitInfo != nil {
		return convertApikitAuth(apikitInfo), nil
	}

	return nil, echo.NewHTTPError(http.StatusUnauthorized, "authentication required")
}

// convertApikitAuth converts apikit's AuthInfo to the workspace package's
// AuthInfo type, mapping credential types to the workspace domain model.
func convertApikitAuth(info *apikit.AuthInfo) *AuthInfo {
	credType := CredentialAPIKey
	switch info.CredentialType {
	case "admin_token":
		credType = CredentialAdmin
	case "pat":
		credType = CredentialPAT
	case "api_key":
		credType = CredentialAPIKey
	}

	return &AuthInfo{
		CredType:    credType,
		UserID:      info.UserID,
		Permissions: info.Permissions,
	}
}

// hasPermission checks whether the AuthInfo contains a specific permission scope.
func (a *AuthInfo) hasPermission(perm string) bool {
	return slices.Contains(a.Permissions, perm)
}

// hasReadAccess reports whether the PAT has any scope that implies workspace
// read access. The read-implying scopes are: workspaces:read, workspaces:create,
// and workspaces:write. workspaces:delete does NOT grant read access.
func (a *AuthInfo) hasReadAccess() bool {
	return a.hasPermission("workspaces:read") ||
		a.hasPermission("workspaces:create") ||
		a.hasPermission("workspaces:write")
}

// hasWriteAccess reports whether the credential can perform mutation operations
// (update, archive, reactivate) on owned workspaces.
// Admin and API key credentials always have write access.
// PATs require the workspaces:write scope.
func (a *AuthInfo) hasWriteAccess() bool {
	if a.CredType == CredentialAdmin || a.CredType == CredentialAPIKey {
		return true
	}
	return a.hasPermission("workspaces:write")
}

// hasDeleteAccess reports whether the credential can delete workspaces.
// Admin and API key credentials always have delete access.
// PATs require the workspaces:delete scope.
func (a *AuthInfo) hasDeleteAccess() bool {
	if a.CredType == CredentialAdmin || a.CredType == CredentialAPIKey {
		return true
	}
	return a.hasPermission("workspaces:delete")
}

// String returns a human-readable representation of the AuthInfo (for debugging).
func (a *AuthInfo) String() string {
	return fmt.Sprintf("AuthInfo{cred_type: %s, user_id: %s, permissions: %v}",
		a.CredType, a.UserID, a.Permissions)
}
