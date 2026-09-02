---
spec_id: '18'
spec_name: hub_audit_query
title: Hub Audit Query
status: draft
created_at: '2026-09-01T13:16:19.655365+00:00'
updated_at: '2026-09-01T13:54:01.667727+00:00'
owner: ''
source: docs/prd/prd14.md
schema_version: 1
---
# Hub Internal Audit and Query

## Source

File: `docs/prd/prd14.md` (split 2 of 3)

## Intent

With the audit storage layer and agent telemetry ingestion in place (spec
17_audit_storage_ingestion), the hub can receive and store agent data. This
spec adds two capabilities: hub-internal audit event emission from every
mutation the hub performs, and query/streaming interfaces for consuming audit
data.

Hub-internal emission means every mutation — workspace create, merge enqueue,
patch archive, secret update, git push — emits a structured audit event to
DuckDB via the `Emitter` interface defined in spec 17. The unified query API
merges hub-internal and agent-submitted events into a single chronological
view with rich filtering. The SSE streaming endpoint provides real-time event
push for dashboards.

## Goals

- Emit structured audit events from all hub mutation points (workspace, merge,
  carry-patch, secrets, variables, git server) using the `Emitter` interface.
- Provide a unified query API (`GET /api/v1/audit`) that merges hub-internal
  and agent-submitted events with filtering and cursor-based pagination.
- Reconstruct agent conversation transcripts from trace data.
- Provide an SSE streaming endpoint for real-time event delivery.
- Require no code changes in handler logic beyond a single emitter call.

## Non-Goals

- DuckDB setup, schema, or Store (spec 17: audit_storage_ingestion).
- Agent data ingestion endpoints (spec 17: audit_storage_ingestion).
- Agent session lifecycle (spec 19: sessions_metrics_retention).
- Prometheus metrics (spec 19: sessions_metrics_retention).
- Retention policies (spec 19: sessions_metrics_retention).
- Real-time alerting rules.

## Tech Stack

- Go 1.26+
- Echo v4
- apikit
- DuckDB (via foundation from spec 17)
- Test tooling: standard library `testing` package + `net/http/httptest` (no external assertion libraries). Tests use a real temp-file DuckDB instance, matching the convention established in spec 17.

## Functional Requirements

### Hub-Internal Audit Events

The hub emits audit events for all mutations via the `Emitter` interface. Each
hub event uses the `HubEvent` struct defined in spec 17 and is stored in the
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
| `workspace` | Workspace slug when scoped to a workspace |
| `metadata` | JSON object with action-specific details |
| `trace_id` | Nullable (populated when OTel is active, deferred) |

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
| gitserver | Push | `hub.git.push` | `{"head_sha": "...", "refs_updated": [...]}` | resource_type=`workspace` (git pushes are workspace-scoped operations) |

> **Note:** `hub.session.force_closed` (emitted when a session is force-closed due
> to workspace archive or delete) is **deferred to spec 19
> (sessions_metrics_retention)**. Spec 19 defines the full emission specification
> including `resource_type`, `resource_id`, the emitting package
> (`internal/sessions`), the corresponding REQ, task, and test case.

#### Emitter Injection Pattern

Existing handler packages receive an `Emitter` via their config structs (e.g.,
`MergeAPIConfig.Audit`, `RebuildAPIConfig.Audit`). The `nil` check pattern
allows existing tests and callers to work without providing an emitter:

```go
if cfg.Audit != nil {
    _ = cfg.Audit.Emit(c.Request().Context(), audit.HubEvent{...})
}
```

#### Packages Modified

| Package | Change |
|---|---|
| `internal/workspace` | Add `Audit audit.Emitter` to handler config. Emit from create, update, archive, delete, sync, reclone handlers. |
| `internal/merge` | Add `Audit audit.Emitter` to `MergeAPIConfig`. Emit from submit and completion/failure handlers. |
| `internal/carrypatch` | Add `Audit audit.Emitter` to config structs. Emit from patch create/delete and rebuild completion/failure. |
| `internal/secrets` | Add `Audit audit.Emitter` to route registration. Emit from CRUD handlers. |
| `internal/gitserver` | Add `Audit audit.Emitter` to config. Emit from post-push hook. |

