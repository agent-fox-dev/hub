# Workspace Upstream Synchronization

## Intent

A workspace is a clone of an upstream repo. Once cloned, it diverges: the
upstream repository continues to receive commits from other developers, CI
systems, and other workspaces, while the workspace's integration branch
only advances through agent merges. This creates three problems:

1. **Stale code.** Agents implement features and fixes against outdated
   code. Changes that apply cleanly to the workspace may conflict with
   current upstream when contributed back.
2. **Invisible upstream changes.** Security patches, dependency updates,
   and API changes made upstream are not visible to agents, leading to
   work that must be reworked after contribution.
3. **Conflict accumulation.** The longer a workspace runs without syncing,
   the larger the eventual reconciliation cost — whether via pull request
   review or direct merge.

This PRD describes a synchronization mechanism that keeps a workspace's
integration branch up to date with its upstream source. The mechanism is
campaign-aware: it applies upstream changes at safe moments to minimize
disruption to running agents, and provides operator controls for
mid-campaign sync when urgency requires it.

The sync mechanism lives in the hub. Agents are unaware of upstream sync —
from their perspective, the integration branch advances (as it does after
any merge), and their spec branches are rebased via the existing cascading
rebase machinery.

## Goals

- Provide a manual sync operation (`afc workspace sync`) that fetches
  upstream changes and fast-forwards the workspace's integration branch.
- Track upstream HEAD separately from the integration branch HEAD so the
  hub always knows how far the workspace has drifted.
- Make pre-campaign sync mandatory by default, ensuring every campaign
  starts from current upstream state.
- Support mid-campaign sync by piggybacking upstream changes onto the
  existing post-merge cascading rebase, making upstream sync invisible to
  agents.
- Detect upstream force-push / history divergence and provide operator
  recovery operations (`--reset-to-upstream`, `reclone`).
- Reuse the existing credential infrastructure (secrets store with
  `GIT_PAT`, `GIT_USERNAME`/`GIT_PASSWORD`) for fetch authentication.
- Introduce sync modes (`pull_only`, `disabled`) as a per-workspace
  setting, extensible for future contribution modes.

## Non-Goals

- **Webhook-driven sync.** Exposing webhook endpoints for upstream forges
  (GitHub, GitLab) to push-notify the hub is deferred. Webhooks require
  the hub to be publicly reachable, per-forge payload parsing, and
  signature verification — significant integration surface with deployment
  constraints. Periodic polling or manual triggers work uniformly with any
  git remote.
- **PR contribution mode.** Creating pull requests on the upstream repo
  via the forge API (GitHub REST, GitLab MR) is future work. This PRD
  covers downstream sync only.
- **Direct push to upstream.** Pushing the integration branch directly to
  the upstream remote (bidirectional sync) is future work. The archive
  push operation (spec 05) remains the only upstream write path.
- **Automatic conflict resolution.** The hub detects and reports conflicts;
  agents or operators resolve them. No "ours" / "theirs" automatic
  strategies are applied.
- **Periodic background sync.** A background scheduler that fetches
  upstream on a configurable interval per workspace is deferred to a later
  phase. This PRD provides the sync machinery; scheduling can be layered
  on top.
- **Cross-workspace sync coordination.** Each workspace syncs
  independently. Coordinating sync across workspaces targeting the same
  upstream is not addressed.

## Functional Requirements

### New workspace fields

Five fields are added to the workspace schema:

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `sync_mode` | TEXT NOT NULL | `'pull_only'` | Sync mode: `pull_only` or `disabled`. Extensible for future modes. |
| `sync_status` | TEXT NOT NULL | `'idle'` | Current sync state: `idle`, `syncing`, `error`. |
| `upstream_head_sha` | TEXT (nullable) | NULL | HEAD SHA of the upstream tracking branch at last fetch. NULL until first sync. |
| `last_sync_at` | TEXT (nullable) | NULL | RFC 3339 timestamp of the last successful sync. NULL until first sync. |
| `sync_error` | TEXT (nullable) | NULL | Error message from the most recent failed sync. NULL when not in error state. |

