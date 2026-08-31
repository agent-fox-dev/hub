package carrypatch

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/gitcmd"
	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// ===========================================================================
// API configuration
// ===========================================================================

// RebuildAPIConfig holds dependencies for rebuild HTTP endpoints.
type RebuildAPIConfig struct {
	DB          *sql.DB
	Queue       *jobqueue.Queue
	GetVariable GetVariableFunc
}

// RebuildJobResponse is the JSON response body for a rebuild job.
type RebuildJobResponse struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Key      string          `json:"key"`
	GroupKey string          `json:"group_key"`
	Status   string          `json:"status"`
	Payload  json.RawMessage `json:"payload"`
}

// RebuildListResponse is the JSON response for GET /rebuilds.
type RebuildListResponse struct {
	Jobs []RebuildJobRecord `json:"jobs"`
}

// RebuildJobRecord is a single rebuild job in list and detail responses.
type RebuildJobRecord struct {
	ID                          string          `json:"id"`
	Status                      string          `json:"status"`
	Strategy                    string          `json:"strategy,omitempty"`
	Error                       string          `json:"error,omitempty"`
	CreatedAt                   string          `json:"created_at"`
	CompletedAt                 *string         `json:"completed_at"`
	PatchResults                json.RawMessage `json:"patch_results,omitempty"`
	IntegrationHeadSHA          string          `json:"integration_head_sha,omitempty"`
	PreviousIntegrationHeadSHA  string          `json:"previous_integration_head_sha,omitempty"`
}

// RollbackResponse is the JSON response for POST /rebuilds/:id/rollback.
type RollbackResponse struct {
	RolledBackTo string `json:"rolled_back_to"`
}

// RebuildPreviewAPIConfig holds dependencies for the rebuild-preview endpoint.
type RebuildPreviewAPIConfig struct {
	DB            *sql.DB
	WorkspaceRoot string
	NewGitRunner  func(repoPath string) (GitRunner, error)
	PatchStore    PatchStore
}

// RebuildPreviewResponse is the JSON response for GET /rebuild-preview.
type RebuildPreviewResponse struct {
	PatchResults []RebuildPreviewPatchResult `json:"patch_results"`
}

// RebuildPreviewPatchResult is a per-patch prediction in the rebuild-preview response.
type RebuildPreviewPatchResult struct {
	PatchID       string   `json:"patch_id"`
	BranchName    string   `json:"branch_name"`
	Position      int      `json:"position"`
	Status        string   `json:"status"`
	TreeSHA       string   `json:"tree_sha,omitempty"`
	ConflictFiles []string `json:"conflict_files"`
}

// RerereAPIConfig holds dependencies for rerere HTTP endpoints.
type RerereAPIConfig struct {
	DB            *sql.DB
	WorkspaceRoot string
	NewGitRunner  func(repoPath string) (GitRunner, error)
}

// RerereListResponse is the JSON response for GET /rerere.
type RerereListResponse struct {
	Resolutions []RerereResolution `json:"resolutions"`
}

// RerereResolution represents a single rerere resolution entry.
type RerereResolution struct {
	Path       *string `json:"path"`
	RecordedAt *string `json:"recorded_at"`
}

// SyncAPIConfig holds dependencies for carry-patch sync extension endpoints.
type SyncAPIConfig struct {
	DB            *sql.DB
	Queue         *jobqueue.Queue
	WorkspaceRoot string
	NewGitRunner  func(repoPath string) (GitRunner, error)
	Fetch         FetchFunc
	ResolveAuth   ResolveAuthFunc
	GetVariable   GetVariableFunc
	PatchStore    PatchStore
}

// CarryPatchSyncResponse extends the standard sync response with carry-patch fields.
type CarryPatchSyncResponse struct {
	PatchesMerged     []string `json:"patches_merged"`
	RebuildTriggered  bool     `json:"rebuild_triggered"`
	RebuildJobID      *string  `json:"rebuild_job_id,omitempty"`
	ForcePushDetected bool     `json:"force_push_detected"`
}

// PatchStatusAPIConfig holds dependencies for patch-status endpoint.
type PatchStatusAPIConfig struct {
	DB            *sql.DB
	Queue         *jobqueue.Queue
	WorkspaceRoot string
	PatchStore    PatchStore
}

