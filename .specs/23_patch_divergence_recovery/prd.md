---
spec_id: "23"
spec_name: "patch_divergence_recovery"
title: "Patch Divergence Recovery: Reset to Origin, Backup-Ref Visibility, Protection and Cleanup"
status: "active"
created_at: "2026-10-01T14:43:42.131229Z"
updated_at: "2026-10-01T14:43:42.131229Z"
intent_hash: "6acf1dec6f0d798d3664e29d1914798165276f6e5e230b3dd113bd316fd38fc8"
schema_version: 2
source: "https://github.com/agent-fox-dev/hub/issues/35"
---
## Intent

When the fork is authoritative for patch branches, sync may discard a hub-side branch tip. It saves that tip under `refs/hub/replaced/<branch>` first. This spec makes the saved tip usable and keeps it tidy. An operator can see that a tip was replaced and fetch it back. An operator can move a patch branch to the fork's tip on demand, for example after a `diverged` report. Clients cannot overwrite the hub's backup refs by pushing. The refs are removed when the patch they belong to is removed or purged. A workspace that never replaces a branch behaves exactly as before.

## Goals

- `GET /workspaces/:slug/patches/:id` exists and returns the patch record, plus `replaced_sha` when `refs/hub/replaced/<branch>` exists. Today there is no single-patch GET route.
- `POST /workspaces/:slug/patches/:id/reset-to-origin` moves a patch branch to the fork's current tip regardless of `PATCH_DIVERGENCE_POLICY`. It saves any discarded hub tip first and returns the patch record. `afc patch reset-to-origin <slug> <patch-id>` calls it.
- A branch moved by a reset triggers the same auto-rebuild as a branch moved by sync, so the move is not lost.
- A client cannot create, update or delete a ref under `refs/hub/replaced/` through the hub's git server. Fetching those refs keeps working.
- `afc patch remove` (`DELETE /workspaces/:slug/patches/:id`) removes the patch's backup ref.
- Purging soft-deleted patches removes their backup refs, and a row whose ref cannot be removed is kept for the next purge.
- Every existing response field, patch status and job record shape is kept. Changes are additive.

## Non-goals

This is the fourth of five scopes. The other scopes have their own specs, and nothing below is built here.

- **`fork_patch_sync`** (`20_fork_patch_sync`, written). It owns `PATCH_BRANCH_SOURCE`, `PATCH_DIVERGENCE_POLICY`, the origin fetch in sync, the patch refresh and writing `refs/hub/replaced/<branch>`. This spec only reads the backup refs, writes one on reset, and removes them.
- **`fork_patch_registration`** (`21_fork_patch_registration`, written). It owns branch resolution at registration and the single-branch fork fetch. This spec reuses that fetch.
- **`fork_push_control`** (`22_fork_push_control`, written). It owns `PUSH_PATCHES_TO_ORIGIN` and the pre-receive hook. This spec adds one fixed rule at the same interception point, but no new hook mechanism.
- **`patch_authority_docs`**. It owns the restructure of `docs/carry_patch_workflow.md`, `docs/examples/AGENTS_carry_patch.md`, and ADR 01 status and errata. This spec edits the guide's patch-endpoint table only.
- **A scheduler for purging soft-deleted patches.** Nothing calls `PurgeExpiredDeletedPatches` in production today (see Background). This spec makes the purge clean up refs when it runs. It does not decide when it runs.
- **More than one backup per branch.** The latest replacement overwrites the previous one.
- **A restore-from-backup endpoint.** Recovery is `git fetch <hub_url> refs/hub/replaced/<branch>` and a push to the fork. Nothing resets a branch back to the backup.
- **`replaced_sha` on the list endpoint, in `patch-status`, or in the patch object returned by other endpoints.**
- **Listing or deleting backup refs independently of patches.**
- **Merging, rebasing or conflict-resolving on reset.** The branch is moved. Nothing is merged.
- **Changing `AUTO_REBUILD_AFTER_PUSH`, the follow-up logic of `01_rebuild_worktree`, or the rebuild's use of patch refs.**

