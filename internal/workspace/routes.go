package workspace

import (
	"database/sql"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// WorkspacePermissions returns the Permission values that hub registers with
// apikit's MountHandlers for workspace operations.
func WorkspacePermissions() []apikit.Permission {
	return []apikit.Permission{
		{Resource: "workspaces", Action: "read"},
		{Resource: "workspaces", Action: "create"},
	}
}

// RegisterRoutes mounts workspace API handlers on the given echo group
// and applies workspace auth middleware.
func RegisterRoutes(api *echo.Group, db *sql.DB) error {
	api.POST("/workspaces", handleCreateWorkspace(db))
	api.GET("/workspaces", handleListWorkspaces(db))
	api.GET("/workspaces/:slug", handleGetWorkspace(db))
	api.POST("/workspaces/:slug/archive", handleArchiveWorkspace(db))
	api.POST("/workspaces/:slug/reactivate", handleReactivateWorkspace(db))
	api.DELETE("/workspaces/:slug", handleDeleteWorkspace(db))
	return nil
}
