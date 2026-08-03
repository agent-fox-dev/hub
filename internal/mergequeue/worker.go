package mergequeue

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"
)

// Queue manages a FIFO merge queue with a single worker goroutine.
// The worker uses a three-way select on:
//   - stopCh (shutdown signal)
//   - wakeup channel (new job enqueued)
//   - pollInterval timer (fallback poll for backoff-delayed jobs)
type Queue struct {
	db           *sql.DB
	deps         MergeDeps
	canMerge     CanMergeFunc
	pollInterval time.Duration

	stopCh chan struct{}
	wakeup chan struct{}
	wg     sync.WaitGroup

	mu      sync.Mutex
	started bool
}

// NewQueue creates and returns a new merge Queue. The deps parameter
// provides git operations, branch locking, variable access, and optional
// hooks. canMerge is the pre-check function for merge eligibility.
func NewQueue(db *sql.DB, deps MergeDeps, canMerge CanMergeFunc) *Queue {
	return &Queue{
		db:           db,
		deps:         deps,
		canMerge:     canMerge,
		pollInterval: 10 * time.Second,
		stopCh:       make(chan struct{}),
		wakeup:       make(chan struct{}, 1),
	}
}

// Start begins the worker goroutine. Calling Start on an already-started
// Queue is a no-op — only one worker runs at a time.
func (q *Queue) Start() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.started {
		return
	}
	q.started = true
	q.wg.Add(1)
	go q.runWorker()
}

// Stop closes stopCh to broadcast the shutdown signal, then blocks on
// the internal WaitGroup until all in-flight merge operations complete.
// Stop returns only after all in-flight operations finish.
func (q *Queue) Stop() {
	q.mu.Lock()
	if !q.started {
		q.mu.Unlock()
		return
	}
	q.mu.Unlock()

	close(q.stopCh)
	q.wg.Wait()

	q.mu.Lock()
	q.started = false
	q.mu.Unlock()
}

// Notify performs a non-blocking send on the buffered(1) wakeup channel
// to interrupt the worker's poll sleep. Multiple rapid calls coalesce
// into a single wakeup since the channel has capacity 1.
func (q *Queue) Notify() {
	select {
	case q.wakeup <- struct{}{}:
	default:
	}
}

// workerRunning reports whether the worker goroutine is active.
func (q *Queue) workerRunning() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.started
}

// runWorker is the main worker goroutine loop. It uses a three-way select
// to wait for shutdown, wakeup, or the fallback poll timer.
func (q *Queue) runWorker() {
	defer q.wg.Done()

	timer := time.NewTimer(q.pollInterval)
	defer timer.Stop()

	for {
		select {
		case <-q.stopCh:
			return
		case <-q.wakeup:
			// Drain the timer if it happened to fire concurrently.
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
			// Fallback poll timer fired.
		}

		// Process all currently eligible jobs sequentially.
		q.processEligibleJobs()

		// Reset the timer for the next fallback poll interval.
		timer.Reset(q.pollInterval)
	}
}

// processEligibleJobs polls for eligible jobs and processes them one at a
// time until no more are available or the shutdown signal is received.
func (q *Queue) processEligibleJobs() {
	for {
		// Check for shutdown signal before picking up the next job.
		select {
		case <-q.stopCh:
			return
		default:
		}

		job, err := NextAvailableJob(q.db)
		if err != nil {
			slog.Error("failed to poll eligible merge jobs",
				"error", err,
			)
			return
		}
		if job == nil {
			return // No more eligible jobs.
		}

		q.processOneJob(job.ID)
	}
}

// processOneJob executes the full merge pipeline for a single job,
// recovering from panics to prevent the worker goroutine from dying.
func (q *Queue) processOneJob(jobID string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic in merge job processing recovered",
				"merge_job_id", jobID,
				"panic", r,
			)
		}
	}()

	if err := processJobByID(context.Background(), q.db, jobID, q.deps, q.canMerge); err != nil {
		slog.Error("failed to process merge job",
			"merge_job_id", jobID,
			"error", err,
		)
	}
}