## Background

**What exists today** (read in the code):

- The patch REST handlers live in `internal/workspace/patch_handlers.go`: `handleAddPatch`, `handleListPatches`, `handleUpdatePatch`, `handleRemovePatch` and `handleRestorePatch`. `RegisterRoutes` in `internal/workspace/routes.go` mounts them. There is no `GET /workspaces/:slug/patches/:id`. `docs/api.md` and `docs/openapi.yaml` document only `PATCH`, `DELETE` and `POST .../restore` on that path.
- `patchResponse`, `scanPatch`, `getPatch` and `patchSelectColumns` (`internal/workspace/patch_store.go`) render a patch row. `getPatch` does not filter on status, so it also returns soft-deleted rows.
- The `workspace` and `carrypatch` packages do not import each other. Git-dependent behaviour reaches the workspace handlers through hooks. The workspace package defines the hook type (`BranchCheckFunc` in `branch_check_hook.go`, `CarryPatchSyncFunc` in `sync_hook.go`). `carrypatch` implements it and `cmd/af-hub/main.go` registers it.
- `handleRemovePatch` hard-deletes the row (`deletePatchAndCompact`) and emits `hub.patch.delete`. It touches no git state, and it does not check the workspace status.
- `carrypatch.SQLPatchStore.SoftDeletePatch` sets `status='deleted'`, `deleted_at` and a negative position. `PurgeDeletedPatches(ctx, olderThan)` deletes expired rows across all workspaces and returns only a count. `PurgeExpiredDeletedPatches(ctx, store)` applies a hard-coded 7-day cutoff. **Neither is called from production code.** Only `internal/carrypatch/soft_delete_test.go` calls them. Soft-deleted patches are therefore never purged today.
- The git server (`internal/gitserver/handlers.go`) serves `info/refs` for `git-upload-pack` and the fetch itself with the `git` CLI (`uploadpack.go`), which advertises every ref in the trunk. Pushes go through go-git's receive-pack on the trunk. Spec 22 adds a per-ref pre-receive interception to that path.
- `carrypatch.GitRunner` provides `Run`, `IsAncestor`, `HardReset` and `UpdateRef`. `NewGitRunnerFactory()` builds one for `<workspace_root>/<slug>/trunk`.
- Carry-patch handlers serialise on the trunk with `wslock.TryLock(slug)`. A busy lock answers `409` with error type `workspace_busy` and the message `another operation is running on this workspace; retry later`.
- `emitHubAudit(c, audit.HubEvent)` (`internal/workspace/audit_emit.go`) fills the actor from the request's auth info.
- The rebuild enqueue in sync uses `jobqueue.EnqueueParams` with type `rebuild`, key `slug`, group `slug:integration_branch` and `SubmittedBy` set to the caller. A deduplicated enqueue leaves `rebuild_triggered` false.

**What the earlier scopes deliver that this spec uses:**

- Spec 20 writes `refs/hub/replaced/<branch>` before a `replace` outcome. It parses `PATCH_BRANCH_SOURCE`. It provides a compare-and-swap ref-move helper that also resets the working tree when the moved branch is the trunk's checked-out branch. It provides the `PatchStore` methods that write `origin_sync_state`, `origin_sha` and `origin_synced_at`. It defines the `hub.patch.replace` audit event, the origin credential resolver, and the `502` messages `origin fetch failed` (error type `origin_fetch_failed`) and `failed to resolve origin credentials`.
- Spec 21 provides a single-branch fork fetch (`+refs/heads/<name>:refs/remotes/origin/<name>`, no tags, no prune) and its classification of "branch not on origin", separate from other fetch failures.
- Spec 22 provides the interception of ref writes in the git server's receive session, with a per-ref error that reaches the client as `ng <ref> <message>`.

The names of those helpers are fixed by the earlier implementations. This spec refers to them by role.

**Why this scope needs more than the issue lists:**

