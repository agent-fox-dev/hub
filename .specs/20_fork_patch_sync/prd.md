---
spec_id: "20"
spec_name: "fork_patch_sync"
title: "Fork-Authoritative Patch Branch Sync"
status: "active"
created_at: "2026-10-01T14:21:48.852422Z"
updated_at: "2026-10-01T14:21:48.852422Z"
intent_hash: "3c7be7e44f83f39ecaa575303a2df20574b32bae706859b0e340627ae52ca0d5"
schema_version: 2
source: "https://github.com/agent-fox-dev/hub/issues/35"
---
## Intent

The carry-patch workflow rebuilds an integration branch from upstream HEAD plus an ordered list of patch branches. Today the hub is the only place those branches are read from, and sync never looks at the fork (`origin`). This spec lets a carry-patch workspace declare that the fork is authoritative for patch branches. In that mode, sync fetches the fork and brings every registered patch branch to the fork's tip. A defined divergence policy applies when the hub's copy and the fork's copy disagree. A patch branch that moved counts as a reason to rebuild, the same as an upstream advance. Per-branch outcomes are reported in the sync response and persisted for the patch-status dashboard. A workspace that does not opt in behaves exactly as before. The authority decision is recorded in `docs/adr/01-choose-the-authority-for-patch-branches.md`.

## Goals

- A workspace variable `PATCH_BRANCH_SOURCE` selects `hub` (default) or `origin` as the authority for patch branches, and is read on every sync, so a change takes effect on the next sync without a restart.
- With `PATCH_BRANCH_SOURCE=origin`, `POST /workspaces/:slug/sync` fetches `origin` and, for every registered patch whose status is `active`, `conflict` or `disabled`, does one of the following:
  - creates the local branch when it is missing;
  - fast-forwards it when the fork is ahead;
  - applies `PATCH_DIVERGENCE_POLICY` (`replace`, the default, or `report`) when the two have diverged;
  - records `missing_on_origin` when the fork has no such branch.
- Before a replacement discards a hub-side tip, that tip is saved under `refs/hub/replaced/<branch>`.
- A patch branch that was created, fast-forwarded or replaced during sync triggers the same auto-rebuild as an upstream advance, under the same `AUTO_REBUILD_AFTER_SYNC` control and queue deduplication. This includes a sync where upstream did not move.
- `last_sync_at` is updated on every completed carry-patch sync, so the dashboard shows when the fork and upstream were last checked.
- The sync response reports per-branch outcomes (`patches_synced`, `patches_diverged`, `origin_fetched`). Each patch persists its last sync state (`origin_sync_state`, `origin_sha`, `origin_synced_at`), and the patch-status dashboard shows it plus two summary counts.
- With the default `hub` source, no `origin` fetch happens, no patch ref is written, and existing sync responses gain only `origin_fetched: false`.
- Every existing API response field, patch status and job record shape is kept; changes are additive.

## Non-goals

This spec is the first of five scopes. The later scopes each get their own spec, and nothing below is built here.

