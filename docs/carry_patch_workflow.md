# Carry-Patch Workflow

This guide explains the carry-patch workflow in af-hub: a workspace mode for
maintaining a fork of an upstream repository with an ordered set of patch
branches applied on top. The hub mechanically rebuilds an integration branch
that combines the upstream base with all carried patches, detects when patches
are merged upstream (including squash merges), and manages conflict resolution
across rebuilds.

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
the rest: fetching from upstream, detecting merged patches (including squash
merges), rebuilding the integration branch, remembering conflict resolutions
via git rerere, and rolling back if something goes wrong.

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
| `upstream_pr_url` | Optional link to the corresponding upstream pull request (also used for squash merge detection) |
| `description` | Optional free-form description |
| `deleted_at` | Timestamp when a merged patch was soft-deleted (null for active patches) |

### Patch statuses

| Status | Meaning |
|--------|---------|
| `active` | Normal state. The patch is applied during rebuilds. |
| `merged_upstream` | The hub detected that this patch's commits have been incorporated into upstream (via ancestry check, content comparison, or PR-number scanning). The patch is skipped during rebuilds and soft-deleted after a successful rebuild completes. |
| `conflict` | The most recent rebuild encountered unresolved conflicts on this patch. The patch remains in the list and will be retried on the next rebuild. |
| `disabled` | Manually disabled by an operator. Skipped during rebuilds but not deleted. |
| `deleted` | Soft-deleted after being merged upstream. Hidden from normal list views but can be restored within the retention period (7 days). |

### Rebuild strategies

The rebuild strategy determines how each patch branch is applied on top of
upstream HEAD:

| Strategy | Behavior |
|----------|----------|
| `rebase` (default) | Cherry-picks individual commits from each patch branch onto the integration branch. Produces a linear history. |
| `merge` | Merges each patch branch with `--no-ff`. Preserves branch topology in the integration history. |

The strategy is controlled by the `REBUILD_STRATEGY` workspace variable and
can be overridden on a per-rebuild basis via the `--strategy` CLI flag or the
`strategy` field in the rebuild request body.

### Fail modes

The fail mode determines how the rebuild handles patches that produce
unresolved conflicts:

| Mode | Behavior |
|------|----------|
| `fail_fast` (default) | Stops the rebuild immediately on the first unresolved conflict. The failing patch is marked `conflict` and no subsequent patches are attempted. |
| `continue` | Records the conflict on the failing patch, resets the temporary branch to the pre-patch state, and continues processing the remaining patches. The final integration branch includes all patches that applied cleanly. |

The fail mode is controlled by the `REBUILD_FAIL_MODE` workspace variable and
can be overridden on a per-rebuild basis via the `--fail-mode` CLI flag or the
`fail_mode` field in the rebuild request body.

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

Register your patch branches in the order they should be applied. Branches
are validated against the workspace repository at add time (unless
`--skip-branch-check` is used).

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

For idempotent automation, use `--if-not-exists` to return the existing patch
record (HTTP 200) instead of an error when the branch is already registered:

```
afc patch add api-gateway \
  --branch feature/custom-auth-headers \
  --if-not-exists
```

To skip git branch existence validation (for example, if the branch will be
pushed later):

```
afc patch add api-gateway \
  --branch feature/future-work \
  --skip-branch-check
```

Multiple patches can be added in a single API call by sending a JSON array
body. The batch is inserted atomically -- if any patch fails validation, the
entire batch is rolled back. See the API reference for the batch format.

### 4. Sync from upstream

Fetch the latest upstream state and detect any patches that have been merged:

```
afc workspace sync api-gateway
```

For carry-patch workspaces, the sync response includes additional fields:

```json
{
  "patches_merged": ["fix/connection-pool-leak"],
  "rebuild_triggered": true,
  "force_push_detected": false
}
```

