---
spec_id: "01"
spec_name: "run_rebuilds_in_a_detached_worktree"
title: "Run Rebuilds in a Detached Worktree"
status: "active"
created_at: "2026-09-30T08:42:22.675574Z"
updated_at: "2026-09-30T08:42:22.675574Z"
intent_hash: "46b5f915335ba1feb9371d0c9d94ccf879010ff08f4da03e627004c4de486bb4"
schema_version: 2
source: "docs/prd/16-run-rebuilds-in-a-detached-worktree.md"
---
## Intent

A carry-patch rebuild must never move the trunk's HEAD, index or working
tree, must hold the per-workspace lock only for the steps that genuinely
conflict with other operations, and must leave nothing behind — in the
trunk or on disk — when it succeeds, fails, is cancelled, or the process
crashes mid-run. This spec is the stable statement of that property for
`internal/carrypatch`'s rebuild executor; it exists so that a rebuild's
choice of working tree can change again later without re-litigating what
"correct" means.

## Goals

- The trunk's `HEAD`, index and working tree are byte-for-byte identical
  before and after every rebuild, regardless of outcome (success, conflict,
  transient error, cancellation).
- Patch application runs in a detached `git worktree` created under
  `<workspace_root>/<slug>/rebuild/<job id>` and removed on every exit path;
  no `_rebuild_temp` branch is created.
- The workspace lock (`internal/wslock`) is held only for the upstream fetch
  plus upstream-base resolution, and again for the final ref update and DB
  bookkeeping — not for the whole run. `POST /sync`, rollback, rerere forget
  and merge jobs proceed while patches are being applied, and a `git push`
  through the hub resets the trunk's working tree as usual during that
  window.
- Archive and reclone cannot run while a rebuild worktree exists (they
  delete the workspace directory the worktree lives under); a rebuild
  cannot start while archive or reclone are running.
- Patch tips are snapshotted to SHAs at the start of a run; a push to a
  patch branch during patch application does not change what that run
  applies, and produces a follow-up rebuild instead of being silently
  dropped by job dedup.
- A crash leaves nothing that blocks the next rebuild: no trunk reset is
  needed, and any stale `rebuild/*` directory is removed automatically,
  both on the next rebuild of that workspace and at hub startup.
- Rerere resolutions keep being recorded and replayed across rebuilds, and
  `GET /rerere` / `DELETE /rerere/*` keep working unchanged.
- No public API shape changes: same endpoints, response fields, job
  records and audit event types. Only the window during which sync,
  rollback and rerere-forget answer `409 workspace_busy` narrows.

## Non-goals

- **Where patch branches live** (hub-local vs. fetched from / mirrored to
  the fork). Separate design question, separate PRD; this one does not
  fetch or push any patch branch.
- **Moving merge jobs, sync, or the git-push hook into worktrees.** The
  merge job (`internal/merge`) still checks out and rebases in the trunk.
  The worktree root layout (`<slug>/rebuild/<job id>`) is chosen so a
  sibling `<slug>/merge/<job id>` can be added later without conflict, but
  that move is out of scope here.
- **Concurrent rebuilds of one workspace.** Already prevented by job-queue
  group-key serialization (`FormatGroupKey`) and `(type, key)` dedup; at
  most one rebuild worktree exists per workspace at a time.
- **Multi-replica hubs.** `wslock` and the new rebuild-active guard are
  in-process; the hub still runs as a single replica.
- **Sparse or partial checkouts.** The worktree checks out the full tree.
- **Changing the rebuild algorithm.** Strategy selection (rebase/merge),
  fail modes, conflict/rerere handling, soft-deletion of `merged_upstream`
  patches, position compaction, progress reporting and rollback keep their
  current semantics; only the working tree and lock scope change.

## Background

Every workspace is a single non-bare clone at `<workspace_root>/<slug>/trunk`
(`internal/workspace`). `internal/carrypatch/rebuild_executor.go`
(`RebuildHandler.HandleRebuildJob`) currently:

