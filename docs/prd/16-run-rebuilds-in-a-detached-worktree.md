# Run Rebuilds in a Detached Worktree

## Intent

Every workspace is a single non-bare clone at `<workspace_root>/<slug>/trunk`.
That one working tree is shared by everything the hub does to a workspace: the
git smart HTTP server resets it after a push, sync fast-forwards it, merge jobs
rebase in it, rollback and rerere forget run in it, and carry-patch rebuilds
check out a temporary branch in it and cherry-pick every patch on top. A
per-workspace lock (`internal/wslock`) keeps these from interleaving.

The rebuild is the operation that holds that lock the longest and mutates the
trunk the most. Today it:

- runs `checkout -B _rebuild_temp <upstream HEAD>` in the trunk, moving HEAD
  and rewriting the working tree;
- cherry-picks or merges every active patch in place;
- force-updates the integration branch, then restores whatever was checked out
  before with `checkout --force`, which discards any uncommitted change that
  was in the trunk;
- holds the workspace lock from the upstream fetch to the final ref update, so
  for the whole duration `POST /sync`, rollback, rerere forget, archive and
  reclone answer `409 workspace_busy`, and a `git push` through the hub lands
  its refs but skips the working-tree reset;
- has to clean up after itself on every exit path, and after a crash: the
  `preflightCleanup` step aborts in-progress cherry-picks, merges and rebases
  and hard-resets the trunk before every rebuild, because a crashed or
  cancelled rebuild used to leave the trunk detached on `_rebuild_temp` with a
  half-applied cherry-pick, which made every later operation fail.

None of this is inherent to the rebuild. Patch branches and the integration
branch are refs; refs do not need a working tree. The only reason the rebuild
touches the trunk's checkout is that cherry-pick and merge need *a* working
tree, and the trunk is the only one there is.

This PRD moves the rebuild into a detached temporary `git worktree` that
shares the trunk's object store, refs, configuration and rerere cache but has
its own HEAD, index and files. The trunk's checkout is never moved by a
rebuild, a crashed rebuild leaves nothing behind but a directory that is
deleted on the next run, and the workspace lock is held only for the short
steps that genuinely conflict with other operations, so pushes, syncs and the
remaining handlers keep working while a rebuild runs.

This is deliberately separate from the question of where patch branches live
(hub-local versus mirrored to the fork). That is about which refs exist and
who updates them; this PRD is only about which working tree the rebuild uses.

## Goals

- A rebuild never changes the trunk's HEAD, index or working tree. Whatever
  was checked out before the rebuild is still checked out, byte for byte,
  after it, whether the rebuild succeeded, failed on a conflict, was
  cancelled, or crashed.
- The rebuild applies patches in a detached worktree created for that run and
  removed when the run ends. Temporary state (in-progress cherry-pick, dirty
  files, the `_rebuild_temp` branch) no longer exists in the trunk.
- The workspace lock is held only where the rebuild actually conflicts with
  other operations: the upstream fetch, and the final ref update plus the
  patch bookkeeping that follows it. Pushes, syncs, rollback, rerere forget
  and merge jobs proceed while patches are being applied.
- Operations that remove or replace the workspace directory (archive, reclone)
  cannot run while a rebuild worktree exists, and a rebuild cannot start while
  they run.
- The set of patch tips a rebuild applies is fixed when the run starts. A push
  to a registered patch branch during the run does not change what that run
  applies; it results in a follow-up rebuild instead of being silently lost.
- Stale worktrees left by a crash are removed automatically before the next
  rebuild of that workspace, without touching the trunk.
- Git rerere keeps working exactly as before: resolutions recorded in one
  rebuild are replayed in the next, and the rerere endpoints keep reading and
  forgetting the same cache.
- The public API (endpoints, response shapes, job records, audit events) is
  unchanged. Only the documented `409 workspace_busy` behaviour narrows.

## Non-goals

- **Changing where patch branches live.** Whether patch branches are
  hub-local, fetched from the fork, or mirrored to it is a separate design
  decision and a separate PRD. This PRD does not fetch or push any patch
  branch.
