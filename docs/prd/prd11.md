# Git Operations Infrastructure

## Intent

The hub manages workspace repositories — clones of upstream repos where agents
implement specs on branches. Three foundational git capabilities are needed
before any coordination layer can be built on top:

1. **Safe git subprocess execution.** Every git CLI call must enforce protocol
   allowlists, suppress interactive prompts, and ignore system-level config to
   prevent hangs, security issues, and environment-dependent behavior.
2. **Serialized branch merging.** When multiple agents complete work on
   separate branches, their results must be merged into the integration branch
   without races, lost work, or dirty repository state. A sequential merge
   queue with rebase-then-fast-forward provides this guarantee.
3. **Upstream synchronization.** Workspace clones diverge from upstream over
   time. A sync mechanism must fetch upstream changes, advance the integration
   branch, detect force-pushes, and provide recovery operations.

These capabilities are general-purpose git infrastructure. They are usable by
any higher-level coordination system (campaign schedulers, manual operator
workflows, external controllers) without being coupled to any specific
orchestration model.

## Goals

- Provide a hardened git subprocess runner that enforces safety defaults on
  every git CLI invocation within the hub.
- Provide a sequential merge queue that serializes branch merges per target
  branch using rebase-then-fast-forward, with conflict detection, retry
  logic, and idempotency guarantees.
- Provide branch rebase operations with conflict detection and structured
  conflict reporting.
- Provide upstream sync that fetches from the remote origin and fast-forwards
  the integration branch, with force-push detection and recovery.
- Provide a reclone operation for nuclear recovery from corrupted or severely
  diverged repository state.
- Expose merge queue and sync operations via REST API and CLI.
- Reuse the existing credential infrastructure (secrets store with `GIT_PAT`,
  `GIT_USERNAME`/`GIT_PASSWORD`) for fetch authentication.

## Non-Goals

- **Orchestration or scheduling.** This PRD provides git primitives. Higher-level
  concepts (campaigns, DAGs, spec scheduling, agent dispatch) are out of scope.
- **Parallel merge execution.** Merges are strictly sequential (FIFO) per target
  branch. This is a deliberate simplicity choice — sufficient for the throughput
  profile of parallel AI agents (~15–20 merges/hour with a 3–4 minute test
  suite).
- **Automatic conflict resolution.** The hub detects and reports conflicts;
  agents or operators resolve them.
- **Webhook-driven sync.** Exposing webhook endpoints for upstream forges
  requires the hub to be publicly reachable and per-forge payload parsing.
  Manual triggers and future periodic polling work uniformly with any git
  remote.
- **Upstream contribution (PR creation, direct push).** This PRD covers
  downstream sync only. Contribution modes are future work.
- **Periodic background sync.** A background scheduler with configurable
  intervals per workspace is deferred. This PRD provides the sync machinery;
  scheduling can be layered on top.

## Functional Requirements

### Hardened Git Subprocess Runner

A `GitRunner` wraps all git CLI subprocess calls with safety defaults and
uniform error handling. All hub packages that execute git commands use this
runner — no direct `exec.Command("git", ...)` calls.

- **Safety environment variables** applied to every invocation:
  - `GIT_ALLOW_PROTOCOL=file:https:ssh` — prevents `ext::` and other dangerous
    protocol handlers.
  - `GIT_TERMINAL_PROMPT=0` — prevents interactive credential prompts from
    hanging the hub process.
  - `GIT_CONFIG_NOSYSTEM=1` — ignores system-level git config that could alter
    behavior.
- **Uniform error formatting:** every failed command captures the command line,
  exit code, and stderr for structured error reporting.
- **Three-way exit code discrimination** for remote queries using
  `git ls-remote --exit-code`:

  | Exit code | Meaning | Action |
  |-----------|---------|--------|
  | 0 | Branch/ref exists | Proceed |
  | 2 | Branch/ref genuinely missing | Create or return "not found" |
  | 1 | Network/auth failure | Propagate error |

  This prevents misinterpreting a network timeout or auth failure as "branch
  does not exist."

- The runner is initialized with a working directory and optional additional
  environment variables.
- The runner is built as `internal/gitcmd` and requires git >= 2.38 on the
  host (for `merge-tree --write-tree` support).

