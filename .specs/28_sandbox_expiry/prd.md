---
spec_id: "28"
spec_name: "sandbox_expiry"
title: "Sandbox expiry: lease timeout, reaper, shutdown, retention and metrics"
status: "active"
created_at: "2026-10-19T16:49:05.09859Z"
updated_at: "2026-10-19T16:49:05.09859Z"
intent_hash: "6db00c57eb90d25cc64394315099946d7e87b9e8105f6731c7424c2a6ea8e1a8"
schema_version: 2
source: "docs/prd/01-create-and-lease-sandboxes.md"
---
## Intent

Put a time limit on every sandbox hub leases, and make sure nothing hub leased lives longer than its lease. A lease nobody uses lapses. A box whose lease has run out is destroyed. When hub shuts down or crashes, no box is left running and no lease still claims to be live. A lease that has ended stays visible long enough to explain what happened to it, and is then removed. Operators can see how many boxes are running, how creates turn out, and how long leases last.

## Goals

1. A lease that gets no `Create` before its `lease_expires_at` reads `expired` with `end_reason: "expired"` after one sweep. Exactly one `hub.sandbox.expire` event is emitted with `state: "expired"`, and the token no longer authenticates at hub's gateway.
2. An `active` lease past its `expires_at` is ended by one sweep. The provider's `List` no longer contains its box, the lease reads `destroyed` with `end_reason: "expired"`, and exactly one `hub.sandbox.expire` event is emitted with `state: "destroyed"` and the `sandbox_id`.
3. A `Create` that arrives after `lease_expires_at` but before any sweep fails with `FailedPrecondition` naming `expired`, and the lease reads `expired` immediately after. Correctness never depends on `reap_interval`.
4. On shutdown, every `active` lease's box is destroyed and the lease reads `destroyed` with `end_reason: "shutdown"`. Leases in `leased` are not changed. The whole step finishes within 20 seconds even when the provider hangs.
5. When hub starts after a crash, every lease left `active` reads `destroyed` with `end_reason: "restart"` before hub's gateway accepts a connection.
6. A terminal lease can be read through `GET` one and `?all=true` for 24 hours after it ended. The first sweep after that removes it.
7. `afhub_sandboxes_active`, `afhub_sandbox_creates_total` and `afhub_sandbox_lease_seconds` appear on `/metrics` from start and match the lease table after every transition and every sweep.
8. A `lease_timeout` or `reap_interval` that is unparseable or out of range stops hub at start with a message naming the key.
9. `make test` covers all of this offline, with an injected clock, a test-double provider or AgentKit's `local`, and no container engine.

## Non-goals

- **Renewing or extending a lease.** A lease's deadlines are fixed when it is issued.
- **The remote `gateway` provider.** It belongs to `sandbox_gateway_provider`. The reaper works on whatever provider the sandbox service holds, so this spec needs no change when that provider arrives.
- **Making retention configurable**, and pruning sandbox audit events. Those events follow the audit store's own retention (`internal/audit/retention.go`).
- **Cleaning up after the feature is switched off or the provider is changed.** No reaper runs when `[sandbox]` is absent.
- **Signal handling of hub's own.** apikit owns the server lifecycle. Hub's shutdown step runs when `server.Start()` returns, and restart recovery covers every case where it does not run.
- Changing what `DELETE` or the client's `Close` do, beyond the reasons and metrics listed here. Readiness or health checks for the reaper. Alerting rules. An `afc` command to trigger a sweep.
- Anything `sandbox_profiles`, `sandbox_leases` or `sandbox_gateway` already deliver: the profile set, the lease API and token, the embedded gateway, secret injection, and the `create`, `failed` and `destroy` events.

## Background

This is the fourth of five specs split from `docs/prd/01-create-and-lease-sandboxes.md`. The three earlier ones deliver:

