# Agent Instructions

Instructions for coding agents (Cursor, Claude Code, Codex, etc.) working on
this repository. Treat this file as mandatory policy for every coding session.

## Project Context (MANDATORY -- Read First)

This repository is a **carry-patch fork** managed by af-hub. It is NOT a
standalone project. The repository maintains a set of patch branches on top of
an upstream codebase, and the hub mechanically rebuilds an integration branch
by replaying those patches onto the latest upstream HEAD.

Key concepts:

- **Upstream:** The canonical repository at `<upstream-repo-url>`. We do not
  commit to upstream directly. All upstream interaction happens through the
  hub's sync and rebuild mechanisms.
- **Fork:** Our copy at `<fork-repo-url>`, configured as the git remote
  `origin` of the workspace trunk.
- **Patch branches:** Named `patch/<descriptive-name>`. Each branch contains
  our changes to upstream. Patches are ordered by position and replayed
  sequentially during rebuild. Depending on the workspace's
  `PATCH_BRANCH_SOURCE` setting, patch branches are written either by pushes
  to the hub's git server (`hub` mode, the default) or by pushes to the fork
  (`origin` mode). See "Understand Before You Code" to determine which mode
  applies.
- **Integration branch:** `<integration-branch>` (typically `deploy`). This
  branch is the primary consumable artifact. It is rebuilt automatically by the
  hub -- never commit to it directly.
- **Hub git server:** The hub exposes the fork at
  `<hub-url>/git/<org-slug>/<workspace-slug>.git`. In `hub` mode, clone from
  here and push patch branches here. In `origin` mode, push patch branches to
  the fork instead; the hub fetches them during sync.

**Important:** The integration branch is machine-generated. If you need to
change what it contains, modify a patch branch and trigger a rebuild.

Do not implement anything before reading this entire file.

## Hub Connection

### Authentication

Log in to the hub before any other `afc` commands:

```
afc login
```

This opens a browser for OAuth authentication, obtains an API key, and stores
it in `~/.af/config.toml`. You only need to do this once per machine.

### Git Credential Helper

Configure git to authenticate with the hub's git server automatically:

```
git config --global credential.<hub-url>.helper '!afc credential-helper'
```

After this, `git clone`, `git push`, and `git pull` against the hub URL work
without manual token entry.

### Clone the Repository

```
git clone <hub-url>/git/<org-slug>/<workspace-slug>.git
cd <workspace-slug>
```

The clone comes from the hub, not from upstream. The hub manages both remotes
internally (`origin` = fork, `upstream` = canonical repo).

## Understand Before You Code (MANDATORY)

Before making any changes, orient yourself:

1. **Read `PATCH_BRANCH_SOURCE`** to determine the authority model:
   ```
   afc vars list --workspace <workspace-slug>
   ```
   Look for `PATCH_BRANCH_SOURCE`. If it is `origin`, patch branches live on
   the fork and the hub fetches them during sync. If it is unset or `hub`,
   patch branches live on the hub's git server and you push them there
   directly. This determines where you push, how you bring changes in, and
   how you resolve conflicts. Read the mode-specific instructions in each
   section below.
2. **Read `README.md`** for project overview.
3. **Check the patch stack:**
   ```
   afc patch list <workspace-slug>
   ```
   Understand what patches exist, their order, and their statuses.
4. **Check integration health:**
   ```
   afc workspace patch-status <workspace-slug>
   ```
   Review the last rebuild result, per-patch status, and conflict state.
5. **Check git state:** `git log --oneline -20`, `git status --short --branch`.
6. **Read existing patch branches** to understand what has already been
   customized. Run `git branch -r` to see all remote patch branches.
7. **Read project-specific docs** in `docs/` if they exist.

**Important:** Read all documents and code in depth -- do not skim.

Do not implement anything before completing these steps.

## Implementing Changes

Follow these steps in order. Do not skip steps.

### Step 1: Decide Where Your Change Belongs

- **New customization to upstream:** Create a new patch branch.
- **Modification to an existing patch:** Check out the existing patch branch.
- **Fix to upstream that should go upstream eventually:** Still create a patch
  branch, but note the upstream PR URL when registering the patch.