// PatchStatusResponse is the JSON response for GET /patch-status.
type PatchStatusResponse struct {
	WorkspaceSlug      string             `json:"workspace_slug"`
	WorkspaceMode      string             `json:"workspace_mode"`
	Status             string             `json:"status"`
	CloneStatus        string             `json:"clone_status"`
	CloneError         string             `json:"clone_error,omitempty"`
	SyncStatus         string             `json:"sync_status"`
	SyncError          string             `json:"sync_error,omitempty"`
	SyncMode           string             `json:"sync_mode"`
	HeadSHA            string             `json:"head_sha"`
	GitURL             string             `json:"git_url"`
	UpstreamURL        string             `json:"upstream_url"`
	UpstreamHeadSHA    string             `json:"upstream_head_sha"`
	IntegrationBranch  string             `json:"integration_branch"`
	IntegrationHeadSHA string             `json:"integration_head_sha"`
	LastSyncAt         *string            `json:"last_sync_at"`
	LastRebuild        *PatchStatusRebuild `json:"last_rebuild"`
	Patches            []PatchStatusEntry `json:"patches"`
	Summary            PatchStatusSummary `json:"summary"`
}

// PatchStatusRebuild is the last rebuild info in the patch-status response.
type PatchStatusRebuild struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// PatchStatusEntry is a single patch in the patch-status response.
type PatchStatusEntry struct {
	ID                string   `json:"id"`
	BranchName        string   `json:"branch_name"`
	Position          int      `json:"position"`
	Status            string   `json:"status"`
	LastRebuildResult *string  `json:"last_rebuild_result"`
	ConflictFiles     []string `json:"conflict_files,omitempty"`
}

// PatchStatusSummary aggregates patch status counts.
type PatchStatusSummary struct {
	TotalPatches          int `json:"total_patches"`
	Active                int `json:"active"`
	MergedUpstream        int `json:"merged_upstream"`
	Conflict              int `json:"conflict"`
	Disabled              int `json:"disabled"`
	TotalRerereResolutions int `json:"total_rerere_resolutions"`
}

// ===========================================================================
// Auth helpers (standalone functions per steering directives)
// ===========================================================================

func isPAT(auth *apikit.AuthInfo) bool {
	return auth.CredentialType == "pat"
}

func hasScope(auth *apikit.AuthInfo, scopes ...string) bool {
	for _, s := range scopes {
		if slices.Contains(auth.Permissions, s) {
			return true
		}
	}
	return false
}

// ===========================================================================
// Rebuild route registration
// ===========================================================================

// RebuildRollbackAPIConfig holds dependencies for the rebuild rollback endpoint.
type RebuildRollbackAPIConfig struct {
	DB            *sql.DB
	Queue         *jobqueue.Queue
	WorkspaceRoot string
	NewGitRunner  func(repoPath string) (GitRunner, error)
}

// RegisterRebuildRoutes mounts rebuild endpoints on the API group.
func RegisterRebuildRoutes(api *echo.Group, cfg RebuildAPIConfig) {
	api.POST("/workspaces/:slug/rebuild", handleSubmitRebuild(cfg))
	api.GET("/workspaces/:slug/rebuilds", handleListRebuilds(cfg))
	api.GET("/workspaces/:slug/rebuilds/:id", handleGetRebuild(cfg))
	api.DELETE("/workspaces/:slug/rebuilds/:id", handleCancelRebuild(cfg))
	api.POST("/workspaces/:slug/rebuilds/:id/requeue", handleRequeueRebuild(cfg))
}

// RegisterRebuildRollbackRoutes mounts the rebuild rollback endpoint on the API group.
func RegisterRebuildRollbackRoutes(api *echo.Group, cfg RebuildRollbackAPIConfig) {
	api.POST("/workspaces/:slug/rebuilds/:id/rollback", handleRollbackRebuild(cfg))
}

// RegisterRebuildPreviewRoutes mounts the rebuild-preview endpoint on the API group.
func RegisterRebuildPreviewRoutes(api *echo.Group, cfg RebuildPreviewAPIConfig) {
	api.GET("/workspaces/:slug/rebuild-preview", handleRebuildPreview(cfg))
}

// ===========================================================================
// GET /workspaces/:slug/rebuild-preview
// ===========================================================================

