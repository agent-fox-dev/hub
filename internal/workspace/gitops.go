package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrAlreadyUpToDate is a sentinel error matching go-git's
// NoErrAlreadyUpToDate. The archive handler treats this push result
// as success (there was simply nothing new to push).
var ErrAlreadyUpToDate = errors.New("already up-to-date")

// ArchiveOpenAndPushFuncType opens a local git repository at repoPath
// and pushes to origin. Returns nil on success, ErrAlreadyUpToDate
// when the remote already has all local commits (nothing to push), or
// an error on failure (including open failures and push rejections).
type ArchiveOpenAndPushFuncType func(repoPath string) error

// ArchiveHeadFuncType reads the 40-character hex SHA of HEAD from
// a local repository at repoPath. Called after a successful push
// (or ErrAlreadyUpToDate) to record the head_sha.
type ArchiveHeadFuncType func(repoPath string) (string, error)

// archiveOpenAndPushFn is the injectable function for the git push
// during archive. Tests replace it to capture calls and simulate push
// outcomes. The production default (set during server init) uses
// go-git PlainOpen and repo.Push.
var archiveOpenAndPushFn ArchiveOpenAndPushFuncType

// archiveHeadFn is the injectable function for reading HEAD SHA
// during archive. Tests replace it to return known SHAs or errors.
// The production default uses go-git repo.Head().Hash().String().
var archiveHeadFn ArchiveHeadFuncType

// defaultWorkspaceRoot holds the resolved workspace root directory
// path (WORKSPACE_ROOT). Set during server boot; used by handlers
// to locate and manage workspace directories on disk.
var defaultWorkspaceRoot string

// defaultQueue holds the in-memory job queue for clone/reclone
// operations. Set during server boot; used by handlers to enqueue
// clone and reclone jobs.
var defaultQueue *JobQueue

// InitCloneQueue sets the production clone function, workspace root, and
// starts the in-memory job queue with the configured number of workers.
// Called during server boot after EnsureWorkspaceRoot.
func InitCloneQueue(ctx context.Context, db *sql.DB, workspaceRoot string, workers int) {
	cloneFn = defaultCloneFn
	archiveOpenAndPushFn = defaultArchiveOpenAndPushFn
	archiveHeadFn = defaultArchiveHeadFn
	defaultWorkspaceRoot = workspaceRoot
	defaultQueue = NewJobQueue(ctx, db, workspaceRoot, workers)
}

// validCloneTransitions defines the allowed clone_status state machine.
// Each key is a current state; the value is the set of valid target states.
var validCloneTransitions = map[string]map[string]bool{
	"pending":  {"cloning": true, "archived": true},
	"cloning":  {"ready": true, "failed": true},
	"ready":    {"archived": true},
	"failed":   {"archived": true},
	"archived": {"pending": true},
}

// ValidateCloneStatusTransition checks whether a clone_status transition
// from the current state to the target state is valid according to the
// state machine defined in 05-REQ-9.1:
//
//	pending   -> cloning
//	cloning   -> ready | failed
//	ready     -> archived
//	pending   -> archived
//	failed    -> archived
//	archived  -> pending
//
// Returns nil if the transition is valid, or an error describing why
// the transition is rejected.
func ValidateCloneStatusTransition(from, to string) error {
	targets, ok := validCloneTransitions[from]
	if !ok {
		return fmt.Errorf("invalid clone_status %q", from)
	}
	if !targets[to] {
		return fmt.Errorf("invalid clone_status transition from %q to %q", from, to)
	}
	return nil
}
