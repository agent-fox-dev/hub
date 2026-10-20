# Architecture

This document describes the high-level architecture of af-hub.

## Sandboxes

`internal/sandboxes` owns the deployment's sandbox definition: where sandboxes
run and what shape they take. It builds one AgentKit sandbox provider from the
`[sandbox]` section of `config.toml`, checks every profile against what that
provider can honour, and publishes the profiles read-only. The lease record
(below) is built on top of it, and the sandbox gateway (below) is what creates
a sandbox for a lease. Expiry and a remote provider are later specs.

| File | Responsibility |
|------|----------------|
| `config.go` | `LoadConfig`: reads the `[sandbox]` table from the file apikit loads, expands `${NAME}` in the section only, refuses unknown keys with their full path. |
| `validate.go` | `Validate`: every static rule for the deployment keys and profiles, collected into one joined error. Uses `secrets.ValidateKey` for secret and env names. |
| `provider.go` | `ProviderFactory` (the shape of `podman.New`), `PodmanFactory`, `BuildProvider`. |
| `service.go` | `Service`, `Start`, `New`: probes the provider once, checks `network_modes` and turns each profile into a `sandbox.Spec` that `sandbox.CheckSpec` must accept. Keeps the provider and the validated profiles in memory. |
| `startup.go` | `Init`: the one call `cmd/af-hub` makes. Loads, validates, starts and logs. |
| `permissions.go`, `auth.go` | The `sandboxes:read` and `sandboxes:write` scopes and the standalone auth helpers. |
| `handlers.go` | `RegisterRoutes` and `GET /api/v1/sandbox-profiles`, built from the in-memory profiles. |
| `lease_schema.go`, `lease.go`, `store_query.go`, `lease_transitions.go` | The `sandbox_leases` table (`InitSchema`), the `Lease` type and the `LeaseStore`: insert, read, list, `LookupByToken`, and the conditional transitions `MarkActive`, `FailLease`, `DestroyLease`, `EndLease`. |
| `lease_service.go`, `lease_validate.go`, `token.go` | `LeaseService.Issue`: request validation, the required-secret check, id and token generation, the capacity count and the insert. |
| `secretcheck.go` | `ResolutionChain` and `CheckRequiredSecrets`: which scopes a workspace's secrets are searched in, and a check that lists names only. |
| `routes.go`, `handlers_issue.go`, `handlers_read.go`, `handlers_delete.go`, `delete_destroy.go`, `render.go` | `RegisterLeaseRoutes`: the five lease routes, the checks they share (`begin`, `authorize`), the lease representation (with `gateway_url`) and how `DELETE` ends a lease and its sandbox. |
| `gateway.go` | `StartGateway`: loads the key pair, binds `listen`, builds the `gatewayd.Server` and serves it; `Gateway.DestroyBox`. |
| `gateway_auth.go` | `NewLeaseAuthenticator`: the function gatewayd calls for every RPC to turn a lease token into a principal. |
| `backend.go`, `create_spec.go`, `secret_resolve.go`, `handle.go`, `events.go`, `scrub.go` | The wrapping backend, the `sandbox.Provider` hub gives gatewayd: `Create` and its outcomes, the spec built from the lease, the secrets read when the sandbox is created, the handle around the provider's sandbox, the audit events and the scrubber for provider errors. |

### Start-up order

`cmd/af-hub` calls `sandboxes.Init` immediately after `apikit.Bootstrap` and
before the workspace root, the job queues, the workers and the routes are set
up:

1. `Bootstrap` runs first, so generating the first-boot admin token never
   depends on the sandbox configuration or on a container engine.
2. `Init` loads the section from `apikit.ConfigPath()`. No section means the
   feature is off: `Init` returns a nil service after one info log line.
3. Otherwise `Start` validates the section; static failures are returned
   before the provider is built or the engine is contacted. Then
   `BuildProvider` builds the Podman provider, `New` probes it, and each
   profile is checked against its capabilities.
4. Any failure is fatal: the process exits before a queue, a worker or a
   listener exists.
5. `sandboxes.Permissions()` is appended to the scopes registered with apikit
   on every start, and `RegisterRoutes` mounts the listing with the service,
   or with a nil service when the feature is off. A nil service answers
   `404 sandboxes_disabled`.
6. Later, after the lease table exists and before `server.Start()`, `StartGateway`
   binds the gateway's listener and starts serving it, and the lease service is
   built with the gateway's URL and the gateway itself, which it destroys boxes
   through. A key pair that cannot be loaded or an address that cannot be bound
   is fatal, like any other failure of the section. A hub without sandboxes
   binds nothing.

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
`failed`) because SQLite cannot alter a `CHECK`. All but `expired` are reachable:

```
leased ── Create succeeds ─────────────────▶ active
  │                                            │
  │                                            ├── DELETE ───────────────────▶ destroyed  (end_reason api)
  │                                            └── Close through the gateway ▶ destroyed  (end_reason client)
  ├── DELETE ───────────────────────────────────────────────────────────────▶ destroyed  (end_reason api)
  └── Create cannot proceed (profile gone, required secret missing,
      spec cannot be built, provider failed) ────────────────────────────────▶ failed     (error set)
```

