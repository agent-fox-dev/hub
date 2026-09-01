package audit

import (
	"database/sql"

	"github.com/labstack/echo/v4"
)

// RegisterSessionRoutes mounts session and token usage API handlers on the
// given echo group. The sqliteDB parameter is used for workspace access
// checks (resolving workspace ownership).
//
// Routes:
//
//	POST   /sessions              - Open a new agent session
//	POST   /sessions/:id/complete - Complete an active session
//	POST   /sessions/:id/usage    - Report incremental token usage
//	GET    /sessions              - List sessions (paginated)
//	GET    /sessions/:id          - Fetch a single session
//	GET    /sessions/:id/usage    - Query token usage records (paginated)
func RegisterSessionRoutes(api *echo.Group, store Store, sqliteDB *sql.DB) {
	api.POST("/sessions", handleCreateSession(store))
	api.POST("/sessions/:id/complete", handleCompleteSession(store))
	api.POST("/sessions/:id/usage", handleReportUsage(store))
	api.GET("/sessions", handleListSessions(store, sqliteDB))
	api.GET("/sessions/:id", handleGetSession(store, sqliteDB))
	api.GET("/sessions/:id/usage", handleListSessionUsage(store, sqliteDB))
}

// RegisterRoutes registers all audit ingestion and query HTTP routes
// on the provided Echo route group.
func RegisterRoutes(api *echo.Group, store Store, emitter Emitter) {
	runs := api.Group("/workspaces/:slug/runs/:run_id")

	// Agent ingestion endpoints.
	runs.POST("/events", handlePostEvent(store))
	runs.POST("/sessions/outcomes", handlePostSessionOutcome(store))
	runs.POST("/tools/calls", handlePostToolCall(store))
	runs.POST("/tools/errors", handlePostToolError(store))
	runs.POST("/traces", handlePostTrace(store))

	// Postmortem endpoints.
	runs.POST("/postmortem", handlePostPostmortem(store))
	runs.GET("/postmortem", handleGetPostmortem(store))
}
