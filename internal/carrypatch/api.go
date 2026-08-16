package carrypatch

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"slices"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

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
	ID           string          `json:"id"`
	Status       string          `json:"status"`
	Strategy     string          `json:"strategy,omitempty"`
	Error        string          `json:"error,omitempty"`
	CreatedAt    string          `json:"created_at"`
	CompletedAt  *string         `json:"completed_at"`
	PatchResults json.RawMessage `json:"patch_results,omitempty"`
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
	PatchesMerged    []string `json:"patches_merged"`
	RebuildTriggered bool     `json:"rebuild_triggered"`
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
	ID                    string   `json:"id"`
	BranchName            string   `json:"branch_name"`
	Position              int      `json:"position"`
	Status                string   `json:"status"`
	LastRebuildResult     *string  `json:"last_rebuild_result"`
	ConflictFiles         []string `json:"conflict_files,omitempty"`
	RerereResolutionCount int      `json:"rerere_resolution_count"`
}

// PatchStatusSummary aggregates patch status counts.
type PatchStatusSummary struct {
	TotalPatches   int `json:"total_patches"`
	Active         int `json:"active"`
	MergedUpstream int `json:"merged_upstream"`
	Conflict       int `json:"conflict"`
	Disabled       int `json:"disabled"`
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

// RegisterRebuildRoutes mounts rebuild endpoints on the API group.
func RegisterRebuildRoutes(api *echo.Group, cfg RebuildAPIConfig) {
	api.POST("/workspaces/:slug/rebuild", handleSubmitRebuild(cfg))
	api.GET("/workspaces/:slug/rebuilds", handleListRebuilds(cfg))
	api.GET("/workspaces/:slug/rebuilds/:id", handleGetRebuild(cfg))
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
		var mode, status, cloneStatus, integrationBranch string
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
			return apikit.WriteAPIError(c, http.StatusBadRequest, "rebuild is only supported for carry_patch workspaces")
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
			return apikit.WriteAPIError(c, http.StatusBadRequest, "no patches with status active or conflict")
		}

		// 16-REQ-1.1: capture REBUILD_STRATEGY at enqueue time.
		strategy := StrategyRebase // default
		if cfg.GetVariable != nil {
			val, varErr := cfg.GetVariable("workspace", slug, "REBUILD_STRATEGY")
			if varErr == nil && val != "" {
				strategy = val
			}
		}

		// Build payload. 16-PROP-3: capture strategy at enqueue time.
		payload := RebuildPayload{
			WorkspaceSlug:     slug,
			Strategy:          strategy,
			SubmittedBy:       auth.UserID,
			IntegrationBranch: integrationBranch,
		}
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to marshal payload")
		}

		// 16-REQ-1.1: enqueue with key=workspace_slug, group_key=<slug>:<integration_branch>.
		groupKey := slug + ":" + integrationBranch
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
			return apikit.WriteAPIError(c, http.StatusConflict, "a rebuild job is already queued or running for this workspace")
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

	// Extract patch_results from result for completed jobs.
	if j.Result != nil {
		var result RebuildResult
		if err := json.Unmarshal(j.Result, &result); err == nil && len(result.PatchResults) > 0 {
			patchResultsJSON, err := json.Marshal(result.PatchResults)
			if err == nil {
				rec.PatchResults = patchResultsJSON
			}
		}
	}

	return rec
}

// RegisterRerereRoutes mounts rerere management endpoints on the API group.
// Stub: implementation in later task groups.
func RegisterRerereRoutes(_ *echo.Group, _ RerereAPIConfig) {
	// GET    /workspaces/:slug/rerere — to be implemented.
	// DELETE /workspaces/:slug/rerere/*pathspec — to be implemented.
}

// RegisterSyncRoutes mounts carry-patch sync extension endpoints on the API group.
// Stub: implementation in later task groups.
func RegisterSyncRoutes(_ *echo.Group, _ SyncAPIConfig) {
	// POST /workspaces/:slug/sync — carry-patch extension to be implemented.
}

// RegisterPatchStatusRoutes mounts patch-status dashboard endpoints on the API group.
// Stub: implementation in later task groups.
func RegisterPatchStatusRoutes(_ *echo.Group, _ PatchStatusAPIConfig) {
	// GET /workspaces/:slug/patch-status — to be implemented.
}

// HandleCarryPatchSync handles a carry-patch sync operation.
// Stub: implementation in later task groups.
func HandleCarryPatchSync(_ *SyncAPIConfig, _ string) (*CarryPatchSyncResponse, error) {
	return nil, nil
}