1. The issue extends `GET /workspaces/:slug/patches/:id`, but the route does not exist. It is added here.
2. A reset that reads only the existing `refs/remotes/origin/<branch>` can use a stale tracking ref. In `hub` mode nothing refreshes it after the clone. The reset therefore fetches that one branch first.
3. A branch moved by a reset looks `in_sync` at the next sync. Without a rebuild trigger of its own, the move would never reach the integration branch until something else changed. Spec 20 has the same reasoning for sync.
4. The purge routine is unwired, so DR-4's "when the row is purged" has no production trigger. This spec makes the routine correct and leaves the schedule to a later decision.
5. Always writing a backup on reset would let a harmless fast-forward overwrite the one backup that holds real discarded work.

## Requirements

### 1. `GET /workspaces/:slug/patches/:id`

The route is mounted next to the other patch routes and requires `patches:read` (a `patches:write` PAT also passes, as for the list endpoint). Ownership works as for the other patch endpoints: the owner or an admin, and `404 workspace not found` otherwise. An unknown patch id answers `404 patch not found`. A soft-deleted patch is returned with its `deleted` status.

The response is the existing patch object (`patchResponse`, including any `origin_*` fields spec 20 adds). When `refs/hub/replaced/<branch>` resolves to a commit, it also carries `replaced_sha`, the full 40-character SHA. The field is omitted when the ref does not exist, when the workspace's trunk is not on disk (for example an archived workspace), when no recovery hook is registered, and when the lookup fails. A lookup failure is logged at warn level and never fails the request. The other patch endpoints are unchanged and do not carry `replaced_sha`.

Verification: tests cover the field present, the field absent without a backup, a soft-deleted patch, an archived workspace, a non-owner (`404`), a PAT without `patches:read` (`403`) and an unknown id.

### 2. `POST /workspaces/:slug/patches/:id/reset-to-origin`

The endpoint requires `patches:write`. Ownership and the "workspace is not active" and "workspace is not in carry_patch mode" `400` responses work as in `handleRestorePatch`. A workspace whose `clone_status` is not `ready` answers `409` with the message `workspace clone is not ready`.

Preconditions on the patch, checked in this order:

- An unknown id answers `404 patch not found`.
- A patch named like the workspace's integration branch answers `400 patch branch is the integration branch`. The integration branch is never moved by this endpoint.
- A patch with status `merged_upstream` or `deleted` answers `409` with the message `patch status <status> cannot be reset`. Only `active`, `conflict` and `disabled` patches are eligible, matching the candidates of sync.

The endpoint takes the workspace lock with `wslock.TryLock(slug)` and holds it for the whole operation. A busy lock answers `409 workspace_busy` with the message above. It is not taken before the checks above succeed.

The success response is `200` with the patch object as `GET` renders it (including `replaced_sha` when a backup exists after the reset). It also carries `rebuild_triggered` (boolean) and `rebuild_job_id` (string, only when a job was enqueued), the same meaning as in the sync response. The reset works in both `PATCH_BRANCH_SOURCE` modes and is not limited by `PATCH_DIVERGENCE_POLICY`.

Verification: handler tests cover each status code and message above, with the hook stubbed. Integration tests cover requirement 3.

### 3. Reset semantics

Under the workspace lock, the reset does the following.

1. **Fetch the branch.** It runs the spec 21 single-branch fetch `+refs/heads/<branch>:refs/remotes/origin/<branch>` with origin credentials from the spec 20 resolver (`GIT_PAT`, then `GIT_USERNAME`/`GIT_PASSWORD`, then none). The fetch runs in both modes, because the operator is asking for the fork's tip now.
   - A credential failure answers `502 failed to resolve origin credentials`.
   - A fetch failure answers `502 origin fetch failed` with error type `origin_fetch_failed`.
   - A branch the fork does not have answers `409` with error type `missing_on_origin` and the message `branch does not exist on origin`.
   - Error text from the fetch is logged and is not echoed.
   - In all three cases nothing is written.