### Merge Queue

The merge queue serializes merge operations per target branch using a FIFO
queue with rebase-then-fast-forward semantics. It is a standalone facility
that any caller (operator, external controller, future orchestration layer)
can submit merge jobs to via the REST API.

#### Merge Jobs

A merge job represents a request to integrate a source branch into a target
branch within a workspace.

- A merge job has: `id` (UUID), `workspace_slug`, `target_branch`,
  `source_ref` (source branch name), `status`, `nonce` (cryptographic, for
  idempotency), `base_sha` (target HEAD when merge started), `merged_sha`
  (resulting HEAD on success), `conflict_details` (JSON file list on conflict),
  `check_output` (stderr/stdout on check failure), `submitted_by` (agent or
  user ID), `retry_count`, `available_at` (backoff timestamp), `created_at`,
  `updated_at`.
- A merge job may optionally carry `callback_url` — a URL the merge queue
  POSTs a status notification to on terminal state transitions (merged,
  conflict, check_failed, push_failed, dead_letter). This enables
  higher-level systems to react to merge outcomes without polling.
- Status values: `prepared`, `queued`, `running`, `merged`, `conflict`,
  `check_failed`, `cancelled`, `push_failed`, `dead_letter`.
- A given `source_ref` may have at most one `queued` or `running` merge job
  at a time per target branch. Submitting a duplicate is rejected with
  HTTP 409.
- Only `queued` jobs can be cancelled. `running` jobs complete or fail.

#### Typed Merge Rejection Reasons

Instead of returning errors for expected rejection scenarios, the merge queue
uses a typed enum (`CantMergeReason`) that separates "expected rejection" from
"unexpected failure." The `CanMerge` pre-check returns
`(bool, CantMergeReason, error)` where the reason is programmatically
matchable without string parsing.

| Reason | Meaning | Queue action |
|--------|---------|--------------|
| `WouldConflict` | Dry-run detected merge conflicts | Set conflict status, notify |
| `AlreadyMerged` | Source branch already integrated into target | Skip idempotently |
| `BranchNotReady` | Source branch has no commits ahead of target | Re-enqueue with backoff |

Higher-level systems may extend the `CantMergeReason` enum with additional
reasons (e.g., dependency ordering) without modifying the core merge queue
logic.

#### Dry-Run Conflict Check

Before performing a real rebase, the merge queue runs a read-only conflict
probe using `git merge-tree --write-tree` (Git 2.38+):

```
git merge-tree --write-tree <target-head> <source-branch-head>
```

If the exit code indicates conflicts, the merge is rejected early with a
structured conflict report (file paths). The target branch is never left in a
dirty state by a failed merge attempt.

#### Merge Algorithm

For each job, in order:

1. **Pre-check** (`CanMerge`): validate prerequisites and return a typed
   `CantMergeReason` if the job should not proceed.
   - `WouldConflict` → set status to `conflict`, record file list, skip.
   - `AlreadyMerged` → skip idempotently.
   - `BranchNotReady` → re-enqueue with backoff.
2. Acquire per-target-branch mutex.
3. Validate nonce (idempotency check).
4. Set status to `running`, record `base_sha` from current target HEAD.
5. Fetch latest target branch state.
6. Rebase source onto target.
   - On conflict: `git rebase --abort`, set status to `conflict`, record
     conflicting file paths in `conflict_details`, release mutex.
7. Run configured check command (if any). The check command is stored as
   the `CHECK_COMMAND` workspace variable (using the existing
   secrets/variables system from spec 07). If not set, the check step is
   skipped.
   - On failure: set status to `check_failed`, record output, release mutex.
8. Fast-forward push target branch to rebased HEAD.
   - On failure: set status to `push_failed`, release mutex.
9. Set status to `merged`, record `merged_sha`, release mutex.
10. If `callback_url` is set, POST a status notification to it.

#### Prepare-Then-Enqueue with Nonce Idempotency

Merge job dispatch uses a two-phase pattern to prevent double-merges:

1. **Prepare:** INSERT a merge job record with a cryptographic nonce and
   `status=prepared` inside the caller's database transaction.
