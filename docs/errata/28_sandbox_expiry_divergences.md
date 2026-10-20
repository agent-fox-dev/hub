# Errata: Spec 28 — Sandbox Expiry Divergences

## Spec Expectation

Spec 28 (`28_sandbox_expiry`) was written before specs 25 to 27 landed and marks
every AgentKit symbol it names as unverified. It assumes a `sandbox.Provider` with
`List` and `Destroy(ctx, id)` and a "not found" sentinel, a `Reap` that returns an
`error`, a lifecycle (`RunStartup`, `StartReaper`, the shutdown flag and
`Shutdown`) kept on the `Service`, a column that comes from a migration and not from
the `CREATE TABLE`, a gateway whose backend answers `FailedPrecondition`, and a
gauge that is reset and then set per workspace. The entries below record where the
code that landed differs, with the code and the test of the delivered behaviour. None
of them weakens a goal of the spec except the first two, which are limits of
AgentKit's provider and say what the hub can and cannot clean up.

## Implementation Reality

1. **`sandbox.Provider` has no `List` and no `Destroy`.** It has `Capabilities`,
   `Create` and `Reap`, and `Sandbox.Close` takes a context (the same finding as
   errata 25 and 27). The sweep, restart recovery and shutdown therefore reach boxes
   through a `Boxes` interface, `List(ctx)` and `Destroy(ctx, id)`
   (`internal/sandboxes/expire.go:30`), which the gateway implements over the boxes
   the hub holds a handle of (`gateway.go:253` and `gateway.go:259`): the same
   handles `DELETE` uses, destroyed through `gatewayd.Server.Destroy` with the
   handle as fallback, the box counting as destroyed when neither holds it
   (`gatewayd.ErrNotHeld` is the "not found"). The criteria that name the provider's
   `List` and `Destroy` (28-REQ-4.4, 4.5, 4.6, 5.1, 5.2, 7.3, 7.5) are verified
   against a `Boxes` double that lists and destroys what a test puts in it
   (`fakeBoxes` in `expiry_helpers_test.go`), and against the real gateway in
   `TestGateway_ListsAndDestroysTheBoxesItHolds` and the smoke tests. `Reap` is the
   provider's own and the first step of every sweep. This is a hub-level view and
   not the provider's: see entry 2.

2. **Boxes of an earlier process are out of reach.** After a restart the hub holds
   no handle, and gatewayd of the new process holds none. Restart recovery
   (`reaper.go:50`) still asks `Boxes.Destroy` for the box of each lease it ends,
   which counts as gone, so the lease is ended as `destroyed` with `end_reason`
   `restart` and the box lives until the TTL the provider enforces (the lease's time
   left when the box was made, never later than its `expires_at`) or until the
   provider's `Reap` removes it. The orphan step of the sweep (`sweeper.go:209`) can
   likewise find only boxes of this process, for example one whose destruction
   failed when its lease ended. The spec's wish that "no box is left running" after a
   crash is therefore bounded by the TTL, as it was before this spec. A provider that
   lists its boxes would let the `Boxes` implementation see them without any other
   change.

3. **`Reap` returns `([]string, error)`, and the backend's returns nothing.** The
   sweep's first step calls `Service.Provider().Reap` (never the backend's, which
   would sweep from the sweep). `Backend.Reap` (`backend.go:420`) runs the sweep when
   the context has no lease principal and returns `(nil, nil)` in every case; the
   sweep logs each step that fails, with the provider's text scrubbed, so gatewayd
   has nothing to log twice. spec 27's `TestBackendReap_ScrubsTheProviderError` was
   changed from "returns the ids and the scrubbed error" to "logs the scrubbed error".
   gatewayd calls `Reap` as soon as it serves and every ten minutes after, so the
   sweep also runs on that call; sweeps never overlap.

4. **`lease_expires_at` is in the `CREATE TABLE` as well.** Design Decision 5 says
   the column comes from the migration and not from editing the table definition.
   `lease_schema.go:33` has the column, as `jobqueue` has `progress`, so that a new
   database and every caller of `InitSchema` have it, and `MigrateLeaseExpiry`
   (`lease_migrate.go:14`) adds it to a table from before; it checks `PRAGMA
   table_info`, is idempotent, tolerates a nil database and leaves old rows NULL.
   `TestMigrateLeaseExpiry_TS_28_7` builds the old table and
   `TestAfHubBinary_MigratesLegacyLeaseTable` starts the real binary on one.

5. **The lifecycle lives on the `Sweeper` and the `Gateway`, not on the `Service`.**
   The plan stores the reaper and calls `RunStartup`, `StartReaper` and `Shutdown`
   on the `Service`. The `Service` is documented read-only after start, so
   `RecoverActive`, `StartReaper`, `RunStartup` and `Shutdown` are methods of the
   `Sweeper` (`reaper.go`), which the gateway builds over itself, and
   `Gateway.Run` (`gateway.go:193`) and `Gateway.Shutdown` (`gateway.go:276`) call
   them. The shutdown mode flag is on the `Service` as the plan has it
   (`service.go:211`), set once. `StartGateway` is split into `NewGateway` and
   serving so that recovery and the first sweep happen before the listener accepts
   (`TestGatewayRun_RecoversBeforeItServes_TS_28_24` reads the leases when serving
   begins). The `Reaper` type and a `StopReaper` method the first draft had were
   removed: only tests called them.

