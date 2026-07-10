// Package server provides HTTP route registration for af-hub.
// It wires all endpoint handlers from the auth, users, keys, and workspace
// packages onto a shared Echo instance with appropriate middleware groups.
package server

import (
	"database/sql"

	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/auth"
	"github.com/agent-fox-dev/hub/internal/keys"
	"github.com/agent-fox-dev/hub/internal/users"
	"github.com/agent-fox-dev/hub/internal/workspace"
)

// RegisterRoutes registers all af-hub HTTP routes on the given Echo instance.
//
// Route groups:
//   - Public (no auth middleware): GET /api/v1/auth/providers
//   - Callback (no auth middleware, validates internally): POST /api/v1/auth/callback
//   - Protected (auth middleware required):
//   - Admin-only: POST /api/v1/users, GET /api/v1/users, GET /api/v1/users/:id
//   - Mixed-auth: PUT /api/v1/users/:id, GET /api/v1/keys,
//     POST /api/v1/keys/:key_id/refresh, DELETE /api/v1/keys/:key_id
//   - Workspace routes (auth middleware): POST/GET /workspaces, etc.
func RegisterRoutes(e *echo.Echo, db *sql.DB, registry *auth.Registry, allowlist *auth.Allowlist) {
	apiGroup := e.Group("/api/v1")

	// Public endpoints — no auth middleware.
	apiGroup.GET("/auth/providers", auth.GetProvidersHandler(registry))

	// Callback endpoint — no auth middleware (validates internally).
	apiGroup.POST("/auth/callback", auth.CallbackHandler(db, registry, allowlist))

	// Protected endpoints — require valid credentials via auth middleware.
	protectedGroup := apiGroup.Group("", auth.Middleware(db))

	// User management routes.
	protectedGroup.POST("/users", users.CreateUserHandler(db, registry))
	protectedGroup.GET("/users", users.ListUsersHandler(db))
	protectedGroup.GET("/users/:id", users.GetUserHandler(db))
	protectedGroup.PUT("/users/:id", users.UpdateUserHandler(db))

	// API key management routes.
	protectedGroup.GET("/keys", keys.ListKeysHandler(db))
	protectedGroup.POST("/keys/:key_id/refresh", keys.RefreshKeyHandler(db))
	protectedGroup.DELETE("/keys/:key_id", keys.RevokeKeyHandler(db))

	// Workspace routes.
	workspace.RegisterRoutes(protectedGroup, db)
}