- the `[sandbox]` section, validated profiles and a provider held by `internal/sandboxes` (`sandbox_profiles`);
- the `sandbox_leases` table. It has five states, `expires_at`, `ended_at` and `end_reason`, and a single `EndLease(lease, targetState, reason)` that ends a lease and clears its token hash in one conditional update. The lease spec also delivers the capacity count and the lease REST API (`sandbox_leases`);
- hub's embedded `gatewayd.Server` (`sandbox_gateway`). It has a backend that builds boxes from leases, an `Authenticate` callback that maps a lease token to its lease, `leased → active|failed` transitions, a box-destroying ending used by `DELETE` and the client's `Close`, and the `hub.sandbox.create`, `hub.sandbox.failed` and `hub.sandbox.destroy` events.

None of these specs makes a lease end because time has passed. `sandbox_leases` works around that gap: a lease past `expires_at` does not count against capacity (its Design Decision 8). Nothing yet moves such a lease out of `leased` or `active`, destroys its box, or removes old rows.

Patterns in the code that this spec reuses:

- **A background worker.** `audit.StartRetentionWorker` (`internal/audit/retention.go:73`) runs once immediately, then repeats on a `time.Ticker` and returns when its context is cancelled. Each step's error is logged, and the later steps still run.
- **Gauge recalibration.** `RetentionStep9_RecalibrateGauges` (`internal/audit/retention.go:522`) calls `Reset()` on `afhub_agent_sessions_active` and then sets one value per workspace from a `GROUP BY` count.
- **Metrics.** `audit.Metrics` (`internal/audit/metrics.go`) holds a custom registry and, per its own doc comment, "all afhub_* metric collectors". This includes `afhub_jobqueue_depth`, which another subsystem owns. Vec metrics are pre-initialised with placeholder labels so they show on `/metrics` from start (19-REQ-10.E2). `main.go` serves the registry at `GET /metrics`.
- **Recovery and cleanup at start.** `workspace.ReconcileStuckSyncs` repairs state left by a crash before the server serves. `carrypatch.CleanupStaleRebuildWorktrees` removes crash leftovers and "never prevents startup".
- **Migrations.** `jobqueue.MigrateProgress` (`internal/jobqueue/migrate.go`) checks `PRAGMA table_info` and runs `ALTER TABLE … ADD COLUMN`. It is idempotent.
- **System events.** Hub events not caused by a caller use `ActorType: "system"` with an empty `ActorID` (`internal/carrypatch/push_mirror.go:211`).
- **Process lifecycle.** In `cmd/af-hub/main.go`, `server.Start()` blocks. After it returns, the deferred calls run in this order: `auditDB.Close`, `mergeQueue.Stop`, `cancel`, `database.Close`. A `log.Fatal` skips them all. `deploy/deployment.yaml` runs one replica with `Recreate`, and Kubernetes gives a pod 30 seconds after SIGTERM by default.

## Requirements

### 1. Configuration

`[sandbox]` gets two new keys. The `sandbox_profiles` loader decodes them, and they join that loader's set of recognised keys.

- `lease_timeout`: a Go duration string. The default is `5m`. It must be at least `1s` and at most `max_ttl`.
- `reap_interval`: a Go duration string. The default is `10m`. It must be at least `10s` and at most `1h`.

If a value cannot be parsed or is out of range, hub refuses to start. The message names `sandbox.lease_timeout` or `sandbox.reap_interval` and is reported together with every other `[sandbox]` validation failure. When the section is absent, neither key is read.

### 2. The lease deadline

Every successful `POST /workspaces/:slug/sandboxes` stores `lease_expires_at`, which is the earlier of `created_at + lease_timeout` and `expires_at`, written with `apikit.FormatUTC`. An idempotent migration adds the column to `sandbox_leases`. The migration runs at every start, right after the lease schema is created, whether or not the feature is enabled. Rows that already exist get NULL.

Every lease representation gets a `lease_expires_at` field: the `POST` 201 body, `GET` one, both lists and the admin list. The field is an RFC 3339 string, or `null` for a row older than the column. The value never changes after the lease is issued, and it only matters while the lease is `leased`.

The capacity count from `sandbox_leases` also leaves out a `leased` lease whose `lease_expires_at` is at or before now.

### 3. Deadlines at hub's gateway

Hub's gateway enforces deadlines whether or not the reaper has run:

- **A late `Create`.** Suppose a lease is in `leased` and its `lease_expires_at` (or its `expires_at`, when `lease_expires_at` is NULL) is at or before now. A `Create` for it is refused with `FailedPrecondition` naming the state `expired`, and the provider is not called. In the same call, the lease is ended as `expired` through the expiry path of Requirement 4, which emits one `hub.sandbox.expire` event.
- **Tokens past `expires_at`.** The authenticator rejects the token of any lease whose `expires_at` is at or before now, whatever the lease's state. The RPC fails with `Unauthenticated`.
- **A lease that ends while its box is being created.** Suppose the lease leaves `leased` while the provider is creating its box, because the reaper expired it or it was deleted. The gateway destroys the box it just created and fails the `Create` with `FailedPrecondition` naming the lease's current state. The lease keeps the terminal state and `end_reason` it already has.

If `sandbox_gateway` already behaves as the second or third point describes, this spec only adds tests for it.

### 4. The sweep

The sweep is one function. It never runs twice at the same time, and it reads time from hub's injected clock. Its steps run in the order below. A failing step is logged, and the later steps still run.

1. **Provider reap.** Call the provider's `Reap`.
2. **Unused leases lapse.** End every lease in `leased` whose `lease_expires_at` is at or before now as `expired`, with `end_reason` `expired`. When `lease_expires_at` is NULL, use `expires_at` instead.
3. **Overdue boxes are destroyed.** For every lease in `active` whose `expires_at` is at or before now, call the provider's `Destroy` for its `sandbox_id`, then end the lease as `destroyed` with `end_reason` `expired`. The lease is ended, and its token revoked, even when `Destroy` fails. The failure is logged with the lease ID, the sandbox ID and the error text after scrubbing.
4. **Orphaned boxes are removed.** Call the provider's `List`. Destroy a listed box when either holds:
   - its ID equals the `sandbox_id` of a lease in a terminal state in this hub's table;
   - the provider's listing shows labels, and the box's `hub.lease` label names such a lease.

   A box that cannot be matched to a terminal lease in this hub's table is never touched. That covers boxes belonging to live leases, boxes with no `hub.lease` label, and boxes whose lease ID this hub does not know.
5. **Old leases are pruned** (Requirement 6).
6. **The active gauge is recalibrated** (Requirement 9).

Every ending goes through the single lease-ending function from `sandbox_leases`, and through `sandbox_gateway`'s box-destroying path where there is a box. The ending is conditional on the lease still being `leased` or `active`. A lease that another path has already ended is skipped, and no event is emitted for it. Each provider call is limited to 30 seconds.

### 5. The reaper and recovery at start

When the feature is enabled, hub runs these steps once at start. They run after the lease schema and migration, the audit emitter and the metrics exist, and before hub's gateway accepts connections.

1. **Restart recovery.** Every lease in `active` was left by an earlier process. For each one, hub calls `Destroy` for its box, then ends the lease as `destroyed` with `end_reason` `restart`. A `Destroy` failure is logged, and step 4 of a later sweep retries it.
2. **One sweep.**
3. **The reaper loop.** A sweep runs every `reap_interval` until shutdown begins.

Errors in recovery or in a sweep are logged and never stop hub from starting.

When gatewayd calls `Reap` on hub's backend, the call runs the sweep only if its context carries no lease principal. A `Reap` arriving with a lease principal returns nil and does nothing.

When the feature is disabled, there is no recovery and no reaper.

### 6. Retention

A terminal lease (`destroyed`, `expired` or `failed`) stays readable through `GET` one, `GET …/sandboxes?all=true` and the admin list for 24 hours after it ended. "Ended" means `ended_at`, or `updated_at` when `ended_at` is NULL. The sweep deletes every terminal lease that ended more than 24 hours before now. It never deletes a lease in `leased` or `active`. A lease therefore disappears between 24 hours and 24 hours plus `reap_interval` after it ended. After that, `GET` one answers 404.

### 7. Shutdown

When `server.Start()` returns, hub runs the sandbox shutdown before the audit database and the SQLite database close. The whole step is limited to 20 seconds.

