# Carry-Patch Workflow

## Intent

When the hub manages a workspace that is a fork of an upstream repository,
operators need to carry patches -- feature branches with pending upstream PRs --
and maintain an integration branch that combines the upstream base with all
carried patches. Today, the hub treats every workspace as a single-remote clone
with no concept of "upstream vs. fork" or "integration branch assembled from a
patch stack." Operators must manually track which patches are carried, manually
rebuild the integration branch when upstream advances, and manually detect when
upstream merges one of their PRs.

This PRD defines the carry-patch workflow: a workspace mode where the hub
manages a fork-upstream relationship, maintains a declarative ordered list of
patch branches, mechanically rebuilds an integration branch as
`upstream/main + patch1 + patch2 + ... + patchN`, automatically detects when
patches are merged upstream, and uses git rerere to replay previously-resolved
conflicts. The operator's job reduces to: maintain the patch list, resolve new
conflicts once, and approve rebuilds.

The primary beneficiary is any operator maintaining a fork with carried patches
-- the classic "vendor branch" problem faced by distributions, platform forks,
and product teams building on upstream projects.

## Goals

- Allow workspaces to operate in a carry-patch mode with a declared fork URL
  (origin) and upstream URL, each with independent credentials.
- Provide a declarative, ordered patch list per workspace that controls which
  branches are included in the integration branch and in what order.
- Mechanically rebuild the integration branch from scratch
  (`upstream HEAD + patches in order`) as a durable background job, never
  editing the integration branch directly. Support configurable rebuild
  strategy (rebase for linear history, or merge --no-ff for visible patch
  boundaries).
- Detect and report per-patch conflicts during rebuild using fail-fast
  semantics, with structured error output (which patch failed, which files
  conflicted).
- Integrate git rerere so that conflict resolutions are recorded once and
  replayed automatically on subsequent rebuilds.
- Detect when carried patches have been merged upstream and automatically
  remove them from the patch list after a successful rebuild.
- Provide a single status view showing the health of the entire patch stack:
  which patches are active, which have conflicts, when the last successful
  rebuild occurred, and what was recently merged upstream.
- Expose all operations via REST API and CLI, consistent with existing hub
  conventions.

## Non-Goals

- **Automatic conflict resolution beyond rerere.** The hub records and replays
  resolutions; it does not attempt AI-driven or heuristic conflict resolution.
- **GitHub PR lifecycle management.** The hub does not create, update, or
  close upstream PRs. The `upstream_pr_url` on a patch is metadata for
  operator reference, not a live integration.
- **Webhook-driven triggers.** Rebuilds and syncs are triggered via API or
  CLI. Webhook endpoints for GitHub push events are deferred.
- **Multi-level patch dependencies.** Patches are applied in a flat ordered
  list. Explicitly modelling dependency graphs between patches is out of
  scope.
- **Pushing the integration branch to the fork remote.** The hub rebuilds the
  integration branch locally within the workspace repository served by the
  built-in git server. Pushing to the fork's remote is an operator-initiated
  action outside the hub.
- **Stacked-diff tooling integration (stgit, jj).** The rebuild uses standard
  git rebase and merge. Integration with specialized patch-stack tooling is
  deferred until plain git proves insufficient.
- **Periodic background sync or rebuild scheduling.** This PRD provides the
  machinery; scheduling can be layered on top via a future spec.
- **Migration of existing standard workspaces to carry-patch mode.** The
  `workspace_mode` field is immutable after creation. Converting an existing
  standard workspace requires creating a new carry-patch workspace.

## Functional Requirements

### Workspace Mode and Upstream URL

A workspace can operate in one of two modes:

- `standard` (default): existing single-remote behavior. No upstream URL, no
  patch list, no rebuild operations. All existing workspace behavior is
  unchanged.
- `carry_patch`: dual-remote workspace with an upstream URL, a patch list, and
  rebuild operations.

When creating a workspace in `carry_patch` mode:

- The `upstream_url` field is required and must be a valid git URL (same
  validation as `git_url`: HTTPS or SSH format, non-empty host and path).
- The `git_url` field represents the fork (origin remote).
- The `upstream_url` represents the original upstream repository (upstream
  remote).
- The existing `branch` field specifies which upstream branch to track (e.g.,
  `main`). If null, the remote's default branch is used.
