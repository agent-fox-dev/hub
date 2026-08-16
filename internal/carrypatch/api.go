package carrypatch

import (
	"database/sql"
	"encoding/json"

	"github.com/labstack/echo/v4"

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

// RegisterRebuildRoutes mounts rebuild endpoints on the API group.
// Stub: implementation in later task groups.
func RegisterRebuildRoutes(_ *echo.Group, _ RebuildAPIConfig) {
	// POST /workspaces/:slug/rebuild — to be implemented.
	// GET  /workspaces/:slug/rebuilds — to be implemented.
	// GET  /workspaces/:slug/rebuilds/:id — to be implemented.
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
