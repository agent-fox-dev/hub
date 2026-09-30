---
spec_id: "05"
spec_name: "patch_divergence_recovery"
title: "Manual Divergence Recovery for Fork-Authoritative Patch Branches"
status: "active"
created_at: "2026-09-30T10:50:37.644094Z"
updated_at: "2026-09-30T10:50:37.644094Z"
intent_hash: "4f9308c1f7edb9bf6c7af6833a05aa18b98a9352542623041f187581316528a9"
schema_version: 2
source: "docs/prd/17-sync-patch-branches-from-the-fork.md"
---
## Intent

When a fork-authoritative patch branch (`PATCH_BRANCH_SOURCE=origin`, delivered
by `02_fork_authoritative_sync`) diverges from the fork under
`PATCH_DIVERGENCE_POLICY=report`, or an operator wants to force the fork's
tip onto the hub's copy immediately rather than waiting for the next sync, the
hub needs a manual recovery action and a way to see and retrieve whatever the
last automatic or manual replacement discarded. This spec is the stable
statement of that recovery surface: the `reset-to-origin` endpoint and CLI
command that force a patch branch to the fork's tip on demand, the
`refs/hub/replaced/*` backup namespace's protection and lifecycle in the git
server, visibility of a replaced tip on the patch-status dashboard, a
scripting-friendly failure mode for `afc workspace sync` when patches have
diverged, and the workflow documentation and ADR update that make the
fork-authoritative model the one the guide recommends first. It exists so
that discarding a branch's hub-side history is never a silent, unrecoverable
side effect of a sync the operator did not watch happen.

## Goals

- `POST /workspaces/:slug/patches/:id/reset-to-origin` moves a registered
  patch branch's local ref to its `refs/remotes/origin/<branch>` tip on
  demand, regardless of `PATCH_DIVERGENCE_POLICY`, backing up the discarded
  local tip under `refs/hub/replaced/<branch>` exactly as an automatic
  `replace` outcome does, and `afc patch reset-to-origin <slug> <patch-id>`
  calls it.
- An operator can find a tip a `replace` outcome (automatic or manual)
  discarded, and recover it with a plain `git fetch`, without needing
  database access or shell access to the hub host.
- The hub's git server never accepts a write into `refs/hub/replaced/*` from
  a client, so the backup namespace cannot be corrupted or spoofed by a push.
- A backup ref does not outlive the patch it belongs to: it is removed when
  the patch row is hard-deleted (`afc patch remove`) or purged after the
  soft-delete retention period.
- `afc workspace sync --fail-on-diverged` gives schedulers a distinct,
  scriptable exit code when a sync under `PATCH_DIVERGENCE_POLICY=report`
  left one or more patches diverged, without changing the default (exit 0)
  behaviour.
- `docs/carry_patch_workflow.md`'s Getting Started walkthrough presents the
  fork-authoritative flow (push to the fork, `afc patch add`, sync) as the
  primary path, the hub-push flow as the alternative for agent-driven
  workspaces, and its "Resolving conflicts after a failed rebuild" section
  describes both authority modes. ADR 01 moves from `Proposed` to `Accepted`.
- Every existing API response field, patch status, and job record shape is
  kept; this spec only adds a field, an endpoint, a CLI command and flag, and
  a git ref namespace.

## Non-goals

- **`PATCH_BRANCH_SOURCE`, `PATCH_DIVERGENCE_POLICY`, the sync-time origin
  fetch, bringing a patch branch to the fork's tip during sync, and the
  `origin_sync_state`/`origin_sha`/`origin_synced_at` patch columns.**
  Delivered by `02_fork_authoritative_sync`, which this spec depends on and
  does not re-specify; this spec only reads those columns and writes them
  from its own recovery action.
- **Resolving a fork-only branch at patch registration time.** That is
  `03_patch_registration_from_fork` and is unrelated to recovering from a
  divergence after registration.
- **`PUSH_PATCHES_TO_ORIGIN` and the git server's pre-receive
  refuse-or-forward and post-push mirror behaviour for ordinary patch-branch
  pushes.** That is `04_patch_branch_push_protection`, which this spec
  depends on for the per-ref pre-receive interception mechanism it reuses to
  reject writes into `refs/hub/replaced/*`.
