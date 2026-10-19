---
spec_id: "01"
spec_name: "rebuild_worktree"
title: "Run Carry-Patch Rebuilds in a Detached Worktree"
status: "active"
created_at: "2026-10-01T07:34:32.481635Z"
updated_at: "2026-10-01T07:34:32.481635Z"
intent_hash: "13620b0887116dd73d752e2a5243199880048a495e509b68a7d1a67684bb797b"
schema_version: 2
source: "https://github.com/agent-fox-dev/hub/issues/34"
---
## Intent

A carry-patch rebuild applies patch branches onto the upstream base to produce the integration branch. Branches are refs and need no working tree, but cherry-pick and merge do, and today the rebuild borrows the workspace's single shared trunk checkout for them and holds the per-workspace lock for the whole run. This spec moves patch application into a detached, per-run `git worktree` that shares the trunk's objects, refs, configuration and rerere cache but has its own HEAD, index and files. After the change a rebuild never moves or modifies the trunk checkout, leaves nothing behind in the trunk if it crashes, and holds the workspace lock only for the steps that really conflict with other operations. The rebuild algorithm (strategy, fail modes, merge detection, soft deletion, position compaction, progress, rollback) and the public API keep their current meaning.

## Goals

- A rebuild never changes the trunk's HEAD, index or working-tree files. After a successful run, a fail-fast conflict, a continue-mode conflict or a cancelled run, `git symbolic-ref HEAD`, `git rev-parse HEAD`, `git status --porcelain` and the contents of a sample file in the trunk are identical to what they were before. The two exceptions are the one-time `_rebuild_temp` migration and the integration-branch-checked-out case (Requirement 2).
- Patches are applied in a worktree created for the run at `<workspace_root>/<slug>/rebuild/<job id>` and removed when the run ends, on every exit path. The `_rebuild_temp` branch is no longer created.
- The workspace lock (`wslock`) is held only for (a) the upstream fetch and base resolution and (b) the final integration-ref update, patch bookkeeping and optional push to `origin`. During patch application, `POST /sync`, rollback, rerere forget, batch rebase, merge jobs and the post-push working-tree reset all proceed.
- Archive and reclone answer `409 workspace_busy` for the whole run, because they delete the directory the worktree lives in. They are guarded by a new per-workspace rebuild-active flag in `internal/wslock`.
- The patch tips applied by a run are fixed when it starts (resolved to SHAs). If a patch branch or the upstream base moved during the run, a follow-up rebuild is enqueued instead of the change being lost.
- Stale rebuild worktrees left by a crash are removed before the next rebuild of that workspace and once at hub startup, without touching the trunk.
- Rerere keeps working: resolutions recorded in one rebuild replay in the next, and `GET /rerere` and `DELETE /rerere/*` read and forget the same `rr-cache`.
- Endpoints, response shapes, job records and existing audit events are unchanged, apart from one additive field `patch_results[].source_sha` and one new audit event `hub.rebuild.followup`. Only the documented `409 workspace_busy` behaviour narrows.

## Non-goals

- **Replacing the git CLI with go-git for the rebuild.** go-git v5.19.1 reportedly has no linked-worktree, cherry-pick, non-FF merge or rerere support (see Verified External API). The rebuild keeps using `gitcmd.GitRunner`. Existing go-git use (fetch, push, post-push reset, sync fast-forward, `DefaultPushIntegrationFunc`) is neither extended nor removed.
- **Changing where patch branches live** (hub-local versus mirrored to the fork; see `docs/adr/01-choose-the-authority-for-patch-branches.md`). This spec fetches and pushes no patch branch.
- **Moving merge jobs, sync or the push hook into worktrees.** They keep using the trunk. The `rebuild/<job id>` layout leaves room for a later `merge/<job id>`.
- **Guarding `DELETE /workspaces/:slug`.** It requires an archived workspace, and archive is refused while a rebuild is active. The residual race (a rebuild that starts on an archived workspace whose clone was retained) ends in a failed rebuild, not corruption.
- **Concurrent rebuilds of one workspace.** The queue serialises by group key and dedups on `(type, key)`. The guard also rejects a second rebuild of the same workspace.
- **Multi-replica hubs.** `wslock` is in-process and stays so.
- **Sparse or partial checkouts** of the worktree.
- **Changing the rebuild algorithm** (strategy selection, fail modes, merge detection, soft deletion, compaction, progress, rollback semantics).
- **Pinning the trunk's checkout to the workspace branch.** Today it is wherever the last merge job or push left it; that stays so.
- **Following up on a retryable failure.** A retried job re-snapshots on its own.

