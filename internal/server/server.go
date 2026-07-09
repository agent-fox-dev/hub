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
	"github.com/labstack/echo/v4/middleware"
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
func SetupServer(cfg *config.Config, s store.Store, registry *auth.Registry, db *sql.DB) *echo.Echo {
	e := echo.New()

	// Set the custom error handler for standardized error envelopes.
	e.HTTPErrorHandler = handler.CustomHTTPErrorHandler

	// Register request logging middleware.
	e.Use(afmiddleware.RequestLoggerMiddleware())

	// Apply body size limit.
	e.Use(middleware.BodyLimit("1M"))

	// Register health probe endpoints.
	e.GET("/healthz", healthzHandler)
	e.GET("/readyz", readyzHandler(db))

	// Create handlers.
	authHandler := handler.NewAuthHandler(registry, s)
	userHandler := handler.NewUserHandler(s)
	workspaceHandler := handler.NewWorkspaceHandler(s)
	workspaceV2Handler := handler.NewWorkspaceV2Handler(s)
	apiKeyHandler := handler.NewAPIKeyHandler(s)

	// Public auth routes (no auth middleware).
	RegisterAuthRoutes(e.Group("/api/v1/auth"), authHandler)

	// Protected routes with auth middleware.
	apiGroup := e.Group("/api/v1", auth.AuthMiddleware(s))

	// Register user management routes.
	RegisterUserRoutes(apiGroup, userHandler)

	// Register legacy workspace management routes (admin only).
	// Note: POST /api/v1/workspaces (create) is handled by the V2 handler
	// below — it is NOT admin-only; the handler checks auth internally.
	RegisterWorkspaceRoutes(apiGroup, workspaceHandler)

	// Register spec 07 workspace V2 create route. This route is behind
	// AuthMiddleware but NOT RequireRole(admin) — the handler itself
	// rejects admin tokens and accepts any authenticated user.
	RegisterWorkspaceV2Routes(apiGroup, workspaceV2Handler)

	// Register API key management routes.
	RegisterAPIKeyRoutes(apiGroup, apiKeyHandler)

	return e
}

// RegisterAuthRoutes registers the authentication routes on the given Echo group.
func RegisterAuthRoutes(g *echo.Group, h *handler.AuthHandler) {
	g.GET("/providers", h.ListProviders)
	g.POST("/callback", h.OAuthCallback)
}

// RegisterUserRoutes registers user management routes on the given Echo group.
// POST, GET /users and GET /users/:id are admin-only.
// PUT /users/:id allows admin or self-update (full_name only for non-admins).
func RegisterUserRoutes(g *echo.Group, h *handler.UserHandler) {
	adminGroup := g.Group("", auth.RequireRole(auth.RoleAdmin))
	adminGroup.POST("/users", h.CreateUser)
	adminGroup.GET("/users", h.ListUsers)
	adminGroup.GET("/users/:id", h.GetUser)

	// PUT /users/:id — admin or self for full_name only.
	g.PUT("/users/:id", h.UpdateUser, auth.RequireAdminOrSelf())
}

// RegisterWorkspaceRoutes registers legacy workspace management routes on
// the given Echo group. These routes are admin-only and operate on the
// legacy workspace/team schema (spec 01). POST /workspaces (create) is
// intentionally NOT registered here — it is handled by the spec 07 V2
// handler via RegisterWorkspaceV2Routes.
func RegisterWorkspaceRoutes(g *echo.Group, h *handler.WorkspaceHandler) {
	adminGroup := g.Group("", auth.RequireRole(auth.RoleAdmin))
	adminGroup.GET("/workspaces", h.ListWorkspaces)
	adminGroup.POST("/workspaces/:id/archive", h.ArchiveWorkspace)
	adminGroup.POST("/workspaces/:id/reactivate", h.ReactivateWorkspace)
	adminGroup.DELETE("/workspaces/:id", h.DeleteWorkspace)
	adminGroup.POST("/workspaces/:id/members", h.AddOrUpdateMember)
	adminGroup.GET("/workspaces/:id/members", h.ListMembers)
}

// RegisterWorkspaceV2Routes registers the spec 07 workspace entity routes
// on the given Echo group. The POST /workspaces route is behind
// AuthMiddleware but NOT RequireRole(admin) — the handler itself checks
// whether the caller is an admin token (rejected) or a regular user
// (accepted). This implements 07-REQ-3.8 and 07-REQ-3.9.
func RegisterWorkspaceV2Routes(g *echo.Group, h *handler.WorkspaceV2Handler) {
	g.POST("/workspaces", h.CreateWorkspaceV2)
}

// RegisterAPIKeyRoutes registers API key management routes on the given
// Echo group. Create, refresh, and revoke require editor or admin role.
// List is available to any authenticated user.
func RegisterAPIKeyRoutes(g *echo.Group, h *handler.APIKeyHandler) {
	editorGroup := g.Group("", auth.RequireRole(auth.RoleAdmin, auth.RoleEditor))
	editorGroup.POST("/keys", h.CreateAPIKey)
	editorGroup.POST("/keys/:key_id/refresh", h.RefreshAPIKey)
	editorGroup.DELETE("/keys/:key_id", h.RevokeAPIKey)

	// List keys — any authenticated user.
	g.GET("/keys", h.ListAPIKeys)
}