1. **Hub enters shutdown.** From this point, hub's gateway refuses new `Create`s with `Unavailable`, and every lease ended for any reason is ended with `end_reason` `shutdown`.
2. **The reaper stops.** Hub cancels the reaper loop and waits for any sweep in progress to return.
3. **Active leases end.** Every `active` lease is ended as `destroyed` with `end_reason` `shutdown`, after `Destroy` has been called for its box. The `Destroy` calls may run concurrently.
4. **The gateway stops.** Hub's gateway is stopped through the stop that `sandbox_gateway` provides.

Leases in `leased` are not changed. When the time limit runs out, the leases still `active` stay that way, and restart recovery ends them at the next start. Calling shutdown a second time does nothing, and shutdown does nothing when the feature is disabled.

### 8. Events and end reasons

`internal/audit/types.go` gets `EventSandboxExpire = "hub.sandbox.expire"`, with a doc comment listing its metadata. Hub emits it once for each lease ended by the expiry path: the late `Create` of Requirement 3 and steps 2 and 3 of the sweep. The event's fields are:

- `ResourceType` `sandbox` and `ResourceID` the lease ID;
- `Action` `expire`;
- `ActorType` `system` and `ActorID` empty;
- `Workspace` the slug;
- `Metadata`: `lease_id`, `state` (`expired` or `destroyed`), and `sandbox_id` when a box was destroyed.

These endings emit no `hub.sandbox.destroy`. Shutdown and restart endings emit `sandbox_gateway`'s `hub.sandbox.destroy` with `reason` `shutdown` or `restart` and `ActorType` `system`. The new `end_reason` values are `expired`, `shutdown` and `restart`.

An event is emitted only when the conditional update actually ended the lease. A nil emitter means no event is emitted. A failed emit is logged with `slog.Error` and changes nothing else.

No event, log line or lease field that this spec writes contains a secret value, an environment value or a token. Provider error text that this spec logs goes through the URL-userinfo scrubber in `internal/sandboxes` first.

### 9. Metrics

`audit.Metrics` gets three collectors. They are registered in `NewMetrics` and appear on `/metrics` from start.

- **`afhub_sandboxes_active`** is a gauge with the label `workspace`. It counts the leases in `active`.
  - It goes up by one when a lease moves from `leased` to `active`.
  - It goes down by one when an ending changes a lease that was `active`.
  - It is recalibrated from the lease table at start, after restart recovery, and at the end of every sweep: `Reset()`, then one `Set` per workspace.
  - Like `afhub_agent_sessions_active`, it starts with a `""` placeholder label.
- **`afhub_sandbox_creates_total`** is a counter with the label `result`. It goes up once for each `Create` the gateway accepts for a `leased` lease: by `created` when the lease becomes `active`, and by `failed` otherwise. "Otherwise" covers a provider error, `secret_missing`, and a lease that left `leased` while its box was being created. A `Create` refused before it is accepted (unknown token, second `Create`, missed deadline, shutdown) is not counted. Both label values start at zero.
- **`afhub_sandbox_lease_seconds`** is a histogram with no labels. It is observed once for each lease that becomes `destroyed`, for any reason, with the value `ended_at − created_at` in seconds. Its buckets are 60, 300, 600, 1200, 1800, 3600, 7200, 14400 and 28800.

The sandbox code takes a `*audit.Metrics`. When it is nil, recording does nothing.

### 10. Documentation

Following the steering, the change updates these documents:

- `docs/configuration.md`: `lease_timeout` and `reap_interval`.
- `docs/api.md` and `docs/openapi.yaml`:
  - the `lease_expires_at` field;
  - the `expired` state;
  - the `end_reason` values;
  - the 24-hour retention;
  - `hub.sandbox.expire` and the `shutdown` and `restart` reasons, in Hub Audit Events;
  - the three metrics, in the Prometheus table.
- `docs/architecture.md`: the reaper, restart recovery, shutdown and the lease lifecycle.
- `docs/cli.md`: what `afc sandbox list --all` shows and for how long.