// handleRebuildPreview performs a read-only conflict prediction for each active
// patch in position order using git merge-tree --write-tree. It does not modify
// any git refs, branches, or patch statuses.
func handleRebuildPreview(cfg RebuildPreviewAPIConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		// 1. Auth check.
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if isPAT(auth) && !hasScope(auth, "rebuilds:read") {
			return apikit.WriteAPIError(c, http.StatusForbidden, "missing required scope: rebuilds:read")
		}

		slug := c.Param("slug")

		// 2. Load workspace and validate.
		var mode, status, cloneStatus string
		err := cfg.DB.QueryRow(
			`SELECT workspace_mode, status, clone_status
			 FROM workspaces WHERE slug = ?`, slug,
		).Scan(&mode, &status, &cloneStatus)
		if err == sql.ErrNoRows {
			return apikit.WriteAPIError(c, http.StatusNotFound, "workspace not found")
		}
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "database error")
		}

		// Workspace must be carry_patch mode.
		if mode != "carry_patch" {
			return apikit.WriteAPIError(c, http.StatusBadRequest,
				"rebuild-preview is only supported for carry_patch workspaces")
		}

		// 3. Determine repo path and create GitRunner.
		repoPath := filepath.Join(cfg.WorkspaceRoot, slug, "trunk")
		git, gitErr := cfg.NewGitRunner(repoPath)
		if gitErr != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError,
				"failed to initialize git runner")
		}

		// 4. Resolve upstream HEAD.
		upstreamHead, headErr := git.Run(c.Request().Context(), "rev-parse", "HEAD")
		if headErr != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError,
				"failed to resolve upstream HEAD")
		}

		// 5. List active patches in position order.
		patches, listErr := cfg.PatchStore.ListPatches(c.Request().Context(), slug)
		if listErr != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError,
				"failed to list patches")
		}

		// 6. Filter to active/conflict patches and sort by position.
		sort.Slice(patches, func(i, j int) bool {
			return patches[i].Position < patches[j].Position
		})

		// 7. Iterate patches and predict each one using MergeTree.
		ctx := c.Request().Context()
		currentBase := upstreamHead
		results := make([]RebuildPreviewPatchResult, 0, len(patches))

		for _, patch := range patches {
			pr := RebuildPreviewPatchResult{
				PatchID:       patch.ID,
				BranchName:    patch.BranchName,
				Position:      patch.Position,
				ConflictFiles: []string{},
			}

			// Skip non-active patches (disabled, merged_upstream).
			if patch.Status == PatchStatusDisabled || patch.Status == PatchStatusMergedUpstream {
				continue
			}

			// Resolve the patch branch tip commit.
			patchHead, revErr := git.Run(ctx, "rev-parse", "--verify", patch.BranchName)
			if revErr != nil {
				// Branch doesn't exist; skip this patch.
				pr.Status = "would_succeed"
				results = append(results, pr)
				continue
			}

			// Perform read-only merge-tree check.
			treeSHA, mergeErr := git.MergeTree(ctx, currentBase, patchHead)
			if mergeErr != nil {
				// Check for MergeConflictError from gitcmd.
				var conflictErr *gitcmd.MergeConflictError
				if errors.As(mergeErr, &conflictErr) {
					pr.Status = "would_conflict"
					pr.ConflictFiles = conflictErr.ConflictingFiles
					results = append(results, pr)
					// Don't update currentBase on conflict; subsequent
					// patches are tested against the last successful base.
					continue
				}
				// Unknown error; report as would_conflict with no files.
				pr.Status = "would_conflict"
				results = append(results, pr)
				continue
			}

			// Clean merge.
			pr.Status = "would_succeed"
			pr.TreeSHA = treeSHA
			results = append(results, pr)

			// For cumulative preview: create a temporary commit from the
			// tree SHA so subsequent patches can be tested against it.
			// Use git commit-tree to create a parentless commit object
			// (read-only: only creates an unreferenced object, no ref updates).
			commitSHA, commitErr := git.Run(ctx, "commit-tree", treeSHA,
				"-p", currentBase, "-p", patchHead, "-m", "preview")
			if commitErr == nil {
				currentBase = commitSHA
			}
		}

		return c.JSON(http.StatusOK, RebuildPreviewResponse{
			PatchResults: results,
		})
	}
}

// ===========================================================================
// POST /workspaces/:slug/rebuild
// ===========================================================================

