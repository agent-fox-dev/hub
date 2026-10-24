# carrypatch

`github.com/agent-fox-dev/carrypatch` is the carry-patch module of the
agent-fox hub: the part that maintains a fork as an ordered stack of patch
branches on top of an upstream repository and keeps an integration branch
that is always "upstream plus the patches".

## The problem

Teams that run a fork of a project they do not control carry their own
changes -- fixes not yet merged upstream, configuration, internal features --
on top of an upstream that keeps moving. Keeping that fork current by hand
means fetching upstream, re-applying every change, resolving the same
conflicts again and again, and noticing which changes upstream has merged in
the meantime. With a handful of patches and a busy upstream, this becomes a
daily chore that is easy to get wrong.

## How it solves it

You register your patch branches with the hub in the order they should be
applied. The hub then does the rest:

- it fetches upstream and, on request or after every change, **rebuilds the
  integration branch** by applying the patches in order onto the latest
  upstream;
- it **notices patches that upstream has merged**, including squash merges,
  and drops them from the stack;
- it **remembers how conflicts were resolved** (git rerere) and replays those
  resolutions in later rebuilds;
- it can **preview** which patches would conflict and **roll back** a bad
  rebuild;
- it lets either the hub or the fork be the authority for the patch branches,
  so teams can push to GitHub and open upstream pull requests as usual, or let
  agents push straight to the hub.

The hub serves all of this through its REST API and its git server, and the
`afc` command-line client includes the commands below.

## Example

```sh
# A carry-patch workspace: the fork, plus the upstream it tracks
afc workspace create --slug api-gateway \
    --git-url https://github.com/your-org/api-gateway-fork.git \
    --workspace-mode carry_patch \
    --upstream-url https://github.com/acme-oss/api-gateway.git \
    --git-pat "$GITHUB_TOKEN" --wait

# Register two patch branches, in the order they should be applied
afc patch add api-gateway --branch fix/connection-pool-leak \
    --upstream-pr https://github.com/acme-oss/api-gateway/pull/287
afc patch add api-gateway --branch internal/custom-metrics

# Rebuild the integration branch: latest upstream plus both patches
afc rebuild submit api-gateway --wait

# Later: fetch upstream, drop patches it has merged, rebuild if anything moved
afc workspace sync api-gateway --wait

# See the stack, the last rebuild and any conflicts
afc workspace patch-status api-gateway
```

The integration branch (`deploy` by default) now holds the latest upstream
with both patches applied.

## Where to go next

| Document | Read it to |
|----------|------------|
| [Carry-Patch Workflow](../docs/carry_patch_workflow.md) | Learn the concepts, walk through a setup, and look up day-to-day operations and settings |
| [Example agent instructions](../docs/examples/AGENTS_carry_patch.md) | Give coding agents working in a carry-patch fork an `AGENTS.md` to follow |
| [API Reference: patches](../docs/api.md#patch-endpoints) and [rebuilds](../docs/api.md#rebuild-endpoints) | Call the REST API directly |
| [CLI Reference: patches](../docs/cli.md#patch-commands) and [rebuilds](../docs/cli.md#rebuild-commands) | Look up an `afc` command and its flags |
| [Architecture: the carry-patch module](../docs/architecture.md#the-carry-patch-module) | Understand how the module is built and how the hub hosts it |
| [ADR 05: Move carry-patch to its own module](../docs/adr/05-move-carry-patch-to-its-own-module.md) | Learn why carry-patch is a module of its own |
| [Go package docs](../docs/generated/carrypatch/) | Browse the module's exported Go API |

## Install and develop

The module lives in the hub's repository and is built into the hub and `afc`;
there is nothing to install separately. It is a Go module of its own and does
not depend on the hub's code: what it needs from its host -- storage,
per-workspace locking, access checks and audit events -- the host supplies
(see [Architecture](../docs/architecture.md#the-carry-patch-module)).

### Prerequisites

- Go 1.27 or later
- Checkouts of [apikit](https://github.com/txsvc/apikit) and
  [aflib](https://github.com/agent-fox-dev/aflib) next to the hub repository
  (`../apikit` and `../aflib`, seen from the repository root)

### Build and test

The repository's Makefile covers both the hub and this module. From the
repository root:

```sh
make test       # tests of the hub and of this module
make check      # fmt, vet, lint and test: run before every commit
make doc        # regenerate docs/generated after changing an exported API
```

To work on the module alone:

```sh
cd carrypatch
go build ./...
go test ./... -count=1
```

Read the repository's [`AGENTS.md`](../AGENTS.md) before changing the module:
it is the working agreement for people and coding agents alike.
