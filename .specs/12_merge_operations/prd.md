---
spec_id: '12'
spec_name: merge_operations
title: Merge Operations
status: draft
created_at: '2026-08-04T10:11:50.248761+00:00'
updated_at: '2026-08-04T10:13:01.335556+00:00'
owner: ''
source: docs/prd/prd12.md
schema_version: 1
---
# Merge Operations

## Intent

When multiple agents complete work on separate branches within a workspace,
their results must be merged into the integration branch without races, lost
work, or dirty repository state. This spec provides merge operations that
integrate a source branch into a target branch using rebase-then-fast-forward
semantics, with conflict detection and structured error reporting.

Merge operations are registered as a job type with the durable job queue,
ensuring serialized execution per target branch. The REST API and CLI are thin
wrappers that enqueue merge jobs and query their status.

## Goals

- Provide merge operations using rebase-then-fast-forward semantics with
  conflict detection and structured error reporting.
- Provide branch rebase operations (single and batch) as reusable building
  blocks, available to higher-level systems.
- Provide dry-run conflict detection using `git merge-tree --write-tree`
  (via GitRunner from spec 11) before attempting real merges.
- Serialize merge execution per target branch via the durable job queue
  (spec 10) while allowing multiple merges to the same target to be queued
  simultaneously.
- Support a configurable check command (test suite) between rebase and merge,
  with rollback on failure.
- Delete source branches after successful merge.
- Expose merge operations via REST API and CLI.
- Expose batch rebase via REST API (no CLI).

## Non-Goals

- **Automatic conflict resolution.** The hub detects and reports conflicts;
  agents or operators resolve them.
- **Parallel merge execution.** Merges are strictly sequential per target
  branch. Sufficient for the throughput profile of parallel AI agents
  (~15-20 merges/hour with a 3-4 minute test suite).
- **Upstream synchronization.** Covered by the `upstream_sync` spec.
- **Orchestration or scheduling.** This spec provides git merge primitives.
  Higher-level concepts (campaigns, DAGs, agent dispatch) are out of scope.

## Functional Requirements

### Jobqueue Extension: Serialization Groups

The durable job queue (spec 10) needs a small extension to support merge
operations. Currently, the jobqueue's `(type, key)` pair serves double duty
as both the dedup key and the serialization key. For merges, these concerns
diverge:

- **Dedup:** prevent resubmitting the same merge (same source + target)
- **Serialization:** only one merge per target branch runs at a time (different
  sources can be queued)

**Ownership:** This extension is owned by the `merge_operations` spec. The
`durable_job_queue` spec is already implemented, and the Group extension is a
backward-compatible addition. This spec owns both the schema migration
(adding the `group_key` column) and the new `Group` field in `EnqueueParams`.
No changes to the `durable_job_queue` spec are required before implementing
`merge_operations`.

**Extension:** Add an optional `Group` field to `EnqueueParams` and a
`group_key TEXT NOT NULL DEFAULT ''` column to the `jobs` table.

- **Enqueue dedup** continues to use `(type, key)` — unchanged behavior.
- **Dispatch serialization** uses `(type, group)` when `group` is non-empty,
  falling back to `(type, key)` when empty — fully backward compatible.

The worker dispatch query changes from:
```sql
NOT EXISTS (SELECT 1 FROM jobs AS j2 WHERE j2.type = jobs.type
  AND j2.key = jobs.key AND j2.status = 'running' AND j2.id != jobs.id)
```
to:
```sql
NOT EXISTS (SELECT 1 FROM jobs AS j2 WHERE j2.type = jobs.type
  AND j2.group_key = jobs.group_key AND j2.status = 'running'
  AND j2.id != jobs.id)
```

When `group_key` is empty (default for all existing job types), serialization
degrades to per-row (each job has a unique empty group, so no serialization
applies beyond the existing `(type, key)` dedup). To preserve the existing
per-key serialization for job types that don't use groups, the fallback
actually uses: `CASE WHEN jobs.group_key != '' THEN jobs.group_key ELSE jobs.key END`.

For merge jobs:
- `Key`: `<workspace_slug>:<target_branch>:<source_ref>` — dedup prevents
  resubmitting the same merge
- `Group`: `<workspace_slug>:<target_branch>` — serialization ensures one
  merge per target runs at a time

### Merge Job Registration

