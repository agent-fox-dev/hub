package merge

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// BranchChecker checks whether a branch exists in a workspace repository.
// Returns true if the branch exists, false if it does not, or an error if
// the check itself fails (e.g. the repository is inaccessible).
type BranchChecker func(workspaceSlug, branch string) (bool, error)

// BatchRebaseFunc executes a batch rebase for the given workspace, rebasing
// each branch in the list onto the target ref. Returns per-branch results.
type BatchRebaseFunc func(ctx context.Context, workspaceSlug, targetRef string, branches []string) ([]RebaseResult, error)

// MergeAPIConfig holds the dependencies needed by merge REST API handlers.
type MergeAPIConfig struct {
	// DB is the workspace database used for workspace lookups.
	DB *sql.DB

	// Queue is the durable job queue for enqueuing merge jobs.
	Queue *jobqueue.Queue

	// BranchExists checks if a branch exists in a workspace repository.
	BranchExists BranchChecker

	// BatchRebase executes batch rebase operations for the POST /rebase
	// endpoint. If nil, the endpoint returns 500 "not configured".
	BatchRebase BatchRebaseFunc
}

// MergeJobResponse is the JSON response body for merge job records.
// It projects job queue fields into merge-specific vocabulary by extracting
// target_branch, source_ref, submitted_by from the job payload and
// base_sha, merged_sha, conflict_files, check_output from the job result.
type MergeJobResponse struct {
	ID            string   `json:"id"`
	WorkspaceSlug string   `json:"workspace_slug"`
	TargetBranch  string   `json:"target_branch"`
	SourceRef     string   `json:"source_ref"`
	Status        string   `json:"status"`
	BaseSHA       *string  `json:"base_sha"`
	MergedSHA     *string  `json:"merged_sha"`
	ConflictFiles []string `json:"conflict_files"`
	CheckOutput   *string  `json:"check_output"`
	Error         *string  `json:"error"`
	RetryCount    int      `json:"retry_count"`
	SubmittedBy   string   `json:"submitted_by"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
}

// mergeJobResult is the JSON structure stored in the job result field
// for completed merge jobs.
type mergeJobResult struct {
	BaseSHA     string `json:"base_sha"`
	MergedSHA   string `json:"merged_sha"`
	CheckOutput string `json:"check_output,omitempty"`
}

// mergeJobError is the JSON structure stored in the job error field
// for failed merge jobs.
type mergeJobError struct {
	Reason        string   `json:"reason"`
	ConflictFiles []string `json:"conflict_files,omitempty"`
	CheckOutput   string   `json:"check_output,omitempty"`
}

// ProjectMergeJobResponse converts a job queue record into a MergeJobResponse
// by extracting merge-specific fields from the job payload and result.
// Returns null for missing fields rather than returning an error, per
// 12-REQ-14.E1.
func ProjectMergeJobResponse(_ *jobqueue.Job) MergeJobResponse {
	// Stub: returns zero-value response.
	// Implementation will be added in task group 13.
	return MergeJobResponse{}
}

// batchRebaseRequest is the JSON request body for POST /rebase.
type batchRebaseRequest struct {
	TargetRef string   `json:"target_ref"`
	Branches  []string `json:"branches"`
}

// batchRebaseResponse is the JSON response body for POST /rebase.
type batchRebaseResponse struct {
	Results []RebaseResult `json:"results"`
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
	api.GET("/workspaces/:slug/merges", handleListMerges(cfg))
	api.GET("/workspaces/:slug/merges/:id", handleGetMerge(cfg))
	api.DELETE("/workspaces/:slug/merges/:id", handleCancelMerge(cfg))
	api.POST("/workspaces/:slug/rebase", handleBatchRebase(cfg))
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

// handleListMerges handles GET /api/v1/workspaces/:slug/merges.
// It queries jobs of type 'merge' scoped to the workspace and returns
// them as merge job response objects.
func handleListMerges(_ MergeAPIConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Stub: returns 500 "not implemented" for all requests.
		// Implementation will be added in task group 13.
		return apikit.WriteAPIError(c, http.StatusInternalServerError, "not implemented")
	}
}

// handleGetMerge handles GET /api/v1/workspaces/:slug/merges/:id.
// It retrieves a single merge job record by ID scoped to the workspace.
func handleGetMerge(_ MergeAPIConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Stub: returns 500 "not implemented" for all requests.
		// Implementation will be added in task group 13.
		return apikit.WriteAPIError(c, http.StatusInternalServerError, "not implemented")
	}
}

// handleCancelMerge handles DELETE /api/v1/workspaces/:slug/merges/:id.
// It cancels a queued merge job via the durable job queue's cancel operation.
func handleCancelMerge(_ MergeAPIConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Stub: returns 500 "not implemented" for all requests.
		// Implementation will be added in task group 13.
		return apikit.WriteAPIError(c, http.StatusInternalServerError, "not implemented")
	}
}

// handleBatchRebase handles POST /api/v1/workspaces/:slug/rebase.
// It validates the request, checks workspace state, and executes a batch
// rebase operation returning per-branch results synchronously.
func handleBatchRebase(_ MergeAPIConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Stub: returns 500 "not implemented" for all requests.
		// Implementation will be added in task group 14.
		return apikit.WriteAPIError(c, http.StatusInternalServerError, "not implemented")
	}
}

// Ensure types are used to prevent lint errors.
var (
	_ = json.Marshal
	_ = batchRebaseRequest{}
	_ = batchRebaseResponse{}
	_ = mergeJobResult{}
	_ = mergeJobError{}
)