## Background

Every workspace is a non-bare clone at `<workspace_root>/<slug>/trunk`, shared by the git smart HTTP server, sync, merge jobs, rollback, rerere forget and rebuilds. `internal/wslock` (a per-slug `sync.Mutex` map offering `Lock` for jobs and `TryLock` for handlers) keeps them apart.

What the code does today (`internal/carrypatch/rebuild_executor.go`, `HandleRebuildJob`):

- Takes `wslock.Lock(slug)` before the upstream fetch and holds it to the end, including the DB bookkeeping and the optional `PushIntegration` to `origin`.
- Records the trunk's checkout (`currentCheckout`), runs `preflightCleanup` (`cherry-pick/merge/rebase --abort` plus `HardReset("HEAD")`), then `checkout -B _rebuild_temp <base>`.
- Cherry-picks (`applyRebasePatch`: `git log --reverse --no-merges --right-only --cherry-pick <base>...<branch>`) or merges (`applyMergePatch`: `MergeNoFF(branchName)`) in the trunk, using `git rerere` through `handleConflictWithRerere`.
- Force-updates the integration branch with `branch -f <integration> HEAD`, then `restoreCheckout` (`checkout --force`) and `branch -D _rebuild_temp`.
- Cleanup runs with the job context. On cancellation that context is already dead and `GitRunner.runWithExitCode` returns `ctx.Err()` without starting git, so cleanup silently does nothing. This is why `preflightCleanup` exists.

Other facts the design depends on:

- Handlers that `TryLock`: `internal/workspace/handlers.go` (archive), `reclone_handler.go`, `sync_handler.go`, `internal/carrypatch/sync_handlers.go` (carry-patch sync), `rerere_handlers.go`, `api.go` (rollback), `internal/merge/api.go`. The merge job uses blocking `Lock`. `internal/gitserver/handlers.go` `updateHeadSHA` does `TryLock` and skips the go-git hard reset when the lock is held.
- Rollback runs `git branch -f <integration> <previous sha>`.
- The jobs queue (`internal/jobqueue/jobqueue.go`) dedups `Enqueue` on `(type, key)` for jobs in status `queued` or `running`. `claimAndExecute` will not start a queued job while another job of the same type and group key is running. `Job` carries `GroupKey`; the running job's ID is available via `jobqueue.JobIDFromContext`.
- `carrypatch.GitRunner` is an interface (`Run`, `CherryPick`, `MergeNoFF`, `MergeTree`, `IsAncestor`, `Cherry`, `HardReset`) implemented in production by `GitRunnerAdapter` over `*gitcmd.GitRunner` (`wire.go`) and in tests by `mockGitRunner` (`testhelpers_test.go`). `RebuildHandler.NewGitRunner(repoPath)` builds one; `gitcmd.New` requires the directory to exist and enforces git ≥ 2.38.
- The rerere handlers read `<trunk>/.git/rr-cache` and `<trunk>/.git/MERGE_RR` directly.
- `carryPatchPostCloneSetup` (`internal/workspace/queue.go`) already enables rerere in repository config at clone time.
- `docs/carry_patch_workflow.md` ("Rebuild algorithm", about line 810) and `docs/examples/AGENTS_carry_patch.md` (lines about 455 and 557) describe `_rebuild_temp`. `docs/api.md` ("Workspace Lock", error table, per-endpoint 409 rows) and `docs/openapi.yaml` describe `workspace_busy`. The only existing ADR is `docs/adr/01-...`.

Three problems in the originating proposal were found by reading the code, and the requirements below handle them:

1. A follow-up rebuild enqueued from inside the running rebuild is rejected by the `(type, key)` dedup, because the running job is itself `running`. The queue needs a way to ignore the caller.
2. The proposal says a sync that advances upstream during a rebuild is harmless because "the next scheduled sync catches up". It does not. Carry-patch sync writes `upstream_head_sha` and only enqueues a rebuild when upstream advanced relative to that stored value, so after a sync that ran during a rebuild the next sync sees no change and never rebuilds. Today this cannot happen because the sync gets 409 and records nothing. Narrowing the lock would turn that into a silently lost rebuild unless the rebuild checks for upstream drift itself.
3. `git branch -f` refuses to move a branch that is checked out in any worktree, including the trunk, so the final ref update must use `update-ref`.

## Requirements

### 1. Worktree lifecycle

For each run the rebuild creates a detached worktree with a typed `WorktreeAdd` call that runs `git worktree add --detach <path> <upstream base sha>` in the trunk. `<path>` is `<workspace_root>/<slug>/rebuild/<job id>`. It must be made absolute (`filepath.Abs`), because git resolves a relative path against the runner's working directory, which is the trunk. When the context carries no job ID (unit tests) a random ID is used. When `WorkspaceRoot` is empty the path is derived from the same parent as the trunk path, so the existing unit tests that use a fake runner keep working.

A second `GitRunner` is built for the worktree with `h.NewGitRunner(worktreePath)`, after the directory exists. All patch application runs through it: `log`, `rev-parse`, cherry-pick, merge, `rerere`, `diff --diff-filter=U`, `commit --no-edit`, `reset --hard` (continue-mode rollback to the pre-patch HEAD) and reading the worktree HEAD. The trunk runner is used only for fetch-adjacent reads, `config`, worktree add/remove/prune, the `_rebuild_temp` migration (Requirement 2) and the final ref update.

The worktree is removed on every exit path: success, conflict, transient error, panic-free early return and cancellation. Removal is typed `WorktreeRemove` (`git worktree remove --force <path>`) followed by `WorktreePrune` (`git worktree prune`). If `worktree remove` fails, the rebuild falls back to `os.RemoveAll(path)` then prune. Removal is best-effort, is logged on failure, and never changes the job outcome. Removal and any other cleanup run with a context derived from `context.WithoutCancel(ctx)` and a bounded timeout (for example two minutes), so they still work after the job context is cancelled.