func handleSubmitRebuild(cfg RebuildAPIConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		// 1. Auth check.
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if isPAT(auth) && !hasScope(auth, "rebuilds:write") {
			return apikit.WriteAPIError(c, http.StatusForbidden, "missing required scope: rebuilds:write")
		}

		slug := c.Param("slug")

		// 2. Load workspace and validate.
		var mode, status, cloneStatus string
		var integrationBranch sql.NullString
		err := cfg.DB.QueryRow(
			`SELECT workspace_mode, status, clone_status, integration_branch
			 FROM workspaces WHERE slug = ?`, slug,
		).Scan(&mode, &status, &cloneStatus, &integrationBranch)
		if err == sql.ErrNoRows {
			return apikit.WriteAPIError(c, http.StatusNotFound, "workspace not found")
		}
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "database error")
		}

		// 16-REQ-1.E1: workspace must be carry_patch mode.
		if mode != "carry_patch" {
			return apikit.WriteAPIErrorWithType(c, http.StatusBadRequest, "rebuild is only supported for carry_patch workspaces", "workspace_mode_mismatch")
		}

		// 16-REQ-1.E2: workspace must be active with ready clone.
		if status != "active" {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "workspace is not active")
		}
		if cloneStatus != "ready" {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "workspace clone is not ready")
		}

		// 16-REQ-1.E3: at least one patch with status 'active' or 'conflict'.
		var patchCount int
		err = cfg.DB.QueryRow(
			`SELECT COUNT(*) FROM patches
			 WHERE workspace_slug = ? AND status IN (?, ?)`,
			slug, PatchStatusActive, PatchStatusConflict,
		).Scan(&patchCount)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "database error")
		}
		if patchCount == 0 {
			return apikit.WriteAPIErrorWithType(c, http.StatusBadRequest, "no patches with status active or conflict", "no_active_patches")
		}

		// Parse optional strategy and fail_mode overrides from request body.
		var bodyStrategy string
		var bodyFailMode string
		if c.Request().ContentLength > 0 {
			var body struct {
				Strategy string `json:"strategy"`
				FailMode string `json:"fail_mode"`
			}
			if err := c.Bind(&body); err != nil {
				return apikit.WriteAPIError(c, http.StatusBadRequest, "invalid request body")
			}
			if body.Strategy != "" {
				if body.Strategy != StrategyRebase && body.Strategy != StrategyMerge {
					return apikit.WriteAPIError(c, http.StatusBadRequest, "strategy must be 'rebase' or 'merge'")
				}
				bodyStrategy = body.Strategy
			}
			if body.FailMode != "" {
				if body.FailMode != FailModeFailFast && body.FailMode != FailModeContinue {
					return apikit.WriteAPIError(c, http.StatusBadRequest, "fail_mode must be 'fail_fast' or 'continue'")
				}
				bodyFailMode = body.FailMode
			}
		}

		// 16-REQ-1.1: capture REBUILD_STRATEGY at enqueue time.
		// Body strategy overrides the workspace variable.
		strategy := bodyStrategy
		if strategy == "" {
			strategy = StrategyRebase // default
			if cfg.GetVariable != nil {
				val, varErr := cfg.GetVariable("workspace", slug, "REBUILD_STRATEGY")
				if varErr == nil && val != "" {
					strategy = val
				}
			}
		}

		// NS-REQ-4: capture REBUILD_FAIL_MODE at enqueue time.
		// Body fail_mode overrides the workspace variable.
		failMode := bodyFailMode
		if failMode == "" {
			if cfg.GetVariable != nil {
				val, varErr := cfg.GetVariable("workspace", slug, "REBUILD_FAIL_MODE")
				if varErr == nil && (val == FailModeFailFast || val == FailModeContinue) {
					failMode = val
				}
			}
			// Default is empty string; executor defaults to fail_fast.
		}

		// Build payload. 16-PROP-3: capture strategy at enqueue time.
		ib := nullStr(integrationBranch)
		payload := RebuildPayload{
			WorkspaceSlug:     slug,
			Strategy:          strategy,
			SubmittedBy:       auth.UserID,
			IntegrationBranch: ib,
			FailMode:          failMode,
		}
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to marshal payload")
		}

		// 16-REQ-1.1: enqueue with key=workspace_slug, group_key=<slug>:<integration_branch>.
		groupKey := slug + ":" + ib
		nonce := uuid.New().String()

		jobID, duplicate, err := cfg.Queue.Enqueue(jobqueue.EnqueueParams{
			Type:        "rebuild",
			Key:         slug,
			Nonce:       nonce,
			Payload:     payloadJSON,
			SubmittedBy: auth.UserID,
			Group:       groupKey,
		})
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to enqueue rebuild job")
		}

		// 16-REQ-1.E4: duplicate queued/running job.
		if duplicate {
			return apikit.WriteAPIErrorWithType(c, http.StatusConflict, "a rebuild job is already queued or running for this workspace", "concurrent_rebuild")
		}

		// Return the job record.
		resp := RebuildJobResponse{
			ID:       jobID,
			Type:     "rebuild",
			Key:      slug,
			GroupKey: groupKey,
			Status:   jobqueue.StatusQueued,
			Payload:  payloadJSON,
		}
		return c.JSON(http.StatusAccepted, resp)
	}
}

