package carrypatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/google/uuid"
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
// found is false when the ref does not exist or the trunk is not on disk.
func (s *RecoveryService) ReadReplacedSHA(ctx context.Context, slug, branch string) (string, bool, error) {
	repoPath := s.trunkPath(slug)

	// Check if the trunk directory exists.
	if _, err := os.Stat(repoPath); os.IsNotExist(err) {
		return "", false, nil
	}

	runner, err := s.NewGitRunner(repoPath)
	if err != nil {
		return "", false, nil
	}

	ref := "refs/hub/replaced/" + branch
	sha, err := resolveRefSHA(ctx, runner, ref)
	if err != nil {
		// Ref does not exist.
		return "", false, nil
	}

	return sha, true, nil
}

// RemoveBackup deletes refs/hub/replaced/<branch>. A missing ref or a
// missing trunk is not an error.
func (s *RecoveryService) RemoveBackup(ctx context.Context, slug, branch string) error {
	repoPath := s.trunkPath(slug)

	// Check if the trunk directory exists.
	if _, err := os.Stat(repoPath); os.IsNotExist(err) {
		slog.Info("recovery: trunk not on disk, skipping backup removal",
			"slug", slug,
			"branch", branch,
		)
		return nil
	}

	runner, err := s.NewGitRunner(repoPath)
	if err != nil {
		return fmt.Errorf("recovery: open runner for %s: %w", slug, err)
	}

	ref := "refs/hub/replaced/" + branch
	_, err = runner.Run(ctx, "update-ref", "-d", ref)
	if err != nil {
		// Check if the ref simply doesn't exist (which is fine).
		if _, resolveErr := resolveRefSHA(ctx, runner, ref); resolveErr != nil {
			// Ref doesn't exist, that's success.
			return nil
		}
		return fmt.Errorf("recovery: delete ref %s in %s: %w", ref, slug, err)
	}

	slog.Info("recovery: removed backup ref",
		"slug", slug,
		"branch", branch,
		"ref", ref,
	)

	return nil
}