- **`fork_patch_registration`**: making `POST /workspaces/:slug/patches` and `afc patch add` find branches that exist only as `refs/remotes/origin/*` or on the fork (the registration changes of the issue, PR-1 to PR-4).
- **`fork_push_control`**: `PUSH_PATCHES_TO_ORIGIN`, the git server's per-ref rejection and forwarding of hub pushes in `origin` mode, the hub-mode mirror to the fork, and the `hub.patch.mirror_failed` event. Until it ships, a push to a registered patch branch on the hub is still accepted in `origin` mode. The next sync replaces it (with a backup ref) or reports it, according to the policy.
- **`patch_divergence_recovery`**: `POST /workspaces/:slug/patches/:id/reset-to-origin`, `afc patch reset-to-origin`, `replaced_sha` on `GET /workspaces/:slug/patches/:id`, rejecting pushes to `refs/hub/replaced/*`, and removing backup refs when a patch is removed or purged. Until it ships, backup refs written here are not cleaned up automatically.
- **`patch_authority_docs`**: the restructure of `docs/carry_patch_workflow.md` (Remotes table, "Where patch branches live", the fork-first Getting Started walkthrough, conflict resolution for both modes), `docs/examples/AGENTS_carry_patch.md`, and moving ADR 01 to `Accepted`.
- **Changing what sync fetches from `upstream`**, how merged patches are detected, or how the integration branch is built.
- **A fork-authoritative integration branch.** Sync never fetches the integration branch from `origin`.
- **Fetching the fork in standard-mode workspaces.** Their sync path is unchanged.
- **Bidirectional merge on divergence.** One side is replaced or reported, never merged.
- **Recording manual conflict resolutions into rerere from a fork push, webhooks from the fork's host, a built-in sync schedule, and restricting the fork fetch to registered branch names.** These are follow-ups.
- **Auto-disabling a `missing_on_origin` patch.** The status is left alone and the state is surfaced.
- **Changing `AUTO_REBUILD_AFTER_PUSH` or the follow-up logic of `01_rebuild_worktree`.**

## Background

**Carry-patch sync today** (`internal/carrypatch/sync_handlers.go`, `runCarryPatchSync`, reached through `NewCarryPatchSyncHook` and `workspace.RegisterCarryPatchSyncHook`):

- It takes `wslock.TryLock(slug)` (409 `workspace_busy` otherwise) and resolves upstream credentials.
- It fetches the `upstream` remote only, through `cfg.Fetch` (`upstream.Fetch` in `internal/upstream/upstream.go`). A failure answers `502 upstream fetch failed`.
- It resolves the upstream base with `resolveUpstreamBase` and compares it to `workspaces.upstream_head_sha`.
- If upstream did not advance, it returns early with empty `patches_merged` and `rebuild_triggered: false`, and writes nothing. `last_sync_at` is therefore written only when upstream advanced.
- Otherwise it writes `upstream_head_sha`, `last_sync_at` and `updated_at`, runs merge detection over `active` patches, and enqueues a `rebuild` job unless `AUTO_REBUILD_AFTER_SYNC` is `"false"`. The job uses key `slug`, group `slug:integration_branch` and `SubmittedBy` set to the caller.
- The response fields (`patches_merged`, `rebuild_triggered`, `rebuild_job_id`, `force_push_detected`) are built by `CarryPatchSyncResponse` / `asExtras` and merged by `internal/workspace/sync_handler.go` into the workspace JSON.
- `SyncAPIConfig` is wired in `cmd/af-hub/main.go`.

**Patch branches today.** The rebuild uses only `refs/heads/<branch>` in `<workspace_root>/<slug>/trunk`. After clone, the fork's branches exist only as `refs/remotes/origin/*`, and only a push to the hub's git server creates a local patch ref.

**Existing pieces this spec reuses:**

- `carrypatch.GitRunner` has `Run`, `IsAncestor` and `UpdateRef`, with the adapter in `wire.go`. `gitcmd.IsAncestor` runs `merge-base --is-ancestor` with exit-code discrimination.
- `workspace.ResolveCloneAuth(store, slug)` resolves origin credentials in the order `GIT_PAT`, then `GIT_USERNAME`/`GIT_PASSWORD`, then none. The same function backs `DefaultPushIntegrationFunc` for `REBUILD_PUSH_INTEGRATION_BRANCH`.
- `upstream.Fetch` shows the go-git fetch pattern: `repo.Remote(name)`, `remote.FetchContext` with `RefSpecs`, `Auth` and `Tags: git.NoTags`, and `git.NoErrAlreadyUpToDate` treated as success.
- Workspace variables are read through `GetVariableFunc` (`store.GetVariableValue`). An unset variable returns an error, which sync treats as the default.
- Patches live in the `patches` table (`internal/workspace/schema.go`: `createPatchesTableSQL`, with idempotent `patchFieldDDL` for added columns). The workspace package's `Patch`, `scanPatch`, `patchSelectColumns` and `patchResponse` (`patch_store.go`) back the REST patch responses. `carrypatch.SQLPatchStore` has its own `SELECT` and the `PatchStore` interface.
- Hub audit events are `audit.HubEvent` values passed to `audit.Emitter.Emit`, and event type strings are constants in `internal/audit/types.go` (`EventRebuildFollowup`).
- `handlePatchStatus` (`internal/carrypatch/api.go`) builds the dashboard from its own `SELECT` over `patches`.
- `docs/configuration.md` does not list workspace variables; they are documented in `docs/api.md` ("Workspace Variables Reference") and `docs/carry_patch_workflow.md` ("Configuration").