### Updated workspace table schema

```sql
CREATE TABLE IF NOT EXISTS workspaces (
    slug              TEXT PRIMARY KEY,
    git_url           TEXT NOT NULL,
    branch            TEXT,
    owner_id          TEXT NOT NULL,
    org_id            TEXT,
    status            TEXT NOT NULL DEFAULT 'active',
    display_name      TEXT NOT NULL DEFAULT '',
    description       TEXT NOT NULL DEFAULT '',
    clone_status      TEXT NOT NULL DEFAULT 'pending'
        CHECK(clone_status IN ('pending','cloning','ready','failed','archived')),
    head_sha          TEXT,
    clone_error       TEXT,
    sync_mode         TEXT NOT NULL DEFAULT 'pull_only'
        CHECK(sync_mode IN ('pull_only','disabled')),
    sync_status       TEXT NOT NULL DEFAULT 'idle'
        CHECK(sync_status IN ('idle','syncing','error')),
    upstream_head_sha TEXT,
    last_sync_at      TEXT,
    sync_error        TEXT,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
);
```

### Sync modes

A workspace's `sync_mode` determines whether sync operations are permitted:

| Mode | Behavior |
|------|----------|
| `pull_only` (default) | Downstream sync is enabled. The hub fetches upstream and fast-forwards the integration branch. |
| `disabled` | Sync is disabled. Manual and automatic sync requests are rejected with a descriptive error. |

The mode is set at workspace creation (optional `--sync-mode` flag,
defaults to `pull_only`) and can be changed via the workspace update
endpoint. The mode is a workspace-level property — it reflects the
workspace's relationship to its upstream repo, not a per-campaign setting.

Future modes (`pr_contribution`, `direct_push`, `gated_push`) can be
added by extending the CHECK constraint and adding the corresponding
contribution logic. The downstream sync machinery is shared across all
modes.

### Sync operation

The sync operation fetches upstream changes and advances the workspace's
integration branch. It is the atomic unit of synchronization — all
triggers (manual, pre-campaign, post-merge piggybacking) invoke the same
underlying operation.

**Sync algorithm:**

1. Validate preconditions:
   - Workspace `status` is `active`.
   - Workspace `clone_status` is `ready`.
   - Workspace `sync_mode` is not `disabled`.
   - Workspace `sync_status` is not `syncing` (prevents concurrent syncs).
   - If the workspace has not been cloned (no local `trunk/` directory),
     reject with an error.
2. Set `sync_status = 'syncing'` on the workspace record.
3. Open the local repository at `<WORKSPACE_ROOT>/<slug>/trunk/`.
4. Resolve fetch credentials from the secrets store using the existing
   `resolveCloneAuth` function (same credentials used for clone).
5. Fetch from upstream:
   - `git fetch origin <branch>` (or the default branch if `branch` is
     null).
   - If the fetch fails due to missing history on a shallow clone, retry
     with `--unshallow` (lazy unshallow). Log a warning that the
     repository was unshallowed.
   - If the fetch fails for other reasons (network, auth), set
     `sync_status = 'error'`, record the error in `sync_error`, and
     abort.
6. Read the fetched upstream HEAD SHA from `FETCH_HEAD` or
   `refs/remotes/origin/<branch>`.
7. Record `upstream_head_sha` on the workspace record (this is always
   updated, even if the integration branch cannot be advanced — it
   represents the known upstream state).
