---
spec_id: "02"
spec_name: "fork_authoritative_sync"
title: "Fork-Authoritative Sync for Carry-Patch Workspaces"
status: "active"
created_at: "2026-09-30T09:28:32.774774Z"
updated_at: "2026-09-30T09:28:32.774774Z"
intent_hash: "66aca248e4fd50c51f68f1250619b500880176215c828c9689d123ad97a21aaa"
schema_version: 2
source: "docs/prd/17-sync-patch-branches-from-the-fork.md"
---
## Intent

A carry-patch workspace must be able to declare the fork (`origin`) rather
than the hub as the authority for its patch branches, so that a patch branch
pushed to GitHub — and never pushed to the hub — is still picked up by sync
and carried into the next rebuild. This spec is the stable statement of that
capability for `internal/carrypatch`'s sync path and the `patches` table: the
workspace variable that selects the authority, the algorithm that brings each
registered patch branch to the fork's tip on every sync, the rebuild trigger
and response/state changes that follow from a patch branch moving, and the
divergence policy applied when the hub's copy and the fork's copy disagree.
It exists so that "where does sync read a patch branch from" is an explicit,
tested, per-workspace choice instead of an accident of what has been pushed
to the hub's git server.

## Goals

- A workspace variable, `PATCH_BRANCH_SOURCE` (`hub` default, or `origin`),
  selects the authority for patch branches; existing workspaces are
  unaffected because the default reproduces today's behaviour exactly.
- In `origin` mode, `POST /workspaces/:slug/sync` additionally fetches the
  `origin` remote and, for every patch whose status is `active`, `conflict`
  or `disabled`, brings `refs/heads/<branch>` to the fork's tip: creates it
  when missing locally, fast-forwards it when the fork is ahead, and applies
  `PATCH_DIVERGENCE_POLICY` (`replace` default, or `report`) when the two
  have diverged, backing up the replaced tip under `refs/hub/replaced/<branch>`.
- A patch branch that is created, fast-forwarded or replaced during sync
  counts as "advanced" for the auto-rebuild decision exactly like an
  upstream advance, gated by the existing `AUTO_REBUILD_AFTER_SYNC` variable.
- `last_sync_at` is updated on every completed sync, whether or not anything
  advanced (today it is only written when upstream advances).
- The sync response gains `patches_synced`, `origin_fetched` and
  `patches_diverged`; the `patches` table gains a nullable
  `origin_sync_state` / `origin_sha` / `origin_synced_at` per patch, exposed
  on the patch record and summarised on the patch-status dashboard.
- Every existing response field, patch status and job record shape is kept;
  this spec only adds fields and columns.

## Non-goals

- **Patch registration finding a fork-only branch.** `POST
  /workspaces/:slug/patches` and `afc patch add` still validate with
  `rev-parse --verify` only; making them resolve
  `refs/remotes/origin/<name>` or fetch a single branch from the fork is
  `patch_registration_from_fork`.
- **Refusing or mirroring pushes to the hub's git server.**
  `PUSH_PATCHES_TO_ORIGIN` and the git server's pre-receive/post-push
  behaviour for registered patch branches are `patch_branch_push_protection`.
- **Manual divergence recovery.** `POST
  /workspaces/:slug/patches/:id/reset-to-origin`, `afc patch
  reset-to-origin`, exposing `replaced_sha`, backup-ref retention, and the
  `--fail-on-diverged` flag on `afc workspace sync` are
  `patch_divergence_recovery`.
- **Changing what sync fetches from `upstream`, how merged-upstream
  detection works, or how the integration branch is built.** Unchanged.
- **Making the integration branch fork-authoritative.** It is always built
  by the hub and only ever pushed to `origin` under
  `REBUILD_PUSH_INTEGRATION_BRANCH`; sync never fetches it from `origin`.
- **Fetching `origin` in standard-mode (non-carry-patch) workspaces.**
  Standard sync already fetches `origin` and fast-forwards the workspace
  branch; unchanged.
- **Bidirectional merge on divergence.** One side is authoritative; the
  other is replaced or reported, never merged.
- **Recording manually resolved conflicts into the hub's rerere cache from a
  fork push.** Left for a later spec (see Design Decisions).
