---
spec_id: '11'
spec_name: mergequeue
title: Mergequeue
status: draft
created_at: '2026-08-03T12:46:14.191439+00:00'
updated_at: '2026-08-03T13:05:40.716319+00:00'
owner: ''
source: docs/prd/prd8.md
schema_version: 1
---
# Merge Queue

## Intent

When multiple agents complete work on separate spec branches, their changes
must be integrated into the shared integration branch safely — without merge
races, without leaving the repository in a dirty state, and without silently
dropping work. The merge queue serializes these integration operations using
a FIFO queue with rebase-then-fast-forward semantics.

## Background

The merge queue is part of the campaign execution system described in the
parent PRD (`docs/prd/prd8.md`). Prior art, motivating context, and the
broader campaign architecture are documented there. This spec focuses solely
on the merge serialization subsystem. In brief: when multiple agents complete
work concurrently on separate spec branches, naive parallel merges produce
race conditions that leave the integration branch in an inconsistent or dirty
state. The merge queue is the component that eliminates those races by
serializing all integration operations through a single FIFO queue per target
branch.

The hub runs as a **single process per deployment**. This is a deliberate
architectural simplification documented in the parent PRD. As a result,
an in-process per-target-branch mutex is sufficient for merge serialization;
no distributed lock or database-level advisory lock is required.

## Goals

- Provide a sequential FIFO merge queue that serializes merge operations per
  target branch using rebase-then-fast-forward.
- Model merge jobs with lifecycle states, typed rejection reasons, dry-run
  conflict checks, exponential backoff, and dead-letter handling.
- Support both campaign-linked merges (which trigger post-merge campaign
  actions) and standalone merges (which skip campaign logic).
- Expose REST endpoints for submitting, listing, querying, cancelling, and
  requeuing merge jobs.
- Implement prepare-then-enqueue with nonce idempotency to prevent
  double-merges after crashes or retries.
- Use wakeup-on-enqueue (buffered(1) channel) for low-latency dispatch
  without busy-waiting.
- Implement graceful shutdown so in-flight merge operations complete before
  the process exits.

## Non-Goals

- **Campaign lifecycle management.** The merge queue notifies the campaign
  scheduler via a post-merge hook after successful merges, but does not manage
  campaigns, DAGs, or spec branch creation. That belongs in the campaign
  package.
- **Cascading rebase.** Post-merge rebase of sibling spec branches is a
  campaign concern. The merge queue fires the hook; the campaign scheduler
  handles the cascade.
- **Agent dispatch.** The merge queue does not interact with agents directly.
- **Parallel merge execution.** Merges are strictly sequential (FIFO) per
  target branch.
- **Automatic conflict resolution.** The merge queue detects and reports
  conflicts; it does not resolve them.
- **Prometheus metrics / observability dashboards.** Structured logging is
  sufficient for this iteration. Metrics are a future concern.
- **Distributed/multi-instance locking.** The hub runs as a single process;
  no cross-process coordination mechanism is needed or provided.
- **Rate limiting on merge submission endpoints.** The queue's backoff/dead-letter
  mechanism and duplicate-submission rejection (at most one queued/running job
  per spec) provide sufficient protection for the expected workload. Rate limiting
  can be added in a future iteration if needed.

## Design

### Merge Jobs

A merge job represents a request to integrate a source branch into a target
branch within a workspace.

Fields:

| Field | Type | Description |
|-------|------|-------------|
| `id` | TEXT (UUID) | Primary key |
| `nonce` | TEXT (UUID) | Cryptographic nonce for idempotency; UNIQUE; server-generated |
| `campaign_id` | TEXT (nullable) | Campaign this merge belongs to; NULL for standalone |
| `spec_id` | TEXT (nullable) | Spec being merged; NULL for standalone |
| `workspace_slug` | TEXT | Workspace containing the repo |
| `target_branch` | TEXT | Branch to merge into (e.g. "main") |
| `source_ref` | TEXT | Branch to merge from (e.g. "spec/07-secrets-variables") |
| `status` | TEXT | Lifecycle status (see below) |
| `rejection_reason` | TEXT (nullable) | CantMergeReason enum value; NULL on success |
| `retry_count` | INTEGER | Number of retries so far |
| `available_at` | TEXT (RFC 3339) | Backoff: invisible to queue until this time. Set to `now()` on initial job creation so the job is immediately eligible for processing. |
| `base_sha` | TEXT (nullable) | Target HEAD when merge started |
| `merged_sha` | TEXT (nullable) | Resulting HEAD on success |
| `conflict_details` | TEXT (nullable) | JSON array of conflicting file paths (stored as TEXT; deserialised to a native JSON array by the HTTP handler before returning) |
| `check_output` | TEXT (nullable) | Captured stdout/stderr from check command |
| `submitted_by` | TEXT | UUID of the authenticated user or agent who submitted the merge; populated by the handler from `GetAuthInfo` — never supplied in the request body |
| `created_at` | TEXT (RFC 3339) | |
| `updated_at` | TEXT (RFC 3339) | |

The `nonce` field is an internal implementation detail and is **not** included
in API responses. It is never supplied by external callers; the hub server
generates it when creating the merge job record.

### Status State Machine

```
prepared → queued → running → merged
                  ↘ conflict
                  ↘ check_failed
                  ↘ push_failed
                  ↘ cancelled (from queued only)
                  ↘ dead_letter (from queued after max retries)
```

