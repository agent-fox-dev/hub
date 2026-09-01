# Audit Data Ingestion and Agent Session Tracking

## Intent

The hub coordinates workspaces, merges, patches, and git operations for
multiple agents and operators. Today, none of these operations leave a
structured trail. When something goes wrong -- a merge fails, a session costs
more than expected, an agent writes to the wrong workspace -- the only
diagnostic path is reading unstructured log output and manually correlating
timestamps. There is no way to answer "what happened to workspace X in the
last hour?" without SSH access and grep.

At the same time, nightshift agents running afaudit produce rich structured
telemetry: audit events, session outcomes, token usage, tool call records,
conversation traces, and postmortem reports. This data has no destination
beyond local JSONL files on the agent host, aged out by a file-count retention
policy. No central view exists.

This PRD defines a single internal package (`internal/audit`) that provides
two capabilities. First, hub-internal audit trail: every mutation the hub
performs -- workspace create, merge enqueue, patch archive, secret update, git
push -- emits a structured audit event to DuckDB. Second, agent telemetry
ingestion: the hub accepts afaudit-shaped data via REST endpoints, stores it
in DuckDB, and makes it queryable alongside hub-internal events. Both
capabilities use the same storage layer, the same query API, and the same
retention policy. A Prometheus-compatible `/metrics` endpoint exposes counters
and histograms for operational monitoring. An SSE streaming endpoint provides
real-time event push for dashboards and future UI work. The design prioritizes
compatibility with the existing afaudit data model so that adding the hub as a
remote sink requires minimal changes to the nightshift agent codebase --
ideally a single new `SessionSink` implementation that POSTs to the hub
instead of writing to local files.

## Goals

- Accept audit events from nightshift agents in the exact JSON format produced
  by afaudit's `event_to_json` serializer, requiring zero changes to the
  afaudit data model.
- Accept session outcomes, tool calls, tool errors, agent trace events, and
  postmortem reports from nightshift agents via dedicated REST endpoints.
- Provide batch ingestion endpoints for bulk upload of existing JSONL audit and
  trace files.
- Use client-generated UUIDs as idempotency keys so that retried submissions
  produce no duplicates.
- Store all ingested data in DuckDB with indexes that support the primary query
  patterns: by run, by workspace, by time range, by event type, by severity.
- Emit structured audit events from existing hub mutation points (workspace,
  merge, carry-patch, secrets, variables, git server) with no code changes
  required in handler logic beyond a single emitter call.
- Provide a query API for audit events with filtering by time range, actor,
  resource, action, event type, severity, and workspace, with cursor-based
  pagination.
- Track agent sessions with explicit open/close lifecycle and usage reporting.
- Expose a Prometheus-compatible `/metrics` endpoint with request counters,
  latency histograms, active session gauges, and job queue depth metrics.
- Provide an SSE streaming endpoint for real-time event delivery to dashboards
  and downstream consumers.
- Enforce retention policies to prevent unbounded database growth.
- Preserve the single-binary deployment model with zero mandatory external
  dependencies.

## Non-Goals

- **Replacing afaudit's local file sinks.** The hub sink is additive. Agents
  continue writing local JSONL files for offline access. The hub is a remote
  copy, not the sole store.
- **Real-time alerting.** The SSE endpoint enables consumers to build alerts,
  but the hub does not define alert rules, thresholds, or notification
  channels.
- **Full APM platform.** The hub emits telemetry to optional external backends
  via OTLP. It does not replace Grafana, Datadog, or Jaeger.
- **Agent memory or workspace context.** Cross-session memory, decision logs,
  and context assembly are a separate concern.
- **Dashboard or UI views.** This PRD defines the data layer and query API.
  Dashboard views that consume these endpoints are separate work.
- **Sandbox lifecycle events.** The schema accommodates future sandbox event
  types, but sandbox-specific event types are not defined here.
- **Modifying afaudit's AuditEventType enum.** The hub accepts all existing
  event types and any future additions. It does not define new event types for
  agents to emit.
- **Server-side postmortem computation.** The hub accepts pre-computed
  postmortem JSON from `build_postmortem()`. Porting afaudit's Python
  postmortem logic to Go is deferred to avoid tight coupling.
- **Log aggregation or full-text search.** Structured logging via slog is a
  prerequisite but log search and shipping are out of scope.
- **Multi-node aggregation.** The hub runs as a single process with a single
  DuckDB file. Aggregating audit data across multiple hub instances is out of
  scope.
- **Inferred session boundaries.** The hub does not attempt to infer sessions
  from credential activity patterns. Agents that do not explicitly open
  sessions simply have their audit data queryable by `run_id` without session
  association.

## Functional Requirements

### afaudit Data Model Compatibility

The hub API accepts data in the exact shapes produced by the afaudit package.
No field renaming, no envelope wrapping, no schema transformation. An agent
that currently calls `event_to_json(event)` and writes the result to a JSONL
file can POST that same JSON to the hub with no modification.

The following afaudit types map directly to hub API endpoints:

| afaudit Type | Hub Endpoint | Notes |
|---|---|---|
| `AuditEvent` (via `event_to_json`) | `POST /api/v1/workspaces/:slug/runs/:run_id/events` | Primary high-volume endpoint |
| `AuditEvent[]` (JSONL file contents) | `POST /api/v1/workspaces/:slug/runs/:run_id/events/batch` | Bulk upload |
| `SessionOutcome` | `POST /api/v1/workspaces/:slug/runs/:run_id/sessions/outcomes` | One per completed session |
| `ToolCall` | `POST /api/v1/workspaces/:slug/runs/:run_id/tools/calls` | One per successful tool invocation |
| `ToolError` | `POST /api/v1/workspaces/:slug/runs/:run_id/tools/errors` | One per failed tool invocation |
| AgentTraceSink event | `POST /api/v1/workspaces/:slug/runs/:run_id/traces` | Any of 5 trace event types |
| AgentTraceSink event[] | `POST /api/v1/workspaces/:slug/runs/:run_id/traces/batch` | Bulk trace upload |
| Postmortem output (precomputed) | `POST /api/v1/workspaces/:slug/runs/:run_id/postmortem` | Client-computed postmortem |

#### Field Defaults and Validation

All `AuditEvent` fields have defaults in the afaudit data model except `run_id`
and `event_type`. The hub enforces the same contract:

| Field | Required | Default if absent |
|---|---|---|
| `run_id` | Yes (from URL path) | -- |
| `event_type` | Yes | -- |
| `id` | No | Hub generates a UUID |
| `timestamp` | No | `apikit.NowUTC()` |
| `severity` | No | Per `default_severity_for()` mapping |
| `node_id` | No | `""` |
| `session_id` | No | `""` |
| `archetype` | No | `""` |
| `payload` | No | `{}` |

When `severity` is absent, the hub applies the same default severity mapping as
afaudit's `default_severity_for()`:

| Event Type | Default Severity |
|---|---|
| `session.fail` | `error` |
| `run.limit_reached` | `warning` |
| `git.conflict` | `warning` |
| `harvest.empty` | `warning` |
| `review.parse_failure` | `warning` |
| All others | `info` |

Valid severity values are: `info`, `warning`, `error`, `critical`.

#### run_id Format

The `run_id` follows the afaudit convention: `YYYYMMDD_HHMMSS_6hexchars`
(e.g., `20260704_143022_a1b2c3`). The hub validates this pattern on ingestion
endpoints via regexp. Invalid `run_id` values receive HTTP 400 with a
descriptive error. The `run_id` in the URL path must match the `run_id` in the
request body when both are present. A mismatch receives HTTP 400.

#### Event Type Validation

The hub accepts the `event_type` string value as-is. It does not reject unknown
event types -- this decouples hub deployment from afaudit releases. New event
types can be added to afaudit without requiring a coordinated hub upgrade.
Unknown event types are accepted, stored, and logged at `warning` level. Note
that 46 of the known types use `dot.notation` while one uses `SCREAMING_CASE`
(`SLEEP_COMPUTE_COMPLETE`). The hub accepts both conventions.

#### Immutability and Idempotency

All afaudit dataclasses are frozen (immutable). The hub treats ingested records
as append-only. There are no update or delete endpoints for agent-submitted
audit data. Records are only removed by retention policy.

Client-generated UUIDs (`AuditEvent.id`, `SessionOutcome.id`, `ToolCall.id`,
`ToolError.id`) serve as idempotency keys. If a record with the same `id`
already exists, the hub returns HTTP 200 (not 201) with the existing record
and does not modify it. This uses `INSERT OR IGNORE` at the DuckDB level and
makes all write endpoints safe for retry without requiring separate
idempotency token headers.

### Workspace Attribution

All agent-submitted data is attributed to a workspace at ingestion time. The
workspace slug is part of the URL path on every ingestion endpoint
(`/api/v1/workspaces/:slug/runs/:run_id/...`). The hub stores the workspace
slug alongside every agent record. This enables workspace-scoped queries
across all agent data tables without joins.

Since a workspace belongs to an org or user, recording the workspace is
sufficient for ownership attribution -- org and user can always be resolved
from the workspace record at query time.

The workspace is resolved via one of two paths depending on the credential
type:

#### Workspace-Scoped Tokens

