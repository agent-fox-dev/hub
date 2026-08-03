package campaign

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

// hasCampaignWriteAccess reports whether the credential can create or cancel campaigns.
func hasCampaignWriteAccess(auth *apikit.AuthInfo) bool {
	return hasScope(auth, "campaigns:write")
}
