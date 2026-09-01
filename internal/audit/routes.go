package audit

import (
	"database/sql"

	"github.com/labstack/echo/v4"
)

// RegisterSessionRoutes mounts session, token usage, and cost API handlers
// on the given echo group. The sqliteDB parameter is used for workspace
// access checks (resolving workspace ownership).
//
// Routes:
//
//	POST   /sessions              - Open a new agent session
//	POST   /sessions/:id/complete - Complete an active session
//	POST   /sessions/:id/usage    - Report incremental token usage
//	GET    /sessions              - List sessions (paginated)
//	GET    /sessions/:id          - Fetch a single session
//	GET    /sessions/:id/usage    - Query token usage records (paginated)
//	GET    /workspaces/:slug/cost - Workspace cost summary
func RegisterSessionRoutes(api *echo.Group, store Store, sqliteDB *sql.DB) {
	api.POST("/sessions", handleCreateSession(store))
	api.POST("/sessions/:id/complete", handleCompleteSession(store))
	api.POST("/sessions/:id/usage", handleReportUsage(store))
	api.GET("/sessions", handleListSessions(store, sqliteDB))
	api.GET("/sessions/:id", handleGetSession(store, sqliteDB))
	api.GET("/sessions/:id/usage", handleListSessionUsage(store, sqliteDB))
	api.GET("/workspaces/:slug/cost", handleWorkspaceCost(store, sqliteDB))
}

// RegisterRoutes registers all audit ingestion and query HTTP routes
// on the provided Echo route group.
func RegisterRoutes(api *echo.Group, store Store, emitter Emitter) {
	runs := api.Group("/workspaces/:slug/runs/:run_id")

	// Agent ingestion endpoints.
	runs.POST("/events", handlePostEvent(store))
	runs.GET("/events", handleGetEvents(store))
	runs.POST("/events/batch", handlePostEventsBatch(store))
	runs.POST("/sessions/outcomes", handlePostSessionOutcome(store))
	runs.GET("/sessions/outcomes", handleGetSessionOutcomes(store))
	runs.POST("/tools/calls", handlePostToolCall(store))
	runs.GET("/tools/calls", handleGetToolCalls(store))
	runs.POST("/tools/errors", handlePostToolError(store))
	runs.GET("/tools/errors", handleGetToolErrors(store))
	runs.POST("/traces", handlePostTrace(store))
	runs.GET("/traces", handleGetTraces(store))
	runs.POST("/traces/batch", handlePostTracesBatch(store))

	// Postmortem endpoints.
	runs.POST("/postmortem", handlePostPostmortem(store))
	runs.GET("/postmortem", handleGetPostmortem(store))
}