- A new `integration_branch` field specifies the name of the mechanically
  rebuilt integration branch (e.g., `deploy`). Defaults to `deploy` if not
  provided. This is the branch that agents and deployments consume.
- Both `workspace_mode` and `upstream_url` are immutable after creation.
- `integration_branch` is immutable after creation.
- Attempting to create a `carry_patch` workspace without `upstream_url`
  returns HTTP 400.
- Attempting to set `upstream_url` or `integration_branch` on a `standard`
  workspace returns HTTP 400.

When a `carry_patch` workspace is cloned:

- The initial clone uses `git_url` (the fork) as the origin remote, consistent
  with standard workspaces.
- After the clone completes, a second remote named `upstream` is added
  pointing to `upstream_url`.
- The repository is configured with `rerere.enabled=true` and
  `rerere.autoupdate=true`.
- A local branch named per `integration_branch` (default `deploy`) is created
  at the upstream tracking branch HEAD. This makes the workspace immediately
  usable -- the integration branch starts identical to upstream and diverges
  only when the first rebuild applies patches.

The workspace response schema gains three new fields:

```json
{
  "workspace_mode": "standard | carry_patch",
  "upstream_url": "string | null",
  "integration_branch": "string | null"
}
```

### Upstream Credentials

Carry-patch workspaces may need separate credentials for the upstream remote
(the upstream repository may be on a different host or require a different
access token than the fork).

- Upstream credentials are stored as workspace secrets with the prefix
  `UPSTREAM_`: `UPSTREAM_GIT_PAT` or `UPSTREAM_GIT_USERNAME` /
  `UPSTREAM_GIT_PASSWORD`.
- When the hub needs to authenticate to the upstream remote (fetch during sync
  or rebuild), it looks up upstream-prefixed credentials first. If none are
  found, it falls back to the standard workspace credentials (same
  PAT/password used for origin). This fallback handles the common case where
  the upstream is public or uses the same PAT.
- The CLI accepts `--upstream-git-pat` and `--upstream-git-username` /
  `--upstream-git-password` flags on `afc credential set`.
- Upstream credentials follow the same storage, encryption, and access control
  rules as existing workspace credentials (spec 09).

### Patch List Management

Each carry-patch workspace has an ordered list of patches. A patch represents
a branch that should be applied on top of the upstream base when rebuilding
the integration branch.

#### Patch fields

| Field | Type | Description |
|-------|------|-------------|
| `id` | TEXT (UUID) | Unique patch identifier, generated on creation. |
| `workspace_slug` | TEXT | The workspace this patch belongs to. |
| `branch_name` | TEXT | The git branch containing this patch's commits. |
| `position` | INTEGER | Application order (1-based). Lower positions are applied first. |
| `status` | TEXT | Patch lifecycle state (see below). |
| `upstream_pr_url` | TEXT (nullable) | URL of the corresponding upstream PR, for operator reference. |
| `description` | TEXT (nullable) | Human-readable description of what this patch does. |
| `added_at` | TEXT (RFC 3339) | When the patch was added to the list. |
| `updated_at` | TEXT (RFC 3339) | When the patch was last modified. |

#### Patch status lifecycle

| Status | Meaning |
|--------|---------|
| `active` | Patch is included in the next rebuild. |
| `merged_upstream` | Patch was detected as merged into upstream. Excluded from the next rebuild. Automatically removed from the list after a successful rebuild completes without it. |
| `conflict` | The most recent rebuild failed on this patch. Still included in rebuild attempts (the conflict may have been resolved since). |
| `disabled` | Operator has manually excluded this patch from rebuilds. |

The transition from `active` to `merged_upstream` is automatic (detected
during carry-patch sync). The transition from `merged_upstream` to deletion
is automatic (after a successful rebuild where the patch was not applied).
All other transitions are operator-initiated.

#### Constraints

- `(workspace_slug, branch_name)` is unique -- a branch cannot appear twice
  in the same patch list.
- `(workspace_slug, position)` is unique -- no two patches share the same
  position.
- Patches are only allowed on `carry_patch` workspaces. Attempting to add a
  patch to a `standard` workspace returns HTTP 400.

#### Patch operations

