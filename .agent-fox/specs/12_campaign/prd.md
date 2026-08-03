---
spec_id: '12'
spec_name: campaign
title: Campaign
status: draft
created_at: '2026-08-03T13:31:42.584430+00:00'
updated_at: '2026-08-03T13:40:58.789465+00:00'
owner: ''
source: docs/prd/prd8.md
schema_version: 1
---
# Campaign Execution and Scheduling

## Intent

A campaign is a set of specs executed together against a shared integration
branch within a workspace. The campaign scheduler uses the spec dependency
DAG to control execution order — dependent specs are sequenced, independent
specs run in parallel. After each successful merge (via the merge queue),
the scheduler rebases active spec branches and advances the DAG frontier to
dispatch new work.

## Background

This spec is part of the campaign execution system described in the parent
PRD (`docs/prd/prd8.md`). The merge queue (spec 11) handles merge
serialization. The git subprocess runner (spec 10) handles git operations.
This spec covers the campaign lifecycle, DAG construction, spec branch
management, cascading rebase with conflict-stop, and the REST API for
campaign management.

## Goals

- Introduce campaigns as a first-class concept with lifecycle states
  (pending, active, completed, failed, cancelled).
- Build a spec dependency DAG from `tasks.json` files and validate it
  (acyclic, all dependencies resolvable).
- Create per-spec branches from the integration branch HEAD when a spec's
  dependencies are all satisfied.
- Track per-spec status within a campaign (pending, active, merged, blocked,
  failed, cancelled).
- After each successful merge, perform cascading rebase of active sibling
  spec branches with conflict-stop semantics (per-branch-subtree, not
  global).
- Advance the DAG frontier after merges — mark specs as complete, create
  branches for newly-unblocked specs.
- Block spec branches on rebase conflict by revoking push access via a
  PushAuthorizer callback registered with the git server.
- Provide a resolve endpoint for agents to signal conflict resolution.
  Verification always rebases onto the current integration branch HEAD.
- Expose REST endpoints for creating, listing (with status filtering),
  querying, and cancelling campaigns. Campaign creation returns a `warnings`
  array for any specs skipped due to missing or malformed `tasks.json`.
- Enforce one active campaign per integration branch (HTTP 409 on conflict).
- Transition the entire campaign to `failed` immediately when any single
  spec reaches terminal `failed` status (triggered by merge-queue dead-letter).
- Serialize concurrent PostMergeHook executions per-campaign using an
  in-memory mutex keyed by campaign ID.

## Non-Goals

- **Merge execution.** The merge queue (spec 11) handles merge
  serialization. The campaign scheduler receives a PostMergeHook callback
  from the merge queue, not the reverse.
- **Git subprocess management.** All git operations use `internal/gitcmd`
  (spec 10).
- **Agent process launch.** Agent dispatch is delegated to an external
  controller that polls the campaign API.
- **Task-group-level gating.** Dependencies are declared at the
  task-group level in `tasks.json`, but the hub gates at the spec level.
  Agents manage task-group ordering internally.
- **File-scope declarations.** Specs do not declare which files they touch.
  Conflict detection is empirical (via rebase), not predictive.
- **Partial-success campaigns.** There is no partial-success mode in this
  iteration. The campaign is atomic — a single spec failure fails the whole
  campaign. Operators may cancel and re-create a campaign excluding the
  failed spec.
- **Branch cleanup on cancellation.** Spec branches are left in place when
  a campaign is cancelled; cleanup is a manual or future-iteration concern.
- **Operator- or agent-driven spec failure.** In this iteration, only
  merge-queue dead-letter status triggers spec failure. A future PATCH
  endpoint for external spec failure signalling is out of scope.
- **Campaign size limits.** No hard limit on specs per campaign or DAG
  depth; expected scale is 10–50 specs. Batched or parallel rebase workers
  are a future-iteration concern.
- **List endpoint pagination.** Campaign count per workspace is expected to
  be low (tens, not thousands). Pagination can be added in a future
  iteration if needed.
