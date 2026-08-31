// Package jobqueue provides a durable, SQLite-backed job queue with retry,
// per-key serialization, crash recovery, and graceful shutdown.
package jobqueue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Status constants for job lifecycle states.
const (
	StatusQueued     = "queued"
	StatusRunning    = "running"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
	StatusDeadLetter = "dead_letter"
	StatusCancelled  = "cancelled"
)

// ErrNotCancellable is returned by CancelJob when the target job is in a
// status that cannot be cancelled (running, completed, or dead_letter).
var ErrNotCancellable = errors.New("job is not in a cancellable status")

// HandlerFunc is the signature for job type handlers.
// On success (err == nil), result is serialized as JSON and stored.
// On failure, retryable indicates whether the job should be retried.
// Handlers must be idempotent because crash recovery may re-dispatch a
// job whose handler had already partially executed.
type HandlerFunc func(ctx context.Context, payload json.RawMessage) (result any, retryable bool, err error)

// jobIDContextKey is a package-private key for storing the job ID in context.
type jobIDContextKey struct{}

// JobIDFromContext extracts the job ID from a context injected by the worker
// during handler dispatch. Returns an empty string if no job ID is present.
func JobIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(jobIDContextKey{}).(string)
	return id
}

// ContextWithJobID returns a new context with the given job ID set. This is
// primarily useful for testing handlers outside of the queue worker loop.
func ContextWithJobID(ctx context.Context, jobID string) context.Context {
	return context.WithValue(ctx, jobIDContextKey{}, jobID)
}

// RetryPolicy configures exponential backoff for a job type.
// Zero-value fields are replaced with defaults: Base 2s, Multiplier 2,
// Cap 2h, MaxRetries 20.
type RetryPolicy struct {
	Base       time.Duration
	Multiplier float64
	Cap        time.Duration
	MaxRetries int
}

// DefaultRetryPolicy returns a RetryPolicy with default values:
// Base=2s, Multiplier=2, Cap=2h, MaxRetries=20.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		Base:       2 * time.Second,
		Multiplier: 2,
		Cap:        2 * time.Hour,
		MaxRetries: 20,
	}
}

// withDefaults returns a copy of the policy with zero-value fields replaced
// by defaults. MaxRetries is only defaulted when at least one other field has
// been explicitly set — a bare RetryPolicy{MaxRetries: 0} is honoured as
// "no retries" (10-REQ-6.E3), while RetryPolicy{Base: 10s} defaults
// MaxRetries to 20. When the caller passes nil to Register, DefaultRetryPolicy
// supplies all defaults including MaxRetries=20.
func (p RetryPolicy) withDefaults() RetryPolicy {
	d := DefaultRetryPolicy()
	hasOtherFields := p.Base != 0 || p.Multiplier != 0 || p.Cap != 0
	if p.Base == 0 {
		p.Base = d.Base
	}
	if p.Multiplier == 0 {
		p.Multiplier = d.Multiplier
	}
	if p.Cap == 0 {
		p.Cap = d.Cap
	}
	if p.MaxRetries == 0 && hasOtherFields {
		p.MaxRetries = d.MaxRetries
	}
	return p
}

// EnqueueParams holds the parameters for enqueueing a new job.
type EnqueueParams struct {
	Type        string
	Key         string
	Nonce       string
	Payload     json.RawMessage
	SubmittedBy string
	Group       string // Optional: when non-empty, used as group_key for group serialization.
}

// ListOpts carries optional filter and pagination parameters for ListByType.
type ListOpts struct {
	Status string
	Key    string
	Offset int
	Limit  int
}