1. Acquires `wslock.Lock(slug)` (`internal/wslock`) and holds it for the
   entire job.
2. Fetches `upstream` and resolves the upstream base
   (`resolveUpstreamBase`, `internal/carrypatch/wire.go`).
3. Records the trunk's current checkout (`currentCheckout`), runs
   `preflightCleanup` (abort any in-progress cherry-pick/merge/rebase, hard
   reset — added because a crashed rebuild used to leave the trunk detached
   mid-cherry-pick), then `checkout -B _rebuild_temp <upstream base>`.
4. Cherry-picks or `merge --no-ff`s each active patch onto `_rebuild_temp`
   in the trunk, replaying `rr-cache` resolutions via `git rerere` on
   conflict (`handleConflictWithRerere`).
5. Force-updates the integration branch (`branch -f <integration> HEAD`),
   restores the original checkout (`checkout --force`), deletes
   `_rebuild_temp` (`cleanupTempBranch`), soft-deletes `merged_upstream`
   patches, compacts positions, and optionally pushes the integration
   branch to `origin` (`DefaultPushIntegrationFunc`, go-git).

While the lock is held, `wslock.TryLock` callers — `POST /sync`
(`internal/workspace/sync_handler.go`), rollback and rerere forget
(`internal/carrypatch/api.go`, `rerere_handlers.go`), archive
(`internal/workspace/handlers.go`), reclone
(`internal/workspace/reclone_handler.go`), and batch rebase
(`internal/merge/api.go`) — return `409 workspace_busy`. The git server's
post-push handler (`internal/gitserver/handlers.go`, `updateHeadSHA`) also
uses `TryLock` and skips its working-tree reset when busy, logging
`"workspace %q busy; skipping worktree reset"`. Merge jobs
(`internal/merge/merge.go`) call the blocking `wslock.Lock` and simply wait.

None of the trunk mutation is inherent: patch branches and the integration
branch are refs, and cherry-pick/merge only need *some* working tree. `git
worktree add --detach` gives the rebuild its own HEAD, index and files
against the same object store, refs, config and `rr-cache` (all of which
live in the trunk's `.git`, the "common" git directory in worktree
terminology), while leaving the trunk's checkout untouched.

`internal/gitcmd.GitRunner` (wrapped by `carrypatch.GitRunnerAdapter` to
satisfy `carrypatch.GitRunner`) is constructed per directory
(`gitcmd.New(workDir, extraEnv)` validates `workDir` is an existing
directory; it does not require a git repository). `RebuildHandler.NewGitRunner
func(repoPath string) (GitRunner, error)` already takes a path, so the same
factory can build one runner rooted at the trunk (fetch, worktree
add/remove/prune, ref update) and a second rooted at the worktree
(cherry-pick, merge, rerere, rev-parse) with no interface redesign.

## Requirements

### Worktree lifecycle

1. **Create.** The rebuild creates the worktree with
   `git worktree add --detach <path> <upstream base>` run from the trunk's
   `GitRunner`, where `<path>` is
   `<workspace_root>/<slug>/rebuild/<job id>` and `<job id>` comes from
   `jobqueue.JobIDFromContext(ctx)`. When the context carries no job ID
   (e.g. a handler invoked directly, as some existing unit tests do), a
   generated UUID is used instead so the path is always unique; this is
   logged at warn level since it should not happen outside tests. All patch
   application (cherry-pick, `merge --no-ff`, `rerere`, `rev-parse`, `diff
   --name-only --diff-filter=U`, and the abort/reset paths on conflict) runs
   against a second `GitRunner` rooted at that worktree path, exactly as it
   runs today against the trunk's runner.
