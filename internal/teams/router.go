package teams

import (
	"database/sql"

	"github.com/labstack/echo/v4"
)

// RegisterTeamRoutes registers all team management routes under the given
// Echo group with admin middleware applied at the group level. The group
// should already have the auth middleware from server_foundation applied
// (so that AuthContext is available for AdminRequired to inspect).
//
// This is the production entry point for wiring team routes into the
// application's Echo router. It creates the Store and Handler internally,
// applies AdminRequired middleware to the group, and registers all routes.
//
// Routes registered:
//
//	POST   /                - Create team
//	GET    /                - List teams
//	GET    /:id             - Get team by ID
//	POST   /:id/archive     - Archive team
//	POST   /:id/reactivate  - Reactivate team
//	DELETE /:id             - Delete team
//	POST   /:id/members     - Add team member
//	GET    /:id/members     - List team members
//
// All routes require admin authentication (HTTP 403 for non-admin callers).
func RegisterTeamRoutes(g *echo.Group, db *sql.DB) {
	store := NewStore(db)
	handler := NewHandler(store)

	// Apply admin middleware at the group level so all handlers are protected.
	g.Use(AdminRequired())

	handler.RegisterRoutes(g)
}
