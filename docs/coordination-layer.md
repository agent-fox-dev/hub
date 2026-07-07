# Coordination Layer Specification

**Version:** 1.0
**Status:** Draft

This document specifies the coordination layer of the af agentic development
harness: the domain model, workspace and campaign management, spec package
integration, agent model, multi-agent orchestration, key flows, data model,
and API surface. It is the largest of the three layer specifications and the
one most developers will read first.

The runtime layer (sandbox isolation, branch management, harness adapters,
agent lifecycle) is specified in [runtime-layer.md](runtime-layer.md). The
services architecture (hub, CLI, storage, protocols, deployment) is
specified in [services-architecture.md](services-architecture.md). The spec
format itself is an independent standard at
[spec-format_v1.3.md](spec-format_v1.3.md); this document covers what the
harness builds on top of it.

---

## 1. Scope

The harness is a headless runtime that isolates each unit of work in its own
workspace, runs AI agents against pluggable providers, coordinates multi-agent
work through a structured spec package, and grounds agents in reusable
Contexts. A separate surface (TUI, web client, API consumer) sits on top;
nothing here assumes a GUI.

Two layers of input are assembled for every agent turn, and keeping them
separate is the spine of this document. **Coordination** is the spec package:
what to build and how it is verified, authored once and frozen on approval.
**Grounding** is the Context: what an agent should know while it works,
supplied by a reusable, access-controlled object the agent reads but does not
change.

### 1.1 Goals

The harness should let a caller:

1. Open a workspace against an existing repo, a clone, or an empty directory, with each workspace isolated at the filesystem and git level.
2. Run one or more agents inside a workspace, each backed by a configurable provider and model, with a tool set scoped to that workspace.
3. Author a structured spec package per unit of work, where humans own the PRD and its intent, and a Planner drafts the requirements, test specification, and task plan as validated JSON artifacts, which freeze once approved.
4. Enforce spec integrity while a spec is being authored: validate each draft write against its schema and against cross-artifact integrity rules, apply changes transactionally, and reject anything that would let an inconsistent package be approved.
5. Ground agents in one or more attached Contexts pinned to a fixed revision for the duration of a run, so grounding is consistent within a run and reproducible across re-runs.
6. Coordinate multi-agent work, where a Planner decomposes intent into task groups and subtasks, a Coordinator delegates them, and worker agents execute subtasks in parallel, reporting progress only through the subtask state they own.
7. Stream a complete, ordered activity log of everything that happened so runs are auditable and reproducible.

### 1.2 Non-goals

- Any GUI: panels, docks, themes, keyboard shortcuts. The harness is headless.
- Hosting, billing, end-user authentication. The harness is provider-agnostic.
- Building a foundation model. Agents run on external providers.
- A merge queue or CI system. The harness reads CI status and drives PRs but does not run pipelines.
- JSON Schema authoring tooling and hosted schema URLs. Schemas are bundled with the validation library.
- The Context authoring UI. This document specifies the contract, not how a Context is edited.
- Agent write-back into a Context. Contexts are read-only to agents by design.

---

## 2. Domain model

The workspace is the aggregate root. Two structures hang off it: the **spec
package** (coordination) and one or more attached **Contexts** (grounding). A
Context lives above the workspace and is referenced by it, because a Context
is reused across many workspaces. The spec package lives in the spec store,
keyed by workspace, because it is per task; agents access it through the
harness API, not as files in the checkout.

```mermaid
graph TD
    W[Workspace] -->|owns| WT[Branch]
    W -->|owns| SP[Spec package]
    SP -->|contains| PRD[prd.md + frontmatter]
    SP -->|contains| REQ[requirements.json]
    SP -->|contains| TS[test_spec.json]
    SP -->|contains| TK[tasks.json]
    SP -->|optionally contains| ARCH[architecture.md]
    TK -->|task_groups to subtasks| STK[Subtasks]
    W -->|attaches at pinned revision| CTX[Context - grounding]
```

| Entity | One-line definition |
| --- | --- |
| Workspace | Isolated environment for one task: a branch, a spec package, attached Contexts, agents, and an activity log. Each workspace runs in its own sandbox with an independent repo checkout. |
| Branch | A git branch dedicated to a workspace, checked out inside the sandbox at `/workspace`. |
| Spec package | The validated set of four required artifacts (and one optional) that define and verify the work. Format details in [spec-format_v1.3.md](spec-format_v1.3.md). |
| PRD (`prd.md`) | The human-authored narrative: intent, goals, non-goals, background, plus machine-read frontmatter and a hashed Intent section. |
| Requirements (`requirements.json`) | EARS acceptance criteria, correctness properties, execution paths, and error handling. |
| Test spec (`test_spec.json`) | A language-agnostic test contract derived from the requirements, with computed coverage. |
| Tasks (`tasks.json`) | The implementation plan: task groups, subtasks with a defined state machine, dependencies, and traceability. |
| Architecture (`architecture.md`) | Optional, free-form module and interface design. No schema, not cross-validated. |
| Context | A durable, owned, reusable bundle of grounding: one instruction and a set of typed sources. Lives above the workspace and is read-only to agents. |
| Source | A typed reference inside a Context, carrying a resolution strategy (pinned or retrieved) and a freshness contract (snapshot or live). Content sources and capability sources (MCP, skills, rules) are both sources. |
| Agent | A running model instance, with a specialist role, an actor capability, a provider, a model, and a scoped tool set. |
| Actor capability | The permission tier (Operator, Planner, Coordinator, or Archetype) that governs what an agent or human may write in the spec package. |
| Provider | An external model backend (for example Anthropic via Claude Agent SDK, Google via ADK, or any LangChain-supported provider via the generic adapter) the harness drives through one interface. |
| Agent memory | An agent-authored body of learnings that outlives a workspace, driven through one contract (`recall` at prompt assembly, `consolidate` at session end). Distinct from a Context: agent-authored and accumulated, not Operator-curated. |

The relationship that matters most: agents do not message each other to
coordinate. They coordinate through a shared store. The Planner authors the
spec package during `draft` through a validated contract; the package freezes
on approval; the Coordinator then delegates work and monitors progress;
workers read the frozen plan and write only their own execution state. They
read attached Contexts but cannot change them. The structured package is the
coordination medium; the Contexts are the grounding medium.

---

## 3. The Workspace

### 3.1 Responsibilities

A workspace is the unit of isolation and the unit of state. It bundles, for one task:

- a dedicated git branch, checked out inside the sandbox at `/workspace`;
- one active spec package stored in the spec store, accessible to agents only through the harness API (§5.5);
- zero or more attached Contexts, each pinned to a fixed revision;
- a registry of agents (active and finished) and their conversation histories;
- managed scripts (long-running processes such as a dev server);
- an append-only activity log.

One workspace per real task. One workspace carries one spec package, and it attaches whichever Contexts describe the domain that task touches.

### 3.2 Isolation through sandboxes

Isolation lets several workspaces run at once without stepping on each
other. Creating a workspace creates a branch (named with a prefix, for
example `af/add-dark-mode`). When a sandbox starts for that workspace, the
runtime clones the repo inside the sandbox and checks out the branch.
Each sandbox has its own independent clone — no shared filesystem or git
state between workspaces. The user's main branch and checkout are never
touched.

A fresh clone does not inherit untracked files. Environment files, local
secrets, seeded data, and installed dependencies do not carry over.
Workspace creation must run a bootstrap step (§3.4) before agents start.