2. **Remove.** On every exit path — success, unresolved conflict in either
   fail mode, a transient/unknown error, or cancellation — the rebuild runs
   `git worktree remove --force <path>` followed by `git worktree prune`
   from the trunk's runner. Both are best-effort: a failure is logged but
   never changes the job's outcome (mirrors the existing
   `cleanupTempBranch`/`restoreCheckout` best-effort pattern).
3. **Stale-worktree cleanup before creating a new one.** Before step 1, the
   rebuild removes (`os.RemoveAll`) every directory directly under
   `<workspace_root>/<slug>/rebuild/` and runs `git worktree prune` in the
   trunk. Because at most one rebuild runs per workspace, anything found
   there is left over from a crash or a killed process and is safe to
   discard unconditionally. This step, plus the equivalent one at hub
   startup (below), replaces `preflightCleanup` entirely — the trunk is
   never reset by a rebuild.
4. **No trunk mutation.** The rebuild does not run `checkout`, `reset`,
   `cherry-pick`, `merge`, `rebase`, `gc --prune=now` or `reflog expire`
   against the trunk's runner at any point. The only exception is the
   one-time `_rebuild_temp` cleanup below.
5. **One-time `_rebuild_temp` deletion.** During the locked fetch phase
   (below), after resolving the upstream base, the rebuild checks whether a
   local branch named `_rebuild_temp` exists in the trunk
   (`rev-parse --verify refs/heads/_rebuild_temp`). If it does: when it is
   the currently checked-out branch (`currentCheckout` returns
   `_rebuild_temp`), the trunk is first checked out onto the workspace's
   configured branch (`workspaceBranch(h.DB, slug)`; the same lookup
   `resolveUpstreamBase` already uses) or, if that is empty, left detached
   at the branch's own tip; the branch is then deleted
   (`branch -D _rebuild_temp`) and the deletion is logged at info level
   with the workspace slug. This runs at most once per workspace (the
   branch cannot reappear afterwards, since no code path creates it any
   more) and is the only trunk mutation a rebuild is ever permitted to make.
6. **Integration branch update by ref, not checkout.** The integration
   branch is force-updated from the worktree's final HEAD SHA with
   `git branch -f <integration> <sha>` (equivalently `update-ref
   refs/heads/<integration> <sha>`), run from the trunk's runner; the
   integration branch itself is never checked out anywhere.
7. **Rerere works unchanged.** `rerere.enabled` / `rerere.autoupdate` are
   repository-level config and `rr-cache` lives in the common `.git`
   directory shared by every worktree, so no per-worktree setup is added.
   The config `git config rerere.enabled/autoupdate true` calls move to
   whichever runner (trunk or worktree) is convenient, since both resolve
   to the same config file; an integration test (Testing, below) proves a
   resolution recorded in one worktree replays in the next rather than
   assuming it from documentation.

### Locking

8. **Fetch phase.** The rebuild acquires the workspace lock with
   `wslock.TryLock(slug)`, not the blocking `wslock.Lock`, before the
   upstream fetch and upstream-base resolution (including the one-time
   `_rebuild_temp` cleanup, which needs the same exclusivity). If the lock
   is not immediately available, the rebuild returns a retryable error
   (`*TransientError`) without doing any work; the job queue's existing
   backoff/retry handles scheduling the next attempt. The lock is released
   before the worktree is created.
9. **Rebuild-active guard.** Immediately before attempting the fetch-phase
   lock (so the window starts no later than the stale-worktree cleanup and
   ends no earlier than the worktree removal), the rebuild sets a
   per-workspace "rebuild active" guard. The guard is cleared after the
   worktree has been removed, on every exit path, via `defer`. Archive
   (`handleArchiveWorkspace`) and reclone (`handleRecloneWorkspace`) check
   the guard — in addition to their existing `wslock.TryLock` — and answer
   `409 workspace_busy` while it is set, because both delete the workspace
   directory the worktree lives under. If the guard is already set when a
   rebuild is about to start (i.e. archive or reclone is mid-run and has
   not released the guard because it never sets it, so this reduces to:
   the fetch-phase `TryLock` above fails because archive/reclone hold
   `wslock`), the rebuild returns the same retryable error as point 8 — a
   rebuild never starts while archive or reclone is running.
