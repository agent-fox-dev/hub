# Architecture

This document describes the high-level architecture of af-hub.

## Sandboxes

`internal/sandboxes` owns the deployment's sandbox definition: where sandboxes
run and what shape they take. It builds one AgentKit sandbox provider from the
`[sandbox]` section of `config.toml`, checks every profile against what that
provider can honour, and publishes the profiles read-only. The lease record
(below) is built on top of it, the sandbox gateway (below) is what creates a
sandbox for a lease, and the expiry machinery (below) puts a time limit on every
lease and cleans up after a shutdown or a crash. The provider is either a container
engine on the hub's own host or a remote `agentkit-gateway` (below).

| File | Responsibility |
|------|----------------|
| `config.go` | `LoadConfig`: reads the `[sandbox]` table from the file apikit loads, expands `${NAME}` in the section only, refuses unknown keys with their full path. |
| `validate.go` | `Validate`: every static rule for the deployment keys and profiles, collected into one joined error. Uses `secrets.ValidateKey` for secret and env names. |
| `provider.go` | `ProviderFactory` (the shape of `podman.New`), `PodmanFactory`, `BuildProvider`; `GatewayFactory` (the shape of `gateway.Dial`), `DialGateway`, `RemoteClient` and the `StartOption` `WithGatewayFactory`. |
| `redact.go` | `RedactedString`: the type the validated `gateway_token` is kept in, which prints as `[redacted]` under every verb, `slog` and JSON. |
| `service.go` | `Service`, `Start`, `New`: builds the provider (`startRemote` for provider `gateway`), probes it once, checks `network_modes` and turns each profile into a `sandbox.Spec` that `sandbox.CheckSpec` must accept. Keeps the provider, the capability snapshot of a remote gateway, the scrub values and the validated profiles in memory; `Close` closes the remote client. |
| `errors_remote.go` | `isNotFound` and `isRemoteRefusal`, and `Service.remoteFailure`: a remote not-found counts as destroyed, and a remote that refuses the hub's token is answered `Unavailable`. |
| `startup.go` | `Init`: the one call `cmd/af-hub` makes. Loads, validates, starts and logs. |
| `permissions.go`, `auth.go` | The `sandboxes:read` and `sandboxes:write` scopes and the standalone auth helpers. |
| `handlers.go` | `RegisterRoutes` and `GET /api/v1/sandbox-profiles`, built from the in-memory profiles. |
| `lease_schema.go`, `lease_migrate.go`, `lease.go`, `store_query.go`, `lease_transitions.go` | The `sandbox_leases` table (`InitSchema`), `MigrateLeaseExpiry` (adds `lease_expires_at` to a table from before it), the `Lease` type and the `LeaseStore`: insert, read, list, `LookupByToken`, and the conditional transitions `MarkActive`, `FailLease`, `ExpireLease`, `DestroyLease`, `EndLease`. |
| `lease_service.go`, `lease_validate.go`, `token.go` | `LeaseService.Issue`: request validation, the required-secret check, id and token generation, the capacity count and the insert. |
| `secretcheck.go` | `ResolutionChain` and `CheckRequiredSecrets`: which scopes a workspace's secrets are searched in, and a check that lists names only. |
| `routes.go`, `handlers_issue.go`, `handlers_read.go`, `handlers_delete.go`, `delete_destroy.go`, `render.go` | `RegisterLeaseRoutes`: the five lease routes, the checks they share (`begin`, `authorize`), the lease representation (with `gateway_url`) and how `DELETE` ends a lease and its sandbox. |
| `gateway.go` | `NewGateway`: loads the key pair, binds `listen`, builds the `gatewayd.Server`, the backend and the sweeper without serving; `Gateway.Run` (start-up work, then serving), `Gateway.Shutdown`, `Gateway.DestroyBox`, and `List` and `Destroy`, which make the gateway the `Boxes` of the sweep. |
| `gateway_auth.go` | `NewLeaseAuthenticator`: the function gatewayd calls for every RPC to turn a lease token into a principal. |
| `backend.go`, `create_spec.go`, `secret_resolve.go`, `handle.go`, `events.go`, `scrub.go` | The wrapping backend, the `sandbox.Provider` hub gives gatewayd: `Create` and its outcomes, the spec built from the lease, the secrets read when the sandbox is created, the handle around the provider's sandbox, the audit events and the scrubber for provider errors. |
| `expire.go`, `metrics.go` | The `Boxes` seam, and the `ender`: the expiry path, `endDestroyed` for shutdown and restart, and the events they emit; the recorder of the three sandbox metrics. |
| `sweeper.go`, `reaper.go` | `Sweeper.Sweep` and its steps; restart recovery (`RecoverActive`), the reaper loop (`StartReaper`, `RunStartup`) and `Sweeper.Shutdown`. |

### Start-up order

`cmd/af-hub` calls `sandboxes.Init` immediately after `apikit.Bootstrap` and
before the workspace root, the job queues, the workers and the routes are set
up:

1. `Bootstrap` runs first, so generating the first-boot admin token never
   depends on the sandbox configuration or on a container engine.
2. `Init` loads the section from `apikit.ConfigPath()`. No section means the
   feature is off: `Init` returns a nil service after one info log line.
