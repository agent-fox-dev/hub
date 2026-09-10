---
spec_id: '13'
spec_name: upstream_sync
title: Upstream Sync
status: draft
created_at: '2026-08-04T10:11:59.543572+00:00'
updated_at: '2026-08-04T10:17:48.717602+00:00'
owner: ''
source: docs/prd/prd12.md
schema_version: 1
---
# Upstream Synchronization

## Intent

Workspace clones diverge from upstream over time. A sync mechanism must fetch
upstream changes, advance the integration branch, detect force-pushes, and
provide recovery operations.

This spec provides upstream synchronization that fetches changes from the
remote origin and advances the workspace's integration branch via
fast-forward. It also provides force-push recovery (reset-to-upstream) and a
reclone operation for nuclear recovery from corrupted or severely diverged
repository state.

## Background

Workspace clones are local copies of upstream git repositories. Over time,
the upstream repository may receive new commits (or force-pushed rewrites)
that the local clone does not reflect. Without a sync mechanism, agents
operating on stale branches produce work that is difficult to rebase or merge
downstream. Force-pushes are a particularly disruptive case: they rewrite
history such that the local branch is no longer a direct ancestor of the
upstream branch, requiring explicit operator intervention.

This spec was split from a larger PRD (`prd12.md` — Git Operations
Infrastructure) that covered three independent functional areas. The git
subprocess runner and merge operations are covered by the `git_runner`
(spec 11) and `merge_operations` specs respectively.

## Goals

- Provide upstream sync that fetches from the remote origin and fast-forwards
  the integration branch, with force-push detection.
- Track sync state via five new workspace fields (`sync_mode`,
  `sync_status`, `upstream_head_sha`, `last_sync_at`, `sync_error`).
- Provide reset-to-upstream recovery for diverged history (force-push
  scenarios).
- Provide a reclone operation for nuclear recovery from corrupted state.
- Expose sync and reclone operations via REST API and CLI.
- Reuse the existing credential infrastructure (`resolveCloneAuth`) for
  fetch authentication.

## Non-Goals

- **Periodic background sync.** A background scheduler with configurable
  intervals per workspace is deferred. This spec provides the sync machinery;
  scheduling can be layered on top.
- **Webhook-driven sync.** Exposing webhook endpoints requires the hub to be
  publicly reachable and per-forge payload parsing. Manual triggers work
  uniformly with any git remote.
- **Upstream contribution (PR creation, direct push).** This spec covers
  downstream sync only.
- **Automatic conflict resolution.**
- **Orchestration or scheduling.**
- **Durable job queue for clone/reclone jobs.** Reclone uses the existing
  in-memory job queue consistent with the `workspace_checkout` spec. Migration
  to the durable queue is deferred to a future spec.

## Functional Requirements

### New Workspace Fields

Five fields are added to the workspace schema:

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `sync_mode` | TEXT NOT NULL | `'pull_only'` | Sync mode: `pull_only` or `disabled`. Extensible for future modes. |
| `sync_status` | TEXT NOT NULL | `'idle'` | Current sync state: `idle`, `syncing`, `error`. |
| `upstream_head_sha` | TEXT (nullable) | NULL | HEAD SHA of the upstream tracking branch at last fetch. |
| `last_sync_at` | TEXT (nullable) | NULL | RFC 3339 timestamp of the last successful sync. |
| `sync_error` | TEXT (nullable) | NULL | Error message from the most recent failed sync. |

These fields are added to the `workspaces` table via DDL ALTER TABLE
statements (pre-production schema; no migration framework needed).

### Sync Modes

| Mode | Behavior |
|------|----------|
| `pull_only` (default) | Downstream sync is enabled. The hub fetches upstream and fast-forwards the integration branch. |
| `disabled` | Sync is disabled. Sync requests are rejected with a descriptive error. |

The mode is set at workspace creation (optional `--sync-mode` CLI flag,
optional `sync_mode` field on create API request) and can be changed via the
workspace update endpoint (`PATCH /workspaces/:slug`).

**PATCH handler ownership:** This spec owns the extension of the existing
workspace PATCH handler to include `sync_mode` as a mutable field. The
`workspace_write_delete` spec owns the handler's permission model and overall
structure; `upstream_sync` adds `sync_mode` to the handler's field list
without modifying `workspace_write_delete`'s spec artifacts. No separate
PATCH codepath is introduced.