10. **Patch application phase.** No lock and no guard beyond the one set in
    point 9 is held while patches are applied. `POST /sync`, rollback,
    rerere forget, batch rebase and merge jobs proceed exactly as if no
    rebuild were running, and the git server's post-push worktree reset
    (`updateHeadSHA`) succeeds instead of being skipped.
11. **Final phase.** The rebuild re-acquires the workspace lock with the
    blocking `wslock.Lock(slug)` for: the integration-branch ref update,
    the stale-patch comparison, soft-deleting `merged_upstream` patches,
    compacting positions, and (when configured) the optional push of the
    integration branch to `origin`. The lock is released before the
    worktree is removed. Blocking here (rather than `TryLock`) matches
    today's behaviour when this phase races with rollback or a merge job:
    whichever holds the lock finishes first, and the rebuild's `branch -f`
    at the end always wins over an interleaved rollback, because the ref
    update happens after the lock is reacquired.

### Snapshotting patch tips

12. Before applying the first patch, the rebuild resolves every patch
    branch to a commit SHA (`rev-parse --verify refs/heads/<branch>`) and
    uses those SHAs — not the branch names — for the `git log` commit
    selection, cherry-pick and `merge --no-ff` for the rest of the run. A
    branch that fails to resolve is recorded as `skipped` /
    `branch_not_found`, as today. The merge commit message still names the
    branch, not the SHA.
