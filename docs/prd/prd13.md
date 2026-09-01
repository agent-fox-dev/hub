# Platform Extensions: Sandboxes, Telemetry, and UI/UX

## Intent

The hub provides the coordination infrastructure for spec-driven, multi-agent
software development: workspaces, git hosting, merge queues, carry-patch
workflows, auth, and secrets. Three areas remain before the platform supports
end-to-end agentic work on production codebases:

1. **Agent sandboxes.** Containerized environments that limit the blast radius
   of agentic work. Today, agents operate with whatever privileges the host OS
   allows. A sandbox constrains filesystem, network, and process access per
   workspace session.
2. **Monitoring, telemetry, and agent memory.** Structured observability for
   hub operations and agent sessions. Today, the hub has no structured
   telemetry, no session tracking, no cost attribution, and no cross-session
   memory for agents.
3. **UI/UX.** Surfaces that make the hub's API usable by humans — a web
   dashboard, terminal UI, and IDE integration. Today, the web scaffold exists
   but contains no functional UI.

These three areas are interdependent: sandbox lifecycle events feed telemetry,
telemetry data populates the UI, and the UI triggers sandbox operations. This
proposal treats them as a single initiative with a phased implementation
sequence.

## Goals

- Provide containerized sandbox isolation for agent sessions, backed by
  Kubernetes-native primitives, with sub-second provisioning for interactive
  use.
- Provide structured audit logging, metrics, and distributed tracing across
  all hub operations, with optional OpenTelemetry export.
- Provide agent session tracking with cost attribution per workspace and
  organization.
- Provide workspace-scoped agent memory that persists across sessions.
- Provide a functional web dashboard for workspace management, job monitoring,
  and patch health.
- Provide a GitHub Copilot Agent Plugin for IDE-native hub access via MCP.
- Provide an interactive terminal UI mode in the `afc` CLI.
- Deliver incrementally: each phase produces a usable, self-contained
  capability with no external infrastructure requirements until Phase 3.

## Non-Goals

- **Agent orchestration or scheduling.** This proposal provides sandbox
  infrastructure, not campaign runners, DAGs, or agent dispatch systems.
- **Custom container runtime development.** The hub delegates isolation to
  existing runtimes (runc, gVisor, Kata) via Kubernetes RuntimeClass.
- **Full APM platform.** The hub emits telemetry; it does not replace Grafana,
  Datadog, or Jaeger. External backends are optional consumers.
- **Hosted SaaS offering.** The hub remains self-hosted. No multi-tenant
  billing, usage metering, or SaaS control plane.
- **Mobile or native desktop UI.** The web dashboard, TUI, and IDE integration
  cover the primary developer surfaces.

---

## Part 1: Agent Sandboxes

### Context

The hub isolates agent work at the git level — each workspace has its own
branch and clone. But agents run with full host OS access. A compromised or
misbehaving agent can read other workspaces, exfiltrate secrets, consume
unbounded resources, or make arbitrary network calls.

### Reference Projects

