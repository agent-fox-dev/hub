---
spec_id: '16'
spec_name: carry_patch_operations
title: Carry Patch Operations
status: draft
created_at: '2026-08-16T16:05:26.807049+00:00'
updated_at: '2026-08-16T16:05:26.807049+00:00'
owner: ''
source: docs/prd/proposals/prd_carry_patch_workflow.md
schema_version: 1
---
# Carry-Patch Operations

## Source

docs/prd/proposals/prd_carry_patch_workflow.md (split 3 of 3)

## Intent

With carry-patch workspaces and their patch lists in place (see
carry_patch_workspace spec), operators need the machinery to actually rebuild
the integration branch, detect upstream merges, and monitor patch stack
health. This spec provides the operational layer: the rebuild algorithm that
mechanically reconstructs the integration branch from upstream + patches,
git rerere integration for automatic conflict replay, the carry-patch sync
extension for upstream merge detection, and a status dashboard for
monitoring.

The primary beneficiary is any operator maintaining a fork with carried
patches -- the classic "vendor branch" problem.

## Goals

- Mechanically rebuild the integration branch from scratch
  (`upstream HEAD + patches in order`) as a durable background job. Support
  configurable rebuild strategy (rebase via cherry-pick for linear history,
  or merge --no-ff for visible patch boundaries).
- Detect and report per-patch conflicts during rebuild using fail-fast
  semantics, with structured error output.
- Integrate git rerere so that conflict resolutions are recorded once and
  replayed automatically on subsequent rebuilds.
- Detect when carried patches have been merged upstream and automatically
  remove them from the patch list after a successful rebuild.
- Extend the existing sync handler for carry-patch workspaces to fetch from
  upstream, detect merged patches, and optionally trigger auto-rebuild.
- Provide a status dashboard showing patch stack health.
- Provide a rerere management API for inspecting and clearing recorded
  resolutions.
- Expose all operations via REST API and CLI.

## Non-Goals

- **Automatic conflict resolution beyond rerere.**
- **GitHub PR lifecycle management.**
- **Webhook-driven triggers.**
- **Multi-level patch dependencies.**
- **Pushing the integration branch to the fork remote.**
- **Periodic background sync or rebuild scheduling.**

## Functional Requirements

### Integration Branch Rebuild

The rebuild operation mechanically reconstructs the integration branch from
the upstream base plus all active patches. The integration branch is never
edited directly -- it is always destroyed and rebuilt from first principles.

#### Rebuild strategy

Configurable per workspace via the `REBUILD_STRATEGY` workspace variable:

| Strategy | Behavior |
|----------|----------|
| `rebase` (default) | Each patch's unique commits are cherry-picked onto the growing integration branch. Produces a linear history. |
| `merge` | Each patch is merged with `--no-ff`. Produces merge commits that delineate patch boundaries. |

If `REBUILD_STRATEGY` is not set or empty, `rebase` is used.

#### Rebuild algorithm

1. Fetch from the upstream remote using upstream credentials (resolved via
   `resolveUpstreamAuth` with fallback to origin credentials).
2. Resolve the upstream tracking branch HEAD. The tracking branch is the
   workspace's `branch` field (defaulting to the remote's default branch).
3. Create a temporary branch at the upstream HEAD.
4. Collect patches to apply: all patches with status `active` or `conflict`,
   in position order. Patches with status `merged_upstream` or `disabled`
   are skipped.
5. For each patch to apply, in position order:
   a. If strategy is `rebase`:
      - Determine commits unique to the patch branch:
        `git log --reverse --format=%H <upstream_head>..<patch_branch>`
      - Cherry-pick each commit onto the temporary branch in order.
      - This preserves the original patch branch refs -- no ref is modified.
   b. If strategy is `merge`:
      - Merge the patch branch into the temporary branch with `--no-ff`.
   c. If any operation succeeds: advance the temporary branch pointer.
   d. If any operation produces a conflict:
      - Check if git rerere can auto-resolve (rerere with `autoupdate=true`
        stages resolved files automatically).
      - If rerere resolved all conflicts: continue the cherry-pick/merge
        and advance.
      - If unresolved conflicts remain: abort, set the patch status to
        `conflict` in the database, record conflicting files, and halt
        (fail-fast).
   e. If a patch branch does not exist in the repository: skip the patch,
      report it as `skipped` in the results. Do not halt the rebuild.
