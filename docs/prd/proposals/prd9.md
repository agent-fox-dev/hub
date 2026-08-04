# Hub Evolution Roadmap

## Context

The hub is inspired by [Intent](https://www.intentapp.dev) from Augment Code
but diverges deliberately: API/CLI backend service (no UI), coordination
via structured spec packages instead of chat, specs freeze on approval,
and all grounding unified under a single Context abstraction.

This document maps Intent's concepts against the hub's current state and
planned capabilities, identifies what to adopt, what to adapt, and what to
skip — then lays out a phased evolution roadmap.

## Where the Hub Is Today

The hub has workspace CRUD, a built-in smart HTTP git server, multi-tenant
auth (admin tokens, API keys, PATs via apikit), encrypted secrets/variables,
and git credential management. The campaign execution system (prd8) is fully
designed — DAG-based spec scheduling, merge queue, continuous rebase, typed
rejection reasons, nonce idempotency — but not yet implemented.

The gap between what exists (workspace infrastructure) and what is designed
(full agentic coordination) is six phases of work.

## Intent's Core Model

Intent is a macOS desktop app that replaces the IDE with a workspace-centric
environment for spec-driven, multi-agent development. Its core concepts:

| Concept | How it works |
|---------|-------------|
| **Workspace** | Isolated environment: git worktree branch + agents + notes + activity log. One workspace per unit of work. |
| **Spec** | Free-form markdown note per workspace. Living document — always editable. Coordinator agent writes plans into it. |
| **Multi-agent orchestration** | Coordinator decomposes work into @@@task blocks, delegates to parallel Implementors, Verifier checks results. |
| **8 specialist roles** | Developer, Coordinator, Implementor, Verifier, UI Designer, Ralph, PR Reviewer, PR Shepherd. |
| **BYOA** | Wraps external agent providers (Augment, Claude Code, Codex). No built-in LLM. |
| **Notes** | Rich markdown with code references, CLI blocks, Mermaid diagrams, agent action buttons, threaded comments. |
| **Context** | Three pillars: Code, Context (AGENTS.md, MCP, Skills), Agents. |
| **Task Notes** | @@@task blocks auto-convert to trackable Task Note entities with linked checkboxes. |

**Intent's workflow:** create workspace → describe goal to Coordinator →
Coordinator writes plan in Spec → user approves → Coordinator delegates to
Implementors → Verifier checks → user reviews changes → PR → merge.

## Key Divergences

| Area | Intent | Hub | Why the hub diverges |
|------|--------|-----|---------------------|
| **Platform** | Desktop app (macOS) | API/CLI backend service | No UI — operators interact via REST API and CLI only |
| **Spec model** | Free-form markdown, always editable | Validated 4-artifact JSON package, freezes on approval | Prevents intent drift in unattended execution |
| **Coordination** | AI Coordinator agent decides sequencing | Deterministic DAG scheduler from tasks.json dependencies | More predictable and auditable than agent-driven delegation |
| **Parallelism** | Multiple agents in one workspace | One agent per workspace, multiple workspaces per campaign | Eliminates file contention between agents |
| **Agent isolation** | Desktop processes with host filesystem access | OCI containers with scoped gRPC to hub | Defense-in-depth for unattended, headless execution |
| **Task tracking** | @@@task blocks → Task Notes | tasks.json with formal state machine + verification gate | Machine-enforced quality gates |
| **Context** | Separate systems: AGENTS.md, MCP, Skills, Context tab | Unified Context abstraction with source types | Single access-controlled, revision-pinned grounding model |
| **Spec mutability** | Edit in place any time | Supersession: freeze → supersede → new spec | Preserves audit trail of what was attempted |

## Evolution Phases

### Phase 1: Campaign Infrastructure and Git Primitives

**Rationale:** Everything else depends on the hub being able to schedule work
across specs, manage per-spec branches, and merge results safely. This phase
implements prd8.

**Capabilities:**

1. **Hardened git subprocess runner** (`internal/gitcmd`) — GitRunner with
   safety defaults, three-way exit code discrimination. Under 50 lines.
   Prerequisite for all subsequent git operations.

2. **Campaign lifecycle** (`internal/campaign`) — Campaign CRUD via REST API.
   DAG construction from tasks.json dependencies. campaign_specs join table.
   Cycle detection. Frontier computation.

3. **Merge queue** (`internal/mergequeue`) — FIFO with per-target-branch
   workers. CantMergeReason enum. Dry-run conflict check
   (`git merge-tree --write-tree`). Nonce idempotency. Exponential backoff.
   Dead-letter queue. Wakeup-on-enqueue. Graceful shutdown.

4. **Continuous rebase with conflict-stop** — Post-merge BFS rebase of
   sibling spec branches. Block on conflict + revoke push access. Skip
   dependents of blocked branches.

5. **Campaign scheduler** — DAG frontier tracking. Three-way select:
   shutdown / timer / wakeup. Create per-spec branches, dispatch work,
   advance frontier on merge completion.

**Intent inspiration:** Intent has no campaign concept — it operates
one-workspace-one-task with a human coordinating across workspaces. The hub
replaces that human with a deterministic DAG scheduler.

### Phase 2: Spec Package and Spec Lifecycle

**Rationale:** Agents need structured plans to execute against. The spec
package is the coordination contract — without it, the campaign scheduler
has nothing to schedule, and agents have no grounding.

**Capabilities:**

1. **Spec store** — Filesystem layout:
   `<data_dir>/specs/<campaign>/<spec>/` with prd.md, requirements.json,
   test_spec.json, tasks.json, architecture.md. Outside the workspace clone
   so agents cannot modify specs via file operations — the freeze is
   structural, not policy.

2. **SpecRef lifecycle** — SQLite table tracking: slug, status
   (draft/active/sealed/superseded/archived), intent_hash, schema_version.
   State machine: draft→active freezes, active→sealed marks completion,
   supersede transitions old spec and archives it. Only active specs are
   visible to the agent API.

3. **Spec validation and rendering** — speclib integration (Python sidecar
   or CLI fallback). JSON Schema validation, cross-file integrity checks,
   deterministic markdown rendering.

4. **Spec read API** — Operator-facing REST endpoints + agent-facing gRPC.
   Artifact views, rendered combined view, coverage, traceability. Access
   gate: only active+ specs served.

5. **Subtask execution state** — `subtask_executions` table tracking live
   state separately from frozen tasks.json. State machine:
   pending → queued → in_progress → awaiting_verification → done /
   pending_reevaluation. Verification outcomes table.

**Intent inspiration:** Intent's Spec is a single free-form note. The hub
replaces this with validated, multi-artifact packages that freeze on
approval. The `awaiting_verification` gate is a hub addition — Intent's
verification is advisory (user decides), the hub's is mandatory (state
machine blocks progression).

