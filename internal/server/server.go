// Package server sets up the Echo HTTP server with health probe routes.
package server

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/agent-fox/af-hub/internal/auth"
	"github.com/agent-fox/af-hub/internal/config"
	"github.com/agent-fox/af-hub/internal/handler"
	afmiddleware "github.com/agent-fox/af-hub/internal/middleware"
	"github.com/agent-fox/af-hub/internal/store"
	"github.com/labstack/echo/v4"
	"github.com/sirupsen/logrus"
)

// NewServer creates and configures an Echo server with health routes
// and middleware. Returns the configured Echo instance.
func NewServer(cfg *config.Config, db *sql.DB) *echo.Echo {
	e := echo.New()

	// Register request logging middleware before route registration.
	e.Use(afmiddleware.RequestLoggerMiddleware())

	// Register health probe endpoints.
	e.GET("/healthz", healthzHandler)
	e.GET("/readyz", readyzHandler(db))

	return e
}

// healthzHandler responds with HTTP 200 and {"status":"ok"} unconditionally.
// It does not touch the database.
func healthzHandler(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// readyzHandler returns a handler that checks database connectivity.
// It pings the database with a 2-second timeout. On success it returns
// HTTP 200 with {"status":"ready"}; on failure it returns HTTP 503 with
// {"status":"not ready"} and logs the error at warn level.
func readyzHandler(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		ctx, cancel := context.WithTimeout(c.Request().Context(), 2*time.Second)
		defer cancel()

		if err := db.PingContext(ctx); err != nil {
			logrus.WithError(err).Warn("readyz: database ping failed")
			return c.JSON(http.StatusServiceUnavailable, map[string]string{"status": "not ready"})
		}

		return c.JSON(http.StatusOK, map[string]string{"status": "ready"})
	}
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
