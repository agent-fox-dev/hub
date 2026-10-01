package carrypatch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
	"github.com/agent-fox-dev/hub/internal/wslock"
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
//  2. Take the workspace lock and set the rebuild-active guard. Phase one,
//     under the lock: fetch from the upstream remote, resolve the upstream
//     base commit, migrate a legacy _rebuild_temp, remove stale rebuild
//     worktrees and configure rerere; then release the lock.
//  3. Create a detached per-run worktree at the upstream base
//     (<workspace_root>/<slug>/rebuild/<job id>). The trunk checkout is never
//     moved or modified.
//  4. Collect patches in position order, snapshot each attempted patch's
//     tip as a SHA (refs/heads/<branch>^{commit}, recorded as source_sha)
//     and apply each one inside the worktree using the captured strategy
//     (rebase or merge). Only the SHAs are used from then on.
//  5. On success, phase two (lock re-acquired): force-update the
//     integration branch ref with update-ref, remove merged_upstream
//     patches, compact positions and optionally push to origin. Then the
//     lock is released, the worktree removed and the guard ended.
//  6. On conflict: abort, mark the conflicting patch and return a
//     non-retryable error.
//  7. On every exit path the worktree is removed with a non-cancelled
//     context, so cleanup still runs after the job context is cancelled.
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
			// The job context may already be cancelled; the event must still
			// be recorded.
			h.emitRebuildAudit(context.WithoutCancel(ctx), payload.WorkspaceSlug, "hub.rebuild.fail", map[string]any{
				"reason": retErr.Error(),
			})
		}
	}()

	// 2. Resolve upstream auth (16-REQ-1.2, 16-REQ-1.E9).
	var auth transport.AuthMethod
	if h.ResolveAuth != nil {
		resolved, err := h.ResolveAuth(payload.WorkspaceSlug)
		if err != nil {
			return nil, true, &TransientError{Err: err}
		}
		auth = resolved
	}

	// 3. Determine repo path.
	repoPath := payload.WorkspaceSlug
	if h.WorkspaceRoot != "" {
		repoPath = filepath.Join(h.WorkspaceRoot, payload.WorkspaceSlug, "trunk")
	}

	slug := payload.WorkspaceSlug

	// acquire takes the blocking workspace lock and returns a release
	// function that is safe to call more than once, so every early return
	// releases the lock exactly once (01-REQ-4.6).
	acquire := func() func() {
		release := wslock.Lock(slug)
		var once sync.Once
		return func() { once.Do(release) }
	}

	// Phase one runs under the workspace lock (01-REQ-4.1, 4.3): the
	// blocking Lock, then, still under it, the rebuild-active guard so that
	// archive and reclone cannot slip in between. The guard is ended by a
	// defer registered before the worktree removal defer below, so it is
	// cleared after the worktree has been removed on every exit path
	// (01-REQ-5.4).
	unlock := acquire()
	defer unlock()
	endGuard, guarded := wslock.BeginRebuild(slug)
	defer endGuard()
	if !guarded {
		return nil, true, &TransientError{Err: fmt.Errorf("a rebuild is already active for workspace %q", slug)}
	}

	// 4. Fetch from upstream (16-REQ-1.2, 16-REQ-1.E5).
	if h.Fetch != nil {
		if err := h.Fetch(ctx, repoPath, auth); err != nil {
			var te *TransientError
			if errors.As(err, &te) {
				return nil, true, err
			}
			return nil, true, &TransientError{Err: err}
		}
	}

	// 5. Create the GitRunner for the trunk. It is used only for reads,
	// config, worktree management and the final ref update.
	trunk, err := h.NewGitRunner(repoPath)
	if err != nil {
		return nil, true, &TransientError{Err: err}
	}

	// 6. Resolve the upstream base: the upstream default branch recorded by
	// the fetch (refs/remotes/upstream/HEAD), falling back to the workspace
	// branch tracking ref and finally FETCH_HEAD (16-REQ-1.2).
	upstreamHead, err := resolveUpstreamBase(ctx, trunk, workspaceBranch(h.DB, payload.WorkspaceSlug))
	if err != nil {
		return nil, true, &TransientError{Err: err}
	}

	// 7. One-time migration of a legacy _rebuild_temp branch (01-REQ-2.6, 2.7).
	h.migrateLegacyRebuildBranch(ctx, trunk, slug)

	// 8. Remove worktrees left behind by a crashed run and prune their
	// registrations, before this run creates its own (01-REQ-8.2).
	h.cleanStaleRebuildDirs(ctx, trunk, slug, repoPath)

	// 9. Configure rerere (repository level, shared with every worktree) for
	// conflict resolution replay (16-REQ-1.5, 01-REQ-3.1). This happens
	// before the worktree is created.
	_, _ = trunk.Run(ctx, "config", "rerere.enabled", "true")
	_, _ = trunk.Run(ctx, "config", "rerere.autoupdate", "true")

	// End of phase one: worktree creation and patch application hold no
	// lock (01-REQ-4.4).
	unlock()

	// 10. Create the detached per-run worktree at the upstream base
	// (01-REQ-1.1, 01-REQ-1.2).
	jobID := jobqueue.JobIDFromContext(ctx)
	worktreePath, err := h.rebuildWorktreePath(payload.WorkspaceSlug, repoPath, jobID)
	if err != nil {
		return nil, true, &TransientError{Err: err}
	}
	if err := trunk.WorktreeAdd(ctx, worktreePath, upstreamHead); err != nil {
		h.discardFailedWorktree(ctx, trunk, payload.WorkspaceSlug, worktreePath)
		return nil, true, &TransientError{Err: fmt.Errorf("create rebuild worktree: %w", err)}
	}
	h.logInfo("rebuild worktree created", "slug", payload.WorkspaceSlug, "path", worktreePath)
	// Removed on every exit path (success, conflict, error, cancellation).
	// On success it is removed explicitly after phase two; this defer covers
	// the other paths and runs after the phase-two unlock defer, so removal
	// never happens under the lock.
	var removeOnce sync.Once
	removeWT := func() {
		removeOnce.Do(func() { h.removeWorktree(ctx, trunk, slug, worktreePath) })
	}
	defer removeWT()

	// Patch application runs through a runner rooted in the worktree; it is
	// built after the directory exists (01-REQ-1.3).
	wt, err := h.NewGitRunner(worktreePath)
	if err != nil {
		return nil, true, &TransientError{Err: err}
	}

	// cancelled reports whether err (or the job context) signals
	// cancellation, in which case the run ends as a retryable failure
	// instead of being recorded as a skip, conflict or success (01-REQ-8.1).
	cancelled := func(err error) bool {
		return ctx.Err() != nil || isContextErr(err)
	}

	// 9. List all patches from the patch store.
	patches, err := h.PatchStore.ListPatches(ctx, payload.WorkspaceSlug)
	if err != nil {
		return nil, true, &TransientError{Err: err}
	}

	// 10. Sort patches by position (16-REQ-1.2: position order).
	sort.Slice(patches, func(i, j int) bool {
		return patches[i].Position < patches[j].Position
	})

	// Snapshot the tip of every patch that will be attempted as a SHA
	// (01-REQ-6.1). With the lock released, branch names can move; from here
	// on only the SHAs are used. An unresolvable branch gets no snapshot and
	// is recorded as skipped / branch_not_found when its turn comes.
	snapshots := make(map[string]string, len(patches))
	for _, patch := range patches {
		if patch.Status == PatchStatusMergedUpstream || patch.Status == PatchStatusDisabled || patch.Status == PatchStatusDeleted {
			continue
		}
		sha, snapErr := trunk.Run(ctx, "rev-parse", "--verify", "refs/heads/"+patch.BranchName+"^{commit}")
		if snapErr != nil {
			if cancelled(snapErr) {
				return nil, true, &TransientError{Err: snapErr}
			}
			continue
		}
		if sha = strings.TrimSpace(sha); sha != "" {
			snapshots[patch.ID] = sha
		}
	}

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

		// A branch that did not resolve is skipped (16-REQ-1.6, 01-REQ-6.4).
		sourceSHA, snapshotted := snapshots[patch.ID]
		if !snapshotted {
			pr.Status = "skipped"
			pr.SkippedReason = "branch_not_found"
			result.PatchResults = append(result.PatchResults, pr)
			result.PatchesSkipped++
			h.writeProgress(jobID, result.PatchResults)
			continue
		}
		pr.SourceSHA = sourceSHA

		// Capture the pre-patch HEAD for continue-mode rollback.
		prePatchHead, headErr := wt.Run(ctx, "rev-parse", "HEAD")
		if headErr != nil {
			return nil, true, &TransientError{Err: headErr}
		}

		// Apply the patch using the configured strategy.
		strategy := payload.Strategy
		if strategy == "" {
			strategy = StrategyRebase
		}

		var applyErr error
		if strategy == StrategyMerge {
			applyErr = h.applyMergePatch(ctx, wt, sourceSHA, patch.BranchName)
		} else {
			applyErr = h.applyRebasePatch(ctx, wt, sourceSHA, upstreamHead)
		}

		if applyErr != nil {
			// Cancellation (or a context error from a git call) is never a
			// skip, conflict or success: discard the run and retry later.
			if cancelled(applyErr) {
				return nil, true, &TransientError{Err: applyErr}
			}

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

					// Reset the worktree to the pre-patch HEAD so subsequent
					// patches apply cleanly against the last good state.
					if err := wt.HardReset(ctx, prePatchHead); err != nil {
						return result, true, &TransientError{Err: err}
					}

					// Write progress after each patch completes (NS-REQ-4).
					h.writeProgress(jobID, result.PatchResults)
					continue
				}

				// Default fail_fast: abort immediately.
				return nil, false, fmt.Errorf("conflict in patch %q: %s",
					patch.BranchName, strings.Join(ce.files, ", "))
			}

			// Unknown / transient error -> signal retry.
			return nil, true, &TransientError{Err: applyErr}
		}

		// Get the new HEAD SHA for the successfully applied patch.
		newHead, headErr := wt.Run(ctx, "rev-parse", "HEAD")
		if headErr != nil {
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

	// A cancellation that arrived after the last git call must not publish
	// a half-finished result.
	if err := ctx.Err(); err != nil {
		return nil, true, &TransientError{Err: err}
	}

	// Get final integration HEAD SHA from the worktree.
	finalHead, err := wt.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return nil, true, &TransientError{Err: err}
	}
	result.IntegrationHeadSHA = finalHead

	integrationBranch := payload.IntegrationBranch
	if integrationBranch == "" {
		integrationBranch = "deploy"
	}

	// Phase two: re-acquire the workspace lock for the integration-ref
	// update, patch bookkeeping and the optional push (01-REQ-4.5). The
	// unlock defer runs before the worktree removal defer, so every early
	// return releases the lock first (01-REQ-4.6).
	unlock2 := acquire()
	defer unlock2()
	if err := ctx.Err(); err != nil {
		return nil, true, &TransientError{Err: err}
	}

	// Force-update integration branch ref to the worktree's final HEAD.

	// Capture the previous integration branch HEAD before force-updating.
	// If the branch does not exist yet (first rebuild), previousHead will be
	// empty and PreviousIntegrationHeadSHA stays at its zero value.
	// The read happens under the phase-two lock, immediately before the
	// update, so a rollback that ran during patch application is what gets
	// recorded (01-REQ-2.4, 01-REQ-4.7).
	integrationRef := "refs/heads/" + integrationBranch
	previousHead, _ := trunk.Run(ctx, "rev-parse", "--verify", integrationRef)
	if previousHead != "" {
		result.PreviousIntegrationHeadSHA = previousHead
	}

	// update-ref (not branch -f, which refuses a checked-out branch). On
	// failure nothing below runs: no soft-deletion, compaction or push
	// (01-REQ-2.8); the deferred unlock releases the lock.
	if err := trunk.UpdateRef(ctx, integrationRef, finalHead); err != nil {
		return nil, true, &TransientError{Err: fmt.Errorf("update %s: %w", integrationRef, err)}
	}

	// Exception A (01-REQ-2.5): moving a ref under a checked-out branch
	// leaves the trunk's index and files at the old commit, so the checkout
	// is brought to the new tip. Only in that case is the trunk reset.
	h.syncTrunkCheckout(ctx, trunk, slug, integrationBranch)

	// Soft-delete merged_upstream patches (set status='deleted', deleted_at).
	for _, id := range mergedPatchIDs {
		_ = h.PatchStore.SoftDeletePatch(ctx, id)
	}
	result.PatchesRemoved = len(mergedPatchIDs)

	// Compact remaining positions to be contiguous.
	_ = h.PatchStore.CompactPositions(ctx, payload.WorkspaceSlug)

	// Opt-in: publish the rebuilt integration branch to the fork so that
	// consumers cloning from origin (rather than from the hub) see it.
	if h.PushIntegration != nil && h.GetVariable != nil {
		if val, err := h.GetVariable("workspace", payload.WorkspaceSlug, "REBUILD_PUSH_INTEGRATION_BRANCH"); err == nil && val == "true" {
			if pushErr := h.PushIntegration(ctx, payload.WorkspaceSlug, repoPath, integrationBranch); pushErr != nil {
				h.logf("rebuild: push of integration branch %q to origin failed for %q: %v",
					integrationBranch, payload.WorkspaceSlug, pushErr)
				h.emitRebuildAudit(ctx, payload.WorkspaceSlug, "hub.rebuild.push_failed", map[string]any{
					"integration_branch": integrationBranch,
					"reason":             pushErr.Error(),
				})
			} else {
				result.IntegrationBranchPushed = true
			}
		}
	}

	// End of phase two (01-REQ-4.8): release the lock, remove the worktree,
	// check whether the run's inputs moved, emit the completion event; the
	// guard is ended last by its defer.
	unlock2()
	removeWT()
	h.checkStaleInputs(ctx, trunk, payload, upstreamHead, result.PatchResults)

	// 18-REQ-3.4: Emit hub.rebuild.complete audit event.
	h.emitRebuildAudit(ctx, payload.WorkspaceSlug, "hub.rebuild.complete", map[string]any{
		"patches_applied": result.PatchesApplied,
	})

	return result, false, nil
}

