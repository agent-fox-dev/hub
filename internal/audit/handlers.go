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
