package carrypatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// ===========================================================================
// Sync hook for workspace sync handler integration
// ===========================================================================

// NewCarryPatchSyncHook returns a function suitable for use with
// workspace.RegisterCarryPatchSyncHook. It wraps the carry-patch sync logic
// so that it can be called from the workspace sync handler when the workspace
// is in carry_patch mode (16-REQ-5).
//
// The returned function receives the Echo context, workspace slug, and repo
// path from the workspace sync handler. It performs upstream fetch, merge
// detection, and auto-rebuild enqueue, then returns (true, nil) to indicate
// the sync was handled.
func NewCarryPatchSyncHook(cfg SyncAPIConfig) func(c echo.Context, slug, repoPath string) (bool, error) {
	handler := handleCarryPatchSyncEndpoint(cfg)
	return func(c echo.Context, slug, repoPath string) (bool, error) {
		// Delegate to the full carry-patch sync handler. Auth is already
		// validated by the workspace sync handler, so this just executes
		// the carry-patch-specific logic.
		if err := handler(c); err != nil {
			return true, err
		}
		return true, nil
	}
}

// ===========================================================================
// POST /workspaces/:slug/sync — carry-patch sync extension
// ===========================================================================

// handleCarryPatchSyncEndpoint handles POST /api/v1/workspaces/:slug/sync.
//
// 16-REQ-5: For carry_patch workspaces, extends the standard sync with
// upstream merge detection (IsAncestor) and auto-rebuild triggering.
// 16-REQ-5.E4: For standard workspaces, returns a simple response without
// carry-patch-specific fields (patches_merged, rebuild_triggered).
func handleCarryPatchSyncEndpoint(cfg SyncAPIConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		// Auth check.
		auth := apikit.GetAuthInfo(c)
		if auth == nil {
			return apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
		}
		if isPAT(auth) && !hasScope(auth, "workspaces:sync", "workspaces:write") {
			return apikit.WriteAPIError(c, http.StatusForbidden, "missing required scope: workspaces:sync")
		}

		slug := c.Param("slug")

		// Load workspace record.
		var mode, status, cloneStatus, integrationBranch string
		var upstreamHeadSHA sql.NullString
		err := cfg.DB.QueryRow(
			`SELECT workspace_mode, status, clone_status, integration_branch, upstream_head_sha
			 FROM workspaces WHERE slug = ?`, slug,
		).Scan(&mode, &status, &cloneStatus, &integrationBranch, &upstreamHeadSHA)
		if err == sql.ErrNoRows {
			return apikit.WriteAPIError(c, http.StatusNotFound, "workspace not found")
		}
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "database error")
		}

		// 16-REQ-5.E4: standard workspaces use standard sync behavior
		// without carry-patch extensions.
		if mode != "carry_patch" {
			return c.JSON(http.StatusOK, map[string]string{"status": "synced"})
		}

		ctx := c.Request().Context()

		// 16-REQ-5.1: Resolve upstream credentials via resolveUpstreamAuth.
		if cfg.ResolveAuth != nil {
			if authErr := cfg.ResolveAuth(slug); authErr != nil {
				// 16-REQ-5.E1: auth failure aborts sync; no state modified.
				return apikit.WriteAPIError(c, http.StatusBadGateway, "failed to resolve upstream credentials")
			}
		}

		// Determine repo path and create git runner.
		repoPath := filepath.Join(cfg.WorkspaceRoot, slug, "trunk")
		git, gitErr := cfg.NewGitRunner(repoPath)
		if gitErr != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to create git runner")
		}

		// 16-REQ-5.1: Fetch from the 'upstream' remote (not 'origin').
		if cfg.Fetch != nil {
			if fetchErr := cfg.Fetch(ctx, repoPath); fetchErr != nil {
				// 16-REQ-5.E1 / 16-ERR-8: fetch failure aborts sync;
				// upstream_tracking_ref and patch statuses are not modified.
				return apikit.WriteAPIError(c, http.StatusBadGateway, "upstream fetch failed")
			}
		}

		// Resolve the new upstream HEAD after fetch.
		newUpstreamHead, err := git.Run(ctx, "rev-parse", "FETCH_HEAD")
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to resolve upstream HEAD")
		}

		// Compare with stored upstream_head_sha.
		storedSHA := ""
		if upstreamHeadSHA.Valid {
			storedSHA = upstreamHeadSHA.String
		}
		upstreamAdvanced := newUpstreamHead != storedSHA

		// Prepare response.
		resp := CarryPatchSyncResponse{
			PatchesMerged:    make([]string, 0),
			RebuildTriggered: false,
		}

		// 16-REQ-5.E3: If upstream HEAD has not changed, complete the sync
		// with no patches_merged and no rebuild triggered.
		if !upstreamAdvanced {
			return c.JSON(http.StatusOK, resp)
		}

		// Upstream has advanced — update the workspace record.
		now := apikit.NowUTC()
		_, err = cfg.DB.Exec(
			`UPDATE workspaces SET upstream_head_sha = ?, last_sync_at = ?, updated_at = ? WHERE slug = ?`,
			newUpstreamHead, now, now, slug,
		)
		if err != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to update workspace")
		}

		// 16-REQ-5.1: Check each active patch for upstream merge via IsAncestor.
		patches, listErr := cfg.PatchStore.ListPatches(ctx, slug)
		if listErr != nil {
			return apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to list patches")
		}

		// Determine squash merge detection mode from workspace variable.
		// Values: "ancestry_only", "content_based", "both" (default).
		squashDetectionMode := "both"
		if cfg.GetVariable != nil {
			val, _ := cfg.GetVariable("workspace", slug, "SQUASH_MERGE_DETECTION")
			if val == "ancestry_only" || val == "content_based" || val == "both" {
				squashDetectionMode = val
			}
		}

		for _, patch := range patches {
			// Only check active patches.
			// 16-PROP-6: merged_upstream is monotonic — never revert.
			if patch.Status != PatchStatusActive {
				continue
			}

			merged := false

			// Step 1: ancestry check (unless mode is content_based only).
			if squashDetectionMode != "content_based" {
				// 16-REQ-5.2: Check if patch branch HEAD is an ancestor of the
				// new upstream HEAD.
				ancestorResult, ancestorErr := git.IsAncestor(ctx, patch.BranchName, newUpstreamHead)
				if ancestorErr != nil {
					// 16-REQ-5.E2: skip patch if IsAncestor errors (e.g., ref
					// does not exist locally). Leave status unchanged.
					continue
				}
				merged = ancestorResult
			}

			// Step 2: squash merge fallback (content-based + PR-number scanning).
			if !merged && squashDetectionMode != "ancestry_only" {
				merged = detectSquashMerge(ctx, git, patch, storedSHA, newUpstreamHead)
			}

			if merged {
				// Transition patch to merged_upstream.
				_ = cfg.PatchStore.UpdatePatchStatus(ctx, patch.ID, PatchStatusMergedUpstream, nil)
				resp.PatchesMerged = append(resp.PatchesMerged, patch.BranchName)
			}
		}

		// ===========================================================
		// 16-REQ-5.3 / 16-REQ-5.4: Auto-rebuild trigger logic
		// ===========================================================

		// Since we already returned early when upstream hasn't advanced,
		// shouldRebuild is always true here (upstream advanced OR patches
		// merged). Check the AUTO_REBUILD_AFTER_SYNC workspace variable.
		autoRebuild := true // default when unset (16-REQ-5.3)
		if cfg.GetVariable != nil {
			val, _ := cfg.GetVariable("workspace", slug, "AUTO_REBUILD_AFTER_SYNC")
			if val == "false" {
				// 16-REQ-5.4: explicitly disabled.
				autoRebuild = false
			}
		}

		if autoRebuild {
			// Capture strategy at enqueue time (16-PROP-3).
			strategy := StrategyRebase
			if cfg.GetVariable != nil {
				val, _ := cfg.GetVariable("workspace", slug, "REBUILD_STRATEGY")
				if val != "" {
					strategy = val
				}
			}

			payload := RebuildPayload{
				WorkspaceSlug:     slug,
				Strategy:          strategy,
				SubmittedBy:       auth.UserID,
				IntegrationBranch: integrationBranch,
			}
			payloadJSON, _ := json.Marshal(payload)
			groupKey := slug + ":" + integrationBranch
			nonce := uuid.New().String()

			jobID, duplicate, enqErr := cfg.Queue.Enqueue(jobqueue.EnqueueParams{
				Type:        "rebuild",
				Key:         slug,
				Nonce:       nonce,
				Payload:     payloadJSON,
				SubmittedBy: auth.UserID,
				Group:       groupKey,
			})

			if enqErr == nil && !duplicate {
				resp.RebuildTriggered = true
				resp.RebuildJobID = &jobID
			}
			// 16-REQ-5.3 / 16-PROP-7: if duplicate key is already queued or
			// running, silently ignore — rebuild_triggered stays false.
		}

		return c.JSON(http.StatusOK, resp)
	}
}