// logf logs through the handler's logger when one is configured.
func (h *RebuildHandler) logf(format string, args ...any) {
	if h.Logger != nil {
		h.Logger.Warn(fmt.Sprintf(format, args...))
	}
}

// logInfo logs at info level through the handler's logger (nil-safe).
func (h *RebuildHandler) logInfo(msg string, args ...any) {
	if h.Logger != nil {
		h.Logger.Info(msg, args...)
	}
}

// logWarn logs at warning level through the handler's logger (nil-safe).
func (h *RebuildHandler) logWarn(msg string, args ...any) {
	if h.Logger != nil {
		h.Logger.Warn(msg, args...)
	}
}

// isContextErr reports whether err is (or wraps) a context cancellation or
// deadline error.
func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// rebuildCleanupTimeout bounds worktree removal and every other cleanup step.
const rebuildCleanupTimeout = 2 * time.Minute

// cleanupContext returns a context for cleanup that survives cancellation of
// ctx (01-REQ-1.6): the job context is dead when a run is cancelled, and git
// would not start under it.
func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), rebuildCleanupTimeout)
}

// legacyRebuildBranch is the temporary branch pre-upgrade rebuilds checked
// out in the trunk. It is never created any more.
const legacyRebuildBranch = "_rebuild_temp"

// localRefExists reports whether the exact ref exists in the trunk. It uses
// for-each-ref and compares the printed name, so a prefix match or an empty
// answer never counts as existing.
func localRefExists(ctx context.Context, trunk GitRunner, ref string) bool {
	out, err := trunk.Run(ctx, "for-each-ref", "--format=%(refname)", ref)
	return err == nil && strings.TrimSpace(out) == ref
}