8. Compare the upstream HEAD with the local integration branch HEAD:
   - **Already up to date:** upstream HEAD equals local HEAD. Set
     `sync_status = 'idle'`, update `last_sync_at`. No further action.
   - **Fast-forward possible:** upstream HEAD is a descendant of local
     HEAD. Fast-forward the integration branch to upstream HEAD. Update
     `head_sha` to the new HEAD. Set `sync_status = 'idle'`, update
     `last_sync_at`.
   - **Diverged (force-push detected):** upstream HEAD is NOT a descendant
     of local HEAD. Set `sync_status = 'error'`, record
     `sync_error = "upstream history has diverged (possible force-push); use --reset-to-upstream or reclone to recover"`.
     Do NOT advance the integration branch. The operator must choose a
     recovery path.
9. If the integration branch was advanced and a campaign is active on this
   workspace, trigger the post-sync cascade (see Campaign Interaction).

### Sync status state machine

```
         ┌──────────────────────────┐
         │                          │
         ▼                          │
Sync ──► syncing ──► idle ──────────┘ (next sync)
            │
            ▼
          error ──► idle  (after operator resolves)
```

- `idle` → `syncing`: Sync operation begins.
- `syncing` → `idle`: Sync succeeds (up to date or fast-forwarded).
- `syncing` → `error`: Sync fails (network, auth, diverged history).
- `error` → `idle`: Operator resolves the issue (via
  `--reset-to-upstream`, `reclone`, or fixing credentials) and
  re-triggers sync.

### Shallow clone handling

The initial workspace clone is shallow (depth 0 — full clone as currently
implemented). If a future iteration introduces shallow clones, the sync
operation handles them as follows:

- `git fetch` on a shallow clone fetches new commits from the shallow
  boundary. Fast-forwarding the integration branch and rebasing spec
  branches works because the relevant commits (branch tips and the new
  upstream HEAD) are present after the fetch.
- If a rebase fails due to missing history (Git reports a specific
  "shallow boundary" error), the sync function retries the fetch with
  `--unshallow` to retrieve full history, then reattempts the
  fast-forward. This is a one-time cost per workspace.

### Force-push and diverged history recovery

When upstream force-pushes or rewrites history, the fast-forward check
fails because upstream HEAD is not a descendant of the local integration
branch HEAD. The sync operation detects this and sets `sync_status` to
`error` with a descriptive message.

Two recovery operations are available:

**Reset to upstream** (`afc workspace sync <slug> --reset-to-upstream`):

Resets the integration branch to match upstream HEAD. This is safe in
`pull_only` mode because the integration branch has no local-only commits
— agent work lives on spec branches, merged via the merge queue. The
operation:

1. Fetches upstream (same as normal sync step 5).
2. Resets the local integration branch to the fetched upstream HEAD
   (`git reset --hard origin/<branch>`).
3. Updates `head_sha` and `upstream_head_sha` to the new HEAD.
4. Sets `sync_status = 'idle'`, updates `last_sync_at`, clears
   `sync_error`.
5. If a campaign is active, triggers the post-sync cascade (all active
   spec branches are rebased onto the new integration branch HEAD).

API: `POST /api/v1/workspaces/:slug/sync?reset_to_upstream=true`

**Reclone** (`afc workspace reclone <slug>`):

Nuclear recovery option. Archives the workspace (pushing any local
commits if possible via the existing archive flow), deletes the local
clone, and re-clones from upstream at current HEAD. Any active campaign is
cancelled — the operator must restart it.

1. If a campaign is active, cancel it (set campaign status to
   `cancelled`).
2. Execute the existing archive flow: push local commits to upstream (if
   credentials and push access allow), record HEAD SHA, delete local
   clone directory.
3. Set `clone_status = 'pending'`, `sync_status = 'idle'`, clear
   `sync_error`, clear `upstream_head_sha`.
4. Enqueue a clone job (same as workspace reactivation).
5. Return the workspace with `clone_status: pending`.

API: `POST /api/v1/workspaces/:slug/reclone`

The error report from a diverged-history sync includes:
- What was expected: fast-forward (upstream HEAD is a descendant of local
  HEAD).
- What happened: upstream HEAD is not a descendant of local HEAD.
- Recovery commands: `afc workspace sync <slug> --reset-to-upstream` or
  `afc workspace reclone <slug>`.