### Unified Audit Event Query API

#### GET /api/v1/audit

Query audit events across all sources. Returns both hub-internal and
agent-submitted events in a unified view, ordered by timestamp.

Permission: `audit:read` (defined in spec 17: audit_storage_ingestion, declared
in `internal/audit/permissions.go`).

Query parameters:

| Parameter | Type | Description |
|---|---|---|
| `source` | string | Filter: `hub` or `agent` |
| `run_id` | string | Filter by agent run ID |
| `actor_id` | string | Filter by actor |
| `actor_type` | string | Filter by actor type |
| `resource_type` | string | Filter by resource type (hub events) |
| `action` | string | Filter by action (hub events) |
| `event_type` | string | Exact match |
| `event_type_prefix` | string | Prefix match (e.g., `hub.workspace`) |
| `severity` | string | Filter by severity |
| `workspace` | string | Filter by workspace slug |
| `since` | string (ISO 8601) | Events after (inclusive) |
| `until` | string (ISO 8601) | Events before (exclusive) |
| `limit` | integer | Page size, default 100, max 1000 |
| `cursor` | string | Opaque pagination cursor (see Cursor Encoding below) |

The `source` field in results distinguishes hub-internal (`"hub"`) from
agent-submitted (`"agent"`) events. The unified view queries both
`agent_audit_events` and `hub_audit_events` via `UNION ALL` ordered by
timestamp.

##### Response Schema

```json
{
  "events": [
    {
      "id": "uuid",
      "timestamp": "2026-09-01T13:16:19.655365Z",
      "source": "hub",
      "event_type": "hub.workspace.create",
      "severity": "info",
      "actor_id": "user-uuid",
      "actor_type": "api_key",
      "resource_type": "workspace",
      "resource_id": "my-workspace",
      "action": "create",
      "workspace": "my-workspace",
      "metadata": {},
      "trace_id": null,
      "run_id": null,
      "node_id": null,
      "session_id": null,
      "archetype": null
    }
  ],
  "next_cursor": "eyJ0cyI6IjIwMjYtMDktMDFUMTM6MTY6MTlaIiwiaWQiOiJ1dWlkIn0",
  "has_more": true
}
```

Fields present for all events: `id`, `timestamp`, `source`, `event_type`,
`severity`, `workspace`, `metadata`, `trace_id`.

Hub-specific fields (`actor_id`, `actor_type`, `resource_type`, `resource_id`,
`action`) are populated for hub events and serialized as `null` for agent events.

Agent-specific fields (`run_id`, `node_id`, `session_id`, `archetype`) are
populated for agent events and serialized as `null` for hub events.

##### Cursor Encoding

Cursors use URL-safe base64 without padding (RFC 4648 §5) encoding of a JSON
object `{"ts": "<ISO8601 timestamp>", "id": "<UUID>"}`. This is a keyset
pagination strategy consistent with all other paginated audit endpoints defined
in spec 17. All audit specs use the same cursor format.

##### Error Responses

| Condition | HTTP Status | Body |
|---|---|---|
| Invalid or expired cursor | 400 | `apikit.WriteAPIError` with message `"invalid cursor"` |
| DuckDB query failure | 500 | `apikit.WriteAPIError` with generic error message |
| Unauthenticated | 401 | Standard apikit 401 response |
| Insufficient scope | 403 | Standard apikit 403 response |

#### GET /api/v1/workspaces/:slug/runs/:run_id/transcript

Reconstructs a conversation transcript by querying `agent_traces` for a
specific run and node, ordered by timestamp. Returns `session.init`,
`assistant.message`, `tool.use`, and `tool.error` entries interleaved
chronologically.

Permission: `audit:read` (defined in spec 17: audit_storage_ingestion).

Query parameters:

| Parameter | Type | Required | Description |
|---|---|---|---|
| `node_id` | string | **Yes** | Node to reconstruct |

