# Services Architecture

**Version:** 1.0
**Status:** Draft

This document specifies the deployable components of an af installation:
the hub, CLI, runtime engine, af SDK, and memory service. It covers
how they communicate, how state is persisted, and how the system starts,
stops, and recovers.

The coordination layer (domain model, spec package, agents, orchestration)
is specified in [coordination-layer.md](coordination-layer.md). The runtime
layer (sandboxes, branches, adapters, agent lifecycle) is specified in
[runtime-layer.md](runtime-layer.md).

---

## 1. Design principles

1. **Local-first.** The default deployment is everything on one machine: one
   hub process, one SQLite database, agent sandboxes on local OpenShell. No
   network services, no cloud dependencies, no accounts.

2. **Single stateful process.** The hub owns all mutable state. The CLI,
   the af SDK, and the runtime engine are stateless or ephemeral. This
   makes reasoning about consistency simple: one writer, many readers.

3. **Process boundaries follow trust boundaries.** The hub runs on the
   host with full access. The af SDK runs inside the agent sandbox with
   scoped identity. The harness (provider SDK or generic adapter) runs
   inside the sandbox with no direct access to hub state.

4. **Pluggable where the coordination layer says pluggable.** Memory, issue
   tracker, web search, and the container runtime are behind interfaces.
   Everything else is built-in.

---

## 2. The af hub

### 2.1 Responsibilities

The hub is the coordination service. It owns:

- **Spec store.** Reads spec artifacts from the filesystem (written by
  speclib via `spec` or the Planner). Serves reads to the af SDK and
  the CLI. Enforces the access gate: only `active` or later specs are
  served.
- **Context store.** Persists Context metadata, source descriptors,
  instructions, and revisions. Serves Context reads to the af SDK.
  Accepts Context edits from the CLI (Operator actions).
- **Operational store.** Persists workspace state, campaigns, runs, agents,
  subtask execution, verification outcomes, messages, and the
  activity log.
- **Spec lifecycle.** Enforces the state machine (`draft → active → sealed /
  superseded`), intent hashing, and the freeze contract.
- **Run management.** Creates and tracks spec-driven runs.
  Manages the Coordinator pattern: delegates subtasks, monitors state,
  triggers verification, applies bounce-backs.