### Phase 3: Agent Runtime and Sandbox Isolation

**Rationale:** With specs defined and campaigns schedulable, the hub needs to
actually run agents. This phase makes the hub operational.

**Capabilities:**

1. **Container runtime interface** — `ContainerRuntime` interface
   (create/start/stop/exec/logs) with OpenShell adapter. Sandboxes with
   Landlock, HTTP CONNECT proxy, Seccomp BPF. Workspace checkout at
   `/workspace` inside container.

2. **Agent lifecycle management** — Two-dimensional state: Phase
   (created/provisioning/running/stopped/error) + Activity
   (working/thinking/waiting/completed/idle). Programmatic start/stop/resume.

3. **Harness adapters** — Tier 1: Claude Agent SDK, Google ADK (provider SDK
   owns agent loop). Tier 2: LangGraph generic (af-owned loop for long-tail
   providers, local inference). Same HarnessAdapter interface for both tiers.

4. **af SDK** — Python library inside agent sandbox. Tools: af.spec_read,
   af.context_search, af.subtask_state, af.ci_status, af.issues. gRPC to
   hub with scoped JWT. No spec-write tool — the freeze is structural.

5. **Specialist templates** — Templates as blueprints: template.yaml +
   home/ directory. Built-in: planner, coordinator, implementor, verifier.
   Project-level overrides global overrides built-in. Prompt assembly
   includes rendered spec slice.

**Intent inspiration:** Intent's BYOA model wraps desktop agent processes
with full host filesystem access. The hub adapts this to containerized,
programmatic lifecycle management — agents run in sandboxes with only gRPC
access to the hub.

### Phase 4: Context, Grounding, and Observability

**Rationale:** Agents are running but operating with minimal grounding. This
phase adds the unified Context abstraction and observability.

**Capabilities:**

1. **Context store** — Unified grounding abstraction. Source types:
   repository, file, PR/issue, blob, free text, MCP server, skill, rule.
   Resolution strategies: pinned (full content in prompt) or retrieved
   (indexed, chunks pulled per turn). Freshness: snapshot or live.

2. **Context retrieval engine** — Embedded mode: in-process ONNX embedding
   model, SQLite vector store. Indexes Context sources, returns ranked
   chunks by similarity. External mode: standalone gRPC service.

3. **MCP server integration** — MCP servers declared as Context sources.
   Available to agents in workspaces that attach the Context. Routed through
   provider SDK's native MCP support (Tier 1) or LangChain (Tier 2).