- **Moving merge jobs, sync, or the push hook into worktrees.** The merge job
  rebases in the trunk and could benefit from the same treatment. It is out
  of scope here so the change stays reviewable; the design leaves the door
  open (see Design Decisions).
- **Concurrent rebuilds of one workspace.** The job queue serialises rebuilds
  per `<slug>:<integration branch>` group and dedups on `(type, key)`. At most
  one rebuild worktree exists per workspace at any time. Parallel rebuilds
  are not a goal.
- **Multi-replica hubs.** `wslock` is in-process and the hub runs as a single
  replica. Nothing here changes that.
- **Sparse or partial checkouts** to reduce the disk cost of the worktree.
  A worktree checks out the full tree; see Technical Boundaries.
- **Changing the rebuild algorithm.** Strategy selection, fail modes, merge
  detection, soft deletion, position compaction, progress reporting and
  rollback keep their current semantics.

## Current behaviour (for reference)

| Step | Where | Lock |
|------|-------|------|
| Resolve upstream auth, fetch `upstream` | trunk | held |
| Resolve upstream base (`refs/remotes/upstream/HEAD`) | trunk | held |
| Record current checkout, `preflightCleanup` (abort cherry-pick/merge/rebase, hard reset) | trunk | held |
| `checkout -B _rebuild_temp <base>` | trunk | held |
| For each patch: cherry-pick commits or `merge --no-ff`, rerere replay | trunk | held |
| `branch -f <integration> HEAD` | trunk | held |
| Restore original checkout (`checkout --force`), delete `_rebuild_temp` | trunk | held |
| Soft-delete `merged_upstream` patches, compact positions | DB | held |
| Optional push of the integration branch to `origin` | trunk | held |

While the lock is held, handlers that `TryLock` return `409 workspace_busy`
(sync, rollback, rerere forget, archive, reclone, batch rebase) and the git
server's post-push worktree reset is skipped. Merge jobs block until the
rebuild finishes.

## Functional Requirements

### Worktree lifecycle

- **WT-1.** The rebuild SHALL create a detached worktree for each run with
  `git worktree add --detach <path> <upstream base>` where `<path>` is
  `<workspace_root>/<slug>/rebuild/<job id>`, and run every patch application
  step in that worktree.
- **WT-2.** The worktree SHALL be removed on every exit path (success,
  conflict, transient error, cancellation) with `git worktree remove --force
  <path>` followed by `git worktree prune`. Removal is best-effort and never
  changes the job outcome.
- **WT-3.** Before creating its worktree, the rebuild SHALL remove any
  directory under `<workspace_root>/<slug>/rebuild/` and run `git worktree
  prune`, so a crash during an earlier run leaves nothing that blocks the next
  one. This replaces `preflightCleanup`; the trunk is no longer reset.
- **WT-4.** The rebuild SHALL NOT run `checkout`, `reset`, `cherry-pick`,
  `merge`, `rebase` or any other working-tree-mutating command in the trunk.
  The trunk's HEAD, index and files SHALL be identical before and after a
  rebuild.
- **WT-5.** The `_rebuild_temp` branch SHALL no longer be created. On the
  first rebuild after this change, if a stale `_rebuild_temp` branch exists in
  the trunk, the rebuild SHALL delete it (moving the trunk's HEAD to the
  workspace branch first if it is checked out) and log that it did so. This
  is the only trunk mutation the rebuild is permitted, and only once.
- **WT-6.** The integration branch SHALL be force-updated from the worktree's
  final HEAD with `git branch -f <integration> <sha>` or
  `git update-ref refs/heads/<integration> <sha>`, never by checking the
  integration branch out anywhere.
- **WT-7.** Rerere SHALL keep working across worktrees. `rerere.enabled` and
  `rerere.autoupdate` are repository-level configuration and `rr-cache` lives
  in the common git directory, so no per-worktree setup is needed; the
  implementation SHALL verify this in an integration test rather than assume
  it (see Testing).

### Locking

- **LK-1.** The rebuild SHALL hold the workspace lock (`wslock.Lock`) for the
  upstream fetch and upstream-base resolution, then release it before creating
  the worktree.