// ===========================================================================
// GET /workspaces/:slug/rebuilds
// ===========================================================================

func handleListRebuilds(cfg RebuildAPIConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Auth check.
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if isPAT(auth) && !hasScope(auth, "rebuilds:read") {
			return apikit.WriteAPIError(c, http.StatusForbidden, "missing required scope: rebuilds:read")
		}

		slug := c.Param("slug")

		// Fetch rebuild jobs for this workspace.
		jobs, err := cfg.Queue.ListByKey("rebuild", slug)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to list rebuild jobs")
		}

		// Build response records.
		records := make([]RebuildJobRecord, 0, len(jobs))
		for _, j := range jobs {
			rec := jobToRebuildRecord(j)
			records = append(records, rec)
		}

		return c.JSON(http.StatusOK, RebuildListResponse{Jobs: records})
	}
}

// ===========================================================================
// GET /workspaces/:slug/rebuilds/:id
// ===========================================================================

func handleGetRebuild(cfg RebuildAPIConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Auth check.
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if isPAT(auth) && !hasScope(auth, "rebuilds:read") {
			return apikit.WriteAPIError(c, http.StatusForbidden, "missing required scope: rebuilds:read")
		}

		slug := c.Param("slug")
		jobID := c.Param("id")

		// Fetch the job.
		j, err := cfg.Queue.GetByID(jobID)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusNotFound, "rebuild job not found")
		}

		// 16-REQ-2.E2: prevent cross-workspace information leakage.
		if j.Key != slug {
			return apikit.WriteAPIError(c, http.StatusNotFound, "rebuild job not found")
		}

		rec := jobToRebuildRecord(j)
		return c.JSON(http.StatusOK, rec)
	}
}

// ===========================================================================
// DELETE /workspaces/:slug/rebuilds/:id
// ===========================================================================

// handleCancelRebuild handles DELETE /api/v1/workspaces/:slug/rebuilds/:id.
// It cancels a queued rebuild job. Returns 200 on success, 409 if the job is
// not in a cancellable state, 404 if the job does not exist or belongs to a
// different workspace.
func handleCancelRebuild(cfg RebuildAPIConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Auth check.
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if isPAT(auth) && !hasScope(auth, "rebuilds:write") {
			return apikit.WriteAPIError(c, http.StatusForbidden, "missing required scope: rebuilds:write")
		}

		slug := c.Param("slug")
		jobID := c.Param("id")

		// Look up the job to verify it exists and belongs to this workspace.
		j, err := cfg.Queue.GetByID(jobID)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusNotFound, "rebuild job not found")
		}

		// Verify the job belongs to this workspace and is a rebuild job.
		if j.Key != slug {
			return apikit.WriteAPIError(c, http.StatusNotFound, "rebuild job not found")
		}
		if j.Type != "rebuild" {
			return apikit.WriteAPIError(c, http.StatusNotFound, "rebuild job not found")
		}

		// Only queued jobs can be cancelled.
		if j.Status != jobqueue.StatusQueued {
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

// ===========================================================================
// POST /workspaces/:slug/rebuilds/:id/requeue
// ===========================================================================

// handleRequeueRebuild handles POST /api/v1/workspaces/:slug/rebuilds/:id/requeue.
// It requeues a dead-lettered rebuild job. Returns 200 with the job record on
// success, 409 if the job is not in dead_letter status or an active job already
// exists, 404 if the job does not exist or belongs to a different workspace.
func handleRequeueRebuild(cfg RebuildAPIConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Auth check.
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if isPAT(auth) && !hasScope(auth, "rebuilds:write") {
			return apikit.WriteAPIError(c, http.StatusForbidden, "missing required scope: rebuilds:write")
		}

		slug := c.Param("slug")
		jobID := c.Param("id")

		// Look up the job to verify it exists and belongs to this workspace.
		j, err := cfg.Queue.GetByID(jobID)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusNotFound, "rebuild job not found")
		}

		// Verify the job belongs to this workspace and is a rebuild job.
		if j.Key != slug {
			return apikit.WriteAPIError(c, http.StatusNotFound, "rebuild job not found")
		}
		if j.Type != "rebuild" {
			return apikit.WriteAPIError(c, http.StatusNotFound, "rebuild job not found")
		}

		// Attempt to requeue the dead-lettered job.
		_, requeueErr := cfg.Queue.RequeueDeadLetter(jobID)
		if requeueErr != nil {
			return apikit.WriteAPIError(c, http.StatusConflict, requeueErr.Error())
		}

		// Retrieve the updated job to return in the response.
		updated, getErr := cfg.Queue.GetByID(jobID)
		if getErr != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to retrieve requeued job")
		}

		rec := jobToRebuildRecord(updated)
		return c.JSON(http.StatusOK, rec)
	}
}

