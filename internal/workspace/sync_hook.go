package workspace

import "github.com/labstack/echo/v4"

// SyncExtension takes part in POST /workspaces/:slug/sync for a workspace in
// carry_patch mode. The handler has authenticated and authorized the caller
// and resolved the trunk directory when it calls the extension with the Echo
// context, the slug and that directory.
//
// The extension returns the fields to merge into the standard sync response
// (the hub re-reads the workspace row first, so that the fields the extension
// wrote are current), handled false to fall through to the standard sync, or
// an error when it has already written a response.
type SyncExtension func(c echo.Context, slug, repoPath string) (extras map[string]any, handled bool, err error)

// syncExtension is the registered extension; nil means every workspace syncs
// the standard way.
var syncExtension SyncExtension

// RegisterSyncExtension registers the extension that syncs carry_patch
// workspaces. The hub registers the carry-patch module's at start.
func RegisterSyncExtension(fn SyncExtension) {
	syncExtension = fn
}
