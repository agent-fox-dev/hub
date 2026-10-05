package carrypatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
)

// RecoveryErrorKind classifies errors returned by the recovery service.
// These are carrypatch-local types; the workspace handler maps them to
// HTTP status codes through an adapter registered in main.go.
type RecoveryErrorKind string

const (
	RecoveryErrBusy             RecoveryErrorKind = "busy"
	RecoveryErrMissingOnOrigin  RecoveryErrorKind = "missing_on_origin"
	RecoveryErrFetchFailed      RecoveryErrorKind = "fetch_failed"
	RecoveryErrCredentialFailed RecoveryErrorKind = "credential_failed"
	RecoveryErrRefChanged       RecoveryErrorKind = "ref_changed"
	RecoveryErrOther            RecoveryErrorKind = "other"
)

// RecoveryError is a classified error returned by the recovery service.
type RecoveryError struct {
	Kind    RecoveryErrorKind
	Message string
	Cause   error
}

func (e *RecoveryError) Error() string {
	if e.Cause != nil {
		return e.Message + ": " + e.Cause.Error()
	}
	return e.Message
}

func (e *RecoveryError) Unwrap() error {
	return e.Cause
}

// ResetPatchInfo carries the patch data the reset operation needs.
// Defined in carrypatch so it does not import workspace.
type ResetPatchInfo struct {
	ID                string
	BranchName        string
	Status            string
	IntegrationBranch string
}

// ResetResult holds the outcome of a reset-to-origin operation.
type ResetResult struct {
	Action           string // one of ActionNone, ActionCreated, ActionFastForwarded, ActionReplaced
	LocalSHA         string // tip before the reset; empty when the local branch was absent
	OriginSHA        string // fork tip the branch was moved to
	ReplacedSHA      string // backup SHA; non-empty only for action "replaced"
	RebuildTriggered bool
	RebuildJobID     string
}

// RebuildEnqueuer abstracts the job queue's Enqueue method so that
// tests can inject a stub or a failing implementation.
type RebuildEnqueuer interface {
	Enqueue(params jobqueue.EnqueueParams) (jobID string, duplicate bool, err error)
}

// RecoveryService implements backup-ref reading, backup-ref removal and
// reset-to-origin operations. It is constructed in main.go and adapted
// to the workspace.RecoveryHook interface.
//
// RunReset needs LockFunc and Fetch: without them it fails with a
// RecoveryErrOther configuration error before it writes anything. ResolveAuth
// may be nil, which means anonymous access to origin (the same reading as spec
// 20's ResolveOriginAuth). PatchStore and Queue are optional; without them the
// reset records no origin sync state and enqueues no rebuild.
type RecoveryService struct {
	NewGitRunner  func(repoPath string) (GitRunner, error)
	WorkspaceRoot string
	GetVariable   GetVariableFunc
	ResolveAuth   ResolveAuthFunc
	Fetch         SingleBranchFetchFunc
	LockFunc      func(slug string) (unlock func(), ok bool)
	PatchStore    PatchStore
	Queue         RebuildEnqueuer
}

// trunkPath returns the path to the trunk directory for a workspace.
func (s *RecoveryService) trunkPath(slug string) string {
	return filepath.Join(s.WorkspaceRoot, slug, "trunk")
}

// ReadReplacedSHA returns the SHA that refs/hub/replaced/<branch> resolves to.
// found is false, with a nil error, when the ref does not exist or the trunk
// is not on disk. Any other failure (a runner that cannot be built, git that
// cannot be run, a corrupt repository) is returned as an error: it must not
// be rendered as "no backup". The caller logs it and still answers the request.
func (s *RecoveryService) ReadReplacedSHA(ctx context.Context, slug, branch string) (string, bool, error) {
	repoPath := s.trunkPath(slug)

	// Check if the trunk directory exists.
	if _, err := os.Stat(repoPath); err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("recovery: stat trunk for %s: %w", slug, err)
	}

	runner, err := s.NewGitRunner(repoPath)
	if err != nil {
		return "", false, fmt.Errorf("recovery: open runner for %s: %w", slug, err)
	}

	ref := "refs/hub/replaced/" + branch
	sha, found, err := lookupRefSHA(ctx, runner, ref)
	if err != nil {
		return "", false, fmt.Errorf("recovery: look up %s in %s: %w", ref, slug, err)
	}
	return sha, found, nil
}