A PAT scoped to a specific workspace carries the workspace in its credential
metadata. The hub validates that the `:slug` in the URL path matches the
token's workspace scope. A mismatch returns HTTP 403 with error type
`workspace_mismatch`. Workspace-scoped tokens are not linked to a user or
org -- they are bound to the workspace itself.

This is the expected path for nightshift agents, where each agent operates on
a single workspace and holds a workspace-scoped PAT with `audit:write` +
`sessions:write`.

#### Generic Tokens (API Keys and Unscoped PATs)

When the credential is an API key or a PAT without workspace scope, the
`:slug` in the URL path is the workspace identifier. The hub verifies that
the token owner has write access to the workspace before accepting the data.
For API keys, this means the key's owner must be a member of the org that
owns the workspace. For unscoped PATs, the PAT must have been granted access
to the workspace's org.

If the workspace does not exist, the hub returns HTTP 404. If the token owner
lacks write access, the hub returns HTTP 403 with error type
`workspace_access_denied`.

#### Admin Tokens

Admin tokens can submit data to any workspace without ownership checks.

### Audit Event Ingestion API

#### POST /api/v1/workspaces/:slug/runs/:run_id/events

Accepts a single `AuditEvent` in the `event_to_json` format.

**Request body:**

```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "timestamp": "2026-09-01T14:30:22.123456+00:00",
  "run_id": "20260901_143022_a1b2c3",
  "event_type": "session.start",
  "node_id": "build_login_page",
  "session_id": "sess_abc123",
  "archetype": "coder",
  "severity": "info",
  "payload": {"spec_name": "07_secrets_variables", "model": "claude-sonnet-4-20250514"}
}
```

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `id` | No | Generated UUID | Client-generated UUID used as idempotency key |
| `timestamp` | No | `apikit.NowUTC()` | ISO 8601 datetime with timezone |
| `event_type` | **Yes** | -- | Event type string (e.g., `session.start`) |
| `node_id` | No | `""` | Task graph node identifier |
| `session_id` | No | `""` | Session identifier within the run |
| `archetype` | No | `""` | Agent archetype (e.g., `coder`, `reviewer`) |
| `severity` | No | Per `default_severity_for()` | One of: `info`, `warning`, `error`, `critical` |
| `payload` | No | `{}` | Arbitrary JSON object with event-specific data |

**Success Response (201 Created):**

```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "run_id": "20260901_143022_a1b2c3",
  "event_type": "session.start",
  "severity": "info",
  "created_at": "2026-09-01T14:30:23.000000+00:00"
}
```

**Idempotency:** If an event with the same `id` already exists, the hub
returns 200 with the existing record. No update is performed.

#### POST /api/v1/workspaces/:slug/runs/:run_id/events/batch

Accepts an array of `AuditEvent` JSON objects for bulk upload.

**Request body:**

```json
{
  "events": [
    {"id": "...", "run_id": "...", "event_type": "run.start", "severity": "info", "payload": {}},
    {"id": "...", "run_id": "...", "event_type": "session.start", "severity": "info", "payload": {}}
  ]
}
```

**Constraints:**

| Field | Required | Constraints |
|-------|----------|-------------|
| `events` | **Yes** | Non-empty array. Maximum 1000 events per batch. |

Each event in the array follows the same schema as the single-event endpoint.
The `run_id` from the URL is applied to all events in the batch.

**Success Response (200 OK):**

```json
{
  "accepted": 42,
  "duplicates": 3,
  "errors": []
}
```

Events within a batch are processed in a single DuckDB transaction. Duplicates
(events whose `id` matches an existing record) are silently skipped and counted
separately -- not treated as errors. This makes batch endpoints safe for retry
of entire batches. If any event fails validation, that event is reported in the
`errors` array with its index and reason; valid events in the same batch are
still inserted.

**Error detail format:**

```json
{
  "errors": [
    {"index": 1, "id": "def-456", "message": "event_type is required"}
  ]
}
```

**Batch size exceeded:** Requests exceeding 1000 events receive HTTP 413.

#### GET /api/v1/workspaces/:slug/runs/:run_id/events

Query audit events for a specific run.

**Query Parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `event_type` | string | -- | Filter by event type (exact match) |
| `severity` | string | -- | Filter by severity (exact match) |
| `node_id` | string | -- | Filter by node ID (exact match) |
| `since` | string (ISO 8601) | -- | Events after this timestamp (inclusive) |
| `until` | string (ISO 8601) | -- | Events before this timestamp (exclusive) |
| `order` | string | `asc` | Sort order: `asc` or `desc` |
| `limit` | integer | `100` | Page size (1-1000) |
| `cursor` | string | -- | Opaque cursor from previous response |

**Success Response (200 OK):**

```json
{
  "events": [
    {
      "id": "550e8400-e29b-41d4-a716-446655440000",
      "timestamp": "2026-09-01T14:30:22.123456+00:00",
      "run_id": "20260901_143022_a1b2c3",
      "event_type": "session.start",
      "node_id": "build_login_page",
      "session_id": "sess_abc123",
      "archetype": "coder",
      "severity": "info",
      "payload": {"spec_name": "07_secrets_variables"}
    }
  ],
  "next_cursor": "eyJ0cyI6IjIwMjYtMDktMDFUMTQ6MzA6MjIuMTIzNDU2WiIsImlkIjoiNTUwZTg0MDAifQ",
  "has_more": false
}
```

Cursor-based pagination uses a base64-encoded composite of `(timestamp, id)`
to ensure stable iteration even as new events are inserted concurrently.

### Session Outcome Ingestion

#### POST /api/v1/workspaces/:slug/runs/:run_id/sessions/outcomes

Accepts a `SessionOutcome` record.

**Request body:**

```json
{
  "id": "660e8400-e29b-41d4-a716-446655440000",
  "spec_name": "07_secrets_variables",
  "task_group": "group_a",
  "node_id": "build_login_page",
  "touched_paths": ["internal/secrets/store.go", "internal/secrets/schema.go"],
  "status": "completed",
  "input_tokens": 125000,
  "output_tokens": 8500,
  "cache_read_input_tokens": 45000,
  "cache_creation_input_tokens": 12000,
  "duration_ms": 180000,
  "error_message": null,
  "response": "I have implemented the secrets store...",
  "created_at": "2026-09-01T14:33:22.123456+00:00",
  "is_transport_error": false
}
```

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `id` | No | Generated UUID | Idempotency key |
| `spec_name` | No | `""` | Spec being worked on |
| `task_group` | No | `""` | Task group within the spec |
| `node_id` | No | `""` | Task graph node identifier |
| `touched_paths` | No | `[]` | Files modified during the session |
| `status` | No | `""` | Outcome: `completed`, `failed`, `timeout` |
| `input_tokens` | No | `0` | Input tokens consumed |
| `output_tokens` | No | `0` | Output tokens consumed |
| `cache_read_input_tokens` | No | `0` | Cached input tokens read |
| `cache_creation_input_tokens` | No | `0` | Cached input tokens created |
| `duration_ms` | No | `0` | Session duration in milliseconds |
| `error_message` | No | `null` | Error message if session failed |
| `response` | No | `""` | Last assistant response text |
| `created_at` | No | `apikit.NowUTC()` | When the session completed |
| `is_transport_error` | No | `false` | Whether the failure was a transport error |

**Success Response:** HTTP 201 Created (or 200 if duplicate `id`).

```json
{
  "id": "660e8400-e29b-41d4-a716-446655440000",
  "run_id": "20260901_143022_a1b2c3",
  "node_id": "build_login_page",
  "status": "completed",
  "created_at": "2026-09-01T14:33:22.123456+00:00"
}
```

#### GET /api/v1/workspaces/:slug/runs/:run_id/sessions/outcomes

Query session outcomes for a run.

**Query Parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `node_id` | string | -- | Filter by node ID |
| `status` | string | -- | Filter by status |
| `order` | string | `asc` | Sort order by `created_at` |
| `limit` | integer | `100` | Page size (1-1000) |
| `cursor` | string | -- | Opaque pagination cursor |

### Tool Call and Tool Error Ingestion

#### POST /api/v1/workspaces/:slug/runs/:run_id/tools/calls

Record a tool invocation.

**Request body:**

```json
{
  "id": "770e8400-e29b-41d4-a716-446655440000",
  "session_id": "sess_abc123",
  "node_id": "build_login_page",
  "tool_name": "Edit",
  "called_at": "2026-09-01T14:31:05.123456+00:00"
}
```

**Success Response:** HTTP 201 Created (or 200 if duplicate `id`).

#### POST /api/v1/workspaces/:slug/runs/:run_id/tools/errors

Record a tool error.

**Request body:**

```json
{
  "id": "880e8400-e29b-41d4-a716-446655440000",
  "session_id": "sess_abc123",
  "node_id": "build_login_page",
  "tool_name": "Bash",
  "failed_at": "2026-09-01T14:31:10.123456+00:00"
}
```

**Success Response:** HTTP 201 Created (or 200 if duplicate `id`).

Both endpoints use `id` as an idempotency key.

#### GET /api/v1/workspaces/:slug/runs/:run_id/tools/calls and GET /api/v1/workspaces/:slug/runs/:run_id/tools/errors

Query tool calls or tool errors for a run. Both support filtering by
`node_id`, `session_id`, and `tool_name`, with cursor-based pagination.

