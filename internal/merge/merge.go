// Package merge implements the merge operations for integrating agent branches
// into target branches using rebase-then-fast-forward semantics, serialized
// via the durable job queue.
package merge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/agent-fox-dev/hub/internal/jobqueue"
	"github.com/txsvc/apikit"
)

// MergePayload contains the data stored in a merge job's payload field.
type MergePayload struct {
	WorkspaceSlug string `json:"workspace_slug"`
	TargetBranch  string `json:"target_branch"`
	SourceRef     string `json:"source_ref"`
	SubmittedBy   string `json:"submitted_by"`
}

// MergeJobType is the job type name registered with the durable job queue.
const MergeJobType = "merge"

// RegisterHandler registers the 'merge' job type handler with the durable
// job queue. Must be called before the queue is started.
//
// When h is non-nil, the handler executes the full rebase-then-fast-forward
// merge algorithm (pre-check, fetch, rebase, check command, ref update,
// branch deletion). When h is nil, a stub handler is registered that always
// fails — this is used by tests that only need the merge type registered
// for enqueue validation without executing real merge operations.
func RegisterHandler(q *jobqueue.Queue, h *Handler) error {
	var handler jobqueue.HandlerFunc
	if h != nil {
		handler = h.HandleMergeJob
	} else {
		handler = func(_ context.Context, _ json.RawMessage) (any, bool, error) {
			return nil, false, fmt.Errorf("merge: handler not configured")
		}
	}
	return q.Register(MergeJobType, handler, nil)
}

// HandleMergeJob orchestrates the full merge algorithm for a single merge job.
// It implements the jobqueue.HandlerFunc signature and is called by the job
// queue worker when a merge job is dispatched.
//
// The algorithm proceeds through these steps in order:
//  1. Pre-check (dry-run conflict detection, AlreadyMerged, BranchNotReady)
//  2. Fetch latest target branch state from upstream
//  3. Capture baseSHA (target HEAD before merge)
//  4. Rebase source branch onto target
//  5. Capture mergedSHA (rebased source HEAD)
//  6. Run check command (if configured for the workspace)
//  7. Update target branch ref to rebased source HEAD
//  8. Delete source branch ref
//  9. Return MergeResult with base_sha and merged_sha
//
// Errors are returned as structured JSON matching mergeJobError format so that
// ProjectMergeJobResponse can extract conflict_files and check_output fields.
func (h *Handler) HandleMergeJob(ctx context.Context, payload json.RawMessage) (retResult any, retRetryable bool, retErr error) {
	var p MergePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, false, newMergeJobError("invalid_payload", nil, "")
	}

	// 18-REQ-2.3: Emit hub.merge.fail on any error return from this function.
	defer func() {
		if retErr != nil {
			h.emitMergeAudit(ctx, p.WorkspaceSlug, "hub.merge.fail", map[string]any{
				"reason": retErr.Error(),
			})
		}
	}()

	slog.Info("merge: starting merge job",
		"workspace", p.WorkspaceSlug,
		"target", p.TargetBranch,
		"source", p.SourceRef,
	)

	// Step 1: Pre-check (dry-run conflict detection, AlreadyMerged, BranchNotReady).
	result, err := h.PreCheck(ctx, p.WorkspaceSlug, p.TargetBranch, p.SourceRef)
	if err != nil {
		retryable, mergeErr := convertMergeError(err)
		return nil, retryable, mergeErr
	}
	if result != nil && result.Reason == AlreadyMerged {
		// Idempotent success — source already integrated into target.
		slog.Info("merge: source already merged (idempotent success)",
			"workspace", p.WorkspaceSlug,
			"target", p.TargetBranch,
			"source", p.SourceRef,
		)
		runner, runErr := h.runnerForWorkspace(p.WorkspaceSlug)
		if runErr != nil {
			return nil, true, runErr
		}
		sha, revErr := runner.RevParse(ctx, p.TargetBranch)
		if revErr != nil {
			return nil, true, revErr
		}
		return &MergeResult{BaseSHA: sha, MergedSHA: sha}, false, nil
	}

	// Step 2: Fetch latest target branch state from upstream remote.
	if err := h.FetchTarget(ctx, p.WorkspaceSlug, p.TargetBranch); err != nil {
		return nil, true, err // retryable (12-REQ-6.E5)
	}

	// Step 3: Capture baseSHA (target HEAD after fetch, before merge).
	runner, err := h.runnerForWorkspace(p.WorkspaceSlug)
	if err != nil {
		return nil, true, err
	}
	baseSHA, err := runner.RevParse(ctx, p.TargetBranch)
	if err != nil {
		return nil, true, fmt.Errorf("merge: resolve target head: %w", err)
	}

	// Step 4: Rebase source branch onto target.
	preRebaseSHA, err := h.RebaseSource(ctx, p.WorkspaceSlug, p.TargetBranch, p.SourceRef)
	if err != nil {
		retryable, mergeErr := convertMergeError(err)
		return nil, retryable, mergeErr
	}

	// Step 5: Capture mergedSHA (rebased source HEAD).
	mergedSHA, err := runner.RevParse(ctx, p.SourceRef)
	if err != nil {
		return nil, true, fmt.Errorf("merge: resolve rebased source head: %w", err)
	}

	// Step 6: Run check command (if configured for the workspace).
	_, err = h.RunCheckStep(ctx, p.WorkspaceSlug, p.TargetBranch, p.SourceRef, preRebaseSHA)
	if err != nil {
		retryable, mergeErr := convertMergeError(err)
		return nil, retryable, mergeErr
	}

	// Step 7: Update target branch ref to rebased source HEAD (fast-forward).
	if err := h.UpdateTargetRef(ctx, p.WorkspaceSlug, p.TargetBranch, mergedSHA); err != nil {
		return nil, true, err // retryable (12-REQ-6.E4)
	}

	// Step 8: Delete source branch ref.
	if err := h.DeleteSourceBranch(ctx, p.WorkspaceSlug, p.SourceRef); err != nil {
		slog.Error("merge: source branch deletion failed after successful ref update",
			"workspace", p.WorkspaceSlug,
			"source", p.SourceRef,
			"error", err.Error(),
		)
		// Continue to return success — the critical ref update already succeeded.
	}

	// Step 9: Return success with base_sha and merged_sha.
	mergeResult, err := h.Finalize(baseSHA, mergedSHA)
	if err != nil {
		return nil, false, err
	}

	// 18-REQ-2.2: Emit hub.merge.complete audit event.
	h.emitMergeAudit(ctx, p.WorkspaceSlug, "hub.merge.complete", map[string]any{
		"base_sha":   baseSHA,
		"merged_sha": mergedSHA,
	})

	return mergeResult, false, nil
}

