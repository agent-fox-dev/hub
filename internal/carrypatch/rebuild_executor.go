package carrypatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// ===========================================================================
// Internal sentinel errors and types for the rebuild executor
// ===========================================================================

// errPatchBranchNotFound indicates a patch branch does not exist in the
// repository. The rebuild executor skips the patch (16-REQ-1.6).
var errPatchBranchNotFound = errors.New("patch branch not found")

// rebuildConflictError wraps unresolved conflict file paths for fail-fast
// reporting (16-REQ-1.5).
type rebuildConflictError struct {
	files []string
}

func (e *rebuildConflictError) Error() string {
	return "unresolved conflict: " + strings.Join(e.files, ", ")
}

// ===========================================================================
// HandleRebuildJob — main rebuild algorithm
// ===========================================================================

// HandleRebuildJob executes the rebuild algorithm.
//
// The algorithm:
//  1. Parse payload and resolve upstream auth.
//  2. Fetch from the upstream remote.
//  3. Resolve upstream HEAD and create a temporary branch at that commit.
//  4. Collect patches in position order and apply each one using the
//     captured strategy (rebase or merge).
//  5. On success: force-update the integration branch ref, delete the
//     temporary branch, remove merged_upstream patches, and compact positions.
//  6. On conflict: abort, mark the conflicting patch, delete the temporary
//     branch, and return a non-retryable error.
//
// Returns (result, retryable, error).
func (h *RebuildHandler) HandleRebuildJob(ctx context.Context, rawPayload json.RawMessage) (retResult any, retRetryable bool, retErr error) {
	// 1. Parse payload.
	var payload RebuildPayload
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		return nil, false, fmt.Errorf("invalid rebuild payload: %w", err)
	}

	// 18-REQ-3.5: Emit hub.rebuild.fail on any error return from this function.
	defer func() {
		if retErr != nil {
			h.emitRebuildAudit(ctx, payload.WorkspaceSlug, "hub.rebuild.fail", map[string]any{
				"reason": retErr.Error(),
			})
		}
	}()

	// 2. Resolve upstream auth (16-REQ-1.2, 16-REQ-1.E9).
	if h.ResolveAuth != nil {
		if err := h.ResolveAuth(payload.WorkspaceSlug); err != nil {
			return nil, true, &TransientError{Err: err}
		}
	}

	// 3. Determine repo path.
	repoPath := payload.WorkspaceSlug
	if h.WorkspaceRoot != "" {
		repoPath = filepath.Join(h.WorkspaceRoot, payload.WorkspaceSlug, "trunk")
	}

	// 4. Fetch from upstream (16-REQ-1.2, 16-REQ-1.E5).
	if h.Fetch != nil {
		if err := h.Fetch(ctx, repoPath); err != nil {
			var te *TransientError
			if errors.As(err, &te) {
				return nil, true, err
			}
			return nil, true, &TransientError{Err: err}
		}
	}

	// 5. Create GitRunner for the workspace repo.
	git, err := h.NewGitRunner(repoPath)
	if err != nil {
		return nil, true, &TransientError{Err: err}
	}

	// 6. Resolve upstream HEAD via FETCH_HEAD (16-REQ-1.2).
	// FETCH_HEAD points to the tip of the most recently fetched branch,
	// which is the correct upstream reference after the fetch in step 4.
	// Using HEAD here would resolve to whatever branch is checked out,
	// which may differ from the fetched upstream tip.
	upstreamHead, err := git.Run(ctx, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return nil, true, &TransientError{Err: err}
	}

	// 7. Create temporary branch at upstream HEAD.
	const tempBranch = "_rebuild_temp"
	if _, err := git.Run(ctx, "checkout", "-b", tempBranch, upstreamHead); err != nil {
		return nil, true, &TransientError{Err: err}
	}

	// cleanupTempBranch deletes the temporary branch. Called on both success
	// and failure to satisfy 16-PROP-9.
	cleanupTempBranch := func() {
		_, _ = git.Run(ctx, "checkout", "--detach")
		_, _ = git.Run(ctx, "branch", "-D", tempBranch)
	}

	// 8. Configure rerere for conflict resolution replay (16-REQ-1.5).
	_, _ = git.Run(ctx, "config", "rerere.enabled", "true")
	_, _ = git.Run(ctx, "config", "rerere.autoupdate", "true")

	// 9. List all patches from the patch store.
	patches, err := h.PatchStore.ListPatches(ctx, payload.WorkspaceSlug)
	if err != nil {
		cleanupTempBranch()
		return nil, true, &TransientError{Err: err}
	}

	// 10. Sort patches by position (16-REQ-1.2: position order).
	sort.Slice(patches, func(i, j int) bool {
		return patches[i].Position < patches[j].Position
	})

	// 11. Determine fail mode.
	failMode := payload.FailMode
	if failMode == "" {
		failMode = FailModeFailFast
	}

	// 12. Build the result and process each patch.
	result := &RebuildResult{
		UpstreamHeadSHA: upstreamHead,
		Strategy:        payload.Strategy,
		FailMode:        failMode,
		PatchResults:    make([]PatchResult, 0, len(patches)),
	}

	var mergedPatchIDs []string

	// Extract the job ID from context for progress updates.
	jobID := jobqueue.JobIDFromContext(ctx)

	for _, patch := range patches {
		pr := PatchResult{
			PatchID:    patch.ID,
			BranchName: patch.BranchName,
			Position:   patch.Position,
		}

		// 16-REQ-1.7: skip merged_upstream, disabled, and deleted patches.
		if patch.Status == PatchStatusMergedUpstream || patch.Status == PatchStatusDisabled || patch.Status == PatchStatusDeleted {
			pr.Status = "skipped"
			pr.SkippedReason = patch.Status // "merged_upstream", "disabled", or "deleted"
			result.PatchResults = append(result.PatchResults, pr)
			result.PatchesSkipped++
			if patch.Status == PatchStatusMergedUpstream {
				mergedPatchIDs = append(mergedPatchIDs, patch.ID)
			}
			// Write progress after each patch completes (NS-REQ-4).
			h.writeProgress(jobID, result.PatchResults)
			continue
		}

		// Capture the pre-patch HEAD for continue-mode rollback.
		prePatchHead, headErr := git.Run(ctx, "rev-parse", "HEAD")
		if headErr != nil {
			cleanupTempBranch()
			return nil, true, &TransientError{Err: headErr}
		}

		// Apply the patch using the configured strategy.
		strategy := payload.Strategy
		if strategy == "" {
			strategy = StrategyRebase
		}

		var applyErr error
		if strategy == StrategyMerge {
			applyErr = h.applyMergePatch(ctx, git, patch.BranchName)
		} else {
			applyErr = h.applyRebasePatch(ctx, git, patch.BranchName, upstreamHead)
		}

		if applyErr != nil {
			// 16-REQ-1.6: branch not found -> skip.
			if errors.Is(applyErr, errPatchBranchNotFound) {
				pr.Status = "skipped"
				pr.SkippedReason = "branch_not_found"
				result.PatchResults = append(result.PatchResults, pr)
				result.PatchesSkipped++
				// Write progress after each patch completes (NS-REQ-4).
				h.writeProgress(jobID, result.PatchResults)
				continue
			}

			// 16-REQ-1.5 / NS-REQ-1: unresolved conflict handling.
			var ce *rebuildConflictError
			if errors.As(applyErr, &ce) {
				_ = h.PatchStore.UpdatePatchStatus(ctx, patch.ID, PatchStatusConflict, ce.files)

				if failMode == FailModeContinue {
					// Continue mode: record the conflict, reset temp branch
					// to pre-patch state, and continue with next patch.
					pr.Status = "conflict"
					pr.ConflictFiles = ce.files
					result.PatchResults = append(result.PatchResults, pr)
					result.PatchesConflicted++

					// Reset temp branch to pre-patch HEAD so subsequent
					// patches apply cleanly against the last good state.
					if err := git.HardReset(ctx, prePatchHead); err != nil {
						cleanupTempBranch()
						return result, true, &TransientError{Err: err}
					}

					// Write progress after each patch completes (NS-REQ-4).
					h.writeProgress(jobID, result.PatchResults)
					continue
				}

				// Default fail_fast: abort immediately.
				cleanupTempBranch()
				return nil, false, fmt.Errorf("conflict in patch %q: %s",
					patch.BranchName, strings.Join(ce.files, ", "))
			}

			// Unknown / transient error -> cleanup and signal retry.
			cleanupTempBranch()
			return nil, true, &TransientError{Err: applyErr}
		}

		// Get the new HEAD SHA for the successfully applied patch.
		newHead, headErr := git.Run(ctx, "rev-parse", "HEAD")
		if headErr != nil {
			cleanupTempBranch()
			return nil, true, &TransientError{Err: headErr}
		}

		pr.Status = "success"
		pr.NewHeadSHA = &newHead
		result.PatchResults = append(result.PatchResults, pr)
		result.PatchesApplied++

		// Write progress after each patch completes (NS-REQ-4).
		h.writeProgress(jobID, result.PatchResults)
	}

	// === Success path (16-REQ-1.2) ===

	// Get final integration HEAD SHA.
	finalHead, err := git.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		cleanupTempBranch()
		return nil, true, &TransientError{Err: err}
	}
	result.IntegrationHeadSHA = finalHead

	// Force-update integration branch ref to the temporary branch HEAD.
	integrationBranch := payload.IntegrationBranch
	if integrationBranch == "" {
		integrationBranch = "integration"
	}

	// Capture the previous integration branch HEAD before force-updating.
	// If the branch does not exist yet (first rebuild), previousHead will be
	// empty and PreviousIntegrationHeadSHA stays at its zero value.
	previousHead, _ := git.Run(ctx, "rev-parse", "--verify", integrationBranch)
	if previousHead != "" {
		result.PreviousIntegrationHeadSHA = previousHead
	}

	if _, err := git.Run(ctx, "branch", "-f", integrationBranch, "HEAD"); err != nil {
		cleanupTempBranch()
		return nil, true, &TransientError{Err: err}
	}

	// Delete temporary branch (16-PROP-9).
	cleanupTempBranch()

	// Soft-delete merged_upstream patches (set status='deleted', deleted_at).
	for _, id := range mergedPatchIDs {
		_ = h.PatchStore.SoftDeletePatch(ctx, id)
	}
	result.PatchesRemoved = len(mergedPatchIDs)

	// Compact remaining positions to be contiguous.
	_ = h.PatchStore.CompactPositions(ctx, payload.WorkspaceSlug)

	// 18-REQ-3.4: Emit hub.rebuild.complete audit event.
	h.emitRebuildAudit(ctx, payload.WorkspaceSlug, "hub.rebuild.complete", map[string]any{
		"patches_applied": result.PatchesApplied,
	})

	return result, false, nil
}

