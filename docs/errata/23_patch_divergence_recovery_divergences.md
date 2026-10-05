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

7. **A hard-reset failure after the branch moved is not an error.** When
   the reset moves a branch that is checked out in the trunk and the
   `git reset --hard` that should bring the work tree along then fails,
   `RunReset` still returns the action, persists `in_sync` (origin mode),
   enqueues the rebuild and answers `200`, and logs the failure at error
   level with the slug and branch. The proposal did not say what a failed
   work-tree reset means. Spec 20's patch refresh reports the same failure
   to its caller as a `RefStageReset` error, but it too keeps the moved
   outcome. The reset treats the ref move as its effect: the persisted
   state, the rebuild and the audit event all describe it, and a `500`
   would not help the operator, because a retry finds the tips equal and
   answers `none` without trying the work tree again. The checked-out work
   tree stays at the old tip until someone runs `git reset --hard` in the
   trunk; the error log line is the signal. See `moveBranchToFork` and
   `RunReset` in `internal/carrypatch/recovery.go`.

8. **Required and optional reset dependencies.** `RunReset` needs a lock
   function and an origin fetch. When either is nil it fails with the
   `other` error kind before it takes a lock or writes a ref: `lock
   function not configured` or `origin fetch is not configured` (the same
   wording as spec 20's missing `FetchOrigin`). The handler answers `500
   failed to update patch branch <branch>`; the specific text is in the
   server log. A missing fetch must not degrade to using the tracking ref
   as it is, because a stale `refs/remotes/origin/<branch>` would look
   like a successful reset. A nil `ResolveAuth` is different: it means
   anonymous access to origin, as a nil `ResolveOriginAuth` does in spec 20
   (erratum 10), and a private fork then fails visibly with `502`. The
   server binary wires all three, so the cases only arise in tests and
   embedders.

9. **Audit events are emitted before the patch is read back.** The
   `hub.patch.reset` event (and `hub.patch.replace` for action `replaced`)
   is emitted as soon as `RunReset` succeeds, before the handler re-reads
   the patch row to render the response. If that re-read fails the handler
   still answers `500 failed to fetch updated patch`, but the audit trail
   records the move that happened. Failed resets still emit nothing
   (23-REQ-10.5). See `handleResetPatchToOrigin` in
   `internal/workspace/patch_reset_handler.go`.

10. **Purge counts only rows it deleted.** `PatchStore.DeletePatchByIDIfDeleted`
    reports whether it removed a row. A row that was restored between the
    listing of expired rows and the delete is left in place and is not
    counted. Its backup ref has already been removed by then: the restore
    is not blocked while a purge runs, so a patch restored in that window
    loses its backup. See `internal/carrypatch/purge_refs.go`.

## Resolution

Each divergence is an implementation detail that improves correctness or
reflects the actual state of the code. The carry-patch workflow guide
documents the delivered behaviour, including the unscheduled purge. No
proposal text is changed; the errata serve as the audit trail.
