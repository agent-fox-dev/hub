# Agentic Development Harness

A headless harness for spec-driven, multi-agent software development. The
harness gives each unit of work an isolated workspace with its own branch,
files, and agents, and coordinates those agents through a validated
specification package rather than ad-hoc chat. It is provider-agnostic:
Claude Code, Gemini CLI, and OpenCode are interchangeable through a
single adapter interface.

The design is inspired by [Intent](https://www.intentapp.dev) from Augment
Code but diverges intentionally: headless instead of desktop, coordination
rebuilt on a structured spec package that freezes on approval, and all
grounding unified under a single Context abstraction.

Start with the **[Architecture overview](docs/README.md)** for the
three-layer system design, or the **[Spec Format overview](docs/specs/README.md)**
for the specification package that drives every unit of work.

## Documents

### Architecture

| Document | Description |
| --- | --- |
| [Architecture Overview](docs/README.md) | Three-layer architecture diagram and layer responsibilities. Start here. |
| [Coordination Layer](docs/coordination-layer.md) | Domain model, workspaces, campaigns, spec package integration, agents, multi-agent orchestration, key flows, data model, and API surface. |
| [Runtime Layer](docs/runtime-layer.md) | Container runtime interface, git worktree management, harness adapters, agent lifecycle, templates, sidecar services, and the af SDK. |
| [Services Architecture](docs/services-architecture.md) | Deployable components (hub, CLI, runtime engine, memory service), the spec creation tool, storage layout, communication protocols, security, deployment modes, and web dashboard. |

### Specification Format

| Document | Description |
| --- | --- |
| [Spec Format Overview](docs/specs/README.md) | Overview of the specification package: artifacts, EARS patterns, validation, lifecycle, and traceability. |
| [Spec Format Specification](docs/specs/spec-format_v1.3.md) | Full on-disk format for a spec package. Field-level schemas, EARS patterns, validation rules, ID formats, and rendering. |

## Key Concepts

| Term | Definition |
| --- | --- |
| **Campaign** | The organizational unit for specs. Every spec belongs to a campaign. Owns a goal document, a dependency graph, and orchestration state. Also the top-level directory in the spec store (`<data_dir>/specs/<campaign>/`). |
| **Workspace** | The isolation boundary for one task: a git worktree on a dedicated branch, one spec package, attached Contexts, running agents, and an activity log. References a campaign and spec. |
| **Spec package** | A validated set of four artifacts (`prd.md`, `requirements.json`, `test_spec.json`, `tasks.json`) that define and verify the work. Authored once in `draft`, frozen on approval. See [Spec Format Specification](docs/specs/spec-format_v1.3.md). |
| **speclib / spec** | The standalone spec creation tool. speclib is the shared library; `spec` is the CLI wrapper. Creates, validates, renders, and manages spec packages on the filesystem — no hub required. Also available as an agent skill. |
| **Context** | A durable, reusable bundle of grounding: one instruction plus typed sources (files, repos, MCP servers, skills, rules). Read-only to agents; owned by the Operator. |
| **Agent** | A running model instance backed by an external provider, with a specialist role and scoped tools, executing inside a workspace. |
| **Specialist** | A named agent role (Planner, Coordinator, Implementor, Verifier, etc.) carrying a system prompt, tool policy, model tier, and actor capability. |
| **Provider** | An external agent backend (Claude Code, Gemini CLI, Codex, OpenCode) the harness drives through a uniform adapter interface. |
| **Coordinator pattern** | Agents coordinate through a shared store (the frozen spec + the operational store), not by messaging each other. The Coordinator delegates subtasks; workers write only their own execution state. |
| **af hub** | The single stateful host process: owns all three stores, manages runs, enforces the spec lifecycle, serves the coordination API, and receives MCP bridge connections. |
| **af MCP bridge** | A sidecar MCP server inside each agent container that exposes harness tools (spec read, Context search, memory recall, subtask state, file claims) to the provider. |