**`origin` mode:** You will work in a local clone of the fork and push to the
fork (GitHub). The hub fetches the branch during sync.

**`hub` mode:** You will push the patch branch to the hub's git server. The
hub accepts it directly.

### Step 2: Create or Check Out the Patch Branch

For a new patch:

```
git checkout -b patch/<descriptive-name>
```

For an existing patch:

```
git checkout patch/<existing-name>
git pull origin patch/<existing-name>
```

### Step 3: Implement and Commit

Make your changes. Follow project-specific coding conventions. Commit with
conventional commit messages:

```
git add <files>
git commit -m "feat: add custom authentication middleware"
```

**Commit conventions:**
- Use `<type>: <description>` (e.g. `feat:`, `fix:`, `refactor:`, `chore:`).
- Only commit files relevant to the current change.
- Each patch branch should represent one coherent customization. Do not bundle
  unrelated changes into a single patch.

### Step 4: Push the Patch Branch

**`origin` mode:** Push to the fork. The hub never accepts pushes to
registered patch branches in `origin` mode -- it refuses or replaces them.
To push, clone the fork if you have not already, and push there:

```
git push origin patch/<descriptive-name>
```

This pushes to the fork (GitHub). Open the upstream PR from the same branch
if the patch is intended to be upstreamed.

**`hub` mode:** Push to the hub's git server:

```
git push origin patch/<descriptive-name>
```

This pushes to the hub's git server. The credential helper handles
authentication. Never push to the upstream remote.

If `PUSH_PATCHES_TO_ORIGIN=true` is set, the hub automatically mirrors
accepted pushes to the fork. This is a mirror the agent does not need to do
by hand.

### Step 5: Register the Patch (New Patches Only)

If this is a new patch branch, register it with the hub:

```
afc patch add <workspace-slug> \
  --branch patch/<descriptive-name> \
  --description "Short description of what this patch does"
```

Optional flags:

- `--position <int>` -- Insert at a specific position in the patch order
  (1-based). If omitted, the patch is appended at the end.
- `--upstream-pr <url>` -- Link to an upstream pull request if this patch is
  intended to be upstreamed eventually.
- `--skip-branch-check` -- Skip branch existence validation. Useful when
  registering a patch before the branch has been pushed anywhere.
- `--if-not-exists` -- Return the existing patch instead of an error if the
  branch is already registered. Idempotent registration for automation.

**`origin` mode:** The hub resolves the branch by checking `refs/heads/<name>`
locally, then `refs/remotes/origin/<name>`, then fetching the single branch
from the fork. A fork-only branch no longer needs `--skip-branch-check`.

**`hub` mode:** The hub checks `refs/heads/<name>` locally, then
`refs/remotes/origin/<name>`. Use `--skip-branch-check` only for branches
that exist nowhere yet.

**Important:** The branch name must not match the integration branch name.

### Step 6: Trigger a Rebuild

**`origin` mode:** Run `afc workspace sync` to bring the fork's branch tips
into the hub and trigger a rebuild. A rebuild reads `refs/heads/<branch>` in
the hub's trunk and does not fetch the fork, so `afc workspace sync` -- not
`afc rebuild submit` -- is what brings new fork commits into the hub:

```
afc workspace sync <workspace-slug>
```

**`hub` mode:** The push hook triggers a rebuild automatically (controlled by
`AUTO_REBUILD_AFTER_PUSH`). You can also trigger one manually:

```
afc rebuild submit <workspace-slug>
```

Optional flags (for `afc rebuild submit`):

- `--strategy <rebase|merge>` -- Override the workspace-level rebuild strategy
  for this specific rebuild.
- `--fail-mode <fail_fast|continue>` -- Control conflict handling. `fail_fast`
  (default) stops at the first conflict. `continue` skips conflicting patches
  and processes the rest.
- `--wait` -- Block until the rebuild reaches a terminal state (completed,
  failed, dead_letter, or cancelled).
- `--timeout <duration>` -- Maximum time to wait (default: 5m). Only effective
  with `--wait`.
- `--poll-interval <duration>` -- Interval between status polls (default: 5s).
  Only effective with `--wait`.