### Sync Algorithm

The `POST /api/v1/workspaces/:slug/sync` endpoint is **synchronous**: the
handler blocks until the fetch completes and returns the final workspace
state. Fetch operations are expected to complete in seconds. Concurrent sync
requests are rejected via the `syncing` status guard (HTTP 409). The HTTP
server's global request timeout (configured via apikit) applies if a fetch
stalls; this spec does not define its own timeout value.

**Stuck-sync recovery:** The sync handler must register a deferred cleanup
function that sets `sync_status = 'error'` and records a descriptive
`sync_error` if the request context is cancelled mid-sync (e.g., due to an
HTTP timeout or client disconnect). This prevents `sync_status` from being
permanently stuck in `'syncing'`. See also the startup reconciliation step
in the Server Startup Reconciliation section below.

1. **Validate preconditions:**
   - Workspace `status` is `active`.
   - Workspace `clone_status` is `ready`.
   - Workspace `sync_mode` is not `disabled`.
   - Workspace `sync_status` is not `syncing` (prevents concurrent syncs).
2. Set `sync_status = 'syncing'`.
3. Open the local repository at `<WORKSPACE_ROOT>/<slug>/trunk/` via go-git.
4. Resolve fetch credentials from the secrets store using the existing
   `resolveCloneAuth` function.
5. Fetch from upstream via go-git: `remote.Fetch()`.
   - If the fetch fails due to missing history on a shallow clone, retry
     with a full history fetch (unshallow). The exact go-git API for
     triggering an unshallow fetch is left to the engineer's discretion;
     the requirement is that the retry results in a complete history fetch.
     Shallow clone unshallow is a best-effort recovery path — log a warning
     and proceed.
   - If the fetch fails for other reasons (network, auth), set
     `sync_status = 'error'`, record `sync_error`, abort.
6. Read the fetched upstream HEAD SHA via go-git.
7. Record `upstream_head_sha` (always updated — represents known upstream
   state regardless of whether the integration branch can be advanced).