6. If all patches applied cleanly:
   a. Force-update the integration branch ref to the temporary branch HEAD.
   b. Update `head_sha` in the database.
   c. Delete the temporary branch.
   d. Auto-cleanup: delete any patches with status `merged_upstream` from
      the database. Compact remaining positions.
7. If the rebuild halted on a conflict:
   a. Delete the temporary branch.
   b. Do not update the integration branch.
   c. Report the failure with the conflicting patch ID, branch name, and
      list of conflicting files.

#### Per-patch result reporting

```json
{
  "upstream_head_sha": "string (40-char hex SHA)",
  "integration_head_sha": "string (40-char hex SHA) | null",
  "strategy": "rebase | merge",
  "patches_applied": 5,
  "patches_skipped": 1,
  "patches_removed": 1,
  "patch_results": [
    {
      "status": "success | conflict | skipped",
      "patch_id": "string (UUID)",
      "branch_name": "string",
      "position": 1,
      "new_head_sha": "string (40-char hex SHA) | null",
      "conflict_files": []
    }
  ]
}
```

Patches with status `merged_upstream`, `disabled`, or missing branches are
reported with `"status": "skipped"`.

#### Rebuild as a durable job

- Registered as a `"rebuild"` job type with the durable job queue (spec 10).
- `Key`: `<workspace_slug>` -- dedup prevents concurrent rebuild submissions.
- `Group`: `<workspace_slug>:<integration_branch>` -- serialized with merges
  targeting the same branch. This prevents concurrent workspace mutations
  between rebuilds and merges on the integration branch.
- Not retryable on conflict (permanent error). Retryable on transient
  failures (network errors during fetch, temporary file system errors).
- Job payload includes `workspace_slug`, `submitted_by`, and the `strategy`
  at time of submission (captured from `REBUILD_STRATEGY` when enqueued, not
  at execution time, for determinism).

#### REST API

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/workspaces/:slug/rebuild` | Trigger a rebuild |
| GET | `/api/v1/workspaces/:slug/rebuilds` | List rebuild jobs |
| GET | `/api/v1/workspaces/:slug/rebuilds/:id` | Get rebuild job status |

POST returns the job record with status `queued`.

#### CLI commands

```
afc rebuild submit <workspace-slug>
afc rebuild list <workspace-slug>
afc rebuild status <workspace-slug> <rebuild-id>
```

#### Error responses

| Condition | HTTP Status |
|-----------|-------------|
| Rebuild on a `standard` workspace | 400 |
| Workspace not active or clone not ready | 400 |
| No active or conflict patches in the patch list | 400 |
| Rebuild already queued or running | 409 |

#### Permissions

| Scope | Description |
|-------|-------------|
| `rebuilds:read` | List and view rebuild jobs |
| `rebuilds:write` | Trigger rebuilds |

### Git Rerere Integration

Git rerere records how conflicts are resolved and replays those resolutions
when the same conflict recurs.

#### Configuration

- `rerere.enabled=true` and `rerere.autoupdate=true` are set in the
  repository config during carry-patch workspace clone (handled by the
  carry_patch_workspace spec).
- Rerere data is stored in `.git/rr-cache/`, managed by git.

#### Rebuild behavior

During rebuild, when a cherry-pick or merge produces a conflict:

1. Git rerere auto-checks if it has a recorded resolution.
2. With `autoupdate=true`, git stages files for which rerere has a resolution.
3. If all conflicts resolved (no remaining conflict markers in working tree):
   continue the cherry-pick/merge.
4. If unresolved conflicts remain after rerere: abort, mark as `conflict`.

For the `rebase` strategy (cherry-pick), rerere resolution is checked after
each individual cherry-pick. For the `merge` strategy, it's checked after the
merge attempt.

To check if rerere resolved all conflicts after a failed cherry-pick/merge:
run `git diff --name-only --diff-filter=U` to list files with unresolved
conflicts. If the list is empty, all conflicts were resolved by rerere.

#### Rerere management API

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/workspaces/:slug/rerere` | List recorded resolutions |
| DELETE | `/api/v1/workspaces/:slug/rerere/*pathspec` | Forget a resolution |

The DELETE endpoint uses an Echo wildcard parameter (`*pathspec`) to match
file paths containing slashes (e.g., `src/config.go`).

**GET response:**

```json
{
  "resolutions": [
    {
      "path": "src/config.go",
      "recorded_at": "string (RFC 3339) | null"
    }
  ]
}
```

