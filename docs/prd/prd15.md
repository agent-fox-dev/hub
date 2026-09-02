# Sandbox Capabilities

## Intent

Agents working through the hub today operate with whatever privileges the host
OS grants them. A compromised or misbehaving agent can read other workspaces,
exfiltrate secrets, consume unbounded CPU and memory, or make arbitrary network
calls. The hub isolates agent work at the git level -- each workspace has its
own branch and clone -- but nothing constrains the runtime environment where
agent code actually executes.

This proposal introduces sandbox capabilities to the hub: containerized,
resource-limited environments in which agents perform their work. Rather than
coupling the hub to a single container runtime or orchestration platform, the
design centers on a provider interface -- an abstraction layer that defines
sandbox operations (create, exec, stop, delete, and so on) and lets concrete
implementations handle the details. The default implementation uses Podman in
rootless mode, running containers on the same host as the hub with no external
infrastructure dependency. This makes sandboxes available immediately in local,
single-host, and development deployments. Future providers (Kubernetes Agent
Sandbox CRD, NVIDIA OpenShell) can be added behind the same interface without
modifying hub core code. The Podman-first approach was chosen because it
requires zero cluster infrastructure, supports rootless operation for
defense-in-depth, works on both Linux and macOS (via podman machine), and
provides the full OCI container lifecycle through stable Go bindings.

## Goals

- Provide containerized runtime isolation for agent work: filesystem, process,
  and resource boundaries enforced per sandbox.
- Define a provider interface that decouples sandbox operations from any single
  container runtime, allowing the hub to support Podman, Kubernetes, or
  policy-enforcement runtimes through configuration rather than code changes.
- Ship a production-ready Podman provider as the default, requiring only a
  Podman installation on the hub host.
- Bind sandbox lifecycle to workspace lifecycle: sandbox provisioning is
  triggered by workspace readiness, and sandbox teardown is triggered by
  workspace archival or deletion.
- Integrate sandboxes with existing hub systems: job queue for asynchronous
  provisioning, secrets for environment injection, auth for scoped token
  generation, audit for event emission, and the git server for repository
  access from within sandboxes.
- Provide operator-configurable resource limits (CPU, memory, process count)
  with sensible defaults.
- Surface sandbox status in workspace API responses and health endpoints.
- Ensure each sandbox is disposable: agents are responsible for syncing work
  back to the hub before a sandbox is stopped, and sandboxes can always be
  restarted fresh.

## Non-goals

- **Kubernetes-native CRD integration.** A Kubernetes provider using the Agent
  Sandbox CRD (sigs.k8s.io/agent-sandbox) is planned as a future provider
  behind the same interface. It is not part of this scope.
- **NVIDIA OpenShell integration.** OpenShell's L7 network filtering, Landlock
  filesystem policies, and credential proxy isolation are valuable security
  enhancements. However, OpenShell is alpha-quality with no stable release. It
  is deferred to a future provider implementation once the project reaches API
  stability.
- **Network restriction and egress control.** The simple Podman provider does
  not enforce network egress policies, domain allowlists, or L7 filtering.
  Network policy enforcement is deferred to a future provider such as
  OpenShell, which is purpose-built for this.
- **Live secret rotation.** Secrets and environment variables are attached to
  the container at creation time. Rotating secrets requires stopping and
  recreating the sandbox. Fine-grained credential management (proxy-based
  injection, hot-reload) is deferred to the future OpenShell provider.
- **Multi-tenant billing or usage metering.** Sandbox resource consumption
  feeds into the existing audit and cost attribution system but does not
  introduce billing or metering capabilities.
- **Custom container runtime development.** The hub delegates isolation to
  existing runtimes (Podman/runc, and in the future gVisor or Kata via
  Kubernetes RuntimeClass). No custom runtime is built.
- **Agent orchestration or scheduling.** This proposal provides sandbox
  infrastructure, not campaign runners, DAGs, or agent dispatch.
- **Warm pool management for the Podman provider.** Image pre-pulling is
  sufficient for single-host deployments. Warm pooling is a capability that
  Kubernetes providers may implement through the optional warm pool interface.
- **Multi-agent sandboxes.** Each sandbox runs exactly one agent session. A
  workspace has at most one active sandbox at a time.
- **Podman machine lifecycle management.** On macOS, the operator is
  responsible for initializing and starting the podman machine. The hub
  detects whether the machine is running and reports a clear error if not, but
  does not manage the machine lifecycle.

---

## Functional Requirements

