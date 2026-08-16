package workspace

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/agent-fox-dev/hub/internal/gitcmd"
	"github.com/agent-fox-dev/hub/internal/secrets"
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
//   - auth: optional transport.AuthMethod for authentication (nil = public repo)
//
// Returns the 40-character hex SHA of the HEAD commit, or an error.
type CloneFuncType func(ctx context.Context, path string, url string, depth int, singleBranch bool, refName string, auth transport.AuthMethod) (headSHA string, err error)

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
	workerCtx, cancel := context.WithCancel(ctx)
	q := &JobQueue{
		workers:       workers,
		workspaceRoot: workspaceRoot,
		db:            db,
		jobs:          make(chan CloneJob, workers*10),
		ctx:           workerCtx,
		cancel:        cancel,
		done:          make(chan struct{}),
	}

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-workerCtx.Done():
					return
				case job, ok := <-q.jobs:
					if !ok {
						return
					}
					// Check context again before processing — if cancelled
					// while we were waiting, discard the job (05-REQ-4.E1).
					if workerCtx.Err() != nil {
						return
					}
					processCloneJob(workerCtx, q.db, q.workspaceRoot, job)
				}
			}
		}()
	}

	// Close the done channel when all workers have exited.
	go func() {
		wg.Wait()
		close(q.done)
	}()

	return q
}

// Enqueue adds a clone job to the queue for processing by a worker goroutine.
// If the channel buffer is full, the call blocks until a worker frees capacity.
// A nil context (e.g. in test stubs) sends directly without cancellation support.
func (q *JobQueue) Enqueue(job CloneJob) {
	if q.ctx == nil {
		q.jobs <- job
		return
	}
	select {
	case q.jobs <- job:
	case <-q.ctx.Done():
		// Context cancelled; discard the job.
	}
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
	slug := job.Slug

	// Step 1: Set clone_status to "cloning".
	if err := updateCloneStatus(db, slug, "cloning", nil, nil); err != nil {
		log.Printf("clone job %q: failed to set cloning status: %v", slug, err)
		return
	}

	// Step 2: Check if workspace directory already exists (idempotency).
	wsDir := filepath.Join(workspaceRoot, slug)
	if _, err := os.Stat(wsDir); err == nil {
		// Directory exists; skip clone, set status to ready.
		if err := updateCloneStatus(db, slug, "ready", nil, nil); err != nil {
			log.Printf("clone job %q: failed to set ready status (idempotent): %v", slug, err)
		}
		return
	}

	// Step 2.5: Look up stored git credentials (09-REQ-6.1, 09-REQ-6.2, 09-REQ-6.3).
	store := secrets.NewStore(db)
	auth, authErr := resolveCloneAuth(store, slug)
	if authErr != nil {
		log.Printf("clone job %q: credential lookup failed: %v", slug, authErr)
		errMsg := fmt.Sprintf("credential lookup failed: %v", authErr)
		_ = updateCloneStatus(db, slug, "failed", nil, &errMsg)
		return
	}

	// Step 3: Create workspace directory and trunk subdirectory.
	trunkDir := filepath.Join(wsDir, "trunk")
	if err := os.MkdirAll(trunkDir, 0o755); err != nil {
		errMsg := err.Error()
		_ = updateCloneStatus(db, slug, "failed", nil, &errMsg)
		return
	}

	// Step 4: Build clone options and call cloneFn.
	depth := 0
	singleBranch := false
	refName := ""
	if job.Branch != nil {
		singleBranch = true
		refName = "refs/heads/" + *job.Branch
	}

	headSHA, cloneErr := cloneFn(ctx, trunkDir, job.GitURL, depth, singleBranch, refName, auth)

	// Step 5: Handle result.
	if cloneErr != nil {
		// Clone failed: remove any partially created workspace directory.
		_ = os.RemoveAll(wsDir)
		errMsg := cloneErr.Error()
		if err := updateCloneStatus(db, slug, "failed", nil, &errMsg); err != nil {
			log.Printf("clone job %q: failed to set failed status: %v", slug, err)
		}
		return
	}

	// Step 5.5: Post-clone carry-patch setup (15-REQ-6).
	// If workspace_mode is 'carry_patch', add upstream remote, set rerere
	// config, and create integration branch. Standard workspaces skip this
	// entirely (15-REQ-6.3). All post-clone setup failures are logged as
	// warnings and do not prevent the workspace from being marked ready
	// (15-REQ-6.2).
	ws, wsErr := getWorkspaceBySlug(db, slug)
	if wsErr == nil && ws != nil && ws.WorkspaceMode == "carry_patch" {
		carryPatchPostCloneSetup(ctx, trunkDir, ws)
	}

	// Step 6: Clone succeeded — record HEAD SHA and set status to ready.
	if err := updateCloneStatus(db, slug, "ready", &headSHA, nil); err != nil {
		log.Printf("clone job %q: failed to set ready status: %v", slug, err)
	}
}