2. **Enqueue:** Send a message to the merge queue referencing the job ID.
3. **Execute:** The queue handler validates the nonce and checks the status.
   If `prepared`, execute and transition to `running`. If `running` or
   `merged`, skip (idempotent). If no record exists (caller's transaction
   rolled back), discard the message.

This guarantees exactly-once execution even after crashes, network retries,
or transaction rollbacks.

#### Wakeup-on-Enqueue

The merge queue uses a `buffered(1)` wakeup channel for low-latency dispatch
without busy-waiting:

- When a job is enqueued, a non-blocking send on the wakeup channel
  interrupts the queue's poll sleep.
- Multiple rapid enqueues coalesce into a single wakeup.
- The queue's main loop uses a three-way select: shutdown signal, normal
  timer expiry, or wakeup.

#### Exponential Backoff with Dead-Letter Queue

Failed merge jobs that are retryable (e.g., `BranchNotReady`) are retried
with exponentially increasing delays:

- Base delay: 2 seconds. Multiplier: 2x per retry. Cap: 2 hours.
  Max retries: 20.
- Retry is implemented via an `available_at` timestamp on the merge job — the
  queue's polling query filters `WHERE available_at <= now()`, making retried
  jobs invisible until their backoff window expires.
- Jobs exceeding max retries transition to `dead_letter` with the failure
  reason preserved. Dead-lettered jobs can be inspected via the API and
  manually requeued.

#### Graceful Shutdown

The merge queue uses `WaitGroup` + closed-channel broadcast for shutdown:

- `Stop()` closes a `stopCh` (broadcast signal to all workers).
- Workers check `stopCh` in their semaphore-acquisition select, preventing
  new dispatches after stop is requested.
- `stopWaitGroup.Wait()` blocks until all in-flight merge operations
  complete.

An interrupted rebase or merge never leaves the repository in a broken state.
In-flight operations finish cleanly before the process exits.

### Branch Rebase

The hub provides a rebase operation that rebases a source branch onto a target
ref. This is a building block used by the merge queue and available to
higher-level systems.

- **Rebase operation:** `git rebase <target-ref>` on the source branch.
- **On clean rebase:** returns the new branch HEAD SHA.
- **On conflict:** `git rebase --abort`, returns a structured conflict report
  containing the list of conflicting file paths. The branch is left in its
  pre-rebase state.