### Agent Trace Ingestion

#### POST /api/v1/workspaces/:slug/runs/:run_id/traces

Accepts a single agent trace event. The `event_type` field discriminates the
payload schema:

| event_type | Payload Fields |
|---|---|
| `session.init` | `model_id`, `archetype`, `system_prompt`, `task_prompt` |
| `assistant.message` | `content` |
| `tool.use` | `tool_name`, `tool_input` (object, string values pre-truncated at 10000 chars) |
| `tool.error` | `tool_name`, `error_message` |
| `session.result` | `status`, `input_tokens`, `output_tokens`, `cache_read_input_tokens`, `cache_creation_input_tokens`, `duration_ms`, `is_error`, `error_message` |

The hub stores the payload as an opaque JSON object. It validates that
`event_type` is one of the five known values but does not validate individual
payload fields.

**Request body (example -- session.init):**

```json
{
  "id": "990e8400-e29b-41d4-a716-446655440000",
  "event_type": "session.init",
  "run_id": "20260901_143022_a1b2c3",
  "timestamp": "2026-09-01T14:30:22.000000+00:00",
  "node_id": "build_login_page",
  "payload": {
    "model_id": "claude-sonnet-4-20250514",
    "archetype": "coder",
    "system_prompt": "You are a coding agent...",
    "task_prompt": "Implement the secrets store..."
  }
}
```

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `id` | No | Generated UUID | Idempotency key |
| `event_type` | **Yes** | -- | One of: `session.init`, `assistant.message`, `tool.use`, `tool.error`, `session.result` |
| `node_id` | No | `""` | Task graph node identifier |
| `timestamp` | No | `apikit.NowUTC()` | When the event occurred |
| `payload` | No | `{}` | Event-type-specific data |

**Success Response:** HTTP 201 Created (or 200 if duplicate `id`).

#### POST /api/v1/workspaces/:slug/runs/:run_id/traces/batch

Accepts an array of trace events for bulk upload of `agent_{run_id}.jsonl`
file contents.

**Request body:**

```json
{
  "events": [
    {"id": "...", "event_type": "session.init", "run_id": "...", "payload": {}},
    {"id": "...", "event_type": "assistant.message", "run_id": "...", "payload": {}}
  ]
}
```

Same batch semantics as audit event batches: maximum 1000 events,
transactional, duplicates silently skipped.

**Success Response:** Same structure as the audit event batch endpoint.

#### GET /api/v1/workspaces/:slug/runs/:run_id/traces

Query trace events for a run. Supports filtering by `event_type` and
`node_id`, with cursor-based pagination. Results are ordered by `timestamp`.

### Transcript Reconstruction

#### GET /api/v1/workspaces/:slug/runs/:run_id/transcript

Reconstructs a conversation transcript by querying `agent_traces` for a
specific run and node, ordered by timestamp. Returns `session.init`,
`assistant.message`, `tool.use`, and `tool.error` entries interleaved
chronologically.

**Query parameters:**

| Parameter | Type | Required | Description |
|---|---|---|---|
| `node_id` | string | **Yes** | The node to reconstruct the transcript for |

**Success Response (200 OK):**

```json
{
  "run_id": "20260901_143022_a1b2c3",
  "node_id": "build_login_page",
  "messages": [
    {
      "role": "system",
      "content": "You are a coding agent...",
      "timestamp": "2026-09-01T14:30:22+00:00"
    },
    {
      "role": "assistant",
      "content": "I will implement the secrets store...",
      "timestamp": "2026-09-01T14:30:25+00:00"
    },
    {
      "role": "tool_use",
      "tool_name": "Edit",
      "content": "{\"file_path\": \"internal/secrets/store.go\", ...}",
      "timestamp": "2026-09-01T14:30:30+00:00"
    }
  ]
}
```

### Postmortem Ingestion

#### POST /api/v1/workspaces/:slug/runs/:run_id/postmortem

Accepts a pre-computed postmortem report (the output of `build_postmortem()`).

**Request body:**

