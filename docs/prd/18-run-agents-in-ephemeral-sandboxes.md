# Run Agents in Ephemeral Sandboxes

> **Status: overview.** This document is the map for the sandbox feature.
> It records the concepts, the decisions that span more than one PRD, and
> the order in which the individual PRDs can be specified and implemented.
> Each PRD listed below is self-contained; this document is context, not a
> requirement source.

## Intent

Agents that work through the hub today run wherever someone starts them: on
a developer laptop, in a codespace, in a container started by hand from
`containers/agents`. The hub owns the workspace, its branches, its secrets
and its audit trail, but it has no say over the environment the agent's tools
execute in. Nothing stops an agent from reading another workspace's clone,
using the operator's credentials, or exhausting the host.

The sandbox feature gives the hub that say. A **sandbox** is an isolated,
ephemeral execution environment that the hub launches for a workspace on a
branch, pinned to a revision, that an agent (or a person with a shell) works
inside, and that pushes its work back to the hub before it disappears. The
hub does not run the agent itself. It provisions the environment, injects the
minimum the environment needs to phone home, and keeps a two-way control
channel open to a small service inside the sandbox, the **outpost**, for the
lifetime of the sandbox. The outpost fetches everything else (code,
variables, secrets, the workload command) from the hub and starts the
workload.

## Concepts

- **Sandbox.** An ephemeral execution environment for one workspace on one
  branch at one revision. Identified by a UUID, recorded in the hub's
  database, realised by a gateway using a provider.
- **Provider.** How a sandbox is realised: `container` (OCI container),
  `microvm` (container image booted in a microVM, podman with libkrun), or
  `pod` (Kubernetes pod).
- **Gateway.** The component that realises sandboxes. The **local gateway**
  is built into the hub and drives podman on the hub host. A **remote
  gateway** is a separate process (`af-gateway`) on another host or in a
  cluster that speaks the same interface to the hub over NATS.
- **Outpost.** The process that runs as the sandbox's entrypoint
  (`afc outpost run`). It bootstraps the sandbox from the hub, connects to
  the hub's NATS server, starts the workload, executes commands and reports
  liveness.
- **Workload.** The command the outpost starts after bootstrap, from the
  sandbox configuration: an agent, a script, or nothing.
- **Bootstrap document.** What the outpost fetches from the hub, once, with
  the sandbox token: workspace, branch, revision, clone URL, NATS
  credentials, workload command, environment (plain values, variables and
  secrets).
- **Collector.** The outpost's loopback endpoint where agents hand over
  telemetry; the outpost forwards it to the hub over NATS.
- **Sandbox configuration.** The JSON document (`sandbox.json`) that says
  which image, provider, gateway, resources, network policy, environment,
  secrets, workload and timeouts a sandbox is launched with.
- **Sandbox token.** A workspace-bound token issued for exactly one sandbox;
  it dies with the sandbox.

## The PRDs

| # | PRD | Delivers | Depends on |
|---|---|---|---|
| 19 | `19-embed-a-nats-server-in-the-hub.md` | Embedded NATS server, WebSocket at `/nats`, per-user credentials added at runtime, hub-side client | nothing |
| 20 | `20-issue-workspace-bound-tokens.md` | Tokens valid for one workspace, one optional subject, with hub-enforced expiry and revocation | nothing |
| 21 | `21-define-sandbox-configuration.md` | `sandbox.json` schema, hub default file, per-workspace document via API, ceilings, environment resolution | nothing |
| 22 | `22-abstract-sandbox-runtimes-behind-a-gateway.md` | `Gateway` interface, registry, local podman gateway with `container` and `microvm` providers | nothing |
| 23 | `23-define-the-sandbox-control-protocol.md` | Subjects, envelope, commands, events, telemetry records, bootstrap document, container variables, as a Go package and a reference doc | nothing (transport is 19) |
| 24 | `24-launch-and-manage-sandboxes.md` | Sandbox records and state machine, launch and stop, bootstrap endpoint, commands and events on the hub side, reconciliation, API and CLI | 19, 20, 21, 22, 23 |
| 25 | `25-build-the-outpost.md` | `afc outpost run`: bootstrap, clone, NATS, commands, workload supervision, heartbeat; image entrypoints | 23 (24 for end-to-end) |
| 26 | `26-collect-telemetry-from-sandboxes.md` | Collector in the outpost, `afc outpost emit`, hub-side ingestion of events, usage, metrics and logs | 23, 24, 25 |
| 27 | `27-refresh-secrets-in-running-sandboxes.md` | `refresh` command: one-shot re-fetch of rotated secrets and variables, optional workload restart | 24, 25 |
| 28 | `28-attach-remote-gateways.md` | `af-gateway` binary, gateway registration over NATS, hub-side remote gateway client | 19, 22, 24 |