**Interaction with `01_rebuild_worktree` (active, implemented).** A rebuild applies patches in a worktree without holding the workspace lock. It snapshots patch tips as SHAs. After the run it enqueues a follow-up if a tip moved, unless `AUTO_REBUILD_AFTER_PUSH` is `"false"`. A sync that moves a patch branch while a rebuild is running is deduplicated against the running job. The running job's own stale-tip check then covers it, under `AUTO_REBUILD_AFTER_PUSH`, not `AUTO_REBUILD_AFTER_SYNC`. This spec does not change that. A workspace that sets `AUTO_REBUILD_AFTER_PUSH=false` and `AUTO_REBUILD_AFTER_SYNC=true` can miss a rebuild for a fork change that lands during a run; the next sync that moves a patch or upstream rebuilds it.

**Problems in the issue found by reading the code, handled below:**

1. go-git's fetch does not prune stale remote-tracking refs. After the fork deletes a branch, `refs/remotes/origin/<branch>` would stay, and `missing_on_origin` could never be detected. The origin fetch must prune.
2. Auto-rebuild enqueue sits behind the "upstream HEAD has not changed" early return. A patch-only change would never reach it, so the early return moves after the patch refresh.
3. Merge detection runs only when upstream advanced. When a patch tip changes with upstream unchanged, detection must also run on the new tips.
4. Git-server pushes do not take the workspace lock. A push could land between sync's ancestry check and its ref write, so ref writes must be compare-and-swap.

## Requirements

### 1. Configuration

`PATCH_BRANCH_SOURCE` is a workspace variable with values `hub` and `origin`. It is read through `GetVariable("workspace", slug, "PATCH_BRANCH_SOURCE")` at the start of each carry-patch sync, after the workspace lock is taken. Any other value, an unset variable or a lookup error means `hub`. The match is exact and case-sensitive.

`PATCH_DIVERGENCE_POLICY` is a workspace variable with values `replace` (default) and `report`, read in the same way. Any other value means `replace`. It is ignored when the source is `hub`.

Setting either variable needs no change to the variables API, because variables are free-form keys. The two variables are documented in `docs/api.md` ("Workspace Variables Reference") and in the Configuration section of `docs/carry_patch_workflow.md`. `docs/configuration.md` does not list workspace variables and is not changed.

Verification: unit tests cover unset, `hub`, `origin`, an unrecognised value and a variable-lookup error for both variables.

### 2. Origin fetch

When the source is `origin`, carry-patch sync fetches the `origin` remote of the trunk with:

- refspec `+refs/heads/*:refs/remotes/origin/*`;
- no tags;
- pruning of tracking refs whose branch no longer exists on the fork;
- credentials resolved with `workspace.ResolveCloneAuth` (`GIT_PAT`, then `GIT_USERNAME`/`GIT_PASSWORD`, then none).

An already-up-to-date result is success.

Credential resolution for `origin` happens before either fetch, so a failure leaves everything unchanged. It answers `502` with the message `failed to resolve origin credentials`.