```json
{
  "schema_version": 1,
  "run_id": "20260901_143022_a1b2c3",
  "run_status": "stalled",
  "started_at": "2026-09-01T14:30:00+00:00",
  "completed_at": "2026-09-01T15:15:00+00:00",
  "task_summary": {
    "total": 5, "completed": 3, "pending": 0,
    "blocked": 1, "failed": 1, "in_progress": 0
  },
  "cost_summary": {
    "total_cost_usd": 4.52, "total_input_tokens": 850000,
    "total_output_tokens": 62000, "total_sessions": 8
  },
  "blocked_tasks": [{"node_id": "build_api", "reason": "dependency not completed"}],
  "session_history": [
    {
      "node_id": "build_login",
      "attempt": 1,
      "status": "completed",
      "archetype": "coder",
      "model": "claude-sonnet-4-20250514",
      "duration_ms": 180000,
      "cost": 2.15,
      "error_message": null,
      "timestamp": "2026-09-01T14:30:22+00:00",
      "is_transport_error": false,
      "is_budget_exhausted": false,
      "is_non_retryable": false
    }
  ]
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `schema_version` | No (default 1) | Must be `1`. Unknown versions rejected with 422. |
| `run_status` | **Yes** | Terminal status: `stalled`, `block_limit`, `cost_limit`, `session_limit` |
| `started_at` | **Yes** | ISO 8601 timestamp |
| `completed_at` | **Yes** | ISO 8601 timestamp |
| `task_summary` | **Yes** | Object with `total`, `completed`, `pending`, `blocked`, `failed`, `in_progress` |
| `cost_summary` | **Yes** | Object with `total_cost_usd`, `total_input_tokens`, `total_output_tokens`, `total_sessions` |
| `blocked_tasks` | No (default `[]`) | Array of `{node_id, reason}` objects |
| `session_history` | No (default `[]`) | Array of session records |

**Success Response (201 Created):**

```json
{
  "id": "aa0e8400-e29b-41d4-a716-446655440000",
  "run_id": "20260901_143022_a1b2c3",
  "run_status": "stalled",
  "created_at": "2026-09-01T15:15:01.000000+00:00"
}
```

**Idempotency:** The `run_id` is unique. Submitting a postmortem for a run
that already has one returns 200 with the existing record.

#### GET /api/v1/workspaces/:slug/runs/:run_id/postmortem

Retrieve the postmortem for a run. Returns 404 if no postmortem exists.

### Hub-Internal Audit Events

The hub emits its own audit events for all mutations performed through the REST
API and git server. These events use a hub-specific schema stored in the
`hub_audit_events` table.

#### Hub Audit Event Schema

| Field | Description |
|---|---|
| `id` | Hub-generated UUID |
| `timestamp` | `apikit.FormatUTC(apikit.NowUTC())` |
| `event_type` | Dot-notation type (e.g., `hub.workspace.create`) |
| `actor_id` | `auth.UserID` from `apikit.GetAuthInfo(c)` |
| `actor_type` | `auth.CredentialType`: `admin_token`, `api_key`, `pat`, `system` |
| `resource_type` | One of: `workspace`, `merge`, `patch`, `secret`, `variable`, `job` |
| `resource_id` | The slug, ID, or key of the affected resource |
| `action` | One of: `create`, `update`, `delete`, `archive`, `push`, `merge`, `rebuild`, `sync`, `reactivate` |
| `workspace` | Workspace slug when the event is scoped to a workspace |
| `metadata` | JSON object with action-specific details |
| `trace_id` | W3C trace ID (nullable, populated when OTel is active) |

#### Emission Points

| Package | Operation | event_type | metadata |
|---|---|---|---|
| workspace | Create workspace | `hub.workspace.create` | `{"git_url": "...", "branch": "..."}` |
| workspace | Update workspace | `hub.workspace.update` | `{"fields": ["display_name", "description"]}` |
| workspace | Archive workspace | `hub.workspace.archive` | `{"head_sha": "..."}` |
| workspace | Reactivate workspace | `hub.workspace.reactivate` | `{}` |
| workspace | Delete workspace | `hub.workspace.delete` | `{}` |
| workspace | Sync workspace | `hub.workspace.sync` | `{"result": "fast_forward"}` |
| merge | Submit merge | `hub.merge.enqueue` | `{"target_branch": "...", "source_ref": "...", "job_id": "..."}` |
| merge | Complete merge | `hub.merge.complete` | `{"base_sha": "...", "merged_sha": "..."}` |
| merge | Fail merge | `hub.merge.fail` | `{"reason": "..."}` |
| carrypatch | Add patch | `hub.patch.create` | `{"branch_name": "...", "position": N}` |
| carrypatch | Remove patch | `hub.patch.delete` | `{"branch_name": "..."}` |
| carrypatch | Rebuild enqueue | `hub.rebuild.enqueue` | `{"job_id": "...", "patch_count": N}` |
| carrypatch | Rebuild complete | `hub.rebuild.complete` | `{"patches_applied": N}` |
| carrypatch | Rebuild fail | `hub.rebuild.fail` | `{"reason": "..."}` |
| secrets | Create secret | `hub.secret.create` | `{"scope": "user", "key": "..."}` |
| secrets | Update secret | `hub.secret.update` | `{"scope": "...", "key": "..."}` |
| secrets | Delete secret | `hub.secret.delete` | `{"scope": "...", "key": "..."}` |
| secrets | Create variable | `hub.variable.create` | `{"scope": "...", "key": "..."}` |
| secrets | Update variable | `hub.variable.update` | `{"scope": "...", "key": "..."}` |
| secrets | Delete variable | `hub.variable.delete` | `{"scope": "...", "key": "..."}` |
| gitserver | Push | `hub.git.push` | `{"head_sha": "...", "refs_updated": [...]}` |
| audit | Force-close session | `hub.session.force_closed` | `{"session_id": "...", "reason": "workspace archived"}` |

#### Audit Emitter Interface

The `internal/audit` package exposes an `Emitter` interface that hub packages
use to emit audit events without taking a dependency on the storage layer:

```go
type Emitter interface {
    Emit(ctx context.Context, event HubEvent) error
}
```

The `HubEvent` struct:

```go
type HubEvent struct {
    EventType    string         // e.g., "hub.workspace.create"
    ActorID      string         // from apikit.AuthInfo.UserID
    ActorType    string         // from apikit.AuthInfo.CredentialType
    ResourceType string         // e.g., "workspace"
    ResourceID   string         // e.g., the slug
    Action       string         // e.g., "create"
    Workspace    string         // workspace slug if applicable
    Metadata     map[string]any // operation-specific details
}
```

Existing handler packages receive an `Emitter` via their config structs (e.g.,
`MergeAPIConfig.Audit`, `RebuildAPIConfig.Audit`). The emitter writes to
DuckDB synchronously. Emit failures are logged via slog and swallowed -- audit
emission must never cause a handler to return an error to the client. Severity
defaults to `info` for all hub events.

### Audit Event Query API

#### GET /api/v1/audit

Query audit events across all sources. Returns both hub-internal and
agent-submitted events in a unified view, ordered by timestamp.

**Query parameters:**

| Parameter | Type | Description |
|---|---|---|
| `source` | string | Filter by source: `hub`, `agent` |
| `run_id` | string | Filter by agent run ID |
| `actor_id` | string | Filter by actor (user ID) |
| `actor_type` | string | Filter by actor type |
| `resource_type` | string | Filter by resource type (hub events) |
| `action` | string | Filter by action (hub events) |
| `event_type` | string | Filter by event type (exact match) |
| `event_type_prefix` | string | Prefix match (e.g., `hub.workspace` matches all workspace events) |
| `severity` | string | Filter by severity |
| `workspace` | string | Filter by workspace slug |
| `since` | string (ISO 8601) | Events after this timestamp |
| `until` | string (ISO 8601) | Events before this timestamp |
| `limit` | integer | Page size, default 100, max 1000 |
| `cursor` | string | Opaque cursor from previous response |

**Response (200 OK):**

```json
{
  "events": [
    {
      "id": "...",
      "timestamp": "2026-09-01T14:30:22.123456+00:00",
      "source": "hub",
      "event_type": "hub.workspace.create",
      "severity": "info",
      "actor_id": "user_abc",
      "actor_type": "api_key",
      "resource_type": "workspace",
      "resource_id": "my-workspace",
      "action": "create",
      "workspace": "my-workspace",
      "metadata": {"git_url": "https://github.com/org/repo"},
      "trace_id": null
    }
  ],
  "next_cursor": "eyJ0cyI6Ii4uLiIsImlkIjoiLi4uIn0",
  "has_more": true
}
```

The `source` field distinguishes hub-internal events (`"hub"`) from
agent-submitted events (`"agent"`). The unified view is constructed by querying
both `agent_audit_events` and `hub_audit_events` tables and merging results
ordered by timestamp.

### Agent Session Lifecycle API

Agent sessions track the lifecycle of an agent's interaction with the hub.
Sessions are created and closed explicitly via the API. The hub does not
attempt to infer sessions from credential activity patterns.

#### POST /api/v1/sessions

Open a new agent session.

**Request body:**

```json
{
  "id": "bb0e8400-e29b-41d4-a716-446655440000",
  "workspace_slug": "my-workspace",
  "run_id": "20260901_143022_a1b2c3",
  "node_id": "build_login_page",
  "archetype": "coder",
  "model": "claude-sonnet-4-20250514",
  "metadata": {"spec_name": "07_secrets_variables"}
}
```

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `id` | No | Generated UUID | Idempotency key |
| `workspace_slug` | **Yes** | -- | Workspace this session operates on |
| `run_id` | No | `""` | Agent run ID |
| `node_id` | No | `""` | Task graph node identifier |
| `archetype` | No | `""` | Agent archetype |
| `model` | No | `""` | Model identifier |
| `metadata` | No | `null` | Arbitrary JSON metadata |

The `credential_id` and `credential_type` are extracted from the authenticated
request's `apikit.AuthInfo` -- agents do not self-report their credentials.

**Success Response (201 Created):**

```json
{
  "id": "bb0e8400-e29b-41d4-a716-446655440000",
  "workspace_slug": "my-workspace",
  "credential_id": "af_key_abc123",
  "credential_type": "api_key",
  "status": "active",
  "started_at": "2026-09-01T14:30:22+00:00"
}
```

#### POST /api/v1/sessions/:id/complete

Close an active session with final status and usage data.

**Request body:**

```json
{
  "status": "completed",
  "input_tokens": 125000,
  "output_tokens": 8500,
  "cache_read_input_tokens": 45000,
  "cache_creation_input_tokens": 12000,
  "duration_ms": 180000,
  "error_message": null
}
```

**Success Response (200 OK):**

```json
{
  "id": "bb0e8400-e29b-41d4-a716-446655440000",
  "status": "completed",
  "ended_at": "2026-09-01T14:33:22+00:00"
}
```

Completing an already-completed session returns 200 (idempotent). Only the
session owner (same `credential_id`) or an admin can complete a session.

#### POST /api/v1/sessions/:id/usage

Report incremental token usage for an active session. May be called multiple
times during a session. Each call creates a new `token_usage` record.

**Request body:**

```json
{
  "id": "cc0e8400-e29b-41d4-a716-446655440000",
  "model": "claude-sonnet-4-20250514",
  "input_tokens": 25000,
  "output_tokens": 1500,
  "cache_read_tokens": 8000
}
```

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `id` | No | Generated UUID | Idempotency key |
| `model` | **Yes** | -- | Model identifier |
| `input_tokens` | No | `0` | Input tokens consumed |
| `output_tokens` | No | `0` | Output tokens consumed |
| `cache_read_tokens` | No | `0` | Cached tokens read |

The `workspace_slug` is resolved from the session record, not from the request
body.

**Success Response:** HTTP 201 Created.

#### GET /api/v1/sessions

List agent sessions with filtering.

**Query parameters:**

| Parameter | Type | Description |
|---|---|---|
| `workspace_slug` | string | Filter by workspace |
| `run_id` | string | Filter by run ID |
| `status` | string | Filter by status: `active`, `completed`, `failed`, `timeout` |
| `credential_type` | string | Filter by credential type |
| `since` | string (ISO 8601) | Sessions started after this timestamp |
| `order` | string | Sort order by `started_at`: `asc` or `desc` (default `desc`) |
| `limit` | integer | Page size, default 50, max 500 |
| `cursor` | string | Opaque pagination cursor |

**Response (200 OK):**

```json
{
  "sessions": [
    {
      "id": "...",
      "workspace_slug": "my-workspace",
      "run_id": "20260901_143022_a1b2c3",
      "node_id": "build_login_page",
      "credential_id": "key_abc",
      "credential_type": "api_key",
      "archetype": "coder",
      "model": "claude-sonnet-4-20250514",
      "status": "completed",
      "started_at": "2026-09-01T14:30:22+00:00",
      "ended_at": "2026-09-01T14:33:22+00:00",
      "token_summary": {
        "total_input_tokens": 125000,
        "total_output_tokens": 8500,
        "total_cache_read_tokens": 45000,
        "models_used": ["claude-sonnet-4-20250514"]
      },
      "duration_ms": 180000,
      "metadata": {}
    }
  ],
  "next_cursor": null,
  "has_more": false
}
```

The `token_summary` is computed by aggregating `token_usage` rows for the
session at query time.

#### GET /api/v1/sessions/:id/usage

Query token usage records for a session. Returns individual usage reports and
a pre-aggregated total.

### Cost Summary

#### GET /api/v1/workspaces/:slug/cost

Returns aggregated token usage for a workspace over a time period.

**Query parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `since` | string (ISO 8601) | Start of period (default: 24 hours ago) |
| `until` | string (ISO 8601) | End of period (default: now) |
| `group_by` | string | `day`, `session`, or `model` (default: `day`) |

**Response (200 OK):**

```json
{
  "workspace": "my-workspace",
  "period": {"since": "...", "until": "..."},
  "totals": {
    "input_tokens": 500000,
    "output_tokens": 45000,
    "cache_read_tokens": 200000,
    "sessions": 12
  },
  "breakdown": [
    {"date": "2026-09-01", "input_tokens": 250000, "output_tokens": 22000, "sessions": 6}
  ]
}
```

### SSE Streaming Endpoint

#### GET /api/v1/events

Real-time event stream using Server-Sent Events. Streams audit events (both
hub-internal and agent-submitted) as they are ingested. Uses Go stdlib
`net/http` Flusher with zero external dependencies.

**Query parameters:**

| Parameter | Type | Description |
|---|---|---|
| `workspace` | string | Filter to events affecting a specific workspace |
| `run_id` | string | Filter to events from a specific agent run |
| `category` | string | Filter by category: `hub`, `agent` |

**Authentication:** Bearer token via `Authorization` header. Browsers use
`fetch()` with `ReadableStream` (not `EventSource`) to pass auth headers.

**Event format:**

```
event: audit_event
data: {"id":"...","event_type":"hub.merge.complete","workspace":"my-workspace",...}