If any patches are detected as merged, their status transitions to
`merged_upstream`. By default, a rebuild is automatically triggered after
sync (see the `AUTO_REBUILD_AFTER_SYNC` variable).

The `force_push_detected` field indicates whether the upstream repository
has rewritten history since the last sync. This is determined by checking
whether the previously stored upstream HEAD is an ancestor of the new
upstream HEAD. A force-push detection is informational -- the sync still
proceeds normally.

### 5. Preview a rebuild

Before running a rebuild, you can preview which patches would conflict
without modifying any git state:

```
afc rebuild preview api-gateway
```

The preview uses `git merge-tree --write-tree` to perform a read-only
conflict prediction for each active patch in position order:

```json
{
  "patch_results": [
    {
      "patch_id": "a1b2c3d4-...",
      "branch_name": "fix/urgent-security-patch",
      "position": 1,
      "status": "would_succeed",
      "tree_sha": "abc123..."
    },
    {
      "patch_id": "e5f6a7b8-...",
      "branch_name": "feature/custom-auth-headers",
      "position": 2,
      "status": "would_conflict",
      "conflict_files": ["pkg/auth.go", "pkg/headers.go"]
    }
  ]
}
```

Each patch reports `would_succeed` or `would_conflict`. The preview is
cumulative: each patch is tested against the result of all previously
successful patches, so cascading conflicts are detected accurately.

No refs, branches, or patch statuses are modified by the preview.

### 6. Trigger a rebuild manually

If auto-rebuild is disabled or you want to rebuild after modifying the patch
list:

```
afc rebuild submit api-gateway
```

Override the strategy or fail mode for this specific rebuild:

```
afc rebuild submit api-gateway --strategy merge --fail-mode continue
```

The rebuild runs asynchronously. Check its status:

```
afc rebuild status api-gateway <rebuild-id>
```

Or submit and wait for completion:

```
afc rebuild submit api-gateway --wait
```

A completed rebuild shows per-patch results:

```json
{
  "id": "d4e5f6a7-...",
  "status": "completed",
  "strategy": "rebase",
  "integration_head_sha": "fff999...",
  "previous_integration_head_sha": "aaa111...",
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

While a rebuild is running, you can poll its status to see per-patch progress
in real time. The `patch_results` field is updated after each patch is
processed, so you can observe which patches have been applied so far before
the job completes.

### 7. Check the status dashboard

Get a comprehensive view of the workspace and patch stack:

```
afc workspace patch-status api-gateway
```

This returns workspace metadata, the last rebuild summary, per-patch status
with last rebuild results, and aggregate counts including total rerere
resolutions.

### 8. Handle a failed rebuild

If a rebuild fails due to conflicts (in `fail_fast` mode), the failing patch
is marked `conflict` and the rebuild stops:

```json
{
  "status": "failed",
  "error": "conflict in patch \"fix/urgent-security-patch\": pkg/auth.go"
}
```

If using `continue` mode, the rebuild completes with a mix of outcomes:

```json
{
  "patch_results": [
    {"branch_name": "fix/urgent-security-patch", "status": "conflict", "conflict_files": ["pkg/auth.go"]},
    {"branch_name": "feature/custom-auth-headers", "status": "success", "new_head_sha": "def456..."},
    {"branch_name": "internal/custom-metrics", "status": "success", "new_head_sha": "ghi789..."}
  ]
}
```

In `continue` mode, the integration branch is updated with all patches that
applied cleanly. Conflicting patches are skipped and marked `conflict`.

To resolve:

1. Check which patch has the conflict: `afc patch list api-gateway`
2. Clone the workspace repo and resolve the conflict manually in the
   workspace trunk (or via the hub's git server).
3. After resolving, the resolution is recorded by rerere for future rebuilds.
4. Set the patch status back to `active`:
   `afc patch update api-gateway <patch-id> --status active`
5. Resubmit the rebuild: `afc rebuild submit api-gateway`

### 9. Roll back a rebuild

If a rebuild produces unexpected results, you can roll back the integration
branch to its previous state:

```
afc rebuild rollback api-gateway <rebuild-id>
```

This resets the integration branch ref to the `previous_integration_head_sha`
recorded in the rebuild result. Rollback is not available for the first-ever
rebuild (there is no previous state).

```json
{
  "rolled_back_to": "aaa111..."
}
```

After rolling back, you can modify the patch list or resolve conflicts and
then submit a new rebuild.

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

### Restoring a soft-deleted patch

When patches are merged upstream and a rebuild completes, they are
soft-deleted rather than permanently removed. During the retention period
(7 days), you can restore a soft-deleted patch:

```
afc patch restore api-gateway <patch-id>
```

The patch is returned to `active` status and placed at the end of the patch
list. After the 7-day retention period, soft-deleted patches are permanently
purged and can no longer be restored.

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
On the next successful rebuild, `merged_upstream` patches are soft-deleted
(status set to `deleted`, `deleted_at` timestamp recorded). They remain in
the database for 7 days, during which they can be restored. After 7 days,
they are permanently purged.

If you know a patch has been merged but sync has not detected it yet, you
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

For **carry-patch workspaces**, the sync handler detects force-pushes
automatically. The sync proceeds normally and reports the detection via the
`force_push_detected` field in the response. A rebuild should follow to
reapply patches on top of the new upstream base. The `--reset-to-upstream`
flag has no effect for carry-patch workspaces because the carry-patch sync
handler intercepts the request before the flag is processed.

For **standard workspaces**, a force-push causes the sync to return a
`409 Diverged` error. Use the `--reset-to-upstream` flag to force-reset
the local tracking ref:

```
afc workspace sync <slug> --reset-to-upstream
```

This discards the local upstream tracking state and replaces it with the
new upstream HEAD.

### Cancelling a queued rebuild

If a rebuild is queued but has not yet started, you can cancel it:

```
afc rebuild cancel api-gateway <rebuild-id>
```

Only queued jobs can be cancelled. Running or completed jobs return an error.

### Requeuing a dead-lettered rebuild

If a rebuild has exhausted its retry attempts and moved to `dead_letter`
status, you can requeue it:

```
afc rebuild requeue api-gateway <rebuild-id>
```

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

Workspace variables control carry-patch behavior. Set them with the
`afc vars` commands.

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
variable does not affect already-queued or running rebuilds. You can also
override the strategy for a single rebuild using the `--strategy` flag on the
`rebuild submit` command or the `strategy` field in the API request body.

### REBUILD_FAIL_MODE

Controls how the rebuild handles patches that produce unresolved conflicts.

| Value | Behavior |
|-------|----------|
| `fail_fast` (default) | Stops the rebuild at the first unresolved conflict. |
| `continue` | Records the conflict, resets to the pre-patch state, and continues with the next patch. |

Set the fail mode:

```
afc vars create REBUILD_FAIL_MODE=continue --workspace api-gateway
```

Like `REBUILD_STRATEGY`, the fail mode is captured at enqueue time and can be
overridden per-rebuild using the `--fail-mode` flag or the `fail_mode` field
in the API request body.

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

### AUTO_REBUILD_AFTER_PUSH

Controls whether a rebuild is automatically triggered when a push to a
registered patch branch is received by the hub's git server.

| Value | Behavior |
|-------|----------|
| (unset or any value other than `"false"`) | Auto-rebuild on push is enabled (default). |
| `false` | Auto-rebuild on push is disabled. Only the exact string `"false"` disables it. |

When enabled, the hub checks whether any of the pushed branches match a
registered patch in a carry-patch workspace. If so, a rebuild is enqueued
with duplicate suppression (if a rebuild is already queued or running, the
push does not create a second one).

The push hook runs asynchronously and does not affect the push response.

### SQUASH_MERGE_DETECTION

Controls the merge detection strategy used during sync. By default, the hub
uses multiple signals to detect both regular merges and squash merges.

| Value | Behavior |
|-------|----------|
| `both` (default) | Uses ancestry check, content-based comparison (`git cherry`), and PR-number scanning. |
| `ancestry_only` | Only uses `git merge-base --is-ancestor`. Squash merges are not detected. |
| `content_based` | Only uses content-based comparison and PR-number scanning. Skips ancestry check. |

Content-based detection works by running `git cherry` to compare patch
commits against upstream. If all commits on the patch branch have
content-equivalent matches in upstream (zero pending commits), the patch is
considered merged.

PR-number scanning looks for GitHub's squash-merge commit message format
`Title (#NNN)` in recent upstream commits when the patch has an
`upstream_pr_url` set. This detects squash merges where the commit content
differs from the original patch commits.

---

## How it works

### Rebuild algorithm

When a rebuild job runs, the hub executes the following steps:

1. **Resolve credentials.** Validates that upstream authentication
   credentials are available. If not, the job is retried.

2. **Fetch upstream.** Runs `git fetch upstream` in the workspace repository
   to get the latest upstream refs.

3. **Create a temporary branch.** Creates `_rebuild_temp` at the current
   upstream HEAD SHA (resolved from `FETCH_HEAD`).

4. **Enable rerere.** Sets `rerere.enabled=true` and
   `rerere.autoupdate=true` in the repo's git config.

5. **Process each patch in position order:**
   - **Skipped patches:** `merged_upstream`, `disabled`, and `deleted`
     patches are skipped. `merged_upstream` patches are collected for
     soft-deletion after the rebuild completes.
   - **Missing branches:** If the branch does not exist in the repository,
     the patch is skipped (not an error).
   - **Rebase strategy:** Identifies commits unique to the patch branch
     (not already in upstream) and cherry-picks each one in order.
   - **Merge strategy:** Merges the patch branch with `--no-ff`.
   - **Conflict handling:** If a cherry-pick or merge produces conflicts,
     the hub runs `git rerere` to attempt automatic resolution. If rerere
     resolves all conflicts, the operation continues. If unresolved
     conflicts remain:
     - In `fail_fast` mode (default): the patch is marked `conflict` and
       the rebuild stops immediately.
     - In `continue` mode: the patch is marked `conflict`, the temporary
       branch is reset to its pre-patch state, and processing continues
       with the next patch.
   - **Progress tracking:** After each patch is processed, the per-patch
     results are written to the job's progress field. Clients polling
     the rebuild status can observe which patches have been processed
     while the job is still running.

6. **Finalize on success.** Captures the previous integration branch HEAD
   SHA (for rollback), force-updates the integration branch ref to the
   final HEAD of the temporary branch, deletes the temporary branch,
   soft-deletes `merged_upstream` patches, and compacts positions.

### Sync algorithm

The carry-patch sync differs from the standard workspace sync:

1. **Resolve upstream credentials** and fetch from the `upstream` remote.

2. **Detect upstream changes.** Compare the new upstream HEAD (from
   `FETCH_HEAD`) with the stored `upstream_head_sha`. If unchanged, return
   immediately.

3. **Detect force-push.** If the stored upstream HEAD is not an ancestor of
   the new upstream HEAD, set `force_push_detected` to true. This is
   informational and does not block the sync.

4. **Detect merged patches.** For each `active` patch, apply the configured
   detection strategy (see `SQUASH_MERGE_DETECTION`):
   - **Ancestry check:** `git merge-base --is-ancestor` to test whether the
     patch branch HEAD is an ancestor of the new upstream HEAD.
   - **Content-based check:** `git cherry` to compare patch commits against
     upstream. If all patch commits have content-equivalent matches upstream
     (zero pending commits), the patch is considered merged.
   - **PR-number scanning:** If the patch has an `upstream_pr_url`, extract
     the PR number and scan recent upstream commit messages for the
     `(#NNN)` pattern used by GitHub's squash-merge.
   - If any signal detects the patch as merged, transition it to
     `merged_upstream`.

5. **Auto-rebuild.** If `AUTO_REBUILD_AFTER_SYNC` is not `"false"`, enqueue
   a rebuild job. If a rebuild job is already queued or running, the
   duplicate is silently ignored.

### Merge detection

Merge detection uses multiple signals to detect both regular merges and
squash merges:

- **Ancestry check** (`git merge-base --is-ancestor`): Detects regular
  merges and fast-forwards where the exact commits were incorporated into
  upstream.

- **Content-based comparison** (`git cherry`): Detects squash merges and
  rebases by comparing patch diffs against upstream diffs. If every commit
  on the patch branch has a content-equivalent match in upstream, the patch
  is considered merged even though the commit SHAs differ.

- **PR-number scanning**: When a patch has an `upstream_pr_url`, the hub
  extracts the PR number and scans recent upstream commit messages for
  GitHub's squash-merge format `Title (#NNN)`. This catches squash merges
  that produce different diffs (for example, when upstream makes additional
  changes before merging).

All three signals are used by default. The `SQUASH_MERGE_DETECTION` workspace
variable can restrict detection to a subset of these methods.

### Rebuild preview

The rebuild preview endpoint (`GET /workspaces/:slug/rebuild-preview`)
performs a read-only conflict prediction using `git merge-tree --write-tree`.
It processes patches in position order, testing each one against the
cumulative result of all previously successful patches. No refs, branches,
or patch statuses are modified.

The preview is useful for:
- Checking whether a newly added patch will conflict before running a full
  rebuild.
- Detecting cascading conflicts across the patch stack.
- Integrating into CI pipelines to gate deployments on conflict-free state.

### Rebuild rollback

Every successful rebuild records the `previous_integration_head_sha` -- the
integration branch HEAD before the rebuild updated it. The rollback endpoint
(`POST /workspaces/:slug/rebuilds/:id/rollback`) resets the integration
branch ref to this previous value.

Rollback is not available for the first-ever rebuild (there is no previous
state to roll back to). After rolling back, you should modify the patch
list or resolve conflicts and submit a new rebuild.

### Auto-rebuild on push

When a push to the hub's git server updates a branch that is registered as
a patch in a carry-patch workspace, the hub automatically enqueues a rebuild
job (unless `AUTO_REBUILD_AFTER_PUSH` is set to `"false"`).

The push hook:
1. Checks whether the workspace is in `carry_patch` mode.
2. Checks whether any pushed branch matches a registered patch.
3. Checks the `AUTO_REBUILD_AFTER_PUSH` variable.
4. Enqueues a rebuild with duplicate suppression. If a rebuild is already
   queued or running, no additional job is created.

The hook runs asynchronously after the push completes. Errors in the hook
are logged but do not affect the push response.

### Rerere integration

Rerere is configured during workspace clone and re-enabled at the start of
each rebuild. The flow during a conflict:

1. A cherry-pick or merge produces conflict markers.
2. `git rerere` checks the `.git/rr-cache/` directory for a matching
   preimage (conflict pattern).
3. If a match is found and `rerere.autoupdate` is enabled, the resolution
   is applied and the conflicted files are staged automatically.
4. The hub checks for remaining unresolved conflicts. If none, the operation
   continues. If some remain, the behavior depends on the fail mode.

Over time, as you resolve conflicts manually, rerere accumulates resolutions.
Repeated rebuilds against a moving upstream become increasingly hands-free.

### Soft-delete lifecycle

When a successful rebuild completes, patches with `merged_upstream` status
are soft-deleted rather than permanently removed:

1. The patch status is set to `deleted` and `deleted_at` is recorded.
2. Soft-deleted patches are excluded from normal list views and from the
   patch-status dashboard.
3. Soft-deleted patches can be restored to `active` status via the restore
   endpoint within the retention period.
4. After 7 days, a background purge process permanently removes expired
   soft-deleted patches from the database.

This provides a safety net: if a patch was incorrectly detected as merged
upstream, it can be recovered without needing to re-create it from scratch.

---

## API reference summary

### Patch endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/workspaces/:slug/patches` | Add one or more patches (single object or JSON array) |
| `GET` | `/workspaces/:slug/patches` | List non-deleted patches in position order |
| `PATCH` | `/workspaces/:slug/patches/:id` | Update patch fields (status, position, description, upstream_pr_url) |
| `DELETE` | `/workspaces/:slug/patches/:id` | Permanently remove a patch |
| `POST` | `/workspaces/:slug/patches/:id/restore` | Restore a soft-deleted patch |
| `POST` | `/workspaces/:slug/patches/reorder` | Reorder all patches |

### Rebuild endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/workspaces/:slug/rebuild` | Submit a rebuild job (accepts optional `strategy` and `fail_mode` in body) |
| `GET` | `/workspaces/:slug/rebuilds` | List rebuild jobs |
| `GET` | `/workspaces/:slug/rebuilds/:id` | Get rebuild job details (includes progress for running jobs) |
| `DELETE` | `/workspaces/:slug/rebuilds/:id` | Cancel a queued rebuild job |
| `POST` | `/workspaces/:slug/rebuilds/:id/requeue` | Requeue a dead-lettered rebuild job |
| `POST` | `/workspaces/:slug/rebuilds/:id/rollback` | Roll back integration branch to pre-rebuild state |
| `GET` | `/workspaces/:slug/rebuild-preview` | Preview rebuild conflicts (read-only) |

### Other carry-patch endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/workspaces/:slug/sync` | Sync from upstream with merge detection and auto-rebuild |
| `GET` | `/workspaces/:slug/patch-status` | Patch-status dashboard with summary counts |
| `GET` | `/workspaces/:slug/rerere` | List recorded rerere resolutions |
| `DELETE` | `/workspaces/:slug/rerere/*pathspec` | Forget a recorded rerere resolution |

### Permission scopes

| Scope | Used by |
|-------|---------|
| `rebuilds:read` | List rebuilds, get rebuild status, rebuild preview |
| `rebuilds:write` | Submit rebuild, cancel rebuild, requeue rebuild, rollback rebuild |
| `patches:read` | List patches |
| `patches:write` | Add, update, remove, restore, reorder patches |
| `workspaces:read` | Patch-status dashboard, rerere list |
| `workspaces:write` | Rerere forget |
| `workspaces:sync` | Sync from upstream |

---

## Limitations

**Immutable workspace fields.** The `workspace_mode`, `upstream_url`, and
`integration_branch` fields are set at workspace creation time and cannot be
changed afterward. To switch from `standard` to `carry_patch` (or vice
versa), you must create a new workspace.

**Integration branch created at fork HEAD.** During workspace creation, the
integration branch is created at the fork's HEAD because upstream refs are
not available until the first fetch. The branch is not correctly positioned
until after the first sync and rebuild.

**Rerere resolution count is workspace-level.** The `total_rerere_resolutions`
field in the patch-status dashboard summary counts all recorded rerere
resolutions for the workspace. It is not broken down per-patch.

**Post-clone setup failures are non-fatal.** If adding the `upstream`
remote, enabling rerere, or creating the integration branch fails during
workspace clone, warnings are logged but the workspace still transitions
to `ready` status. Check workspace logs if sync or rebuild operations fail
immediately after creation.

**Concurrent rebuild prevention.** Only one rebuild job can be queued or
running for a workspace at a time. Submitting a rebuild while one is already
in progress returns HTTP 409. Wait for the current rebuild to complete before
resubmitting.

**First rebuild cannot be rolled back.** The rollback mechanism requires a
previous integration branch HEAD SHA, which is only available after at least
one prior rebuild has completed.