- `prepared`: Record inserted by the caller; not yet visible to the queue.
- `queued`: Ready for processing; picked up by the next available worker.
- `running`: Actively being processed (rebase + check + push).
- `merged`: Successfully integrated.
- `conflict`: Rebase or dry-run detected a conflict.
- `check_failed`: Post-rebase check command failed.
- `push_failed`: Fast-forward push failed.
- `cancelled`: Cancelled by the user (only from `queued`).
- `dead_letter`: Exceeded max retries; requires operator intervention.

### Typed Rejection Reasons (CantMergeReason)

The `CanMerge` pre-check returns `(canMerge bool, reason CantMergeReason,
err error)` where the reason is a typed enum, not a string.

| Reason | Meaning | Queue Action |
|--------|---------|--------------|
| `BeforeDependency` | Upstream spec not yet merged | Re-enqueue with backoff |
| `WouldConflict` | Dry-run detected merge conflicts | Set status to `conflict` |
| `AlreadyMerged` | Source branch already integrated | Skip idempotently |
| `BranchNotReady` | No new commits on source branch | Re-enqueue with backoff |
| `SpecBlocked` | Spec is in `blocked` status | Re-enqueue with backoff |

### SpecBlocked Re-evaluation

When `CanMerge` returns `SpecBlocked`, the job is **re-enqueued with
exponential backoff** (same parameters as `BranchNotReady`). The job
remains in `queued` status with a future `available_at` timestamp. On each
subsequent attempt, `CanMerge` is re-evaluated. When the campaign scheduler
unblocks the spec, the job's next `CanMerge` check will pass and processing
continues normally. There is no separate `blocked` status in the state
machine; `SpecBlocked` does not require manual intervention.

### CanMerge Function

`CanMerge` is a package-level function in `internal/mergequeue` with the
following signature:

```go
// CanMerge evaluates whether a merge job is eligible to proceed.
// It queries the database directly for dependency status and branch readiness.
// Returns (true, "", nil) when the merge may proceed.
// Returns (false, reason, nil) when the merge should be deferred or rejected.
// Returns (false, "", err) on unexpected database or git errors.
func CanMerge(ctx context.Context, db *sql.DB, job MergeJob) (bool, CantMergeReason, error)
```

`CanMerge` is a pure pre-check function — no interface injection is needed.
It queries the database directly for dependency and branch-readiness checks.
For standalone merges (`campaign_id == ""`), the `BeforeDependency` and
`SpecBlocked` checks are skipped (no campaign context).

### Dry-Run Conflict Check

Before performing a real rebase, run a read-only conflict probe:

```
git merge-tree --write-tree <target-head> <source-head>
```

Uses `GitRunner.RunExitCode` from `internal/gitcmd`. If the exit code
indicates conflicts, the merge is rejected early with a structured conflict
report (file paths). The integration branch is never left in a dirty state.

Requires Git >= 2.38 (validated at startup by `gitcmd.CheckGitVersion`).

The `merge-tree` invocation is called directly by `mergequeue` via
`GitRunner.RunExitCode`. `gitcmd` owns subprocess execution with safety
defaults; `mergequeue` owns merge semantics. Adding domain-specific helpers
like `MergeTree` to `gitcmd` would bloat that package unnecessarily.

**Conflict file path extraction (dry-run):** When `merge-tree --write-tree`
exits with a non-zero exit code, conflicts are present. The conflicting file
paths are extracted by parsing the command's stderr output, which lists them
in git's standard conflict-reporting format. The implementer follows git's
documented output format directly; no helper is added to `gitcmd` for this
parsing.

**Conflict file path extraction (real rebase):** When `git rebase` exits
with a non-zero exit code (indicating a conflict), conflicting file paths are
extracted by running `git diff --name-only --diff-filter=U` after the
conflict is detected. This produces a clean, newline-separated list of
unmerged file paths. The rebase is then aborted with `git rebase --abort`.
All three steps (`rebase`, `diff --name-only`, `rebase --abort`) are
performed inside the per-target-branch mutex before releasing it.

**TOCTOU window (by design):** The dry-run conflict check is performed
outside the per-target-branch mutex (before step 3). A concurrent merge
completing between the dry-run and the real rebase could cause the conflict
check result to be stale. This race is intentional: the dry-run is a
best-effort early exit to avoid acquiring the mutex for obviously conflicting
merges. The real rebase inside the mutex is the authoritative check. If the
dry-run passes but the real rebase detects a conflict (due to a concurrent
merge completing in the window), the job transitions to `conflict` status
normally and the integration branch is never left dirty. See Design Decision
#14.

### Worker Goroutine Topology

There is **one global worker goroutine** that processes all target branches
sequentially. The per-target-branch mutex exists for future extensibility
(e.g., parallel per-branch workers) but in this iteration a single worker
polls for the next available job across all branches and processes it one
at a time.

This matches the existing clone queue pattern in `internal/workspace/queue.go`.

**Polling cadence:** The worker uses a three-way select:
1. **Shutdown signal** (`stopCh` closed) — exit immediately.
2. **Wakeup channel** (buffered(1)) — a newly enqueued job interrupts the
   timer sleep immediately.
3. **10-second timer** — fallback poll to pick up jobs whose `available_at`
   has elapsed after a backoff delay.

The 10-second fallback is fast enough to pick up backoff-delayed jobs
reasonably quickly while avoiding unnecessary database churn.

### Merge Algorithm

For each job, in order:

1. **Pre-check** (`CanMerge`): validate prerequisites and return a typed
   `CantMergeReason` if the job should not proceed.
   - `BeforeDependency` → re-enqueue with backoff.
   - `BranchNotReady` → re-enqueue with backoff.
   - `SpecBlocked` → re-enqueue with backoff (same parameters as `BranchNotReady`).
   - `AlreadyMerged` → set status to `merged`, skip.