// Job represents a persistent job record in the jobs table.
type Job struct {
	ID          string
	Type        string
	Key         string
	GroupKey    string // Group serialization key; empty for legacy per-key serialization.
	Nonce       string
	Status      string
	Payload     json.RawMessage
	Result      json.RawMessage
	Error       string
	Progress    json.RawMessage // Intermediate progress data written during execution.
	RetryCount  int
	AvailableAt time.Time
	SubmittedBy string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// registration holds a registered handler and its retry policy.
type registration struct {
	handler HandlerFunc
	policy  RetryPolicy
}

// inflight tracks a running handler's cancel function and job metadata.
type inflight struct {
	cancel context.CancelFunc
	jobID  string
}

// Queue is the durable job queue backed by SQLite.
type Queue struct {
	db           *sql.DB
	logger       *slog.Logger
	workerCount  int
	pollInterval time.Duration
	gracePeriod  time.Duration

	// Handler registry: type name -> registration.
	mu       sync.RWMutex
	handlers map[string]registration

	// Lifecycle state.
	started    bool
	startedMu  sync.Mutex
	stopCh     chan struct{}
	shutdownFn sync.Once
	wg         sync.WaitGroup
	wakeupCh   chan struct{}

	// In-flight handler tracking for graceful shutdown.
	inflightMu sync.Mutex
	inflights  map[int]*inflight // workerID -> inflight
}

// Option configures the Queue constructor.
type Option func(*Queue) error

// WithWorkers sets the number of worker goroutines (default 4).
func WithWorkers(n int) Option {
	return func(q *Queue) error {
		if n <= 0 {
			return fmt.Errorf("jobqueue: worker count must be positive, got %d", n)
		}
		q.workerCount = n
		return nil
	}
}

// WithPollInterval sets the duration between poll cycles (default 5s).
func WithPollInterval(d time.Duration) Option {
	return func(q *Queue) error {
		q.pollInterval = d
		return nil
	}
}

// WithGracePeriod sets the maximum duration to wait for in-flight handlers
// to complete during graceful shutdown (default 30s).
func WithGracePeriod(d time.Duration) Option {
	return func(q *Queue) error {
		q.gracePeriod = d
		return nil
	}
}

// New creates a new Queue instance with default configuration (4 workers,
// 5s poll interval, 30s grace period) overridden by any provided options.
// Does not start worker goroutines.
// Returns (nil, non-nil error) if configuration is invalid or db is nil.
func New(db *sql.DB, logger *slog.Logger, opts ...Option) (*Queue, error) {
	if db == nil {
		return nil, fmt.Errorf("jobqueue: db must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}

	q := &Queue{
		db:           db,
		logger:       logger,
		workerCount:  4,
		pollInterval: 5 * time.Second,
		gracePeriod:  30 * time.Second,
		handlers:     make(map[string]registration),
		stopCh:       make(chan struct{}),
		wakeupCh:     make(chan struct{}, 1),
		inflights:    make(map[int]*inflight),
	}

	for _, opt := range opts {
		if err := opt(q); err != nil {
			return nil, err
		}
	}

	return q, nil
}

// Register registers a job type with its handler and optional retry policy.
// Must be called before the queue is started.
// Returns an error if the type name is empty, the handler is nil, the type
// is already registered, or the queue has already been started.
func (q *Queue) Register(typeName string, handler HandlerFunc, policy *RetryPolicy) error {
	if typeName == "" {
		return fmt.Errorf("jobqueue: type name must not be empty")
	}
	if handler == nil {
		return fmt.Errorf("jobqueue: handler must not be nil")
	}

	q.startedMu.Lock()
	isStarted := q.started
	q.startedMu.Unlock()

	if isStarted {
		return fmt.Errorf("jobqueue: cannot register type %q after queue has started", typeName)
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	if _, exists := q.handlers[typeName]; exists {
		return fmt.Errorf("jobqueue: type %q already registered", typeName)
	}

	var rp RetryPolicy
	if policy != nil {
		rp = policy.withDefaults()
	} else {
		rp = DefaultRetryPolicy()
	}

	q.handlers[typeName] = registration{
		handler: handler,
		policy:  rp,
	}

	return nil
}

// getPolicy returns the retry policy registered for the given type name,
// with defaults applied for any zero-value fields.
func (q *Queue) getPolicy(typeName string) RetryPolicy {
	q.mu.RLock()
	defer q.mu.RUnlock()
	reg, ok := q.handlers[typeName]
	if !ok {
		return DefaultRetryPolicy()
	}
	return reg.policy
}

// getHandler returns the handler for the given type name.
func (q *Queue) getHandler(typeName string) (HandlerFunc, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	reg, ok := q.handlers[typeName]
	if !ok {
		return nil, false
	}
	return reg.handler, true
}

// Enqueue enqueues a new job for execution.
// It validates the type is registered, checks nonce uniqueness, checks for
// active (type, key) duplicates, and inserts the job record.
// Returns (jobID, duplicate, err) where duplicate=true indicates a (type, key)
// dedup match and duplicate=false indicates either a new job or a nonce match.
func (q *Queue) Enqueue(params EnqueueParams) (jobID string, duplicate bool, err error) {
	// Validate required fields.
	if params.Nonce == "" {
		return "", false, fmt.Errorf("jobqueue: nonce must not be empty")
	}
	if params.Key == "" {
		return "", false, fmt.Errorf("jobqueue: key must not be empty")
	}

	// Validate type is registered.
	q.mu.RLock()
	_, registered := q.handlers[params.Type]
	q.mu.RUnlock()
	if !registered {
		return "", false, fmt.Errorf("jobqueue: type %q not registered", params.Type)
	}

	// Check nonce uniqueness: if a job with this nonce exists, return it.
	var existingID string
	nonceErr := q.db.QueryRow(
		"SELECT id FROM jobs WHERE nonce = ?", params.Nonce,
	).Scan(&existingID)
	if nonceErr == nil {
		// Nonce match: return existing job (idempotent retransmission).
		return existingID, false, nil
	}
	if nonceErr != sql.ErrNoRows {
		return "", false, fmt.Errorf("jobqueue: nonce check: %w", nonceErr)
	}

	// Check for active (type, key) duplicate: queued or running.
	var activeID string
	activeErr := q.db.QueryRow(
		"SELECT id FROM jobs WHERE type = ? AND key = ? AND status IN (?, ?)",
		params.Type, params.Key, StatusQueued, StatusRunning,
	).Scan(&activeID)
	if activeErr == nil {
		// Active job exists for this (type, key).
		return activeID, true, nil
	}
	if activeErr != sql.ErrNoRows {
		return "", false, fmt.Errorf("jobqueue: active check: %w", activeErr)
	}

	// Insert new job.
	id := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)

	_, insertErr := q.db.Exec(
		`INSERT INTO jobs (id, type, key, nonce, status, payload, result, error,
		  retry_count, available_at, submitted_by, group_key, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, 0, ?, ?, ?, ?, ?)`,
		id, params.Type, params.Key, params.Nonce, StatusQueued,
		string(params.Payload), now, params.SubmittedBy, params.Group, now, now,
	)
	if insertErr != nil {
		// Handle concurrent nonce collision: the unique index on nonce
		// causes the INSERT to fail. Re-check for the existing record and
		// return it as an idempotent duplicate (10-REQ-13.E1).
		var raceID string
		raceErr := q.db.QueryRow(
			"SELECT id FROM jobs WHERE nonce = ?", params.Nonce,
		).Scan(&raceID)
		if raceErr == nil {
			return raceID, false, nil
		}

		// Also handle concurrent (type, key) active job race (10-REQ-3.E5):
		// another goroutine inserted a job with the same (type, key) between
		// our check and our INSERT.
		var raceActiveID string
		raceActiveErr := q.db.QueryRow(
			"SELECT id FROM jobs WHERE type = ? AND key = ? AND status IN (?, ?)",
			params.Type, params.Key, StatusQueued, StatusRunning,
		).Scan(&raceActiveID)
		if raceActiveErr == nil {
			return raceActiveID, true, nil
		}

		return "", false, fmt.Errorf("jobqueue: insert job: %w", insertErr)
	}

	// Non-blocking send on wakeup channel to wake an idle worker.
	select {
	case q.wakeupCh <- struct{}{}:
	default:
	}

	return id, false, nil
}

// Start performs crash recovery and launches worker goroutines.
// Returns an error if crash recovery fails or if the queue is already started.
func (q *Queue) Start() error {
	q.startedMu.Lock()
	if q.started {
		q.startedMu.Unlock()
		return fmt.Errorf("jobqueue: queue is already started")
	}
	q.started = true
	q.startedMu.Unlock()

	// Crash recovery: reset all running jobs back to queued.
	if err := q.recoverRunningJobs(); err != nil {
		// Roll back started state since workers were never launched.
		q.startedMu.Lock()
		q.started = false
		q.startedMu.Unlock()
		return fmt.Errorf("jobqueue: crash recovery failed: %w", err)
	}

	// Start worker goroutines.
	for i := 0; i < q.workerCount; i++ {
		q.wg.Add(1)
		go q.workerLoop(i)
	}

	q.logger.Info("queue started", "workers", q.workerCount)

	return nil
}

// recoverRunningJobs resets any jobs in running status back to queued with
// available_at set to now. This handles the case where the hub crashed or was
// killed while handlers were executing. Handlers must be idempotent because
// crash recovery may re-dispatch a job whose handler had already partially
// executed.
func (q *Queue) recoverRunningJobs() error {
	rows, err := q.db.Query(
		"SELECT id FROM jobs WHERE status = ?", StatusRunning,
	)
	if err != nil {
		return fmt.Errorf("query running jobs: %w", err)
	}

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan running job id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate running jobs: %w", err)
	}
	rows.Close()

	if len(ids) == 0 {
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for _, id := range ids {
		_, err := q.db.Exec(
			"UPDATE jobs SET status = ?, available_at = ?, updated_at = ? WHERE id = ?",
			StatusQueued, now, now, id,
		)
		if err != nil {
			return fmt.Errorf("reset job %s: %w", id, err)
		}
		q.logger.Warn("crash recovery: reset running job to queued",
			"job_id", id,
		)
	}

	return nil
}

// workerLoop is the main loop for a worker goroutine.
// It polls for available jobs, claims them, and executes their handlers.
func (q *Queue) workerLoop(workerID int) {
	defer q.wg.Done()

	for {
		select {
		case <-q.stopCh:
			return
		default:
		}

		// Promote failed jobs whose available_at has passed.
		q.promoteFailedJobs()

		// Try to claim and execute a job.
		claimed := q.claimAndExecute(workerID)

		if !claimed {
			// No work available; wait for wakeup or poll interval.
			select {
			case <-q.stopCh:
				return
			case <-q.wakeupCh:
				// New job enqueued; loop back to poll.
			case <-time.After(q.pollInterval):
				// Poll interval elapsed; loop back to poll.
			}
		}
	}
}

// promoteFailedJobs transitions failed jobs whose available_at has passed
// back to queued status so they can be re-polled (10-REQ-4.1, 10-REQ-6.3).
// If the query or update fails, the error is logged and the worker continues
// to the poll step (10-REQ-4.E3).
func (q *Queue) promoteFailedJobs() {
	now := time.Now().UTC().Format(time.RFC3339)

	// Collect job metadata first, then close rows before running updates.
	// This avoids holding the connection open during Exec calls,
	// which would deadlock with MaxOpenConns(1).
	type promotable struct {
		id         string
		typ        string
		key        string
		retryCount int
	}

	rows, err := q.db.Query(
		"SELECT id, type, key, retry_count FROM jobs WHERE status = ? AND available_at <= ?",
		StatusFailed, now,
	)
	if err != nil {
		q.logger.Debug("promote step query failed", "error", err.Error())
		return
	}

	var jobs []promotable
	for rows.Next() {
		var p promotable
		if err := rows.Scan(&p.id, &p.typ, &p.key, &p.retryCount); err != nil {
			continue
		}
		jobs = append(jobs, p)
	}
	rows.Close()

	for _, j := range jobs {
		_, updateErr := q.db.Exec(
			"UPDATE jobs SET status = ?, updated_at = ? WHERE id = ? AND status = ?",
			StatusQueued, now, j.id, StatusFailed,
		)
		if updateErr != nil {
			q.logger.Debug("promote step update failed",
				"job_id", j.id,
				"error", updateErr.Error(),
			)
			continue
		}
		q.logger.Debug("promoted failed job to queued",
			"job_id", j.id,
			"type", j.typ,
			"key", j.key,
			"status", StatusQueued,
			"retry_count", j.retryCount,
		)
	}
}

// claimAndExecute attempts to claim the oldest available queued job and
// execute its handler. Returns true if a job was claimed and executed.
func (q *Queue) claimAndExecute(workerID int) bool {
	now := time.Now().UTC().Format(time.RFC3339)

	// Find the oldest queued job whose available_at has passed and that
	// has no active (running) job for the same (type, effective serialization key).
	// The effective serialization key is: CASE WHEN group_key != '' THEN group_key ELSE key END
	// This preserves per-key serialization for legacy jobs (group_key='') while
	// enabling per-group serialization for merge jobs (12-REQ-1.4, 12-REQ-1.5).
	var id, typ, key, nonce, payloadStr, submittedBy, createdAt string
	var retryCount int
	err := q.db.QueryRow(`
		SELECT id, type, key, nonce, payload, retry_count, submitted_by, created_at
		FROM jobs
		WHERE status = ? AND available_at <= ?
		  AND NOT EXISTS (
		    SELECT 1 FROM jobs AS j2
		    WHERE j2.type = jobs.type
		      AND (CASE WHEN j2.group_key != '' THEN j2.group_key ELSE j2.key END) =
		          (CASE WHEN jobs.group_key != '' THEN jobs.group_key ELSE jobs.key END)
		      AND j2.status = ? AND j2.id != jobs.id
		  )
		ORDER BY available_at ASC, created_at ASC
		LIMIT 1`,
		StatusQueued, now, StatusRunning,
	).Scan(&id, &typ, &key, &nonce, &payloadStr, &retryCount, &submittedBy, &createdAt)
	if err != nil {
		if err != sql.ErrNoRows {
			q.logger.Debug("poll query failed", "error", err.Error())
		}
		return false
	}

	// Atomically claim: update status to running WHERE status=queued
	// (10-REQ-4.2). The WHERE guard ensures exactly one worker claims the
	// job even under concurrent polling (10-REQ-4.E1).
	res, claimErr := q.db.Exec(
		"UPDATE jobs SET status = ?, updated_at = ? WHERE id = ? AND status = ?",
		StatusRunning, now, id, StatusQueued,
	)
	if claimErr != nil {
		q.logger.Debug("claim update failed",
			"job_id", id,
			"error", claimErr.Error(),
		)
		return false
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return false // Someone else claimed it.
	}

	q.logger.Debug("job claimed",
		"job_id", id,
		"type", typ,
		"key", key,
		"status", StatusRunning,
		"retry_count", retryCount,
	)

	// Execute the handler.
	handler, ok := q.getHandler(typ)
	if !ok {
		// No registered handler for this type — dead-letter the job
		// (10-REQ-4.E4). This can happen if a handler was removed after
		// the job was enqueued.
		noHandlerErr := fmt.Errorf("no handler registered for type %q", typ)
		q.logger.Warn("no handler registered for job type",
			"job_id", id,
			"type", typ,
			"key", key,
			"status", StatusDeadLetter,
			"retry_count", retryCount,
		)
		q.finalizeJob(id, typ, key, retryCount, nil, false, noHandlerErr)
		return true
	}

	ctx, cancel := context.WithCancel(context.Background())
	ctx = context.WithValue(ctx, jobIDContextKey{}, id)

	// Track inflight handler for graceful shutdown context cancellation.
	q.inflightMu.Lock()
	q.inflights[workerID] = &inflight{cancel: cancel, jobID: id}
	q.inflightMu.Unlock()

	// Run handler, capturing panics.
	result, retryable, handlerErr := q.safeCallHandler(ctx, cancel, handler, json.RawMessage(payloadStr), id)

	// Remove from inflight tracking.
	q.inflightMu.Lock()
	delete(q.inflights, workerID)
	q.inflightMu.Unlock()
	cancel()

	q.finalizeJob(id, typ, key, retryCount, result, retryable, handlerErr)
	return true
}

// safeCallHandler invokes the handler, recovering from panics.
func (q *Queue) safeCallHandler(ctx context.Context, cancel context.CancelFunc, handler HandlerFunc, payload json.RawMessage, jobID string) (result any, retryable bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("handler panicked: %v", r)
			retryable = false
		}
	}()

	return handler(ctx, payload)
}

// finalizeJob updates the job record after handler execution.
func (q *Queue) finalizeJob(id, typ, key string, retryCount int, result any, retryable bool, handlerErr error) {
	now := time.Now().UTC().Format(time.RFC3339)
	policy := q.getPolicy(typ)

	if handlerErr == nil {
		// Success: store result and mark completed.
		var resultJSON []byte
		if result != nil {
			var marshalErr error
			resultJSON, marshalErr = json.Marshal(result)
			if marshalErr != nil {
				// Serialization failure: dead-letter the job (10-REQ-5.E2).
				_, _ = q.db.Exec(
					"UPDATE jobs SET status = ?, error = ?, updated_at = ? WHERE id = ?",
					StatusDeadLetter, fmt.Sprintf("result serialization failed: %v", marshalErr), now, id,
				)
				q.logger.Warn("job dead-lettered",
					"job_id", id,
					"type", typ,
					"key", key,
					"status", StatusDeadLetter,
					"retry_count", retryCount,
					"error", marshalErr.Error(),
				)
				return
			}
		}

		var resultPtr *string
		if resultJSON != nil {
			s := string(resultJSON)
			resultPtr = &s
		}

		_, _ = q.db.Exec(
			"UPDATE jobs SET status = ?, result = ?, updated_at = ? WHERE id = ?",
			StatusCompleted, resultPtr, now, id,
		)
		q.logger.Debug("job completed",
			"job_id", id,
			"type", typ,
			"key", key,
			"status", StatusCompleted,
			"retry_count", retryCount,
		)
		return
	}

	// Error path.
	errMsg := handlerErr.Error()

	if !retryable {
		// Non-retryable (permanent) error: dead-letter without incrementing
		// retry_count (10-REQ-5.4).
		_, _ = q.db.Exec(
			"UPDATE jobs SET status = ?, error = ?, updated_at = ? WHERE id = ?",
			StatusDeadLetter, errMsg, now, id,
		)
		q.logger.Warn("job dead-lettered",
			"job_id", id,
			"type", typ,
			"key", key,
			"status", StatusDeadLetter,
			"retry_count", retryCount,
			"error", errMsg,
		)
		return
	}

	// Retryable error: always increment retry_count first (10-REQ-5.2).
	newRetryCount := retryCount + 1

	if newRetryCount > policy.MaxRetries {
		// Retries exhausted after increment: dead-letter with the
		// incremented retry_count stored (10-REQ-5.3, 10-REQ-6.E1).
		_, _ = q.db.Exec(
			"UPDATE jobs SET status = ?, error = ?, retry_count = ?, updated_at = ? WHERE id = ?",
			StatusDeadLetter, errMsg, newRetryCount, now, id,
		)
		q.logger.Warn("job dead-lettered",
			"job_id", id,
			"type", typ,
			"key", key,
			"status", StatusDeadLetter,
			"retry_count", newRetryCount,
			"error", errMsg,
		)
		return
	}

	// Retryable with retries remaining: compute backoff delay, set
	// status=failed, and schedule re-poll (10-REQ-5.2, 10-REQ-6.1).
	delay := computeBackoff(policy, newRetryCount)
	availableAt := time.Now().UTC().Add(delay).Format(time.RFC3339)

	_, _ = q.db.Exec(
		"UPDATE jobs SET status = ?, error = ?, retry_count = ?, available_at = ?, updated_at = ? WHERE id = ?",
		StatusFailed, errMsg, newRetryCount, availableAt, now, id,
	)
	q.logger.Debug("job failed with retry",
		"job_id", id,
		"type", typ,
		"key", key,
		"status", StatusFailed,
		"retry_count", newRetryCount,
		"error", errMsg,
	)
}

// computeBackoff computes the retry delay as
// min(base * multiplier^retryCount, cap).
// The retryCount parameter should already be incremented before calling.
// If the computed value overflows, it is clamped to cap.
func computeBackoff(policy RetryPolicy, retryCount int) time.Duration {
	delay := policy.Base
	for i := 0; i < retryCount; i++ {
		delay = time.Duration(float64(delay) * policy.Multiplier)
		if delay > policy.Cap || delay <= 0 {
			return policy.Cap
		}
	}
	if delay > policy.Cap {
		return policy.Cap
	}
	return delay
}

// Shutdown initiates graceful shutdown by closing the stop channel to
// broadcast a stop signal to all worker goroutines. Workers stop claiming
// new jobs and complete their current handler calls.
// Multiple calls are safe (protected by sync.Once).
func (q *Queue) Shutdown() {
	q.shutdownFn.Do(func() {
		close(q.stopCh)
	})
}

// Wait blocks until all workers have exited or the grace period expires.
// If the grace period expires before all workers finish, in-flight handler
// contexts are cancelled, a WARN is logged per interrupted job, and Wait
// returns. Returns nil on clean shutdown.
func (q *Queue) Wait() error {
	done := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		q.logger.Info("queue stopped")
		return nil
	case <-time.After(q.gracePeriod):
		// Cancel all in-flight handler contexts and log each one.
		q.inflightMu.Lock()
		for _, inf := range q.inflights {
			inf.cancel()
			q.logger.Warn("grace period expired, interrupting handler",
				"job_id", inf.jobID,
			)
		}
		q.inflightMu.Unlock()

		// Give handlers a brief moment to react to context cancellation.
		briefDone := make(chan struct{})
		go func() {
			q.wg.Wait()
			close(briefDone)
		}()
		select {
		case <-briefDone:
		case <-time.After(500 * time.Millisecond):
		}

		q.logger.Info("queue stopped")
		return nil
	}
}