- **Batch rebase:** given a list of branches, rebase each onto a target ref.
  On conflict for any branch, stop processing that branch and report it.
  Continue with remaining branches (independent branches are not blocked by
  a sibling's conflict).

### Upstream Synchronization

The sync operation fetches upstream changes and advances the workspace's
integration branch. It is the atomic unit of synchronization.

#### New Workspace Fields

Five fields are added to the workspace schema:

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `sync_mode` | TEXT NOT NULL | `'pull_only'` | Sync mode: `pull_only` or `disabled`. Extensible for future modes. |
| `sync_status` | TEXT NOT NULL | `'idle'` | Current sync state: `idle`, `syncing`, `error`. |
| `upstream_head_sha` | TEXT (nullable) | NULL | HEAD SHA of the upstream tracking branch at last fetch. |
| `last_sync_at` | TEXT (nullable) | NULL | RFC 3339 timestamp of the last successful sync. |
| `sync_error` | TEXT (nullable) | NULL | Error message from the most recent failed sync. |

#### Sync Modes

| Mode | Behavior |
|------|----------|
| `pull_only` (default) | Downstream sync is enabled. The hub fetches upstream and fast-forwards the integration branch. |
| `disabled` | Sync is disabled. Sync requests are rejected with a descriptive error. |

The mode is set at workspace creation (optional `--sync-mode` flag) and can
be changed via the workspace update endpoint.

#### Sync Algorithm

1. Validate preconditions:
   - Workspace `status` is `active`.
   - Workspace `clone_status` is `ready`.
   - Workspace `sync_mode` is not `disabled`.
   - Workspace `sync_status` is not `syncing` (prevents concurrent syncs).
2. Set `sync_status = 'syncing'`.
3. Open the local repository at `<WORKSPACE_ROOT>/<slug>/trunk/`.
4. Resolve fetch credentials from the secrets store using the existing
   `resolveCloneAuth` function.
5. Fetch from upstream: `git fetch origin <branch>`.
   - If the fetch fails due to missing history on a shallow clone, retry
     with `--unshallow` (lazy unshallow). Log a warning.
   - If the fetch fails for other reasons (network, auth), set
     `sync_status = 'error'`, record `sync_error`, abort.
6. Read the fetched upstream HEAD SHA.
7. Record `upstream_head_sha` (always updated — represents known upstream
   state regardless of whether the integration branch can be advanced).
8. Compare upstream HEAD with local integration branch HEAD:
   - **Already up to date:** upstream HEAD equals local HEAD. Set
     `sync_status = 'idle'`, update `last_sync_at`.
   - **Fast-forward possible:** upstream HEAD is a descendant of local HEAD.
     Fast-forward the integration branch. Update `head_sha`. Set
     `sync_status = 'idle'`, update `last_sync_at`.
   - **Diverged (force-push detected):** upstream HEAD is NOT a descendant of
     local HEAD. Set `sync_status = 'error'`, record descriptive error. Do
     NOT advance the integration branch. The operator must choose a recovery
     path.

#### Sync Status State Machine

```
         ┌──────────────────────────┐
         │                          │
         ▼                          │
Sync ──► syncing ──► idle ──────────┘ (next sync)
            │
            ▼
          error ──► idle  (after operator resolves)
```

#### Force-Push and Diverged History Recovery

When upstream force-pushes, the fast-forward check fails. Two recovery
operations are available:

**Reset to upstream** (`afc workspace sync <slug> --reset-to-upstream`):

Resets the integration branch to match upstream HEAD. Safe in `pull_only`
mode because the integration branch has no local-only commits — agent work
lives on separate branches.

1. Fetch upstream.
2. Reset the local integration branch to the fetched upstream HEAD.
3. Update `head_sha` and `upstream_head_sha`.
4. Set `sync_status = 'idle'`, update `last_sync_at`, clear `sync_error`.

API: `POST /api/v1/workspaces/:slug/sync?reset_to_upstream=true`

**Reclone** (`afc workspace reclone <slug>`):

Nuclear recovery option. Archives the workspace (pushing any local commits
if possible via the existing archive flow), deletes the local clone, and
re-clones from upstream at current HEAD.

1. Execute the existing archive flow: push local commits to upstream (if
   credentials and push access allow), record HEAD SHA, delete local clone
   directory.
2. Set `clone_status = 'pending'`, `sync_status = 'idle'`, clear
   `sync_error`, clear `upstream_head_sha`.
3. Enqueue a clone job (same as workspace reactivation).
4. Return the workspace with `clone_status: pending`.

API: `POST /api/v1/workspaces/:slug/reclone`

### REST API

#### Merge Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/workspaces/:slug/merges` | Submit a merge request |
| GET | `/api/v1/workspaces/:slug/merges` | List merge jobs |
| GET | `/api/v1/workspaces/:slug/merges/:id` | Get merge job status |
| DELETE | `/api/v1/workspaces/:slug/merges/:id` | Cancel a queued job |

**POST request body:**

```json
{
  "target_branch": "main",
  "source_ref": "feature/my-branch",
  "callback_url": "https://example.com/hooks/merge-status"
}
```

`callback_url` is optional. When provided, the merge queue POSTs a JSON
notification to this URL when the merge job reaches a terminal state.

**GET merge job response:**

```json
{
  "id": "string (UUID)",
  "workspace_slug": "string",
  "target_branch": "string",
  "source_ref": "string",
  "status": "prepared | queued | running | merged | conflict | check_failed | cancelled | push_failed | dead_letter",
  "rejection_reason": "string (CantMergeReason) | null",
  "base_sha": "string (40-char hex SHA) | null",
  "merged_sha": "string (40-char hex SHA) | null",
  "conflict_details": ["path/to/file1", "path/to/file2"],
  "check_output": "string | null",
  "retry_count": 0,
  "submitted_by": "string",
  "created_at": "string (RFC 3339)",
  "updated_at": "string (RFC 3339)"
}
```

#### Sync Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/workspaces/:slug/sync` | Trigger upstream sync |
| POST | `/api/v1/workspaces/:slug/reclone` | Archive and re-clone from upstream |

**POST `/api/v1/workspaces/:slug/sync`**

Query parameters:

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `reset_to_upstream` | boolean | `false` | Reset the integration branch to match upstream HEAD. For recovering from force-push / diverged history. |

**POST `/api/v1/workspaces/:slug/reclone`**

Request body: none.

#### Error Responses

| Condition | HTTP Status |
|-----------|-------------|
| Sync on workspace with `sync_mode = 'disabled'` | 400 |
| Sync on workspace with `status != 'active'` | 400 |
| Sync on workspace with `clone_status != 'ready'` | 400 |
| Sync already in progress (`sync_status = 'syncing'`) | 409 |
| Upstream fetch failed (network, auth) | 502 |
| Upstream history diverged (force-push detected) | 409 |
| Reclone without `--confirm` flag (CLI only) | 400 |
| Duplicate merge job for same source_ref | 409 |

### CLI Commands

**Merge commands:**

```
afc merge submit <workspace-slug> --target <branch> --source <branch>
afc merge list <workspace-slug>
afc merge status <workspace-slug> <merge-id>
afc merge cancel <workspace-slug> <merge-id>
```

**Sync commands:**

```
afc workspace sync <slug> [--reset-to-upstream]
afc workspace reclone <slug> --confirm
```

- `afc workspace sync <slug>` — Trigger upstream sync.
- `--reset-to-upstream` — Reset the integration branch to match upstream HEAD.
- `afc workspace reclone <slug> --confirm` — Archive and re-clone. `--confirm`
  is required (same pattern as `afc workspace delete`).

**Workspace create extension:**

```
afc workspace create --git-url <url> --slug <slug> [--sync-mode <mode>]
```

- `--sync-mode` accepts `pull_only` (default) or `disabled`.

### Permissions

| Scope | Description |
|-------|-------------|
| `merges:read` | Query merge job status |
| `merges:write` | Submit and cancel merge jobs |
| `workspaces:sync` | Trigger sync and reclone operations |

Admin tokens and API keys have implicit full access. PATs require explicit
scope grants. Sync status fields are included in standard workspace responses
under existing `workspaces:read` permission.

### Updated Workspace Response Schema

The workspace response object gains five new fields:

```json
{
  "slug": "string",
  "git_url": "string",
  "branch": "string | null",
  "display_name": "string",
  "description": "string",
  "owner_id": "string (UUID)",
  "org_id": "string (UUID) | null",
  "status": "active | archived",
  "clone_status": "pending | cloning | ready | failed | archived",
  "head_sha": "string (40-char hex SHA) | null",
  "clone_error": "string | null",
  "sync_mode": "pull_only | disabled",
  "sync_status": "idle | syncing | error",
  "upstream_head_sha": "string (40-char hex SHA) | null",
  "last_sync_at": "string (RFC 3339) | null",
  "sync_error": "string | null",
  "created_at": "string (RFC 3339)",
  "updated_at": "string (RFC 3339)"
}
```

## Technical Boundaries

- **Language:** Go (1.26+)
- **Foundation:** `github.com/txsvc/apikit` — server framework,
  authentication, CLI.
- **Git requirement:** git >= 2.38 on the hub host (for
  `git merge-tree --write-tree`).
- **Git operations:** git CLI via `GitRunner` for merge, rebase, sync, and
  conflict detection. `github.com/go-git/go-git/v5` for fetch and branch
  manipulation (consistent with clone operations).
- **Credential reuse:** `resolveCloneAuth` from `internal/workspace/queue.go`
  resolves fetch credentials from the secrets store.
- **Database:** SQLite (pure Go, no CGo). Pre-production; schema changes are
  applied as DDL updates.

## Dependencies

| Spec | Relationship |
|------|--------------|
| 05_workspace_checkout | Requires clone infrastructure (JobQueue, clone lifecycle, workspace directory structure) |
| 06_git_server | Merge queue integrates with git server for local push operations |
| 07_secrets_variables | CHECK_COMMAND workspace variable uses the existing secrets/variables system |
| 09_git_credentials | Requires credential storage and `resolveCloneAuth` for fetch authentication |
