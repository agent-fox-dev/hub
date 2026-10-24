package workspace

import (
	"github.com/agent-fox-dev/hub/internal/wsaccess"
	"github.com/txsvc/apikit"
)

// hasReadAccess reports whether the PAT has any scope that implies workspace
// read access. The read-implying scopes are: workspaces:read, workspaces:create,
// and workspaces:write. workspaces:delete does NOT grant read access.
func hasReadAccess(auth *apikit.AuthInfo) bool {
	return wsaccess.HasScope(auth, "workspaces:read", "workspaces:create", "workspaces:write")
}

// hasWriteAccess reports whether the credential can perform mutation operations
// (update, archive, reactivate) on owned workspaces.
func hasWriteAccess(auth *apikit.AuthInfo) bool {
	return wsaccess.HasScope(auth, "workspaces:write")
}

// hasDeleteAccess reports whether the credential can delete workspaces.
func hasDeleteAccess(auth *apikit.AuthInfo) bool {
	return wsaccess.HasScope(auth, "workspaces:delete")
}