// Stop initiates graceful shutdown and waits for completion.
// Equivalent to calling Shutdown() followed by Wait().
func (q *Queue) Stop() error {
	q.Shutdown()
	return q.Wait()
}

// CancelJob transitions a queued job to cancelled status.
// Returns nil on success or if the job is already cancelled (idempotent).
// Returns ErrNotCancellable for jobs in running, completed, or dead_letter status.
// Returns a not-found error if the job ID does not exist.
func (q *Queue) CancelJob(jobID string) error {
	var status, typ, key string
	var retryCount int
	err := q.db.QueryRow(
		"SELECT status, type, key, retry_count FROM jobs WHERE id = ?", jobID,
	).Scan(&status, &typ, &key, &retryCount)
	if err == sql.ErrNoRows {
		return fmt.Errorf("jobqueue: job %q not found", jobID)
	}
	if err != nil {
		return fmt.Errorf("jobqueue: query job: %w", err)
	}

	switch status {
	case StatusCancelled:
		return nil // Idempotent (10-REQ-10.2).
	case StatusQueued:
		// Can be cancelled.
	default:
		// running, completed, dead_letter, failed → not cancellable (10-REQ-10.3).
		return ErrNotCancellable
	}

	// Atomic cancel with WHERE status='queued' guard so a concurrent worker
	// claim (queued→running) causes 0 rows affected (10-REQ-10.E2).
	now := time.Now().UTC().Format(time.RFC3339)
	res, execErr := q.db.Exec(
		"UPDATE jobs SET status = ?, updated_at = ? WHERE id = ? AND status = ?",
		StatusCancelled, now, jobID, StatusQueued,
	)
	if execErr != nil {
		return fmt.Errorf("jobqueue: cancel job: %w", execErr)
	}

	affected, _ := res.RowsAffected()
	if affected == 0 {
		// The job was concurrently claimed by a worker (now running).
		return ErrNotCancellable
	}

	q.logger.Debug("job cancelled",
		"job_id", jobID,
		"type", typ,
		"key", key,
		"status", StatusCancelled,
		"retry_count", retryCount,
	)

	return nil
}