// structuredMergeError wraps merge failure details so that Error() returns
// a JSON string parseable by ProjectMergeJobResponse. It reuses the
// mergeJobError type defined in api.go.
type structuredMergeError struct {
	data mergeJobError
}

func (e *structuredMergeError) Error() string {
	b, _ := json.Marshal(e.data)
	return string(b)
}

// newMergeJobError creates a structuredMergeError with the given fields.
func newMergeJobError(reason string, conflictFiles []string, checkOutput string) error {
	return &structuredMergeError{
		data: mergeJobError{
			Reason:        reason,
			ConflictFiles: conflictFiles,
			CheckOutput:   checkOutput,
		},
	}
}

// convertMergeError converts an error from a merge step into the
// (retryable, error) tuple expected by jobqueue.HandlerFunc.
// For *MergeRejection errors, it produces structured JSON errors;
// for other errors, it passes them through as non-retryable.
func convertMergeError(err error) (retryable bool, mergeErr error) {
	var rejection *MergeRejection
	if errors.As(err, &rejection) {
		retryable = !rejection.Permanent
		return retryable, newMergeJobError(
			string(rejection.Reason),
			rejection.ConflictFiles,
			"",
		)
	}
	// Non-MergeRejection errors: pass through as non-retryable.
	return false, err
}

// ResolveSubmittedBy determines the submitted_by attribution string from
// the authenticated user's AuthInfo:
//   - For API key auth (CredentialType="api_key"): returns the UserID
//   - For admin token auth (CredentialType="admin_token"): returns "admin"
//   - For PAT auth (CredentialType="pat"): returns the UserID (PAT owner)
//
// Returns an error if auth is nil or the username cannot be resolved.
func ResolveSubmittedBy(auth *apikit.AuthInfo) (string, error) {
	if auth == nil {
		return "", fmt.Errorf("merge: cannot resolve submitted_by: auth info is nil")
	}

	if auth.CredentialType == "admin_token" {
		return "admin", nil
	}

	// For api_key and pat, use the UserID field.
	if auth.UserID == "" {
		return "", fmt.Errorf("merge: cannot resolve submitted_by: empty user ID for credential type %q", auth.CredentialType)
	}
	return auth.UserID, nil
}

// EnqueueMergeJob creates and enqueues a merge job with:
//   - Key set to '<workspace_slug>:<target_branch>:<source_ref>'
//   - Group set to '<workspace_slug>:<target_branch>'
//
// Returns (jobID, duplicate, error). duplicate=true indicates an active job
// for the same (type, key) already exists.
func EnqueueMergeJob(q *jobqueue.Queue, workspaceSlug, targetBranch, sourceRef, submittedBy string) (string, bool, error) {
	payload := MergePayload{
		WorkspaceSlug: workspaceSlug,
		TargetBranch:  targetBranch,
		SourceRef:     sourceRef,
		SubmittedBy:   submittedBy,
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", false, fmt.Errorf("merge: marshal payload: %w", err)
	}

	key := fmt.Sprintf("%s:%s:%s", workspaceSlug, targetBranch, sourceRef)
	group := fmt.Sprintf("%s:%s", workspaceSlug, targetBranch)

	return q.Enqueue(jobqueue.EnqueueParams{
		Type:        MergeJobType,
		Key:         key,
		Nonce:       uuid.New().String(),
		Payload:     payloadJSON,
		SubmittedBy: submittedBy,
		Group:       group,
	})
}