// ===========================================================================
// POST /workspaces/:slug/rebuilds/:id/rollback
// ===========================================================================

// handleRollbackRebuild handles POST /api/v1/workspaces/:slug/rebuilds/:id/rollback.
// It rolls back the integration branch to the previous HEAD SHA stored in the
// rebuild result. Returns 200 on success, 409 if no previous SHA is available
// (first-ever rebuild), 404 if the job does not exist or belongs to a different
// workspace.
func handleRollbackRebuild(cfg RebuildRollbackAPIConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Auth check.
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if isPAT(auth) && !hasScope(auth, "rebuilds:write") {
			return apikit.WriteAPIError(c, http.StatusForbidden, "missing required scope: rebuilds:write")
		}

		slug := c.Param("slug")
		jobID := c.Param("id")

		// Look up the job to verify it exists and belongs to this workspace.
		j, err := cfg.Queue.GetByID(jobID)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusNotFound, "rebuild job not found")
		}

		// Anti-enumeration: cross-workspace lookup returns 404.
		if j.Key != slug {
			return apikit.WriteAPIError(c, http.StatusNotFound, "rebuild job not found")
		}
		if j.Type != "rebuild" {
			return apikit.WriteAPIError(c, http.StatusNotFound, "rebuild job not found")
		}

		// Extract the previous integration head SHA from the job result.
		if j.Result == nil {
			return apikit.WriteAPIError(c, http.StatusConflict, "no previous integration head to roll back to")
		}
		var result RebuildResult
		if err := json.Unmarshal(j.Result, &result); err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to parse rebuild result")
		}
		if result.PreviousIntegrationHeadSHA == "" {
			return apikit.WriteAPIError(c, http.StatusConflict, "no previous integration head to roll back to")
		}

		// Determine the integration branch name from the payload.
		var payload RebuildPayload
		if j.Payload != nil {
			_ = json.Unmarshal(j.Payload, &payload)
		}
		integrationBranch := payload.IntegrationBranch
		if integrationBranch == "" {
			integrationBranch = "integration"
		}

		// Reset the integration branch to the previous HEAD.
		repoPath := filepath.Join(cfg.WorkspaceRoot, slug, "trunk")
		git, gitErr := cfg.NewGitRunner(repoPath)
		if gitErr != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to initialize git runner")
		}

		if _, err := git.Run(c.Request().Context(), "branch", "-f", integrationBranch, result.PreviousIntegrationHeadSHA); err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to roll back integration branch")
		}

		return c.JSON(http.StatusOK, RollbackResponse{
			RolledBackTo: result.PreviousIntegrationHeadSHA,
		})
	}
}

// ===========================================================================
// Helpers
// ===========================================================================

// jobToRebuildRecord converts a jobqueue.Job to a RebuildJobRecord for API responses.
func jobToRebuildRecord(j *jobqueue.Job) RebuildJobRecord {
	rec := RebuildJobRecord{
		ID:        j.ID,
		Status:    j.Status,
		CreatedAt: apikit.FormatUTC(j.CreatedAt),
	}

	// Extract strategy from payload.
	if j.Payload != nil {
		var payload RebuildPayload
		if err := json.Unmarshal(j.Payload, &payload); err == nil {
			rec.Strategy = payload.Strategy
		}
	}

	// Set error if present.
	if j.Error != "" {
		rec.Error = j.Error
	}

	// Set completed_at for terminal states.
	if j.Status == jobqueue.StatusCompleted || j.Status == jobqueue.StatusFailed || j.Status == jobqueue.StatusDeadLetter {
		ts := apikit.FormatUTC(j.UpdatedAt)
		rec.CompletedAt = &ts
	}

	// Extract fields from result for completed jobs.
	if j.Result != nil {
		var result RebuildResult
		if err := json.Unmarshal(j.Result, &result); err == nil {
			if len(result.PatchResults) > 0 {
				patchResultsJSON, err := json.Marshal(result.PatchResults)
				if err == nil {
					rec.PatchResults = patchResultsJSON
				}
			}
			rec.IntegrationHeadSHA = result.IntegrationHeadSHA
			rec.PreviousIntegrationHeadSHA = result.PreviousIntegrationHeadSHA
		}
	}

	// For running jobs, expose intermediate progress as patch_results (NS-REQ-5).
	if j.Status == jobqueue.StatusRunning && j.Progress != nil && rec.PatchResults == nil {
		rec.PatchResults = j.Progress
	}

	return rec
}