2. **Dry-run conflict check**: `git merge-tree --write-tree`.
   - Conflicts detected → set status to `conflict`, record file list (parsed
     from `merge-tree` stderr), skip.
   - This check is intentionally performed outside the mutex (best-effort
     early exit; see TOCTOU note above).
3. **Acquire** per-target-branch mutex.
4. **Validate nonce** (idempotency check against DB record).
5. Set status to `running`, record `base_sha` from current target HEAD.
6. **Fetch** latest target branch state from `origin` using
   `git fetch origin <target_branch>`. Authentication for private
   repositories is handled via workspace-scoped git credentials (see
   `git_credentials` spec), which are applied to the GitRunner environment
   before the fetch.
7. **Rebase** source branch onto the freshly fetched remote tracking ref
   using `git rebase origin/<target_branch>` via `GitRunner.Run`. Step 6
   updates the `origin/<target_branch>` remote tracking ref; step 7 rebases
   against that ref to ensure the freshly fetched state is used rather than
   a stale local copy.
   - On conflict: run `git diff --name-only --diff-filter=U` to collect the
     list of unmerged file paths, then run `git rebase --abort`. Set status
     to `conflict`, store the collected paths in `conflict_details`, release
     mutex.
8. **Run check command** (if configured). See [Check Command Execution](#check-command-execution) below.
   - On failure: set status to `check_failed`, record output, release mutex.
9. **Fast-forward push** target branch to rebased HEAD.
   - On failure: set status to `push_failed`, release mutex.
10. Set status to `merged`, record `merged_sha`, release mutex.
11. **Invoke** `PostMergeHook` (only if `campaign_id` is non-NULL).

### Check Command Execution

The check command is stored as the `CHECK_COMMAND` workspace variable (using
the secrets/variables system from spec 07). If not set, the check step is
skipped.

The `CHECK_COMMAND` value is passed as-is to `sh -c "<command>"`, allowing
operators to use shell operators such as pipes, redirects, and compound
commands (e.g. `make test 2>&1 | head -100`). Because `CHECK_COMMAND` is
a trusted operator-configured value (only workspace admins can set it, not
end users), the shell injection surface is acceptable given the trust model.

Execution environment:

| Property | Value |
|----------|-------|
| **Working directory** | `<WORKSPACE_ROOT>/<slug>/trunk` |
| **Shell** | `sh -c "<CHECK_COMMAND>"` — shell-interpreted to support pipes, redirects, and compound commands |
| **Environment** | Inherits GitRunner's safety defaults: `GIT_ALLOW_PROTOCOL`, `GIT_TERMINAL_PROMPT=0`, `GIT_CONFIG_NOSYSTEM=1` |
| **Timeout** | 10 minutes by default; configurable via the `CHECK_TIMEOUT` workspace variable (e.g. `"5m"`, `"15m"`) |
| **Execution** | `os/exec.CommandContext` with the configured timeout, invoking `sh -c` |
| **Output capture** | Stdout and stderr are captured and stored in `check_output` on failure |

If the `CHECK_COMMAND` variable fetch fails (e.g., secret-store error), the
merge job transitions to `check_failed` and the error is recorded in
`check_output`.

### Post-Merge Hook (Campaign Scheduler Interface)

The merge queue accepts a `PostMergeHook` at construction time to decouple it
from the campaign scheduler:

```go
// PostMergeHook is called after a merge job completes successfully.
// For standalone merges (campaign_id == ""), implementations should be a no-op.
type PostMergeHook func(ctx context.Context, job MergeJob) error
```

- The hook is injected at `Queue` construction time (e.g., `NewQueue(..., hook PostMergeHook)`).
- For standalone merges (`campaign_id == ""`), the hook is a no-op (or the
  caller supplies `nil`, which the queue treats as no-op).
- The campaign package owns the concrete implementation and wires it at
  startup.
- The hook is called synchronously after `status` is set to `merged` and
  `merged_sha` is recorded. If the hook returns an error, it is **logged but
  does not change the job status** — the merge itself succeeded and the job
  remains in `merged` status. The hook error is not surfaced in the API
  (the job's `status` stays `merged`); it is visible only in structured logs
  with the `merge_job_id` field for operator correlation.
- The hook inherits the merge job's context. If graceful shutdown cancels the
  context, the hook invocation is interrupted. Hook implementations are
  expected to be fast (e.g., enqueue work onto a channel rather than blocking
  on downstream operations). This is a responsibility of the campaign package.

### Exponential Backoff with Dead-Letter

Failed merge jobs that are retriable (`BeforeDependency`, `BranchNotReady`,
`SpecBlocked`) are re-enqueued with exponentially increasing delays:

- Base delay: 2 seconds
- Multiplier: 2x per retry
- Cap: 2 hours **per attempt** (no single retry delay exceeds 2 hours; this is not a total elapsed-time ceiling)
- Max retries: 20

With base=2s and multiplier=2x, the per-attempt cap kicks in around retry 12
(2^12 × 2s ≈ 8192s ≈ 2.3h, capped to 2h). Retries 12–20 all use the 2h
cap.

Implemented via `available_at` timestamp — the queue's polling query filters
`WHERE status = 'queued' AND available_at <= now()`.

Jobs exceeding max retries transition to `dead_letter` status with the
failure reason preserved. Dead-lettered jobs can be inspected via the API
and manually requeued via the requeue endpoint (see [REST API](#rest-api)).

### Prepare-Then-Enqueue with Nonce Idempotency

Two-phase pattern to prevent double-merges:

1. **Prepare:** INSERT merge job with a server-generated UUID nonce and
   `status=prepared` inside the caller's database transaction. The nonce is
   always generated by the hub; external API callers never supply it.
2. **Enqueue:** Send the job ID to the queue channel, setting status to
   `queued`.
3. **Execute:** The queue handler validates the nonce and checks the status.
   If `prepared` or `queued`, execute. If `running` or `merged`, skip. If
   no record exists (caller's transaction rolled back), discard.

### Duplicate Submission Guard

At most one job per `(workspace_slug, source_ref)` pair may be in `queued`
or `running` status at any time. This is enforced by a **pre-insert SELECT
check** in the POST `/merges` handler:

1. Before inserting a new merge job, the handler queries for any existing job
   with the same `workspace_slug` and `source_ref` where `status IN ('queued',
   'running')`.
2. If such a job exists, the handler returns **HTTP 409 Conflict** with an
   apikit error body that includes the `id` of the conflicting job.
3. If no active job exists, the insert proceeds normally.

No database UNIQUE constraint is used for this guard — filtering by a
dynamic set of active status values is not expressible as a SQLite CHECK
constraint or partial UNIQUE index in a clean way. The application-level
check is sufficient given the single-process deployment model.

**Example HTTP 409 response body:**

```json
{
  "error": "merge already in progress for this source branch",
  "existing_job_id": "UUID"
}
```

### Wakeup-on-Enqueue

The merge queue uses a `buffered(1)` wakeup channel:

- When a new job is enqueued, a non-blocking send on the channel interrupts
  the worker's poll sleep.
- Multiple rapid enqueues coalesce into a single wakeup.
- The worker's main loop uses a three-way select: shutdown signal, normal
  timer expiry (10-second fallback), or wakeup.

### Graceful Shutdown

- `Stop()` closes a `stopCh` (broadcast signal to all workers).
- Workers check `stopCh` in their select, preventing new dispatches.
- `stopWaitGroup.Wait()` blocks until all in-flight merge operations
  complete.
- In-flight rebases or pushes finish cleanly before the process exits.

### REST API

| Method | Endpoint | Success Code | Description |
|--------|----------|--------------|-------------|
| POST | `/api/v1/workspaces/:slug/merges` | 202 Accepted | Submit a merge request (enqueued, not executed synchronously) |
| GET | `/api/v1/workspaces/:slug/merges` | 200 OK | List merge jobs |
| GET | `/api/v1/workspaces/:slug/merges/:id` | 200 OK | Get merge job status |
| DELETE | `/api/v1/workspaces/:slug/merges/:id` | 204 No Content | Cancel a queued job |
| POST | `/api/v1/workspaces/:slug/merges/:id/requeue` | 202 Accepted | Requeue a dead-lettered job |

**POST /api/v1/workspaces/:slug/merges — request body:**

```json
{
  "target_branch": "main",
  "source_ref": "spec/07-secrets-variables"
}
```

The `submitted_by` field is **not** supplied in the request body. The handler
populates it automatically from the authenticated caller's UUID using apikit's
`GetAuthInfo` — this works for both user sessions and agent API keys. The hub
infers `campaign_id` and `spec_id` from the `source_ref` branch name
(following the `spec/<spec_id>-<spec_name>` convention). If no matching
active campaign is found, the merge is accepted as a standalone merge with
`campaign_id = NULL`. The hub server generates the idempotency nonce
internally when creating the merge job record; callers do not supply a nonce.

Before inserting the new job, the handler checks for an existing active job
for the same `(workspace_slug, source_ref)` pair and returns **HTTP 409
Conflict** with the `existing_job_id` if one is found (see
[Duplicate Submission Guard](#duplicate-submission-guard)).

**DELETE /api/v1/workspaces/:slug/merges/:id — cancellation rules:**

Cancellation is only valid when the job is in `queued` status. If the job
is in any other state (e.g., `running`, `merged`, `dead_letter`,
`cancelled`), the server returns **HTTP 409 Conflict** with the standard
apikit error body. The error message includes the current job status so
the client knows why cancellation was rejected.

**POST /api/v1/workspaces/:slug/merges/:id/requeue:**

Only valid for jobs in `dead_letter` status. Creates a new merge job with a
fresh server-generated nonce, `status=queued`, `retry_count=0`, and
`available_at=now()`, copying `source_ref` and `target_branch` from the
original job. The `submitted_by` field on the new job is set to the UUID of
the authenticated caller of the requeue request (obtained via `GetAuthInfo`),
not the original submitter. The new job starts with a completely fresh retry
budget (20 retries). The original dead-lettered job is left unchanged in
`dead_letter` status for audit trail purposes. Returns **HTTP 409 Conflict**
if the job is not in `dead_letter` status. Requires `merges:write` scope.

**GET /api/v1/workspaces/:slug/merges — Query Parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `after` | string (UUID) | — | Cursor-based pagination: returns jobs created after the referenced job, ordered by `(created_at ASC, id ASC)`. The `after` value is the `id` of the last job on the previous page. |
| `limit` | integer | 50 | Page size; maximum 100 |
| `status` | string | — | Filter by merge job status. Must be one of the valid status values (e.g. `queued`, `merged`, `dead_letter`). An unrecognised value returns **HTTP 400 Bad Request** with an apikit error body listing the invalid value and the valid options. |

Results are ordered `(created_at ASC, id ASC)` — oldest first. The first
page (no `after` cursor) returns the oldest jobs up to `limit`. The cursor
is stable: new jobs inserted after a page was fetched will not disrupt
previously retrieved pages.

Response is a JSON array of merge job objects wrapped in a pagination envelope:

```json
{
  "items": [ /* merge job summary objects */ ],
  "next_cursor": "UUID | null"
}
```

`next_cursor` is `null` when there are no further pages.

**List vs. detail response shapes:** The list endpoint returns a **summary
shape** that omits the `check_output` field to keep list payloads small
(check output can be large test output). All other fields — including
`conflict_details` (a compact file-path list) — are included in both the
list and single-item responses. The single-item GET (`GET /merges/:id`)
returns the **full shape** including `check_output`.

**Authentication:** All endpoints are authenticated using the existing apikit
middleware (Bearer token, API key, or session cookie, consistent with other
hub endpoints). Admin tokens and API keys have implicit full access. PATs
require explicit `merges:read` or `merges:write` scope grants. Auth
enforcement is handled at the middleware level by apikit, not in individual
handlers.

**GET /merges/:id — full merge job response:**

```json
{
  "id": "UUID",
  "campaign_id": "UUID | null",
  "spec_id": "string | null",
  "workspace_slug": "string",
  "target_branch": "string",
  "source_ref": "string",
  "status": "prepared | queued | running | merged | conflict | check_failed | cancelled | push_failed | dead_letter",
  "rejection_reason": "string | null",
  "retry_count": 0,
  "base_sha": "string | null",
  "merged_sha": "string | null",
  "conflict_details": ["file1.go", "file2.go"],
  "check_output": "string | null",
  "submitted_by": "string",
  "created_at": "RFC 3339",
  "updated_at": "RFC 3339"
}
```

**GET /merges — summary item shape** (same as above but `check_output` is
omitted):

```json
{
  "id": "UUID",
  "campaign_id": "UUID | null",
  "spec_id": "string | null",
  "workspace_slug": "string",
  "target_branch": "string",
  "source_ref": "string",
  "status": "...",
  "rejection_reason": "string | null",
  "retry_count": 0,
  "base_sha": "string | null",
  "merged_sha": "string | null",
  "conflict_details": ["file1.go", "file2.go"],
  "submitted_by": "string",
  "created_at": "RFC 3339",
  "updated_at": "RFC 3339"
}
```

Note: The `nonce` field is **not** included in any API response. It is an
internal implementation detail for idempotency and is not exposed to clients.

`conflict_details` is `null` when no conflicts are recorded, or a native
JSON array of file path strings when populated. The store returns the raw
TEXT column value; the HTTP handler unmarshals it into a native JSON array
before responding — clients always receive a proper array or null, never a
JSON-encoded string.

**Error responses** follow the existing apikit error format used throughout
the hub. All error responses share the same envelope already established by
apikit — no new error schema is defined here.

### Permissions

| Scope | Description |
|-------|-------------|
| `merges:read` | Query merge job status and list merge jobs |
| `merges:write` | Submit, cancel, and requeue merge jobs |

Admin tokens and API keys have implicit full access. PATs require explicit
scope grants. Authentication is enforced via apikit middleware, consistent
with all other hub endpoints.

### Database Schema

```sql
CREATE TABLE IF NOT EXISTS merge_jobs (
    id               TEXT PRIMARY KEY,
    nonce            TEXT NOT NULL UNIQUE,
    campaign_id      TEXT REFERENCES campaigns(id),
    spec_id          TEXT,
    workspace_slug   TEXT NOT NULL,
    target_branch    TEXT NOT NULL,
    source_ref       TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'prepared'
        CHECK(status IN (
            'prepared','queued','running','merged',
            'conflict','check_failed','cancelled','push_failed',
            'dead_letter'
        )),
    rejection_reason TEXT,
    retry_count      INTEGER NOT NULL DEFAULT 0,
    available_at     TEXT NOT NULL,
    base_sha         TEXT,
    merged_sha       TEXT,
    conflict_details TEXT,
    check_output     TEXT,
    submitted_by     TEXT NOT NULL,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);

CREATE INDEX idx_merge_jobs_campaign
    ON merge_jobs(campaign_id, status);
CREATE INDEX idx_merge_jobs_workspace
    ON merge_jobs(workspace_slug, status, created_at);
CREATE INDEX idx_merge_jobs_available
    ON merge_jobs(status, available_at);
```

Schema is applied at startup via `InitSchema` in `store.go` using
`CREATE TABLE IF NOT EXISTS`, consistent with the existing hub pattern used
by `internal/workspace/schema.go`. No external migration tool (goose,
atlas, golang-migrate) is used — the hub manages its own schema at startup.

### Package Structure

The merge queue lives in `internal/mergequeue/` with these components:

| File | Responsibility |
|------|---------------|
| `reason.go` | `CantMergeReason` enum type with defined constants |
| `hook.go` | `PostMergeHook` type definition |
| `store.go` | Merge job CRUD, `InitSchema`, nonce validation |
| `queue.go` | FIFO queue with wakeup channel, backoff, graceful shutdown |
| `merge.go` | `processMergeJob`: pre-check, dry-run, rebase, check, push |

### Standalone Merge Support

When `campaign_id` is NULL on a merge job, the merge proceeds without
campaign-specific logic:

- No cascading rebase.
- No DAG advancement.
- `PostMergeHook` is a no-op (nil or explicitly skipped).
- The `CanMerge` pre-check skips `BeforeDependency` and `SpecBlocked`
  checks (no campaign context).

This enables non-campaign merges (e.g., manual branch integration by an
operator) and simplifies testing.

### Observability

For this iteration, observability is limited to structured logging. No
Prometheus metrics are required. The hub uses Go's standard `log` package.

All significant state transitions and errors are logged with the following
fields:

| Log Field | Description |
|-----------|-------------|
| `merge_job_id` | UUID of the merge job |
| `workspace_slug` | Workspace slug |
| `status` | New status after transition |
| `error` | Error detail (if applicable) |
| `retry_count` | Current retry count (for backoff events) |
| `rejection_reason` | `CantMergeReason` value (if applicable) |

`PostMergeHook` errors are logged with the `merge_job_id` field and an
`error` detail. They do not alter job status (which remains `merged`) and
are not surfaced in any API response.

## Verified External API Surface

### internal/gitcmd (Spec 10)

The merge queue depends on the following signatures from `internal/gitcmd`:

```go
// NewRunner creates a GitRunner rooted at the given working directory.
// Safety env vars (GIT_TERMINAL_PROMPT=0, GIT_CONFIG_NOSYSTEM=1,
// GIT_ALLOW_PROTOCOL) are applied to every subprocess.
func NewRunner(workDir string) *GitRunner

// Run executes a git command and returns combined stdout/stderr output
// and any error. A non-zero exit code is returned as a *GitError.
func (r *GitRunner) Run(ctx context.Context, args ...string) (output string, err error)

// RunExitCode executes a git command and returns the raw exit code along
// with combined stdout/stderr. It does not treat non-zero exit codes as
// errors, allowing the caller to interpret the code directly.
func (r *GitRunner) RunExitCode(ctx context.Context, args ...string) (exitCode int, output string, err error)

// CheckGitVersion returns an error if the installed git version is below
// the required minimum (2.38 for merge-tree --write-tree support).
func CheckGitVersion(ctx context.Context) error

// GitError is returned by Run when git exits with a non-zero exit code.
type GitError struct {
    ExitCode int
    Output   string
}
func (e *GitError) Error() string
```

**Usage in mergequeue:**

| Operation | Method | Args |
|-----------|--------|------|
| Dry-run conflict check | `RunExitCode` | `"merge-tree", "--write-tree", targetHead, sourceHead` |
| Fetch target branch | `Run` | `"fetch", "origin", targetBranch` |
| Rebase source onto target | `Run` | `"rebase", "origin/<targetBranch>"` |
| Collect rebase conflict paths | `Run` | `"diff", "--name-only", "--diff-filter=U"` |
| Abort failed rebase | `Run` | `"rebase", "--abort"` |
| Fast-forward push | `Run` | `"push", "origin", targetBranch` |
| Startup version gate | `CheckGitVersion` | — (called by hub main, not by mergequeue) |

**Failure modes:**

- `RunExitCode` returns exit code `1` with conflict details in output when
  `merge-tree` detects conflicts. The merge queue parses the output to
  extract conflicting file paths by following git's standard documented
  conflict-reporting format. No helper is added to `gitcmd` for this parsing;
  the implementer owns it within `mergequeue`.
- When `git rebase` fails with a conflict, `git diff --name-only --diff-filter=U`
  is run to collect unmerged file paths, then `git rebase --abort` is run.
  Both follow-up commands are invoked via `Run`; errors from either are logged
  but do not change the final `conflict` status outcome.
- `Run` returns `*GitError` on non-zero exit; the merge queue inspects
  `ExitCode` and `Output` to classify the failure.
- `CheckGitVersion` is called once by the hub's main startup routine (not
  by `mergequeue` itself). If Git < 2.38, the hub refuses to start before
  any queue is constructed.

## Tech Stack

- **Language:** Go (matching the rest of the hub)
- **Database:** SQLite via `database/sql` (existing pattern)
- **Schema management:** `InitSchema` in `store.go` using `CREATE TABLE IF NOT EXISTS`, applied at startup (matches `internal/workspace/schema.go` pattern)
- **Git operations:** `internal/gitcmd` package (spec 10)
- **HTTP framework:** apikit (existing pattern, Gin-based)
- **Auth:** apikit auth middleware (`GetAuthInfo`, `AuthInfo`); enforced at the middleware layer consistent with all other hub endpoints. The `submitted_by` field is populated by handlers using `GetAuthInfo` to retrieve the authenticated caller's UUID.
- **Check command execution:** `os/exec.CommandContext` with configurable timeout, invoking `sh -c "<CHECK_COMMAND>"` to support shell operators (pipes, redirects, compound commands). The command string is a trusted operator-configured value (only workspace admins can set `CHECK_COMMAND`).
- **Startup version gate:** `gitcmd.CheckGitVersion` is called once by the hub's main startup routine before constructing the merge queue. The `mergequeue` package does not call it internally.
- **Testing:**
  - `go test` with `-race` on all packages
  - **Unit tests** (`store`, `reason`, algorithm logic): SQLite `:memory:` database and a fake/mock `GitRunner`
  - **Integration tests** (`processMergeJob` pipeline): real local git repos created with `t.TempDir()` and `git init`; exercises the full queue loop end-to-end
  - Table-driven test style throughout

## Dependencies

| Spec | From Group | To Group | Relationship |
|------|-----------|----------|--------------|
| 10_gitcmd | 7 | 1 | Uses GitRunner.Run, GitRunner.RunExitCode for all git subprocess calls; CheckGitVersion called by hub main at startup |
| 07_secrets_variables | — | — | `CHECK_COMMAND` and `CHECK_TIMEOUT` workspace variables for post-rebase check command configuration |
| git_credentials | — | — | Workspace-scoped git credentials applied to GitRunner for authenticated fetch/push to private repositories |

## Design Decisions

1. **Per-target-branch mutex, not global.** The merge queue acquires a
   mutex per target branch, not a global lock. This allows merges to
   different target branches (different workspaces or different integration
   branches) to proceed concurrently while serializing merges to the same
   target branch. The mutex is held in-process (a `sync.Mutex` per target
   branch key stored in an in-process map). This is sufficient because the
   hub runs as a single process per deployment.

2. **CantMergeReason is a typed enum, not error strings.** Every merge
   rejection scenario is a defined constant (`BeforeDependency`,
   `WouldConflict`, `AlreadyMerged`, `BranchNotReady`, `SpecBlocked`).
   This prevents fragile string matching and makes queue routing
   decisions compiler-checked.

3. **Standalone merge support.** The merge queue is usable without
   campaigns. When `campaign_id` is NULL, the merge proceeds without
   campaign-specific logic. This simplifies testing and enables
   operator-initiated merges.

4. **Dry-run before real rebase.** `git merge-tree --write-tree` checks
   for conflicts before touching the worktree. The integration branch is
   never left in a dirty state by a failed merge attempt.

5. **Nonce-based idempotency; server-generated.** Each merge job has a
   unique cryptographic nonce generated by the hub server when the job
   record is created. External API callers never supply a nonce. The queue
   handler validates the nonce before executing, preventing double-merges
   after crashes, retries, or transaction rollbacks. The nonce is an
   internal implementation detail and is not exposed in API responses.

6. **Dead-letter after 20 retries.** Jobs that fail repeatedly are moved to
   `dead_letter` status instead of retrying forever. Operators can inspect
   and manually requeue them via `POST /api/v1/workspaces/:slug/merges/:id/requeue`.
   The original dead-lettered job is preserved for audit purposes; the
   requeue operation creates a fresh job with a new server-generated nonce,
   `retry_count=0`, and sets `submitted_by` to the authenticated caller of
   the requeue request.

7. **Campaign ID inference from source_ref.** The POST merge body requires
   only `target_branch` and `source_ref`. The hub parses the source_ref
   (which follows `spec/<spec_id>-<spec_name>`) to infer campaign_id and
   spec_id. If no matching campaign is found, the merge is standalone.

8. **PostMergeHook over wakeup channel.** The campaign scheduler is
   decoupled via a `PostMergeHook func(ctx context.Context, job MergeJob) error`
   injected at construction time. This is more testable than a raw channel
   and makes the interface boundary explicit without requiring the merge
   queue to import campaign types.

9. **mergequeue calls merge-tree directly.** `gitcmd` is a thin subprocess
   utility; domain-specific git operations (merge-tree, rebase) belong in
   `mergequeue`. Adding a `MergeTree` helper to `gitcmd` would add campaign
   domain knowledge to a general-purpose package. Conflict file path parsing
   from merge-tree output is similarly owned by `mergequeue`, following
   git's standard documented format.

10. **Structured logging only.** Prometheus metrics are deferred to a future
    iteration. All critical state transitions are logged with structured
    fields for operational visibility.

11. **SpecBlocked uses backoff, not a separate state.** Rather than
    introducing a new `blocked` status, `SpecBlocked` jobs re-enqueue with
    the same exponential backoff as `BranchNotReady`. This keeps the state
    machine simple and avoids the need for an external signal to re-trigger
    evaluation.

12. **Startup schema via InitSchema.** Schema is applied at startup using
    `CREATE TABLE IF NOT EXISTS`, matching `internal/workspace/schema.go`.
    No external migration tool is introduced; this is consistent with the
    existing hub pattern.

13. **Pagination uses (created_at ASC, id ASC) ordering.** The list
    endpoint uses stable ascending order (oldest first) with cursor-based
    pagination. The `after` parameter is the `id` of the last seen job;
    the query returns jobs where `(created_at, id) > (after_created_at, after_id)`.
    This avoids the inconsistency of mixing descending `created_at` with
    ascending `id` comparison. The first page (no `after` cursor) returns
    the oldest jobs up to `limit`.

14. **Dry-run conflict check is outside the mutex (TOCTOU accepted by design).**
    The `git merge-tree` dry-run at step 2 is performed before acquiring the
    per-target-branch mutex at step 3. A concurrent merge completing in this
    window can make the dry-run result stale. This race is intentional: the
    dry-run is a best-effort early exit to avoid acquiring the mutex for
    obviously conflicting merges. The real rebase inside the mutex is the
    authoritative conflict check. A stale-passing dry-run followed by a
    conflicting real rebase results in a normal `conflict` status transition
    with no dirty integration branch state.

15. **Backoff cap is per-attempt, not a total elapsed-time ceiling.**
    The 2-hour cap applies to each individual retry delay. With base=2s and
    multiplier=2x, the cap kicks in at approximately retry 12 (2^12 × 2s
    ≈ 2.3h). Retries 12–20 each wait 2 hours. The total maximum elapsed
    time across all 20 retries is therefore bounded but not tightly
    constrained; the cap exists to prevent indefinite single-attempt waits.

16. **conflict_details serialisation boundary.** The `conflict_details`
    column stores a JSON array as TEXT in SQLite. The HTTP handler is
    responsible for unmarshalling this TEXT value into a native JSON array
    before serialising the response. Clients always receive a proper JSON
    array (or null) — never a JSON-encoded string. The store layer returns
    raw TEXT; the handler layer owns the deserialisation.

17. **CHECK_COMMAND is shell-interpreted.** The check command is executed
    via `sh -c "<CHECK_COMMAND>"` rather than direct exec. This allows
    operators to write commands with pipes, redirects, and compound
    expressions. The command is a trusted operator-configured value (only
    workspace admins can set workspace variables), so the shell injection
    surface is acceptable within this trust model.

18. **HTTP success codes for REST endpoints.** POST `/merges` returns
    202 Accepted (the merge is enqueued asynchronously, not executed
    synchronously). DELETE returns 204 No Content. POST `/requeue` returns
    202 Accepted (same rationale as submit). GET endpoints return 200 OK.

19. **Invalid status filter returns HTTP 400.** Supplying an unrecognised
    value for the `status` query parameter on GET `/merges` returns
    HTTP 400 Bad Request with an apikit error body listing the invalid
    value and the set of valid status values. An empty result set is not
    returned for invalid inputs.

20. **Requeue submitted_by reflects the operator, not the original submitter.**
    When a dead-lettered job is requeued, the new job's `submitted_by` is
    set to the UUID of the authenticated caller of the requeue request
    (obtained via `GetAuthInfo`). The original submitter is preserved on
    the unchanged dead-lettered job, which remains in `dead_letter` status
    for audit purposes.

21. **No rate limiting for this iteration.** The POST `/merges` endpoint is
    intentionally unbounded. The queue's backoff/dead-letter mechanism and
    duplicate-submission rejection (at most one queued/running job per spec)
    provide sufficient protection for the expected workload. Rate limiting
    can be added in a future iteration if needed.

22. **PostMergeHook timeout is context-driven.** The hook inherits the merge
    job's context. Graceful shutdown cancels this context, which interrupts
    any blocking hook invocation. Hook implementations (owned by the campaign
    package) are expected to be fast — enqueuing work onto a channel rather
    than blocking on downstream operations. No separate per-hook timeout is
    imposed by the merge queue.

23. **Authentication via apikit middleware.** All merge queue endpoints use
    the existing apikit authentication middleware (Bearer token, API key, or
    session cookie). No endpoint-level auth logic is added; this is consistent
    with all other hub endpoints. The `submitted_by` field is populated from
    the authenticated caller's UUID via `GetAuthInfo` and is never accepted
    from request bodies.

24. **One global worker goroutine for this iteration.** A single goroutine
    services all target branches sequentially. The per-target-branch mutex
    supports future parallelism (one worker per branch) without requiring a
    design change. This matches the clone queue pattern in
    `internal/workspace/queue.go`.

25. **Fallback poll interval is 10 seconds.** The worker's timer leg fires
    every 10 seconds. This is fast enough to pick up backoff-delayed jobs
    promptly and slow enough to avoid unnecessary database churn. The wakeup
    channel handles the zero-latency fast path for newly enqueued jobs.

26. **Rebase uses origin/<target_branch> ref.** Step 7 invokes
    `git rebase origin/<target_branch>` after step 6 runs
    `git fetch origin <target_branch>`. This ensures the rebase uses the
    freshly fetched remote tracking ref rather than a potentially stale
    local branch ref.

27. **Duplicate submission guard is application-level, not a DB constraint.**
    A pre-insert SELECT filters for existing jobs with the same
    `(workspace_slug, source_ref)` in `queued` or `running` status. A
    UNIQUE constraint cannot cleanly express a partial index over a dynamic
    set of status values in SQLite. The single-process deployment model
    makes the application-level check safe against races.

28. **List responses omit check_output; detail responses include it.**
    `check_output` can be large (full test runner output). It is excluded
    from the list endpoint's summary shape to keep list payloads small.
    `conflict_details` (a compact list of file paths) is included in both
    shapes because it is useful for list-view filtering and display.

29. **PostMergeHook errors are log-only; job status remains merged.**
    A hook error does not roll back or alter the merge result. The job
    transitions to `merged` unconditionally once the push succeeds. Hook
    failures are recorded in structured logs (with `merge_job_id`) and are
    not surfaced in any API response field.

30. **CanMerge is a package-level function, not an injected interface.**
    The pre-check function lives in `internal/mergequeue` and takes
    `(ctx, db, job)` directly. This avoids unnecessary abstraction — the
    function is deterministic given its inputs and is straightforwardly
    testable with an in-memory SQLite database.

31. **submitted_by is handler-inferred, never caller-supplied.** The
    `submitted_by` field is always the UUID of the authenticated caller,
    obtained via apikit's `GetAuthInfo`. It is not accepted in POST request
    bodies. This applies to both the initial merge submission and the requeue
    operation (where `submitted_by` reflects the operator who initiated the
    requeue, not the original submitter).

32. **Rebase conflict file paths extracted via git diff --name-only --diff-filter=U.**
    After a `git rebase` conflict, unmerged file paths are collected by
    running `git diff --name-only --diff-filter=U`. This provides a clean,
    newline-separated list of unresolved files. The rebase is then aborted
    with `git rebase --abort`. Errors from either follow-up command are
    logged but do not affect the final `conflict` status transition.

33. **available_at is set to now() on initial job creation.** Newly created
    merge jobs (both `prepared` → `queued` and freshly requeued jobs) have
    `available_at` set to the current time, making them immediately eligible
    for processing. Only backoff re-enqueues set `available_at` to a future
    timestamp.

34. **Requeue resets retry_count to 0.** The new job created by the requeue
    endpoint starts with `retry_count=0` and a full 20-retry budget. The
    dead-lettered original job is left unchanged for audit purposes.

35. **CheckGitVersion is called by hub main, not by mergequeue.** The
    startup version gate (`gitcmd.CheckGitVersion`) is the responsibility of
    the hub's main startup routine. The `mergequeue` package does not call it
    internally. This matches the `gitcmd` spec's design where
    `CheckGitVersion` is a package-level function called once at startup,
    not per-consumer.

36. **Per-target-branch mutex map entries are never evicted.** The in-process
    map of `sync.Mutex` values per target branch accumulates entries for the
    lifetime of the process. The number of distinct target branches is small
    and bounded in practice (typically 1–3 per workspace), so unbounded
    growth is not a practical concern. No cleanup mechanism is provided.
