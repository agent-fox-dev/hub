# Runtime Layer Specification

**Version:** 1.0
**Status:** Draft

This document specifies the runtime layer that sits underneath the af
coordination layer. It covers container isolation, git branch management,
harness adapters, agent lifecycle, templates, sidecar services, and the af
SDK. The design follows patterns established by Google's Scion project,
adapted to our requirements.

The coordination layer (domain model, spec package, agents, orchestration)
is specified in [coordination-layer.md](coordination-layer.md). The services
architecture (hub, CLI, storage, deployment) is specified in
[services-architecture.md](services-architecture.md).

---

## 1. Design principles

1. **Thin and focused.** The runtime handles infrastructure; it has no opinion
   on specs, Contexts, coordination, or verification. It starts sandboxes,
   manages branches, and exposes agent lifecycle operations.

2. **Provider-agnostic.** Anthropic, Google, open-weight, and local models
   are interchangeable through one harness adapter interface. Provider SDK
   adapters (Tier 1) and the generic LangGraph adapter (Tier 2) implement
   the same interface.

3. **Sandbox-first isolation.** Each agent runs in its own sandbox managed
   by [NVIDIA OpenShell](https://github.com/NVIDIA/OpenShell). The repo is
   cloned inside the sandbox and checked out to the workspace branch;
   everything else (spec store, harness configuration, sibling agents) is
   invisible. OpenShell enforces isolation out-of-process through
   defense-in-depth: kernel-level filesystem restrictions, network egress
   filtering, and syscall sandboxing — the agent cannot override its own
   guardrails.

4. **The coordination layer drives.** The runtime exposes a narrow API. The
   coordination layer calls it to start/stop agents, provision branches, and
   inject configuration. The runtime never calls back into the coordination
   layer — the af SDK handles that direction (§8).

5. **Portable across sandbox backends.** OpenShell abstracts the underlying
   container backend: Docker, Podman, MicroVM, and Kubernetes are supported
   through a single sandbox interface. The af runtime delegates container
   concerns to OpenShell rather than implementing backend adapters directly.

---

## 2. Container runtime interface

The runtime abstracts the container backend behind one interface. Every
operation the coordination layer needs goes through it. The primary
implementation is an adapter over
[NVIDIA OpenShell](https://github.com/NVIDIA/OpenShell)'s sandbox API (§2.1).

```
interface ContainerRuntime:
    create(spec: ContainerSpec) → ContainerId
    start(id: ContainerId) → void
    stop(id: ContainerId, timeout: Duration) → void
    remove(id: ContainerId) → void
    exec(id: ContainerId, command: list[string]) → ExecResult
    logs(id: ContainerId, follow: boolean) → stream[string]
    inspect(id: ContainerId) → ContainerState

record ContainerSpec:
    image:      string
    name:       string
    mounts:     list[Mount]              -- agent home, optional host mounts
    env:        map[string, string]
    command:    list[string]
    services:   list[ServiceSpec]        -- sidecar processes
    resources:  ResourceLimits, optional -- CPU, memory caps

record Mount:
    source:    string   -- host path
    target:    string   -- container path
    readonly:  boolean

record ContainerState:
    id:        ContainerId
    status:    "created" | "running" | "stopped" | "error"
    exitCode:  number or null
    startedAt: string or null
    stoppedAt: string or null
```

### 2.1 OpenShell adapter (default)

The default implementation. Uses
[NVIDIA OpenShell](https://github.com/NVIDIA/OpenShell) (Apache 2.0, Rust)
to create, manage, and enforce isolation on agent sandboxes. OpenShell
itself supports Docker, Podman (rootless), MicroVM, and Kubernetes as
underlying container backends, so the af runtime does not implement
backend-specific adapters.

**Sandbox lifecycle.** Each `ContainerRuntime` operation maps to an
OpenShell sandbox operation:

| `ContainerRuntime` method | OpenShell equivalent |
| --- | --- |
| `create(spec)` | `openshell sandbox create --from <image>` with mounts, env, and policy |
| `start(id)` | Sandbox starts on creation; no separate start step |
| `stop(id, timeout)` | `openshell sandbox stop` with configurable grace period |
| `remove(id)` | `openshell sandbox rm` |
| `exec(id, command)` | `openshell sandbox exec` |
| `logs(id, follow)` | `openshell logs --tail` |
| `inspect(id)` | Sandbox status query via API |

**Repository checkout.** The sandbox clones the project repository and
checks out the workspace branch at `/workspace`. Each sandbox gets its own
independent clone — no shared git state between sandboxes. The agent's home
directory is at a configurable path (default `/home/agent`). OpenShell's
filesystem policy locks allowed paths at sandbox creation via Landlock
LSM — kernel-enforced, not bypassable from inside the sandbox.

**Credential injection.** OpenShell manages credentials as *providers*:
named credential bundles injected into sandboxes at creation. The CLI
auto-discovers credentials for recognized providers (Anthropic, OpenAI,
Google, GitHub) from the host's shell environment, or the Operator can
create providers explicitly via `openshell provider create`. Credentials
are injected as environment variables at runtime and never appear on the
sandbox filesystem.

**Defense-in-depth isolation.** OpenShell enforces constraints
out-of-process — the agent cannot override its own guardrails:

| Layer | Mechanism | Enforcement |
| --- | --- | --- |
| Filesystem | Landlock LSM | Kernel-enforced path allowlists, locked at sandbox creation |
| Network | HTTP CONNECT proxy with OPA/Rego policies | Deny-by-default; every outbound connection evaluated at the HTTP method and path level |
| Process | Seccomp BPF filters | Blocks privilege escalation, dangerous syscalls, and socket creation outside the proxy |
| Inference | Privacy router | Routes LLM API calls based on policy; strips caller credentials, injects backend credentials |

Static protections (filesystem, process) are immutable after creation.
Dynamic protections (network, inference) can be hot-reloaded on running
sandboxes via `openshell policy set`.

**Policy model.** Sandbox behavior is governed by declarative YAML policies.
The af runtime ships a default policy that allows: read-write access to
`/workspace` and the agent home, read-only access to template files,
localhost egress to the hub's agent API port, and inference egress to the
configured model provider. The Operator can customize policies per workspace
or per agent role via the configuration hierarchy (§11).

**Inference routing.** The privacy router intercepts LLM API calls from the
harness and routes them based on policy — not the agent's preference. This
enables deployments where sensitive context stays on-device using local
open-weight models while frontier models (Claude, GPT) are used only when
policy allows. The router is model-agnostic by design.

**Built-in provider support.** OpenShell ships with pre-configured
credential auto-discovery for Anthropic, OpenAI, Google, and GitHub. The
af harness adapters (SDK-based or generic) run inside sandboxes without
modification. The default sandbox image includes Python 3.14, Node 22,
git, and standard developer tools.

### 2.2 Kubernetes deployment

OpenShell provides an experimental Helm chart for Kubernetes deployment.
The same `ContainerRuntime` adapter works because OpenShell abstracts the
container backend — the adapter calls `openshell sandbox create` regardless
of whether the sandbox runs as a Docker container, a Podman pod, or a
Kubernetes pod. Kubernetes-specific concerns (init containers, CSI volumes,
pod scheduling) are handled by OpenShell, not by the af runtime.

### 2.3 Direct container fallback

For environments where OpenShell is not available (minimal setups, custom
container runtimes), the `ContainerRuntime` interface supports a direct
Podman or Docker adapter. This fallback uses the container engine's socket
API directly, with the repo cloned and checked out inside the container. It provides
process-level isolation but lacks OpenShell's defense-in-depth (no Landlock,
no network proxy, no inference routing). The interface contract is
identical; only the isolation guarantees are weaker.

---

## 3. Git branch management

Each workspace gets its own branch. The sandbox provides filesystem
isolation — each sandbox clones the repo and checks out the workspace
branch, so parallel workspaces never share a working directory.

```
interface BranchManager:
    create(input: {
        repoUrl:    string    -- remote URL or local path to clone from
        branch:     string    -- e.g. "af/add-dark-mode"
        baseBranch: string    -- e.g. "main"
    }) → BranchInfo

    delete(branch: string) → void

    list(repoUrl: string) → list[BranchInfo]

record BranchInfo:
    branch:     string
    baseBranch: string
    head:       string   -- current commit SHA
```

### 3.1 Branch naming

Branches follow the convention `af/<workspace-name>`, e.g.
`af/add-dark-mode`. The prefix is configurable per installation. Collisions
are rejected at creation.

### 3.2 Sandbox checkout

When a sandbox starts, the runtime clones the repo inside the sandbox
filesystem and checks out the workspace branch at `/workspace`. The clone
is fresh — no untracked files, environment files, secrets, or installed
dependencies carry over. Each sandbox has its own independent clone; there
is no shared git state between sandboxes.

### 3.3 Lifecycle

- **Create:** `git branch` from the base branch. The branch exists in the
  remote but no checkout happens until a sandbox starts.
- **Sandbox start:** `git clone --branch <workspace-branch>` inside the
  sandbox. The clone is ephemeral — it lives only for the sandbox's
  lifetime.
- **Delete:** `git branch -d` when the workspace is deleted. The
  coordination layer decides when to delete (see
  [coordination-layer.md §3.7](coordination-layer.md#37-workspace-ownership-and-management));
  the runtime executes it.

---

## 4. Harness adapters

A harness adapter integrates one provider into the runtime. It handles
everything provider-specific so the coordination layer sees a uniform
interface. Adapters fall into two tiers:

- **Tier 1 — Provider SDK adapters.** The
  [Claude Agent SDK](https://docs.anthropic.com/en/docs/agents-and-tools/claude-agent-sdk)
  (Anthropic) and
  [Google ADK](https://google.github.io/adk-docs/)
  (Google) give full-fidelity access to their respective model families. The
  provider SDK owns the agent loop, tool dispatch, and prompt engineering. The
  adapter wraps the SDK, not a CLI.
- **Tier 2 — Generic adapter.** For all other providers — open-weight models,
  local inference, and the long tail of API-based services — the af runtime
  owns the agent loop. [LangGraph](https://www.langchain.com/langgraph)
  provides the runtime (durable execution, streaming, checkpointing) and
  [LangChain's chat model integrations](https://docs.langchain.com/oss/python/integrations/chat/)
  provide provider routing across Ollama, OpenAI, OpenRouter, Together,
  Fireworks, vLLM, and many more.

Both tiers implement the same `HarnessAdapter` interface. The coordination
layer does not know which tier is running.

```
interface HarnessAdapter:
    name() → string

    -- Build the container command to launch the harness.
    -- `resume` indicates whether to continue a prior session.
    getCommand(input: {
        task:     string
        resume:   boolean
        baseArgs: list[string]
    }) → list[string]

    -- Return harness-specific environment variables.
    getEnv(input: {
        agentName: string
        agentHome: string
    }) → map[string, string]

    -- Perform harness-specific setup in the agent's home directory
    -- after templates are copied. Called once at agent creation.
    provision(input: {
        agentName:     string
        agentHome:     string   -- host path to agent home dir
        workspacePath: string   -- path to workspace checkout
    }) → void

    -- Inject system prompt content into the harness's expected location.
    injectSystemPrompt(agentHome: string, content: string) → void

    -- Inject agent instructions (rules, conventions) into the harness's
    -- expected location.
    injectInstructions(agentHome: string, content: string) → void

    -- Translate external MCP server configs (from attached Contexts) into
    -- the harness's native MCP configuration format.
    applyMCPServers(
        agentHome: string,
        servers: map[string, MCPServerConfig]
    ) → void

    -- Resolve authentication: select the best auth method and return
    -- the env vars and file mounts needed to inject credentials.
    resolveAuth(auth: AuthConfig) → ResolvedAuth

    -- Whether this harness supports session suspend/resume.
    supportsResume() → boolean

    -- The key sequence to interrupt the harness process (e.g. "Ctrl-C").
    interruptKey() → string
```

### 4.1 Claude Agent SDK adapter (Tier 1)

Wraps the
[Claude Agent SDK](https://docs.anthropic.com/en/docs/agents-and-tools/claude-agent-sdk)
for programmatic access to Anthropic models. The SDK owns the agent loop:
it makes model calls, dispatches tool use, and manages conversation state.
The adapter configures the SDK and maps its lifecycle to the
`HarnessAdapter` interface.

- **Agent loop:** SDK-managed. The SDK calls the Anthropic API, executes
  tool calls, and loops until the task is complete. The af runtime does not
  sit in this loop.
- **System prompt:** Injected via the SDK's system prompt parameter at
  agent creation.
- **af SDK integration:** The adapter imports the af SDK and registers
  its functions (`af.spec_read`, `af.context_search`, `af.subtask_state`,
  etc.) as Claude Agent SDK tools. The SDK communicates directly with the
  hub over gRPC — no sidecar process.
- **External MCP servers:** For MCP servers from attached Contexts, the
  SDK's native MCP support is used. These are external tools, not the af
  hub connection.
- **Model features:** Extended thinking, prompt caching, computer use, and
  token-efficient tool results are available through the SDK as Anthropic
  ships them — no adapter changes needed.
- **Auth:** `ANTHROPIC_API_KEY` env var for direct API access. Vertex AI
  and AWS Bedrock credentials for cloud deployments.
- **Resume:** Supported. The SDK supports conversation continuations; the
  adapter persists conversation state for suspend/resume.
- **Models:** All Anthropic models (Opus, Sonnet, Haiku families).

### 4.2 Google ADK adapter (Tier 1)

Wraps the
[Google Agent Development Kit (ADK)](https://google.github.io/adk-docs/)
for programmatic access to Gemini models. Like the Claude adapter, the SDK
owns the agent loop.

- **Agent loop:** ADK-managed. The ADK calls the Gemini API, dispatches
  tool calls, and manages session state.
- **System prompt:** Injected via the ADK's instruction parameter at agent
  creation.
- **af SDK integration:** The adapter imports the af SDK and registers
  its functions as ADK tools. The SDK communicates directly with the hub
  over gRPC.
- **External MCP servers:** The ADK's MCP support is used for MCP servers
  from attached Contexts.
- **Auth:** `GEMINI_API_KEY` for API access, or
  `GOOGLE_APPLICATION_CREDENTIALS` for Google Cloud deployments.
- **Resume:** Supported. The ADK supports session persistence; the adapter
  maps this to the harness suspend/resume lifecycle.
- **Models:** All Gemini models.

### 4.3 Generic adapter (Tier 2 — LangGraph)

For all providers beyond Anthropic and Google: open-weight models, local
inference, and the long tail of API-based services. The af runtime owns
the agent loop in this tier.

**Architecture:**

```
af agent loop (Python)
  └── LangGraph runtime (durability, streaming, checkpointing)
       └── LangChain chat models (provider routing)
            ├── ChatOllama              -- local, Apple Silicon
            ├── ChatOpenAI              -- OpenAI, OpenRouter, vLLM, llama.cpp
            ├── ChatFireworks           -- Fireworks AI
            ├── ChatTogether            -- Together AI
            └── ...community chat model integrations
```

- **Agent loop:** Owned by the af runtime. A Python process running on the
  LangGraph runtime makes model calls through LangChain chat model
  integrations, dispatches tool use, and manages conversation state. The
  loop implements the same tool-calling pattern as the Tier 1 adapters:
  call the model, execute any tool calls, feed results back, repeat until
  done.
- **LangGraph runtime:** Provides durable execution (agents survive crashes
  and resume from the last checkpoint), streaming (token-level and event-
  level), state persistence, and human-in-the-loop interrupts. The harness
  `suspend()`/`resume()` lifecycle maps directly to LangGraph checkpoints.
- **Provider routing:** LangChain's chat model integrations handle provider-
  specific API translation and tool-calling format differences. Each provider
  has a dedicated chat model class (e.g. `ChatOllama`, `ChatOpenAI`,
  `ChatTogether`). Provider switching is a configuration change, not a code
  change.
- **System prompt:** The af runtime owns the system prompt for this tier.
  A base prompt template covers coding capabilities (file editing, shell
  execution, git operations, verification). Model-tier-specific overrides
  adjust for capability differences between frontier, mid-tier, and local
  models.
- **Tool set:** File read/write/edit, shell execution, git operations,
  browser control (CDP) — the same capabilities as the Tier 1 adapters,
  implemented as LangGraph tool nodes. The af SDK functions are registered
  as additional tool nodes alongside the built-in tools.
- **External MCP servers:** For MCP servers from attached Contexts, the
  generic adapter connects to them programmatically using LangChain's MCP
  client support.
- **Auth:** Provider-specific API keys following each chat model's
  conventions (e.g. `OPENAI_API_KEY`, `OPENROUTER_API_KEY`,
  `TOGETHER_API_KEY`). For local inference via Ollama or vLLM, no API key
  is needed.
- **Resume:** Supported via LangGraph checkpointing. Conversation state,
  tool history, and agent context are persisted and restored on resume.
- **Local inference:** Ollama (macOS, Linux) and vLLM (GPU servers) are
  supported through LangChain's `ChatOllama` and OpenAI-compatible chat
  model integrations. Apple Silicon deployments use Ollama with quantized
  models (e.g. Qwen3-32B, DeepSeek-R1-14B). Any model server that exposes
  an OpenAI-compatible API works through `ChatOpenAI` with a custom base
  URL.

### 4.4 Credential injection under OpenShell

When running under OpenShell (§2.1), credential injection is handled by
OpenShell's provider mechanism rather than by each adapter individually.
OpenShell auto-discovers credentials for recognized providers from the
host's shell environment and injects them into the sandbox as environment
variables. The adapter's `resolveAuth()` delegates to this mechanism.

| Adapter | OpenShell auto-discovery | Fallback |
| --- | --- | --- |
| Claude Agent SDK | `ANTHROPIC_API_KEY` | `openshell provider create` for Vertex AI, Bedrock |
| Google ADK | `GEMINI_API_KEY`, `GOOGLE_APPLICATION_CREDENTIALS` | `openshell provider create` for Vertex AI |
| Generic (LangGraph) | `OPENAI_API_KEY`, `OPENROUTER_API_KEY`, etc. | `openshell provider create --type custom` |
| Generic (local) | None needed | Ollama/vLLM run inside or alongside the sandbox |

### 4.5 Adding a new adapter

Implement the `HarnessAdapter` interface. Register it in the adapter
registry. No changes to the coordination layer or the sandbox runtime.

For providers with a mature agent SDK, implement a Tier 1 adapter that
wraps the SDK. For providers with a LangChain chat model integration (or
an OpenAI-compatible API), no new adapter is needed — configure the
generic adapter with the appropriate provider and model.

---

## 5. Agent lifecycle

The runtime manages agent lifecycle through a state model and a set of
operations the coordination layer calls.

### 5.1 Agent state

Two dimensions, following the Scion pattern:

**Phase** — the container lifecycle:

| Phase | Meaning | Transitions |
| --- | --- | --- |
| `created` | Container spec built, not yet started. | → `provisioning` |
| `provisioning` | Harness adapter running `provision()`, template hydration. | → `starting`, → `error` |
| `starting` | Container starting, harness initializing. | → `running`, → `error` |
| `running` | Harness active, agent working. | → `stopping`, → `suspended`, → `error` |
| `stopping` | Graceful shutdown in progress (SIGTERM sent). | → `stopped` |
| `stopped` | Container exited cleanly. Session ended. | → `provisioning` (fresh start) |
| `suspended` | Container torn down with intent to resume. | → `starting` (resume) |
| `error` | Container exited with non-zero code or setup failed. | → `provisioning` (retry) |

**Activity** — what the agent is doing within the `running` phase:

| Activity | Meaning |
| --- | --- |
| `working` | Agent actively editing, running tools. |
| `thinking` | Agent reasoning (model inference in progress). |
| `waiting_for_input` | Agent waiting for human or Coordinator input. |
| `completed` | Agent finished its task (sticky until restart/stop). |
| `idle` | Agent running but not currently active. |

The coordination layer maps these to its own concepts: a `running` agent
with activity `completed` triggers the Coordinator to check subtask state.
A `stopped` or `error` phase triggers error handling in the run.

### 5.2 Lifecycle operations

```
interface AgentLifecycle:
    -- Create an agent: build container spec, provision harness, copy
    -- template, inject system prompt and af SDK config. Does not start.
    create(input: {
        name:         string
        workspace:    WorkspaceRef
        template:     TemplateRef
        systemPrompt: string
        instructions: string
        mcpServers:   map[string, MCPServerConfig]
        env:          map[string, string]
        services:     list[ServiceSpec]
    }) → AgentRef

    -- Start a created or stopped agent. Fresh session.
    start(ref: AgentRef, task: string) → void

    -- Resume a suspended agent. Continues the prior session.
    -- Falls back to fresh start if the harness doesn't support resume.
    resume(ref: AgentRef, task: string, optional) → void

    -- Graceful stop. Sends SIGTERM, waits for timeout, then SIGKILL.
    stop(ref: AgentRef, timeout: Duration, optional) → void

    -- Suspend: stop with intent to resume.
    -- Only for harnesses that support session resume.
    suspend(ref: AgentRef) → void

    -- Remove agent: stop if running, delete container, optionally
    -- delete home directory and workspace branch.
    delete(ref: AgentRef, cleanup: { branch: boolean; home: boolean }, optional) → void

    -- Send a message to a running agent's input stream.
    message(ref: AgentRef, text: string) → void

    -- Query current state.
    state(ref: AgentRef) → { phase: Phase, activity: Activity, detail: string, optional }

    -- Stream agent output.
    logs(ref: AgentRef, follow: boolean) → stream[string]
```

### 5.3 Session management

Agents run inside a terminal multiplexer (tmux) within the container. This
gives:

- **Detached execution.** The agent runs in the background; the user or
  coordination layer attaches/detaches without interrupting work.
- **Session persistence.** On suspend, the tmux session's state (including
  the harness's conversation history, if the harness supports it) enables
  resume.
- **Input injection.** The `message` operation sends text into the tmux pane,
  which the harness reads as user input.

---

## 6. Templates

A template is a blueprint for agent configuration. The coordination layer's
specialists (see [coordination-layer.md §6.4](coordination-layer.md#64-specialists-actor-capabilities-and-instruction-precedence))
map to templates: the specialist defines the role semantically (actor
capability, tool policy); the template defines the configuration
mechanically (system prompt file, env vars, external MCP servers).

### 6.1 Template structure

```
templates/
  <template-name>/
    template.yaml           # metadata and configuration
    home/                   # files copied to the agent's home directory
      CLAUDE.md             # (or .gemini/system_prompt.md, etc.)
      ...
```

### 6.2 Template configuration

```yaml
name: implementor
harness: claude-sdk                     # claude-sdk | google-adk | generic
description: "Implements a subtask against a frozen spec."

env:
  AF_ROLE: implementor

# For generic adapter only: LangChain chat model provider and model
# model: ollama:qwen3-32b

# The af SDK connects to the hub using these env vars (injected by the runtime):
#   AF_HUB_HOST, AF_HUB_PORT, AF_AGENT_TOKEN,
#   AF_WORKSPACE_ID, AF_AGENT_ID, AF_RUN_ID

# External MCP servers from attached Contexts (if any):
# mcp_servers:
#   external-tool:
#     transport: stdio
#     command: /usr/local/bin/some-mcp-server
```

### 6.3 Template resolution

Templates are resolved in order: project-level (`.af/templates/`)
overrides global (`<data_dir>/templates/`), which overrides built-in defaults.
The coordination layer can also pass inline configuration at agent creation
time, which overrides the template.

### 6.4 Built-in templates

The runtime ships default templates for each specialist role:

| Template | Harness | Role |
| --- | --- | --- |
| `planner` | configurable | Drafts spec artifacts during `draft`. |
| `coordinator` | configurable | Delegates subtasks, monitors execution. |
| `implementor` | configurable | Implements one subtask. |
| `verifier` | configurable | Runs verification checks. |

Each template includes the af SDK as a dependency. The SDK connects to
the hub using environment variables injected at sandbox creation
(`AF_HUB_HOST`, `AF_HUB_PORT`, `AF_AGENT_TOKEN`, etc.). The adapter
(Claude Agent SDK, Google ADK, or generic) is configurable per template
via the `harness` field. When using the generic adapter, the `model`
field selects the LangChain chat model provider and model.

---

## 7. Sidecar services

A sidecar service is a long-running process that runs alongside the harness
inside the agent's container. The runtime manages sidecar lifecycle: start
before the harness, health-check, restart on failure, stop on agent stop.

```
record ServiceSpec:
    name:       string
    command:    list[string]
    restart:    "always" | "on-failure" | "never"
    env:        map[string, string], optional
    readyCheck: optional
        type:    "tcp" | "http" | "delay"
        target:  string   -- "localhost:7400", "http://localhost:8080/health", "3s"
        timeout: string   -- max wait before giving up
```

The harness does not start until all sidecar services with readiness checks
have reported ready.

### 7.1 Sidecars under OpenShell

OpenShell sandboxes are full environments, not single-process containers.
Each sandbox includes a shell, language runtimes, and standard developer
tools. Sidecar services run as background processes inside the sandbox,
managed by the runtime's service supervisor.

The sandbox policy must allow the network access sidecars require.
Sidecars that reach external services need corresponding network policy
entries — either in the default policy or in a per-workspace override.

---

## 8. The af SDK

The af SDK is the integration point between the runtime layer and the
coordination layer. It is a Python library imported by each harness
adapter, exposing hub capabilities (spec read, Context search, memory
recall, subtask state transitions) as native tool functions that the
adapter registers with its provider.

### 8.1 Why an SDK, not a sidecar

The harness adapters are Python libraries (provider SDKs or LangGraph).
The natural extension point is a function import, not a network protocol.
The af SDK communicates directly with the hub over gRPC — no intermediate
sidecar process, no MCP server, no readiness check, no heartbeat protocol.
Each adapter registers the SDK's functions as native tools:

- **Tier 1 (Claude Agent SDK):** SDK functions registered as Claude tools.
- **Tier 1 (Google ADK):** SDK functions registered as ADK tools.
- **Tier 2 (LangGraph):** SDK functions registered as LangGraph tool nodes.

External MCP servers from attached Contexts are a separate concern — agents
call *out* to those using the provider SDK's or LangChain's MCP client
support. The af SDK handles only the *inbound* direction (agent → hub).

### 8.2 Functions exposed

| Function | Description | Direction |
| --- | --- | --- |
| `af.spec_read` | Fetch spec artifacts, rendered views, traceability, coverage. | Agent → hub |
| `af.context_search` | Search retrieved sources in attached Contexts. Params: `query`, optional `context_id`, `source_id`, `max_results`. Returns ranked chunks. | Agent → hub |
| `af.context_get` | Fetch a pinned source from an attached Context in full. Params: `context_id`, `source_id`. | Agent → hub |
| `af.memory_recall` | Search agent memory for relevant learnings. | Agent → hub |
| `af.subtask_state` | Transition the agent's own subtask state. | Agent → hub |
| `af.ci_status` | Query CI pipeline runs, job results, and logs. | Agent → hub |
| `af.issues` | Read, search, create, comment on, update issues through the tracker-agnostic interface. | Agent → hub |
| `af.web_search` | Search and fetch public web content through the provider-agnostic interface. | Agent → hub |

### 8.3 Architecture

```
┌─── Agent Sandbox ───────────────────────────────────────┐
│                                                         │
│  ┌──────────────────────────────────────┐               │
│  │   Harness adapter                    │               │
│  │ (SDK-based or generic)               │               │
│  │                                      │               │
│  │  ┌──────────────┐                   │               │
│  │  │   af SDK     │───── gRPC ────────┼───────────┐   │
│  │  │  (imported)  │                   │           │   │
│  │  └──────────────┘                   │           │   │
│  └──────────┬───────────────────────────┘           │   │
│             │                                       │   │
│        /workspace                                   │   │
│        (branch checkout)                            │   │
└─────────────────────────────────────────────────────┼───┘
                                                      │
                                         ┌────────────▼────────────┐
                                         │   af Hub                │
                                         │                         │
                                         │  Spec store             │
                                         │  Context store          │
                                         │  Operational store      │
                                         │  Prompt assembly        │
                                         │  Run management         │
                                         └─────────────────────────┘
```

The SDK communicates with the hub on the host via gRPC. The hub is the
source of truth for spec content, Context data, memory, and subtask state.
The SDK is stateless — it sends requests and returns responses. Connection
failures are retried per-call with exponential backoff; no persistent
connection management is needed.

### 8.4 Authentication and scoping

Every SDK instance knows its agent's identity (workspace ID, agent ID,
run ID, specialist role) via environment variables injected at sandbox
creation. The hub uses this identity to scope all calls: an Implementor's
`af.subtask_state` call can only transition its own assigned subtask; an
`af.spec_read` call returns only the artifacts for the agent's workspace.

Authentication uses the same short-lived JWT token (`AF_AGENT_TOKEN`)
described in [services-architecture.md §10.1](services-architecture.md#101-agent-identity).
The SDK passes the token in gRPC metadata on every call.

### 8.5 Activity logging

The SDK logs every tool call and response as activity events, sent to the
hub as part of the call or via a batched `LogEvent` RPC. This is how
agent-level tool calls (spec reads, Context searches, subtask transitions)
enter the activity log. The hub uses call frequency for stall detection —
an agent that stops making SDK calls for a configurable duration (default
5 minutes) is flagged as stalled.

---

## 9. Agent provisioning flow

The full sequence from workspace creation to a running agent:

1. **Coordination layer** calls `BranchManager.create()` to provision the
   workspace branch.

2. **Coordination layer** assembles the agent configuration: resolves the
   specialist to a template, composes the system prompt (see
   [coordination-layer.md §6.3](coordination-layer.md#63-prompt-assembly)),
   gathers external MCP server configs (from attached Contexts), and
   collects environment variables including af SDK connection parameters
   (`AF_HUB_HOST`, `AF_HUB_PORT`, `AF_AGENT_TOKEN`, etc.).

3. **Coordination layer** calls `AgentLifecycle.create()` with the assembled
   configuration.

4. **Runtime** resolves the template: copies home directory content, runs the
   harness adapter's `provision()` method, calls `injectSystemPrompt()` and
   `injectInstructions()`, calls `applyMCPServers()` to translate external
   MCP server configs into the harness's native format.

5. **Runtime** builds the `ContainerSpec`: image, env vars (including af SDK connection
   parameters), and the harness command.

6. **Runtime** calls `ContainerRuntime.create()` and
   `ContainerRuntime.start()`.

7. **Runtime** starts the harness process inside the sandbox with the task
   as input.

8. **Agent** begins working. The adapter imports the af SDK, which connects
   to the hub using the injected environment variables and exposes hub tools
   as native adapter tools.

---

## 10. Sandbox image

OpenShell's default sandbox image includes a shell, standard Unix tools,
Python 3.14, Node 22, git, and common developer tools (`gh`, `vim`, `nano`).
The af runtime extends this with:

- A terminal multiplexer (tmux).
- The af SDK Python package.
- Additional language runtimes and build tools as needed.

OpenShell supports three image sources:

- **Community catalog:** `--from <name>` pulls a pre-built image from the
  OpenShell community registry. The af runtime can publish its base image
  here for zero-setup onboarding.
- **Custom Dockerfile:** `--from ./path` builds from a local Dockerfile.
  This is the standard path for af images that need project-specific tools
  or dependencies.
- **Container registry:** `--from registry.io/img:tag` pulls an existing
  OCI image. For organizations with private registries.

The provider SDK (Claude Agent SDK, Google ADK) or the generic adapter's
Python dependencies (LangGraph, LangChain) are either pre-installed in the
image or installed during provisioning.

The image does not include the af coordination service, the spec store,
or any coordination logic. These run on the host and the bridge reaches them
over the network.

---

## 11. Configuration

### 11.1 Global configuration

```yaml
# ~/.af/settings.yaml
data_dir: ~/.local/share/af   # default; override with AF_DATA_DIR env var

runtime:
  backend: openshell           # openshell (default) | podman | kubernetes
  image: af/agent:latest       # default base image (or --from source for OpenShell)
  openshell:
    policy: ~/.af/sandbox-policy.yaml   # default sandbox policy
    inference:                          # inference privacy routing
      local_model: null                 # e.g. "ollama:llama3" for local routing
      allow_remote: true                # allow frontier model API calls

defaults:
  harness: claude-sdk          # claude-sdk | google-adk | generic
  template: implementor

# Generic adapter defaults (used when harness is 'generic')
generic:
  model: ollama:qwen3-32b     # LangChain provider:model string
  prompt_tier: local           # frontier | mid-tier | local — selects prompt variant

branches:
  prefix: af                # branch prefix: af/<workspace-name>

spec_tool:
  model: claude-sonnet-4-6  # model for PRD assessment and artifact generation
```

### 11.2 Per-workspace overrides

```yaml
# Set via the coordination layer's workspace config
# (see coordination-layer.md §3.6)
harness: generic
model: openrouter:deepseek/deepseek-r1
template: implementor
env:
  CUSTOM_VAR: value
```

Global settings are the defaults. Per-workspace overrides take precedence.
Inline overrides at agent creation time take highest precedence.