- **Sentinel edge semantics.** The `sentinel` field in `tasks.json`
  dependency entries is read during DAG construction but has no effect on
  scheduling or edge construction in this iteration. It is reserved for
  future use cases (e.g., soft dependencies, notification-only edges).
- **PostMergeHook crash-recovery checkpointing.** If the hub crashes
  mid-hook, no checkpoint replay is performed on restart. The frontier is
  recomputed from DB state; the next hook invocation heals any
  partially-applied cascade. This is a documented known limitation.

## Design

### Campaign Lifecycle

A campaign is a named execution of a set of specs against a shared
integration branch within a single workspace.

**Creation modes:**

- **Implicit (default):** all specs in the workspace whose `tasks.json`
  has any subtask with `"state": "pending"` form the campaign.
- **Explicit:** the user selects specific spec IDs.

**Statuses:** `pending` → `active` → `completed` / `failed` / `cancelled`.

**`pending` → `active` transition:** The campaign creation endpoint is
synchronous. It computes the initial DAG frontier, creates spec branches for
all frontier specs, sets those specs to `active`, and transitions the
campaign itself from `pending` to `active` — all before returning the HTTP
201 response. A successful POST always returns an already-active campaign
with frontier branches ready for agent dispatch. The `pending` status exists
transiently during the creation request and is never observable by external
callers via the list or get endpoints (unless queried in an extremely narrow
race window before the creation request completes, which is not a supported
use case).

**Campaign failure policy:** Any single spec reaching terminal `failed`
status immediately transitions the entire campaign to `failed`. Spec failure
is triggered exclusively by the merge queue signalling a dead-letter job via
PostMergeHook (exhausted retries). There is no threshold or retry policy in
this iteration — the campaign is atomic. Operators who want partial
execution should cancel the campaign and re-create it excluding the failed
spec.

**Scope constraint:** only one active campaign per integration branch at a
time. Creating a campaign on a workspace/branch combination that already
has an active campaign returns HTTP 409.

**Name uniqueness:** Campaign names are permanent identifiers within a
workspace — the `UNIQUE(workspace_slug, name)` constraint applies across all
statuses (including completed and cancelled). This provides a clean audit
trail and unambiguous campaign references in logs and API responses. To
rerun a campaign, use a new name (e.g., `sprint-42-retry`).

### Cancellation Semantics

Cancellation is a soft operation:

1. Campaign status is set to `cancelled`.
2. All spec statuses are set to `cancelled`.
3. Spec branches are **left in place** on the git server (no deletion).
   Operators can inspect work-in-progress branches after cancellation.
4. No merge queue drain is performed. Any merge queue jobs already submitted
   for campaign specs will be rejected at `CanMerge` time, because
   `CanMerge` checks `campaign_specs.status` and will find `cancelled`.
5. There is no active agent notification mechanism. Agents and external
   controllers detect cancellation by polling campaign status via GET.

### Spec Failure Mechanism

A spec reaches terminal `failed` status via exactly one mechanism in this
iteration: the merge queue signals a dead-letter merge job through the
`PostMergeHook`. When a merge job for a campaign spec exhausts all retries
and enters dead-letter status, the hook sets the spec's status to `failed`
and immediately propagates campaign status to `failed`.

A future PATCH endpoint allowing an external controller or operator to
manually set a spec to `failed` is explicitly out of scope for this spec.

### Concurrency: PostMergeHook Serialization

The `PostMergeHook` acquires an in-memory mutex keyed by campaign ID before
performing any state mutations (rebase cascade, frontier advancement, status
transitions). This serializes concurrent hook executions for the same
campaign, preventing data races when two campaign specs targeting different
source branches merge within milliseconds of each other. The merge queue
already serializes merges for a given integration branch, so concurrent
hooks are possible but uncommon. No DB-level locking is used; the in-memory
mutex is sufficient given the single-hub deployment model.

### Branch Creation Atomicity

Campaign creation (POST /campaigns) is atomic with respect to spec branch
creation. If any frontier spec branch fails to be created at the git level
(e.g., corrupted repository, missing integration branch HEAD), the endpoint:

1. Deletes any branches that were successfully created during the current
   request.