### Provider Interface

The sandbox provider interface defines the behavioral contract that all
implementations must satisfy. The hub interacts with sandboxes exclusively
through this interface.

**Sandbox creation.** Given a workspace identifier, a container image
reference, resource limits (CPU, memory, process count), a set of environment
variables (including injected secrets), and a set of filesystem mounts
(workspace directory, read-only tool mounts), the provider creates a sandbox
and returns a unique sandbox identifier. If the specified image is not
available locally, the provider pulls it before creating the sandbox. If the
image pull fails (network error, image not found, registry auth failure),
creation fails with a descriptive error. If the host lacks sufficient
resources to satisfy the requested limits, creation fails with a
resource-exhaustion error.

**Command execution.** Given a sandbox identifier and a command (program path
plus arguments), the provider executes the command inside the sandbox and
returns the combined result: exit code, stdout content, and stderr content as
separate streams. Execution must support concurrent commands in the same
sandbox without interference. If the sandbox is not in a running state,
execution fails with a state error. There is no default timeout for command
execution -- commands may be long-running processes. Callers may optionally
specify a timeout; if provided and the command exceeds it, the provider
terminates the command and returns a timeout error. Standard input may
optionally be provided as a byte stream.

**File read.** Given a sandbox identifier and an absolute path within the
sandbox filesystem, the provider returns the file contents as a byte stream.
If the file does not exist or the path is outside the sandbox's accessible
filesystem, the operation returns a not-found error.

**File write.** Given a sandbox identifier, an absolute path, and content as a
byte stream, the provider writes the content to the specified path inside the
sandbox. If the path's parent directory does not exist, the provider creates
it. If the sandbox filesystem is read-only at the target path, the operation
returns a permission error.

**Sandbox listing.** Given an optional workspace filter, the provider returns
all sandboxes it manages, including their identifiers, associated workspace,
current state, creation time, and resource usage (if available). The listing
must be consistent -- a sandbox that was created and not yet deleted always
appears.

**Sandbox stop.** Given a sandbox identifier and a timeout duration, the
provider sends a graceful shutdown signal to the sandbox's primary process. If
the process does not exit within the timeout, the provider forcibly terminates
it. After stop, the sandbox's filesystem state is not preserved -- the
container can be restarted fresh. The agent is responsible for syncing any
work back to the hub (via git push) before the sandbox is stopped. If the
sandbox is already stopped, the operation is idempotent (succeeds without
error).

**Sandbox deletion.** Given a sandbox identifier, the provider stops the
sandbox (if running), removes its container and associated storage, and
releases all resources. After deletion, the sandbox identifier is permanently
invalid. If the sandbox does not exist, deletion is idempotent.

**Health check.** The provider reports whether it is operational: whether the
underlying runtime is reachable (e.g., Podman socket is responsive), whether
required images are available, and any degraded conditions. This feeds into
the hub's /healthz and /readyz endpoints.

**Provider capability reporting.** The provider reports which optional
capabilities it supports: warm pooling (pre-provisioned sandbox pools for fast
allocation), pause/resume (hibernating a sandbox and restoring it later), and
network policy enforcement (egress filtering, domain allowlists). The hub
adapts its behavior based on reported capabilities -- for example, network
policy configuration is only exposed in the API when the active provider
reports network policy support.

**Error contract.** All provider operations return typed errors that the hub
can classify without string matching: not-found (sandbox does not exist),
conflict (sandbox already exists for this workspace), resource-exhaustion
(host/cluster lacks capacity), timeout (operation exceeded deadline),
runtime-unavailable (Podman socket unreachable, daemon not running), and
state-error (operation invalid for current sandbox state).

### Sandbox Lifecycle

**States.** A sandbox progresses through: pending (creation requested, not yet
provisioned), provisioning (container being created, image being pulled),
running (container started, ready to accept exec commands), stopping (graceful
shutdown in progress), stopped (process terminated, container disposable),
failed (provisioning or runtime error), and terminated (container and
resources removed).

**Transitions.** pending to provisioning occurs when the job queue worker
picks up the sandbox provision job. provisioning to running occurs when the
container starts successfully. provisioning to failed occurs on image pull
failure, resource exhaustion, or any creation error. running to stopping
occurs on explicit stop request or workspace archival. stopping to stopped
occurs when the container process exits (gracefully or forcibly). stopped to
terminated occurs on explicit delete or workspace deletion. running to failed
occurs on unexpected container exit (crash, OOM kill). failed to terminated
occurs on explicit delete or cleanup. Any non-terminated state to terminated
occurs during orphan cleanup.