// RemoveBackup deletes refs/hub/replaced/<branch>. A missing ref or a
// missing trunk is not an error.
func (s *RecoveryService) RemoveBackup(ctx context.Context, slug, branch string) error {
	return removeBackupRef(ctx, s.WorkspaceRoot, slug, branch, s.NewGitRunner)
}

// removeBackupRef removes refs/hub/replaced/<branch> from the workspace's
// trunk. It is the one implementation behind both RecoveryService.RemoveBackup
// (patch removal) and the expired-row purge (PurgeExpiredDeletedPatchesWithRefs).
//
// A missing trunk and a missing ref both count as success. The ref is looked
// up first because git update-ref -d exits 0 for a ref that does not exist,
// so the delete alone could not tell "removed" from "nothing to remove": the
// lookup keeps a missing ref from being logged as removed, and puts the removed
// SHA in the log (23-REQ-10.7). Any other failure is returned.
func removeBackupRef(
	ctx context.Context,
	workspaceRoot, slug, branch string,
	newGitRunner func(repoPath string) (GitRunner, error),
) error {
	repoPath := filepath.Join(workspaceRoot, slug, "trunk")

	// Check if the trunk directory exists.
	if _, err := os.Stat(repoPath); err != nil {
		if os.IsNotExist(err) {
			slog.Info("recovery: trunk not on disk, skipping backup removal",
				"slug", slug,
				"branch", branch,
			)
			return nil
		}
		return fmt.Errorf("recovery: stat trunk for %s: %w", slug, err)
	}

	runner, err := newGitRunner(repoPath)
	if err != nil {
		return fmt.Errorf("recovery: open runner for %s: %w", slug, err)
	}

	ref := "refs/hub/replaced/" + branch
	removedSHA, found, err := lookupRefSHA(ctx, runner, ref)
	if err != nil {
		return fmt.Errorf("recovery: look up %s in %s: %w", ref, slug, err)
	}
	if !found {
		// Nothing to remove.
		return nil
	}

	if _, err := runner.Run(ctx, "update-ref", "-d", ref); err != nil {
		return fmt.Errorf("recovery: delete ref %s in %s: %w", ref, slug, err)
	}

	slog.Info("recovery: removed backup ref",
		"slug", slug,
		"branch", branch,
		"ref", ref,
		"removed_sha", removedSHA,
	)

	return nil
}

