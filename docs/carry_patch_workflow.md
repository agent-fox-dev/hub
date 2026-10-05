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
| `origin` | Your fork (the `git_url` from workspace creation) | Fork; push target for `REBUILD_PUSH_INTEGRATION_BRANCH` and, in `origin` mode, the authority for patch branches |
| `upstream` | The upstream project (the `upstream_url` from workspace creation) | Source of truth for the base; fetched during sync and rebuild |

In `hub` mode (the default), patch branches are written by pushes to the
hub's own git server, not to `origin`. See
[Where patch branches live](#where-patch-branches-live) below for details.

### Where patch branches live

A rebuild reads each patch branch only from `refs/heads/<branch>` in the
workspace trunk (the hub's bare clone). The initial clone brings the fork's
branches in only as `refs/remotes/origin/*`, so they are not visible to the
rebuild until something copies them to `refs/heads/`.

The workspace variable `PATCH_BRANCH_SOURCE` selects who writes those
`refs/heads/` refs:

- **`hub`** (default) — patch branches are written by pushes to the hub's
  git server. Sync never fetches the fork.
- **`origin`** — sync fetches the fork and brings every registered patch
  branch to the fork's tip, according to `PATCH_DIVERGENCE_POLICY`.

#### Comparison of authority models

| Aspect | `hub` mode | `origin` mode |
|--------|-----------|---------------|
| Who writes the branch | A push to the hub's git server | Sync copies the fork's tip to `refs/heads/<branch>` |
| What sync does with it | Nothing (fork is not fetched) | Fetches the fork, updates each registered branch to the fork's tip |
| What registration does | Checks `refs/heads/<branch>` then `refs/remotes/origin/<branch>` | Same checks, then a single-branch fork fetch if needed |
| What a push to the hub does | Accepted; optionally mirrored to the fork when `PUSH_PATCHES_TO_ORIGIN=true` | Rejected by default; forwarded to the fork when `PUSH_PATCHES_TO_ORIGIN=true` |
| What happens on divergence | N/A (hub is the sole writer) | Controlled by `PATCH_DIVERGENCE_POLICY`: `replace` (default, old tip saved under `refs/hub/replaced/<branch>`) or `report` |
| Where a person fixes a conflict | In the trunk or by pushing a fixed branch to the hub's git server | In a local clone of the fork; push to the fork, then `afc workspace sync` |
| Which copy survives a lost hub data directory | Lost (re-push from a local clone) | The fork's copy; the next sync restores it |

**Recommendation.** Teams that open upstream pull requests and review on the
fork should use `origin` with `REBUILD_PUSH_INTEGRATION_BRANCH=true`, so
people push only to GitHub and the hub is a rebuild engine. `hub` mode fits
workspaces driven by agents that push to the hub and never open upstream
pull requests.

The integration branch is always built by the hub and only pushed to the
fork (when `REBUILD_PUSH_INTEGRATION_BRANCH=true`). Sync never fetches the
integration branch from `origin`; it is never fetched from `origin` by sync.

**Switching modes.** The mode is read at the start of each operation (sync,
registration, push), so changing `PATCH_BRANCH_SOURCE` takes effect on the
next operation without a restart.

- Switching to `origin` makes the next sync bring registered branches to the
  fork's tips. If the hub's copy has diverged, the previous tip is kept
  under `refs/hub/replaced/<branch>` (when the policy is `replace`).
- Switching to `hub` stops fork fetches and clears persisted origin sync
  state at the next sync.

See the [Configuration](#configuration) section for full details on
`PATCH_BRANCH_SOURCE`, `PATCH_DIVERGENCE_POLICY` and
`PUSH_PATCHES_TO_ORIGIN`.

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
| `origin_sync_state` | Sync state relative to the fork (`in_sync`, `diverged` or `missing_on_origin`); absent when null, written only in origin mode |
| `origin_sha` | The fork's tip SHA recorded at the last sync; absent when null, written only in origin mode |
| `origin_synced_at` | Timestamp of the last sync that updated this patch's origin state; absent when null, written only in origin mode |

### Patch statuses

| Status | Meaning |
|--------|---------|
| `active` | Normal state. The patch is applied during rebuilds. |
| `merged_upstream` | The hub detected that this patch's commits have been incorporated into upstream (via ancestry check, content comparison, or PR-number scanning). The patch is skipped during rebuilds and soft-deleted after a successful rebuild completes. |
| `conflict` | The most recent rebuild encountered unresolved conflicts on this patch. The patch remains in the list and will be retried on the next rebuild. |
| `disabled` | Manually disabled by an operator. Skipped during rebuilds but not deleted. |
| `deleted` | Soft-deleted after being merged upstream. Hidden from normal list views but can be restored within the retention period (7 days). |

#### Origin sync states

In `origin` mode, each registered patch carries an `origin_sync_state` that
records how the hub's copy relates to the fork's copy:

- **`in_sync`** — the hub's `refs/heads/<branch>` matches the fork's tip.
- **`diverged`** — the hub's copy and the fork's copy have diverged (the
  hub's tip is not an ancestor of the fork's tip). What happens next depends
  on `PATCH_DIVERGENCE_POLICY`: `replace` overwrites the hub's copy (saving
  the old tip under `refs/hub/replaced/<branch>`), while `report` leaves the
  hub's copy unchanged and reports the divergence.
- **`missing_on_origin`** — the branch does not exist on the fork. This
  state leaves the patch status unchanged: the rebuild keeps applying the
  hub's copy until an operator disables or removes the patch.

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
with three patch branches. The primary path uses the fork-authoritative
model (`PATCH_BRANCH_SOURCE=origin`), recommended for teams that open
upstream pull requests. An
[alternative hub-authoritative walkthrough](#alternative-hub-authoritative-workspace)
follows for agent-driven workspaces.

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
printf '%s' "$UPSTREAM_TOKEN" | \
  afc secrets create UPSTREAM_GIT_PAT --from-stdin --workspace api-gateway
```

Alternatively, use username/password authentication:

```
afc secrets create UPSTREAM_GIT_USERNAME=your-bot-user --workspace api-gateway
printf '%s' "$UPSTREAM_TOKEN" | \
  afc secrets create UPSTREAM_GIT_PASSWORD --from-stdin --workspace api-gateway
```

`UPSTREAM_GIT_PAT`, `UPSTREAM_GIT_USERNAME` and `UPSTREAM_GIT_PASSWORD` are
*reserved* secret names: the hub reads them to authenticate against the
upstream remote (see [Reserved secret names](cli.md#reserved-secret-names)).
Use `afc secrets list|update|delete --workspace api-gateway` to manage them.

The hub resolves upstream credentials in this priority order:

1. `UPSTREAM_GIT_PAT` workspace secret
2. `UPSTREAM_GIT_USERNAME` + `UPSTREAM_GIT_PASSWORD` workspace secrets
3. Falls back to the origin credentials (`GIT_PAT` or `GIT_USERNAME` +
   `GIT_PASSWORD`) if no upstream-specific credentials are set

For public upstream repositories, no credential setup is needed.

### 3. Choose the authority

Tell the hub that the fork is the authority for patch branches:

```
afc vars create PATCH_BRANCH_SOURCE=origin --workspace api-gateway
```

Optionally, push the rebuilt integration branch back to the fork so CI and
codespaces see it:

```
afc vars create REBUILD_PUSH_INTEGRATION_BRANCH=true --workspace api-gateway
```

A fork clone and a fork token with read access are needed on every sync,
because the sync fetches `origin` with the workspace's `GIT_PAT` or
`GIT_USERNAME`/`GIT_PASSWORD` credentials. Make sure these are set when
creating the workspace (the `--git-pat` flag above) or via
`afc secrets create`.

See [Where patch branches live](#where-patch-branches-live) for a full
comparison of the two authority models.

### 4. Create and push a patch branch

In a normal clone of the fork, create a branch, commit your changes, and
push it to the fork (GitHub):

```
git clone https://github.com/your-org/api-gateway-fork.git
cd api-gateway-fork
git checkout -b feature/custom-auth-headers
# ... make changes ...
git commit -am "Add X-Org-Id header to all proxied requests"
git push origin feature/custom-auth-headers
```

Open the upstream PR from the same branch so the patch is tracked against
the upstream project:

```
gh pr create \
  --repo acme-oss/api-gateway \
  --head your-org:feature/custom-auth-headers \
  --title "Add X-Org-Id header to all proxied requests"
```

### 5. Register your patches

Register your patch branches in the order they should be applied. At add
time the hub resolves each branch in this order:

1. `refs/heads/<name>` in the workspace trunk (local head).
2. `refs/remotes/origin/<name>` (origin tracking ref).
3. In `origin` mode only, a single-branch fork fetch for the branch.

If none of these finds the branch, the request fails with
`400 branch does not exist in repository or on origin`. A branch that
exists only on the fork no longer needs `--skip-branch-check`;
`--skip-branch-check` is for branches that exist nowhere yet (for example,
a branch that will be pushed later).

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

### 6. Sync

Bring the fork's patch branches and the latest upstream state into the hub:

```
afc workspace sync api-gateway
```

In `origin` mode, sync -- not `afc rebuild submit` -- is what brings new
fork commits into the hub. A rebuild reads the hub's copy of each branch
(`refs/heads/<branch>` in the trunk) and does not fetch the fork. Only sync
fetches the fork and updates those refs to the fork's tips.

For carry-patch workspaces, the sync response is the usual workspace JSON
with extra fields:

```json
{
  "slug": "api-gateway",
  "workspace_mode": "carry_patch",
  "sync_status": "idle",
  "upstream_head_sha": "abc123def456...",
  "last_sync_at": "2024-06-15T10:30:00Z",

  "origin_fetched": true,
  "patches_merged": ["fix/connection-pool-leak"],
  "patches_synced": ["feature/custom-auth-headers", "internal/custom-metrics"],
  "patches_diverged": [],
  "rebuild_triggered": true,
  "force_push_detected": false
}
```

(Abbreviated -- every standard workspace field is present too.)

`origin_fetched` is always present in the sync response (both modes).
`patches_synced` and `patches_diverged` appear only in `origin` mode.

The patch refresh produces one of three outcomes per branch:

- **`created`** -- the branch did not exist on the hub and was created from
  the fork's tip.
- **`fast_forwarded`** -- the hub's copy was an ancestor of the fork's tip
  and was advanced.
- **`replaced`** -- the hub's copy diverged from the fork's tip and was
  replaced (the old tip is saved under `refs/hub/replaced/<branch>`).

A moved `active` or `conflict` patch triggers the same auto-rebuild as an
upstream advance (unless `AUTO_REBUILD_AFTER_SYNC=false`).

If any patches are detected as merged, their status transitions to
`merged_upstream`. By default, a rebuild is automatically triggered after
sync (see the `AUTO_REBUILD_AFTER_SYNC` variable).

The `force_push_detected` field indicates whether the upstream repository
has rewritten history since the last sync. This is informational -- the
sync still proceeds normally.

**Divergence handling.** When `PATCH_DIVERGENCE_POLICY` is `report` (instead
of the default `replace`), diverged branches are left unchanged and listed
in `patches_diverged`. Use `--fail-on-diverged` to make the CLI exit with
exit code 3 when `patches_diverged` is non-empty:

```
afc workspace sync api-gateway --fail-on-diverged
```

The sync response is still printed to stdout as a single JSON document, and
a message naming the diverged branches goes to stderr, so a CI job can both
fail on the exit code and read `patches_diverged` from stdout.

### 7. Preview a rebuild

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

### 8. Trigger a rebuild manually

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

### 9. Check the status dashboard

Get a comprehensive view of the workspace and patch stack:

```
afc workspace patch-status api-gateway
```

This returns workspace metadata, the last rebuild summary, per-patch status
with last rebuild results, and aggregate counts including total rerere
resolutions. In `origin` mode, each patch entry includes `origin_sync_state`
(`in_sync`, `diverged` or `missing_on_origin`), and the summary includes
`patches_diverged` and `patches_missing_on_origin` counts.

### 10. Handle a failed rebuild

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

Start by reading the `conflict_files` from the patch-status dashboard:

```
afc workspace patch-status api-gateway
```

The `conflict_files` field on the conflicting patch lists the affected files.

**Resolving in hub mode.** Resolve the conflict in the workspace trunk or by
pushing a fixed branch to the hub's git server. Rerere records the resolution
when it happens during a rebuild, so subsequent rebuilds with the same
conflict pattern resolve automatically. You can also record a resolution
manually by resolving the conflict in the trunk's working tree and running
`git rerere` there. Then set the patch back to `active` and rebuild:

```
afc patch update api-gateway <patch-id> --status active
afc rebuild submit api-gateway
```

**Resolving in origin mode.** Do not edit the trunk and do not push to the
hub: a hub push is rejected, forwarded or replaced, and a trunk edit is
overwritten at the next sync. Instead, fix the branch in a local clone of
the fork (merge or rebase the latest upstream into the patch branch, resolve
conflicts, commit), push it to the fork, and run sync:

```
afc workspace sync api-gateway
```

Sync brings the new tip into the hub and triggers a rebuild. If the patch
status is still `conflict` after the rebuild, set it back to `active`:

```
afc patch update api-gateway <patch-id> --status active
afc rebuild submit api-gateway
```

**Known limitation:** a resolution made on the fork is not recorded in the
hub's rerere cache, so the same conflict is resolved by hand again unless
the rebuild itself resolves it. This is a known limitation, not a planned
feature.

### 11. Roll back a rebuild

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

### Alternative: hub-authoritative workspace

If your workspace is driven by agents that push directly to the hub and
never open upstream PRs, leave `PATCH_BRANCH_SOURCE` unset (it defaults to
`hub`). In this flow:

1. **Create the workspace** as in step 1 above.
2. **Push the patch branch to the hub's git URL** (the workspace `hub_url`)
   instead of to the fork:

   ```
   git push <hub-git-url> feature/custom-auth-headers
   ```

3. **Register the patch** with `afc patch add` as in step 5.
4. **Let the push hook rebuild** -- pushing a registered patch branch
   automatically enqueues a rebuild (unless `AUTO_REBUILD_AFTER_PUSH` is
   `"false"`). Alternatively, trigger a rebuild manually:

   ```
   afc rebuild submit api-gateway
   ```

5. **Optionally mirror to the fork.** Set `PUSH_PATCHES_TO_ORIGIN=true` to
   have the hub force-push (mirror) every accepted patch-branch update to
   the fork, so the fork stays in sync without a separate push:

   ```
   afc vars create PUSH_PATCHES_TO_ORIGIN=true --workspace api-gateway
   ```

   A mirror failure is logged and emitted as `hub.patch.mirror_failed` but
   does not fail the push or the rebuild.

This flow suits agent-driven workspaces where the hub is the sole writer of
patch branches and the fork is only a mirror.

---

## Day-to-day operations

### Adding a new patch

The hub resolves the branch in this order when you add a patch:

1. `refs/heads/<name>` in the workspace trunk (local head).
2. `refs/remotes/origin/<name>` (origin tracking ref).
3. In `origin` mode only, a single-branch fork fetch for the branch.

If none of these finds the branch, the request fails with
`400 branch does not exist in repository or on origin`. A branch that
exists only on the fork no longer needs `--skip-branch-check`;
`--skip-branch-check` is for branches that exist nowhere yet.

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
position sequence with no gaps. If a backup ref `refs/hub/replaced/<branch>`
exists for the removed patch, it is deleted with the patch row. The branch
itself is not deleted.

### Restoring a soft-deleted patch

When patches are merged upstream and a rebuild completes, they are
soft-deleted rather than permanently removed. During the retention period
(7 days), you can restore a soft-deleted patch:

```
afc patch restore api-gateway <patch-id>
```

The patch is returned to `active` status and placed at the end of the patch
list. After the 7 days retention period, expired soft-deleted patches are
permanently removed only when a purge runs and can no longer be restored.

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
the database for 7 days, during which they can be restored. Expired
soft-deleted patches are permanently removed only when a purge runs (see
[Soft-delete lifecycle](#soft-delete-lifecycle)).

If you know a patch has been merged but sync has not detected it yet, you
can manually mark it:

```
afc patch update api-gateway <patch-id> --status merged_upstream
```

### Resolving conflicts after a failed rebuild

Start by reading the `conflict_files` from the patch-status dashboard:

```
afc workspace patch-status api-gateway
```

The `conflict_files` field on the conflicting patch lists the affected files.
The resolution procedure depends on the authority model.

#### Resolving in hub mode

Resolve the conflict in the workspace trunk or by pushing a fixed branch to
the hub's git server. Rerere records the resolution when it happens during a
rebuild, so subsequent rebuilds with the same conflict pattern resolve
automatically. You can also record a resolution manually by resolving the
conflict in the trunk's working tree and running `git rerere` there.

After resolving, set the patch status back to `active` and rebuild:

```
afc patch update api-gateway <patch-id> --status active
afc rebuild submit api-gateway
```

#### Resolving in origin mode

Do not edit the trunk and do not push to the hub: a hub push is rejected,
forwarded or replaced, and a trunk edit is overwritten at the next sync.

Instead, fix the branch in a local clone of the fork: merge or rebase the
latest upstream into the patch branch, resolve conflicts, and commit. Then
push the fixed branch to the fork and run sync:

```
afc workspace sync api-gateway
```

Sync brings the new tip into the hub and triggers a rebuild. If the patch
status is still `conflict` after the rebuild, set it back to `active`:

```
afc patch update api-gateway <patch-id> --status active
afc rebuild submit api-gateway
```

**Known limitation:** a resolution made on the fork is not recorded in the
hub's rerere cache, so the same conflict is resolved by hand again unless
the rebuild itself resolves it. This is a known limitation, not a planned
feature.

### Recovering a replaced or diverged patch branch

In `origin` mode, sync may replace a hub-side patch branch tip or report it
as diverged. Use the patch-status dashboard or the single-patch endpoint to
read the `origin_sync_state` and `replaced_sha` fields:

```
afc workspace patch-status api-gateway
```

If `origin_sync_state` is `diverged` (when `PATCH_DIVERGENCE_POLICY` is
`report`) or a replacement has occurred, the old tip is saved under
`refs/hub/replaced/<branch>`. Fetch it to a local clone:

```
git fetch <hub_url> refs/hub/replaced/<branch>
```

Push the fetched tip to the fork to keep it (for example, under a different
branch name) before it is overwritten. Only one backup per branch exists;
later replacements overwrite the earlier backup.

To move a `diverged` branch to the fork's current tip on demand, use:

```
afc patch reset-to-origin <slug> <patch-id>
```

The response includes `rebuild_triggered` indicating whether a rebuild was
enqueued. Reset is available for patches in `active`, `conflict` or
`disabled` status.

If `origin_sync_state` is `missing_on_origin`, the branch does not exist on
the fork. The hub keeps applying its own copy of the branch during rebuilds.
To resolve, either push the branch to the fork so the next sync picks it up,
or remove the patch if it is no longer needed.

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
    {"id": "3f2a9c0e...", "path": null, "recorded_at": "2025-03-15T14:22:00Z", "resolved": true},
    {"id": "9d8c7b6a...", "path": null, "recorded_at": "2025-03-10T09:15:00Z", "resolved": false}
  ]
}
```

Git keys rerere entries by a hash of the conflict, not by file, and only
knows the path while a conflict is in progress, so `path` is usually null.
`resolved: false` marks an entry for which only the conflict pattern has
been seen (no resolution recorded yet).

If a recorded resolution is outdated or incorrect and you want rerere to
re-prompt for manual resolution on the next conflict, forget it by id:

```
afc rerere forget api-gateway 3f2a9c0e...
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

### REBUILD_PUSH_INTEGRATION_BRANCH

Opt-in. When set to `"true"`, every successful rebuild force-pushes the
integration branch to `origin` using the workspace's stored credentials,
so codespaces and CI that clone the fork see the rebuilt branch without an
extra step. Off by default because the push rewrites the branch on the
remote; the job record reports `integration_branch_pushed: true` when it
happened, and a push failure is logged without failing the rebuild.

```
afc vars create REBUILD_PUSH_INTEGRATION_BRANCH=true --workspace api-gateway
```

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

### PATCH_BRANCH_SOURCE

Selects the authority for patch branches.

| Value | Behavior |
|-------|----------|
| `hub` (default) | Patch branches are read from the hub's local clone. The fork (`origin`) is never fetched during sync. |
| `origin` | The fork is fetched during sync and every registered patch branch is brought to the fork's tip, according to `PATCH_DIVERGENCE_POLICY`. |

The match is exact and case-sensitive. An unset variable or any unrecognised
value is treated as `hub`. The variable is read on every sync, so a change
takes effect on the next sync without a restart.

```
afc vars create PATCH_BRANCH_SOURCE=origin --workspace api-gateway
```

### PATCH_DIVERGENCE_POLICY

Controls what happens when the hub's copy and the fork's copy of a patch
branch have diverged. This variable is ignored in `hub` mode (where the hub
is the sole writer and divergence does not arise).

| Value | Behavior |
|-------|----------|
| `replace` (default) | The hub's copy is replaced with the fork's tip. The old tip is saved under `refs/hub/replaced/<branch>`. |
| `report` | The hub's copy is left unchanged and the divergence is reported in the sync response (`patches_diverged`). |

The match is exact and case-sensitive. An unset variable or any unrecognised
value is treated as `replace`. The variable is read on every sync.

```
afc vars create PATCH_DIVERGENCE_POLICY=report --workspace api-gateway
```

### AUTO_REBUILD_AFTER_SYNC

Controls whether a rebuild is automatically triggered when a sync detects
that upstream has advanced or a patch branch has changed (created,
fast-forwarded or replaced for an active or conflict patch).

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

### PUSH_PATCHES_TO_ORIGIN

Enables forwarding or mirroring of hub pushes to registered patch branches.
Only exactly `true` enables it; any other value or an unset variable means
disabled. In `origin` mode it means forwarding (the push is sent to the fork
before the hub writes its ref); in `hub` mode it means mirroring (accepted
pushes are force-pushed to the fork, best effort).

| Value | Behavior |
|-------|----------|
| `true` | Enables forwarding (in `origin` mode) or mirroring (in `hub` mode) of pushes to registered patch branches to the fork. |
| (unset or any other value) | Forwarding and mirroring are disabled (default). In `origin` mode, pushes to registered patch branches are rejected. |

The behaviour depends on `PATCH_BRANCH_SOURCE`:

- **Origin mode, disabled:** the push to a registered patch branch is rejected
  with a message naming the fork.
- **Origin mode, enabled:** the push is forwarded to the fork before the hub
  writes its ref. If the forward fails, the hub ref is not written.
- **Hub mode, enabled:** after the push is accepted, each updated registered
  patch branch is force-pushed (mirrored) to the fork, best effort. A mirror
  failure is logged and emitted as `hub.patch.mirror_failed` but does not fail
  the push or the rebuild.

```
afc vars create PUSH_PATCHES_TO_ORIGIN=true --workspace api-gateway
```

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

### Recommended combinations

| Style | Variables | Notes |
|-------|-----------|-------|
| fork-first | `PATCH_BRANCH_SOURCE=origin`, optional `REBUILD_PUSH_INTEGRATION_BRANCH=true` | Teams push to GitHub and open upstream PRs; the hub is a rebuild engine |
| fork-first with hub forwarding | `PATCH_BRANCH_SOURCE=origin`, `PUSH_PATCHES_TO_ORIGIN=true`, optional `REBUILD_PUSH_INTEGRATION_BRANCH=true` | Same as above, but tooling that only knows the hub URL can push and the hub forwards to the fork |
| hub-only | (defaults) | Agent-driven workspaces; patches live only on the hub |
| hub with a durable mirror | `PUSH_PATCHES_TO_ORIGIN=true` | Agent-driven, but every accepted push is mirrored to the fork so the fork stays in sync |

---

## How it works

### Rebuild algorithm

When a rebuild job runs, the hub executes the following steps:

1. **Resolve credentials.** Resolves the upstream credentials
   (`UPSTREAM_GIT_PAT`, then username/password, then the origin
   credentials). If resolution fails, the job is retried.

2. **Fetch upstream.** Fetches `refs/heads/*` and `HEAD` of the `upstream`
   remote into `refs/remotes/upstream/*` with those credentials (go-git,
   so private upstreams work). The job takes the workspace lock for this
   step and the next one only (the *fetch phase*).

3. **Resolve the base and create a worktree.** Determines the upstream base
   as `refs/remotes/upstream/HEAD`, falling back to
   `refs/remotes/upstream/<workspace branch>` and finally `FETCH_HEAD`, and
   sets the rebuild-active flag (archive and reclone answer `409
   workspace_busy` until the run has ended). Worktrees left by a crashed
   run under `<workspace_root>/<slug>/rebuild/` are removed and pruned, and
   a legacy `_rebuild_temp` branch from a pre-worktree hub version is
   deleted once (legacy migration). The lock is then released and the job
   creates a detached `git worktree` for this run at
   `<workspace_root>/<slug>/rebuild/<job id>`, positioned at the base. The
   worktree shares the trunk's objects, refs, configuration and rerere
   cache but has its own HEAD, index and files, so a rebuild does not
   change the trunk's checkout (its HEAD, index and files stay as they
   were). While patches are applied no lock is held: syncs, rollbacks,
   merge jobs and pushes to the same workspace proceed normally.

4. **Enable rerere and snapshot the patch tips.** Sets `rerere.enabled=true`
   and `rerere.autoupdate=true` in the repo's git config (shared by every
   worktree). Then each patch that will be attempted is resolved to a
   commit SHA at `refs/heads/<branch>`; only that SHA is used for the rest
   of the run and it is reported as `source_sha` in the patch result.
   The patch tips it snapshots are whatever the last sync (origin mode)
   or push (hub mode) left in the trunk.

5. **Process each patch in position order, inside the worktree:**
   - **Skipped patches:** `merged_upstream`, `disabled`, and `deleted`
     patches are skipped. `merged_upstream` patches are collected for
     soft-deletion after the rebuild completes.
   - **Missing branches:** If the branch does not exist as a local head
     (`refs/heads/<branch>`), the patch is skipped (not an error).
   - **Rebase strategy:** Identifies commits unique to the patch branch
     (`git log --right-only --cherry-pick --no-merges <base>...<sha>`, so
     commits already applied upstream under a different SHA are skipped) and
     cherry-picks each one in order. A cherry-pick that turns out empty is
     skipped rather than treated as a conflict.
   - **Merge strategy:** Merges the patch tip with `--no-ff`, with the
     message `Merge branch '<branch name>'`.
   - **Conflict handling:** If a cherry-pick or merge produces conflicts,
     the hub runs `git rerere` to attempt automatic resolution. If rerere
     resolves all conflicts, the operation continues. If unresolved
     conflicts remain:
     - In `fail_fast` mode (default): the patch is marked `conflict` and
       the rebuild stops immediately.
     - In `continue` mode: the patch is marked `conflict`, the worktree
       is reset to its pre-patch state, and processing continues with the
       next patch.
   - **Progress tracking:** After each patch is processed, the per-patch
     results are written to the job's progress field. Clients polling
     the rebuild status can observe which patches have been processed
     while the job is still running.

6. **Finalize on success.** Re-acquires the workspace lock (the *final
   phase*), captures the previous integration branch HEAD SHA (for
   rollback), force-updates the integration branch ref to the worktree's
   final HEAD with `git update-ref` (the integration branch is never checked
   out), soft-deletes `merged_upstream` patches, and compacts positions.
   When the `REBUILD_PUSH_INTEGRATION_BRANCH` workspace variable is
   `"true"`, the integration branch is then force-pushed to `origin` with the
   workspace credentials and the job record reports
   `integration_branch_pushed`. The only time the trunk is touched is when
   the trunk happens to have the integration branch checked out: it is then
   hard-reset so its files match the new tip.

   The worktree is removed on every exit path (success, conflict, failure,
   cancellation), and the rebuild-active flag is cleared afterwards. Clones
   and fetches served by the hub during a rebuild see the pre-rebuild
   integration branch until the final phase completes.

   **Follow-up rebuild.** Because patch application runs without the lock,
   a patch branch can be pushed, or upstream synced, while a run is in
   progress. After the worktree is removed, the job compares each applied
   patch tip and the upstream base with their current values. If a patch tip
   is stale, one follow-up rebuild is enqueued (`submitted_by:
   system:stale-snapshot`, audit event `hub.rebuild.followup`) unless
   `AUTO_REBUILD_AFTER_PUSH` is `"false"`; if the upstream base is stale, one
   is enqueued unless `AUTO_REBUILD_AFTER_SYNC` is `"false"`. The check runs
   after a successful run and after a fail-fast conflict, not after a
   retryable failure or cancellation (a retry takes a fresh snapshot).

### Sync algorithm

The carry-patch sync differs from the standard workspace sync. The
`PATCH_BRANCH_SOURCE` workspace variable selects the model (see
[Where patch branches live](#where-patch-branches-live)). The sync flow
proceeds through these phases in order:

1. **Resolve credentials.** Read `PATCH_BRANCH_SOURCE` and
   `PATCH_DIVERGENCE_POLICY` workspace variables. If `PATCH_BRANCH_SOURCE`
   is `origin`, resolve origin credentials before any fetch (failure
   answers `502 failed to resolve origin credentials` and aborts without
   touching any state). Then resolve upstream credentials.

2. **Fetch upstream.** Fetch `refs/heads/*` and `HEAD` of the `upstream`
   remote into `refs/remotes/upstream/*` with the resolved credentials
   (same refspecs and credentials as the rebuild). A fetch failure answers
   `502 upstream fetch failed` and aborts; no refs or patch state are
   modified.

3. **Fetch origin (origin mode only).** Fetch the `origin` remote with
   pruning (stale tracking refs are removed). If the origin fetch fails,
   the sync answers `502 origin fetch failed` (error type
   `origin_fetch_failed`) and all refs and patch state remain unchanged —
   the upstream fetch that already succeeded is not rolled back, but no
   patch refresh, merge detection or timestamp write occurs.

4. **Refresh patch branches (origin mode only).** For each candidate patch
   (status `active`, `conflict` or `disabled`, excluding the integration
   branch), compare the hub's `refs/heads/<branch>` with the fork's
   `refs/remotes/origin/<branch>` and bring the local branch to the fork's
   tip using compare-and-swap ref writes. Each branch produces one of
   three outcomes:
   - **`created`** — the branch did not exist on the hub and was created
     from the fork's tip.
   - **`fast_forwarded`** — the hub's copy was an ancestor of the fork's
     tip and was advanced.
   - **`replaced`** — the hub's copy diverged from the fork's tip and was
     replaced (the old tip is saved under `refs/hub/replaced/<branch>`).

   Each branch also records a sync state: `in_sync`, `diverged` or
   `missing_on_origin`. When `PATCH_DIVERGENCE_POLICY` is `report`,
   diverged branches are left unchanged instead of being replaced.

   A ref-write failure stops the refresh. Outcomes already produced are
   persisted, and a rebuild is enqueued for branches already moved (unless
   `AUTO_REBUILD_AFTER_SYNC` is `"false"`), but `last_sync_at` is not
   written. A branch that moved but whose trunk work-tree reset then failed
   counts as moved too: its outcome is kept and persisted, and the sync
   answers `500` with a message that says the reset failed, not that the
   ref update failed.

5. **Resolve the base.** Determine the new upstream base
   (`refs/remotes/upstream/HEAD`, with the same fallbacks as the rebuild)
   and compare it with the stored `upstream_head_sha`.

6. **Detect force-push.** If the stored upstream HEAD is not an ancestor of
   the new upstream HEAD, set `force_push_detected` to true. This is
   informational and does not block the sync.

7. **Detect merged patches.** Merge detection runs when upstream advanced
   or when any patch branch was moved (also when only patch tips changed
   and upstream did not advance). For each `active` patch, apply the
   configured detection strategy (see `SQUASH_MERGE_DETECTION`):
   - **Ancestry check:** `git merge-base --is-ancestor` to test whether the
     patch branch HEAD is an ancestor of the new upstream HEAD.
   - **Content-based check:** `git cherry` to compare patch commits against
     upstream. If all patch commits have content-equivalent matches upstream
     (zero pending commits), the patch is considered merged.
   - **PR-number scanning:** If the patch has an `upstream_pr_url`, extract
     the PR number and scan recent upstream commit messages for the
     `(#NNN)` pattern used by GitHub's squash-merge.
   - If any signal detects the patch as merged, transition it to
     `merged_upstream`. A newly merged patch triggers a rebuild.

8. **Auto-rebuild.** If `AUTO_REBUILD_AFTER_SYNC` is not `"false"` and
   upstream advanced, an `active` or `conflict` patch branch was moved, or
   at least one patch was newly marked `merged_upstream`, enqueue a rebuild
   job. A moved `disabled` patch does not count as advanced. If a rebuild
   job is already queued or running, the duplicate is silently ignored.

9. **Write timestamps.** `last_sync_at` and `updated_at` are written on
   every completed sync, whether or not anything advanced. They are not
   written when the sync ends on a ref-write failure (phase 4).

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

**How a push to a registered patch branch is treated in each mode:**

- **`origin` mode, `PUSH_PATCHES_TO_ORIGIN` disabled (default):** the push
  is rejected with a message naming the fork URL (e.g. `branch is synced
  from origin; push to <git_url> instead`). Deletes of a registered patch
  branch are also rejected.
- **`origin` mode, `PUSH_PATCHES_TO_ORIGIN=true`:** the push is forwarded
  to the fork before the hub writes its ref. If the forward fails, the hub
  ref is not written. Deletes are rejected without contacting the fork.
- **`hub` mode (default):** the push is accepted normally. When
  `PUSH_PATCHES_TO_ORIGIN=true`, each accepted update to a registered
  patch branch is force-pushed (mirrored) to the fork, best effort. A
  mirror failure is logged and emitted as `hub.patch.mirror_failed` but
  does not fail the push or the rebuild. The mirror runs even when
  `AUTO_REBUILD_AFTER_PUSH=false`.

`head_sha` is refreshed from the trunk HEAD after every push, including a
push in which every ref was rejected. Only accepted ref updates drive the
`hub.git.push` event and the post-push hook (including the rebuild
enqueue). Rejected refs, and forwards that fail, trigger neither.

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
   endpoint within the 7 days retention period.
4. Expired soft-deleted patches (older than 7 days) are permanently removed
   only when a purge runs. No background scheduler triggers the purge
   automatically; an operator or external job must call the purge routine.

This provides a safety net: if a patch was incorrectly detected as merged
upstream, it can be recovered without needing to re-create it from scratch.

---

## API reference summary

### Patch endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/workspaces/:slug/patches` | Add one or more patches (single object or JSON array) |
| `GET` | `/workspaces/:slug/patches` | List non-deleted patches in position order |
| `GET` | `/workspaces/:slug/patches/:id` | Get a single patch (includes `replaced_sha` when a backup ref exists) |
| `PATCH` | `/workspaces/:slug/patches/:id` | Update patch fields (status, position, description, upstream_pr_url) |
| `DELETE` | `/workspaces/:slug/patches/:id` | Permanently remove a patch (also removes the backup ref) |
| `POST` | `/workspaces/:slug/patches/:id/restore` | Restore a soft-deleted patch |
| `POST` | `/workspaces/:slug/patches/:id/reset-to-origin` | Reset a patch branch to the fork's current tip |
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
| `POST` | `/workspaces/:slug/sync` | Sync from upstream (and origin in `origin` mode) with merge detection and auto-rebuild |
| `GET` | `/workspaces/:slug/patch-status` | Patch-status dashboard with summary counts |
| `GET` | `/workspaces/:slug/rerere` | List recorded rerere resolutions |
| `DELETE` | `/workspaces/:slug/rerere/*pathspec` | Forget a recorded rerere resolution |

### Permission scopes

| Scope | Used by |
|-------|---------|
| `rebuilds:read` | List rebuilds, get rebuild status, rebuild preview |
| `rebuilds:write` | Submit rebuild, cancel rebuild, requeue rebuild, rollback rebuild |
| `patches:read` | List patches, get single patch |
| `patches:write` | Add, update, remove, restore, reorder, reset patches |
| `workspaces:read` | Patch-status dashboard, rerere list |
| `workspaces:write` | Rerere forget |
| `workspaces:sync` | Sync from upstream |

---

## Limitations

**Immutable workspace fields.** The `workspace_mode`, `upstream_url`, and
`integration_branch` fields are set at workspace creation time and cannot be
changed afterward. To switch from `standard` to `carry_patch` (or vice
versa), you must create a new workspace.

**Integration branch position after creation.** During workspace creation
the hub fetches `upstream` (with the stored credentials) and creates the
integration branch at the upstream base when that fetch succeeds; if the
fetch fails (for example because credentials are added later), the branch is
created at the fork's HEAD and is only correctly positioned after the first
sync and rebuild.

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
resubmitting. More generally, every operation that touches the clone (sync,
rebuild, merge, rollback, rerere forget, archive, reclone, git push) takes a
per-workspace lock; HTTP calls that find it taken answer 409
`workspace_busy` instead of waiting. A rebuild holds that lock only in two
phases: the upstream fetch and base resolution at the start (fetch phase) and
the integration-ref update, bookkeeping and optional push at the end (final
phase). While it applies patches in its own worktree, sync, rollback, rerere
forget, batch rebase, merge jobs and pushes proceed. Archive and reclone
delete the directory the worktree lives in, so they answer 409
`workspace_busy` for the whole rebuild.

**First rebuild cannot be rolled back.** The rollback mechanism requires a
previous integration branch HEAD SHA, which is only available after at least
one prior rebuild has completed.

**No fork webhooks and no built-in sync schedule.** The hub does not
receive push events from the fork. Sync is triggered by `afc workspace sync`,
an operator's scheduler, or any client of `POST /workspaces/:slug/sync`.
There is no built-in cron or webhook listener.

**Sync fetches every fork branch.** In `origin` mode, the origin fetch
retrieves all branches from the fork, not only the registered patch branches.
A fork with many branches incurs a larger fetch.

**Manual resolutions are not fed into rerere in origin mode.** When a
conflict is resolved on the fork and pushed, the hub's rerere cache does not
learn the resolution. The same conflict must be resolved by hand again unless
the rebuild itself resolves it via a previously recorded rerere entry.

**`missing_on_origin` does not disable the patch.** When a registered patch
branch is absent from the fork, the hub keeps applying its own copy during
rebuilds. The patch is not automatically disabled or removed; an operator
must act.

**Origin mode needs working origin credentials on every sync.** The fork
fetch uses the workspace's `GIT_PAT` or `GIT_USERNAME`/`GIT_PASSWORD`
credentials. If they expire or are revoked, sync fails with
`502 failed to resolve origin credentials` and no state is updated.
