package audit

import (
	"database/sql"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// handleCreateSession handles POST /api/v1/sessions.
// Creates a new agent session or returns existing if id is duplicate.
func handleCreateSession(store Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return apikit.WriteAPIError(c, http.StatusNotImplemented, "not implemented")
	}
}

// handleCompleteSession handles POST /api/v1/sessions/:id/complete.
// Transitions an active session to a terminal state.
func handleCompleteSession(store Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return apikit.WriteAPIError(c, http.StatusNotImplemented, "not implemented")
	}
}

// handleReportUsage handles POST /api/v1/sessions/:id/usage.
// Records incremental token usage for an active session.
func handleReportUsage(store Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return apikit.WriteAPIError(c, http.StatusNotImplemented, "not implemented")
	}
}

// handleListSessions handles GET /api/v1/sessions.
// Returns a paginated list of sessions with token summaries.
func handleListSessions(store Store, sqliteDB *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return apikit.WriteAPIError(c, http.StatusNotImplemented, "not implemented")
	}
}

// handleGetSession handles GET /api/v1/sessions/:id.
// Returns a single session with its token summary.
func handleGetSession(store Store, sqliteDB *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return apikit.WriteAPIError(c, http.StatusNotImplemented, "not implemented")
	}
}

// handleListSessionUsage handles GET /api/v1/sessions/:id/usage.
// Returns paginated token usage records and unbounded totals.
func handleListSessionUsage(store Store, sqliteDB *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return apikit.WriteAPIError(c, http.StatusNotImplemented, "not implemented")
	}
}

// handlePostEvent handles POST /workspaces/:slug/runs/:run_id/events.
// Ingests a single audit event into agent_audit_events.
func handlePostEvent(_ Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "not implemented"})
	}
}

// handlePostSessionOutcome handles POST /workspaces/:slug/runs/:run_id/sessions/outcomes.
// Ingests a session outcome into session_outcomes.
func handlePostSessionOutcome(_ Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "not implemented"})
	}
}

// handlePostToolCall handles POST /workspaces/:slug/runs/:run_id/tools/calls.
// Ingests a tool call record into tool_calls.
func handlePostToolCall(_ Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "not implemented"})
	}
}

// handlePostToolError handles POST /workspaces/:slug/runs/:run_id/tools/errors.
// Ingests a tool error record into tool_errors.
func handlePostToolError(_ Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "not implemented"})
	}
}

// handlePostTrace handles POST /workspaces/:slug/runs/:run_id/traces.
// Ingests a single trace event into agent_traces.
func handlePostTrace(_ Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "not implemented"})
	}
}

// handlePostPostmortem handles POST /workspaces/:slug/runs/:run_id/postmortem.
// Ingests a postmortem report into postmortems.
func handlePostPostmortem(_ Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "not implemented"})
	}
}

// handleGetPostmortem handles GET /workspaces/:slug/runs/:run_id/postmortem.
// Retrieves a postmortem report by run_id.
func handleGetPostmortem(_ Store) echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "not implemented"})
	}
}
