package merge

import (
	"database/sql"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// BranchChecker checks whether a branch exists in a workspace repository.
// Returns true if the branch exists, false if it does not, or an error if
// the check itself fails (e.g. the repository is inaccessible).
type BranchChecker func(workspaceSlug, branch string) (bool, error)

// MergeAPIConfig holds the dependencies needed by merge REST API handlers.
type MergeAPIConfig struct {
	// DB is the workspace database used for workspace lookups.
	DB *sql.DB

	// Queue is the durable job queue for enqueuing merge jobs.
	Queue *jobqueue.Queue

	// BranchExists checks if a branch exists in a workspace repository.
	BranchExists BranchChecker
}

// MergePermissions returns the Permission values that hub registers with
// apikit's MountHandlers for merge operations.
func MergePermissions() []apikit.Permission {
	return []apikit.Permission{
		{Resource: "merges", Action: "read"},
		{Resource: "merges", Action: "write"},
	}
}

// RegisterMergeRoutes mounts merge API handlers on the given echo group.
// This is also used by tests which set up their own echo instance and
// auth middleware instead of using the full apikit server stack.
func RegisterMergeRoutes(api *echo.Group, cfg MergeAPIConfig) {
	api.POST("/workspaces/:slug/merges", handleSubmitMerge(cfg))
}

// handleSubmitMerge handles POST /api/v1/workspaces/:slug/merges.
// It validates the request, checks workspace state, verifies branch existence,
// and enqueues a merge job.
func handleSubmitMerge(_ MergeAPIConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Stub: returns 500 "not implemented" for all requests.
		// Implementation will be added in task group 13.
		return apikit.WriteAPIError(c, http.StatusInternalServerError, "not implemented")
	}
}