4. **Activity event stream** — Append-only `activity_events` table. Event
   types: text, tool_call, commit, status_change, verification_outcome.
   REST query endpoint + SSE stream for consumers. Recovery backbone —
   run history reconstructable from events.

5. **Workspace bootstrap** — Per-workspace setup scripts run inside sandbox
   before agents act. Gate workspace activation on script success.

**Intent inspiration:** Intent distributes grounding across AGENTS.md, MCP,
Skills, and a Context tab. The hub unifies all of these under one abstraction
with consistent access control and revision pinning.

### Phase 5: Orchestration, Verification, and Integrations

**Rationale:** The hub can run agents with grounding. This phase adds the
coordination intelligence: run management, verification gates, and external
integrations.

**Capabilities:**

1. **Run management** — Runs table. Starting a run: pin Context revisions,
   create subtask state, start Coordinator. Coordinator reads frozen spec,
   delegates to Implementors, monitors progress, triggers verification. One
   agent at a time per workspace — campaign-level parallelism compensates.

2. **Verification gates** — Implementor → awaiting_verification → Verifier
   runs checks → done or pending_reevaluation. Wiring verification traces
   execution paths, confirms return-value propagation, runs smoke tests.
   Mandatory — state machine blocks progression.

3. **CI/CD bridge** — Read-only CIProvider interface (GitHub Actions, GitLab
   CI). Exposed via af.ci_status. Verifier uses CI status for wiring
   verification.

4. **Issue tracker interface** — Provider-agnostic: GitHub, GitLab, Jira,
   Linear. Exposed via af.issues. Agents create/comment/close issues.

5. **PR operations** — Agent tools for PR lifecycle. PR Shepherd specialist
   drives PRs to merge-ready. Integrated with merge queue for upstream
   delivery.

### Phase 6: Memory, Notifications, and Operational Maturity

**Rationale:** Production readiness. Not blocking for the core execution
loop.

**Capabilities:**

1. **Agent memory** — Recall/consolidate contract. Learnings in SQLite with
   embedding search. Revision pinned at run start. Agent-authored, unlike
   operator-curated Contexts.

2. **Notification service** — Subscribes to activity events, matches
   triggers (spec_ready_for_review, run_failed, workspace_unblocked).
   Channels: webhook (Slack/Discord/generic HTTP) and structured log.

3. **Planner specialist** — Hub-mediated spec authoring. Planner uses
   speclib to draft artifacts grounded in attached Contexts. Operator
   approves via hub API.

4. **Spec supersession** — Stop agents, drop in-flight subtasks, commit
   partial work, supersede spec, new spec on same branch. Controlled escape
   valve for the freeze contract.

## Key Architectural Decisions

| Decision | Recommendation | Rationale |
|----------|---------------|-----------|
| go-git vs git CLI | git CLI via GitRunner for merge/rebase; go-git for smart HTTP server and clone | go-git lacks merge-tree support; GitRunner adds defense-in-depth |
| speclib integration | HTTP sidecar co-located with hub; CLI fallback for one-off ops | Clean process isolation; spec format stays in Python |
| Concurrent campaigns | One active campaign per integration branch | Concurrent cross-campaign coordination too complex for the throughput profile |
| Agent dispatch | Hub spawns containers directly; webhook for external controllers | Single-process default; distributed via campaign API |
| Spec storage | Hub-managed directory outside workspace clone | Freeze is structural — agents cannot touch spec files |
| Conflict resolution | Dedicated resolve endpoint | Explicit intent; hub verifies resolution before restoring access |
| Check command source | Campaign config → workspace variable → tasks.json | Cascade gives control without requiring config for simple cases |

## What NOT to Build

| Intent feature | Why not |
|---------------|---------|
| **Any UI** (web dashboard, desktop app, command palette) | Hub is API/CLI only. UI is a separate concern — any frontend consumes the same REST API as the CLI. |
| **Notes system** (rich markdown with code refs, CLI blocks, agent actions) | Hub coordinates via structured spec packages, not prose. Notes would undermine the freeze. |
| **@@@task blocks** | tasks.json with formal state machine + traceability is more rigorous. |
| **Embedded browser** | Browser is a sandbox sidecar, not a hub feature. Hub coordinates, sandbox executes. |
| **Ralph specialist** (autonomous test loops) | Unconstrained looping risks burning compute. Hub uses controlled bounce-back via state machine. |
| **Developer specialist** (solo end-to-end) | Collapses the separation of planner/coordinator/implementor/verifier. Bypasses the freeze and verification gates. |
| **Conversation forking** | Agent conversations are adapter implementation details, not hub concepts. |