### Step 7: Monitor Rebuild Status

```
afc rebuild status <workspace-slug> <rebuild-id>
```

Or check the full dashboard:

```
afc workspace patch-status <workspace-slug>
```

The rebuild is complete when the job status is `completed`. If it is `failed`,
see the Conflict Resolution section below.

For running rebuilds, the status response includes intermediate `patch_results`
showing which patches have been processed so far.

### Step 8: Preview Before Rebuilding (Optional)

Before committing to a rebuild, you can preview which patches would conflict:

```
afc rebuild preview <workspace-slug>
```

This performs a read-only conflict prediction for each active patch without
modifying any git refs or patch statuses. Each patch is reported as
`would_succeed` or `would_conflict` (with the list of conflicting files).

### Step 9: Verify the Integration Branch

After a successful rebuild, verify the integration branch builds and tests
pass:

```
git fetch origin
git checkout <integration-branch>
git pull origin <integration-branch>
```

Run the project's test suite against the integration branch. The integration
branch is the artifact that gets deployed -- it must always be in a working
state.

## Upstream Sync

The hub syncs with upstream when you trigger it:

```
afc workspace sync <workspace-slug>
```

Optional flags:

- `--wait` -- Block until any auto-triggered rebuild completes.
- `--timeout <duration>` -- Maximum time to wait (default: 5m). Only effective
  with `--wait`.
- `--poll-interval <duration>` -- Interval between status polls (default: 5s).
  Only effective with `--wait`.
- `--fail-on-diverged` -- Exit with code 3 when `patches_diverged` is
  non-empty (`origin` mode with `PATCH_DIVERGENCE_POLICY=report`).

### What Happens During Sync

1. The hub fetches the latest commits from the upstream remote.
2. **`origin` mode only:** The hub fetches the fork (`origin`) with pruning,
   bringing registered patch branches to the fork's tips. Patch branches may
   be `created`, `fast_forwarded` or `replaced` (with the previous tip backed
   up under `refs/hub/replaced/<branch>`). Per-patch `origin_sync_state` is
   set to `in_sync`, `diverged` or `missing_on_origin`.
3. For each active patch, the hub checks whether the patch branch HEAD is now
   an ancestor of the new upstream HEAD (i.e., the patch was merged upstream).
4. Patches detected as merged are automatically transitioned to
   `merged_upstream` status.
5. By default, a rebuild is triggered automatically after sync when upstream
   advanced, a patch tip moved, or a patch was newly merged. This behavior
   is controlled by the `AUTO_REBUILD_AFTER_SYNC` workspace variable (enabled
   by default; set to `"false"` to disable).

**`hub` mode:** Sync fetches only upstream. Patch branches are not fetched
from the fork.

**`origin` mode:** Sync fetches both upstream and the fork. This is the only
way new fork commits reach the hub, because a rebuild reads the hub's copy of
each branch and does not fetch the fork.

### After Sync

Check the dashboard:

```
afc workspace patch-status <workspace-slug>
```

- Patches marked `merged_upstream` will be skipped during rebuild and then
  **soft-deleted** on the next successful rebuild. Soft-deleted patches are
  hidden from the patch-status dashboard but retained in the database.
- To restore a soft-deleted patch: `afc patch restore <workspace-slug> <patch-id>`.
- If the sync introduced upstream changes that conflict with existing patches,
  the auto-rebuild will fail and the conflicting patch will be marked
  `conflict`. See Conflict Resolution below.
- **`origin` mode:** Check `origin_sync_state` for each patch. A `diverged`
  patch means the hub replaced the branch tip with the fork's tip. A
  `missing_on_origin` patch means the branch was not found on the fork; the
  hub keeps applying its own copy until an operator disables or removes the
  patch.

### Recovery After Upstream Force-Push

If upstream force-pushed (rewrote history), use the reset flag:

```
afc workspace sync <workspace-slug> --reset-to-upstream
```

This force-resets the local tracking of upstream HEAD. Follow with a rebuild.

## Conflict Resolution

By default (fail mode `fail_fast`), rebuilds stop at the **first** patch that
produces an unresolved conflict. All subsequent patches are not attempted.