8. Compare upstream HEAD with local integration branch HEAD to determine
   fast-forward eligibility. The implementation approach (go-git commit graph
   walk, log range, or any other correct ancestry check) is left to the
   engineer's discretion. The required semantics are:
   - **Already up to date:** upstream HEAD equals local HEAD. Set
     `sync_status = 'idle'`, update `last_sync_at`.
   - **Fast-forward possible:** upstream HEAD is a descendant of local HEAD.
     Fast-forward the integration branch via go-git ref update. Update
     `head_sha`. Set `sync_status = 'idle'`, update `last_sync_at`.
   - **Diverged (force-push detected):** upstream HEAD is NOT a descendant of
     local HEAD and is not equal to local HEAD. Set `sync_status = 'error'`,
     record descriptive error ("upstream history has diverged; use
     --reset-to-upstream to recover"). Do NOT advance the integration branch.
     The operator must choose a recovery path.

### Sync Status State Machine

```
         ┌──────────────────────────┐
         │                          │
         ▼                          │
Sync ──► syncing ──► idle ──────────┘ (next sync)
            │
            ▼
          error ──► idle  (after operator resolves)
```

`syncing` → `error` also occurs on context cancellation (HTTP timeout,
client disconnect, or server shutdown) via the deferred cleanup function
described above.

### Server Startup Reconciliation

On server startup, the hub must reset all workspaces with
`sync_status = 'syncing'` to `sync_status = 'error'` with
`sync_error = 'sync interrupted by server restart'`. This handles the case
where a server crash or restart left one or more workspaces permanently stuck
in the `syncing` state, which would otherwise block all future sync attempts
on those workspaces.

This reconciliation step runs before the HTTP server begins accepting
requests.

### Force-Push and Diverged History Recovery

**Reset to upstream** (`afc workspace sync <slug> --reset-to-upstream`):

Resets the integration branch to match upstream HEAD. Safe in `pull_only`
mode because the integration branch has no local-only commits — agent work
lives on separate branches.

1. Fetch upstream via go-git.
2. Reset the local integration branch to the fetched upstream HEAD via go-git
   reference update.
3. Update `head_sha` and `upstream_head_sha`.
4. Set `sync_status = 'idle'`, update `last_sync_at`, clear `sync_error`.

API: `POST /api/v1/workspaces/:slug/sync?reset_to_upstream=true`

**Reclone** (`afc workspace reclone <slug>`):

Nuclear recovery option. Archives the workspace (pushing any local commits
if possible via the existing archive flow), deletes the local clone, and
re-clones from upstream at current HEAD.

1. Execute the existing archive flow (owned by `workspace_checkout` spec):
   attempt to push local commits to upstream if credentials and push access
   allow. **If the push fails for any reason (no credentials, remote
   rejection, network error), log a warning and continue.** Reclone is a
   nuclear recovery option — blocking on push failure defeats its purpose.
   Local commits may be lost if the push fails; this is accepted behavior.
2. Record HEAD SHA, delete local clone directory.
3. **Atomically** set `clone_status = 'pending'`, `sync_status = 'idle'`,
   clear `sync_error`, clear `upstream_head_sha`. The workspace `status`
   remains `'active'` throughout the entire reclone operation — it is never
   transitioned away from `active`.
4. Enqueue a clone job using the existing in-memory job queue (consistent
   with clone and reactivation behavior in `workspace_checkout`). The durable
   job queue is not used for reclone. `clone_status` then transitions
   `pending → cloning → ready` as the clone job executes.
5. Return the workspace with `clone_status: pending`.

The `clone_status` lifecycle during reclone is:
```
ready → pending (atomic, before clone is enqueued) → cloning → ready
```
No intermediate state is exposed between the archive step and the transition
to `pending`. The workspace remains accessible (status = `active`) throughout.

API: `POST /api/v1/workspaces/:slug/reclone`

### REST API

#### Sync Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/workspaces/:slug/sync` | Trigger upstream sync (synchronous) |
| POST | `/api/v1/workspaces/:slug/reclone` | Archive and re-clone from upstream |

**POST `/api/v1/workspaces/:slug/sync`**

Query parameters:

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `reset_to_upstream` | boolean | `false` | Reset the integration branch to match upstream HEAD. For recovering from force-push / diverged history. |

Response: the workspace object with updated sync fields (final state — the
handler blocks until sync completes).

**POST `/api/v1/workspaces/:slug/reclone`**

Request body: none.

Response: the workspace object with `clone_status: "pending"`.

### Error Responses

Error response bodies use the existing apikit `WriteAPIError` envelope,
consistent with all other hub endpoints. This spec does not introduce a new
error envelope format.

| Condition | HTTP Status |
|-----------|-------------|
| Sync on workspace with `sync_mode = 'disabled'` | 400 |
| Sync on workspace with `status != 'active'` | 400 |
| Sync on workspace with `clone_status != 'ready'` | 400 |
| Sync already in progress (`sync_status = 'syncing'`) | 409 |
| Upstream fetch failed (network, auth) | 502 |
| Upstream history diverged (force-push detected) without reset flag | 409 |
| Workspace not found | 404 |
| Context cancelled / HTTP timeout mid-sync | 504 |

### CLI Commands

```
afc workspace sync <slug> [--reset-to-upstream]
afc workspace reclone <slug> --confirm
```

- `afc workspace sync <slug>` — Trigger upstream sync.
- `--reset-to-upstream` — Reset the integration branch to match upstream HEAD.
- `afc workspace reclone <slug> --confirm` — Archive and re-clone.
  `--confirm` is required as a CLI-only safety check. The API does not
  require confirmation.

**Workspace create extension:**

```
afc workspace create --git-url <url> --slug <slug> [--sync-mode <mode>]
```

- `--sync-mode` accepts `pull_only` (default) or `disabled`.

### Permissions

| Scope | Description |
|-------|-------------|
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
- **Git operations:** go-git for fetch and ref manipulation (consistent with
  clone operations).
- **Credential reuse:** `resolveCloneAuth` from `internal/workspace/clone_auth.go`
  resolves fetch credentials from the secrets store.
- **Database:** SQLite (pure Go, no CGo). Schema changes are applied as
  DDL ALTER TABLE statements (pre-production).
- **Job queue:** In-memory job queue (same as `workspace_checkout`). No
  dependency on the `durable_job_queue` spec.
- **Error responses:** apikit `WriteAPIError` envelope (consistent with all
  other hub endpoints).

## Dependencies

| Spec | From Group | To Group | Relationship |
|------|-----------|----------|--------------|
| 05_workspace_checkout | - | 1 | Requires clone infrastructure, workspace directory structure, in-memory job queue, and archive flow for reclone. |
| 09_git_credentials | - | 1 | Requires credential storage and `resolveCloneAuth` for fetch authentication. |
| workspace_write_delete | - | 1 | PATCH handler is extended (not replaced) to include `sync_mode` as a mutable field. |

## Design Decisions

This spec was split from a larger PRD (prd12.md — Git Operations
Infrastructure) that covered three independent functional areas. This spec
covers upstream synchronization; the git subprocess runner and merge
operations are covered by the `git_runner` (spec 11) and `merge_operations`
specs.

1. **go-git for all sync operations.** The sync algorithm uses go-git
   exclusively — fetch, ref reading, ref updates, ancestry checks. No CLI
   operations are needed, so no GitRunner dependency. This is consistent with
   the existing clone infrastructure.

2. **Ancestry check is semantics-specified, not API-prescribed.** The spec
   requires correct fast-forward eligibility detection (upstream is a
   descendant of local → fast-forward; otherwise → diverged) but leaves the
   specific go-git API or commit graph algorithm to the engineer's discretion.
   This avoids locking to a specific go-git version's helper surface while
   preserving correctness requirements.

3. **Reclone `--confirm` is CLI-only.** The API does not require a confirmation
   parameter. The `--confirm` flag is a CLI UX safety check to prevent
   accidental reclone operations from the terminal. Programmatic API consumers
   are expected to implement their own confirmation workflows.

4. **Sync status is workspace-level, not job-level.** Sync is a
   synchronous request-response operation (not a queued job). The handler
   blocks until fetch completes and returns the final workspace state. The
   `sync_status` field on the workspace tracks whether a sync is in progress.
   Concurrent sync requests are rejected via the `syncing` status guard (HTTP
   409). The HTTP server's global request timeout (configured via apikit)
   applies if a fetch stalls; this spec does not define its own timeout value.

5. **Stuck-sync recovery via deferred cleanup and startup reconciliation.**
   The sync handler registers a deferred function that sets
   `sync_status = 'error'` on context cancellation, preventing stuck
   `'syncing'` states from live requests. A startup reconciliation step resets
   any remaining `'syncing'` workspaces to `'error'` on boot, handling crashes
   and unclean shutdowns.

6. **Schema changes via ALTER TABLE.** The five new workspace fields are added
   via ALTER TABLE statements rather than a migration framework. The project
   is pre-production and uses direct DDL updates for schema changes.

7. **`sync_mode` extensibility.** The `sync_mode` field accepts `pull_only`
   and `disabled` values. The TEXT type and application-level validation allow
   future modes (e.g., `bidirectional`) to be added without schema changes.

8. **Reclone uses the in-memory job queue.** Clone jobs are fast and
   idempotent; the existing in-memory queue from `workspace_checkout` is
   sufficient. The `durable_job_queue` spec targets long-running, persistent
   operations (merges, etc.) and is not a dependency of this spec.

9. **Reclone proceeds on push failure.** During reclone, if the attempt to
   push local commits to upstream fails, the operation logs a warning and
   proceeds with deletion and re-clone. This is intentional: reclone is a
   nuclear recovery option, and blocking on push failure would prevent
   recovery from the most severe repository states.

10. **Reclone workspace stays active; clone_status is the progress signal.**
    The workspace `status` remains `'active'` throughout reclone. The
    `clone_status` field (`pending → cloning → ready`) is the mechanism for
    consumers to track reclone progress. The transition to `'pending'` is
    atomic and occurs before the clone job is enqueued.

11. **Unshallow retry is best-effort.** If a shallow clone fetch fails due to
    missing history, the handler retries with a full history fetch. The exact
    go-git API is left to the engineer; the requirement is that the retry
    fetches complete history. If the unshallow retry also fails, the sync is
    treated as a fetch failure (sync_status = 'error').

12. **PATCH handler extension, not replacement.** The `sync_mode` field is
    added to the existing `PATCH /workspaces/:slug` handler's mutable field
    list. The `workspace_write_delete` spec retains ownership of the handler's
    permission model and structure; this spec declares a dependency on it
    rather than introducing a parallel codepath.

13. **Error envelope reuses apikit `WriteAPIError`.** Sync and reclone
    endpoints return errors using the same apikit error envelope as all other
    hub endpoints. No new error schema is introduced.