// migrateLegacyRebuildBranch is Exception B (01-REQ-2.6, 2.7): the one-time
// removal of a _rebuild_temp branch left by a pre-upgrade run. If the
// trunk's HEAD is on it, any in-progress cherry-pick, merge or rebase is
// aborted (best effort) and HEAD is moved to the workspace branch, else the
// local branch origin/HEAD points to, else detached; only then is the branch
// deleted. It runs in phase one under the lock and never fails the run.
func (h *RebuildHandler) migrateLegacyRebuildBranch(ctx context.Context, trunk GitRunner, slug string) {
	legacyRef := "refs/heads/" + legacyRebuildBranch
	if !localRefExists(ctx, trunk, legacyRef) {
		return
	}

	if head, err := trunk.Run(ctx, "symbolic-ref", "-q", "HEAD"); err == nil && strings.TrimSpace(head) == legacyRef {
		for _, op := range []string{"cherry-pick", "merge", "rebase"} {
			_, _ = trunk.Run(ctx, op, "--abort")
		}
		if target := h.legacyMigrationTarget(ctx, trunk, slug); target != "" {
			if _, err := trunk.Run(ctx, "checkout", "--force", target); err != nil {
				h.logWarn("cannot move trunk off legacy rebuild branch",
					"slug", slug, "branch", legacyRebuildBranch, "target", target, "error", err)
				return
			}
			h.logInfo("moved trunk off legacy rebuild branch", "slug", slug, "branch", legacyRebuildBranch, "target", target)
		} else {
			h.logWarn("no workspace branch or origin/HEAD found locally; detaching trunk HEAD from legacy rebuild branch",
				"slug", slug, "branch", legacyRebuildBranch)
			if _, err := trunk.Run(ctx, "checkout", "--detach", "--force"); err != nil {
				h.logWarn("cannot detach trunk from legacy rebuild branch",
					"slug", slug, "branch", legacyRebuildBranch, "error", err)
				return
			}
		}
	}

	if _, err := trunk.Run(ctx, "branch", "-D", legacyRebuildBranch); err != nil {
		h.logWarn("cannot delete legacy rebuild branch", "slug", slug, "branch", legacyRebuildBranch, "error", err)
		return
	}
	h.logInfo("removed legacy rebuild branch", "slug", slug, "branch", legacyRebuildBranch)
}