The spec package does not live in the checkout. It is stored in the spec
store, keyed by workspace, and agents access it only through the af SDK
(§5.5). Spec artifacts never appear in the project's source tree or git
history.

### 3.3 Workspace lifecycle

Distinct from the spec lifecycle (§5.3); a workspace contains a spec, and the two are tracked separately.

| State | Meaning | Transitions |
| --- | --- | --- |
| Created | Branch exists; bootstrap pending or deferred (campaign-gated, §4.5). | to Active on bootstrap success; to Failed on bootstrap error. |
| Failed | Bootstrap did not complete; no agents run. | to Created (retry); to Deleted. |
| Active | Agents may run; the spec and files are live. | to Archived; to Deleted. |
| Archived | Read-only; kept for reference, hidden from default listings. | to Active (reopen); to Deleted. |
| Deleted | Harness metadata removed. | Terminal. |

Creation accepts one of three origins: a local repo path, a remote URL the harness clones, or an empty start. Creation also accepts the set of Contexts to attach and, per Context, whether to pin its current revision (default) or track it live.

Deletion removes the harness record and metadata. The git branch is left in place unless the caller asks to remove it. Attached Contexts are never deleted by deleting a workspace.

### 3.4 Bootstrap and setup scripts

Each workspace carries setup commands that run when its sandbox initializes, before any agent acts: install dependencies, copy a `.env`, run a seed script. The harness runs these inside the sandbox's checkout, captures output into the activity log, and only marks the workspace Active when they succeed.

### 3.5 State surfaces

The harness exposes the workspace's live state through read APIs:

- file listing and file contents for the workspace checkout;
- git status and per-file diffs;
- the spec package: each artifact in raw JSON or markdown, and a rendered combined view;
- computed coverage and traceability;
- attached Contexts and their pinned revisions;
- the activity log, filterable by agent and time.

### 3.6 Per-workspace configuration

A workspace can override global defaults for: git remote and base branch, attached Contexts and pin mode, setup scripts, and the default provider and model.

### 3.7 Workspace ownership and management

Every workspace is created and managed by the Operator. No agent creates, archives, or deletes a workspace.

**Creation.** The Operator supplies:

- an **origin**: a local repo path, a remote URL, or an empty start;
- the **Contexts** to attach, with pin mode per Context (pinned by default);
- optionally, a **Campaign** to register the workspace into.

The harness provisions the branch, runs bootstrap inside the sandbox, and transitions to `Active` on success. If registered into a Campaign with unsatisfied dependencies, bootstrap is deferred until the gate clears (§4.5).

**Management.** Archive, reopen, and delete are Operator actions. The harness does not auto-archive or auto-delete.

The one automatic transition is `Created` to `Active` (or `Failed`) on bootstrap completion. Every other lifecycle transition requires an explicit Operator action.

---

## 4. Campaigns

Every spec belongs to a campaign. A Campaign is the organizational unit that groups related specs, provides a dependency graph between them, and serves as the top-level directory in the spec store filesystem. A single-spec task creates a campaign with one spec; multi-spec work adds specs incrementally.

### 4.1 What a Campaign is