The three typed methods are added to `gitcmd.GitRunner`, to the `carrypatch.GitRunner` interface, to `GitRunnerAdapter` and to `mockGitRunner`. They are the only place worktree commands are spelled, so a later move to go-git touches one adapter. `MergeNoFF` gains a message parameter (`MergeNoFF(ctx, ref, message string) error`; an empty message keeps git's default) so Requirement 6 can merge a SHA while naming the branch.

Verification: a real-repository integration test runs a rebuild and asserts the worktree directory is gone and `git worktree list` in the trunk shows only the trunk after success, conflict, transient failure and cancellation.

### 2. The trunk is not touched

During a rebuild the trunk runner runs no `checkout`, `reset`, `cherry-pick`, `merge` or `rebase`. `currentCheckout`, `restoreCheckout` and `preflightCleanup` are deleted.

**Final ref update.** The integration branch is force-updated from the worktree's final HEAD SHA with `git update-ref refs/heads/<integration> <sha>` (the existing `UpdateRef` method). It never uses `branch -f` (which refuses checked-out branches) and never checks the integration branch out. The previous value is read with `rev-parse --verify refs/heads/<integration>` immediately before the update, under the lock, and recorded as `previous_integration_head_sha`; an absent branch leaves it empty as today.

**Exception A, integration branch checked out in the trunk.** If, under the final lock, the trunk's HEAD is the symbolic ref of the integration branch (possible after a merge job or a hub push), moving the ref would leave the trunk's index and files at the old commit. In that case, and only then, the rebuild runs `reset --hard HEAD` in the trunk after the ref update so the checkout matches the new tip, which is what the old restore step achieved. This is logged at info level.

**Exception B, one-time `_rebuild_temp` migration.** Under the first lock phase, if a local `refs/heads/_rebuild_temp` exists (left by a pre-upgrade crash or an interrupted pre-upgrade run), the rebuild removes it and logs that it did so. If the trunk's HEAD is on that branch, it first aborts any in-progress cherry-pick, merge or rebase (best-effort) and moves HEAD with `checkout --force <branch>`, where `<branch>` is the workspace's configured branch (`workspaceBranch`) if it exists as a local branch, else the short name of `refs/remotes/origin/HEAD` if that exists locally, else `checkout --detach --force` with a warning. After migration the branch is never created again.

Verification: the test in Requirement 1 compares trunk HEAD, `rev-parse HEAD`, `status --porcelain` and a sample file before and after a successful run, a fail-fast conflict, a continue-mode conflict and a cancelled run. Separate tests cover Exception A and Exception B.

### 3. Rerere across worktrees

`rerere.enabled=true` and `rerere.autoupdate=true` are repository-level and are set with the trunk runner before the worktree is created, as the rebuild does today. The hub does not enable `extensions.worktreeConfig`. `rr-cache` lives in the common git directory, so replay and recording in the worktree use the same cache the rerere endpoints read. `MERGE_RR` is per-worktree in git; it is only populated while a conflict is in progress and the worktree is discarded afterwards, so the endpoints' use of `<trunk>/.git/MERGE_RR` is unaffected in practice. The implementation must not assume any of this without the test below.

Verification: an integration test records a rerere resolution in one rebuild's worktree, runs a second rebuild and asserts the resolution is replayed with no conflict, and that `GET /workspaces/:slug/rerere` lists the entry and `DELETE` forgets it.

### 4. Lock phases

The run proceeds in this order:

1. Resolve upstream credentials (as today; failure is a retryable `TransientError`).
2. `wslock.Lock(slug)` (blocking, as today). Under it, set the rebuild-active guard (Requirement 5).
3. **Phase one, under the lock:** upstream fetch, trunk runner creation, upstream base resolution (`resolveUpstreamBase`), the `_rebuild_temp` migration, stale worktree cleanup (Requirement 8), rerere config. Then unlock.
4. Create the worktree, list patches, snapshot tips (Requirement 6), apply patches. **No lock held.**
5. **Phase two, re-acquire `wslock.Lock(slug)`:** read the previous integration head, update the integration ref (with Exception A), soft-delete `merged_upstream` patches, compact positions, optional `PushIntegration` to `origin`. Unlock.
6. Remove the worktree, run the stale-input check (Requirement 7), emit `hub.rebuild.complete`, clear the guard, return.

The lock is released on every early return in phase one and phase two. Patch application and worktree creation and removal never run under the lock.

During step 4, `POST /sync`, rollback, rerere forget, batch rebase and merge jobs proceed as if no rebuild were running. A rollback during step 4 is legitimate: the rebuild's later `update-ref` overwrites it, and `previous_integration_head_sha` reflects the value at phase two. Because rollback and the rebuild's final update both take the lock, they cannot interleave writes to the integration branch. A `hub.rebuild.fail` event is still emitted on any error return.

If the hub is waiting on `Lock` while archive or reclone run and the workspace is removed, the fetch then fails and the job is retried by the queue under its existing retry policy; no new behaviour is added.

Verification: a test with a fake runner that blocks inside a patch step asserts sync, rollback and rerere forget succeed meanwhile and that the lock is not held then.

### 5. Rebuild-active guard

`internal/wslock` gains a per-workspace guard, independent of the mutex: `BeginRebuild(slug string) (end func(), ok bool)` and `RebuildActive(slug string) bool`. `BeginRebuild` is called by the rebuild while it holds the workspace lock, so there is no gap against archive and reclone, and returns `ok=false` if a guard is already set for that slug. In that case the rebuild returns a retryable error. `end` is idempotent and is called (deferred) after the worktree has been removed on every exit path, so the guard covers the interval from before stale cleanup until after worktree removal.

Archive (`internal/workspace/handlers.go`, `clone_status == "ready"` branch) and reclone (`reclone_handler.go`) call `RebuildActive(slug)` after a successful `TryLock` and, when it is true, release the lock and answer `409` with `error_type: workspace_busy` and the existing message. Behaviour of archive and reclone when no rebuild is active is unchanged. A process crash clears the guard because it is in memory.

Verification: tests assert archive and reclone return `409 workspace_busy` while the guard is set and the lock is free, and succeed after `end()`; and that sync, rollback and rerere forget are not rejected by the guard.

### 6. Snapshotting patch tips

After the patch list is sorted and before the first patch is applied, the rebuild resolves each patch that will be attempted (status not `merged_upstream`, `disabled` or `deleted`) with `rev-parse --verify refs/heads/<branch>^{commit}` in the trunk runner. From then on only the resulting SHA is used for `git log`, cherry-pick and merge. A branch that does not resolve (error or empty output) is recorded as `skipped` / `branch_not_found` at that point, as today; an empty commit list for the rebase strategy is also `branch_not_found`, as today.

- Rebase strategy: `git log ... <base>...<sha>`.
- Merge strategy: `MergeNoFF(ctx, sha, "Merge branch '<branch name>'")`. The merge commit message names the branch and the SHA.
- `PatchResult` gains `SourceSHA string` with JSON name `source_sha` (omitted when empty), set for every patch that was snapshotted, including ones that then conflicted. It appears in the job result, in the running job's progress, and therefore in the `patch_results` of rebuild API responses. `docs/api.md` and `docs/openapi.yaml` list the new field.

Resolution is limited to `refs/heads/` (a tag or remote-tracking ref of the same name no longer matches), in line with ADR 01's finding that patch branches are only usable as local heads.

### 7. Stale-input follow-up

As the last step before returning, after worktree removal and after the phase-two lock is released, the rebuild checks whether its inputs moved during the run:

- **Patch tips:** for each patch with a snapshot SHA whose status was `success` or `conflict`, resolve `refs/heads/<branch>` again. If any now differs from its snapshot SHA, the patch is stale. Patches skipped for status (`merged_upstream`, `disabled`, `deleted`) are ignored.
- **Upstream base:** re-run `resolveUpstreamBase`. If it differs from the SHA the run used, upstream is stale.

A stale patch tip enqueues a follow-up unless the workspace variable `AUTO_REBUILD_AFTER_PUSH` is `"false"`. A stale upstream enqueues one unless `AUTO_REBUILD_AFTER_SYNC` is `"false"`. At most one follow-up is enqueued per run. The check runs for a successful run and for a fail-fast conflict exit (a push that fixes the conflicting patch while the run was applying must not be lost). It does not run on retryable errors, cancellation or other failures.

The follow-up carries the same payload (strategy, fail mode and integration branch as the original) with `SubmittedBy: "system:stale-snapshot"`, a fresh nonce, `Type: "rebuild"`, `Key: <slug>`, and the running job's `GroupKey` (read with `Queue.GetByID(jobID)`, falling back to `FormatGroupKey(slug, payload.IntegrationBranch)`). To make this possible `jobqueue.EnqueueParams` gains `ExcludeJobID string`: when set, the active `(type, key)` duplicate check (both the pre-insert query and the race-recovery query) ignores the job with that ID. The new job is inserted as `queued` and, because it shares the group key, does not start until the current job is finalised. A later push hook or sync enqueue is then deduplicated against the queued follow-up, as intended. With a nil `Queue` (unit tests) or an enqueue error, the rebuild logs a warning and the job outcome is unchanged. A `hub.rebuild.followup` audit event is emitted when a follow-up is enqueued, with `stale_patches` (branch names), `upstream_stale` (bool) and `follow_up_job_id` in metadata.

A residual window remains between the final re-resolution and the job being finalised in which a push or sync enqueue is still deduplicated against the running job. It is tolerated; the final check is placed last to keep it to milliseconds.

Verification: a test pushes to a registered patch branch while a rebuild is blocked mid-application (controllable fake for one step), asserts the run applied the snapshot SHA, the trunk's HEAD moved to the pushed commit, a follow-up job exists in `queued` state with `submitted_by = system:stale-snapshot`, and `hub.rebuild.followup` was emitted. Further tests cover upstream drift, both variables set to `"false"`, and a `jobqueue` test for `ExcludeJobID`.

### 8. Cancellation and crash recovery

On cancellation of the job context during patch application the rebuild discards the worktree (removal with `--force` also discards any in-progress cherry-pick or merge), ends the guard, and returns a retryable `TransientError`. The cleanup uses the non-cancelled context from Requirement 1. A context error from any git call is treated as cancellation and takes this path.

If the hub dies mid-run, nothing needs doing in the trunk. Before creating its worktree, and under the guard, the next rebuild of the workspace removes every directory under `<workspace_root>/<slug>/rebuild/` (`os.RemoveAll`) and runs `git worktree prune` in the trunk. This replaces `preflightCleanup`.

On startup, before `mergeQueue.Start()` in `cmd/af-hub/main.go`, the hub calls a new `carrypatch.CleanupStaleRebuildWorktrees(ctx, workspaceRoot, logger)` that, for every `<workspace_root>/*/rebuild` directory, removes it, runs `git worktree prune` in the matching `trunk` (skipped with a warning if the trunk is missing), and logs each removal. Errors are logged and never prevent startup. Running before the queue starts guarantees no rebuild is concurrently using one of those directories.

Verification: a test creates a registered-but-orphaned worktree directory under `rebuild/` (and a plain stale directory), then asserts the next rebuild succeeds and the directories and registrations are gone; another test covers the startup cleanup function, including a workspace without a trunk.

### 9. Coexistence with go-git, pushes and gc

A `git push` through the hub during patch application updates refs and resets the trunk's working tree exactly as when no rebuild runs (the `TryLock` in `updateHeadSHA` now succeeds, so the reset happens). It does not affect the worktree. A `POST /sync` during patch application fetches upstream and updates `upstream_head_sha`; the running rebuild keeps the base it resolved in phase one, and Requirement 7 ensures the drift produces a follow-up. Objects reachable only from the worktree's HEAD survive any `git gc` because git treats every worktree's HEAD as a root; the rebuild never runs `gc --prune=now` or `reflog expire`.

An integration test with a real repository, while a linked rebuild worktree is registered under the trunk, exercises every go-git path the hub uses on the trunk and asserts each succeeds and leaves the worktree usable: `git.PlainOpen` plus `Worktree().Reset(&git.ResetOptions{Mode: git.HardReset})` (post-push reset), `defaultSyncUpdateLocalRefFn` (sync fast-forward in `internal/workspace/sync_git.go`), `upstream.Fetch`, and `DefaultPushIntegrationFunc` (to a local bare remote). This test is the evidence for the coexistence assumption. If it fails, the design must change before implementation continues.

### 10. Observability, documentation and decision record

Worktree creation and removal, stale worktree cleanup (per-run and startup) and the `_rebuild_temp` migration are logged at info level with workspace slug and path. `hub.rebuild.complete` and `hub.rebuild.fail` are emitted as today. Removal failures are logged as warnings.

Documentation is updated in the same change:

- `docs/api.md` ("Workspace Lock", the `workspace_busy` row, and the 409 rows for sync, rollback, rerere forget, archive, reclone) and `docs/openapi.yaml`: sync, rollback, rerere forget and batch rebase are rejected only during a rebuild's fetch phase and final phase; archive and reclone are rejected for the whole rebuild. Also document `patch_results[].source_sha`.
- `docs/carry_patch_workflow.md`: rewrite the "Rebuild algorithm" steps 3–6 for the worktree flow, state that a rebuild does not change the trunk's checkout, that clones served by the hub during a rebuild see the pre-rebuild integration branch until the run completes, and that a stale patch tip or upstream triggers a follow-up rebuild. Update the lock note near line 1068.
- `docs/examples/AGENTS_carry_patch.md`: the two `_rebuild_temp` statements are removed or marked as legacy.
- A new `docs/adr/02-run-rebuilds-with-the-git-cli.md` records the decision to implement the worktree rebuild with the git CLI rather than go-git, including the capability table (worktree add/remove/prune, cherry-pick, non-FF merge, rerere, gc/prune) as reported by go-git v5.19.1's `COMPATIBILITY.md`, and the condition for revisiting it (go-git gaining linked-worktree, cherry-pick, `--no-ff` merge and rerere support, e.g. in v6).

Existing rebuild unit tests that use the fake runner are updated to expect the worktree commands and `update-ref` instead of `checkout -B`, the restore sequence and `branch -f`.

## Design Decisions

1. **One worktree per run, detached, no temporary branch.** A persistent worktree would carry half-finished state between runs and bring back the cleanup problem. A per-run detached worktree makes crash recovery "delete the directory" and leaves nothing in the trunk's ref namespace.
2. **git CLI through `GitRunner`, not go-git.** go-git v5.19.1 has no linked-worktree creation, cherry-pick, non-FF merge or rerere, and re-implementing them would change an algorithm this spec keeps fixed and put `rr-cache` compatibility at risk. Worktree commands sit behind typed methods so this can be revisited.
3. **Split the lock; add a separate guard instead of keeping the lock.** Only the fetch and the final ref/DB phase write shared state. A mutex cannot express "held long, others may proceed", so archive and reclone, which delete the directory, check a flag in `wslock` instead.
4. **Set the guard under the lock, check it under the lock.** Both sides use the workspace mutex around the flag, so archive cannot slip in between the rebuild acquiring the lock and setting the guard.
5. **Rebuild keeps blocking `Lock`; no special "retryable while archive holds the lock" rule.** Waiting for the lock already gives that behaviour, and a vanished workspace fails at the fetch and is retried by the queue as today.
6. **Cleanup uses `context.WithoutCancel` with a timeout.** The existing cleanup silently did nothing on cancellation because it reused the cancelled context; the worktree design depends on removal actually running.
7. **Final ref update uses `update-ref`, not `branch -f`.** `branch -f` refuses a branch checked out in any worktree, including the trunk. `update-ref` always works.
8. **Exception A: hard-reset the trunk when it has the integration branch checked out.** Moving a ref under a checked-out branch would leave the trunk inconsistent; the old restore step covered this case implicitly. The reset runs only in that case, so the common case is untouched.
9. **Exception B: migrate a legacy `_rebuild_temp` once.** A pre-upgrade crash can leave the trunk on that branch with a half-applied pick, and the removed `preflightCleanup` was what dealt with it. The target branch is the workspace branch, then `origin/HEAD`, then a detached HEAD, because the workspace branch may be unset or missing locally.
10. **Snapshot tips as SHAs resolved at `refs/heads/<branch>`.** With the lock released, branch names move. Using SHAs makes the run deterministic and `source_sha` tells the operator what was applied. Limiting to `refs/heads/` matches ADR 01.
11. **Follow-up needs `jobqueue.EnqueueParams.ExcludeJobID`.** The queue dedups on `(type, key)` over `queued` and `running` jobs, so a follow-up enqueued by the running rebuild would be deduplicated against itself. Excluding the caller lets it queue; the shared group key keeps it from starting until the current job finishes.
12. **Follow-up also covers upstream drift.** Carry-patch sync only enqueues when `upstream_head_sha` changes, and a sync during a rebuild now stores the new value, so without a rebuild-side check the new upstream is never rebuilt. The proposal's "next sync catches up" is incorrect. Patch drift honours `AUTO_REBUILD_AFTER_PUSH`; upstream drift honours `AUTO_REBUILD_AFTER_SYNC`.
13. **Follow-up only for patches that were attempted (`success` or `conflict`) and for fail-fast conflict exits.** Disabled, merged and deleted patches take no part in the run, so their movement is irrelevant. A fix pushed during a run that failed on that patch should still be rebuilt. Retryable failures re-snapshot on retry.
14. **One follow-up per run, enqueued last.** Enqueuing after worktree removal and after unlocking keeps the unprotected window (a push or sync deduplicated against the still-running job) to milliseconds.
15. **`MergeNoFF` takes a message.** Merging a SHA would otherwise produce "Merge commit '<sha>'" and lose the branch name from history.
16. **No git-version check added.** The proposal asked for a check against 2.17; `gitcmd.New` already refuses git older than 2.38, which covers `worktree add --detach` and `worktree remove`.
17. **The review PRD erratum is dropped.** The proposal asked to mark `docs/prd/review_2026_09_codebase.md` superseded; that file does not exist in the repository. The `_rebuild_temp` descriptions that do exist are in `docs/carry_patch_workflow.md` and `docs/examples/AGENTS_carry_patch.md`, and are updated instead.
18. **`DELETE /workspaces/:slug` is not guarded.** It already requires an archived workspace, and archiving is refused while a rebuild is active.
19. **`source_sha` is additive.** It is the only change to a response shape, is omitted when empty, and gives operators the applied tip.
20. **Sparse checkout is not used.** It would change what cherry-pick sees and complicate rerere; cost is one extra tree per rebuilding workspace, freed at the end of the run or at startup.
21. **Merge jobs stay in the trunk.** They also delete the source branch and run a check command whose working directory is part of the workspace contract. Moving them is a separate change.

## Dependencies

| Spec | Relationship | Why |
|------|--------------|-----|
| `10_durable_job_queue` | modifies | `EnqueueParams` gains `ExcludeJobID` so a running job can enqueue a follow-up of its own type and key. |
| `11_git_runner` / `14_git_runner_extensions` | modifies | `gitcmd.GitRunner` gains `WorktreeAdd`, `WorktreeRemove`, `WorktreePrune`; the `carrypatch` adapter and interface gain the same plus the `MergeNoFF` message parameter. |
| `16_carry_patch_operations` | modifies | Rebuild executor, progress and result shape (`source_sha`), audit events (`hub.rebuild.followup`), docs. |
| `15_carry_patch_workspace` | depends | Trunk layout, `upstream` remote, rerere config at clone time, `integration_branch`. |
| `13_upstream_sync` | depends | Sync fast-forward path (`sync_git.go`) must keep working while a worktree is registered; reclone is guarded. |
| `05_workspace_checkout` | modifies | Archive answers `409 workspace_busy` while a rebuild is active. |
| `06_git_server` | depends | Post-push `updateHeadSHA` reset must run during patch application and keep working with a registered worktree. |
| `12_merge_operations` | depends | Merge jobs keep using blocking `wslock.Lock` and now run during patch application. |
| `18_hub_audit_query` | modifies | New `hub.rebuild.followup` event type. |

Also needed: `internal/wslock` (guard), `cmd/af-hub/main.go` (startup cleanup), `internal/workspace/reclone_handler.go` (guard check). No new Go modules. `go-git` stays at v5.19.1.

## Verified External API

go-git source is not readable from the module cache in this environment, so the signatures below are taken from calls that already compile in this repository, and the capability claims are taken from the issue's reading of go-git v5.19.1's `COMPATIBILITY.md`. The last item below must be re-checked when the ADR is written.

| Symbol (`github.com/go-git/go-git/v5`) | Use in this repo | Role in this spec |
|---|---|---|
| `git.PlainOpen(path string) (*git.Repository, error)` | `gitserver/handlers.go`, `workspace/sync_git.go`, `carrypatch/wire.go`, `upstream/upstream.go` | Still opens the trunk, unchanged, while a linked worktree is registered (Requirement 9 test). |
| `(*Repository).Worktree() (*git.Worktree, error)` and `(*Worktree).Reset(*git.ResetOptions) error` with `ResetOptions{Commit plumbing.Hash, Mode git.ResetMode}` and `git.HardReset` | post-push reset, sync fast-forward | Must still work on the trunk while the rebuild worktree exists. |
| `(*Repository).PushContext(ctx, *git.PushOptions) error`, `git.NoErrAlreadyUpToDate` | `DefaultPushIntegrationFunc` | Runs in the final phase, unchanged. |
| `(*Repository).Storer.SetReference(*plumbing.Reference) error` | `defaultSyncUpdateLocalRefFn` | Unchanged. |
| Linked-worktree creation, `cherry-pick`, `--no-ff` merge, `rerere`, `gc`/`prune` | **NOT FOUND** as go-git features (per the issue's reading of `COMPATIBILITY.md`; `PlainOpenOptions.EnableDotGitCommonDir` only opens an existing linked worktree) | The reason the CLI is used. Not re-verified here. |

Git CLI behaviours this design relies on, to be confirmed by the integration tests rather than assumed: `git worktree add --detach <abs path> <sha>` resolves a relative path against the working directory; `git worktree remove --force` removes a worktree with a conflicted index; `rr-cache` and `rerere.*` config are shared across linked worktrees while `MERGE_RR` is per worktree; `git branch -f` refuses a branch checked out in any worktree while `git update-ref` does not.