// legacyMigrationTarget picks the branch the trunk moves to when it is found
// on the legacy rebuild branch: the workspace's configured branch if it
// exists locally, else the short name of refs/remotes/origin/HEAD if that
// exists locally. It returns "" when neither does.
func (h *RebuildHandler) legacyMigrationTarget(ctx context.Context, trunk GitRunner, slug string) string {
	if wb := workspaceBranch(h.DB, slug); wb != "" && wb != legacyRebuildBranch &&
		localRefExists(ctx, trunk, "refs/heads/"+wb) {
		return wb
	}
	if out, err := trunk.Run(ctx, "symbolic-ref", "--short", "-q", "refs/remotes/origin/HEAD"); err == nil {
		short := strings.TrimPrefix(strings.TrimSpace(out), "origin/")
		if short != "" && short != legacyRebuildBranch && localRefExists(ctx, trunk, "refs/heads/"+short) {
			return short
		}
	}
	return ""
}

// syncTrunkCheckout is Exception A (01-REQ-2.5). It runs under the phase-two
// lock right after the integration ref was moved. When the trunk's HEAD is
// the integration branch, the ref move left its index and files at the old
// commit, so `reset --hard HEAD` brings the checkout to the new tip. In every
// other case the trunk is not touched. A failing reset is logged and does
// not fail the run: the ref is already updated.
func (h *RebuildHandler) syncTrunkCheckout(ctx context.Context, trunk GitRunner, slug, integrationBranch string) {
	head, err := trunk.Run(ctx, "symbolic-ref", "-q", "HEAD")
	if err != nil || strings.TrimSpace(head) != "refs/heads/"+integrationBranch {
		return
	}
	if err := trunk.HardReset(ctx, "HEAD"); err != nil {
		h.logWarn("cannot reset trunk checkout to the new integration tip",
			"slug", slug, "branch", integrationBranch, "error", err)
		return
	}
	h.logInfo("reset trunk checkout to the new integration tip: the integration branch is checked out",
		"slug", slug, "branch", integrationBranch)
}