2. Does **not** persist the campaign or any `campaign_specs` rows.
3. Returns HTTP 500 (internal git error) or HTTP 422 (invalid input, e.g.,
   integration branch does not exist).

Branch creation is a fast local git operation that rarely fails; when it
does, it indicates a fundamental problem that warrants a full rollback rather
than partial activation. Callers can safely retry the POST after the
underlying issue is resolved.

### Crash Recovery

On hub restart:
- Re-evaluate active campaigns by recomputing the frontier from the DAG
  and current spec statuses in `campaign_specs`.
- Specs with status `active` are assumed to still have agents working.
- Specs with status `blocked` retain their state.
- The per-campaign mutex map is re-initialized empty; locks are re-acquired
  as PostMergeHook invocations arrive post-restart.
- The DB is the source of truth; in-memory state (including the mutex map)
  is reconstructed from it.

**Known limitation — mid-hook crash:** If the hub crashes after a merge
completes but before the PostMergeHook finishes all state mutations (e.g.,
rebase cascade half-done, frontier not yet advanced), no checkpoint replay
is performed on restart. The campaign frontier is recomputed from current
`campaign_specs` status rows. The next PostMergeHook invocation processes
the full active spec set and heals any partial cascade, because rebasing an
already-rebased branch is a no-op. This is an accepted known limitation;
adding checkpoint machinery would add significant complexity with minimal
benefit given the rarity of hub crashes during hook execution.

### DAG Construction

The hub reads `tasks.json` from each spec in the campaign and extracts the
`dependencies` array. Each dependency entry has the form:

```json
{
  "depends_on_spec": "07",
  "from_group": 3,
  "to_group": 1,
  "relationship": "Uses secrets Store ...",
  "sentinel": false
}
```

**Field semantics for DAG construction:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `depends_on_spec` | string | Yes | Spec ID of the upstream dependency. |
| `from_group` | integer | Yes | Task group in the upstream spec. Used to derive spec-level edges; not persisted. |
| `to_group` | integer | Yes | Task group in the current spec. Used to derive spec-level edges; not persisted. |
| `relationship` | string | No | Human-readable description of the dependency. Defaults to empty string if absent. |
| `sentinel` | boolean | No | Reserved for future use. Treated identically to `false` in this iteration — has no effect on edge construction or scheduling. |

The hub builds a DAG where vertices are specs and edges are dependency
relationships. Edge semantics: "this spec cannot start until the upstream
spec has fully merged."

The `from_group`, `to_group`, and `sentinel` fields from `tasks.json` are
used during DAG construction to derive spec-level edges but are **not**
persisted. Only the spec-level edge (`from`, `to`, `relationship`) is stored.

Dependencies referencing specs outside the campaign are treated as
pre-satisfied.

**Validation:**
- The DAG is acyclic (topological sort).
- All `depends_on_spec` references within the campaign resolve to a spec
  in the campaign or to an already-completed spec.

### DAG JSON Schema

The `dag` column in the `campaigns` table stores JSON with the following
schema — identical to the API response shape:

```json
{
  "specs": ["07", "08", "09"],
  "edges": [
    {"from": "07", "to": "09", "relationship": "Uses secrets Store"}
  ]
}
```

Fields:
- `specs`: array of spec ID strings included in the campaign.
- `edges`: array of directed dependency edges. Each edge has:
  - `from` (string): the upstream spec ID (must merge first).
  - `to` (string): the downstream spec ID (blocked until `from` merges).
  - `relationship` (string): human-readable description copied from
    `tasks.json`. May be empty string if not provided.

The `from_group`, `to_group`, and `sentinel` fields from `tasks.json` are
intentionally excluded from the stored schema.

### Campaign Execution

1. **Compute initial frontier:** specs with no unmet dependencies.
2. **Create spec branches:** `spec/<spec_id>-<spec_name>` from integration
   branch HEAD (see Spec Branch Naming below). If any branch creation fails,
   roll back all created branches and return an error — do not persist the
   campaign (see Branch Creation Atomicity).
3. **Set frontier specs to `active`; set campaign to `active`.** All of
   this happens synchronously within the POST /campaigns request before
   the 201 response is returned.