Every transition is one conditional `UPDATE` on the state the caller expects to
find, and `destroyed` and `failed` are terminal. Two callers racing for the same
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
   workspaces) and the lease is inserted unless `max_sandboxes` is reached. The
   secret check runs before the transaction: reading secrets inside it would need
   a second connection. `apikit.OpenDatabase` begins transactions `IMMEDIATE`;
   the lock keeps the limit with a handle whose transactions are deferred.
5. One `hub.sandbox.lease` audit event is emitted after the commit.

Every ending of a lease (`failed` and `destroyed` alike) goes through one
statement in `lease.go`, `endLease`: it sets the terminal state, `ended_at`,
`updated_at`, the end reason or the error text, and clears the token hash, for a
lease that is in a state the caller names. `EndLease`, `FailLease` and
`DestroyLease` are thin callers of it, and a test pins that no other production
code clears a token hash. Clearing the hash is what revokes a token, so ending a
lease and revoking its token are the same act. `MarkActive` (leased to active,
recording the sandbox id) leaves the token alone.

`LeaseStore.LookupByToken` (a presented token to its live lease, or
`ErrLeaseNotFound` without saying why) is what the gateway authenticates with.
`LeaseStore.RevokeToken` still has no production caller. `ResolutionChain` serves
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
  `Create` at a time), loads it, refuses one that is not `leased` or whose time is
  up, builds the spec from the lease alone (`buildSpec`), reads the secrets
  (`resolveSecrets`) and asks the provider. The outcome is recorded by the
  conditional transitions above. A box that arrives for a lease that a `DELETE`
  has ended in the meantime is destroyed on the spot.
- **The handle** is what `Create` returns: the provider's sandbox with two
  differences. `Spec` redacts the environment values, because the gateway sends
  the spec back to the caller. `Close` is the client destroy path: gatewayd calls
  it when the holder destroys its sandbox (and also at the sandbox's TTL, when it
  prunes a box that is no longer live, and at shutdown), and it ends the lease with the reason `client` unless a `DELETE` is
  ending it or the lease's time is up (within two seconds of `expires_at`, since
  the box's TTL is the lease's time cut to whole seconds and gatewayd closes the
  box a moment early). The handle has an `Unwrap`, without which
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
- **Reap.** The backend's `Reap` passes through to the provider for the gateway's
  own periodic call, with the error text scrubbed; with a principal, a lease
  holder's call, it does nothing.
- **Scrubbing.** Provider error text is scrubbed (`scrub`) before it is stored on
  a lease, put in an event, logged or returned: every secret value and fixed
  environment value becomes `[redacted]`, URL user information is removed, and
  only then is the text cut to 1024 bytes on a UTF-8 boundary.
- **Events** are emitted in one place (`events.go`; `delete_destroy.go` for
  `DELETE`): `hub.sandbox.create`, `hub.sandbox.failed` and `hub.sandbox.destroy`.
  A nil emitter skips emission and an emit error is logged and changes no outcome.

The gateway never calls `Server.Shutdown`: destroying the boxes when the hub
stops, and marking leases that run out, belong to the expiry work. Until then a
box left behind is bounded by the TTL the provider enforces.

Where the gateway differs from what its spec expected of AgentKit (no provider
`List` or `Destroy`, the codes gatewayd answers with, the handle's `Close`) is in
`docs/errata/27_sandbox_gateway_divergences.md`.

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

`sandbox.Provider` has no `List` or `Destroy`; the start-up probe is therefore
one `Reap` call under a 10-second timeout. See
`docs/errata/25_sandbox_profiles_divergences.md`.

## Git Server

The git server (`internal/gitserver`) provides smart HTTP git endpoints for
clone, fetch and push operations against workspace repositories. It is mounted
at `/git/<org>/<slug>.git/` and uses HTTP Basic authentication backed by the
hub's credential store.

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

`internal/workspace` and `internal/carrypatch` do not import each other
(`TestTS21_36_WorkspaceAndCarrypatchDoNotImportEachOther` in
`internal/carrypatch/import_graph_test.go` checks this with `go list -deps`).
Classified resolver errors cross the boundary structurally: the carrypatch
error type has a `BranchResolveKind() string` method, which the workspace
handler reads with `errors.As` against its own `BranchResolveError`
interface.

### Shared leaf package: `internal/wsaccess`

`internal/wsaccess` imports neither of the two packages, so both can import
it. It holds:

- `AuthorizeWorkspace`, the per-workspace ownership check.
  `workspace.AuthorizeWorkspace` delegates to it, and `carrypatch`, which
  cannot import `workspace`, calls it directly.
- A per-request scope, `WithRequestScope` and `RequestScoped`: a small
  memo carried in the `context.Context`. It lets the handler pass
  request-scoped state to the hook through the hook's fixed signature.

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
