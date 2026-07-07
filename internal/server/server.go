// Package server sets up the Echo HTTP server with health probe routes.
package server

import (
	"database/sql"

	"github.com/agent-fox/af-hub/internal/auth"
	"github.com/agent-fox/af-hub/internal/config"
	"github.com/agent-fox/af-hub/internal/handler"
	"github.com/agent-fox/af-hub/internal/store"
	"github.com/labstack/echo/v4"
)

// NewServer creates and configures an Echo server with health routes
// and middleware. Returns the configured Echo instance.
func NewServer(cfg *config.Config, db *sql.DB) *echo.Echo {
	// Stub — implementation in a later task group.
	e := echo.New()
	return e
}

// SetupServer creates and configures the Echo server with all routes, middleware,
// and handlers. Returns the configured Echo instance.
func SetupServer(cfg *config.Config, s store.Store, registry *auth.Registry) *echo.Echo {
	panic("not implemented")
}

// RegisterAuthRoutes registers the authentication routes on the given Echo group.
func RegisterAuthRoutes(g *echo.Group, h *handler.AuthHandler) {
	panic("not implemented")
}