// RunReset moves a patch branch to the fork's current tip.
// It takes the workspace lock, fetches the branch from the fork,
// compares tips and applies the appropriate action.
//
// A hard-reset failure after the branch ref has moved is not fatal. The ref
// move is the reset's effect: the persisted state, the rebuild and the audit
// event all describe it, and a retry would find the tips equal and answer
// action "none", so an error would not lead the operator to a repaired work
// tree. The failure is logged at error level instead, with the slug and
// branch, and the reset reports the move. The patch refresh in spec 20 keeps
// the same outcome for the same failure and additionally reports the failed
// reset to its caller; see docs/errata/23_patch_divergence_recovery_divergences.md.
func (s *RecoveryService) RunReset(ctx context.Context, slug string, patch ResetPatchInfo, auth *apikit.AuthInfo) (ResetResult, error) {
	var result ResetResult

	// Required dependencies. Both are checked before anything is taken or
	// written. A missing Fetch in particular must not degrade to "use the
	// tracking ref as it is": a stale refs/remotes/origin/<branch> would look
	// like a successful reset (Design Decision 4).
	if s.LockFunc == nil {
		slog.Error("recovery: reset is not configured: lock function missing",
			"slug", slug,
			"branch", patch.BranchName,
		)
		return result, &RecoveryError{
			Kind:    RecoveryErrOther,
			Message: "lock function not configured",
		}
	}
	if s.Fetch == nil {
		slog.Error("recovery: reset is not configured: origin fetch missing",
			"slug", slug,
			"branch", patch.BranchName,
		)
		return result, &RecoveryError{
			Kind:    RecoveryErrOther,
			Message: "origin fetch is not configured",
		}
	}

	// 23-REQ-3: Take the workspace lock.
	unlock, ok := s.LockFunc(slug)
	if !ok {
		return result, &RecoveryError{
			Kind:    RecoveryErrBusy,
			Message: "workspace is busy",
		}
	}
	defer unlock()

	repoPath := s.trunkPath(slug)

	// 23-REQ-3.1: Resolve origin credentials. A nil ResolveAuth means
	// anonymous access to origin.
	var originAuth transport.AuthMethod
	if s.ResolveAuth != nil {
		resolved, authErr := s.ResolveAuth(slug)
		if authErr != nil {
			slog.Error("recovery: failed to resolve origin credentials",
				"slug", slug,
				"branch", patch.BranchName,
				"error", authErr.Error(),
			)
			return result, &RecoveryError{
				Kind:    RecoveryErrCredentialFailed,
				Message: "failed to resolve origin credentials",
				Cause:   authErr,
			}
		}
		originAuth = resolved
	}

	// 23-REQ-3.1: Fetch the single branch from the fork.
	if fetchErr := s.Fetch(ctx, repoPath, patch.BranchName, originAuth); fetchErr != nil {
		// 23-REQ-3.3: Classify "branch not found on origin".
		if errors.Is(fetchErr, ErrBranchNotOnOrigin) {
			return result, &RecoveryError{
				Kind:    RecoveryErrMissingOnOrigin,
				Message: "branch does not exist on origin",
			}
		}
		// 23-REQ-3.2: Log the underlying error but don't echo it.
		slog.Error("recovery: origin fetch failed",
			"slug", slug,
			"branch", patch.BranchName,
			"error", fetchErr.Error(),
		)
		return result, &RecoveryError{
			Kind:    RecoveryErrFetchFailed,
			Message: "origin fetch failed",
			Cause:   fetchErr,
		}
	}

	// Create a git runner for the trunk.
	runner, err := s.NewGitRunner(repoPath)
	if err != nil {
		return result, &RecoveryError{
			Kind:    RecoveryErrOther,
			Message: fmt.Sprintf("failed to create git runner for %s", slug),
			Cause:   err,
		}
	}

	branchRef := "refs/heads/" + patch.BranchName
	originRef := "refs/remotes/origin/" + patch.BranchName

	// 23-REQ-3.1: Resolve fork tip.
	forkTip, forkErr := resolveRefSHA(ctx, runner, originRef)
	if forkErr != nil {
		// This shouldn't happen after a successful fetch, but handle it.
		return result, &RecoveryError{
			Kind:    RecoveryErrMissingOnOrigin,
			Message: "branch does not exist on origin",
		}
	}
	result.OriginSHA = forkTip

	// 23-REQ-3.1: Resolve local tip (may be absent).
	localTip, localErr := resolveRefSHA(ctx, runner, branchRef)
	if localErr == nil {
		result.LocalSHA = localTip
	}

	// 23-REQ-3.4-3.6: Apply the first matching rule.
	if localErr != nil {
		// Rule: Local branch missing → create.
		if recErr := moveBranchToFork(ctx, runner, repoPath, slug, patch, forkTip, zeroSHA); recErr != nil {
			return result, recErr
		}
		result.Action = ActionCreated

		slog.Info("recovery: created patch branch from origin",
			"slug", slug,
			"branch", patch.BranchName,
			"origin_sha", forkTip,
		)
	} else if localTip == forkTip {
		// Rule: Same commit → none.
		result.Action = ActionNone
	} else {
		// Rule: Check if local tip is a strict ancestor of fork tip (fast-forward).
		isAnc, ancErr := runner.IsAncestor(ctx, localTip, forkTip)
		if ancErr != nil {
			return result, &RecoveryError{
				Kind:    RecoveryErrOther,
				Message: fmt.Sprintf("failed to check ancestry for %s", patch.BranchName),
				Cause:   ancErr,
			}
		}

		if isAnc {
			// Fast-forward: move the branch, no backup.
			if recErr := moveBranchToFork(ctx, runner, repoPath, slug, patch, forkTip, localTip); recErr != nil {
				return result, recErr
			}
			result.Action = ActionFastForwarded

			slog.Info("recovery: fast-forwarded patch branch",
				"slug", slug,
				"branch", patch.BranchName,
				"local_sha", localTip,
				"origin_sha", forkTip,
			)
		} else {
			// Rule: Diverged or fork-behind → write backup, then move.
			backupRef := "refs/hub/replaced/" + patch.BranchName

			// 23-REQ-3.6: Write the backup ref (force-write, overwriting any earlier backup).
			if err := runner.UpdateRef(ctx, backupRef, localTip); err != nil {
				return result, &RecoveryError{
					Kind:    RecoveryErrOther,
					Message: fmt.Sprintf("failed to write backup ref for %s", patch.BranchName),
					Cause:   err,
				}
			}

			// Move the branch to the fork tip using CAS.
			// 23-REQ-3.8: A lost race keeps the backup already written.
			if recErr := moveBranchToFork(ctx, runner, repoPath, slug, patch, forkTip, localTip); recErr != nil {
				return result, recErr
			}

			result.Action = ActionReplaced
			result.ReplacedSHA = localTip

			slog.Info("recovery: replaced patch branch",
				"slug", slug,
				"branch", patch.BranchName,
				"replaced_sha", localTip,
				"origin_sha", forkTip,
			)
		}
	}

	// ===========================================================
	// 23-REQ-4.1: Persist origin sync state (origin mode only)
	// ===========================================================
	if s.PatchStore != nil {
		patchSource := ParsePatchBranchSource(s.GetVariable, slug)
		if patchSource == "origin" {
			now := apikit.NowUTC()
			originSHA := forkTip
			if err := s.PatchStore.SetOriginSyncState(ctx, patch.ID, StateInSync, &originSHA, now); err != nil {
				slog.Warn("recovery: failed to persist origin sync state",
					"slug", slug,
					"branch", patch.BranchName,
					"error", err,
				)
			}
		}
	}

	// ===========================================================
	// 23-REQ-4.3: Rebuild trigger (the same enqueue as sync)
	// ===========================================================
	branchMoved := result.Action == ActionCreated || result.Action == ActionFastForwarded || result.Action == ActionReplaced
	rebuildEligible := patch.Status == PatchStatusActive || patch.Status == PatchStatusConflict

	if branchMoved && rebuildEligible && s.Queue != nil && autoRebuildEnabledFor(s.GetVariable, slug) {
		userID := ""
		if auth != nil {
			userID = auth.UserID
		}
		jobID, enqueued, enqErr := enqueueRebuildJob(s.Queue, s.GetVariable, slug, patch.IntegrationBranch, userID)
		if enqErr != nil {
			slog.Error("recovery: failed to enqueue rebuild",
				"slug", slug,
				"branch", patch.BranchName,
				"error", enqErr,
			)
		} else if enqueued {
			result.RebuildTriggered = true
			result.RebuildJobID = jobID
		}
	}

	return result, nil
}

