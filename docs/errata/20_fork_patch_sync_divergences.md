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

## Resolution

Each divergence is an implementation detail that improves robustness or
correctness. The carry-patch workflow guide and the agent example document
the delivered behaviour. No proposal text is changed; the errata serve as
the audit trail.