// GetByID returns a single job record by its UUID.
func (q *Queue) GetByID(jobID string) (*Job, error) {
	var j Job
	var payload, result, errStr, progress sql.NullString
	var availableAt, createdAt, updatedAt string

	err := q.db.QueryRow(
		`SELECT id, type, key, group_key, nonce, status, payload, result, error,
		  progress, retry_count, available_at, submitted_by, created_at, updated_at
		 FROM jobs WHERE id = ?`, jobID,
	).Scan(&j.ID, &j.Type, &j.Key, &j.GroupKey, &j.Nonce, &j.Status, &payload,
		&result, &errStr, &progress, &j.RetryCount, &availableAt, &j.SubmittedBy,
		&createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("jobqueue: job %q not found", jobID)
	}
	if err != nil {
		return nil, fmt.Errorf("jobqueue: query job: %w", err)
	}

	if payload.Valid {
		j.Payload = json.RawMessage(payload.String)
	}
	if result.Valid {
		j.Result = json.RawMessage(result.String)
	}
	if errStr.Valid {
		j.Error = errStr.String
	}
	if progress.Valid {
		j.Progress = json.RawMessage(progress.String)
	}

	j.AvailableAt, _ = time.Parse(time.RFC3339, availableAt)
	j.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	j.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)

	return &j, nil
}