- **Any database schema change.** The three columns this spec writes already
  exist (added by `02_fork_authoritative_sync`); a backup ref lives entirely
  in git, not in a table row.
- **Recording manually resolved conflicts into the hub's rerere cache from a
  fork-side fix.** Still an open question at the ADR level; unrelated to
  reading back a backup ref.
- **Scheduling the soft-deleted-patch purge routine into a running
  background job.** `carrypatch.PurgeExpiredDeletedPatches` is not invoked
  anywhere in `cmd/af-hub/main.go` today; that is a pre-existing gap this
  spec does not close. This spec only changes what the routine also cleans
  up on the (currently manual/test-only) occasions it runs.
- **A dedicated `GET /workspaces/:slug/patches/:id` endpoint.** No such
  endpoint exists in the codebase today (only list, add, update, delete,
  restore, and reorder). Adding one, purely to carry a `replaced_sha` field
  that is otherwise idle between recovery events, is not worth a git ref
  lookup on a new, otherwise-unneeded per-request read path; see Design
  Decision 4 for where `replaced_sha` is exposed instead.
- **Further restructuring of the workflow guide beyond the Getting Started
  walkthrough, the Remotes/API-reference tables, and the conflict-resolution
  section.** The guide's other sections (rebuild algorithm, merge detection,
  rerere) are unaffected by this spec and are not rewritten.

## Background

`internal/workspace/patch_handlers.go` registers `POST`, `GET`, `PATCH`,
`DELETE`, `.../restore`, and `.../reorder` on `/workspaces/:slug/patches`
(`internal/workspace/routes.go`); there is no `GET .../patches/:id`. Every
one of those handlers builds its JSON response through
`patchResponse(*Patch)` in `internal/workspace/patch_store.go`, an unexported
function reading an unexported `Patch` struct scanned from the `patches`
table. `internal/carrypatch` reads the same table through its own,
independently-defined `Patch` struct and `SQLPatchStore`
(`internal/carrypatch/wire.go`) — the two packages do not share types, by
design (`internal/workspace` must not import git- or variable-dependent code,
per the comment on `BranchCheckFunc`; `02_fork_authoritative_sync`'s Design
Decision 7 already established that a column added to `patches` is exposed
independently by both packages).

Carry-patch HTTP endpoints that need git operations, a workspace lock, or a
workspace variable live in `internal/carrypatch/api.go` and are registered
from `cmd/af-hub/main.go` (`RegisterSyncRoutes`, `RegisterPatchStatusRoutes`,
`RegisterRebuildRollbackRoutes`, ...), each taking a small `*APIConfig`
struct of dependencies (`DB`, `WorkspaceRoot`, `NewGitRunner`, `GetVariable`,
`PatchStore`, `Audit`). `handleRollbackRebuild` is the closest existing
analogue to the endpoint this spec adds: it takes `wslock.TryLock(slug)`,
opens a `GitRunner` on the trunk, writes a ref (`git branch -f`), and returns
a small JSON object — no new database write. `handlePatchStatus` is the
closest analogue for read-heavy, best-effort-enriched responses: it already
tolerates a missing or inaccessible filesystem resource (the rr-cache
directory) by leaving a field at its zero value rather than failing the
request (16-REQ-6.E4).

`internal/gitcmd.GitRunner` (wrapped for carry-patch by
`carrypatch.GitRunnerAdapter`) already provides `Run(ctx, args...)` for
arbitrary git subcommands, `UpdateRef(ctx, ref, sha)` for `git update-ref`,
and `HardReset(ctx, ref)` for `git reset --hard`; `rebuild_executor.go`
already captures and restores "whatever branch was checked out before" via
`git symbolic-ref --short -q HEAD`. `internal/gitserver/gitserver.go`'s
`WorkspaceLoader.Load` already wraps the trunk's `storer.Storer` once
(`thinPackSafeStorer`) for an unrelated reason; `04_patch_branch_push_protection`
adds a second wrapper that intercepts individual reference writes during
`git-receive-pack` to reject or forward specific branches without
`internal/gitserver` importing `internal/carrypatch` — this spec's
`refs/hub/replaced/*` rejection is a third, unconditional rule at that same
interception point, not a new mechanism.