// checkStaleInputs compares the run's patch tips and upstream base with the
// current refs after the run and enqueues a follow-up rebuild when they
// moved (01-REQ-7). It runs after the worktree is removed and the lock is
// released. Not implemented yet: filled in by the task that owns the
// follow-up.
func (h *RebuildHandler) checkStaleInputs(_ context.Context, _ GitRunner, _ RebuildPayload, _ string, _ []PatchResult) {
}

// rebuildDir returns the directory that holds this workspace's rebuild
// worktrees: <workspace_root>/<slug>/rebuild, or, when WorkspaceRoot is
// empty, the sibling "rebuild" of the trunk path.
func (h *RebuildHandler) rebuildDir(slug, trunkPath string) string {
	if h.WorkspaceRoot != "" {
		return filepath.Join(h.WorkspaceRoot, slug, "rebuild")
	}
	return filepath.Join(filepath.Dir(trunkPath), "rebuild")
}

// cleanStaleRebuildDirs removes every directory under the workspace's
// rebuild directory (left by a crashed run) and prunes the worktree
// registrations in the trunk (01-REQ-8.2, 8.6). It runs in phase one, under
// the lock and the guard, and never fails the run: errors are logged.
func (h *RebuildHandler) cleanStaleRebuildDirs(ctx context.Context, trunk GitRunner, slug, trunkPath string) {
	dir := h.rebuildDir(slug, trunkPath)
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		h.logWarn("cannot list rebuild directory", "slug", slug, "path", dir, "error", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		stale := filepath.Join(dir, entry.Name())
		if err := os.RemoveAll(stale); err != nil {
			h.logWarn("cannot remove stale rebuild worktree directory", "slug", slug, "path", stale, "error", err)
			continue
		}
		h.logInfo("removed stale rebuild worktree directory", "slug", slug, "path", stale)
	}
	if err := trunk.WorktreePrune(ctx); err != nil {
		h.logWarn("stale rebuild worktree prune failed", "slug", slug, "path", dir, "error", err)
	}
}