Response maps `agent_traces` rows to transcript messages using the actual DDL columns:
- `role` column → message role field
- `content` column → content (string)
- `tool_calls` column → tool_calls (JSON array or null)

Messages are ordered by `created_at ASC`. All rows matching the given `workspace_slug`, `run_id`, and `node_id` are included.

##### Response Schema

```json
{
  "run_id": "run-uuid",
  "node_id": "node-uuid",
  "messages": [
    {
      "role": "system",
      "content": "You are a helpful assistant...",
      "tool_name": null,
      "timestamp": "2026-09-01T13:16:19.655365Z"
    },
    {
      "role": "assistant",
      "content": "I will help you with that.",
      "tool_name": null,
      "timestamp": "2026-09-01T13:16:20.000000Z"
    },
    {
      "role": "tool_use",
      "content": "",
      "tool_name": "bash",
      "timestamp": "2026-09-01T13:16:21.000000Z"
    },
    {
      "role": "tool_error",
      "content": "command not found",
      "tool_name": "bash",
      "timestamp": "2026-09-01T13:16:22.000000Z"
    }
  ]
}
```

##### Error Responses

| Condition | HTTP Status | Body |
|---|---|---|
| Missing `node_id` query parameter | 400 | `apikit.WriteAPIError` with message `"node_id is required"` |
| No traces found for run_id + node_id | 200 | `{"run_id": "...", "node_id": "...", "messages": []}` |
| DuckDB query failure | 500 | `apikit.WriteAPIError` with generic error message |
| Unauthenticated | 401 | Standard apikit 401 response |
| Insufficient scope | 403 | Standard apikit 403 response |

Note: An unknown `run_id` returns an empty `messages` array (200 OK) — the
endpoint does not distinguish "no matching traces" from "run does not exist".

### SSE Streaming Endpoint

#### GET /api/v1/events

Real-time event stream using Server-Sent Events. Streams audit events (both
hub-internal and agent-submitted) as they are ingested.

Permission: `audit:read` (defined in spec 17: audit_storage_ingestion).

Query parameters:

| Parameter | Type | Description |
|---|---|---|
| `workspace` | string | Filter to specific workspace |
| `run_id` | string | Filter to specific agent run |
| `category` | string | Filter: `hub` or `agent` |

Authentication via `Authorization: Bearer` header. Browsers use `fetch()` with
`ReadableStream` (not `EventSource`) to pass auth headers. Standard
`EventSource` connections (which cannot set custom headers) are not supported
and will fail authentication in the same way as any other unauthenticated
request (401).

##### SSE Event Payload Schemas

Each SSE frame uses the format:

```
event: <event_type>
data: <JSON object>

```

**`audit_event`** — hub or agent audit event. The `data` JSON object matches
the unified audit event schema defined for `GET /api/v1/audit` (same fields,
same null conventions for source-specific fields).

**`session_outcome`** — agent session outcome. The `data` JSON object matches
the session outcome schema defined in spec 17.

**`heartbeat`** — keepalive. The `data` JSON object is:

```json
{"timestamp": "2026-09-01T13:16:19.655365Z"}
```

##### Error Responses

| Condition | HTTP Status | Body |
|---|---|---|
| SSE connection limit exceeded | 503 | `{"error": "too many SSE connections"}` |
| Unauthenticated | 401 | Standard apikit 401 response |
| Insufficient scope | 403 | Standard apikit 403 response |

##### SSE Connection Management

Heartbeat interval: **30 seconds** (hardcoded constant).
Stale-connection timeout: **60 seconds** (hardcoded constant).

Both values are intentionally not configurable. The heartbeat interval is half
the stale timeout by design, ensuring at least one heartbeat arrives before a
connection is declared stale. Making them independently configurable risks
operators breaking this invariant.

Maximum 100 concurrent SSE connections (configurable via
`AF_SSE_MAX_CONNECTIONS`). Connections exceeding this limit receive HTTP 503.
Stale connections (no reads for 60 seconds) are closed.