The merge operation is registered as a `"merge"` job type with the durable
job queue. Registration happens during server boot. The handler function
implements the merge algorithm described below.

### Merge Job Payload

```json
{
  "workspace_slug": "string",
  "target_branch": "string",
  "source_ref": "string",
  "submitted_by": "string (username of the authenticated user)"
}
```

### `submitted_by` Resolution

The `submitted_by` field is populated from the authenticated user's identity:
- **API key:** the user's username from `AuthInfo`
- **Admin token:** "admin"
- **PAT:** the PAT owner's username from `AuthInfo`

### Merge Job Result

On success:
```json
{
  "base_sha": "string (40-char hex SHA)",
  "merged_sha": "string (40-char hex SHA)"
}
```

On failure, the job's error field contains a structured description. For
conflicts, the error includes the list of conflicting file paths.

### Typed Merge Rejection Reasons

Before attempting the actual merge, the handler runs a pre-check that returns
a typed reason if the merge should not proceed. This separates "expected
rejection" from "unexpected failure" without string parsing.

| Reason | Meaning | Handler action |
|--------|---------|----------------|
| `WouldConflict` | Dry-run detected merge conflicts | Return permanent error with conflict file list |
| `AlreadyMerged` | Source branch already integrated into target | Return success (idempotent) |
| `BranchNotReady` | Source branch has no commits ahead of target | Return retryable error |

### Dry-Run Conflict Check

Before performing a real rebase, the merge handler runs a read-only conflict
probe using `git merge-tree --write-tree` (Git 2.38+) via the GitRunner
(spec 11):

```
git merge-tree --write-tree <target-head> <source-branch-head>
```

If the exit code indicates conflicts, the merge is rejected with a structured
conflict report (file paths). The target branch is never left in a dirty state
by a failed merge attempt.

### Merge Algorithm

The merge handler executes these steps:

1. **Pre-check:** validate prerequisites and return a typed rejection reason
   if the merge should not proceed.
   - `WouldConflict` → permanent error with file list
   - `AlreadyMerged` → success (idempotent skip)
   - `BranchNotReady` → retryable error
2. **Fetch** latest target branch state from the upstream remote via go-git,
   using the same `resolveCloneAuth` credential resolution as the workspace
   checkout. This fetch is independent of the `upstream_sync` spec — merge
   fetches to ensure it has the latest target state before rebasing; sync
   fetches to advance the integration branch. The two operations are
   independent and use the same credential helper but do not share logic.
3. **Rebase** source onto target via GitRunner (`git rebase <target-ref>`).
   The pre-rebase SHA of the source branch is captured immediately before
   this step (before any git operations alter the branch).
   - On conflict: `git rebase --abort`, return permanent error with
     conflicting file paths.
4. **Run check command** (if configured). The check command is stored as the
   `CHECK_COMMAND` workspace variable (using the existing secrets/variables
   system from spec 07). If not set, the check step is skipped.

   **CHECK_COMMAND execution contract:**
   - **Working directory:** workspace clone root (`<WORKSPACE_ROOT>/<slug>/trunk/`)
   - **Execution:** run via `sh -c "<CHECK_COMMAND>"`
   - **Environment variables injected:**
     - `MERGE_TARGET` — target branch name
     - `MERGE_SOURCE` — source branch name (pre-rebase name)
     - `WORKSPACE_SLUG` — workspace slug
   - **Timeout:** 10 minutes (default, not currently configurable)

   On failure: roll back the rebase by running `git checkout <source-branch>
   && git reset --hard <pre-rebase-sha>` in the workspace clone root. This
   restores both the branch ref and the working tree to the pre-rebase state.
   Return permanent error with check output.

5. **Update target branch ref** to rebased HEAD via go-git reference update.
   This is a local ref advancement — the workspace repository is the canonical
   store served by the built-in git server; no remote push is involved.
   - On failure (e.g., ref lock contention): return retryable error.
6. **Delete source branch** from the local repository via go-git reference
   deletion.
7. Return success with `base_sha` (pre-merge target HEAD) and `merged_sha`
   (new target HEAD).

The per-group serialization from the job queue guarantees that only one merge
runs per target branch at a time — no additional mutex is needed.

**Working tree note:** Workspace repos are non-bare regular working trees
located at `<WORKSPACE_ROOT>/<slug>/trunk/`. The rebase (Step 3) operates on
this working tree. The rollback (Step 4 failure path) must restore both the
ref and the working tree using `git checkout <source-branch> && git reset
--hard <pre-rebase-sha>` executed via GitRunner.

