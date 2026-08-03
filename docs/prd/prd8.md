# Campaign Execution and Merge Coordination

## Intent

A workspace is a clone of an upstream repo. Multiple AI agents work on a set
of specs (a "campaign") in parallel, isolated from other agents or users
working on the same codebase for other purposes. Two problems must be solved:

1. **Conflict prevention** — minimize the chance that parallel agents produce
   conflicting changes in the first place.
2. **Conflict-safe merging** — when agents finish, serialize their merges back
   into the integration branch so that no work is lost or overwritten.

This PRD describes a campaign execution system that uses the spec dependency
DAG for intelligent scheduling (preventing conflicts through sequencing) and
continuous rebase as a safety net (catching conflicts between independent
specs early), backed by a sequential merge queue for the final integration
step.

The coordination logic lives entirely in the hub. Agents are stateless
workers: the hub dispatches them to a spec, they implement it on a branch
within the workspace, and they submit a merge request when done. They do not
manage branches, resolve ordering, or coordinate with other agents.

## Prior Art

The merge queue design is informed by analysis of
[claude-code-merge-queue](https://github.com/funador/claude-code-merge-queue),
a local CLI tool that serializes merge operations for Claude Code agents
sharing a repo via git worktrees. Key takeaways:

- **FIFO queue + filesystem locking** eliminates merge races by construction.
- **Rebase-then-fast-forward** is the merge strategy: rebase source onto
  target, run checks, fast-forward push. Conflicts abort with a file list.
- A sequential queue is sufficient for the throughput profile of parallel AI
  agents (~15–20 merges/hour with a 3–4 minute test suite).

The campaign scheduling layer (DAG-based execution, continuous rebase) is
original to the hub and has no direct prior art in the external tool.

Several infrastructure patterns are adopted from
[twigg-vc/monorepo](https://github.com/twigg-vc/monorepo), an open-source
VCS and forge built in Go with SQLite. Key takeaways:

- **Typed rejection reasons** (`CantSubmitReason` enum) replace error strings
  with compiler-checked cases for programmatic routing decisions.
- **Dry-run conflict check** (`CanRebaseWithoutConflict`) validates merges
  without side effects before committing to the operation.
- **Prepare-then-enqueue with nonce idempotency** prevents double-execution
  after crashes or retries.
- **Wakeup-on-enqueue** via a `buffered(1)` channel gives low-latency
  dispatch without busy-waiting.
- **Hardened git subprocess runner** wraps every `git` call with protocol
  allowlists, terminal prompt suppression, and uniform error formatting.
- **Cascading rebase with conflict-stop** propagates rebases via BFS but
  halts the cascade when a branch conflicts, avoiding wasted work on
  dependents of an already-conflicted branch.
- **Exponential backoff + dead-letter queue** handles transient failures
  gracefully and surfaces persistent failures for operator intervention.

## Goals

- Introduce campaigns as a first-class concept: a set of specs executed
  together against a shared integration branch within a workspace.
- Use the spec dependency DAG to schedule execution — dependent specs are
  sequenced; independent specs run in parallel.
- Create per-spec branches within the workspace when a spec's dependencies
  are satisfied, branching from the current integration branch HEAD.
- Dispatch agents to ready specs — the hub controls assignment, agents do not
  self-select work.
- After each successful merge, rebase all active spec branches onto the new
  HEAD. Block agents whose rebase conflicts until the conflict is resolved.
- Provide a sequential merge queue that agents submit to when their work is
  complete, serializing merges per target branch.

## Non-Goals

- **Task-group-level gating.** Dependencies in `tasks.json` are declared at
  the task-group level (`from_group` / `to_group`), but the hub gates at the
  spec level. A spec's branch is not created until all upstream specs it
  depends on have fully merged. Agents manage task-group ordering internally.
- **File-scope declarations in specs.** Specs do not declare which files or
  directories they will touch. Conflict detection is empirical (via rebase),
  not predictive.
- **Parallel merge execution.** Merges are strictly sequential (FIFO) per
  target branch.
- **Automatic conflict resolution.** The hub detects and reports conflicts;
  agents resolve them.
- **Cross-workspace campaign coordination.** A campaign operates within one
  workspace's git repository.

## Design

### Two-Layer Architecture

The system has two layers that compose:

| Layer | Purpose | Mechanism |
|-------|---------|-----------|
| **Campaign scheduler** | Prevent conflicts by controlling *when* specs start | Spec dependency DAG + per-spec branch creation + agent dispatch |
| **Merge queue** | Safely integrate completed work | Sequential FIFO queue with rebase-then-fast-forward |

Continuous rebase bridges the two: after each merge, the scheduler rebases
active spec branches and advances the DAG frontier.

### Campaign Lifecycle

A campaign is a named execution of a set of specs against a shared
integration branch within a single workspace.

**Workspace model.** A workspace is a clone of an upstream repo, created
deliberately by a user or operator. The campaign operates within this
workspace by creating per-spec branches off the integration branch. Each
agent receives a branch to work on — the workspace itself is shared
infrastructure, not per-agent.

**Creation.** A campaign can be created in two ways:

- **Implicit (default):** all specs in the workspace whose `tasks.json` has
  any subtask with `"state": "pending"` form the campaign. This is the
  default when no explicit selection is made.
- **Explicit:** the user or controller selects a specific set of specs and
  declares them as the campaign.

**DAG construction.** The hub reads `tasks.json` from each spec in the
campaign and extracts the `dependencies` array. Each dependency entry has the
form:

```json
{
  "depends_on_spec": "07",
  "from_group": 3,
  "to_group": 1,
  "relationship": "Uses secrets Store ...",
  "sentinel": false
}
```

The hub builds a DAG where vertices are specs and edges are dependency
relationships. For spec-level gating, the edge means: "this spec cannot start
until the upstream spec has fully merged." The `from_group` / `to_group`
granularity is recorded but not used for scheduling — the hub waits for the
entire upstream spec to merge before starting the downstream spec.

Dependencies referencing specs outside the campaign are treated as
pre-satisfied (their work is assumed to already be in the integration branch).

**Validation.** The hub validates:
- The DAG is acyclic (no circular dependencies).
- All `depends_on_spec` references within the campaign resolve to a spec in
  the campaign or to an already-completed spec.

**Statuses:** `pending` → `active` → `completed` / `failed` / `cancelled`.

**Scope constraint:** only one active campaign per integration branch at a
time. Creating a campaign on a workspace/branch combination that already
has an active campaign returns HTTP 409.

### Campaign Execution

```
Campaign: specs [A, B, C, D, E] in workspace "my-project"
DAG: A ──→ C ──→ E
     B ──→ D
     (A, B independent; C depends on A; D depends on B; E depends on C)

1. Scan DAG: A and B have no unmet dependencies → initial frontier.
   - Create branch spec/A from main@sha0
   - Create branch spec/B from main@sha0
   - Set spec/A and spec/B status to `active` (ready for agent dispatch)

2. Agent A completes → submits merge request → merge queue
   - Merge queue: rebase spec/A onto main, run checks, push → main@sha1
   - Post-merge: rebase spec/B onto main@sha1
     - Clean → B's agent continues, unaware
     - Conflict → B is blocked (push access revoked until resolved)
   - Advance DAG: A is merged → C's dependencies satisfied
   - Create branch spec/C from main@sha1, set status to `active`

3. Agent B completes → merge queue → main@sha2
   - Post-merge: rebase spec/C onto main@sha2
   - Advance DAG: B is merged → D's dependencies satisfied
   - Create branch spec/D from main@sha2, set status to `active`

4. Agent C completes → merge queue → main@sha3
   - Post-merge: rebase spec/D onto main@sha3
   - Advance DAG: C is merged → E's dependencies satisfied
   - Create branch spec/E from main@sha3, set status to `active`

5. Agents D, E complete → merge queue → main@sha4, main@sha5
   - DAG exhausted → campaign complete.
```

### Continuous Rebase

After every successful merge, the hub rebases all other active spec branches
in the campaign onto the new integration branch HEAD. This serves two
purposes:

1. **Keeps branches fresh.** Agents always work on a baseline that includes
   all previously merged work. Drift stays small.
2. **Early conflict detection.** If an independent spec happens to touch the
   same files as a just-merged spec, the rebase conflict surfaces immediately
   — not at merge time when more work has accumulated on top.

**On clean rebase:** the branch is updated silently. The agent's working copy
reflects the rebased state on next fetch. No action needed.

**On rebase conflict:** the hub blocks the agent by revoking push access to
the spec's branch via the git server. The agent is notified that its branch
has a conflict with recently merged work and must resolve it before push
access is restored.

### Merge Queue

The merge queue is the final integration step. It serializes merge operations
per target branch using a FIFO queue.

#### Merge Jobs

A merge job represents a request to integrate a spec branch into the
campaign's integration branch.

- A merge job has: `id` (UUID), `campaign_id`, `spec_id`, `workspace_slug`,
  `target_branch`, `source_ref` (spec branch name), `status`, `base_sha`
  (target HEAD when merge started), `merged_sha` (resulting HEAD on success),
  `conflict_details` (JSON file list on conflict), `check_output`
  (stderr/stdout on check failure), `submitted_by` (agent or user ID),
  `created_at`, `updated_at`.
- Status values: `queued`, `running`, `merged`, `conflict`, `check_failed`,
  `cancelled`, `push_failed`.
- A spec may have at most one `queued` or `running` merge job at a time.
  Submitting a duplicate is rejected.
- Only `queued` jobs can be cancelled. `running` jobs complete or fail.

#### Merge Algorithm

For each job, in order:

1. **Pre-check** (`CanMerge`): validate prerequisites and return a typed
   `CantMergeReason` if the job should not proceed (see Design Patterns).
   - `BeforeDependency` → re-enqueue with backoff.
   - `BranchNotReady` → re-enqueue with backoff.
   - `AlreadyMerged` → skip idempotently.
   - `SpecBlocked` → skip until unblocked.
2. **Dry-run conflict check**: run `git merge-tree --write-tree` to detect
   conflicts without side effects.
   - `WouldConflict` → set status to `conflict`, record file list, skip.
3. Acquire per-target-branch mutex.
4. Validate nonce (prepare-then-enqueue idempotency check).
5. Set status to `running`, record `base_sha` from current target HEAD.
6. Fetch latest target branch state.
7. Rebase source onto target.
   - On conflict: `git rebase --abort`, set status to `conflict`, record
     conflicting file paths in `conflict_details`, release mutex.
8. Run configured check command (if any). The check command is stored as
   the `CHECK_COMMAND` workspace variable (using the existing
   secrets/variables system from spec 07). If not set, the check step is
   skipped.
   - On failure: set status to `check_failed`, record output, release mutex.
9. Fast-forward push target branch to rebased HEAD.
   - On failure: set status to `push_failed`, release mutex.
10. Set status to `merged`, record `merged_sha`, release mutex.
11. **Wakeup** the campaign scheduler via the wakeup channel.
12. **Trigger post-merge actions** (executed by the campaign scheduler):
    - Cascading rebase of active sibling spec branches (with conflict-stop).
    - Advance the DAG frontier and dispatch agents to newly-unblocked specs.

#### Post-Merge Sequence

After step 10 (successful merge), the campaign scheduler is woken up and
executes these actions:

1. **Cascading rebase with conflict-stop.** BFS traversal of active spec
   branches in the campaign (excluding the just-merged one):
   - Attempt rebase onto the new integration branch HEAD.
   - On clean rebase: update branch SHA in `campaign_specs`, agent continues
     unaware.
   - On conflict: set spec status to `blocked`, revoke push access via git
     server, record conflicting files. **Stop the cascade** — do not rebase
     any specs that depend on the blocked spec (they would inevitably
     conflict too). Skipped specs are rebased when the blocked spec is
     unblocked.

2. **Advance DAG frontier.** Mark the merged spec as complete. For each spec
   whose dependencies are now all satisfied:
   - Create a new branch `spec/<spec_id>-<spec_name>` from the current HEAD.
   - Set the spec's campaign status to `active`.
   - The external controller detects the new `active` spec via the campaign
     API and launches an agent (see Agent Dispatch).

3. **Check campaign completion.** If all specs in the DAG are merged, set the
   campaign status to `completed`.

### REST API

#### Campaign Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/workspaces/:slug/campaigns` | Create a campaign |
| GET | `/api/v1/workspaces/:slug/campaigns` | List campaigns |
| GET | `/api/v1/workspaces/:slug/campaigns/:id` | Get campaign status |
| DELETE | `/api/v1/workspaces/:slug/campaigns/:id` | Cancel a campaign |
| POST | `/api/v1/workspaces/:slug/campaigns/:id/specs/:spec_id/resolve` | Signal conflict resolution |

**POST create request body:**

```json
{
  "name": "sprint-42",
  "spec_ids": ["07", "08", "09"],
  "integration_branch": "main"
}
```

If `spec_ids` is omitted or empty, the hub includes all specs with pending
subtasks (implicit campaign).

**Implicit campaign spec discovery:** the hub reads `tasks.json` from
each spec directory in the workspace clone at
`<WORKSPACE_ROOT>/<slug>/trunk/.agent-fox/specs/<spec_dir>/tasks.json`.
Specs whose `tasks.json` contains any subtask with `"state": "pending"`
are included in the implicit campaign.

**GET campaign response:**

```json
{
  "id": "string (UUID)",
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
      "status": "pending | active | merged | blocked | failed",
      "branch_name": "spec/07-secrets-variables | null",
      "branch_sha": "string (40-char hex SHA) | null",
      "conflict_details": "JSON array of file paths | null",
      "blocked_by_merge": "string (merge job UUID) | null"
    }
  ],
  "created_by": "string",
  "created_at": "string (RFC 3339)",
  "updated_at": "string (RFC 3339)"
}
```

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
  "source_ref": "spec/07-secrets-variables"
}
```

The hub infers `campaign_id` and `spec_id` from the `source_ref` branch
name (which follows the `spec/<spec_id>-<spec_name>` convention). If no
matching active campaign is found, the merge is accepted as a standalone
(non-campaign) merge with `campaign_id = NULL`. Standalone merges skip
post-merge campaign actions (no cascading rebase, no DAG advancement).

### Permissions

| Scope | Description |
|-------|-------------|
| `campaigns:read` | Query campaign status and DAG |
| `campaigns:write` | Create and cancel campaigns |
| `merges:read` | Query merge job status |
| `merges:write` | Submit and cancel merge jobs |

Admin tokens and API keys have implicit full access. PATs require explicit
scope grants.

### Agent Dispatch

The hub controls which specs are ready and which agents are assigned (via
the `campaign_specs` table). Agent process launch is delegated to an
external controller that polls the campaign API.

- The hub marks specs as `active` and creates branches. It does not spawn
  agent processes directly.
- The campaign API response includes per-spec status and branch
  information, giving the external controller everything it needs to
  launch an agent against the right branch.
- The hub is agnostic to agent runtime (Claude Code, Codex, custom
  agents). Process management belongs to the external controller.

### Spec Branch Naming

Spec branches follow the convention `spec/<spec_id>-<spec_name>` (e.g.
`spec/07-secrets-variables`). The campaign ID is not included in the
branch name because only one active campaign per integration branch is
allowed, so there is no collision risk.

### Spec Branch Blocking

When a post-merge rebase of an active spec branch conflicts, the hub blocks
the agent working on that spec:

- The spec's campaign status is set to `blocked`.
- The git server rejects push operations to that spec's branch. This is
  implemented via a `PushAuthorizer` function registered with the git
  server's receive-pack handler. Before executing a push, the handler
  calls the authorizer, which checks if the target ref is a campaign spec
  branch in `blocked` status and rejects the push with a descriptive
  pkt-line error if so.
- The campaign status response includes `conflict_details` (file list) and
  `blocked_by_merge` (the merge job ID that caused the conflict).
- The agent resolves the conflict locally, then calls the resolve endpoint:
  `POST /api/v1/workspaces/:slug/campaigns/:id/specs/:spec_id/resolve`.
  The hub verifies the branch rebases cleanly onto the current integration
  branch HEAD. If clean: push access is restored and the spec transitions
  back to `active`. If still conflicting: the endpoint returns an error
  with the conflicting file list.
- The resolve endpoint also rejects pushes to the campaign's integration
  branch directly — all merges must go through the merge queue.

### Design Patterns

The following patterns, adapted from twigg-vc/monorepo, form the
foundational infrastructure that the campaign scheduler and merge queue are
built on.

#### Typed Merge Rejection Reasons

Instead of returning errors for expected rejection scenarios, the merge queue
uses a typed enum that separates "expected rejection" from "unexpected
failure." The `CanMerge` pre-check returns `(bool, CantMergeReason, error)`
where the reason is programmatically matchable without string parsing.

Defined reasons:

| Reason | Meaning | Queue action |
|--------|---------|--------------|
| `BeforeDependency` | Upstream spec not yet merged | Re-enqueue with backoff |
| `WouldConflict` | Dry-run detected merge conflicts | Block spec, notify agent |
| `AlreadyMerged` | Spec branch already integrated | Skip idempotently |
| `BranchNotReady` | Agent has not pushed any commits | Re-enqueue with backoff |
| `SpecBlocked` | Spec is in `blocked` status (unresolved rebase conflict) | Skip until unblocked |

This shapes every merge queue decision path and prevents fragile
error-string matching.

#### Dry-Run Conflict Check

Before performing a real rebase, the hub runs a read-only conflict probe
using `git merge-tree --write-tree` (Git 2.38+). This checks for conflicts
without touching the worktree or index:

```
git merge-tree --write-tree <integration-head> <spec-branch-head>
```

If the exit code indicates conflicts, the merge is rejected early with a
structured conflict report (file paths, conflict markers). The integration
branch is never left in a dirty state by a failed merge attempt.

This is the Git-native equivalent of twigg's `CanRebaseWithoutConflict`,
which runs the full merge computation as a read-only probe.

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

The merge queue and campaign scheduler use a `buffered(1)` wakeup channel
to achieve low-latency dispatch without busy-waiting:

- When a merge completes and downstream specs become unblocked, a
  non-blocking send on the wakeup channel interrupts the scheduler's poll
  sleep.
- Multiple rapid enqueues coalesce into a single wakeup (the buffered
  channel drops duplicates).
- The scheduler's main loop uses a three-way select: shutdown signal, normal
  timer expiry, or wakeup.

This ensures newly-unblocked specs are dispatched within milliseconds of the
previous merge completing, rather than waiting for the next poll cycle.

#### Hardened Git Subprocess Runner

All git subprocess calls go through a `GitRunner` that enforces safety
defaults:

```go
type GitRunner struct {
    WorkDir string
    Env     []string // always includes safety defaults
}
```

Safety defaults applied to every invocation:
- `GIT_ALLOW_PROTOCOL=file:https:ssh` — prevents `ext::` and other
  dangerous protocol handlers.
- `GIT_TERMINAL_PROMPT=0` — prevents interactive credential prompts from
  hanging the hub process.
- `GIT_CONFIG_NOSYSTEM=1` — ignores system-level git config that could
  alter behavior.
- Uniform error formatting with command, exit code, and stderr captured.

This is built as `internal/gitcmd` and used by the merge queue, continuous
rebase, and any future git operations. Under 50 lines of code but provides
defense-in-depth for every git subprocess.

#### Cascading Rebase with Conflict-Stop

The post-merge rebase of sibling spec branches uses BFS traversal of the
campaign DAG. When a branch's rebase conflicts, the cascade **stops** for
that branch and all of its downstream dependents:

```
After merging spec A → main@sha1:

Rebase spec/B onto main@sha1 → clean ✓
Rebase spec/C onto main@sha1 → CONFLICT ✗
  → spec/C blocked
  → spec/E (depends on C) is NOT rebased — skipped
Rebase spec/D onto main@sha1 → clean ✓
```

This prevents wasting work rebasing downstream branches that will inevitably
conflict because their upstream dependency already has unresolved conflicts.
The skipped branches are rebased when the blocked branch is unblocked.

#### Exponential Backoff with Dead-Letter Queue

Failed merge jobs are retried with exponentially increasing delays:

- Base delay: 2 seconds
- Multiplier: 2x per retry
- Cap: 2 hours
- Max retries: 20

The retry is implemented via an `available_at` timestamp on the merge job —
the queue's polling query filters `WHERE available_at <= now()`, making
retried jobs invisible until their backoff window expires.

Jobs exceeding max retries are moved to a dead-letter state (`status =
dead_letter`) with the failure reason preserved. Dead-lettered jobs can be
inspected via the API and manually requeued after the underlying issue is
resolved.

#### Three-Way Exit Code Discrimination

When checking remote branch state, the hub distinguishes three outcomes:

| Exit code | Meaning | Action |
|-----------|---------|--------|
| 0 | Branch exists | Proceed |
| 2 | Branch genuinely missing | Create or error |
| 1 | Network/auth failure | Propagate error |

This uses `git ls-remote --exit-code` and prevents misinterpreting a network
timeout or auth failure as "branch does not exist" — which could silently
create a duplicate branch or lose track of branch state.

#### Graceful Shutdown

The merge queue uses `WaitGroup` + closed-channel broadcast for shutdown:

- `Stop()` closes a `stopCh` (broadcast signal to all workers).
- Workers check `stopCh` in their semaphore-acquisition select, preventing
  new dispatches after stop is requested.
- `stopWaitGroup.Wait()` blocks until all in-flight merge operations
  complete.

This ensures an interrupted rebase or merge never leaves the repository in a
broken state. In-flight operations finish cleanly before the process exits.

## Strategies Considered

Three merge strategies were evaluated before arriving at the two-layer design.
They informed the merge queue component but do not represent the full system.

### Strategy 1: Sequential FIFO Merge Queue (Adopted for merge layer)

A server-side FIFO queue that serializes all merge operations per target
branch. Rebase-then-fast-forward. Adopted as the merge queue component of the
two-layer design.

**Complexity:** Low. **Throughput:** ~15–20 merges/hour with 3–4 minute
checks. Sufficient for the target workload.

### Strategy 2: File-Level Conflict Detection with Speculative Merges

Pre-flight file-overlap analysis, parallel execution of non-conflicting
merges, serialized push phase. Rejected: adds significant complexity (parallel
workers, temp worktrees, two-phase queue) with marginal benefit given that the
campaign scheduler already prevents most conflicts through sequencing.

### Strategy 3: Dependency-Aware Priority Queue with Rebase Cascading

DAG management, priority levels, automatic rebase cascading, webhooks, merge
policies. Rejected as a merge-layer design: over-engineered for a
single-process SQLite system. However, the dependency DAG concept was adopted
at the campaign scheduling layer where it belongs — controlling when specs
start, not how merges are ordered.

## Implementation Sketch

### New Packages

| Package | Purpose |
|---------|---------|
| `internal/gitcmd/` | Hardened git subprocess runner with safety defaults |
| `internal/campaign/` | Campaign lifecycle, DAG construction, frontier tracking, post-merge orchestration |
| `internal/mergequeue/` | Merge job CRUD, FIFO queue, rebase-and-push logic |

### Database Schema

```sql
-- Campaigns

CREATE TABLE IF NOT EXISTS campaigns (
    id                  TEXT PRIMARY KEY,
    workspace_slug      TEXT NOT NULL,
    name                TEXT NOT NULL,
    integration_branch  TEXT NOT NULL,
    status              TEXT NOT NULL DEFAULT 'pending'
        CHECK(status IN ('pending','active','completed','failed','cancelled')),
    dag                 TEXT NOT NULL,   -- JSON: spec vertices + dependency edges
    created_by          TEXT NOT NULL,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    UNIQUE(workspace_slug, name)
);

CREATE TABLE IF NOT EXISTS campaign_specs (
    campaign_id   TEXT NOT NULL REFERENCES campaigns(id),
    spec_id       TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'pending'
        CHECK(status IN ('pending','active','merged','blocked','failed')),
    branch_name   TEXT,                 -- spec branch (NULL until active)
    branch_sha    TEXT,                 -- current HEAD of spec branch
    conflict_details TEXT,              -- JSON: conflicting files when blocked
    blocked_by_merge TEXT,              -- merge job ID that caused the block
    updated_at    TEXT NOT NULL,
    PRIMARY KEY(campaign_id, spec_id)
);

-- Merge jobs

CREATE TABLE IF NOT EXISTS merge_jobs (
    id               TEXT PRIMARY KEY,
    nonce            TEXT NOT NULL UNIQUE, -- cryptographic nonce for idempotency
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
    rejection_reason TEXT,               -- CantMergeReason enum value (NULL on success)
    retry_count      INTEGER NOT NULL DEFAULT 0,
    available_at     TEXT NOT NULL,       -- backoff: invisible to queue until this time
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
    ON merge_jobs(workspace_slug, status);
CREATE INDEX idx_merge_jobs_available
    ON merge_jobs(status, available_at);
```

### Key Components

**`internal/gitcmd/runner.go`** — `GitRunner` struct wrapping `exec.Command`
with `GIT_ALLOW_PROTOCOL`, `GIT_TERMINAL_PROMPT=0`, `GIT_CONFIG_NOSYSTEM=1`.
Three-way exit code discrimination for remote queries (`ls-remote
--exit-code`). Used by all other packages for git subprocess calls.

**`internal/campaign/dag.go`** — DAG construction from `tasks.json`
dependency arrays. Cycle detection (topological sort). Frontier computation:
specs whose upstream dependencies are all in `merged` status.

**`internal/campaign/scheduler.go`** — Campaign execution loop with
wakeup-on-enqueue (`buffered(1)` channel). Three-way select: shutdown /
timer / wakeup. On campaign start: compute initial frontier, create spec
branches, dispatch agents. On wakeup (merge completed): advance frontier,
create new branches, dispatch agents. On rebase conflict: block spec branch.
On campaign exhaustion: mark complete.

**`internal/campaign/rebase.go`** — Post-merge cascading rebase with
conflict-stop. BFS traversal of active sibling spec branches. On conflict:
block the branch and skip all its downstream dependents in the DAG.

**`internal/mergequeue/reason.go`** — `CantMergeReason` enum type with
defined constants (`BeforeDependency`, `WouldConflict`, `AlreadyMerged`,
`BranchNotReady`, `SpecBlocked`). Used by `CanMerge` pre-check and returned
in API responses.

**`internal/mergequeue/queue.go`** — FIFO merge queue with per-target-branch
workers, wakeup-on-enqueue channel, and graceful shutdown
(`WaitGroup` + `stopCh` broadcast). Polling query filters
`WHERE status = 'queued' AND available_at <= now()` for backoff support.
Jobs exceeding max retries transition to `dead_letter`.

**`internal/mergequeue/merge.go`** — `processMergeJob`: pre-check
(`CanMerge`), dry-run conflict check (`git merge-tree --write-tree`), nonce
validation, rebase source onto target, run checks, fast-forward push.
Injectable function variables (`mergeFn`, `checkFn`, `dryRunFn`) for
testability.

### Hub Integration

In `cmd/af-hub/main.go`:

- Initialize `gitcmd.NewRunner(workspacePath)` — shared git subprocess
  runner used by all packages.
- Initialize schemas: `campaign.InitSchema(db)`,
  `mergequeue.InitSchema(db)`.
- Start merge queue workers:
  `mergequeue.InitMergeQueue(ctx, db, gitRunner, workers)` — workers use
  graceful shutdown (`WaitGroup` + `stopCh`) to ensure in-flight merges
  complete before process exit.
- Start campaign scheduler:
  `campaign.InitScheduler(ctx, db, gitRunner, mergeQueue)` — wired to the
  merge queue's wakeup channel.
- Register permissions: `campaign.Permissions()`,
  `mergequeue.Permissions()`.
- Mount routes: `campaign.RegisterRoutes(apiGroup, db)`,
  `mergequeue.RegisterRoutes(apiGroup, db)`.
- Wire post-merge hook: merge queue sends on the campaign scheduler's wakeup
  channel after each successful merge, triggering cascading rebase + DAG
  advancement.

On startup, recover in-flight state: reset `running` merge jobs to `queued`,
re-evaluate active campaigns, re-enqueue pending merge jobs. Dead-lettered
jobs are surfaced via the API for operator inspection.

### Git Server Integration

In `internal/gitserver/handlers.go`, the `handleReceivePack` function
enforces campaign discipline:

- If the target ref is a spec branch in `blocked` status, reject the push
  with a message directing the agent to resolve the rebase conflict first.
- If push protection is enabled for the integration branch, reject direct
  pushes and require the merge queue.

## Dependencies

| Spec | From Group | To Group | Relationship |
|------|-----------|----------|--------------|
| 06_git_server | — | — | Campaign push blocking integrates with the git server's receive-pack handler via a PushAuthorizer callback |
| 07_secrets_variables | — | — | CHECK_COMMAND workspace variable uses the existing secrets/variables system |

## Design Decisions

1. **Dual git approach: go-git + git CLI.** go-git is used for clone
   operations (spec 05, already implemented). The git CLI via GitRunner is
   used for merge queue and rebase operations because go-git lacks
   `merge-tree --write-tree` (required for the dry-run conflict check) and
   has limited rebase support. The hub host requires git >= 2.38 installed.
   This dual approach avoids unnecessary migration of working clone
   infrastructure while providing the git CLI features the merge queue needs.

2. **Check command from workspace variables.** The post-rebase check command
   is stored as the `CHECK_COMMAND` workspace variable using the existing
   secrets/variables system (spec 07). This keeps configuration outside the
   repo and is already implemented. If `CHECK_COMMAND` is not set, the check
   step is skipped. Per-spec `test_commands` from `tasks.json` is not used
   because the check is a workspace-level quality gate, not a spec-level
   concern.

3. **Spec branch naming: `spec/<spec_id>-<spec_name>`.** Campaign ID is not
   included in the branch name because only one active campaign per
   integration branch is allowed (decision 7), so there is no collision risk.
   Short, readable branch names improve the developer experience when
   inspecting branches. If a spec is abandoned (campaign failed/cancelled),
   the branch is cleaned up when the campaign is cancelled.

4. **Agent dispatch via external controller polling.** The hub marks specs as
   `active` and creates branches but does not spawn agent processes. An
   external controller polls the campaign API for specs in `active` status
   and launches agents itself. This keeps the hub agnostic to agent runtime
   (Claude Code, Codex, custom agents) and avoids the hub needing process
   management capabilities. The campaign API response includes per-spec
   status and branch information, giving the controller everything it needs.

5. **Dedicated resolve endpoint for conflict resolution.** When a spec branch
   is blocked, the agent (or its controller) calls
   `POST /campaigns/:id/specs/:spec_id/resolve` after resolving the conflict
   locally. The hub verifies the branch rebases cleanly before restoring push
   access. This is explicit and auditable — the alternative (agent pushes and
   hub auto-retries) would require the hub to distinguish "conflict
   resolution push" from "new work push," adding implicit state tracking.

6. **Crash recovery: DB is source of truth.** On hub restart: reset `running`
   merge jobs to `queued`; re-evaluate active campaigns by recomputing the
   frontier from the DAG and current spec statuses in `campaign_specs`; specs
   with status `active` are assumed to still have agents working on them (the
   external controller manages agent lifecycle); specs with status `blocked`
   retain their state. Mid-rebase interruption is safe because the merge
   algorithm acquires a per-target-branch mutex and is atomic — each step
   either completes or aborts cleanly. The integration branch is never left
   in a dirty state.

7. **One active campaign per integration branch.** Multiple concurrent
   campaigns on the same integration branch would require cross-campaign
   rebase coordination (a merge from campaign A's spec must rebase campaign
   B's specs too). This complexity is not justified. Creating a campaign on a
   workspace/branch combination that already has an active campaign returns
   HTTP 409.

8. **Specs live in the cloned repo.** The hub reads `tasks.json` from
   `<WORKSPACE_ROOT>/<slug>/trunk/.agent-fox/specs/<spec_dir>/tasks.json`
   for each spec directory. This matches the convention used throughout the
   project. No additional configuration is needed — the hub discovers specs
   by scanning the `.agent-fox/specs/` directory in the workspace clone.

9. **Git server push blocking via PushAuthorizer.** The campaign package
   registers a `PushAuthorizer` function that the git server's receive-pack
   handler calls before executing a push. The authorizer checks if the target
   ref is a campaign spec branch in `blocked` status and rejects the push
   with a descriptive pkt-line error. It also rejects direct pushes to the
   campaign's integration branch, requiring the merge queue. This integrates
   with the existing `internal/gitserver/handlers.go` receive-pack handler
   by adding the check between request decode and session execution.

10. **Merge submission infers campaign context.** The POST merge body requires
    only `target_branch` and `source_ref`. The hub infers `campaign_id` and
    `spec_id` from the `source_ref` branch name (which follows the
    `spec/<spec_id>-<spec_name>` convention). If no matching active campaign
    is found, the merge is accepted as a standalone merge with
    `campaign_id = NULL`. Standalone merges skip post-merge campaign actions.

11. **Standalone merge queue.** The merge queue is usable without campaigns.
    When `campaign_id` is NULL on a merge job, the merge proceeds without
    campaign-specific logic (no cascading rebase, no DAG advancement). The
    standalone mode enables non-campaign merges (e.g., manual branch
    integration by an operator) and simplifies testing. The post-merge wakeup
    is sent to the campaign scheduler only when `campaign_id` is non-NULL.

12. **Campaign GET response includes full state.** The campaign GET response
    includes the DAG structure (specs and edges), per-spec status with branch
    info and conflict details, and campaign metadata. This gives the external
    controller everything it needs to know which specs are ready for agent
    dispatch and which are blocked.
