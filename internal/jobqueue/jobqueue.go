// Package jobqueue provides a durable, SQLite-backed job queue with retry,
// per-key serialization, crash recovery, and graceful shutdown.
package jobqueue

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"
)

// HandlerFunc is the signature for job type handlers.
// On success (err == nil), result is serialized as JSON and stored.
// On failure, retryable indicates whether the job should be retried.
type HandlerFunc func(ctx context.Context, payload json.RawMessage) (result any, retryable bool, err error)

// RetryPolicy configures exponential backoff for a job type.
// Zero-value fields are replaced with defaults: Base 2s, Multiplier 2,
// Cap 2h, MaxRetries 20.
type RetryPolicy struct {
	Base       time.Duration
	Multiplier float64
	Cap        time.Duration
	MaxRetries int
}

// EnqueueParams holds the parameters for enqueueing a new job.
type EnqueueParams struct {
	Type        string
	Key         string
	Nonce       string
	Payload     json.RawMessage
	SubmittedBy string
}

// Job represents a persistent job record in the jobs table.
type Job struct {
	ID          string
	Type        string
	Key         string
	Nonce       string
	Status      string
	Payload     json.RawMessage
	Result      json.RawMessage
	Error       string
	RetryCount  int
	AvailableAt time.Time
	SubmittedBy string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Queue is the durable job queue backed by SQLite.
type Queue struct {
	wakeupCh chan struct{}
}

// Option configures the Queue constructor.
type Option func(*Queue)

// InitSchema creates the jobs table and required indexes using
// CREATE TABLE IF NOT EXISTS and CREATE INDEX IF NOT EXISTS.
// The caller's *sql.DB must be configured with PRAGMA journal_mode=WAL
// and PRAGMA busy_timeout before calling this function.
func InitSchema(_ *sql.DB) error {
	return nil
}

// New creates a new Queue instance.
func New(_ *sql.DB, _ *slog.Logger, _ ...Option) *Queue {
	return &Queue{}
}

// Register registers a job type with its handler and optional retry policy.
// Must be called before the queue is started.
func (q *Queue) Register(_ string, _ HandlerFunc, _ *RetryPolicy) error {
	return nil
}

// Enqueue enqueues a new job for execution.
func (q *Queue) Enqueue(_ EnqueueParams) (jobID string, duplicate bool, err error) {
	return "", false, nil
}

// WithWorkers sets the number of worker goroutines (default 4).
func WithWorkers(_ int) Option {
	return func(q *Queue) {}
}

// WithPollInterval sets the duration between poll cycles (default 5s).
func WithPollInterval(_ time.Duration) Option {
	return func(q *Queue) {}
}

// Start performs crash recovery and launches worker goroutines.
func (q *Queue) Start() error {
	return nil
}

// Stop initiates graceful shutdown: signals workers to stop claiming
// new jobs and waits up to the grace period for in-flight handlers to
// finish.
func (q *Queue) Stop() error {
	return nil
}

// GetByID returns a single job record by its UUID.
func (q *Queue) GetByID(_ string) (*Job, error) {
	return nil, nil
}