**Disposable containers.** Sandboxes are treated as disposable. When a sandbox
is stopped, its filesystem state is not guaranteed to persist. The agent must
sync all meaningful work (code changes, artifacts) back to the hub via git
push before the sandbox stops. This simplifies lifecycle management and avoids
storage overhead from preserving container layers.

**Workspace binding.** When a workspace's clone_status transitions to "ready,"
the hub enqueues a sandbox provision job (if sandboxes are enabled for that
workspace). When a workspace's status transitions to "archived," the hub stops
and deletes the associated sandbox. When a workspace is deleted, the hub
deletes the associated sandbox. When a workspace is reactivated (archived to
pending), the previous sandbox (if any) is deleted, and a new one is
provisioned after the reclone completes.

**Automatic re-creation on failure.** When a sandbox fails (image pull error,
OOM crash, unexpected exit), the hub automatically re-creates it with
exponential backoff (base: 30 seconds, multiplier: 2, cap: 10 minutes, max
retries: 5). After exhausting retries, the sandbox remains in failed state and
requires explicit user action (delete and re-provision) to recover. Each
re-creation attempt is logged as an audit event with the failure reason.

**Orphan detection.** On hub startup, the hub queries the provider for all
managed sandboxes and compares against workspace records. Sandboxes whose
associated workspace no longer exists or is archived are terminated. Sandboxes
in provisioning or stopping state for longer than a configurable timeout
(default: 10 minutes) are forcibly terminated.

**Timeout enforcement.** Sandbox provisioning has a configurable timeout
(default: 5 minutes). If provisioning does not complete within the timeout,
the sandbox transitions to failed with a timeout error, and the partially
created container is cleaned up. A periodic cleanup job (configurable
interval, default: 15 minutes) scans for sandboxes stuck in transient states
and terminates them.

### Podman Provider

The default sandbox provider uses Podman's Go bindings to communicate with
the Podman service over a Unix socket.

**Socket discovery.** On Linux, the provider connects to the rootless socket
at /run/user/{UID}/podman/podman.sock, falling back to the rootful socket at
/run/podman/podman.sock. On macOS, the provider discovers the forwarded
socket path by inspecting the podman machine configuration. The socket path
can be overridden via the CONTAINER_HOST environment variable.

**Podman service lifecycle.** The provider verifies the Podman service is
reachable at startup. If the service is not running on Linux with systemd, the
provider relies on socket activation (the service starts automatically on
first connection). On macOS, the provider checks that a podman machine is
running and reports a clear error if it is not, including instructions for
`podman machine init` and `podman machine start`. The provider does not start
or manage the podman machine itself -- this is a manual prerequisite for the
operator.

**Container creation.** Every container is created with: capabilities dropped
to none (CAP_DROP=ALL) with selective add-back only when required; the
NoNewPrivileges flag set to true to prevent privilege escalation; a read-only
root filesystem with a writable /tmp (ReadOnlyFilesystem=true,
ReadWriteTmpfs=true); a non-root user inside the container; and masked
sensitive paths (/proc/kcore, /proc/keys, /proc/timer_list, /sys/firmware). If
a custom seccomp profile is configured, it is applied; otherwise the Podman
default profile is used.

**Workspace mount.** The workspace directory on the host is bind-mounted into
the container at a well-known path (/workspace) with read-write access. On
systems with SELinux, the mount uses the :Z label option for proper context
relabeling. A separate read-only mount is used for any tool or configuration
directories the operator specifies.

**Network access.** The Podman provider does not restrict network access.
Containers are created with Podman's default network configuration, giving
agents unrestricted network access for package installation, API calls, and
other operations. Network egress control (domain allowlists, L7 filtering) is
deferred to future providers such as OpenShell, which are purpose-built for
policy enforcement.

**Resource limits.** Every container has explicit resource limits applied
through OCI Linux resource controls: memory limit (default: 4 GB), CPU quota
(default: equivalent of 2 cores, configured as quota/period), and process ID
limit (default: 512). These defaults are overridable per workspace and per
organization through configuration. On macOS via podman machine, the provider
validates that the VM has sufficient resources allocated and warns (via health
check) if the VM's resources are smaller than the configured sandbox limits.