2. **Resolve both tips to SHAs.** The fork tip is `refs/remotes/origin/<branch>^{commit}`. The local tip is `refs/heads/<branch>^{commit}`, and may be absent.
3. **Apply the first matching rule.**
   - *Local branch missing.* Create `refs/heads/<branch>` at the fork tip with a compare-and-swap create. Action `created`. No backup is written.
   - *Same commit.* Write nothing. Action `none`. The response is still `200`.
   - *The local tip is a strict ancestor of the fork tip* (a fast-forward). Move the local branch. Action `fast_forwarded`. No backup is written, because no commit is discarded.
   - *Anything else* (diverged, or the fork tip is a strict ancestor of the local tip). First write the local tip to `refs/hub/replaced/<branch>`, overwriting any earlier backup. Then move the local branch. Action `replaced`.

   Ancestry uses `GitRunner.IsAncestor` on SHAs. The move reuses spec 20's compare-and-swap helper: `update-ref <ref> <new> <expected-old>`, with `update-ref` and never `checkout`, `reset` or `branch -f`, and a hard reset of the working tree when the moved branch is the trunk's checked-out branch. If spec 20's implementation does not expose that helper in a form `carrypatch` can call from here, it is extracted and shared, not duplicated.
4. **Lost race.** A hub push can land between the SHA resolution and the write. If the compare-and-swap fails, the endpoint answers `409` with error type `ref_changed` and the message `patch branch changed during reset; retry`. A backup already written stays. It points at the tip the branch had when it was read, and a retry overwrites it. Any other write failure answers `500 failed to update patch branch <branch>`.
5. **Persisted sync state.** When `PATCH_BRANCH_SOURCE` is `origin`, a reset that completes (any action) records the patch's state through spec 20's `PatchStore` method as `in_sync`, with `origin_sha` set to the fork tip and `origin_synced_at` set to now. The dashboard then no longer shows `diverged` for the patch. When the source is `hub`, nothing is written, because spec 20 clears that state at each hub-mode sync.
6. **Rebuild.** When the branch moved (action `created`, `fast_forwarded` or `replaced`) and the patch status is `active` or `conflict`, the endpoint enqueues a `rebuild` job exactly as sync does: same type, key, group, payload builder and deduplication, with `SubmittedBy` set to the caller. It does so unless `AUTO_REBUILD_AFTER_SYNC` is `"false"`. A moved `disabled` patch does not trigger a rebuild, because the rebuild skips it. A deduplicated enqueue leaves `rebuild_triggered` false. An enqueue error is logged and does not fail the reset.

Verification: integration tests with a real bare fork and a real trunk, asserting the local ref, the backup ref, the response, the persisted state and the rebuild for each rule. They also cover:
- the trunk's checked-out branch being moved and the working tree following;
- a fast-forward not touching an existing backup;
- a repeated `replaced` reset overwriting the backup;
- the three `502` and `409` paths leaving every ref unchanged;
- a hub push racing the move (`ref_changed`);
- `AUTO_REBUILD_AFTER_SYNC=false` suppressing the rebuild;
- a `disabled` patch not triggering a rebuild;
- a reset in `hub` mode writing no persisted state.

### 4. CLI: `afc patch reset-to-origin`

`afc patch reset-to-origin <workspace-slug> <patch-id>` takes exactly two arguments, sends `POST /api/v1/workspaces/<slug>/patches/<id>/reset-to-origin` with no body, and prints the JSON result. It is added to `PatchCmd` in `internal/cli/patch_cmd.go`, with the same `SilenceErrors`/`SilenceUsage` and `apikit.CLIHandleError` handling as `patch restore`. Its help text names the backup ref and the `replaced_sha` field.

Verification: a CLI test with a stub server covers the path, the method, the printed result and error propagation.

### 5. Protecting `refs/hub/replaced/*` in the git server

The git server never accepts a client push that creates, updates or deletes a ref whose name starts with `refs/hub/replaced/`. The rule is built into `internal/gitserver`. It applies to every workspace and every credential with `git:write`, and it does not depend on a registered pre-receive hook or on the workspace mode.