// ListByType returns jobs of the given type filtered by optional status and
// key fields in opts, ordered by created_at descending, with pagination
// applied via offset and limit. Returns an empty slice (not nil) when no
// jobs match.
func (q *Queue) ListByType(typeName string, opts ListOpts) ([]*Job, error) {
	query := "SELECT id, type, key, group_key, nonce, status, payload, result, error, progress, retry_count, available_at, submitted_by, created_at, updated_at FROM jobs WHERE type = ?"
	args := []any{typeName}

	if opts.Status != "" {
		query += " AND status = ?"
		args = append(args, opts.Status)
	}
	if opts.Key != "" {
		query += " AND key = ?"
		args = append(args, opts.Key)
	}

	query += " ORDER BY created_at DESC"

	// Apply default limit of 50 when caller passes 0 (10-REQ-11.E2).
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	query += " LIMIT ?"
	args = append(args, limit)

	if opts.Offset > 0 {
		query += " OFFSET ?"
		args = append(args, opts.Offset)
	}

	return q.queryJobs(query, args...)
}

// ListByKey returns all job records for the given (type, key) combination
// ordered by created_at descending. Returns an empty slice (not nil) when
// no jobs match.
func (q *Queue) ListByKey(typeName string, key string) ([]*Job, error) {
	return q.queryJobs(
		`SELECT id, type, key, group_key, nonce, status, payload, result, error,
		  progress, retry_count, available_at, submitted_by, created_at, updated_at
		 FROM jobs WHERE type = ? AND key = ? ORDER BY created_at DESC`,
		typeName, key,
	)
}

