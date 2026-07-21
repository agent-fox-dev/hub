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
func MountWorkspaceHandlers(s *apikit.Server, db *apikit.DB) error {
	// Initialise the workspaces table schema.
	if err := initSchema(db.SqlDB); err != nil {
		return fmt.Errorf("workspace schema init: %w", err)
	}

	// Register workspace permissions and mount all built-in handlers.
	perms := WorkspacePermissions()
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
	api.POST("/workspaces/:slug/archive", handleArchiveWorkspace(db))
	api.POST("/workspaces/:slug/reactivate", handleReactivateWorkspace(db))
	api.DELETE("/workspaces/:slug", handleDeleteWorkspace(db))
	return nil
}