It is evaluated at the spec 22 interception point, before the registered pre-receive hook is consulted, so the rejection is a per-ref status. The client sees `unpack ok` and `ng <ref> refs/hub/replaced/ is maintained by the hub and cannot be pushed to`. Other refs in the same push are processed as usual. The rejected ref keeps its old value. Because spec 22's accepted-refs-only rule applies, a rejected ref triggers no `head_sha` update, no audit entry and no post-push hook.

Hub-internal writes (sync, reset, removal) do not pass through a client push and are unaffected. Other names under `refs/hub/` are not covered by this rule. Spec 22 is the only other user of that namespace (`refs/hub/forward/`), and it removes its own temporary refs.

Fetching is unchanged. The `git` CLI serves `git-upload-pack` and advertises every trunk ref, so `git ls-remote` and `git fetch <hub_url> refs/hub/replaced/<branch>` already work. A test asserts this, so a later change to the advertisement cannot silently break recovery.

Verification: gitserver tests with a real `git push` cover:
- a rejected create, update and delete;
- a push mixing a `refs/hub/replaced/` ref with a normal branch, where the branch is written and only the protected ref is rejected;
- a standard-mode workspace getting the same rejection;
- a client fetching a backup ref by name;
- the backup ref being listed by `ls-remote`.

### 6. Backup cleanup on `afc patch remove`

After `deletePatchAndCompact` succeeds in `handleRemovePatch`, the handler asks the recovery hook to delete `refs/hub/replaced/<branch>` for the removed patch (`git update-ref -d`). Removal is best effort.

- A missing ref counts as success.
- A missing trunk, as for an archived workspace, counts as success and is logged at info level.
- Any other failure is logged at warn level with slug, branch and error. It never changes the response: `204` is still returned, because the row is already gone.
- No workspace lock is taken. Deleting a single ref is atomic in git, and taking the lock would make `DELETE` start failing with `409` during a sync.

The patch row is looked up before deletion (`handleRemovePatch` already does so for the audit event), and the branch name is taken from that lookup. Without a registered hook, nothing is removed and removal works as today. Soft deletion by a rebuild does not remove the ref, so `POST .../restore` brings back a patch with its backup intact.

Verification: tests cover removal with and without a backup, a trunk that is missing, a failing hook still returning `204`, and soft-delete plus restore keeping the ref.

### 7. Backup cleanup on purge

Purging expired soft-deleted patches removes each purged patch's backup ref.

- The `PatchStore` interface gains a method that lists the soft-deleted rows older than a cutoff, with id, workspace slug and branch name. `SQLPatchStore` and the test doubles implement it.
- The purge routine, for each expired row, removes `refs/hub/replaced/<branch>` in that workspace's trunk. It then deletes that row (by id, only while `status='deleted'`). A row whose ref removal failed is left in place and retried at the next purge, with a warn log. A missing ref or a missing trunk counts as success.
- The routine returns the number of rows purged. `PurgeDeletedPatches` keeps its current signature and behaviour for existing callers.
- The routine takes the workspace root and the same git runner factory the other carry-patch code uses. The existing two-argument `PurgeExpiredDeletedPatches(ctx, store)` keeps working without ref cleanup. The ref-aware variant is a new function with the same 7-day retention.
- No scheduler is added and no call is wired into `cmd/af-hub/main.go`.

Verification: tests cover purge with and without a backup ref, a failing ref removal keeping the row, a missing trunk purging the row, and rows within the retention window being untouched.

### 8. Package boundaries and wiring

The workspace package defines a recovery hook type in a new file next to `branch_check_hook.go`, with a registration function and a nil default. It carries three operations:

- reading the replaced SHA for a slug and branch;
- removing the backup ref for a slug and branch;
- running a reset for a slug, a patch and the calling auth info, returning the action, the SHAs and the rebuild outcome, or a classified error.

The classified errors distinguish: workspace busy, branch missing on origin, origin fetch failure, credential failure, ref changed, and other failure. The handlers map them to the status codes and messages of requirement 3.