`internal/carrypatch/wire.go`'s `SoftDeletePatch` / `PurgeDeletedPatches` /
`PurgeExpiredDeletedPatches` implement the existing 7-day soft-delete
retention for patches whose branch was detected as merged upstream. Nothing
in `cmd/af-hub/main.go` currently calls `PurgeExpiredDeletedPatches` — it is
exercised only by `internal/carrypatch/soft_delete_test.go` — so the routine
this spec extends is not on a running schedule today; that gap predates this
spec and is not closed by it. Separately, `DELETE /workspaces/:slug/patches/:id`
(`handleRemovePatch` → `deletePatchAndCompact`) is an immediate, unconditional
hard delete with no retention window at all; `afc patch remove` is a thin
wrapper over it.

`docs/carry_patch_workflow.md`'s Getting Started walkthrough and "Resolving
conflicts after a failed rebuild" section currently describe only the
hub-authoritative flow (branches validated and pushed through the hub's git
server, conflicts resolved directly in the trunk so rerere records them).
ADR `docs/adr/01-choose-the-authority-for-patch-branches.md` is `Proposed`
and states in its Decision section that "the workflow guide will present
this as the primary workflow and the hub model as the alternative for
agent-driven workspaces" once the whole split lands; this is the last spec of
that split.

## Requirements