- **Prompt assembly.** Composes the system prompt for each agent from the
  specialist role, Context sources, spec slice, recalled memory, and actor
  constraints (see [coordination-layer.md §6.3](coordination-layer.md#63-prompt-assembly)).
- **Agent memory.** Drives the memory service through `recall` (at prompt
  assembly and behind the MCP tool) and `consolidate` (at session end).
  See [coordination-layer.md §6.6](coordination-layer.md#66-the-agent-memory-contract).
- **Activity log.** Receives events from the af SDK, the runtime engine,
  and internal operations. Appends to the operational store.
- **Coordination API.** Serves the Operator-facing API (see
  [coordination-layer.md §10.1](coordination-layer.md#101-operator-facing-api))
  over a local socket or TCP.
- **Agent API.** Serves the agent-facing API over gRPC, accepting
  connections from af SDK instances inside agent sandboxes.

### 2.2 Process model

The hub is a single long-running process. It starts when the Operator
invokes `af hub start` (or automatically on the first CLI command that
needs it) and runs until explicitly stopped or until the machine shuts down.

It listens on two interfaces:

- **CLI socket.** A Unix domain socket (default `<data_dir>/hub.sock`) for
  CLI-to-hub communication. HTTP/JSON over the socket. Local only; no
  network exposure.
- **Agent API port.** A TCP port (default `localhost:7400`) for af SDK
  connections from agent sandboxes. gRPC with per-agent identity tokens
  for authentication. Bound to localhost by default; bindable to a network
  interface for remote container runtimes.

### 2.3 Startup and shutdown

**Startup:**
1. Resolve the data directory: `AF_DATA_DIR` env var, then `data_dir` in
   `~/.af/settings.yaml`, then the default `~/.local/share/af/`. Create it
   if it does not exist.
2. Open or create the SQLite database (`<data_dir>/af.db`).
3. Run schema migrations if needed.
4. Verify the spec store directory exists (`<data_dir>/specs/`).
5. Start listening on the CLI socket and agent API port.
6. Recover in-flight runs: re-evaluate workspace and agent state against the
   runtime engine. Agents that were running when the hub last stopped are
   detected via the container runtime (containers may still be alive) and
   their state is reconciled.

**Shutdown:**
1. Stop accepting new CLI and agent API connections.
2. For each active run: commit partial work, transition in-flight subtasks
   to a recoverable state, log a `hub_shutdown` activity event.
3. Close the database.
4. Exit.

Agents in containers are not stopped on hub shutdown by default — they
continue running and reconnect to the bridge when the hub restarts. The
Operator can choose to stop all agents before stopping the hub.

### 2.4 Crash recovery

The hub is the single writer to the operational store. SQLite's WAL mode
ensures the database is consistent after a crash. On restart, the hub:

1. Opens the database (SQLite recovers the WAL automatically).
2. Scans for runs marked `running` in the operational store.
3. Queries the container runtime for the actual state of each agent.
4. Reconciles: agents still running reconnect through the bridge; agents
   that exited while the hub was down are transitioned to `stopped` or
   `error` based on exit code.
No data is lost. The activity log may have a gap (events that occurred
between hub crash and restart are lost if the bridge couldn't reach the
hub), but the operational store state is consistent.

---

## 3. The af CLI

### 3.1 Responsibilities

The CLI is the Operator's interface. It is stateless: every command talks
to the hub, which owns all state. The CLI never reads or writes the
database directly.

### 3.2 Command structure

Commands mirror the Operator-facing API (see
[coordination-layer.md §10.1](coordination-layer.md#101-operator-facing-api)):

```
af workspace create --campaign <slug> --spec <slug> [--origin <path|url>] [--context <id>...]
af workspace list [--status <status>] [--campaign <slug>]
af workspace get <id>
af workspace archive <id>
af workspace reopen <id>
af workspace delete <id>

af spec show <campaign>/<spec> [--artifact <name>] [--rendered]
af spec validate <campaign>/<spec>
af spec coverage <campaign>/<spec>
af spec approve <campaign>/<spec>
af spec seal <campaign>/<spec>
af spec supersede <campaign>/<spec> --new-spec <campaign>/<new-spec>

af context create --name <name> --instruction <text>
af context list
af context get <id>
af context source add <context-id> --type <type> --locator <loc>
af context source remove <context-id> <source-id>
af context attach <workspace-id> <context-id> [--pin-mode <pinned|live>]
af context detach <workspace-id> <context-id>

af run start <workspace-id>
af run start-planner <workspace-id>
af run list <workspace-id>
af run get <run-id>
af run stop <run-id>

af agent list <workspace-id>
af agent get <agent-id>
af agent stop <agent-id>
af agent logs <agent-id> [--follow]
af agent message <agent-id> <text>

af campaign create --goal <file>
af campaign list
af campaign register <campaign-slug> <spec-slug> [--depends-on <spec:group>...]
af campaign status <campaign-slug>
af campaign abandon <campaign-slug>

af activity <workspace-id> [--run <id>] [--agent <id>] [--type <type>...] [--follow]

af hub start [--foreground]
af hub stop
af hub status
```

Spec authoring commands (`init`, `draft`, `generate`, `approve`) are in the
standalone `spec` CLI (§7), not here. The `af` CLI operates on approved
specs for lifecycle transitions, reads, and validation.

### 3.3 Spec authoring

Two paths to create a spec:

- **Standalone path.** The Operator uses the `spec` CLI (§7) or the
  agent skill to author and approve a spec. No hub required. This is the
  primary authoring workflow.
- **Harness-mediated path.** The Operator starts a Planner run via
  `af run start-planner`. The Planner uses speclib to draft the spec
  within the harness, grounded in attached Contexts. The Operator reviews
  and approves through the harness. See
  [coordination-layer.md §8.1](coordination-layer.md#81-the-generic-spec-driven-flow).

---

## 4. The runtime engine

The runtime engine (specified in [runtime-layer.md](runtime-layer.md)) runs
as a library embedded in the hub, not as a separate process. The hub
calls it to create sandboxes, start agents, and manage branches.

This means the hub process is the only thing the Operator needs to start.
[NVIDIA OpenShell](https://github.com/NVIDIA/OpenShell) must be installed
on the host (see [runtime-layer.md §2.1](runtime-layer.md#21-openshell-adapter-default));
the Operator interacts with it only through the af CLI, never directly.
OpenShell in turn requires a container backend (Docker or Podman) on the
host.

### 4.1 Runtime lifecycle within the hub

- On hub startup: the runtime engine initializes, connects to the
  container backend, and inventories existing containers.
- On workspace create: the runtime creates the workspace branch.
- On run start: the runtime provisions and starts agent sandboxes with
  templates. The sandbox clones the repo and checks out the workspace
  branch. The af SDK (imported by the adapter) connects to the hub on
  first use.
- On agent stop/suspend: the runtime stops or suspends the container.
- On workspace delete: the runtime removes containers and optionally the
  workspace branch.

---

## 5. The af SDK

Specified in [runtime-layer.md §8](runtime-layer.md#8-the-af-sdk).
The af SDK is a Python library imported by each harness adapter inside the
agent sandbox. One instance per running agent.

### 5.1 Hub connection

The SDK connects to the hub's agent API port (`localhost:7400` by default)
on first use. The connection parameters are injected via environment
variables at sandbox creation:

```
AF_HUB_HOST=host.containers.internal
AF_HUB_PORT=7400
AF_AGENT_TOKEN=<jwt>
AF_WORKSPACE_ID=<uuid>
AF_AGENT_ID=<uuid>
AF_RUN_ID=<uuid>
```

The agent token is a short-lived JWT issued by the hub at sandbox
creation, encoding the agent's identity (workspace, agent, run, specialist
role). The hub validates it on every request and scopes the response
accordingly.

### 5.2 Connection management

If the hub restarts while an agent is running, the SDK retries individual
calls with exponential backoff. While the hub is unreachable, the harness
can still work (editing files, running commands) but hub tool calls (spec
read, Context search, etc.) return errors. The SDK logs failures locally
and reports them to the hub when connectivity is restored.

### 5.3 Stall detection

The hub detects stalled agents by monitoring SDK call frequency. An agent
that stops making SDK calls for a configurable duration (default 5 minutes)
is flagged as stalled. No separate heartbeat protocol is needed — the
regular flow of tool calls serves as the liveness signal.

---

## 6. The memory service

### 6.1 Embedded mode (default)

The memory service runs in-process within the hub. Learnings are stored
in the SQLite database alongside the operational store (in a separate set of
tables). Recall uses a simple keyword and embedding-based similarity search
over stored learnings.

This is sufficient for single-user, single-machine use. No external
dependencies.

### 6.2 External mode

The hub connects to an external memory service over gRPC, using the
`AgentMemory` interface (see
[coordination-layer.md §6.6](coordination-layer.md#66-the-agent-memory-contract)).
The external service owns storage, retrieval, and distillation. The hub
passes through `recall` and `consolidate` calls.

This mode is for deployments that want shared memory across machines, more
sophisticated retrieval (vector search, re-ranking), or integration with an
existing knowledge base.

### 6.3 Configuration

```yaml
# ~/.af/settings.yaml
memory:
  backend: embedded            # embedded | external
  # For external mode:
  # endpoint: localhost:7401
  # auth: token
```

---

## 7. The spec creation tool

The spec creation tool is a standalone component for authoring spec packages.
It is implemented in Python, built on the
[Claude Agent SDK](https://docs.anthropic.com/en/docs/agents-and-tools/agent-sdk),
and works independently of the harness — no hub required for authoring. It
consists of three parts: a shared library (speclib), a CLI (`spec`), and
an agent skill.

Spec creation is a **stateful, session-based process**. A session starts
with an existing PRD (rough draft or fully fleshed out), uses an agent to
assess and optionally refine it, then generates the remaining artifacts.
The session works against a **local campaign directory**, not the hub's
spec store. Once a spec is complete, the user pushes it to the hub via
`spec submit`.

### 7.1 speclib

The core Python library. Implements the
[Spec Format Specification](spec-format_v1.3.md) and drives spec
authoring through the Claude Agent SDK. speclib is used by:

- the `spec` CLI (standalone authoring),
- the Planner specialist inside the harness (harness-mediated authoring),
- the harness itself (validation, rendering, and prompt assembly at
  execution time — these do not require the Agent SDK).

#### 7.1.1 The session model

Spec creation is stateful. A **session** tracks the lifecycle of authoring
one spec, from PRD input to completed package.

```python
class Campaign:
    """Manages a campaign working directory containing 1..n specs."""

    @classmethod
    def create(cls, path: Path, name: str, description: str) -> "Campaign":
        """Create a new campaign directory with campaign.yaml."""
        ...

    @classmethod
    def open(cls, path: Path) -> "Campaign":
        """Open an existing campaign directory."""
        ...

    def new_spec(
        self,
        spec_name: str,            # e.g. "data_models" → creates 01_data_models/
        prd: str | Path,           # PRD content or file path
        mode: Literal["interactive", "one-shot"] = "interactive",
    ) -> "SpecSession":
        """Create a new spec within this campaign and return its session."""
        ...

    def specs(self) -> list[Path]:
        """List spec directories in this campaign."""
        ...

    @property
    def path(self) -> Path: ...
    @property
    def metadata(self) -> CampaignMetadata: ...


class SpecSession:
    """A stateful authoring session for one spec within a campaign."""

    @classmethod
    def resume(cls, spec_dir: Path) -> "SpecSession": ...

    # --- PRD assessment and refinement ---

    def assess(self) -> Assessment:
        """Agent analyzes the PRD. Returns an assessment with quality
        score, identified gaps, and structured questions (if any)."""
        ...

    def refine(self, answers: dict[str, str]) -> Assessment:
        """Submit answers to the agent's questions. The agent updates
        the PRD and re-assesses. Returns a new assessment. Repeats
        until no questions remain or the user accepts."""
        ...

    def accept_prd(self) -> None:
        """Mark the PRD as accepted, ending the refinement loop."""
        ...

    # --- Artifact generation ---

    def generate(self) -> GenerateResult:
        """Agent generates requirements.json, test_spec.json, and
        tasks.json from the accepted PRD. Validates the full package
        on completion. Requires an accepted PRD."""
        ...

    # --- Validation and rendering ---

    def validate(self) -> ValidationResult:
        """Run schema validation and cross-file integrity checks."""
        ...

    def render(self, combined: bool = False) -> str:
        """Render the spec as markdown."""
        ...

    # --- State ---

    @property
    def state(self) -> SessionState: ...
    # SessionState: "init" | "assessing" | "refining" | "prd_accepted"
    #             | "generating" | "generated"

    @property
    def spec_dir(self) -> Path: ...

    @property
    def assessment(self) -> Assessment | None: ...


@dataclass
class CampaignMetadata:
    name: str
    description: str
    created_at: str
    updated_at: str
```

```python
@dataclass
class Assessment:
    quality: Literal["ready", "needs_refinement", "incomplete"]
    summary: str                        # agent's assessment of the PRD
    gaps: list[str]                     # identified gaps or weaknesses
    questions: list[Question]           # structured questions for the user

@dataclass
class Question:
    id: str
    text: str                           # the question
    context: str                        # why the agent is asking
    options: list[str] | None           # suggested answers, if applicable
    required: bool

@dataclass
class GenerateResult:
    artifacts: dict[str, Path]          # artifact name → file path
    validation: ValidationResult
    warnings: list[str]

@dataclass
class ValidationResult:
    valid: bool
    schema_errors: list[str]
    integrity_errors: list[str]
    repair_suggestions: list[RepairSuggestion]

@dataclass
class RepairSuggestion:
    artifact: str                       # which file
    description: str                    # what's wrong
    patch: dict                         # RFC 6902 JSON Patch to apply
    auto_fixable: bool                  # True if speclib can fix it silently
```

#### 7.1.2 Session persistence

A session's state is persisted in the spec's directory as
`_session.json`. This allows resuming an interrupted session via
`SpecSession.resume(spec_dir)`. The session file tracks: the current
state, the PRD path, assessment history, Q&A history, and which artifacts
have been generated.

The working directory is a **campaign directory** containing 1..n spec
subdirectories and a `campaign.yaml`:

```
<work_dir>/                         # campaign working directory
  campaign.yaml                     # campaign metadata (§4.2 of coordination-layer.md)
  01_data_models/                   # spec (complete or in progress)
    _session.json                   # session state (speclib-managed)
    prd.md                          # the PRD (copied or created at init)
    requirements.json               # generated (once generate() completes)
    test_spec.json                  # generated
    tasks.json                      # generated
    architecture.md                 # optional, user-provided
  02_auth/                          # another spec in the same campaign
    _session.json
    prd.md
    ...
```

`spec init` creates the campaign directory and `campaign.yaml`.
`spec new` creates a spec subdirectory within it. Multiple specs can
be authored in parallel within the same campaign directory. Each spec
has its own session.

#### 7.1.3 The agent

speclib uses the Claude Agent SDK to drive a Claude model for two
operations: PRD assessment/refinement and artifact generation.

**PRD assessment.** The agent reads the PRD, evaluates it against the
structure expected by the format spec (Intent section, goals, non-goals,
background), and produces an `Assessment`. In interactive mode, if the
assessment identifies gaps, the agent formulates structured `Question`
objects — specific, answerable questions with context and optional
suggested answers. This mirrors the request/response pattern of the
harness's `AskUserQuestion` tool: the agent asks, the user answers, the
agent incorporates and re-assesses.

**Artifact generation.** Once the PRD is accepted, the agent generates
the three JSON artifacts in sequence: `requirements.json` first (EARS
criteria, correctness properties, execution paths, error handling), then
`test_spec.json` (derived from requirements), then `tasks.json` (task
groups, subtasks, traceability). Each artifact is validated against its
schema as it is generated. Cross-file integrity runs after all three
exist.

**Configuration.** The agent uses `ANTHROPIC_API_KEY` for authentication.
The model is configurable via `AF_SPEC_MODEL` (default: the latest Claude
Sonnet). These are read from environment variables or from
`~/.af/settings.yaml` under a `spec_tool` key:

```yaml
# ~/.af/settings.yaml
spec_tool:
  model: claude-sonnet-4-6       # default model for spec generation
```

#### 7.1.4 Non-agent operations

Validation, rendering, lifecycle transitions, and the repair pass do not
use the Agent SDK. These are deterministic operations implemented directly
in Python:

- **Validate** — JSON Schema validation per artifact, then cross-file
  integrity checks. See [spec-format_v1.3.md §10](spec-format_v1.3.md#10-validation).
- **Render** — deterministic markdown output from JSON artifacts.
  See [spec-format_v1.3.md §11](spec-format_v1.3.md#11-rendering).
- **Repair** — auto-fix near-miss structural errors (empty required fields
  with inferable values, EARS field name mismatches, IDs one transform from
  valid). Returns `RepairSuggestion` objects: auto-fixable repairs are
  applied silently and logged; non-obvious repairs are surfaced for user
  review.
- **Lifecycle** — enforce the state machine defined in
  [spec-format_v1.3.md §9](spec-format_v1.3.md#9-lifecycle). The
  `draft → active` transition (Intent hashing, freeze) is performed by
  the hub, not by speclib standalone (see §7.4).

### 7.2 spec CLI

A separate Python binary wrapping speclib. Does not require the hub to be
running — except for `submit` (push a spec to the hub) and `import` (pull
from the hub). The `draft → active` transition (`approve`) is a hub
operation invoked through the `af` CLI, not `spec` (see §7.4).

**Campaign commands:**

```
spec init <path> --name <name> --description <text>
    Create a new campaign working directory at <path> with campaign.yaml.
    If <path> exists and is empty, initialize it in place.

spec import --hub <url> <campaign-slug> [--target <path>]
    Pull an existing campaign (campaign.yaml + all specs) from the hub's
    spec store into a local working directory. Useful for context when
    authoring a new spec that depends on existing ones.

spec list [<campaign-dir>]
    List specs in a campaign directory with their session state.
```

**Spec authoring commands** (run from within a campaign directory):

```
spec new <prd-file> [--name <spec-name>] [--one-shot]
    Create a new spec in the campaign from a PRD. The spec directory is
    named {NN}_{spec-name} (next available number). In one-shot mode,
    assess → accept → generate → validate in one pass. In interactive
    mode (default), assess and pause for refinement.

spec assess <spec>
    Run or re-run the PRD assessment for a spec. Prints the assessment
    summary, gaps, and structured questions (if any). <spec> is the
    spec directory name (e.g. "01_data_models") or number (e.g. "01").

spec refine <spec> --answers <file>
    Submit answers to the agent's questions as a JSON file mapping
    question IDs to answer strings. The agent updates the PRD and
    re-assesses. Repeat until no questions remain.

spec accept <spec>
    Accept the PRD in its current state, ending the refinement loop.

spec generate <spec>
    Generate the JSON artifacts from the accepted PRD.

spec validate <spec>
    Run schema and cross-file integrity checks on the spec package.

spec render <spec> [--combined]
    Render the spec as markdown.

spec show <spec> [--artifact <name>]
    Display an artifact or the session state.

spec status [<spec>]
    Print the session state. Without <spec>, show all specs in the
    campaign.
```

**Hub interaction commands:**

```
spec submit --hub <url> [<spec>]
    Push a completed spec to the hub, adding it to the campaign in the
    hub's spec store. The hub creates a SpecRef. Without <spec>, submits
    all completed specs. Requires a running hub.
```

**Working directory convention.** `spec` commands operate relative
to the current directory, which must be a campaign directory (one that
contains `campaign.yaml`). The `--campaign-dir` flag can override this.
The campaign directory is self-contained: it holds campaign metadata,
spec directories, and session state.

**One-shot mode.** `spec new --one-shot` runs the full pipeline in a
single invocation: assess → accept (regardless of gaps) → generate →
validate. The user reviews the output with `spec show` or
`spec render`, then submits. This is for cases where the PRD is
known to be complete or where speed matters more than refinement.

**Interactive mode.** The default. `spec new` creates the spec and
runs the initial assessment. If the agent has questions, the user answers
them with `spec refine <spec> --answers answers.json` and the agent
re-assesses. The loop continues until the user runs `spec accept`.
Then `spec generate` produces the artifacts.

### 7.3 Spec skill

An agent skill that exposes speclib capabilities to agents (via the
provider SDK or generic adapter). The skill drives speclib's session model
programmatically rather than through the `spec` CLI.

**Interactive mode.** The user asks the agent to create a spec (e.g.,
"create a spec for feature X from this PRD"). The agent:

1. Opens the campaign via `Campaign.open()` (or creates one).
2. Creates a `SpecSession` via `campaign.new_spec()`.
3. Runs `assess()` and presents the assessment to the user.
4. If questions exist, presents them conversationally (mirroring the
   structured Q&A pattern). The user answers in natural language; the
   skill maps answers to `Question` IDs and calls `refine()`.
5. Repeats until the user is satisfied, then calls `accept_prd()`.
6. Runs `generate()` and presents the rendered spec for review.

**One-shot mode.** The user provides a PRD and asks for immediate
generation. The skill calls `campaign.new_spec(mode="one-shot")`,
which runs assess → accept → generate in sequence, then presents the
result.

The skill does not require the hub to be running for authoring. It
invokes `spec submit` only when the user explicitly asks to push to
the hub.

### 7.4 Relationship to the harness

speclib has two roles: **authoring** (creating specs) and **runtime**
(validation, rendering, prompt assembly for the hub). Only the authoring
role uses the Claude Agent SDK; the runtime role is pure Python.

- **`spec` and the spec skill** handle authoring standalone. They
  work against a local campaign directory, produce spec packages, and
  push them to the hub via `spec submit`.
- **`spec submit`** pushes a completed spec to the hub's spec store.
  The hub creates a SpecRef and makes the spec available for execution.
  The spec arrives in `draft` status.
- **`af spec approve`** (the `af` CLI, not `spec`) transitions a spec
  from `draft` to `active` — computing the `intent_hash`, freezing the
  package, and updating the SpecRef. This is a hub operation because
  approval has consequences for the operational store (the spec becomes
  visible to agents and executable against).
- **`spec import`** pulls an existing campaign from the hub into a
  local working directory. This gives the agent context about existing
  specs when authoring a new one that depends on them.
- The **Planner specialist** uses speclib's session model inside the
  harness. This is the harness-mediated alternative: the hub starts a
  Planner agent, the Planner creates a `SpecSession`, and the
  interactive refinement loop runs through the hub's message channel
  rather than through the CLI.
- The **hub** uses speclib's non-agent operations (validate, render) at
  execution time. It reads from the spec store filesystem — the same
  directory that `spec submit` writes to.
- Only specs in `active` status or later are served by the harness API.
  Draft specs are invisible to the harness.

---

## 8. Storage layout

Configuration and data are stored in separate directory trees.

- **Config directory** (`~/.af/`) — global configuration only. Contains
  `settings.yaml` and nothing else that changes at runtime.
- **Data directory** (`<data_dir>`) — all runtime artifacts: the SQLite
  database, spec store, templates, and the hub socket. Resolved in order:
  `AF_DATA_DIR` env var → `data_dir` key in `~/.af/settings.yaml` →
  default `~/.local/share/af/`.

### 8.1 Filesystem layout

```
~/.af/                             # Config directory (global configuration)
  settings.yaml                    # Global configuration

<data_dir>/                        # Data directory (default: ~/.local/share/af/)
  hub.sock                         # CLI-to-hub Unix socket
  af.db                            # SQLite database (operational + Context stores)
  specs/                           # Spec store (campaign/spec hierarchy)
    <campaign-slug>/
      campaign.yaml                # Campaign metadata (name, description, timestamps)
      <spec-slug>/
        prd.md
        requirements.json
        test_spec.json
        tasks.json
        architecture.md            # optional
      archive/                     # superseded and archived specs
        <spec-slug>/
          ...
  templates/                       # Global templates
    planner/
    coordinator/
    implementor/
    verifier/
  # Workspace branches are checked out inside sandboxes, not on the host.
  # See runtime-layer.md §3.2
```

### 8.2 Database schema (SQLite)

The operational store tables map directly to the entities in
[coordination-layer.md §9.3](coordination-layer.md#93-operational-store):

- `teams` — id, name, slug, url, status, created_at, created_by.
  Renamed from the previous organizational table by the team_rename spec.
  Represents an organizational group that users belong to.
- `team_configs` — team_id, setup_scripts (JSON), default_provider,
  default_model.
- `workspaces` — id, slug, git_url, branch, owner_id, team_id, status,
  created_at. Maps a git repository plus optional branch to an execution
  context. Owned by a user via owner_id, optionally associated with a
  team via team_id. Status is `active` or `archived`.
- `campaigns` — id, name, status, goal_document, shared_context_ids (JSON),
  created_at, updated_at
- `campaign_members` — campaign_id, workspace_id, spec_id, dependency_edges
  (JSON)
- `spec_refs` — workspace_id, spec_id, spec_name, status, intent_hash,
  schema_version, supersedes, created_at, updated_at
- `context_attachments` — workspace_id, context_id, pinned_revision,
  pin_mode, attached_at
- `runs` — id, workspace_id, spec_id, kind, status, started_at, ended_at
- `agents` — id, workspace_id, run_id, specialist_role, actor_capability,
  provider, model, phase, activity, parent_agent_id, started_at, ended_at
- `subtask_executions` — workspace_id, spec_id, subtask_id, run_id,
  assigned_agent_id, state, drop_rationale, started_at, completed_at
- `verification_outcomes` — workspace_id, spec_id, run_id, group_id,
  verification_subtask_id, check_id, result, detail, recorded_at
- `managed_scripts` — workspace_id, agent_id, run_id, command, pid, status,
  started_at, stopped_at
- `messages` — id, agent_id, role, content, parent_message_id, created_at
- `activity_events` — id, workspace_id, run_id, agent_id, type, payload
  (JSON), created_at

Context store tables:

- `contexts` — id, name, owner_principal, access_policy (JSON), instruction,
  current_revision, created_at, updated_at
- `sources` — id, context_id, type, locator, resolution_strategy,
  freshness_contract, revision

Memory tables (embedded mode only):

- `memory_pins` — workspace_id, run_id, principal, namespace,
  pinned_revision, recorded_at
- `learnings` — id, principal, namespace, content, provenance, kind,
  revision, confidence, recorded_at
- `learning_embeddings` — learning_id, embedding (blob)

### 8.3 Backup and portability

Harness state is split between the config directory (`~/.af/`) and the data
directory (`<data_dir>`). Backup requires copying both:
`cp -r ~/.af/ <backup>/config && cp -r <data_dir> <backup>/data`. Moving
to a new machine is the same copy plus setting `data_dir` in the new
machine's `~/.af/settings.yaml`. Workspace branches live in the remote
repo and are cloned on demand when sandboxes start.

---

## 9. Communication protocols

### 9.1 CLI ↔ Hub

HTTP/JSON over Unix domain socket. The CLI sends requests; the hub
responds. For streaming operations (activity follow, agent logs), the hub
uses server-sent events (SSE) over the same connection.

The socket path is `<data_dir>/hub.sock` by default, overridable via
`AF_HUB_SOCK` or `--hub-sock`.

### 9.2 af SDK ↔ Hub

gRPC over TCP. The SDK is the client; the hub is the server. Each RPC
includes the agent token in metadata for authentication and scoping.

Services:

```protobuf
service AfAgent {
  // Spec access
  rpc ReadSpec(ReadSpecRequest) returns (ReadSpecResponse);
  rpc RenderSpec(RenderSpecRequest) returns (RenderSpecResponse);
  rpc GetTraceability(TraceabilityRequest) returns (TraceabilityResponse);

  // Context access
  rpc SearchContext(SearchContextRequest) returns (SearchContextResponse);
  rpc GetSource(GetSourceRequest) returns (GetSourceResponse);

  // Memory
  rpc RecallMemory(RecallMemoryRequest) returns (RecallMemoryResponse);

  // Subtask state
  rpc TransitionSubtask(TransitionRequest) returns (TransitionResponse);

  // Activity logging
  rpc LogEvent(LogEventRequest) returns (LogEventResponse);

  // CI/CD status
  rpc ListCIRuns(ListCIRunsRequest) returns (ListCIRunsResponse);
  rpc GetCIRun(GetCIRunRequest) returns (GetCIRunResponse);
  rpc GetCIJobLog(GetCIJobLogRequest) returns (GetCIJobLogResponse);

  // Issue tracker
  rpc SearchIssues(SearchIssuesRequest) returns (SearchIssuesResponse);
  rpc GetIssue(GetIssueRequest) returns (GetIssueResponse);
  rpc CreateIssue(CreateIssueRequest) returns (CreateIssueResponse);
  rpc CommentIssue(CommentIssueRequest) returns (CommentIssueResponse);
  rpc UpdateIssue(UpdateIssueRequest) returns (UpdateIssueResponse);

  // Web search
  rpc WebSearch(WebSearchRequest) returns (WebSearchResponse);
  rpc WebFetch(WebFetchRequest) returns (WebFetchResponse);

  // Heartbeat
  rpc Heartbeat(HeartbeatRequest) returns (HeartbeatResponse);
}
```

### 9.3 Hub ↔ Runtime engine

In-process function calls. The runtime engine is a Go library (or equivalent)
linked into the hub binary. No IPC.

### 9.4 Hub ↔ Memory service (external mode)

gRPC over TCP. The hub is the client; the memory service is the server.
Uses the `AgentMemory` interface from
[coordination-layer.md §6.6](coordination-layer.md#66-the-agent-memory-contract),
translated to protobuf.

---

## 10. Security and isolation

### 10.1 Agent identity

Each agent receives a short-lived JWT at container creation. The token
encodes:

- `workspace_id` — scopes all data access
- `agent_id` — identifies the agent
- `run_id` — scopes the run
- `role` — the specialist role (used for actor capability checks)
- `exp` — expiry (refreshed on SDK calls)

The hub validates the token on every bridge request. An Implementor
cannot read another workspace's spec. An Archetype cannot transition another
agent's subtask. The token is the enforcement mechanism for the actor
capability model (see
[coordination-layer.md §6.4](coordination-layer.md#64-specialists-actor-capabilities-and-instruction-precedence))
at the bridge boundary.

### 10.2 Sandbox isolation

Detailed in [runtime-layer.md §2.1](runtime-layer.md#21-openshell-adapter-default).
The key guarantee: an agent sandbox sees only its workspace checkout, its
agent home directory, and the af SDK's gRPC connection to the hub. It cannot see the af
database, other agents' homes, or the spec store on the host filesystem.

OpenShell enforces this through defense-in-depth, all out-of-process:

- **Filesystem:** Landlock LSM locks path allowlists at sandbox creation.
  Kernel-enforced — the agent cannot override or escape them.
- **Network:** Deny-by-default. Every outbound connection goes through an
  HTTP CONNECT proxy evaluated by OPA/Rego policies at the HTTP method and
  path level. The sandbox policy must explicitly allow egress to the hub's
  agent API port and to the model provider's API.
- **Process:** Seccomp BPF filters block privilege escalation, dangerous
  syscalls, and socket creation outside the proxy.
- **Inference:** The privacy router intercepts LLM API calls, strips caller
  credentials, and injects backend credentials. Routing is policy-driven,
  not agent-driven.

Static protections (filesystem, process) are immutable after sandbox
creation. Dynamic protections (network, inference) can be hot-reloaded
via `openshell policy set` without restarting the sandbox.

### 10.3 Hub access

The CLI socket is file-permission-protected (owner-only). The agent API
port is localhost-only by default. No authentication is needed for the CLI
socket (the Operator has host access); the agent API port uses agent tokens.

---

## 11. Deployment modes

### 11.1 Local (default)

Everything on one machine. OpenShell for agent sandboxes (backed by Docker
or Podman on the host). SQLite for storage. Embedded memory service. The
Operator runs `af hub start` and uses the CLI.

### 11.2 Future: remote hub

The hub runs on a remote machine (a beefy server, a cloud VM). The CLI
connects over TCP instead of a Unix socket. The agent API port is exposed on the
network with TLS. Agent containers run on the same remote machine. This
requires adding TLS and Operator authentication to the hub — the gRPC and
HTTP interfaces already support it structurally.

### 11.3 Future: distributed

Multiple machines each running a hub instance, coordinated by a central
registry (analogous to Scion's Hub). Out of scope for this document.

---

## 12. Context retrieval engine

### 12.1 Purpose

The coordination layer defines two resolution strategies for Context sources
(see [coordination-layer.md §5.9](coordination-layer.md#59-grounding-the-context)):
`pinned` (full content in the prompt every turn) and `retrieved` (indexed,
only relevant chunks pulled in per turn). Pinned sources work for small
documents but cannot scale to a full repository or a large document set.
The retrieval engine makes `retrieved` sources practical.

### 12.2 Interface

The hub calls the retrieval engine when an agent invokes the Context
search tool and the target source has resolution strategy `retrieved`.

```
interface RetrievalEngine {
  // Index a source. Called when a Context revision is cut or when a live
  // source's origin changes. The engine reads the content, chunks it,
  // computes embeddings, and stores the index.
  index(input: {
    contextId: string
    sourceId: string
    revision: string
    content: ContentStream       // the source content, streamed
    contentType: string          // e.g. "repository", "file", "blob"
  }): Promise<void>

  // Search a source. Returns the chunks most relevant to the query,
  // ranked by similarity.
  search(input: {
    contextId: string
    sourceId: string
    revision: string             // must match the pinned revision
    query: string
    maxResults?: number          // default 10
    maxTokens?: number           // budget for total returned content
  }): Promise<RetrievalResult[]>

  // Remove an index. Called when a source is removed from a Context
  // or when a revision is superseded and no workspace pins it.
  remove(input: {
    contextId: string
    sourceId: string
    revision: string
  }): Promise<void>
}

type RetrievalResult = {
  chunk: string                  // the retrieved text
  path?: string                  // file path within a repository source
  score: number                  // similarity score, 0-1
  startLine?: number             // position in the source, if applicable
  endLine?: number
}
```

### 12.3 Deployment

**Embedded (default).** The engine runs in-process within the hub.
Embeddings are computed using a local model (e.g. a small ONNX embedding
model shipped with the hub). The index is stored in SQLite (using a
vector extension) or in a local file-based index (e.g. HNSW). No external
dependencies.

**External.** The hub connects to a standalone retrieval service over
gRPC, using the same interface. This mode supports GPU-accelerated
embeddings, larger indices, and shared retrieval across multiple hub
instances.

### 12.4 Indexing lifecycle

- When a Context revision is cut: the hub calls `index()` for each
  `retrieved` source in the revision.
- When a workspace attaches a Context: the hub verifies the pinned
  revision is indexed. If not (e.g. the engine was added after the
  revision was cut), it triggers indexing.
- When a `live` source re-resolves: the hub calls `index()` with the
  new content, then `remove()` for the old revision once no workspace
  pins it.
- When a source is removed from a Context: `remove()` on all revisions.

### 12.5 Storage

Embedded mode adds to the hub's storage:

- `retrieval_indices` — context_id, source_id, revision, status
  (indexing/ready/failed), chunk_count, indexed_at
- `retrieval_chunks` — index_id, chunk_text, path, start_line, end_line,
  embedding (blob)

The embedding dimension is fixed per installation (determined by the
embedding model). A vector similarity index (exact or approximate) is
maintained over the embeddings for search.

---

## 13. CI/CD bridge

### 13.1 Purpose

The harness reads CI status and drives PRs but does not run pipelines itself.
The CI/CD bridge is the read-only adapter that lets agents query pipeline
status so the Verifier can confirm CI passed as part of the wiring
verification gate (see
[coordination-layer.md §7.4](coordination-layer.md#74-verification-gate))
and the PR Shepherd can wait for green CI before marking a PR merge-ready.

### 13.2 Interface

Defined in the coordination layer (see
[coordination-layer.md §6.5](coordination-layer.md#65-tools-available-to-agents)).
The `CIProvider` interface is pluggable, the same pattern as `IssueTracker`
and `WebSearch`.

### 13.3 Adapters

**GitHub Actions adapter.** Uses the GitHub REST API
(`GET /repos/{owner}/{repo}/actions/runs`, `GET .../jobs`,
`GET .../jobs/{id}/logs`). Authenticates with the workspace's configured
GitHub token (the same token used by the Git tool for PR operations).

**GitLab CI adapter.** Uses the GitLab REST API
(`GET /projects/{id}/pipelines`, `GET .../jobs`, `GET .../jobs/{id}/trace`).
Authenticates with a GitLab access token.

Adding a new CI provider means implementing the `CIProvider` interface.

### 13.4 Configuration

```yaml
# Per-workspace or global
ci:
  provider: github              # github | gitlab
  # Provider-specific settings are inherited from the Git tool's
  # configuration (remote URL, auth token). No separate config needed
  # for the common case where CI runs on the same platform as the repo.
```

### 13.5 Agent access

The CI/CD bridge is exposed to agents through the af SDK as the
`af.ci_status` tool. Queries and results are logged as activity events.
The tool is read-only — agents cannot trigger pipelines or modify CI
configuration.

---

## 14. Notification service

### 14.1 Purpose

The Operator is not always watching the terminal. The notification service
alerts the Operator when events of interest occur, so they can step in at
the right moments (approve a drafted spec, review a completed run, handle
a failure).

### 14.2 Interface

The notification service subscribes to the hub's activity event stream
and matches events against a set of triggers. When a trigger fires, it
delivers a notification through one or more configured channels.

```
interface NotificationService {
  addChannel(channel: NotificationChannel): void
  setTriggers(triggers: NotificationTrigger[]): void
}

interface NotificationChannel {
  name: string
  deliver(notification: Notification): Promise<void>
}

type NotificationTrigger = {
  event: string                 // activity event type or pattern
  filter?: {
    workspaceId?: string
    runId?: string
    campaignId?: string
  }
  priority: "info" | "action_required"
}

type Notification = {
  title: string
  body: string
  priority: "info" | "action_required"
  source: {
    workspaceId: string
    runId?: string
    agentId?: string
  }
  timestamp: string
  actionUrl?: string            // deep link to the CLI command or dashboard
}
```

### 14.3 Built-in channels

**Desktop notification.** Uses the OS notification system (macOS
Notification Center, Linux `notify-send`). Zero configuration for local
deployments.

**Webhook.** HTTP POST to a configured URL with the notification payload as
JSON. The building block for integrations — Slack incoming webhooks, Discord
webhooks, PagerDuty, or a custom endpoint.

**Log.** Writes notifications to a file or stdout. Useful for headless
servers or CI environments where no interactive notification is possible.

### 14.4 Default triggers

| Trigger | Priority | When |
| --- | --- | --- |
| `spec_ready_for_review` | action_required | A Planner has drafted a spec ready for Operator approval. |
| `run_complete` | info | A spec-driven run completed successfully. |
| `run_failed` | action_required | A run stopped due to error or unrecoverable failure. |
| `campaign_workspace_unblocked` | action_required | A Campaign dependency gate cleared. |
| `verification_failed` | info | A verification gate failed; the Coordinator will re-delegate. |
| `agent_stalled` | info | An agent has been stalled beyond the configured threshold. |

The Operator can add, remove, or modify triggers through configuration.

### 14.5 Deployment

The notification service runs inside the hub process as an event
subscriber — not a separate service.

### 14.6 Configuration

```yaml
# ~/.af/settings.yaml
notifications:
  channels:
    - type: desktop
    - type: webhook
      url: https://hooks.slack.com/services/T.../B.../xxx
  triggers:
    # Override or extend the defaults
    - event: run_complete
      priority: action_required   # escalate from info to action_required
    - event: agent_stalled
      filter:
        workspaceId: "abc-123"
      priority: action_required
```

---

## 15. Web dashboard

### 15.1 Purpose

A read-only web frontend for observing system state without the CLI. Shows
what's happening across workspaces, runs, agents, and Campaigns at a glance.
The Operator uses it alongside the CLI, not instead of it — all write
operations remain CLI-driven.

### 15.2 Architecture

The dashboard is a static single-page application (SPA) served by the hub
over its HTTP interface. It consumes the same Operator-facing API and
observability API that the CLI uses. No additional backend logic.

```
Browser ──► Hub HTTP ──► Same API endpoints as CLI
                     └──► SSE streams for live updates
```

### 15.3 Views

| View | Content |
| --- | --- |
| **Home** | Active workspaces with status, current run, agent count. Campaigns as collapsible groups with dependency graphs. |
| **Workspace detail** | State, attached Contexts, spec lifecycle, git status, file listing. |
| **Spec viewer** | Rendered combined view. Coverage and traceability tables. Diff against previous spec. |
| **Run timeline** | Gantt-style view: phases, agent lifetimes, subtask transitions, verification outcomes. |
| **Activity stream** | Filterable, scrollable event log with live SSE updates. |
| **Agent detail** | State, conversation history, tool call log, assigned subtask. |
| **Campaign graph** | Dependency graph across specs. Node color by status. |

### 15.4 Live updates

The dashboard subscribes to the hub's SSE activity stream for real-time
updates. Workspace status changes, subtask transitions, verification
outcomes, and agent state changes appear without polling.

### 15.5 Deployment

The hub serves the dashboard's static assets from a built-in directory.
No separate web server. Available at
`http://localhost:<hub-http-port>/` when the hub is running.

### 15.6 Scope boundary

The dashboard is read-only. It does not provide forms for creating
workspaces, authoring specs, or starting runs. If write support is added
later, it routes through the same API.

---

## 16. Deferred

One component remains architecturally anticipated but not specified:

- **Remote hub.** Running the hub on a remote machine with TLS,
  Operator authentication, and multi-tenant isolation. The gRPC and HTTP
  interfaces already support remote access structurally; what's missing is
  the auth layer and tenant separation.