- **LK-2.** The rebuild SHALL re-acquire the workspace lock for the final
  phase: updating the integration branch ref, soft-deleting `merged_upstream`
  patches, compacting positions, and the optional push of the integration
  branch to `origin`. It SHALL release the lock before removing the worktree.
- **LK-3.** Patch application (WT-1) SHALL run without the workspace lock.
  During this phase `POST /sync`, rollback, rerere forget, batch rebase and
  merge jobs SHALL proceed as if no rebuild were running, and the post-push
  worktree reset SHALL happen.
- **LK-4.** A per-workspace "rebuild active" guard SHALL exist for the whole
  run, from before WT-3 until after WT-2. Archive and reclone SHALL check it
  and answer `409 workspace_busy` while it is set, because both remove the
  workspace directory that the worktree lives under. The rebuild SHALL NOT
  start (SHALL return a retryable error) while archive or reclone hold the
  workspace lock.
- **LK-5.** Rollback SHALL keep taking the workspace lock. Because the
  rebuild's final ref update (LK-2) also takes it, a rollback and a rebuild
  cannot interleave their writes to the integration branch. A rollback that
  runs during patch application is legitimate: the rebuild's later `branch -f`
  overwrites it, and the job record's `previous_integration_head_sha` reflects
  the value at the time of the final phase.

### Snapshotting patch tips

- **SN-1.** Before applying the first patch, the rebuild SHALL resolve every
  patch branch in the list to a commit SHA (`rev-parse --verify
  refs/heads/<branch>`) and SHALL use those SHAs, not the branch names, for
  `git log`, cherry-pick and merge for the rest of the run. A branch that
  does not resolve is recorded as `skipped` / `branch_not_found` at that
  point, as today.
- **SN-2.** The `merge` strategy SHALL merge the snapshot SHA (`git merge
  --no-ff <sha>`), so a push during the run cannot change what is merged. The
  merge commit message SHALL still name the branch.
- **SN-3.** After the final phase (LK-2), the rebuild SHALL compare each
  active patch's current tip with its snapshot SHA. If any differ, and
  `AUTO_REBUILD_AFTER_PUSH` is not `"false"`, the rebuild SHALL enqueue a
  follow-up rebuild with the same payload (`submitted_by`:
  `system:stale-snapshot`). This closes the existing gap where a push during
  a running rebuild is deduplicated away by the `(type, key)` check and never
  rebuilt.
- **SN-4.** The job's result SHALL record the snapshot SHA of each patch as
  `patch_results[].source_sha`, so an operator can tell which tip a rebuild
  applied.

### Concurrency with pushes and syncs

- **CC-1.** A `git push` through the hub during patch application SHALL
  update refs and reset the trunk's working tree exactly as when no rebuild
  is running. It SHALL NOT affect the running rebuild's worktree.
- **CC-2.** A `POST /sync` during patch application SHALL fetch upstream and
  update `upstream_head_sha` as usual. Its auto-rebuild enqueue is
  deduplicated by the queue while the current run is active, as today. The
  running rebuild keeps using the upstream base it resolved under LK-1.
- **CC-3.** Objects reachable only from the worktree's HEAD (the partially
  built integration history) SHALL survive a `git gc` triggered by another
  operation. Git treats every worktree's HEAD as a GC root, so this holds by
  construction; the implementation SHALL NOT run `gc --prune=now` or
  `reflog expire` in the trunk during a rebuild.

### Cancellation and crash recovery

- **CR-1.** When the job context is cancelled during patch application, the
  rebuild SHALL abort the in-progress cherry-pick or merge in the worktree,
  remove the worktree (WT-2), release the rebuild-active guard, and return a
  retryable error, as the current code does for the trunk.
- **CR-2.** If the hub process dies during a rebuild, the next rebuild of that
  workspace SHALL start cleanly because of WT-3. No operator action and no
  trunk reset is required.
- **CR-3.** On startup, the hub SHALL remove every `<workspace_root>/*/rebuild`
  directory and run `git worktree prune` in the corresponding trunk, logging
  each removal. This keeps disk usage bounded for workspaces that are not
  rebuilt again soon.

### Configuration

- **CF-1.** No new configuration is required. The worktree root is derived
  from `workspace.path`.
