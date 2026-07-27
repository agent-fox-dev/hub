package workspace

import (
	"context"
	"database/sql"
	"fmt"
)

// CloneJob represents a unit of work for the clone job queue.
// Each job corresponds to a workspace that needs its git repository cloned.
type CloneJob struct {
	Slug   string
	GitURL string
	Branch *string // nil means clone the remote's default branch
}

// CloneFuncType is the signature for the function that performs a git clone.
// Parameters:
//   - ctx: context for cancellation
//   - path: local directory to clone into
//   - url: remote repository URL
//   - depth: clone depth (1 for shallow)
//   - singleBranch: whether to clone only one branch
//   - refName: branch reference name (e.g. "refs/heads/main"); empty for default
//
// Returns the 40-character hex SHA of the HEAD commit, or an error.
type CloneFuncType func(ctx context.Context, path string, url string, depth int, singleBranch bool, refName string) (headSHA string, err error)

// cloneFn is the injectable clone function used by processCloneJob.
// Tests replace it to capture arguments or simulate errors. The production
// default (set during server init) uses go-git PlainCloneContext.
var cloneFn CloneFuncType

// JobQueue manages an in-memory FIFO queue of clone jobs consumed by
// worker goroutines. The queue is backed by a buffered Go channel.
type JobQueue struct {
	workers       int
	workspaceRoot string
	db            *sql.DB
	jobs          chan CloneJob
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{} // closed when all workers have exited
}

// NewJobQueue creates a new job queue and starts the specified number of
// worker goroutines that consume clone jobs from the channel. Workers run
// until the provided context is cancelled.
//
// The queue channel is buffered to allow non-blocking enqueue in most cases.
// Workers process jobs sequentially per-worker, with up to `workers` jobs
// executing concurrently across all workers.
func NewJobQueue(ctx context.Context, db *sql.DB, workspaceRoot string, workers int) *JobQueue {
	// TODO: implement for spec 05-REQ-4.1
	return nil
}

// Enqueue adds a clone job to the queue for processing by a worker goroutine.
// If the channel buffer is full, the call blocks until a worker frees capacity.
func (q *JobQueue) Enqueue(job CloneJob) {
	// TODO: implement for spec 05-REQ-4.2
}

// WorkerCount returns the number of configured worker goroutines.
func (q *JobQueue) WorkerCount() int {
	if q == nil {
		return 0
	}
	return q.workers
}

// Wait blocks until all worker goroutines have exited after context cancellation.
func (q *JobQueue) Wait() {
	if q == nil || q.done == nil {
		return
	}
	<-q.done
}

// processCloneJob executes the clone operation for a single job:
//  1. Sets clone_status to "cloning"
//  2. Checks if workspace directory already exists (idempotency)
//  3. Creates workspace directory and performs shallow clone
//  4. On success: sets clone_status to "ready" and records head_sha
//  5. On failure: sets clone_status to "failed", records error, removes partial dir
func processCloneJob(ctx context.Context, db *sql.DB, workspaceRoot string, job CloneJob) {
	// TODO: implement for spec 05-REQ-4.2
}

// updateCloneStatus updates the clone_status, head_sha, and clone_error
// fields for the workspace identified by slug.
func updateCloneStatus(db *sql.DB, slug string, cloneStatus string, headSHA *string, cloneError *string) error {
	// TODO: implement for spec 05-REQ-4
	return fmt.Errorf("updateCloneStatus: not implemented")
}

// getCloneFields retrieves the current clone_status, head_sha, and clone_error
// for the workspace identified by slug.
func getCloneFields(db *sql.DB, slug string) (cloneStatus string, headSHA *string, cloneError *string, err error) {
	// TODO: implement for spec 05-REQ-4
	return "", nil, nil, fmt.Errorf("getCloneFields: not implemented")
}