6. **TS-28-33 tests `runServer`, not the order of two `Close` calls.** main's two
   closes are deferred calls, which run after the function body that calls the
   shutdown, so the order holds by the language. `runServer`
   (`cmd/af-hub/main.go:521`) is tested for running the shutdown after `Start`, also
   when `Start` fails, and `TestAfHubBinary_ShutdownEndsActiveLeases_TS_28_56` stops
   the real binary with SIGTERM (apikit's `Start` returns on SIGTERM and SIGINT, which
   the spec lists as unverified) and finds the lease ended and the event recorded,
   which needs both databases open.

7. **gatewayd hands a backend failure to the client as `Unavailable`.** TS-28-11 and
   TS-28-14 expect `FailedPrecondition` at the client (the same as errata 27, entry
   on codes). The code is asserted at the backend (`backend.go:269`), and the
   message through the real client.

8. **The metrics are recorded where a lease changes state.** The plan puts the
   gauge and the histogram in an ending helper in `expire.go` and the activation in
   `backend.go`. `LeaseStore.endLease` (`lease.go:266`) now returns the row it
   changed (`UPDATE ... RETURNING workspace, created_at, ended_at, sandbox_id`) and
   `MarkActive` returns the workspace, and both record on the metrics after a
   changed row (`metrics.go`). So `DELETE`, the client's `Close`, the expiry path,
   shutdown and restart all count through the one function that changes a lease, an
   ending that lost the update records nothing (28-REQ-8.4), and a lease with a
   `sandbox_id` is known to have been active even when the ending allows both live
   states.

9. **`afhub_sandboxes_active` keeps its `""` placeholder when no lease is active.**
   28-REQ-9.3 says `Reset()` then one `Set` per workspace. A gauge vector with no
   series is not listed on `/metrics`, so on a hub with no active lease the metric
   would disappear after the first recalibration, against goal 7 ("appear on
   `/metrics` from start"). `metricsRecorder.recalibrate` (`metrics.go:75`) sets the
   placeholder again when there is no workspace to set. Any workspace with an active
   lease gets exactly its count and no placeholder (`TestGauge_RecalibratedAfterRecoveryAndSweeps_TS_28_47`).

10. **An ending whose context has ended does not complete.** 28-REQ-4.5 and 5.2 end a
    lease although `Destroy` fails; 28-REQ-7.5 leaves a lease `active` when the
    shutdown limit runs out before `Destroy` returns. Both hold: an error from
    `Destroy` is logged and the lease ended, a cancelled context (the limit, or the
    reaper's context cancelled by the shutdown) leaves the lease for the next start
    (`expire.go:68` and `expire.go:97`). `TestShutdown_HungDestroy_TS_28_37` covers a
    `Destroy` that honours its context and one that ignores it.

11. **`lease_timeout`'s default is not compared with `max_ttl`.** 28-REQ-1.3 holds
    an explicit value to `1s` and `max_ttl`. A deployment with `max_ttl` under five
    minutes and no `lease_timeout` would otherwise be refused at start for a value it
    never wrote; the lease deadline is `min(created_at + lease_timeout, expires_at)`
    in any case (`validate.go`, `TestExpiryKeys_Defaults_TS_28_1`).

12. **Tests that differ from the plan.**
    - TS-28-3 and TS-28-4 use an invalid `sandbox.listen` as the second failure, since
      `Validate` does not check `runtime`.
    - TS-28-44 searches every column of a lease row except `env` (the profile's fixed
      environment, stored since spec 26) and the token hash of a live lease.
    - TS-28-5, 12, 13, 29, 30, 32 and 36 verify behaviour that already held or that
      only asserts rows are kept, and so passed before the change; TS-28-9, 41, 44, 50
      and 52 verify what tasks 3 to 9 built. Mutants of the production code each fail
      one of them.
    - Spec 27's `TestBackendCreate_Expired_TS_27_23` expects a late `Create` to end
      the lease as `expired` (28-REQ-3.1) where it required the lease to be left as it
      was; it and `TestBackendCreate_BuildsHubsSpec_TS_27_25` use a lease timeout
      equal to the TTL so that they stay about the time the lease has left;
      `TestAfHubMain_WiresTheGateway` follows `NewGateway`, `Run` and `runServer` in
      `main.go`.
    - The test gateway (`startGateway` in `testhelpers_test.go`) waits for gatewayd's
      first sweep, which would otherwise end a lease a test has just made.

13. **Provider errors in the log are scrubbed with the lease's fixed environment
    only.** `ender.destroyBox` removes URL user information and the values of the
    lease's `env` from the text it logs (the same as the `DELETE` path); the secrets
    are not at hand there. The text reaches it through `closeProviderBox`, which has
    already removed every secret and environment value of the box, so nothing is lost
    (`TestExpiryFlowsDoNotLeak_TS_28_44`).

## Impact

A lease is still given a deadline for its `Create`, ends at its `expires_at`, is
ended and its token revoked on every path the spec lists, stays readable for 24
hours and is then pruned, and the three metrics follow the table. What the hub cannot
do is find or destroy a box it holds no handle for: after a crash those boxes live
until their TTL, which is at most the time their lease had left. Entry 1's
interface is the one place a provider with a listing and a destroy-by-id would be
connected.
