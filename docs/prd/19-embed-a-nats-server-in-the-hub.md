# Embed a NATS Server in the Hub

## Intent

The hub is a single process with one HTTP listener. Every interaction with
it is request/response, initiated by the client. That is enough for the CLI
and for agents that poll, but not for the sandbox feature
(`docs/prd/18-run-agents-in-ephemeral-sandboxes.md`), where the hub must
send commands to a process running inside a container it launched, possibly
on another host, and receive events and telemetry back, without opening
ports on that host or polling the container runtime.

This PRD embeds a NATS server (`github.com/nats-io/nats-server/v2`) in the
`hub` process and makes it reachable the same way the REST API is: over
WebSocket at `/nats` on the hub's existing listener, behind the same TLS
termination and the same Route or ingress. It gives the rest of the hub a
way to mint per-client NATS credentials with subject-level permissions at
runtime, and an in-process client with full permissions. It does not define
what travels over NATS; that is
`docs/prd/23-define-the-sandbox-control-protocol.md`. This PRD is complete
when an external client can connect through `/nats` with a credential the
hub created a moment earlier and is confined to the subjects that
credential allows.

## Goals

- Run a NATS server inside the `hub` process, started before the HTTP
  server accepts requests and stopped after it drains.
- Serve NATS over WebSocket at `GET /nats` on the hub's HTTP listener, so
  that clients outside the hub's network need nothing beyond the hub's
  `external_url`.
- Optionally listen on a plain NATS TCP port for clients on the hub's own
  network.
- Let hub code add and remove NATS users at runtime, each with an explicit
  publish and subscribe allow-list and `allow_responses`.
- Provide an in-process client for the hub's own use.
- Report the server in `/readyz` and Prometheus metrics.

## Non-goals

- Clustering, JetStream, leaf nodes, accounts beyond the single hub
  account. The server serves one hub.
- Auth callout (the server asking the hub to validate each connection).
  Listed as an open question; per-user credentials are enough to start.
- Any hub feature that uses NATS. Subjects, envelopes and semantics belong
  to the PRDs that own them.
- Exposing NATS to end users as a product feature. Credentials are created
  only by hub code for hub-launched parties.

## Current behaviour (for reference)

- `cmd/af-hub/main.go` builds an `apikit.Server` around an Echo instance,
  mounts handlers, and calls `server.Start()`. The git server mounts routes
  on the same Echo instance outside the API group, which is the pattern
  this PRD follows for `/nats`.
- `/readyz` is served by `health.NewDBChecker`; there is one checker.
- Metrics are registered through `internal/audit`'s `Metrics` and exposed at
  `/metrics`.
- `deploy/route.yaml` exposes only the HTTP port with edge TLS.

## Functional Requirements

### Configuration

- **NS-1.** `config.toml` gains a `[nats]` section:

  ```toml
  [nats]
  enabled      = true
  port         = 4222        # plain NATS TCP; 0 disables the TCP listener
  bind         = "0.0.0.0"
  external_url = ""          # default: [server] external_url with ws/wss scheme and path /nats
  ```

  With `enabled = false` (the default) nothing in this PRD runs, and
  `Client()` (NS-8) returns `ErrDisabled`.
- **NS-2.** `external_url` is what the hub hands to parties that must
  connect from outside. When empty it is derived from
  `[server] external_url` by replacing `http` with `ws` and `https` with
  `wss` and appending `/nats`. When `[server] external_url` is also empty
  the hub logs a warning at startup and `ExternalURL()` returns an empty
  string; callers decide whether that is fatal for them.

### Server lifecycle

- **NS-3.** The embedded server SHALL start before `server.Start()` and
  SHALL be shut down after the HTTP server has stopped, so that in-flight
  hub requests can still publish. Startup failure (port in use, bad
  options) SHALL abort hub startup.