`internal/carrypatch` implements the hook with the git runner factory, the workspace root, the variable getter (`store.GetVariableValue`), the origin credential resolver, the single-branch fetch function (injectable, so tests can use a stub), the job queue, the spec 20 `PatchStore` methods and the `PATCH_BRANCH_SOURCE` parser. `cmd/af-hub/main.go` registers it next to the existing hooks. The workspace package does not import `carrypatch`, and `carrypatch` does not import `workspace`.

If the hook is not registered, `GET` omits `replaced_sha`, `DELETE` skips the cleanup, and the reset endpoint answers `500` with the message `patch reset is not configured`.

The route handlers use the existing workspace auth helpers (`requirePatchReadScope`, `requirePatchWriteScope`, `lookupPatchWorkspace`) built on `apikit.GetAuthInfo`, per the project steering. No local auth structs or custom context keys are added.

### 9. Audit and logging

A successful reset emits one `hub.patch.reset` event with `resource_type` `patch`, the workspace slug, the caller as actor, and metadata `{ branch_name, action, local_sha, origin_sha, replaced_sha }`. `action` is one of `none`, `created`, `fast_forwarded` or `replaced`. `local_sha` is the tip before the reset and is omitted when there was no local branch. `replaced_sha` is present only for `replaced`. The event type is added as a constant in `internal/audit/types.go` next to `EventRebuildFollowup`.

A reset with action `replaced` also emits `hub.patch.replace`, the event spec 20 defines for sync replacements, with `branch_name`, `replaced_sha`, `origin_sha` and an added `trigger` key set to `reset_to_origin`. Events emitted by sync carry no `trigger` key, and a consumer reads its absence as `sync`.

A failed reset emits nothing. Replacements and removals are logged at info level with slug, branch and the SHAs. A nil emitter skips emission, and an emit error is logged without affecting the response.

Verification: tests assert the event types and metadata for each action, the absence of events on failure, and a failing emitter not changing the outcome.

### 10. Documentation

All of the following are updated in the same change, per the project steering.

- **`docs/api.md`:**
  - a new `GET /api/v1/workspaces/:slug/patches/:id` section with `replaced_sha`;
  - a new `POST .../patches/:id/reset-to-origin` section with the request, response, errors, backup-ref semantics and the recovery command `git fetch <hub_url> refs/hub/replaced/<branch>`;
  - the removal section, noting that the backup ref is deleted;
  - the git-server push section, noting the `refs/hub/replaced/` rejection and its message;
  - the permissions table at the top (lines for `patches:read` and `patches:write`);
  - the new audit event.
- **`docs/openapi.yaml`:** the new `get` operation on `/api/v1/workspaces/{slug}/patches/{id}`, the new `/reset-to-origin` path with its `200`, `400`, `404`, `409` and `502` responses, and the `replaced_sha`, `rebuild_triggered` and `rebuild_job_id` properties.
- **`docs/cli.md`:** `afc patch reset-to-origin`.
- **`docs/permissions.md`:** the endpoint lists of `patches:read` and `patches:write`.
- **`docs/architecture.md`:** the recovery hook, if the hook list there names the others.
- **`docs/carry_patch_workflow.md`:** two rows in the patch-endpoint table only. The wider restructure, including a recovery walkthrough, belongs to `patch_authority_docs`.
- `docs/configuration.md` is unchanged, because this spec adds no variable or environment key.

## Design Decisions