// writeProgress writes the current patch results to the job's progress column.
// It is a best-effort operation: errors are logged but do not fail the rebuild.
func (h *RebuildHandler) writeProgress(jobID string, patchResults []PatchResult) {
	if h.Queue == nil || jobID == "" {
		return
	}
	_ = h.Queue.UpdateProgress(jobID, patchResults)
}

// ===========================================================================
// Patch application strategies
// ===========================================================================

// applyRebasePatch applies a patch using the rebase (cherry-pick) strategy.
//
// For each unique commit on the patch branch (determined via git log --reverse),
// cherry-picks it onto the current temporary branch. If the branch does not
// exist, returns errPatchBranchNotFound. If an unresolvable conflict occurs,
// returns *rebuildConflictError.
func (h *RebuildHandler) applyRebasePatch(ctx context.Context, git GitRunner, branchName, upstreamHead string) error {
	// 16-REQ-1.3: determine unique commits via git log --reverse.
	logOutput, err := git.Run(ctx, "log", "--reverse", "--format=%H",
		upstreamHead+".."+branchName)
	if err != nil {
		// Branch doesn't exist or is not valid.
		return errPatchBranchNotFound
	}

	commits := splitNonEmpty(logOutput)
	if len(commits) == 0 {
		return errPatchBranchNotFound
	}

	// Cherry-pick each commit in order.
	for _, commit := range commits {
		if err := git.CherryPick(ctx, commit); err != nil {
			var cpErr *CherryPickConflictError
			if errors.As(err, &cpErr) {
				// Attempt rerere resolution (16-REQ-1.5).
				if conflictErr := h.handleConflictWithRerere(ctx, git, "cherry-pick"); conflictErr != nil {
					return conflictErr
				}
				// Rerere resolved all conflicts; continue to the next commit.
				continue
			}
			return err
		}
	}

	return nil
}

