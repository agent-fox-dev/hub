package merge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/gitcmd"
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
func ProjectMergeJobResponse(j *jobqueue.Job) MergeJobResponse {
	resp := MergeJobResponse{
		ID:            j.ID,
		Status:        j.Status,
		RetryCount:    j.RetryCount,
		ConflictFiles: []string{}, // Always non-nil per spec.
		CreatedAt:     apikit.FormatUTC(j.CreatedAt),
		UpdatedAt:     apikit.FormatUTC(j.UpdatedAt),
	}

	// Extract payload fields (workspace_slug, target_branch, source_ref, submitted_by).
	if len(j.Payload) > 0 {
		var payload MergePayload
		if err := json.Unmarshal(j.Payload, &payload); err == nil {
			resp.WorkspaceSlug = payload.WorkspaceSlug
			resp.TargetBranch = payload.TargetBranch
			resp.SourceRef = payload.SourceRef
			resp.SubmittedBy = payload.SubmittedBy
		}
	}

	// Extract result fields (base_sha, merged_sha, check_output).
	if len(j.Result) > 0 {
		var result mergeJobResult
		if err := json.Unmarshal(j.Result, &result); err == nil {
			if result.BaseSHA != "" {
				resp.BaseSHA = &result.BaseSHA
			}
			if result.MergedSHA != "" {
				resp.MergedSHA = &result.MergedSHA
			}
			if result.CheckOutput != "" {
				resp.CheckOutput = &result.CheckOutput
			}
		}
	}

	// Extract error fields (conflict_files, check_output, error message).
	if j.Error != "" {
		var jobErr mergeJobError
		if err := json.Unmarshal([]byte(j.Error), &jobErr); err == nil {
			if len(jobErr.ConflictFiles) > 0 {
				resp.ConflictFiles = jobErr.ConflictFiles
			}
			if jobErr.CheckOutput != "" {
				resp.CheckOutput = &jobErr.CheckOutput
			}
			errMsg := j.Error
			resp.Error = &errMsg
		} else {
			// Error is not structured JSON — include raw string.
			errMsg := j.Error
			resp.Error = &errMsg
		}
	}

	return resp
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

// NewMergeAPIConfig creates a MergeAPIConfig with the batch rebase operation
// wired to the real BatchRebase function using a GitRunner for the workspace.
// This factory ensures that RebaseBranch and BatchRebase are called from
// production code paths (not only from tests).
func NewMergeAPIConfig(db *sql.DB, q *jobqueue.Queue, branchChecker BranchChecker, workspaceRoot string) MergeAPIConfig {
	return MergeAPIConfig{
		DB:           db,
		Queue:        q,
		BranchExists: branchChecker,
		BatchRebase: func(ctx context.Context, workspaceSlug, targetRef string, branches []string) ([]RebaseResult, error) {
			trunkDir := fmt.Sprintf("%s/%s/trunk", workspaceRoot, workspaceSlug)
			runner, err := gitcmd.New(trunkDir, nil)
			if err != nil {
				return nil, fmt.Errorf("merge: create git runner for batch rebase: %w", err)
			}
			return BatchRebase(ctx, runner, targetRef, branches)
		},
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

// ---------------------------------------------------------------------------
// Auth helpers (local copies — workspace package equivalents are unexported)
// ---------------------------------------------------------------------------

func mergeIsPAT(auth *apikit.AuthInfo) bool {
	return auth.CredentialType == "pat"
}

func mergeHasScope(auth *apikit.AuthInfo, scopes ...string) bool {
	for _, s := range scopes {
		if slices.Contains(auth.Permissions, s) {
			return true
		}
	}
	return false
}

// requireMergeWriteScope checks that the caller has merges:write permission
// when using a PAT. Returns an error response if forbidden, or nil to proceed.
func requireMergeWriteScope(c echo.Context, auth *apikit.AuthInfo) error {
	if mergeIsPAT(auth) && !mergeHasScope(auth, "merges:write") {
		return apikit.WriteAPIError(c, http.StatusForbidden, "PAT requires merges:write scope")
	}
	return nil
}

// requireMergeReadScope checks that the caller has merges:read permission
// when using a PAT. Returns an error response if forbidden, or nil to proceed.
func requireMergeReadScope(c echo.Context, auth *apikit.AuthInfo) error {
	if mergeIsPAT(auth) && !mergeHasScope(auth, "merges:read") {
		return apikit.WriteAPIError(c, http.StatusForbidden, "PAT requires merges:read scope")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Workspace lookup helpers
// ---------------------------------------------------------------------------

// workspaceState holds the minimal fields needed by merge handlers.
type workspaceState struct {
	Slug        string
	Status      string
	CloneStatus string
}

// lookupWorkspace retrieves workspace status by slug. Returns nil if not found.
func lookupWorkspace(db *sql.DB, slug string) (*workspaceState, error) {
	var ws workspaceState
	err := db.QueryRow(
		"SELECT slug, status, clone_status FROM workspaces WHERE slug = ?", slug,
	).Scan(&ws.Slug, &ws.Status, &ws.CloneStatus)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ws, nil
}

// validateWorkspaceForMerge checks that the workspace exists, is active, and
// has a ready clone. Writes an error response and returns non-nil error if
// validation fails. Returns nil to proceed.
func validateWorkspaceForMerge(c echo.Context, db *sql.DB, slug string) error {
	ws, err := lookupWorkspace(db, slug)
	if err != nil {
		return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
	}
	if ws == nil {
		return apikit.WriteAPIError(c, http.StatusNotFound, "workspace not found")
	}
	if ws.Status != "active" {
		return apikit.WriteAPIError(c, http.StatusBadRequest, "workspace is not active")
	}
	if ws.CloneStatus != "ready" {
		return apikit.WriteAPIError(c, http.StatusBadRequest, "workspace clone is not ready")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Handler: POST /api/v1/workspaces/:slug/merges
// ---------------------------------------------------------------------------

// submitMergeRequest is the JSON request body for POST /merges.
type submitMergeRequest struct {
	TargetBranch string `json:"target_branch"`
	SourceRef    string `json:"source_ref"`
}

// handleSubmitMerge handles POST /api/v1/workspaces/:slug/merges.
// It validates the request, checks workspace state, verifies branch existence,
// and enqueues a merge job.
func handleSubmitMerge(cfg MergeAPIConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if err := requireMergeWriteScope(c, auth); err != nil {
			return nil // Response already written.
		}

		slug := c.Param("slug")

		// Parse request body.
		var req submitMergeRequest
		if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid request body: "+err.Error())
		}

		// Validate required fields.
		if req.TargetBranch == "" {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "target_branch is required")
		}
		if req.SourceRef == "" {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "source_ref is required")
		}

		// Validate workspace exists, is active, and clone is ready.
		if err := validateWorkspaceForMerge(c, cfg.DB, slug); err != nil {
			return nil // Response already written.
		}

		// Check that both branches exist in the workspace repository.
		if cfg.BranchExists != nil {
			targetExists, err := cfg.BranchExists(slug, req.TargetBranch)
			if err != nil {
				return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
			}
			if !targetExists {
				return apikit.WriteAPIError(c, http.StatusBadRequest,
					fmt.Sprintf("branch %q does not exist in workspace", req.TargetBranch))
			}

			sourceExists, err := cfg.BranchExists(slug, req.SourceRef)
			if err != nil {
				return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
			}
			if !sourceExists {
				return apikit.WriteAPIError(c, http.StatusBadRequest,
					fmt.Sprintf("branch %q does not exist in workspace", req.SourceRef))
			}
		}

		// Resolve submitted_by from auth context.
		submittedBy, err := ResolveSubmittedBy(auth)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		}

		// Enqueue the merge job.
		jobID, duplicate, err := EnqueueMergeJob(cfg.Queue, slug, req.TargetBranch, req.SourceRef, submittedBy)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to enqueue merge job")
		}
		if duplicate {
			return apikit.WriteAPIError(c, http.StatusConflict,
				"a merge job for this source and target branch is already queued or running")
		}

		// Retrieve the created job to return the full response.
		job, err := cfg.Queue.GetByID(jobID)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		}

		resp := ProjectMergeJobResponse(job)
		return c.JSON(http.StatusAccepted, resp)
	}
}