- **Reacting to webhooks from the fork's host.** Sync is still triggered by
  `afc workspace sync`, an operator's scheduler, or any client of `POST
  /sync`.
- **Changing archive.** Archive keeps pushing `refs/heads/*` to `origin`
  before deleting the clone, in both modes.
- **Where the rebuild applies patches (worktree vs. trunk).** That is
  `01_run_rebuilds_in_a_detached_worktree`, already active and independent of
  this spec: it changes how patches are applied once fetched, not where the
  branches that get applied come from.

## Background

A carry-patch workspace (`internal/workspace`, `internal/carrypatch`) has two
remotes in its trunk clone at `<workspace_root>/<slug>/trunk`: `origin` (the
fork, `git_url`) and `upstream` (`upstream_url`). Today:

- `internal/carrypatch/sync_handlers.go`'s `runCarryPatchSync` fetches only
  `upstream` (via `internal/upstream.Fetch`, refspecs
  `+refs/heads/*:refs/remotes/upstream/*` and `+HEAD:refs/remotes/upstream/HEAD`),
  compares the result against the stored `upstream_head_sha`, and returns
  early with no state change when it is unchanged (`if !upstreamAdvanced {
  return &resp, nil }`). `origin` is never touched by sync.
- A patch branch exists for the rebuild only once `refs/heads/<name>` exists
  in the trunk; the only way to create it today is a `git push` to the hub's
  git server (`internal/gitserver`) or a manual `git branch` on the host.
  `refs/remotes/origin/<name>` (populated by the initial clone) is never
  promoted to a local branch by sync.
- `AUTO_REBUILD_AFTER_SYNC` (checked via `GetVariableFunc`, default true,
  disabled only by the exact string `"false"`) gates the rebuild enqueue that
  follows an upstream advance, using `BuildRebuildPayload` /
  `jobqueue.Queue.Enqueue` with group key `slug + ":" + integrationBranch`
  for dedup.
- The `patches` table (`internal/workspace/schema.go`) has an idempotent
  `ALTER TABLE` migration list (`patchFieldDDL`) already used to add
  `conflict_files` and `deleted_at`; the same mechanism is the model for the
  three new columns here. Two packages read/write this table with their own
  column lists and structs: `internal/workspace/patch_store.go` (`Patch`,
  `patchResponse`, used by `GET/PATCH /workspaces/:slug/patches`) and
  `internal/carrypatch/wire.go`'s `SQLPatchStore` (`carrypatch.Patch`, used by
  the sync/rebuild/patch-status code). Both need the new columns.
- `internal/carrypatch/api.go`'s `handlePatchStatus` builds
  `PatchStatusSummary` from patch statuses already in memory; adding two
  counts is additive to that function.
- ADR `docs/adr/01-choose-the-authority-for-patch-branches.md` (status
  `Proposed`) records the decision this spec implements: an explicit
  per-workspace `PATCH_BRANCH_SOURCE`, defaulting to `hub`, with `origin`
  bringing every registered patch branch to the fork's tip on sync and
  `replace` as the default divergence policy. This spec does not flip the
  ADR to `Accepted` — that happens once the whole split (this spec plus the
  three listed in Non-goals) is implemented, alongside filing any erratum
  under `docs/errata/`.
- `docs/carry_patch_workflow.md`'s "Remotes" table currently describes
  `origin` as "where patch branches live", which is not true for any
  workspace today; this spec is the first of the split to make that
  statement true (for `origin`-mode workspaces) and needs the table and a
  new "Where patch branches live" section corrected accordingly.

## Requirements

1. **`PATCH_BRANCH_SOURCE` workspace variable.** Read via the existing
   `GetVariableFunc("workspace", slug, "PATCH_BRANCH_SOURCE")` at the start
   of sync. Accepted values are `hub` (the behaviour when unset or any other
   value) and `origin`. No schema or API change to the workspace object;
   this follows the same pattern as `AUTO_REBUILD_AFTER_SYNC` and
   `REBUILD_STRATEGY`.

2. **`PATCH_DIVERGENCE_POLICY` workspace variable.** Read the same way,
   consulted only when `PATCH_BRANCH_SOURCE` is `origin`. Accepted values
   are `replace` (default, when unset or any other value) and `report`.

3. **Fetching `origin` during sync.** When `PATCH_BRANCH_SOURCE` is
   `origin`, sync fetches the `origin` remote with refspec
   `+refs/heads/*:refs/remotes/origin/*` and no tags, using the same
   credential resolution as the clone and the integration-branch push
   (`GIT_PAT`, then `GIT_USERNAME`/`GIT_PASSWORD`, then no auth — i.e. the
   function the codebase calls `resolveCloneAuth`, already exported as
   `workspace.ResolveCloneAuth`). This fetch runs after the upstream fetch,
   under the same `wslock` the sync already holds. A failure aborts the
   sync with `502` and the message `origin fetch failed`, leaving
   `upstream_head_sha`, patch statuses and every patch ref exactly as they
   were before the sync started — the origin fetch must be attempted, and
   fail, without first writing anything from the upstream fetch's result.
   When `PATCH_BRANCH_SOURCE` is `hub`, no `origin` fetch happens, exactly
   as today, and this must be true even when a workspace has previously run
   in `origin` mode (the variable is re-read on every sync, not cached).

4. **Bringing each patch branch to the fork's tip.** After the `origin`
   fetch succeeds, for every patch of the workspace whose status is
   `active`, `conflict` or `disabled` (never `merged_upstream`, `deleted`,
   or the integration branch, even if a row with that name exists), in
   position order:
   - If `refs/remotes/origin/<branch>` does not exist, leave the local
     branch untouched and record sync state `missing_on_origin`.
   - If `refs/heads/<branch>` does not exist locally, create it at the
     origin tip with `update-ref` and record action `created`.
   - If both exist and point at the same commit, record state `in_sync`;
     no ref write.
   - If the local tip is an ancestor of the origin tip (and they differ),
     move `refs/heads/<branch>` to the origin tip with `update-ref` and
     record action `fast_forwarded`.
   - Otherwise (diverged: neither is an ancestor of the other, or the
     origin tip is an ancestor of the local tip) apply
     `PATCH_DIVERGENCE_POLICY`: under `replace`, first write the current
     local tip to `refs/hub/replaced/<branch>` (overwriting any earlier
     backup for that branch), then move `refs/heads/<branch>` to the origin
     tip with `update-ref`, and record action `replaced` with both SHAs;
     under `report`, leave the local branch untouched and record state
     `diverged` with both SHAs.
   Every ref update in this step uses `update-ref`, never `checkout` or
   `reset`, and every ancestry check uses `GitRunner.IsAncestor` (`git
   merge-base --is-ancestor`) on the two resolved SHAs, never on branch
   names, so a concurrent push cannot change the answer between the check
   and the write — the whole step runs under the sync's existing `wslock`.
   If the branch being moved is the trunk's current checkout, the working
   tree is reset to the new tip immediately afterwards (`git reset --hard`,
   the same recovery the post-push hook already performs after a push moves
   the checked-out branch), so the trunk's files stay consistent with its
   HEAD. A branch created, fast-forwarded or replaced under this step counts
   as "patch advanced" for requirement 5. `missing_on_origin` does not
   change the patch's status — it stays `active`/`conflict`/`disabled` and
   the rebuild keeps applying the hub's last-known copy.

5. **Rebuild trigger and ordering.** The existing early return ("upstream
   HEAD has not changed, respond with empty `patches_merged` and
   `rebuild_triggered=false`") moves to after both the upstream fetch and
   (in `origin` mode) the fork-refresh step, and now also depends on whether
   any patch advanced there: sync only returns early when upstream did
   *not* advance and no patch branch was created, fast-forwarded or
   replaced. Otherwise, sync proceeds exactly as today (persist
   `upstream_head_sha`, run the existing merge-upstream detection for each
   active patch against the current — possibly just-refreshed — branch
   tips) and enqueues an auto-rebuild when `AUTO_REBUILD_AFTER_SYNC` is not
   `"false"`, using the existing group key and duplicate suppression, if
   upstream advanced **or** at least one patch advanced under requirement 4.
   `rebuild_triggered` and `rebuild_job_id` keep their existing meaning.
   `last_sync_at` is written on every sync that completes without error,
   including the early-return case — today it is written only when upstream
   advances; this corrects that so the dashboard can show when the fork was
   last checked even on a no-op sync.

6. **Sync response additions.** The carry-patch sync response gains
   `origin_fetched` (boolean, present always: `true` only when `origin` was
   actually fetched this sync). It gains `patches_synced` (array, present
   only when `PATCH_BRANCH_SOURCE` is `origin`), one element per patch
   considered under requirement 4: `{"branch_name", "action", "state",
   "local_sha", "origin_sha", "replaced_sha"}`, where `action` is one of
   `none`, `created`, `fast_forwarded`, `replaced` and `state` is one of
   `in_sync`, `diverged`, `missing_on_origin`; `replaced_sha` is present only
   when `action` is `replaced`. It gains `patches_diverged` (array of branch
   names, populated only under `PATCH_DIVERGENCE_POLICY=report`, empty
   otherwise) as a convenience for scripts. `afc workspace sync` already
   prints the JSON response verbatim, so these fields appear in the CLI
   output with no CLI code change.

7. **Persisted per-patch sync state.** The `patches` table gains three
   nullable columns, added through the existing idempotent `ALTER TABLE`
   list (`internal/workspace/schema.go`'s `patchFieldDDL`), following the
   pattern already used for `conflict_files` and `deleted_at`:
   `origin_sync_state` (`in_sync`, `diverged`, `missing_on_origin`, or
   null), `origin_sha`, `origin_synced_at`. Fork-authoritative sync writes
   them for every patch it considers under requirement 4. When
   `PATCH_BRANCH_SOURCE` is `hub` at the start of a sync, they are cleared
   (set to null) for every non-deleted patch of that workspace during that
   sync, so a workspace that switches back to `hub` mode does not keep
   showing stale fork state. `GET/PATCH /workspaces/:slug/patches[/:id]`
   expose them as `origin_sync_state`, `origin_sha`, `origin_synced_at`,
   omitted (or null, matching the existing convention for nullable patch
   fields such as `upstream_pr_url`) when unset. `GET
   /workspaces/:slug/patch-status` includes the same three fields per patch
   entry and adds `patches_diverged` and `patches_missing_on_origin` counts
   to its summary, computed the same way the existing status counts are.

8. **Audit.** Fork-authoritative sync emits one `hub.patch.sync` event per
   sync (actor type `system`, resource type `patch`) with metadata
   `{origin_fetched, created: [...], fast_forwarded: [...], replaced: [...],
   diverged: [...], missing_on_origin: [...]}` (branch name lists). Each
   `replaced` outcome additionally emits its own `hub.patch.replace` event
   with the branch name and both SHAs, because it discards commits from the
   hub's copy and needs its own audit trail independent of the batch event.
   Audit emission follows the existing pattern (`audit.Emitter`, nil-safe,
   errors logged and swallowed) already used by `RebuildHandler`; add an
   `Audit audit.Emitter` field to `SyncAPIConfig` and wire it from
   `cmd/af-hub/main.go` the same way `rebuildHandler.Audit` is wired.

9. **Documentation.** `docs/carry_patch_workflow.md`'s Remotes table is
   corrected (`origin` is "your fork; push target; also read from directly
   when `PATCH_BRANCH_SOURCE=origin`"), and a new "Where patch branches
   live" section explains `PATCH_BRANCH_SOURCE` and
   `PATCH_DIVERGENCE_POLICY`, the sync algorithm description gains the
   fork-refresh step, and the Configuration section documents both new
   variables. `docs/api.md`'s sync section and `docs/openapi.yaml` document
   `origin_fetched`, `patches_synced`, `patches_diverged`, the `502 origin
   fetch failed` outcome, and the three new patch fields (in the patch
   schema and the patch-status schema). `docs/configuration.md` is
   server/CLI configuration only and does not list workspace variables
   today, so it is not touched by this requirement. The Getting Started
   walkthrough and the "Resolving conflicts" section stay as they are —
   presenting the fork-authoritative flow as the primary path is
   `patch_divergence_recovery`'s documentation work, once registration and
   push-protection also exist.

## Design Decisions

1. **A workspace variable, not a workspace column, for both new settings.**
   Every carry-patch behaviour switch today (`REBUILD_STRATEGY`,
   `AUTO_REBUILD_AFTER_SYNC`, `SQUASH_MERGE_DETECTION`, ...) is a workspace
   variable read through `GetVariableFunc`. Adding a column would need a
   migration and API/OpenAPI/CLI changes for settings most workspaces set
   once; a variable can be promoted later without a behaviour change.

2. **`replace` as the default divergence policy.** The point of `origin`
   mode is that the fork is the truth; leaving the hub's stale copy in place
   by default would keep rebuilding it and only surface the problem to
   someone reading the response. `report` remains available for teams that
   want a human in the loop before any commit is discarded.

3. **Ordering: fetch origin, refresh branches, then run merge detection,
   then decide on the early return.** The input requires merge detection and
   the auto-rebuild decision to "see the refreshed patch tips" (origin fetch
   before merge detection) and requires a patch-only change to still trigger
   a rebuild (early return after the refresh). The only ordering consistent
   with both is: upstream fetch → origin fetch → per-patch fork-refresh
   (writes `refs/heads/*`) → compute `upstreamAdvanced` /
   `patchAdvanced` → early return if neither → workspace record update →
   existing merge-upstream detection loop (now over the just-refreshed
   branches) → rebuild trigger. This is implemented as a straight-line
   extension of `runCarryPatchSync`, not a new code path.

4. **`missing_on_origin` does not change the patch's status.** Leaving it
   `active` means a rebuild keeps applying the hub's last-known copy of a
   branch the fork no longer has; auto-disabling would be surprising if the
   deletion on the fork was accidental. The state is surfaced on the patch
   record and the dashboard so an operator can decide. Revisiting this after
   real usage is deferred, per the input's own open question.

5. **Full fetch of every fork branch, not a filtered refspec per registered
   patch.** Simpler, and consistent with what the initial clone already
   does (fetches every branch as `refs/remotes/origin/*`). Fetching only
   registered branches would save bandwidth for forks with hundreds of
   branches but would also need `patch_registration_from_fork`'s
   single-branch fetch path to duplicate this one's credential handling;
   left as a follow-up if it turns out to matter.

6. **Backup refs (`refs/hub/replaced/<branch>`) instead of a database table
   of replaced tips.** The replaced commit is already in the object store;
   a ref is the smallest thing that keeps it reachable and fetchable
   (`git fetch <hub_url> refs/hub/replaced/<branch>`), and a table would
   duplicate git's own job without stopping `gc` from pruning the commit if
   the ref were forgotten. Advertising the namespace to fetch and rejecting
   pushes into it, and purging it on patch deletion/purge, belong to
   `patch_divergence_recovery`, which is where the ref is actually read back
   from; this spec only writes it.

7. **Two structs, two packages, one table.** `internal/workspace`'s `Patch`
   /`patchResponse` and `internal/carrypatch`'s `Patch`/`SQLPatchStore`
   both read the `patches` table independently today (verified in
   `internal/workspace/patch_store.go` and `internal/carrypatch/wire.go`).
   The three new columns and the "clear on hub mode" behaviour must be
   added to both: `internal/workspace/schema.go` for the migration and
   `patch_store.go` for the API-facing struct and response, and
   `internal/carrypatch`'s `PatchStore` interface gains the write path the
   sync handler needs (recording per-patch outcome and clearing on `hub`
   mode) without either package importing the other's concrete types.

8. **`origin_fetched` is always present, `patches_synced` only in `origin`
   mode.** A client scripting against the sync response needs a single
   boolean to know whether the fork was consulted at all, regardless of
   mode; the detailed per-branch array is only meaningful — and only
   populated — when there was a refresh to report.

## Dependencies

None. `01_run_rebuilds_in_a_detached_worktree` changes how the rebuild
applies patches once fetched (worktree vs. trunk); it does not touch sync,
the `patches` table, or where a patch branch's ref comes from, and this spec
does not touch the rebuild executor's working tree. The two specs modify
disjoint code paths in the same package.

## Verified External API

This spec's only external dependency is `github.com/go-git/go-git/v5`
(`v5.19.1`, per `go.mod`), used exactly the way the codebase already fetches
the `upstream` remote — verified by reading the existing, tested call site
rather than assuming a signature:

- `git.PlainOpen(path string) (*git.Repository, error)` — used in
  `internal/upstream/upstream.go`.
- `(*git.Repository).Remote(name string) (*git.Remote, error)` — same file.
- `(*git.Remote).FetchContext(ctx context.Context, o *git.FetchOptions) error`,
  with `git.FetchOptions{RemoteName, RefSpecs []config.RefSpec, Auth
  transport.AuthMethod, Tags git.TagMode}` and `git.NoTags` — same file. The
  new `origin` fetch reuses this exact shape with `RemoteName: "origin"` and
  `RefSpecs: []config.RefSpec{"+refs/heads/*:refs/remotes/origin/*"}`.
- `errors.Is(err, git.NoErrAlreadyUpToDate)` to treat an up-to-date fetch as
  success — same file.
- `config.RefSpec(string)` — same file and `internal/workspace/clone.go`.

No new external module is introduced.

## Open Questions

- **Should a patch whose branch is missing on the fork (`missing_on_origin`)
  be auto-disabled instead of left `active`?** Decision: leave the status
  untouched and only surface the state (Design Decision 4). Flagged because
  it is a real behaviour trade-off the input itself left open ("revisit
  after use"), and a reviewer with operational experience of fork deletions
  might prefer auto-disable.
- **Should the `origin` fetch be scoped to registered branch names instead
  of fetching every branch on the fork?** Decision: full fetch, matching the
  initial clone (Design Decision 5). Flagged because it is a real
  performance trade-off for forks with hundreds of branches, deferred by the
  input as a named follow-up rather than settled.