// applyMergePatch applies a patch using the merge (--no-ff) strategy.
//
// Merges the patch branch into the current temporary branch with --no-ff.
// If the branch does not exist, returns errPatchBranchNotFound. If an
// unresolvable conflict occurs, returns *rebuildConflictError.
func (h *RebuildHandler) applyMergePatch(ctx context.Context, git GitRunner, branchName string) error {
	// Check if the branch exists before attempting merge.
	if _, err := git.Run(ctx, "rev-parse", "--verify", branchName); err != nil {
		return errPatchBranchNotFound
	}

	// 16-REQ-1.4: merge with --no-ff.
	if err := git.MergeNoFF(ctx, branchName); err != nil {
		var mergeErr *MergeNoFFConflictError
		if errors.As(err, &mergeErr) {
			// Attempt rerere resolution (16-REQ-1.5).
			if conflictErr := h.handleConflictWithRerere(ctx, git, "merge"); conflictErr != nil {
				return conflictErr
			}
			// All resolved — merge commit was finalized via git commit --no-edit.
			return nil
		}
		return err
	}

	return nil
}

// ===========================================================================
// Conflict resolution with rerere
// ===========================================================================

// handleConflictWithRerere attempts to resolve a conflict using git rerere.
//
// Flow:
//  1. Run 'git rerere' to replay any recorded resolutions (with autoupdate
//     enabled, resolved files are automatically staged).
//  2. Run 'git diff --name-only --diff-filter=U' to check for remaining
//     unresolved conflicts.
//  3. If unresolved remain: abort the operation and return *rebuildConflictError.
//  4. If all resolved: continue/commit the operation and return nil.
func (h *RebuildHandler) handleConflictWithRerere(ctx context.Context, git GitRunner, operation string) *rebuildConflictError {
	// 16-REQ-1.5: allow git rerere with autoupdate to stage resolved files.
	_, _ = git.Run(ctx, "rerere")

	// Check for remaining unresolved conflicts via diff filter.
	diffOutput, _ := git.Run(ctx, "diff", "--name-only", "--diff-filter=U")
	unresolvedFiles := splitNonEmpty(diffOutput)

	if len(unresolvedFiles) > 0 {
		// Unresolved conflicts remain: abort the operation and report.
		_, _ = git.Run(ctx, operation, "--abort")
		return &rebuildConflictError{files: unresolvedFiles}
	}

	// All conflicts resolved by rerere: continue the operation.
	if operation == "cherry-pick" {
		_, _ = git.Run(ctx, "cherry-pick", "--continue")
	} else {
		// For merge: finalize with git commit.
		_, _ = git.Run(ctx, "commit", "--no-edit")
	}

	return nil
}

// ===========================================================================
// String utilities
// ===========================================================================

// splitNonEmpty splits a string by newlines and returns non-empty,
// whitespace-trimmed lines.
func splitNonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}