The resolution list is derived from the `.git/rr-cache/` directory. Each
subdirectory in rr-cache represents a recorded resolution. The `path` is
obtained by reading the `preimage` or `postimage` files. `recorded_at` is
derived from the file modification time; null if unavailable.

**DELETE** executes `git rerere forget <pathspec>` via GitRunner. Returns
404 if the pathspec has no recorded resolution.

#### CLI commands

```
afc rerere list <workspace-slug>
afc rerere forget <workspace-slug> <pathspec>
```

### Carry-Patch Sync Extension

For carry-patch workspaces, the existing sync operation (spec 13) is extended.

#### Sync behavior by workspace mode

| Behavior | `standard` | `carry_patch` |
|----------|-----------|---------------|
| Fetch remote | origin | upstream |
| Credentials | workspace credentials | upstream credentials (fallback) |
| Branch advancement | Fast-forward integration branch | Fast-forward upstream tracking ref |
| Post-sync | Done | Check patches for merge, optionally trigger rebuild |

The carry-patch sync handler:

1. Resolves upstream credentials via `resolveUpstreamAuth`.
2. Fetches from the `upstream` remote (not `origin`).
3. Compares the upstream HEAD against the stored `upstream_head_sha`.
4. Advances the upstream tracking ref (same fast-forward logic as standard
   sync, but for the upstream remote).
5. After successful fetch, checks each active patch for upstream merge.
6. Optionally triggers an auto-rebuild.

#### Upstream merge detection

After a successful upstream fetch, check each active patch:

- A patch is considered merged upstream when its branch HEAD is an ancestor
  of (or equal to) the new upstream HEAD. Uses `git merge-base --is-ancestor`
  via GitRunner's `IsAncestor` method.
- Patches detected as merged are transitioned to `merged_upstream` status.
- The sync response includes the list of merged branch names.

This is a heuristic. It catches direct merges and fast-forward merges.
Squash merges that produce different commit SHAs are a known false negative.
False negatives are harmless -- the patch remains active and applies cleanly
as a no-op. Operators can manually transition patches for cases the heuristic
misses.

#### Auto-rebuild after sync

After sync completes for a carry-patch workspace, if any patches were
transitioned to `merged_upstream` OR the upstream tracking ref advanced:

- If workspace variable `AUTO_REBUILD_AFTER_SYNC` is `true` (default):
  enqueue a rebuild job.
- If `AUTO_REBUILD_AFTER_SYNC` is `false`: do not enqueue.

If a rebuild job is already queued or running (duplicate key), the enqueue
is silently ignored and `rebuild_triggered` is set to `false` in the sync
response. This accurately reflects that no new rebuild was triggered; the
existing queued rebuild handles the work.

#### Sync response extension

```json
{
  "patches_merged": ["feature/foo", "feature/bar"],
  "rebuild_triggered": true
}
```

These fields are omitted (or null) for standard workspaces.

### Carry-Patch Status Dashboard

A read-only aggregation endpoint providing a comprehensive view of the patch
stack health.

#### GET `/api/v1/workspaces/:slug/patch-status` response

```json
{
  "workspace_slug": "string",
  "workspace_mode": "carry_patch",
  "upstream_url": "string",
  "upstream_head_sha": "string (40-char hex SHA) | null",
  "integration_branch": "string",
  "integration_head_sha": "string (40-char hex SHA) | null",
  "last_sync_at": "string (RFC 3339) | null",
  "last_rebuild": {
    "id": "string (UUID)",
    "status": "completed | failed",
    "completed_at": "string (RFC 3339)",
    "strategy": "rebase | merge",
    "error": "string | null"
  },
  "patches": [
    {
      "id": "string (UUID)",
      "branch_name": "string",
      "position": 1,
      "status": "active | merged_upstream | conflict | disabled",
      "upstream_pr_url": "string | null",
      "description": "string | null",
      "last_rebuild_result": "success | conflict | skipped | null",
      "conflict_files": [],
      "rerere_resolution_count": 0
    }
  ],
  "summary": {
    "total_patches": 5,
    "active": 3,
    "merged_upstream": 1,
    "conflict": 1,
    "disabled": 0
  }
}
```

`last_rebuild` is null if no rebuild has been attempted. Each patch's
`last_rebuild_result` and `conflict_files` are populated from the most recent
rebuild job result; null if no rebuild has run. `rerere_resolution_count` is
the number of recorded rerere resolutions relevant to files touched by this
patch (0 if unknown or no resolutions).

Returns HTTP 400 if the workspace is not in `carry_patch` mode.

