package sandboxes

import (
	"github.com/agent-fox-dev/hub/internal/wsaccess"
	"github.com/txsvc/apikit"
)

// canReadSandboxes reports whether the credential may read sandbox profiles.
// API keys and admin tokens are not scope-checked. A PAT needs sandboxes:read,
// which sandboxes:write implies. A credential type hub does not know gets no
// access.
func canReadSandboxes(auth *apikit.AuthInfo) bool {
	if wsaccess.IsPAT(auth) {
		return wsaccess.HasScope(auth, "sandboxes:read", "sandboxes:write")
	}
	return auth.CredentialType == "api_key" || auth.CredentialType == "admin_token"
}

// canWriteSandboxes reports whether the credential may issue and end leases.
// API keys and admin tokens are not scope-checked. A PAT needs
// sandboxes:write, which is not implied by sandboxes:read. A credential type
// hub does not know gets no access.
func canWriteSandboxes(auth *apikit.AuthInfo) bool {
	if wsaccess.IsPAT(auth) {
		return wsaccess.HasScope(auth, "sandboxes:write")
	}
	return auth.CredentialType == "api_key" || auth.CredentialType == "admin_token"
}
