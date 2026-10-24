# ADR 05: Move carry-patch to its own module

- **Status:** Accepted
- **Date:** 2026-10-24

## Context

Carry-patch (the patch list, the rebuild of the integration branch, rerere, the
patch status dashboard, the sync extension, the push control of patch branches
and the `afc patch`, `rebuild`, `rerere` and `patch-status` commands) was the
largest feature of the hub and lived in `internal/carrypatch`, with its patch
routes and store in `internal/workspace` and its git runner in
`internal/gitcmd`. It imported the hub's workspace, audit, lock, access and git
server packages, and the hub's workspace package held hook types and globals for
it (the branch check hook, the recovery hook, the sync hook). The two sides met
in many places, so neither could be read, tested or changed on its own, and the
hub's core carried code that only carry-patch workspaces use.

## Decision

1. **Carry-patch is a Go module of its own,** `github.com/agent-fox-dev/carrypatch`,
   nested in this repository at `./carrypatch` and replaced in the hub's `go.mod`.
   It holds everything carry-patch: the routes, the patch store, the reset
   handler, the rebuild job, rerere, patch status, the sync extension, the git
   hooks, the git runner (`carrypatch/gitcmd`, which the hub's merge and
   workspace packages also use) and the `afc` commands (`carrypatch/cli`).
   Nesting keeps one checkout and one pull request per change; the module can
   move to a repository of its own later without a code change.

2. **The module imports nothing of the hub.** It cannot: the hub's packages are
   `internal`. What it needs from its host it declares as a contract in
   `host.go`: `Host` (the per-workspace locks and the workspace access rule, set
   once with `SetHost`), `Emitter` and `Event` (audit), and the git hook types
   `RefUpdate`, `PreReceiveHook` and `PostPushHook`. Its routes take their
   collaborators in `APIConfig` (the database, the queue, the patch store, the
   branch resolver, the recovery service, the audit emitter) instead of reading
   hub globals.

3. **The hub adapts in one package,** `internal/cphost`: `Bind` supplies
   `wslock` and `wsaccess`, `Emitter` wraps `audit.Emitter`, and `PreReceive`
   and `PostPush` convert the hook types for `gitserver`. `cmd/af-hub/main.go`
   is the only other file that names the module.

4. **The workspace core keeps the mode.** The `workspace_mode`, `upstream_url`
   and `integration_branch` fields, their validation at creation, the clone of
   a carry-patch workspace with both remotes, the `patches` table DDL and the
   generic sync extension point (`workspace.RegisterSyncExtension`) stay in the
   hub: a workspace knows which mode it is in, and the module fills the mode in.

5. **Tests follow the code.** The module's tests run against doubles of the
   host (in-process locks, the access rule over the test database); the tests
   that need the hub's git server or workspace routes live in `internal/cphost`
   and `internal/workspace`.

## Consequences

- `make test`, `make vet`, `make lint`, `make tidy` and `make doc` run over
  both modules; `docs/generated/carrypatch/` holds the module's package docs.
- The hub image build copies `carrypatch/go.mod` before `go mod download`; the
  module's own replaces (`../../apikit`, `../../aflib`) resolve from
  `/build/hub/carrypatch` the same way the hub's do.
- The hub's workspace package no longer knows carry-patch's hook types: the
  branch check hook, the recovery hook and the patch routes are gone from it,
  and the request scope helper moved into the module.