- **CF-2.** The existing variables `REBUILD_STRATEGY`, `REBUILD_FAIL_MODE`,
  `REBUILD_PUSH_INTEGRATION_BRANCH`, `AUTO_REBUILD_AFTER_SYNC` and
  `AUTO_REBUILD_AFTER_PUSH` keep their meaning.

### Observability

- **OB-1.** The `hub.rebuild.complete` and `hub.rebuild.fail` audit events
  SHALL be emitted as today. A new `hub.rebuild.followup` event SHALL be
  emitted when SN-3 enqueues a follow-up, with the list of stale patch
  branches in metadata.
- **OB-2.** Worktree creation and removal, stale-worktree cleanup (WT-3,
  CR-3) and the one-time `_rebuild_temp` deletion (WT-5) SHALL be logged at
  info level with the workspace slug and path.

### Documentation

- **DC-1.** `docs/api.md` "Workspace Lock" and the `409` rows for sync,
  rollback, rerere forget, archive and reclone SHALL be updated: sync,
  rollback and rerere forget are rejected only during the rebuild's fetch and
  final phases; archive and reclone are rejected for the whole rebuild.
- **DC-2.** `docs/carry_patch_workflow.md` SHALL state that a rebuild does not
  change the trunk's checkout, and that clones served by the hub during a
  rebuild see the pre-rebuild integration branch until the run completes.
- **DC-3.** The `preflightCleanup` and `_rebuild_temp` behaviour described in
  `docs/prd/review_2026_09_codebase.md` SHALL be marked superseded by this
  PRD in an erratum or a note in that document.

### Testing

- **TS-1.** An integration test with a real git repository SHALL assert that
  the trunk's `HEAD`, `rev-parse HEAD`, `status --porcelain` output and a
  sample file's contents are identical before and after a successful rebuild,
  a fail-fast conflict, a continue-mode conflict, and a cancelled rebuild.
- **TS-2.** An integration test SHALL create a stale directory under
  `rebuild/` with a registered but orphaned worktree and assert that the next
  rebuild succeeds and the directory is gone.
- **TS-3.** An integration test SHALL record a rerere resolution in one
  rebuild's worktree and assert that the next rebuild replays it, and that
  `GET /rerere` lists it.
- **TS-4.** A test SHALL push to a registered patch branch while a rebuild is
  blocked mid-application (using a controllable fake for one patch step) and
  assert that the run applies the snapshot SHA, that the trunk's HEAD moved to
  the pushed commit, and that a follow-up rebuild was enqueued (SN-3).
- **TS-5.** A test SHALL assert that archive and reclone answer `409
  workspace_busy` while the rebuild-active guard is set, and that sync,
  rollback and rerere forget succeed during patch application.
- **TS-6.** Existing rebuild tests that use the fake `GitRunner` SHALL be
  updated to expect the worktree commands instead of `checkout -B` and the
  restore/delete sequence.

## Technical Boundaries

- **`GitRunner` takes a directory.** `NewGitRunner(repoPath)` builds a
  `gitcmd.GitRunner` rooted at a path, so the rebuild can construct one runner
  for the trunk (fetch, worktree add/remove, ref update) and one for the
  worktree (cherry-pick, merge, rerere, rev-parse). No interface change is
  required beyond adding the worktree commands through `Run`.
- **`gitcmd.hasRebaseState` assumes `<workDir>/.git` is a directory.** In a
  linked worktree `.git` is a file pointing at the per-worktree git dir. The
  rebuild does not call `Rebase`, so this is not on the path, but any helper
  moved onto the worktree later must use `git rev-parse --git-dir` instead.
- **The rerere handlers read `trunk/.git/rr-cache` directly.** That is the
  common git directory, shared by all worktrees, so the handlers stay correct
  without change. WT-7's test is what proves it.
- **`DefaultPushIntegrationFunc` opens the trunk with go-git.** It pushes
  `refs/heads/<integration>`, which is shared, so it keeps working. It runs
  under LK-2.
- **Disk.** A linked worktree checks out the full tree once more. Because at
  most one rebuild runs per workspace, the worst case is one extra working
  tree per workspace being rebuilt, freed when the run ends or at startup
  (CR-3). Object storage is shared and not duplicated.