The order is: upstream fetch, then origin fetch, then upstream-base resolution, patch refresh, merge detection and the workspace update. All of this happens under the lock the sync already holds.

An origin fetch failure answers `502` with the message `origin fetch failed` and `error_type` `origin_fetch_failed`, so an operator can tell it apart from `upstream fetch failed`. The failure leaves `upstream_head_sha`, `last_sync_at`, patch statuses, persisted sync state and every `refs/heads/*` ref unchanged, even though the upstream fetch has already succeeded. The upstream tracking refs it updated are not rolled back. They are harmless, because `upstream_head_sha` is unchanged and the next sync sees the advance again.

When the source is `hub`, `origin` is never fetched and origin credentials are never resolved.

Because the fetch function and the origin-credentials resolver are injected (`SyncAPIConfig` gains `FetchOrigin` and `ResolveOriginAuth`, wired in `cmd/af-hub/main.go`), tests can substitute a counting stub. If the source is `origin` and `FetchOrigin` is nil, sync answers `500` with `origin fetch is not configured`.

Verification:
- A test shows `hub` performs zero origin fetches and zero origin credential lookups, using a counting stub.
- A test shows a failing origin fetch after a successful upstream fetch leaves `upstream_head_sha`, `last_sync_at`, patch statuses, persisted sync state and patch refs unchanged.
- An integration test with a real bare fork shows a branch deleted on the fork loses its `refs/remotes/origin/<branch>` ref after sync.

### 3. Bringing patch branches to the fork's tip

This runs only when the source is `origin`, after both fetches.

Candidates are the workspace's patches, in position order, whose status is `active`, `conflict` or `disabled`. Patches in `merged_upstream` or `deleted` are not candidates. The integration branch is never a candidate, even if a patch row has its name.

For each candidate, resolve the local tip (`refs/heads/<branch>^{commit}`) and the fork tip (`refs/remotes/origin/<branch>^{commit}`) to SHAs, then apply the first rule that matches:

1. **Fork has no such branch.** The local branch is left unchanged. State is `missing_on_origin`, action is `none`.
2. **No local branch.** Create `refs/heads/<branch>` at the fork tip. Action is `created`.
3. **Same commit.** Nothing is written. State is `in_sync`, action is `none`.
4. **Local tip is a strict ancestor of the fork tip.** Move the local branch to the fork tip. Action is `fast_forwarded`.
5. **Diverged** (neither is an ancestor of the other, or the fork tip is a strict ancestor of the local tip):
   - Under `replace`: first write the local tip to `refs/hub/replaced/<branch>`, overwriting any earlier backup for that branch. Then move the local branch to the fork tip. Action is `replaced`, and the element carries the replaced SHA.
   - Under `report`: leave the local branch unchanged. State is `diverged`, action is `none`, and both SHAs are reported.

After `created`, `fast_forwarded` and `replaced`, the state is `in_sync`, because the branch now equals the fork tip.

Ancestry is checked with `GitRunner.IsAncestor` on SHAs, not names.

Ref writes use `git update-ref <ref> <new> <expected-old>` through `GitRunner.Run`. The expected old value is the SHA resolved at the start of the step, or the all-zero SHA for a create. A push that lands between the check and the write therefore makes the write fail instead of being overwritten. Ref writes never use `checkout`, `reset` or `branch -f`.

If the moved branch is the one the trunk's `HEAD` currently points at (`symbolic-ref -q HEAD` equals `refs/heads/<branch>`), the working tree is hard-reset to the new tip afterwards (`GitRunner.HardReset(ctx, "HEAD")`), so the trunk's files match its HEAD, as the post-push reset does.

A failed ref write (including a lost compare-and-swap) stops the refresh at that patch. Outcomes already produced for earlier patches are kept and persisted. Sync still enqueues the auto-rebuild decision for branches that already moved (Requirement 4), does not update `upstream_head_sha`, and answers `500` with the message `failed to update patch branch <branch>`. The next sync starts again and sees the unmoved branches as still needing work.