### Campaign interaction

Upstream sync interacts with campaign execution at three points:

**Between campaigns (no active campaign):**

Sync is trivially safe. No spec branches exist, no merge queue jobs are
pending. The sync operation fast-forwards the integration branch and
updates `head_sha`. No cascade needed.

**Before campaign start (pre-campaign sync):**

When a campaign is created (`POST /api/v1/workspaces/:slug/campaigns`),
the hub syncs the integration branch with upstream before building the
DAG and creating spec branches. This ensures every campaign starts from
current upstream state.

- Pre-campaign sync is mandatory by default.
- The campaign creation request accepts an optional `skip_sync` boolean
  (default `false`). When `true`, the hub skips the pre-campaign sync —
  useful when the operator has just synced manually or wants to run
  against a pinned baseline.
- If sync fails (network error, diverged history), campaign creation
  fails with a descriptive error. The operator must resolve the sync
  issue before creating the campaign, or retry with `skip_sync: true`.
- Pre-campaign sync is synchronous — it blocks the campaign creation
  response until the fetch and fast-forward complete. This adds network
  I/O latency but guarantees the campaign starts from a known-good state.

**During active campaigns (mid-campaign sync):**

Mid-campaign sync is supported but gated. The mechanism:

1. **Fetch is always allowed.** When sync is triggered during an active
   campaign (manually or by future periodic scheduling), the fetch
   executes and `upstream_head_sha` is updated. This tells the hub (and
   the operator via the API) how far the workspace has drifted from
   upstream.

2. **Integration branch advancement uses post-merge piggybacking.** After
   updating `upstream_head_sha`, the hub checks whether the integration
   branch can be advanced. If `upstream_head_sha` differs from the
   integration branch HEAD, the advancement is deferred until the next
   successful merge in the merge queue. At that point, the post-merge
   handler includes the upstream advancement in the same cascading rebase
   that agents already expect.

   From the agent's perspective, a post-merge rebase that includes
   upstream changes is indistinguishable from a normal post-merge rebase.
   The only observable difference is that the integration branch advanced
   by more commits than the single spec merge.

3. **Force-apply with `--force`.** The operator can override the deferral
   and force immediate integration branch advancement during a campaign
   via `afc workspace sync <slug> --force` or
   `POST /api/v1/workspaces/:slug/sync?force=true`. This triggers an
   immediate cascading rebase of all active spec branches. The operator
   accepts the blast radius — any spec branch that conflicts with
   upstream changes will be blocked.

   Use case: a critical security patch landed upstream and agents must
   work against the patched code immediately, even at the cost of
   disrupting some in-progress work.

**Sync is not a campaign phase.** The campaign does not gain a `syncing`
status. Sync is an integration branch operation that the campaign observes
through the existing rebase mechanism. The campaign status remains
`active` throughout.

### Overlap-aware gated sync (future enhancement)

When mid-campaign sync is triggered, the hub can optionally perform a
file-overlap analysis before advancing the integration branch:

1. Compute the set of files changed upstream since the last sync.
2. Compare against the set of files modified by each active spec branch.
3. **No overlap (common case):** advance the integration branch
   automatically. No spec branch rebase is needed — the spec branches
   will rebase at merge time via the normal merge queue.
4. **Overlap exists:** report the overlap to the operator and offer
   options:
   - **(a) Proceed:** advance and let the cascading rebase handle
     conflicts. Agents resolve via the existing blocked-branch workflow.
   - **(b) Defer:** wait until overlapping spec branches merge before
     advancing the integration branch.

The file-overlap check reuses the `git merge-tree --write-tree`
infrastructure from the merge queue's dry-run conflict check.

This enhancement is deferred to a later implementation phase. The MVP
uses the simpler post-merge piggybacking approach for mid-campaign sync,
without file-overlap analysis.

### REST API