// carryPatchPostCloneSetup performs post-clone configuration for carry_patch
// workspaces: adds the upstream remote, enables rerere, and creates the
// integration branch. All errors are logged as warnings; none prevent the
// workspace from being marked ready (15-REQ-6.2).
func carryPatchPostCloneSetup(ctx context.Context, trunkDir string, ws *Workspace) {
	runner, err := gitcmd.New(trunkDir, nil)
	if err != nil {
		log.Printf("clone job %q: warning: carry-patch setup: git runner init failed: %v", ws.Slug, err)
		return
	}

	// Step 1: Add upstream remote (15-REQ-6.1).
	if ws.UpstreamURL != nil && *ws.UpstreamURL != "" {
		if err := runner.RemoteAdd(ctx, "upstream", *ws.UpstreamURL); err != nil {
			log.Printf("clone job %q: warning: carry-patch setup: RemoteAdd: %v", ws.Slug, err)
		}
	}

	// Step 2: Set rerere configuration (15-REQ-6.1).
	if err := runner.ConfigSet(ctx, "rerere.enabled", "true"); err != nil {
		log.Printf("clone job %q: warning: carry-patch setup: ConfigSet rerere.enabled: %v", ws.Slug, err)
	}
	if err := runner.ConfigSet(ctx, "rerere.autoupdate", "true"); err != nil {
		log.Printf("clone job %q: warning: carry-patch setup: ConfigSet rerere.autoupdate: %v", ws.Slug, err)
	}

	// Step 3: Create integration branch (15-REQ-6.1).
	// Ideally the branch would be created at the upstream tracking branch
	// HEAD, but upstream refs are not available until the first fetch/sync.
	// Check for the upstream tracking ref; if absent, fall back to HEAD
	// and log a warning (15-REQ-6.E1).
	if ws.IntegrationBranch != nil && *ws.IntegrationBranch != "" {
		startPoint := "HEAD"
		if _, err := runner.Run(ctx, "rev-parse", "--verify", "refs/remotes/upstream/HEAD"); err != nil {
			log.Printf("clone job %q: warning: carry-patch setup: upstream tracking branch not available, creating integration branch at HEAD", ws.Slug)
		} else {
			startPoint = "refs/remotes/upstream/HEAD"
		}
		if err := runner.CreateBranch(ctx, *ws.IntegrationBranch, startPoint); err != nil {
			log.Printf("clone job %q: warning: carry-patch setup: CreateBranch %q: %v", ws.Slug, *ws.IntegrationBranch, err)
		}
	}
}

// updateCloneStatus updates the clone_status, head_sha, and clone_error
// fields for the workspace identified by slug.
func updateCloneStatus(db *sql.DB, slug string, cloneStatus string, headSHA *string, cloneError *string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(
		`UPDATE workspaces SET clone_status = ?, head_sha = ?, clone_error = ?, updated_at = ? WHERE slug = ?`,
		cloneStatus, headSHA, cloneError, now, slug,
	)
	if err != nil {
		return fmt.Errorf("update clone status for %q: %w", slug, err)
	}
	return nil
}

// getCloneFields retrieves the current clone_status, head_sha, and clone_error
// for the workspace identified by slug.
func getCloneFields(db *sql.DB, slug string) (cloneStatus string, headSHA *string, cloneError *string, err error) {
	err = db.QueryRow(
		`SELECT clone_status, head_sha, clone_error FROM workspaces WHERE slug = ?`,
		slug,
	).Scan(&cloneStatus, &headSHA, &cloneError)
	if err != nil {
		return "", nil, nil, fmt.Errorf("get clone fields for %q: %w", slug, err)
	}
	return cloneStatus, headSHA, cloneError, nil
}
