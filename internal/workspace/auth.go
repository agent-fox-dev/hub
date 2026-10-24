package workspace

import (
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/wsaccess"
)

// hasReadAccess reports whether a PAT may read workspaces: workspaces:read,
// or workspaces:write, which implies it.
func hasReadAccess(auth *apikit.AuthInfo) bool {
	return wsaccess.CanRead(auth, "workspaces")
}

// hasWriteAccess reports whether the credential may change workspaces:
// create, update, archive, reactivate, sync, reclone, delete and the branch
// push.
func hasWriteAccess(auth *apikit.AuthInfo) bool {
	return wsaccess.CanWrite(auth, "workspaces")
}