#### Sync endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/workspaces/:slug/sync` | Trigger upstream sync |
| POST | `/api/v1/workspaces/:slug/reclone` | Archive and re-clone from upstream |

**POST `/api/v1/workspaces/:slug/sync`**

Query parameters:

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `force` | boolean | `false` | Force immediate integration branch advancement during an active campaign (bypasses post-merge piggybacking deferral). |
| `reset_to_upstream` | boolean | `false` | Reset the integration branch to match upstream HEAD. For recovering from force-push / diverged history. |

Request body: none.

Response: HTTP 200 with the updated workspace object (including sync
fields).

**POST `/api/v1/workspaces/:slug/reclone`**

Request body: none.

Response: HTTP 200 with the updated workspace object (`clone_status:
"pending"`, `sync_status: "idle"`).

#### Updated campaign creation

The `POST /api/v1/workspaces/:slug/campaigns` request body gains an
optional field:

```json
{
  "name": "sprint-42",
  "spec_ids": ["07", "08", "09"],
  "integration_branch": "main",
  "skip_sync": false
}
```

When `skip_sync` is `false` (default) or omitted, the hub performs a
synchronous upstream sync before building the campaign DAG.

### Permissions

| Scope | Description |
|-------|-------------|
| `workspaces:sync` | Trigger sync and reclone operations |

Sync and reclone require workspace ownership, admin access, or a PAT
with `workspaces:sync` scope. Read access to sync status fields is
governed by existing `workspaces:read` permission (sync fields are
included in standard workspace responses).

### Updated response schema

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

The five new fields appear in all workspace response objects (create,
list, get, archive, reactivate, sync, reclone).

### CLI commands

**Sync command:**

```
afc workspace sync <slug> [--force] [--reset-to-upstream]
```

- `afc workspace sync <slug>` — Trigger upstream sync. Prints the updated
  workspace object as JSON to stdout.
- `--force` — Force immediate integration branch advancement during an
  active campaign.
- `--reset-to-upstream` — Reset the integration branch to match upstream
  HEAD. For recovering from force-push / diverged history.

`--force` and `--reset-to-upstream` are mutually exclusive.

**Reclone command:**

```
afc workspace reclone <slug> --confirm
```

- Archives the workspace (pushing local commits if possible), deletes the
  local clone, and re-clones from upstream.
- `--confirm` is required (same pattern as `afc workspace delete`).
  Omitting it prints a usage hint to stderr and exits 1.
- Cancels any active campaign on the workspace.
- Prints the updated workspace object as JSON to stdout.

**Workspace create extension:**

```
afc workspace create --git-url <url> --slug <slug> [--sync-mode <mode>]
```

- `--sync-mode` accepts `pull_only` (default) or `disabled`.

### Error responses

Additional error conditions, using apikit's standard JSON envelope:

| Condition | HTTP Status |
|-----------|-------------|
| Sync on workspace with `sync_mode = 'disabled'` | 400 |
| Sync on workspace with `status != 'active'` | 400 |
| Sync on workspace with `clone_status != 'ready'` | 400 |
| Sync already in progress (`sync_status = 'syncing'`) | 409 |
| Upstream fetch failed (network, auth) | 502 |
| Upstream history diverged (force-push detected) | 409 |
| Reclone without `--confirm` flag | 400 |
| Pre-campaign sync failed | 502 |

When upstream history has diverged, the error response includes the
recovery commands in the error message:

```json
{
  "error": {
    "code": 409,
    "message": "upstream history has diverged (possible force-push); use POST /workspaces/:slug/sync?reset_to_upstream=true or POST /workspaces/:slug/reclone to recover"
  }
}
```

## Dependencies

| Spec | From Group | To Group | Relationship |
|------|-----------|----------|--------------|
| 05_workspace_checkout | — | — | Requires clone infrastructure (JobQueue, clone lifecycle, workspace directory structure) |
| 09_git_credentials | — | — | Requires credential storage and `resolveCloneAuth` for fetch authentication |