- **Add patch:** Appends to the end of the list by default. An optional
  `position` parameter inserts at a specific position, shifting existing
  patches down. If the specified branch does not exist in the workspace
  repository, the add succeeds (the branch may be created later; validation
  happens at rebuild time).
- **Remove patch:** Removes a patch and compacts positions (no gaps). The
  removed patch's position is reclaimed and all patches at higher positions
  shift up.
- **Update patch:** Modify position, status, description, or
  `upstream_pr_url`. Changing position shifts other patches accordingly.
- **List patches:** Returns all patches for a workspace in position order.
- **Reorder patches:** Accepts a complete ordered list of patch IDs
  representing the desired order. Assigns new positions based on the provided
  order. All patch IDs for the workspace must be included; partial reorder is
  rejected with HTTP 400.

#### REST API

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/workspaces/:slug/patches` | Add a patch |
| GET | `/api/v1/workspaces/:slug/patches` | List patches in position order |
| PATCH | `/api/v1/workspaces/:slug/patches/:id` | Update a patch |
| DELETE | `/api/v1/workspaces/:slug/patches/:id` | Remove a patch |
| POST | `/api/v1/workspaces/:slug/patches/reorder` | Bulk reorder patches |

**POST `/api/v1/workspaces/:slug/patches` request body:**

```json
{
  "branch_name": "feature/my-patch",
  "position": 3,
  "upstream_pr_url": "https://github.com/upstream/repo/pull/42",
  "description": "Add support for custom auth headers"
}
```

Only `branch_name` is required. `position`, `upstream_pr_url`, and
`description` are optional.

**POST `/api/v1/workspaces/:slug/patches/reorder` request body:**

```json
{
  "patch_ids": ["uuid-1", "uuid-3", "uuid-2"]
}
```

**Patch response schema:**

```json
{
  "id": "string (UUID)",
  "workspace_slug": "string",
  "branch_name": "string",
  "position": 1,
  "status": "active | merged_upstream | conflict | disabled",
  "upstream_pr_url": "string | null",
  "description": "string | null",
  "added_at": "string (RFC 3339)",
  "updated_at": "string (RFC 3339)"
}
```

#### CLI commands

```
afc patch add <workspace-slug> --branch <name> [--position <n>] [--upstream-pr <url>] [--description <text>]
afc patch remove <workspace-slug> <patch-id>
afc patch list <workspace-slug>
afc patch reorder <workspace-slug> <patch-id-1> <patch-id-2> ...
afc patch update <workspace-slug> <patch-id> [--position <n>] [--status <status>] [--description <text>] [--upstream-pr <url>]
```

#### Permissions

| Scope | Description |
|-------|-------------|
| `patches:read` | List and view patches |
| `patches:write` | Add, remove, update, and reorder patches |

Admin tokens and API keys have implicit full access. PATs require explicit
scope grants.

### Integration Branch Rebuild

The rebuild operation mechanically reconstructs the integration branch from
the upstream base plus all active patches. The integration branch is never
edited directly -- it is always destroyed and rebuilt from first principles.

#### Rebuild strategy

The rebuild strategy is configurable per workspace via the `REBUILD_STRATEGY`
workspace variable (using the existing secrets/variables system from spec 07):

| Strategy | Behavior |
|----------|----------|
| `rebase` (default) | Each patch is rebased onto the growing integration branch. Produces a linear history where every carry commit is self-identifying. Matches the convention used by OpenShift's `openshift/kubernetes` fork. |
| `merge` | Each patch is merged with `--no-ff` into the integration branch. Produces merge commits that clearly delineate patch boundaries. Each merge commit message includes the patch's branch name and description. |

If `REBUILD_STRATEGY` is not set or empty, `rebase` is used.

#### Rebuild algorithm

1. Fetch from the upstream remote using upstream credentials (or fallback to
   origin credentials).
2. Resolve the upstream tracking branch HEAD. The tracking branch is the
   workspace's `branch` field (defaulting to the remote's default branch if
   null).
3. Create a temporary branch at the upstream HEAD.
4. Collect the list of patches to apply: all patches with status `active` or
   `conflict`, in position order. Patches with status `merged_upstream` or
   `disabled` are skipped.
5. For each patch to apply, in position order:
   a. If strategy is `rebase`: attempt to rebase the patch branch onto the
      temporary branch.
   b. If strategy is `merge`: attempt to merge the patch branch into the
      temporary branch with `--no-ff`.
   c. If the operation succeeds: advance the temporary branch to the new HEAD.
   d. If the operation produces a conflict:
      - Check if git rerere can auto-resolve the conflict (rerere with
        `autoupdate=true` stages resolved files automatically).
      - If rerere resolved all conflicts: continue the rebase/merge and
        advance the temporary branch.
      - If unresolved conflicts remain: abort the rebase/merge, set the
        patch status to `conflict` in the database, record which files
        conflicted, and halt the rebuild (fail-fast).
6. If all patches applied cleanly:
   a. Force-update the integration branch ref (named per
      `integration_branch`, default `deploy`) to the temporary branch HEAD.
   b. Update `head_sha` in the database.
   c. Delete the temporary branch.
   d. Auto-cleanup: delete any patches with status `merged_upstream` from the
      patch list (they were skipped in this rebuild and are no longer needed).
      Compact remaining positions.
7. If the rebuild halted on a conflict:
   a. Delete the temporary branch.
   b. Do not update the integration branch.
   c. Report the failure with the conflicting patch ID, branch name, and list
      of conflicting files.

#### Per-patch result reporting

The rebuild produces a structured result with an entry for each patch
processed:

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

Patches with status `merged_upstream` or `disabled` are reported with
`"status": "skipped"`.

#### Rebuild as a durable job

- The rebuild is registered as a `"rebuild"` job type with the durable job
  queue (spec 10).
- `Key`: `<workspace_slug>` -- dedup prevents resubmitting the same rebuild.
- `Group`: `<workspace_slug>` -- serialized with other rebuilds and merges
  for the same workspace. This prevents concurrent workspace mutations.
- The rebuild job is **not retryable** on conflict (permanent error). It is
  retryable on transient failures (network errors during fetch, temporary
  file system errors).
- The rebuild job payload includes `workspace_slug`, `submitted_by`, and
  the `strategy` at time of submission (captured from `REBUILD_STRATEGY` when
  the job is enqueued, not at execution time, to ensure determinism).

#### REST API

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/api/v1/workspaces/:slug/rebuild` | Trigger an integration branch rebuild |
| GET | `/api/v1/workspaces/:slug/rebuilds` | List rebuild jobs |
| GET | `/api/v1/workspaces/:slug/rebuilds/:id` | Get rebuild job status with per-patch results |

