package forge

import (
	"github.com/agent-fox-dev/hub/internal/wsaccess"
	"github.com/txsvc/apikit"
)

func isAPIKey(auth *apikit.AuthInfo) bool {
	return auth.CredentialType == "api_key"
}

// canReadForge reports whether the credential may read a workspace's forge. API
// keys and admin tokens are not scope-checked. A PAT needs forge:read, which
// forge:write implies. A credential type hub does not know gets no access.
func canReadForge(auth *apikit.AuthInfo) bool {
	if wsaccess.IsPAT(auth) {
		return wsaccess.HasScope(auth, "forge:read", "forge:write")
	}
	return isAPIKey(auth) || wsaccess.IsAdmin(auth)
}

// canWriteForge reports whether the credential may change a workspace's forge. API
// keys and admin tokens are not scope-checked. A PAT needs forge:write itself:
// forge:read does not grant it. A credential type hub does not know gets no access.
func canWriteForge(auth *apikit.AuthInfo) bool {
	if wsaccess.IsPAT(auth) {
		return wsaccess.HasScope(auth, "forge:write")
	}
	return isAPIKey(auth) || wsaccess.IsAdmin(auth)
}