**Command execution.** The provider executes commands using the two-step
Podman exec API: create an exec session (with Tty=false for proper
stdout/stderr separation), then start and attach to it. Input, output, and
error streams use in-memory buffers, never terminal file descriptors, to avoid
the known concurrency issue with global terminal state manipulation (Podman
GitHub issue 25090). Commands run without a default timeout and may be
long-running processes.

**Image management.** The provider pre-pulls configured template images at hub
startup and checks for image existence before each container creation. If the
image does not exist locally at creation time, the provider pulls it inline.
The provider supports building custom images from a Containerfile when the
operator specifies a build context in configuration. A periodic image cleanup
job removes dangling and unused images.

**Graceful shutdown.** When the hub shuts down, it iterates all managed
containers, sends SIGTERM with a configurable timeout (default: 10 seconds),
then forcibly removes any container that did not stop within the timeout. On
hub restart after a crash, the provider queries Podman for containers with the
hub's label prefix and reconciles them against the database: containers with
no matching workspace record are removed; containers whose workspace expects a
different state are corrected.

**Error handling.** When the Podman socket is unreachable, all operations
return a runtime-unavailable error. The health check endpoint reports Podman
as degraded. When an image pull fails due to network issues, the provider
retries once after a 5-second delay before reporting failure. When a container
exits unexpectedly (OOM kill, crash), the provider detects the exit via
container inspection, transitions the sandbox to failed, and records the exit
code and signal in the sandbox status. Resource exhaustion errors (disk full,
cgroup limit hit) are surfaced as typed errors with actionable messages.

### Hub Integration

**Workspace model extension.** The workspace record gains three new fields:
sandbox_id (nullable string, the provider-assigned container identifier),
sandbox_status (nullable string, one of the lifecycle states), and
sandbox_provider (nullable string, the configured provider name, e.g.,
"podman"). These fields are added via idempotent ALTER TABLE migration in the
workspace schema. API responses for workspaces include these fields when
non-null. The sandbox_provider field is set at sandbox creation time and does
not change for the lifetime of that sandbox.

**Job queue integration.** Sandbox provisioning is registered as a job type
("sandbox_provision") with the existing durable job queue. The job payload
contains the workspace slug, image reference, resource limits, environment
variables, and mount configuration. The job handler calls the configured
provider's create operation, starts the container, and updates the
workspace's sandbox_id, sandbox_status, and sandbox_provider fields. The retry
policy uses exponential backoff (base: 5 seconds, multiplier: 2, cap: 5
minutes, max retries: 3) because sandbox creation failures are typically
deterministic (bad image, insufficient resources) rather than transient. Jobs
use the workspace slug as the group key so that at most one sandbox provision
job runs per workspace at a time.

**Git server reachability.** Sandboxes must be able to reach the hub's git
server to push work back before stopping. Since the Podman provider uses
default networking, the hub's listen address is reachable from within
containers. The hub's address is included in container environment variables
so agents know where to push.

**Secrets injection.** When a sandbox is provisioned, the hub resolves all
workspace-scoped secrets and variables (using the existing secrets.Store and
vars resolution logic) and passes them to the provider as environment
variables. Secrets are attached to the container at creation time and are
never written to the sandbox filesystem. If a secret is rotated while a
sandbox is running, the sandbox must be stopped and recreated to pick up the
new value.

**Auth token generation.** At sandbox creation, the hub generates a
short-lived, workspace-scoped PAT with read and write permissions for the
associated workspace. This token is injected as an environment variable
(AF_HUB_TOKEN) so agents inside the sandbox can authenticate to the hub's API
and git endpoints. The token's lifetime matches the sandbox's expected
lifetime (configurable, default: 24 hours). When the sandbox is deleted, the
token is revoked.

**Audit events.** All sandbox mutations emit audit events through the existing
audit.Emitter interface: hub.sandbox.create (with workspace slug, provider,
image), hub.sandbox.start, hub.sandbox.stop (with reason),
hub.sandbox.delete, hub.sandbox.failed (with error details). Actor
information is derived from the API caller's auth context. Audit events for
automated operations (orphan cleanup, timeout enforcement, automatic
re-creation) use "system" as the actor.

**API endpoints.** The hub exposes sandbox operations through
workspace-scoped REST endpoints:
- POST /api/v1/workspaces/{slug}/sandbox -- request sandbox provisioning
  (enqueues a job, returns 202 with job ID).
- GET /api/v1/workspaces/{slug}/sandbox -- retrieve current sandbox status,
  including state, provider, resource usage, and creation time.
- DELETE /api/v1/workspaces/{slug}/sandbox -- stop and delete the sandbox
  (returns 202, teardown is asynchronous).