13. After the final phase (point 11) completes and its lock is released,
    the rebuild re-reads each patch that was `active` when snapshotted and
    compares its current tip (`rev-parse --verify refs/heads/<branch>`,
    read against the trunk's runner, no lock needed) to its snapshot SHA.
    Patches that were skipped, disabled, in conflict, or merged/deleted are
    not compared. If any active patch's tip moved, and
    `AUTO_REBUILD_AFTER_PUSH` is not `"false"`, the rebuild enqueues a
    follow-up rebuild job with the same payload shape as
    `BuildRebuildPayload` produces, `SubmittedBy: "system:stale-snapshot"`,
    the same `Group`/`Key` as the current job (so ordinary `(type, key)` +
    group-key dedup applies) and a fresh nonce. This is what turns a push
    to a patch branch during a running rebuild — today silently dropped by
    dedup — into a follow-up rebuild.
14. The job result records the snapshot SHA used for each patch as
    `patch_results[].source_sha` (new field on `PatchResult`), alongside
    the existing `new_head_sha`, so an operator can tell which tip a
    completed rebuild actually applied.

### Concurrency with pushes and syncs

15. A `git push` through the hub during patch application updates refs and
    resets the trunk's working tree exactly as when no rebuild is running
    (point 10); it has no effect on the rebuild's worktree, which has its
    own index and HEAD.
16. A `POST /sync` during patch application fetches upstream and updates
    `upstream_head_sha` as usual; its own auto-rebuild enqueue keeps being
    deduplicated by the job queue while the current run is active. The
    running rebuild keeps using the upstream base it resolved during its
    own fetch phase (point 8), never a base that changed underneath it.
17. Objects reachable only from the worktree's HEAD survive a `git gc` run
    by another operation, because git treats every worktree's HEAD as a GC
    root; the rebuild must not itself run `gc --prune=now` or
    `reflog expire` against the trunk (restated from point 4 because it is
    the property that makes this safe).

### Cancellation and crash recovery

18. When the job context is cancelled during patch application, the
    rebuild aborts any in-progress cherry-pick or merge in the worktree,
    removes the worktree (point 2), clears the rebuild-active guard, and
    returns a retryable error — the same shape the trunk-based version
    returns today, just scoped to the worktree instead.
19. If the hub process dies mid-rebuild, the next rebuild of that
    workspace starts cleanly because of the stale-worktree cleanup (point
    3); no operator action and no trunk reset is needed.
20. At hub startup, before the job queue starts dispatching jobs, the hub
    removes every `<workspace_root>/*/rebuild` directory and runs
    `git worktree prune` in the corresponding `<slug>/trunk`, logging each
    removal at info level with the workspace slug and path. This bounds
    disk usage for workspaces not rebuilt again soon after a restart.

### Configuration

21. No new configuration. The worktree root is derived from the existing
    `workspace.path` / `WorkspaceRoot` value already passed to
    `RebuildHandler`. `REBUILD_STRATEGY`, `REBUILD_FAIL_MODE`,
    `REBUILD_PUSH_INTEGRATION_BRANCH`, `AUTO_REBUILD_AFTER_SYNC` and
    `AUTO_REBUILD_AFTER_PUSH` keep their current meaning and resolution
    order.

### Observability

22. `hub.rebuild.complete` and `hub.rebuild.fail` keep being emitted as
    today. A new `hub.rebuild.followup` audit event is emitted when point
    13 enqueues a follow-up rebuild, with metadata listing the stale patch
    branch names.
23. Worktree creation and removal, the stale-worktree cleanups (points 3
    and 20), and the one-time `_rebuild_temp` deletion (point 5) are
    logged at info level with the workspace slug and the worktree path.

### Documentation

24. `docs/api.md`: the "Workspace Lock" section and the `workspace_busy`
    row in the error-type table are updated so that sync, rollback and
    rerere forget are described as rejected only during a rebuild's fetch
    and final phases, while archive and reclone are described as rejected
    for the whole rebuild (via the new guard).
25. `docs/carry_patch_workflow.md`: the "Rebuild algorithm" section is
    rewritten to describe the worktree-based flow (no `_rebuild_temp`, no
    trunk checkout change) and states explicitly that clones served by the
    hub's git server during a rebuild see the pre-rebuild integration
    branch until the run's final phase completes.
26. `docs/prd/review_2026_09_codebase.md`: the paragraph describing
    `preflightCleanup` and `_rebuild_temp` gets a note that this behaviour
    is superseded by this PRD.

## Design Decisions

1. **A worktree per run, created at the upstream base and discarded, not a
   long-lived second checkout.** A persistent worktree would still need
   the same cleanup-on-crash handling the current trunk-based code has
   (half-finished cherry-pick, stale index); one created fresh from
   `<upstream base>` and removed at the end means every run starts from a
   known state and crash recovery is "delete the directory."
2. **Detached HEAD, not a temporary branch.** `_rebuild_temp` existed only
   to give the trunk something to check out; a worktree's detached HEAD
   does that job without ever touching the trunk's ref namespace. The
   integration branch is written once, at the end, from a SHA.
3. **The fetch-phase lock acquisition uses `TryLock`, not the blocking
   `Lock` the current code and the input's LK-1 both describe.** The
   requirement that a rebuild "SHALL NOT start... SHALL return a retryable
   error" while archive or reclone hold the workspace lock is only
   satisfiable by a non-blocking acquisition at that point: archive and
   reclone delete the workspace directory the fetch is about to read, so a
   rebuild that blocked on `Lock` until they finished could try to fetch
   into a directory that no longer exists. The job queue already treats
   `*TransientError` as retryable with backoff, so this reuses existing
   infrastructure rather than adding a new one. The final-phase lock
   (point 11) keeps the blocking `Lock`, because by then the rebuild has
   already invested the work of patch application and re-running it on a
   `TryLock` failure would be wasteful; that phase is short regardless of
   who else holds the lock.
4. **The rebuild-active guard is a new, separate primitive in
   `internal/wslock`, not a reuse of the mutex.** `wslock`'s mutex means
   "exclusive access, briefly"; the guard means "don't delete the
   directory out from under me, but otherwise proceed." A boolean map
   keyed by slug, guarded by its own mutex, is enough — only archive,
   reclone and the rebuild itself ever look at it, and at most one rebuild
   runs per workspace.
5. **Snapshot patch tips to SHAs at the start of the run.** Once the lock
   is released for patch application, branch names are a moving target.
   Resolving them up front makes a run deterministic and lets the final
   comparison (point 13) turn a push during a running rebuild into a
   follow-up rebuild instead of a dropped update — a latent bug that
   exists independently of this change (the queue's `(type, key)` dedup
   already silently drops such a push today).
6. **Follow-up rebuilds are enqueued only for patches that were `active`
   at snapshot time.** Disabled, conflicted, merged and deleted patches
   are excluded from the comparison in point 13: a push to a disabled
   patch branch, for instance, should not trigger a rebuild it has no
   effect on.
7. **The trunk's checkout is not pinned to the workspace branch by this
   change.** Nothing in the rebuild path moves it any more, but deciding
   what hub-served clones should see by default (today: "whichever branch
   a push or merge job last left checked out") is a behaviour change with
   its own tradeoffs and is left for a separate PRD.
8. **`git worktree` commands are added as typed methods, not driven
   through the generic `Run`.** `gitcmd.GitRunner` gains `WorktreeAdd`,
   `WorktreeRemove` and `WorktreePrune`; `carrypatch.GitRunnerAdapter` and
   the `carrypatch.GitRunner` interface gain the same three methods. The
   fakes used throughout `internal/carrypatch`'s tests already enumerate
   commands as typed fields (`CherryPickFunc`, `RunFunc`, ...), so typed
   methods keep the new commands equally explicit and equally easy to
   assert against, and avoid a string-matching `Run(ctx, "worktree",
   "add", ...)` call scattered across the executor and its tests.
9. **Merge jobs are unchanged.** They have the same checkout/rebase/restore
   shape and could benefit from the same treatment, but they also delete
   the source branch and run an operator-configured check command whose
   working directory is part of the workspace's contract; moving them is
   a separate, separately reviewable change. The worktree root
   (`<slug>/rebuild/<job id>`) is chosen so a sibling `<slug>/merge/<job
   id>` fits the same layout later.
10. **No sparse checkout.** It would reduce the worktree's disk footprint
    but changes what cherry-pick sees and complicates rerere. At most one
    extra full checkout exists per rebuilding workspace at a time, which is
    an acceptable cost for the repositories the hub targets.

## Dependencies

| Spec / package | Reason |
|---|---|
| `internal/wslock` | Gains the rebuild-active guard (set/check/clear by slug) alongside the existing `Lock`/`TryLock`. |
| `internal/gitcmd` | Gains `WorktreeAdd`, `WorktreeRemove`, `WorktreePrune` typed methods on `GitRunner`. |
| `internal/carrypatch` (`rebuild_executor.go`, `wire.go`, `carrypatch.go`) | Main change site: worktree lifecycle, split locking, snapshot/follow-up logic, `PatchResult.source_sha`. |
| `internal/workspace` (`handlers.go` archive, `reclone_handler.go`) | Add the rebuild-active guard check alongside the existing `wslock.TryLock`. |
| `internal/jobqueue` | Consumed as-is: `JobIDFromContext`, `EnqueueParams`, `*TransientError`-driven retry/backoff. No changes required. |
| `cmd/af-hub/main.go` | Wires the startup stale-worktree cleanup (point 20) before `mergeQueue.Start()`. |
| `docs/api.md`, `docs/carry_patch_workflow.md`, `docs/prd/review_2026_09_codebase.md` | Updated per Requirements 24-26. |

## Open Questions

None remain unresolved in this PRD; the items the input left open are
answered in Design Decisions 3, 6, 7 and 8 above.