`make doc` is run. No tests are written about the docs.

## Design Decisions

1. **The reaper is a goroutine driven by a ticker, not a `jobqueue` job.** The sweep is idempotent and depends only on time. Its state lives in the lease table, and it runs at every start, so a durable queue adds nothing. `audit.StartRetentionWorker` is the precedent.
2. **`lease_timeout` defaults to `5m` and must be between `1s` and `max_ttl`.** `5m` is the input's value. The bounds catch typos without blocking short timeouts in tests.
3. **`reap_interval` defaults to `10m` and must be between `10s` and `1h`.** The floor stops a busy loop against the provider. The ceiling keeps the retention promise of Requirement 6 tight. Correctness does not depend on the interval (Decision 7).
4. **`lease_expires_at` is the earlier of `created_at + lease_timeout` and `expires_at`, and it never changes.** A lease cannot be usable after its own expiry. A stable value is easier to reason about than one that is cleared at `Create`.
5. **The column comes from an idempotent `ALTER TABLE` migration, not from editing `sandbox_leases`' `CREATE TABLE`.** By the time this spec ships, the table may already exist in deployed databases. `jobqueue.MigrateProgress` is the pattern.
6. **A `leased` lease past `lease_expires_at` stops counting against capacity.** This extends `sandbox_leases`' Decision 8: capacity must not wait for a sweep.
7. **The gateway checks deadlines itself and expires a late lease on the spot.** A token must never outlive its deadline because of how often the reaper runs, and a `FailedPrecondition` naming `expired` should match what the lease then reads.
8. **Endings caused by time, shutdown or restart record the terminal state and revoke the token even when `Destroy` fails. Cleaning up the box is retried by the orphan step.** Revoking access must not depend on a provider call succeeding. The box's TTL and the provider's `Reap` limit how long it lives in any case.
9. **The orphan step destroys only boxes it can match to a terminal lease in this hub's table.** A Podman engine may be shared with other hubs or users. A box hub cannot attribute is not hub's to destroy.
10. **Restart recovery ends every `active` lease at start, with reason `restart`.** The boxes belong to the earlier gatewayd process, and the new gateway cannot relay to them. Left alone, they would hold capacity and show as live for up to their TTL.
11. **Leases in `leased` survive shutdown and restart.** They have no box. With `Recreate` a restart is short, and the deadline still applies after it.
12. **Shutdown runs after `server.Start()` returns, within 20 seconds, and hub adds no signal handling.** apikit owns the lifecycle (steering). Twenty seconds leaves room inside Kubernetes' 30-second default, and restart recovery covers a shutdown that never ran.
13. **During shutdown, new `Create`s are refused and every ending carries `shutdown`.** No new box can start while hub is closing boxes. The reason also stays correct if `gatewayd`'s own stop closes boxes through hub's backend.
14. **Endings by the reaper and by a late `Create` emit only `hub.sandbox.expire`. Shutdown and restart emit `hub.sandbox.destroy`.** The input's event table assigns reaper endings to `expire` and shutdown to `destroy`. One event per ending keeps consumers from double counting.
15. **`end_reason` is `expired` both for a lapsed lease and for a box destroyed at `expires_at`.** The input's acceptance criterion 6 asks for `reason: expired` on the destroyed box. The `state` field already tells the two cases apart.
16. **Retention is a fixed 24 hours, measured from `ended_at`.** The input states 24 hours. The audit stream keeps the longer history, so a setting would add surface without adding information.
17. **The three metrics live in `audit.Metrics`.** That struct is documented as holding "all afhub_* metric collectors", including ones owned elsewhere (`afhub_jobqueue_depth`), and it is the one registry `/metrics` serves.
18. **`afhub_sandboxes_active` counts only `active` leases. It is kept up to date at each transition and recalibrated from the table after each sweep.** "Active" means a box exists. Recalibration repairs any drift from races, as the session gauge's recalibration does.
19. **`afhub_sandbox_creates_total` counts `Create`s the gateway accepted, by outcome, and not refusals.** It measures how often hub's attempts to start a box succeed. Refusals are caller errors and do not belong in that ratio.
20. **`afhub_sandbox_lease_seconds` observes only leases that end `destroyed`, measured from issue.** The input defines it as "lease to destroy". Lapsed leases would only pile up at `lease_timeout`.
21. **A `Reap` sent through the gateway by a lease holder does nothing.** A caller must not be able to start sweeps across the whole hub. A call from gatewayd itself, with no principal, still runs the shared sweep.
22. **Sweeps never overlap, each provider call is limited to 30 seconds, and a failing step does not stop the later ones.** This mirrors `RunRetention`: one hung engine call must not stall expiry or pruning.
23. **No reaper runs when the feature is disabled.** No provider exists to destroy anything with. The rows stay and are handled when the feature is enabled again.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `25_sandbox_profiles` | modifies | Adds `lease_timeout` and `reap_interval` to its `[sandbox]` config struct, its validation and its set of recognised keys. Uses `Service.Provider()` and the start-up wiring after `sandboxes.Init` |
| `26_sandbox_leases` | modifies | Adds the `lease_expires_at` column (by migration) and representation field, extends the capacity count, and ends leases through its single `EndLease` with new reasons. Prunes its rows. Updates its exact-field representation test |
| `sandbox_gateway` (expected `.specs/27_sandbox_gateway`) | modifies | Adds deadline checks to its `Create` path and authenticator, handles a lease that ends during `Create`, picks the event in its box-destroying ending (`expire` for `expired`), adds the `shutdown` and `restart` reasons to `hub.sandbox.destroy`, refuses `Create` during shutdown, and calls its gateway stop at shutdown |
| `18_hub_audit_query` | modifies | Adds the `EventSandboxExpire` constant to `internal/audit/types.go` |
| `19_sessions_metrics_retention` | modifies | Adds three collectors to `audit.Metrics` and `NewMetrics`, following its pre-initialisation and recalibration conventions |