- POST /api/v1/workspaces/{slug}/sandbox/exec -- execute a command in the
  sandbox (synchronous, returns exit code and output).

**Permission scopes.** Sandbox operations require the following scopes:
sandbox:read (view sandbox status), sandbox:write (create, exec),
sandbox:delete (stop, delete). These are registered via a Permissions()
function and included in the hub's permission set.

**Health integration.** The sandbox provider's health status is included in
the hub's /healthz and /readyz responses. A degraded provider (e.g., Podman
socket unreachable) does not cause the hub to report unhealthy, but the
sandbox subsystem reports as unavailable. A failed provider blocks sandbox
creation but does not affect other hub operations.

### Security and Isolation

**Filesystem isolation.** Each sandbox has its own filesystem namespace. The
only host paths visible inside the sandbox are the workspace directory
(read-write at /workspace) and operator-configured tool mounts (read-only).
The root filesystem is read-only to prevent modification of system binaries.
/tmp is writable for agent scratch space. Agents cannot access other
workspaces' directories, the hub's data directory, or any host path not
explicitly mounted.

**Process isolation.** Sandboxes run with all Linux capabilities dropped. The
NoNewPrivileges flag prevents SUID binaries from escalating privileges. The
process ID limit caps the number of processes an agent can spawn, preventing
fork bombs. Seccomp profiles block dangerous system calls (ptrace, mount,
reboot, and others inappropriate for sandboxed workloads).

**Resource isolation.** Memory limits are enforced via cgroups. When a sandbox
exceeds its memory limit, the kernel's OOM killer terminates the container
process, and the hub's automatic re-creation with backoff takes over. CPU
limits are enforced via cgroup CPU quota, preventing a sandbox from
monopolizing host CPU. Disk usage is bounded by the workspace directory size
and the writable /tmp; the read-only root filesystem prevents unbounded disk
growth.