// rebuildWorktreePath returns the absolute path of this run's worktree:
// <workspace_root>/<slug>/rebuild/<job id>. When no job ID is available a
// random one is used; when WorkspaceRoot is empty the path is derived from
// the parent directory of the trunk path (01-REQ-1.1, 01-REQ-1.2).
func (h *RebuildHandler) rebuildWorktreePath(slug, trunkPath, jobID string) (string, error) {
	if jobID == "" {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", fmt.Errorf("generate rebuild id: %w", err)
		}
		jobID = hex.EncodeToString(b[:])
	}
	dir := h.rebuildDir(slug, trunkPath)
	// git resolves a relative path against the runner's working directory
	// (the trunk), so the path must be absolute.
	abs, err := filepath.Abs(filepath.Join(dir, jobID))
	if err != nil {
		return "", fmt.Errorf("resolve worktree path: %w", err)
	}
	return abs, nil
}

// removeWorktree discards the run's worktree: `git worktree remove --force`
// (which also discards any in-progress cherry-pick or merge) followed by
// `git worktree prune`. If git cannot remove it the directory is deleted
// directly. It is best-effort, runs with a non-cancelled bounded context and
// never changes the job outcome (01-REQ-1.4, 1.5, 1.6).
func (h *RebuildHandler) removeWorktree(ctx context.Context, trunk GitRunner, slug, path string) {
	cctx, cancel := cleanupContext(ctx)
	defer cancel()

	if err := trunk.WorktreeRemove(cctx, path); err != nil {
		h.logWarn("rebuild worktree removal failed; deleting directory",
			"slug", slug, "path", path, "error", err)
		if rmErr := os.RemoveAll(path); rmErr != nil {
			h.logWarn("rebuild worktree directory removal failed",
				"slug", slug, "path", path, "error", rmErr)
		}
	}
	if err := trunk.WorktreePrune(cctx); err != nil {
		h.logWarn("rebuild worktree prune failed", "slug", slug, "path", path, "error", err)
	}
	h.logInfo("rebuild worktree removed", "slug", slug, "path", path)
}

