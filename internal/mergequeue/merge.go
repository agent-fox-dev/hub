package mergequeue

import (
	"context"
	"database/sql"
	"time"
)

// GitOps defines the git operations used by processMergeJob.
// In production, *gitcmd.GitRunner satisfies this interface.
type GitOps interface {
	Run(ctx context.Context, args ...string) (stdout []byte, stderr []byte, err error)
	RunExitCode(ctx context.Context, args ...string) (stdout []byte, stderr []byte, exitCode int, err error)
}

// BranchLocker provides per-target-branch mutual exclusion for merge
// operations. Lock blocks until the branch lock is acquired; Unlock releases
// it. Implementations must be safe for concurrent use.
type BranchLocker interface {
	Lock(branch string)
	Unlock(branch string)
}

// VariableGetter retrieves workspace variables by key.
// In production, this wraps the secrets.Store to look up CHECK_COMMAND
// and CHECK_TIMEOUT for a given workspace.
type VariableGetter interface {
	GetVariable(ctx context.Context, workspaceSlug, key string) (value string, found bool, err error)
}

// PostMergeHook is a function called after a successful merge when
// campaign_id is non-NULL. It notifies the campaign scheduler of the
// completed integration. Hook errors are logged but do not change
// the job status from merged.
type PostMergeHook func(ctx context.Context, job MergeJob) error

// CheckRunnerFunc executes a check command in the given directory with
// the specified timeout. It returns the combined stdout+stderr output
// and an error if the command failed or timed out.
// In production, this wraps os/exec.CommandContext with sh -c.
type CheckRunnerFunc func(ctx context.Context, dir string, command string, timeout time.Duration) (output []byte, err error)

// MergeDeps bundles the dependencies required by processMergeJob.
// All fields except Git and Locker are optional; when nil/zero the
// corresponding feature is skipped (e.g. no check command, no hook).
type MergeDeps struct {
	Git           GitOps
	Locker        BranchLocker
	Variables     VariableGetter
	Hook          PostMergeHook
	RunCheck      CheckRunnerFunc
	WorkspaceRoot string
}

// processMergeJob executes the full merge algorithm for a single job:
// resolve SHAs, dry-run conflict check (outside mutex), acquire mutex,
// validate nonce, set running, fetch, rebase, check, push.
// It updates job status in the database at each stage transition.
func processMergeJob(ctx context.Context, db *sql.DB, job *MergeJob, deps MergeDeps) error {
	// TODO: implement merge pipeline
	return nil
}