// CountByStatus returns a map of status string to integer count for all
// jobs of the given type. Statuses with zero jobs are omitted from the map.
func (q *Queue) CountByStatus(typeName string) (map[string]int, error) {
	rows, err := q.db.Query(
		"SELECT status, COUNT(*) FROM jobs WHERE type = ? GROUP BY status",
		typeName,
	)
	if err != nil {
		return nil, fmt.Errorf("jobqueue: count by status: %w", err)
	}
	defer rows.Close()

	result := make(map[string]int)
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("jobqueue: scan count: %w", err)
		}
		result[status] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobqueue: count rows: %w", err)
	}
	return result, nil
}

// RequeueDeadLetter requeues a dead-lettered job for re-execution.
// It resets retry_count to 0, sets status to queued, sets available_at
// to now(), and sends a non-blocking signal on the wakeup channel.
// Returns (jobID, nil) on success.
// Returns (existingActiveJobID, error) if an active job exists for
// the same (type, key).
// Returns ("", error) if the job is not in dead_letter status or
// does not exist.
func (q *Queue) RequeueDeadLetter(jobID string) (string, error) {
	var typ, key, status string
	err := q.db.QueryRow(
		"SELECT type, key, status FROM jobs WHERE id = ?", jobID,
	).Scan(&typ, &key, &status)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("jobqueue: job %q not found", jobID)
	}
	if err != nil {
		return "", fmt.Errorf("jobqueue: query job: %w", err)
	}

	if status != StatusDeadLetter {
		return "", fmt.Errorf("jobqueue: job %q is not in dead_letter status (current: %q)", jobID, status)
	}

	// Check for active (type, key) duplicate.
	var activeID string
	activeErr := q.db.QueryRow(
		"SELECT id FROM jobs WHERE type = ? AND key = ? AND status IN (?, ?)",
		typ, key, StatusQueued, StatusRunning,
	).Scan(&activeID)
	if activeErr == nil {
		return activeID, fmt.Errorf("jobqueue: active job %q exists for (%s, %s)", activeID, typ, key)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	res, execErr := q.db.Exec(
		"UPDATE jobs SET status = ?, retry_count = 0, available_at = ?, updated_at = ? WHERE id = ? AND status = ?",
		StatusQueued, now, now, jobID, StatusDeadLetter,
	)
	if execErr != nil {
		return "", fmt.Errorf("jobqueue: requeue: %w", execErr)
	}

	affected, _ := res.RowsAffected()
	if affected == 0 {
		// Another concurrent RequeueDeadLetter call already requeued this job.
		// The job is now active (queued), so return it as a duplicate (10-REQ-7.E2).
		var nowActiveID string
		activeErr := q.db.QueryRow(
			"SELECT id FROM jobs WHERE type = ? AND key = ? AND status IN (?, ?)",
			typ, key, StatusQueued, StatusRunning,
		).Scan(&nowActiveID)
		if activeErr == nil {
			return nowActiveID, fmt.Errorf("jobqueue: active job %q exists for (%s, %s)", nowActiveID, typ, key)
		}
		return "", fmt.Errorf("jobqueue: job %q was concurrently modified", jobID)
	}

	// Wake a worker.
	select {
	case q.wakeupCh <- struct{}{}:
	default:
	}

	return jobID, nil
}

