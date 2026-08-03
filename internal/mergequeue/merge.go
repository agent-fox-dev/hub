package mergequeue

import (
	"context"
	"database/sql"
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

// processMergeJob executes the full merge algorithm for a single job:
// resolve SHAs, dry-run conflict check (outside mutex), acquire mutex,
// rebase, check, push. It updates job status in the database at each
// stage transition.
func processMergeJob(ctx context.Context, db *sql.DB, job *MergeJob, git GitOps, mu BranchLocker) error {
	// TODO: implement merge pipeline
	return nil
}
