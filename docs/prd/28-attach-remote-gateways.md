# Attach Remote Gateways

## Intent

The local gateway (`docs/prd/22-abstract-sandbox-runtimes-behind-a-gateway.md`)
runs sandboxes on the hub host. Real deployments want sandboxes on a
bigger machine, in a cloud account, or in a cluster the hub is not part
of, without opening inbound ports on those hosts. This PRD adds **remote
gateways**: a small static binary, `af-gateway`, that hosts the same podman
implementation the local gateway uses, connects out to the hub's NATS
endpoint, registers itself with its capabilities, and answers the gateway
operations of `docs/prd/23-define-the-sandbox-control-protocol.md`. On the
hub side, a remote gateway is one more entry in the registry, backed by a
thin NATS client, so the lifecycle code does not know the difference.

## Goals

- `[[sandbox.gateways]]` entries in `config.toml` with a name, a
  credential and the URLs sandboxes launched there should use.
- `af-gateway` (`cmd/af-gateway`): configuration, NATS connection with
  reconnect, registration and heartbeat, request handling for `launch`,
  `inspect`, `stop`, `list`, `health`, `capabilities`, hosting the podman
  implementation.
- A hub-side `Gateway` implementation that forwards to the remote over
  NATS and reports `connected`/`disconnected` state.
- `GET /api/v1/gateways` showing remote gateways with their state.

## Non-goals

- A Kubernetes implementation inside `af-gateway` (future PRD; the binary
  is designed to host it).
- Gateway auto-discovery. Gateways are declared in `config.toml`.
- Scheduling across gateways. The configuration names one gateway.

## Current behaviour (for reference)

- PRD 22 defines `Gateway`, `Registry`, the podman implementation and the
  `local` gateway; PRD 19 provides NATS users with subject permissions;
  PRD 24 computes per-gateway `AF_HUB_URL` and NATS URLs from
  `[sandbox.local]` or the server defaults.

## Functional Requirements

### Hub side

- **RG-1.** `config.toml`:

  ```toml
  [[sandbox.gateways]]
  name     = "cloud-1"
  token    = "${GW_CLOUD1_TOKEN}"    # NATS password for user gw-cloud-1
  hub_url  = "https://hub.example.com"
  nats_url = "wss://hub.example.com/nats"
  ```

  At startup the hub SHALL add a NATS user `gw-<name>` with the token as
  password, subscribe `af.gw.<name>.cmd`, publish `af.gw.<name>.evt.>`,
  `allow_responses`, and register a remote `Gateway` under `name`.
  Duplicate names or names clashing with `local` abort startup.
- **RG-2.** The remote `Gateway` SHALL implement every method as a NATS
  request on `af.gw.<name>.cmd` with the PR-8 payloads and timeouts
  (`launch` 10m, others 30s), mapping error codes back to the sentinel
  errors. `Capabilities` returns the last `registered` payload without a
  round trip. `Health` fails when the gateway is `disconnected`.
- **RG-3.** State: `connected` after a `registered` event, `disconnected`
  when no `heartbeat` (every 30s) arrives for 90s or the NATS server
  reports the user's connection closed. A launch on a disconnected
  gateway fails with `503 gateway_unavailable` before any record changes.
  `sandbox_state` events (PR-9) are forwarded to the lifecycle as if
  `Inspect` had returned them.
- **RG-4.** PRD 24's URL computation SHALL use the entry's `hub_url` and
  `nats_url` for sandboxes launched on that gateway, falling back to the
  server defaults when unset.
- **RG-5.** `GET /api/v1/gateways` lists remote gateways with
  `kind: "remote"`, `connected`, `last_heartbeat_at`, `version` and
  capabilities.

### `af-gateway`

- **RG-6.** `af-gateway run --config gateway.toml` with:

  ```toml
  name     = "cloud-1"
  nats_url = "wss://hub.example.com/nats"
  token    = "${GW_CLOUD1_TOKEN}"

  [podman]
  binary  = "podman"
  prepull = ["quay.io/agentfox/agents:latest"]
  ```

  It SHALL connect as `gw-<name>`, publish `registered` with the podman
  implementation's `Capabilities` and its version, then `heartbeat` every
  30s, and re-publish `registered` after every reconnect. It SHALL answer
  requests on `af.gw.<name>.cmd` by calling the podman implementation and
  encoding results and errors per PR-8, handling requests concurrently.
- **RG-7.** It SHALL publish `sandbox_state` when a periodic scan (every
  60s) of its labelled containers finds a state change since the last
  scan, so the hub learns of crashes between its own inspections.
- **RG-8.** It SHALL log with `slog`, expose `/healthz` and `/metrics` on
  an optional local port, and exit non-zero on a configuration error. It
  needs no inbound connectivity from the hub.
- **RG-9.** The binary SHALL be static (`CGO_ENABLED=0`), built by
  `make build` next to `afc`, and published by the CLI build workflow.

### Testing

- Unit: remote `Gateway` request encoding and error mapping with an
  embedded NATS server; state transitions from `registered`, `heartbeat`
  and silence; URL selection in the lifecycle.
- Integration: `af-gateway` as a goroutine with the `FakeGateway` behind
  it, driven through the hub's registry: launch, inspect, stop, list,
  disconnect and reconnect; with real podman behind a build tag.

## Technical Boundaries

- Go; `internal/sandbox/gateway/remote` (hub side) and
  `internal/gateway` plus `cmd/af-gateway` (daemon). The daemon imports the
  podman implementation and the protocol package only.
- Documentation: `docs/configuration.md` (`[[sandbox.gateways]]`), a new
  `docs/gateway.md` for the daemon, `README.md` (third binary),
  `docs/api.md` (`GET /api/v1/gateways` fields).

## Dependencies

- PRD 19 (users, WebSocket endpoint), PRD 22 (interface, podman
  implementation, registry), PRD 23 (gateway operations), PRD 24 (URL
  computation and `sandbox_state` handling).

## Design Decisions

### 1. Gateways dial out over the same NATS endpoint

No inbound ports, no per-host TLS, no per-host credentials beyond one
NATS password; the same envelope and auth model as the outposts.

### 2. The daemon hosts the same implementation as the local gateway

`af-gateway` is the local gateway's podman code behind a NATS adapter.
A Kubernetes implementation later slots into the same daemon.

### 3. Static configuration

Declaring gateways in `config.toml` keeps the set of hosts that may run
workspace code an operator decision, and gives each a stable name that
configurations can reference.

## Open Questions

- Should a gateway be able to serve several hubs, or a hub accept a
  gateway that registers with a name not in its configuration (with a
  shared secret)? The draft is strict: one hub, declared names only.
