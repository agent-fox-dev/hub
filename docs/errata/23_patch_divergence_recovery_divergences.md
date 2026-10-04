# Errata: Spec 23 — Patch Divergence Recovery Divergences

## Spec Expectation

Spec 23 (`23_patch_divergence_recovery`) proposed `reset-to-origin`,
`replaced_sha`, protection of `refs/hub/replaced/*`, backup cleanup and
the purge routine. The following entries record where the delivered
implementation diverges from the original proposal (GitHub issue #35,
"PRD17").

## Implementation Reality

1. **Single-patch GET route added.** The `GET /workspaces/:slug/patches/:id`
   route was added because it did not exist before spec 23. The proposal
   assumed it already existed. See `internal/workspace/routes.go`,
   `RegisterRoutes`.

2. **Reset semantics and status limits.** The reset fetches the branch
   from the fork first, writes a backup ref only when it discards commits
   (action `replaced`), enqueues a rebuild when the branch moved and the
   patch status is `active`, `conflict` or `disabled`, and is limited to
   patches with status `active`, `conflict` or `disabled`. The proposal
   did not specify the status restriction or the conditional backup. See
   `internal/carrypatch/recovery.go`, `RunReset`.

3. **Sync state recorded only in `origin` mode.** The reset persists
   `origin_sync_state`, `origin_sha` and `origin_synced_at` only when
   `PATCH_BRANCH_SOURCE` is `origin`. In `hub` mode these columns are
   not written. The proposal did not distinguish per-mode persistence.
   See `RunReset` in `recovery.go`, the `ParsePatchBranchSource` check.

4. **`hub.patch.reset` event added.** A `hub.patch.reset` audit event is
   emitted on every successful reset. The event type is defined in
   `internal/audit/types.go` as `EventPatchReset`. The proposal did not
   specify an audit event for the reset operation. See
   `internal/workspace/patch_reset_audit_test.go`.

5. **Only the `refs/hub/replaced/` prefix is protected.** Client pushes
   that target any ref under `refs/hub/replaced/` are rejected before the
   pre-receive hook is consulted. Other names under `refs/hub/` (e.g.
   `refs/hub/forward/`) are not covered. The proposal said
   `refs/hub/replaced/*` but the implementation uses a prefix match on
   `refs/hub/replaced/`. See `internal/gitserver/protected_refs.go`,
   `isProtectedRef`.

6. **Purge routine removes refs but is not scheduled.** The
   `PurgeExpiredDeletedPatchesWithRefs` function removes backup refs and
   expired patch rows, but no production code calls it on a schedule.
   Only test files invoke the purge routines. The proposal described a
   background purge process. See `internal/carrypatch/purge_refs.go` and
   the absence of callers in non-test code.

## Resolution

Each divergence is an implementation detail that improves correctness or
reflects the actual state of the code. The carry-patch workflow guide
documents the delivered behaviour, including the unscheduled purge. No
proposal text is changed; the errata serve as the audit trail.
