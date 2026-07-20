package workspace

import (
	"database/sql"

	"github.com/labstack/echo/v4"
)

// handleCreateWorkspace handles POST /api/v1/workspaces.
func handleCreateWorkspace(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		panic("not implemented")
	}
}

// handleListWorkspaces handles GET /api/v1/workspaces.
func handleListWorkspaces(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		panic("not implemented")
	}
}

// handleGetWorkspace handles GET /api/v1/workspaces/:slug.
func handleGetWorkspace(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		panic("not implemented")
	}
}

// handleArchiveWorkspace handles POST /api/v1/workspaces/:slug/archive.
func handleArchiveWorkspace(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		panic("not implemented")
	}
}

// handleReactivateWorkspace handles POST /api/v1/workspaces/:slug/reactivate.
func handleReactivateWorkspace(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		panic("not implemented")
	}
}

// handleDeleteWorkspace handles DELETE /api/v1/workspaces/:slug.
func handleDeleteWorkspace(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		panic("not implemented")
	}
}