1. **The single-patch GET route is added here.** The issue says to extend it, but it does not exist in `RegisterRoutes`, `docs/api.md` or the OpenAPI file. A `replaced_sha` field needs a read endpoint, and the route is small. It returns the existing patch object, so nothing else changes.
2. **`replaced_sha` appears on the single-patch GET only.** Reading a ref per row on the list endpoint would add a git call to every list request. The issue asks for the single-patch endpoint, and an operator looking at a `replaced` outcome already has the patch id.
3. **The reset and the removal hook sit behind a hook in the `workspace` package.** `patchResponse`, the auth helpers and `emitHubAudit` live in `workspace`, and git and the lock live in `carrypatch`. The two packages cannot import each other, and the existing sync and branch-check hooks solve the same split the same way.
4. **The reset fetches the one branch before moving it.** The issue says to move to `refs/remotes/origin/<branch>`. In `hub` mode that ref is as old as the clone, and in `origin` mode it is as old as the last sync. Resetting to it could move a patch to a stale tip and look like a success. The single-branch fetch from spec 21 is already available. The cost is a possible `502`, which is clearer than silent staleness.
5. **The reset writes a backup only when it discards commits.** The issue says to write the backup "first as in PB-5", and PB-5 is the diverged case. With one backup per branch, an unconditional write would let a fast-forward overwrite a backup that still holds real discarded work. A `replaced_sha` therefore always means that a tip was discarded.
6. **The reset enqueues a rebuild like sync does.** The issue says the endpoint returns the patch record. A branch moved by the reset reads `in_sync` at the next sync, so no later event would pick it up, which is the reasoning of spec 20's decision on ref-write failures. The trigger uses `AUTO_REBUILD_AFTER_SYNC`, the same opt-out as a fork-driven move. The response gains `rebuild_triggered` and `rebuild_job_id` as additive fields.
7. **Only `active`, `conflict` and `disabled` patches can be reset.** These are the statuses sync refreshes. A `merged_upstream` or `deleted` patch is no longer part of the rebuild, and moving its branch would only confuse the dashboard.
8. **A reset updates persisted sync state only in `origin` mode.** Spec 20 clears that state at every hub-mode sync and shows it only for workspaces that opted in. Writing it in `hub` mode would show a state that disappears at the next sync.
9. **The `refs/hub/replaced/` rule is built into the git server and independent of workspace mode.** The reserved ref is written only by the hub. A fixed rule needs no database lookup and cannot fail open on a query error. A rule inside the carry-patch hook would leave standard workspaces and a missing hook unprotected. It reuses spec 22's interception point rather than adding a mechanism.
10. **The protected prefix is `refs/hub/replaced/`, not all of `refs/hub/`.** That is what the issue names. Spec 22 keeps a temporary ref under `refs/hub/forward/`, and a broader rule could collide with the way it writes that ref.
11. **`DELETE` removes the backup ref on a best-effort basis and takes no lock.** The row is already gone, so a failed ref removal must not turn a completed delete into an error. Taking the workspace lock would make `DELETE` return `409` during a sync, which it does not today.
12. **Purge keeps a row until its ref has been removed.** Removing the row first would orphan the ref, and nothing else would ever find it. Leaving the row means the next purge retries, at the cost of an expired row living a little longer after a git failure.
13. **The purge routine is made correct, but not scheduled.** `PurgeExpiredDeletedPatches` has no production caller. Scheduling it would start permanently deleting rows that have never been deleted, which is a data-retention decision outside this scope. It is recorded as an open question.
14. **Errors on the reset use `409` for states, `502` for the fork and `500` for the hub's own failures.** A missing fork branch, a lost race and a busy workspace are conditions of the resource. A failure to reach the fork is an upstream failure, with the messages and error type already fixed by specs 20 and 21. Typed errors let scripts branch without matching text.
15. **The reset has its own `hub.patch.reset` event, and a replacement also emits `hub.patch.replace`.** Operators filter on `hub.patch.replace` to find every discarded commit, and sync emits it for the same reason. A `trigger` key tells the two sources apart, and its absence means sync, so spec 20's events do not change.
16. **No new CLI flags and no `--force`.** The endpoint is explicit by its name and already ignores the divergence policy. A confirmation flag would only duplicate the command's meaning.

## Dependencies

