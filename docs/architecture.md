# Architecture

This document is for contributors to the hub. It explains how the code is
organised, how a request travels through it, where state is kept and how the
parts depend on each other, and it names the packages, files, functions and
tables involved, with their locking and the order of their internal steps.
For what the hub is and why it exists, read the [README](../README.md). For
what a caller sends, sees and may do, read the [API reference](api.md), the
[CLI reference](cli.md), [Server Configuration](configuration.md) and
[Permissions](permissions.md); the [Carry-Patch Workflow](carry_patch_workflow.md)
explains carry-patch from a user's side. The reasons behind the design are
recorded in the [ADRs](adr/), and the exported Go API of each public package
is in [docs/generated](generated/) (written by `make doc`).

## Contents

- [System overview](#system-overview)
  - [Two binaries](#two-binaries)
  - [How a request flows](#how-a-request-flows)
  - [Where state lives](#where-state-lives)
  - [Sibling modules](#sibling-modules)
- [Component map](#component-map)
- [Start-up and shutdown](#start-up-and-shutdown)
  - [Start-up sequence](#start-up-sequence)
  - [The maintenance loop](#the-maintenance-loop)
  - [Shutdown](#shutdown)
- [What apikit provides](#what-apikit-provides)
- [Workspaces](#workspaces)
  - [The workspace row and its settings](#the-workspace-row-and-its-settings)
  - [Creating and cloning](#creating-and-cloning)
  - [Git credentials of a workspace](#git-credentials-of-a-workspace)
  - [Sync](#sync)
  - [Archive, reactivate, reclone and delete](#archive-reactivate-reclone-and-delete)
  - [Pushing to origin](#pushing-to-origin)
- [The workspace lock](#the-workspace-lock)
- [Job queues](#job-queues)
  - [The clone queue](#the-clone-queue)
  - [The durable job queue](#the-durable-job-queue)
- [Workspace access and bound tokens](#workspace-access-and-bound-tokens)
  - [Scopes](#scopes)
  - [Workspace access](#workspace-access)
  - [Token workspace bindings](#token-workspace-bindings)
  - [Issuing and retiring bound tokens](#issuing-and-retiring-bound-tokens)
- [Secrets and variables](#secrets-and-variables)
  - [Storage](#storage)
  - [The routes](#the-routes)
  - [Resolution chains](#resolution-chains)
- [Merge and rebase](#merge-and-rebase)
  - [A merge job](#a-merge-job)
  - [Batch rebase](#batch-rebase)
- [Git server](#git-server)
  - [Request chain and authorization](#request-chain-and-authorization)
  - [Fetch and clone](#fetch-and-clone)
  - [Receive-pack flow](#receive-pack-flow)
  - [Protected refs](#protected-refs)
- [The carry-patch module](#the-carry-patch-module)
  - [The host contract](#the-host-contract)
  - [Inside the module](#inside-the-module)
  - [The rebuild](#the-rebuild)
  - [Rollback and preview](#rollback-and-preview)
  - [The carry-patch sync](#the-carry-patch-sync)
  - [Carry-patch push control](#carry-patch-push-control)
  - [Post-push hook](#post-push-hook)
  - [Patch branch resolver hook](#patch-branch-resolver-hook)
  - [Reading `patch_branch_source` once per request](#reading-patch_branch_source-once-per-request)
  - [Batch order](#batch-order)
  - [Reset to origin and backup refs](#reset-to-origin-and-backup-refs)
  - [The patch table](#the-patch-table)
  - [The git runner](#the-git-runner)
- [Sandboxes](#sandboxes)
  - [Start-up order](#start-up-order)
  - [The lease record](#the-lease-record)
  - [The sandbox gateway](#the-sandbox-gateway)
  - [The remote gateway provider](#the-remote-gateway-provider)
  - [Lease lifetime: the sweep, recovery, shutdown and retention](#lease-lifetime-the-sweep-recovery-shutdown-and-retention)
  - [AgentKit dependency](#agentkit-dependency)
- [Forge client](#forge-client)
  - [A forge read](#a-forge-read)
  - [A forge write](#a-forge-write)
  - [A webhook delivery](#a-webhook-delivery)
  - [What the writes do themselves](#what-the-writes-do-themselves)
  - [What issuex does not give the hub](#what-issuex-does-not-give-the-hub)
- [Audit, sessions and events](#audit-sessions-and-events)
  - [The audit database](#the-audit-database)
  - [Hub events and the emitter](#hub-events-and-the-emitter)
  - [The event stream](#the-event-stream)
  - [Agent ingestion and the audit query](#agent-ingestion-and-the-audit-query)
  - [Sessions, usage and cost](#sessions-usage-and-cost)
  - [Retention](#retention)
  - [Metrics](#metrics)
- [Scrubbing](#scrubbing)
- [The CLI](#the-cli)

## System overview

The hub holds everything an AI-agent run needs except the place to run it:
workspaces (a git clone per repository), merge and rebuild queues, carry-patch
fork maintenance, secrets and variables, sandbox leases, the forge client and
webhook ingress, agent session and cost accounting, and an audit log with a
live event stream. It runs as **one process and one replica**: the
per-workspace lock, the rebuild-active flag, the clone queue and the SSE
connections live in memory.

```
  afc, HTTP clients      git clients        GitHub / GitLab        AgentKit clients
         │                   │              webhooks │                    │
         ▼                   ▼                       ▼                    ▼
 ┌───────────────────── af-hub: HTTP listener (apikit's Echo server) ─────┐  ┌─ [sandbox].listen ─┐
 │ /api/v1/...   apikit auth → binding guard → handler → wsaccess          │  │ gatewayd.Server    │
 │ /git/...      Basic auth → resolver → wsaccess → go-git / git CLI       │  │ lease authenticator│
 │ /hooks/forge/:slug   signature → forge_deliveries → audit emitter       │  │ wrapping backend   │
 │ /metrics, /healthz, /readyz, /version                                   │  └─────────┬──────────┘
 └──────┬───────────────┬──────────────────┬──────────────────┬───────────┘            │
        ▼               ▼                  ▼                  ▼                        ▼
   SQLite (apikit  DuckDB (audit,   <workspace root>/   clone queue (memory),   Podman, or a remote
   DB + hub tables) sessions)       <slug>/trunk        durable job queue       agentkit-gateway
        │                                │                                      
        └── forge API (issuex) ──▶ GitHub / GitLab      git push/fetch (go-git) ──▶ origin, upstream
```

### Two binaries

- **`af-hub`** (`cmd/af-hub`; `make build` writes it to `bin/hub`) is the
  server. It serves the REST API, the git smart HTTP server, the forge webhook
  ingress and `/metrics` on apikit's HTTP listener (`[server].port`), and, with
  a `[sandbox]` section, the AgentKit sandbox gateway on a second listener
  (`[sandbox].listen`). It needs CGo, for the DuckDB driver, and the git CLI
  at run time, since rebuilds, merges, `upload-pack` and the carry-patch
  helpers run `git`.
- **`afc`** (`cmd/afc`) is the command-line client. It is apikit's command
  tree plus the hub's commands (`internal/cli`) and the carry-patch module's
  (`carrypatch/cli`), and it speaks only the REST API. It is a static
  `CGO_ENABLED=0` binary: neither `cmd/afc` nor `internal/cli` imports
  `internal/sandboxes`, `internal/forge`, any AgentKit package, gRPC or
  DuckDB in a build.

### How a request flows

**A REST request** under the API mount point (`/api/v1`, apikit's
`[server].mount_point`):

1. Echo's `Pre` middleware runs before routing. The hub's only one is the git
   server's `gitBodyPassthrough`, which touches `/git/...` requests alone.
2. The middleware of the root Echo instance runs for every route, in the
   order it was added with `Use`: apikit's own (among them the body size
   limit, `[server].max_body_size`), then `wsaccess.APIKitRouteGuard` (which
   acts only on the routes apikit's `MountHandlers` registered), then the
   Prometheus middleware of `audit.Metrics`.
3. The API group's middleware: apikit's bearer authentication, which puts an
   `apikit.AuthInfo` on the context, then `wsaccess.BindingGuard`, which
   confines a token bound to a workspace (see
   [Token workspace bindings](#token-workspace-bindings)).
4. The handler checks, in this order: a credential (401); a PAT's scope
   (`wsaccess.CanRead`/`CanWrite` or the package's helper; 403, or 404 on the
   workspace CRUD routes); the caller's access to the workspace through
   `wsaccess.AuthorizeWorkspace` with the route's need (404 or 403, see
   [Workspace access](#workspace-access)); the workspace's status and the
   route's own preconditions (400, 409); for a route that touches the clone,
   the [workspace lock](#the-workspace-lock) with `wslock.TryLock` (409
   `workspace_busy`). Then it does its work against SQLite, the clone, the
   job queue or the forge, and records an audit event through `audit.Emitter`.

**A git request** (`/git/<org>/<slug>.git/...`) passes the same `Pre` and root
middleware, then the git group's own chain: HTTP Basic authentication of a
hub credential, the URL check, and the resolver, which applies the same access
rule (see [Git server](#git-server)).

**A forge delivery** (`POST /hooks/forge/:slug`) is on the root instance,
outside the API group: no bearer authentication, no API body limit. The forge
proves itself with the signature of the workspace's webhook secret (see
[A webhook delivery](#a-webhook-delivery)).

**A sandbox gateway call** does not go through Echo at all: it is gRPC on the
gateway's own listener, authenticated with a lease token (see
[The sandbox gateway](#the-sandbox-gateway)).

### Where state lives

| Store | What it holds | Created by |
|-------|---------------|------------|
| SQLite, `[database].path` (modernc.org/sqlite, pure Go), opened by `apikit.OpenDatabase` | apikit's tables (`users`, `orgs`, `org_members`, `api_keys`, `pats`, ...) and the hub's: `workspaces` and `patches`; `token_workspace_bindings`; `secrets` and `variables`; the job queue's tables; `forge_deliveries`; `sandbox_leases` | apikit; `workspace.InitSchema`; `wsaccess.InitBindingSchema`; `secrets.InitSchema`; `jobqueue.InitSchema` and its migrations; `forge.InitSchema`; `sandboxes.InitSchema` |
| DuckDB, `AF_AUDIT_DB_PATH` (default `audit.duckdb` beside the SQLite file) | `hub_audit_events`, `agent_audit_events`, `session_outcomes`, `tool_calls`, `tool_errors`, `agent_traces`, `postmortems`, `agent_sessions`, `token_usage` | `audit.OpenDB`, `audit.InitSchema` |
| Disk, `[workspace].path` (the workspace root) | One directory per workspace: `<root>/<slug>/trunk`, a non-bare clone with the remote `origin` (and `upstream` on a carry-patch fork), and `<root>/<slug>/rebuild/<job id>`, the worktree of a running rebuild | `workspace.EnsureWorkspaceRoot`, the clone queue, the rebuild |
| Memory | The per-workspace mutexes and rebuild-active flags (`wslock`), the clone queue, the SSE connections, the bound-refusal limiter, the sandbox gateway's box handles and `Create` claims | at start |

Every table is created (and migrated) at every start with `CREATE TABLE IF NOT
EXISTS` and idempotent `ALTER TABLE`s, so a new release needs no migration
step of its own. SQLite cannot alter a `CHECK` constraint, so a change of one
rebuilds the table: `workspace.InitSchema` folds the old status columns into
one this way ([ADR 08](adr/08-fold-the-workspace-status-columns.md)).

`apikit.OpenDatabase` begins every transaction `IMMEDIATE`, so a write
transaction takes SQLite's write lock at its start and two of them never
deadlock on an upgrade.

### Sibling modules

| Module | Expected at | What the hub uses |
|--------|-------------|-------------------|
| [apikit](https://github.com/txsvc/apikit) | `../apikit` | Configuration, the SQLite database, first-boot bootstrap, the Echo server and its authentication, users, orgs, API keys, PATs and the scope registry, error envelopes, and the CLI scaffolding (see [What apikit provides](#what-apikit-provides)) |
| [agentkit-go](https://github.com/agent-fox-dev/agentkit-go) | `../agentkit-go` | Three modules: the root module, `.../sandbox` (providers, `sandbox.Spec`) and `.../sandbox/gatewayd` (the gateway server); see [AgentKit dependency](#agentkit-dependency) |
| [aflib](https://github.com/agent-fox-dev/aflib) | `../aflib` | `issuex`, the forge client (only `internal/forge` imports it), and `jobqueue`, the durable job queue |
| `carrypatch` | `./carrypatch` | The carry-patch module, nested in this repository with a `go.mod` of its own that replaces apikit and aflib at `../../apikit` and `../../aflib` ([ADR 05](adr/05-move-carry-patch-to-its-own-module.md)) |

All four are `replace` targets in `go.mod` (a `replace` in a dependency's own
`go.mod` is ignored, so the hub replaces all three AgentKit modules itself).
The hub image build and the CI workflows supply the sibling checkouts as build
contexts. The Makefile and the Containerfile set `apikit.TokenPrefix` to `af`
at link time, so credentials look like `af_pat_...`.

## Component map

| Package | Responsibility | Key entry points |
|---------|----------------|------------------|
| `cmd/af-hub` | Wires everything at start, runs the maintenance loop, shuts the sandbox gateway down | `main`, `startSandboxes`, `runServer`, `runMaintenance` (`maintenance.go`) |
| `cmd/afc` | The CLI's entry point | `main`: `cli.BuildRootCommand`, `apikit.CLIExecute`, `cli.ExitCode` |
| `internal/workspace` | Workspace CRUD, the clone queue, sync and its extension point, archive, reactivate, reclone, delete, the branch push, settings, the personal org hook, the `workspaces` and `patches` DDL | `MountWorkspaceHandlers`, `InitSchema`, `InitCloneQueue`, `InitSyncFunctions`, `RegisterSyncExtension`, `ResolveCloneAuth`, `ResolveUpstreamAuthMethod`, `SettingsReader`, `MigrateVariableSettings`, `CreatePersonalOrg`, `SetAuditDependencies` |
| `internal/wslock` | The per-workspace lock and the rebuild-active flag | `Lock`, `TryLock`, `BeginRebuild`, `RebuildActive` |
| `internal/wsaccess` | The workspace access rule, the scope helpers and their migration, token bindings and their guards | `AuthorizeWorkspace`, `AuthorizeWorkspaceAt`, `ReadableSlugs`, `CanRead`, `CanWrite`, `MigrateScopes`, `BindingGuard`, `APIKitRouteGuard`, `AllowBoundTokens`, `SweepBindings`, `ReportBoundRefusal` |
| `internal/boundtoken` | Minting and listing workspace-bound tokens; the reporter of bound refusals | `RegisterRoutes`, `Install` |
| `internal/secrets` | The secrets and variables store and its 25 routes | `NewStore`, `InitSchema`, `RegisterRoutesWithAudit`, `Store.GetSecretValue`, `Store.ResolveVariables` |
| `internal/merge` | Merge jobs (rebase, then fast-forward) and the synchronous batch rebase | `RegisterHandler`, `Handler.HandleMergeJob`, `NewMergeAPIConfig`, `RegisterMergeRoutes` |
| `internal/gitserver` | The git smart HTTP server and its two hook points | `MountGitHandlers`, `RegisterPreReceiveHook`, `RegisterPostPushHook`, `SetAuditEmitter`, `GitPermissions` |
| `internal/cphost` | Binds the carry-patch module to the hub | `Bind`, `Emitter`, `PreReceive`, `PostPush` |
| `internal/forge` | The forge read and write routes, the webhook secret, the ingress and the delivery log | `RegisterRoutes`, `MountWebhookIngress`, `InitSchema`, `PruneDeliveries` |
| `internal/sandboxes` | Sandbox configuration and profiles, leases, the embedded gateway, the sweep and the reaper | `Init`, `RegisterRoutes`, `InitSchema`, `MigrateLeaseExpiry`, `NewGateway`, `NewLeaseService`, `RegisterLeaseRoutes` |
| `internal/audit` | The DuckDB audit store, hub events and the SSE stream, agent ingestion, the unified query, sessions, usage and cost, retention, the Prometheus metrics | `OpenDB`, `InitSchema`, `NewStore`, `NewEmitter`, `NewSSEManager`, `RegisterSessionRoutes`, `RegisterRoutes`, `RegisterAuditQueryRoutes`, `RunRetention`, `NewMetrics` |
| `internal/scrub` | Making text that may carry a credential safe to show | `Text`, `Userinfo`, `TruncateUTF8` |
| `internal/cli` | The hub's `afc` commands | `BuildRootCommand`, `ExitCode` |
| `internal/testutil` | Shared test helpers (database, credentials, requests, seeds) | tests only |
| `carrypatch` | The carry-patch module: patch routes and store, rebuild job, rollback, preview, rerere, patch status, the sync extension, push control | `SetHost`, `RegisterRoutes`, `RegisterRebuildJob`, `NewCarryPatchSyncHook`, `NewPreReceiveHook`, `NewPostPushRebuildHook`, `NewBranchResolverHook`, `RecoveryService`, `CleanupStaleRebuildWorktrees` |
| `carrypatch/gitcmd` | The hardened git CLI runner, also used by `internal/merge` and `internal/workspace` | `New`, `GitRunner`, `ValidateRefName` |
| `carrypatch/cli` | The `afc` commands `patch`, `rebuild`, `rerere` and `workspace patch-status` | `PatchCmd`, `RebuildCmd`, `RerereCmd`, `PatchStatusCmd` |

The dependencies point one way: `internal/wslock`, `internal/wsaccess`,
`internal/scrub` and `internal/secrets` are leaves the feature packages import;
`internal/workspace` imports `wsaccess`, `wslock`, `secrets` and `audit`; the
feature packages (`merge`, `gitserver`, `forge`, `sandboxes`, `boundtoken`)
import those; `internal/cphost` imports the module and the hub packages it
adapts; `cmd/af-hub` imports everything. The carry-patch module imports
nothing of the hub.

## Start-up and shutdown

### Start-up sequence

`cmd/af-hub/main.go` does everything in one function, in this order. Any
failure up to the last step is fatal (`log.Fatal`), before a listener exists.

1. Parse `--admin-email` and `--reset-admin-token`; `apikit.LoadConfig`;
   `apikit.OpenDatabase` (closed by a deferred call).
2. `apikit.Bootstrap`: on first boot or a token rotation it writes the admin
   token and exits. It runs before anything else can fail, so generating the
   first admin token never depends on the rest of the configuration.
3. `wsaccess.MigrateScopes` rewrites the scopes of stored PATs that name a
   scope from before the read/write collapse ([ADR 06](adr/06-collapse-scopes-to-read-and-write.md)).
4. `sandboxes.Init` loads, validates and probes the `[sandbox]` section (see
   [Start-up order](#start-up-order)).
5. `workspace.EnsureWorkspaceRoot`; a context `ctx` that `main` cancels when it
   returns; `workspace.InitCloneQueue` (the clone workers start on `ctx`) and
   `workspace.InitSyncFunctions`.
6. `jobqueue.InitSchema`, `MigrateGroupKey` and `MigrateProgress`, then
   `jobqueue.New`: one durable queue for merge and rebuild jobs.
7. `secrets.NewStore`, `workspace.SettingsReader`, and the two credential
   closures over `workspace.ResolveCloneAuth` (origin) and
   `ResolveUpstreamAuthMethod` (upstream) that every git and forge caller
   shares.
8. The job handlers: `merge.RegisterHandler` and
   `carrypatch.RegisterRebuildJob`. All job types are registered before the
   queue starts.
9. `carrypatch.CleanupStaleRebuildWorktrees` removes rebuild worktrees a crash
   left behind, while no rebuild can be running.
10. `Queue.Start` (stopped by a deferred `Stop`).
11. `audit.NewMetrics` and `audit.SetMetrics`; `audit.OpenDB`,
    `audit.InitSchema`, `audit.NewStore`; the SSE manager
    (`AF_SSE_MAX_CONNECTIONS`, run on `ctx`); `audit.NewEmitter` over both.
12. `apikit.NewServer` with a health check that pings SQLite;
    `OnAfterUserCreate(workspace.CreatePersonalOrg)`, registered before the
    user-creation paths are mounted.
13. The scopes of every module (`gitserver`, `secrets`, `merge`, `carrypatch`,
    `audit`, `sandboxes`, `forge`) are collected and passed to
    `workspace.MountWorkspaceHandlers`, which adds its own, mounts apikit's
    handlers, installs the two binding guards and registers the workspace
    routes (see [Token workspace bindings](#token-workspace-bindings)). Every
    hub route is registered after this call, so the binding guard covers it.
14. `boundtoken.RegisterRoutes` and `boundtoken.Install`;
    `workspace.SetAuditDependencies`; the audit emitter is handed to the merge
    and rebuild handlers.
15. The audit routes (`RegisterSessionRoutes`, `RegisterRoutes`,
    `RegisterAuditQueryRoutes`).
16. `secrets.InitSchema` and `secrets.RegisterRoutesWithAudit`; then
    `workspace.MigrateVariableSettings` moves the behaviour switches that were
    workspace variables into the settings ([ADR 07](adr/07-make-the-behaviour-switches-workspace-settings.md)).
17. `forge.InitSchema`; `sandboxes.InitSchema` and `MigrateLeaseExpiry`
    (whether or not sandboxes are enabled).
18. `go runMaintenance(...)` (see [The maintenance loop](#the-maintenance-loop)).
19. The merge routes; the sandbox profile listing; `startSandboxes` builds and
    starts the sandbox gateway and the lease service, and the lease routes are
    mounted; the forge routes.
20. The carry-patch module: `cphost.Bind`, the branch resolver and the recovery
    service, `carrypatch.RegisterRoutes`, the sync extension
    (`workspace.RegisterSyncExtension`), and the git hooks
    (`gitserver.RegisterPreReceiveHook`, `RegisterPostPushHook`, through
    `cphost.PreReceive` and `cphost.PostPush`).
21. `gitserver.SetAuditEmitter`, `gitserver.MountGitHandlers` and
    `forge.MountWebhookIngress` on the root Echo instance; the Prometheus
    middleware and `GET /metrics`.
22. `runServer(server.Start, ...)`: `server.Start` serves until apikit shuts
    the server down on SIGTERM or SIGINT.

### The maintenance loop

`runMaintenance` (`cmd/af-hub/maintenance.go`) runs a list of tasks once at
start and then every `maintenanceInterval` (one hour) until `ctx` ends: the
audit retention (`audit.RunRetention`, see [Retention](#retention)), the
pruning of the forge delivery log (`forge.PruneDeliveries`) and the binding
sweep (`wsaccess.SweepBindings`). It is started with `go` after every table
the tasks touch exists, so it never delays the start. A task that fails is
logged and the others still run; a task cut short by the cancellation is no
failure.

### Shutdown

When `server.Start` returns, `runServer` calls `Gateway.Shutdown` (a no-op on
a hub without sandboxes; see
[Lease lifetime](#lease-lifetime-the-sweep-recovery-shutdown-and-retention)),
also when the server failed, because a `log.Fatal` after it would skip the
deferred calls. Then `main` returns and its deferred calls run in reverse:
`cancel` ends the clone workers (a clone in progress is interrupted, queued
clone jobs are dropped), the maintenance loop and the SSE manager; the
durable queue stops; the DuckDB and SQLite databases close.

## What apikit provides

The hub builds on apikit and defines no auth structs, context keys or error
envelopes of its own ([ADR 10](adr/10-follow-apikit-and-the-code-where-the-specs-disagree.md)):

- **Configuration and storage.** `apikit.LoadConfig` reads `config.toml`
  (`apikit.ConfigPath()` names the file, which `sandboxes.LoadConfig` reads
  again for its own section); `apikit.OpenDatabase` opens SQLite.
- **Identity.** `apikit.Bootstrap` (the admin token); the OAuth login that
  creates users and API keys; orgs and `org_members`; PATs and their scopes.
  `MountHandlers` mounts all of it under the API group and takes the hub's
  scopes as `apikit.Permission` values, so a PAT can be minted with them.
  `OnAfterUserCreate` lets the hub create a personal org for every new user in
  the same transaction (`workspace.CreatePersonalOrg`: the username sanitised
  into a slug, up to ten suffixes on a collision, an `orgs` row and an
  `org_members` row).
- **The server.** `apikit.NewServer` builds the Echo instance with its
  middleware, the API group with bearer authentication, `/healthz`, `/readyz`
  and `/version`; `ExternalURL()` is `[server].external_url`.
- **Handler helpers.** `apikit.GetAuthInfo` reads the credential; an
  `AuthInfo` carries the credential type (`admin_token`, `api_key`, `pat`),
  the user id, a PAT's token id and permissions. `apikit.WriteAPIError` and
  `WriteAPIErrorWithType` write the error envelope.
- **The CLI.** `apikit.RootCommand`, the `login`, `user`, `keys`, `tokens`,
  `orgs` and `admin` commands, `CLIExecute`, `DoRequest` (which prepends
  `/api/v1`), `CLIHandleError` and `CLIExitCode`.

## Workspaces

`internal/workspace` owns the workspace row, its clone on disk and the routes
that change either (`routes.go`): create, list, get, update, archive,
reactivate, sync, reclone, delete and the branch push.

| File | Responsibility |
|------|----------------|
| `schema.go` | `InitSchema`: the `workspaces` and `patches` tables, their idempotent column migrations, and `foldStatusColumns`, the one-time rebuild of an old `workspaces` table ([ADR 08](adr/08-fold-the-workspace-status-columns.md)) |
| `store.go` | The `Workspace` type and its queries: insert, get, list (`listWorkspacesBySlugs`, in chunks of 500), update, archive, reactivate, `deleteWorkspaceRevoked` |
| `settings.go` | `Settings`, their validation and JSON encoding, `SettingsReader` and `MigrateVariableSettings` |
| `handlers.go` | Create, list, get, update, archive, reactivate and delete; `lookupWorkspaceForAuth`; the response, with `access`, `org_slug` and `hub_url` |
| `queue.go`, `clone.go`, `gitops.go`, `boot.go` | The clone queue and its job; the go-git clone and the archive's push; the package's injectable functions, set by `InitCloneQueue`; `EnsureWorkspaceRoot` |
| `clone_auth.go`, `credentials.go` | The origin and upstream credential resolvers; the `ls-remote` check of credentials at creation and the compensating delete |
| `sync_handler.go`, `sync_git.go`, `sync_hook.go`, `upstream.go` | Sync: the standard fetch and fast-forward, the reset to upstream, the `SyncExtension` hook point, the upstream fetch used after a carry-patch clone |
| `reclone_handler.go` | Reclone |
| `push_core.go`, `branch_push.go` | `pushToOrigin` and the branch push (see [Pushing to origin](#pushing-to-origin)) |
| `forceclose.go`, `audit_emit.go` | Closing a workspace's agent sessions on archive and delete; the `hub.workspace.*` and `hub.branch.push` events |
| `org_hook.go`, `slug.go` | `CreatePersonalOrg` |
| `authz.go`, `auth.go` | The workspace view of `wsaccess.AuthorizeWorkspace`; the scope helpers |

### The workspace row and its settings

A workspace is a row of `workspaces`, keyed by its `slug`. Besides its
identity (`git_url`, `branch`, `owner_id`, `org_id`, `display_name`,
`description`) it carries:

- **One `status`** (`pending`, `cloning`, `ready`, `failed`, `archived`, with
  a `CHECK`), with `head_sha` and `clone_error`. "Usable" is `ready`: the
  handlers that touch the clone check `status == ready`, those that only
  refuse an archived workspace check `status != archived`. A sync in progress
  is not stored; the workspace lock refuses a second one, and a crash leaves
  nothing behind. `sync_error`, `last_sync_at` and `upstream_head_sha` record
  the last sync ([ADR 08](adr/08-fold-the-workspace-status-columns.md)).
- **The mode**: `workspace_mode` (`standard` or `carry_patch`),
  `upstream_url` and `integration_branch`. The workspace core keeps the mode
  and validates it at creation; the carry-patch module fills it in (see
  [The carry-patch module](#the-carry-patch-module)).
- **`settings`**, one JSON document of the behaviour switches (`Settings`:
  five enumerations, four booleans, two URLs, each with a default). A key a
  stored document lacks gets its default on read, so a new setting needs no
  migration. A write is validated against `settingChoices` and
  `settingKinds`; a `PATCH` merges (`applySettings`: a key given takes the
  value, `null` returns it to the default).
  [ADR 07](adr/07-make-the-behaviour-switches-workspace-settings.md) records
  why.

The carry-patch module and the forge client read settings through a
`GetVariable(ownerType, ownerID, key)` function, by the upper-case names the
settings had as variables (`PATCH_BRANCH_SOURCE`, `FORGE_API_URL`, ...).
`SettingsReader` serves that function from the settings document: it maps the
name through `legacySettingKeys`, reads the row on every call (no cache, so a
change applies to the next read) and answers `*secrets.NotFoundError` for a
name that is not a setting, a missing workspace and an empty value, which the
readers take as "not set". `MigrateVariableSettings` moves workspace variables
with one of those names into the settings once, at start, and deletes them.

`hub_url` in a response is `<external_url>/git/<org slug>/<slug>.git`, null
without an external URL or an org. The listing reads the readable slugs with
`wsaccess.ReadableSlugs` and then the rows with `listWorkspacesBySlugs`, so the
membership predicate lives only in `wsaccess`.

### Creating and cloning

`handleCreateWorkspace`:

1. Binds the body and copies the credential fields (`git_pat`,
   `git_username`, `git_password`) out of the request struct at once, so no
   later log of it can carry them. Validates their shape (not empty, a PAT and
   a username exclude each other, username and password come together,
   credentials need an `https://` URL), the slug, the URLs, the branch names,
   the mode and the settings.
2. With credentials, checks them against the remote with an `ls-remote`
   (`validateCredentialsFn`, go-git's remote listing) before anything is
   written; a failure is logged and answered without the remote's text.
3. Resolves the org (an id or a slug the owner is a member of, else the
   owner's personal org), checks the slug is free and inserts the row with
   `status = pending`. A concurrent insert of the same slug is caught by the
   primary key.
4. Stores the credentials as workspace secrets (`GIT_PAT`, or `GIT_USERNAME`
   and `GIT_PASSWORD`). If that fails, `compensatingDeleteFn` deletes the row
   again with a plain `DELETE` (not `deleteWorkspace`, which would touch the
   secrets table that just failed); a failure of both is logged as critical.
5. Enqueues a clone job and emits `hub.workspace.create`.

The **clone job** (`processCloneJob`, on a worker of the
[clone queue](#the-clone-queue)) sets `cloning`; treats an existing
`<root>/<slug>` directory as already cloned and sets `ready` (this is how a
reactivated workspace reuses a clone the archive kept); resolves the origin
credential; clones `git_url` into `<root>/<slug>/trunk` with go-git
(`git.PlainCloneContext`, full history, a single branch when `branch` is set);
and sets `ready` with `head_sha`, or `failed` with `clone_error` after removing
the partial directory. For a `carry_patch` workspace,
`carryPatchPostCloneSetup` then adds the `upstream` remote and fetches it
(`FetchUpstream`, with the upstream credential), sets `rerere.enabled` and
`rerere.autoupdate`, and creates the integration branch at
`refs/remotes/upstream/HEAD` (at `HEAD` when the upstream fetch failed). Each
failure there is a logged warning; the workspace still becomes `ready`.

### Git credentials of a workspace

Every git operation against a remote, and the forge client, takes its
credential from the workspace's secrets, read on every use:

- **Origin** (`resolveCloneAuth`, exported as `ResolveCloneAuth`): `GIT_PAT`
  as HTTP Basic with the username `x-token-auth`; else `GIT_USERNAME` and
  `GIT_PASSWORD`; else none (a public repository). A username without a
  password is logged and treated as none.
- **Upstream** of a carry-patch fork (`resolveUpstreamAuth`, exported as
  `ResolveUpstreamAuthMethod`): `UPSTREAM_GIT_PAT`, else
  `UPSTREAM_GIT_USERNAME` and `UPSTREAM_GIT_PASSWORD`, else the origin's.

Only the workspace scope is read; a store failure other than "not found" is an
error, never "no credential". `cmd/af-hub` wraps the two resolvers in closures
and hands the same closures to the merge handler, the carry-patch module and
the forge client, so git and API access share one precedence chain
([ADR 04](adr/04-hub-is-the-workspaces-forge-client.md)).

### Sync

`handleSyncWorkspace` checks the scope, write access, `status == ready` and
the `sync_enabled` setting, then branches on the mode:

- **`carry_patch`**: when a `SyncExtension` is registered
  (`workspace.RegisterSyncExtension`, which `cmd/af-hub` calls with
  `carrypatch.NewCarryPatchSyncHook`), the handler hands the request to it.
  The extension takes the workspace lock itself and does the upstream fetch,
  the patch refresh, merge detection and the rebuild enqueue (see
  [The carry-patch sync](#the-carry-patch-sync)). It returns its fields as a
  map; the handler re-reads the row and answers the workspace object with
  those fields merged in.
- **`standard`**: under `wslock.TryLock` (409 `workspace_busy`), it resolves
  the origin credential, fetches `origin` with go-git
  (`defaultSyncFetchAndCompareFn`, retrying as an unshallow fetch when the
  clone is shallow) and compares `refs/remotes/origin/<branch>` with the local
  branch (the trunk's `HEAD` branch when `branch` is null). Up to date and a
  fast-forward record success (`recordSyncSuccess`: `head_sha`,
  `upstream_head_sha`, `last_sync_at`, `sync_error` cleared); a fast-forward
  first moves the ref and hard-resets the worktree
  (`defaultSyncUpdateLocalRefFn`; a failed reset is logged only). A local
  branch that is not an ancestor of origin's is a divergence (409), recorded
  in `sync_error`. `?reset_to_upstream=true` (`handleResetToUpstream`) moves
  the branch to origin's tip whatever the ancestry. A deferred function
  records `sync_error` for every exit that recorded no outcome (a panic, a
  cancelled context).

### Archive, reactivate, reclone and delete

- **Archive** (`handleArchiveWorkspace`). A `cloning` workspace is refused
  (409). A `ready` one is archived under `wslock.TryLock`, and refused with 409
  `workspace_busy` while `wslock.RebuildActive` says a rebuild has a worktree
  in the directory: it reads `HEAD` (a failure aborts the archive), pushes
  every local branch to origin (`archiveOpenAndPushFn`, see
  [Pushing to origin](#pushing-to-origin)) because branches pushed only to the
  hub's git server and the rebuilt integration branch exist nowhere else, and
  removes `<root>/<slug>` only when the push succeeded or had nothing to send;
  otherwise it keeps the directory and logs a warning. A `pending` or `failed`
  workspace just has its directory removed. Then `status = archived`, the
  workspace's active agent sessions are force-closed in DuckDB
  (`forceCloseWorkspaceSessions`, through `audit.Store.ForceCloseSessions`,
  with one `hub.session.force_closed` event per session and the session gauge), and
  `hub.workspace.archive` is emitted.
- **Reactivate** sets `pending` and enqueues a clone job; the job finds a kept
  directory and sets `ready` at once, or clones again.
- **Reclone** (`handleRecloneWorkspace`) refuses an archived, `pending` or
  `cloning` workspace, takes `wslock.TryLock` and checks `RebuildActive` like
  the archive, pushes like the archive (a failed push is logged and the
  reclone goes on), removes `<root>/<slug>` (a failure is 500 and nothing else
  changes), sets `pending` and clears `sync_error` and `upstream_head_sha` in
  one `UPDATE`, and enqueues a clone job.
- **Delete** (`handleDeleteWorkspace`) needs an archived workspace. It
  force-closes the sessions first, removes the directory if one is left, then
  runs `deleteWorkspaceRevoked`: one transaction that revokes the live tokens
  bound to the slug and deletes their bindings
  (`wsaccess.RevokeAndUnbindSlug`), deletes the row, and deletes the
  workspace's `patches`, workspace-scope `secrets` and `variables` rows. The
  sorted revoked token ids go into `hub.workspace.delete`. Audit data stays in
  DuckDB until retention removes data of workspaces that no longer exist (see
  [Retention](#retention)).

### Pushing to origin

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
text that leaves the route goes through `scrub.Text`, with the credential as a
value to redact; `classifyPushError` maps go-git's errors and origin's
sideband messages to the route's `forge_*` error types.

A push to hub's git server can move the trunk's checked-out branch while the
branch push holds the lock: receive-pack writes refs without the lock, and
`updateHeadSHA` (`internal/gitserver/handlers.go`) skips its worktree reset
when the lock is held, leaving that to the lock holder. The branch push
therefore notes the commit `HEAD` resolves to when it has opened the trunk
under the lock and, before it releases the lock, hard-resets the worktree to
`HEAD` when that commit has changed (`restoreMovedWorktree`). A failed reset
is logged and changes nothing else.

## The workspace lock

Every workspace has one non-bare clone, `<root>/<slug>/trunk`, shared by the
git server, sync, archive, reclone, merge jobs, batch rebase, the rebuild,
rollback, rerere forget, reset-to-origin, the patch branch resolver and the
branch push. Two of them at once would corrupt its worktree, index or refs, and
the job queue only serialises jobs that share a group key. `internal/wslock` is
the process-wide guard; the hub runs as one replica, so an in-process lock
suffices.

- **`Lock(slug)`** blocks until it holds the workspace's `sync.Mutex`. Jobs
  call it: a merge job for its whole run, a rebuild for its two locked phases.
- **`TryLock(slug)`** takes it without waiting. Request handlers call it and
  answer `409 workspace_busy` when it is held, rather than wait for a long job.
- **`BeginRebuild(slug)`** sets the workspace's rebuild-active flag and
  returns the function that clears it; **`RebuildActive(slug)`** reads it. The
  flag is independent of the mutex: a rebuild applies patches in a worktree
  under `<root>/<slug>/rebuild/` without holding the mutex, so the two
  handlers that delete the workspace directory, archive and reclone, check the
  flag after a successful `TryLock` and refuse for the whole rebuild. The
  rebuild sets the flag while it holds the mutex, which leaves no gap. Each
  flag is a distinct token, so a clearing function clears only the flag its
  call set, and a second `BeginRebuild` while one is set returns `ok = false`.

Who takes what:

| Operation | Lock | Rebuild-active flag |
|-----------|------|---------------------|
| Standard sync, carry-patch sync, batch rebase, rollback, rerere forget, reset-to-origin, branch push, patch branch resolution | `TryLock`, 409 `workspace_busy` | not read |
| Archive (of a `ready` workspace), reclone | `TryLock`, 409 `workspace_busy` | read: 409 `workspace_busy` for the whole rebuild |
| Merge job | `Lock`, for the whole job | not read |
| Rebuild job | `Lock` for its fetch phase and its final phase only | set from the fetch phase until the worktree is removed |
| Worktree reset after a push to the git server (`updateHeadSHA`) | `TryLock`; skipped when held, the holder restores the worktree | not read |

A rebuild between its two phases holds nothing, so the operations in the first
row proceed while it applies patches. The post-push hook (rebuild enqueue and
mirror), the forge routes and the sandbox routes take no lock.

## Job queues

The hub has two queues: an in-memory one for clones, which belong to a
request's lifetime, and a durable one for merge and rebuild jobs, which must
survive a restart and be inspected, cancelled and requeued.

### The clone queue

`workspace.JobQueue` (`queue.go`) is a buffered channel (`workers × 10`) and
`[workspace].workers` goroutines (default 4), started by `InitCloneQueue` on
`cmd/af-hub`'s context. Create, reactivate and reclone enqueue a `CloneJob`
(slug, `git_url`, `branch`); `Enqueue` blocks when the buffer is full. When the
context ends, the workers stop, a clone in progress is interrupted and queued
jobs are dropped. Nothing re-enqueues a clone at start: a workspace whose job
was lost keeps the status it had.

### The durable job queue

Merge and rebuild jobs run on aflib's `jobqueue`, a queue in SQLite tables that
`jobqueue.InitSchema`, `MigrateGroupKey` and `MigrateProgress` create; the hub
keeps no copy of it. `cmd/af-hub` creates one `jobqueue.Queue`, registers the
job types `merge` (`merge.RegisterHandler`) and `rebuild`
(`carrypatch.RegisterRebuildJob`) and starts its workers.

- **Enqueue** takes a type, a key, a group, a nonce, a payload and
  `submitted_by`, and inserts a row into the queue's `jobs` table. A job of the
  same type and key that is still queued or running makes the call a duplicate
  (`duplicate = true`, the existing id): that is how a second rebuild of a
  workspace or a second identical merge is refused or silently dropped. A
  repeated nonce returns the job it created before.
- **Groups serialise.** A worker claims a job only when no job with the same
  `group_key` (the `key` when the group is empty) is running, so at most one
  job of a group runs at a time.

| Job | Key | Group |
|-----|-----|-------|
| merge | `<slug>:<target>:<source>` | `<slug>:<target>` |
| rebuild | `<slug>` | `<slug>:<integration branch>` (`carrypatch.FormatGroupKey`) |

- **A handler** returns `(result, retryable, err)`. A retryable failure is
  retried with exponential backoff under the queue's default retry policy
  (both job types register with a nil policy: 2 s doubling up to 2 h, 20
  retries); a permanent one fails the job; a job out of retries is `dead_letter`, which the requeue routes put
  back. Only a `queued` job can be cancelled. How the policy reads
  `MaxRetries: 0` is recorded in [ADR 10](adr/10-follow-apikit-and-the-code-where-the-specs-disagree.md).
- **Progress.** A rebuild writes its per-patch results with
  `Queue.UpdateProgress` after each patch, so a running job's record shows
  them.

The REST routes of both job types read the queue's jobs and project them
(`merge.ProjectMergeJobResponse`, `carrypatch.jobToRebuildRecord`).

## Workspace access and bound tokens

Who may see and change a workspace is decided in one place, `internal/wsaccess`
([ADR 09](adr/09-decide-workspace-access-in-one-place.md)). The package imports
neither `internal/workspace` nor the carry-patch module, so both can use it: it
holds the access rule, the scope helpers, and the token workspace binding with
its table, lookup, guards and allowlist. No REST handler in `workspace`,
`carrypatch`, `merge`, `sandboxes`, `forge`, `boundtoken` or `secrets` compares
`owner_id` with the caller itself. `workspace.AuthorizeWorkspace` delegates to
it, and the carry-patch module reaches it through `cphost.Bind`.

### Scopes

Every hub resource has a `read` and a `write` scope, and `write` implies `read`
([ADR 06](adr/06-collapse-scopes-to-read-and-write.md)). Each package registers
its own (`GitPermissions`, `secrets.Permissions`, `MergePermissions`,
`CarryPatchPermissions`, `audit.Permissions`, `sandboxes.Permissions`,
`forge.Permissions`, and `WorkspacePermissions` for `workspaces` and
`patches`); `cmd/af-hub` passes them to `MountWorkspaceHandlers`, which hands
them to apikit's `MountHandlers`, so apikit refuses a PAT asking for any other.
`wsaccess.CanRead` and `CanWrite` (`scopes.go`) state the rule; API keys and
admin tokens pass every scope check. `MigrateScopes` rewrites stored PATs that
name a scope from before the collapse once, at start (`RenameScopes`).

### Workspace access

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
  need. It returns `WorkspaceAccess{Slug, OwnerID, OrgID, Status, Access}`.
  Any database error is 500. `AuthorizeWorkspace` is the same with the
  request-less site `{404, api}`; `lookupWorkspaceForAuth` uses `SiteOf(c, 404)`
  so its reports carry the method and route, then loads the full row.
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

Each route checks its need after the PAT scope check and before status and
precondition checks, so a PAT without the scope gets the scope answer, and a
member's write to an archived workspace is 403, not 400.

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
  and requires the workspace not to be archived; any failure is `false`,
  answered `403 workspace access denied`.
- `getAccessibleWorkspaces` (the list without a slug) keeps the slugs of
  `ReadableSlugs` that are not archived (`activeSlugs`), never nil;
  `readableWorkspaces` (`GET /audit`, `GET /events`) turns `ReadableSlugs` into
  the `allowedWorkspaces` set (nil for an admin token), which the query renders
  as `workspace IN (...)` and the SSE stream keeps from connect to close.
- `checkWorkspaceAccess` (ingestion) and the session-creator check of complete
  and usage report stay owner-only. `checkWorkspaceAccess` lets an admin token
  through without any check, reads the row for everyone else (404), applies the
  binding (404), refuses an archived workspace (409 `workspace_archived`) and
  a caller that is not the owner (403 `workspace_access_denied`), before
  anything is stored.

The git server decides through `wsaccess.AuthorizeWorkspaceAt` too (see
[Git server](#git-server)): members clone and fetch, and their pushes are
refused with a pkt-line 403.

### Token workspace bindings

A personal access token can be bound to one workspace. The binding is hub's own
record, the SQLite table `token_workspace_bindings` (`token_id TEXT PRIMARY
KEY`, `slug TEXT NOT NULL`, `created_at TEXT NOT NULL`, index
`idx_token_workspace_bindings_slug`), keyed by apikit's token id; a PAT without
a row is unbound. It has no foreign key to `workspaces`: archiving a workspace
leaves its bindings alone, and deleting it retires them explicitly
(`deleteWorkspace` calls `wsaccess.RevokeAndUnbindSlug` in its transaction: the
live tokens are revoked and the bindings deleted). The workspace schema
initialisation creates the table, and so do `MountWorkspaceHandlers` and
`MountGitHandlers`, before they register anything; each returns the error if it
cannot.

Everything lives in `internal/wsaccess`:

| Piece | What it does |
|-------|--------------|
| `LookupBinding`, `BoundSlug`, `DeniesWorkspace` (`binding.go`) | One primary-key lookup of a token id. `BoundSlug` only looks up a PAT that carries a `TokenID`, so API keys, admin tokens and requests without credentials never query. `DeniesWorkspace` is the rule itself: is this a PAT bound to a workspace other than this slug? A database error is returned, and every caller fails closed with it. |
| `BindingGuard` (`guard.go`) | Middleware on the API group, after apikit's auth middleware. For a bound PAT, a route whose template has `workspaces/:slug` passes only for its own slug (else `404 workspace not found`), before the handler runs and so before any scope, existence or status check. Any other route passes only if it is on the allowlist (else `403 token_bound`); a path that matches no route is refused the same way. A PAT without a `TokenID` gets the id from its Bearer header (`patTokenID`, which parses `<apikit.TokenPrefix>_pat_<id>_...`). A failed lookup is 500. |
| `APIKitRouteGuard` (`apikit_guard.go`) | Deny-only middleware on the root Echo instance for the routes apikit's `MountHandlers` registered, which never see API-group middleware added later. It runs after routing and before apikit authenticates. A Bearer PAT id with a binding row gets `403 token_bound` without the slug. It never sets `AuthInfo` and never looks at the secret. |
| `AllowBoundTokens`, `IsAllowed` (`allowlist.go`) | The routes a bound token may call although they are not workspace-scoped, marked with the `*echo.Route` values returned at registration: `GET /workspaces`, the six session routes, `GET /audit`, `GET /events`, `GET /sandbox-profiles`. |
| `AuthorizeWorkspace` | Applies the same rule after its existence and access checks (a caller with no access is refused before the binding is looked up), for the code paths that reach a workspace without the guard. |
| `binding_store.go` | The SQL on the table and on `pats` revocation: `InsertBinding`, `ListBoundTokens`, `RevokeToken`, `RevokeAndUnbindSlug`, `SweepBindings`. It lives here so that `internal/workspace` can use it. |
| `ReportBoundRefusal`, `SetRefusalObserver` (`observer.go`) | Every refusal of a bound token is reported once through the observer `boundtoken.Install` sets; `wsaccess` never imports `audit`. |

The wiring in `MountWorkspaceHandlers` is:

1. Create the `workspaces`, `patches` and binding tables.
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

### Issuing and retiring bound tokens

`internal/boundtoken` holds the two routes that mint and list bound tokens and
the refusal reporter. `cmd/af-hub/main.go` calls
`RegisterRoutes(server.APIGroup(), Config{DB, APIKit: server.Echo(), Audit})`
right after `MountWorkspaceHandlers` and registers the reporter with `Install`;
the maintenance loop runs `wsaccess.SweepBindings` at start and then hourly.

- **Minting by in-process delegation.** apikit's PAT handler is internal, so the
  mint handler serves `POST <mount>/user/tokens` through `Config.APIKit` with
  the caller's body, `Authorization` header and context, and records the answer
  in memory (`delegateMint`). `<mount>` is the matched route template without
  `/workspaces/:slug/tokens`. apikit's validation, expiry and
  privilege-escalation rules therefore apply unchanged, and any answer but 201
  is passed through. The inner request passes through the root middleware, so
  Prometheus counts it as a second request to `/user/tokens`. Before it
  delegates, the handler refuses a bound caller (`refuseBoundToken`), an admin
  token (no user to own the token), a caller without private access and an
  archived workspace.
- **Binding and rollback.** After apikit's 201, `wsaccess.InsertBinding` writes
  the row only while the workspace exists. Every failure after the 201 revokes
  the token first (`wsaccess.RevokeToken`, which sets `pats.revoked_at` as
  apikit's own revocation does), so no live unbound token is left behind. Hub
  never logs the token, apikit's answer or the `Authorization` header. A
  committed binding emits `hub.token.bind`.
- **Listing** (`list.go`) joins the bindings of the slug with `pats` and leaves
  out a binding whose token apikit no longer holds.
- **Retiring.** `deleteWorkspace` calls `wsaccess.RevokeAndUnbindSlug` in its
  transaction, before the workspace row goes. `SweepBindings` revokes the live
  tokens of bindings whose workspace is gone and deletes those bindings, and
  deletes bindings whose token is gone, in one transaction per pass; bindings
  of revoked or expired tokens whose workspace exists are kept, so the listing
  shows them.
- **Refusal reporting.** Every refusal point (both guards, and
  `DeniesWorkspace` with the `RefusalSite` its caller passes: the request's
  method and route, or none for `AuthorizeWorkspace`, which has no request)
  calls `wsaccess.ReportBoundRefusal` once per refused request. The observer is
  the `Reporter` (`reporter.go`), which emits `hub.token.bound_refused` at most
  once per token per minute with a `suppressed` count; it keeps an entry with
  its count until the token's next refusal.

## Secrets and variables

`internal/secrets` stores secrets and variables at three owner tiers (user,
org, workspace) and serves their routes.

### Storage

Two tables of the same shape, `secrets` and `variables` (`owner_type` in
`user`, `org`, `workspace`; `owner_id`, the user id, the org id or the
workspace slug; `key`; `value`; timestamps; primary key `(owner_type,
owner_id, key)`), created by `InitSchema`. A value is stored base64-encoded
(`EncodeValue`, `DecodeValue`), not encrypted. `Store` (`store.go`) validates
every write (`validation.go`: `ValidateKey`, letters, digits and underscores,
not starting with a digit, at most 255 characters; `ValidateValue`, at most
256 KiB), enforces 100 entries per owner (`MaxEntriesPerScope`), looks keys up
case-insensitively and keeps the stored casing. A secret's value leaves the
store only through `GetSecretValue`, which no route calls; the routes list
names and timestamps. Variables are returned with their values.

### The routes

The API is four operations (create, list, update, delete) on two resources at
three tiers, 24 routes served by one handler (`handle` in `handlers.go`) built
from a `tier`, a `resource` and an `operation`, plus
`GET /workspaces/:slug/vars/resolved`. Every handler checks in the same order:
a credential (401); the resource's scope for a PAT (403); the tier's owner (an
admin token on the user tier 403, since it has no user; an org the caller is
not a member of 404; the workspace access rule, with `NeedPrivate` for
workspace secrets and `NeedRead`/`NeedWrite` for workspace variables); then the
operation, which emits one `hub.secret.*` or `hub.variable.*` event per key
changed (`handler_audit_config.go`).

User and org deletion belong to apikit, which has no hooks, so secrets and
variables are deleted with a workspace only; rows of a deleted user or org stay
and nothing reaches them ([ADR 10](adr/10-follow-apikit-and-the-code-where-the-specs-disagree.md)).

### Resolution chains

Three readers merge the tiers, each in its own way:

- **Resolved variables** (`handleResolvedWorkspaceVars`, `Store.ResolveVariables`):
  the workspace owner's user tier, then the org tier, then the workspace tier,
  later tiers overwriting earlier ones by upper-cased key, each entry with its
  `origin`. The org tier is used only while the workspace owner is a member of
  the org (an admin token skips that check). A member of the org reads the
  owner's document.
- **Sandbox secrets** (`sandboxes.ResolutionChain`): the workspace, then the
  org while the owner is a member, then the owner's user scope; the first
  scope that has the name wins. The lease check at issue lists names only, and
  the gateway reads values through the same chain when it creates a sandbox
  (see [The lease record](#the-lease-record)).
- **Git credentials and the webhook secret** read the workspace scope only
  (see [Git credentials of a workspace](#git-credentials-of-a-workspace) and
  [A webhook delivery](#a-webhook-delivery)).

## Merge and rebase

`internal/merge` integrates a source branch into a target branch of a
workspace's trunk with rebase-then-fast-forward, as a job on the
[durable job queue](#the-durable-job-queue), and rebases a list of branches
synchronously.

| File | Responsibility |
|------|----------------|
| `api.go` | `RegisterMergeRoutes`: submit, list, get, cancel, requeue and `POST .../rebase`; the scope and access checks; `ProjectMergeJobResponse` |
| `merge.go` | `RegisterHandler`, `HandleMergeJob`, `EnqueueMergeJob` (key and group), `ResolveSubmittedBy` |
| `handler.go` | `Handler`: `PreCheck`, `FetchTarget`, `RebaseSource`, `UpdateTargetRef`, `DeleteSourceBranch`, `Finalize`; the rejection reasons |
| `rebase.go` | `RebaseBranch`, `BatchRebase` |
| `wire.go` | `DefaultFetchFunc` (go-git fetch of `origin`), `DefaultBranchChecker` |

The submit handler validates both names with `gitcmd.ValidateRefName`, needs
write access and a `ready` workspace, checks that the source branch exists in
the trunk (`BranchExists`, a `rev-parse`), and enqueues; a duplicate is 409
`duplicate_merge`.

### A merge job

`HandleMergeJob` holds `wslock.Lock` for the whole job and runs a
`gitcmd.GitRunner` in the trunk:

1. **Pre-check.** Source and target are resolved. Equal heads are
   `BranchNotReady`; a source that is an ancestor of the target is
   `AlreadyMerged`, an idempotent success; otherwise
   `git merge-tree --write-tree` is a dry run and a conflict is
   `WouldConflict` with its files. Rejections are permanent.
2. **Fetch** `origin` with the origin credential (retryable on failure), then
   record the target's head as `base_sha`.
3. **Rebase.** The runner clears what a crashed or cancelled job left
   (`ResetInProgressState`), checks out the source and rebases it onto the
   target; a conflict aborts the rebase and is a permanent `WouldConflict`, a
   cancellation aborts it with a fresh context.
4. **Fast-forward.** The target ref is set to the rebased source head
   (`merged_sha`).
5. **Clean up.** The target is checked out and the source branch deleted (a
   failure there is logged; the merge has succeeded). A deferred step always
   restores the checkout the trunk had, or the target when that was the
   source, or detaches `HEAD`.

The job records `hub.merge.complete` or, on any failure, `hub.merge.fail`. A
failure's error is the JSON of `mergeJobError` (reason and conflict files), so
the job record can show `conflict_files`. A merge changes only the hub's trunk;
nothing is pushed to origin.

### Batch rebase

`POST .../rebase` validates the names, needs a `ready` workspace and takes
`wslock.TryLock` (409 `workspace_busy`), then runs `BatchRebase` in the
request: each branch in turn is checked out and rebased onto the target ref,
and a conflict or error on one is recorded in its result without stopping the
others.

## Git server

The git server (`internal/gitserver`) provides smart HTTP git endpoints for
clone, fetch and push operations against workspace repositories. It is mounted
by `MountGitHandlers` at `/git/:org/:slug.git/` on the root Echo instance,
outside the API group, and uses HTTP Basic authentication backed by the hub's
credential store.

| File | Responsibility |
|------|----------------|
| `handlers.go` | `MountGitHandlers`, the body-limit exemption, `info/refs`, receive-pack, `updateHeadSHA`, the PAT's git scope, `RegisterPostPushHook` |
| `auth.go` | `GitAuthMiddleware`: the password of HTTP Basic as a hub credential |
| `resolver.go`, `authz.go` | `gitResolverMiddleware`: workspace lookup and the access rule |
| `uploadpack.go` | Fetch and clone through `git upload-pack --stateless-rpc` |
| `gitserver.go` | `WorkspaceLoader` (go-git's `server.Loader`) and `thinPackSafeStorer`, the storer that runs the hook checks on every ref write |
| `prereceive.go`, `protected_refs.go`, `accepted_refs.go` | The pre-receive hook point, the protected namespace, the accepted ref updates |
| `audit_config.go`, `audit_emit.go`, `permissions.go` | `SetAuditEmitter`, `hub.git.push`, `GitPermissions` |

apikit's body size limit applies to every route on the instance, and any real
push is larger. `gitBodyPassthrough`, an Echo `Pre` middleware, therefore
stashes the body of a `/git/` request in the context and clears its
`Content-Length` so the limit is skipped; the handlers read the stashed body
(`requestBody`).

### Request chain and authorization

Every git route runs `GitAuthMiddleware`, `requireDotGitSuffix`, then
`gitResolverMiddleware`:

- **Authenticate.** The Basic username is ignored; the password is classified
  by prefix (`af_pat_`, `af_key_`, `af_admin_`, else an API key in the
  `af_<key_id>_<secret>` form the OAuth login issues) and checked against
  apikit's tables (`pats`, `api_keys`, the admin token): not revoked, not
  expired, SHA-256 compared. A blocked user is refused. A missing credential is
  401 with `WWW-Authenticate: Basic realm="af-hub"`, an invalid one a bare 401.
  The result is an `apikit.AuthInfo`, as on the API.

Then the resolver:

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

The handler then checks the `service` value and the PAT's git scope
(`requireGitScope`; API keys and admin tokens have both), so a member's push is
refused before the scope check and before anything is unpacked, hooked,
written or audited.

### Fetch and clone

The ref advertisement for `service=git-upload-pack` and `POST
.../git-upload-pack` are served by the git CLI, `git upload-pack
--stateless-rpc`, in the trunk (`uploadpack.go`), with a 30-minute limit when
the request has no deadline. go-git's upload-pack server ignores `have` lines
and `done` and always answers with a whole pack, which breaks incremental
fetches; the git CLI gives the full negotiation, `side-band-64k`, shallow and
filter support, and gzip-encoded request bodies are accepted. The git CLI is a
run-time dependency of the hub anyway. `info/refs` for `git-receive-pack` is
advertised by go-git (`writeRefAdvertisement`).

### Receive-pack flow

The receive-pack handler (`handleReceivePack`) processes `git push` requests
with go-git's receive-pack session, through a per-request copy of the
`WorkspaceLoader` (`forRequest`) that carries the request context and the
actor, so that the storer it creates can consult the hook with them:

1. **Unpack.** The incoming packfile is decoded and its objects are written
   into the workspace trunk's object store via go-git's `ReceivePack`. An empty
   body is a no-op push: `unpack ok`, and `head_sha` is refreshed.
2. **Pre-receive hook.** A registered `PreReceiveHookFunc` is called once per
   ref update, after unpacking and before the hub writes the ref. The hook
   receives the request context, database handle, workspace slug, actor
   identity and the ref update (name, old hash, new hash). Returning a
   non-nil error rejects that single ref update; the error text becomes the
   per-ref status message the client sees (`ng <ref> <message>`). Other refs
   in the same push are processed independently.
3. **Ref write.** The trunk storer wrapper (`thinPackSafeStorer`) intercepts
   `SetReference`, `CheckAndSetReference` and `RemoveReference` to apply the
   [protected refs](#protected-refs) rule and then invoke the pre-receive hook
   before delegating to the underlying storer; it records the refs the hook
   rejected (`RejectedRefs`).
4. **Side effects.** `head_sha` is refreshed from the trunk HEAD after every
   push, including a push in which every ref was rejected (`updateHeadSHA`,
   which also hard-resets the worktree to `HEAD` under `wslock.TryLock` and
   skips the reset when the workspace is busy). Only accepted ref updates
   (`acceptedCommands`: report-status entry `ok`; for a client that did not
   request report-status, the updates the hook did not reject) drive the
   `hub.git.push` audit event and the post-push hook, which runs in a goroutine
   of its own, so the push answer never waits for it. Rejected refs are
   excluded from both.

The hooks are registered with `gitserver.RegisterPreReceiveHook(fn)` and
`RegisterPostPushHook(fn)`; with none registered every ref update is written.
The git server imports no carry-patch code; `cmd/af-hub/main.go` registers the
carry-patch module's hooks through `cphost.PreReceive` and `cphost.PostPush`,
which convert the module's hook types (`carrypatch.RefUpdate`,
`carrypatch.PreReceiveHook`, `carrypatch.PostPushHook`) to the git server's
(see [Carry-patch push control](#carry-patch-push-control)).

### Protected refs

`refs/hub/replaced/` holds the hub's backups of replaced patch branches. The
storer refuses a client push that creates, updates or deletes a ref under that
prefix (`isProtectedRef`, `errProtectedRef`) for every workspace and every
credential, before the pre-receive hook is consulted, whatever the mode. The
hub's own writes (the sync, the reset, the removal) go through the git CLI and
not through receive-pack, so they are unaffected. Fetching those refs works.

## The carry-patch module

Carry-patch is a Go module of its own, `github.com/agent-fox-dev/carrypatch`,
nested at `./carrypatch` and replaced in the hub's `go.mod`
([ADR 05](adr/05-move-carry-patch-to-its-own-module.md)). It holds everything
carry-patch: the patch routes and store, the reset-to-origin handler, the
rebuild job and its executor, rollback, the rebuild preview, rerere, the patch
status dashboard, the sync extension, the push control of patch branches, the
git runner (`carrypatch/gitcmd`, which `internal/merge` and
`internal/workspace` also use for merge and rebase) and the `afc` commands
`patch`, `rebuild`, `rerere` and `workspace patch-status` (`carrypatch/cli`,
which `internal/cli` adds to the root command). `make test`, `vet`, `lint`,
`tidy` and `doc` run over both modules.

The workspace core keeps the mode: the `workspace_mode`, `upstream_url` and
`integration_branch` fields and their validation at creation, the clone of a
carry-patch workspace with both remotes, the `patches` table DDL
(`workspace.InitSchema`), the cascade delete of patches with their workspace,
the refusal of the integration and patch branches in the branch push, and the
sync extension point: `workspace.RegisterSyncExtension` takes a
`SyncExtension` that `POST /workspaces/:slug/sync` calls for a `carry_patch`
workspace, and `main.go` registers `carrypatch.NewCarryPatchSyncHook(...)`.

### The host contract

The module imports nothing of the hub; the hub's packages are `internal`, so
it cannot. What it needs from its host it declares in `host.go`:

| Contract | What the module needs | What the hub supplies |
|----------|----------------------|-----------------------|
| `Host` (`SetHost`, once at start) | `Lock`, `TryLock`, `BeginRebuild`: the per-workspace locks and the rebuild guard; `Authorize`: the workspace access rule, answering a status and message or 0 | `cphost.Bind`: `wslock` and `wsaccess.AuthorizeWorkspace` |
| `Emitter`, `Event` | Audit emission; the module's events (`hub.patch.*`, `hub.rebuild.*`) are `Event`s without identity or time | `cphost.Emitter`, which wraps `audit.Emitter` and fills `audit.HubEvent` |
| `RefUpdate`, `PreReceiveHook`, `PostPushHook` | The git server's ref update and its two hook points | `cphost.PreReceive` and `cphost.PostPush` convert to `gitserver`'s types |
| `APIConfig`, `SyncAPIConfig`, `RebuildHandler`, `RecoveryService` | The database, the job queue, the workspace root, the settings reader (`GetVariable`), the credential resolvers and the fetch functions | `cmd/af-hub/main.go` passes the hub's values |

`internal/cphost` is the one place the hub and the module meet besides
`cmd/af-hub/main.go`. The tests that drive the module's hooks through the hub's
git server live there; the module's own tests run against doubles of the host
(in-process locks, the access rule over the test database).

### Inside the module

| File | Responsibility |
|------|----------------|
| `host.go` | The host contract; `loadWorkspaceRow`, the module's view of a workspace row |
| `api.go` | `APIConfig`, `RegisterRoutes`: rebuild submit, list, get, cancel, requeue, rollback, preview, rerere, patch status |
| `patch_handlers.go`, `patch_store.go`, `branch_resolve.go`, `branch_resolver.go`, `requestscope.go`, `validate_branch.go` | The patch routes (`RegisterPatchRoutes`) and their SQL; the branch resolver hook |
| `patch_reset_handler.go`, `recovery.go`, `reset.go`, `purge_refs.go` | Reset to origin, backup refs, the purge of expired soft-deleted patches |
| `carrypatch.go`, `rebuild_executor.go`, `stale_cleanup.go`, `upstream.go` | The rebuild job (`RegisterRebuildJob`, `HandleRebuildJob`), its worktrees and the cleanup at start; the upstream fetch |
| `sync_handlers.go`, `patch_refresh.go` | The sync extension, the patch branch refresh, merge detection |
| `push_control.go`, `push_hook.go`, `push_mirror.go` | The pre-receive hook, the post-push rebuild hook and the mirror |
| `rerere_handlers.go` | The rerere list and forget routes over `.git/rr-cache` |
| `wire.go` | The production adapters: `GitRunnerAdapter` over `gitcmd.GitRunner`, `NewGitRunnerFactory`, `SQLPatchStore`, `FormatGroupKey`, the fetch and push functions |

### The rebuild

A rebuild rebuilds the integration branch as the upstream base plus every
active patch branch in position order. `HandleRebuildJob`
(`rebuild_executor.go`) runs as a `rebuild` job and applies the patches in a
detached git worktree of its own, so the trunk's checkout is never moved and
the workspace lock is held only for two short phases; [ADR 02](adr/02-run-rebuilds-with-the-git-cli.md)
records why this uses the git CLI and not go-git.

1. **Resolve the upstream credential** (`ResolveAuth`); a failure is retried.
2. **Fetch phase**, under `host.Lock`: `host.BeginRebuild` sets the
   rebuild-active flag (a flag already set makes the job a retryable
   failure); `fetchUpstream` fetches `refs/heads/*` and `HEAD` of `upstream`
   into `refs/remotes/upstream/*` with go-git; the base is
   `refs/remotes/upstream/HEAD`, falling back to
   `refs/remotes/upstream/<workspace branch>` and then `FETCH_HEAD`; worktrees
   a crashed run left under `<root>/<slug>/rebuild/` are removed and pruned;
   `rerere.enabled` and `rerere.autoupdate` are set in the repository's config,
   which every worktree shares. The lock is released.
3. **Apply, without the lock.** `git worktree add --detach` at
   `<root>/<slug>/rebuild/<job id>`, at the base. Every patch to attempt is
   resolved to its tip SHA at `refs/heads/<branch>` (its `source_sha`); from
   here on only the SHAs are used. In position order, inside the worktree:
   `merged_upstream`, `disabled` and `deleted` patches are skipped (merged
   ones are collected for soft deletion); a branch that did not resolve is
   skipped as `branch_not_found`; the `rebase` strategy cherry-picks each
   commit of `git log --right-only --cherry-pick --no-merges <base>...<sha>`
   (an empty cherry-pick is skipped), the `merge` strategy runs
   `git merge --no-ff` with `Merge branch '<name>'`. A conflict runs
   `git rerere`; if conflicts remain, `fail_fast` (the default) ends the run
   and `continue` marks the patch `conflict`, resets the worktree to the
   pre-patch head and goes on. After each patch, `Queue.UpdateProgress` stores
   the results so far. A cancellation is never a skip, conflict or success:
   the run ends as a retryable failure.
4. **Final phase**, under `host.Lock` again: the integration branch's current
   head is recorded as `previous_integration_head_sha` (read here, so a
   rollback during the run is what gets recorded); `git update-ref` moves the
   branch to the worktree's head (`update-ref`, since `branch -f` refuses a
   checked-out branch); if the trunk has the integration branch checked out,
   its worktree is hard-reset to the new tip (`syncTrunkCheckout`);
   `merged_upstream` patches are soft-deleted and positions compacted; with
   the `rebuild_push_integration_branch` setting, the branch is force-pushed to
   origin with the origin credential (a failure is logged and emitted as
   `hub.rebuild.push_failed`; the rebuild still succeeds).
5. **After the lock**: the worktree is removed (a deferred removal covers
   every other exit path, after the lock is released), and `checkStaleInputs`
   compares each applied `source_sha` and the base with their current values.
   A moved patch tip enqueues one follow-up rebuild unless
   `auto_rebuild_after_push` is false, a moved base one unless
   `auto_rebuild_after_sync` is false (`submitted_by: system:stale-snapshot`,
   `hub.rebuild.followup`). The check runs after success and after a fail-fast
   conflict, not after a retryable failure (a retry takes a new snapshot).
   `hub.rebuild.complete` (or `hub.rebuild.fail`) is emitted, and the
   rebuild-active flag is cleared last.

`CleanupStaleRebuildWorktrees` runs at start, before the queue: for every
`<root>/*/rebuild` it removes the directory and runs `git worktree prune` in
the trunk. It logs what fails and never stops the start.

Rebuilds are enqueued by the submit route (409 `concurrent_rebuild` on a
duplicate), the sync, the post-push hook, reset-to-origin and the follow-up
check, all with key `<slug>` and group `<slug>:<integration branch>`, and all
capture the strategy and fail mode from the settings at enqueue time.

### Rollback and preview

- **Rollback** (`handleRollbackRebuild`) reads `previous_integration_head_sha`
  from a completed rebuild's result and, under `host.TryLock`, moves the
  integration branch back with `git branch -f`.
- **Preview** (`handleRebuildPreview`) takes no lock and does not fetch: it
  starts from `refs/remotes/upstream/HEAD` as the last fetch left it and runs
  `git merge-tree --write-tree` for each active or conflict patch in position
  order, each against the tree of the last clean result, so the prediction is
  cumulative and a conflict does not stop it. Nothing is written.

### The carry-patch sync

`runCarryPatchSync` (`sync_handlers.go`), called through the sync extension
after the workspace handler's checks, takes `host.TryLock` (409
`workspace_busy`) and then:

1. Reads `patch_branch_source` and `patch_divergence_policy`. In `origin`
   mode it resolves the origin credential before any fetch.
2. Resolves the upstream credential and fetches `upstream` (`refs/heads/*` and
   `HEAD` into `refs/remotes/upstream/*`, the refspecs of the rebuild). A
   failure is 502 and changes nothing.
3. In `origin` mode, fetches `origin` with pruning. A failure is 502
   `origin_fetch_failed`; the upstream refs already updated stay.
4. Resolves the new base, compares it with the stored `upstream_head_sha` and
   sets `force_push_detected` when the stored commit is not an ancestor of the
   new one.
5. In `origin` mode, refreshes every candidate patch (`active`, `conflict` or
   `disabled`, not the integration branch) against `refs/remotes/origin/<branch>`
   (`refreshPatchBranches`): create, fast-forward, or, on divergence, replace
   (saving the old tip as `refs/hub/replaced/<branch>`) or report, by policy.
   Ref writes are compare-and-swap (`git update-ref <ref> <new> <old>`,
   `casUpdateRef`); a moved branch that is checked out in the trunk has its
   worktree hard-reset. The outcomes are persisted in the patch rows
   (`origin_sync_state`, `origin_sha`, `origin_synced_at`); in `hub` mode the
   three columns are cleared for every patch. A ref-write failure stops the
   refresh: outcomes so far are persisted, a rebuild is enqueued for moved
   branches, events are emitted, and the sync answers 500 without writing
   `upstream_head_sha` or `last_sync_at`.
6. When upstream advanced or a patch branch moved, detects merged patches:
   `git merge-base --is-ancestor`, `git cherry` (no pending commits), and the
   `(#NNN)` of the patch's `upstream_pr_url` in recent upstream commit
   messages, as `squash_merge_detection` selects. Matches become
   `merged_upstream`.
7. Writes `last_sync_at` (and `upstream_head_sha` when upstream advanced);
   enqueues a rebuild when upstream advanced, an `active` or `conflict` patch
   branch moved, or a patch was newly merged, unless `auto_rebuild_after_sync`
   is false (a duplicate is ignored); emits `hub.patch.sync` and one
   `hub.patch.replace` per replaced branch.

### Carry-patch push control

`NewPreReceiveHook` (`push_control.go`) returns the hook that enforces a
single-writer model for registered patch branches. It acts only on a
`carry_patch` workspace, a `refs/heads/<name>` ref, a `<name>` that is a
`patches` row whose status is not `deleted`, and not the integration branch;
every other update passes. The behaviour depends on `patch_branch_source` and
`push_patches_to_origin`:

- **Reject mode** (`origin` source, forwarding off): the push is refused with
  a message naming `git_url` without its userinfo (`stripUserinfo`).
- **Forward mode** (`origin` source, forwarding on): the push is forwarded to
  the fork before the hub writes its ref (`forwardToOrigin`): the new commit is
  made resolvable under a temporary ref `refs/hub/forward/<branch>`, pushed
  (not forced) to `refs/heads/<branch>` of `origin` with the origin credential
  within 120 seconds, and the temporary ref is removed either way. A failure
  rejects the update, so the hub's ref is not written. A delete is refused
  without contacting the fork.
- **Mirror mode** (`hub` source, mirroring on): the push is accepted and then
  mirrored to the fork, best effort, by the post-push hook.

### Post-push hook

After the push, the post-push hook (`NewPostPushRebuildHook`, `push_hook.go`)
runs asynchronously and takes no lock. It loads the workspace; for a
`carry_patch` workspace it checks whether any accepted pushed branch is a
registered patch and, if `auto_rebuild_after_push` is not `false`, enqueues a
rebuild (`submitted_by: system:push-hook`; a duplicate is dropped). In hub mode
with mirroring enabled, it then force-pushes each accepted registered patch
branch to the fork (`mirrorBranches`), whether or not a rebuild was enqueued; a
failure is logged and emitted as `hub.patch.mirror_failed` with the error
stripped of URL userinfo. Its errors never reach the push's answer.

### Patch branch resolver hook

Registering a patch (`POST /api/v1/workspaces/:slug/patches`, single or
batch) resolves each branch name in the workspace trunk before the row is
inserted: `local` (`refs/heads/<name>` resolves to a commit), then
`origin_tracking` (`refs/remotes/origin/<name>` does, and the local branch is
created from it with a compare-and-swap `update-ref`), then, only with
`patch_branch_source=origin`, `origin_fetch` (a single-branch fetch of
`<name>` from the fork, then the same). The lock is taken only once a write is
needed, and the local check is repeated under it, since another request may
have created the branch meanwhile. A resolved branch ends up as a local
`refs/heads/<name>` ref. Elements with `skip_branch_check` are not
resolved.

The resolver is the `ResolveBranch` field of the module's `APIConfig`, a
`BranchResolverFunc`, `func(ctx, slug, branchName) (method, error)`:

- The patch handlers own the classified error, the HTTP error mapping (`400`
  not found, `409` workspace busy, `502` origin fetch or credentials, `500`
  anything unclassified) and the `branch_resolution` audit metadata. With no
  resolver configured a branch is accepted without resolution.
- `carrypatch.NewBranchResolverHook(...)` builds the resolver: ref checks
  through the git runner, a single-branch fetch from the fork, and the host's
  `TryLock` before anything is written. The hook releases the lock before it
  returns, so the database insert never runs under it.
- `cmd/af-hub/main.go` builds the resolver and passes it in `APIConfig`.

### Reading `patch_branch_source` once per request

`handleAddPatch` installs a request scope on the request context before it
dispatches to the single or batch path. The resolver hook reads
`patch_branch_source` through the module's `requestScoped`, so the first
resolution in a request reads the setting and every later one in the same
request (the remaining batch elements) reuses that value. A change to the
setting while a batch runs cannot mix modes within one request; it takes
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

### Reset to origin and backup refs

`RecoveryService` (`recovery.go`) is the patch routes' `Recoverer`. `RunReset`
(behind `POST .../patches/:id/reset-to-origin`) takes its `LockFunc`
(`wslock.TryLock`), resolves the origin credential, fetches the one branch
from the fork, and moves `refs/heads/<branch>` to the fork's tip with a
compare-and-swap `update-ref`: created, unchanged, fast-forwarded, or replaced
after saving the old tip as `refs/hub/replaced/<branch>` (overwriting an older
backup). A lost compare-and-swap is `ref_changed`; a moved branch that is
checked out has its worktree hard-reset (a failure is logged only). In
`origin` mode the patch is recorded `in_sync`; a moved `active` or `conflict`
patch enqueues a rebuild unless `auto_rebuild_after_sync` is false.
`ReadReplacedSHA` gives the patch representation its `replaced_sha`, and
`RemoveBackup` deletes the backup when a patch is removed.

### The patch table

The `patches` table (`workspace.InitSchema`; `id`, `workspace_slug`,
`branch_name`, `position`, `status`, `conflict_files`, `upstream_pr_url`,
`description`, `deleted_at`, the three `origin_*` columns, timestamps) has
`UNIQUE(workspace_slug, branch_name)` and `UNIQUE(workspace_slug, position)`.
To reassign positions without breaking the second constraint midway,
`patch_store.go` first parks every row of the workspace at a negative position
(`parkPositions`, `-rowid`) and then writes the new ones, in one transaction
(insert at a position, delete and compact, reorder). A soft-deleted patch
(`status = deleted`, `deleted_at`) stays parked and can be restored;
`PurgeExpiredDeletedPatchesWithRefs` removes those older than seven days with
their backup refs, but nothing in the hub calls it.

### The git runner

`carrypatch/gitcmd` runs the git CLI for the rebuild, the carry-patch helpers,
merge jobs, batch rebase and the carry-patch clone setup. `gitcmd.New` checks
the git version (at least 2.38, for `merge-tree --write-tree`). Every call
runs with a hardened environment: the repository-redirecting variables
(`GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE`, `GIT_CONFIG_PARAMETERS`, ...)
are dropped; `LC_ALL=C`, `GIT_EDITOR=true`, `GIT_TERMINAL_PROMPT=0` and
`GIT_CONFIG_NOSYSTEM=1` are set; the committer identity defaults to
`af-hub <af-hub@localhost>` unless the `GIT_AUTHOR_*`/`GIT_COMMITTER_*`
variables say otherwise. Each invocation runs in its own process group, which
is killed as a whole on cancellation, and a call without a deadline gets
`DefaultTimeout` (10 minutes), so a hung git cannot hold a workspace lock
forever. A failure is a `*GitError` (arguments with URL userinfo redacted,
exit code, stderr); conflicts are typed (`MergeConflictError`,
`RebaseConflictError`, `CherryPickConflictError`, `MergeNoFFConflictError`)
with their files.

## Sandboxes

`internal/sandboxes` owns the deployment's sandbox definition: where sandboxes
run and what shape they take. It builds one AgentKit sandbox provider from the
`[sandbox]` section of `config.toml`, checks every profile against what that
provider can honour, and publishes the profiles read-only. The lease record
(below) is built on top of it, the sandbox gateway (below) is what creates a
sandbox for a lease, and the expiry machinery (below) puts a time limit on every
lease and cleans up after a shutdown or a crash. The provider is either a container
engine on the hub's own host or a remote `agentkit-gateway` (below). The
decisions behind this design are recorded in
[ADR 03](adr/03-hub-is-the-sandbox-gateway.md).

| File | Responsibility |
|------|----------------|
| `config.go` | `LoadConfig`: reads the `[sandbox]` table from the file apikit loads, expands `${NAME}` in the section only, refuses unknown keys with their full path. |
| `validate.go` | `Validate`: every static rule for the deployment keys and profiles, collected into one joined error. Uses `secrets.ValidateKey` for secret and env names. |
| `provider.go` | `ProviderFactory` (the shape of `podman.New`), `PodmanFactory`, `BuildProvider`; `GatewayFactory` (the shape of `gateway.Dial`), `DialGateway`, `RemoteClient` and the `StartOption` `WithGatewayFactory`. |
| `redact.go` | `RedactedString`: the type the validated `gateway_token` is kept in, which prints as `[redacted]` under every verb, `slog` and JSON. |
| `service.go` | `Service`, `Start`, `New`: builds the provider (`startRemote` for provider `gateway`), probes it once, checks `network_modes` and turns each profile into a `sandbox.Spec` that `sandbox.CheckSpec` must accept. Keeps the provider, the capability snapshot of a remote gateway, the scrub values and the validated profiles in memory; `Close` closes the remote client. |
| `errors_remote.go` | `isNotFound` and `isRemoteRefusal`, and `Service.remoteFailure`: a remote not-found counts as destroyed, and a remote that refuses the hub's token is answered `Unavailable`. |
| `startup.go` | `Init`: the one call `cmd/af-hub` makes. Loads, validates, starts and logs. |
| `permissions.go`, `auth.go` | The `sandboxes:read` and `sandboxes:write` scopes and the `canRead`/`canWrite` helpers over `wsaccess.IsPAT` and `HasScope`. |
| `handlers.go` | `RegisterRoutes` and `GET /api/v1/sandbox-profiles`, built from the in-memory profiles. |
| `lease_schema.go`, `lease_migrate.go`, `lease.go`, `store_query.go`, `lease_transitions.go` | The `sandbox_leases` table (`InitSchema`), `MigrateLeaseExpiry` (adds `lease_expires_at` to a table that lacks it), the `Lease` type and the `LeaseStore`: insert, read, list, `LookupByToken`, and the conditional transitions `MarkActive`, `FailLease`, `ExpireLease`, `DestroyLease`, `EndLease`. |
| `lease_service.go`, `lease_validate.go`, `token.go` | `LeaseService.Issue`: request validation, the required-secret check, id and token generation, the capacity count and the insert. |
| `secretcheck.go` | `ResolutionChain` and `CheckRequiredSecrets`: which scopes a workspace's secrets are searched in, and a check that lists names only. |
| `routes.go`, `handlers_issue.go`, `handlers_read.go`, `handlers_delete.go`, `delete_destroy.go`, `render.go` | `RegisterLeaseRoutes`: the five lease routes, the checks they share (`begin`, `authorize`), the lease representation (with `gateway_url`) and how `DELETE` ends a lease and its sandbox. |
| `gateway.go` | `NewGateway`: loads the key pair, binds `listen`, builds the `gatewayd.Server`, the backend and the sweeper without serving; `Gateway.Run` (start-up work, then serving), `Gateway.Shutdown`, `Gateway.DestroyBox`, and `List` and `Destroy`, which make the gateway the `Boxes` of the sweep. |
| `gateway_auth.go` | `NewLeaseAuthenticator`: the function gatewayd calls for every RPC to turn a lease token into a principal. |
| `backend.go`, `create_spec.go`, `secret_resolve.go`, `handle.go`, `events.go` | The wrapping backend, the `sandbox.Provider` hub gives gatewayd: `Create` and its outcomes, the spec built from the lease, the secrets read when the sandbox is created, the handle around the provider's sandbox and the audit events. Provider errors are scrubbed with `internal/scrub`. |
| `expire.go`, `metrics.go` | The `Boxes` seam, and the `ender`: the expiry path, `endDestroyed` for shutdown and restart, and the events they emit; the recorder of the three sandbox metrics. |
| `sweeper.go`, `reaper.go` | `Sweeper.Sweep` and its steps; restart recovery (`RecoverActive`), the reaper loop (`StartReaper`, `RunStartup`) and `Sweeper.Shutdown`. |

### Start-up order

`cmd/af-hub` calls `sandboxes.Init` immediately after `apikit.Bootstrap` and
the scope migration, and before the workspace root, the job queues, the workers
and the routes are set up:

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
   on every start, so PATs can carry the sandbox scopes on a hub without
   sandboxes, and `RegisterRoutes` mounts the listing with the service, or
   with a nil service when the feature is off. A nil service answers
   `404 sandboxes_disabled`.
6. Later, after the lease table and `MigrateLeaseExpiry`, the audit emitter and
   the metrics exist and before `server.Start()`, `startSandboxes` calls
   `NewGateway`, which binds the gateway's listener and builds the server, and
   `Gateway.Run`, which ends the leases a crash left `active`, runs one sweep,
   starts the reaper and only then serves the listener. The lease service is
   built with the gateway's URL and the gateway itself, which it destroys boxes
   through. A key pair that cannot be loaded or an address that cannot be bound
   is fatal, like any other failure of the section; what fails in `Run` is
   logged and is not. A hub without sandboxes binds nothing and runs none of it.
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
at every start, after the secrets and forge tables, whether or not sandboxes are
enabled, so switching them off and on again keeps the history. Its `state`
column allows all five states (`leased`, `active`, `destroyed`, `expired`,
`failed`) because SQLite cannot alter a `CHECK`. The column `lease_expires_at`, the
deadline for a `Create`, is in the table for a new database and added by
`MigrateLeaseExpiry` to one that lacks it (checked with `PRAGMA table_info`, so a
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
and `DestroyLease` are thin callers of it, and no other production code clears a
token hash. Clearing the hash is what revokes a token, so ending a lease and
revoking its token are the same act. `MarkActive` (leased to active, recording
the sandbox id) leaves the token alone.

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
  from the lease alone (`buildSpec`: the caller's working directory and labels
  are the only things read from the caller's spec, with every `hub.` label key
  dropped and the lease's labels, `hub.lease` and `hub.workspace` added; the TTL
  is the lease's time left in whole seconds), reads the secrets
  (`resolveSecrets`) and asks the provider. While the hub is shutting down it
  refuses with `Unavailable` before all of this. A lease whose deadline has
  passed (`lease_expires_at`, or `expires_at` for an old row) is refused with
  `FailedPrecondition` naming `expired` (gatewayd hands any backend failure that
  is no refusal to the client as `Unavailable`, with the message), without a
  provider call, and ended as `expired` in the same call through the expiry
  path. The outcome is recorded by the conditional transitions above, and every
  `Create` that gets as far as making a box is counted once on
  `afhub_sandbox_creates_total`. A box that arrives for a lease that a `DELETE`
  or a sweep has ended in the meantime is destroyed on the spot. A failure of
  the secrets store leaves the lease `leased`, so the call can be repeated.
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
  restarted. A provider that fails or does not answer within 30 seconds leaves
  the lease unchanged (502 `sandbox_provider_error`).
- **Reap.** The backend's `Reap` runs the sweep (below) for the gateway's own
  periodic call, the one with no principal; with a principal, a lease holder's
  call, it does nothing. It returns nothing: the sweep logs what fails in it.
- **Scrubbing.** Provider error text is scrubbed (`scrub.Text`) before it is
  stored on a lease, put in an event, logged or returned: every secret value and
  fixed environment value becomes `[redacted]`, URL user information is removed,
  and only then is the text cut to 1024 bytes on a UTF-8 boundary.
- **Events** are emitted in one place (`events.go`; `delete_destroy.go` for
  `DELETE`; `expire.go` for the endings by time, shutdown and restart):
  `hub.sandbox.create`, `hub.sandbox.failed`, `hub.sandbox.destroy` and
  `hub.sandbox.expire`. A nil emitter skips emission and an emit error is logged and
  changes no outcome.

How the gateway works around what AgentKit lacks (no provider `List` or
`Destroy`, the codes gatewayd answers with, the handle's `Close`) is recorded in
ADR 03.

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
  refused. `Start` takes the factory as a `StartOption`, so the Podman path needs
  no option.
- **One shared token.** Every lease's sandbox is made on the remote under the hub's
  one `gateway_token`. A lease token never leaves the hub, and the remote sees only
  the hub's. A lease's `gateway_url` is the hub's own gateway.
- **The capability snapshot.** `startRemote` reads the client's `Capabilities()` once
  and keeps it in `Service.caps`. `Service.Capabilities` returns a copy of it, and
  the backend (the spec of a box, `Capabilities`) and the gateway (its network
  modes) use that and never the client, so a remote that is down delays and empties
  nothing, and a change on the remote waits for a restart. A provider that is not
  remote is asked every time.
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
sandbox's own `agentkit-sandbox-id` to the next gateway, which the `agentkit-go`
checkout must support; with a checkout that does not, every `Create` through the
hub fails at the agent handshake. The reasons for the factory's signature, the
probe, the snapshot, the destroy by id and the lease that a sweep ends while the
remote is down are recorded in ADR 03.

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

The `Boxes` seam in place of the provider's `List` and `Destroy`, and where the
lifecycle methods live, are recorded in ADR 03.

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
remote gateway, `*gateway.Provider`, has both, and its probe is one `List` (ADR
03).

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
relays them, and never acts on one or calls the forge for it. No forge route takes the
workspace lock or needs a clone.

The forge client is the `issuex` package of the sibling module
[aflib](https://github.com/agent-fox-dev/aflib), expected at `../aflib` and replaced
in `go.mod` like `../apikit` and `../agentkit-go`; the hub image build and the CI
workflows supply the checkout in the same way. Only the packages under
`internal/forge` import `issuex`. `afc` stays a static `CGO_ENABLED=0` binary:
neither `cmd/afc` nor `internal/cli` imports `internal/forge` in a build (a test
file of `internal/cli` imports it to run `afc` against the routes).

| File | Responsibility |
|------|----------------|
| `permissions.go`, `auth.go` | The `forge:read` and `forge:write` scopes (`Permissions`, appended to `extraPerms` in `cmd/af-hub/main.go`) and `canReadForge`/`canWriteForge`; `forge:write` implies `forge:read`. |
| `config.go` | `Config`: the database, the two credential resolvers (`workspace.ResolveCloneAuth` and `ResolveUpstreamAuthMethod`, as closures in `main.go`), `GetVariable` (the settings reader), an optional base `Transport`, an optional `NewClient`, the audit emitter `Audit` that `main.go` builds with `audit.NewEmitter`, the secrets store `Secrets` (the interface `SecretStore`, which `*secrets.Store` satisfies) and `ExternalURL` (`server.external_url`); and `webhookURL`, the ingress URL of a workspace. One value serves `RegisterRoutes` and `MountWebhookIngress`. |
| `routes.go` | `RegisterRoutes`: the twenty-two routes under `/workspaces/:slug` (ten reads, nine writes, and the webhook secret and delivery routes), and `authorize`, the checks they share, in order: 401, 403 (the scope of the verb), 404, 400 for a write on an archived workspace, then the workspace's `git_url`, `workspace_mode` and `upstream_url`. |
| `target.go`, `params.go` | `resolveTarget` (`?target=`), `parseNumber` (`:n`) and `parseIssueFilter` (the query of `GET .../issues`): the refusals that come before a client is built. |
| `client.go` | `buildClient`: a fresh issuex client for one request, from the remote URL, the API URL setting and the credential; `credentialOf` (`pat`, `basic`, `none`); `parseRemote` and `redactRemote`; `describeRemote` for the diagnostic. |
| `httpclient.go` | The one `*http.Client` of the process: `forgeTransport` (user agent, no environment token for a workspace without a credential), the redirect policy, and `callDeadline` (30 seconds). |
| `handlers_read.go`, `handlers_forge.go` | `serve`, the handler the nine read routes share, and the routes themselves (`prepare*`, `numbered`, `valueOf`, `listOf`); `diagnostic`, `GET .../forge`. |
| `errors.go` | `mapForgeError`: every issuex and transport failure to one status and `error_type`, with `Retry-After`; `mapCall`, which logs a 5xx or 429 answer once; `failCall`. Messages go through `scrub.Text`: the token and URL userinfo out, cut to 1024 bytes. |
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
| `retention.go` | `PruneDeliveries`: rows older than 7 days and rows of deleted workspaces, which the maintenance loop of `cmd/af-hub` runs at start and every hour. |

### A forge read

1. `authorize` applies the checks in order and reads the workspace's remotes.
2. `resolveTarget` and the route's own parameters are checked.
3. `buildClient` reads the remote URL (`git_url` for `origin`, `upstream_url` for
   `upstream`), finds the repository, reads `forge_api_url` (`upstream_forge_api_url`
   for `upstream`, with no fallback), resolves the credential (the origin or the
   upstream resolver) and calls `issuex.NewWithOptions` with the shared HTTP
   client. Nothing is cached, so a rotated secret or a changed setting applies to
   the next call. The credential's kind is `pat` when the resolver returns the
   username `x-token-auth`, `basic` (the password sent as the token) for any other
   username, `none` without a credential.
4. The call runs under a context that carries the workspace's token and a 30-second
   deadline. The transport adds `User-Agent: af-hub/<apikit.Version>`, removes the
   token headers when the workspace has no credential (so the `GITHUB_TOKEN`,
   `GH_TOKEN` or `GITLAB_TOKEN` of the hub's environment is never sent), follows
   the environment's proxy settings, and refuses a redirect to another host, port
   or scheme.
5. The result is answered as issuex encodes it. A failure goes through `failCall`.

The diagnostic (`GET .../forge`) takes steps 1 and 3 up to the point of the client:
it sends nothing.

### A forge write

1. `write` applies the checks of `authorize` for the write scope, then resolves the
   target, reads the body and lets the route check it (`prepare`): all 400s.
2. `buildClient` builds the client as for a read, but gives it the API URL explicitly
   (the setting's, else the hub's statement for the host), so that the requests of
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
   without the secret answer the same 404. The workspace's status does not matter.
2. The scheme is chosen by header (`X-Hub-Signature-256` is GitHub's, else `X-Gitlab-Token`
   is GitLab's, else 401); a GitLab token is compared in constant time before the body is
   read, a GitHub signature after, over the raw bytes (at most 1 MiB), with `hmac.Equal`.
   An unverified request writes nothing and logs the slug and the scheme only.
3. The delivery id and the event name come from the headers of the scheme
   (`X-GitHub-Delivery`; GitLab's `Idempotency-Key`, else `X-Gitlab-Event-UUID`).
4. `reduceDelivery` turns the payload into an outcome, a target (`origin` or `upstream`,
   from the payload's repository path and the workspace's `git_url` and, on a `carry_patch`
   workspace, `upstream_url`) and zero or more `audit.HubEvent`s of actor type `forge`.
5. One `INSERT … ON CONFLICT DO NOTHING` into `forge_deliveries`, keyed by the workspace
   and the delivery id, decides: no row inserted is a duplicate, answered and nothing
   else; a failing insert is a 500 with nothing emitted, so that the forge's redelivery
   is processed in full.
6. The 202 answer is written, with its length, and flushed. Only then are the events handed
   to `Config.Audit` in order, on `context.WithoutCancel(request context)`: the broadcast
   emitter stores them in `hub_audit_events` and puts them on the SSE stream, where
   `GET /events` and `GET /audit` find them. An emit error is logged and changes nothing.

`forge.InitSchema` creates `forge_deliveries` at every start, and the maintenance loop of
`main.go` runs `forge.PruneDeliveries` (rows older than 7 days and rows of deleted workspaces)
once at start and then every hour; nothing else prunes the table. The delivery log is SQLite,
in the store where workspaces and secrets live, so the insert that deduplicates is atomic; the
audit store (DuckDB) stays the place of the events. The decisions behind this are recorded in
ADR 04.

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

These are findings for aflib; ADR 04 records them.

### What issuex does not give the hub

- It has no accessor for the forge or the API URL of a client. The diagnostic takes
  the forge from `issuex.DetectForge` and states the default API URL itself
  (`defaultAPIURL`).
- `issuex.ParseRemote` reads only hosts whose names contain `github` or `gitlab`, so
  `parseRemote` splits the URL of any other host itself and lets issuex try to classify
  it. issuex classifies by name only: a host that names neither forge is told apart
  only by an API URL that does. issuex also reads `GITHUB_API_URL` and
  `GITLAB_API_URL` from the hub's environment.
- A client given no token uses the one in the hub's own environment, and the
  repository it works on, if it is given none, is that of the working directory's git
  remote. The hub always gives both (the transport removes the headers; the parsed
  repository is always in the options).
- `ListIssues` and `ListComments` return a struct with an `incomplete` or `truncated`
  flag; the routes answer the bare array and drop it.

## Audit, sessions and events

`internal/audit` keeps everything the hub records about what happened: the hub's
own events, the telemetry agents send, agent sessions with their token usage and
cost, and the Prometheus metrics. It is the only package that touches DuckDB.

| File | Responsibility |
|------|----------------|
| `db.go`, `schema.go` | `OpenDB` (creates the parent directory, opens and pings DuckDB) and `InitSchema` |
| `types.go`, `validate.go`, `severity.go`, `cursor.go` | The request and row types, run id and timestamp validation, default severities, opaque cursors |
| `store.go`, `store_audit.go` | `Store` over DuckDB: hub events, sessions, usage, cost, force-close; agent ingestion and the per-run queries |
| `emitter.go` | `Emitter`, `NewEmitter` |
| `sse.go` | `SSEManager` |
| `auth.go`, `permissions.go` | The scope helpers, the workspace checks (see [Workspace access](#workspace-access)), the `audit` and `sessions` scopes |
| `routes.go`, `handlers.go`, `handlers_audit.go` | Session routes; ingestion and per-run read routes |
| `query_routes.go`, `handlers_unified_query.go` | `GET /audit`, the transcript, `GET /events` |
| `retention.go` | `RunRetention` and its steps |
| `metrics.go` | `Metrics`: the Prometheus registry, the HTTP middleware, `/metrics` |

### The audit database

DuckDB is an embedded, column-oriented database, chosen for the append-heavy,
query-by-range audit data; it is why the hub needs CGo. `cmd/af-hub` opens it at
`AF_AUDIT_DB_PATH` (default: `audit.duckdb` in the directory of the SQLite file)
and `InitSchema` creates the nine tables: `hub_audit_events` (the hub's own
events), `agent_audit_events`, `session_outcomes`, `tool_calls`, `tool_errors`,
`agent_traces` and `postmortems` (agent telemetry, keyed by workspace and run
id), and `agent_sessions` and `token_usage`. The SQLite database is consulted
for workspace ownership and status; the two databases share no transaction.

### Hub events and the emitter

Every package that records what it did calls `audit.Emitter.Emit` with an
`audit.HubEvent` (type, actor id and type, resource type and id, action,
workspace, metadata). The emitter `NewEmitter` builds gives the event a UUID and
a timestamp (`RFC3339Nano`, so hub and agent events interleave correctly),
inserts it into `hub_audit_events`, counts it on `afhub_audit_events_total` and,
when an SSE manager is wired, broadcasts it. An insert failure is logged and
swallowed: an audit failure never fails the operation it records, and an event
that was not stored is not broadcast. Packages that cannot import `audit` get an
adapter: the carry-patch module through `cphost.Emitter`, `wsaccess` through the
refusal observer that `boundtoken.Install` sets.

### The event stream

`SSEManager` (`sse.go`) serves `GET /events`. It runs a broadcaster goroutine
fed by a channel of 512 events, a heartbeat goroutine and a mutex-protected
registry of connections, each with a buffer of 256 events. `Register` refuses a
connection over the limit (`AF_SSE_MAX_CONNECTIONS`, default 100), which the
handler answers 503. The fan-out applies each connection's filters (workspace,
category, and `allowedWorkspaces`, the caller's readable set computed once when
the stream opens; events without a workspace go only to unrestricted, admin,
connections); a full buffer drops its oldest event. Every 30 seconds
(`HeartbeatInterval`) a heartbeat goes to every connection and connections that
have not read for 60 seconds (`StaleConnectionTimeout`) are closed. The stream
carries hub events only; agent events are read through the query.

### Agent ingestion and the audit query

The ingestion routes under `/workspaces/:slug/runs/:run_id/` check the scope,
the run id format and `checkWorkspaceAccess` (owner only; see
[Workspace access](#workspace-access)) before anything is stored, then insert
with `INSERT OR IGNORE`, so a repeated event is a duplicate (200, not 201).
Every insert has a write timeout (`AF_AUDIT_WRITE_TIMEOUT_MS`, default 2000 ms)
and a timeout is its own error. The batch routes report per item. The per-run
reads apply `requireWorkspaceRead`.

`GET /audit` (`handleAuditQuery`) merges `hub_audit_events` and
`agent_audit_events` with a `UNION ALL` of two subqueries
(`buildHubSubQuery`, `buildAgentSubQuery`), each with the filters that apply
to it (a hub-only filter such as `actor_id` excludes the agent side) and the
caller's `allowedWorkspaces` as `workspace IN (...)`, ordered by timestamp and
id and paged with an opaque cursor of the last timestamp and id. The transcript
(`queryTranscriptMessages`) reconstructs a run's conversation from its agent
events.

### Sessions, usage and cost

Agent sessions live in `agent_sessions`, their token reports in `token_usage`.
Opening a session needs write access to its workspace and is idempotent on the
session id; completing it and reporting usage are the creator's (or an admin
token's), and a closed session takes no usage. The reads (`hasWorkspaceAccess`,
`getAccessibleWorkspaces`) need read access and a workspace that is not
archived. The cost route aggregates `token_usage` per workspace over a period,
with totals and a breakdown. `afhub_agent_sessions_active` and
`afhub_agent_tokens_total` follow the session writes. Archiving or deleting a
workspace force-closes its active sessions (`Store.ForceCloseSessions`, called
from `internal/workspace`).

### Retention

`RunRetention` runs from the maintenance loop with the limits of
`LoadRetentionConfigFromEnv` (`AF_AUDIT_MAX_AGE_DAYS`, `AF_AUDIT_MAX_RUNS`,
`AF_TRACE_MAX_AGE_DAYS`, `AF_SESSION_MAX_AGE_DAYS`,
`AF_SESSION_MAX_ACTIVE_AGE_DAYS`, `AF_POSTMORTEM_MAX_AGE_DAYS`,
`AF_AUDIT_ORPHAN_RETENTION_DAYS`; a value that is not a positive integer falls
back to its default with a warning). In order: aged agent records; the oldest
runs beyond the maximum number of runs; aged hub events; active sessions older
than the active limit force-closed as `timeout`; aged terminal sessions;
`token_usage` of sessions that are gone; aged traces; aged postmortems; the data
of workspaces that no longer exist in SQLite, after the orphan grace period;
and the recalibration of the session gauge and `afhub_audit_table_rows`. A
failed step is counted on `afhub_retention_errors_total` and the others run;
`afhub_retention_last_run_timestamp_seconds` is set only when every step
succeeded.

### Metrics

`audit.NewMetrics` builds a Prometheus registry of its own with every `afhub_*`
collector: the HTTP request counter and latency histogram (labelled with the
Echo route template, not the path, to keep the cardinality bounded), the session
gauge and token counter, the audit event counter, the SSE gauge, the job queue
depth gauge, the retention collectors, the audit table rows gauge, and the three
sandbox collectors. The label sets are pre-initialised with empty placeholders so
every metric is listed from start. `SetMetrics` installs the instance as a
package-level sink for the SSE gauge and the event counter; the other users get
it passed in. `cmd/af-hub` adds `PrometheusMiddleware` to the root Echo instance
and serves `MetricsHandler` at `GET /metrics`, outside the API group and without
authentication.

## Scrubbing

`internal/scrub` makes text that may carry a credential safe to log, store in an
event or return. The forge client, the sandboxes, the branch push and the
webhook ingress use it; the carry-patch module, which cannot import it, strips URL
userinfo with a helper of its own, and `gitcmd` redacts the URLs in its
arguments itself.

- **`Text(text, values...)`** replaces every non-empty value with `[redacted]`
  (the longest first, so a value that contains another goes as a whole), removes
  URL user information, and repeats both until the text no longer changes,
  because removing user information can join two pieces into a value; a value
  still present after the last pass returns only the marker. Only then is the
  text cut to `MaxBytes` (1024) on a UTF-8 boundary, so the cut can never leave
  part of a value behind.
- **`Userinfo(text)`** removes the `user:password@` of every URL with a scheme in
  free text, and leaves everything else byte for byte (an `scp`-style address or
  an e-mail address is left alone).
- **`TruncateUTF8`** cuts without splitting a rune.

## The CLI

`cmd/afc/main.go` builds the command tree with `cli.BuildRootCommand` and runs it
with `apikit.CLIExecute`. `BuildRootCommand` (`internal/cli/workspace_cmd.go`)
takes apikit's root command and adds apikit's `login`, `user`, `keys`, `orgs`
and `admin`; the hub's `tokens` (apikit's, wrapped by `wrapTokensCmd` with a
`--workspace` flag that mints and lists bound tokens through
`/workspaces/<slug>/tokens`); `workspace` (with the module's `patch-status`),
`credential-helper` (hidden), `secrets`, `vars`, `merge`, `rebase`, `sandbox`,
`forge`, `issue`, `pr` and `label`; and the module's `patch`, `rebuild` and
`rerere` (`carrypatch/cli`).

- **Requests** go through apikit's CLI client (`apikit.DoRequest`, which reads
  `ENDPOINT_URL`/`API_KEY` or `~/.af/config.toml` and prepends `/api/v1`), so
  commands pass bare paths. `apiPath` percent-encodes every user-supplied path
  segment.
- **Errors and exit codes.** Every error is written as the JSON envelope on
  stdout through `apikit.CLIHandleError`; `cli.ExitCode` is
  `apikit.CLIExitCode` (1 for an API or network error, 2 for a usage error)
  plus exit 3 for `workspace sync --fail-on-diverged`, which apikit would map
  to 2 (`ExitCodeDiverged`, `IsDivergedError`; `main` does not print that
  error, since the command already wrote its output).
- **Waiting.** `--wait`, `--timeout` (default 5 minutes) and `--poll-interval`
  (default 5 seconds) come from `addWaitFlags` (`wait.go`); the commands that
  wait poll the job or workspace record until it is terminal.
- **The credential helper** (`credential_cmd.go`) implements git's credential
  protocol: it answers `get` only when the scheme and host:port match the
  configured endpoint (so the key is never sent over plain `http` to an `https`
  hub), with the API key as the Basic password; `store` and `erase` do nothing.