## Verified External API

**agentkit-go** is a sibling checkout outside this repository, and it is not yet in `go.mod`. Every symbol below is **unverified** and taken from the input and the earlier scopes' PRDs.

| Symbol | Assumed shape | Status |
|---|---|---|
| `sandbox.Provider.Reap` | `Reap(ctx context.Context)`, returning at least an `error`. It destroys boxes past their TTL | unverified (return shape unknown) |
| `sandbox.Provider.List` | `List(ctx) (…, error)`. Each element exposes the box ID and, through `Spec()`, its `Labels map[string]string` | unverified. If labels are not exposed, the orphan step matches by `sandbox_id` only |
| `sandbox.Provider.Destroy` | `Destroy(ctx context.Context, id string) error` | unverified |
| `gatewayd.PrincipalFromContext` | `func(ctx context.Context) (string, bool)`, the AgentKit addition `sandbox_gateway` depends on | NOT FOUND (pending upstream). Assumed to work as the input's §8 describes |
| `(*gatewayd.Server).Shutdown` | stops serving. The input says it "closes every box" | unverified |
| gRPC codes `FailedPrecondition`, `Unauthenticated`, `Unavailable` | returned by hub's backend and authenticator the way `sandbox_gateway` returns them | unverified |

**apikit** (sibling `../apikit`):
- `(*apikit.Server).Start() error` blocks while the server runs (call site: `cmd/af-hub/main.go`). Whether it returns on SIGTERM is **unverified**.
- `apikit.FormatUTC(t time.Time) string` (call site: `internal/carrypatch/wire.go:250`).

**github.com/prometheus/client_golang v1.25.0** (in `go.mod`). These were verified from their use in `internal/audit/metrics.go`: `prometheus.NewGaugeVec`, `prometheus.NewCounterVec`, `prometheus.NewHistogramVec` with `HistogramOpts{Name, Help, Buckets}`, `(*GaugeVec).Reset()`, `WithLabelValues(...).Set/Inc/Add`, and `(*Registry).MustRegister`. `prometheus.NewHistogram(HistogramOpts) Histogram` and `Histogram.Observe(float64)` are the library's standard v1 API but are not used in the repository.
