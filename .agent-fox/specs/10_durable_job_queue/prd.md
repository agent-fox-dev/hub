---
spec_id: '10'
spec_name: durable_job_queue
title: Durable Job Queue
status: draft
created_at: '2026-08-04T05:10:07.435660+00:00'
updated_at: '2026-08-04T05:16:16.232089+00:00'
owner: ''
source: docs/prd/prd11job.md
schema_version: 1
---
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
- Shut down gracefully — in-flight jobs complete before the process exits
  (within the configured grace period).

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
  `JobQueue` (owned by the `workspace_checkout` spec) continues to work
  alongside this queue. Migration from the in-memory clone queue to the durable
  queue is optional and incremental; a future spec can address it if needed.
  The two queue implementations are fully independent — `durable_job_queue` has
  no dependency on `workspace_checkout`, and `workspace_checkout` is not
  required to adopt the durable queue.
- **Schema evolution.** `InitSchema` handles only the initial table creation.
  Schema migration for future column additions is deferred to a future spec.
  The hub is pre-production; schema changes are applied as DDL updates when
  needed.

## Relationship to Existing Specs

This spec is **fully independent** of the `workspace_checkout` spec. The
`workspace_checkout` spec owns the existing in-memory clone `JobQueue` and will
continue to do so. The durable job queue is introduced alongside it with no
planned migration. If a future spec migrates clone operations to the durable
queue, that spec will declare the dependency explicitly.

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
| `retry_count` | INTEGER | Number of retry attempts so far. Starts at 0; incremented before each backoff delay is computed. |
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