### Branch Rebase

The hub provides a rebase operation that rebases a source branch onto a target
ref. This is a building block used by the merge handler and available to
higher-level systems.

- **Rebase operation:** `git rebase <target-ref>` on the source branch,
  executed via GitRunner (spec 11).
- **On clean rebase:** returns the new branch HEAD SHA.
- **On conflict:** `git rebase --abort`, returns a structured conflict report
  containing the list of conflicting file paths. The branch is left in its
  pre-rebase state.
- **Batch rebase:** given a list of branches, rebase each onto a target ref.
  On conflict for any branch, abort that rebase, report it, and continue with
  remaining branches (independent branches are not blocked by a sibling's
  conflict). Returns a per-branch result list.

### REST API

#### Merge Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/workspaces/:slug/merges` | Submit a merge request |
| GET | `/api/v1/workspaces/:slug/merges` | List merge jobs |
| GET | `/api/v1/workspaces/:slug/merges/:id` | Get merge job status |
| DELETE | `/api/v1/workspaces/:slug/merges/:id` | Cancel a queued job |

**POST `/api/v1/workspaces/:slug/merges` request body:**

```json
{
  "target_branch": "main",
  "source_ref": "feature/my-branch"
}
```

**GET merge job response:**

The response is a projection of the underlying job queue record with
merge-specific fields extracted from the payload and result:

```json
{
  "id": "string (UUID)",
  "workspace_slug": "string",
  "target_branch": "string",
  "source_ref": "string",
  "status": "queued | running | completed | failed | dead_letter | cancelled",
  "base_sha": "string (40-char hex SHA) | null",
  "merged_sha": "string (40-char hex SHA) | null",
  "conflict_files": ["path/to/file1", "path/to/file2"],
  "check_output": "string | null",
  "error": "string | null",
  "retry_count": 0,
  "submitted_by": "string",
  "created_at": "string (RFC 3339)",
  "updated_at": "string (RFC 3339)"
}
```

The merge endpoint handlers translate between the domain-specific merge
vocabulary and the generic job queue. `POST /merges` enqueues a job with
`type = "merge"`. `GET /merges` queries jobs of type `"merge"` scoped to
the workspace. `DELETE /merges/:id` cancels a queued job.