Permissions: requires `workspaces:read` (existing scope).

#### CLI

```
afc workspace patch-status <workspace-slug>
```

### End-to-End Execution Path

The full carry-patch user flow, from sync to rebuild to status check, traces
through all three specs in the split:

1. Operator creates a `carry_patch` workspace with `upstream_url`
   (carry_patch_workspace spec).
2. Clone sets up dual remotes and rerere config (carry_patch_workspace spec,
   using git_runner_extensions).
3. Operator adds patches to the patch list (carry_patch_workspace spec).
4. Operator triggers sync -> upstream fetch, merge detection, auto-rebuild
   (this spec).
5. Rebuild job executes: cherry-pick/merge patches onto upstream HEAD, rerere
   replays resolutions, integration branch updated (this spec).
6. Operator checks status dashboard (this spec).
7. On conflict: operator resolves, re-triggers rebuild, rerere records the
   resolution for future replays (this spec).

## Technical Boundaries

- **Language:** Go (1.26+)
- **Foundation:** `github.com/txsvc/apikit` for server, auth, CLI.
- **Git operations:** GitRunner (extended by git_runner_extensions spec) for
  cherry-pick, merge --no-ff, checkout, branch management, config, log, diff,
  is-ancestor. go-git for fetch and ref manipulation.
- **Job queue:** Durable SQLite-backed job queue (spec 10) for rebuild jobs.
- **Database:** SQLite (pure Go). Patches table from carry_patch_workspace.
- **Credential storage:** Existing secrets/variables system for upstream
  credentials and workspace variables (`REBUILD_STRATEGY`,
  `AUTO_REBUILD_AFTER_SYNC`).
- **Git requirement:** git >= 2.38 on the hub host.

## Dependencies

| Spec | From Group | To Group | Relationship |
|------|-----------|----------|--------------|
| 10_durable_job_queue | all | 1 | Rebuild registered as new job type |
| 12_merge_operations | all | 1 | Rebuild shares group_key serialization |
| 13_upstream_sync | all | 3 | Sync handler extended for carry-patch |
| 14_git_runner_extensions | all | 1 | Uses cherry-pick, merge-noff, checkout, branch, config, log, is-ancestor |
| 15_carry_patch_workspace | all | 1 | Uses workspace mode, upstream_url, patches table, resolveUpstreamAuth |

## Design Decisions

1. **Cherry-pick for rebase strategy.** Rather than `git rebase` which moves
   branch refs, the rebase strategy uses `git log` to identify unique commits
   and cherry-picks each onto the temporary branch. This preserves original
   patch branch refs across rebuilds.

2. **Fail-fast on conflict.** When a rebuild encounters a conflict, it stops
   immediately. Subsequent patches may depend on the conflicting patch, so
   attempting them independently would produce false conflict reports.

3. **Merged patches auto-removed after successful rebuild.** Patches
   transitioned to `merged_upstream` by sync are excluded from the next
   rebuild. After that rebuild succeeds without them, they're deleted from
   the patch list, keeping it clean.

4. **Ancestry-based merge detection.** Uses `git merge-base --is-ancestor`
   to detect merged patches. Catches direct merges and fast-forwards.
   Squash merges are a known false negative -- the patch remains active and
   applies cleanly. Operators can manually transition for missed cases.

5. **Rerere is enabled at clone time, not per-rebuild.** Setting it in repo
   config at clone time means every operation benefits automatically.

6. **Rebuild strategy captured at enqueue time.** Ensures determinism -- an
   operator can't change the strategy while a rebuild is queued.

7. **Rebuild group_key includes integration_branch.** Using
   `<slug>:<integration_branch>` serializes rebuilds with merges targeting the
   same branch, preventing concurrent mutations. Merges to other branches in
   the same workspace can proceed concurrently.

8. **Missing branches skipped, not fatal.** During rebuild, if a patch's
   branch doesn't exist, the patch is skipped and reported as such. This
   is less disruptive than halting the entire rebuild for a planning-stage
   patch that hasn't been created yet.

9. **Auto-rebuild duplicate silently ignored.** When sync tries to enqueue a
   rebuild but one is already queued, `rebuild_triggered` is `false`. This
   accurately reflects that no new rebuild was triggered; the existing one
   handles the work.

10. **Rerere pathspec uses Echo wildcard.** File paths contain slashes, so
    the DELETE endpoint uses `*pathspec` (Echo wildcard) to match the full
    path without requiring URL-encoding.