With fail mode `continue`, the rebuild skips conflicting patches and continues
processing the remaining patches. The conflicting patches are marked with
status `conflict` but the rebuild still completes (with those patches missing
from the integration branch).

### Step 1: Identify the Conflict

```
afc workspace patch-status <workspace-slug>
```

Look for patches with status `conflict`. The `conflict_files` field lists the
affected files.

For detailed conflict information from a specific rebuild:

```
afc rebuild status <workspace-slug> <rebuild-id>
```

The `patch_results` array shows per-patch outcomes including `conflict_files`
for any patches that conflicted.

### Step 2: Fix the Patch Branch

**`hub` mode:** Resolve in the workspace trunk or by pushing a fixed branch to
the hub's git server. Rerere records the resolution when the resolution
happens during a rebuild.

```
git checkout patch/<conflicting-patch>
```

Examine the conflict files, make the necessary changes to resolve the
conflict, commit, and push:

```
git add <fixed-files>
git commit -m "fix: resolve conflict with upstream changes in <area>"
git push origin patch/<conflicting-patch>
```

**`origin` mode:** Fix the branch in a local clone of the fork. Merge or
rebase the latest upstream into the patch branch, resolve conflicts, commit,
and push to the fork. Then run `afc workspace sync` to bring the new tip into
the hub and trigger a rebuild. Do not edit the trunk or push to the hub -- a
hub push is rejected or forwarded in `origin` mode, and a trunk edit would be
overwritten at the next sync.

```
git checkout patch/<conflicting-patch>
git merge upstream/main   # or rebase
# resolve conflicts
git add <fixed-files>
git commit -m "fix: resolve conflict with upstream changes in <area>"
git push origin patch/<conflicting-patch>
afc workspace sync <workspace-slug>
```

**Limitation (`origin` mode):** A resolution made on the fork is not recorded
in the hub's rerere cache, so the same conflict may need to be resolved by
hand again unless the rebuild itself resolves it. This is a known limitation,
not a planned feature.

### Step 3: Rebuild Again

**`hub` mode:**

```
afc rebuild submit <workspace-slug>
```

**`origin` mode:** The sync in Step 2 already triggers a rebuild. If the
patch status is still `conflict` after the rebuild, set it to `active` and
rebuild:

```
afc patch update <workspace-slug> <patch-id> --status active
afc rebuild submit <workspace-slug>
```

### Step 4: Verify

```
afc workspace patch-status <workspace-slug>
```

Confirm all patches show `active` status and `last_rebuild_result` is
`success`.

### Rerere (Reuse Recorded Resolution)

The hub records conflict resolutions automatically via git's rerere mechanism.
When the same conflict recurs in a future rebuild, rerere replays the recorded
resolution without manual intervention.

List recorded resolutions:

```
afc rerere list <workspace-slug>
```

If a recorded resolution is producing wrong merge results (e.g., the
resolution was incorrect or the context has changed), clear it:

```
afc rerere forget <workspace-slug> <pathspec>
```

After forgetting a resolution, the next rebuild will stop at that conflict
again, allowing you to resolve it correctly.

## Rollback

If a rebuild produced a bad integration branch, you can roll back to the
previous state:

```
afc rebuild rollback <workspace-slug> <rebuild-id>
```

This resets the integration branch to the HEAD SHA it had before the specified
rebuild. Rollback is not available for the very first rebuild (there is no
previous state). After rolling back, fix the problematic patch and trigger a
new rebuild.

## Patch Management

### Listing Patches

```
afc patch list <workspace-slug>
```

Patches are returned ordered by position (ascending). Position determines
replay order during rebuild.

### Reordering Patches

Patch order matters. Patches are replayed sequentially, and a patch that
depends on changes from another patch must come after it.

```
afc patch reorder <workspace-slug> <patch-id-1> <patch-id-2> <patch-id-3>
```

You must list ALL patch IDs in the desired order. Trigger a rebuild after
reordering.

### Updating Patch Metadata

```
afc patch update <workspace-slug> <patch-id> \
  --description "Updated description" \
  --upstream-pr "https://github.com/upstream/repo/pull/42"
```

You can also change status or position:

```
afc patch update <workspace-slug> <patch-id> --status disabled
afc patch update <workspace-slug> <patch-id> --position 3
```

Valid statuses: `active`, `merged_upstream`, `conflict`, `disabled`.

- `disabled` patches are skipped during rebuild but retained in the database.
- `merged_upstream` patches are skipped during rebuild and soft-deleted after a
  successful rebuild.

### Restoring a Soft-Deleted Patch

Patches that were soft-deleted (e.g., after being merged upstream) can be
restored:

```
afc patch restore <workspace-slug> <patch-id>
```

This transitions the patch back to `active` status. Trigger a rebuild after
restoring.

### Removing a Patch

```
afc patch remove <workspace-slug> <patch-id>
```

This deletes the patch registration. Remaining patch positions are compacted
automatically. The patch branch itself is not deleted from git -- only the
hub's tracking record is removed. The backup ref `refs/hub/replaced/<branch>`
is also deleted with the patch row, if one exists.

**`origin` mode:** The branch on the fork is not deleted. Only the hub's
registration and backup ref are removed.

**`hub` mode:** The branch on the hub's git server is not deleted. If
`PUSH_PATCHES_TO_ORIGIN=true`, the mirrored copy on the fork is also not
deleted.

Trigger a rebuild after removal.

## Conventions

### Branch Naming

- Patch branches: `patch/<descriptive-name>` (e.g., `patch/custom-auth-middleware`,
  `patch/increase-rate-limits`, `patch/fix-logging-format`).
- Never use branch names that match the integration branch.
- Do not create a branch named `_rebuild_temp`. (Legacy: older hub versions
  used it as a temporary rebuild branch; current versions rebuild in a
  detached git worktree and delete a leftover legacy `_rebuild_temp` branch.)

### Commit Messages

Use conventional commits: `<type>: <description>`.

- `feat:` -- new functionality added by the patch
- `fix:` -- bug fix within a patch
- `refactor:` -- restructuring patch code without changing behavior
- `chore:` -- maintenance (dependency updates, config changes)

### Patch Descriptions

When registering a patch, write a description that explains **why** the patch
exists, not just what it changes. Future agents and operators need to
understand the intent.

Good: `"Override default session timeout to 4 hours for compliance with internal security policy"`
Bad: `"Change timeout value"`

### Patch Ordering

- Independent patches can be in any order.
- If patch B depends on changes introduced by patch A, patch A must have a
  lower position number.
- Place patches that are likely to be upstreamed soon at the end of the stack
  to minimize reorder churn when they are removed.

## Rebuild Strategy

The hub supports two rebuild strategies, controlled by the `REBUILD_STRATEGY`
workspace variable:

| Strategy | Behavior | When to use |
|----------|----------|-------------|
| `rebase` (default) | Cherry-picks individual commits from each patch branch onto upstream HEAD | Produces a linear history on the integration branch; preferred for most workflows |
| `merge` | Merges each patch branch with `--no-ff` | Preserves patch branch merge boundaries; useful when patches have complex internal history |

The strategy is set at the workspace level and can be overridden per-rebuild
with `afc rebuild submit --strategy <rebase|merge>`.

## Rebuild Fail Mode

The hub supports two fail modes for handling conflicts during rebuild,
controlled by the `REBUILD_FAIL_MODE` workspace variable:

| Fail Mode | Behavior | When to use |
|-----------|----------|-------------|
| `fail_fast` (default) | Stops at the first conflict; no integration branch update | When all patches must apply cleanly |
| `continue` | Skips conflicting patches and continues; updates the integration branch with only the successful patches | When partial integration is acceptable while conflicts are being resolved |

The fail mode is set at the workspace level and can be overridden per-rebuild
with `afc rebuild submit --fail-mode <fail_fast|continue>`.

## Commands Reference

