# Collect Telemetry from Sandboxes

## Intent

Agents already report audit events and token usage to the hub over HTTPS.
Inside a sandbox (`docs/prd/18-run-agents-in-ephemeral-sandboxes.md`) the
outpost holds a live NATS connection to the hub, and agents should not have
to hold one too, nor manage credentials, reconnects and back-pressure. This
PRD gives every sandbox a **collector**: a loopback HTTP endpoint run by the
outpost where anything inside the sandbox hands over telemetry (agent
events, token usage, metrics, logs). The outpost batches and forwards it
to the hub over NATS, the hub stores it through the same paths as the HTTP
endpoints and makes it queryable by sandbox. Only what an agent
deliberately reports reaches the hub; stdout and stderr are never captured.

## Goals

- A collector in the outpost at `AF_COLLECTOR_URL` accepting the record
  shape of PRD 23, batching, buffering while disconnected, redacting
  secrets, and publishing on `af.sbx.<id>.tel.<type>`.
- `afc outpost emit` for shell scripts and agents without an HTTP client.
- Hub-side ingestion: `event` and `usage` through the existing audit and
  session paths; `metrics` and `log` persisted in the audit store; all
  attributed to the sandbox.
- A `sandbox_id` filter on the unified audit query and the SSE stream, and
  the latest metric values on the sandbox record.

## Non-goals

- Capturing workload output, files or shell history.
- Telemetry influencing lifecycle. Only outpost events do.
- Acknowledged delivery. Telemetry is best-effort.
- Aggregation, dashboards or alerting on the hub.

## Current behaviour (for reference)

- `POST /api/v1/workspaces/:slug/runs/:run_id/events` ingests agent audit
  events into DuckDB; `POST /api/v1/sessions/:id/usage` records token
  usage; `GET /api/v1/audit` and `GET /api/v1/events` (SSE) query and
  stream them (`docs/api.md`). Retention is governed by
  `AF_AUDIT_MAX_AGE_DAYS` and related variables.
- The outpost (PRD 25) forwards workload output to the container's stdout
  only and passes `AF_HUB_URL`, `AF_HUB_TOKEN`, `AF_SANDBOX_ID` and the
  bootstrap `env` to the workload.

## Functional Requirements

### Collector (outpost side)

- **CO-1.** The outpost SHALL listen on `127.0.0.1:9911` (overridable
  with the `--collector-addr` flag of `afc outpost run`, set at image
  level) and export `AF_COLLECTOR_URL` to the workload. `POST /v1/telemetry` accepts one record or a JSON array
  of records in the PR-7 shape, validates the shape and enum values,
  stamps a receipt time when `at` is absent, and answers `202` with
  `{ "accepted": n, "rejected": n }`. Any other path is `404`; the listener
  has no authentication because it is reachable only inside the sandbox's
  network namespace.
- **CO-2.** Accepted records are queued per type and published as batches
  of at most 1000 records or every 5s, whichever first, each batch one
  envelope on `af.sbx.<id>.tel.<type>`.
- **CO-3.** While NATS is disconnected the queue SHALL hold up to 10 MB of
  encoded records, then drop the oldest; the number dropped since the last
  heartbeat is reported as `telemetry_dropped` in the heartbeat and reset.
- **CO-4.** Before publishing, the collector SHALL replace every occurrence
  of a value under `secret_keys` (and the token and NATS password) in
  `log.message`, `log.fields` and `event` payloads with `[redacted]`.
- **CO-5.** `afc outpost emit --type <event|usage|metrics|log>
  [--json <payload> | --json -]` and the shorthands `afc outpost emit log
  --level info "<message>"` and `afc outpost emit metric <name> <value>
  [--label k=v]...` SHALL post to `AF_COLLECTOR_URL` and exit 0 on `202`.
- **CO-6.** The outpost SHALL NOT tail, capture or forward the workload's
  stdout or stderr, `workdir` contents or shell history. This is a
  requirement, not an omission.

### Ingestion (hub side)