A branch moved by `created`, `fast_forwarded` or `replaced` counts as "patch advanced" if the patch status is `active` or `conflict`. A moved `disabled` patch is refreshed and reported but does not count, because the rebuild skips disabled patches.

Verification: integration tests with a real fork repository and a real upstream repository cover:
- fork branch missing (rule 1);
- local branch missing (rule 2);
- equal tips (rule 3);
- fast-forward (rule 4);
- divergence under `replace`, asserting the backup ref holds the old tip and the local ref the fork tip;
- divergence under `report`, asserting the local ref is unchanged;
- the fork tip being an ancestor of the local tip (also divergence);
- the trunk's checked-out branch being moved and the working tree following;
- a disabled patch not triggering a rebuild;
- a ref-write failure persisting earlier outcomes and returning `500`.

Each case asserts the local ref, the backup ref, the response element and the persisted state.

### 4. Rebuild trigger and sync completion

The "upstream HEAD has not changed" early return moves after the patch refresh.

Merge detection (Requirement 5 of spec 16, unchanged in method) runs when upstream advanced or any patch branch counted as advanced in Requirement 3, so refreshed tips are examined. With the `hub` source it runs exactly when upstream advanced, as today.

An auto-rebuild is enqueued when upstream advanced, a patch branch counted as advanced, or at least one patch was newly marked `merged_upstream`. It is not enqueued when `AUTO_REBUILD_AFTER_SYNC` is `"false"`. The enqueue payload, key, group and deduplication are unchanged, so a patch-triggered and an upstream-triggered rebuild collapse into one job. `rebuild_triggered` and `rebuild_job_id` keep their meaning, including `rebuild_triggered: false` when the enqueue is deduplicated.

When neither upstream nor any patch changed, the response is as today: empty `patches_merged` and `rebuild_triggered: false`.

`last_sync_at` (with `updated_at`) is written on every sync that completes, whether or not anything advanced, in both modes. `upstream_head_sha` is still written only when upstream advanced. On the ref-write failure path of Requirement 3, `last_sync_at` is not written.

Verification:
- A test shows a patch-only change with unchanged upstream enqueues a rebuild.
- A test shows `AUTO_REBUILD_AFTER_SYNC=false` suppresses it.
- A test shows `last_sync_at` is updated on a no-change sync in both modes.
- A test shows a no-change sync still returns empty `patches_merged` and `rebuild_triggered: false`.
- Existing sync tests that assert no write on an unchanged sync are updated.

### 5. Sync response and CLI

The carry-patch sync response gains:

- `origin_fetched` (boolean, always present): `true` when the fork was fetched in this sync, `false` otherwise.
- `patches_synced` (array, present only when the source is `origin`, empty when there are no candidates). Each element is `{ "branch_name", "action", "state", "local_sha", "origin_sha", "replaced_sha" }`, where:
  - `action` is one of `none`, `created`, `fast_forwarded`, `replaced`;
  - `state` is one of `in_sync`, `diverged`, `missing_on_origin`;
  - `local_sha` is the local tip after the refresh, omitted when there is no local branch;
  - `origin_sha` is omitted when the fork has no such branch;
  - `replaced_sha` is present only for `replaced`.
- `patches_diverged` (array of branch names, present only when the source is `origin`): the branches whose state is `diverged`. It is always `[]` under `replace`.

In the standalone route's JSON body (`CarryPatchSyncResponse`) and in the extras merged into the workspace response (`asExtras`), an empty `patches_synced` or `patches_diverged` is still serialised as `[]` in `origin` mode. A zero-length slice with `omitempty` must not drop the field.

`afc workspace sync` prints these fields in the JSON it already prints and exits `0` on success even when `patches_diverged` is not empty. A new `--fail-on-diverged` flag makes it exit `3` after printing when `patches_diverged` is not empty, with a message naming the branches. With `--wait`, the exit-3 check runs after the rebuild wait finishes, and a wait failure (exit `1`) takes precedence. Exit codes `1` and `2` keep their current meaning.

