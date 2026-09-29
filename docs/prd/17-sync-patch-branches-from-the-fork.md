# Sync Patch Branches from the Fork

## Intent

The carry-patch workflow keeps a fork of an upstream project current by
rebuilding an integration branch from upstream HEAD plus an ordered list of
patch branches. Today the hub is the only place those patch branches are
read from: a patch branch exists for the rebuild only once someone has pushed
it to the hub's git server, sync fetches `upstream` and never `origin`, and
branches that exist on the fork but were never pushed through the hub are
invisible. The workflow guide, meanwhile, says patch branches live on the
fork. ADR 01 (`docs/adr/01-choose-the-authority-for-patch-branches.md`)
records the decision to make this an explicit per-workspace choice and to
recommend the fork as the authority for teams that upstream their patches.

This PRD specifies that choice. It adds a fork-authoritative mode in which
sync also fetches the fork and brings every registered patch branch to the
fork's tip, defines what happens when the hub's copy and the fork's copy
disagree, makes patch registration find branches on the fork in either mode,
adds an opt-in mirror of hub pushes to the fork, and updates the workflow
guide so that the recommended day-to-day workflow is "push to GitHub, let the
hub sync and rebuild".

Where patch branches live is a separate question from which working tree the
rebuild uses; the latter is `docs/prd/16-run-rebuilds-in-a-detached-worktree.md`
and this PRD does not depend on it.

## Goals

- Let a carry-patch workspace declare whether the hub or the fork is
  authoritative for patch branches, defaulting to the hub so that nothing
  changes for existing workspaces.
- In fork-authoritative mode, make `POST /sync` bring every registered patch
  branch to the fork's tip: create it when it is missing locally, fast-forward
  it when the fork is ahead, and apply a defined divergence policy otherwise.
- Treat a patch branch that moved during sync as a reason to rebuild, exactly
  like an upstream advance, with the same `AUTO_REBUILD_AFTER_SYNC` control.
- Report per-branch outcomes in the sync response and persist a per-patch
  sync state so the patch-status dashboard shows drift between syncs.
- Make `afc patch add` accept a branch that exists on the fork but not yet as
  a local branch, in both modes.
- In fork-authoritative mode, keep a single writer: the hub's git server
  refuses direct pushes to registered patch branches unless it can forward
  them to the fork first.
- Provide an opt-in mirror in hub-authoritative mode that pushes accepted
  patch-branch pushes on to the fork.
- Keep every existing API response field, patch status, and job record
  shape; additions only.
- Rewrite the affected parts of the workflow guide so the two models are
  described accurately and the fork-authoritative model is the recommended
  path for teams that open upstream pull requests.

## Non-goals

- **Changing what sync fetches from `upstream`**, how merged patches are
  detected, or how the integration branch is built.
- **Making the integration branch fork-authoritative.** It is always built by
  the hub and only ever pushed to the fork (`REBUILD_PUSH_INTEGRATION_BRANCH`).
  Sync never fetches it from `origin`.
- **Fetching the fork in standard-mode workspaces.** Standard sync already
  fetches `origin` and fast-forwards the workspace branch; it is unchanged.
- **Bidirectional merge on divergence.** One side is authoritative. The other
  side is replaced or reported, never merged.
- **Recording manual conflict resolutions into the hub's rerere cache from a
  fork push.** Rerere keeps recording resolutions that happen during rebuilds.
  A way to feed manually resolved conflicts back into rerere in
  fork-authoritative mode is a follow-up (see Open Questions).