**POST `/api/v1/workspaces/:slug/rebuild` response:**

Returns the job record with status `queued`, consistent with the merge job
submission pattern.

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
| Rebuild already queued or running for this workspace | 409 |

#### Permissions

| Scope | Description |
|-------|-------------|
| `rebuilds:read` | List and view rebuild jobs |
| `rebuilds:write` | Trigger rebuilds |

### Git Rerere Integration

Git rerere (reuse recorded resolution) records how conflicts are resolved and
automatically replays those resolutions when the same conflict recurs.

#### Configuration

- `rerere.enabled=true` and `rerere.autoupdate=true` are set in the
  repository config during carry-patch workspace clone.
- Rerere data is stored in the repository's `.git/rr-cache/` directory,
  managed by git.

#### Rebuild behavior

During rebuild, when a rebase or merge produces a conflict, the handler
checks whether rerere has auto-resolved it. With `autoupdate=true`, git
automatically stages files for which rerere has a recorded resolution:

- If rerere resolved all conflicts (no remaining conflict markers): the
  rebase/merge is continued and the rebuild proceeds to the next patch.
- If unresolved conflicts remain after rerere: the rebase/merge is aborted,
  the patch is marked as `conflict`, and the rebuild halts.

#### Resolution workflow

1. A rebuild fails on a conflict in patch `feature/foo`.
2. The operator examines the conflict: `afc rebuild status <slug> <id>` shows
   which files conflicted.
3. The operator resolves the conflict by updating the `feature/foo` branch
   (editing the branch locally and pushing via the git server, or using any
   external git workflow).
4. The operator triggers another rebuild (`afc rebuild submit <slug>`). This
   rebuild replays the same operation; git rerere records the new resolution.