4. **On merge completion** (PostMergeHook from merge queue, serialized
   per-campaign via mutex):
   a. If the merge job is a dead-letter failure: set spec to `failed`, set
      campaign to `failed`, release lock, return.
   b. Cascading rebase of active sibling branches (see below).
   c. Advance frontier — mark merged spec complete, create branches for
      newly-unblocked specs.
   d. Check campaign completion — if all specs merged, set campaign
      `completed`.
   e. Check for spec failure — if any spec transitions to `failed`, set
      campaign status to `failed` immediately.

### Spec Branch Naming

`spec/<spec_id>-<spec_name>` (e.g. `spec/07-secrets-variables`).

The `spec_name` component is derived deterministically from the spec
directory name in the workspace clone. Spec directories follow the
convention `NN_snake_case_name` (e.g., `07_secrets_variables`). The branch
name uses the numeric prefix as `spec_id` and the snake_case suffix
converted to kebab-case as `spec_name`. No additional metadata lookup is
required. Campaign ID is not included in the branch name because only one
active campaign per integration branch is allowed.

### Cascading Rebase with Conflict-Stop

After each merge, rebase all active spec branches onto the new integration
branch HEAD:

- **Order:** branches are processed in topological order of the DAG (roots
  first, leaves last). This ensures upstream branches are rebased before
  downstream branches within the same subtree.
- **Clean rebase:** update branch SHA in `campaign_specs`. Agent continues
  unaware.
- **Conflict:** set spec status to `blocked`, revoke push access via
  PushAuthorizer, record conflicting files. **Stop the cascade only for
  that branch's downstream dependents** — they would inevitably conflict
  too. Skipped specs are rebased when the blocked spec is unblocked.
- **Independent branches:** a conflict in one branch-subtree does not affect
  rebasing of branches in unrelated parts of the DAG. Conflict-stop
  semantics are per-branch-subtree, not global.

### Spec Branch Blocking (PushAuthorizer)

When a spec branch is blocked:

