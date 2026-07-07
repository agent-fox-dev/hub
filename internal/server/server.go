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

// NewServer is a placeholder stub. It will create and configure the Echo
// server instance with health routes and middleware.
func NewServer(_ *config.Config, _ *sql.DB) error {
	// Stub — implementation in a later task group.
	return nil
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