**Escape mitigation.** If an agent compromises the container and escapes to
the host, the damage is limited by rootless Podman's user namespace mapping
(the container's root is an unprivileged host user), the absence of
capabilities, and seccomp filtering. The hub does not claim that containers
provide VM-level isolation. Operators requiring stronger isolation boundaries
should use a future Kubernetes provider with gVisor or Kata RuntimeClass, or
the future OpenShell provider with Landlock enforcement.

**Network.** The Podman provider does not enforce network restrictions.
Network-level security (egress filtering, domain allowlists, L7 inspection,
credential proxy injection) is deferred to the future OpenShell provider,
which provides out-of-process policy enforcement purpose-built for this use
case.

### Configuration

**Provider selection.** The active provider is configured at the hub level
(environment variable or config file) with a default of "podman." Future
providers will be selectable by name (e.g., "kubernetes," "openshell").
Per-organization and per-workspace provider overrides are supported through
provider-specific configuration stored as JSON in the workspace model.

**Resource defaults.** Operators configure default resource limits (memory,
CPU, process count) at the hub level. These defaults apply to all sandboxes
unless overridden per organization or per workspace.

**Image templates.** Operators configure one or more container image
references as templates for sandbox creation. A default image is required.
Additional images can be mapped to workspace types or agent archetypes. The
provider pre-pulls all configured images at startup.

**Cleanup intervals.** The orphan scan interval (default: 15 minutes),
provisioning timeout (default: 5 minutes), stop timeout (default: 10
seconds), and re-creation backoff parameters are configurable at the hub
level.

**Sandbox enablement.** Sandboxes can be enabled or disabled at the hub level
(global toggle), at the organization level, or at the workspace level. When
disabled, workspace lifecycle transitions do not trigger sandbox operations,
and sandbox API endpoints return 404.

---

## Technical Boundaries

- **Language:** Go (matching the existing hub codebase, currently Go 1.26.5).
- **Container runtime:** Podman v5 Go bindings
  (github.com/containers/podman/v5/pkg/bindings) communicating via Unix
  socket. The internal libpod library is not used (it is explicitly
  unsupported for external consumers and can break without notice).
- **Rootless mode:** The Podman provider operates in rootless mode by default.
  Rootful mode is supported but not the default. Rootless requires cgroups v2
  with systemd delegation for resource limits on Linux.
- **State storage:** Sandbox state (sandbox_id, sandbox_status,
  sandbox_provider) is stored in the existing workspace SQLite database. No
  additional database is introduced.
- **Job queue:** Sandbox provisioning uses the existing durable
  jobqueue.Queue (SQLite-backed, with retry, crash recovery, and graceful
  shutdown).
- **Audit:** Sandbox events use the existing audit.Emitter interface.
- **macOS support:** Requires podman machine (Apple Virtualization Framework)
  to be manually initialized and running.

## Dependencies

- **Podman v5:** Required on the hub host for the default provider. On macOS,
  requires podman machine to be initialized and running (manual prerequisite).
- **internal/workspace:** Sandbox fields are added to the Workspace model.
  Lifecycle hooks tie sandbox provisioning to clone_status transitions.
- **internal/jobqueue:** The sandbox_provision job type is registered with the
  existing durable queue.
- **internal/secrets and internal/vars:** Workspace-scoped secrets and
  variables are resolved and injected into sandbox environments at creation
  time.
- **internal/audit:** Sandbox mutation events are emitted through the existing
  Emitter interface.
- **apikit:** Auth middleware, error envelopes, permissions, and time
  utilities are used throughout sandbox handlers.

## Design Decisions

### 1. Provider interface instead of direct Podman integration

A direct Podman integration would be simpler initially but would couple the
hub to a single runtime. The research identified three credible runtime
options (Podman, Kubernetes Agent Sandbox CRD, NVIDIA OpenShell), each suited
to different deployment scales. PRD13 Option B already recommended wrapping
SDK calls behind an interface to insulate the hub from CRD API instability.
The provider interface makes this insulation the architectural default rather
than a mitigation. The interface also enables operators to choose their
isolation level through configuration (rootless Podman on a laptop, gVisor on
GKE, Kata on OpenShift) without forking or modifying hub code.

### 2. Podman as the default provider (not Docker, not Kubernetes)

Podman was chosen over Docker because it is rootless by default (no daemon
running as root), daemonless (each container is an independent process,
reducing single-point-of-failure risk), has no commercial licensing
restrictions (Docker Desktop requires a paid license for organizations above
250 employees), and drops more capabilities by default (11 vs Docker's 14).
Podman was chosen over Kubernetes as the default because sandboxes should work
in local and single-host deployments without requiring a cluster. Kubernetes
remains the right choice for multi-node, production-scale deployments and will
be the second provider implementation.

### 3. OpenShell deferred to a future provider

NVIDIA OpenShell provides genuine security capabilities that raw container
runtimes cannot match: L7 network filtering by HTTP method/path/MCP tool,
credential injection via proxy (keys never touch the sandbox filesystem),
hot-reloadable network policies, and per-binary egress restrictions. However,
OpenShell is alpha-quality with no tagged stable release, an unstable API, and
kernel-level features (Landlock, seccomp, eBPF) that require Linux and do not
work on macOS development machines. The provider interface makes future
OpenShell integration architecturally clean -- it would wrap another provider
(Podman or Kubernetes) and add policy enforcement as a composable layer. The
integration should be pursued once OpenShell reaches a tagged stable release
with API stability guarantees. Network restriction and credential management
are deferred entirely to the OpenShell integration.

### 4. Disposable containers with agent-driven sync

Sandbox containers are treated as disposable rather than preserving filesystem
state across stop/start cycles. The agent is responsible for syncing all
meaningful work back to the hub via git push before a sandbox is stopped. This
decision simplifies lifecycle management (no persistent volumes, no storage
overhead from container layers), avoids state divergence between the hub's git
repo and the sandbox's working directory, and aligns with the hub's git-centric
model where the repository is the source of truth.

### 5. No network restriction in the simple provider

The Podman provider deliberately does not enforce network egress policies.
Network policy enforcement adds significant complexity (firewall rule
management, DNS filtering, domain resolution) and is the core value
proposition of OpenShell. Attempting a partial implementation in the Podman
provider would be redundant work that gets replaced when OpenShell is
integrated. Agents in the simple provider have unrestricted network access for
dependency installation, API calls, and other operations.

### 6. Relationship to PRD13 Option B

This proposal preserves all behavioral requirements from PRD13 Part 1
(sandbox lifecycle operations, workspace binding, git server reachability,
secrets injection, auth token generation, integration points). The key change
is that Option B's direct Agent Sandbox SDK integration becomes one provider
implementation behind the interface, rather than the only path. The hub
integration points (workspace model fields, job queue registration, API
endpoints, audit events) are identical -- only the field name changes from
sandbox_runtime to sandbox_provider to reflect that the abstraction is not
limited to Kubernetes RuntimeClass.