- **Two lock acquisitions instead of one.** Between LK-1 and LK-2 another
  operation may change refs. The design accepts this: patch tips are
  snapshotted (SN-1), the integration branch is force-updated regardless of
  intermediate writes (LK-5), and the upstream base is the one fetched under
  LK-1. A sync that advances upstream during the run enqueues nothing new
  because of dedup, which is the same outcome as today when the sync is
  rejected with 409; the next scheduled sync catches up.
- **The rebuild-active guard is separate from `wslock`.** `wslock` is a mutex
  and cannot express "held for a long time but others may proceed". The guard
  is a per-workspace flag (or a second map in `internal/wslock`) checked only
  by archive, reclone and the rebuild itself.

## Dependencies

- Git 2.5 or later on the hub host for `git worktree`; the container image
  already ships a current git for `upload-pack`.
- `internal/wslock` gains the rebuild-active guard.
- `internal/carrypatch/rebuild_executor.go` is the main change site;
  `internal/workspace/handlers.go` (archive) and
  `internal/workspace/reclone_handler.go` gain the guard check.
- No new external Go modules.

## Design Decisions

### 1. Worktree per run, not a long-lived worktree per workspace

A persistent second checkout would also keep the trunk clean, but it would
carry state between runs (a half-finished cherry-pick, a stale index) and
bring back the cleanup problem the current code has. A worktree per run is
created at the upstream base and thrown away, so every rebuild starts from a
known state and crash recovery is "delete the directory". The cost is one
checkout per rebuild, which the rebuild already pays today with
`checkout -B`.

### 2. Detached HEAD, no temporary branch

`_rebuild_temp` existed to give the trunk something to check out. In a
worktree, a detached HEAD does the same job with nothing to delete
afterwards and nothing that can be left dangling in the trunk's ref
namespace. The integration branch is written once, at the end, from a SHA.

### 3. Split the lock instead of dropping it

Holding the lock for the whole run is what makes the trunk unavailable today.
Dropping it entirely would let archive delete the directory under a running
worktree. The split keeps the two phases that write shared state (the fetch
that rewrites `refs/remotes/upstream/*`, and the ref update plus DB
bookkeeping) exclusive, and adds a lightweight guard for the two operations
that remove the directory. Everything else is safe by construction because
the worktree has its own HEAD and index.

### 4. Snapshot patch tips at start

Once the lock is released during patch application, branch names become a
moving target. Resolving them to SHAs up front makes the run deterministic,
and comparing again at the end (SN-3) turns a dropped push into a follow-up
rebuild. This also fixes a latent bug that exists today irrespective of
worktrees: a push during a running rebuild is deduplicated by the queue and
never rebuilt.

### 5. Merge jobs stay in the trunk for now

The merge job has the same shape (checkout, rebase, restore) and would fit
the same pattern, but it also deletes the source branch and runs a check
command whose working directory is part of the workspace's contract. Moving
it is a separate change with its own PRD; the rebuild-active guard and the
worktree root layout are designed so a `merge/<job id>` worktree can sit
beside `rebuild/<job id>` later.

### 6. No sparse checkout

Sparse checkout would reduce the worktree's disk footprint but changes what
cherry-pick sees and complicates rerere. The disk cost is bounded to one
extra tree per rebuilding workspace and is acceptable for the repositories
the hub targets.

## Open Questions

- Should the follow-up rebuild (SN-3) be enqueued unconditionally, or only
  when the stale patch is `active`? The current proposal enqueues when any
  active patch's tip moved; disabled and merged patches are ignored.
- Should the trunk's checkout be pinned to the workspace branch after this
  change, given nothing in the rebuild path moves it any more? Today it is
  wherever the last merge job or push left it. Pinning would make hub-served
  clones predictable but is a behaviour change outside this PRD.
- Whether `git worktree` should be added to the `GitRunner` interface as
  typed methods (`WorktreeAdd`, `WorktreeRemove`, `WorktreePrune`) or driven
  through `Run`. Typed methods make the fakes in tests clearer; `Run` keeps
  the interface small. The proposal leans towards typed methods since the
  fake runner already enumerates commands.
