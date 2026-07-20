package workspace

import (
	"database/sql"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// WorkspacePermissions returns the Permission values that hub registers with
// apikit's MountHandlers for workspace operations.
func WorkspacePermissions() []apikit.Permission {
	panic("not implemented")
}

// RegisterRoutes mounts workspace API handlers on the given echo group
// and applies workspace auth middleware.
func RegisterRoutes(api *echo.Group, db *sql.DB) error {
	panic("not implemented")
}
