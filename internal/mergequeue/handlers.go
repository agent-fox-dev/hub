package mergequeue

import (
	"database/sql"

	"github.com/labstack/echo/v4"
)

// RegisterMergeRoutes registers merge queue HTTP routes on the given API group.
// Routes:
//   - POST   /workspaces/:slug/merges              (submit a merge job)
//   - GET    /workspaces/:slug/merges              (list merge jobs)
//   - GET    /workspaces/:slug/merges/:id          (get single merge job)
//   - DELETE /workspaces/:slug/merges/:id          (cancel queued merge job)
//   - POST   /workspaces/:slug/merges/:id/requeue  (requeue dead-lettered job)
func RegisterMergeRoutes(api *echo.Group, db *sql.DB) error {
	ws := api.Group("/workspaces/:slug")
	ws.POST("/merges", handleSubmitMerge(db))
	ws.GET("/merges", handleListMerges(db))
	ws.GET("/merges/:id", handleGetMerge(db))
	ws.DELETE("/merges/:id", handleCancelMerge(db))
	ws.POST("/merges/:id/requeue", handleRequeueMerge(db))
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

// handleGetMerge handles GET /api/v1/workspaces/:slug/merges/:id.
// It validates auth (merges:read scope), retrieves the merge job by ID,
// verifies the job belongs to the workspace in the URL path (anti-enumeration),
// and returns the full merge job JSON including check_output and conflict_details
// deserialized to a native JSON array. The nonce field is excluded from the response.
func handleGetMerge(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		// TODO: implement get single merge job handler
		return nil
	}
}

// handleCancelMerge handles DELETE /api/v1/workspaces/:slug/merges/:id.
// It validates auth (merges:write scope), verifies the job is in queued status,
// transitions it to cancelled status using a conditional UPDATE, and returns
// HTTP 204 No Content. If the job is not in queued status (or transitions
// between check and update), returns HTTP 409 with the current status.
func handleCancelMerge(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		// TODO: implement cancel merge job handler
		return nil
	}
}

// handleRequeueMerge handles POST /api/v1/workspaces/:slug/merges/:id/requeue.
// It validates auth (merges:write scope), verifies the job is in dead_letter
// status, checks the duplicate submission guard for the same (workspace_slug,
// source_ref), creates a new merge job with a fresh nonce, status=queued,
// retry_count=0, available_at=now(), and submitted_by from GetAuthInfo.
// The original dead-lettered job is left unchanged.
func handleRequeueMerge(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		// TODO: implement requeue merge job handler
		return nil
	}
}