The broadcaster uses a fan-out channel pattern: the emitter pushes to a single
broadcast channel, and per-connection goroutines filter and forward. Per-client
buffered channel holds 256 events; when full, oldest events are dropped to
prevent blocking the broadcaster. Dropped events are logged at `debug` level.

The SSE connection manager is integrated with the `Emitter` — when the emitter
writes an event, it also broadcasts to SSE subscribers.

## New Files

| File | Contents |
|------|----------|
| `internal/audit/sse.go` | SSE connection manager, broadcaster, heartbeat goroutine |

## Modified Files

| File | Change |
|------|-------|
| `internal/audit/emitter.go` | Extend default emitter to broadcast to SSE on emit |
| `internal/audit/handlers.go` | Add unified query handler, transcript handler, SSE handler |
| `internal/audit/routes.go` | Register new GET routes |
| `internal/workspace/handlers.go` | Add `Audit` field to config, emit from mutation handlers |
| `internal/merge/api.go` | Add `Audit` field to `MergeAPIConfig`, emit from submit handler |
| `internal/merge/handler.go` | Emit from merge completion/failure handler |
| `internal/carrypatch/*.go` | Add `Audit` to config, emit from patch and rebuild handlers |
| `internal/secrets/*.go` | Add `Audit` to route registration, emit from CRUD handlers |
| `internal/gitserver/*.go` | Add `Audit` to config, emit from post-push handler |

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `AF_SSE_MAX_CONNECTIONS` | `100` | Maximum concurrent SSE connections |

Note: SSE heartbeat interval (30s) and stale-connection timeout (60s) are
hardcoded constants and are not configurable via environment variables.

## Dependencies

| Spec | From Group | To Group | Relationship |
|------|-----------|----------|--------------|
| 17_audit_storage_ingestion | 1 | 1 | Uses `Emitter` interface, `HubEvent` struct, `Store`, DuckDB schema, cursor encoding strategy, and `audit:read` permission scope |

Note: spec 19 (sessions_metrics_retention) depends on this spec. That
dependency is declared in spec 19's own Dependencies section.

## Design Decisions

1. **SSE backpressure: per-client 256-event buffer, drop oldest on full.**
   Prevents slow consumers from blocking the broadcaster. Dropped events are
   logged at `debug` level.

2. **Emitter nil-check pattern for backward compatibility.** Existing tests
   and callers continue working without providing an emitter. The `nil` check
   is a temporary bridge during incremental adoption.

3. **Unified query via UNION ALL.** Separate tables for agent and hub events
   (different schemas) with a unified view via SQL UNION ALL. This keeps each
   table's schema tight while presenting a coherent query interface.

4. **Cursor encoding: URL-safe base64 (RFC 4648 §5) over `{"ts", "id"}` JSON.**
   Consistent with spec 17's keyset pagination strategy. All audit specs share
   the same cursor format, making pagination behaviour predictable across
   endpoints.

5. **Unknown run_id returns 200 with empty messages, not 404.** The transcript
   endpoint is a query over trace data; it cannot distinguish "run does not
   exist" from "run has no traces". An empty result is the correct semantic.

6. **SSE heartbeat (30s) and stale timeout (60s) are hardcoded constants.**
   The 2:1 ratio is a correctness invariant — independent configurability would
   allow operators to break it. Only the connection cap (`AF_SSE_MAX_CONNECTIONS`)
   is configurable because it has no such coupling.

7. **Standard `EventSource` is unsupported for SSE.** Browsers must use
   `fetch()` with `ReadableStream` to supply the `Authorization: Bearer` header.
   Unauthenticated SSE connections are rejected with 401, same as any other
   endpoint.

8. **`audit:read` permission is defined in spec 17.** This spec only consumes
   it; all four audit permission scopes (`audit:read`, `audit:write`,
   `sessions:read`, `sessions:write`) are declared in
   `internal/audit/permissions.go` as part of spec 17.

9. **Test tooling: standard library only.** `testing` + `net/http/httptest` +
   real temp-file DuckDB. No external assertion libraries (e.g., testify),
   matching codebase convention from spec 17.