// ===========================================================================
// Squash merge detection helpers
// ===========================================================================

// detectSquashMerge uses content-based and PR-number heuristics to determine
// whether a patch has been squash-merged upstream. Returns true if the patch
// is detected as merged.
//
// Strategy:
//  1. Content-based: use `git cherry` to compare patch commits against the
//     upstream range. If all patch commits have content-equivalent matches
//     upstream (no pending commits), the patch is effectively merged.
//  2. PR-number scanning: if the patch has an upstream_pr_url, extract the PR
//     number and scan recent upstream commit messages for GitHub's squash-merge
//     format "Title (#NNN)".
//
// Either signal is sufficient to declare the patch merged.
func detectSquashMerge(ctx context.Context, git GitRunner, patch Patch, oldUpstreamHead, newUpstreamHead string) bool {
	// Try content-based detection via git cherry.
	if detectSquashMergeByContent(ctx, git, patch.BranchName, newUpstreamHead) {
		return true
	}

	// Try PR-number scanning if upstream_pr_url is set.
	if patch.UpstreamPRURL != nil && *patch.UpstreamPRURL != "" {
		prNumber := extractPRNumber(*patch.UpstreamPRURL)
		if prNumber != "" {
			return detectSquashMergeByPRNumber(ctx, git, prNumber, oldUpstreamHead, newUpstreamHead)
		}
	}

	return false
}