// discardFailedWorktree cleans up after a failed `git worktree add`: nothing
// may be left under rebuild/ for this run (01-REQ-1.8).
func (h *RebuildHandler) discardFailedWorktree(ctx context.Context, trunk GitRunner, slug, path string) {
	cctx, cancel := cleanupContext(ctx)
	defer cancel()

	if err := os.RemoveAll(path); err != nil {
		h.logWarn("rebuild worktree directory removal failed", "slug", slug, "path", path, "error", err)
	}
	if err := trunk.WorktreePrune(cctx); err != nil {
		h.logWarn("rebuild worktree prune failed", "slug", slug, "path", path, "error", err)
	}
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
// cherry-picks it onto the worktree's HEAD. sha is the snapshot of the
// branch tip (01-REQ-6.1); the branch name is never used. If the commit
// list is empty, returns errPatchBranchNotFound. If an unresolvable
// conflict occurs, returns *rebuildConflictError.
func (h *RebuildHandler) applyRebasePatch(ctx context.Context, git GitRunner, sha, upstreamHead string) error {
	// 16-REQ-1.3: determine the commits to replay: those on the patch branch
	// that are not in upstream, skipping merge commits (as git rebase does)
	// and commits whose patch-id already exists upstream (already
	// cherry-picked or squash-merged content), which would otherwise stop
	// the cherry-pick as "empty".
	logOutput, err := git.Run(ctx, "log", "--reverse", "--format=%H", "--no-merges",
		"--right-only", "--cherry-pick", upstreamHead+"..."+sha)
	if err != nil {
		if isContextErr(err) {
			return err
		}
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
// Merges the snapshot SHA of the patch branch into the worktree's HEAD with
// --no-ff (01-REQ-6.3). The commit message names the branch, which a bare
// SHA merge would lose. If an unresolvable conflict occurs, returns
// *rebuildConflictError.
func (h *RebuildHandler) applyMergePatch(ctx context.Context, git GitRunner, sha, branchName string) error {
	// 16-REQ-1.4: merge with --no-ff.
	if err := git.MergeNoFF(ctx, sha, "Merge branch '"+branchName+"'"); err != nil {
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
//
// It returns *rebuildConflictError for unresolved conflicts, a context error
// when the run was cancelled (so that cancellation is never mistaken for a
// resolved conflict), and nil when all conflicts were resolved.
func (h *RebuildHandler) handleConflictWithRerere(ctx context.Context, git GitRunner, operation string) error {
	// ctxFailure reports a cancellation seen after a (best-effort) git call.
	ctxFailure := func(err error) error {
		if isContextErr(err) {
			return err
		}
		return ctx.Err()
	}

	// 16-REQ-1.5: allow git rerere with autoupdate to stage resolved files.
	_, err := git.Run(ctx, "rerere")
	if cerr := ctxFailure(err); cerr != nil {
		return cerr
	}

	// Check for remaining unresolved conflicts via diff filter.
	diffOutput, err := git.Run(ctx, "diff", "--name-only", "--diff-filter=U")
	if cerr := ctxFailure(err); cerr != nil {
		return cerr
	}
	unresolvedFiles := splitNonEmpty(diffOutput)

	if len(unresolvedFiles) > 0 {
		// Unresolved conflicts remain: abort the operation and report.
		_, _ = git.Run(ctx, operation, "--abort")
		return &rebuildConflictError{files: unresolvedFiles}
	}

	// All conflicts resolved by rerere: continue the operation.
	if operation == "cherry-pick" {
		_, err = git.Run(ctx, "cherry-pick", "--continue")
	} else {
		// For merge: finalize with git commit.
		_, err = git.Run(ctx, "commit", "--no-edit")
	}
	if cerr := ctxFailure(err); cerr != nil {
		return cerr
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
