# Define the Sandbox Control Protocol

## Intent

The hub and the outpost inside a sandbox
(`docs/prd/18-run-agents-in-ephemeral-sandboxes.md`) are built by
different PRDs and should be buildable in parallel. What they share is a
contract: the three environment variables the container receives, the
bootstrap document the outpost fetches, the NATS subjects each side uses,
the message envelope, the commands the hub may send, the events the outpost
reports, the telemetry records agents may hand in, and the operations a
remote gateway answers.

This PRD writes that contract down once, as a Go package with types,
encoding and validation, and as a reference document. It has no runtime
behaviour beyond marshalling. It is complete when the package round-trips
every message in the spec, rejects malformed ones, and the reference
document is published.

## Goals

- One package, `internal/sandbox/protocol`, with Go types for every message
  and document, JSON encoding, validation, and version constants.
- One reference document, `docs/sandbox_protocol.md`, that an outpost or a
  gateway written in another language could implement from.
- Stable subject names and an envelope that carries an id for idempotency
  and correlation.

## Non-goals

- Transport. NATS itself is `docs/prd/19-embed-a-nats-server-in-the-hub.md`.
- Behaviour. What the hub does with an event, or the outpost with a
  command, is specified where each is implemented (PRDs 24 to 28). This
  PRD says only what the bytes mean.
- Backwards compatibility machinery. Version 1 is the first version; the
  envelope carries a version field so that a second can be negotiated
  later.

## Current behaviour (for reference)

- Nothing exists. Agent audit events and token usage have JSON formats
  accepted by `POST /api/v1/workspaces/:slug/runs/:run_id/events` and
  `POST /api/v1/sessions/:id/usage` (`docs/api.md`); the telemetry record
  types below reuse them rather than defining new ones.

## Functional Requirements

### Container environment

- **PR-1.** A sandbox receives exactly three hub-provided environment
  variables: `AF_HUB_URL` (HTTP base URL of the hub as reachable from the
  sandbox), `AF_HUB_TOKEN` (a workspace-bound token issued for the
  sandbox), `AF_SANDBOX_ID` (UUID). Nothing else of the hub's is set on the
  container.

### Bootstrap document

- **PR-2.** `GET /api/v1/sandboxes/:id/bootstrap`, authenticated with the
  sandbox token, returns:

  ```json
  {
    "version": 1,
    "sandbox_id": "uuid",
    "workspace": "my-project",
    "git_url": "https://hub.example.com/git/org/my-project.git",
    "branch": "main",
    "revision": "abc123…",
    "workdir": "/opt/app-root/workspace",
    "command": ["claude", "-p", "…"],
    "on_exit": "stop",
    "nats": { "url": "wss://hub.example.com/nats", "user": "sbx-uuid", "password": "…" },
    "env": { "AGENT_LOG_LEVEL": "info", "MY_VAR": "…", "ANTHROPIC_API_KEY": "…" },
    "secret_keys": ["ANTHROPIC_API_KEY"],
    "timeouts": { "boot": "5m", "max": "8h" }
  }
  ```

  `command` may be absent (idle). `env` values under `secret_keys` are
  secrets and must never be logged by the receiver. The document is
  returned once per sandbox; later requests are `404` (a `refresh` nonce,
  PRD 27, allows one more: `?nonce=<value>`).

### Subjects

- **PR-3.** All subjects live under `af.`:

  | Subject | Direction | Pattern |
  |---|---|---|
  | `af.sbx.<id>.cmd` | hub → outpost | request/reply |
  | `af.sbx.<id>.evt.<type>` | outpost → hub | publish |
  | `af.sbx.<id>.tel.<type>` | outpost (collector) → hub | publish, batched |
  | `af.gw.<name>.cmd` | hub → remote gateway | request/reply |
  | `af.gw.<name>.evt.<type>` | remote gateway → hub | publish |

  `<id>` is the sandbox UUID, `<name>` a gateway name matching
  `^[a-z0-9-]{1,63}$`.

### Envelope

- **PR-4.** Every message is one JSON object:

  ```json
  { "v": 1, "id": "uuid", "type": "sync", "sent_at": "RFC3339", "sandbox_id": "uuid", "payload": {} }
  ```

  Replies to requests carry the request's `id` and either
  `{ "v": 1, "id": "…", "ok": true, "payload": {} }` or
  `{ "v": 1, "id": "…", "ok": false, "error": { "code": "dirty_worktree", "message": "…" } }`.
  Error codes are lower-snake strings from a registry in the package.
  Gateway messages carry `gateway` instead of `sandbox_id`. Unknown `type`
  values in a request are answered `unsupported`; in an event they are
  dropped. Receivers treat `id` as an idempotency key.

### Commands (hub → outpost)

