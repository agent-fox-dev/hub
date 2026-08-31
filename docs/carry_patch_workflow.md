# Carry-Patch Workflow

This guide explains the carry-patch workflow in af-hub: a workspace mode for
maintaining a fork of an upstream repository with an ordered set of patch
branches applied on top. The hub mechanically rebuilds an integration branch
that combines the upstream base with all carried patches, detects when patches
are merged upstream, and manages conflict resolution across rebuilds.

For detailed endpoint schemas and CLI flag reference, see
[docs/api.md](api.md) and [docs/cli.md](cli.md).

---

## The problem carry-patch solves

Organizations that operate a fork of an open-source project (or any upstream
repository they do not control) face a recurring challenge: they need to carry
local modifications -- configuration changes, bug fixes not yet merged,
security patches, vendor-specific features -- on top of a moving upstream
baseline.

The typical approach is manual. An engineer fetches the latest upstream,
rebases or cherry-picks each local branch, resolves conflicts, and pushes a
combined result. When there are five or ten patches and upstream moves daily,
this becomes a significant maintenance burden. Conflicts that were resolved
once reappear after a force-push or rebase. Patches that have been merged
upstream linger in the stack because nobody noticed.

The carry-patch workflow automates this. You declare the upstream repository,
register your patch branches in a specific order, and the hub takes care of
the rest: fetching from upstream, detecting merged patches, rebuilding the
integration branch, and remembering conflict resolutions via git rerere.

**Example scenario:** Your team maintains a fork of
`github.com/open-source-org/platform` at
`github.com/your-org/platform-fork`. You carry three patches submitted as
upstream PRs, plus one internal-only change. You want a `deploy` branch that
always reflects "upstream HEAD plus all four patches, in order" and is ready
to deploy to your environment.

---

## Concepts

### Workspace modes

Every af-hub workspace operates in one of two modes, set at creation time and
immutable afterward:

| Mode | Description |
|------|-------------|
| `standard` | Default. Single-remote workspace for branch-based development. |
| `carry_patch` | Dual-remote workspace with an ordered patch list and mechanical integration branch rebuild. |

### Remotes

A carry-patch workspace has two git remotes configured in the cloned
repository:

| Remote | Points to | Purpose |
|--------|-----------|---------|
| `origin` | Your fork (the `git_url` from workspace creation) | Where patch branches live; push target for local work |
| `upstream` | The upstream project (the `upstream_url` from workspace creation) | Source of truth for the base; fetched during sync and rebuild |

### Integration branch

The integration branch is a mechanically maintained branch that represents
"upstream HEAD plus all active patches applied in order." By default it is
named `deploy`, but you can choose any name at workspace creation time.

You should never commit directly to the integration branch. It is
force-updated by the rebuild process and any manual commits will be
overwritten.

### Patch list

The patch list is an ordered sequence of branch names registered with the
workspace. Each patch has a 1-based position that determines the order in
which branches are applied during a rebuild. The hub stores metadata for each
patch:

| Field | Description |
|-------|-------------|
| `branch_name` | Git branch in the workspace repository |
| `position` | 1-based application order (lower = applied first) |
| `status` | Current lifecycle state (see below) |
| `upstream_pr_url` | Optional link to the corresponding upstream pull request |
| `description` | Optional free-form description |

### Patch statuses

| Status | Meaning |
|--------|---------|
| `active` | Normal state. The patch is applied during rebuilds. |
| `merged_upstream` | The hub detected that this patch's commits are ancestors of upstream HEAD. The patch is skipped during rebuilds and deleted after a successful rebuild completes. |
| `conflict` | The most recent rebuild failed with unresolved conflicts on this patch. The patch remains in the list and will be retried on the next rebuild. |
| `disabled` | Manually disabled by an operator. Skipped during rebuilds but not deleted. |

### Rebuild strategies

The rebuild strategy determines how each patch branch is applied on top of
upstream HEAD:

| Strategy | Behavior |
|----------|----------|
| `rebase` (default) | Cherry-picks individual commits from each patch branch onto the integration branch. Produces a linear history. |
| `merge` | Merges each patch branch with `--no-ff`. Preserves branch topology in the integration history. |

The strategy is controlled by the `REBUILD_STRATEGY` workspace variable.

### Git rerere

The hub enables git rerere (reuse recorded resolution) in every carry-patch
workspace. When a conflict is resolved during a rebuild -- either manually or
automatically from a previously recorded resolution -- git remembers the
resolution. On subsequent rebuilds, the same conflict is resolved
automatically without operator intervention.

This is particularly valuable when upstream force-pushes or when the same
file changes on both sides across multiple rebuild cycles.

---

## Getting started

This walkthrough uses a concrete example: maintaining a fork of
`github.com/acme-oss/api-gateway` at `github.com/your-org/api-gateway-fork`
with three patch branches.

### 1. Create a carry-patch workspace

```
afc workspace create \
  --slug api-gateway \
  --git-url https://github.com/your-org/api-gateway-fork.git \
  --workspace-mode carry_patch \
  --upstream-url https://github.com/acme-oss/api-gateway.git \
  --integration-branch deploy \
  --git-pat ghp_fork_token_here
```

The `--integration-branch` flag is optional and defaults to `deploy`. The hub
clones the fork, adds an `upstream` remote pointing to the upstream URL,
enables rerere, and creates the integration branch.

### 2. Set upstream credentials (if the upstream repo is private)

If the upstream repository requires authentication, store credentials
separately from the fork credentials:

```
afc credential set api-gateway --upstream-git-pat ghp_upstream_token_here
```

Alternatively, use username/password authentication:

```
afc credential set api-gateway \
  --upstream-git-username your-bot-user \
  --upstream-git-password ghp_upstream_token_here
```

The hub resolves upstream credentials in this priority order:

1. `UPSTREAM_GIT_PAT` workspace secret
2. `UPSTREAM_GIT_USERNAME` + `UPSTREAM_GIT_PASSWORD` workspace secrets
3. Falls back to the origin credentials (`GIT_PAT` or `GIT_USERNAME` +
   `GIT_PASSWORD`) if no upstream-specific credentials are set

For public upstream repositories, no credential setup is needed.

### 3. Add patches

Register your patch branches in the order they should be applied. The branch
must exist in the workspace repository (pushed to `origin`), though this is
not validated until rebuild time.

```
afc patch add api-gateway \
  --branch feature/custom-auth-headers \
  --upstream-pr https://github.com/acme-oss/api-gateway/pull/142 \
  --description "Add X-Org-Id header to all proxied requests"

afc patch add api-gateway \
  --branch fix/connection-pool-leak \
  --upstream-pr https://github.com/acme-oss/api-gateway/pull/287 \
  --description "Fix connection pool exhaustion under load"

afc patch add api-gateway \
  --branch internal/custom-metrics \
  --description "Internal-only: export custom Prometheus metrics"
```

Patches are appended to the end of the list by default. To insert at a
specific position, use `--position`:

```
afc patch add api-gateway \
  --branch fix/urgent-security-patch \
  --position 1 \
  --description "Security fix that must be applied first"
```

### 4. Sync from upstream

Fetch the latest upstream state and detect any patches that have been merged:

```
afc workspace sync api-gateway
```

For carry-patch workspaces, the sync response includes additional fields:

```json
{
  "patches_merged": ["fix/connection-pool-leak"],
  "rebuild_triggered": true
}
```

If any patches are detected as merged, their status transitions to
`merged_upstream`. By default, a rebuild is automatically triggered after
sync (see the `AUTO_REBUILD_AFTER_SYNC` variable).

### 5. Trigger a rebuild manually

If auto-rebuild is disabled or you want to rebuild after modifying the patch
list:

```
afc rebuild submit api-gateway
```

The rebuild runs asynchronously. Check its status:

```
afc rebuild status api-gateway <rebuild-id>
```

A completed rebuild shows per-patch results:

```json
{
  "id": "d4e5f6a7-...",
  "status": "completed",
  "strategy": "rebase",
  "patch_results": [
    {
      "patch_id": "a1b2c3d4-...",
      "branch_name": "fix/urgent-security-patch",
      "position": 1,
      "status": "success",
      "new_head_sha": "abc123..."
    },
    {
      "patch_id": "e5f6a7b8-...",
      "branch_name": "feature/custom-auth-headers",
      "position": 2,
      "status": "success",
      "new_head_sha": "def456..."
    }
  ]
}
```

### 6. Check the status dashboard

Get a comprehensive view of the workspace and patch stack:

```
afc workspace patch-status api-gateway
```

This returns workspace metadata, the last rebuild summary, per-patch status,
and aggregate counts.

### 7. Handle a failed rebuild

If a rebuild fails due to conflicts, the failing patch is marked `conflict`
and the rebuild stops:

```json
{
  "status": "failed",
  "error": "conflict on patch fix/urgent-security-patch: unresolved files [pkg/auth.go]"
}
```

To resolve:

1. Check which patch has the conflict: `afc patch list api-gateway`
2. Clone the workspace repo and resolve the conflict manually in the
   workspace trunk (or via the hub's git server).
3. After resolving, the resolution is recorded by rerere for future rebuilds.
4. Set the patch status back to `active`:
   `afc patch update api-gateway <patch-id> --status active`
5. Resubmit the rebuild: `afc rebuild submit api-gateway`

---

## Day-to-day operations

### Adding a new patch

Add a patch at the end of the list:

```
afc patch add api-gateway \
  --branch feature/new-rate-limiter \
  --upstream-pr https://github.com/acme-oss/api-gateway/pull/315 \
  --description "Custom rate limiting by tenant ID"
```

Insert at a specific position (existing patches shift down):

```
afc patch add api-gateway \
  --branch fix/critical-hotfix \
  --position 1
```

After adding a patch, submit a rebuild to update the integration branch:

```
afc rebuild submit api-gateway
```

### Removing a patch

List patches to find the patch ID:

```
afc patch list api-gateway
```

Remove the patch by ID:

```
afc patch remove api-gateway a1b2c3d4-5678-90ab-cdef-1234567890ab
```

Remaining patches are automatically recompacted to maintain a contiguous
position sequence with no gaps.

### Reordering patches

Reordering requires providing the complete list of patch IDs in the desired
order:

```
afc patch reorder api-gateway \
  e5f6a7b8-... \
  a1b2c3d4-... \
  c9d0e1f2-...
```

The first ID gets position 1, the second gets position 2, and so on. All
patch IDs for the workspace must be included -- no missing, no extra, no
duplicates.

### Disabling a patch temporarily

To skip a patch during rebuilds without removing it:

```
afc patch update api-gateway <patch-id> --status disabled
```

Re-enable it later:

```
afc patch update api-gateway <patch-id> --status active
```

### Handling a merged patch

When a sync detects that a patch's commits have been incorporated into
upstream, the patch status transitions to `merged_upstream` automatically.
On the next successful rebuild, `merged_upstream` patches are permanently
deleted from the database and positions are compacted.

If you know a patch has been merged but sync has not detected it yet (for
example, the upstream PR was squash-merged and the commit SHAs differ), you
can manually mark it:

```
afc patch update api-gateway <patch-id> --status merged_upstream
```

### Resolving conflicts after a failed rebuild

1. Check which files have conflicts:

   ```
   afc workspace patch-status api-gateway
   ```

   The `conflict_files` field on the conflicting patch lists the affected
   files.

2. Access the workspace repository via the hub's built-in git server or
   directly on the host filesystem, resolve the conflict, and commit the
   resolution.

3. The resolution is recorded by rerere. On subsequent rebuilds, if the
   same conflict pattern appears, it is resolved automatically.

4. Reset the patch status to `active` and rebuild:

   ```
   afc patch update api-gateway <patch-id> --status active
   afc rebuild submit api-gateway
   ```

### Recovering from an upstream force-push

If the upstream repository force-pushes (rewriting history), a standard sync
returns a `409 Diverged` error. Use the `--reset-to-upstream` flag to
force-reset the local tracking ref:

```
afc workspace sync api-gateway --reset-to-upstream
```

This discards the local upstream tracking state and replaces it with the
new upstream HEAD. A rebuild should follow to reapply patches on top of the
new base.

### Managing rerere resolutions

List recorded conflict resolutions:

```
afc rerere list api-gateway
```

```json
{
  "resolutions": [
    {"path": "pkg/auth.go", "recorded_at": "2025-03-15T14:22:00Z"},
    {"path": "internal/config/defaults.go", "recorded_at": "2025-03-10T09:15:00Z"}
  ]
}
```

If a recorded resolution is outdated or incorrect and you want rerere to
re-prompt for manual resolution on the next conflict, forget it:

```
afc rerere forget api-gateway pkg/auth.go
```

### Viewing rebuild history

List all rebuild jobs for the workspace:

```
afc rebuild list api-gateway
```

Get details for a specific rebuild, including per-patch results:

```
afc rebuild status api-gateway <rebuild-id>
```

---

## Configuration

Two workspace variables control carry-patch behavior. Set them with the
`afc vars` commands:

### REBUILD_STRATEGY

Controls how patch branches are applied during a rebuild.

| Value | Behavior |
|-------|----------|
| `rebase` (default) | Cherry-picks individual commits from each patch branch onto the temp branch. Produces a clean, linear integration history. |
| `merge` | Merges each patch branch with `--no-ff`. Each patch appears as a merge commit in the integration history, preserving the branch's original commit structure. |

Set the strategy:

```
afc vars create REBUILD_STRATEGY=merge --workspace api-gateway
```

Change it later:

```
afc vars update REBUILD_STRATEGY=rebase --workspace api-gateway
```

The strategy is captured at the time a rebuild job is enqueued. Changing the
variable does not affect already-queued or running rebuilds.

### AUTO_REBUILD_AFTER_SYNC

Controls whether a rebuild is automatically triggered when a sync detects
that upstream has advanced.

| Value | Behavior |
|-------|----------|
| (unset or any value other than `"false"`) | Auto-rebuild is enabled (default). |
| `false` | Auto-rebuild is disabled. Only the exact string `"false"` disables it. |

Disable auto-rebuild:

```
afc vars create AUTO_REBUILD_AFTER_SYNC=false --workspace api-gateway
```

Re-enable it by deleting the variable or setting it to any other value:

```
afc vars delete AUTO_REBUILD_AFTER_SYNC --workspace api-gateway
```

---

## How it works

### Rebuild algorithm

When a rebuild job runs, the hub executes the following steps:

1. **Resolve credentials.** Validates that upstream authentication
   credentials are available. If not, the job is retried.

2. **Fetch upstream.** Runs `git fetch upstream` in the workspace repository
   to get the latest upstream refs.

3. **Create a temporary branch.** Creates `_rebuild_temp` at the current
   upstream HEAD SHA.

4. **Enable rerere.** Sets `rerere.enabled=true` and
   `rerere.autoupdate=true` in the repo's git config.

5. **Process each patch in position order:**
   - **Skipped patches:** `merged_upstream` and `disabled` patches are
     skipped. `merged_upstream` patches are collected for deletion after
     the rebuild completes.
   - **Missing branches:** If the branch does not exist in the repository,
     the patch is skipped (not an error).
   - **Rebase strategy:** Identifies commits unique to the patch branch
     (not already in upstream) and cherry-picks each one in order.
   - **Merge strategy:** Merges the patch branch with `--no-ff`.
   - **Conflict handling:** If a cherry-pick or merge produces conflicts,
     the hub runs `git rerere` to attempt automatic resolution. If rerere
     resolves all conflicts, the operation continues. If unresolved
     conflicts remain, the patch is marked `conflict` in the database and
     the rebuild stops immediately (fail-fast).

6. **Finalize on success.** Force-updates the integration branch ref to the
   final HEAD of the temporary branch, deletes the temporary branch,
   removes `merged_upstream` patches from the database, and compacts
   positions.

### Sync algorithm

The carry-patch sync differs from the standard workspace sync:

1. **Resolve upstream credentials** and fetch from the `upstream` remote.

2. **Detect upstream changes.** Compare the new upstream HEAD with the stored
   `upstream_head_sha`. If unchanged, return immediately.

3. **Detect merged patches.** For each `active` patch, check whether its
   branch HEAD is an ancestor of the new upstream HEAD using
   `git merge-base --is-ancestor`. If true, transition the patch to
   `merged_upstream`.

4. **Auto-rebuild.** If `AUTO_REBUILD_AFTER_SYNC` is not `"false"`, enqueue
   a rebuild job. If a rebuild job is already queued or running, the
   duplicate is silently ignored.

### Merge detection

Merge detection uses `git merge-base --is-ancestor` to check whether a
patch branch's HEAD commit is an ancestor of the upstream HEAD. This is a
heuristic: it checks whether the exact commits were incorporated, not
whether a specific PR was merged. Squash merges, where upstream rewrites
commits, are not detected by this method. In those cases, you need to
manually mark the patch as `merged_upstream`.

### Rerere integration

Rerere is configured during workspace clone and re-enabled at the start of
each rebuild. The flow during a conflict:

1. A cherry-pick or merge produces conflict markers.
2. `git rerere` checks the `.git/rr-cache/` directory for a matching
   preimage (conflict pattern).
3. If a match is found and `rerere.autoupdate` is enabled, the resolution
   is applied and the conflicted files are staged automatically.
4. The hub checks for remaining unresolved conflicts. If none, the operation
   continues. If some remain, the rebuild fails on that patch.

Over time, as you resolve conflicts manually, rerere accumulates resolutions.
Repeated rebuilds against a moving upstream become increasingly hands-free.

---

## Limitations

**Immutable workspace fields.** The `workspace_mode`, `upstream_url`, and
`integration_branch` fields are set at workspace creation time and cannot be
changed afterward. To switch from `standard` to `carry_patch` (or vice
versa), you must create a new workspace.

**Fail-fast rebuild.** The rebuild stops at the first patch that produces an
unresolved conflict. All subsequent patches are not attempted. Resolve the
conflict, reset the patch status to `active`, and resubmit.

**Ancestry-based merge detection.** Merge detection uses `git merge-base
--is-ancestor`, which works for regular merges and fast-forwards but does not
detect squash merges where upstream rewrites commit history. Patches merged
via squash must be manually marked as `merged_upstream`.

**Merged patches are permanently deleted.** After a successful rebuild, all
`merged_upstream` patches are permanently removed from the database. There
is no undo. If a patch was incorrectly marked, it must be re-added.

**Integration branch created at fork HEAD.** During workspace creation, the
integration branch is created at the fork's HEAD because upstream refs are
not available until the first fetch. The branch is not correctly positioned
until after the first sync and rebuild.

**No per-rebuild strategy override.** The rebuild strategy is always read from
the `REBUILD_STRATEGY` workspace variable. There is no flag to override the
strategy for a single rebuild.

**Rerere resolution count is global.** The `rerere_resolution_count` in the
patch-status dashboard counts all recorded rerere resolutions for the
workspace, not per-patch. The count is the same for every patch in the list.

**Post-clone setup failures are non-fatal.** If adding the `upstream`
remote, enabling rerere, or creating the integration branch fails during
workspace clone, warnings are logged but the workspace still transitions
to `ready` status. Check workspace logs if sync or rebuild operations fail
immediately after creation.

**Concurrent rebuild prevention.** Only one rebuild job can be queued or
running for a workspace at a time. Submitting a rebuild while one is already
in progress returns HTTP 409. Wait for the current rebuild to complete before
resubmitting.