A Campaign is a named container that owns a set of specs, a dependency graph across them, a goal document, and orchestration state. It sits above the workspace: a Campaign references workspaces, it does not own their internals. Every spec is stored under its parent campaign in the spec store (see [services-architecture.md §8.1](services-architecture.md#81-filesystem-layout)).

A Campaign does not decompose the goal upfront. Spec authoring happens one spec at a time (see §5). The human registers specs into the campaign incrementally — start with spec 01, see what it reveals, register spec 02 with a declared dependency on spec 01.

### 4.2 Campaign metadata

Each Campaign has a `campaign.yaml` file in its directory containing lightweight metadata:

```yaml
name: auth-system
description: "End-to-end authentication and authorization for the platform."
created_at: "2026-06-09T10:00:00Z"
updated_at: "2026-06-09T10:00:00Z"
```

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `name` | string | yes | Human-readable campaign name. |
| `description` | string | yes | The top-level goal: what this campaign aims to achieve and any cross-cutting constraints. |
| `created_at` | ISO 8601 datetime | yes | When the campaign was created. Immutable. |
| `updated_at` | ISO 8601 datetime | yes | Last modification timestamp. |

This is lighter than a spec PRD: no Intent hash, no schema validation, no freeze. The campaign directory and `campaign.yaml` are created by `spec init` (see [services-architecture.md §7.2](services-architecture.md#72-spec-cli)).

**Filesystem vs. hub.** `campaign.yaml` on the filesystem is the authoring-time copy, created and edited by `spec`. When a spec is submitted to the hub (`spec submit`), the hub reads `campaign.yaml` and replicates its fields into the operational store's `Campaign` table. The hub supplements this with execution-time state that only the hub manages: campaign status (`active`/`complete`/`abandoned`), shared Context ids, and workspace bindings. The hub's operational store is the source of truth for campaign state at execution time; the filesystem copy is the source of truth during authoring.

### 4.3 The dependency graph

Dependencies between specs in a campaign are declared inside each spec's `tasks.json`, using the `dependencies` array with `depends_on_spec`, `from_group`, and `to_group` fields (see [spec-format_v1.3.md §8.2](spec-format_v1.3.md#82-dependencies)). Edges are authored as part of the spec, not stored separately in the campaign.

An edge reads: "spec B's workspace may not activate until spec A's task group N is complete." "Complete" means the group's verification subtask `{N}.V` has passed.

Specs with no declared dependencies are ready immediately. Specs with dependencies stay blocked until their upstream groups clear. Independent specs may run in parallel.

The hub evaluates the dependency graph at execution time by reading each spec's `tasks.json` dependencies. Because a downstream spec is often authored before its upstream is fully planned, `from_group: 0` serves as a sentinel for "upstream not yet planned" (see [spec-format_v1.3.md §8.2](spec-format_v1.3.md#82-dependencies)).

**Sentinel handling at runtime.** When the hub encounters a `from_group: 0` edge, it treats the entire upstream spec as the dependency — the downstream workspace's bootstrap is deferred until the upstream spec is `sealed` (all task groups complete). Once the upstream spec's `tasks.json` exists with concrete groups, the sentinel resolves to the actual group but remains a whole-spec gate until the edge is updated. An unresolved sentinel never silently passes; the workspace stays in `Created` until the gate clears.

### 4.4 Campaign lifecycle

| State | Meaning |
| --- | --- |
| `active` | Specs are being registered and workspaces are running. |
| `complete` | All registered specs are `sealed`. |
| `abandoned` | Stopped before completion. |

### 4.5 Workspace activation

A campaign-gated workspace defers bootstrap until its dependency gate clears. Once all upstream dependencies are satisfied, bootstrap runs and the normal `Created → Active` (or `Failed`) transition follows.

On activation the harness notifies the Operator, who then authors a PRD for that spec and continues the normal single-spec flow.

### 4.6 Shared Contexts across a Campaign

When a campaign is registered with the hub, the Operator may declare Contexts that are automatically attached to every workspace in that campaign. This is a hub-level configuration, not part of `campaign.yaml` (Contexts are hub entities that live in the Context store). Each workspace still pins its own revision at run start.

### 4.7 What a Campaign is not

A Campaign is not a spec (no `requirements.json`, no freeze, no traceability). It is not a workspace (no branch, no agents). It does not replace spec authoring: specs are still authored one at a time, either through the standalone `spec` tool or through the harness Planner (see §5).

---

## 5. The Spec Package

The coordination layer is a validated package of artifacts, not a single prose
note. The format reference is the **Spec Format Specification** at
[spec-format_v1.3.md](spec-format_v1.3.md); that document is the authority
on artifact structure, field-level schemas, EARS patterns, ID formats,
validation rules, and rendering. Where this document and the format spec
disagree on structure or field semantics, the format spec wins; where the
harness adopts a stricter operating policy (such as the freeze below), that
policy stands.

This section covers what the harness builds on top of the format.

### 5.1 The package structure

The spec separates four concerns:

- narrative intent (`prd.md`),
- what the system must do and guarantee (`requirements.json`),
- how each requirement is verified (`test_spec.json`),
- what work to do and in what order (`tasks.json`).

Architectural detail has an explicit, optional home in `architecture.md`. The harness treats the package as a unit: a workspace's spec is valid only when all four required artifacts are present and consistent.

For artifact structure, field definitions, and schema details, see [spec-format_v1.3.md §4-8](spec-format_v1.3.md).

### 5.2 Identity, completeness, and bootstrap

Specs are stored in the spec store on the filesystem, organized under their parent campaign: `<data_dir>/specs/<campaign-slug>/<spec-slug>/`. The spec-slug follows the `{NN}_{snake_case_name}` convention from the format spec. The numeric prefix is auto-assigned as `max(existing) + 1` within the campaign; collisions are rejected.

Spec creation is handled by **speclib**, a shared library used by both the standalone `spec` CLI and the harness Planner. During creation speclib operates in bootstrap mode, writing the four artifacts sequentially and deferring cross-artifact validation until all four exist. See [services-architecture.md §7](services-architecture.md#7-the-spec-creation-tool) for the full spec creation tool description.

A spec cannot move from `draft` to `active` while incomplete. See [spec-format_v1.3.md §3](spec-format_v1.3.md#3-folder-layout-and-naming) for naming and completeness rules.

### 5.3 Spec lifecycle and intent protection

The spec carries its own lifecycle in `prd.md` frontmatter, separate from the workspace lifecycle.

| State | Meaning | Mutations allowed |
| --- | --- | --- |
| `draft` | Being authored | All, including Intent edits |
| `active` | Work in progress | None to the artifacts; frozen on approval. `architecture.md` may still be revised. |
| `sealed` | Complete; no further mutation | None |
| `superseded` | Replaced by another spec; moved to archive | None; deprecation banner applied |
| `archived` | Complete and put away; moved to archive | None |

Both `superseded` and `archived` are terminal. A `superseded` spec was replaced; an `archived` spec completed normally. See [spec-format_v1.3.md §9](spec-format_v1.3.md#9-lifecycle) for full lifecycle and transition details.

At the `draft` to `active` transition the harness computes `intent_hash`:

1. Extract the markdown body following the `## Intent` heading, up to (but not including) the next heading at the same or higher level (`##` or `#`).
2. Strip all leading and trailing whitespace from the extracted text (equivalent to Python `str.strip()`).
3. Compute the SHA-256 hex digest of the resulting string.
4. Store the digest in `prd.md` frontmatter as `intent_hash`.

The harness recomputes and checks this hash whenever it loads the spec (on workspace open, on render, and in standalone `validate()`), reporting a mismatch rather than trusting the file.

### 5.4 The write contract: author once, then freeze

A spec is authored once and not changed after that. The spec is composed while in `draft` through speclib's session-based authoring model — either standalone via `spec` or through the harness Planner. The human reviews, and the `draft` to `active` transition freezes the package. From that point the declarative content is immutable. When the requirements turn out to be wrong, the response is a new spec that supersedes this one (§8.3), not an in-place edit.

Authoring is a stateful session: the agent assesses the PRD, optionally refines it through structured Q&A iterations with the user, then generates the JSON artifacts. The session works against a local campaign directory, not the hub's spec store. Once the spec is complete, the user submits it to the hub via `spec submit`. The `draft` to `active` transition (`af spec approve`) is a hub operation — see [services-architecture.md §7.4](services-architecture.md#74-relationship-to-the-harness).

Within `draft`, writes are expressed as RFC 6902 JSON Patches validated against each artifact's schema.

Execution state is not spec state. Subtask progress lives in the operational store (§9), not in the frozen `tasks.json`.

Write authority:

| Scope | Operator | Planner | Coordinator | Archetype |
| --- | --- | --- | --- | --- |
| `prd.md` body and Intent, `draft` only | write | — | — | — |
| `requirements.json`, `test_spec.json`, `tasks.json`, `draft` only | review | author | — | — |
| `architecture.md`, any time, not validated | write | write (`draft` only) | — | — |
| Any frozen artifact, `active` and later | — | — | — | — |
| Subtask execution state, operational store | — | — | read | own subtask only |

The Spec Format Specification permits mutating an `active` spec; this harness deliberately does not use that latitude and freezes at approval instead. That is a stricter operating policy, not a disagreement about the format.

### 5.5 Spec access at runtime

Agents do not read spec artifacts as files. The spec package lives in the spec store, outside the workspace checkout, and agents access it through the af SDK. This enforces the freeze structurally.

**Access gate.** Only specs in `active` status or later (`sealed`, `superseded`) are served by the harness API. Draft specs are invisible to the harness — they exist only on the filesystem and are accessible only through speclib (via `spec` or the Planner). This ensures agents never see an unapproved, unfrozen spec.

The harness provides spec content through two channels:

- **Prompt assembly (§6.3).** Before each turn the harness renders the spec slice relevant to the agent's current work and includes it in the system prompt. For a worker Archetype this is its assigned subtask plus the requirements and test specs that subtask traces to.

- **Spec read tool.** A read-only tool that lets an agent query the spec package on demand. It draws from two sources: the **frozen spec artifacts** on the filesystem (the plan) and the **operational store** (live execution state). The tool merges these to present the correct current state. It accepts a `what` parameter:

  | `what` | Returns |
  | --- | --- |
  | `artifact` | A single frozen artifact, specified by an `artifact_name` parameter: `prd`, `requirements`, `test_spec`, `tasks`, or `architecture`. |
  | `rendered` | The combined rendered markdown view. |
  | `traceability` | The traceability table, with `test_path` merged from the operational store (which tracks live test-to-requirement mappings as tests are written). |
  | `coverage` | Computed coverage from `test_spec.json`. |
  | `execution` | Live subtask execution state from the operational store: current state, assigned agent, timestamps, verification outcomes. Optionally filtered by `subtask_id`. This is not in the frozen spec. |

  Full signature: `af_spec_read(what, artifact_name?, subtask_id?)`.

There is no spec-write tool. The write path runs through speclib during authoring (via `spec` or the Planner).

### 5.6 The task model

`tasks.json` is the canonical task store. Tasks are organized into ordered task groups, each holding subtasks plus exactly one verification subtask.

Structural rules the harness enforces through schema validation:

- Task group 1 is always `kind: "tests"`.
- The final task group is always `kind: "wiring_verification"`, and there is at most one.
- `"checkpoint"` and `"standard"` groups may appear between.
- Each group carries exactly one verification subtask `{group}.V`.

For detailed task group structure, subtask fields, verification subtask format, wiring verification requirements, and traceability, see [spec-format_v1.3.md §8](spec-format_v1.3.md#8-tasksjson).

Subtask state is a fixed machine:

| State | Meaning | Allowed next |
| --- | --- | --- |
| `pending` | Not started | `queued`, `dropped` |
| `queued` | Selected for dispatch | `in_progress`, `pending`, `dropped` |
| `in_progress` | An archetype is executing it | `awaiting_verification`, `dropped` |
| `awaiting_verification` | Implementation complete; queued for verification | `done`, `pending_reevaluation`, `dropped` |
| `done` | Verification passed | `pending_reevaluation` |
| `pending_reevaluation` | Verification failed; needs rework | `pending`, `dropped` |
| `dropped` | Removed with explicit rationale | terminal |

The `dropped` transition from `in_progress` or `awaiting_verification` is a harness-initiated action on Operator command, not an Archetype-initiated transition. Under the freeze, `pending_reevaluation` captures a verification bounce-back (a group check or the final wiring gate failing a subtask).

The live state of each subtask is not stored in `tasks.json`: it lives in the operational store (§9), because the spec freezes on approval and `tasks.json` stays declarative.

Traceability links every requirement through its test spec and task to an executable test path. Because the spec freezes while tests are written during execution, `test_path` may be null in the frozen `tasks.json`; the harness tracks the live mapping in the operational store and exposes the merged view.

### 5.7 Validation

Validation is implemented in **speclib** and shared by the `spec` CLI, the harness Planner, and the harness itself. Two layers run on every authoring write while a spec is in `draft`:

1. **Schema validation** — rejects malformed structure, unknown fields, missing fields, EARS mismatches, illegal transitions, invalid IDs. Sub-millisecond.
2. **Cross-file integrity** — checks the four artifacts together: referential integrity of IDs, requirement-to-test coverage, glossary completeness, spec identity consistency, and traceability uniqueness.

A mutation that breaks integrity is rejected during drafting, so an inconsistent spec cannot be approved. speclib also exposes a standalone `validate()` for CI and pre-commit hooks.

For the full list of validation rules, see [spec-format_v1.3.md §10](spec-format_v1.3.md#10-validation).

speclib eases the path to a clean commit with a **repair pass**: a required field with an inferable value, EARS field names that map cleanly to the declared pattern, or an ID one transform from valid are auto-corrected and logged. Hard rejection is reserved for semantic failures that need a human or agent decision. Repair suggestions are recorded in the activity log (when running through the harness) or to stderr (when running standalone via `spec`).

### 5.8 Rendering

The harness provides a deterministic renderer: same JSON in produces same markdown out, byte for byte. It offers per-file rendering and a combined view (PRD, then `architecture.md` if present, then requirements, test spec, and tasks). See [spec-format_v1.3.md §11](spec-format_v1.3.md#11-rendering).

### 5.9 Grounding: the Context

Grounding is the second layer of input attached to a workspace, distinct from the spec package. Where the spec says what to build, a Context says what to know while building it. This section unifies MCP servers, skills, and rules into one abstraction, so there is a single place grounding comes from and a single precedence order.

**A Context is one instruction plus a set of typed sources, owned by a principal and reusable across workspaces.** It lives above the workspace. A Context outlives any one task: it is the durable description of a domain, maintained once and reused.

**Sources are typed, and every source declares two contracts:**

- *Resolution strategy* — `pinned` (full content in the prompt every turn) or `retrieved` (indexed, relevant chunks pulled in per turn through a tool).
- *Freshness contract* — `snapshot` (captured at a revision, immutable) or `live` (re-resolves when the origin changes).

| Source type | Default resolution | Default freshness | Notes |
| --- | --- | --- | --- |
| Repository | retrieved | live | Indexed and searched per turn. |
| File (in-repo) | pinned | live | Full contents in the prompt every turn. |
| Linked PR / issue | retrieved or pinned by size | live | |
| Linked file | pinned | live | |
| Uploaded blob | pinned | snapshot | |
| Free text | pinned | snapshot | |
| MCP server (capability) | n/a (capability) | live | Exposes external tools. |
| Skill (capability) | pinned (on demand) | snapshot | Named instruction set for a task kind. |
| Rule (capability) | pinned | live | `AGENTS.md` and user-level rules. |

MCP, skills, and rules are source types inside a Context, not separate grounding systems. The Context is the single grounding abstraction.

When a workspace attaches a Context, the harness pins it to its current revision by default. Pinning makes a run reproducible. Tracking a Context live is an explicit opt-in. Pinned revisions are recorded in the activity log at run start.

Agents read a Context; they never write to it. There is no tool that mutates a Context and no API by which an agent edits one. Editing a Context is an Operator action performed outside a run.

A Context is owned by a principal and carries an access policy. A source the acting principal cannot read is treated as absent.

---

## 6. Agents

### 6.1 The provider abstraction

The harness contains no model. It drives an external provider, and "bring
your own model" is first-class. Provider independence is achieved through
a two-tier adapter model (see
[runtime-layer.md §4](runtime-layer.md#4-harness-adapters)):

- **Tier 1 adapters** wrap a provider SDK (Claude Agent SDK for Anthropic,
  Google ADK for Google). The SDK owns the agent loop, tool dispatch, and
  prompt engineering. This gives full-fidelity access to each provider's
  model features.
- **Tier 2 (generic adapter)** runs an af-owned agent loop on
  [LangGraph](https://www.langchain.com/langgraph), with model calls routed
  through LangChain's chat model integrations. This covers open-weight
  models, local inference (Ollama, vLLM on Apple Silicon), and any
  provider with a LangChain integration or an OpenAI-compatible API.

The coordination layer extends the provider's tool set through the **af
SDK** (see [runtime-layer.md §8](runtime-layer.md#8-the-af-sdk)), a Python
library imported by each adapter that exposes hub capabilities as native
tool functions. Both adapter tiers use the SDK identically.

The coordination layer interacts with a running provider through two
channels: it injects configuration before the provider starts (system
prompt, instructions, MCP server declarations, environment variables), and
it receives tool calls and state updates through the af SDK during
execution. This contract is the same regardless of which adapter tier runs.

### 6.2 The agent execution model

Each agent runs inside an OpenShell sandbox. The execution model differs
by adapter tier, but the coordination layer sees the same behavior:

- **Tier 1 (provider SDK):** The SDK runs its own tool loop. It reads and
  writes files in the workspace checkout, executes shell commands, drives a
  browser, and calls af SDK functions and external MCP tools according to
  its own reasoning.
- **Tier 2 (generic adapter):** The af runtime runs the tool loop on
  LangGraph. It calls the model through LangChain chat models, dispatches tool use
  (file operations, shell, git, browser, MCP), and feeds results back. The
  tool set and capabilities match Tier 1; the agent loop is af-owned
  instead of SDK-owned.

From the coordination layer's perspective, both tiers produce the same
observable behavior: an agent that reads the spec, edits files, runs
commands, calls tools, and transitions subtask state. The coordination
layer observes this through the af SDK's activity logging and through the
runtime's agent state reporting.

An agent can be stopped mid-execution with its session preserved for
resume. Tier 1 adapters use the SDK's conversation continuation support;
the generic adapter uses LangGraph checkpointing. The Coordinator can send
follow-up messages to a running agent through the runtime's message
injection.

### 6.3 Prompt assembly

Before each turn the harness builds the system prompt and message set from both layers of input, pinned for the run:

- the agent's specialist role and applicable rules, composed per the precedence in §6.4;
- always-on pinned sources of attached Contexts, materialized in full;
- a rendered slice of the spec relevant to the agent;
- skills loaded on demand for this task kind;
- agent memory recalled for this work, retrieved from the agent-memory service against a revision pinned at run start (§6.6);
- conversation history.

Retrieved grounding sources are not injected — they are reached through a Context search tool when the agent needs them. Recalled agent memory follows the same discipline. Context revisions and the agent-memory revision are fixed at run start, so re-running a turn assembles the same prompt.

### 6.4 Specialists, actor capabilities, and instruction precedence

A specialist is a role: a system prompt, a tool policy, a model tier, and a behavior pattern. Each specialist carries an **actor capability** that determines its write authority over the spec package (§5.4).

| Specialist  | Actor capability | Role |
| --- | --- | --- |
| Planner     | Planner          | Drafts the JSON artifacts from the PRD while the spec is in `draft`, using speclib. The harness-mediated path for spec authoring (see §8.1). |
| Coordinator | Coordinator      | Delegates subtasks, reads execution state, triggers verification, reports ready for review. |
| Implementor | Archetype        | Implements one assigned subtask; transitions only that subtask's state. |
| Verifier    | Archetype        | Runs verification checks and wiring verification; reports pass or fail. |
| UI Designer | Archetype        | Builds and visually checks interfaces for assigned subtasks. |
| PR Reviewer | Archetype        | Reviews a pull request and gives feedback. |
| PR Shepherd | Archetype        | Drives a PR to merge-ready. |

The Operator capability is reserved for the human caller.

The format spec's actor model defines three tiers (Operator, Coordinator, Archetype). This harness splits the Coordinator into Planner (who authors during `draft`) and Coordinator (who drives execution after approval), adding a fourth tier. The split does not contradict the format spec's field semantics.

Instruction precedence: harness policy → actor-capability constraints → Context instructions → task-level instruction. A Context instruction can narrow behavior but never widens actor permissions.

### 6.5 Tools available to agents

| Tool | What it does | Notable constraint |
| --- | --- | --- |
| File read/write | Reads and edits files in the workspace checkout. | Confined to the sandbox's `/workspace`. Single agent has exclusive access. |
| Exec / script | Runs a shell command; long-running ones become managed scripts. | Output streamed to the activity log. |
| Browser control | Drives a headless browser over CDP. | For end-to-end UI verification. |
| Context search | Searches retrieved sources in attached Contexts: `af_context_search(query, context_id?, source_id?, max_results?)`. Returns ranked chunks. | Read-only against pinned revisions. |
| Context get | Fetches a pinned source in full: `af_context_get(context_id, source_id)`. | Read-only. |
| Memory recall | Searches agent memory for relevant learnings. | Read-only against the pinned memory revision. |
| MCP call | Invokes a tool from an MCP server that is a Context source. | Availability follows the attached Contexts. |
| Git | Stages, commits, opens a PR, reads PR and CI status. | Commits land on the workspace branch only. |
| Issue tracker | Read, search, create, comment on, update issues. | Backend-agnostic (GitHub, GitLab, Jira, Linear). |
| Web search | Search and fetch public web content. | Read-only; provider-agnostic. Results are untrusted data, never instructions. |
| Spec read | Fetches spec artifacts, rendered views, traceability, coverage (§5.5). | Read-only; no write path. |
| CI/CD status | Queries pipeline status, job results, and logs. | Read-only; provider-agnostic. |

There is no spec-write tool, no Context-write tool, and no memory-write tool. An agent working against an active spec interacts with its contract as a read-only surface.

**Issue tracker interface:**

```
interface IssueTracker:
    search(query: IssueQuery) → list[IssueRef]
    get(ref: IssueRef) → Issue
    create(input: NewIssue) → IssueRef
    comment(ref: IssueRef, body: string) → void
    update(ref: IssueRef, patch: IssuePatch) → void

record IssueRef:
    tracker: string
    project: string
    key:     string

record IssueQuery:
    text:     string, optional
    state:    "open" | "closed", optional
    labels:   list[string], optional
    assignee: string, optional

record NewIssue:
    title:    string
    body:     string
    labels:   list[string], optional
    assignee: string, optional

record IssuePatch:                         -- all fields optional
    title:    string, optional
    body:     string, optional
    labels:   list[string], optional
    assignee: string, optional
    state:    "open" | "closed", optional

record Issue extends IssueRef:
    title:    string
    body:     string
    state:    string
    labels:   list[string]
    comments: list[{ author: string, body: string, at: string }]
```

**Web search interface:**

```
interface WebSearch:
    search(query: string, opts: optional) → list[SearchResult]
        opts:
            count:   number, optional
            site:    string, optional
            recency: "day" | "week" | "month" | "year", optional

    fetch(url: string) → { url: string, title: string, text: string, retrievedAt: string }

record SearchResult:
    title:       string
    url:         string
    snippet:     string
    publishedAt: string, optional
```

**Untrusted external content.** Web search and `fetch` return content from arbitrary third parties. Results are injected as `tool_result` events with a fixed schema, not as free text in the system prompt.

**CI/CD interface:**

```
interface CIProvider:
    listRuns(ref: CIRef) → list[PipelineRun]
    getRun(runId: string) → PipelineRun
    getJobLog(jobId: string) → string

record CIRef:
    branch: string, optional
    sha:    string, optional
    pr:     number, optional

record PipelineRun:
    id:          string
    ref:         CIRef
    status:      "queued" | "running" | "passed" | "failed" | "cancelled"
    startedAt:   string
    completedAt: string, optional
    jobs:        list[PipelineJob]
    url:         string

record PipelineJob:
    id:          string
    name:        string
    status:      "queued" | "running" | "passed" | "failed" | "cancelled" | "skipped"
    startedAt:   string, optional
    completedAt: string, optional
```

### 6.6 The agent-memory contract

The harness contains no memory store. Agent memory is driven through one contract. The backend may run in-process or as an independent service; that choice sits behind the interface.

Memory is grounding, not coordination, so it never touches the spec package. It differs from a Context: a Context is Operator-curated truth about a domain; memory is agent-authored learnings from past work. The two compose without overlapping.

Two operations carry the contract:

```
interface AgentMemory:
    recall(input: {
        scope:    MemoryScope
        query:    string
        revision: string, optional
        budget:   optional
            maxItems:  number, optional
            maxTokens: number, optional
    }) → {
        revision: string
        items:    list[MemoryItem]
    }

    consolidate(input: {
        scope:        MemoryScope
        baseRevision: string
        learnings:    list[Learning]
        session:      SessionRef, optional
    }) → {
        revision: string
        accepted: number
    }

record MemoryScope:
    principal: string
    namespace: string

record MemoryItem:
    id:         string
    content:    string
    provenance: string
    recordedAt: string
    confidence: number, optional
    relevance:  number, optional

record Learning:
    content:    string
    provenance: string
    kind:       "episodic" | "semantic" | "procedural", optional
```

`recall` pins a revision at run start. `consolidate` runs once at session end, advancing the revision. Memory grows between runs, never during one.

---

## 7. Orchestration

### 7.1 Single-agent execution

Each workspace runs **one agent at a time**. The Coordinator reads the frozen spec, works through task groups and subtasks sequentially, implements each subtask, runs verification, and advances state. There is no parallel agent concurrency within a workspace, which eliminates the need for file claims or cross-agent coordination protocols.

The agent has exclusive access to the workspace checkout. It reads its assigned subtask and the requirements and test specs it traces to (via prompt assembly and the spec-read tool), implements the work, commits, and transitions the subtask state. When a subtask is complete, the agent moves to the next one.

```mermaid
sequenceDiagram
    participant H as Operator
    participant S as Spec package
    participant CX as Attached Contexts
    participant A as Agent
    participant RT as Checkout + op store

    H->>S: spec authored and approved
    H->>CX: attach Contexts, pin revisions
    H->>A: start execution run
    A->>CX: read grounding
    A->>S: read task groups, subtasks, requirements, tests
    loop For each task group
        loop For each subtask
            A->>RT: implement, commit, transition subtask state
        end
        A->>RT: run group verification checks
    end
    A->>RT: run wiring verification
    A-->>H: ready for review and merge
```

### 7.2 The shared store

The spec package and the operational store serve as the coordination medium, even with a single agent. The store has two layers the freeze keeps distinct:

- The **spec package** (frozen, read-only during a run) — the plan: what to build, how to verify, in what order.
- The **operational store** (subtask execution state, verification outcomes) — the progress: what has been done, what passed, what failed.

The agent reads the plan from the frozen spec and writes progress to the operational store. If the agent stops mid-run and resumes (or a new agent continues), the operational store shows exactly where work left off.

Grounding sits outside this loop. Contexts are read-only and pinned, so grounding is a stable input rather than a shared mutable surface.

### 7.3 Subtask state transitions

The agent transitions its subtask state as it progresses, ending at `awaiting_verification`. The harness moves it to `done` after verification passes or to `pending_reevaluation` on failure.

The `awaiting_verification` state is a harness extension not present in the format spec's state machine (see [spec-format_v1.3.md §8.3.1](spec-format_v1.3.md#831-subtask)). The format spec defines `in_progress → done`; the harness inserts `awaiting_verification` between them to gate completion on verification. This is a stricter operating policy, the same pattern as the freeze in §5.4.

### 7.4 Verification gate

The agent signals subtask completion by moving it to `awaiting_verification`, not to `done`. Verification then runs:

1. The group's verification subtask checks.
2. The wiring verification group (final group): traces execution paths through production code, confirms return-value propagation, runs smoke tests with real components, audits for unreplaced stubs.

On success the harness transitions the subtask to `done`. On failure it transitions to `pending_reevaluation` (and from there to `pending` if rework is needed). Verification outcomes (pass or fail per check) are recorded in the operational store.

The verification subtask (`{N}.V`) does not follow the subtask state machine. Its execution state (running, passed, failed) lives entirely in the operational store as `VerificationOutcome` records. The spec's `tasks.json` defines only the checks; the harness tracks their outcomes.

Only when the wiring verification passes does the work roll up to "ready for review."

---

## 8. Key flows

### 8.1 The generic spec-driven flow

Every spec-driven task follows one flow. Spec authoring (phases 1-2) can happen either standalone or through the harness; execution (phases 3-6) is always harness-driven.

**Phase 0: Spec authoring.** Before the harness flow begins, the spec must exist and be approved (`active`). Two paths:

- **Standalone path.** The Operator uses `spec` (or the agent skill) to author the spec in a local campaign directory, then submits it to the hub via `spec submit`. Authoring requires no hub; only submission and approval do. See [services-architecture.md §7](services-architecture.md#7-the-spec-creation-tool).
- **Harness-mediated path.** The Operator creates a workspace, then starts a Planner run. The Planner uses speclib to draft the spec within the harness, grounded in attached Contexts. The Operator reviews and approves through the harness API.

Both paths produce the same output: an `active`, frozen spec package on the filesystem under the campaign hierarchy.

**Phases 1-6: Harness execution.** Once an approved spec exists:

| Phase | Who acts | What happens |
| --- | --- | --- |
| **1. Provision** | Operator | Creates the workspace: supplies origin, references the campaign and spec, attaches Contexts. |
| **2. Bootstrap** | Harness | Runs setup scripts. On success → `Active`; on failure → `Failed`. Campaign-gated workspaces defer bootstrap. |
| **3. Execute** | Coordinator, Implementors | The Coordinator delegates subtasks. Implementors read the frozen spec, implement, commit, transition to `awaiting_verification`. |
| **4. Verify** | Verifier, Harness | Runs group verification checks and the wiring verification. Pass → `done`; fail → `pending_reevaluation` → re-delegate. |
| **5. Deliver** | Coordinator, Operator | Coordinator signals ready. Operator reviews, merges, and seals the spec. |
| **6. Close** | Operator | Archives the workspace. |

The human checkpoint is phase 5 (review the result before merge). Everything between phases 2 and 5 is agent-driven.

### 8.2 Variants

**Single worker vs. parallel workers.** The flow is identical. The difference is the shape of `tasks.json` authored during spec authoring.

**Empty origin.** Phase 1 supplies an empty directory. The spec's `tasks.json` includes scaffolding in its first group.

**Campaign with dependencies.** Multiple specs in a campaign with a dependency graph. Phase 2 is deferred until upstream dependencies clear. Each spec proceeds through phases 1-6 independently once unblocked.

### 8.3 Superseding a spec

Supersession is the modeled escape when the frozen plan is wrong.

**Superseding a completed spec.** The new spec sets `supersedes`. The harness transitions the prior spec to `superseded`, applies a deprecation banner, and moves it to the archive.

**Superseding mid-flight.** The harness:

1. Stops all running agents in the workspace.
2. Transitions every `in_progress` or `awaiting_verification` subtask to `dropped` with rationale "spec superseded."
3. Commits partial work on the branch.
4. Transitions the spec to `superseded`.

The workspace stays `Active`. The Operator authors a corrective spec (via `spec` or the Planner), then creates a new workspace referencing it on the same branch, so partial commits carry forward.

---

## 9. Data model and persistence

The harness persists state across three stores so a process restart resumes cleanly.

- **Spec store.** Holds the spec artifacts (the four required files plus optional `architecture.md`), organized under campaigns on the filesystem: `<data_dir>/specs/<campaign-slug>/<spec-slug>/`. Source of truth for spec content. Written by speclib (via `spec` or the Planner); read by the harness at execution time.
- **Context store.** Holds Contexts and their sources above the workspace, keyed by Context id and revision.
- **Operational store.** Holds everything else: workspace and campaign state, agent and run records, subtask execution state, conversation history, and the activity log.

### 9.1 Spec store

| Entity | Key fields | Notes |
| --- | --- | --- |
| SpecArtifacts | campaign slug, spec slug | Contains `prd.md`, `requirements.json`, `test_spec.json`, `tasks.json`, optionally `architecture.md`. Written by speclib. Agents access these through the spec-read tool and prompt assembly (§5.5), but only once the spec reaches `active` status. |

### 9.2 Context store

| Entity | Key fields | Notes |
| --- | --- | --- |
| Context | id, name, owner principal, access policy, instruction, current revision, timestamps | Lives above workspaces. Many workspaces may attach the same Context. Edited only by the Operator outside a run (§5.9). |
| Source | id, context id, type, locator, resolution strategy, freshness contract, revision | A typed reference inside a Context. Types include content sources and capability sources. |

### 9.3 Operational store

**Workspace and campaign layer.**

| Entity | Key fields | Notes |
| --- | --- | --- |
| Workspace | id, name, status, owner, origin, branch, base branch, remote, campaign_slug, spec_slug, timestamps | The aggregate root. References the campaign and spec it executes against. `status` follows §3.3. |
| WorkspaceConfig | workspace id, setup scripts, default provider, default model | Per-workspace overrides (§3.6). |
| Campaign | id, slug, name, status, shared Context ids, timestamps | Every spec belongs to a campaign. `campaign.yaml` on the filesystem carries the static metadata (§4.2); the operational store carries execution-time state. `status` is `active`, `complete`, or `abandoned` (§4.4). Shared Context ids are a hub-level configuration (§4.6). |
| CampaignMember | campaign id, workspace id, spec_slug | Links a workspace to its campaign and spec. Dependency edges live in each spec's `tasks.json` (§4.3), not in this entity. |
| ContextAttachment | workspace id, context id, pinned revision, pin mode, attached_at | Records which Context revision a workspace is pinned to. |

**Spec lifecycle layer.**

| Entity | Key fields | Notes |
| --- | --- | --- |
| SpecRef | campaign slug, spec slug, spec_name, status, intent_hash, schema_version, supersedes, timestamps | Lightweight summary of spec identity and lifecycle. Artifacts live in the spec store under `<data_dir>/specs/<campaign-slug>/<spec-slug>/`. |

**Run and execution layer.**

| Entity | Key fields | Notes |
| --- | --- | --- |
| Run | id, workspace id, spec_id, kind, status, timestamps | The unit of execution. A spec-driven run covers phases 3-5. |
| Agent | id, workspace id, run id, specialist role, actor capability, provider, model, phase, activity, parent agent id, timestamps | Phase tracks the container lifecycle; activity tracks what the agent is doing within `running`. See [runtime-layer.md §5](runtime-layer.md#5-agent-lifecycle). |
| SubtaskExecution | workspace id, spec_id, subtask id, run id, assigned agent id, state, drop rationale, timestamps | The live execution state. Transitions are harness-enforced (§5.6). |
| VerificationOutcome | workspace id, spec_id, run id, group id, verification subtask id, check id, result, detail, recorded_at | One row per check. The Verifier reports; the harness records and transitions accordingly (§7.4). |
| ManagedScript | workspace id, agent id, run id, command, pid, status, timestamps | Long-running process tracked for cleanup. |

**Conversation layer.**

| Entity | Key fields | Notes |
| --- | --- | --- |
| Message | id, agent id, role, content, parent message id, timestamp | `parent_message_id` supports conversation forking. |

**Observability layer.**

| Entity | Key fields | Notes |
| --- | --- | --- |
| MemoryPin | workspace id, run id, memory scope, pinned revision, recorded_at | Records which memory revision a run read. |
| ActivityEvent | id, workspace id, run id, agent id, type, payload, timestamp | Append-only event stream. Types: `text`, `thinking`, `tool_call`, `tool_result`, `spec_patch`, `context_pin`, `memory_pin`, `commit`, `status_change`, `verification_outcome`, `script_start`, `script_stop`. |

### 9.4 Persistence and recovery

All three stores are durable. The `ActivityEvent` stream is the recovery backbone: it records every spec patch, Context and memory pin, subtask transition, verification outcome, and agent action. A run's full history is reconstructable from the event stream alone.


---

## 10. Harness public surface

The API is split by audience. **Operator-facing** operations are for the human caller. **Agent-facing** operations are exposed as tools during a run. **Observability** operations are available to both.

### 10.1 Operator-facing API

**Workspace.**

- Create: supply origin, owner, campaign and spec references, Contexts with pin mode.
- Get, list (filterable by status, campaign, owner).
- Archive, reopen, delete.
- Read workspace files: file listing, file contents, git status, diffs.
- Managed scripts: list, stop.

**Campaign.**

- Create with goal document and optional shared Contexts.
- Register a spec (submitted via `spec submit`).
- Query status, dependency graph, blocked/unblocked workspaces.
- Abandon.

**Spec lifecycle.**

- Transition: `active` → `sealed`; supersede (§8.3).
- Read `intent_hash` and lifecycle state.
- Spec creation and the `draft` → `active` transition are handled by speclib, either through the standalone `spec` CLI or through the Planner specialist. The harness API does not accept writes to draft specs.

**Spec authoring (harness-mediated path).**

- Start a Planner run: the Planner uses speclib to draft artifacts within the harness. The Operator reviews and approves through the harness API. This is the harness-mediated alternative to using `spec` standalone.

**Spec read and render.**

- Fetch any artifact, the combined rendered view, coverage, traceability, or standalone `validate()` (via speclib). Only `active` or later specs are served.

**Contexts.**

- Create, get, list. Add/remove sources. Set instruction, ownership, access policy. Cut a new revision.
- Attach to a workspace (with revision + pin mode); detach.

**Agent memory.**

- Configure backend and scope. The harness drives `recall` and `consolidate` internally (§6.6).

**Runs.**

- Start spec-driven: supply workspace and spec. The harness creates a Run, pins revisions, starts the Coordinator.
- Get status; list runs.
- Stop a run (stops agents, commits partial work).

**Agents.**

- Start with specialist role, provider, and model within a run.
- Send a message; stop; fork from an earlier message.
- Subscribe to event stream.

**Orchestration.**

- Start a Planner on a PRD.
- Approve a drafted spec, or return with feedback.
- Hand off to the Coordinator.
- Query subtask execution state and verification outcomes.
- Force re-delegate a subtask.

**Config.**

- Set global and per-workspace defaults: providers, models, Contexts, memory backend, issue tracker, web search, CI/CD, notifications, setup scripts.

### 10.2 Agent-facing API (tools)

These operations are exposed as tools during a run (§6.5). Each is scoped to the agent's workspace and governed by its actor capability.

| Tool | Operations | Reference |
| --- | --- | --- |
| File read/write | Read and edit files. Single agent has exclusive checkout access. | §6.5 |
| Exec / script | Run shell commands; long-running → managed scripts. | §6.5 |
| Browser control | Drive a headless browser over CDP. | §6.5 |
| Spec read | Fetch artifacts, rendered views, traceability, coverage. Read-only. | §5.5 |
| Context search / get | Search retrieved sources; fetch pinned sources. Read-only. | §5.9 |
| Memory recall | Search agent memory. Read-only. | §6.6 |
| MCP call | Invoke a tool from an MCP server in an attached Context. | §6.5 |
| Git | Stage, commit, open PR, read PR/CI status. | §6.5 |
| Issue tracker | Read, search, create, comment, update issues. | §6.5 |
| Web search | Search and fetch public web content. | §6.5 |
| CI/CD status | Query pipelines, jobs, logs. Read-only. | §6.5 |
| Subtask state | Transition the agent's own subtask state. | §5.6, §7.4 |

### 10.3 Observability API

Read-only; available to both the Operator and diagnostic tooling.

- **Activity stream.** Subscribe to or page through `ActivityEvent` (see §9.3 for event types). Filterable by workspace, run, agent, event type, time range.
- **Grounding read (debug).** Fetch the full prompt assembled for a given turn, including spec slice, Context content, recalled memory, and composed instructions. Fetch pinned Context and memory revisions for a run.
- **Run history.** Completed runs with status, duration, and summary.

---

## 11. Non-functional requirements

The harness streams agent output with low latency. Workspace operations do not block each other. Tool execution is sandboxed to the owning workspace's checkout. Every state-changing action lands in the activity log so runs are reproducible and auditable. Provider failures and tool errors surface as events. Long-running scripts are tracked and cleaned up on archive or delete.

Schema validation is sub-millisecond. Authoring writes are atomic across files. Rendering is deterministic.

A Context's pinned revision is immutable for the duration of a run, so re-running reproduces the same grounding. Retrieval over retrieved sources adds acceptable latency without blocking other workspaces. Per-principal access on a Context is enforced at prompt-assembly time.

---

## 12. Layer boundaries

This document specifies the coordination layer. Three companion components complete the system:

- **[Spec Creation Tool](services-architecture.md#7-the-spec-creation-tool)** — speclib (shared library), the `spec` CLI, and the agent skill. Handles spec authoring independently of the harness. Writes to the spec store filesystem. The harness Planner uses speclib for the harness-mediated authoring path.

- **[Runtime Layer](runtime-layer.md)** — sandbox isolation, branch management, harness adapters per provider, agent lifecycle (phase and activity), templates, sidecar services, and the af SDK. The coordination layer drives the runtime through a narrow interface and never reaches past it.

- **[Services Architecture](services-architecture.md)** — the af hub (single stateful process owning all three stores), CLI, storage layout (filesystem + SQLite), communication protocols (HTTP/JSON for CLI, gRPC for bridge), security and isolation, deployment modes, the retrieval engine, CI/CD bridge, notification service, and web dashboard.

| Coordination layer owns | Runtime layer owns |
| --- | --- |
| Prompt assembly (what the agent is told) | Container lifecycle (how the agent runs) |
| Spec store, Context store, operational store | Branch provisioning and sandbox checkout |
| Runs, subtask state, verification gates | Agent start/stop/suspend/resume |
| Activity log (harness-level events) | Provider-level telemetry |
| The af SDK tool logic | Sandbox, env, and credential isolation |
| Specialist → template mapping | Template hydration and harness provisioning |

The af SDK is the integration point between the two layers: it is a library imported by each adapter inside the agent sandbox (runtime) that communicates directly with the hub (coordination) over gRPC. The coordination layer does not know whether the sandbox runs on a local Docker or Podman backend or on a Kubernetes cluster — OpenShell abstracts the container backend.

---

## Appendix A: terminology

| Term | Meaning in this document |
| --- | --- |
| Campaign | The organizational unit for specs. Every spec belongs to a campaign. Owns a goal document, workspaces, a dependency graph, and orchestration state. Also the top-level directory in the spec store filesystem. |
| Workspace branch | A git branch dedicated to a workspace, checked out inside the sandbox at `/workspace`. |
| Spec package | The validated four-artifact set (plus optional `architecture.md`). Format details in [spec-format_v1.3.md](spec-format_v1.3.md). |
| PRD | `prd.md`: human-authored intent, goals, non-goals, with hashed Intent and frontmatter. |
| Requirements | `requirements.json`: EARS criteria, correctness properties, execution paths, error handling. |
| Test spec | `test_spec.json`: language-agnostic test contract with computed coverage. |
| Tasks | `tasks.json`: task groups, subtasks, dependencies, traceability. |
| Architecture | `architecture.md`: optional, free-form, unvalidated module and interface design. |
| EARS | Easy Approach to Requirements Syntax; the six-pattern language for acceptance criteria. |
| Context | A durable, owned, reusable bundle of grounding: one instruction and typed sources. Read-only to agents. |
| Source | A typed reference inside a Context with a resolution strategy and freshness contract. |
| Resolution strategy | `pinned` (full content in prompt every turn) or `retrieved` (indexed, pulled in per turn). |
| Freshness contract | `snapshot` (fixed at a revision) or `live` (re-resolves on origin change). |
| Pinned revision | The Context revision a workspace is fixed to for its runs. |
| Planner | Harness specialist that drafts JSON artifacts from PRD input during `draft`, using speclib. The harness-mediated path for spec authoring. |
| speclib | The shared library for spec creation, validation, and rendering. Used by `spec`, the Planner, and the harness. See [services-architecture.md §7](services-architecture.md#7-the-spec-creation-tool). |
| spec | Standalone CLI for spec authoring. Wraps speclib. Works without the hub. See [services-architecture.md §7](services-architecture.md#7-the-spec-creation-tool). |
| Coordinator | Agent that drives execution after spec approval: delegates, monitors, triggers verification. |
| Archetype | Agent that executes a subtask and transitions only its own subtask's state. |
| Awaiting verification | Subtask state set by an Implementor on completion; harness moves to `done` or `pending_reevaluation`. |
| Actor capability | The permission tier (Operator, Planner, Coordinator, Archetype). |
| Specialist | A named agent role: prompt, tool policy, model tier, behavior, and actor capability. |
| Intent hash | SHA-256 of the PRD Intent section, set at draft-to-active and protected thereafter. |
| Provider | External model backend (Anthropic, Google, OpenRouter, Ollama, etc.) driven through a harness adapter. |
| Tier 1 adapter | Harness adapter wrapping a provider SDK (Claude Agent SDK or Google ADK). The SDK owns the agent loop. See [runtime-layer.md §4.1-4.2](runtime-layer.md#41-claude-agent-sdk-adapter-tier-1). |
| Generic adapter | Tier 2 harness adapter: af-owned agent loop on LangGraph with LangChain chat models for provider routing. Covers open-weight, local, and long-tail providers. See [runtime-layer.md §4.3](runtime-layer.md#43-generic-adapter-tier-2--langgraph). |
| Claude Agent SDK | Anthropic's SDK for building agents with Claude models. Tier 1 adapter wraps this. |
| Google ADK | Google's Agent Development Kit for building agents with Gemini models. Tier 1 adapter wraps this. |
| LangGraph | LangChain's low-level orchestration runtime for stateful agents. Used by the generic adapter for durable execution, streaming, and checkpointing. |
| LangChain chat models | Provider-specific model integrations (ChatOllama, ChatOpenAI, etc.) that handle API translation and tool-calling format differences. Used by the generic adapter for provider routing. |
| Runtime layer | Infrastructure layer: sandboxes, branches, adapters, agent lifecycle. See [runtime-layer.md](runtime-layer.md). |
| OpenShell | NVIDIA's open-source sandbox runtime for agent isolation. See [runtime-layer.md §2.1](runtime-layer.md#21-openshell-adapter-default). |
| Harness adapter | Runtime adapter integrating a provider into the af runtime. Two tiers: provider SDK (Tier 1) and generic (Tier 2). See [runtime-layer.md §4](runtime-layer.md#4-harness-adapters). |
| Template | Blueprint for agent configuration. See [runtime-layer.md §6](runtime-layer.md#6-templates). |
| af SDK | Python library imported by each adapter inside the agent sandbox. Exposes hub capabilities as native tool functions. See [runtime-layer.md §8](runtime-layer.md#8-the-af-sdk). |
| af hub | Long-running host process owning the three stores. See [services-architecture.md §2](services-architecture.md#2-the-af-hub). |
| af CLI | Operator's command-line interface. See [services-architecture.md §3](services-architecture.md#3-the-af-cli). |
| Memory service | Pluggable backend for agent memory. See [services-architecture.md §6](services-architecture.md#6-the-memory-service). |
| Activity log | Append-only, ordered event stream covering all state-changing actions. |