| Spec | Relationship | Why |
|------|--------------|-----|
| `20_fork_patch_sync` | depends | It writes `refs/hub/replaced/<branch>` and defines `PATCH_BRANCH_SOURCE` parsing, the compare-and-swap ref-move helper, the `PatchStore` methods for `origin_*` state, the origin credential resolver, the `502` messages and the `hub.patch.replace` event. This spec reads and removes the backup refs and reuses all of those. |
| `21_fork_patch_registration` | depends | The reset reuses its single-branch fork fetch and its "branch not on origin" classification, and follows its hook pattern between `workspace` and `carrypatch`. |
| `22_fork_push_control` | depends | The `refs/hub/replaced/` rejection is added at the receive-pack interception point and per-ref status path that spec introduces, and relies on its accepted-refs-only rule. |
| `01_rebuild_worktree` | depends | The rebuild enqueued by a reset is the job that spec's executor runs. Its snapshot and follow-up logic is unchanged. |
| `15_carry_patch_workspace` (archived) | modifies | The patch REST handlers and routes gain a `GET` by id and the reset endpoint. Patch removal gains the cleanup call. |
| `16_carry_patch_operations` (archived) | modifies | `PatchStore` gains the expired-patch listing, and the purge routine gains a ref-cleaning variant. |
| `06_git_server` (archived) | modifies | Receive-pack gains the fixed protected-namespace rule. |
| `18_hub_audit_query` (archived) | modifies | New hub event type `hub.patch.reset`, and an added `trigger` key on `hub.patch.replace`. |
| `09_git_credentials` (archived) | depends | `ResolveCloneAuth` for the fork credentials used by the reset's fetch. |

## Verified External API

No new external module is used. `github.com/go-git/go-git/v5` v5.19.1 (go.mod) is used only indirectly, through the fetch from spec 21 and the receive-pack path of spec 22. Its source is outside the repository and could not be read.

Git CLI commands run through `GitRunner.Run`. Their behaviour is standard git, and is exercised by the integration tests:

| Command | Use |
|---|---|
| `git update-ref <ref> <new> <expected-old>` | Compare-and-swap move. An all-zero old value means "must not exist". |
| `git update-ref <ref> <sha>` (`GitRunner.UpdateRef`) | Force-write the backup ref. |
| `git update-ref -d <ref>` | Delete a backup ref. Exit 0 when the ref is absent. **Unverified** for the repo's git versions. The test for a missing ref confirms it. |
| `git rev-parse --verify --end-of-options <ref>^{commit}` | Resolve a tip. A non-zero exit means the ref is absent. |
| `git merge-base --is-ancestor` (`GitRunner.IsAncestor`) | Ancestry on SHAs, with the exit-code discrimination in `internal/gitcmd/ancestor.go`. |

Repository symbols this spec calls, read in this repo: `carrypatch.GitRunner.{Run, IsAncestor, UpdateRef, HardReset}`, `carrypatch.NewGitRunnerFactory`, `carrypatch.PatchStore`, `carrypatch.SQLPatchStore.{SoftDeletePatch, PurgeDeletedPatches}`, `carrypatch.PurgeExpiredDeletedPatches`, `carrypatch.BuildRebuildPayload`, `workspace.patchResponse`, `workspace.getPatch`, `workspace.requirePatchReadScope`, `workspace.requirePatchWriteScope`, `workspace.lookupPatchWorkspace`, `workspace.emitHubAudit`, `workspace.ResolveCloneAuth(store *secrets.Store, slug string) (transport.AuthMethod, error)`, `wslock.TryLock(slug) (unlock func(), ok bool)`, `jobqueue.EnqueueParams`, `apikit.GetAuthInfo`, `apikit.WriteAPIError`, `apikit.WriteAPIErrorWithType`, `apikit.CLIHandleError` and `apikit.CLIClientFromCmd`.

The following come from specs 20, 21 and 22. They are not yet in the code, and their names are fixed by those specs' implementations:

- the `PATCH_BRANCH_SOURCE` parser;
- the origin credential resolver;
- the compare-and-swap ref-move helper;
- the `PatchStore` methods that write origin sync state;
- the single-branch fork fetch and its classification of a missing branch;
- the receive-pack interception point.

Treat all of them as **unverified**. If a helper is not reachable from `carrypatch` (for example, it sits in a test-only file or is unexported in another package), the implementation extracts it instead of duplicating it.
