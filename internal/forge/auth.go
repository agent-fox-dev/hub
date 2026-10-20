package forge

import (
	"slices"

	"github.com/txsvc/apikit"
)

func isAdmin(auth *apikit.AuthInfo) bool {
	return auth.CredentialType == "admin_token"
}

func isAPIKey(auth *apikit.AuthInfo) bool {
	return auth.CredentialType == "api_key"
}

func isPAT(auth *apikit.AuthInfo) bool {
	return auth.CredentialType == "pat"
}

func hasScope(auth *apikit.AuthInfo, scopes ...string) bool {
	for _, s := range scopes {
		if slices.Contains(auth.Permissions, s) {
			return true
		}
	}
	return false
}

// canReadForge reports whether the credential may read a workspace's forge. API
// keys and admin tokens are not scope-checked. A PAT needs forge:read, which
// forge:write implies. A credential type hub does not know gets no access.
func canReadForge(auth *apikit.AuthInfo) bool {
	if isPAT(auth) {
		return hasScope(auth, "forge:read", "forge:write")
	}
	return isAPIKey(auth) || isAdmin(auth)
}

// canWriteForge reports whether the credential may change a workspace's forge. API
// keys and admin tokens are not scope-checked. A PAT needs forge:write, which
// forge:read does not imply. A credential type hub does not know gets no access.
func canWriteForge(auth *apikit.AuthInfo) bool {
	if isPAT(auth) {
		return hasScope(auth, "forge:write")
	}
	return isAPIKey(auth) || isAdmin(auth)
}