3. Otherwise `Start` validates the section; static failures are returned
   before the provider is built or the engine or the remote gateway is
   contacted. Then, for provider `podman`, `BuildProvider` builds the Podman
   provider, `New` probes it, and each profile is checked against its
   capabilities; for provider `gateway`, `startRemote` dials the remote, probes it
   with one `List` and checks each profile against the capabilities it read (see
   [The remote gateway provider](#the-remote-gateway-provider)).
4. Any failure is fatal: the process exits before a queue, a worker or a
   listener exists.
5. `sandboxes.Permissions()` is appended to the scopes registered with apikit
   on every start, and `RegisterRoutes` mounts the listing with the service,
   or with a nil service when the feature is off. A nil service answers
   `404 sandboxes_disabled`.
6. Later, after the lease table and `MigrateLeaseExpiry`, the audit emitter and
   the metrics exist and before `server.Start()`, `NewGateway` binds the gateway's
   listener and builds the server, and `Gateway.Run` ends the leases a crash left
   `active`, runs one sweep, starts the reaper and only then serves the listener.
   The lease service is built with the gateway's URL and the gateway itself, which
   it destroys boxes through. A key pair that cannot be loaded or an address that
   cannot be bound is fatal, like any other failure of the section; what fails in
   `Run` is logged and is not. A hub without sandboxes binds nothing and runs none
   of it.
7. `server.Start()` blocks until apikit shuts the server down on SIGTERM or SIGINT.
   When it returns, `runServer` calls `Gateway.Shutdown` (a no-op on a hub without
   sandboxes) before `main` returns, so before the deferred closes of the audit
   database and the SQLite database run.

The service holds no database handle and no secrets store, and the listing
reads nothing but the profiles in memory, so it keeps answering when the
engine becomes unreachable after start.

### The lease record

A **lease** is a row of the `sandbox_leases` table: a caller's right to one
sandbox in one workspace, with a snapshot of the profile taken when it was
issued (image, network, resources, landlock, the required and optional secret
names and the fixed environment). The table is created by `sandboxes.InitSchema`
at every start, right after `secrets.InitSchema`, whether or not sandboxes are
enabled, so switching them off and on again keeps the history. Its `state`
column allows all five states (`leased`, `active`, `destroyed`, `expired`,
`failed`) because SQLite cannot alter a `CHECK`. The column `lease_expires_at`, the
deadline for a `Create`, is in the table for a new database and added by
`MigrateLeaseExpiry` to one from before it (checked with `PRAGMA table_info`, so a
second run changes nothing; old rows stay NULL), which `cmd/af-hub` calls right
after `InitSchema` at every start. All five states are reachable:

```
leased ── Create succeeds ─────────────────▶ active
  │                                            │
  │                                            ├── DELETE ───────────────────▶ destroyed  (end_reason api)
  │                                            ├── Close through the gateway ▶ destroyed  (end_reason client)
  │                                            ├── expires_at passes, sweep ─▶ destroyed  (end_reason expired)
  │                                            ├── hub shuts down ───────────▶ destroyed  (end_reason shutdown)
  │                                            └── hub starts after a crash ─▶ destroyed  (end_reason restart)
  ├── DELETE ───────────────────────────────────────────────────────────────▶ destroyed  (end_reason api)
  ├── lease_expires_at passes: late Create, or sweep ───────────────────────▶ expired    (end_reason expired)
  └── Create cannot proceed (profile gone, required secret missing,
      spec cannot be built, provider failed) ────────────────────────────────▶ failed     (error set)
```

Every transition is one conditional `UPDATE` on the state the caller expects to
find, and `destroyed`, `expired` and `failed` are terminal. While the hub is
shutting down every ending that a caller would give `api` or `client` carries
`shutdown` instead. Two callers racing for the same
lease cannot both win: the one that finds zero rows changed reads the lease again
and acts on what it is now.

`cmd/af-hub` builds a `LeaseService` from the validated profiles, the database,
the secrets store and the audit emitter when sandboxes are enabled, and passes
`nil` otherwise; `RegisterLeaseRoutes` mounts the routes either way, and a `nil`
service answers `404 sandboxes_disabled`. Issuing a lease, in `Issue`:

1. The body is validated (`lease_validate.go`) and resolved to a profile, a TTL, a
   purpose and labels. Nothing is stored on a refusal.
2. `CheckRequiredSecrets` lists the key names of the workspace's resolution chain
   (`ResolutionChain`: the workspace, the org only while the workspace owner is a
   member of it, the owner's user scope) through a `SecretLister`, an interface
   with the single method `ListSecrets`. It cannot read a value.
3. The id (`sbx_` and 16 random bytes in hex) and the token (`sbl_` and 33 random
   bytes in base64url) are drawn from the injected random source. Only the token's
   SHA-256 is stored.
4. Under one lock in the process and in one transaction, the live leases are
   counted (`leased` or `active` with `expires_at` in the future, across
   workspaces, less the `leased` ones whose `lease_expires_at` is at or before now)
   and the lease is inserted, with `lease_expires_at` (the earlier of `created_at`
   plus `lease_timeout` and `expires_at`, never changed afterwards), unless
   `max_sandboxes` is reached. The
   secret check runs before the transaction: reading secrets inside it would need
   a second connection. `apikit.OpenDatabase` begins transactions `IMMEDIATE`;
   the lock keeps the limit with a handle whose transactions are deferred.
5. One `hub.sandbox.lease` audit event is emitted after the commit.

Every ending of a lease (`failed`, `expired` and `destroyed` alike) goes through
one statement in `lease.go`, `endLease`: it sets the terminal state, `ended_at`,
`updated_at`, the end reason or the error text, and clears the token hash, for a
lease that is in a state the caller names. `EndLease`, `FailLease`, `ExpireLease`
and `DestroyLease` are thin callers of it, and a test pins that no other
production code clears a token hash. Clearing the hash is what revokes a token, so
ending a lease and revoking its token are the same act. `MarkActive` (leased to
active, recording the sandbox id) leaves the token alone.

The store also records the sandbox metrics (`metrics.go`), at the one place a lease
changes state. `endLease` returns the row it changed, in the same statement: a lease
with a `sandbox_id` had a box, so the ending takes one off `afhub_sandboxes_active`,
and one that becomes `destroyed` is observed on `afhub_sandbox_lease_seconds`;
`MarkActive` adds one. Only a transition that changed a row records anything, so an
ending that lost a race to another path records nothing, and a store without
metrics (a nil `*audit.Metrics`) records nothing.

`LeaseStore.LookupByToken` (a presented token to its live lease, or
`ErrLeaseNotFound` without saying why) is what the gateway authenticates with.
`ResolutionChain` serves
both the presence check at issue and the lookup of secret *values* when a sandbox
is created, so that the check and the lookup agree.

### The sandbox gateway

The hub embeds an AgentKit gateway (`gatewayd.Server`, from the sibling module
`agentkit-go/sandbox/gatewayd`) and serves it on a second listener. The REST API
issues and ends leases; the gateway is where a lease holder turns its lease into
a sandbox and uses it.

```
lease holder ── gateway.Dial(gateway_url, token) ──▶ gatewayd.Server
                                                       │ every RPC
                                                       ▼
                                              lease authenticator ── LeaseStore.LookupByToken
                                                       │ principal = lease id
                                                       ▼
                                   Backend (wrapping sandbox.Provider) ──▶ the provider (Podman)
                                                       │
                                  lease store, secrets store, audit emitter
```

- **The authenticator** (`NewLeaseAuthenticator`) hashes the presented token, looks
  the lease up on every call and keeps nothing, so a lease that ends is refused on
  the next call. It accepts only a `leased` or `active` lease whose `expires_at`
  is in the future. The principal gatewayd hands the backend is the lease id, which
  also owns the sandboxes the call creates: gatewayd lists and destroys per
  principal, so a lease sees only its own sandbox.
- **The backend** implements `sandbox.Provider`, which has only `Capabilities`,
  `Create` and `Reap`. `Create` claims the lease in a set in the process (one
  `Create` at a time), loads it, refuses one that is not `leased`, builds the spec
  from the lease alone (`buildSpec`), reads the secrets (`resolveSecrets`) and asks
  the provider. While the hub is shutting down it refuses with `Unavailable` before
  all of this. A lease whose deadline has passed (`lease_expires_at`, or
  `expires_at` for an old row) is refused with `FailedPrecondition` naming
  `expired` (gatewayd hands any backend failure that is no refusal to the client as
  `Unavailable`, with the message), without a provider call, and ended as `expired`
  in the same call through the expiry path. The outcome is recorded by the conditional transitions
  above, and every `Create` that gets as far as making a box is counted once on
  `afhub_sandbox_creates_total`. A box that arrives for a lease that a `DELETE` or a
  sweep has ended in the meantime is destroyed on the spot.
- **The handle** is what `Create` returns: the provider's sandbox with two
  differences. `Spec` redacts the environment values, because the gateway sends
  the spec back to the caller. `Close` is the client destroy path: gatewayd calls
  it when the holder destroys its sandbox (and also at the sandbox's TTL, when it
  prunes a box that is no longer live, and when gatewayd stops), and it ends the
  lease with the reason `client` (`shutdown` while the hub shuts down) unless a
  `DELETE` or the expiry machinery is ending it or the lease's time is up (within
  two seconds of `expires_at`, since the box's TTL is the lease's time cut to whole
  seconds and gatewayd closes the box a moment early; the next sweep ends such a
  lease as `expired`). The handle has an `Unwrap`, without which
  gatewayd could not relay the holder's calls to the sandbox's agent.
- **`DELETE`** ends a lease through `LeaseService.destroyLease`. For an `active`
  lease it asks the gateway to destroy the box first (`Gateway.DestroyBox`): the
  embedder's `gatewayd.Server.Destroy`, which also frees the box's place in the
  gateway. gatewayd forgets a box whose destruction failed, and does not hold one
  until its `Create` has returned, so the backend keeps the handle of every box
  from before its lease reads `active` until it is closed, and a `DELETE` closes
  that handle when the gateway no longer holds the box. A box neither holds is
  taken to be gone, which is not true of one made by a process that has since been
  restarted.
- **Reap.** The backend's `Reap` runs the sweep (below) for the gateway's own
  periodic call, the one with no principal; with a principal, a lease holder's
  call, it does nothing. It returns nothing: the sweep logs what fails in it.
- **Scrubbing.** Provider error text is scrubbed (`scrub`) before it is stored on
  a lease, put in an event, logged or returned: every secret value and fixed
  environment value becomes `[redacted]`, URL user information is removed, and
  only then is the text cut to 1024 bytes on a UTF-8 boundary.
- **Events** are emitted in one place (`events.go`; `delete_destroy.go` for
  `DELETE`; `expire.go` for the endings by time, shutdown and restart):
  `hub.sandbox.create`, `hub.sandbox.failed`, `hub.sandbox.destroy` and
  `hub.sandbox.expire`. A nil emitter skips emission and an emit error is logged and
  changes no outcome.

Where the gateway differs from what its spec expected of AgentKit (no provider
`List` or `Destroy`, the codes gatewayd answers with, the handle's `Close`) is in
`docs/errata/27_sandbox_gateway_divergences.md`.

### The remote gateway provider

With `provider = "gateway"` the provider that `Start` builds is the client of a
remote `agentkit-gateway`, and nothing else in the package knows the difference but
a few places named below. The hub becomes a gateway over a gateway:

```
lease holder ──lease token──▶ hub's gatewayd ──▶ Backend ──hub's one token──▶ remote gatewayd ──▶ its backend
                              (lease authenticator)   │ RemoteClient (gateway.Provider)
                                                      └── lease store, secrets, audit
```

- **Building it.** `startRemote` calls the `GatewayFactory` (`DialGateway`, which is
  `gateway.Dial`, in production) with the validated `gateway_url` and the token
  revealed from its `RedactedString`, then makes one `List`, both under one
  `probeTimeout` of 10 seconds. Nothing is created. A failure becomes an error that
  begins `sandbox:` and names the three keys, with the client's text scrubbed of
  the token and of URL user information; the client is closed when the start is
  refused. `Start` takes the factory as a `StartOption`, so the Podman path and its
  callers are unchanged.
- **One shared token.** Every lease's sandbox is made on the remote under the hub's
  one `gateway_token`. A lease token never leaves the hub, and the remote sees only
  the hub's. A lease's `gateway_url` is the hub's own gateway.
- **The capability snapshot.** `startRemote` reads the client's `Capabilities()` once
  and keeps it in `Service.caps`. `Service.Capabilities` returns a copy of it, and
  the backend (the spec of a box, `Capabilities`) and the gateway (its network
  modes) use that and never the client, so a remote that is down delays and empties
  nothing, and a change on the remote waits for a restart. A provider that is not
  remote is asked every time, as before.
- **Where the remote's failures show up.** At start, as a refused start. At a
  `Create`, as a `failed` lease with the scrubbed message; a remote that refuses the
  hub's token is answered `Unavailable` and logs one error naming
  `sandbox.gateway_token` (`remoteFailure`, also for the sweep's `Reap`). At a destroy, in the one place a box
  is destroyed for the caller's `Close`, `DELETE`, the sweep and the shutdown
  (`closeProviderBox`): a remote not-found, as gatewayd's `ErrNotHeld` or a gRPC
  `NotFound`, counts as destroyed; any other failure is the usual 502, or a lease
  that stays `active`. In the sweep, as logged steps that run again.
- **Destroying again.** AgentKit's attached sandbox tears down once, so its `Close`
  answers `nil` after a failed one. The handle remembers a failed close
  (`closeFailed`), and a later try destroys the box by id through the client's
  `Destroy`, which can be repeated and which the remote answers OK for a box it does
  not have. The sweep, `DELETE` and the shutdown all go through it.
- **Scrubbing.** `Service.scrubValues` gives the token, the `gateway_url` string, its
  host with the port and its host name alone to every site that turns provider text
  into output, besides a lease's secrets and fixed environment: the failure of a
  `Create`, the error of a box's `Close`, `DELETE`'s 502, the expiry path's log, the
  sweep's `Reap`, listing and orphan destroy, and the error of `Service.Close`. The
  capability snapshot has its notes scrubbed once, since the gateway client writes
  the remote's host and limits into them and hub's gateway serves them to every
  holder. gatewayd relays the status of a call to a sandbox's agent as it is, which
  hub cannot scrub, so `DialGateway` gives the client a dialer (`dialWithoutAddress`)
  whose failures say why and not where; what is left is a TLS failure on `https`,
  which names the host.
- **No redial, no retry.** The client reconnects by itself, with gRPC's backoff (from
  about a second up to two minutes between attempts, during which a call fails at
  once); the hub adds no redial, monitor or circuit breaker, and calls the remote's
  `Create` once per caller `Create`.
- **Closing.** `Gateway.Shutdown` closes the client last, through `Service.Close`,
  after it has destroyed the box of every active lease through it, within the
  shutdown limit.

The relay of the holder's calls to the sandbox needs `gatewayd` to send the backend
sandbox's own `agentkit-sandbox-id` to the next gateway, which `agentkit-go`
supports since its pull request 150; with an older checkout every `Create` through
the hub fails at the agent handshake. Where this differs from the spec (the
factory's signature, the probe, the snapshot, the destroy by id, the lease that a
sweep ends while the remote is down) is in
`docs/errata/29_sandbox_gateway_provider_divergences.md`.

### Lease lifetime: the sweep, recovery, shutdown and retention

Every lease has two deadlines and the hub enforces both, whether or not a sweep has
run. The authenticator refuses a token past `expires_at` in any state, and the
backend refuses a `Create` past `lease_expires_at` and ends the lease on the spot.
The sweep and the reaper tidy up what nobody asked about:

```
Gateway.Run (start)         RecoverActive ─▶ StartReaper: Sweep, then Sweep every reap_interval ─▶ serve
gatewayd, Backend.Reap      Sweep                                (no principal)
Gateway.Shutdown (stop)     shutdown mode ─▶ stop the reaper ─▶ end the active leases ─▶ gatewayd Shutdown
```

- **The sweep** (`Sweeper.Sweep`) is one function that never runs twice at once and
  reads the injected clock. It runs, in order, the provider's `Reap`; the lapse of
  every `leased` lease past its deadline; the destruction of the box of every
  `active` lease past `expires_at`; the removal of the boxes of ended leases
  (orphans); the pruning of terminal leases that ended more than 24 hours ago; and
  the recalibration of `afhub_sandboxes_active` from a `GROUP BY` of the table
  (`Reset`, then one `Set` per workspace; with no active lease the `""`
  placeholder is set again, so the metric stays listed). A failing step is logged
  and the later ones run. Every call to the provider has a 30-second limit.
- **The endings** go through the `ender` (`expire.go`). The expiry path ends a
  `leased` lease as `expired`, or destroys the box of an `active` one and ends it
  as `destroyed`, with the reason `expired` and one `hub.sandbox.expire` event;
  `endDestroyed` does the same with the reason `shutdown` or `restart` and one
  `hub.sandbox.destroy` event by the system. Each is conditioned on the state the
  caller read, so a lease another path has ended is skipped without an event or a
  metric, and a box that cannot be destroyed does not keep the lease: revoking the
  token does not wait for a provider call, and the failure is logged, scrubbed. A
  context that has ended does keep it: the lease stays for the next start.
- **The boxes** are reached through the `Boxes` interface (`List`, `Destroy`),
  because AgentKit's `sandbox.Provider` can do neither. The gateway implements it
  over what the hub holds: the handles of the boxes it made and has not seen closed.
  So the orphan step finds a box only in this process (for example one whose
  destruction failed), a box of an earlier process is beyond it, and it lives until
  its TTL or until the provider's `Reap` removes it.
- **Restart recovery** (`RecoverActive`) ends every lease an earlier process left
  `active` as `destroyed` with the reason `restart`, before the gateway serves, then
  recalibrates the gauge. The sweep's first run follows, and then the reaper
  (`StartReaper`: a goroutine on a ticker of `reap_interval`).
- **Shutdown** (`Gateway.Shutdown`, within `ShutdownLimit`, 20 seconds) sets the
  shutdown flag of the `Service`, after which the gateway refuses every `Create`
  with `Unavailable`, no sweep starts, and `DELETE` and the client's `Close` end
  with `shutdown`; stops the reaper and waits for a sweep in progress; destroys the
  boxes of all `active` leases at once; and stops gatewayd, which closes any box it
  still holds. Leases in `leased` are not touched. When the limit runs out the
  leases not yet ended stay `active` for recovery. A second call, or a call on a hub
  without sandboxes, does nothing.
- **Retention** is a fixed 24 hours from `ended_at` (`updated_at` when it is NULL),
  applied by the sweep.
- **Metrics** (`afhub_sandboxes_active`, `afhub_sandbox_creates_total`,
  `afhub_sandbox_lease_seconds`) are collectors of `audit.Metrics`, passed to the
  store, the backend and the sweeper as a `*audit.Metrics` that may be nil.

Where this differs from the spec (the `Boxes` seam in place of the provider's `List`
and `Destroy`, where the lifecycle methods live) is in
`docs/errata/28_sandbox_expiry_divergences.md`.

### AgentKit dependency

The provider comes from the sibling checkout `../agentkit-go`. Its sandbox
packages are a separate Go module, `github.com/agent-fox-dev/agentkit-go/sandbox`,
that requires the root module, and the gateway server is a third module,
`github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd`, that requires both, so
`go.mod` replaces all three with the sibling (`replace` lines in a dependency's
`go.mod` are ignored). The hub image build
and both CI workflows supply the checkout next to the repository the same way
they supply `../apikit`. `afc` stays a static `CGO_ENABLED=0` binary: neither
`cmd/afc` nor `internal/cli` imports `internal/sandboxes`, any AgentKit
package or gRPC.

`sandbox.Provider` has no `List` or `Destroy`; the start-up probe of the Podman
provider is therefore one `Reap` call under a 10-second timeout. The client of a
remote gateway, `*gateway.Provider`, has both, and its probe is one `List`. See
`docs/errata/25_sandbox_profiles_divergences.md`.

## Forge client

`internal/forge` lets a caller read and write a workspace's repository, issues and
pull requests on its forge, GitHub or GitLab, through the hub. The hub is the
workspace's forge client: it uses the git credential it already holds for the
workspace and never one of the caller's, so a caller needs no forge access of its
own. The decision is recorded in
[ADR 04](adr/04-hub-is-the-workspaces-forge-client.md); the endpoints are in
[Forge Endpoints](api.md#forge-endpoints) and the commands in
[Forge Commands](cli.md#forge-commands). The same package also receives the forge's webhook
deliveries (see [A webhook delivery](#a-webhook-delivery)); hub only verifies, records and
relays them, and never acts on one or calls the forge for it.

The forge client is the `issuex` package of the sibling module
[aflib](https://github.com/agent-fox-dev/aflib), expected at `../aflib` and replaced
in `go.mod` like `../apikit` and `../agentkit-go`; the hub image build and the CI
workflows supply the checkout in the same way. The durable job queue that
`cmd/af-hub`, `internal/carrypatch` and `internal/merge` run their jobs on is the
`jobqueue` package of the same module; hub keeps no copy of its own. Only the
packages under `internal/forge` import `issuex`. `afc` stays a static `CGO_ENABLED=0` binary:
neither `cmd/afc` nor `internal/cli` imports `internal/forge` in a build (a test
file of `internal/cli` imports it to run `afc` against the routes).

| File | Responsibility |
|------|----------------|
| `permissions.go`, `auth.go` | The `forge:read` and `forge:write` scopes (`Permissions`, appended to `extraPerms` in `cmd/af-hub/main.go`) and the standalone auth helpers; `forge:write` implies `forge:read`. |
| `config.go` | `Config`: the database, the two credential resolvers (`workspace.ResolveCloneAuth` and `ResolveUpstreamAuthMethod`, as closures in `main.go`), `GetVariable`, an optional base `Transport`, an optional `NewClient`, the audit emitter `Audit` that `main.go` builds with `audit.NewEmitter`, the secrets store `Secrets` (the interface `SecretStore`, which `*secrets.Store` satisfies) and `ExternalURL` (`server.external_url`); and `webhookURL`, the ingress URL of a workspace. One value serves `RegisterRoutes` and `MountWebhookIngress`. |
| `routes.go` | `RegisterRoutes`: the twenty-two routes under `/workspaces/:slug` (ten reads, nine writes, and the webhook secret and delivery routes), and `authorize`, the checks they share, in order: 401, 403 (the scope of the verb), 404, 400 for a write on a workspace that is not active, then the workspace's `git_url`, `workspace_mode` and `upstream_url`. |
| `target.go`, `params.go` | `resolveTarget` (`?target=`), `parseNumber` (`:n`) and `parseIssueFilter` (the query of `GET .../issues`): the refusals that come before a client is built. |
| `client.go` | `buildClient`: a fresh issuex client for one request, from the remote URL, the API URL variable and the credential; `credentialOf` (`pat`, `basic`, `none`); `parseRemote` and `redactRemote`; `describeRemote` for the diagnostic. |
| `httpclient.go` | The one `*http.Client` of the process: `forgeTransport` (user agent, no environment token for a workspace without a credential), the redirect policy, and `callDeadline` (30 seconds). |
| `handlers_read.go`, `handlers_forge.go` | `serve`, the handler the nine read routes share, and the routes themselves (`prepare*`, `numbered`, `valueOf`, `listOf`); `diagnostic`, `GET .../forge`. |
| `errors.go`, `scrub.go` | `mapForgeError`: every issuex and transport failure to one status and `error_type`, with `Retry-After`; `mapCall`, which logs a 5xx or 429 answer once; `scrub`: the token and URL userinfo out of a message, cut to 1024 bytes. |
| `write.go` | `write`, the handler of the nine write routes and the steps they share (checks, target, request, client, body limit, call, event, answer); `writeRoute` and `writePlan`, which a route fills in; `writeRun.call`, through which every request to the forge goes; and the JSON field readers of a request body. |
| `write_issues.go`, `write_labels.go`, `write_pulls.go` | The nine routes: what each reads of its request and what it sends. |
| `write_limits.go` | The body limits (65,536 on GitHub, 1,000,000 on GitLab, in code points) and the `413 body_too_large` refusal. |
| `write_events.go` | `emit`: the one `hub.forge.*` event of a write that reached the forge. |
| `write_raw.go` | The `writer` the routes call, and `rawForge`: the requests issuex cannot make the way a write needs (see below). |
| `schema.go` | `InitSchema`: the SQLite table `forge_deliveries` (the delivery log) and its index, created at every start. |
| `webhook_handlers.go` | `PUT` and `DELETE .../forge/webhook`: the webhook secret in the workspace scope, its generation, validation and rotation, and `emitEvent`, which hands an event to the emitter on a context the request does not cancel. |
| `deliveries_handler.go` | `GET .../forge/deliveries`: the delivery log, newest first. The `webhook` block of `GET .../forge` is `webhookInfo` in `handlers_forge.go`. |
| `ingress.go` | `MountWebhookIngress` and `POST /hooks/forge/:slug`: lookup, the scheme and its verification, the 1 MiB body, the delivery identity, the insert that deduplicates, the 202 answer and the relay. |
| `reduce.go`, `reduce_github.go`, `reduce_gitlab.go` | `reduceDelivery`: a verified payload to an outcome, a target and the events (hub never stores the payload); the GitHub and GitLab mappings. |
| `retention.go` | `PruneDeliveries` and `StartDeliveryRetention`: rows older than 7 days and rows of deleted workspaces, pruned at start and every hour. |

### A forge read

1. `authorize` applies the checks in order and reads the workspace's remotes.
2. `resolveTarget` and the route's own parameters are checked.
3. `buildClient` reads the remote URL (`git_url` for `origin`, `upstream_url` for
   `upstream`), finds the repository, reads `FORGE_API_URL` (`UPSTREAM_FORGE_API_URL`
   for `upstream`), resolves the credential and calls `issuex.NewWithOptions` with the
   shared HTTP client. Nothing is cached.
4. The call runs under a context that carries the workspace's token and a 30-second
   deadline. The transport adds `User-Agent: af-hub/<apikit.Version>`, removes the
   token headers when the workspace has no credential, and refuses a redirect to
   another host, port or scheme.
5. The result is answered as issuex encodes it. A failure goes through `failCall`.

The diagnostic (`GET .../forge`) takes steps 1 and 3 up to the point of the client:
it sends nothing.

### A forge write

1. `write` applies the checks of `authorize` for the write scope, then resolves the
   target, reads the body and lets the route check it (`prepare`): all 400s.
2. `buildClient` builds the client as for a read, but gives it the API URL explicitly
   (the variable's, else the hub's statement for the host), so that the requests of
   issuex and the ones the hub sends itself go to one API root. The forge is told
   (`issuex.DetectForge`); a forge that cannot be told is 422 here.
3. The route may refuse what this forge cannot take (`writePlan.refuse`: an update that
   sets only assignees on GitLab is 400), and its text fields are measured against the
   forge's limit (413).
4. The route's `send` runs, calling the forge through `writeRun.call`: each call has
   the request's context with the cancellation detached, so that a write the caller
   leaves is finished and recorded, and a deadline of its own, so that a write with
   several requests gives each the whole 30 seconds. There is no retry.
5. `emit` records the event, whether the write succeeded or failed, before the answer is
   written; a failure goes through `mapCall` first, so that the event can say the
   `error_type` the hub answers with. A request refused before step 4 records nothing,
   and neither does a write whose calls issuex refused with `ErrNoToken` (the workspace
   has no credential) before sending anything.

### A webhook delivery

`POST /hooks/forge/:slug` is mounted by `forge.MountWebhookIngress(server.Echo(), …)` in
`cmd/af-hub/main.go` next to `gitserver.MountGitHandlers`: on the Echo root, outside the API
group, so outside apikit's bearer authentication, its body size limit and its content-type
check. The forge proves who it is with the signature of the workspace's `FORGE_WEBHOOK_SECRET`.

1. The handler reads the workspace row (`git_url`, `workspace_mode`, `upstream_url`) and the
   workspace-scope secret for every delivery, with no cache; an unknown slug and a workspace
   without the secret answer the same 404.
2. The scheme is chosen by header (`X-Hub-Signature-256` is GitHub's, else `X-Gitlab-Token`
   is GitLab's, else 401); a GitLab token is compared in constant time before the body is
   read, a GitHub signature after, over the raw bytes, with `hmac.Equal`. An unverified
   request writes nothing and logs the slug and the scheme only.
3. The delivery id and the event name come from the headers of the scheme.
4. `reduceDelivery` turns the payload into an outcome, a target (`origin` or `upstream`,
   from the payload's repository path and the workspace's `git_url` and, on a `carry_patch`
   workspace, `upstream_url`) and zero or more `audit.HubEvent`s of actor type `forge`.
5. One `INSERT … ON CONFLICT DO NOTHING` into `forge_deliveries` decides: no row inserted
   is a duplicate, answered and nothing else; a failing insert is a 500 with nothing emitted,
   so that the forge's redelivery is processed in full.
6. The 202 answer is written, with its length, and flushed. Only then are the events handed
   to `Config.Audit` in order, on `context.WithoutCancel(request context)`: the broadcast
   emitter stores them in `hub_audit_events` and puts them on the SSE stream, where
   `GET /events` and `GET /audit` find them. An emit error is logged and changes nothing.

`forge.InitSchema` creates `forge_deliveries` at every start, and `main.go` starts
`go forge.StartDeliveryRetention(ctx, db)` right after it: the pruner (rows older than 7 days
and rows of deleted workspaces) runs once at start and then every hour, and is the only thing
that prunes the table. The delivery log is SQLite, in the store where workspaces and secrets
live, so the insert that deduplicates is atomic; the audit store (DuckDB) stays the place of
the events. The divergences from the spec are in
`docs/errata/32_forge_webhook_ingress_divergences.md`.

### What the writes do themselves

issuex's `Client` has no label lookup; its `CreateLabel` returns no label and counts an
existing label as a success; its `UpdateIssueRequest` sends only non-empty fields, so it
cannot clear a body or a set of labels or assignees; GitHub's `RemoveLabel` counts a 404
as a success, which would hide a missing issue; `CloseIssue` returns no issue and
`PostReviewComment` returns no URL (and on GitHub posts a review). The hub does
the following instead (the `writer` interface of `write_raw.go` is how the routes call
either; a client from `Config.NewClient` that is also a `writer` is used as it is, which
is how tests put a fake in its place):

- an issue update, a label lookup, a label create, the removal of a label on GitHub and
  the read of the issue after a not found are `rawForge` requests: through the same HTTP
  client, with the same token rules, and failing with the `*issuex.HTTPError` that
  `mapForgeError` already maps. They are not retried at all;
- a close is a comment (`AddComment`, which returns its URL) and an update with the state
  `closed`, and a comment on a pull request is `AddComment` on a reference to the pull
  request, which on both forges posts on its conversation;
- GitLab assignees are not sent, because GitLab takes user ids and issuex sends none.

These are findings for aflib. The divergences from the spec are in
`docs/errata/31_forge_writes_divergences.md`.

### What issuex does not give the hub

- It has no accessor for the forge or the API URL of a client. The diagnostic takes
  the forge from `issuex.DetectForge` and states the default API URL itself
  (`defaultAPIURL`).
- `issuex.ParseRemote` reads only hosts whose names contain `github` or `gitlab`, so
  `parseRemote` splits the URL of any other host itself and lets issuex try to classify
  it. issuex classifies by name only: a host that names neither forge is told apart
  only by an API URL that does.
- A client given no token uses the one in the hub's own environment, and the
  repository it works on, if it is given none, is that of the working directory's git
  remote. The hub always gives both (the transport removes the headers; the parsed
  repository is always in the options).
- `ListIssues` and `ListComments` return a struct with an `incomplete` or `truncated`
  flag; the routes answer the bare array and drop it.

The divergences from the spec are in
`docs/errata/30_forge_client_reads_divergences.md`.

## Git Server

The git server (`internal/gitserver`) provides smart HTTP git endpoints for
clone, fetch and push operations against workspace repositories. It is mounted
at `/git/<org>/<slug>.git/` and uses HTTP Basic authentication backed by the
hub's credential store.

### Request chain and authorization

Every git route runs `GitAuthMiddleware` (credential → `AuthInfo`, 401 for a
missing, invalid or blocked credential), `requireDotGitSuffix`, then
`gitResolverMiddleware`:

1. **Resolve.** `resolveWorkspaceFromDB` reads the workspace by slug (a NULL
   `org_id` reads as no org, which matches no URL) and requires the URL's org
   slug to match its org: otherwise pkt-line `404 repository not found`; a
   database error is a plain 500. A clone that is not ready is pkt-line
   `404 repository not available`.
2. **Authorize.** `authorizeGitAccess` derives the need from the request
   (`gitNeed`: `NeedWrite` for `git-receive-pack` and for `info/refs` with
   `service=git-receive-pack`, `NeedRead` otherwise) and makes one call to
   `wsaccess.AuthorizeWorkspaceAt` with the git refusal site (`Surface: git`,
   the route template, status 404). The owner and admin tokens may read and
   write, a member of the workspace's org may read, and a PAT bound to another
   workspace is refused and reported. wsaccess's 404 is written as pkt-line
   `404 repository not found`, its 403 as a pkt-line 403 with its message
   (`org members have read-only access to this workspace`), anything else as a
   pkt-line 500; the resolver then stops without a second response.
3. **Open.** `git.PlainOpen` of `<root>/<slug>/trunk`; a failure is a plain 500.

The handler then checks the `service` value and the PAT's git scope, so a
member's push is refused before the scope check and before anything is
unpacked, hooked, written or audited.

### Receive-Pack Flow

The receive-pack handler (`handleReceivePack`) processes `git push` requests:

1. **Unpack.** The incoming packfile is decoded and its objects are written
   into the workspace trunk's object store via go-git's `ReceivePack`.
2. **Pre-receive hook.** A registered `PreReceiveHookFunc` is called once per
   ref update, after unpacking and before the hub writes the ref. The hook
   receives the request context, database handle, workspace slug, actor
   identity and the ref update (name, old hash, new hash). Returning a
   non-nil error rejects that single ref update; the error text becomes the
   per-ref status message the client sees (`ng <ref> <message>`). Other refs
   in the same push are processed independently.
3. **Ref write.** The trunk storer wrapper (`thinPackSafeStorer`) intercepts
   `SetReference`, `CheckAndSetReference` and `RemoveReference` to invoke the
   pre-receive hook before delegating to the underlying storer.
4. **Side effects.** `head_sha` is refreshed from the trunk HEAD after every
   push, including a push in which every ref was rejected. Only accepted ref
   updates (report-status entry `ok`; for a client that did not request
   report-status, the updates the hook did not reject) drive the
   `hub.git.push` audit event and the post-push hook. Rejected refs are
   excluded from both.

The pre-receive hook is registered via `gitserver.RegisterPreReceiveHook(fn)`,
following the same pattern as `RegisterPostPushHook`. The git server imports
no carry-patch code; the hook is wired in `cmd/af-hub/main.go`.

A `RecoveryHook` is registered via `workspace.RegisterRecoveryHook(fn)`,
following the same nil-default pattern. It carries three operations: reading
the replaced SHA for a slug and branch, removing the backup ref for a slug
and branch, and running a reset-to-origin for a patch. The workspace package
defines the hook type; `carrypatch` implements it; `cmd/af-hub/main.go` wires
them together. Neither package imports the other.

### Carry-Patch Push Control

The carry-patch package registers a pre-receive hook that enforces a
single-writer model for registered patch branches. The behaviour depends on
`PATCH_BRANCH_SOURCE` and `PUSH_PATCHES_TO_ORIGIN`:

- **Reject mode** (`origin` source, forwarding off): the push is refused.
- **Forward mode** (`origin` source, forwarding on): the push is forwarded to
  the fork before the hub writes its ref.
- **Mirror mode** (`hub` source, mirroring on): the push is accepted and then
  mirrored to the fork, best effort.

### Post-Push Hook

After the push, the post-push hook runs asynchronously. It checks whether any
accepted pushed branch is a registered patch and, if
`AUTO_REBUILD_AFTER_PUSH` is not `"false"`, enqueues a rebuild. In hub mode
with mirroring enabled, it also force-pushes each accepted registered patch
branch to the fork.

## Pushing to origin

Hub pushes a workspace's trunk to its `origin` in two places, through one
function. `pushToOrigin` (`internal/workspace/push_core.go`, behind the
`pushToOriginFn` seam) opens the trunk, checks that it has an `origin`
remote, and pushes the given refspecs to `git_url` with go-git; a trunk that
cannot be opened or has no `origin` comes back as a `*trunkError`, hub's own
failure, apart from the push's own errors. It takes a progress writer because
a forge's reason for refusing a ref (a hook's message, a protected-branch rule)
reaches go-git only over the sideband.

- **Archive and reclone** call `archiveOpenAndPushFn`, whose production value
  `defaultArchiveOpenAndPushFn` pushes `refs/heads/*:refs/heads/*` through
  `pushToOrigin` with no deadline and maps go-git's already-up-to-date to
  `ErrAlreadyUpToDate`.
- **The branch push** (`POST /api/v1/workspaces/:slug/branches/:name/push`,
  `handleBranchPush` in `internal/workspace/branch_push.go`) pushes exactly
  `refs/heads/<name>:refs/heads/<name>` (with `+` when forced), with the
  credential `resolveCloneAuth` gives the archive.

The branch push decides everything that reads only the database and the
workspace row before it takes the workspace lock (access, body, name,
workspace state, the carry-patch integration and patch branches, a forced
push of the `branch` column), so those answers do not depend on whether the
workspace is busy. It then takes the lock with `wslock.TryLock` (`409
workspace_busy` when held; the rebuild-active flag is not consulted, because
the push deletes nothing) and does every read of the trunk under it in
`pushBranchLocked`: the `HEAD` branch for a forced push without a `branch`
column, the branch's commit, and the push.

The push and the listing of origin's refs after a non-fast-forward run on the
request context with its cancellation removed and a fixed two-minute
deadline, synchronously in the handler, so a caller that goes away cancels
nothing and the deferred unlock runs only after the push has ended. Each push
attempt emits one `hub.branch.push` event through `emitHubAuditContext` on a
context without cancellation, and each failed attempt logs one warning. All
text that leaves the route goes through the package's own `scrubText`, which
repeats internal/forge's userinfo pattern rather than importing it.

A push to hub's git server can move the trunk's checked-out branch while the
branch push holds the lock: receive-pack writes refs without the lock, and
`updateHeadSHA` (`internal/gitserver/handlers.go`) skips its worktree reset
when the lock is held, leaving that to the lock holder. The branch push
therefore notes the commit `HEAD` resolves to when it has opened the trunk
under the lock and, before it releases the lock, hard-resets the worktree to
`HEAD` when that commit has changed. A failed reset is logged and changes
nothing else.

## Patch Branch Resolver Hook

Registering a patch (`POST /api/v1/workspaces/:slug/patches`, single or
batch) resolves each branch name in the workspace trunk before the row is
inserted: `local`, then `origin_tracking`, then (with
`PATCH_BRANCH_SOURCE=origin`) `origin_fetch`. A resolved branch ends up as a
local `refs/heads/<name>` ref. Elements with `skip_branch_check` are not
resolved.

The resolver follows the same nil-default hook pattern as the recovery hook:

- `workspace.RegisterBranchCheckHook(fn)` registers a `BranchResolverFunc`,
  `func(ctx, slug, branchName) (method, error)`. The workspace package owns
  the type, the HTTP error mapping (`400` not found, `409` workspace busy,
  `502` origin fetch or credentials, `500` anything unclassified) and the
  `branch_resolution` audit metadata. With no hook registered a branch is
  accepted without resolution.
- `carrypatch.NewBranchResolverHook(...)` implements it: ref checks through
  the git runner, a single-branch fetch from the fork, and
  `wslock.TryLock` before anything is written. The hook releases the lock
  before it returns, so the database insert never runs under it.
- `cmd/af-hub/main.go` wires the two together.

`internal/workspace` and `internal/carrypatch` do not import each other.
Classified resolver errors cross the boundary structurally: the carrypatch
error type has a `BranchResolveKind() string` method, which the workspace
handler reads with `errors.As` against its own `BranchResolveError`
interface.

### Shared leaf package: `internal/wsaccess`

`internal/wsaccess` imports neither of the two packages, so both can import
it. It holds:

- The workspace access rule (spec 35, see below): `AuthorizeWorkspace`,
  `AuthorizeWorkspaceAt`, `ReadableSlugs` and `WriteRefusal`.
  `workspace.AuthorizeWorkspace` delegates to it, and `carrypatch`, which
  cannot import `workspace`, calls it directly.
- A per-request scope, `WithRequestScope` and `RequestScoped`: a small
  memo carried in the `context.Context`. It lets the handler pass
  request-scoped state to the hook through the hook's fixed signature.
- The token workspace binding (spec 33): the table, its lookup, both guards and
  the allowlist of routes bound tokens may call (see below).

### Workspace access (`internal/wsaccess`)

Who may see and change a workspace is decided in one place (spec 35). No REST
handler in `workspace`, `carrypatch`, `merge`, `sandboxes`, `forge`,
`boundtoken` or `secrets` compares `owner_id` with the caller itself.

- **`Access`** (`wsaccess.go`) is the caller's level on one workspace:
  `AccessAdmin` for an admin token, `AccessOwner` for a user id equal to
  `owner_id`, `AccessMember` for a user id with an `org_members` row for the
  workspace's `org_id`, else `AccessNone`. Admins and owners are decided
  without reading `org_members`; membership is one `COUNT(*)` per request, not
  cached, so a removed member loses access on the next request.
- **`Need`** is what an endpoint requires: `NeedRead` (member, owner, admin),
  `NeedWrite` (owner, admin; a member gets `403` with `error_type: forbidden`),
  `NeedPrivate` (owner, admin; a member gets the stranger's `404`).
- **`AuthorizeWorkspaceAt(db, auth, slug, need, site)`** checks, in order: nil
  auth 401; unknown slug 404; `AccessNone` 404 (no binding lookup, no report);
  a PAT bound to another workspace 404, reported once with `site`; then the
  need. It returns `WorkspaceAccess{Slug, OwnerID, OrgID, Status, CloneStatus,
  Access}`. Any database error is 500. `AuthorizeWorkspace` is the same with
  the request-less site `{404, api}`; `lookupWorkspaceForAuth` uses
  `SiteOf(c, 404)` so its reports carry the method and route, then loads the
  full row.
- **`WriteRefusal`** (`refusal.go`) writes a refusal in the apikit envelope,
  with `error_type: forbidden` (`ErrorTypeForbidden`) on a 403 only. Like
  apikit's writers it returns nil once written, so every caller decides that it
  refused from the status, not from the error.
- **`ReadableSlugs(db, auth)`** (`readable.go`) is the readable set in one
  query: owned workspaces of any status as owner, the workspaces of the
  caller's orgs as member; `all == true` for an admin token, an empty map for a
  credential with no user id, at most the bound slug for a bound PAT (resolved
  before rows open). The listing loads those slugs with
  `listWorkspacesBySlugs` (chunks of 500, sorted by `created_at` descending),
  so the membership predicate exists only in `wsaccess`.

The need of each route sits where the ownership check sat: after the PAT scope
check, before status and precondition checks.

`internal/audit` decides through the same rule, with its own refusal answers
and no `owner_id` comparison of its own outside ingestion:

- `requireWorkspaceRead` (per-run reads, the transcript) and
  `requireWorkspaceWrite` (`POST /sessions`) call `wsaccess.DeniesWorkspace`
  with the request's site (status 404) and then `AuthorizeWorkspace` with
  `NeedRead` or `NeedWrite`, through the shared `authorizeWorkspace`; the
  idempotent replay of another workspace's session decides with `NeedRead`.
  Workspace status is not checked.
- `hasWorkspaceAccess` (session get, usage, cost, the list with a slug) calls
  `DeniesWorkspace` (status 403), then `AuthorizeWorkspace` with `NeedRead`,
  and requires the workspace to be active; any failure is `false`, answered
  `403 workspace access denied`.
- `getAccessibleWorkspaces` (the list without a slug) keeps the active slugs
  of `ReadableSlugs`, never nil; `readableWorkspaces` (`GET /audit`,
  `GET /events`) turns `ReadableSlugs` into the `allowedWorkspaces` set (nil
  for an admin token), which the query renders as `workspace IN (...)` and the
  SSE stream keeps from connect to close.
- `checkWorkspaceAccess` (ingestion) and the session-creator check of
  complete and usage report are unchanged and stay owner-only.

The git server decides through `wsaccess.AuthorizeWorkspaceAt` too (see
[Git Server](#git-server)): members clone and fetch, and their pushes are
refused with a pkt-line 403.

### Token workspace bindings

A personal access token can be bound to one workspace. The binding is hub's own
record, the SQLite table `token_workspace_bindings` (`token_id TEXT PRIMARY
KEY`, `slug TEXT NOT NULL`, `created_at TEXT NOT NULL`, index
`idx_token_workspace_bindings_slug`). It has no foreign key to `workspaces`:
archiving a workspace leaves its bindings alone, and deleting it retires them
explicitly (`deleteWorkspace` calls `wsaccess.RevokeAndUnbindSlug` in its
transaction: the live tokens are revoked and the bindings deleted). The
workspace schema initialisation creates the table, and so do
`MountWorkspaceHandlers` and `MountGitHandlers`, before they register anything;
each returns the error if it cannot.

Everything lives in `internal/wsaccess`:

| Piece | What it does |
|-------|--------------|
| `LookupBinding`, `BoundSlug`, `DeniesWorkspace` (`binding.go`) | One primary-key lookup of a token id. `BoundSlug` only looks up a PAT that carries a `TokenID`, so API keys, admin tokens and requests without credentials never query. `DeniesWorkspace` is the rule itself: is this a PAT bound to a workspace other than this slug? A database error is returned, and every caller fails closed with it. |
| `BindingGuard` (`guard.go`) | Middleware on the API group, after apikit's auth middleware. For a bound PAT, a route whose template has `workspaces/:slug` passes only for its own slug (else `404 workspace not found`). Any other route passes only if it is on the allowlist (else `403 token_bound`). A PAT without a `TokenID` gets the id from its Bearer header. |
| `APIKitRouteGuard` (`apikit_guard.go`) | Deny-only middleware on the root Echo instance for the routes apikit's `MountHandlers` registered, which never see API-group middleware added later. It runs after routing and before apikit authenticates. A Bearer PAT id with a binding row gets `403 token_bound` without the slug. It never sets `AuthInfo` and never looks at the secret. |
| `AllowBoundTokens`, `IsAllowed` (`allowlist.go`) | The routes a bound token may call although they are not workspace-scoped, marked with the `*echo.Route` values returned at registration: `GET /workspaces`, the six session routes, `GET /audit`, `GET /events`, `GET /sandbox-profiles`. |
| `AuthorizeWorkspace` | Applies the same rule after its existence and access checks (a caller with no access is refused before the binding is looked up), for the code paths that reach a workspace without the guard. |

The wiring in `MountWorkspaceHandlers` is:

1. Create the table.
2. Snapshot the Echo route table, call `MountHandlers`, and record the routes it
   added (without `/healthz`, `/readyz`, `/version`).
3. Install `APIKitRouteGuard` on the root instance and `BindingGuard` on the API
   group.
4. Register hub routes.

Echo copies group middleware into every route and subgroup when it is added, so
every hub route registered afterwards, including routes added in the future, is
guarded. A route nobody marks is refused. The workspace handlers
(`lookupWorkspaceForAuth`, the list), the audit helpers (`requireWorkspaceRead`,
`requireWorkspaceWrite`, `readableWorkspaces`, `checkWorkspaceAccess`,
`hasWorkspaceAccess`, `getAccessibleWorkspaces`, the session writes) apply the
rule themselves too, and every caller of `wsaccess.AuthorizeWorkspace` or
`AuthorizeWorkspaceAt`, the git server included, gets it from that call. Each
resolves the binding before it opens any rows, because the test databases use
one connection.

### Issuing and retiring bound tokens: `internal/boundtoken`

`internal/boundtoken` (spec 34) holds the two routes that mint and list bound
tokens, the binding sweep and the refusal reporter. `cmd/af-hub/main.go` calls
`RegisterRoutes(server.APIGroup(), Config{DB, APIKit: server.Echo(), Audit})`
right after `MountWorkspaceHandlers`, registers the reporter with `Install` and
starts `StartBindingSweep` with `go`.

- **Minting by in-process delegation.** apikit's PAT handler is internal, so the
  mint handler serves `POST <mount>/user/tokens` through `Config.APIKit` with
  the caller's body, `Authorization` header and context, and records the answer
  in memory (`delegateMint`). `<mount>` is the matched route template without
  `/workspaces/:slug/tokens`. The inner request passes through the root
  middleware, so Prometheus counts it as a second request to `/user/tokens`.
- **Binding and rollback.** After apikit's 201, `wsaccess.InsertBinding` writes
  the row only while the workspace exists. Every failure after the 201 revokes
  the token first (`wsaccess.RevokeToken`, which sets `pats.revoked_at` as
  apikit's own revocation does), so no live unbound token is left behind.
- **Retiring.** `deleteWorkspace` calls `wsaccess.RevokeAndUnbindSlug` in its
  transaction, before the workspace row goes; `StartBindingSweep` runs
  `wsaccess.SweepBindings` at once and then hourly, for bindings whose
  workspace or token is gone.
- **Refusal reporting.** Every refusal point of spec 33 (both guards, and
  `DeniesWorkspace` with the `RefusalSite` its caller passes: the request's
  method and route, or none for `AuthorizeWorkspace`, which has no request)
  calls `wsaccess.ReportBoundRefusal` once per refused request. The observer is the
  `Reporter`, which emits `hub.token.bound_refused` at most once per token per
  minute with a `suppressed` count; `wsaccess` never imports `audit`.

The SQL on `token_workspace_bindings` and the `pats` revocation live in
`internal/wsaccess` (`binding_store.go`), beside spec 33's table and lookup, so
that `internal/workspace` can use them.

### Reading `PATCH_BRANCH_SOURCE` once per request

`handleAddPatch` installs a request scope on the request context before it
dispatches to the single or batch path. The resolver hook reads
`PATCH_BRANCH_SOURCE` through `wsaccess.RequestScoped`, so the first
resolution in a request reads the variable and every later one in the same
request (the remaining batch elements) reuses that value. A change to the
variable while a batch runs cannot mix modes within one request; it takes
effect on the next request. A caller that passes a context without a scope
gets one read per hook call.

### Batch order

A batch is processed in this order: syntax validation of every element
(`branch_name`, integration branch, `position`); resolution of every
non-skipped element in array order; duplicate and already-registered checks;
one transaction that inserts all rows. Syntax errors are therefore reported
before any element is fetched or given a local ref, and resolution always
precedes the duplicate checks. Local refs created for earlier elements stay
in place when a later element fails.
