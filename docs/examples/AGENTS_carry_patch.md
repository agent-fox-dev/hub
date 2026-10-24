# Agent Instructions

> **About this file (for the human reader -- delete this box after copying).**
> This is an example instruction file for coding agents (Claude Code, Cursor,
> Codex, ...) that work in a fork maintained with the hub's
> [carry-patch workflow](../carry_patch_workflow.md). It tells an agent that
> the fork is a stack of patch branches on top of an upstream, that the
> integration branch is built by the hub, and which `afc` commands to use.
>
> To use it:
>
> 1. Copy it to the root of your fork as `AGENTS.md` (or `CLAUDE.md`, or
>    whatever file your agent reads).
> 2. Replace every placeholder:
>
>    | Placeholder | Replace with | Example |
>    |-------------|--------------|---------|
>    | `<hub-url>` | The hub's base URL | `https://hub.example.com` |
>    | `<org-slug>` | The organization that owns the workspace | `acme` |
>    | `<workspace-slug>` | The carry-patch workspace's slug | `api-gateway` |
>    | `<fork-repo-url>` | Your fork's clone URL | `https://github.com/acme/api-gateway-fork.git` |
>    | `<fork-repo-name>` | The directory a clone of the fork creates | `api-gateway-fork` |
>    | `<upstream-repo-url>` | The upstream's clone URL | `https://github.com/acme-oss/api-gateway.git` |
>    | `<upstream-default-branch>` | The upstream's default branch | `main` |
>    | `<integration-branch>` | The workspace's integration branch | `deploy` |
>
> 3. Adjust the conventions (branch prefix `patch/`, commit style, test
>    command) to your project, and delete the sections about the authority
>    mode you do not use if you want a shorter file.
>
> The agent needs `afc` and an API key for the hub; a personal access token
> with `patches:write`, `rebuilds:write`, `workspaces:write` and `git:write`
> covers everything below (see the [Permissions Reference](../permissions.md)).

Instructions for coding agents working on this repository. Treat this file as
mandatory policy for every coding session.

## Contents