#### Batch Rebase Endpoint

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/workspaces/:slug/rebase` | Rebase branches onto a target ref |

**POST `/api/v1/workspaces/:slug/rebase` request body:**

```json
{
  "target_ref": "main",
  "branches": ["feature/a", "feature/b", "feature/c"]
}
```

**Response (200 OK):**

```json
{
  "results": [
    {"branch": "feature/a", "status": "ok", "new_head": "abc123def456..."},
    {"branch": "feature/b", "status": "conflict", "conflict_files": ["file1.go"]},
    {"branch": "feature/c", "status": "ok", "new_head": "def456abc789..."}
  ]
}
```

### CLI Commands

```
afc merge submit <workspace-slug> --target <branch> --source <branch>
afc merge list <workspace-slug>
afc merge status <workspace-slug> <merge-id>
afc merge cancel <workspace-slug> <merge-id>
```

### Permissions

| Scope | Description |
|-------|-------------|
| `merges:read` | Query merge job status and list merge jobs |
| `merges:write` | Submit and cancel merge jobs, trigger batch rebase |

Admin tokens and API keys have implicit full access. PATs require explicit
scope grants.

### Error Responses

| Condition | HTTP Status |
|-----------|-------------|
| Duplicate merge job for same source_ref and target_branch | 409 |
| Workspace not found | 404 |
| Workspace not active | 400 |
| Workspace clone not ready | 400 |
| Source or target branch not found in workspace | 400 |
| Missing required fields (target_branch, source_ref) | 400 |
| Batch rebase with empty branches list | 400 |

## Technical Boundaries

- **Language:** Go (1.26+)
- **Foundation:** `github.com/txsvc/apikit` — server framework,
  authentication, CLI.
- **Git operations:** go-git for fetch, ref updates, branch deletion;
  GitRunner (spec 11) for rebase, merge-tree, checkout.
- **Job queue:** durable job queue (spec 10) with Group extension for
  serialization. The Group extension (schema migration + `EnqueueParams`
  field) is owned and delivered by this spec.
- **Check command:** uses workspace variables system (spec 07) for
  `CHECK_COMMAND`; executed via `sh -c` in the workspace clone root with a
  10-minute timeout.
- **Git requirement:** git >= 2.38 on the hub host (for
  `git merge-tree --write-tree`).
- **Database:** SQLite (pure Go, no CGo).
- **Workspace layout:** non-bare working trees at
  `<WORKSPACE_ROOT>/<slug>/trunk/`.

## Dependencies

| Spec | From Group | To Group | Relationship |
|------|-----------|----------|--------------|
| 10_durable_job_queue | all | 1 | Merge operations are registered as a job type. This spec owns the backward-compatible Group extension (schema migration + EnqueueParams field). |
| 11_git_runner | all | 1 | Uses GitRunner for rebase, merge-tree, checkout, and rollback (git reset --hard). |
| 07_secrets_variables | - | - | CHECK_COMMAND workspace variable uses the existing secrets/variables system. |
| 05_workspace_checkout | - | - | Requires clone infrastructure and workspace directory structure (`<WORKSPACE_ROOT>/<slug>/trunk/`). Uses the same `resolveCloneAuth` credential helper for fetch. |

## Design Decisions

This spec was split from a larger PRD (prd12.md — Git Operations
Infrastructure) that covered three independent functional areas. This spec
covers merge operations; the git subprocess runner and upstream synchronization
are covered by the `git_runner` (spec 11) and `upstream_sync` specs.

1. **Jobqueue Group extension ownership.** The Group extension (adding
   `group_key` to the `jobs` table and modifying the dispatch query) is owned
   by this spec. The `durable_job_queue` spec is already implemented; this is
   a backward-compatible additive change delivered as part of `merge_operations`
   implementation. All existing job types remain unaffected (empty `group_key`
   degrades to existing per-key behavior).

2. **Source branch deletion.** The source branch is deleted after a successful
   merge. Agent work branches are consumed by the merge — leaving them would
   accumulate stale refs. If a merge fails, the source branch is preserved for
   retry or manual intervention.

3. **Check command rollback.** When the check command fails after a successful
   rebase, the rebase is rolled back using `git checkout <source-branch> &&
   git reset --hard <pre-rebase-sha>` (executed via GitRunner in the workspace
   clone root). This restores both the branch ref and the working tree, leaving
   the source branch in a consistent, mergeable state for retry after the check
   issue is fixed. The pre-rebase SHA is captured before any git operations
   begin.

4. **Local ref update, not remote push.** The merge step 5 updates the target
   branch ref locally via go-git. The workspace repository is the canonical
   store served by the built-in git server — agents clone from it and push to
   it. No remote push is involved.

5. **`submitted_by` resolution.** Maps to the authenticated user's username.
   For admin tokens, uses the literal string "admin". For PATs, uses the PAT
   owner's username. This provides human-readable attribution in merge job
   records.

6. **Batch rebase: API only, no CLI.** Batch rebase is exposed via REST API
   for programmatic use by higher-level systems but not via CLI. The merge CLI
   commands cover the primary interactive workflow; batch rebase is an advanced
   operation better suited to API consumers.

7. **go-git / GitRunner boundary.** Fetch uses go-git (transport-level,
   consistent with clone operations, using `resolveCloneAuth`). Rebase,
   merge-tree, and rollback (`git reset --hard`) use GitRunner (CLI operations
   that go-git does not support). Ref updates and branch deletion use go-git
   (ref manipulation).

8. **Merge fetch is independent of upstream_sync.** The merge handler performs
   its own go-git fetch (Step 2) to ensure it has the latest target branch
   state before rebasing. The `upstream_sync` spec also fetches from upstream,
   but for the distinct purpose of advancing the integration branch. Both use
   `resolveCloneAuth` for credential resolution but operate independently with
   no shared code path between the two fetch operations.

9. **Non-bare workspace repos and working tree rollback.** Workspace repos are
   non-bare (regular working trees at `<WORKSPACE_ROOT>/<slug>/trunk/`).
   Rollback after a check command failure must restore both the ref and the
   working tree. The chosen mechanism (`git checkout <branch> && git reset
   --hard <sha>`) is safe for non-bare repos and is consistent with GitRunner
   usage elsewhere in this spec.