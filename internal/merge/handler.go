package merge

import (
	"context"
	"fmt"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/hub/internal/gitcmd"
)

// RejectionReason is a typed constant identifying why a merge was rejected
// during the pre-check phase.
type RejectionReason string

const (
	// WouldConflict indicates the dry-run merge-tree detected conflicts.
	WouldConflict RejectionReason = "WouldConflict"

	// AlreadyMerged indicates the source branch is already integrated into
	// the target branch.
	AlreadyMerged RejectionReason = "AlreadyMerged"

	// BranchNotReady indicates the source branch has no commits ahead of
	// the target branch.
	BranchNotReady RejectionReason = "BranchNotReady"
)

// MergeRejection is returned by PreCheck and related methods when the merge
// cannot proceed for a known, typed reason. IsPermanent indicates whether
// the error should be treated as a permanent failure (no retries) or a
// retryable condition.
type MergeRejection struct {
	Reason        RejectionReason
	ConflictFiles []string
	Permanent     bool
}

func (e *MergeRejection) Error() string {
	return fmt.Sprintf("merge rejected: %s", e.Reason)
}

// PreCheckResult is returned by PreCheck on non-error paths. For example,
// AlreadyMerged returns a successful PreCheckResult with Reason set.
type PreCheckResult struct {
	Reason RejectionReason
}

// CheckCommandTimeout is the maximum duration for the workspace check command.
const CheckCommandTimeout = 10 * time.Minute

// CommandExecutor abstracts shell command execution for testing.
type CommandExecutor interface {
	// Run executes a command in the given directory with the provided
	// environment variables and timeout. Returns stdout+stderr and error.
	Run(ctx context.Context, dir string, env []string, timeout time.Duration, command string, args ...string) (string, error)
}

// FetchFunc abstracts the go-git fetch operation for testing. It fetches
// the named ref from the upstream remote using the provided auth credentials.
type FetchFunc func(trunkDir string, targetBranch string, auth transport.AuthMethod) error

// ResolveAuthFunc abstracts credential resolution for testing.
type ResolveAuthFunc func(workspaceSlug string) (transport.AuthMethod, error)

// Handler executes the merge algorithm for a single merge job.
type Handler struct {
	// WorkspaceRoot is the root directory under which workspace clones live.
	// Each workspace clone is at <WorkspaceRoot>/<slug>/trunk/.
	WorkspaceRoot string

	// Runner is the git CLI runner for subprocess operations.
	Runner *gitcmd.GitRunner

	// Fetch performs go-git fetch from the upstream remote.
	Fetch FetchFunc

	// ResolveAuth resolves clone credentials for a workspace.
	ResolveAuth ResolveAuthFunc

	// Executor runs shell commands (e.g. CHECK_COMMAND).
	Executor CommandExecutor

	// GetVariable retrieves a workspace variable by scope, slug, and key.
	GetVariable func(scope, slug, key string) (string, error)

	// Rollback reverts the source branch to a previous SHA after a failed
	// check command or other post-rebase failure.
	Rollback RollbackFunc
}

// TrunkDir returns the workspace trunk directory path for the given slug.
func (h *Handler) TrunkDir(slug string) string {
	return fmt.Sprintf("%s/%s/trunk", h.WorkspaceRoot, slug)
}

// PreCheck runs the merge pre-check phase:
//   - Resolves source and target branch refs
//   - Checks if the source is already merged into the target
//   - Checks if the source has any commits ahead of the target
//   - Runs dry-run conflict detection via merge-tree
//
// Returns (*PreCheckResult, nil) for AlreadyMerged (success path).
// Returns (nil, *MergeRejection) for WouldConflict or BranchNotReady.
// Returns (nil, error) for unexpected errors.
func (h *Handler) PreCheck(_ context.Context, _, _, _ string) (*PreCheckResult, error) {
	return nil, fmt.Errorf("merge: PreCheck not implemented")
}

// DryRunCheck invokes git merge-tree --write-tree to detect conflicts
// without modifying the working tree. Returns nil on clean merge, or
// *MergeRejection{Reason: WouldConflict} with ConflictFiles on conflicts.
func (h *Handler) DryRunCheck(_ context.Context, _, _, _ string) error {
	return fmt.Errorf("merge: DryRunCheck not implemented")
}

// FetchTarget fetches the latest target branch state from the upstream
// remote via go-git using resolved clone credentials.
func (h *Handler) FetchTarget(_ context.Context, _, _ string) error {
	return fmt.Errorf("merge: FetchTarget not implemented")
}

// RebaseSource captures the pre-rebase SHA of the source branch, then
// invokes git rebase <target-ref> in the workspace trunk directory.
// Returns the pre-rebase SHA on success.
func (h *Handler) RebaseSource(_ context.Context, _, _, _ string) (preRebaseSHA string, err error) {
	return "", fmt.Errorf("merge: RebaseSource not implemented")
}

// RunCheckCommand executes the workspace CHECK_COMMAND via 'sh -c' in the
// trunk directory with MERGE_TARGET, MERGE_SOURCE, and WORKSPACE_SLUG
// environment variables injected, enforcing CheckCommandTimeout.
func (h *Handler) RunCheckCommand(_ context.Context, _, _, _, _ string) error {
	return fmt.Errorf("merge: RunCheckCommand not implemented")
}

// MergeResult contains the successful outcome of a merge operation.
// BaseSHA is the target branch HEAD before the merge; MergedSHA is the
// target branch HEAD after the merge (the rebased source HEAD).
type MergeResult struct {
	BaseSHA   string `json:"base_sha"`
	MergedSHA string `json:"merged_sha"`
}

// CheckCommandError is returned when the workspace CHECK_COMMAND exits
// with a non-zero exit code.
type CheckCommandError struct {
	Output   string
	ExitCode int
}

func (e *CheckCommandError) Error() string {
	return fmt.Sprintf("check command failed (exit %d): %s", e.ExitCode, e.Output)
}

// RollbackFunc reverts the source branch to a previous SHA after a failed
// check command or other post-rebase failure. It executes
// 'git checkout <branch> && git reset --hard <sha>' in the workspace trunk.
type RollbackFunc func(ctx context.Context, trunkDir, branch, sha string) error

// RunCheckStep looks up CHECK_COMMAND for the workspace and, if set,
// executes it via RunCheckCommand. If CHECK_COMMAND is not set, the check
// step is skipped (returns executed=false, nil). If the check command
// fails or times out, it rolls back the rebase using the Rollback function
// and returns a permanent error with the check output.
func (h *Handler) RunCheckStep(_ context.Context, _, _, _, _ string) (executed bool, err error) {
	return false, fmt.Errorf("merge: RunCheckStep not implemented")
}

// UpdateTargetRef updates the target branch ref to point to newSHA using
// go-git reference update. This is a local ref update only — no remote
// push is performed.
func (h *Handler) UpdateTargetRef(_ context.Context, _, _, _ string) error {
	return fmt.Errorf("merge: UpdateTargetRef not implemented")
}

// DeleteSourceBranch deletes the source branch ref from the local
// repository via go-git reference deletion.
func (h *Handler) DeleteSourceBranch(_ context.Context, _, _ string) error {
	return fmt.Errorf("merge: DeleteSourceBranch not implemented")
}

// Finalize constructs a MergeResult from the pre-merge target HEAD
// (baseSHA) and the post-merge target HEAD (mergedSHA). Both must be
// 40-character hex SHAs.
func (h *Handler) Finalize(_, _ string) (*MergeResult, error) {
	return nil, fmt.Errorf("merge: Finalize not implemented")
}
