package workspace

import (
	"database/sql"

	"github.com/labstack/echo/v4"
)

// RegisterRoutes registers all workspace and workspace token HTTP handlers
// on the given Echo route group. The group should already have the auth
// middleware applied.
//
// Routes registered:
//
//	POST   /workspaces              - Create workspace (user API key only)
//	GET    /workspaces              - List workspaces (user: own; admin: all)
//	GET    /workspaces/:slug        - Get workspace by slug
//	POST   /workspaces/:slug/tokens - Create workspace token
//	GET    /workspaces/:slug/tokens - List workspace tokens
//	DELETE /workspaces/:slug/tokens/:token_id - Revoke workspace token
//
// Stub: routes not yet implemented. Future implementation groups will
// fill in the handler logic.
func RegisterRoutes(g *echo.Group, db *sql.DB) {
	// Stub: no routes registered yet.
}
