# Errata: Spec 21 — Fork Patch Registration Divergences

## Spec Expectation

Spec 21 (`21_fork_patch_registration`) proposed the branch resolution order
at registration time, new error responses and audit metadata. The following
entries record where the delivered implementation diverges from the original
proposal (GitHub issue #35, "PRD17").

## Implementation Reality

1. **`refs/heads` existence check.** Branch existence is checked on
   `refs/heads/<name>` (a local branch ref). A tag, SHA or
   remote-qualified name is no longer accepted as a valid branch. The
   proposal described a `rev-parse --verify` check that would accept any
   resolvable name. See `internal/carrypatch/branch_resolver.go`,
   `resolveBranch`.

2. **502 on fork fetch failure.** Fork fetch failures other than a missing
   branch answer `502` with error kind `origin_fetch_failed`, not `400`.
   The proposal mapped all fetch failures to `400`. See
   `branchResolveKindOriginFetchFailed` in `branch_resolver.go` and the
   workspace handler's error mapping.

3. **409 `workspace_busy`.** When registration needs to write (e.g. to
   create a local branch from a fork fetch) and the workspace lock is
   held, the response is `409 workspace_busy`. The proposal did not
   mention a lock requirement for registration. See
   `branchResolveKindWorkspaceBusy` in `branch_resolver.go`.

4. **`branch_resolution` has a fourth value `skipped`.** The audit
   metadata field `branch_resolution` can be `local`, `origin_tracking`,
   `origin_fetch` or `skipped`. The `skipped` value is emitted when
   `skip_branch_check` is true. The proposal listed only the first three
   values. See `docs/api.md` audit events table and
   `internal/workspace/smoke_spec21_test.go`.

5. **No batch audit event.** Batch registration (a JSON array body to `POST .../patches`)
   does not emit a `hub.patch.create` audit event. Only single-patch
   registration emits the event. The proposal did not specify batch audit
   behaviour, but the absence is a divergence from the implied "every
   registration emits an event" reading.

6. **Batch syntax validation precedes resolution.** The batch handler
   validates the syntax of every element (`branch_name`, integration
   branch, `position`) in array order before it resolves any element, and
   resolves in a second pass. Before spec 21 the branch check ran inside
   the per-element validation loop, so a batch of `[missing-branch,
   bad-position]` answered `patch[0]: branch does not exist in repository
   or on origin`; it now answers `patch[1]: position must be >= 1`
   because the position error is found before resolution begins. The
   reason is that resolution has side effects (a fork fetch, a local ref)
   and malformed input should be rejected before any of them. The
   single-patch path is unchanged. See `internal/workspace/patch_handlers.go`
   and the batch section of `docs/api.md`.

## Resolution

Each divergence is an implementation detail that improves error reporting
or correctness. The carry-patch workflow guide documents the delivered
behaviour. No proposal text is changed; the errata serve as the audit
trail.
