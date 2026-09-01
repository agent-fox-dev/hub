package audit

import (
	"slices"

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

// hasSessionsWrite reports whether the credential has sessions:write scope.
// Admin and api_key credentials are implicitly granted all scopes.
// PAT credentials require explicit scope grants.
func hasSessionsWrite(auth *apikit.AuthInfo) bool {
	if isAdmin(auth) || auth.CredentialType == "api_key" {
		return true
	}
	return slices.Contains(auth.Permissions, "sessions:write")
}

// hasSessionsRead reports whether the credential has sessions:read scope.
func hasSessionsRead(auth *apikit.AuthInfo) bool {
	if isAdmin(auth) || auth.CredentialType == "api_key" {
		return true
	}
	return slices.Contains(auth.Permissions, "sessions:read")
}