Verification: handler tests cover the field presence rules in both modes, and CLI tests cover exit `0` and exit `3`, `--wait` combined with `--fail-on-diverged`, and the unchanged fields.

### 6. Persisted sync state and dashboard

The `patches` table gains three nullable columns through the existing idempotent `patchFieldDDL` mechanism. There is no backfill.

- `origin_sync_state` (`in_sync`, `diverged` or `missing_on_origin`);
- `origin_sha`;
- `origin_synced_at` (RFC 3339 UTC via `apikit.NowUTC`).

In `origin` mode, each candidate patch gets its state, the fork tip SHA (null for `missing_on_origin`) and the timestamp written during sync. `merged_upstream` and `deleted` rows have the three columns cleared. In `hub` mode, every row of the workspace has them cleared on each carry-patch sync. A hub-mode workspace never shows origin state.

Writing and clearing go through additive methods on `PatchStore`, `SQLPatchStore` and the test doubles.

The workspace package's `Patch`, `scanPatch`, `patchSelectColumns` and `patchResponse` expose `origin_sync_state`, `origin_sha` and `origin_synced_at` on every REST patch response, omitted when null. `GET /workspaces/:slug/patch-status` includes the same three fields per patch (omitted when null) and adds `patches_diverged` and `patches_missing_on_origin` to its summary. The summary counts are always present and are `0` in `hub` mode.

Verification: tests cover the migration on a database that already has a `patches` table, repeated `initSchema` runs, the response and dashboard shapes, clearing in `hub` mode, and clearing for `merged_upstream` patches. The carrypatch test helper DDL for the `patches` table is updated to include the columns.

### 7. Audit and logging

In `origin` mode, each completed sync emits one `hub.patch.sync` audit event. Its metadata is `{ origin_fetched, created[], fast_forwarded[], replaced[], diverged[], missing_on_origin[] }`, each list holding branch names. Each `replaced` outcome also emits a `hub.patch.replace` event with `branch_name`, `replaced_sha` and `origin_sha`, because it discards commits.

The events use the calling credential as actor, `resource_type` `patch` and the workspace slug. Their type strings are added as constants in `internal/audit/types.go`, following `EventRebuildFollowup`. `SyncAPIConfig` gains an optional `Audit audit.Emitter`, wired in `main.go`. A nil emitter skips emission, and an emit error is logged without affecting the sync.

In `hub` mode no `hub.patch.sync` event is emitted. The ref-write failure path still emits the events for the outcomes already produced. Replacements are logged at info level with slug, branch and both SHAs.

Verification: tests assert event types and metadata for a mixed sync, absence in `hub` mode, and that a failing emitter does not fail the sync.

### 8. Documentation

All of the following are updated in the same change, per the project steering:

- **`docs/api.md`:**
  - the sync response table and "Carry-Patch Sync Flow" (origin fetch, patch refresh, the early-return move, `last_sync_at` on every sync, rebuild on patch change);
  - the two new variables;
  - `AUTO_REBUILD_AFTER_SYNC` text, which now also covers patch changes;
  - the patch response schema and patch-status fields;
  - the `502 origin fetch failed` and `502 failed to resolve origin credentials` messages;
  - the new audit events.
- **`docs/openapi.yaml`:** the sync description and 200/502 responses, `CarryPatchSyncResponse` (including the always-present `origin_fetched`), the patch schema, and the patch-status schema.
- **`docs/cli.md`:** the `--fail-on-diverged` flag and exit code `3`.
- **`docs/carry_patch_workflow.md`:** the Configuration entries for `PATCH_BRANCH_SOURCE` and `PATCH_DIVERGENCE_POLICY`, and one short note under the sync section. The note covers the two models and that, until `fork_push_control` ships, hub pushes to patch branches are replaced by the next sync in `origin` mode.