event: session_outcome
data: {"id":"...","run_id":"...","node_id":"...","status":"completed",...}

event: heartbeat
data: {"timestamp":"2026-09-01T14:30:30.000000+00:00"}
```

Heartbeat events are sent every 30 seconds to keep connections alive and
detect stale clients. The hub enforces a maximum of 100 concurrent SSE
connections (configurable via `AF_SSE_MAX_CONNECTIONS`). Connections exceeding
this limit receive HTTP 503. Stale connections (no reads for 60 seconds) are
closed by the server.

The broadcaster uses a fan-out channel pattern: the emitter pushes to a single
broadcast channel, and per-connection goroutines filter and forward.

### Prometheus Metrics Endpoint

#### GET /metrics

Prometheus-compatible metrics scrape endpoint using `promhttp` from
`github.com/prometheus/client_golang`. Metrics are registered on a custom
registry (not the global default) to avoid conflicts with apikit or other
dependencies.

**Metrics exposed:**

| Metric | Type | Labels | Description |
|---|---|---|---|
| `afhub_http_requests_total` | Counter | `method`, `path`, `status` | Total HTTP requests |
| `afhub_http_request_duration_seconds` | Histogram | `method`, `path` | Request latency |
| `afhub_audit_events_total` | Counter | `source`, `event_type` | Audit events ingested |
| `afhub_agent_sessions_active` | Gauge | `workspace` | Currently active agent sessions |
| `afhub_agent_tokens_total` | Counter | `workspace`, `model`, `direction` | Token usage (direction: input/output/cache_read) |
| `afhub_jobqueue_depth` | Gauge | `type`, `status` | Job queue depth by type and status |
| `afhub_sse_connections` | Gauge | -- | Active SSE connections |
| `afhub_audit_table_rows` | Gauge | `table` | Row count per audit table (sampled periodically) |

The `/metrics` endpoint is unauthenticated (standard Prometheus convention).
It is mounted on the raw Echo instance (not the API group) to avoid the auth
middleware chain, following the same pattern as `internal/gitserver` which
mounts git endpoints outside the API group.

HTTP latency histogram buckets: 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5,
1.0, 2.5, 5.0, 10.0 seconds.

Metrics are collected via an Echo middleware that wraps every request for HTTP
metrics, plus direct counter increments from the audit emitter and session
tracker.

## Database Schema

Audit, telemetry, and session data are stored in a dedicated DuckDB database
file, separate from the hub's operational SQLite database. DuckDB's columnar
storage engine provides significant advantages for audit workloads: analytical
queries (aggregations, time-range scans, group-by workspace/event_type) run
10-100x faster than equivalent SQLite queries on the same data. This matters
because audit data is write-once, read-many, and the primary access patterns
are analytical: "show all events for workspace X in the last hour", "aggregate
token usage by model across all sessions", "count events by severity over the
last 7 days".

The DuckDB file lives alongside the SQLite database at the same filesystem
location (configurable via `AF_AUDIT_DB_PATH`, default:
`<data_dir>/audit.duckdb`). The hub opens the DuckDB connection at startup
using `github.com/duckdb/duckdb-go` and passes it to the `internal/audit`
package. The SQLite database continues to store operational data (workspaces,
patches, secrets, variables, jobs); DuckDB stores only audit and telemetry
data.

Tables are created via `InitSchema(db *sql.DB) error` in the
`internal/audit` package using `CREATE TABLE IF NOT EXISTS`. DuckDB supports
this pattern natively. Schema migrations use `ALTER TABLE ... ADD COLUMN IF
NOT EXISTS` (supported in DuckDB without the `existingColumns()` workaround
needed for SQLite).

```sql
-- Agent audit events (afaudit AuditEvent records)
CREATE TABLE IF NOT EXISTS agent_audit_events (
    id            VARCHAR PRIMARY KEY,
    timestamp     TIMESTAMPTZ NOT NULL,
    run_id        VARCHAR NOT NULL,
    workspace     VARCHAR NOT NULL,
    event_type    VARCHAR NOT NULL,
    node_id       VARCHAR NOT NULL DEFAULT '',
    session_id    VARCHAR NOT NULL DEFAULT '',
    archetype     VARCHAR NOT NULL DEFAULT '',
    severity      VARCHAR NOT NULL DEFAULT 'info',
    payload       JSON NOT NULL DEFAULT '{}',
    ingested_at   TIMESTAMPTZ NOT NULL
);

-- Hub-internal audit events
CREATE TABLE IF NOT EXISTS hub_audit_events (
    id            VARCHAR PRIMARY KEY,
    timestamp     TIMESTAMPTZ NOT NULL,
    event_type    VARCHAR NOT NULL,
    actor_id      VARCHAR NOT NULL DEFAULT '',
    actor_type    VARCHAR NOT NULL DEFAULT 'system',
    resource_type VARCHAR NOT NULL DEFAULT '',
    resource_id   VARCHAR NOT NULL DEFAULT '',
    action        VARCHAR NOT NULL DEFAULT '',
    workspace     VARCHAR NOT NULL DEFAULT '',
    metadata      JSON DEFAULT '{}',
    trace_id      VARCHAR
);

-- Session outcomes (afaudit SessionOutcome records)
CREATE TABLE IF NOT EXISTS session_outcomes (
    id                          VARCHAR PRIMARY KEY,
    run_id                      VARCHAR NOT NULL,
    workspace                   VARCHAR NOT NULL,
    spec_name                   VARCHAR NOT NULL DEFAULT '',
    task_group                  VARCHAR NOT NULL DEFAULT '',
    node_id                     VARCHAR NOT NULL DEFAULT '',
    touched_paths               VARCHAR[] NOT NULL DEFAULT [],
    status                      VARCHAR NOT NULL DEFAULT '',
    input_tokens                BIGINT NOT NULL DEFAULT 0,
    output_tokens               BIGINT NOT NULL DEFAULT 0,
    cache_read_input_tokens     BIGINT NOT NULL DEFAULT 0,
    cache_creation_input_tokens BIGINT NOT NULL DEFAULT 0,
    duration_ms                 BIGINT NOT NULL DEFAULT 0,
    error_message               VARCHAR,
    response                    VARCHAR NOT NULL DEFAULT '',
    created_at                  TIMESTAMPTZ NOT NULL,
    is_transport_error          BOOLEAN NOT NULL DEFAULT false,
    ingested_at                 TIMESTAMPTZ NOT NULL
);

-- Tool calls (afaudit ToolCall records)
CREATE TABLE IF NOT EXISTS tool_calls (
    id          VARCHAR PRIMARY KEY,
    run_id      VARCHAR NOT NULL,
    workspace   VARCHAR NOT NULL,
    session_id  VARCHAR NOT NULL DEFAULT '',
    node_id     VARCHAR NOT NULL DEFAULT '',
    tool_name   VARCHAR NOT NULL DEFAULT '',
    called_at   TIMESTAMPTZ NOT NULL,
    ingested_at TIMESTAMPTZ NOT NULL
);

-- Tool errors (afaudit ToolError records)
CREATE TABLE IF NOT EXISTS tool_errors (
    id          VARCHAR PRIMARY KEY,
    run_id      VARCHAR NOT NULL,
    workspace   VARCHAR NOT NULL,
    session_id  VARCHAR NOT NULL DEFAULT '',
    node_id     VARCHAR NOT NULL DEFAULT '',
    tool_name   VARCHAR NOT NULL DEFAULT '',
    failed_at   TIMESTAMPTZ NOT NULL,
    ingested_at TIMESTAMPTZ NOT NULL
);

-- Agent trace events (AgentTraceSink records)
CREATE TABLE IF NOT EXISTS agent_traces (
    id          VARCHAR PRIMARY KEY,
    run_id      VARCHAR NOT NULL,
    workspace   VARCHAR NOT NULL,
    event_type  VARCHAR NOT NULL,
    node_id     VARCHAR NOT NULL DEFAULT '',
    timestamp   TIMESTAMPTZ NOT NULL,
    payload     JSON NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL
);

-- Postmortem reports
CREATE TABLE IF NOT EXISTS postmortems (
    id              VARCHAR PRIMARY KEY,
    run_id          VARCHAR NOT NULL UNIQUE,
    workspace       VARCHAR NOT NULL,
    schema_version  INTEGER NOT NULL DEFAULT 1,
    run_status      VARCHAR NOT NULL,
    started_at      TIMESTAMPTZ NOT NULL,
    completed_at    TIMESTAMPTZ NOT NULL,
    task_summary    JSON NOT NULL,
    cost_summary    JSON NOT NULL,
    blocked_tasks   JSON NOT NULL DEFAULT '[]',
    session_history JSON NOT NULL DEFAULT '[]',
    created_at      TIMESTAMPTZ NOT NULL
);

