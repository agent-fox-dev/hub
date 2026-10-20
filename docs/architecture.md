# Architecture

This document describes the high-level architecture of af-hub.

## Sandboxes

`internal/sandboxes` owns the deployment's sandbox definition: where sandboxes
run and what shape they take. It builds one AgentKit sandbox provider from the
`[sandbox]` section of `config.toml`, checks every profile against what that
provider can honour, and publishes the profiles read-only. It creates no
sandbox. The lease record (below) is built on top of it; the gateway, expiry and
a remote provider are later specs.

| File | Responsibility |
|------|----------------|
| `config.go` | `LoadConfig`: reads the `[sandbox]` table from the file apikit loads, expands `${NAME}` in the section only, refuses unknown keys with their full path. |
| `validate.go` | `Validate`: every static rule for the deployment keys and profiles, collected into one joined error. Uses `secrets.ValidateKey` for secret and env names. |
| `provider.go` | `ProviderFactory` (the shape of `podman.New`), `PodmanFactory`, `BuildProvider`. |
| `service.go` | `Service`, `Start`, `New`: probes the provider once, checks `network_modes` and turns each profile into a `sandbox.Spec` that `sandbox.CheckSpec` must accept. Keeps the provider and the validated profiles in memory. |
| `startup.go` | `Init`: the one call `cmd/af-hub` makes. Loads, validates, starts and logs. |
| `permissions.go`, `auth.go` | The `sandboxes:read` and `sandboxes:write` scopes and the standalone auth helpers. |
| `handlers.go` | `RegisterRoutes` and `GET /api/v1/sandbox-profiles`, built from the in-memory profiles. |
| `lease_schema.go`, `lease.go`, `store_query.go` | The `sandbox_leases` table (`InitSchema`), the `Lease` type and the `LeaseStore`: insert, read, list, `LookupByToken`, `EndLease`, `RevokeToken`. |
| `lease_service.go`, `lease_validate.go`, `token.go` | `LeaseService.Issue`: request validation, the required-secret check, id and token generation, the capacity count and the insert. |
| `secretcheck.go` | `ResolutionChain` and `CheckRequiredSecrets`: which scopes a workspace's secrets are searched in, and a check that lists names only. |
| `routes.go`, `handlers_issue.go`, `handlers_read.go`, `handlers_delete.go`, `render.go` | `RegisterLeaseRoutes`: the five lease routes, the checks they share (`begin`, `authorize`) and the lease representation. |

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
`failed`) because SQLite cannot alter a `CHECK`; only `leased` and `destroyed`
are reachable in this release.

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

Every ending of a lease goes through `LeaseStore.EndLease`: one conditional
`UPDATE` that sets the terminal state, `ended_at`, `updated_at` and `end_reason`
and clears the token hash for a lease that is still `leased` or `active`. The
`DELETE` route calls it, and a test pins that no other production code clears a
token hash.

Two exported pieces are seams for the sandbox gateway and have no production
caller yet: `LeaseStore.LookupByToken` (a presented token to its live lease, or
`ErrLeaseNotFound` without saying why) and `LeaseStore.RevokeToken`. A third,
`ResolutionChain`, is used today by the presence check at issue and is the order
the gateway will resolve secret *values* in, so that the check and the lookup at
creation agree.

### AgentKit dependency

The provider comes from the sibling checkout `../agentkit-go`. Its sandbox
packages are a separate Go module, `github.com/agent-fox-dev/agentkit-go/sandbox`,
that requires the root module, so `go.mod` replaces both with the sibling
(`replace` lines in a dependency's `go.mod` are ignored). The hub image build
and both CI workflows supply the checkout next to the repository the same way
they supply `../apikit`. `afc` stays a static `CGO_ENABLED=0` binary: neither
`cmd/afc` nor `internal/cli` imports `internal/sandboxes` or any AgentKit
package.

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