// RunReset moves a patch branch to the fork's current tip.
// It takes the workspace lock, fetches the branch from the fork,
// compares tips and applies the appropriate action.
func (s *RecoveryService) RunReset(ctx context.Context, slug string, patch ResetPatchInfo, auth *apikit.AuthInfo) (ResetResult, error) {
	var result ResetResult

	// 23-REQ-3: Take the workspace lock.
	if s.LockFunc == nil {
		return result, &RecoveryError{
			Kind:    RecoveryErrOther,
			Message: "lock function not configured",
		}
	}
	unlock, ok := s.LockFunc(slug)
	if !ok {
		return result, &RecoveryError{
			Kind:    RecoveryErrBusy,
			Message: "workspace is busy",
		}
	}
	defer unlock()

	repoPath := s.trunkPath(slug)

	// 23-REQ-3.1: Resolve origin credentials.
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
	if s.Fetch != nil {
		fetchErr := s.Fetch(ctx, repoPath, patch.BranchName, originAuth)
		if fetchErr != nil {
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
		if _, err := runner.Run(ctx, "update-ref", branchRef, forkTip, zeroSHA); err != nil {
			// CAS create failed — check if the ref appeared (race).
			if _, checkErr := resolveRefSHA(ctx, runner, branchRef); checkErr == nil {
				return result, &RecoveryError{
					Kind:    RecoveryErrRefChanged,
					Message: "patch branch changed during reset; retry",
				}
			}
			return result, &RecoveryError{
				Kind:    RecoveryErrOther,
				Message: fmt.Sprintf("failed to update patch branch %s", patch.BranchName),
				Cause:   err,
			}
		}
		result.Action = ActionCreated

		// Hard reset working tree if this branch is checked out.
		if err := maybeHardReset(ctx, runner, repoPath, branchRef); err != nil {
			slog.Warn("recovery: hard reset failed after create",
				"slug", slug,
				"branch", patch.BranchName,
				"error", err,
			)
		}

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
			if _, err := runner.Run(ctx, "update-ref", branchRef, forkTip, localTip); err != nil {
				return result, classifyCASError(err, patch.BranchName, runner, ctx, branchRef, localTip)
			}
			result.Action = ActionFastForwarded

			if err := maybeHardReset(ctx, runner, repoPath, branchRef); err != nil {
				slog.Warn("recovery: hard reset failed after fast-forward",
					"slug", slug,
					"branch", patch.BranchName,
					"error", err,
				)
			}

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
			if _, err := runner.Run(ctx, "update-ref", branchRef, forkTip, localTip); err != nil {
				// 23-REQ-3.8: Keep the backup already written.
				return result, classifyCASError(err, patch.BranchName, runner, ctx, branchRef, localTip)
			}

			result.Action = ActionReplaced
			result.ReplacedSHA = localTip

			if err := maybeHardReset(ctx, runner, repoPath, branchRef); err != nil {
				slog.Warn("recovery: hard reset failed after replace",
					"slug", slug,
					"branch", patch.BranchName,
					"error", err,
				)
			}

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
	// 23-REQ-4.3: Rebuild trigger
	// ===========================================================
	branchMoved := result.Action == ActionCreated || result.Action == ActionFastForwarded || result.Action == ActionReplaced
	rebuildEligible := patch.Status == PatchStatusActive || patch.Status == PatchStatusConflict

	if branchMoved && rebuildEligible && s.Queue != nil {
		autoRebuild := true
		if s.GetVariable != nil {
			val, _ := s.GetVariable("workspace", slug, "AUTO_REBUILD_AFTER_SYNC")
			if val == "false" {
				autoRebuild = false
			}
		}

		if autoRebuild {
			userID := ""
			if auth != nil {
				userID = auth.UserID
			}
			jobID, triggered, enqErr := s.enqueueRebuild(slug, patch.IntegrationBranch, userID)
			if enqErr != nil {
				slog.Error("recovery: failed to enqueue rebuild",
					"slug", slug,
					"branch", patch.BranchName,
					"error", enqErr,
				)
			} else if triggered {
				result.RebuildTriggered = true
				result.RebuildJobID = jobID
			}
		}
	}

	return result, nil
}

// enqueueRebuild enqueues a rebuild job with the same parameters as sync.
func (s *RecoveryService) enqueueRebuild(slug, integrationBranch, userID string) (string, bool, error) {
	payload := BuildRebuildPayload(slug, integrationBranch, userID, s.GetVariable, "", "")
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", false, fmt.Errorf("marshal rebuild payload: %w", err)
	}
	groupKey := slug + ":" + integrationBranch
	nonce := uuid.New().String()

	jobID, duplicate, enqErr := s.Queue.Enqueue(jobqueue.EnqueueParams{
		Type:        "rebuild",
		Key:         slug,
		Nonce:       nonce,
		Payload:     payloadJSON,
		SubmittedBy: userID,
		Group:       groupKey,
	})
	if enqErr != nil {
		return "", false, enqErr
	}
	if duplicate {
		return "", false, nil
	}
	return jobID, true, nil
}

// classifyCASError determines whether a CAS update-ref failure is a lost race
// (the ref changed) or some other failure.
func classifyCASError(err error, branchName string, runner GitRunner, ctx context.Context, branchRef, expectedOld string) *RecoveryError {
	// Check if the ref still holds the expected old value.
	currentSHA, resolveErr := resolveRefSHA(ctx, runner, branchRef)
	if resolveErr == nil && currentSHA != expectedOld {
		// The ref changed — lost race.
		return &RecoveryError{
			Kind:    RecoveryErrRefChanged,
			Message: "patch branch changed during reset; retry",
		}
	}

	// Check if the ref was deleted (also a race).
	if resolveErr != nil && strings.Contains(err.Error(), "update_ref") {
		return &RecoveryError{
			Kind:    RecoveryErrRefChanged,
			Message: "patch branch changed during reset; retry",
		}
	}

	return &RecoveryError{
		Kind:    RecoveryErrOther,
		Message: fmt.Sprintf("failed to update patch branch %s", branchName),
		Cause:   err,
	}
}