-- Agent sessions (hub-managed session lifecycle)
CREATE TABLE IF NOT EXISTS agent_sessions (
    id              VARCHAR PRIMARY KEY,
    workspace_slug  VARCHAR NOT NULL,
    run_id          VARCHAR NOT NULL DEFAULT '',
    node_id         VARCHAR NOT NULL DEFAULT '',
    credential_id   VARCHAR NOT NULL,
    credential_type VARCHAR NOT NULL,
    archetype       VARCHAR NOT NULL DEFAULT '',
    model           VARCHAR NOT NULL DEFAULT '',
    started_at      TIMESTAMPTZ NOT NULL,
    ended_at        TIMESTAMPTZ,
    status          VARCHAR NOT NULL DEFAULT 'active',
    duration_ms     BIGINT NOT NULL DEFAULT 0,
    error_message   VARCHAR,
    metadata        JSON
);

-- Token usage records (per-session, per-model)
CREATE TABLE IF NOT EXISTS token_usage (
    id                VARCHAR PRIMARY KEY,
    session_id        VARCHAR NOT NULL,
    workspace_slug    VARCHAR NOT NULL,
    model             VARCHAR NOT NULL,
    input_tokens      BIGINT NOT NULL DEFAULT 0,
    output_tokens     BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens BIGINT NOT NULL DEFAULT 0,
    reported_at       TIMESTAMPTZ NOT NULL
);
```

### Schema Design Notes

**DuckDB, not SQLite, for audit data.** The hub uses SQLite for operational
data (workspaces, patches, secrets, jobs) and DuckDB for audit and telemetry
data. This separation reflects the different access patterns: operational data
is row-oriented (lookup by primary key, small transactional updates), while
audit data is column-oriented (time-range scans, aggregations, group-by
queries, analytics). DuckDB's columnar storage, vectorized execution engine,
and built-in analytical functions (window functions, approximate aggregates)
make it the right tool for "what happened to workspace X in the last hour?"
queries. The nightshift codebase already uses DuckDB via `DuckDBSink` for
similar audit workloads, making this a proven pattern within the project.

**No explicit indexes.** DuckDB uses adaptive, automatic indexing via
zonemap-based min/max statistics on every column segment. Explicit `CREATE
INDEX` statements are unnecessary for the common query patterns (range scans
on `timestamp`, equality filters on `workspace`, `run_id`, `event_type`).
DuckDB's columnar layout means these filters are already fast. If specific
query patterns prove slow under load, ART indexes can be added later without
schema changes.

**Separate tables for agent and hub events.** Agent-submitted audit events and
hub-internal audit events have fundamentally different schemas. Agent events
are run-centric (`run_id`, `workspace`, `event_type`, `node_id`, `session_id`,
`archetype`, `severity`). Hub events are resource-centric (`actor_id`,
`actor_type`, `resource_type`, `resource_id`, `action`, `workspace`,
`trace_id`). Both schemas include `workspace` for scoped queries, but the
remaining columns differ. Forcing them into a single table requires many
nullable/empty columns. Separate tables keep each schema tight. The
`GET /api/v1/audit` query endpoint presents a unified view by querying both
tables via `UNION ALL` ordered by timestamp. The `workspace` filter works
across both tables.

**Workspace on every agent table.** Every agent-submitted data table includes
a `workspace` column populated at ingestion time from the URL path. This
denormalization avoids cross-table joins for workspace-scoped queries -- the
most common access pattern for dashboards and cost reports. Since a workspace
belongs to exactly one org or user, org-level aggregation can be performed by
joining the workspace table (in SQLite, via DuckDB's `sqlite_scanner`
extension or application-level join) at query time rather than storing
`org_id` redundantly on every audit row.

**Native DuckDB types.** The schema uses DuckDB-native types instead of
SQLite conventions:

| Concept | SQLite convention | DuckDB type | Benefit |
|---------|-------------------|-------------|---------|
| Strings | `TEXT` | `VARCHAR` | Same semantics, DuckDB convention |
| Timestamps | `TEXT` (ISO 8601 strings) | `TIMESTAMPTZ` | Native timestamp arithmetic, range comparisons without parsing |
| Booleans | `INTEGER` (0/1) | `BOOLEAN` | Native true/false, no conversion in Go code |
| JSON objects | `TEXT` (JSON-encoded strings) | `JSON` | Native JSON functions (`json_extract`, `json_array_length`) without parsing |
| Lists | `TEXT` (JSON arrays) | `VARCHAR[]` | Native array type for `touched_paths`, supports `array_contains()` |
| Token counts | `INTEGER` | `BIGINT` | Handles cumulative token counts exceeding 2^31 |

**Trace events in a single table with JSON payload.** The five trace event
types share the same access pattern (sequential replay within a run+node).
A single `agent_traces` table with a `payload` JSON column and an
`event_type` discriminator is simpler and sufficient. DuckDB's native JSON
type means payload fields can be queried directly
(`payload->>'model_id'`) without extracting to separate columns.

**Postmortem fields stored as JSON.** `task_summary`, `cost_summary`,
`blocked_tasks`, and `session_history` are stored as DuckDB `JSON` columns
in the `postmortems` table rather than fully normalized, because they are
always read and written as complete units. DuckDB's JSON functions allow
analytical queries into these fields when needed (e.g.,
`cost_summary->>'total_cost_usd'` for cost reporting).

### DuckDB Concurrency Model

DuckDB supports multiple concurrent readers with a single writer. This is
sufficient for the hub's audit workload: a single hub process writes audit
events from API handlers and the internal emitter, while multiple concurrent
readers serve query API requests, SSE streams, and metrics collection.

The `duckdb-go` driver opens the database with `database/sql` compatibility.
The hub opens one `*sql.DB` connection pool for DuckDB at startup (separate
from the SQLite pool). Batch ingestion uses explicit transactions. Single-event
inserts use individual statements.

For the write path, DuckDB's append-optimized columnar storage is well-suited
to the audit workload pattern: high-volume inserts of immutable records with
no updates. DuckDB batches small inserts internally, so even single-row
inserts benefit from columnar compression without explicit application-level
batching.

## API Endpoints

| Method | Endpoint | Permission | Description |
|--------|----------|------------|-------------|
| POST | `/api/v1/workspaces/:slug/runs/:run_id/events` | `audit:write` | Ingest single audit event |
| POST | `/api/v1/workspaces/:slug/runs/:run_id/events/batch` | `audit:write` | Ingest audit event batch |
| GET | `/api/v1/workspaces/:slug/runs/:run_id/events` | `audit:read` | Query events for a run |
| GET | `/api/v1/audit` | `audit:read` | Query events across all runs/sources |
| POST | `/api/v1/workspaces/:slug/runs/:run_id/sessions/outcomes` | `audit:write` | Record session outcome |
| GET | `/api/v1/workspaces/:slug/runs/:run_id/sessions/outcomes` | `audit:read` | Query session outcomes |
| POST | `/api/v1/workspaces/:slug/runs/:run_id/tools/calls` | `audit:write` | Record tool call |
| GET | `/api/v1/workspaces/:slug/runs/:run_id/tools/calls` | `audit:read` | Query tool calls |
| POST | `/api/v1/workspaces/:slug/runs/:run_id/tools/errors` | `audit:write` | Record tool error |
| GET | `/api/v1/workspaces/:slug/runs/:run_id/tools/errors` | `audit:read` | Query tool errors |
| POST | `/api/v1/workspaces/:slug/runs/:run_id/traces` | `audit:write` | Ingest single trace event |
| POST | `/api/v1/workspaces/:slug/runs/:run_id/traces/batch` | `audit:write` | Ingest trace event batch |
| GET | `/api/v1/workspaces/:slug/runs/:run_id/traces` | `audit:read` | Query traces for a run |
| GET | `/api/v1/workspaces/:slug/runs/:run_id/transcript` | `audit:read` | Reconstruct conversation transcript |
| POST | `/api/v1/workspaces/:slug/runs/:run_id/postmortem` | `audit:write` | Submit postmortem report |
| GET | `/api/v1/workspaces/:slug/runs/:run_id/postmortem` | `audit:read` | Retrieve postmortem report |
| POST | `/api/v1/sessions` | `sessions:write` | Open agent session |
| POST | `/api/v1/sessions/:id/complete` | `sessions:write` | Close agent session |
| POST | `/api/v1/sessions/:id/usage` | `sessions:write` | Report token usage |
| GET | `/api/v1/sessions` | `sessions:read` | List agent sessions |
| GET | `/api/v1/sessions/:id/usage` | `sessions:read` | Query token usage |
| GET | `/api/v1/workspaces/:slug/cost` | `sessions:read` | Workspace cost summary |
| GET (SSE) | `/api/v1/events` | `audit:read` | Real-time event stream |
| GET | `/metrics` | (unauthenticated) | Prometheus metrics |

### Permissions

Four new PAT permission scopes:

| Scope | Description |
|-------|-------------|
| `audit:read` | Query audit events, traces, postmortems, tool calls/errors, transcripts |
| `audit:write` | Submit audit events, traces, postmortems, tool calls/errors |
| `sessions:read` | Query agent sessions, token usage, cost summaries |
| `sessions:write` | Create/close agent sessions, report token usage |

Access control:

| Operation | Admin | API Key | PAT `audit:read` | PAT `audit:write` | PAT `sessions:read` | PAT `sessions:write` |
|-----------|-------|---------|------|------|------|------|
| GET audit/traces/postmortem | all | own | own | -- | -- | -- |
| POST audit/traces/postmortem | all | own | -- | own | -- | -- |
| GET sessions/usage/cost | all | own | -- | -- | own | -- |
| POST sessions, complete, usage | all | own | -- | -- | -- | own |
| GET /metrics | all | all | all | all | all | all |

Admin tokens and API keys have implicit full access. PATs require explicit
scope grants. A single PAT with `audit:write` + `sessions:write` is sufficient
for a nightshift agent to report all telemetry.

## Hub Integration Points

| Integration | Details |
|---|---|
| `cmd/af-hub/main.go` | Open DuckDB connection via `audit.OpenDB(auditDBPath)`. Call `audit.InitSchema(auditDB)`. Create `audit.NewStore(auditDB)`. Create `audit.NewEmitter(store)`. Pass emitter to config structs. Register `audit.RegisterRoutes(api, store, emitter)`. Mount `/metrics` on `server.Echo()`. Collect `audit.Permissions()` in `extraPerms`. Close DuckDB connection on shutdown. |
| `internal/workspace` | Add `Audit audit.Emitter` field to handler config. Emit from `handleCreateWorkspace`, `handleUpdateWorkspace`, `handleArchiveWorkspace`, `handleDeleteWorkspace`, `handleSync`, `handleReclone`. |
| `internal/merge` | Add `Audit audit.Emitter` field to `MergeAPIConfig`. Emit from `handleSubmitMerge`. Emit from `Handler.Handle()` on merge completion/failure. |
| `internal/carrypatch` | Add `Audit audit.Emitter` field to `RebuildAPIConfig` and rebuild handler. Emit from patch create/delete handlers and rebuild completion/failure. |
| `internal/secrets` | Add `Audit audit.Emitter` field to `RegisterRoutes` params. Emit from create/update/delete handlers for secrets and variables. |
| `internal/gitserver` | Add `Audit audit.Emitter` to git handler config. Emit from post-push hook. |
| Echo middleware | Register Prometheus metrics-counting middleware on `server.Echo()`. |

### Emitter Injection Pattern

Following the hub's established config struct pattern:

```go
// In internal/merge
type MergeAPIConfig struct {
    DB           *sql.DB
    Queue        *jobqueue.Queue
    BranchExists BranchChecker
    BatchRebase  BatchRebaseFunc
    Audit        audit.Emitter  // NEW: audit event emission
}

