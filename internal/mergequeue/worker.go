package mergequeue

import (
	"database/sql"
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
	// TODO: implement worker goroutine startup
}

// Stop closes stopCh to broadcast the shutdown signal, then blocks on
// the internal WaitGroup until all in-flight merge operations complete.
// Stop returns only after all in-flight operations finish.
func (q *Queue) Stop() {
	// TODO: implement graceful shutdown
}

// Notify performs a non-blocking send on the buffered(1) wakeup channel
// to interrupt the worker's poll sleep. Multiple rapid calls coalesce
// into a single wakeup since the channel has capacity 1.
func (q *Queue) Notify() {
	// TODO: implement wakeup notification
}

// workerRunning reports whether the worker goroutine is active.
func (q *Queue) workerRunning() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.started
}