// ---------------------------------------------------------------------------
// Handler: GET /api/v1/workspaces/:slug/merges
// ---------------------------------------------------------------------------

// handleListMerges handles GET /api/v1/workspaces/:slug/merges.
// It queries jobs of type 'merge' scoped to the workspace and returns
// them as merge job response objects.
func handleListMerges(cfg MergeAPIConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if err := requireMergeReadScope(c, auth); err != nil {
			return nil
		}

		slug := c.Param("slug")

		// List all merge jobs and filter by workspace slug via key prefix.
		jobs, err := cfg.Queue.ListByType(MergeJobType, jobqueue.ListOpts{})
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "internal server error")
		}

		// Filter to jobs belonging to this workspace (key starts with "slug:").
		prefix := slug + ":"
		var results []MergeJobResponse
		for _, j := range jobs {
			if strings.HasPrefix(j.Key, prefix) {
				results = append(results, ProjectMergeJobResponse(j))
			}
		}

		// Return non-nil empty array per spec (12-REQ-10.E1).
		if results == nil {
			results = []MergeJobResponse{}
		}

		return c.JSON(http.StatusOK, results)
	}
}

// ---------------------------------------------------------------------------
// Handler: GET /api/v1/workspaces/:slug/merges/:id
// ---------------------------------------------------------------------------