// In handleSubmitMerge
func handleSubmitMerge(cfg MergeAPIConfig) echo.HandlerFunc {
    return func(c echo.Context) error {
        // ... existing handler logic ...
        if cfg.Audit != nil {
            _ = cfg.Audit.Emit(c.Request().Context(), audit.HubEvent{
                EventType:    "hub.merge.enqueue",
                ActorID:      auth.UserID,
                ActorType:    auth.CredentialType,
                ResourceType: "merge",
                ResourceID:   jobID,
                Action:       "create",
                Workspace:    slug,
                Metadata:     map[string]any{"target_branch": req.TargetBranch, "source_ref": req.SourceRef},
            })
        }
        // ...
    }
}
```

The `nil` check allows existing tests and callers to continue working without
providing an emitter, maintaining backward compatibility during incremental
adoption.

## Workspace Lifecycle and Audit Data

Audit data is tied to a workspace via the `workspace` column on every agent
data table. When a workspace's lifecycle state changes, audit data must be
handled consistently.

### Archived Workspaces

Archiving a workspace sets its `status` to `archived`. The workspace row
remains in the database and can be reactivated.

**Ingestion behavior:** The hub rejects all write requests to archived
workspaces with HTTP 409 Conflict and error type `workspace_archived`. This
applies to all `/api/v1/workspaces/:slug/runs/...` POST endpoints. Agents
holding workspace-scoped tokens for an archived workspace receive 409 on
every submission attempt.

**Active sessions:** When a workspace is archived, the hub force-closes all
active `agent_sessions` for that workspace. Each session's `status` is set to
`terminated`, `ended_at` is set to the archive timestamp, and
`error_message` is set to `"workspace archived"`. This is performed within
the archive handler, after the workspace status update and before the
response is sent. A `hub.session.force_closed` audit event is emitted for
each terminated session.

**Query behavior:** Existing audit data for archived workspaces remains fully
queryable. All GET endpoints continue to work. The `GET /api/v1/audit`
unified query includes events from archived workspaces unless the caller
filters by workspace status.

**Reactivation:** When an archived workspace is reactivated, ingestion
resumes immediately. No audit data is modified or restored -- the data was
never removed.

### Deleted Workspaces

Deleting a workspace physically removes the workspace row. The hub's existing
delete handler cascade-deletes patches, secrets, and variables within a
single transaction.

**Audit data is retained, not cascade-deleted.** Audit data has forensic and
compliance value that outlives the workspace. Deleting a workspace does not
delete its audit events, session outcomes, tool calls, tool errors, traces,
postmortems, agent sessions, or token usage records. These records remain
queryable by `run_id`, `workspace` slug (even though the workspace row no
longer exists), and through the unified `GET /api/v1/audit` endpoint.

**Ingestion behavior:** After deletion, the workspace slug no longer resolves
to a workspace row. All write requests to the deleted workspace's slug
receive HTTP 404 (workspace not found). This is the normal workspace lookup
failure -- no special handling is needed.

**Orphaned data cleanup:** The retention policy's background worker handles
eventual cleanup. In addition to the time-based and run-count retention
rules, the worker identifies audit data referencing workspace slugs that no
longer exist in the `workspaces` table. Orphaned records are retained for
`AF_AUDIT_ORPHAN_RETENTION_DAYS` (default: 30 days after the workspace was
deleted) before deletion. This grace period allows operators to query
forensic data after a workspace is removed.

**Active sessions:** Same as archive -- any active sessions for the workspace
are force-closed before the workspace row is deleted. The force-close happens
within the delete transaction, before the cascade deletes.

### Workspace Lifecycle Summary

| Workspace State | Ingestion | Query | Active Sessions | Audit Data |
|-----------------|-----------|-------|-----------------|------------|
| Active | Accepted | Available | Allowed | Stored normally |
| Archived | Rejected (409) | Available | Force-closed | Retained, fully queryable |
| Deleted | Rejected (404) | Available by slug/run_id | Force-closed | Retained for grace period, then cleaned up |

## Retention Policies

The hub enforces retention policies via a background goroutine that runs every
hour. Unbounded audit data growth will eventually exhaust disk space.

Configuration via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `AF_AUDIT_MAX_AGE_DAYS` | `90` | Max age for audit events (both agent and hub) |
| `AF_AUDIT_MAX_RUNS` | `50` | Maximum distinct run_id values to retain for agent data |
| `AF_TRACE_MAX_AGE_DAYS` | `30` | Max age for agent trace data |
| `AF_SESSION_MAX_AGE_DAYS` | `90` | Max age for completed sessions |
| `AF_POSTMORTEM_MAX_AGE_DAYS` | `180` | Max age for postmortem reports |
| `AF_AUDIT_ORPHAN_RETENTION_DAYS` | `30` | Grace period for audit data after workspace deletion |

Retention logic:

1. Identify agent runs older than `max_age_days`. Delete their records from
   `agent_audit_events`, `session_outcomes`, `tool_calls`, `tool_errors`,
   `agent_traces`, and `postmortems`.
2. If the number of distinct `run_id` values exceeds `max_runs`, delete the
   oldest runs' data across all agent tables until the count is at or below
   `max_runs`. This matches afaudit's `enforce_file_retention` behavior.
3. Delete `hub_audit_events` where `timestamp` is older than `max_age_days`.
4. Delete completed `agent_sessions` where `ended_at` is older than
   `session_max_age_days`.
5. Delete orphaned `token_usage` rows whose `session_id` references a
   deleted session.
6. Delete `agent_traces` where `timestamp` is older than `trace_max_age_days`.
7. Delete `postmortems` where `completed_at` is older than
   `postmortem_max_age_days`.
8. Identify audit data where `workspace` does not match any row in the
   `workspaces` table and `ingested_at` (or `created_at`) is older than
   `orphan_retention_days`. Delete these orphaned records from all agent
   data tables.
9. Log the number of rows deleted per table via slog.

### Optional OpenTelemetry Export

When `OTEL_EXPORTER_OTLP_ENDPOINT` is set, the hub initializes an OTLP trace
exporter using the standard `go.opentelemetry.io/otel` SDK. When unset (the
default), a no-op tracer provider is used and no OTel dependencies are loaded
at runtime. This is a day-two enhancement -- the hub works fully without an
OTLP endpoint configured. Audit events and Prometheus metrics provide
sufficient local debugging.

Planned OTel integration points (for when the feature is enabled):

| Integration | Package | Implementation |
|---|---|---|
| HTTP tracing | Echo middleware | `otelecho` middleware |
| Job queue spans | jobqueue | Manual spans in worker loop |
| Trace context propagation | jobqueue | `traceparent` column on `jobs` table |

The `traceparent` column migration and OTel SDK initialization are deferred
until an operator or deployment actually requires distributed tracing. The
audit schema includes `trace_id` fields (nullable) to support future
correlation.

## New Internal Packages

| File | Contents |
|------|----------|
| `internal/audit/db.go` | `OpenDB(path string) (*sql.DB, error)` — opens DuckDB connection via `duckdb-go`, returns `database/sql` compatible handle |
| `internal/audit/schema.go` | DDL constants, `InitSchema(db *sql.DB) error`, table creation |
| `internal/audit/store.go` | `Store` struct wrapping `*sql.DB` (DuckDB) with CRUD methods for all audit tables |
| `internal/audit/emitter.go` | `Emitter` interface, default implementation wrapping Store + SSE broadcast |
| `internal/audit/handlers.go` | HTTP handler closures for all ingestion and query endpoints |
| `internal/audit/routes.go` | `RegisterRoutes(api *echo.Group, store *Store, emitter Emitter)` |
| `internal/audit/permissions.go` | `Permissions() []apikit.Permission` returning the four scopes |
| `internal/audit/auth.go` | Auth helpers: `auditIsPAT`, `auditHasScope`, scope-check wrappers |
| `internal/audit/types.go` | Request/response types, domain structs, `HubEvent` |
| `internal/audit/severity.go` | `defaultSeverityFor()` mapping (Go port of afaudit logic) |
| `internal/audit/validate.go` | `run_id` regexp validation, event type validation |
| `internal/audit/sse.go` | SSE connection manager, event broadcasting, heartbeat goroutine |
| `internal/audit/metrics.go` | Prometheus custom registry, metric definitions, middleware factory |
| `internal/audit/retention.go` | Background retention worker (run-count + time-based cleanup) |

## Error Handling

| Condition | HTTP Status | Error Type | Notes |
|-----------|-------------|------------|-------|
| Missing or invalid `run_id` format | 400 | -- | Must match `YYYYMMDD_HHMMSS_[0-9a-f]{6}` |
| Missing required field (`event_type`) | 400 | -- | Descriptive error message |
| `run_id` in body does not match URL | 400 | -- | -- |
| Invalid `severity` value | 400 | -- | Must be one of: `info`, `warning`, `error`, `critical` |
| Invalid `payload` (not a JSON object) | 400 | -- | -- |
| Batch exceeds size limit | 413 | -- | 1000 for events/traces |
| Unknown `schema_version` in postmortem | 422 | -- | Only version 1 accepted |
| Empty `events` array in batch | 400 | -- | -- |
| Unknown trace `event_type` | 400 | -- | Must be one of the five known types |
| Duplicate `id` (idempotent) | 200 | -- | Existing record unchanged |
| Session not found | 404 | -- | -- |
| Session already completed | 200 | -- | Idempotent |
| Unauthenticated request | 401 | -- | -- |
| Insufficient PAT scope | 403 | -- | Descriptive scope requirement |
| Workspace not found | 404 | -- | `:slug` does not match any workspace |
| Workspace is archived | 409 | `workspace_archived` | Ingestion rejected for archived workspaces |
| Workspace-scoped token mismatch | 403 | `workspace_mismatch` | Token's workspace scope does not match `:slug` |
| Token owner lacks workspace access | 403 | `workspace_access_denied` | Generic token owner has no write permission on workspace |
| Invalid JSON body | 400 | -- | -- |
| SSE connection limit exceeded | 503 | -- | -- |
| DuckDB write failure | 500 | -- | Logged; client receives generic error |

All error responses use `apikit.WriteAPIError(c, code, message)`. For typed
errors, `apikit.WriteAPIErrorWithType(c, code, message, errorType)` adds an
`error_type` field.

## Configuration

All configuration via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `AF_AUDIT_DB_PATH` | `<data_dir>/audit.duckdb` | Path to the DuckDB database file for audit data |
| `AF_AUDIT_MAX_AGE_DAYS` | `90` | Max age for audit events |
| `AF_AUDIT_MAX_RUNS` | `50` | Max distinct run_ids to retain |
| `AF_TRACE_MAX_AGE_DAYS` | `30` | Max age for trace data |
| `AF_SESSION_MAX_AGE_DAYS` | `90` | Max age for completed sessions |
| `AF_POSTMORTEM_MAX_AGE_DAYS` | `180` | Max age for postmortems |
| `AF_AUDIT_ORPHAN_RETENTION_DAYS` | `30` | Grace period for audit data after workspace deletion |
| `AF_SSE_MAX_CONNECTIONS` | `100` | Maximum concurrent SSE connections |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | (unset) | OTLP endpoint for trace export (optional) |

## Dependencies

| Dependency | Relationship |
|------------|--------------|
| apikit | Auth middleware, `WriteAPIError`, `NowUTC`/`FormatUTC`, `GetAuthInfo`, SQLite database connection (operational data) |
| `github.com/duckdb/duckdb-go` | New dependency: DuckDB Go driver (`database/sql` compatible) for audit and telemetry storage |
| `github.com/prometheus/client_golang` | New dependency: Prometheus metrics endpoint |
| `internal/workspace` | Workspace lookups for cost queries. Hub-internal event emission from workspace mutations. |
| `internal/merge` | Hub-internal event emission from merge operations. `MergeAPIConfig` gains `Audit` field. |
| `internal/carrypatch` | Hub-internal event emission from rebuild and patch operations. Config structs gain `Audit` field. |
| `internal/secrets` | Hub-internal event emission from secret/variable mutations. |
| `internal/gitserver` | Hub-internal event emission from git push. |
| `internal/jobqueue` | Job queue metrics exposure (queue depth gauge). Future: `traceparent` column for trace propagation. |
| afaudit (external, Python) | Defines the data model this PRD accepts. Future `HubSink` implementation on the agent side. |

## afaudit HubSink (Agent-Side Reference)

For completeness, this section describes the intended integration on the
afaudit side. This is NOT a hub implementation task but documents the expected
client behavior.

A new `HubSink` class implementing the `SessionSink` protocol:

| SessionSink Method | Hub API Endpoint |
|--------------------|-----------------|
| `emit_audit_event(event)` | `POST /api/v1/workspaces/{workspace}/runs/{run_id}/events` |
| `record_session_outcome(outcome)` | `POST /api/v1/workspaces/{workspace}/runs/{run_id}/sessions/outcomes` |
| `record_tool_call(call)` | `POST /api/v1/workspaces/{workspace}/runs/{run_id}/tools/calls` |
| `record_tool_error(error)` | `POST /api/v1/workspaces/{workspace}/runs/{run_id}/tools/errors` |
| `close()` | Flush buffered events |
| `record_session_init(...)` | `POST /api/v1/workspaces/{workspace}/runs/{run_id}/traces` |
| `record_assistant_message(...)` | `POST /api/v1/workspaces/{workspace}/runs/{run_id}/traces` |
| `record_tool_use(...)` | `POST /api/v1/workspaces/{workspace}/runs/{run_id}/traces` |
| `record_tool_error_trace(...)` | `POST /api/v1/workspaces/{workspace}/runs/{run_id}/traces` |
| `record_session_result(...)` | `POST /api/v1/workspaces/{workspace}/runs/{run_id}/traces` |

The `HubSink` is constructed with a `workspace_slug` (in addition to
`hub_url`, `api_key`, and `run_id`). All endpoint URLs are built using
`/api/v1/workspaces/{workspace}/runs/{run_id}/...`. When using a
workspace-scoped PAT, the workspace slug in the URL must match the token's
scope.

The `HubSink` would buffer audit events and trace events independently,
flushing in batches (50 events or 5 seconds, whichever comes first) to their
respective batch endpoints. Session outcomes, tool calls, and tool errors
would be sent immediately (low-volume, confirmation needed). On persistent
failure (3 consecutive HTTP errors), the sink would fall back to local JSONL
writing using the existing `AuditJsonlSink` as degraded-mode backup.

Hub URL, API key, and workspace configured via `AF_HUB_URL`,
`AF_HUB_API_KEY`, and `AF_HUB_WORKSPACE` environment variables.

## Open Questions

1. **Event type prefix for hub events.** Hub-internal events use the `hub.`
   prefix (e.g., `hub.workspace.create`). Should agent events use an `agent.`
   prefix, or continue using afaudit's bare event types (`session.start`,
   `run.limit_reached`)? Using bare types maintains compatibility with afaudit;
   adding a prefix would require agent-side changes but improve clarity in the
   unified query view.

2. **Postmortem server-side computation.** This PRD defers server-side
   postmortem computation (accepting `PostmortemInput` and running
   `build_postmortem()` in Go). If there is demand for agents that cannot or
   should not run postmortem logic locally, a `POST /api/v1/workspaces/:slug/runs/:run_id/postmortem/compute` endpoint could be added later without
   changing the existing contract.

3. **Batch size limits.** The current limits (1000 for events, 1000 for traces)
   are conservative estimates. Should these be configurable via environment
   variables, or are fixed limits sufficient?

4. **OTel dependency timing.** OTel SDK packages
   (`go.opentelemetry.io/otel`, `otelecho`, `otelsql`) are listed as optional
   dependencies. Should they be added to `go.mod` immediately (even if the
   no-op path means they are never initialized), or deferred until the first
   operator requests distributed tracing?
