package carrypatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
	"github.com/agent-fox-dev/hub/internal/wslock"
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
// detection, and auto-rebuild enqueue, and returns the carry-patch response
// fields for the caller to merge into the standard sync response, as
// 16-REQ-5.1 requires. It does not write a success body of its own.
//
// A (nil, false, nil) return means the workspace turned out not to be in
// carry_patch mode, and the caller should apply standard sync behaviour
// (16-REQ-5.E4).
func NewCarryPatchSyncHook(cfg SyncAPIConfig) func(c echo.Context, slug, repoPath string) (map[string]any, bool, error) {
	return func(c echo.Context, slug, repoPath string) (map[string]any, bool, error) {
		// Auth is already validated by the workspace sync handler, so this
		// just executes the carry-patch-specific logic.
		resp, err := runCarryPatchSync(cfg, c)
		if err != nil {
			return nil, true, err
		}
		if resp == nil {
			return nil, false, nil
		}
		return resp.asExtras(), true, nil
	}
}

// asExtras renders the carry-patch response as top-level JSON fields to merge
// into the standard sync response. rebuild_job_id is omitted when no rebuild
// was enqueued, matching the `omitempty` tag on the struct field.
func (r *CarryPatchSyncResponse) asExtras() map[string]any {
	extras := map[string]any{
		"patches_merged":      r.PatchesMerged,
		"rebuild_triggered":   r.RebuildTriggered,
		"force_push_detected": r.ForcePushDetected,
		"origin_fetched":      r.OriginFetched,
	}
	if r.RebuildJobID != nil {
		extras["rebuild_job_id"] = *r.RebuildJobID
	}
	// 20-REQ-6: patches_synced and patches_diverged are present only in
	// origin mode. A nil slice means hub mode (omit), a non-nil empty
	// slice means origin mode with no candidates (serialize as []).
	if r.PatchesSynced != nil {
		extras["patches_synced"] = r.PatchesSynced
	}
	if r.PatchesDiverged != nil {
		extras["patches_diverged"] = r.PatchesDiverged
	}
	return extras
}

// ===========================================================================
// POST /workspaces/:slug/sync — carry-patch sync extension
// ===========================================================================