// RegisterRerereRoutes mounts rerere management endpoints on the API group.
func RegisterRerereRoutes(api *echo.Group, cfg RerereAPIConfig) {
	api.GET("/workspaces/:slug/rerere", handleListRerere(cfg))
	api.DELETE("/workspaces/:slug/rerere/*", handleForgetRerere(cfg))
}

// RegisterSyncRoutes mounts carry-patch sync extension endpoints on the API group.
func RegisterSyncRoutes(api *echo.Group, cfg SyncAPIConfig) {
	api.POST("/workspaces/:slug/sync", handleCarryPatchSyncEndpoint(cfg))
}

// RegisterPatchStatusRoutes mounts patch-status dashboard endpoints on the API group.
func RegisterPatchStatusRoutes(api *echo.Group, cfg PatchStatusAPIConfig) {
	api.GET("/workspaces/:slug/patch-status", handlePatchStatus(cfg))
}

// ===========================================================================
// GET /workspaces/:slug/patch-status
// ===========================================================================

// handlePatchStatus handles GET /api/v1/workspaces/:slug/patch-status.
//
// 16-REQ-6.1: Aggregates workspace metadata, last rebuild, patches with
// last_rebuild_result, and summary counts including total_rerere_resolutions.
// 16-REQ-6.E1: Returns 400 if workspace is not in carry_patch mode.
// 16-REQ-6.E2: Returns 403 if PAT lacks 'workspaces:read' scope.
// 16-REQ-6.E3: Returns empty patches array and zero summary if no patches.
// 16-REQ-6.E4: Sets total_rerere_resolutions to 0 if rr-cache is inaccessible.
func handlePatchStatus(cfg PatchStatusAPIConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Auth check.
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if isPAT(auth) && !hasScope(auth, "workspaces:read") {
			return apikit.WriteAPIError(c, http.StatusForbidden, "missing required scope: workspaces:read")
		}

		slug := c.Param("slug")

		// Load workspace metadata.
		var mode, wsStatus, cloneStatus, syncStatus, syncMode string
		var upstreamURL, upstreamHeadSHA, integrationBranch, lastSyncAt sql.NullString
		var cloneError, syncError, headSHA, gitURL sql.NullString
		err := cfg.DB.QueryRow(
			`SELECT workspace_mode, status, clone_status, clone_error,
			        sync_status, sync_error, sync_mode, head_sha, git_url,
			        upstream_url, upstream_head_sha, integration_branch, last_sync_at
			 FROM workspaces WHERE slug = ?`, slug,
		).Scan(&mode, &wsStatus, &cloneStatus, &cloneError,
			&syncStatus, &syncError, &syncMode, &headSHA, &gitURL,
			&upstreamURL, &upstreamHeadSHA, &integrationBranch, &lastSyncAt)
		if err == sql.ErrNoRows {
			return apikit.WriteAPIError(c, http.StatusNotFound, "workspace not found")
		}
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "database error")
		}

		// 16-REQ-6.E1: workspace must be carry_patch mode.
		if mode != "carry_patch" {
			return apikit.WriteAPIError(c, http.StatusBadRequest, "patch-status is only available for carry_patch workspaces")
		}

		// Query patches from the database, ordered by position.
		// Exclude soft-deleted patches from the patch-status dashboard.
		patchRows, queryErr := cfg.DB.Query(
			`SELECT id, workspace_slug, branch_name, position, status, conflict_files
			 FROM patches WHERE workspace_slug = ? AND (status != 'deleted' OR status IS NULL) ORDER BY position ASC`, slug,
		)
		if queryErr != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to list patches")
		}
		defer patchRows.Close()

		var patches []Patch
		for patchRows.Next() {
			var p Patch
			var conflictFilesJSON sql.NullString
			if scanErr := patchRows.Scan(&p.ID, &p.WorkspaceID, &p.BranchName, &p.Position, &p.Status, &conflictFilesJSON); scanErr != nil {
				return apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to scan patch")
			}
			if conflictFilesJSON.Valid && conflictFilesJSON.String != "" {
				_ = json.Unmarshal([]byte(conflictFilesJSON.String), &p.ConflictFiles)
			}
			patches = append(patches, p)
		}
		if err := patchRows.Err(); err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to iterate patches")
		}

		// Query the most recent rebuild job.
		var lastRebuild *PatchStatusRebuild
		var lastRebuildResult *RebuildResult
		jobs, jobErr := cfg.Queue.ListByKey("rebuild", slug)
		if jobErr == nil && len(jobs) > 0 {
			// ListByKey returns jobs ordered by created_at descending.
			latestJob := jobs[0]
			lastRebuild = &PatchStatusRebuild{
				ID:     latestJob.ID,
				Status: latestJob.Status,
			}
			// Extract patch_results from the job result.
			if latestJob.Result != nil {
				var result RebuildResult
				if jsonErr := json.Unmarshal(latestJob.Result, &result); jsonErr == nil {
					lastRebuildResult = &result
				}
			}
		}

		// Build per-patch result map from last rebuild.
		patchResultMap := make(map[string]*PatchResult)
		if lastRebuildResult != nil {
			for i := range lastRebuildResult.PatchResults {
				pr := &lastRebuildResult.PatchResults[i]
				patchResultMap[pr.PatchID] = pr
			}
		}

		// Count total rerere resolutions for the workspace.
		// 16-REQ-6.E4: If rr-cache is inaccessible, count remains 0.
		rrCacheDir := filepath.Join(cfg.WorkspaceRoot, slug, "trunk", ".git", "rr-cache")
		totalRerereResolutions := countRerereResolutions(rrCacheDir)

		// Build patches array.
		patchEntries := make([]PatchStatusEntry, 0, len(patches))
		for _, p := range patches {
			entry := PatchStatusEntry{
				ID:         p.ID,
				BranchName: p.BranchName,
				Position:   p.Position,
				Status:     p.Status,
			}

			// Set last_rebuild_result from the most recent rebuild.
			if pr, ok := patchResultMap[p.ID]; ok {
				entry.LastRebuildResult = &pr.Status
				entry.ConflictFiles = pr.ConflictFiles
			}
			// If no rebuild has been attempted, last_rebuild_result stays nil.

			patchEntries = append(patchEntries, entry)
		}

		// 16-REQ-6.2: Compute summary counts from patch statuses.
		summary := PatchStatusSummary{
			TotalPatches:           len(patchEntries),
			TotalRerereResolutions: totalRerereResolutions,
		}
		for _, p := range patchEntries {
			switch p.Status {
			case PatchStatusActive:
				summary.Active++
			case PatchStatusMergedUpstream:
				summary.MergedUpstream++
			case PatchStatusConflict:
				summary.Conflict++
			case PatchStatusDisabled:
				summary.Disabled++
			}
		}

		// Derive integration_head_sha from the last rebuild result.
		integrationHeadSHA := ""
		if lastRebuildResult != nil {
			integrationHeadSHA = lastRebuildResult.IntegrationHeadSHA
		}

		// Build response.
		resp := PatchStatusResponse{
			WorkspaceSlug:      slug,
			WorkspaceMode:      mode,
			Status:             wsStatus,
			CloneStatus:        cloneStatus,
			CloneError:         nullStr(cloneError),
			SyncStatus:         syncStatus,
			SyncError:          nullStr(syncError),
			SyncMode:           syncMode,
			HeadSHA:            nullStr(headSHA),
			GitURL:             nullStr(gitURL),
			UpstreamURL:        nullStr(upstreamURL),
			UpstreamHeadSHA:    nullStr(upstreamHeadSHA),
			IntegrationBranch:  nullStr(integrationBranch),
			IntegrationHeadSHA: integrationHeadSHA,
			LastRebuild:        lastRebuild,
			Patches:            patchEntries,
			Summary:            summary,
		}
		if lastSyncAt.Valid {
			resp.LastSyncAt = &lastSyncAt.String
		}

		return c.JSON(http.StatusOK, resp)
	}
}

// nullStr extracts the string value from a NullString, returning "" if null.
func nullStr(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}

// countRerereResolutions reads the rr-cache directory and returns the total
// count of resolution entries. Returns 0 if rr-cache is inaccessible
// (16-REQ-6.E4).
func countRerereResolutions(rrCacheDir string) int {
	entries, err := os.ReadDir(rrCacheDir)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		subdir := filepath.Join(rrCacheDir, entry.Name())
		path := derivePathFromRRCache(subdir)
		if path != nil {
			count++
		}
	}
	return count
}