// detectSquashMergeByContent uses git cherry to check whether ALL commits on
// the patch branch have content-equivalent matches in the upstream. Returns
// true only if there are zero pending commits (all applied).
func detectSquashMergeByContent(ctx context.Context, git GitRunner, patchBranch, upstreamHead string) bool {
	applied, pending, err := git.Cherry(ctx, upstreamHead, patchBranch)
	if err != nil {
		return false
	}
	// Cherry returns applied and pending. If there are no pending commits,
	// all patch content exists upstream.
	// Guard: if cherry returned nothing at all (no commits to compare),
	// don't falsely detect as merged.
	if len(applied) == 0 && len(pending) == 0 {
		return false
	}
	return len(pending) == 0
}

// extractPRNumber extracts the PR number from a GitHub PR URL.
// Example: "https://github.com/org/repo/pull/42" -> "42"
func extractPRNumber(url string) string {
	// Match the /pull/<number> suffix.
	idx := strings.LastIndex(url, "/pull/")
	if idx < 0 {
		return ""
	}
	num := url[idx+len("/pull/"):]
	// Strip any trailing path segments or query strings.
	if slashIdx := strings.IndexByte(num, '/'); slashIdx >= 0 {
		num = num[:slashIdx]
	}
	if qIdx := strings.IndexByte(num, '?'); qIdx >= 0 {
		num = num[:qIdx]
	}
	// Validate it's numeric.
	for _, c := range num {
		if c < '0' || c > '9' {
			return ""
		}
	}
	if num == "" {
		return ""
	}
	return num
}

// detectSquashMergeByPRNumber scans recent upstream commits for a GitHub
// squash-merge commit message containing "(#NNN)" where NNN matches the
// given PR number. Only searches commits between oldUpstreamHead and
// newUpstreamHead to avoid false matches on old history.
func detectSquashMergeByPRNumber(ctx context.Context, git GitRunner, prNumber, oldUpstreamHead, newUpstreamHead string) bool {
	// Build the git log range. If we have the old upstream head, scope to
	// only new commits. Otherwise scan the last 50 commits.
	var logOutput string
	var err error
	if oldUpstreamHead != "" {
		logOutput, err = git.Run(ctx, "log", "--oneline", "--format=%s", fmt.Sprintf("%s..%s", oldUpstreamHead, newUpstreamHead))
	} else {
		logOutput, err = git.Run(ctx, "log", "--oneline", "--format=%s", "-50", newUpstreamHead)
	}
	if err != nil {
		return false
	}

	// Scan each commit message for the PR number pattern.
	target := fmt.Sprintf("(#%s)", prNumber)
	for _, line := range strings.Split(logOutput, "\n") {
		if strings.Contains(line, target) {
			return true
		}
	}
	return false
}
