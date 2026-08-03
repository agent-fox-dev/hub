package mergequeue

import (
	"database/sql"

	"github.com/labstack/echo/v4"
)

// RegisterMergeRoutes registers merge queue HTTP routes on the given API group.
// Routes:
//   - POST /workspaces/:slug/merges  (submit a merge job)
//   - GET  /workspaces/:slug/merges  (list merge jobs)
func RegisterMergeRoutes(api *echo.Group, db *sql.DB) error {
	ws := api.Group("/workspaces/:slug")
	ws.POST("/merges", handleSubmitMerge(db))
	ws.GET("/merges", handleListMerges(db))
	return nil
}

// handleSubmitMerge handles POST /api/v1/workspaces/:slug/merges.
// It validates auth (merges:write scope), checks for duplicate active jobs
// via a pre-insert SELECT, generates a server-side UUID nonce, inserts the
// job with status=prepared, transitions to queued, and returns HTTP 202
// Accepted with the merge job JSON (nonce excluded from response).
func handleSubmitMerge(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		// TODO: implement submit merge handler
		return nil
	}
}

// handleListMerges handles GET /api/v1/workspaces/:slug/merges.
// It validates auth (merges:read scope), applies optional status filter
// and cursor-based pagination via ?after=<uuid>&limit=<int>&status=<value>,
// and returns merge jobs ordered by (created_at ASC, id ASC) with a
// pagination envelope { items: [...], next_cursor: "uuid" | null }.
// The check_output field is omitted from each item in the response.
func handleListMerges(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		// TODO: implement list merges handler
		return nil
	}
}