- [Project Context](#project-context)
- [Hub Connection](#hub-connection)
- [Understand Before You Code](#understand-before-you-code)
- [Implementing Changes](#implementing-changes)
- [Upstream Sync](#upstream-sync)
- [Conflict Resolution](#conflict-resolution)
- [Rollback](#rollback)
- [Patch Management](#patch-management)
- [Rebuild Strategy and Fail Mode](#rebuild-strategy-and-fail-mode)
- [Conventions](#conventions)
- [Commands Reference](#commands-reference)
- [Quality Gates](#quality-gates)
- [Scope Discipline](#scope-discipline)

## Project Context

**Read this section first.**

This repository is a **carry-patch fork** managed by the hub. It is NOT a
standalone project. It holds a set of patch branches on top of an upstream
codebase, and the hub rebuilds an integration branch by applying those patches,
in order, onto the latest upstream.

- **Upstream:** the canonical repository at `<upstream-repo-url>`. Never push
  to it. The hub fetches it during sync and rebuild.
- **Fork:** our copy at `<fork-repo-url>`.
- **Patch branches:** named `patch/<descriptive-name>`. Each one holds one
  coherent change to upstream. Patches have positions and are applied in that
  order.
- **Integration branch:** `<integration-branch>`. This is what gets deployed.
  The hub rebuilds it from upstream plus the patches -- **never commit to
  it.** To change what it contains, change a patch branch and rebuild.
- **Hub git server:** the hub serves the workspace at
  `<hub-url>/git/<org-slug>/<workspace-slug>.git`.
- **Authority mode:** the workspace setting `patch_branch_source` decides
  where patch branches are written:
  - **`hub` mode** (the default): push patch branches to the hub's git server.
  - **`origin` mode:** push patch branches to the fork; the hub copies them
    in when it syncs. The hub refuses pushes of patch branches.

  Every step below that differs between the modes says so. Find out which
  mode applies before doing anything (see
  [Understand Before You Code](#understand-before-you-code)).

Do not implement anything before reading this entire file.

## Hub Connection

### Authentication

Sign in to the hub once per machine:

```
afc login
```

This stores the hub URL and an API key in `~/.af/config.toml`. In a container
or CI job, set the `ENDPOINT_URL` and `API_KEY` environment variables instead.

### Git Credential Helper

Let git authenticate to the hub with the same key:

```
git config --global credential.<hub-url>.helper '!afc credential-helper'
```

After this, `git clone`, `git fetch` and `git push` against the hub work
without a token in the URL.

### Clone the Repository

**`hub` mode:** clone from the hub. In this clone, `origin` is the hub.

```
git clone <hub-url>/git/<org-slug>/<workspace-slug>.git
cd <workspace-slug>
```

**`origin` mode:** clone the fork. In this clone, `origin` is the fork.

```
git clone <fork-repo-url>
cd <fork-repo-name>
```

In both modes, add the upstream as a read-only remote, so you can bring
upstream changes into a patch branch:

```
git remote add upstream <upstream-repo-url>
```

Every `origin` in the commands below means the `origin` of the clone you work
in for your mode.

## Understand Before You Code

**Mandatory.** Before making any changes, orient yourself:

1. **Find the authority mode:**
   ```
   afc workspace get <workspace-slug>
   ```
   Read `settings.patch_branch_source`: `origin` means patch branches live on
   the fork; `hub` means they live on the hub's git server. This decides where
   you push, how changes reach the hub, and how you resolve conflicts. Note
   `settings.push_patches_to_origin` and
   `settings.rebuild_push_integration_branch` too; later steps refer to them.
2. **Read `README.md`** for the project overview.
3. **Check the patch stack:**
   ```
   afc patch list <workspace-slug>
   ```
   Understand which patches exist, their order and their statuses.
4. **Check integration health:**
   ```
   afc workspace patch-status <workspace-slug>
   ```
   Review the last rebuild, each patch's status and result, and any conflicts.
5. **Check git state:** `git log --oneline -20`, `git status --short --branch`.
6. **Read the existing patch branches** (`git branch -r`) to understand what
   has already been customized.
7. **Read project-specific docs** in `docs/` if they exist.

**Important:** Read all documents and code in depth -- do not skim.

Do not implement anything before completing these steps.

## Implementing Changes

Follow these steps in order. Do not skip steps.

### Step 1: Decide Where Your Change Belongs

- **New customization of upstream:** create a new patch branch.
- **Change to an existing customization:** work on that patch branch.
- **Fix that should eventually go upstream:** still a patch branch; record
  the upstream PR URL when you register it.

### Step 2: Create or Check Out the Patch Branch

For a new patch, start from the upstream:

```
git fetch upstream
git checkout -b patch/<descriptive-name> upstream/<upstream-default-branch>
```

For an existing patch:

```
git checkout patch/<existing-name>
git pull origin patch/<existing-name>
```

### Step 3: Implement and Commit

Make your changes, following the project's coding conventions, and commit
with conventional commit messages:

```
git add <files>
git commit -m "feat: add custom authentication middleware"
```

Only commit files relevant to this patch. One patch branch is one coherent
customization; do not bundle unrelated changes.

### Step 4: Push the Patch Branch

```
git push origin patch/<descriptive-name>
```

- **`origin` mode:** this pushes to the fork. Never push a patch branch to
  the hub in this mode; the hub refuses it (or, with
  `push_patches_to_origin=true`, only forwards it to the fork). Open the
  upstream PR from this branch if the patch is meant to go upstream.
- **`hub` mode:** this pushes to the hub's git server. If
  `push_patches_to_origin=true`, the hub copies the branch to the fork
  itself; do not push it there by hand.

### Step 5: Register the Patch (New Patches Only)

```
afc patch add <workspace-slug> \
  --branch patch/<descriptive-name> \
  --description "Why this patch exists"
```

Optional flags:

- `--position <n>` -- insert at this 1-based position instead of appending.
- `--upstream-pr <url>` -- the upstream pull request, if there is one. It
  also helps the hub notice when the patch has been squash-merged upstream.
- `--if-not-exists` -- return the existing patch instead of an error if the
  branch is already registered.
- `--skip-branch-check` -- register a branch that has not been pushed
  anywhere yet.

The branch name must not be the integration branch.

### Step 6: Get the Change into a Rebuild

- **`origin` mode:** sync. This is the only way new fork commits reach the
  hub; a rebuild does not fetch the fork. The sync queues a rebuild:
  ```
  afc workspace sync <workspace-slug> --wait
  ```
- **`hub` mode:** the push already queued a rebuild (unless the workspace
  turned `auto_rebuild_after_push` off). A new registration does not; queue
  one yourself:
  ```
  afc rebuild submit <workspace-slug> --wait
  ```

`--wait` blocks until the rebuild ends (`--timeout`, default 5m). To check a
rebuild later: `afc rebuild status <workspace-slug> <rebuild-id>`; while it
runs, its `patch_results` show the patches processed so far.

To see whether patches would conflict before rebuilding, run
`afc rebuild preview <workspace-slug>`. It changes nothing and reports each
patch as `would_succeed` or `would_conflict` with the conflicting files.

### Step 7: Check the Result

```
afc workspace patch-status <workspace-slug>
```

The rebuild succeeded when its status is `completed`. If it is `failed`, or a
patch shows `conflict`, go to [Conflict Resolution](#conflict-resolution).

### Step 8: Verify the Integration Branch

Fetch the rebuilt integration branch and run the project's test suite on it.
It is the artifact that gets deployed and must always work.

- **`hub` mode,** or **`origin` mode** with
  `rebuild_push_integration_branch=true` (then the fork has it too):
  ```
  git fetch origin
  git checkout <integration-branch>
  git reset --hard origin/<integration-branch>
  ```
- **`origin` mode** without that setting: the branch exists only on the hub:
  ```
  git fetch <hub-url>/git/<org-slug>/<workspace-slug>.git <integration-branch>
  git checkout --detach FETCH_HEAD
  ```

Use `reset --hard` or a detached checkout, not `git pull`: every rebuild
rewrites the integration branch.

## Upstream Sync

Sync brings the latest upstream into the hub (and, in `origin` mode, the
fork's patch branches):

```
afc workspace sync <workspace-slug>
```

Useful flags: `--wait` (block until the rebuild the sync queued finishes),
`--fail-on-diverged` (exit with code 3 when patch branches have diverged from
the fork; `origin` mode with `patch_divergence_policy=report`).

During a sync the hub:

1. fetches the upstream;
2. **`origin` mode only:** fetches the fork and brings each registered patch
   branch to the fork's tip (`created`, `fast_forwarded` or `replaced`);
3. marks patches that upstream has merged -- including squash merges -- as
   `merged_upstream`;
4. queues a rebuild when upstream or a patch branch moved, or a patch was
   merged (unless the workspace turned `auto_rebuild_after_sync` off).

### After Sync

Check `afc workspace patch-status <workspace-slug>`:

- `merged_upstream` patches are skipped by the next rebuild and then
  soft-deleted. If one was wrongly detected, restore it:
  `afc patch restore <workspace-slug> <patch-id>`.
- A `conflict` patch means upstream changed under it. See
  [Conflict Resolution](#conflict-resolution).
- **`origin` mode:** check each patch's `origin_sync_state`:
  - `diverged` -- the hub's copy and the fork's copy differ and the hub kept
    its own (policy `report`). To make the hub take the fork's version:
    `afc patch reset-to-origin <workspace-slug> <patch-id>`.
  - `missing_on_origin` -- the branch is not on the fork; the hub keeps
    applying its own copy. Push the branch to the fork, or remove the patch if
    it is obsolete.

  When a sync **replaced** the hub's copy, the old version is kept on the hub
  as `refs/hub/replaced/<branch>`. Fetch it with
  `git fetch <hub-url>/git/<org-slug>/<workspace-slug>.git refs/hub/replaced/<branch>`
  if commits there must be saved.

### Upstream Force-Push

If upstream rewrote its history, a normal sync handles it: the response
reports `force_push_detected: true` and the rebuild reapplies the patches.
Do not use `--reset-to-upstream`; it does nothing for carry-patch workspaces.

## Conflict Resolution

By default (`fail_fast`), a rebuild stops at the **first** patch with a
conflict the hub cannot resolve, and the integration branch stays as it was.
With fail mode `continue`, the rebuild leaves conflicting patches out and
completes with the rest.

### Step 1: Identify the Conflict

```
afc workspace patch-status <workspace-slug>
```

Patches with status `conflict` list the affected files in `conflict_files`.
`afc rebuild status <workspace-slug> <rebuild-id>` shows the same per rebuild.

### Step 2: Fix the Patch Branch

Bring the latest upstream into the patch branch, resolve, commit and push:

```
git fetch upstream
git checkout patch/<conflicting-patch>
git pull origin patch/<conflicting-patch>
git merge upstream/<upstream-default-branch>   # or rebase onto it
# resolve the conflicts
git add <fixed-files>
git commit -m "fix: resolve conflict with upstream changes in <area>"
git push origin patch/<conflicting-patch>
```

- **`hub` mode:** this pushes to the hub, which queues a rebuild.
- **`origin` mode:** this pushes to the fork. Then bring it into the hub,
  which queues a rebuild:
  ```
  afc workspace sync <workspace-slug>
  ```

### Step 3: Reactivate and Rebuild

A patch stays `conflict` even after a rebuild applies it cleanly. Set it back
to `active`, then rebuild if the push or sync has not already done so:

```
afc patch update <workspace-slug> <patch-id> --status active
afc rebuild submit <workspace-slug> --wait
```

If `afc rebuild submit` answers 409, a rebuild is already queued or running;
wait for it with `afc rebuild status`.

### Step 4: Verify

```
afc workspace patch-status <workspace-slug>
```

Every patch should be `active` with `last_rebuild_result` `success`.

### Rerere (Reuse Recorded Resolution)

The hub records conflict resolutions with git rerere and replays them when
the same conflict recurs. It does not learn resolutions you make on a branch
(as in Step 2), so the same conflict may need fixing by hand again.

```
afc rerere list <workspace-slug>
afc rerere forget <workspace-slug> <id>
```

Forget a resolution (by the `id` from `afc rerere list`) when it produces
wrong results; the next rebuild then stops at that conflict again.

## Rollback

If a rebuild produced a bad integration branch, return it to where it was
before that rebuild:

```
afc rebuild rollback <workspace-slug> <rebuild-id>
```

This moves only the integration branch on the hub; patches stay as they are,
and the next rebuild replaces the branch again. The first rebuild of a
workspace cannot be rolled back. After rolling back, fix the problematic patch
and rebuild.

## Patch Management

Every change to the patch list below needs a rebuild afterwards:
`afc rebuild submit <workspace-slug>`.

- **List:** `afc patch list <workspace-slug>` -- in position order, with ids.
- **Reorder:** `afc patch reorder <workspace-slug> <id-1> <id-2> ...` -- list
  ALL patch ids in the new order. A patch that depends on another must come
  after it.
- **Move one patch:** `afc patch update <workspace-slug> <patch-id> --position <n>`.
- **Edit metadata:**
  ```
  afc patch update <workspace-slug> <patch-id> \
    --description "Updated description" \
    --upstream-pr "https://github.com/upstream/repo/pull/42"
  ```
- **Change status:** `afc patch update <workspace-slug> <patch-id> --status <status>`
  with `active`, `disabled` (skipped by rebuilds, kept), `merged_upstream`
  (skipped, then soft-deleted by the next successful rebuild) or `conflict`.
- **Restore a soft-deleted patch:** `afc patch restore <workspace-slug> <patch-id>`
  -- it becomes `active` at the end of the list.
- **Remove:** `afc patch remove <workspace-slug> <patch-id>` -- the hub
  forgets the patch; the branch itself is not deleted (not on the hub, not on
  the fork).

## Rebuild Strategy and Fail Mode

The workspace settings `rebuild_strategy` and `rebuild_fail_mode` (see
`afc workspace get`) decide how a rebuild applies patches and handles
conflicts. Override them for one rebuild with `afc rebuild submit`:

| Flag | Values | Meaning |
|------|--------|---------|
| `--strategy` | `rebase` (default) | Cherry-pick each patch's commits; linear history. |
| | `merge` | Merge each patch branch with `--no-ff`; keeps patch boundaries. |
| `--fail-mode` | `fail_fast` (default) | Stop at the first conflict; integration branch unchanged. |
| | `continue` | Leave conflicting patches out; integration branch gets the rest. |

Do not change the workspace settings themselves unless asked to.

## Conventions

### Branch Naming

- Patch branches: `patch/<descriptive-name>` (e.g.
  `patch/custom-auth-middleware`, `patch/increase-rate-limits`).
- Never use the integration branch's name for anything else.

### Commit Messages

Use conventional commits: `<type>: <description>`.

- `feat:` -- new functionality added by the patch
- `fix:` -- bug fix within a patch
- `refactor:` -- restructuring patch code without changing behavior
- `chore:` -- maintenance (dependency updates, config changes)

### Patch Descriptions

Describe **why** the patch exists, not just what it changes. Future agents
and operators need the intent.

- Good: `"Override default session timeout to 4 hours for compliance with internal security policy"`
- Bad: `"Change timeout value"`

### Patch Ordering

- Independent patches can be in any order.
- If patch B depends on changes from patch A, A must have the lower position.
- Put patches likely to be merged upstream soon at the end of the stack, so
  their removal does not disturb the others.

## Commands Reference

| Task | Command |
|------|---------|
| Log in to the hub | `afc login` |
| Check the authority mode and settings | `afc workspace get <workspace-slug>` |
| Check the patch stack | `afc patch list <workspace-slug>` |
| Check integration health | `afc workspace patch-status <workspace-slug>` |
| Add a patch | `afc patch add <workspace-slug> --branch <name> --description "<text>"` |
| Update a patch | `afc patch update <workspace-slug> <patch-id> [--status <s>] [--description <text>] [--upstream-pr <url>] [--position <n>]` |
| Remove a patch | `afc patch remove <workspace-slug> <patch-id>` |
| Restore a soft-deleted patch | `afc patch restore <workspace-slug> <patch-id>` |
| Reorder all patches | `afc patch reorder <workspace-slug> <id1> <id2> ...` |
| Reset a diverged patch to the fork (`origin` mode) | `afc patch reset-to-origin <workspace-slug> <patch-id>` |
| Sync | `afc workspace sync <workspace-slug> [--wait] [--fail-on-diverged]` |
| Rebuild | `afc rebuild submit <workspace-slug> [--strategy <s>] [--fail-mode <m>] [--wait]` |
| Preview conflicts | `afc rebuild preview <workspace-slug>` |
| Rebuild status / history | `afc rebuild status <workspace-slug> <rebuild-id>` / `afc rebuild list <workspace-slug>` |
| Cancel a queued rebuild | `afc rebuild cancel <workspace-slug> <rebuild-id>` |
| Requeue a dead-lettered rebuild | `afc rebuild requeue <workspace-slug> <rebuild-id>` |
| Roll back a rebuild | `afc rebuild rollback <workspace-slug> <rebuild-id>` |
| List / forget rerere resolutions | `afc rerere list <workspace-slug>` / `afc rerere forget <workspace-slug> <id>` |

Full reference: the `afc` [CLI reference](../cli.md#patch-commands) and the
[carry-patch guide](../carry_patch_workflow.md).

## Quality Gates

### Before Considering Work Done

A session is not complete until all of the following are true:

1. Your changes are committed and pushed to the right place:
   - **`origin` mode:** pushed to the fork, then `afc workspace sync` run.
   - **`hub` mode:** pushed to the hub's git server.
2. A new patch is registered: `afc patch list <workspace-slug>` shows it.
3. A rebuild has completed and `afc workspace patch-status <workspace-slug>`
   shows every patch you touched as `active` with `last_rebuild_result`
   `success`.
4. The integration branch builds and passes the project's tests.
5. `git status` shows a clean working tree.
6. You provide a brief handoff note summarizing what was done and what
   remains.

### What NOT to Do

- **Never commit to the integration branch.** Every rebuild replaces it.
- **Never push to the upstream remote.**
- **Never push a patch branch to the hub in `origin` mode.** Push to the
  fork and run `afc workspace sync`.
- **Never change workspace settings** (`afc workspace update --setting ...`)
  unless the task asks for it.
- **Never submit a rebuild while one is queued or running** -- the hub
  answers 409. Wait for it to finish (`afc rebuild status`).

## Scope Discipline

- Focus on one patch per session. Do not modify multiple unrelated patches.
- Do not include "while here" fixes that belong in a different patch.
- Priority: fix patches in `conflict` before adding new patches.
- If a patch is no longer needed (merged upstream or obsolete), remove it
  before adding new ones to keep the stack clean.
