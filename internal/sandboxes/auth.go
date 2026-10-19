package sandboxes

import (
	"slices"

	"github.com/txsvc/apikit"
)

func isAdmin(auth *apikit.AuthInfo) bool {
	return auth.CredentialType == "admin_token"
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

// canReadSandboxes reports whether the credential may read sandbox profiles.
// API keys and admin tokens are not scope-checked. A PAT needs sandboxes:read,
// which sandboxes:write implies. A credential type hub does not know gets no
// access.
func canReadSandboxes(auth *apikit.AuthInfo) bool {
	if isPAT(auth) {
		return hasScope(auth, "sandboxes:read", "sandboxes:write")
	}
	return auth.CredentialType == "api_key" || auth.CredentialType == "admin_token"
}

// canWriteSandboxes reports whether the credential may issue and end leases.
// API keys and admin tokens are not scope-checked. A PAT needs
// sandboxes:write, which is not implied by sandboxes:read. A credential type
// hub does not know gets no access.
func canWriteSandboxes(auth *apikit.AuthInfo) bool {
	if isPAT(auth) {
		return hasScope(auth, "sandboxes:write")
	}
	return auth.CredentialType == "api_key" || auth.CredentialType == "admin_token"
}