Dependency graph, top to bottom:

```
 19 nats     20 tokens     21 config     22 gateway     23 protocol
    \            \            |            /              /
     +------------+-----------+-----------+--------------+
                              |
                        24 lifecycle -------- 25 outpost
                              |                  |
                +-------------+-------------+    |
                |             |             |    |
           26 telemetry   27 refresh   28 remote gateways
```

19 through 23 have no dependencies on each other and can be specified and
implemented in parallel. 24 and 25 both implement 23 and can be built in
parallel against it: 24 with a fake outpost, 25 with a fake hub. 26, 27 and
28 are independent extensions once 24 and 25 exist.

Future PRDs, not written: a Kubernetes gateway with the `pod` provider,
network egress enforcement through
[NVIDIA OpenShell](https://github.com/NVIDIA/OpenShell), and a Kubernetes
manifest for `af-gateway`. Interactive exec or shell through the hub is not
planned.

## Cross-cutting decisions

These were settled in the workshop rounds and hold across all PRDs. Each PRD
repeats the ones it needs in its own Design Decisions.

1. **Gateway × provider, not one provider enum.** A provider is a launch
   recipe, a gateway is who executes it and where. The podman recipe is
   shared by the in-process local gateway and by `af-gateway` on a remote
   host; a Kubernetes gateway adds one recipe without touching the protocol.
2. **Clone through the git server, no host mounts.** Every sandbox is
   self-contained and every gateway equal.
3. **Three variables in, everything else fetched once.** The container gets
   `AF_HUB_URL`, `AF_HUB_TOKEN`, `AF_SANDBOX_ID`. Secrets, variables, NATS
   credentials and the workload command come from the bootstrap document
   over HTTPS, exactly once. Anything structural changing after launch means
   a new sandbox; rotated secrets and variables go through `refresh`.
4. **The outpost supervises the workload.** A consequence of 3: only the
   outpost holds the fetched environment, so it starts the workload,
   forwards signals and output, and reports the exit. `on_exit` decides
   whether the sandbox then stops or stays.
5. **NATS is the only control channel**, for outposts and remote gateways
   alike, served over WebSocket at `/nats` behind the hub's HTTP listener so
   that one hostname, port and certificate cover everything and remote
   parties dial out.
6. **Work on the workspace branch by default.** Isolation per sandbox is one
   launch parameter away (`branch`); the merge queue already exists.
7. **Liveness by heartbeat, not idleness by inference.** No idle timeout.
   Lifetime is bounded by `timeouts.max` and the workload's exit.
8. **The outpost is the collector.** Agents report telemetry to a loopback
   endpoint; the outpost forwards. Agents never hold NATS credentials. Only
   agent-provided telemetry reaches the hub; stdout and stderr are never
   captured.
9. **No hard caps** on sandbox counts. Gateways refuse when out of capacity.
10. **Sandboxes outlive the hub process.** Reconcile on startup; outposts
    reconnect.
11. **The outpost is `afc`.** No new binary for the sandbox side.
12. **Workspace configuration lives on the hub**, via the API, replacing the
    default wholesale; never read from the repository.
13. **Network policy waits for OpenShell.** The configuration carries a
    `network` block; no gateway in these PRDs enforces `restricted`, and the
    hub never silently downgrades.
14. **Token binding in the hub instead of a new credential type.** apikit
    owns credentials; the hub binds a PAT to a workspace and subject.
15. **The podman CLI, not the Go bindings.**

## Open questions that span PRDs

- **NATS auth model** (PRD 19): per-user options reload now, or auth callout
  from the start so one credential serves HTTPS and NATS.
