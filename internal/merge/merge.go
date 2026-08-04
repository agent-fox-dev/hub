// Package merge implements the merge operations for integrating agent branches
// into target branches using rebase-then-fast-forward semantics, serialized
// via the durable job queue.
package merge

import (
	"context"
	"encoding/json"
	"fmt"

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

// mergeHandler is a placeholder handler for the 'merge' job type.
// The actual merge algorithm (pre-check, fetch, rebase, check, ref-update,
// branch-delete) will be wired in by later task groups (9–11).
func mergeHandler(_ context.Context, _ json.RawMessage) (any, bool, error) {
	return nil, false, fmt.Errorf("merge: handler not implemented")
}

// RegisterHandler registers the 'merge' job type handler with the durable
// job queue. Must be called before the queue is started.
//
// The merge handler executes the rebase-then-fast-forward merge algorithm
// for a given source and target branch within a workspace.
func RegisterHandler(q *jobqueue.Queue) error {
	return q.Register(MergeJobType, mergeHandler, nil)
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
