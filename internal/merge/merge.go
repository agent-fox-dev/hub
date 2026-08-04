// Package merge implements the merge operations for integrating agent branches
// into target branches using rebase-then-fast-forward semantics, serialized
// via the durable job queue.
package merge

import (
	"fmt"

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

// RegisterHandler registers the 'merge' job type handler with the durable
// job queue. Must be called before the queue is started.
//
// The merge handler executes the rebase-then-fast-forward merge algorithm
// for a given source and target branch within a workspace.
func RegisterHandler(q *jobqueue.Queue) error {
	_ = q // TODO: implement in task group 8
	return fmt.Errorf("merge: RegisterHandler not implemented")
}

// ResolveSubmittedBy determines the submitted_by attribution string from
// the authenticated user's AuthInfo:
//   - For API key auth (CredentialType="api_key"): returns the UserID
//   - For admin token auth (CredentialType="admin_token"): returns "admin"
//   - For PAT auth (CredentialType="pat"): returns the UserID (PAT owner)
//
// Returns an error if auth is nil or the username cannot be resolved.
func ResolveSubmittedBy(auth *apikit.AuthInfo) (string, error) {
	_ = auth // TODO: implement in task group 8
	return "", fmt.Errorf("merge: ResolveSubmittedBy not implemented")
}

// EnqueueMergeJob creates and enqueues a merge job with:
//   - Key set to '<workspace_slug>:<target_branch>:<source_ref>'
//   - Group set to '<workspace_slug>:<target_branch>'
//
// Returns (jobID, duplicate, error). duplicate=true indicates an active job
// for the same (type, key) already exists.
func EnqueueMergeJob(q *jobqueue.Queue, workspaceSlug, targetBranch, sourceRef, submittedBy string) (string, bool, error) {
	_ = q             // TODO: implement in task group 8
	_ = workspaceSlug // TODO: implement in task group 8
	_ = targetBranch  // TODO: implement in task group 8
	_ = sourceRef     // TODO: implement in task group 8
	_ = submittedBy   // TODO: implement in task group 8
	return "", false, fmt.Errorf("merge: EnqueueMergeJob not implemented")
}
