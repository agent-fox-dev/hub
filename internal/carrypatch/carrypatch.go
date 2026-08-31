package carrypatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// ===========================================================================
// Constants
// ===========================================================================

const (
	// StrategyRebase uses cherry-pick to apply patches (default).
	StrategyRebase = "rebase"
	// StrategyMerge uses --no-ff merge to apply patches.
	StrategyMerge = "merge"
)

// Fail mode constants for rebuild conflict handling.
const (
	// FailModeFailFast aborts the rebuild on the first conflict (default).
	FailModeFailFast = "fail_fast"
	// FailModeContinue skips conflicting patches and continues processing.
	FailModeContinue = "continue"
)

// Patch status constants.
const (
	PatchStatusActive         = "active"
	PatchStatusConflict       = "conflict"
	PatchStatusDisabled       = "disabled"
	PatchStatusMergedUpstream = "merged_upstream"
)

// ===========================================================================
// Error types
// ===========================================================================

// CherryPickConflictError indicates a cherry-pick produced a merge conflict.
type CherryPickConflictError struct {
	Files []string
}

func (e *CherryPickConflictError) Error() string {
	return "cherry-pick conflict"
}

// MergeNoFFConflictError indicates a --no-ff merge produced a conflict.
type MergeNoFFConflictError struct {
	Files []string
}

func (e *MergeNoFFConflictError) Error() string {
	return "merge --no-ff conflict"
}

// TransientError wraps a transient (retryable) error.
type TransientError struct {
	Err error
}

func (e *TransientError) Error() string {
	if e.Err != nil {
		return "transient error: " + e.Err.Error()
	}
	return "transient error"
}

func (e *TransientError) Unwrap() error {
	return e.Err
}

// ===========================================================================
// Domain types
// ===========================================================================

// Patch represents a carry-patch record from the patches table.
type Patch struct {
	ID             string   `json:"id"`
	WorkspaceID    string   `json:"workspace_id"`
	BranchName     string   `json:"branch_name"`
	Position       int      `json:"position"`
	Status         string   `json:"status"`
	ConflictFiles  []string `json:"conflict_files,omitempty"`
	UpstreamPRURL  *string  `json:"upstream_pr_url,omitempty"`
}

// RebuildPayload is the JSON payload stored in the job queue for rebuild jobs.
type RebuildPayload struct {
	WorkspaceSlug     string `json:"workspace_slug"`
	Strategy          string `json:"strategy"`
	SubmittedBy       string `json:"submitted_by"`
	IntegrationBranch string `json:"integration_branch,omitempty"`
	FailMode          string `json:"fail_mode,omitempty"`
}

// PatchResult is the per-patch outcome recorded in the rebuild result.
type PatchResult struct {
	PatchID       string   `json:"patch_id"`
	BranchName    string   `json:"branch_name"`
	Position      int      `json:"position"`
	Status        string   `json:"status"`
	SkippedReason string   `json:"skipped_reason,omitempty"`
	NewHeadSHA    *string  `json:"new_head_sha"`
	ConflictFiles []string `json:"conflict_files,omitempty"`
}

// RebuildResult is the structured result returned by a successful rebuild job.
type RebuildResult struct {
	UpstreamHeadSHA             string        `json:"upstream_head_sha"`
	IntegrationHeadSHA          string        `json:"integration_head_sha"`
	PreviousIntegrationHeadSHA  string        `json:"previous_integration_head_sha,omitempty"`
	Strategy                    string        `json:"strategy"`
	FailMode                    string        `json:"fail_mode,omitempty"`
	PatchesApplied              int           `json:"patches_applied"`
	PatchesSkipped              int           `json:"patches_skipped"`
	PatchesConflicted           int           `json:"patches_conflicted"`
	PatchesRemoved              int           `json:"patches_removed"`
	PatchResults                []PatchResult `json:"patch_results"`
}

// ===========================================================================
// Interfaces
// ===========================================================================

// GitRunner abstracts git CLI operations for testing.
type GitRunner interface {
	Run(ctx context.Context, args ...string) (string, error)
	CherryPick(ctx context.Context, commitSHA string) error
	MergeNoFF(ctx context.Context, branch string) error
	MergeTree(ctx context.Context, base, head string) (string, error)
	IsAncestor(ctx context.Context, ancestor, descendant string) (bool, error)
	Cherry(ctx context.Context, upstream, head string) (applied []string, pending []string, err error)
	HardReset(ctx context.Context, ref string) error
}

// PatchStore abstracts patch table operations for testing.
type PatchStore interface {
	ListPatches(ctx context.Context, workspaceSlug string) ([]Patch, error)
	UpdatePatchStatus(ctx context.Context, patchID, status string, conflictFiles []string) error
	DeletePatch(ctx context.Context, patchID string) error
	CompactPositions(ctx context.Context, workspaceSlug string) error
}

// FetchFunc fetches from the upstream remote.
type FetchFunc func(ctx context.Context, repoPath string) error

// ResolveAuthFunc resolves upstream auth credentials.
type ResolveAuthFunc func(workspaceSlug string) error

// GetVariableFunc retrieves a workspace variable.
type GetVariableFunc func(scope, slug, key string) (string, error)

// ===========================================================================
// RebuildHandler
// ===========================================================================

// RebuildHandler holds dependencies for the rebuild job executor.
type RebuildHandler struct {
	DB            *sql.DB
	Queue         *jobqueue.Queue
	Logger        *slog.Logger
	WorkspaceRoot string
	NewGitRunner  func(repoPath string) (GitRunner, error)
	Fetch         FetchFunc
	ResolveAuth   ResolveAuthFunc
	GetVariable   GetVariableFunc
	PatchStore    PatchStore
}

// RegisterRebuildJob registers the 'rebuild' job type in the job queue.
func RegisterRebuildJob(q *jobqueue.Queue, h *RebuildHandler) error {
	handler := func(ctx context.Context, payload json.RawMessage) (any, bool, error) {
		return h.HandleRebuildJob(ctx, payload)
	}
	return q.Register("rebuild", jobqueue.HandlerFunc(handler), nil)
}

// HandleRebuildJob is implemented in rebuild_executor.go.