// handleGetMerge handles GET /api/v1/workspaces/:slug/merges/:id.
// It retrieves a single merge job record by ID scoped to the workspace.
func handleGetMerge(cfg MergeAPIConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if err := requireMergeReadScope(c, auth); err != nil {
			return nil
		}

		slug := c.Param("slug")
		jobID := c.Param("id")

		job, err := cfg.Queue.GetByID(jobID)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusNotFound, "merge job not found")
		}

		// Verify the job belongs to this workspace (key starts with "slug:").
		if !strings.HasPrefix(job.Key, slug+":") {
			return apikit.WriteAPIError(c, http.StatusNotFound, "merge job not found")
		}

		// Verify the job is a merge job.
		if job.Type != MergeJobType {
			return apikit.WriteAPIError(c, http.StatusNotFound, "merge job not found")
		}

		resp := ProjectMergeJobResponse(job)
		return c.JSON(http.StatusOK, resp)
	}
}

// ---------------------------------------------------------------------------
// Handler: DELETE /api/v1/workspaces/:slug/merges/:id
// ---------------------------------------------------------------------------

// handleCancelMerge handles DELETE /api/v1/workspaces/:slug/merges/:id.
// It cancels a queued merge job via the durable job queue's cancel operation.
func handleCancelMerge(cfg MergeAPIConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if err := requireMergeWriteScope(c, auth); err != nil {
			return nil
		}

		slug := c.Param("slug")
		jobID := c.Param("id")

		// Look up the job first to verify it exists and belongs to this workspace.
		job, err := cfg.Queue.GetByID(jobID)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusNotFound, "merge job not found")
		}

		// Verify the job belongs to this workspace.
		if !strings.HasPrefix(job.Key, slug+":") {
			return apikit.WriteAPIError(c, http.StatusNotFound, "merge job not found")
		}

		// Verify the job is a merge job.
		if job.Type != MergeJobType {
			return apikit.WriteAPIError(c, http.StatusNotFound, "merge job not found")
		}

		// The merge API requires 409 for any non-queued status (12-REQ-11.2),
		// including cancelled — even though the jobqueue's CancelJob is
		// idempotent for already-cancelled jobs (returns nil).
		if job.Status != jobqueue.StatusQueued {
			return apikit.WriteAPIError(c, http.StatusConflict,
				"job cannot be cancelled in its current state")
		}

		// Attempt to cancel the job.
		if err := cfg.Queue.CancelJob(jobID); err != nil {
			if errors.Is(err, jobqueue.ErrNotCancellable) {
				return apikit.WriteAPIError(c, http.StatusConflict,
					"job cannot be cancelled in its current state")
			}
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to cancel job")
		}

		return c.JSON(http.StatusOK, map[string]string{"status": "cancelled"})
	}
}

// ---------------------------------------------------------------------------
// Handler: POST /api/v1/workspaces/:slug/rebase
// ---------------------------------------------------------------------------

// handleBatchRebase handles POST /api/v1/workspaces/:slug/rebase.
// It validates the request, checks workspace state, and executes a batch
// rebase operation returning per-branch results synchronously.
func handleBatchRebase(cfg MergeAPIConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if err := requireMergeWriteScope(c, auth); err != nil {
			return nil // Response already written.
		}

		slug := c.Param("slug")

		// Parse request body.
		var req batchRebaseRequest
		if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid request body: "+err.Error())
		}

		// Validate required fields.
		if req.TargetRef == "" {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "target_ref is required")
		}
		if len(req.Branches) == 0 {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "branches list must not be empty")
		}

		// Validate workspace exists and is active.
		if err := validateWorkspaceForMerge(c, cfg.DB, slug); err != nil {
			return nil // Response already written.
		}

		// Execute batch rebase.
		if cfg.BatchRebase == nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "batch rebase not configured")
		}

		results, err := cfg.BatchRebase(c.Request().Context(), slug, req.TargetRef, req.Branches)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "batch rebase failed: "+err.Error())
		}

		return c.JSON(http.StatusOK, batchRebaseResponse{Results: results})
	}
}
