---
spec_id: "03"
spec_name: "patch_registration_from_fork"
title: "Resolve Patch Branches From the Fork at Registration Time"
status: "active"
created_at: "2026-09-30T09:48:54.124918Z"
updated_at: "2026-09-30T09:48:54.124918Z"
intent_hash: "ff9100ecce9e3f53ace49a7e108eaf078267c52ddc500f5be45b7aecc470d927"
schema_version: 2
source: "docs/prd/17-sync-patch-branches-from-the-fork.md"
---
## Intent

Patch registration (`POST /workspaces/:slug/patches`, and `afc patch add`
through it) must be able to find a branch that exists on the fork but has no
local ref yet in the hub's trunk clone, in either `PATCH_BRANCH_SOURCE`
authority mode, so that a patch can be registered as soon as it exists on
GitHub without first being pushed through the hub's git server. This spec is
the stable statement of that registration-time resolution: the validation
order it follows, when it is allowed to create a local branch or fetch a
single branch from the fork, and how the outcome is recorded in the
`hub.patch.create` audit trail.

## Goals

- `POST /workspaces/:slug/patches` (single and batch) and `afc patch add`
  (which only ever sends a single object) accept a `branch_name` that exists
  as `refs/remotes/origin/<name>` — the fork's tracking ref already present
  from the initial clone or a previous fetch — without `--skip-branch-check`,
  in both `hub` and `origin` `PATCH_BRANCH_SOURCE` modes.
- In `origin` mode only, when neither `refs/heads/<name>` nor
  `refs/remotes/origin/<name>` exists locally, registration performs a
  single-branch fetch of that one ref from the fork and retries, so a branch
  pushed to GitHub moments ago and never fetched by anything else is still
  registrable.
- The three resolution attempts (local branch, tracking ref, single-branch
  fetch) share one failure message: registration still fails with `400` and
  a message naming both places checked when none of them find the branch.
- Batch registration applies the same resolution to every element before any
  row is inserted, keeping the batch all-or-nothing; local branches created
  for elements that did resolve are left in place even if a later element in
  the same batch fails.
- Every patch a request creates — single or batch — has its `hub.patch.create`
  audit event record which of the three paths resolved its branch.
- `skip_branch_check` keeps its exact current meaning: none of this
  resolution runs, and no local branch is created as a side effect.
- No `afc patch add` flag changes: the richer resolution is entirely
  server-side, behind the same `branch_name` field the CLI already sends.

## Non-goals

- **Bringing registered patch branches to the fork's tip on sync, the
  `PATCH_DIVERGENCE_POLICY` divergence handling, the sync response and
  persisted per-patch sync-state fields, and the `PATCH_BRANCH_SOURCE`
  workspace variable itself.** All delivered by `02_fork_authoritative_sync`,
  which this spec depends on and does not re-specify.
- **`PUSH_PATCHES_TO_ORIGIN` and any git-server pre-receive/post-push
  behaviour for patch branches** (refusing a direct hub push in `origin`
  mode unless it can forward to the fork, mirroring an accepted hub push in
  `hub` mode). That is `patch_branch_push_protection`.
- **`POST /workspaces/:slug/patches/:id/reset-to-origin`, `afc patch
  reset-to-origin`, `replaced_sha`, `refs/hub/replaced/*` advertisement and
  retention, `--fail-on-diverged`, and rewriting the workflow guide's Getting
  Started walkthrough and Remotes table to present the fork-authoritative
  flow as primary.** That is `patch_divergence_recovery`.
- **Any database schema change.** This spec persists nothing beyond the
  existing `patches` row and the git refs it was already free to write
  (a local branch); no new column, no new nullable state.
- **Standard (non `carry_patch`) workspaces.** `POST /patches` already
  rejects them with `400` before any branch resolution runs; unchanged.
- **Distinguishing a genuinely missing branch from a slow or flaky
  single-branch fetch with a different HTTP status.** Both fold into the
  same `400`, for the reason given in Design Decision 5.

## Background

A carry-patch workspace's trunk (`<workspace_root>/<slug>/trunk`) is cloned
with `origin` (the fork) and `upstream` remotes. The initial clone brings
every fork branch across as `refs/remotes/origin/*`; only the workspace's
own branch becomes a local branch (`refs/heads/<branch>`). Today,
`POST /workspaces/:slug/patches` (`internal/workspace/patch_handlers.go`,
`handleAddPatchSingle` and `handleAddPatchBatch`) validates a submitted
`branch_name` by calling `branchCheckHook`, a package-level `BranchCheckFunc`
(`internal/workspace/branch_check_hook.go`) registered from
`cmd/af-hub/main.go`:

```go
workspace.RegisterBranchCheckHook(func(slug, branchName string) error {
    runner, err := cpGitRunnerFactory(filepath.Join(cfg.Workspace.Path, slug, "trunk"))
    ...
    _, err = runner.Run(context.Background(), "rev-parse", "--verify", branchName)
    return err
})
```

This only ever checks `refs/heads/<name>` (rev-parse's default resolution for
a bare name falls back to `refs/heads/` before remotes). A branch that exists
only on the fork — including one the initial clone already fetched into
`refs/remotes/origin/<name>` — fails this check and registration requires
`skip_branch_check`, after which the rebuild skips the patch as
`branch_not_found` until someone pushes the branch to the hub. `afc patch
add` (`internal/cli/patch_cmd.go`) is a thin wrapper that posts `branch_name`
and the other fields verbatim; it needs no change here because the
resolution this spec adds is entirely behind that same field.

`02_fork_authoritative_sync` (a dependency of this spec, not restated here)
introduces the `PATCH_BRANCH_SOURCE` workspace variable (`hub` default,
`origin`) and, for the `origin` fetch it adds to sync, reuses
`workspace.ResolveCloneAuth` (`internal/workspace/clone_auth.go`) for fork
credentials in the same order the clone and the integration-branch push
already use (`GIT_PAT`, then `GIT_USERNAME`/`GIT_PASSWORD`, then none) and a
go-git fetch verified against the existing, tested `internal/upstream`
package. This spec's single-branch fetch reuses the same credential function
and the same go-git call shape for the same remote, so a workspace's fork
credentials are resolved identically wherever they are needed.

`internal/workspace` does not read workspace variables or perform git
operations directly — by design, per the comment on `BranchCheckFunc`, to
avoid an import cycle with the packages (`internal/carrypatch`,
`internal/gitcmd`) that do. Every variable- or git-dependent decision in this
package today is delegated to a hook registered from `main.go`, which does
have `store.GetVariableValue`, `cpGitRunnerFactory` and
`workspace.ResolveCloneAuth` in scope — the same shape `main.go` already
uses for `workspace.RegisterCarryPatchSyncHook`. This spec's richer
resolution (read `PATCH_BRANCH_SOURCE`, check two refs, conditionally fetch,
create a branch) is naturally another such hook, replacing the current
one-line `rev-parse` closure without changing `internal/workspace`'s import
graph.

`hub.patch.create` is emitted today only from `handleAddPatchSingle`, with
metadata `{"branch_name", "position"}` (`emitHubAudit`,
`internal/workspace/audit_emit.go`; `internal/audit.HubEvent`).
`handleAddPatchBatch` inserts rows through the same store functions but emits
no audit event for any of them — a pre-existing gap. Ref writes and fetches
against the trunk are otherwise always protected by `wslock.TryLock(slug)`
(sync, reclone, archive, rebuild); the current branch check takes no lock
because it only reads.