1. **`reset-to-origin` gating.** `POST /workspaces/:slug/patches/:id/reset-to-origin`
   requires the workspace to be `carry_patch` mode, `active` status, and clone
   `ready` (400 otherwise, using the same phrasing already used by the
   sibling carry-patch endpoints, e.g. "reset-to-origin is only supported for
   carry_patch workspaces"), and requires `PATCH_BRANCH_SOURCE` to currently
   read `origin` (400 `reset-to-origin requires PATCH_BRANCH_SOURCE=origin`
   otherwise, using `apikit.WriteAPIErrorWithType` with a
   `patch_branch_source_mismatch` type). `PATCH_DIVERGENCE_POLICY` is not
   consulted: the endpoint always replaces, which is the point of an
   explicit manual action (mirrors the input's "regardless of policy").
   Authentication and authorization match the other single-patch endpoints:
   API key or admin token, or a PAT with `patches:write`; workspace
   ownership enforced the same way `authorizeWorkspace` already enforces it
   for `carrypatch`'s other endpoints (404 for a non-owner or nonexistent
   workspace, anti-enumeration).

2. **Patch lookup and guards.** The patch is looked up by `:id` scoped to
   `:slug`; a miss is 404 `patch not found`. A patch whose status is
   `deleted` is rejected with 409 `patch is soft-deleted; restore it first`
   (the same message `PATCH .../patches/:id` already uses for the same
   condition). A patch whose `branch_name` equals the workspace's
   `integration_branch` is rejected with 400 `cannot reset the integration
   branch` (defensive: registration already prevents this row from
   existing, matching PB-7's defensive stance in `02_fork_authoritative_sync`).
   Any other patch status (`active`, `conflict`, `disabled`,
   `merged_upstream`) is eligible.

3. **The reset itself.** Under the per-workspace lock
   (`wslock.TryLock(slug)`; busy returns 409 `workspace_busy` with the same
   message `handleRollbackRebuild` already uses), in the trunk:
   - Resolve `refs/remotes/origin/<branch>`. If it does not exist, fail with
     400 `branch not found on origin; sync the workspace first` — reset-to-origin
     never fetches; it only promotes whatever `origin` state the last sync
     or clone already brought in, keeping this spec independent of the
     worktree PRD's scope and of sync's credential path.
   - Resolve `refs/heads/<branch>`. If it exists, write its current SHA to
     `refs/hub/replaced/<branch>` (`update-ref`, overwriting any earlier
     backup for that branch) before moving it; if it does not exist, skip
     the backup (there is nothing to back up) and simply create the local
     branch at the origin tip. In both cases the local branch ends at the
     origin tip via `update-ref`, never `checkout` or `reset`. This
     unconditional backup-then-move happens even when the local tip already
     equals the origin tip, keeping the operation idempotent and its
     semantics uniform rather than adding a "no-op" special case.
   - If `<branch>` is the trunk's current checkout (`git symbolic-ref
     --short -q HEAD`), reset the working tree to the new tip afterwards
     (`HardReset`), the same recovery the post-push hook and
     `02_fork_authoritative_sync`'s fork-refresh already perform for the
     same situation.
   - Update the patch row's `origin_sync_state` to `in_sync`, `origin_sha`
     to the origin tip, and `origin_synced_at` to now, so the patch-status
     dashboard reflects the reset immediately rather than waiting for the
     next sync. `status`, `position`, `description`, and `upstream_pr_url`
     are left untouched — recovering the ref is not the same as clearing a
     `conflict` status, which remains a separate, explicit `PATCH
     .../patches/:id --status active` step before the next rebuild, exactly
     as the existing hub-mode conflict-recovery flow already documents.
   - Emit one `hub.patch.replace` audit event (the same event type
     `02_fork_authoritative_sync`'s automatic replace already emits) with
     the branch name, the discarded local SHA (when a backup was written),
     and the new origin SHA, actor attributed to the calling credential
     rather than `system`.

4. **Response.** 200 OK with the patch record in the same JSON shape
   `GET`/`PATCH .../patches` already return (including `origin_sync_state`,
   `origin_sha`, `origin_synced_at` from `02_fork_authoritative_sync`), plus
   `replaced_sha`: the SHA written to `refs/hub/replaced/<branch>` in this
   call, present only when a backup was actually written (i.e., absent when
   the local branch did not exist beforehand). Built by a direct query
   against the `patches` table from within `internal/carrypatch` (the same
   "read the row directly" approach `handlePatchStatus` already uses),
   rather than by exporting `internal/workspace`'s response helpers or
   introducing a shared type between the two packages.

5. **`afc patch reset-to-origin <workspace-slug> <patch-id>`.** Sends the
   above endpoint with no request body and prints the returned patch JSON,
   following the exact shape of `afc patch restore`. Exit 0 on success; exit
   1 for any error response (patch or workspace not found, wrong
   `PATCH_BRANCH_SOURCE`, branch missing on origin, soft-deleted patch,
   workspace busy, network error). Requires `patches:write` for PATs.

6. **`refs/hub/replaced/*` is never writable by a push.** The hub's git
   server already advertises every ref in the trunk unconditionally
   (`writeRefAdvertisement` iterates `AdvRefs.References` with no filtering),
   so a backup ref is fetchable (`git fetch <hub-url>
   refs/hub/replaced/<branch>`) as soon as it exists, with no code change.
   A push whose target ref matches `refs/hub/replaced/*` is rejected — for
   any workspace, in any `PATCH_BRANCH_SOURCE` mode, unconditionally — using
   the same per-ref pre-receive interception `04_patch_branch_push_protection`
   introduces for patch-branch guarding, so `internal/gitserver` gains no
   second, competing interception path. The rejection message names the
   namespace (e.g. `refs/hub/replaced/* is a hub-managed backup namespace
   and cannot be pushed to`); other refs in the same push are unaffected,
   under the same partial-acceptance behaviour `04_patch_branch_push_protection`
   already established (or whatever equivalent mechanism it landed on, if
   go-git's per-command reporting did not support the storer-wrapper
   approach that spec assumed).

7. **Backup ref retention.** `refs/hub/replaced/<branch>` for a given patch
   is deleted (`git update-ref -d`, best-effort, errors logged not raised)
   in two cases: (a) `DELETE /workspaces/:slug/patches/:id` (and so `afc
   patch remove`), immediately after the row is hard-deleted, via a new
   hook registered from `main.go` in the same style as
   `RegisterBranchCheckHook` (so `internal/workspace` still performs no git
   operations directly); (b) when a soft-deleted patch row is purged after
   the retention period. For (b), `PurgeDeletedPatches`/
   `PurgeExpiredDeletedPatches` (`internal/carrypatch/wire.go`) is extended
   to know the workspace slug and branch name of every row it is about to
   remove (a new `PatchStore` accessor, or a query added ahead of the
   existing `DELETE`, either way returning that information to the caller
   before the rows disappear) and, given a `WorkspaceRoot` and a
   `NewGitRunner` factory now threaded through
   `PurgeExpiredDeletedPatches`, deletes the corresponding backup ref in
   each workspace's trunk, best-effort, logging rather than failing the
   purge when a trunk is missing (e.g., the workspace was itself archived)
   or the ref deletion errors. `PurgeExpiredDeletedPatches`'s only existing
   caller is its own test, which is updated for the new parameters; wiring
   the routine into a running schedule remains out of scope (see Non-goals).

8. **Patch-status dashboard gains `replaced_sha`.** `GET
   /workspaces/:slug/patch-status`'s per-patch entry gains `replaced_sha`
   (string, `omitempty`), the current tip of `refs/hub/replaced/<branch>`
   when that ref exists, computed with one `rev-parse --verify` per patch
   using a `GitRunner` opened on the trunk. `PatchStatusAPIConfig` gains a
   `NewGitRunner func(repoPath string) (GitRunner, error)` field, wired in
   `main.go` to the same `cpGitRunnerFactory` every other carry-patch
   endpoint already uses. When `NewGitRunner` is nil (as in existing tests
   that construct the config without it) or opening the runner fails (e.g.,
   clone not ready), `replaced_sha` stays absent for every patch — the same
   fail-open convention `total_rerere_resolutions` already uses for a
   missing rr-cache directory (16-REQ-6.E4) — rather than failing the whole
   dashboard request.

9. **`afc workspace sync --fail-on-diverged`.** A new boolean flag on `afc
   workspace sync`. After the sync response is printed (immediately in the
   non-`--wait` path; before any rebuild-status polling in the `--wait`
   path — checked first so a scheduler is not blocked waiting on a rebuild
   before learning about the divergence), if the flag is set and the
   response's `patches_diverged` array (from `02_fork_authoritative_sync`,
   populated only under `PATCH_DIVERGENCE_POLICY=report`) is non-empty, the
   command exits 3 instead of 0. The flag has no effect when
   `patches_diverged` is absent or empty (hub mode, `replace` policy, or a
   clean sync) — the command's default exit-0 behaviour for those cases is
   unchanged.

10. **Documentation and ADR.** `docs/carry_patch_workflow.md`'s Getting
    Started walkthrough is restructured so steps 1–4 (create workspace with
    `PATCH_BRANCH_SOURCE=origin`, push the patch branch to the fork, `afc
    patch add`, `afc workspace sync`) are the primary path, with the
    hub-push flow kept as a clearly labelled alternative for agent-driven
    workspaces; "Resolving conflicts after a failed rebuild" gains an
    `origin`-mode variant (fix the branch in a local clone of the fork, push
    to the fork, sync or `afc patch reset-to-origin`, then `--status active`
    and rebuild — noting rerere will not have recorded a resolution made
    outside the trunk); the Day-to-day operations section documents `afc
    patch reset-to-origin` and the `refs/hub/replaced/*` recovery path; the
    API reference summary table gains the new endpoint row; and the `afc
    workspace sync` CLI reference's stale "Your fork (`origin`) is never
    fetched by sync" note is corrected to describe both `PATCH_BRANCH_SOURCE`
    modes. `docs/cli.md`'s `afc workspace sync` section documents
    `--fail-on-diverged` and exit code 3, and gains a new `afc patch
    reset-to-origin` section modelled on `afc patch restore`. `docs/api.md`
    documents the new endpoint (Patch Endpoints section) and the
    patch-status `replaced_sha` field. `docs/openapi.yaml` gains the new
    path, its request/response schemas (including a `replaced_sha` property
    on the `Patch` schema, documented as populated only by this endpoint and
    the patch-status dashboard), and the `--fail-on-diverged`-driven exit
    behaviour is CLI-only and not an OpenAPI concern. ADR 01's status
    changes from `Proposed` to `Accepted`. An erratum is filed under
    `docs/errata/` recording the two points where this spec's design departs
    from the literal original input: no dedicated `GET
    /workspaces/:slug/patches/:id` endpoint was added (Non-goals), and
    `reset-to-origin` requires `PATCH_BRANCH_SOURCE=origin` rather than
    working in `hub` mode too (Design Decision 3).

## Design Decisions

1. **`reset-to-origin` lives in `internal/carrypatch`, not
   `internal/workspace`.** It needs a workspace lock, a `GitRunner`, and a
   workspace variable read — exactly the dependencies `internal/workspace`
   is structurally forbidden from having (per the existing comment on
   `BranchCheckFunc` and `02`/`03`'s precedent of solving this with a hook).
   Unlike the branch-check hook, this endpoint has no natural place to sit
   as a one-function hook called from an `internal/workspace` handler: it
   has its own auth, its own error shapes, and its own response body. It is
   therefore a new route registered directly on the API group from
   `main.go`, following the `RegisterRebuildRollbackRoutes` /
   `RegisterPatchStatusRoutes` pattern already used for every other
   carry-patch endpoint that needs the same dependencies.

2. **The response is built from a fresh, direct SQL read, not from
   `internal/workspace`'s `patchResponse`.** `patchResponse` and the `Patch`
   struct it serializes are unexported. Exporting them (or introducing a
   shared response type between the two packages) would be a larger, cross-
   cutting change for one endpoint; `handlePatchStatus` already establishes
   the precedent of `internal/carrypatch` reading the `patches` table
   directly and shaping its own response JSON to match the documented
   schema field names.

3. **`reset-to-origin` requires `PATCH_BRANCH_SOURCE=origin`; it does not
   work in `hub` mode.** The input's DR-1 says the endpoint moves a branch
   to the origin tip "regardless of policy" — read here as regardless of
   `PATCH_DIVERGENCE_POLICY` (`replace` vs. `report`), not regardless of
   `PATCH_BRANCH_SOURCE`. In `hub` mode, `refs/remotes/origin/<branch>` is
   whatever the initial clone or an incidental fetch happened to bring in;
   sync never refreshes it, so "the origin tip" carries none of the
   authority meaning it has in `origin` mode, and forcing a hub-authoritative
   branch to a stale, unrefreshed tracking ref would be actively misleading.
   Gating the endpoint to `origin` mode keeps "the fork is the single
   source of truth this action pulls from" true whenever it can be invoked.

4. **`replaced_sha` is exposed on the patch-status dashboard and the
   `reset-to-origin` response, not on a new single-patch `GET` endpoint or
   on the general `Patch` schema used by list/add/update/restore.** No `GET
   .../patches/:id` exists today, and the common, high-frequency patch read
   paths (list, the create/update/restore responses) currently do zero git
   operations; adding a `rev-parse` per patch to all of them to populate a
   field that is only interesting right after a replacement would add a
   subprocess call to paths that do not need one. The patch-status
   dashboard already exists specifically to answer "what is the state of my
   patch stack" and, after this spec, already reports
   `origin_sync_state`/`origin_sha`/`origin_synced_at`; adding one more git
   lookup there, and to the response of the one endpoint that just performed
   the write, covers the stated need ("so an operator can recover a
   replaced tip") without the broader cost.

5. **The always-backup-then-move semantics apply even when local already
   equals origin.** Treating "already in sync" as a no-op that skips the
   backup would need its own branch of logic and its own response shape
   (no `replaced_sha`) for a case that is harmless to handle uniformly: the
   backup ref simply ends up identical to the new head. Simplicity here
   outweighs the minor redundancy of a same-value backup write.

6. **`PurgeExpiredDeletedPatches` gains parameters rather than a second,
   parallel purge routine.** Its only caller today is its own test, so
   changing its signature to also accept `WorkspaceRoot` and a
   `NewGitRunner` factory (needed to delete a backup ref per purged row) has
   no production call site to break, and keeps "purge soft-deleted patches"
   a single routine rather than splitting DB cleanup and ref cleanup into
   two things an operator has to remember to run together.

7. **The backup-ref cleanup hook for `afc patch remove` follows the
   branch-check hook's shape.** `handleRemovePatch` already knows the
   branch name (it fetches the patch row before deleting, for audit
   metadata) and only needs a git operation performed after a successful
   delete; a `func(slug, branchName string)` hook registered from `main.go`,
   called best-effort and its errors logged, matches the existing
   `RegisterBranchCheckHook` pattern exactly and needs no new import in
   `internal/workspace`.

8. **`--fail-on-diverged` checks the sync response before any `--wait`
   polling, not after.** A scheduler using this flag wants to know about
   divergence as soon as the sync itself finishes, not after however long a
   possibly-unrelated rebuild takes to complete; checking first also means
   the exit code is defined identically whether or not `--wait` is passed.

9. **`refs/hub/replaced/*` push-rejection is unconditional, not gated by
   `PATCH_BRANCH_SOURCE` or workspace mode.** It is a hub-reserved
   namespace regardless of a workspace's authority setting; checking only a
   ref-name prefix (no workspace lookup, no variable read) is also the
   cheapest possible guard, so applying it to every workspace costs nothing
   for the common case where the namespace is never touched.

## Dependencies

| Spec | Reason |
|------|--------|
| `02_fork_authoritative_sync` | Provides `PATCH_BRANCH_SOURCE` (read to gate the endpoint), the `refs/hub/replaced/<branch>` backup-ref convention this endpoint reuses for its own replace, and the `origin_sync_state`/`origin_sha`/`origin_synced_at` patch columns this endpoint writes and the patch-status dashboard already exposes. |
| `04_patch_branch_push_protection` | Provides the git server's per-ref pre-receive interception mechanism (the second `storer.Storer` wrapper / hook registration around `git-receive-pack`) that this spec reuses, unconditionally, to reject pushes into `refs/hub/replaced/*`. |

## Verified External API

This spec adds no new external package dependency. It reuses internal,
already-tested wrappers over `github.com/go-git/go-git/v5` rather than
calling go-git directly:

- `internal/gitcmd.GitRunner.Run(ctx, args...) (string, error)`,
  `.UpdateRef(ctx, ref, sha string) error`, and `.HardReset` (via
  `carrypatch.GitRunnerAdapter.HardReset(ctx, ref string) error`) — verified
  in `internal/gitcmd/gitcmd.go` and exercised by existing rebuild and merge
  code (`internal/merge/handler.go`'s `update-ref -d` call is the model for
  this spec's backup-ref deletion).
- `wslock.TryLock(slug string) (unlock func(), ok bool)` — verified in
  `internal/gitserver/handlers.go`'s `updateHeadSHA` and
  `internal/carrypatch/api.go`'s `handleRollbackRebuild`, the direct model
  for this spec's lock usage.
- The git-server-side interception this spec's `refs/hub/replaced/*`
  rejection relies on is the one `04_patch_branch_push_protection` builds
  and already flags as needing implementation-time confirmation against the
  installed `go-git/v5` `plumbing/transport/server` and `plumbing/storer`
  packages (whether a single reference-write failure is reported as that
  command's own status in `packp.ReportStatus` rather than failing the
  whole `ReceivePack` call). This spec does not re-verify that assumption;
  it inherits whatever mechanism `04_patch_branch_push_protection` actually
  ships.

## Open Questions

- **Should `reset-to-origin` be gated to `PATCH_BRANCH_SOURCE=origin`, or
  usable in `hub` mode too against whatever `refs/remotes/origin/<branch>`
  happens to hold?** Decision: gated to `origin` mode (Design Decision 3).
  Flagged because the input's own wording ("regardless of policy") is
  ambiguous between the two readings, and a reviewer who intended the
  broader reading would need to relax this gate.
- **Where should `replaced_sha` be exposed, given the input names a `GET
  /workspaces/:slug/patches/:id` endpoint that does not exist in this
  codebase?** Decision: the patch-status dashboard and the
  `reset-to-origin` response only, not a new single-patch `GET` endpoint or
  the general `Patch` schema (Design Decision 4, Non-goals). Flagged because
  it is the clearest point where this spec's design departs from the
  literal input, and an erratum is required to record it (Requirement 10).
- **Should the purge routine's new git-cleanup behaviour be exercised by a
  production schedule as part of this spec?** Decision: no — wiring
  `PurgeExpiredDeletedPatches` into a running background job is a
  pre-existing gap this spec does not close (Non-goals). Flagged because a
  reviewer might expect "retention" work to include making the retention
  routine actually run.