- **NS-4.** The server SHALL always run a WebSocket listener bound to
  loopback on an ephemeral port, with compression enabled and `no_tls`
  (TLS is the HTTP listener's job). The hub SHALL register `GET /nats` on
  the Echo instance, outside the API group and its auth middleware, and
  reverse-proxy WebSocket upgrades on it to that listener, preserving
  `Sec-WebSocket-*` headers and closing both sides when either closes.
  Non-upgrade requests to `/nats` get `426 Upgrade Required`.
- **NS-5.** When `port > 0` the server SHALL also listen on `bind:port` for
  plain NATS connections.
- **NS-6.** The server SHALL refuse connections without credentials
  (`no_auth_user` unset, no anonymous access). Server logs go through the
  hub's `slog` at `debug` unless the server reports an error.

### Users and permissions

- **NS-7.** The package SHALL expose:

  ```go
  type Permissions struct {
      Publish        []string // subject patterns
      Subscribe      []string
      AllowResponses bool     // may reply to requests it receives
  }
  func (s *Server) AddUser(name, password string, p Permissions) error
  func (s *Server) RemoveUser(name string) error
  ```

  implemented with the server's options reload, so a user is usable
  immediately after `AddUser` returns and a removed user's existing
  connections are closed. Names SHALL be unique; adding an existing name
  replaces its password and permissions. Users SHALL NOT be persisted:
  after a hub restart the set is empty and owners re-add what they need
  (the lifecycle PRD does this during reconciliation).
- **NS-8.** `Client()` SHALL return an in-process `*nats.Conn` for the hub
  with no permission restrictions, connected through the server's in-process
  connection option (no TCP round trip), with automatic reconnect.

### Health and metrics

- **NS-9.** `/readyz` SHALL include a `nats` checker that fails when the
  server is not running or the WebSocket listener is not accepting.
- **NS-10.** Metrics: `af_nats_connections` (gauge, by listener `tcp` or
  `ws`), `af_nats_users` (gauge), `af_nats_messages_total{direction}`
  (counter, from the server's varz).

### Testing

- `Server` on random ports in tests with a helper `nats.StartTest(t)`;
  a `nats.go` client connecting through an `httptest.Server` that mounts
  the `/nats` proxy; a user added at runtime that can publish only on its
  allow-list and gets a permissions violation elsewhere; removal closes the
  connection; `allow_responses` lets a user answer a request on a subject
  it may not otherwise publish to.

## Technical Boundaries

- Go; new package `internal/nats` wrapping `nats-server/v2` and `nats.go`.
  Both are pure Go and add no cgo.
- The reverse proxy uses `net/http` hijacking on both sides; no third-party
  WebSocket library beyond what `nats-server` already depends on.
- Documentation: `docs/configuration.md` (`[nats]`), `README.md` (the
  `/nats` endpoint), `docs/api.md` (a short "NATS endpoint" section noting
  it is not part of the REST API).

## Dependencies

- `nats-server/v2` options reload for NS-7; the WebSocket listener and
  in-process client connection are stable features of the 2.10 line.
- `apikit.Server` must expose the Echo instance (it does: `server.Echo()`)
  and a way to run code after shutdown; if the latter is missing, the hub
  stops NATS from `main` after `server.Start()` returns.

## Design Decisions

### 1. WebSocket on the hub's own listener

A separate NATS port is one more thing to open in firewalls, Routes and
ingress controllers, and the reference manifests only route HTTP. Serving
the WebSocket listener behind `/nats` means one hostname, one port, one
certificate, and remote parties that dial out. The TCP port stays for
in-network clients that prefer it.

### 2. Per-user credentials through options reload

Embedded `nats-server` supports per-user publish and subscribe permissions
and `allow_responses`, and its options reload adds and removes users
without restarting. It is the simplest thing that confines each client to
its own subjects. Users are not persisted because their owners (sandbox
records, gateway entries) already are.

### 3. Loopback-only WebSocket listener

Binding the WebSocket listener to loopback and reaching it only through
the proxy keeps TLS, access logging and rate limiting in one place, the
HTTP listener.

## Open Questions

- **Auth callout.** NATS 2.10 can delegate authentication to a service. The
  hub could validate its own tokens there, so that one credential (the
  workspace-bound token of PRD 20) serves both HTTPS and NATS and no NATS
  user list exists. It needs NKey signing and a callout responder. Start
  with options reload and revisit when the user list becomes a burden, or
  decide now?