- **CO-7.** The hub SHALL subscribe to `af.sbx.*.tel.>` once and, per
  record: `event` → the audit store through the same validation as the
  HTTP ingestion endpoint, with `workspace_slug` and `sandbox_id` from the
  sandbox record (the sandbox's token identity is the actor); `usage` →
  the token usage store keyed by the record's `session_id`, rejected if
  that session does not belong to the sandbox's workspace; `metrics` → a
  `sandbox_metrics(sandbox_id, workspace_slug, name, labels JSON, value,
  at)` table in the audit store, and the latest value per name on the
  sandbox record (`metrics: { "name": value }`); `log` → a
  `sandbox_logs(sandbox_id, workspace_slug, level, message, fields JSON,
  at)` table in the audit store.
- **CO-8.** Records from a subject whose sandbox is unknown or terminal
  SHALL be dropped and counted. Malformed records SHALL be dropped and
  counted (`af_sandbox_telemetry_rejected_total{type,reason}`). Accepted
  records count in `af_sandbox_telemetry_total{type}`.
- **CO-9.** `GET /api/v1/audit` and `GET /api/v1/events` SHALL accept
  `sandbox_id=<uuid>` and, for owners of the workspace (admin bypasses),
  return that sandbox's events and logs alongside hub events. A new
  `GET /api/v1/workspaces/:slug/sandboxes/:id/logs?level=&since=&limit=`
  (`sandboxes:read`) returns `sandbox_logs` for one sandbox, newest first.
  `GET …/sandboxes/:id/metrics?name=&since=` returns samples.
- **CO-10.** `sandbox_metrics` and `sandbox_logs` SHALL be pruned by the
  retention worker with `AF_AUDIT_MAX_AGE_DAYS`, and deleted with the
  workspace.

### CLI

- **CO-11.** `afc sandbox logs <slug> <id> [--level] [--since] [--follow]`
  (follow uses the SSE stream with the `sandbox_id` filter) and
  `afc sandbox metrics <slug> <id> [--name]`.

### Testing

- Unit: collector validation, batching boundaries, buffer limit and drop
  counting, redaction; hub-side mapping per type, unknown sandbox
  handling, `usage` session ownership check.
- Integration: a workload in the outpost integration test posts each
  record type through the collector; the hub test asserts rows in the
  audit store and the `sandbox_id` filter on the audit query.

## Technical Boundaries

- Go; `internal/outpost/collector` and `internal/sandbox/telemetry`.
  Two new DuckDB tables in `internal/audit`'s schema.
- Documentation: `docs/api.md` (new endpoints, `sandbox_id` filter),
  `docs/cli.md`, `docs/sandbox_protocol.md` (collector HTTP API section).

## Dependencies

- PRD 23 (record shapes and subjects), PRD 25 (the outpost hosts the
  collector), PRD 24 (sandbox records for attribution), PRD 19 (the hub's
  NATS client).
- `internal/audit` ingestion and query paths.

## Design Decisions

### 1. The outpost is the collector

Agents could publish to NATS themselves, but then every agent would need
the sandbox's NATS credentials, its own client library and reconnect
logic, and a misbehaving agent could flood the hub. A loopback collector
gives agents a one-line HTTP call, keeps NATS credentials in one process,
and puts batching, buffering, redaction and back-pressure in one place.

### 2. Agent-provided telemetry only

The hub stores what an agent chose to report. Captured output is noisy,
hard to attribute to an agent action, and the most likely place for a
secret to leak. A person debugging a local sandbox has `podman logs`.

### 3. Reuse the HTTP ingestion paths

`event` and `usage` go through the same validation and storage as their
HTTP counterparts, so an agent can report either way and the audit query
does not care which.

## Open Questions

- **Transport.** Loopback HTTP because every agent runtime can `POST`
  JSON. A Unix socket would keep other processes in a shared network
  namespace out, at the cost of a less universal client story. Loopback in
  the sandbox's own namespace is proposed as good enough.
- **Log volume.** Should the collector cap `log` records per sandbox (for
  example 10 MB per lifetime) before the hub has to, and should the hub
  cap per sandbox as well?
