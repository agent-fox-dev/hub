package workspace

import (
	"github.com/labstack/echo/v4"
)

// CarryPatchSyncFunc is a callback that handles sync for carry_patch workspaces.
// It is called from handleSyncWorkspace when the workspace mode is carry_patch
// and the hook is registered. Returns true if the sync was handled (the caller
// should return the response); false to fall through to standard sync behavior.
//
// The function receives:
//   - c: the Echo context (for reading request, writing response)
//   - slug: the workspace slug
//   - repoPath: the filesystem path to the repository trunk directory
//
// This hook decouples the carry-patch package from the workspace package,
// avoiding an import cycle while allowing carry-patch sync extensions
// (16-REQ-5) to integrate into the existing sync endpoint.
type CarryPatchSyncFunc func(c echo.Context, slug, repoPath string) (handled bool, err error)

// carryPatchSyncHook is the registered carry-patch sync handler. When non-nil
// and the workspace is carry_patch mode, handleSyncWorkspace delegates to it.
var carryPatchSyncHook CarryPatchSyncFunc

// RegisterCarryPatchSyncHook registers a function to handle sync for
// carry_patch workspaces. Called from main.go during server initialization.
func RegisterCarryPatchSyncHook(fn CarryPatchSyncFunc) {
	carryPatchSyncHook = fn
}