```go
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
3. Checks for an active job with the same `(type, key)` (status `queued` or
   `running`). If one exists, returns the existing job's ID and a
   `duplicate=true` flag — no new record is inserted.
4. Inserts a new job record with `status = 'queued'` and
   `available_at = now()`.
5. Sends a non-blocking signal on the wakeup channel.
6. Returns the job ID.

### Polling and Dispatch

The queue runs a configurable number of worker goroutines (default **4**,
set at queue construction time via a `WithWorkers(n int)` option — matching
the pattern of the existing clone `JobQueue`). All worker goroutines share a
**single buffered(1) wakeup channel**. When `Enqueue` is called, it performs
a non-blocking send on this shared channel, waking exactly one idle worker.
This is acceptable because workers loop continuously after processing a job —
checking for the next available job before sleeping — so a missed wakeup
costs at most one poll interval (default 5 s). The simplicity of a single
shared channel outweighs the marginal latency of occasionally missing a
wakeup when all workers are busy.

Each worker runs its own processing loop servicing all registered job types:

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
6. **Loop:** immediately check for the next available job. If none, wait on
   a three-way select: shutdown signal, poll timer (configurable, default 5s),
   or wakeup channel.

The producer (`Enqueue`) is responsible for sending on the wakeup channel.
There is no dedicated dispatcher goroutine.

### Exponential Backoff

When a handler returns a retryable error:

1. Increment `retry_count` (starting from 0, so the first failure sets it to 1).
2. If `retry_count > max_retries`: transition to `dead_letter`.
3. Otherwise: compute `delay = min(base * multiplier^retry_count, cap)`,
   set `available_at = now() + delay`, set `status = 'failed'`.

**Example backoff schedule** (base=2s, multiplier=2, cap=7200s):

| Failure | `retry_count` after increment | Delay |
|---------|-------------------------------|-------|
| 1st | 1 | `min(2 × 2¹, 7200)` = **4 s** |
| 2nd | 2 | `min(2 × 2², 7200)` = **8 s** |
| 3rd | 3 | `min(2 × 2³, 7200)` = **16 s** |
| … | … | … |
| 10th | 10 | `min(2 × 2¹⁰, 7200)` = **2048 s** |
| 11th+ | 11+ | **7200 s** (cap) |

The job remains in `failed` status until the promote step in the polling
loop transitions it back to `queued` when `available_at` passes.

### Dead-Letter Inspection

Dead-lettered jobs remain in the database for inspection. A consumer can:

- Query dead-lettered jobs by type.
- Manually requeue a dead-lettered job (resets `retry_count` to 0, sets
  `status = 'queued'`, sets `available_at = now()`).

**Manual requeue and duplicate prevention:** Before requeuing a dead-lettered
job, the queue applies the same duplicate-prevention check as a normal enqueue.
If an active (`queued` or `running`) job for the same `(type, key)` already
exists, the requeue is rejected and the existing active job's ID is returned.
This keeps the at-most-one-active-per-key invariant consistent across all
code paths.

These operations are exposed through programmatic APIs on the queue, not
REST endpoints. Consumers may choose to expose them through their own
endpoints.

### Schema Initialisation

The queue creates its `jobs` table and associated indexes programmatically at
startup using `CREATE TABLE IF NOT EXISTS` and `CREATE INDEX IF NOT EXISTS`
statements, executed as part of an `InitSchema(db *sql.DB) error` function
called when the queue is initialised. This matches the pattern established by
`internal/secrets/schema.go` and other hub packages — no separate migration
file or migration framework is required.

`InitSchema` handles only the initial creation case. If the `jobs` table
already exists (e.g., on hub restart), `CREATE TABLE IF NOT EXISTS` succeeds
silently and no schema validation is performed. Schema evolution for future
column additions is out of scope and deferred to a future spec.

**Required indexes** (to be created by `InitSchema`):

| Index | Columns | Purpose |
|-------|---------|---------|
| `idx_jobs_nonce` | `nonce` (UNIQUE) | Fast nonce deduplication lookup. |
| `idx_jobs_type_key_status` | `type`, `key`, `status` | Duplicate-prevention and per-key serialization check. |
| `idx_jobs_status_available_at` | `status`, `available_at` | Polling query: find next claimable `queued` job. |
| `idx_jobs_type_created_at` | `type`, `created_at` | `ListByType` pagination ordered by `created_at`. |

**SQLite connection requirements:** The caller's `*sql.DB` **must** be
configured with `PRAGMA journal_mode=WAL` and a `PRAGMA busy_timeout` (e.g.,
5000 ms) before passing it to the queue. WAL mode is required for concurrent
workers to read and write without serialising on the database lock. The queue
does **not** set these pragmas itself — it documents the requirement and
relies on the hub's existing startup configuration. If WAL mode is not
enabled, concurrent workers will experience `SQLITE_BUSY` errors under load.

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
3. In-flight handler calls are allowed to run to completion. The queue waits
   up to a **configurable grace period (default 30 seconds)** for all workers
   to exit.
4. If the grace period expires before all workers have finished, the queue
   cancels the in-flight handler contexts, logs a warning that lists each
   interrupted job ID, and exits anyway. It does **not** wait indefinitely.
5. `Wait()` blocks until all workers have exited or the grace period has
   elapsed.

No job is left in a partially-executed state with a dirty external resource
under normal shutdown. Interrupted jobs (grace period exceeded) are reset to
`queued` on the next startup via crash recovery.

### Query and Management API

The queue exposes the following programmatic methods for consumers:

- **GetByID(id string):** returns a single job by its UUID, or an error if
  not found.
- **ListByType(type string, opts ListOpts):** returns jobs of the given type,
  optionally filtered by status and/or key. Supports pagination via
  `offset`/`limit`. Results are ordered by `created_at` descending.
- **ListByKey(type string, key string):** returns all jobs for a specific
  type and key combination, ordered by `created_at` descending.
- **CountByStatus(type string):** returns a map of status → count for all
  jobs of the given type. Useful for monitoring and dashboards.
- **CancelJob(id string) error:** cancels a job. Only `queued` jobs can be
  cancelled; the job transitions to `cancelled`. This method is idempotent:
  cancelling an already-`cancelled` job succeeds silently (returns `nil`).
  Cancelling a job in `running`, `completed`, or `dead_letter` status returns
  `ErrNotCancellable`. Consumers (e.g., a merge endpoint) call this method
  to implement cancel endpoints in their own domain.

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
nonce, same type+key). The same duplicate-prevention check applies to the
manual requeue of dead-lettered jobs (see Dead-Letter Inspection above).

### Logging Contract

The queue uses `log/slog` (Go standard library structured logging). Every log
line includes the structured fields `job_id`, `type`, `key`, `status`, and
`retry_count` where applicable.

| Event | Level |
|-------|-------|
| Job claimed (transitions to `running`) | `DEBUG` |
| Job completed (transitions to `completed`) | `DEBUG` |
| Job failed, will retry (transitions to `failed`) | `DEBUG` |
| Job promoted from `failed` back to `queued` | `DEBUG` |
| Job dead-lettered (transitions to `dead_letter`) | `WARN` |
| Job cancelled | `DEBUG` |
| Crash recovery: job reset from `running` to `queued` on startup | `WARN` |
| Graceful shutdown: grace period exceeded, interrupting job | `WARN` (one line per interrupted job, including `job_id`) |
| Queue started / stopped | `INFO` |

No other log levels (e.g., ERROR) are used by the queue itself. Handler
errors surface through the job's `error` field and the state transition logs
above.

## Technical Boundaries

- **Language:** Go (1.26+)
- **Storage:** SQLite via the hub's existing `*sql.DB` connection (must be
  configured with WAL mode and busy_timeout by the caller). No additional
  dependencies.
- **Package:** `internal/jobqueue/` — a single package with no domain-specific
  imports.
- **Concurrency:** Go channels and goroutines. No external message broker.
  All worker goroutines share a single buffered(1) wakeup channel.
- **Worker count:** Default 4 goroutines, configurable at construction time
  via `WithWorkers(n int)`.
- **Logging:** `log/slog` (standard library). Structured fields on every log
  line. See Logging Contract above.
- **Test tooling:** stdlib `testing` package only. Tests use an in-memory
  SQLite database via `modernc.org/sqlite` with the `":memory:"` DSN,
  matching the existing hub test suite. No `testify` or external test
  frameworks.

## Dependencies

None beyond the hub's `*sql.DB` connection (WAL mode required) and standard
library. The job queue is a foundational package with no dependencies on other
hub subsystems. It is fully independent of the `workspace_checkout` spec and
its in-memory clone `JobQueue`.

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
   track what they've submitted. The same check applies to manual requeue of
   dead-lettered jobs, keeping the invariant consistent across all paths.

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

5. **Richer query API: GetByID, ListByType, ListByKey, CountByStatus, CancelJob.**
   The merge REST endpoints need to list and get individual merge jobs, which
   requires at minimum GetByID and ListByType. ListByKey and CountByStatus
   add observability for monitoring and debugging without significant
   implementation cost. CancelJob is included as a first-class method because
   consumers need a clear, idempotent API for cancel workflows; returning
   `ErrNotCancellable` for terminal or in-progress jobs gives consumers a
   precise error to propagate.

6. **Programmatic schema initialisation via `InitSchema`.** The `jobs` table
   and its indexes are created with `CREATE TABLE IF NOT EXISTS` at queue
   startup, matching the pattern of `internal/secrets/schema.go` and other
   hub packages. This avoids introducing a migration framework dependency and
   keeps the queue self-contained. Schema evolution is deferred to a future
   spec.

7. **Fixed default of 4 workers, configurable via `WithWorkers`.** Four
   workers allows meaningful concurrency for jobs with different keys without
   over-provisioning resources. The option mirrors the existing clone
   `JobQueue` constructor, which also accepts a worker-count parameter.

8. **30-second graceful shutdown with forced exit and warning log.** A
   bounded grace period prevents the hub from hanging indefinitely on a
   misbehaving handler. Interrupted jobs are logged by ID at WARN level and
   recovered on next startup via the crash-recovery scan, so no work is
   silently lost.

9. **Fully independent of `workspace_checkout`.** The durable queue and the
   existing in-memory clone queue coexist without interdependency. Migration
   is deferred to a future spec to avoid coupling the introduction of the
   durable queue to an unrelated subsystem change.

10. **Single shared wakeup channel; one worker woken per enqueue.** All
    workers share one buffered(1) channel. `Enqueue` performs a non-blocking
    send, waking at most one idle worker. If all workers are busy, the next
    poll timer fires (default 5 s) and all workers check again. The simplicity
    of a single channel outweighs the marginal latency of a missed wakeup.

11. **WAL mode and busy_timeout are caller's responsibility.** The queue
    documents the requirement but does not set SQLite pragmas itself,
    preserving the caller's ability to configure the connection consistently
    at hub startup. Overriding pragmas inside the queue could conflict with
    hub-level settings.

12. **retry_count incremented before backoff formula.** `retry_count` starts
    at 0 and is incremented first, so the first backoff delay is
    `base × multiplier¹` (4 s with defaults), not `base × multiplier⁰`
    (2 s). This matches the intuition that the first retry waits longer than
    the base delay and produces a cleaner backoff schedule.

13. **Logging via `log/slog` with structured fields.** Consistent structured
    fields (`job_id`, `type`, `key`, `status`, `retry_count`) on every log
    line enable log-based alerting and debugging without additional
    instrumentation. WARN-level logs for dead-letter and interrupted-job
    events are the minimum signal operators need to detect persistent
    failures.