// UpdateProgress persists serialized progress JSON to the progress column
// for a running job. The data parameter is marshalled to JSON before writing.
// This is intended to be called during handler execution to record intermediate
// progress (e.g., per-patch rebuild results).
func (q *Queue) UpdateProgress(jobID string, data any) error {
	progressJSON, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("jobqueue: marshal progress: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = q.db.Exec(
		"UPDATE jobs SET progress = ?, updated_at = ? WHERE id = ?",
		string(progressJSON), now, jobID,
	)
	if err != nil {
		return fmt.Errorf("jobqueue: update progress: %w", err)
	}
	return nil
}

// queryJobs is a helper that runs a query and scans results into Job slices.
func (q *Queue) queryJobs(query string, args ...any) ([]*Job, error) {
	rows, err := q.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("jobqueue: query: %w", err)
	}
	defer rows.Close()

	jobs := make([]*Job, 0)
	for rows.Next() {
		var j Job
		var payload, result, errStr, progress sql.NullString
		var availableAt, createdAt, updatedAt string

		if err := rows.Scan(&j.ID, &j.Type, &j.Key, &j.GroupKey, &j.Nonce, &j.Status,
			&payload, &result, &errStr, &progress, &j.RetryCount, &availableAt,
			&j.SubmittedBy, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("jobqueue: scan: %w", err)
		}

		if payload.Valid {
			j.Payload = json.RawMessage(payload.String)
		}
		if result.Valid {
			j.Result = json.RawMessage(result.String)
		}
		if errStr.Valid {
			j.Error = errStr.String
		}
		if progress.Valid {
			j.Progress = json.RawMessage(progress.String)
		}

		j.AvailableAt, _ = time.Parse(time.RFC3339, availableAt)
		j.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		j.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)

		jobs = append(jobs, &j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("jobqueue: rows: %w", err)
	}

	return jobs, nil
}