The campaign interaction features (pre-campaign sync, post-merge
piggybacking) depend on PRD 8 (Campaign Execution and Merge
Coordination) being implemented. The sync machinery itself can be
implemented and used independently via manual triggers.

## Technical Boundaries

- **Language:** Go (1.26+)
- **Foundation:** `github.com/txsvc/apikit` — server framework,
  authentication, CLI.
- **Git operations:** `github.com/go-git/go-git/v5` for fetch, HEAD
  reading, and branch manipulation (consistent with clone operations).
- **Credential reuse:** `resolveCloneAuth` from
  `internal/workspace/queue.go` resolves fetch credentials from the
  secrets store.
- **Schema migration:** Pre-production; schema changes are applied as DDL
  updates (no migration framework).

## Design Decisions

1. **Pull-only as the default sync mode.** Every workspace benefits from
   staying current with upstream. Pull-only solves the universal
   staleness problem with zero risk to the upstream repo — no write
   credentials are needed beyond read access, and no code is pushed
   without explicit operator action. Contribution modes (PR, direct push)
   are deferred until the downstream sync foundation is proven.

2. **Manual-first, event-gated later.** The MVP provides manual sync
   (`afc workspace sync`) and pre-campaign automatic sync. Periodic
   background sync is deferred — it can be layered on top without
   changing the sync machinery, API, or schema. This avoids introducing
   a background scheduler, goroutine lifecycle management, and interval
   configuration before they are needed.

3. **Pre-campaign sync is mandatory by default.** Campaigns that start
   from stale code produce results that are harder to contribute back.
   Mandatory pre-campaign sync ensures agents work against current
   upstream, reducing the reconciliation cost at contribution time. The
   `skip_sync` flag preserves operator control for reproducibility or
   when sync was just performed.

4. **Post-merge piggybacking for mid-campaign sync.** Rather than
   applying upstream changes at arbitrary times during a campaign
   (disruptive — equivalent to deploying during peak traffic), upstream
   advancement is deferred to the next post-merge cascade. Agents already
   expect a rebase after each merge; bundling upstream changes into that
   cascade makes upstream sync invisible to them. The `--force` flag
   overrides this for urgent upstream changes (security patches).

5. **Fetch eagerly, apply conservatively.** The fetch step always runs
   and updates `upstream_head_sha`, even during active campaigns. This
   gives the hub and operator visibility into drift without disrupting
   agents. The integration branch only advances at safe moments (between
   campaigns, at campaign start, after a merge, or when forced by the
   operator).

6. **Error-and-report for diverged history.** When upstream force-pushes,
   the sync operation reports the divergence and provides explicit
   recovery commands rather than silently resetting. In `pull_only` mode,
   `--reset-to-upstream` is safe because the integration branch has no
   local-only commits (agent work lives on spec branches). But the
   operator should be aware of the upstream event and choose the recovery
   path.

7. **Lazy unshallow on history errors.** Rather than preemptively
   unshallowing all workspaces (expensive), the sync function catches the
   specific "missing history" error from git, retries with `--unshallow`,
   and proceeds. This handles the rare case gracefully without the
   upfront cost.

8. **`sync_mode` column from day one.** Including the mode column
   immediately (even though only `pull_only` and `disabled` are
   implemented) avoids a schema migration when contribution modes are
   added later. The CHECK constraint is extended, not replaced.

9. **Sync is not a campaign phase.** Adding a `syncing` campaign status
   would complicate the campaign state machine for an operation that is
   semantically an integration branch update, not a campaign lifecycle
   event. The campaign observes sync through the existing rebase
   mechanism — the same way it observes agent merges.

10. **Reclone as nuclear recovery.** The `reclone` operation provides a
    clean recovery path when incremental sync cannot work (severely
    diverged history, corrupted repository state). It follows the
    existing archive/reactivate lifecycle — archive pushes what it can,
    deletes the local clone, and re-clones fresh. Active campaigns are
    cancelled because their spec branches reference commits that no
    longer exist in the re-cloned repository.