| Task | Command |
|------|---------|
| Log in to hub | `afc login` |
| Check authority model | `afc vars list --workspace <workspace-slug>` |
| Check patch stack | `afc patch list <workspace-slug>` |
| Check integration health | `afc workspace patch-status <workspace-slug>` |
| Add a new patch | `afc patch add <workspace-slug> --branch <name> --description "<text>"` |
| Update patch metadata | `afc patch update <workspace-slug> <patch-id> [--status <s>] [--description <text>] [--position <n>]` |
| Remove a patch | `afc patch remove <workspace-slug> <patch-id>` |
| Restore a soft-deleted patch | `afc patch restore <workspace-slug> <patch-id>` |
| Reorder all patches | `afc patch reorder <workspace-slug> <id1> <id2> ...` |
| Trigger rebuild | `afc rebuild submit <workspace-slug> [--strategy <s>] [--fail-mode <m>] [--wait]` |
| Preview rebuild conflicts | `afc rebuild preview <workspace-slug>` |
| Check rebuild status | `afc rebuild status <workspace-slug> <rebuild-id>` |
| List recent rebuilds | `afc rebuild list <workspace-slug>` |
| Cancel a queued rebuild | `afc rebuild cancel <workspace-slug> <rebuild-id>` |
| Requeue a dead-lettered rebuild | `afc rebuild requeue <workspace-slug> <rebuild-id>` |
| Roll back a rebuild | `afc rebuild rollback <workspace-slug> <rebuild-id>` |
| Sync with upstream (and origin in `origin` mode) | `afc workspace sync <workspace-slug> [--wait]` |
| Sync and fail on diverged patches | `afc workspace sync --fail-on-diverged <workspace-slug>` |
| Reset to upstream (recovery) | `afc workspace sync <workspace-slug> --reset-to-upstream` |
| Reset a diverged patch to origin's tip | `afc patch reset-to-origin <workspace-slug> <patch-id>` |
| List rerere resolutions | `afc rerere list <workspace-slug>` |
| Forget a rerere resolution | `afc rerere forget <workspace-slug> <pathspec>` |
| Set upstream credentials | `afc secrets create UPSTREAM_GIT_PAT=<token> --workspace <workspace-slug>` (reserved secret name) |

**`origin` mode:** Use `afc workspace sync` to bring fork commits into the
hub and trigger a rebuild. Use `afc patch reset-to-origin` to move a diverged
or replaced patch branch to the fork's tip on demand.

**`hub` mode:** Use `afc rebuild submit` to trigger a rebuild after pushing
to the hub. If `PUSH_PATCHES_TO_ORIGIN=true`, accepted pushes are
automatically mirrored to the fork.

## Quality Gates

### Before Considering Work Done

A session is not complete until all of the following are true:

1. Your changes are committed and pushed to the correct target:
   - **`origin` mode:** pushed to the fork, then `afc workspace sync` run.
   - **`hub` mode:** pushed to the hub's git server.
2. The patch is registered (if new): `afc patch list <workspace-slug>` shows it.
3. A rebuild has been triggered and completed successfully:
   `afc workspace patch-status <workspace-slug>` shows all active patches with
   `last_rebuild_result: success`.
4. The integration branch builds and passes tests. Check out the integration
   branch and run the project's test suite.
5. `git status` shows a clean working tree.
6. You provide a brief handoff note summarizing what was done and what remains.

### What NOT to Do

- **Never commit to the integration branch.** It is rebuilt from scratch by
  the hub. Any direct commits will be overwritten.
- **Never push to the upstream remote.** All upstream interaction is managed
  by the hub.
- **Never push a registered patch branch to the hub in `origin` mode.** The
  hub refuses or replaces it. Push to the fork and run `afc workspace sync`.
- **Never touch anything under the hub's `rebuild/` worktree directory.**
  A rebuild applies patches in a per-run detached worktree there and removes
  it when the run ends. (Legacy: older hub versions used a transient
  `_rebuild_temp` branch for this.)
- **Never run `afc rebuild submit` if a rebuild is already running.** The hub
  returns 409 Conflict in this case. Wait for the current rebuild to finish,
  or cancel it first with `afc rebuild cancel`.

## Scope Discipline

- Focus on one patch per session. Do not modify multiple unrelated patches.
- Do not include "while here" fixes that belong in a different patch.
- Priority: fix conflict-status patches before adding new patches.
- If a patch is no longer needed (merged upstream or obsolete), remove it
  before adding new ones to keep the stack clean.
