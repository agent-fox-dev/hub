# Durable Job Queue

## Intent

The hub needs to execute background work reliably: clone repositories, merge
branches, sync with upstream, and (in the future) run checks, send
notifications, and manage agent lifecycles. Today the only background
processing is the clone `JobQueue` — an in-memory Go channel with worker
goroutines. It works but has no persistence, no retry logic, no backoff, and
no crash recovery. A job in flight when the hub restarts is silently lost.

This PRD defines a generic, durable job queue backed by SQLite. Job types are
registered with handler functions. Jobs are persisted before execution, retried
on transient failure, and dead-lettered on persistent failure. The queue is an
internal building block — it has no REST API of its own. Consumers (merge
operations, sync, future subsystems) register handlers and expose their own
endpoints.

## Goals

- Provide a single durable job queue that any hub subsystem can use by
  registering a job type and handler function.
- Persist jobs in SQLite so that in-flight work survives hub restarts.
- Retry failed jobs with exponential backoff, and dead-letter jobs that
  exceed the retry limit for operator inspection.
- Guarantee exactly-once execution via nonce-based idempotency.
- Provide per-key serialization so that jobs sharing a logical resource
  (e.g., the same target branch) execute sequentially while unrelated jobs
  run concurrently.
- Support low-latency dispatch via wakeup-on-enqueue without busy-waiting.
- Shut down gracefully — in-flight jobs complete before the process exits.

## Non-Goals

- **REST API for jobs.** The queue is an internal facility. Consumers expose
  their own domain-specific endpoints (e.g., `/merges`, `/sync`) and map
  those to job operations internally.
- **Distributed queue.** The queue runs in-process backed by the hub's SQLite
  database. Multi-node distribution is out of scope.
- **Priority levels.** Jobs are processed FIFO within each serialization key.
  Priority ordering adds complexity without a demonstrated need.
- **Job chaining or workflows.** Composing jobs into multi-step pipelines is
  the consumer's responsibility. The queue processes individual jobs.
- **Replacing the clone queue immediately.** The existing in-memory clone
  `JobQueue` continues to work. Migration to the durable queue is optional
  and incremental.

## Functional Requirements

### Job Model

A job represents a unit of background work.

| Field | Type | Description |
|-------|------|-------------|
| `id` | TEXT (UUID) | Unique job identifier, generated on creation. |
| `type` | TEXT | Registered job type (e.g., `merge`, `sync`). |
| `key` | TEXT | Serialization key. Jobs with the same `type` and `key` execute sequentially. Jobs with different keys execute concurrently. |
| `nonce` | TEXT (unique) | Cryptographic nonce for idempotency. Callers generate the nonce; the queue rejects duplicates. |
| `status` | TEXT | Current job state (see state machine below). |
| `payload` | TEXT (JSON) | Job-type-specific input data. Opaque to the queue. |
| `result` | TEXT (JSON, nullable) | Handler output on success. Opaque to the queue. |
| `error` | TEXT (nullable) | Error message from the most recent failed attempt. |
| `retry_count` | INTEGER | Number of retry attempts so far. |
| `available_at` | TEXT (RFC 3339) | Job is invisible to the queue until this time. Used for backoff. |
| `submitted_by` | TEXT | Identifier of the submitter (user ID, agent ID, or system). |
| `created_at` | TEXT (RFC 3339) | When the job was created. |
| `updated_at` | TEXT (RFC 3339) | When the job was last modified. |

### Job Status State Machine

```
enqueue ──► queued ──► running ──► completed
                │          │
                │          ├──► failed ──► queued (when available_at passes)
                │          │
                │          └──► dead_letter (retries exhausted or permanent error)
                │
                └──► cancelled
```

| Status | Meaning |
|--------|---------|
| `queued` | Waiting for a worker. Visible to the polling query when `available_at <= now()`. |
| `running` | A worker has claimed the job and is executing the handler. |
| `completed` | Handler returned success. `result` contains the output. |
| `failed` | Handler returned a retryable error. `error` contains the message. The job remains in `failed` status with an updated `available_at`. The polling loop transitions it back to `queued` when `available_at` passes. This gives consumers visibility into retry state. |
| `dead_letter` | Retry limit exceeded or handler returned a permanent error. The job is preserved for inspection but will not be retried automatically. |
| `cancelled` | Cancelled by the caller before execution started. Only `queued` jobs can be cancelled. |