| Project | Approach | Maturity | Key Insight |
|---|---|---|---|
| [Kubernetes Agent Sandbox](https://github.com/kubernetes-sigs/agent-sandbox) | CRD + warm pools + Go SDK | Beta | Go SDK maps directly to hub patterns; SandboxWarmPool enables sub-second allocation |
| [NVIDIA OpenShell](https://github.com/NVIDIA/OpenShell) | Gateway + policy enforcement (Landlock, OPA, seccomp) | Alpha | Out-of-process policy enforcement across 4 domains; K8s driver depends on Agent Sandbox CRDs |
| [E2B](https://github.com/e2b-dev/e2b) | Firecracker microVMs, managed cloud | Production | ~78ms median startup, snapshot restore; reference for fast provisioning |
| [Daytona](https://daytona.io) | Docker-based, SDK-driven lifecycle | Production | ~90ms startup; reference for lifecycle management patterns |

### Design Options

#### Option A: Container-per-Workspace with NetworkPolicy (Low Complexity)

Add a `sandbox_id` field to the Workspace model. When an agent session starts,
the hub creates a standard Kubernetes Pod in a dedicated namespace with
NetworkPolicy restricting egress to the hub's git server and API endpoints.
Secrets are injected as environment variables. No special runtime — standard
runc with dropped capabilities and seccomp profiles.

**Pros:**
- Uses standard Kubernetes primitives the hub already deploys on
- No additional infrastructure dependencies
- NetworkPolicy provides meaningful network isolation

**Cons:**
- Standard containers share the host kernel — container escape gives node access
- Cold start latency of 5-30 seconds without warm pooling
- No filesystem isolation beyond Linux namespaces

#### Option B: Agent Sandbox CRD with SandboxWarmPool (Medium Complexity) — Recommended

Integrate the Kubernetes Agent Sandbox Go SDK
(`sigs.k8s.io/agent-sandbox/clients/go/sandbox`) into the hub. Define
SandboxTemplate CRs for workspace types (pre-configured with git, language
runtimes, and hub CLI). Deploy SandboxWarmPool CRs to maintain pre-warmed
sandbox pods for sub-second allocation. Map workspace lifecycle
(pending/ready/archived) to sandbox lifecycle (create/pause/resume/delete).
Use gVisor RuntimeClass on GKE or Kata Containers on OpenShift for kernel-level
isolation.

The Go SDK provides `CreateSandbox`, `Run`, `Read`, `Write`, `List`, and
`DeleteAll` — these map to the hub's existing patterns (context-based, error
returns).

**Pros:**
- Sub-second sandbox allocation via SandboxWarmPool
- Go SDK maps cleanly to hub codebase
- Isolation upgradeable via RuntimeClass without changing hub code
- Red Hat ships a downstream build for OpenShift

**Cons:**
- Requires installing Agent Sandbox controller and CRDs on the cluster
- SandboxWarmPool consumes cluster resources when idle
- Project is beta (v1beta1 API) — potential breaking changes

#### Option C: Agent Sandbox CRD + OpenShell Policy Layer (High Complexity)

Build on Option B by deploying NVIDIA OpenShell alongside the Agent Sandbox
controller. OpenShell's gateway enforces declarative YAML policies for
filesystem access (Landlock LSM), network traffic (OPA-backed proxy with L7
filtering), process execution (seccomp), and inference routing. Credentials
are injected via OpenShell's provider abstraction, never written to the
sandbox filesystem.

**Pros:**
- Fine-grained, out-of-process policy enforcement across 4 domains
- L7 network filtering (allow git clone but block arbitrary HTTP)
- Credential injection via proxy — keys never enter sandbox

**Cons:**
- OpenShell is alpha-quality with experimental Kubernetes support
- Three operators to manage (Agent Sandbox, OpenShell gateway, OpenShell operator)
- OpenShell's K8s operator is in early development

#### Option D: Firecracker MicroVM with Snapshot Restore (High Complexity)

Deploy Firecracker microVMs as the sandbox runtime, either via Kata Containers
(Firecracker VMM backend) or direct management. Use VM snapshots for ~120ms
startup from a fully-booted state.

**Pros:**
- Strongest isolation — each agent gets its own kernel
- 5MB per-VM memory overhead enables high density
- Snapshot restore provides ~120ms startup

**Cons:**
- Requires KVM on cluster nodes — not available in all environments
- Nested virtualization overhead in cloud VMs
- Snapshot management is significant operational overhead

### Hub Integration Points

| Integration | Details |
|---|---|
| Workspace model | Add `sandbox_id`, `sandbox_status`, `sandbox_runtime` fields. Map `clone_status` transitions to sandbox lifecycle. |
| Job queue | Register `sandbox_provision` job type alongside existing merge and clone jobs. Handler calls Agent Sandbox SDK and updates workspace `sandbox_status`. |
| Git server | Hub's `/git/<org>/<slug>.git` must be reachable from sandboxes. Requires K8s Service for hub + NetworkPolicy allowing sandbox-to-hub egress. |
| Secrets injection | Extend `internal/secrets/get_secret_value.go` to resolve workspace-scoped secrets and inject as environment variables into sandbox pod specs. |
| Auth tokens | Generate short-lived, workspace-scoped PATs at sandbox creation for agent-to-hub API/git auth. |

### Open Questions

1. Should the hub enforce a specific RuntimeClass (Kata on OpenShift, gVisor on
   GKE) or make it a per-workspace/per-org configuration?
2. Should sandboxes have internet access (for npm/PyPI), access through a proxy
   with allowlist, or be fully air-gapped with pre-baked images?
3. When a sandbox is paused, should filesystem state persist on a PVC or be
   discarded? Persistence enables resume-where-left-off but increases storage
   costs.
4. Can multiple agents share a single sandbox, or does each agent get its own?
5. What agent connection model — exec (SDK `Run`), SSH, or custom protocol?
   Existing agents (Claude Code, Codex) expect a shell-like environment.

---

## Part 2: Monitoring, Telemetry, and Agent Memory

### Context

The hub has no structured telemetry. Operations succeed or fail silently.
There is no way to answer: which agent is working on which workspace? How long
did that rebuild take? What did the last merge attempt look like? How much did
this workspace cost to operate? What context should an agent have when it
starts a new session?

### Design Options

#### Option A: Structured Logging + SQLite Audit Table + Basic Metrics (Low Complexity)

Unify all logging on `slog` with consistent field names (`workspace`,
`user_id`, `operation`, `duration_ms`). Create an `audit_events` SQLite table.
Add a Prometheus-compatible `/metrics` endpoint with basic counters and
histograms. Expose audit events via `GET /api/v1/audit`.

**Pros:**
- Minimal dependencies — slog is stdlib, promhttp is a single import
- Immediately useful for debugging and operations
- Audit table provides compliance foundation

**Cons:**
- No distributed tracing across handler → job queue → git operation
- No agent session tracking or cost attribution
- Metrics are process-local without an external scraper

#### Option B: OpenTelemetry Traces + Metrics + Agent Session API (Medium Complexity) — Recommended

Add the full OpenTelemetry Go SDK with `otelecho` for HTTP tracing, `otelsql`
for database tracing, and manual spans in the job queue worker loop. Propagate
trace context through job records via a `traceparent` column. OTLP exporter
configurable via `OTEL_EXPORTER_OTLP_ENDPOINT` with a no-op default.

Introduce an `agent_sessions` table populated by tracking credential usage
through auth middleware. Add a `token_usage` table with a voluntary
`POST /api/v1/sessions/:id/usage` reporting endpoint.

**Pros:**
- End-to-end distributed traces from HTTP → job queue → git operations
- Agent session tracking with cost attribution per workspace/org
- OTLP export is optional — hub works without external infrastructure

**Cons:**
- Adds opentelemetry-go SDK, otelecho, otelsql dependencies
- Job queue schema migration needed for `traceparent` column
- Session boundaries require defining what a "session" means for headless clients

#### Option C: Full OTEL Stack + Agent Memory + Context Management (High Complexity)

Everything in Option B, plus workspace-scoped memory combining in-repo context
files (CLAUDE.md, `.agent-fox/context/`) with SQLite-stored structured memory.
A decisions log per workspace records architectural decisions and error
patterns. A `GET /api/v1/workspaces/:slug/context` endpoint returns merged
context that agents fetch at session start. OTel GenAI semantic convention
support for structured agent spans.

**Pros:**
- Cross-session memory means agents do not start from zero each time
- Workspace briefing reduces agent onboarding time and token usage
- Decisions log preserves institutional knowledge

**Cons:**
- Substantial implementation effort
- Risk of over-engineering memory before understanding what agents need
- Auto-generated briefings require careful token budgeting

### Database Schema

```sql
-- Phase 1: Telemetry foundation
CREATE TABLE audit_events (
    id            TEXT PRIMARY KEY,
    timestamp     TEXT NOT NULL,
    actor_id      TEXT NOT NULL,
    actor_type    TEXT NOT NULL,   -- user, api_key, pat, system
    resource_type TEXT NOT NULL,   -- workspace, merge, patch, secret, variable
    resource_id   TEXT NOT NULL,
    action        TEXT NOT NULL,   -- create, update, delete, archive, push, merge, rebuild
    metadata      TEXT,            -- JSON
    trace_id      TEXT             -- W3C trace ID, nullable
);

-- Phase 2: Agent sessions and cost tracking
CREATE TABLE agent_sessions (
    id              TEXT PRIMARY KEY,
    workspace_slug  TEXT NOT NULL,
    credential_id   TEXT NOT NULL,
    credential_type TEXT NOT NULL,
    started_at      TEXT NOT NULL,
    ended_at        TEXT,
    status          TEXT NOT NULL DEFAULT 'active',
    metadata        TEXT
);

CREATE TABLE token_usage (
    id                TEXT PRIMARY KEY,
    session_id        TEXT NOT NULL REFERENCES agent_sessions(id),
    workspace_slug    TEXT NOT NULL,
    org_id            TEXT,
    model             TEXT NOT NULL,
    input_tokens      INTEGER NOT NULL DEFAULT 0,
    output_tokens     INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens INTEGER NOT NULL DEFAULT 0,
    reported_at       TEXT NOT NULL
);

-- Phase 2: Job queue trace context
ALTER TABLE jobs ADD COLUMN traceparent TEXT;

-- Phase 3: Agent memory
CREATE TABLE workspace_memory (
    id              TEXT PRIMARY KEY,
    workspace_slug  TEXT NOT NULL,
    scope           TEXT NOT NULL DEFAULT 'workspace',
    key             TEXT NOT NULL,
    value           TEXT NOT NULL,
    source_session  TEXT,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    expires_at      TEXT,
    UNIQUE(workspace_slug, scope, key)
);
```

### Hub Integration Points

| Integration | Details |
|---|---|
| Echo middleware chain | `otelecho` slots alongside apikit auth middleware; audit emission hooks into `respondError`/`respondWorkspace` helpers |
| Job queue worker | Trace context serialized into jobs table at enqueue, restored before handler; `finalizeJob` records duration metrics and completes spans |
| Auth middleware | Session tracking piggybacks on `apikit.AuthInfo` — credential ID and type identify the agent/user |
| Git server handlers | Push and fetch create child spans; post-push hook emits audit events |
| Workspace store | All mutations (insert, update, archive, delete) are natural audit emission points |

### Agent Memory Approach

- **SQLite** for structured memory: decisions, error patterns, cost history.
  Queryable, scoped, managed by the hub.
- **Git repo** for in-repo context: CLAUDE.md, `.agent-fox/` files. Agents
  already read these. The hub does not auto-commit machine-generated content.
- **Context endpoint** (`GET /api/v1/workspaces/:slug/context`) merges both
  sources into a single response for agent consumption.

### Open Questions

1. What defines an agent session for headless API clients — explicit API call,
   time window of activity, or single credential authentication?
2. Should token usage reporting be mandatory or voluntary? Affects reliability
   of cost attribution.
3. Should audit events share the main SQLite database or use a separate file
   for independent retention?
4. How should the hub handle memory conflicts when two agents write
   contradictory context to the same workspace?

---

## Part 3: UI/UX

### Context

The hub is headless-first — the API is the primary interface. But operators and
developers need visual access to workspace status, job progress, patch health,
and agent activity. The existing web scaffold (Vite + React + TypeScript +
shadcn/ui) is configured but contains no functional UI.

### Reference Projects

| Project | What it is | Key Insight |
|---|---|---|
| [FullSend](https://github.com/fullsend-ai/fullsend) | Terminal-native web UI for Claude Code | Chat-centered agentic UI, streaming output display |
| [Observal](https://github.com/Observal/Observal) | React observability dashboard | TanStack Router + shadcn/ui architecture reference |
| [GitHub Copilot](https://docs.github.com/en/copilot) | Extension ecosystem | MCP tools + agent.md plugin model for IDE integration |
| [Block Buzz](https://github.com/block/buzz) | AI agent framework | Multi-provider orchestration patterns |

### Design Options

#### Option A: Functional React Dashboard (Low Complexity)

Flesh out the existing scaffold into a working operational dashboard. Four
core views: workspace list/detail, patch stack health, rebuild/merge job
history with live SSE progress, and admin panel. Use TanStack React Query for
data fetching with SSE-driven cache invalidation. No new infrastructure.

**Pros:**
- Zero new dependencies beyond what is already installed
- SSE is trivial in Go (stdlib only, ~50 lines) and browser-native
- Directly addresses the operational visibility gap

**Cons:**
- Web-only — does not address CLI TUI, IDE integration, or chat platforms
- No component reuse strategy for other frontends

#### Option B: React Dashboard + Copilot Plugin + TUI (Medium Complexity) — Recommended

Build the React dashboard (as in Option A) plus two additional surfaces:

1. **GitHub Copilot Agent Plugin** — MCP server embedded in hub binary
   (`/mcp` endpoint, SSE transport). Exposes workspace CRUD, patch management,
   rebuild, and merge as MCP tools. Agent definitions as markdown files.
   Distributable via marketplace or `.github/copilot-plugins/`.
2. **Bubble Tea TUI** — interactive terminal dashboard in `afc`. Workspace
   overview, patch stack, live job progress. Same Go codebase, shares API
   client code with existing CLI.

**Pros:**
- Three surfaces (web, IDE, terminal) covering primary developer contexts
- Copilot plugin requires minimal new code — MCP server is a thin adapter
- Bubble Tea is Go — no polyglot overhead, shares code with `afc`

**Cons:**
- Three UI codebases to maintain (React, Bubble Tea, Copilot config)
- MCP server adds protocol handling to the main server
- Bubble Tea v2 has a learning curve for the Elm Architecture pattern

#### Option C: Multi-Frontend Platform (High Complexity)

Comprehensive strategy: React dashboard, TypeScript API client (generated from
OpenAPI), VS Code extension, Copilot plugin, Bubble Tea TUI, Slack bot, and
shared event bus (SSE + WebSocket). Extract OpenAPI spec from existing handlers
for client generation.

**Pros:**
- Covers every developer surface
- Shared TypeScript API client eliminates drift
- OpenAPI spec creates a contract between backend and all frontends

**Cons:**
- Months of work across multiple technology stacks
- OpenAPI extraction requires framework adoption or manual annotation
- VS Code extension requires publisher account and ongoing compatibility testing

### New API Endpoints

| Endpoint | Method | Purpose | Phase |
|---|---|---|---|
| `/api/v1/events` | GET (SSE) | Real-time event stream, filterable by workspace/type | 1 |
| `/api/v1/audit` | GET | Query audit events with filtering | 1 |
| `/metrics` | GET | Prometheus-compatible metrics scrape | 1 |
| `/api/v1/sessions` | GET | List agent sessions with filtering | 2 |
| `/api/v1/sessions/:id/usage` | POST | Report token usage for a session | 2 |
| `/api/v1/workspaces/:slug/context` | GET | Assembled workspace context (memory + vars + repo metadata) | 2 |
| `/api/v1/workspaces/:slug/memory` | GET/POST/DELETE | Workspace-scoped memory entries | 3 |
| `/api/v1/workspaces/:slug/sandbox` | POST/GET/DELETE | Sandbox lifecycle management | 3 |
| `/mcp` | POST (JSON-RPC) / GET (SSE) | MCP server for Copilot/IDE integration | 2 |

### SSE Authentication

The browser's native `EventSource` API cannot set `Authorization` headers.
Three options:

1. **Cookie-based session auth for SSE** — introduces a new auth surface the
   hub does not currently have.
2. **Token as query parameter** — appears in server logs and browser history.
   Security risk.
3. **fetch() ReadableStream** (recommended) — full header control, no token
   leakage. Requires a thin wrapper for auto-reconnect. No cookies needed.

### Hub Integration Points

| Integration | Details |
|---|---|
| SSE endpoint | `GET /api/v1/events?workspace=<slug>` wrapping job queue status. Go stdlib `net/http` Flusher, zero external dependencies. Serves web dashboard, MCP server, and TUI. |
| MCP server | `/mcp` endpoint exposing hub operations as MCP tools via JSON-RPC 2.0 over SSE transport. Thin adapter over existing REST handlers. |
| React Query | `useQuery` hooks per API endpoint with SSE-driven `queryClient.invalidateQueries()` for live updates. |
| Bubble Tea TUI | Connects to hub API using existing Go HTTP client code in `internal/cli`. Adds interactive views on top of command-based interface. |
| Copilot Plugin | File-based package: `plugin.json` manifest, `agents/*.agent.md` definitions, MCP server configs for common workflows. |

### Open Questions

1. SSE vs WebSocket? SSE is sufficient for unidirectional job-status push, but
   WebSocket enables bidirectional features later (interactive conflict
   resolution, terminal streaming).
2. Should the MCP server be embedded in the hub binary or a separate process?
3. Is a dedicated VS Code extension worth building when a Copilot plugin gives
   80% of the value for 20% of the effort?
4. Should the scaffold migrate to TanStack Router now while it has zero routes?

---

## Cross-Cutting Dependencies

```
                  +-------------------+
                  |    UI / UX        |
                  | (web, TUI, MCP)   |
                  +--------+----------+
                           |
              consumes     |    displays
          +----------------+----------------+
          |                                 |
+---------v----------+          +-----------v---------+
|    Telemetry       |          |    Sandboxes         |
| (traces, metrics,  |  <------| (lifecycle, resource |
|  audit, sessions,  |  emits  |  usage, isolation)   |
|  agent memory)     |  events |                      |
+--------------------+          +----------------------+
```

**Telemetry first** — without structured logging, audit events, and metrics,
sandbox behavior is unobservable and dashboards have no data.

**UI second** — the delivery mechanism that makes telemetry and sandbox data
usable by humans.

**Sandboxes third** — highest complexity, highest risk, most cluster
infrastructure changes. The hub gains observability and operational UI before
taking on the hardest work.

### Shared Infrastructure

Three capabilities are needed by all three areas:

1. **SSE streaming endpoint.** Telemetry needs it for live metric push.
   Sandboxes need it for lifecycle events. The UI needs it for dashboard
   updates. The MCP server uses SSE as its transport.
2. **Expanded SQLite schema.** All three areas need new tables:
   `audit_events`, `agent_sessions`, `token_usage`, `workspace_memory`.
3. **Event emission from existing handlers.** The workspace, merge,
   carrypatch, gitserver, and secrets packages all need to emit structured
   events.

---

## New Internal Packages

| Package | Purpose | Phase |
|---|---|---|
| `internal/audit` | Audit event store, typed categories, emission helpers, query API | 1 |
| `internal/sse` | SSE connection manager, event broadcasting, heartbeat, per-topic subscriptions | 1 |
| `internal/telemetry` | OTel provider init, span helpers, trace propagation through job queue, metrics registry | 2 |
| `internal/session` | Agent session tracking, token usage recording, cost queries | 2 |
| `internal/mcp` | MCP JSON-RPC 2.0 server, tool definitions wrapping existing handlers, SSE transport | 2 |
| `internal/memory` | Workspace-scoped memory store, context assembly for agent/sandbox injection | 3 |
| `internal/sandbox` | Agent Sandbox Go SDK wrapper, warm pool management, lifecycle mapping | 3 |
| `cmd/afc/tui/` | Bubble Tea views for interactive terminal dashboard | 2 |

## New External Dependencies

| Dependency | Type | Phase |
|---|---|---|
| `github.com/prometheus/client_golang` | Go module | 1 |
| `go.opentelemetry.io/otel` + contrib | Go module | 2 |
| `github.com/charmbracelet/bubbletea/v2` | Go module | 2 |
| `github.com/charmbracelet/bubbles` | Go module | 2 |
| `sigs.k8s.io/agent-sandbox/clients/go/sandbox` | Go module | 3 |
| `k8s.io/client-go` | Go module | 3 |
| Agent Sandbox CRDs + controller | Cluster operator | 3 |
| OTLP collector (optional) | Infrastructure | 2 |

## Deployment Impact

**Phase 1:** No deployment changes. New tables in existing SQLite. `/metrics`
served by the same pod.

**Phase 2:** No hub pod changes. Optional OTLP collector as sidecar or
DaemonSet. MCP server runs inside hub binary on the same port.

**Phase 3:** Significant changes:
- Agent Sandbox CRD and controller installed on cluster (separate operator)
- Hub deployment needs RBAC for SandboxClaim resources
- SandboxTemplate and SandboxWarmPool CRs in `deploy/`
- NetworkPolicy manifests restricting sandbox egress to hub Service
- Hub resource limits may increase for many sandbox connections

---

## Phased Roadmap

### Phase 1: Foundation (Weeks 1-6)

*Observable hub with operational UI. No external infrastructure changes.*

| Work Item | Effort |
|---|---|
| Unify all logging on slog (12 files use stdlib `log`) | 1 week |
| `internal/audit` + `audit_events` table + `GET /api/v1/audit` | 1 week |
| SSE endpoint (`GET /api/v1/events`) wrapping job queue status | 1 week |
| `/metrics` endpoint with basic counters/histograms | 0.5 week |
| Emit audit events from workspace, merge, carrypatch, secrets, gitserver | 1 week |
| Web dashboard: workspace list/detail + job history with live SSE progress | 2 weeks |
| `afc audit list` and `afc stats` CLI commands | 0.5 week |

**Deliverables:** Structured slog logging. Audit trail for all mutations.
Prometheus metrics. SSE streaming. Working web dashboard. Zero new cluster
dependencies.

### Phase 2: Integration (Weeks 7-14)

*Tracing, agent sessions, IDE integration, TUI.*

| Work Item | Effort |
|---|---|
| OTel tracing: otelecho + otelsql + job queue trace propagation | 1.5 weeks |
| Agent session tracking: `agent_sessions` table, auto-detect from auth middleware | 1 week |
| Token usage reporting: `token_usage` table + POST endpoint | 0.5 week |
| Workspace context endpoint: `GET /workspaces/:slug/context` | 1 week |
| MCP server embedded in hub binary (SSE transport) | 1.5 weeks |
| Copilot Agent Plugin package (plugin.json + agent.md + skills) | 0.5 week |
| Bubble Tea TUI for `afc`: workspace overview, patch stack, live progress | 2 weeks |
| Web dashboard: session history, cost report, audit log views | 1 week |

**Deliverables:** Distributed traces. Agent session tracking with cost
attribution. Workspace context endpoint. MCP server for IDE access. Copilot
plugin. Interactive TUI. Expanded dashboard.

### Phase 3: Polish (Weeks 15-24)

*Sandbox isolation, agent memory, enterprise readiness.*

| Work Item | Effort |
|---|---|
| Agent Sandbox CRD integration: SDK wrapper, SandboxTemplate, SandboxWarmPool | 3 weeks |
| Workspace-sandbox lifecycle mapping: activate → provision, archive → delete | 1 week |
| NetworkPolicy manifests + secret injection into sandbox pods | 1 week |
| Scoped PAT generation for sandbox auth | 1 week |
| Workspace-scoped agent memory: `workspace_memory` table + CRUD API | 1.5 weeks |
| Sandbox cleanup: archive/delete handlers + orphan reconciliation | 1 week |
| Web dashboard + TUI: sandbox status panel, memory browser | 1.5 weeks |
| Sandbox health in `/healthz` and `/readyz` | 0.5 week |

**Deliverables:** Isolated sandbox execution via K8s Agent Sandbox CRD.
Sub-second provisioning. Workspace-scoped agent memory. Sandbox visibility in
dashboard and TUI.

---

## Key Decisions

Ordered by impact. Each requires human input before implementation.

### 1. Agent Session Boundaries

**Options:** (a) Agents explicitly open/close sessions via API
(`POST /sessions`, `DELETE /sessions/:id`). (b) Hub infers sessions from
credential activity patterns (same API key active within a time window = one
session). (c) Dual mode — explicit with inference fallback.

**Recommendation:** Option (c). Agents that support it open sessions
explicitly. For agents that do not, the hub infers from activity gaps.

**Impact:** Affects telemetry schema, cost attribution reliability, and UI
session display. Must be decided before Phase 2.

### 2. Sandbox Runtime Class

**Options:** (a) Enforce a specific RuntimeClass (Kata on OpenShift, gVisor on
GKE). (b) Defer to cluster admin — hub requests RuntimeClass by configurable
name.

**Recommendation:** Option (b). Default to cluster default RuntimeClass.
Document security implications. Allow per-org or per-workspace override via
variables.

**Impact:** Affects deployment manifests, documentation, and security posture.

### 3. Agent Memory Location

**Options:** (a) SQLite only. (b) Git repo only. (c) Both — SQLite for
structured memory, git for in-repo context files.

**Recommendation:** Option (c). SQLite for decisions, error patterns, cost
history. Git repo for CLAUDE.md/.agent-fox/ that agents already read. Context
endpoint merges both. Do not auto-commit machine-generated content.

**Impact:** Affects workspace data model, context endpoint, and agent
interaction patterns.

### 4. Network Egress for Sandboxes

**Options:** (a) Fully air-gapped with pre-baked images. (b) Proxy with
per-workspace/per-org allowlist. (c) Open internet.

**Recommendation:** Option (b). Deny-by-default with configurable
`ALLOWED_EGRESS_DOMAINS` variable mapped to NetworkPolicy egress rules.
Pre-baked images for common toolchains.

**Impact:** Affects provisioning speed, security posture, and operational
complexity. Package installation failures will be a top support issue if
egress is too restrictive.

### 5. SSE Authentication

**Options:** (a) Cookie-based session auth. (b) Token in query parameter.
(c) fetch() ReadableStream with Authorization header.

**Recommendation:** Option (c). Avoids introducing cookies (hub is token-only)
and avoids leaking tokens in URLs. Requires a thin auto-reconnect wrapper in
the frontend.

**Impact:** Affects web frontend implementation and MCP transport.

### 6. MCP Server Placement

**Options:** (a) Embedded in hub binary on `/mcp` path (SSE transport).
(b) Separate binary/process (STDIO transport).

**Recommendation:** Option (a). Hub is already a single binary. SSE transport
enables remote access. STDIO adapter can be added later if needed.

**Impact:** Affects deployment model and Copilot plugin configuration.

### 7. OTLP Backend

**Options:** (a) No-op default, export if `OTEL_EXPORTER_OTLP_ENDPOINT` is
set. (b) Built-in SQLite trace store for local mode.

**Recommendation:** Option (a). Do not build a local trace store. Audit events
and metrics provide sufficient local debugging. Traces are a bonus for teams
with existing observability infrastructure.

**Impact:** Affects telemetry package complexity.

### 8. Warm Pool Sizing

**Options:** (a) Fixed small pool (2 per SandboxTemplate). (b) Dynamic scaling
via KEDA/HPA.

**Recommendation:** Option (a) initially. Expose pool size as configurable
variable per org. Add hit/miss rate metric. Defer dynamic scaling until usage
patterns are understood.

**Impact:** Directly affects operational cost and provisioning latency.

### 9. Cost Attribution Granularity

**Options:** (a) Per-workspace-per-day. (b) Per-session-per-model.

**Recommendation:** Store per-session-per-model in `token_usage` but expose
aggregated per-workspace-per-day views in API and UI. Raw data enables
drill-down; aggregations keep queries fast.

**Impact:** Affects token_usage schema and dashboard design.

### 10. VS Code Extension vs Copilot Plugin

**Options:** (a) Dedicated VS Code extension with tree views, webview panels,
status bar. (b) Copilot Agent Plugin only.

**Recommendation:** Option (b) initially. Copilot plugin gives 80% of the
value for 20% of the effort. Revisit dedicated extension after validating
usage.

**Impact:** Low — easier to add an extension later than to remove one.

---

## Risks

| Risk | Severity | Mitigation |
|---|---|---|
| SQLite contention under write load from audit events, sessions, and memory | High | Enable WAL mode. Consider separate SQLite file for audit events. Monitor via otelsql. |
| Agent Sandbox CRD stability (v1beta1) | Medium | Wrap all SDK calls behind an interface. Red Hat downstream provides stability on OpenShift. |
| SSE connection limits under many concurrent agents/dashboards | Medium | Set maximum connection limit. Heartbeat-based stale cleanup. |
| Scope creep across three areas | High | Phase 1 has zero external dependencies by design. Resist jumping to Phase 3. |
| Cluster admin dependency for sandbox CRDs | High | Start the conversation during Phase 1. Design sandboxes as optional — hub must work without them. |
| Breaking single-binary simplicity | Medium | Every new dependency must be optional. Hub functions fully without OTel collector, sandbox controller, or warm pool. |
| Over-engineering agent memory | Medium | Start with SQLite key-value + in-repo files. Defer vector databases, knowledge graphs, and auto-generated briefings until usage patterns emerge. |
| Agent adoption of session/usage reporting | Medium | Design as best-effort. Hub tracks session duration and API counts from its own middleware regardless. |