1. Spec status set to `blocked` in `campaign_specs`.
2. Git server rejects pushes to the branch via PushAuthorizer callback.
3. Campaign response includes `conflict_details` and `blocked_by_merge`.
4. Agent resolves conflict locally, then calls the resolve endpoint.
5. Hub verifies by rebasing onto the **current integration branch HEAD** at
   resolve time (not the HEAD at the time of blocking). If clean: restore
   push access, set status to `active`, write the new `branch_sha`. If
   still conflicting (including conflicts introduced by newer merges since
   the agent's local fix): return HTTP 409 with the updated conflict file
   list. The agent must resolve again. This process is self-healing and
   naturally incorporates all merges that occurred while the branch was
   blocked.

The PushAuthorizer also rejects direct pushes to the integration branch —
all merges must go through the merge queue.

### Resolve Endpoint Idempotency

The resolve endpoint (`POST .../specs/:spec_id/resolve`) is idempotent with
respect to retries:

| Spec status at call time | Response |
|--------------------------|----------|
| `blocked` | Perform rebase verification. Return 200 + new SHA on success; 409 + updated conflict list on failure. |
| `active` | Return 200 with current `spec_id`, `status: active`, and current `branch_sha`. No rebase performed. |
| `merged` | Return 200 with current `spec_id`, `status: merged`, and current `branch_sha`. No rebase performed. |
| `pending` | Return HTTP 409 (wrong state for resolution). |
| `failed` | Return HTTP 409 (wrong state for resolution). |
| `cancelled` | Return HTTP 409 (wrong state for resolution). |

This ensures agent retries due to network errors do not cause confusion or
spurious failures.

### Spec Discovery (Implicit Campaigns)

For implicit campaigns, the hub reads `tasks.json` from each spec
directory in the workspace clone at:
`<WORKSPACE_ROOT>/<slug>/trunk/.agent-fox/specs/<spec_dir>/tasks.json`

Specs whose `tasks.json` contains any subtask with `"state": "pending"`
are included.

**Error handling during discovery:**
- If a spec directory has **no `tasks.json`**: skip the spec silently. A
  missing `tasks.json` means no pending work; the spec is not included.
- If a spec directory has a **malformed or unparseable `tasks.json`**: skip
  the spec and add an entry to the `warnings` array in the campaign creation
  response (HTTP 201). Campaign creation still succeeds for all valid specs.
- The `warnings` array is also returned for explicit campaigns if a provided
  `spec_id` cannot be discovered or its `tasks.json` cannot be parsed.

### PostMergeHook Interface Ownership

The `PostMergeHook` interface is defined in `internal/mergequeue` (spec 11):

```go
// Defined in internal/mergequeue
type PostMergeHook func(ctx context.Context, job MergeJob)
```

The campaign package (`internal/campaign`) imports `internal/mergequeue`,
provides a concrete `PostMergeHook` implementation, and registers it when
wiring the merge queue worker at application startup. This maintains a
clean unidirectional dependency: `campaign` → `mergequeue`. There is no
circular import.

The `MergeJob` struct carries a `DeadLetter bool` field indicating whether
the job reached dead-letter status (exhausted retries). The campaign hook
inspects this field to distinguish successful merges from
failure-triggered invocations.

### REST API

#### Campaign Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/workspaces/:slug/campaigns` | Create a campaign |
| GET | `/api/v1/workspaces/:slug/campaigns` | List campaigns (filterable by status) |
| GET | `/api/v1/workspaces/:slug/campaigns/:id` | Get campaign status |
| DELETE | `/api/v1/workspaces/:slug/campaigns/:id` | Cancel a campaign (soft cancel) |
| POST | `/api/v1/workspaces/:slug/campaigns/:id/specs/:spec_id/resolve` | Signal conflict resolution |

**POST create request body:**

```json
{
  "name": "sprint-42",
  "spec_ids": ["07", "08", "09"],
  "integration_branch": "main"
}
```

If `spec_ids` is omitted or empty, implicit campaign (all pending specs).

**POST create response (HTTP 201):**

The response always reflects the campaign in `active` status because the
creation endpoint is synchronous — branches are created and frontier specs
are activated before the response is returned.

```json
{
  "id": "UUID",
  "workspace_slug": "string",
  "name": "string",
  "integration_branch": "string",
  "status": "active",
  "dag": {
    "specs": ["07", "08", "09"],
    "edges": [
      {"from": "07", "to": "09", "relationship": "Uses secrets Store"}
    ]
  },
  "specs": [...],
  "warnings": [
    "spec/11: tasks.json is malformed (unexpected EOF) — spec skipped"
  ],
  "created_by": "string",
  "created_at": "RFC 3339",
  "updated_at": "RFC 3339"
}
```

The `warnings` array is omitted or empty when no specs are skipped.

**GET /campaigns list query parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `status` | string | Optional. Filter by campaign status. One of: `active`, `completed`, `failed`, `cancelled`. If omitted, all campaigns are returned. |

No pagination is supported in this iteration. Campaign count per workspace
is expected to remain low (tens, not thousands).

**GET campaign response:**

```json
{
  "id": "UUID",
  "workspace_slug": "string",
  "name": "string",
  "integration_branch": "string",
  "status": "pending | active | completed | failed | cancelled",
  "dag": {
    "specs": ["07", "08", "09"],
    "edges": [
      {"from": "07", "to": "09", "relationship": "Uses secrets Store"}
    ]
  },
  "specs": [
    {
      "spec_id": "07",
      "status": "pending | active | merged | blocked | failed | cancelled",
      "branch_name": "spec/07-secrets-variables | null",
      "branch_sha": "40-char hex SHA | null",
      "conflict_details": ["file1.go", "file2.go"],
      "blocked_by_merge": "UUID | null"
    }
  ],
  "created_by": "string",
  "created_at": "RFC 3339",
  "updated_at": "RFC 3339"
}
```

**`conflict_details` serialization:** stored as a JSON array of file path
strings in the `campaign_specs.conflict_details` TEXT column
(e.g., `'["file1.go","file2.go"]'`). Deserialized directly into the API
response array. Consistent with how the `dag` column stores JSON; no
secondary table is required.

**`blocked_by_merge` semantics:** the UUID of the `MergeJob` from
`internal/mergequeue` whose successful merge triggered the rebase that
conflicted. This is stored for informational and audit purposes only — it
is **not** a foreign key into any database table. Operators can use it to
trace back to the specific merge event that caused the conflict. Null when
the spec is not blocked.

**Resolve endpoint response (success / idempotent):**

```json
{
  "spec_id": "07",
  "status": "active",
  "branch_sha": "40-char hex SHA"
}
```

When the spec is already `active` or `merged`, the same shape is returned
with the current status and SHA — no rebase is performed.

**Resolve endpoint response (still conflicting):**

HTTP 409 with conflict_details file list. The agent must resolve again.

**Resolve endpoint response (wrong state):**

HTTP 409 when spec is in `pending`, `failed`, or `cancelled` status.

### Permissions

| Scope | Description |
|-------|-------------|
| `campaigns:read` | Query campaign status and DAG |
| `campaigns:write` | Create, cancel campaigns, resolve conflicts |

Admin tokens and API keys have implicit full access.

### Database Schema

```sql
CREATE TABLE IF NOT EXISTS campaigns (
    id                  TEXT PRIMARY KEY,
    workspace_slug      TEXT NOT NULL,
    name                TEXT NOT NULL,
    integration_branch  TEXT NOT NULL,
    status              TEXT NOT NULL DEFAULT 'pending'
        CHECK(status IN ('pending','active','completed','failed','cancelled')),
    dag                 TEXT NOT NULL,  -- JSON: {specs:[...], edges:[{from,to,relationship}]}
    created_by          TEXT NOT NULL,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    UNIQUE(workspace_slug, name)        -- unique across all statuses; names are permanent identifiers
);

CREATE TABLE IF NOT EXISTS campaign_specs (
    campaign_id      TEXT NOT NULL REFERENCES campaigns(id),
    spec_id          TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'pending'
        CHECK(status IN ('pending','active','merged','blocked','failed','cancelled')),
    branch_name      TEXT,
    branch_sha       TEXT,
    -- JSON array of conflicting file paths, e.g. '["file1.go","file2.go"]'.
    -- Null when spec is not blocked. Serialized/deserialized identically to API response.
    conflict_details TEXT,
    -- UUID of the MergeJob that triggered the conflicting rebase.
    -- Informational only; not a foreign key. Null when spec is not blocked.
    blocked_by_merge TEXT,
    updated_at       TEXT NOT NULL,
    PRIMARY KEY(campaign_id, spec_id)
);
```

Note: `campaign_specs.status` includes `cancelled` to reflect soft
cancellation of individual specs when a campaign is cancelled.

### Package Structure

The campaign package lives in `internal/campaign/` with these components:

| File | Responsibility |
|------|---------------|
| `dag.go` | DAG construction, cycle detection, frontier computation, topological ordering for rebase |
| `scheduler.go` | Campaign execution loop, PostMergeHook implementation (with per-campaign mutex), campaign failure propagation, cancellation |
| `rebase.go` | Cascading rebase with per-subtree conflict-stop, resolve endpoint verification |
| `store.go` | Campaign/campaign_specs CRUD, schema initialization, DAG JSON serialization, conflict_details JSON serialization |
| `authz.go` | PushAuthorizer callback for git server integration |
| `handlers.go` | REST API handlers |

### PostMergeHook Integration

The campaign scheduler implements the `PostMergeHook` interface from the
merge queue (spec 11), which is defined in `internal/mergequeue`:

```go
// Defined in internal/mergequeue (spec 11)
type PostMergeHook func(ctx context.Context, job MergeJob)
```

The `internal/campaign` package imports `internal/mergequeue` and provides
a concrete implementation. There is no circular dependency.

The `MergeJob` struct includes a `DeadLetter bool` field indicating whether
the job exhausted retries. The hook dispatches on this field:

- **`DeadLetter == true`:** set spec to `failed`, immediately set campaign
  to `failed`, release per-campaign mutex, return.
- **`DeadLetter == false` (successful merge):**
  1. Trigger cascading rebase of sibling branches (topological order,
     per-subtree conflict-stop).
  2. Advance the DAG frontier.
  3. Check for campaign completion.
  4. Propagate any spec `failed` status to campaign `failed`.

For standalone merges (campaign_id=NULL), the hook is a no-op.

## Tech Stack

- **Language:** Go
- **Database:** SQLite via `database/sql`
- **Git operations:** `internal/gitcmd` (spec 10)
- **Merge queue:** `internal/mergequeue` (spec 11) — PostMergeHook
  interface defined there; campaign imports and implements it.
- **Git server:** `internal/gitserver` (spec 06) — PushAuthorizer
- **HTTP framework:** apikit (Gin-based)
- **Auth:** apikit patterns (`GetAuthInfo`, `AuthInfo`)
- **Testing:** `go test -race`, table-driven unit tests, real git repos for
  integration tests (rebase, conflict-stop, resolve flows), SQLite
  in-memory for store tests. Key scenarios requiring integration test
  coverage:
  - Cascading rebase with mid-cascade conflict (per-subtree stop, independent branches unaffected).
  - Resolve endpoint with advanced integration branch (verifies current HEAD, not blocked HEAD).
  - Campaign failure propagation from merge-queue dead-letter spec failure.
  - DAG frontier advancement after merge (branch creation for newly-unblocked specs).
  - Concurrent PostMergeHook invocations (verified via `-race` flag and explicit goroutine tests).
  - Resolve endpoint idempotency (already-active and already-merged spec returns 200).
  - Cancellation: CanMerge rejection of cancelled spec, branch left in place.
  - Synchronous creation: POST response always returns `active` campaign with frontier branches present.
  - Atomic rollback: POST /campaigns returns error and leaves no state when any branch creation fails.
  - List endpoint: status filter returns only matching campaigns.

## Dependencies

| Spec | From Group | To Group | Relationship |
|------|-----------|----------|--------------|
| 10_gitcmd | 7 | 1 | Uses GitRunner for all git operations (rebase, branch creation, fetch) |
| 11_mergequeue | — | — | Imports PostMergeHook type and MergeJob (including DeadLetter field) from mergequeue; campaign provides the concrete hook implementation |
| 06_git_server | — | — | Registers PushAuthorizer callback with receive-pack handler |

## Design Decisions

1. **Spec-level gating, not task-group-level.** The hub waits for the
   entire upstream spec to merge before starting a downstream spec. This
   simplifies DAG management and avoids the complexity of tracking
   individual task group completion.

2. **External controller for agent dispatch.** The hub marks specs as
   `active` and creates branches. An external controller polls the API and
   launches agents. This keeps the hub agnostic to agent runtime.

3. **Per-subtree conflict-stop in cascading rebase.** When a branch
   conflicts during post-merge rebase, only its downstream dependents in
   the DAG are skipped. Branches in unrelated parts of the DAG continue
   rebasing. This prevents wasted work while maximising parallelism for
   independent specs.

4. **PushAuthorizer for branch blocking.** Push access is revoked at the
   git server level, not at the application level. This provides a hard
   enforcement point that agents cannot bypass.

5. **Explicit resolve endpoint with current-HEAD verification.** Agents
   call POST /resolve after fixing conflicts locally. The hub verifies by
   rebasing onto the current integration branch HEAD (not the blocked HEAD),
   naturally incorporating all merges that occurred during the conflict.
   This is auditable and self-healing.

6. **One campaign per integration branch.** Multiple concurrent campaigns
   would require cross-campaign rebase coordination. HTTP 409 enforces
   this constraint.

7. **DAG stored as JSON in campaigns table with minimal schema.** The DAG
   structure stores only `{specs, edges: [{from, to, relationship}]}` —
   the from_group/to_group/sentinel fields from tasks.json are used during
   construction but not persisted. This keeps the stored schema simple and
   avoids coupling to tasks.json internals.

8. **Implicit campaign via pending subtask scan with warnings.** When no
   spec_ids are provided, the hub discovers specs by scanning
   `.agent-fox/specs/` in the workspace clone. Specs with missing or
   malformed `tasks.json` are skipped and surfaced in a `warnings` array
   in the creation response rather than failing the entire operation.

9. **Integration branch push protection.** The PushAuthorizer rejects
   direct pushes to the campaign's integration branch, requiring the merge
   queue. This prevents agents from bypassing the serialised merge flow.

10. **Atomic campaign failure.** Any single spec reaching `failed` status
    immediately fails the whole campaign. Spec failure is exclusively
    triggered by merge-queue dead-letter status in this iteration. There is
    no partial-success mode. This simplifies the scheduler state machine and
    makes campaign outcomes unambiguous.

11. **Campaign names are permanent workspace identifiers.** The
    `UNIQUE(workspace_slug, name)` constraint applies across all statuses.
    This provides an unambiguous audit trail — a campaign name always
    refers to the same execution in logs, API responses, and tooling.

12. **PostMergeHook interface defined in mergequeue.** The interface type
    lives in `internal/mergequeue` (spec 11), and `internal/campaign`
    imports and implements it. This maintains a clean unidirectional
    dependency and avoids circular imports.

13. **Synchronous campaign activation on creation.** The POST /campaigns
    endpoint computes the frontier, creates branches, and activates the
    campaign before returning 201. This makes the API predictable for
    external controllers: a successful POST always yields an immediately
    actionable active campaign. The transient `pending` status is not
    observable externally.

14. **Soft cancellation with branches left in place.** DELETE sets campaign
    and spec statuses to `cancelled`. Spec branches are retained for
    operator inspection. Cancelled specs are rejected at CanMerge time,
    preventing orphaned merge queue jobs from completing. Branch cleanup is
    deferred to a future iteration.

15. **In-memory per-campaign mutex for hook serialization.** Rather than
    introducing DB-level locking or a persistent job queue, concurrent
    PostMergeHook executions for the same campaign are serialized via a
    sync.Map of mutexes keyed by campaign ID. This is sufficient for the
    single-hub deployment model and requires no additional infrastructure.

16. **Spec branch name derived from directory naming convention.** The
    `spec_name` component of `spec/<spec_id>-<spec_name>` is derived
    deterministically from the spec directory (`NN_snake_case_name` →
    kebab-case suffix). No metadata lookup is required, keeping branch
    creation self-contained.

17. **Resolve endpoint idempotency.** Returning 200 for already-active or
    already-merged specs allows agents to safely retry the resolve call
    after network errors without causing spurious failures or unnecessary
    rebase operations.

18. **Spec failure exclusively via merge-queue dead-letter.** Restricting
    the failure trigger to one well-defined mechanism (exhausted merge
    retries) keeps the state machine simple and auditable. External failure
    signalling is deferred to a future iteration.

19. **Atomic branch creation rollback on POST /campaigns failure.** If any
    frontier spec branch cannot be created at the git level, all
    partially-created branches are deleted and the campaign is not
    persisted. This prevents partially-active campaigns that would require
    manual cleanup and ensures the POST is a clean all-or-nothing
    operation.

20. **conflict_details stored as JSON TEXT, no secondary table.** Consistent
    with the `dag` column pattern. The JSON array of conflicting file paths
    is serialized/deserialized in the store layer and maps 1:1 to the API
    response shape without requiring a join.

21. **blocked_by_merge stored for audit, not referential integrity.** The
    MergeJob UUID is stored as a plain TEXT column with no foreign key.
    Operators can cross-reference it against merge queue logs to trace the
    root cause of a conflict, without coupling the campaign schema to the
    merge queue schema.

22. **List endpoint filtered by status, no pagination.** Campaign volume
    per workspace is expected to remain in the tens. A simple
    `?status=` query parameter covers the primary use case (e.g., external
    controllers querying for active campaigns). Pagination is deferred.

23. **sentinel field ignored in this iteration.** The `sentinel` boolean
    in `tasks.json` dependency entries is parsed but has no effect on DAG
    edge construction or scheduling. It is reserved for future soft-
    dependency or notification-only edge semantics.

24. **Mid-hook crash accepted as known limitation.** Adding checkpoint
    machinery for PostMergeHook crash recovery would add significant
    complexity with minimal benefit. The cascade is idempotent, the
    frontier is recomputed from DB state on restart, and the next hook
    invocation heals any partial state.
