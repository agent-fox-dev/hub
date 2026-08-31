package workspace

import (
	"database/sql"
	"fmt"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// WorkspacePermissions returns the Permission values that hub registers with
// apikit's MountHandlers for workspace operations.
func WorkspacePermissions() []apikit.Permission {
	return []apikit.Permission{
		{Resource: "workspaces", Action: "read"},
		{Resource: "workspaces", Action: "create"},
		{Resource: "workspaces", Action: "write"},
		{Resource: "workspaces", Action: "delete"},
		{Resource: "workspaces", Action: "sync"},
		{Resource: "patches", Action: "read"},
		{Resource: "patches", Action: "write"},
	}
}

// MountWorkspaceHandlers initialises the workspace schema, registers workspace
// permission scopes with apikit (via MountHandlers), and mounts workspace API
// handlers on the server's API group.
//
// This is the single entry point for wiring up workspace support in af-hub.
// It calls s.MountHandlers to register both apikit's built-in handlers and
// workspace-specific permission scopes, then registers workspace routes.
//
// Must be called after NewServer and before Start.
func MountWorkspaceHandlers(s *apikit.Server, db *apikit.DB, extraPerms ...apikit.Permission) error {
	// Initialise the workspaces table schema.
	if err := initSchema(db.SqlDB); err != nil {
		return fmt.Errorf("workspace schema init: %w", err)
	}

	// Capture the external URL for hub_url construction in workspace responses.
	defaultExternalURL = s.ExternalURL()

	// Register workspace permissions (plus any extra module scopes) and
	// mount all built-in handlers.
	perms := WorkspacePermissions()
	perms = append(perms, extraPerms...)
	if err := s.MountHandlers(db, perms...); err != nil {
		return fmt.Errorf("mount handlers: %w", err)
	}

	// Register workspace API routes on the server's API group.
	api := s.APIGroup()
	return RegisterRoutes(api, db.SqlDB)
}

// RegisterRoutes mounts workspace API handlers on the given echo group.
// This is also used by tests which set up their own echo instance and
// auth middleware instead of using the full apikit server stack.
func RegisterRoutes(api *echo.Group, db *sql.DB) error {
	api.POST("/workspaces", handleCreateWorkspace(db))
	api.GET("/workspaces", handleListWorkspaces(db))
	api.GET("/workspaces/:slug", handleGetWorkspace(db))
	api.PATCH("/workspaces/:slug", handleUpdateWorkspace(db))
	api.POST("/workspaces/:slug/archive", handleArchiveWorkspace(db))
	api.POST("/workspaces/:slug/reactivate", handleReactivateWorkspace(db))
	api.POST("/workspaces/:slug/sync", handleSyncWorkspace(db))
	api.POST("/workspaces/:slug/reclone", handleRecloneWorkspace(db))
	api.DELETE("/workspaces/:slug", handleDeleteWorkspace(db))

	// Patch routes (15-REQ-8, 15-REQ-9, 15-REQ-10, 15-REQ-11, 15-REQ-12).
	api.POST("/workspaces/:slug/patches", handleAddPatch(db))
	api.GET("/workspaces/:slug/patches", handleListPatches(db))
	api.PATCH("/workspaces/:slug/patches/:id", handleUpdatePatch(db))
	api.DELETE("/workspaces/:slug/patches/:id", handleRemovePatch(db))
	api.POST("/workspaces/:slug/patches/reorder", handleReorderPatches(db))
	api.POST("/workspaces/:slug/patches/:id/restore", handleRestorePatch(db))

	return nil
}