5. On subsequent rebuilds, when the same conflict recurs (e.g., because
   upstream advanced but the patch didn't change), rerere replays the recorded
   resolution automatically.

#### Rerere management API

Operators may need to inspect or clear recorded resolutions (e.g., when a
previously-recorded resolution is no longer correct).

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/workspaces/:slug/rerere` | List recorded conflict resolutions (file paths with recorded resolutions) |
| DELETE | `/api/v1/workspaces/:slug/rerere/:pathspec` | Forget a specific recorded resolution |

**GET `/api/v1/workspaces/:slug/rerere` response:**

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

#### CLI commands

```
afc rerere list <workspace-slug>
afc rerere forget <workspace-slug> <pathspec>
```

### Carry-Patch Sync

For carry-patch workspaces, the existing sync operation (spec 13) is extended
with additional behavior. The core sync algorithm remains the same; the
changes affect which remote is fetched and what happens after a successful
fetch.

#### Sync behavior by workspace mode

| Behavior | `standard` workspace | `carry_patch` workspace |
|----------|---------------------|------------------------|
| Fetch remote | origin | upstream |
| Credentials | workspace credentials | upstream credentials (fallback to workspace credentials) |
| Branch advancement | Fast-forward integration branch | Fast-forward upstream tracking ref |
| Post-sync | Done | Check patches for upstream merge, optionally trigger rebuild |

#### Upstream merge detection

After a successful upstream fetch, the sync handler checks each active patch
for upstream merge:

- A patch is considered merged upstream when its commits are reachable from
  the new upstream HEAD. The detection uses an ancestry check: if the patch
  branch's HEAD is an ancestor of (or equal to) the upstream HEAD, the patch
  is considered merged.
- Patches detected as merged are transitioned to `merged_upstream` status.
- The sync response includes a list of patch branch names that were detected
  as merged.

This detection is a heuristic. It catches the common case (patch branch
merged via fast-forward or squash where the resulting commit is a superset of
the patch's changes). It may not catch all cases (e.g., cherry-picks that
produce different commit SHAs). False negatives are harmless -- the patch
remains active and the next rebuild will show it applies cleanly as a no-op.
Operators can manually transition patches to `disabled` or `merged_upstream`
for cases the heuristic misses.

#### Auto-rebuild after sync

After sync completes for a carry-patch workspace, if any patches were
transitioned to `merged_upstream` OR the upstream tracking ref advanced:

- If the workspace variable `AUTO_REBUILD_AFTER_SYNC` is `true` (default):
  enqueue a rebuild job.
- If `AUTO_REBUILD_AFTER_SYNC` is `false`: do not enqueue a rebuild. The
  operator triggers rebuilds manually.

#### Sync response extension

The sync response for carry-patch workspaces includes additional fields:

```json
{
  "patches_merged": ["feature/foo", "feature/bar"],
  "rebuild_triggered": true
}
```

These fields are omitted (or null) for standard workspaces.

### Carry-Patch Status Dashboard

A read-only aggregation endpoint that provides a comprehensive view of the
patch stack health for a carry-patch workspace.

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
rebuild job result; null if no rebuild has run.

#### CLI

```
afc workspace patch-status <workspace-slug>
```

Returns HTTP 400 if the workspace is not in `carry_patch` mode.

Permissions: requires `workspaces:read` (existing scope).

### Extended Git Runner Operations

The rebuild algorithm requires git CLI operations that the current GitRunner
(spec 11) does not provide. The following convenience methods are added,
following the existing safety and error-handling patterns (typed error returns,
safety environment variables, concurrent-use safety):

- **Checkout(ctx, branch):** Switch the working tree to an existing branch or
  detached HEAD. On failure, returns `*GitError`.
- **CreateBranch(ctx, name, startPoint):** Create a new branch at a specified
  commit or ref. On failure (e.g., branch already exists), returns
  `*GitError`.
- **DeleteBranch(ctx, name):** Delete a local branch. On failure, returns
  `*GitError`.
- **CherryPick(ctx, sha):** Apply a single commit. On conflict: auto-abort
  and return a `*CherryPickConflictError` with conflicting file paths. Same
  pattern as `RebaseConflictError`.
- **ConfigSet(ctx, key, value):** Set a repository-local config value (e.g.,
  `rerere.enabled`). On failure, returns `*GitError`.
- **RemoteAdd(ctx, name, url):** Add a named remote with a URL. On failure
  (e.g., remote already exists), returns `*GitError`.
- **Log(ctx, args...):** Query commit history with caller-specified arguments.
  Returns raw stdout.
- **Diff(ctx, args...):** Compare trees or commits with caller-specified
  arguments. Returns raw stdout.
- **MergeNoFF(ctx, branch):** Merge a branch with `--no-ff`. On conflict:
  auto-abort (`git merge --abort`) and return a `*MergeNoFFConflictError`
  with conflicting file paths. On success: returns the merge commit SHA.
- **RebaseContinue(ctx):** Continue a paused rebase (for use after rerere
  auto-resolves conflicts). On failure, returns `*GitError`.

New conflict error type:

```go
type CherryPickConflictError struct {
    ConflictingFiles []string
}

type MergeNoFFConflictError struct {
    ConflictingFiles []string
}
```

### Sync Working Tree Fix

The existing sync fast-forward (spec 13) updates the branch ref via
`Storer.SetReference` but does not update the working tree. This causes the
on-disk files to remain at the old commit after a sync fast-forward.

After advancing the branch ref during sync fast-forward, the working tree must
be reset to match the new HEAD SHA. This is applied to **both** standard and
carry-patch workspaces -- it is a correctness fix, not a carry-patch-specific
change. The pattern matches the existing `updateHeadSHA` function in
`internal/gitserver/handlers.go`, which performs a hard reset after
receive-pack.

If the working tree reset fails, the sync logs an error but does not fail
(the ref update is the critical operation; the working tree will be corrected
on the next push via the git server or the next rebuild).

### Error Responses

All new endpoints use the existing apikit `WriteAPIError` envelope, consistent
with all other hub endpoints. No new error envelope format is introduced.

**Carry-patch-specific error conditions:**

| Condition | HTTP Status |
|-----------|-------------|
| Carry-patch operation on a `standard` workspace | 400 |
| Create `carry_patch` workspace without `upstream_url` | 400 |
| Set `upstream_url` on a `standard` workspace | 400 |
| Invalid `upstream_url` format | 400 |
| Patch `branch_name` already in patch list for this workspace | 409 |
| Reorder with incomplete or duplicate patch ID list | 400 |
| Rebuild with no active or conflict patches | 400 |
| Rebuild already queued or running for this workspace | 409 |
| Upstream fetch failed during sync or rebuild (network, auth) | 502 |
| Rerere pathspec not found | 404 |
| Workspace not active or clone not ready | 400 |
| Patch not found | 404 |

## Technical Boundaries

- **Language:** Go (1.26+)
- **Foundation:** `github.com/txsvc/apikit` -- server framework,
  authentication, CLI.
- **Git operations:** go-git for transport-level operations (clone, fetch, ref
  manipulation, remote configuration, worktree reset). GitRunner (spec 11,
  extended) for rebase, merge --no-ff, checkout, branch management,
  cherry-pick, config set, and conflict detection.
- **Job queue:** Durable SQLite-backed job queue (spec 10) for rebuild jobs.
  Rebuild uses the same `group_key` serialization pattern as merge operations
  (spec 12).
- **Database:** SQLite (pure Go, no CGo). Schema changes via `ALTER TABLE`
  and new table creation (pre-production; no migration framework).
- **Git requirement:** git >= 2.38 on the hub host (inherited from spec 11).
- **Credential storage:** Existing secrets/variables system (spec 07) for
  upstream credentials (`UPSTREAM_GIT_PAT`, etc.) and rebuild configuration
  (`REBUILD_STRATEGY`, `AUTO_REBUILD_AFTER_SYNC`).

## Dependencies

| Spec | Relationship |
|------|-------------|
| 01_workspaces | Extended with `workspace_mode`, `upstream_url`, and `integration_branch` fields. |
| 05_workspace_checkout | Clone infrastructure extended for dual-remote setup and rerere config. |
| 07_secrets_variables | Upstream credential storage (`UPSTREAM_` prefix) and workspace variables (`REBUILD_STRATEGY`, `AUTO_REBUILD_AFTER_SYNC`). |
| 09_git_credentials | Upstream credential resolution via `resolveUpstreamAuth` function alongside existing `resolveCloneAuth`. |
| 10_durable_job_queue | Rebuild registered as a new `"rebuild"` job type. Uses existing `group_key` serialization. |
| 11_git_runner | Extended with checkout, branch, cherry-pick, merge --no-ff, config, remote, log, diff, and rebase-continue operations. |
| 12_merge_operations | Rebuild shares serialization group with merges to prevent concurrent workspace mutations. |
| 13_upstream_sync | Sync handler extended for carry-patch mode: upstream fetch, patch merge detection, auto-rebuild trigger. Working tree fix applied to all sync modes. |

## Design Decisions

1. **Workspace mode, not a separate entity.** A carry-patch workspace IS a
   workspace -- it needs clone, git server, credentials, sync, archive, and
   all existing lifecycle operations. Adding a separate entity would duplicate
   the entire workspace management stack. The `workspace_mode` field gates
   carry-patch-specific behavior while keeping the standard workspace path
   unchanged.

2. **`branch` tracks upstream, `integration_branch` is the derived output.**
   The workspace's `branch` field (e.g., `main`) specifies which upstream
   branch to track. The `integration_branch` field (default `deploy`) names
   the mechanically rebuilt branch. This separation is explicit: `branch` is
   the input (what we're building on), `integration_branch` is the output
   (what we're building). Agents and deployments point at the integration
   branch.

3. **Configurable rebuild strategy.** Both rebase (linear) and merge --no-ff
   (visible patch boundaries) are valid approaches with different trade-offs.
   Rather than forcing one, the strategy is configurable per workspace via the
   `REBUILD_STRATEGY` variable. Rebase is the default because it produces
   cleaner history for a branch that is force-pushed on every rebuild, and
   matches the convention used by OpenShift.

4. **Fail-fast on conflict.** When a rebuild encounters a conflict, it stops
   immediately rather than attempting remaining patches. The integration
   branch cannot be correctly assembled past a conflicting patch -- subsequent
   patches may depend on the conflicting patch's changes, and attempting them
   independently would produce false conflict reports that waste operator
   time.

5. **Merged patches auto-removed after successful rebuild.** Patches
   transitioned to `merged_upstream` by sync are excluded from the next
   rebuild. After that rebuild succeeds without them, they are automatically
   deleted from the patch list. This keeps the patch list clean without
   requiring operator intervention. The merged patch data (branch name,
   upstream PR URL) is preserved in the rebuild job result for audit purposes.

6. **Ancestry-based merge detection.** The sync handler uses an ancestry
   check (is the patch branch HEAD reachable from upstream HEAD?) to detect
   merged patches. This is a pragmatic heuristic: it catches the common
   merge and squash-merge cases. Cherry-picks that produce different SHAs are
   not detected, requiring manual operator intervention. More sophisticated
   detection (patch-id comparison, diff equivalence) can be added later
   without API changes.

7. **Rerere is enabled at clone time, not per-rebuild.** Setting
   `rerere.enabled=true` in the repo config at clone time means every
   rebase/merge/rebuild automatically benefits from recorded resolutions
   without any per-operation configuration. The rerere cache persists across
   rebuilds in the repository's `.git/rr-cache/` directory.

8. **Integration branch created at clone time.** Creating the integration
   branch at upstream HEAD during clone means the workspace is immediately
   usable. Agents can clone from the git server and start working before
   the first rebuild. The first rebuild advances the integration branch
   beyond upstream by applying patches.

9. **Rebuild strategy captured at enqueue time.** The `REBUILD_STRATEGY`
   value is read when the rebuild job is enqueued, not when it executes. This
   ensures determinism: an operator cannot change the strategy while a
   rebuild is queued and get unexpected behavior. The strategy is recorded in
   the job payload and the result.

10. **Sync working tree fix applies to all workspaces.** The stale working
    tree after sync fast-forward is a correctness bug that affects standard
    workspaces too. The fix is delivered as part of this PRD but benefits all
    workspace modes.

11. **Upstream credentials fall back to origin credentials.** Many forks use
    the same PAT for both upstream and origin (e.g., a GitHub PAT with access
    to both orgs). Requiring separate credentials for every carry-patch
    workspace would be unnecessarily cumbersome. The fallback eliminates
    configuration overhead for the common case while allowing separate
    credentials when needed.