- **Webhooks from the fork's host.** Sync is triggered as today: by `afc
  workspace sync`, by an operator's scheduler, or by any client of
  `POST /sync`. Reacting to GitHub push events is a separate feature.
- **Changing archive.** Archive keeps pushing `refs/heads/*` to `origin`
  before deleting the clone, in both modes.

## Current behaviour (for reference)

| Concern | Today |
|---------|-------|
| Where a patch branch must exist for the rebuild | `refs/heads/<name>` in `<workspace_root>/<slug>/trunk` |
| How that ref gets created | `git push` to the hub's git server only |
| What the initial clone brings from the fork | all branches, as `refs/remotes/origin/*`; only HEAD becomes a local branch |
| What sync fetches in carry-patch mode | `upstream` only |
| `afc patch add` validation | `rev-parse --verify <name>` in the trunk; fails for a fork-only branch unless `--skip-branch-check` |
| Rebuild with a fork-only branch | `skipped` / `branch_not_found` |
| How patch branches reach the fork | manual push by a person, or archive |
| Push to a registered patch branch on the hub | accepted; `AUTO_REBUILD_AFTER_PUSH` enqueues a rebuild |

## Functional Requirements

### Configuration

- **CF-1.** A workspace variable `PATCH_BRANCH_SOURCE` SHALL select the
  authority for patch branches. Accepted values are `hub` (default when unset
  or unrecognised) and `origin`. It is read at the start of each sync, patch
  registration, and push, so changing it takes effect on the next operation
  without restarting anything.
- **CF-2.** A workspace variable `PATCH_DIVERGENCE_POLICY` SHALL control what
  fork-authoritative sync does when the hub's copy of a patch branch is
  neither equal to nor an ancestor of the fork's copy. Accepted values are
  `replace` (default) and `report`. It is ignored when
  `PATCH_BRANCH_SOURCE` is `hub`.
- **CF-3.** A workspace variable `PUSH_PATCHES_TO_ORIGIN` SHALL enable
  mirroring of patch-branch pushes to the fork. Only the exact string `true`
  enables it. Its meaning depends on `PATCH_BRANCH_SOURCE` (see PU-1, PU-2).
- **CF-4.** All three variables SHALL be documented in the Configuration
  section of `docs/carry_patch_workflow.md`, in `docs/configuration.md` if
  workspace variables are listed there, and in the `openapi.yaml`
  descriptions of the endpoints whose behaviour they change.

### Fork fetch during sync

- **FS-1.** When `PATCH_BRANCH_SOURCE` is `origin`, carry-patch sync SHALL
  fetch the `origin` remote with the refspec `+refs/heads/*:refs/remotes/origin/*`
  and no tags, using the origin credentials resolved the same way the clone
  and the integration-branch push resolve them (`GIT_PAT`, then
  `GIT_USERNAME`/`GIT_PASSWORD`, then none).
- **FS-2.** The origin fetch SHALL happen after the upstream fetch and before
  merge detection, under the same workspace lock, so that merge detection and
  the auto-rebuild see the refreshed patch tips.
- **FS-3.** An origin fetch failure SHALL abort the sync with `502` and the
  message `origin fetch failed`, leaving `upstream_head_sha`, patch statuses
  and patch refs unchanged, mirroring the existing upstream-fetch failure
  behaviour. The response SHALL distinguish it from an upstream failure so an
  operator knows which credential to fix.
- **FS-4.** When `PATCH_BRANCH_SOURCE` is `hub`, sync SHALL NOT fetch
  `origin`, exactly as today.

### Bringing patch branches to the fork's tip

For every patch of the workspace whose status is `active`, `conflict` or
`disabled` (not `merged_upstream`, not `deleted`), in position order:

- **PB-1.** If `refs/remotes/origin/<branch>` does not exist after the fetch,
  the local branch SHALL be left unchanged and the patch SHALL be recorded
  with sync state `missing_on_origin`.
- **PB-2.** If `refs/heads/<branch>` does not exist locally, it SHALL be
  created at the origin tip and the patch recorded with action `created`.
- **PB-3.** If both exist and point at the same commit, the patch SHALL be
  recorded with sync state `in_sync` and no ref is written.
- **PB-4.** If the local tip is an ancestor of the origin tip, the local
  branch SHALL be moved to the origin tip with `update-ref` and the patch
  recorded with action `fast_forwarded`.
- **PB-5.** If the two have diverged (neither is an ancestor of the other, or
  the origin tip is an ancestor of the local tip) and the policy is
  `replace`, the hub SHALL first write the current local tip to
  `refs/hub/replaced/<branch>` (overwriting any earlier backup for that
  branch), then move `refs/heads/<branch>` to the origin tip, and record the
  action `replaced` with both SHAs.
- **PB-6.** If the two have diverged and the policy is `report`, the local
  branch SHALL be left unchanged and the patch recorded with sync state
  `diverged`, including both SHAs.
- **PB-7.** The integration branch SHALL never be a candidate for PB-1 to
  PB-6, even if a row with that name exists in the patches table.
- **PB-8.** Ref updates SHALL be performed in the trunk with `update-ref`,
  never with `checkout` or `reset`. If the branch being moved is the trunk's
  current checkout, the working tree SHALL be reset to the new tip afterwards
  in the same way the post-push hook does it, so the trunk's files match its
  HEAD.
- **PB-9.** A branch changed under PB-2, PB-4 or PB-5 SHALL count as "patch
  advanced" for the purpose of the auto-rebuild decision (see RB-1).

### Rebuild trigger

- **RB-1.** Carry-patch sync SHALL enqueue an auto-rebuild when upstream
  advanced (as today) **or** when at least one patch branch changed under
  PB-2, PB-4 or PB-5, subject to `AUTO_REBUILD_AFTER_SYNC` not being `false`
  and to the existing queue deduplication. `rebuild_triggered` and
  `rebuild_job_id` in the response keep their meaning.
- **RB-2.** The early return "upstream HEAD has not changed" in the current
  sync SHALL be moved after the patch refresh so that a patch-only change can
  still trigger a rebuild. When neither upstream nor any patch changed, the
  response SHALL be as today: empty `patches_merged`, `rebuild_triggered`
  false.
- **RB-3.** `last_sync_at` SHALL be updated whenever the sync completes,
  whether or not anything advanced. (Today it is written only when upstream
  advanced; this is corrected so the dashboard can show when the fork was
  last checked.)

### Sync response

- **SR-1.** The carry-patch sync response SHALL gain a `patches_synced`
  array, present only when `PATCH_BRANCH_SOURCE` is `origin`. Each element is
  `{ "branch_name", "action", "state", "local_sha", "origin_sha",
  "replaced_sha" }` where `action` is one of `none`, `created`,
  `fast_forwarded`, `replaced`; `state` is one of `in_sync`, `diverged`,
  `missing_on_origin`; `replaced_sha` is present only for `replaced`.
- **SR-2.** The response SHALL gain `origin_fetched` (boolean) so that
  clients can tell whether the fork was consulted, and `patches_diverged`
  (array of branch names) as a convenience for scripts, populated only under
  policy `report`.
- **SR-3.** `afc workspace sync` SHALL print these fields as part of the
  JSON it already prints and SHALL exit `0` when the sync succeeded even if
  `patches_diverged` is non-empty. A `--fail-on-diverged` flag SHALL make it
  exit `3` in that case so schedulers can alert.

### Persisted sync state

- **PS-1.** The `patches` table SHALL gain three nullable columns:
  `origin_sync_state` (`in_sync`, `diverged`, `missing_on_origin`),
  `origin_sha`, and `origin_synced_at`. They are written by fork-authoritative
  sync for each patch it considers and cleared (set to null) when the
  workspace's `PATCH_BRANCH_SOURCE` is `hub` at the next sync.
- **PS-2.** The patch response schema SHALL expose them as
  `origin_sync_state`, `origin_sha` and `origin_synced_at`, absent when null.
- **PS-3.** The patch-status dashboard (`GET /workspaces/:slug/patch-status`)
  SHALL include them per patch and SHALL add `patches_diverged` and
  `patches_missing_on_origin` counts to its summary.

### Patch registration

- **PR-1.** `POST /workspaces/:slug/patches` SHALL validate branch existence
  in this order, in both modes: `refs/heads/<name>`; then
  `refs/remotes/origin/<name>`, in which case the local branch is created at
  that tip before the row is inserted; then, only when
  `PATCH_BRANCH_SOURCE` is `origin`, a single-branch fetch
  `+refs/heads/<name>:refs/remotes/origin/<name>` from the fork followed by
  the same check. If none succeeds the request fails with `400` and the
  message `branch does not exist in repository or on origin`.
- **PR-2.** `skip_branch_check` keeps its meaning: none of PR-1 runs and no
  local branch is created.
- **PR-3.** Batch registration SHALL apply PR-1 to every element before
  inserting any row, so a batch is still all-or-nothing. Local branches
  created for elements that validated SHALL be left in place if a later
  element fails; they are harmless refs and the next registration finds them.
- **PR-4.** The `hub.patch.create` audit event SHALL record how the branch was
  resolved (`local`, `origin_tracking`, `origin_fetch`) in metadata.

### Pushes to the hub's git server

- **PU-1.** When `PATCH_BRANCH_SOURCE` is `origin` and `PUSH_PATCHES_TO_ORIGIN`
  is not `true`, the git server SHALL reject a push that updates a registered
  patch branch (any status other than `deleted`) with a per-ref error
  `branch is synced from origin; push to <git_url> instead`. Other refs in
  the same push are unaffected. Pushes to unregistered branches and to the
  integration branch keep today's behaviour.
- **PU-2.** When `PATCH_BRANCH_SOURCE` is `origin` and `PUSH_PATCHES_TO_ORIGIN`
  is `true`, the git server SHALL forward each registered-patch-branch update
  to `origin` with a non-force push using the origin credentials **before**
  accepting it locally. If the forward is rejected (non-fast-forward, auth,
  network), the local update SHALL be rejected with the origin error text so
  the fork and the hub never disagree because of a hub push.
- **PU-3.** When `PATCH_BRANCH_SOURCE` is `hub` and `PUSH_PATCHES_TO_ORIGIN`
  is `true`, the post-push hook SHALL push each registered patch branch that
  the push updated to `origin` with a force push, best-effort, after the push
  has been accepted and in the same goroutine that enqueues the rebuild. A
  failure SHALL be logged and emitted as `hub.patch.mirror_failed` with the
  branch and the error; it SHALL NOT fail the push or the rebuild.
- **PU-4.** The pre-receive decision in PU-1 and PU-2 SHALL be implemented as
  a hook registered by the carry-patch package into the git server, in the
  same style as the existing post-push hook, so the git server does not import
  carry-patch code.

### Divergence recovery

- **DR-1.** `POST /workspaces/:slug/patches/:id/reset-to-origin` SHALL move a
  patch branch to its `refs/remotes/origin/<branch>` tip regardless of
  policy, writing `refs/hub/replaced/<branch>` first as in PB-5. It requires
  `patches:write`, takes the workspace lock, and returns the patch record.
  `afc patch reset-to-origin <slug> <patch-id>` SHALL call it.
- **DR-2.** `GET /workspaces/:slug/patches/:id` SHALL include `replaced_sha`
  when `refs/hub/replaced/<branch>` exists, so an operator can recover a
  replaced tip with `git fetch <hub_url> refs/hub/replaced/<branch>`.
- **DR-3.** The hub's git server SHALL advertise `refs/hub/replaced/*` to
  fetch (it already advertises all refs) and SHALL reject pushes to that
  namespace.
- **DR-4.** Backup refs SHALL be removed when the patch row is purged after
  its soft-delete retention period, and by `afc patch remove`.

### Audit and logging

- **AU-1.** Fork-authoritative sync SHALL emit one `hub.patch.sync` audit
  event per sync with metadata `{ origin_fetched, created[], fast_forwarded[],
  replaced[], diverged[], missing_on_origin[] }`. Individual `replaced`
  outcomes SHALL additionally be emitted as `hub.patch.replace` with both
  SHAs, because they discard commits.
- **AU-2.** PU-1 rejections SHALL be logged at info level with the slug,
  branch and pushing user; PU-2 forward failures at warn level.

### Documentation

- **DC-1.** `docs/carry_patch_workflow.md` SHALL be restructured so that the
  Remotes table is correct for both modes, a new section "Where patch
  branches live" explains `PATCH_BRANCH_SOURCE`, and the Getting Started
  walkthrough shows the fork-authoritative flow (create the branch locally,
  push to the fork, `afc patch add`, sync) as the primary path with the hub
  flow as the alternative for agent-driven workspaces. The "Resolving
  conflicts after a failed rebuild" section SHALL describe both modes.
- **DC-2.** `docs/api.md`, `docs/cli.md` and `docs/openapi.yaml` SHALL document
  the new response fields, the new patch fields, the new endpoint, the new CLI
  command and flag, and the `400`/`502` messages introduced here.
- **DC-3.** `docs/examples/AGENTS_carry_patch.md` SHALL tell agents to check
  `PATCH_BRANCH_SOURCE` before deciding where to push a patch branch.
- **DC-4.** ADR 01 SHALL be moved from `Proposed` to `Accepted` when this PRD
  is implemented, and the erratum for any divergence from this PRD SHALL be
  filed under `docs/errata/`.

### Testing

- **TS-1.** Integration tests with a real fork repository and a real
  upstream repository SHALL cover each of PB-1 to PB-6, asserting the local
  ref, the backup ref, the response element and the persisted sync state.
- **TS-2.** A test SHALL show that a patch-only change (upstream unchanged)
  enqueues a rebuild, and that `AUTO_REBUILD_AFTER_SYNC=false` suppresses it.
- **TS-3.** A test SHALL show that `PATCH_BRANCH_SOURCE=hub` performs no
  origin fetch (using a counting fetch stub) and produces a response without
  `patches_synced`.
- **TS-4.** Tests for PR-1 SHALL cover all three resolution paths and the
  failure message, in both modes, single and batch.
- **TS-5.** Git-server tests SHALL cover PU-1 (rejected ref, other refs
  accepted), PU-2 (forward success and forward rejection), and PU-3 (mirror
  success and logged failure) using a local bare repository as `origin`.
- **TS-6.** A test SHALL show that an origin fetch failure leaves
  `upstream_head_sha`, patch statuses and patch refs unchanged even though
  the upstream fetch succeeded moments earlier.
- **TS-7.** A test SHALL show that `last_sync_at` is updated on a no-change
  sync (RB-3).

## Technical Boundaries

- **Two fetches, two credentials.** The origin fetch reuses the go-git fetch
  path and `resolveCloneAuth`; the upstream fetch is unchanged. A public fork
  with a private upstream and vice versa both work because each remote
  resolves its own credentials.
- **`update-ref` is enough.** Bringing a branch to the fork's tip is a ref
  write; no checkout is needed except when the branch is the trunk's current
  checkout (PB-8). This keeps the change independent of the worktree PRD.
- **Ancestry checks use `merge-base --is-ancestor`** through the existing
  `GitRunner.IsAncestor`, on SHAs rather than names, so a concurrent push
  cannot change the answer between the check and the write. The whole
  refresh runs under the workspace lock that sync already holds.
- **The git server's receive-pack is go-git.** Rejecting individual refs in
  a push requires returning per-command status in the report-status phase.
  go-git's server supports per-command status; the implementation must
  confirm that the client receives the per-ref message rather than a whole
  push failure.
- **Forwarding a push (PU-2) means the hub pushes objects it just received.**
  The objects are already in the trunk's object store after the receive-pack
  session unpacks them, so the forward is an ordinary go-git push from the
  trunk. The forward must complete before the report-status is sent, which
  lengthens the push; this is acceptable because it only applies to
  registered patch branches in fork-authoritative mode.
- **Schema migration.** Three nullable columns on `patches`, added through
  the existing idempotent `ALTER TABLE` list. No backfill.
- **Group key and deduplication.** The rebuild enqueued by RB-1 uses the same
  `(type, key)` and group as today, so a patch-triggered and an
  upstream-triggered rebuild collapse into one.
- **Backup ref retention.** One backup per branch (the latest replacement).
  Older tips are recoverable only from the fork's own history or reflog.

## Dependencies

- `internal/carrypatch/sync_handlers.go`: fetch, refresh, response.
- `internal/workspace/patch_handlers.go` and `cmd/af-hub/main.go`: PR-1 in
  the branch-check hook.
- `internal/gitserver/handlers.go`: pre-receive hook registration and per-ref
  rejection; post-push mirror.
- `internal/workspace/schema.go`: new columns.
- `internal/cli/`: `afc patch reset-to-origin`, `--fail-on-diverged`.
- No new external modules.

## Design Decisions

### 1. A workspace variable, not a workspace column

Every carry-patch behaviour today is a workspace variable (`REBUILD_STRATEGY`,
`AUTO_REBUILD_AFTER_SYNC`, `REBUILD_PUSH_INTEGRATION_BRANCH`, and so on).
Adding a column would be more discoverable in `afc workspace get`, but it
would need a migration, a change to the workspace object in the API and
OpenAPI, and a CLI flag, for a setting that most workspaces set once. The
variable can be promoted to a column later without changing behaviour.

### 2. `replace` as the default divergence policy

The point of `origin` mode is that the fork is the truth. A default that left
the hub's copy in place on divergence would keep rebuilding a stale tip and
would surface the problem only to someone reading the response. Replacing,
with a backup ref and a dedicated audit event, is the behaviour the mode
promises. `report` exists for teams that want a human in the loop.

### 3. Refuse or forward hub pushes in `origin` mode, never accept silently

Accepting a hub push and letting the next sync replace it would throw away
the developer's work with a delay and no warning at push time. Refusing at
push time with a message that names the fork is immediate and cheap.
Forwarding (PU-2) is offered for teams whose tooling only knows the hub URL;
because the forward happens before local acceptance, the fork stays the
single writer.

### 4. Resolve fork branches at registration in both modes

Even a hub-authoritative workspace starts life as a clone of a fork whose
branches are in `refs/remotes/origin/*`. Creating the local branch from the
tracking ref at registration time costs nothing and removes the most common
reason for `--skip-branch-check`. It does not change who is authoritative
afterwards: in `hub` mode the branch is then written only by hub pushes.

### 5. Patch-branch changes trigger the same rebuild as upstream changes

The rebuild's inputs are upstream HEAD and the patch tips. A change to either
should produce the same job, under the same dedup and the same opt-out. This
also fixes `last_sync_at` being written only on upstream change.

### 6. Backup refs instead of a database table of replaced tips

The replaced commit is already in the object store; a ref is the smallest
thing that keeps it reachable and fetchable. A table would duplicate git's
job and would not stop `gc` from pruning the commit.

## Open Questions

- **Manual rerere recording in `origin` mode.** Today the guide tells
  operators to resolve conflicts in the trunk so rerere records the
  resolution. In `origin` mode the fix lands on the fork and the hub never
  sees the conflict being resolved. Options: a rebuild step that, on
  conflict, checks whether the fork's newer tip applies cleanly and records
  that as the resolution; or an endpoint that accepts a resolved tree for a
  conflict id. Not decided here.
- **Should `missing_on_origin` disable the patch?** Leaving it `active` means
  the rebuild keeps applying the hub's copy of a branch the fork deleted.
  Marking it `disabled` automatically would be surprising if the deletion was
  a mistake. The proposal leaves the status alone and surfaces the state;
  revisit after use.
- **Sync frequency.** Fork-authoritative mode is most useful with frequent
  syncs. Whether the hub should gain a built-in schedule per workspace
  (rather than relying on an external scheduler) is a separate feature.
- **Origin branch filter.** Fetching every fork branch on each sync is
  wasteful for forks with hundreds of branches. A follow-up could fetch only
  registered branch names with explicit refspecs, at the cost of PR-1's
  tracking-ref path needing its own fetch. Start with the full fetch for
  simplicity.
