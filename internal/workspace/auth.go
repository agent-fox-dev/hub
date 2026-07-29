package workspace

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

// hasReadAccess reports whether the PAT has any scope that implies workspace
// read access. The read-implying scopes are: workspaces:read, workspaces:create,
// and workspaces:write. workspaces:delete does NOT grant read access.
func hasReadAccess(auth *apikit.AuthInfo) bool {
	return hasScope(auth, "workspaces:read", "workspaces:create", "workspaces:write")
}

// hasWriteAccess reports whether the credential can perform mutation operations
// (update, archive, reactivate) on owned workspaces.
func hasWriteAccess(auth *apikit.AuthInfo) bool {
	return hasScope(auth, "workspaces:write")
}

// hasDeleteAccess reports whether the credential can delete workspaces.
func hasDeleteAccess(auth *apikit.AuthInfo) bool {
	return hasScope(auth, "workspaces:delete")
}
