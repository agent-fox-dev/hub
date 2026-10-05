# Errata: Spec 20 — Fork Patch Sync Divergences

## Spec Expectation

Spec 20 (`20_fork_patch_sync`) proposed the origin fetch, patch refresh,
divergence policy, sync response fields and per-patch sync state for the
fork-authoritative model. The following entries record where the delivered
implementation diverges from the original proposal (GitHub issue #35,
"PRD17").

## Implementation Reality

1. **Origin fetch pruning.** The origin fetch prunes stale tracking refs
   whose branch no longer exists on the fork. The proposal did not mention
   pruning. `DefaultFetchOriginFunc` in `internal/carrypatch/wire.go` sets
   `Prune: true` on the `git.FetchOptions`.

2. **502 credential failure and `origin_fetch_failed` error type.** A
   credential resolution failure answers `502 failed to resolve origin
   credentials` (no error type). An origin fetch failure answers
   `502 origin fetch failed` with error type `origin_fetch_failed`. The
   proposal did not specify the error type field. See
   `internal/carrypatch/sync_handlers.go`.

3. **Compare-and-swap ref writes.** Patch ref writes use compare-and-swap
   (`git update-ref <ref> <new> <old>`) so that a concurrent write is
   detected. The proposal described a simple force-write. See
   `internal/carrypatch/patch_refresh.go`, `refreshOnePatch`.

4. **Disabled patches do not count as advanced.** A moved `disabled` patch
   does not count as "patch advanced" for the rebuild trigger. Only `active`
   and `conflict` patches count. The proposal said any moved patch triggers
   a rebuild. See `refreshPatchBranches` in `patch_refresh.go`.

5. **Merge detection on patch-only changes.** Merge detection also runs when
   only patch tips changed (no upstream advance). A newly merged patch
   triggers a rebuild. The proposal tied merge detection to upstream
   advances only. See `shouldRunMergeDetection` in `sync_handlers.go`.

6. **Field presence of `origin_fetched`, `patches_synced` and
   `patches_diverged`.** `origin_fetched` is always present in the sync
   response (both modes). `patches_synced` and `patches_diverged` appear
   only in `origin` mode; in `hub` mode they are omitted (nil). The
   proposal did not distinguish per-mode presence. See `asExtras()` in
   `sync_handlers.go`.

7. **`last_sync_at` not written on ref-write failure.** When a ref-write
   failure occurs during the patch refresh, `last_sync_at` is not written.
   The function returns a `500` error before reaching the timestamp write.
   The proposal said `last_sync_at` is written unconditionally. See the
   `refreshErr != nil` path in `sync_handlers.go`.

8. **Variables documented in `docs/api.md`.** `PATCH_BRANCH_SOURCE` and
   `PATCH_DIVERGENCE_POLICY` are documented in `docs/api.md` (Workspace
   Variables Reference), not in `docs/configuration.md`.
   `docs/configuration.md` lists environment keys, not workspace variables.
   The proposal did not specify which document carries the variable
   reference.

9. **`--fail-on-diverged` exit code 3 is produced by the hub's CLI, not by
   apikit.** apikit's `CLIExitCode` maps every `CLIError` code other than 1
   and 400 or above to 2, so returning `apikit.NewCLIError(3, ...)` alone
   exits 2. `cmd/afc/main.go` therefore exits with `cli.ExitCode(err)`
   (`internal/cli/exit.go`), which returns 3 for the diverged error and
   otherwise defers to apikit; exit codes 1 and 2 keep their meaning. The
   same file's `IsDivergedError` makes `main` skip `apikit.CLIPrintError` for
   that error. `checkDiverged` does not route the error through
   `apikit.CLIHandleError` either. Both would print a second JSON document
   (an `{"error": ...}` envelope) to stdout after the sync response. With the
   flag, stdout holds exactly one document, the message naming the branches
   is on stderr, and the process exits 3. The proposal said "exit 3" without
   saying how apikit would carry it. See `checkDiverged` in
   `internal/cli/workspace_cmd.go`.

10. **A nil `ResolveOriginAuth` means anonymous origin access.** In `origin`
    mode a `SyncAPIConfig` whose `ResolveOriginAuth` is nil fetches the origin
    with no credentials, exactly as a nil `ResolveAuth` does for the
    upstream fetch (a public fork needs none). A nil `FetchOrigin` is
    different: there is nothing to call, so the sync answers
    `500 origin fetch is not configured`. The proposal did not say what a
    missing credential resolver means. The server binary wires both, so the
    case only arises in tests and embedders.

11. **Distinct 500 messages for refresh failures that are not a ref
    write.** Only a failed ref write answers `500 failed to update patch
    branch <branch>`. An ancestry-check failure (`IsAncestor`) answers
    `500 failed to compare patch branch <branch> with origin`, and a failed
    work-tree reset after the ref moved answers `500 failed to reset working
    tree for patch branch <branch>`. The proposal treated every refresh
    failure as a ref-write failure. A branch whose ref moved but whose reset
    failed keeps its outcome: it is persisted, audited and counted as
    advanced for the rebuild decision. See `RefWriteError.Stage` in
    `patch_refresh.go` and `refreshFailureMessage` in `sync_handlers.go`.

12. **The ref-write failure path honours `AUTO_REBUILD_AFTER_SYNC`.** When
    the refresh fails after some branches moved, the rebuild for those
    branches is enqueued unless `AUTO_REBUILD_AFTER_SYNC` is `"false"`,
    matching the success path. See `autoRebuildEnabled` in
    `sync_handlers.go`.

13. **Origin columns of a patch merged in the same sync are cleared.** The
    refresh persists origin state while a patch is still `active`; when
    merge detection then marks it `merged_upstream`, the three columns are
    cleared again before the sync ends. The response's `patches_synced` and
    `patches_diverged` still describe the refresh, so a patch that was
    diverged and is detected as merged in the same sync appears in
    `patches_diverged` once, and not in `patch-status` afterwards.

14. **Shape of the sync audit events.** `hub.patch.replace` carries
    `resource_id` = the branch name and `action` = `replace`, like the event
    the reset endpoint emits. `hub.patch.sync` carries `resource_id` = the
    workspace slug and `action` = `sync`. The proposal named neither field.
    Replacements are logged at info level even when no audit emitter is
    configured.

## Resolution

Each divergence is an implementation detail that improves robustness or
correctness. The carry-patch workflow guide and the agent example document
the delivered behaviour. No proposal text is changed; the errata serve as
the audit trail.
