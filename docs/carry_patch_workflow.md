# Carry-Patch Workflow

This guide is for people who maintain a fork of a repository they do not
control and carry their own changes on top of it. It explains the hub's
carry-patch workspace mode: you register an ordered list of patch branches, and
the hub keeps an integration branch that is always "upstream plus your
patches", notices when a patch has been merged upstream, remembers how
conflicts were resolved, and lets you roll back a bad result.

Neighbouring documents: the [API reference](api.md) and the
[CLI reference](cli.md) give every request, response and flag; the
[settings reference](api.md#workspace-settings-reference) lists every workspace
setting; [architecture.md](architecture.md#the-carry-patch-module) describes how
carry-patch is built. For coding agents working in a carry-patch fork, see the
example instruction file [examples/AGENTS_carry_patch.md](examples/AGENTS_carry_patch.md).

## Contents

- [The problem carry-patch solves](#the-problem-carry-patch-solves)
- [Concepts](#concepts)
  - [Workspace modes](#workspace-modes)
  - [Remotes](#remotes)
  - [Where patch branches live](#where-patch-branches-live)
  - [Integration branch](#integration-branch)
  - [Patch list](#patch-list)
  - [Patch statuses](#patch-statuses)
  - [Origin sync states](#origin-sync-states)
  - [Rebuild strategies](#rebuild-strategies)
  - [Fail modes](#fail-modes)
  - [Git rerere](#git-rerere)
- [Getting started](#getting-started)
  - [1. Create a carry-patch workspace](#1-create-a-carry-patch-workspace)
  - [2. Set upstream credentials](#2-set-upstream-credentials)
  - [3. Choose the authority](#3-choose-the-authority)
  - [4. Create and push a patch branch](#4-create-and-push-a-patch-branch)
  - [5. Register your patches](#5-register-your-patches)
  - [6. Sync](#6-sync)
  - [7. Preview a rebuild](#7-preview-a-rebuild)
  - [8. Run a rebuild](#8-run-a-rebuild)
  - [9. Check the status dashboard](#9-check-the-status-dashboard)
  - [10. Handle a failed rebuild](#10-handle-a-failed-rebuild)
  - [11. Roll back a rebuild](#11-roll-back-a-rebuild)
  - [Alternative: hub-authoritative workspace](#alternative-hub-authoritative-workspace)
- [Day-to-day operations](#day-to-day-operations)
  - [Adding a new patch](#adding-a-new-patch)
  - [Removing a patch](#removing-a-patch)
  - [Reordering patches](#reordering-patches)
  - [Disabling a patch temporarily](#disabling-a-patch-temporarily)
  - [Handling a merged patch](#handling-a-merged-patch)
  - [Restoring a soft-deleted patch](#restoring-a-soft-deleted-patch)
  - [Resolving conflicts after a failed rebuild](#resolving-conflicts-after-a-failed-rebuild)
  - [Recovering a replaced or diverged patch branch](#recovering-a-replaced-or-diverged-patch-branch)
  - [Recovering from an upstream force-push](#recovering-from-an-upstream-force-push)
  - [Cancelling or requeuing a rebuild](#cancelling-or-requeuing-a-rebuild)
  - [Viewing rebuild history](#viewing-rebuild-history)
  - [Managing rerere resolutions](#managing-rerere-resolutions)
  - [Pushing other branches to the fork](#pushing-other-branches-to-the-fork)
- [Settings reference](#settings-reference)
  - [patch_branch_source](#patch_branch_source)
  - [patch_divergence_policy](#patch_divergence_policy)
  - [push_patches_to_origin](#push_patches_to_origin)
  - [rebuild_strategy](#rebuild_strategy)
  - [rebuild_fail_mode](#rebuild_fail_mode)
  - [rebuild_push_integration_branch](#rebuild_push_integration_branch)
  - [auto_rebuild_after_sync](#auto_rebuild_after_sync)
  - [auto_rebuild_after_push](#auto_rebuild_after_push)
  - [squash_merge_detection](#squash_merge_detection)
  - [Recommended combinations](#recommended-combinations)
- [How it works](#how-it-works)
  - [What a sync does](#what-a-sync-does)
  - [What a rebuild does](#what-a-rebuild-does)
  - [How merged patches are detected](#how-merged-patches-are-detected)
  - [How a push to a patch branch is handled](#how-a-push-to-a-patch-branch-is-handled)
  - [How rerere replays resolutions](#how-rerere-replays-resolutions)
  - [What a preview does](#what-a-preview-does)
  - [What a rollback restores](#what-a-rollback-restores)
  - [Soft delete and restore](#soft-delete-and-restore)
  - [What can run alongside a rebuild](#what-can-run-alongside-a-rebuild)
- [Limitations](#limitations)
- [API and CLI reference](#api-and-cli-reference)

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

Every hub workspace operates in one of two modes, set at creation time and
immutable afterward:

| Mode | Description |
|------|-------------|
| `standard` | Default. Single-remote workspace for branch-based development. |
| `carry_patch` | Dual-remote workspace with an ordered patch list and a mechanically rebuilt integration branch. |

### Remotes

The hub's clone of a carry-patch workspace has two git remotes:

| Remote | Points to | Purpose |
|--------|-----------|---------|
| `origin` | Your fork (the `git_url` given at creation) | Push target for the rebuilt integration branch (when [`rebuild_push_integration_branch`](#rebuild_push_integration_branch) is on) and, in `origin` mode, the authority for patch branches |
| `upstream` | The upstream project (the `upstream_url` given at creation) | Source of the base the patches are applied to; fetched by every sync and every rebuild |

### Where patch branches live

A rebuild applies the hub's own copy of each patch branch. The workspace
setting [`patch_branch_source`](#patch_branch_source) decides who writes that
copy:

- **`hub`** (default) -- patch branches are written by pushes to the hub's
  git server. Sync never fetches the fork.
- **`origin`** -- the fork is the authority. Every sync fetches the fork and
  brings each registered patch branch to the fork's tip, according to
  [`patch_divergence_policy`](#patch_divergence_policy).

A fork branch is not used by a rebuild until something copies it into the
hub's clone: in `origin` mode, a sync; in `hub` mode, a push to the hub (or
registering the patch, see [step 5](#5-register-your-patches)).

**Comparison of the two authority models:**

| Aspect | `hub` mode | `origin` mode |
|--------|-----------|---------------|
| Who writes the branch | A push to the hub's git server | Sync copies the fork's tip into the hub |
| What sync does with it | Nothing (the fork is not fetched) | Fetches the fork, moves each registered branch to the fork's tip |
| What a push to the hub does | Accepted; also mirrored to the fork when `push_patches_to_origin=true` | Rejected by default; forwarded to the fork when `push_patches_to_origin=true` |
| What happens on divergence | Does not arise (the hub is the sole writer) | `replace` (default; the old tip is kept as a backup) or `report`, per `patch_divergence_policy` |
| Where a person fixes a conflict | On the hub: push a fixed branch to the hub's git server | On the fork: push a fixed branch to the fork, then `afc workspace sync` |
| Which copy survives a lost hub data directory | None (re-push from a local clone) | The fork's copy; the next sync restores it |

**Recommendation.** Teams that open upstream pull requests and review on the
fork should use `origin` with `rebuild_push_integration_branch=true`, so
people push only to GitHub and the hub is a rebuild engine. `hub` mode fits
workspaces driven by agents that push to the hub and never open upstream
pull requests.

The integration branch is always built by the hub. It is pushed to the fork
only when `rebuild_push_integration_branch=true` and is never fetched from
the fork.

**Switching modes.** The mode is read at the start of every sync,
registration and push, so a change takes effect with the next one; no restart
is needed.

- Switching to `origin` makes the next sync bring registered branches to the
  fork's tips. If the hub's copy has diverged, the previous tip is kept as a
  backup (when the policy is `replace`).
- Switching to `hub` stops fork fetches; the next sync clears the patches'
  origin sync state.

### Integration branch

The integration branch is a mechanically maintained branch that represents
"upstream HEAD plus all active patches applied in order." By default it is
named `deploy`; you can choose another name at workspace creation time.

Never commit directly to the integration branch. Every rebuild replaces it,
and any manual commits are lost.

### Patch list

The patch list is the ordered sequence of branches registered with the
workspace. Each patch has a 1-based position that determines the order in
which branches are applied during a rebuild. The hub keeps these fields for
each patch:

| Field | Description |
|-------|-------------|
| `id` | The patch's identifier, used by every `afc patch` command |
| `branch_name` | Git branch in the workspace repository |
| `position` | 1-based application order (lower = applied first) |
| `status` | Current lifecycle state (see below) |
| `conflict_files` | Files that conflicted in the last rebuild, while the patch is in `conflict` |
| `upstream_pr_url` | Optional link to the corresponding upstream pull request (also used to detect squash merges) |
| `description` | Optional free-form description |
| `deleted_at` | When a merged patch was soft-deleted (null otherwise) |
| `origin_sync_state`, `origin_sha`, `origin_synced_at` | `origin` mode only: how the hub's copy relates to the fork's, the fork's tip and when the last sync recorded them |

The full schema is in [Patch Endpoints](api.md#patch-endpoints).

### Patch statuses

| Status | Meaning |
|--------|---------|
| `active` | Normal state. The patch is applied during rebuilds. |
| `merged_upstream` | The hub detected that the patch is now part of upstream (see [How merged patches are detected](#how-merged-patches-are-detected)). It is skipped by rebuilds and soft-deleted by the next successful rebuild. |
| `conflict` | A rebuild hit an unresolved conflict on this patch. It stays in the list and is tried again by every rebuild, but keeps this status -- even after a rebuild applies it cleanly -- until you set it back to `active`. |
| `disabled` | Disabled by an operator. Skipped by rebuilds but kept. |
| `deleted` | Soft-deleted after being merged upstream. Hidden from lists and the dashboard, but can be restored (see [Soft delete and restore](#soft-delete-and-restore)). |

### Origin sync states

In `origin` mode, every sync records for each registered patch how the hub's
copy relates to the fork's copy:

- **`in_sync`** -- the hub's copy matches the fork's tip. This is also the
  state after a sync created, fast-forwarded or replaced the hub's copy.
- **`diverged`** -- the copies have diverged and the hub left its copy alone
  because `patch_divergence_policy` is `report`. (With the default
  `replace`, the hub's copy is replaced instead and the state is `in_sync`.)
- **`missing_on_origin`** -- the branch does not exist on the fork. The patch
  status does not change: rebuilds keep applying the hub's copy until an
  operator pushes the branch to the fork, disables the patch or removes it.

### Rebuild strategies

The rebuild strategy determines how each patch branch is applied on top of
upstream HEAD:

| Strategy | Behavior |
|----------|----------|
| `rebase` (default) | Cherry-picks the patch branch's own commits, one by one. Produces a linear history. |
| `merge` | Merges each patch branch with `--no-ff`. Each patch appears as a merge commit, preserving the branch's own commit structure. |

The [`rebuild_strategy`](#rebuild_strategy) setting chooses the strategy; a
single rebuild can override it with `afc rebuild submit --strategy`.

### Fail modes

The fail mode determines what a rebuild does with a patch that produces a
conflict rerere cannot resolve:

| Mode | Behavior |
|------|----------|
| `fail_fast` (default) | Stops the rebuild at the first unresolved conflict. The failing patch is marked `conflict`, later patches are not attempted, and the integration branch is not changed. |
| `continue` | Marks the failing patch `conflict`, leaves it out, and carries on with the remaining patches. The integration branch is updated with every patch that applied cleanly. |

The [`rebuild_fail_mode`](#rebuild_fail_mode) setting chooses the mode; a
single rebuild can override it with `afc rebuild submit --fail-mode`.

### Git rerere

The hub enables git rerere ("reuse recorded resolution") in every carry-patch
workspace. Git records each conflict it meets; once a resolution for that
conflict has been recorded, later rebuilds that meet the same conflict resolve
it automatically, without an operator.

This matters most when upstream force-pushes or when the same lines change on
both sides across many rebuild cycles. See
[How rerere replays resolutions](#how-rerere-replays-resolutions) and
[Managing rerere resolutions](#managing-rerere-resolutions).

---

## Getting started

This walkthrough maintains a fork of `github.com/acme-oss/api-gateway` at
`github.com/your-org/api-gateway-fork` with three patch branches. It uses the
fork-authoritative model (`patch_branch_source=origin`), recommended for teams
that open upstream pull requests. An
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

`--integration-branch` is optional and defaults to `deploy`. The hub clones
the fork, adds an `upstream` remote pointing to the upstream URL, enables
rerere, and creates the integration branch. Settings can be given here too
(`--setting key=value`), or later with `afc workspace update`.

### 2. Set upstream credentials

Skip this step for a public upstream. For a private one, store credentials
separately from the fork credentials:

```
printf '%s' "$UPSTREAM_TOKEN" | \
  afc secrets create UPSTREAM_GIT_PAT --from-stdin --workspace api-gateway
```

Or use username/password authentication:

```
afc secrets create UPSTREAM_GIT_USERNAME=your-bot-user --workspace api-gateway
printf '%s' "$UPSTREAM_TOKEN" | \
  afc secrets create UPSTREAM_GIT_PASSWORD --from-stdin --workspace api-gateway
```

These are *reserved* secret names the hub reads to authenticate against the
upstream (see [Reserved secret names](cli.md#reserved-secret-names)). The hub
uses, in this order:

1. the `UPSTREAM_GIT_PAT` workspace secret;
2. the `UPSTREAM_GIT_USERNAME` + `UPSTREAM_GIT_PASSWORD` workspace secrets;
3. the fork's credentials (`GIT_PAT`, or `GIT_USERNAME` + `GIT_PASSWORD`).

Manage them with `afc secrets list|update|delete --workspace api-gateway`.

### 3. Choose the authority

Make the fork the authority for patch branches:

```
afc workspace update api-gateway --setting patch_branch_source=origin
```

Optionally, push every rebuilt integration branch back to the fork, so CI and
codespaces see it:

```
afc workspace update api-gateway --setting rebuild_push_integration_branch=true
```

In `origin` mode every sync fetches the fork with the workspace's fork
credentials (`GIT_PAT`, or `GIT_USERNAME`/`GIT_PASSWORD`), so they must allow
reading the fork. Set them at creation (`--git-pat` above) or with
`afc secrets create`.

See [Where patch branches live](#where-patch-branches-live) for a full
comparison of the two authority models.

### 4. Create and push a patch branch

In a normal clone of the fork, create a branch, commit your changes, and
push it to the fork:

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

Register your patch branches in the order they should be applied:

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

The hub looks for the branch in its own clone first, then among the fork
branches it cloned, and, in `origin` mode only, fetches that one branch from
the fork. A branch that exists only on the fork can therefore be registered
directly. If the branch is found nowhere, the request fails with
`400 branch does not exist in repository or on origin`.

Patches are appended to the end of the list. To insert at a specific position
(existing patches shift down), use `--position`:

```
afc patch add api-gateway \
  --branch fix/urgent-security-patch \
  --position 1 \
  --description "Security fix that must be applied first"
```

Two more flags help automation:

- `--if-not-exists` returns the existing patch (HTTP 200) instead of an error
  when the branch is already registered.
- `--skip-branch-check` skips the branch lookup entirely, for a branch that
  exists nowhere yet (for example, one that will be pushed later).

The API also accepts several patches in one request (a JSON array); the batch
is all-or-nothing. See [Patch Endpoints](api.md#patch-endpoints).

### 6. Sync

Bring the latest upstream and the fork's patch branches into the hub:

```
afc workspace sync api-gateway
```

In `origin` mode, sync -- not `afc rebuild submit` -- is what brings new fork
commits into the hub. A rebuild applies the hub's copy of each branch and does
not fetch the fork.

For carry-patch workspaces, the sync response is the usual workspace JSON
with extra fields:

```json
{
  "slug": "api-gateway",
  "workspace_mode": "carry_patch",
  "sync_error": null,
  "upstream_head_sha": "abc123def456...",
  "last_sync_at": "2024-06-15T10:30:00Z",

  "origin_fetched": true,
  "patches_merged": ["fix/connection-pool-leak"],
  "patches_synced": [
    {
      "branch_name": "feature/custom-auth-headers",
      "action": "fast_forwarded",
      "state": "in_sync",
      "local_sha": "9f1c2ab...",
      "origin_sha": "9f1c2ab..."
    }
  ],
  "patches_diverged": [],
  "rebuild_triggered": true,
  "force_push_detected": false
}
```

(Abbreviated -- every standard workspace field is present too.)

| Field | Meaning |
|-------|---------|
| `origin_fetched` | Whether the fork was fetched (`true` only in `origin` mode). Always present. |
| `patches_merged` | Patches this sync found merged upstream; they are now `merged_upstream`. |
| `patches_synced` | `origin` mode only: what the sync did to each patch branch (see below). |
| `patches_diverged` | `origin` mode only: branches left alone because they diverged and the policy is `report`. |
| `rebuild_triggered`, `rebuild_job_id` | Whether the sync queued a rebuild, and its id. |
| `force_push_detected` | Upstream rewrote its history since the last sync. Informational; the sync proceeds normally. |

Each entry of `patches_synced` has an `action`:

- **`created`** -- the branch did not exist on the hub and was created from
  the fork's tip.
- **`fast_forwarded`** -- the hub's copy was behind the fork's tip and was
  advanced.
- **`replaced`** -- the hub's copy had diverged from the fork's tip and was
  replaced; the old tip is kept as a backup (see
  [Recovering a replaced or diverged patch branch](#recovering-a-replaced-or-diverged-patch-branch)).

A sync queues a rebuild when upstream advanced, when an `active` or `conflict`
patch branch moved, or when a patch was newly found merged -- unless
[`auto_rebuild_after_sync`](#auto_rebuild_after_sync) is off. Add `--wait` to
block until that rebuild finishes.

**Divergence handling.** With `patch_divergence_policy=report`, diverged
branches are left unchanged and listed in `patches_diverged`. Use
`--fail-on-diverged` to make the CLI exit with code 3 when that list is not
empty:

```
afc workspace sync api-gateway --fail-on-diverged
```

stdout still holds the sync response as a single JSON document, and a message
naming the diverged branches goes to stderr, so a CI job can both fail on the
exit code and read `patches_diverged` from stdout.

### 7. Preview a rebuild

Before running a rebuild, you can preview which patches would conflict
without changing anything:

```
afc rebuild preview api-gateway
```

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
cumulative: each patch is tested on top of all earlier patches that would
succeed, so cascading conflicts show up. See
[What a preview does](#what-a-preview-does).

### 8. Run a rebuild

A sync or a push usually queues a rebuild for you. To run one by hand -- for
example after changing the patch list:

```
afc rebuild submit api-gateway
```

Override the strategy or fail mode for this rebuild only:

```
afc rebuild submit api-gateway --strategy merge --fail-mode continue
```

The rebuild runs in the background. Check its status, or submit and wait:

```
afc rebuild status api-gateway <rebuild-id>
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

Each patch result is `success`, `conflict` or `skipped` (with a
`skipped_reason`). While a rebuild is running, `patch_results` already lists
the patches processed so far, so polling shows progress.

### 9. Check the status dashboard

Get one view of the workspace and its patch stack:

```
afc workspace patch-status api-gateway
```

This returns workspace metadata, the last rebuild, each patch with its status,
its result in the last rebuild and any `conflict_files`, and summary counts
(including the number of recorded rerere resolutions). In `origin` mode each
patch also carries its `origin_sync_state`, and the summary counts
`patches_diverged` and `patches_missing_on_origin`.

### 10. Handle a failed rebuild

In `fail_fast` mode, a conflict stops the rebuild, marks the patch `conflict`
and leaves the integration branch as it was:

```json
{
  "status": "failed",
  "error": "conflict in patch \"fix/urgent-security-patch\": pkg/auth.go"
}
```

In `continue` mode the rebuild completes with a mix of outcomes, and the
integration branch gets every patch that applied cleanly:

```json
{
  "patch_results": [
    {"branch_name": "fix/urgent-security-patch", "status": "conflict", "conflict_files": ["pkg/auth.go"]},
    {"branch_name": "feature/custom-auth-headers", "status": "success", "new_head_sha": "def456..."},
    {"branch_name": "internal/custom-metrics", "status": "success", "new_head_sha": "ghi789..."}
  ]
}
```

Either way, fix the patch branch and rebuild; the steps depend on the
authority model and are in
[Resolving conflicts after a failed rebuild](#resolving-conflicts-after-a-failed-rebuild).

### 11. Roll back a rebuild

If a rebuild produced a bad integration branch, put the branch back where it
was before that rebuild:

```
afc rebuild rollback api-gateway <rebuild-id>
```

```json
{
  "rolled_back_to": "aaa111..."
}
```

The first rebuild of a workspace cannot be rolled back (there is no earlier
state). After rolling back, fix the patch list or the patch branches and run
a new rebuild. See [What a rollback restores](#what-a-rollback-restores).

### Alternative: hub-authoritative workspace

If agents push straight to the hub and nobody opens upstream PRs, leave
`patch_branch_source` at its default, `hub`:

1. **Create the workspace** as in [step 1](#1-create-a-carry-patch-workspace).
2. **Push the patch branch to the hub** -- the workspace's `hub_url` --
   instead of to the fork:

   ```
   git push <hub_url> feature/custom-auth-headers
   ```

3. **Register the patch** with `afc patch add` as in
   [step 5](#5-register-your-patches).
4. **Let the push rebuild.** Every push to a registered patch branch queues a
   rebuild (unless [`auto_rebuild_after_push`](#auto_rebuild_after_push) is
   off). You can also run one by hand with `afc rebuild submit api-gateway`.
5. **Optionally mirror to the fork.** With `push_patches_to_origin=true` the
   hub force-pushes every accepted patch-branch update to the fork, so the
   fork keeps a copy:

   ```
   afc workspace update api-gateway --setting push_patches_to_origin=true
   ```

   A failed mirror is logged and recorded as a `hub.patch.mirror_failed`
   audit event; it does not fail the push or the rebuild.

---

## Day-to-day operations

Each operation below takes a patch or rebuild id. `afc patch list <slug>`
prints the patches with their ids; `afc rebuild list <slug>` prints the
rebuilds. The rebuild that follows most changes to the patch list is not
automatic: run `afc rebuild submit <slug>` afterwards.

### Adding a new patch

Push the branch (to the fork in `origin` mode, to the hub in `hub` mode), then
register it. It is appended to the end of the list unless you give
`--position` (existing patches shift down):

```
afc patch add api-gateway \
  --branch feature/new-rate-limiter \
  --upstream-pr https://github.com/acme-oss/api-gateway/pull/315 \
  --description "Custom rate limiting by tenant ID"

afc patch add api-gateway --branch fix/critical-hotfix --position 1

afc rebuild submit api-gateway
```

How the hub finds the branch, and the `--if-not-exists` and
`--skip-branch-check` flags, are described in
[step 5](#5-register-your-patches).

### Removing a patch

```
afc patch remove api-gateway a1b2c3d4-5678-90ab-cdef-1234567890ab
```

The remaining patches close up so positions stay contiguous. The hub forgets
the patch and deletes its backup of a replaced branch, if any; the branch
itself is not deleted -- not on the hub, and not on the fork.

### Reordering patches

Give the complete list of patch ids in the new order:

```
afc patch reorder api-gateway \
  e5f6a7b8-... \
  a1b2c3d4-... \
  c9d0e1f2-...
```

The first id gets position 1, the second position 2, and so on. Every patch
of the workspace must be listed exactly once. To move a single patch, use
`afc patch update <slug> <patch-id> --position <n>` instead.

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

When a sync finds that a patch has been merged upstream, the patch becomes
`merged_upstream` on its own. The next successful rebuild skips it and
soft-deletes it (status `deleted`, `deleted_at` set); it can still be
restored, see [Soft delete and restore](#soft-delete-and-restore).

If you know a patch has been merged but no sync has noticed it yet, mark it
yourself:

```
afc patch update api-gateway <patch-id> --status merged_upstream
```

### Restoring a soft-deleted patch

If a patch was wrongly taken for merged, restore it:

```
afc patch restore api-gateway <patch-id>
```

The patch becomes `active` again and is placed at the end of the list.

### Resolving conflicts after a failed rebuild

Find the conflicting patch and its files on the dashboard:

```
afc workspace patch-status api-gateway
```

Patches with status `conflict` list the affected files in `conflict_files`;
`afc rebuild status api-gateway <rebuild-id>` shows the same per rebuild. What
you do next depends on the authority model.

**In `hub` mode,** fix the branch in a local clone (merge or rebase the
latest upstream into it, resolve, commit) and push the fixed branch to the
hub's git server. Then set the patch back to `active` (a successful rebuild
does not do that for you) and rebuild, if the push has not already queued
one:

```
afc patch update api-gateway <patch-id> --status active
afc rebuild submit api-gateway
```

**In `origin` mode,** do not push to the hub: a hub push is rejected or
forwarded to the fork. Fix the branch in a local clone of the fork the same
way, push it to the fork, and sync:

```
afc workspace sync api-gateway
```

The sync brings the new tip into the hub and queues a rebuild. Then set the
patch back to `active` (a successful rebuild does not do that for you) and,
if it was not applied cleanly yet, rebuild again:

```
afc patch update api-gateway <patch-id> --status active
afc rebuild submit api-gateway
```

**Rerere and manual fixes.** Rerere replays only resolutions recorded in the
hub's own clone. A conflict fixed on a branch outside the
hub (on the fork, or in a local clone pushed to the hub) is not learned, so
if the same conflict appears again it is resolved by hand again. The hub
offers no way to record a resolution outside a rebuild; it only lists and
forgets recorded ones (see [Managing rerere resolutions](#managing-rerere-resolutions)).

### Recovering a replaced or diverged patch branch

In `origin` mode, a sync may replace the hub's copy of a patch branch or
report it as diverged. The dashboard shows each patch's `origin_sync_state`;
the single-patch API (`GET /api/v1/workspaces/:slug/patches/:id`) also returns
`replaced_sha` when a backup exists.

A backup exists only after a replacement (sync action `replaced`) or a
reset to the fork (below) that discarded commits. It is kept under `refs/hub/replaced/<branch>` on the hub; fetch it
into a local clone with:

```
git fetch <hub_url> refs/hub/replaced/<branch>
```

Push the fetched commit somewhere safe (for example, to a new branch on the
fork). There is only one backup per branch; a later replacement overwrites
it.

If `origin_sync_state` is `diverged`, nothing was replaced: under the
`report` policy the hub keeps its own copy and writes no backup.
To move it to the fork's current tip on demand, regardless of the policy:

```
afc patch reset-to-origin api-gateway <patch-id>
```

This works for patches in `active`, `conflict` or `disabled` status. The
response says whether a rebuild was queued (`rebuild_triggered`).

If `origin_sync_state` is `missing_on_origin`, the branch does not exist on
the fork and rebuilds keep applying the hub's copy. Push the branch to the
fork so the next sync picks it up, or remove the patch if it is no longer
needed.

### Recovering from an upstream force-push

Nothing special is needed. A sync notices that upstream rewrote its history,
reports `force_push_detected: true`, and proceeds normally; the rebuild it
queues reapplies the patches on the new upstream base.

The `--reset-to-upstream` flag of `afc workspace sync` is for standard
workspaces, whose sync fails with `409 Diverged` after a force-push on their
remote. Carry-patch workspaces ignore it.

### Cancelling or requeuing a rebuild

A rebuild that is still queued can be cancelled; one that has started or
finished cannot:

```
afc rebuild cancel api-gateway <rebuild-id>
```

A rebuild that failed every retry ends in `dead_letter` status. Requeue it
once the cause is fixed:

```
afc rebuild requeue api-gateway <rebuild-id>
```

### Viewing rebuild history

```
afc rebuild list api-gateway
afc rebuild status api-gateway <rebuild-id>
```

The first lists the workspace's rebuilds; the second shows one with its
per-patch results.

### Managing rerere resolutions

List the recorded conflict resolutions:

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

Git keys rerere entries by the shape of the conflict, not by file, and knows
the file only while a conflict is in progress, so `path` is usually null.
`resolved: false` marks a conflict that was seen but for which no resolution
has been recorded yet.

If a recorded resolution is wrong or out of date, forget it by id; the next
rebuild that meets the conflict stops on it again:

```
afc rerere forget api-gateway 3f2a9c0e...
```

### Pushing other branches to the fork

`afc workspace push <slug> <branch>` pushes one branch of the hub's clone to
the fork, but not the branches carry-patch owns:

- the integration branch is refused with `400 integration_branch` -- it
  reaches the fork through
  [`rebuild_push_integration_branch`](#rebuild_push_integration_branch);
- a registered patch (any status but `deleted`) is refused with
  `400 patch_branch` -- patches reach the fork through
  [`push_patches_to_origin`](#push_patches_to_origin) or by being pushed there
  directly.

Nothing is sent to the fork in either case. Any other branch is pushed as on
a standard workspace.

---

## Settings reference

Workspace settings control carry-patch behavior. Set them at creation
(`afc workspace create --setting key=value`) or later with
`afc workspace update <slug> --setting key=value`; `--setting key=` returns a
setting to its default. The hub validates every value and refuses an unknown
key or a value outside the listed choices with `400`. Every workspace object
shows the effective values under `settings`
(`afc workspace get <slug>`). Changes take effect with the next sync, push or
rebuild; a rebuild that is already queued keeps the strategy and fail mode it
was queued with.

| Setting | Values | Default | Controls |
|---------|--------|---------|----------|
| [`patch_branch_source`](#patch_branch_source) | `hub`, `origin` | `hub` | Who writes patch branches |
| [`patch_divergence_policy`](#patch_divergence_policy) | `replace`, `report` | `replace` | What sync does with a diverged branch (`origin` mode) |
| [`push_patches_to_origin`](#push_patches_to_origin) | `true`, `false` | `false` | Whether hub pushes to patch branches reach the fork |
| [`rebuild_strategy`](#rebuild_strategy) | `rebase`, `merge` | `rebase` | How patches are applied |
| [`rebuild_fail_mode`](#rebuild_fail_mode) | `fail_fast`, `continue` | `fail_fast` | What a conflict does to the rebuild |
| [`rebuild_push_integration_branch`](#rebuild_push_integration_branch) | `true`, `false` | `false` | Whether the integration branch is pushed to the fork |
| [`auto_rebuild_after_sync`](#auto_rebuild_after_sync) | `true`, `false` | `true` | Whether a sync queues a rebuild |
| [`auto_rebuild_after_push`](#auto_rebuild_after_push) | `true`, `false` | `true` | Whether a push to a patch branch queues a rebuild |
| [`squash_merge_detection`](#squash_merge_detection) | `ancestry_only`, `content_based`, `both` | `both` | How merged patches are detected |

The workspace's other settings (`forge_api_url`, `upstream_forge_api_url` and
`sync_enabled`, which when `false` refuses every sync) are described in the
[Workspace Settings Reference](api.md#workspace-settings-reference).

### patch_branch_source

Selects the authority for patch branches.

| Value | Behavior |
|-------|----------|
| `hub` (default) | Patch branches are written by pushes to the hub. Sync never fetches the fork. |
| `origin` | Every sync fetches the fork and brings each registered patch branch to the fork's tip, according to `patch_divergence_policy`. Pushes to registered patch branches on the hub are rejected or forwarded (see `push_patches_to_origin`). |

```
afc workspace update api-gateway --setting patch_branch_source=origin
```

See [Where patch branches live](#where-patch-branches-live).

### patch_divergence_policy

What a sync does when the hub's copy and the fork's copy of a patch branch
have diverged -- that is, the hub's copy has commits the fork's tip does not
contain. Ignored in `hub` mode, where divergence does not arise.

| Value | Behavior |
|-------|----------|
| `replace` (default) | The hub's copy is replaced with the fork's tip; the old tip is kept under `refs/hub/replaced/<branch>`. |
| `report` | The hub's copy is left unchanged and the branch is listed in the sync response's `patches_diverged`. |

```
afc workspace update api-gateway --setting patch_divergence_policy=report
```

### push_patches_to_origin

Lets a push to a registered patch branch on the hub reach the fork.

| Value | Behavior |
|-------|----------|
| `true` | `origin` mode: the push is **forwarded** to the fork before the hub accepts it; if the fork refuses it, the hub refuses it too. `hub` mode: each accepted push is **mirrored** (force-pushed) to the fork, best effort. |
| `false` (default) | `origin` mode: pushes to registered patch branches are rejected with a message naming the fork. `hub` mode: nothing is sent to the fork. |

```
afc workspace update api-gateway --setting push_patches_to_origin=true
```

A failed mirror is logged and recorded as a `hub.patch.mirror_failed` audit
event, but does not fail the push or the rebuild. See
[How a push to a patch branch is handled](#how-a-push-to-a-patch-branch-is-handled).

### rebuild_strategy

How patch branches are applied during a rebuild.

| Value | Behavior |
|-------|----------|
| `rebase` (default) | Cherry-picks the patch branch's own commits onto the integration result. Produces a clean, linear history. |
| `merge` | Merges each patch branch with `--no-ff`. Each patch appears as a merge commit, preserving the branch's own commit structure. |

```
afc workspace update api-gateway --setting rebuild_strategy=merge
```

A single rebuild can override it with `afc rebuild submit --strategy` (or the
`strategy` field of the API request).

### rebuild_fail_mode

What a rebuild does with a conflict rerere cannot resolve.

| Value | Behavior |
|-------|----------|
| `fail_fast` (default) | Stops at the first unresolved conflict; the integration branch is not changed. |
| `continue` | Leaves the conflicting patch out and carries on with the next one. |

```
afc workspace update api-gateway --setting rebuild_fail_mode=continue
```

A single rebuild can override it with `afc rebuild submit --fail-mode` (or
the `fail_mode` field of the API request).

### rebuild_push_integration_branch

When `true`, every successful rebuild force-pushes the integration branch to
the fork with the workspace's fork credentials, so codespaces and CI that
clone the fork see it without an extra step. Off by default because the push
rewrites the branch on the fork.

| Value | Behavior |
|-------|----------|
| `true` | Push after every successful rebuild. The rebuild record shows `integration_branch_pushed: true` when the push succeeded; a failed push is logged and does not fail the rebuild. |
| `false` (default) | The integration branch exists only on the hub. |

```
afc workspace update api-gateway --setting rebuild_push_integration_branch=true
```

### auto_rebuild_after_sync

Whether a sync queues a rebuild when upstream advanced, an `active` or
`conflict` patch branch moved (created, fast-forwarded or replaced), or a
patch was newly found merged.

| Value | Behavior |
|-------|----------|
| `true` (default) | The sync queues a rebuild, unless one is already queued or running. |
| `false` | The sync never queues a rebuild; run `afc rebuild submit` yourself. |

```
afc workspace update api-gateway --setting auto_rebuild_after_sync=false
afc workspace update api-gateway --setting auto_rebuild_after_sync=
```

### auto_rebuild_after_push

Whether a push to the hub that updates a registered patch branch queues a
rebuild.

| Value | Behavior |
|-------|----------|
| `true` (default) | The push queues a rebuild, unless one is already queued or running. The push itself is not delayed. |
| `false` | Pushes never queue a rebuild. |

```
afc workspace update api-gateway --setting auto_rebuild_after_push=false
```

### squash_merge_detection

Which checks a sync uses to decide that a patch has been merged upstream (see
[How merged patches are detected](#how-merged-patches-are-detected)).

| Value | Behavior |
|-------|----------|
| `both` (default) | Ancestry check, content comparison and PR-number scan. |
| `ancestry_only` | Only the ancestry check. Squash merges are not detected. |
| `content_based` | Only the content comparison and the PR-number scan. |

```
afc workspace update api-gateway --setting squash_merge_detection=ancestry_only
```

### Recommended combinations

| Style | Settings | Notes |
|-------|----------|-------|
| fork-first | `patch_branch_source=origin`, optional `rebuild_push_integration_branch=true` | Teams push to GitHub and open upstream PRs; the hub is a rebuild engine |
| fork-first with hub forwarding | `patch_branch_source=origin`, `push_patches_to_origin=true`, optional `rebuild_push_integration_branch=true` | Same, but tooling that only knows the hub URL can push and the hub forwards to the fork |
| hub-only | (defaults) | Agent-driven workspaces; patches live only on the hub |
| hub with a durable mirror | `push_patches_to_origin=true` | Agent-driven, but every accepted push is mirrored so the fork keeps a copy |

---

## How it works

This section describes what the hub does, in the order it does it, as far as
it matters for using carry-patch. The internal design is in
[architecture.md](architecture.md#the-carry-patch-module).

### What a sync does

A carry-patch sync (`afc workspace sync`) runs these steps. A failure in the
first three steps changes nothing.

1. **Fetch upstream.** The hub fetches all branches of the upstream with the
   upstream credentials. A failure answers `502` (`upstream fetch failed`, or
   `failed to resolve upstream credentials`).
2. **Fetch the fork** (`origin` mode only). The hub fetches all branches of
   the fork with the fork credentials, removing branches the fork deleted. A
   failure answers `502 origin fetch failed` (or
   `failed to resolve origin credentials`); the upstream fetch is kept, but
   no patch or sync state is changed.
3. **Find the new base.** The upstream base is the upstream's default branch
   (falling back to the upstream branch named like the workspace branch). If
   the previously recorded base is not an ancestor of the new one, upstream
   rewrote its history and the sync reports `force_push_detected`.
4. **Refresh patch branches** (`origin` mode only). For every registered
   patch in `active`, `conflict` or `disabled` status, the hub compares its
   copy with the fork's: it creates a missing copy, fast-forwards a copy that
   is behind, and replaces (or, under `report`, leaves alone) a copy that has
   diverged. Each patch's origin sync state is recorded. If writing a branch
   fails, the sync stops with `500`; what was already done is kept and a
   rebuild is still queued for branches that moved.
5. **Detect merged patches.** When upstream advanced or a patch branch moved,
   every `active` patch is checked (see
   [How merged patches are detected](#how-merged-patches-are-detected)); a
   merged one becomes `merged_upstream`. A patch never goes back from
   `merged_upstream` on its own.
6. **Record the sync.** `upstream_head_sha` (when upstream advanced) and
   `last_sync_at` are written.
7. **Queue a rebuild** when upstream advanced, an `active` or `conflict`
   patch branch moved, or a patch was newly merged -- unless
   `auto_rebuild_after_sync` is `false`. If a rebuild is already queued or
   running, no second one is queued and `rebuild_triggered` is `false`.

### What a rebuild does

1. **Fetch upstream** and find the base, as a sync does. If the credentials
   or the fetch fail, the rebuild is retried later.
2. **Take the patch tips.** For every patch that will be applied, the hub
   notes the commit its copy of the branch points to now -- whatever the last
   sync (`origin` mode) or push (`hub` mode) left there. The rebuild applies
   exactly these commits, reported as `source_sha` in each patch result,
   even if a branch moves while it runs.
3. **Apply the patches in position order,** starting from the upstream base,
   in a scratch area of its own:
   - `merged_upstream`, `disabled` and `deleted` patches are skipped. A patch
     whose branch does not exist on the hub is skipped with
     `skipped_reason: branch_not_found`; it is not an error.
   - **`rebase`:** the commits on the patch branch that are not already in
     upstream are cherry-picked one by one, oldest first. Merge commits are
     left out, and commits whose change upstream already contains (for
     example after a squash merge) are skipped instead of conflicting.
   - **`merge`:** the patch is merged with `--no-ff` and the message
     `Merge branch '<branch name>'`.
   - **On a conflict,** rerere is asked to replay recorded resolutions. If
     that resolves every file, the patch continues. Otherwise the patch is
     marked `conflict` with its `conflict_files`, and the fail mode decides:
     `fail_fast` ends the rebuild as `failed` without touching the integration
     branch; `continue` drops that patch's changes and goes on with the next.
   - After each patch, its result is published in the rebuild's
     `patch_results`, so a running rebuild shows its progress.
4. **Publish the result.** The integration branch is moved to the result; its
   previous position is recorded as `previous_integration_head_sha` for
   rollback. `merged_upstream` patches are soft-deleted, positions are closed
   up, and, when `rebuild_push_integration_branch` is `true`, the integration
   branch is force-pushed to the fork.
5. **Catch up.** If a patch branch or the upstream base moved while the
   rebuild was running, the hub queues one follow-up rebuild (submitted by
   `system:stale-snapshot`), unless `auto_rebuild_after_push` (for a moved
   branch) or `auto_rebuild_after_sync` (for a moved base) is `false`. This
   check also runs after a `fail_fast` conflict, so a fix pushed during the
   run is not lost.

The integration branch changes only in step 4. Until then, clones and fetches
from the hub see the previous integration branch.

A rebuild that fails for a reason other than a conflict (a network error, a
cancelled run) is retried; after its last attempt it ends in `dead_letter`
(see [Cancelling or requeuing a rebuild](#cancelling-or-requeuing-a-rebuild)).

### How merged patches are detected

During a sync, each `active` patch is checked with up to three signals,
chosen by [`squash_merge_detection`](#squash_merge_detection). Any one is
enough to mark the patch `merged_upstream`. Patches in any other status
(including `conflict`) are not checked; mark them by hand if needed (see
[Handling a merged patch](#handling-a-merged-patch)).

- **Ancestry check:** the patch branch's tip is already part of upstream's
  history. Detects regular merges and fast-forwards.
- **Content comparison:** every commit on the patch branch has an equivalent
  change in upstream (`git cherry` finds none left to apply). Detects squash
  merges and rebases where the commit ids differ.
- **PR-number scan** (only for a patch with `upstream_pr_url`): an upstream
  commit whose subject ends with `(#NNN)` -- GitHub's squash-merge format --
  where `NNN` is the PR number from the URL. Only commits new since the last
  sync are scanned (the last 50 on the first sync). Detects squash merges
  whose content changed before merging.

### How a push to a patch branch is handled

What happens when someone pushes a registered patch branch to the hub's git
server:

| Mode | `push_patches_to_origin` | Result |
|------|--------------------------|--------|
| `origin` | `false` (default) | Rejected: `branch is synced from origin; push to <git_url> instead`. Deleting the branch is rejected too. |
| `origin` | `true` | Forwarded to the fork first; the hub accepts the update only if the fork does. Deleting the branch is rejected without contacting the fork. |
| `hub` | `false` (default) | Accepted. |
| `hub` | `true` | Accepted, then force-pushed (mirrored) to the fork, best effort. The mirror runs even when `auto_rebuild_after_push` is `false`. |

When the hub accepts an update to a registered patch branch, it queues a
rebuild unless `auto_rebuild_after_push` is `false` or a rebuild is already
queued or running. This happens after the push has completed and never fails
it. Rejected updates and failed forwards queue nothing.

### How rerere replays resolutions

Rerere is enabled when the workspace is cloned and again before every
rebuild. When a cherry-pick or merge in a rebuild conflicts:

1. Git looks up the conflict in the workspace's rerere cache.
2. If a resolution was recorded for it, git applies it and stages the files.
3. If no conflicted file is left, the rebuild continues. Otherwise the fail
   mode decides.

The more conflicts are resolved in the hub's clone, the more of them later
rebuilds resolve on their own. Resolutions made outside the hub are not
learned (see [Resolving conflicts after a failed rebuild](#resolving-conflicts-after-a-failed-rebuild)).

### What a preview does

`afc rebuild preview` predicts each patch's outcome with
`git merge-tree --write-tree`, which computes a merge without touching any
branch or working files. Patches are tried in position order, each on top of
the result of the earlier ones that would succeed; `disabled` and
`merged_upstream` patches are skipped. Nothing is changed: no branch, no
patch status. Use it to check a new patch before rebuilding, to spot
cascading conflicts, or as a CI gate.

### What a rollback restores

`afc rebuild rollback <slug> <rebuild-id>` moves the integration branch on the
hub back to that rebuild's `previous_integration_head_sha`. That is all it
does:

- patch statuses, positions and soft-deleted patches stay as they are;
- the fork is not changed, even with `rebuild_push_integration_branch=true`;
- the next rebuild (including one queued by a sync or push) replaces the
  integration branch again.

It is refused with `409` for a rebuild that has no previous position, such as
a workspace's first rebuild or a rebuild that did not complete.

### Soft delete and restore

When a successful rebuild skips a `merged_upstream` patch, it soft-deletes it:
the status becomes `deleted` and `deleted_at` is set. Soft-deleted patches
are hidden from `afc patch list` and the dashboard, and
`afc patch restore` brings one back as `active` at the end of the list.

Soft-deleted patches older than 7 days are eligible to be purged for good,
but the hub runs no purge today -- no schedule, command or API route
triggers one -- so a soft-deleted patch stays restorable.

This is a safety net: a patch wrongly taken for merged can be recovered
without re-creating it.

### What can run alongside a rebuild

A workspace runs one rebuild at a time: submitting another while one is
queued or running answers `409`.

Operations that change the hub's clone of a workspace -- sync, rebuild,
merge and rebase jobs, rollback, rerere forget, git pushes -- take turns. An
API call that finds the workspace busy answers `409 workspace_busy` instead of
waiting; retry it. A rebuild keeps the workspace busy only briefly, while it
fetches upstream at the start and while it publishes at the end. While it
applies patches, syncs, pushes and the other operations proceed normally.
Archiving and recloning the workspace are refused with `409 workspace_busy`
for the whole rebuild.

---

## Limitations

**Immutable workspace fields.** `workspace_mode`, `upstream_url` and
`integration_branch` are set at creation and cannot be changed. To switch
between `standard` and `carry_patch`, create a new workspace.

**Integration branch position after creation.** At creation the hub fetches
the upstream and creates the integration branch at the upstream base. If that
fetch fails (for example because upstream credentials are added later), the
branch starts at the fork's HEAD and is positioned correctly only after the
first sync and rebuild.

**Setup problems at creation do not fail the workspace.** If adding the
`upstream` remote, enabling rerere or creating the integration branch fails
during the clone, the hub logs a warning and the workspace still becomes
`ready`. Check the hub's logs if the first sync or rebuild fails right away.

**A rebuild needs a patch to apply.** A rebuild is refused with
`400 no_active_patches` when no patch is `active` or `conflict`.

**One rebuild at a time.** A second rebuild while one is queued or running
answers `409`; wait for it, or cancel it while it is still queued. See
[What can run alongside a rebuild](#what-can-run-alongside-a-rebuild).

**The first rebuild cannot be rolled back.** There is no earlier integration
branch to return to.

**Rerere counts are per workspace.** The dashboard's
`total_rerere_resolutions` counts every recorded resolution of the workspace;
there is no per-patch count.

**No fork webhooks and no built-in sync schedule.** The hub does not react to
pushes on the fork. Sync runs when something calls it: `afc workspace sync`,
your own scheduler, or any client of `POST /api/v1/workspaces/:slug/sync`.

**Sync fetches every fork branch.** In `origin` mode, the fork fetch
retrieves all of the fork's branches, not only the registered patch branches.
A fork with many branches makes a larger fetch.

**Resolutions made outside the hub are not learned by rerere.** A conflict
fixed on the fork (or in a local clone) does not reach the hub's rerere
cache; the same conflict is resolved by hand again unless a resolution
recorded earlier in the hub covers it.

**`missing_on_origin` does not disable the patch.** When a registered patch
branch is absent from the fork, rebuilds keep applying the hub's copy until
an operator acts.

**Origin mode needs working fork credentials on every sync.** If the fork
credentials expire or are revoked, every sync answers `502 origin fetch
failed` and no patch state is updated. (`502 failed to resolve origin
credentials` means the hub could not read the stored credentials at all.)

---

## API and CLI reference

Every request, response and flag is described in the reference documents:

| Area | API | CLI | Token scopes |
|------|-----|-----|--------------|
| Patches | [Patch Endpoints](api.md#patch-endpoints) | [Patch Commands](cli.md#patch-commands) | `patches:read`, `patches:write` |
| Rebuilds and preview | [Rebuild Endpoints](api.md#rebuild-endpoints) | [Rebuild Commands](cli.md#rebuild-commands) | `rebuilds:read`, `rebuilds:write` |
| Rerere | [Rerere Management Endpoints](api.md#rerere-management-endpoints) | [Rerere Commands](cli.md#rerere-commands) | `workspaces:read` (list), `workspaces:write` (forget) |
| Status dashboard | [Patch Status Dashboard Endpoint](api.md#patch-status-dashboard-endpoint) | [afc workspace patch-status](cli.md#afc-workspace-patch-status) | `workspaces:read` |
| Sync | [POST /api/v1/workspaces/:slug/sync](api.md#post-apiv1workspacesslugsync) | [afc workspace sync](cli.md#afc-workspace-sync) | `workspaces:write` |
| Settings | [Workspace Settings Reference](api.md#workspace-settings-reference) | [afc workspace update](cli.md#afc-workspace-update) | `workspaces:write` |

Scopes are explained in the [Permissions Reference](permissions.md).