11. **`last_sync_at` updates on reactivation.** When a workspace is
    reactivated (re-cloned from upstream), it implicitly syncs to
    upstream HEAD. `last_sync_at` is updated to reflect this. This
    prevents stale `last_sync_at` values from triggering unnecessary
    syncs after reactivation.

12. **Archived workspaces do not track `upstream_head_sha`.** The local
    clone is deleted during archive — there is nothing to sync. The
    `head_sha` recorded at archive time is the relevant bookmark for the
    workspace's last known state.

13. **PR contribution granularity (future).** When PR contribution mode
    is implemented, the default will be one PR per campaign, with a
    per-workspace option to create one PR per merged spec. Per-campaign
    bundles changes for more manageable review; per-spec gives finer
    granularity at the cost of reviewer volume. This decision is deferred
    until real-world usage demonstrates which pattern teams prefer.

## Implementation Phases

These phases describe an incremental implementation path. Each phase
builds on the previous one without throwaway work. They are informational
— implementation is gated by individual specs, not by this phasing.

### Phase 1: Explicit-Only Pull Sync (MVP)

Add sync fields to the workspaces table. Implement `SyncFuncType`
(injectable function for git fetch + fast-forward, matching the existing
`CloneFuncType` pattern). Add `POST /workspaces/:slug/sync` API endpoint
and `afc workspace sync` CLI command. Add `POST /workspaces/:slug/reclone`
endpoint and `afc workspace reclone` CLI command. Include sync fields in
workspace GET responses. Reuse `resolveCloneAuth` for fetch
authentication.

Estimated scope: ~200–300 lines of new code in `internal/workspace/`.

Depends on: nothing — can be implemented immediately.

### Phase 2: Pre-Campaign Sync Gate

Add a sync step to the campaign creation handler that calls the Phase 1
sync machinery before DAG construction. Add `skip_sync` boolean to the
campaign create API request body.

Depends on: Phase 1 + PRD 8 campaign implementation.

### Phase 3: Post-Merge Upstream Piggybacking

After each successful merge in the merge queue, check whether
`upstream_head_sha` differs from the integration branch HEAD. If so,
include the upstream advancement in the same cascading rebase. Add a
background fetch goroutine per ready workspace with a configurable
interval (future — initially triggered only by manual sync or
pre-campaign sync).

Depends on: Phase 1 + PRD 8 merge queue implementation.

### Phase 4: Overlap-Aware Gated Sync

Before applying upstream changes to the integration branch during an
active campaign, compute file-level overlap between the upstream delta
and active spec branches using `git merge-tree --write-tree`. Auto-apply
when no overlap; gate and report to operator when overlap exists.

Depends on: Phase 3 + `git merge-tree` infrastructure from PRD 8.

### Phase 5: PR Contribution Mode

Add `sync_mode = 'pr_contribution'`. After campaign completion, push the
integration branch (or a named contribution branch) to upstream or a
configured fork remote, then create a pull request via the forge API.
Default granularity: one PR per campaign, with option for one PR per
merged spec. Requires forge API integration and write credentials.

Depends on: Phase 4 + clear demand signal from real-world usage.

## Open Questions

1. **go-git vs git CLI for fetch.** Clone operations use go-git. The
   merge queue and continuous rebase (PRD 8) use the git CLI via
   `GitRunner`. Should sync use go-git (consistent with clone) or
   `GitRunner` (consistent with merge operations)? The choice affects
   whether `--unshallow` and `--reset` operations use go-git APIs or
   subprocess calls.

2. **Check command during pre-campaign sync.** Should the hub run the
   configured check command (tests, lint) after advancing the integration
   branch during pre-campaign sync? This would catch upstream regressions
   before agents start, but adds latency to campaign creation.