### Job Type Registration

Consumers register a job type with the queue at startup:

- **Type name:** a short string identifier (e.g., `"merge"`, `"sync"`).
- **Handler function:** called by the queue worker to process the job.
  Receives the job's `payload` (as raw JSON bytes) and a context. Returns
  a result (JSON-serializable), an error, and a boolean indicating whether
  the error is retryable.
- **Retry policy (optional):** base delay, multiplier, cap, and max retries.
  Defaults: base 2s, multiplier 2x, cap 2h, max retries 20.

Registering a type name that already exists is a startup error (fail fast).

The handler function signature:

```
func(ctx context.Context, payload json.RawMessage) (result any, retryable bool, err error)
```

- On `err == nil`: job transitions to `completed`, `result` is stored.
- On `err != nil && retryable`: job transitions to `failed`. The job remains
  in `failed` status (with the error message visible) until `available_at`
  passes, then the polling loop transitions it back to `queued`.
- On `err != nil && !retryable`: job transitions to `dead_letter`.

### Per-Key Serialization

Jobs are serialized by the combination of `type` + `key`. The queue ensures
that at most one job with a given `(type, key)` pair is in `running` status at
any time. Other jobs with the same key remain `queued` and are picked up after
the running job completes.

This allows consumers to express domain-specific serialization constraints
without the queue understanding the domain. For example, the merge consumer
uses the target branch as the key — merges to the same branch are serialized,
merges to different branches run concurrently.

### Enqueueing

To enqueue a job, the caller provides: `type`, `key`, `nonce`, `payload`,
and `submitted_by`. The queue:

1. Validates that the `type` is registered.
2. Checks the `nonce` for uniqueness. If a job with the same nonce already
   exists, returns the existing job (idempotent — no error).
3. Inserts a new job record with `status = 'queued'` and
   `available_at = now()`.
4. Sends a non-blocking signal on the wakeup channel.
5. Returns the job ID.

### Polling and Dispatch

The queue runs a single processing loop that services all registered job
types:

1. **Promote:** transition any `failed` jobs whose `available_at <= now()`
   back to `queued`.
2. **Poll:** query for the oldest `queued` job (any type) where
   `available_at <= now()` and no other job with the same `(type, key)` is
   currently `running`.
3. **Claim:** atomically set `status = 'running'` (using a WHERE clause
   that includes `status = 'queued'` to prevent double-claim).
4. **Execute:** call the registered handler function for the job's type.
5. **Finalize:** update the job based on the handler result (completed,
   failed with retry, or dead-letter).
6. **Loop:** check for the next available job. If none, wait on a three-way
   select: shutdown signal, poll timer (configurable, default 5s), or
   wakeup channel.

The wakeup channel is `buffered(1)`. Multiple rapid enqueues coalesce into a
single wakeup. This ensures newly enqueued jobs are picked up within
milliseconds rather than waiting for the next poll cycle.

### Exponential Backoff

When a handler returns a retryable error:

1. Increment `retry_count`.
2. If `retry_count > max_retries`: transition to `dead_letter`.
3. Otherwise: compute `delay = min(base * multiplier^retry_count, cap)`,
   set `available_at = now() + delay`, set `status = 'failed'`.

The job remains in `failed` status until the promote step in the polling
loop transitions it back to `queued` when `available_at` passes.

### Dead-Letter Inspection

Dead-lettered jobs remain in the database for inspection. A consumer can:

- Query dead-lettered jobs by type.
- Manually requeue a dead-lettered job (resets `retry_count` to 0, sets
  `status = 'queued'`, sets `available_at = now()`).

These operations are exposed through programmatic APIs on the queue, not
REST endpoints. Consumers may choose to expose them through their own
endpoints.

### Crash Recovery

