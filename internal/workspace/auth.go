package workspace

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
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

// authInfoKey is the echo context key for storing AuthInfo.
const authInfoKey = "workspace.auth"

// AuthInfo holds the authenticated identity and permissions for a request.
type AuthInfo struct {
	CredType    CredentialType `json:"cred_type"`
	UserID      string         `json:"user_id"`
	Permissions []string       `json:"permissions"`
}

// getAuth retrieves the AuthInfo from the echo context.
// Returns nil and an error if no auth information is present.
func getAuth(c echo.Context) (*AuthInfo, error) {
	val := c.Get(authInfoKey)
	if val == nil {
		return nil, echo.NewHTTPError(http.StatusUnauthorized, "authentication required")
	}
	info, ok := val.(*AuthInfo)
	if !ok {
		return nil, fmt.Errorf("invalid auth info type in context")
	}
	return info, nil
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