## Design Decisions

1. **Workspace variables, not columns.** Every carry-patch behaviour is a workspace variable today, and the variables API already accepts free-form keys. This avoids a workspace migration and an API change on the workspace object. It matches ADR 01.
2. **`replace` is the default divergence policy.** The point of `origin` mode is that the fork is the truth. Leaving a stale hub copy in place would keep rebuilding it. The backup ref and the dedicated audit event are the safety net, and `report` is the opt-out.
3. **The origin fetch prunes.** go-git does not remove stale remote-tracking refs on fetch. Without pruning, a branch deleted on the fork would still resolve at `refs/remotes/origin/<branch>` and `missing_on_origin` could never occur.
4. **Origin credentials resolve before any fetch.** A credential failure then leaves the workspace untouched, and the two fetches each use their own credentials. This keeps a public fork with a private upstream (and the reverse) working.
5. **`origin fetch failed` gets its own `error_type`.** The message alone distinguishes it from the upstream failure, and the type lets scripts do the same. The upstream error is left as it is.
6. **The state describes the branch after the sync, the action describes what was done.** A `created` branch is `in_sync` afterwards, and `missing_on_origin` with action `none` is a reported condition. This keeps `state` a stable three-value enum that maps directly to the persisted column and dashboard counts.
7. **Ref writes are compare-and-swap.** The git server does not take the workspace lock, so a hub push can land mid-sync. `update-ref` with the expected old value turns that race into a visible error rather than lost work. `IsAncestor` on SHAs, not names, keeps the check consistent with the write.
8. **A ref-write failure stops the refresh, keeps earlier outcomes, and still triggers the rebuild for branches already moved.** A moved branch looks `in_sync` on the next sync, so skipping the rebuild decision would lose the rebuild. `upstream_head_sha` is not written, so the next sync re-detects an upstream advance.
9. **A moved `disabled` patch is refreshed but does not trigger a rebuild.** The rebuild skips disabled patches, so a rebuild would do nothing. `conflict` patches do trigger one, since a fork-side fix is what the operator is waiting for.
10. **Merge detection also runs when only patch tips changed.** The issue requires merge detection to see the refreshed tips. Under `hub` it runs exactly when upstream advanced, so existing behaviour is unchanged.
11. **A newly merged patch also triggers a rebuild.** It changes the rebuild's input. Today this was implied because detection ran only on an upstream advance.
12. **`last_sync_at` is written on every completed sync, in both modes.** The issue asks for it in order to show when the fork was last checked, and a `hub` workspace gets the same correction. It is not written on the ref-write failure path, because that sync did not complete.
13. **`origin_fetched` is always present, `patches_synced` and `patches_diverged` only in `origin` mode.** One always-present boolean tells clients whether the fork was consulted, and the arrays do not appear in responses of workspaces that never opted in.
14. **Persisted state is cleared in `hub` mode and for merged or deleted patches.** A stale `diverged` label on a patch that is no longer considered, or in a workspace that switched back to `hub`, would mislead the dashboard.
15. **Interaction with rebuilds already in progress is left to `01_rebuild_worktree`.** Its follow-up check already notices a patch tip that moved during a run, under `AUTO_REBUILD_AFTER_PUSH`. Changing that variable's meaning is out of scope, and the residual gap is stated in Background.
16. **Backup-ref cleanup and reading it back are separate.** `fork_patch_sync` writes `refs/hub/replaced/<branch>` because `replace` needs the safety net on day one. Exposing it (`replaced_sha`), resetting from it, and cleaning it up belong to `patch_divergence_recovery`.
17. **The issue is split into five specs.** It spans sync, registration, git-server push control, recovery endpoints and a documentation rewrite. Each of these can be built and tested independently, and together they exceed the limit of one spec. Sync comes first because every other part depends on `PATCH_BRANCH_SOURCE` and the origin fetch.