On hub startup, the queue scans for jobs in `running` status. These represent
jobs that were interrupted by a crash or restart. The queue resets them to
`queued` with `available_at = now()` so they are re-dispatched.

This is safe because handlers must be idempotent or tolerate re-execution
after a partial run. The queue documents this contract — handler authors are
responsible for idempotency.

### Graceful Shutdown

When the hub receives a shutdown signal:

1. The queue closes the `stopCh` channel (broadcast to all worker loops).
2. Worker loops stop claiming new jobs.
3. In-flight handler calls run to completion (or until the context is
   cancelled with a configurable grace period).
4. `Wait()` blocks until all workers have exited.

No job is left in a partially-executed state with a dirty external resource.

### Query API

The queue exposes programmatic query methods for consumers to look up jobs:

- **GetByID(id string):** returns a single job by its UUID, or an error if
  not found.
- **ListByType(type string, opts ListOpts):** returns jobs of the given type,
  optionally filtered by status and/or key. Supports pagination via
  `offset`/`limit`. Results are ordered by `created_at` descending.
- **ListByKey(type string, key string):** returns all jobs for a specific
  type and key combination, ordered by `created_at` descending.
- **CountByStatus(type string):** returns a map of status → count for all
  jobs of the given type. Useful for monitoring and dashboards.

These methods return the full job record including `payload` and `result`.
Consumers project domain-specific fields from the JSON payload and result in
their own endpoints.

### Duplicate Prevention

A given `(type, key)` may have at most one job in `queued` or `running`
status at any time. Attempting to enqueue a job with a `(type, key)` pair
that already has an active job returns the existing job's ID and a flag
indicating it was a duplicate. This prevents unbounded queue growth from
repeated submissions.

The `nonce` check is separate from duplicate prevention — it catches
retransmissions of the exact same request (same nonce), while duplicate
prevention catches semantically equivalent but distinct requests (different
nonce, same type+key).

## Technical Boundaries

- **Language:** Go (1.26+)
- **Storage:** SQLite via the hub's existing `*sql.DB` connection. No
  additional dependencies.
- **Package:** `internal/jobqueue/` — a single package with no domain-specific
  imports.
- **Concurrency:** Go channels and goroutines. No external message broker.

## Dependencies

None. The job queue is a foundational package with no dependencies on other
hub subsystems. It depends only on the hub's `*sql.DB` connection and
standard library.

## Design Decisions

1. **Drop per-type concurrency; keep only per-key serialization.** The PRD
   originally defined both per-type concurrency (max N jobs of a type running
   simultaneously) and per-key serialization. These are redundant in practice:
   per-key serialization already prevents concurrent execution within a key,
   and the queue runs as many distinct keys in parallel as worker goroutines
   allow. Removing per-type concurrency simplifies the registration API and
   eliminates a tuning knob that would rarely be adjusted.

2. **Reject duplicate (type, key) submissions.** When a `(type, key)` pair
   already has a `queued` or `running` job, new submissions with a different
   nonce are rejected with the existing job's ID returned. This prevents
   unbounded queue growth from repeated submissions and gives callers a clear
   signal to wait. The alternative — accepting and queuing multiple jobs per
   key — enables fire-and-forget but risks accumulation when callers don't
   track what they've submitted.

3. **`failed` is a stored, visible status.** When a handler returns a
   retryable error, the job transitions to `failed` and remains there until
   `available_at` passes. The polling loop's promote step then transitions it
   back to `queued`. This gives consumers visibility into retry state ("this
   job failed and is waiting to retry at time X") rather than hiding retries
   behind a `queued` status with an opaque `available_at`.

4. **Accept `*sql.DB`, not `*apikit.DB`.** All existing internal packages
   (workspace, secrets, vars) accept `*sql.DB` directly. The job queue
   follows this convention for consistency. If the codebase migrates to
   `*apikit.DB` in the future, the job queue can be updated then.

5. **Richer query API: GetByID, ListByType, ListByKey, CountByStatus.** The
   merge REST endpoints need to list and get individual merge jobs, which
   requires at minimum GetByID and ListByType. ListByKey and CountByStatus
   add observability for monitoring and debugging without significant
   implementation cost.