// moveBranchToFork moves refs/heads/<branch> from expectedOld to the fork tip
// with the compare-and-swap move shared with the patch refresh (casMoveRef),
// then lets the trunk work tree follow when the branch is checked out.
//
// A write that fails is classified (classifyCASError) and returned; the
// branch did not move. A work-tree reset that fails after the ref moved is
// logged at error level and not returned, so the caller still persists,
// enqueues and reports the move (see RunReset).
func moveBranchToFork(
	ctx context.Context,
	runner GitRunner,
	repoPath, slug string,
	patch ResetPatchInfo,
	forkTip, expectedOld string,
) *RecoveryError {
	branchRef := "refs/heads/" + patch.BranchName

	moved, _, err := casMoveRef(ctx, runner, repoPath, branchRef, forkTip, expectedOld)
	if err == nil {
		return nil
	}
	if !moved {
		return classifyCASError(ctx, runner, patch.BranchName, branchRef, expectedOld, err)
	}

	slog.Error("recovery: hard reset failed after the branch moved; the checked-out work tree may still show the old tip",
		"slug", slug,
		"branch", patch.BranchName,
		"error", err,
	)
	return nil
}

// classifyCASError classifies a failed compare-and-swap write of a branch ref
// from the state of the ref afterwards, never from the error text:
//
//   - the ref now holds a different commit than expectedOld, or exists when it
//     was to be created, or is gone when it was to be moved: another writer got
//     in first, RecoveryErrRefChanged;
//   - the ref is as it was, or the ref cannot be read again: the write failed
//     for another reason, RecoveryErrOther.
//
// expectedOld is zeroSHA for a create.
func classifyCASError(ctx context.Context, runner GitRunner, branchName, branchRef, expectedOld string, err error) *RecoveryError {
	current, found, lookupErr := lookupRefSHA(ctx, runner, branchRef)
	if lookupErr != nil {
		slog.Error("recovery: cannot re-read patch branch after a failed update",
			"branch", branchName,
			"ref", branchRef,
			"error", lookupErr,
		)
	} else {
		create := expectedOld == zeroSHA
		changed := (found && (create || current != expectedOld)) || (!found && !create)
		if changed {
			return &RecoveryError{
				Kind:    RecoveryErrRefChanged,
				Message: "patch branch changed during reset; retry",
			}
		}
	}

	return &RecoveryError{
		Kind:    RecoveryErrOther,
		Message: fmt.Sprintf("failed to update patch branch %s", branchName),
		Cause:   err,
	}
}
