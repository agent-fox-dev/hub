package workspace

import (
	"github.com/labstack/echo/v4"
)

// CarryPatchSyncFunc is a callback that handles sync for carry_patch workspaces.
// It is called from handleSyncWorkspace when the workspace mode is carry_patch
// and the hook is registered.
//
// The function receives:
//   - c: the Echo context (for reading the request)
//   - slug: the workspace slug
//   - repoPath: the filesystem path to the repository trunk directory
//
// It returns:
//   - extras: the carry-patch-specific response fields (patches_merged,
//     rebuild_triggered, rebuild_job_id, force_push_detected) to merge into
//     the standard sync response. 16-REQ-5.1 requires these to be returned
//     "in addition to standard sync fields", so the hook deliberately does
//     not write a response body of its own -- handleSyncWorkspace owns
//     response construction and merges these on top of the workspace JSON.
//   - handled: true when the carry-patch path ran. False falls through to the
//     standard sync behaviour, which is what a workspace that is not in
//     carry_patch mode requires (16-REQ-5.E4).
//   - err: a response already written by the hook (an Echo error), if the
//     sync failed.
//
// This hook decouples the carry-patch package from the workspace package,
// avoiding an import cycle while allowing carry-patch sync extensions
// (16-REQ-5) to integrate into the existing sync endpoint.
type CarryPatchSyncFunc func(c echo.Context, slug, repoPath string) (extras map[string]any, handled bool, err error)

// carryPatchSyncHook is the registered carry-patch sync handler. When non-nil
// and the workspace is carry_patch mode, handleSyncWorkspace delegates to it.
var carryPatchSyncHook CarryPatchSyncFunc

// RegisterCarryPatchSyncHook registers a function to handle sync for
// carry_patch workspaces. Called from main.go during server initialization.
func RegisterCarryPatchSyncHook(fn CarryPatchSyncFunc) {
	carryPatchSyncHook = fn
}