// runCarryPatchSync executes the carry-patch sync extension for the workspace
// named by the request's :slug parameter: upstream fetch, merge detection
// (IsAncestor plus the squash-merge heuristics), and auto-rebuild enqueue
// (16-REQ-5).
//
// It returns the carry-patch response fields on success. A nil response with a
// nil error means the workspace is not in carry_patch mode and the caller
// should apply standard sync behaviour instead (16-REQ-5.E4).
//
// This function deliberately does not write a success body. 16-REQ-5.1
// requires the carry-patch fields to accompany the standard sync fields, and
// only the workspace package can render a workspace. Error responses are still
// written here, and are returned as the error value.
func runCarryPatchSync(cfg SyncAPIConfig, c echo.Context) (*CarryPatchSyncResponse, error) {
	// Auth check.
	auth := apikit.GetAuthInfo(c)
	if auth == nil {
		return nil, apikit.WriteAPIError(c, http.StatusUnauthorized, "authentication required")
	}
	if isPAT(auth) && !hasScope(auth, "workspaces:sync", "workspaces:write") {
		return nil, apikit.WriteAPIError(c, http.StatusForbidden, "missing required scope: workspaces:sync")
	}

	slug := c.Param("slug")
	if !authorizeWorkspace(c, cfg.DB, auth, slug) {
		return nil, echo.ErrNotFound
	}

	// Load workspace record.
	var mode, status, cloneStatus, integrationBranch string
	var upstreamHeadSHA, wsBranch sql.NullString
	err := cfg.DB.QueryRow(
		`SELECT workspace_mode, status, clone_status, integration_branch, upstream_head_sha, branch
		 FROM workspaces WHERE slug = ?`, slug,
	).Scan(&mode, &status, &cloneStatus, &integrationBranch, &upstreamHeadSHA, &wsBranch)
	if err == sql.ErrNoRows {
		return nil, apikit.WriteAPIError(c, http.StatusNotFound, "workspace not found")
	}
	if err != nil {
		return nil, apikit.WriteAPIError(c, http.StatusInternalServerError, "database error")
	}

	// 16-REQ-5.E4: a standard workspace uses standard sync behaviour without
	// carry-patch extensions. Report "not handled" so the caller falls
	// through to it, rather than substituting a non-standard response body.
	if mode != "carry_patch" {
		return nil, nil
	}

	ctx := c.Request().Context()

	// The fetch and merge detection touch the shared trunk; refuse to run
	// while a rebuild, merge, or other sync holds the workspace.
	unlock, locked := wslock.TryLock(slug)
	if !locked {
		return nil, apikit.WriteAPIErrorWithType(c, http.StatusConflict,
			"another operation is running on this workspace; retry later", "workspace_busy")
	}
	defer unlock()

	// 20-REQ-1.1: Read PATCH_BRANCH_SOURCE and PATCH_DIVERGENCE_POLICY
	// through GetVariable on every sync, after the lock is taken and
	// before any fetch.
	patchSource := "hub"
	patchDivergencePolicy := "replace"
	if cfg.GetVariable != nil {
		val, _ := cfg.GetVariable("workspace", slug, "PATCH_BRANCH_SOURCE")
		// 20-REQ-1.2 / 20-REQ-1.3: only the exact string "origin" selects origin.
		if val == "origin" {
			patchSource = "origin"
		}
		policyVal, _ := cfg.GetVariable("workspace", slug, "PATCH_DIVERGENCE_POLICY")
		// 20-REQ-1.4 / 20-REQ-1.5: only the exact string "report" selects report.
		if policyVal == "report" {
			patchDivergencePolicy = "report"
		}
	}

	isOriginMode := patchSource == "origin"

	// 20-REQ-2.6: If origin mode and FetchOrigin is nil, abort.
	if isOriginMode && cfg.FetchOrigin == nil {
		return nil, apikit.WriteAPIError(c, http.StatusInternalServerError, "origin fetch is not configured")
	}

	// 20-REQ-2.2: Origin credential resolution happens before either fetch.
	var originAuth transport.AuthMethod
	if isOriginMode {
		if cfg.ResolveOriginAuth != nil {
			resolved, authErr := cfg.ResolveOriginAuth(slug)
			if authErr != nil {
				// 20-REQ-2.3: credential failure aborts before any fetch.
				return nil, apikit.WriteAPIError(c, http.StatusBadGateway, "failed to resolve origin credentials")
			}
			originAuth = resolved
		}
	}

	// 16-REQ-5.1: Resolve upstream credentials via resolveUpstreamAuth.
	var upstreamAuth transport.AuthMethod
	if cfg.ResolveAuth != nil {
		resolved, authErr := cfg.ResolveAuth(slug)
		if authErr != nil {
			// 16-REQ-5.E1: auth failure aborts sync; no state modified.
			return nil, apikit.WriteAPIError(c, http.StatusBadGateway, "failed to resolve upstream credentials")
		}
		upstreamAuth = resolved
	}

	// Determine repo path and create git runner.
	repoPath := filepath.Join(cfg.WorkspaceRoot, slug, "trunk")
	git, gitErr := cfg.NewGitRunner(repoPath)
	if gitErr != nil {
		return nil, apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to create git runner")
	}

	// 16-REQ-5.1: Fetch from the 'upstream' remote (not 'origin').
	if cfg.Fetch != nil {
		if fetchErr := cfg.Fetch(ctx, repoPath, upstreamAuth); fetchErr != nil {
			// 16-REQ-5.E1 / 16-ERR-8: fetch failure aborts sync;
			// upstream_tracking_ref and patch statuses are not modified.
			return nil, apikit.WriteAPIError(c, http.StatusBadGateway, "upstream fetch failed")
		}
	}

	// 20-REQ-2.1 / 20-REQ-2.4: Fetch origin (origin mode only).
	if isOriginMode {
		if fetchErr := cfg.FetchOrigin(ctx, repoPath, originAuth); fetchErr != nil {
			// 20-REQ-2.4: origin fetch failure leaves everything unchanged.
			return nil, apikit.WriteAPIErrorWithType(c, http.StatusBadGateway,
				"origin fetch failed", "origin_fetch_failed")
		}
	}

	// Resolve the new upstream HEAD after fetch (the upstream default branch;
	// see resolveUpstreamBase).
	newUpstreamHead, err := resolveUpstreamBase(ctx, git, wsBranch.String)
	if err != nil {
		return nil, apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to resolve upstream HEAD")
	}

	// Compare with stored upstream_head_sha.
	storedSHA := ""
	if upstreamHeadSHA.Valid {
		storedSHA = upstreamHeadSHA.String
	}
	upstreamAdvanced := newUpstreamHead != storedSHA

	// Detect upstream force-push via ancestry check.
	// If the stored SHA is non-empty and the new upstream HEAD is not
	// a descendant of it, the upstream has been force-pushed (history
	// rewrite). The flag is informational — sync still proceeds.
	forcePushDetected := false
	if upstreamAdvanced && storedSHA != "" {
		isAnc, ancErr := git.IsAncestor(ctx, storedSHA, newUpstreamHead)
		if ancErr == nil && !isAnc {
			forcePushDetected = true
		}
		// If IsAncestor errors, conservatively leave forcePushDetected
		// as false to avoid false alarms (NS-REQ-5).
	}

	// Prepare response.
	resp := CarryPatchSyncResponse{
		PatchesMerged:     make([]string, 0),
		RebuildTriggered:  false,
		ForcePushDetected: forcePushDetected,
		OriginFetched:     isOriginMode,
	}
	// 20-REQ-6: In origin mode, patches_synced and patches_diverged are
	// always present (as [] when empty). In hub mode they are nil/omitted.
	if isOriginMode {
		resp.PatchesSynced = make([]PatchSyncedElement, 0)
		resp.PatchesDiverged = make([]string, 0)
	}

	// ===========================================================
	// 20-REQ-5.1: Patch refresh runs before the early return, so a
	// patch-only change reaches merge detection and the rebuild enqueue.
	// ===========================================================

	// List patches for both the refresh and merge detection.
	patches, listErr := cfg.PatchStore.ListPatches(ctx, slug)
	if listErr != nil {
		return nil, apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to list patches")
	}

	// 20-REQ-3 / 20-REQ-4: Patch-branch refresh (origin mode only).
	patchAdvanced := false
	var outcomes []PatchRefreshOutcome
	if isOriginMode {
		var advanced bool
		var refreshErr error
		outcomes, advanced, refreshErr = refreshPatchBranches(ctx, git, repoPath, patches, integrationBranch, patchDivergencePolicy)
		patchAdvanced = advanced

		// 20-REQ-7.2: Persist per-branch outcomes produced so far, even on
		// the ref-write failure path.
		now := apikit.NowUTC()
		persistPatchOutcomes(ctx, cfg.PatchStore, outcomes, now)

		// 20-REQ-7.2: Clear origin columns of merged_upstream and deleted rows.
		clearMergedDeletedOriginState(ctx, cfg.PatchStore, slug)

		// 20-REQ-6: Build patches_synced and patches_diverged from outcomes.
		resp.PatchesSynced = outcomesToSyncedElements(outcomes)
		resp.PatchesDiverged = outcomesToDiverged(outcomes)

		if refreshErr != nil {
			// 20-REQ-4.3: A ref-write failure stops the refresh. Outcomes
			// already produced are kept. The rebuild decision for branches
			// already moved still runs below. upstream_head_sha and
			// last_sync_at are NOT written.
			// Enqueue rebuild for branches already moved, then return 500.
			if patchAdvanced {
				enqueueRebuildIfNeeded(cfg, slug, integrationBranch, auth.UserID, true)
			}
			return nil, apikit.WriteAPIError(c, http.StatusInternalServerError,
				fmt.Sprintf("failed to update patch branch %s", refreshErr.(*RefWriteError).Branch))
		}
	}

	// 20-REQ-7.3: In hub mode, clear origin columns of every patch row.
	if !isOriginMode {
		if clearErr := cfg.PatchStore.ClearOriginSyncState(ctx, slug); clearErr != nil {
			// Log but don't fail the sync.
			_ = clearErr
		}
	}

	// 20-REQ-5.2: Merge detection runs when upstream advanced or any patch
	// branch counted as advanced. In hub mode, run it exactly when upstream
	// advanced (patchAdvanced is always false in hub mode).
	shouldRunMergeDetection := upstreamAdvanced || patchAdvanced

	if shouldRunMergeDetection {
		// Re-list patches to get refreshed state (patches may have been
		// updated by the refresh). For hub mode, the original list is fine.
		if isOriginMode {
			patches, listErr = cfg.PatchStore.ListPatches(ctx, slug)
			if listErr != nil {
				return nil, apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to list patches")
			}
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
	}

	// 20-REQ-5.6: When neither upstream nor any patch changed, return
	// empty patches_merged and rebuild_triggered false.
	newlyMerged := len(resp.PatchesMerged)
	shouldRebuild := upstreamAdvanced || patchAdvanced || newlyMerged > 0

	// 20-REQ-5.5: Write last_sync_at and updated_at on every completed sync.
	// upstream_head_sha is written only when upstream advanced.
	now := apikit.NowUTC()
	if upstreamAdvanced {
		_, err = cfg.DB.Exec(
			`UPDATE workspaces SET upstream_head_sha = ?, last_sync_at = ?, updated_at = ? WHERE slug = ?`,
			newUpstreamHead, now, now, slug,
		)
	} else {
		_, err = cfg.DB.Exec(
			`UPDATE workspaces SET last_sync_at = ?, updated_at = ? WHERE slug = ?`,
			now, now, slug,
		)
	}
	if err != nil {
		return nil, apikit.WriteAPIError(c, http.StatusInternalServerError, "failed to update workspace")
	}

	// ===========================================================
	// 20-REQ-5.3 / 20-REQ-5.4: Auto-rebuild trigger logic
	// ===========================================================

	if shouldRebuild {
		autoRebuild := true // default when unset (16-REQ-5.3)
		if cfg.GetVariable != nil {
			val, _ := cfg.GetVariable("workspace", slug, "AUTO_REBUILD_AFTER_SYNC")
			if val == "false" {
				// 16-REQ-5.4 / 20-REQ-5.4: explicitly disabled.
				autoRebuild = false
			}
		}

		if autoRebuild {
			jobID, triggered := enqueueRebuildIfNeeded(cfg, slug, integrationBranch, auth.UserID, false)
			if triggered {
				resp.RebuildTriggered = true
				resp.RebuildJobID = &jobID
			}
			// 16-REQ-5.3 / 16-PROP-7: if duplicate key is already queued or
			// running, silently ignore — rebuild_triggered stays false.
		}
	}

	return &resp, nil
}

// enqueueRebuildIfNeeded enqueues a rebuild job for the workspace. It returns
// the job ID and whether the job was actually enqueued (not deduplicated).
// When ignoreResult is true, the return values are not meaningful (used on
// the ref-write failure path where we fire-and-forget).
func enqueueRebuildIfNeeded(cfg SyncAPIConfig, slug, integrationBranch, userID string, ignoreResult bool) (string, bool) {
	payload := BuildRebuildPayload(slug, integrationBranch, userID, cfg.GetVariable, "", "")
	payloadJSON, _ := json.Marshal(payload)
	groupKey := slug + ":" + integrationBranch
	nonce := uuid.New().String()

	jobID, duplicate, enqErr := cfg.Queue.Enqueue(jobqueue.EnqueueParams{
		Type:        "rebuild",
		Key:         slug,
		Nonce:       nonce,
		Payload:     payloadJSON,
		SubmittedBy: userID,
		Group:       groupKey,
	})

	if enqErr == nil && !duplicate {
		return jobID, true
	}
	return "", false
}

// handleCarryPatchSyncEndpoint adapts runCarryPatchSync to an echo.HandlerFunc
// for RegisterSyncRoutes, which mounts the carry-patch sync logic as a
// standalone route.
//
// The server binary does not use this route: it registers the workspace sync
// endpoint and reaches the carry-patch logic through NewCarryPatchSyncHook, so
// that the response carries the standard sync fields as well (16-REQ-5.1).
// Mounted on its own, only the carry-patch fields are available, because the
// workspace record is rendered by the workspace package.
func handleCarryPatchSyncEndpoint(cfg SyncAPIConfig) echo.HandlerFunc {
	return func(c echo.Context) error {
		resp, err := runCarryPatchSync(cfg, c)
		if err != nil {
			return err
		}
		if resp == nil {
			// 16-REQ-5.E4: standard workspace. Without the workspace package
			// there is no standard sync response to fall through to, so
			// acknowledge the sync without carry-patch fields.
			return c.JSON(http.StatusOK, map[string]string{"status": "synced"})
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

	// Scan each commit subject for GitHub's squash-merge suffix. Only a
	// trailing "(#NNN)" counts: a subject that merely mentions the PR (for
	// example `Revert "Feature (#42)" (#57)`) must not mark #42 as merged.
	target := fmt.Sprintf("(#%s)", prNumber)
	for _, line := range strings.Split(logOutput, "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), target) {
			return true
		}
	}
	return false
}

// ===========================================================================
// Origin sync state persistence helpers
// ===========================================================================

// persistPatchOutcomes writes the origin sync state for each outcome through
// the PatchStore. For missing_on_origin, origin_sha is NULL.
func persistPatchOutcomes(ctx context.Context, store PatchStore, outcomes []PatchRefreshOutcome, syncedAt string) {
	for _, o := range outcomes {
		var sha *string
		if o.OriginSHA != "" {
			s := o.OriginSHA
			sha = &s
		}
		_ = store.SetOriginSyncState(ctx, o.PatchID, o.State, sha, syncedAt)
	}
}

// clearMergedDeletedOriginState clears the origin sync columns of patches
// whose status is merged_upstream or deleted. Since ListPatches excludes
// deleted rows, this function uses a direct DB query through the PatchStore's
// ClearOriginSyncStateForMergedDeleted method.
func clearMergedDeletedOriginState(ctx context.Context, store PatchStore, slug string) {
	_ = store.ClearOriginSyncStateForMergedDeleted(ctx, slug)
}

// outcomesToSyncedElements converts PatchRefreshOutcome slices to the
// response elements.
func outcomesToSyncedElements(outcomes []PatchRefreshOutcome) []PatchSyncedElement {
	elems := make([]PatchSyncedElement, 0, len(outcomes))
	for _, o := range outcomes {
		elem := PatchSyncedElement{
			BranchName: o.BranchName,
			Action:     o.Action,
			State:      o.State,
		}
		if o.LocalSHA != "" {
			elem.LocalSHA = o.LocalSHA
		}
		if o.OriginSHA != "" {
			elem.OriginSHA = o.OriginSHA
		}
		if o.ReplacedSHA != "" {
			elem.ReplacedSHA = o.ReplacedSHA
		}
		elems = append(elems, elem)
	}
	return elems
}

// outcomesToDiverged returns the branch names of outcomes whose state is
// diverged.
func outcomesToDiverged(outcomes []PatchRefreshOutcome) []string {
	diverged := make([]string, 0)
	for _, o := range outcomes {
		if o.State == StateDiverged {
			diverged = append(diverged, o.BranchName)
		}
	}
	return diverged
}