## Dependencies

| Spec | Relationship | Why |
|------|--------------|-----|
| `01_rebuild_worktree` | depends | The rebuild's snapshot and follow-up logic decides what happens to a patch branch moved by sync while a rebuild runs. This spec only reads that behaviour and does not change it. |
| `16_carry_patch_operations` (archived) | modifies | Carry-patch sync (`sync_handlers.go`), the sync response, patch-status and the rebuild enqueue it shares with the post-push hook. |
| `15_carry_patch_workspace` (archived) | modifies | The `patches` table gains three columns, and patch REST responses gain three fields. |
| `13_upstream_sync` (archived) | depends | The workspace sync handler merges the carry-patch extras into the workspace response and owns `last_sync_at`. |
| `10_durable_job_queue` (archived) | depends | Enqueue deduplication on `(type, key)` and the group key. |
| `18_hub_audit_query` (archived) | modifies | New hub event types `hub.patch.sync` and `hub.patch.replace`. |
| `09_git_credentials` (archived) | depends | Origin credential resolution (`ResolveCloneAuth`). |

## Verified External API

`github.com/go-git/go-git/v5` v5.19.1 is the only external package in use (go.mod, `go.sum`). Its source is outside the repository and could not be read. The signatures below come from call sites that already compile in this repository, except where marked.

| Symbol | Signature as used in this repo | Status |
|---|---|---|
| `git.PlainOpen` | `func PlainOpen(path string) (*Repository, error)` (`internal/upstream/upstream.go`) | verified by call site |
| `(*Repository).Remote` | `func (r *Repository) Remote(name string) (*Remote, error)` (`internal/upstream/upstream.go`) | verified by call site |
| `(*Remote).FetchContext` | `func (r *Remote) FetchContext(ctx context.Context, o *git.FetchOptions) error` (`internal/upstream/upstream.go`) | verified by call site |
| `git.FetchOptions` fields `RemoteName string`, `RefSpecs []config.RefSpec`, `Auth transport.AuthMethod`, `Tags git.TagMode` | all four set in `internal/upstream/upstream.go` | verified by call site |
| `git.NoTags`, `git.NoErrAlreadyUpToDate` | used in `internal/upstream/upstream.go` and `wire.go` | verified by call site |
| `config.RefSpec` | `type RefSpec string`, used in `upstream.go` and `wire.go` | verified by call site |
| `git.FetchOptions.Prune` | `Prune bool`: removes local refs that match the refspecs and no longer exist remotely | **unverified.** It is not used anywhere in the repo. The implementation must confirm it prunes `refs/remotes/origin/*` when a remote branch is deleted. The integration test in Requirement 2 is the check. If it does not, the fetch must delete the stale tracking refs itself, by listing the remote's branches. |

Other packages, with signatures taken from call sites only (their source is outside the repository, for example `../apikit` through the `replace` in go.mod):

| Symbol | Signature as used in this repo | Status |
|---|---|---|
| `apikit.WriteAPIErrorWithType` | `(c echo.Context, status int, msg, errorType string) error` (`sync_handlers.go`) | unverified, from call sites |
| `apikit.NowUTC` | `() string`-compatible timestamp helper (`wire.go`) | unverified, from call sites |
| `apikit.NewCLIError`, `apikit.CLIHandleError` | `NewCLIError(code int, msg string)`, `CLIHandleError(cmd *cobra.Command, err error) error` (`internal/cli`) | unverified, from call sites |

Repository symbols this spec calls, all read in this repo: `carrypatch.GitRunner.{Run, IsAncestor, HardReset, UpdateRef}`, `workspace.ResolveCloneAuth(store *secrets.Store, slug string) (transport.AuthMethod, error)`, `audit.Emitter.Emit(ctx, audit.HubEvent) error`, `wslock.TryLock(slug) (unlock func(), ok bool)`.