- **PR-5.** Request payloads and reply payloads:

  | Type | Request payload | Reply payload | Error codes |
  |---|---|---|---|
  | `ping` | `{}` | the `heartbeat` payload (PR-6) | |
  | `sync` | `{ "direction": "pull" \| "push", "ref": "…"?, "force": bool }` | `{ "sha": "…" }` | `dirty_worktree`, `non_fast_forward`, `git_error` |
  | `shutdown` | `{ "grace_seconds": 30, "push": bool }` | `{ "push": { "ok": bool, "sha"?: "…", "error"?: "…" } \| null }` (sent as the `exiting` event as well) | |
  | `refresh` | `{ "nonce": "…", "restart_command": bool }` | `{ "changed_keys": ["…"], "restarted": bool }` | `bootstrap_failed` |

  Suggested timeouts for callers: `ping` 5s, `sync` 10m, `shutdown` grace + 5s,
  `refresh` 60s.

### Events (outpost → hub)

- **PR-6.** Payloads:

  | Type | Payload |
  |---|---|
  | `ready` | `{ "revision": "…", "head_sha": "…", "outpost_version": "…" }` (`head_sha` differs from `revision` when the branch moved between launch and boot) |
  | `boot_failed` | `{ "stage": "bootstrap" \| "clone" \| "nats", "error": "…" }` |
  | `heartbeat` | `{ "head_sha": "…", "dirty": bool, "uptime_s": n, "command_running": bool, "telemetry_dropped": n, "stats": { "cpu_pct": f, "mem_bytes": n, "disk_bytes": n, "procs": n } }` |
  | `command_exited` | `{ "exit_code": n, "signal"?: "…", "ran_for_s": n }` |
  | `exiting` | `{ "reason": "shutdown" \| "sigterm" \| "boot_failed", "push": {…} \| null }` |

### Telemetry records

- **PR-7.** Published on `af.sbx.<id>.tel.<type>` as an envelope whose
  `payload` is `{ "records": [ … ] }`, each record
  `{ "type": "…", "at": "RFC3339", "payload": {} }`:

  | Type | Payload |
  |---|---|
  | `event` | an agent audit event exactly as `POST …/runs/:run_id/events` accepts |
  | `usage` | exactly as `POST /sessions/:id/usage` accepts, plus `session_id` |
  | `metrics` | `{ "name": "…", "value": f, "labels": { "…": "…" } }` |
  | `log` | `{ "level": "debug" \| "info" \| "warn" \| "error", "message": "…", "fields": {} }` |

  The collector's local HTTP API (PRD 26) accepts one record or an array
  of records in exactly this shape at `POST /v1/telemetry`.

### Gateway operations (hub ↔ remote gateway)

- **PR-8.** Requests on `af.gw.<name>.cmd` mirror the `Gateway` interface
  of `docs/prd/22-abstract-sandbox-runtimes-behind-a-gateway.md`:
  `launch` (payload: `LaunchSpec`; reply: `{ "handle": "…" }`), `inspect`
  (`{ "handle" }` → `Status`), `stop` (`{ "handle", "grace_seconds" }` →
  `{}`), `list` (`{}` → `{ "items": [ … ] }`), `health` (`{}` → `{}`),
  `capabilities` (`{}` → `Capabilities`). Error codes mirror the sentinel
  errors: `unsupported_provider`, `unsupported_policy`, `image_pull`,
  `capacity`, `not_found`, `unavailable`.
- **PR-9.** Events on `af.gw.<name>.evt.<type>`: `registered`
  (`{ "capabilities": {…}, "version": "…" }`), `heartbeat` (`{}`),
  `sandbox_state` (`{ "handle": "…", "status": Status }`, sent when the
  gateway notices a change on its own).

### Package

- **PR-10.** `internal/sandbox/protocol` SHALL provide typed structs for
  everything above, `Encode`/`Decode` helpers that enforce `v`, required
  fields and enum values, subject builders (`SandboxCmd(id)`,
  `SandboxEvt(id, type)`, …) and parsers, the error-code registry, and
  `Version = 1`. Validation errors name the field.
- **PR-11.** `docs/sandbox_protocol.md` SHALL be generated from, or kept
  in lock-step with, the package by a test that renders every example in
  the document through `Decode` and fails on drift.

### Testing

- Round-trip tests for every message; rejection tests for missing `v`,
  unknown enum values, bad subjects; golden files for the examples in the
  reference document.

## Technical Boundaries

- Go; `encoding/json` only. The package has no dependencies on other hub
  packages so that `afc` (the outpost) can import it without pulling in
  server code.
- Documentation: `docs/sandbox_protocol.md` (new), referenced from
  `docs/api.md` next to the bootstrap endpoint.

## Dependencies

- None at build time. The audit event and usage formats it references are
  defined by `docs/api.md`.

## Design Decisions

### 1. A contract package instead of two implementations agreeing

Having the hub and the outpost import the same types makes drift a compile
error, and the reference document keeps the door open for an outpost that
is not `afc`.

### 2. Idempotency keys in the envelope

Redelivery and reordering are normal on a reconnecting link. Giving every
message an id lets the hub apply events idempotently and lets the outpost
ignore a duplicated command.

### 3. Telemetry reuses existing ingestion formats

`event` and `usage` are the HTTP ingestion bodies verbatim, so an agent can
report through the collector or over HTTPS interchangeably and the hub
stores them through the same path.

## Open Questions

- Whether `sent_at` should be authoritative for ordering or only
  informational. The draft treats it as informational and orders by
  arrival, with `id` for idempotency.