ADR `docs/adr/01-choose-the-authority-for-patch-branches.md` already states
the target behaviour this spec implements (point 4: "Registering a patch
resolves the fork's copy in both modes... This removes the
`--skip-branch-check` workaround regardless of authority").

## Requirements

1. **Resolution order.** For a submitted `branch_name`, in both
   `PATCH_BRANCH_SOURCE` modes and unless `skip_branch_check` is `true`,
   registration checks, in order: (a) `refs/heads/<name>` already exists —
   resolution `local`, nothing written; (b)
   `refs/remotes/origin/<name>` exists — resolution `origin_tracking`, and
   the local branch is created at that tip (`git update-ref
   refs/heads/<name> <tracking-ref sha>`, never `checkout` or `git branch`,
   so the trunk's working tree is untouched) before the row is inserted;
   (c) only when `PATCH_BRANCH_SOURCE` is `origin`, a single-branch fetch of
   `+refs/heads/<name>:refs/remotes/origin/<name>` from the fork, using the
   same credential resolution as `02_fork_authoritative_sync`'s origin
   fetch, followed by the same tracking-ref check and branch creation as
   (b) — resolution `origin_fetch`. `PATCH_BRANCH_SOURCE` is read fresh for
   every request (never cached); an error reading it (unset, or a genuine
   read failure) is treated as `hub`, matching how every other on/off
   carry-patch workspace variable in this codebase already defaults on a
   read error.

2. **Failure.** If none of (a)–(c) resolves the branch, the request fails
   with `400` and the message `branch does not exist in repository or on
   origin` for a single request, or `patch[<i>]: branch does not exist in
   repository or on origin` for the failing element of a batch — replacing
   the current `branch does not exist in repository` text in both call
   sites.

3. **`skip_branch_check` is unchanged.** When set, none of requirement 1
   runs: no ref is read, no branch is created, no fetch happens. This is
   true in both modes and both request shapes, exactly as today.

4. **Workspace lock around mutation.** Steps (b) and (c) of requirement 1
   write a ref (and, for (c), also touch the network and
   `refs/remotes/origin/*`); registration acquires the same per-workspace
   lock (`wslock.TryLock`) that sync, reclone and rebuild already use before
   attempting either, and releases it before the patch row is inserted. If
   the lock is held by another operation, the request fails with `409` and
   the same `workspace_busy` error the other lock holders already return,
   before any git or database write. Step (a) — the existing
   `rev-parse --verify refs/heads/<name>` check — takes no lock, exactly as
   today, since it only reads and is the common case (branch already
   local).

5. **Batch semantics.** `handleAddPatchBatch` applies requirement 1 to every
   element, in order, before building the rows to insert; the existing
   fail-fast behaviour (stop at the first element that fails validation,
   reporting its index) is kept. A local branch created by requirement 1 for
   an element earlier in the batch than one that later fails is left in
   place — it is a harmless ref, and a later `POST /patches` call finds it
   under resolution `local`. The whole batch's resolution phase runs under
   one lock acquisition (requirement 4); the lock is released before the
   existing atomic insert.

6. **Audit metadata.** The `hub.patch.create` event for every inserted patch
   — single or batch, closing the batch path's current lack of any audit
   emission — carries `branch_resolution` in its metadata, one of `local`,
   `origin_tracking`, `origin_fetch`, alongside the existing `branch_name`
   and `position`. `branch_resolution` is omitted from the metadata when
   `skip_branch_check` bypassed resolution entirely (requirement 3), since
   no resolution was attempted.

7. **Credential-resolution failures are distinct from "not found".** A
   genuine failure resolving the fork's credentials for the step 1(c) fetch
   (a secrets-store error, not "no credentials configured") aborts the
   request with `502` and a `credential resolution failed` message, the same
   outcome `internal/workspace/sync_handler.go` already returns for the
   equivalent failure, rather than being folded into the `400` of
   requirement 2. Any other failure of the step 1(c) fetch itself (branch
   not present on the fork, transient network error) is not distinguished
   further and simply means step (c) did not resolve the branch, per
   Design Decision 5.

8. **Documentation.** `docs/api.md`'s `POST /api/v1/workspaces/:slug/patches`
   section and `docs/openapi.yaml`'s `addPatches` operation are updated to
   describe the three-step resolution order and the new `400` message text.
   `docs/carry_patch_workflow.md`'s "3. Add patches" step, which currently
   says branches are "validated against the workspace repository at add
   time," is corrected to describe the tracking-ref and (`origin` mode)
   fetch fallback in one or two sentences; the larger restructuring of that
   guide's Getting Started walkthrough is `patch_divergence_recovery`'s
   work (see Non-goals). No change to `docs/cli.md` or
   `docs/examples/AGENTS_carry_patch.md`: neither documents a CLI flag this
   spec adds or removes.

## Design Decisions

1. **Extend the existing branch-check hook rather than add a second hook.**
   `internal/workspace/branch_check_hook.go` already exists precisely to let
   `main.go` supply git-dependent behaviour to the workspace package without
   an import cycle. This spec's richer resolution (read a variable, check
   two refs, conditionally fetch, create a branch, and report which path
   succeeded) is a natural extension of that same seam, not a new one:
   `main.go`'s registration closure grows from a one-line `rev-parse` call
   into a call against `internal/carrypatch`'s git-runner and fetch
   machinery, following the same `NewCarryPatchSyncHook`-style factory
   pattern already used for the sync hook.

2. **Fork credentials, not upstream credentials, for the single-branch
   fetch.** A patch branch lives on the fork; `workspace.ResolveCloneAuth`
   is what the clone, the integration-branch push, and
   `02_fork_authoritative_sync`'s origin fetch already use for that same
   remote. Reusing it here means a workspace's fork credentials are resolved
   the same way everywhere they are needed, with nothing new to configure.

3. **`update-ref`, never `checkout` or `git branch`, to create the local
   branch.** Matches `02_fork_authoritative_sync`'s convention for every ref
   write it makes and keeps registration from touching the trunk's working
   tree — there is no reason to check out a branch that is merely being
   registered.

4. **Lock only the mutating steps, not the existing read-only fast path.**
   The common case — the branch is already local — is an unchanged,
   unlocked `rev-parse --verify`; only promoting a tracking ref or fetching
   a new one needs the same protection sync and reclone already require for
   any trunk ref write, so this spec adds no new contention to the
   already-fast common path.

5. **A single-branch fetch failure folds into the same `400`, unlike
   sync's `502`.** Sync's origin fetch (`02_fork_authoritative_sync`) covers
   every registered branch in one call; if it fails, the whole sync must
   abort loudly because nothing after it can be trusted. Registration's
   fetch targets exactly one branch as one of three fallback attempts for
   one patch; a miss here is not a systemic failure; the input's own PR-1
   only specifies one combined failure outcome ("if none succeeds... 400"),
   and this spec follows that literally rather than inventing a finer-
   grained status the input did not ask for.

6. **Extend audit emission to the batch path.** The input's PR-4 asks the
   `hub.patch.create` event to record how the branch was resolved, and this
   split's own scope statement pairs that with "batch and single." Since
   `handleAddPatchBatch` emits no `hub.patch.create` event at all today,
   satisfying that literally means adding the event to the batch path too,
   closing a pre-existing gap rather than leaving it half-instrumented
   relative to the single-add path.

7. **`PATCH_BRANCH_SOURCE` read errors default to `hub`.** No workspace
   variable read anywhere else in this codebase (`AUTO_REBUILD_AFTER_SYNC`,
   `REBUILD_STRATEGY`, `REBUILD_FAIL_MODE`, ...) distinguishes "unset" from
   "store error" — both fall back to the variable's default. Registration
   follows the same convention rather than introducing a new error path for
   this one variable.

8. **No CLI change.** `afc patch add` already sends `branch_name` and
   `skip_branch_check` verbatim; the entire behaviour change described here
   sits behind the server's handling of that field, so nothing in
   `internal/cli/patch_cmd.go` needs to move.

## Dependencies

| Spec | Reason |
|------|--------|
| `02_fork_authoritative_sync` | Delivers the `PATCH_BRANCH_SOURCE` workspace variable this spec reads to decide whether to attempt the single-branch fetch, and establishes the origin-remote credential and fetch conventions (`workspace.ResolveCloneAuth`, the go-git fetch shape verified against `internal/upstream`, treating `git.NoErrAlreadyUpToDate` as success) that this spec's single-branch fetch reuses rather than re-deriving. |

## Verified External API

Same package as `02_fork_authoritative_sync`, `github.com/go-git/go-git/v5`
(`v5.19.1`, per `go.mod`), used with the identical call shape already
exercised by the existing, tested code this spec's single-branch fetch is
modelled on:

- `git.PlainOpen(path string) (*git.Repository, error)` and
  `(*git.Repository).Remote(name string) (*git.Remote, error)` — verified in
  `internal/upstream/upstream.go`.
- `(*git.Remote).FetchContext(ctx context.Context, o *git.FetchOptions) error`
  with `git.FetchOptions{RemoteName, RefSpecs []config.RefSpec, Auth
  transport.AuthMethod, Tags git.TagMode}` and `git.NoTags` — same file. The
  single-branch fetch uses `RemoteName: "origin"` and one
  `config.RefSpec("+refs/heads/<name>:refs/remotes/origin/<name>")`, the same
  templated-string construction already used for
  `internal/carrypatch/wire.go`'s `DefaultPushIntegrationFunc`.
- `errors.Is(err, git.NoErrAlreadyUpToDate)` to treat an up-to-date fetch as
  success — same file.

What was not independently re-verified: the exact error go-git's transport
returns when the requested branch does not exist on the remote at all (as
opposed to a network or auth failure). Requirement 7 and Design Decision 5
make this unnecessary for correctness — every fetch outcome other than a
credential-resolution failure is treated identically (the branch was not
found this way) — but an implementer should still spot-check that no such
error is mistakenly classified as a credential failure.

## Open Questions

- **Should the batch registration path gain `hub.patch.create` audit
  emission as part of this spec?** Decision: yes (Design Decision 6),
  because the split's scope statement explicitly pairs "recording the
  resolution path" with "batch and single," and the field is meaningless in
  a path that emits no event at all. Flagged because it fixes a gap the
  input's PR-4 does not call out by name, and a reviewer might prefer that
  fix land as its own change.
- **Should a single-branch fetch failure produce a distinct status from
  "branch not found," e.g. to separate a flaky network from a genuinely
  absent branch?** Decision: no, both produce the same `400` (Design
  Decision 5), since the input's PR-1 specifies one combined failure
  outcome and registration's fetch is a single best-effort fallback, not
  the all-branches fetch sync depends on. Flagged because it is a real
  operability trade-off — a misconfigured fork URL or transient outage
  would look identical to "branch does not exist" from the caller's side.
