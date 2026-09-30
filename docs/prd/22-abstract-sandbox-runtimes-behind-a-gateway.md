# Abstract Sandbox Runtimes Behind a Gateway

## Intent

A sandbox (`docs/prd/18-run-agents-in-ephemeral-sandboxes.md`) can be an
OCI container, a microVM or a Kubernetes pod, and it can run on the hub
host, on another machine, or in a cluster. The hub should not know which.
It needs one small interface to launch something from an image with
resources, a network policy and a few environment variables, to ask whether
it is still running, to stop it, and to enumerate what it started.

This PRD defines that interface, the **gateway**, a registry of gateways
by name, and the first implementation: the **local gateway**, which drives
the `podman` CLI on the hub host and offers two **providers**, `container`
(a plain OCI container) and `microvm` (the same image booted under libkrun).
It ships an admin endpoint that lists gateways and their capabilities. It
does not create sandbox records or decide when to launch; that is the
lifecycle PRD. It is complete when hub code can call `Launch` on the local
gateway with an image and get a running container with the hub's labels,
and `Inspect`, `Stop` and `List` behave as specified against real podman.

## Goals

- A Go `Gateway` interface with typed errors and a capability report.
- A registry that resolves a gateway by name and reports health.
- A local gateway on the `podman` CLI with the `container` and `microvm`
  providers, hardened like the hub's git subprocesses.
- Container security defaults: non-root, no capabilities, no new
  privileges, read-only root, pid limit.
- `GET /api/v1/gateways` for operators.

## Non-goals

- Remote gateways and their protocol
  (`docs/prd/28-attach-remote-gateways.md`). The interface is designed so a
  remote implementation is a client of it.
- The `pod` provider and a Kubernetes implementation (future PRD).
- Enforcing `restricted` network egress. The local gateway reports it as
  unsupported; enforcement is planned through OpenShell.
- Exec, file copy, logs or port forwarding. The gateway launches, inspects,
  stops and lists; everything else goes through the outpost.
- Sandbox records, state machines, tokens or configuration documents.

## Current behaviour (for reference)

- `internal/gitcmd` runs git as a subprocess with a scrubbed environment,
  its own process group, and a bounded deadline (`docs/configuration.md`,
  "hardened environment"). The same runner pattern applies to podman.
- `containers/sandbox` and `containers/agents` images exist and are built
  with `make build-*-container` via podman.
- `/readyz` has one checker (database); `health.NewDBChecker` shows the
  checker shape.

## Functional Requirements

### Interface

- **GW-1.** Package `internal/sandbox/gateway`:

  ```go
  type LaunchSpec struct {
      ID        string            // sandbox id; becomes the container name suffix and a label
      Provider  string            // "container" | "microvm" | "pod"
      Image     string
      Workdir   string            // "" = image default
      Resources Resources         // CPU, Memory (quantities), PIDs, Disk (advisory)
      Network   NetworkPolicy     // Egress "all" | "restricted", Allow []string
      Env       map[string]string // passed as-is; callers keep it minimal
      Labels    map[string]string // always includes af.sandbox=<id>, af.workspace=<slug>
  }
  type Handle string             // gateway-specific reference (container id, pod name)
  type Status struct {
      State    string             // "running" | "exited" | "gone"
      ExitCode *int
      StartedAt, FinishedAt *time.Time
  }
  type Capabilities struct {
      Providers      []string
      NetworkEgress  []string     // policies it enforces: always contains "all"
      MaxResources   *Resources   // nil = unknown
  }
  type Gateway interface {
      Name() string
      Capabilities(ctx context.Context) (Capabilities, error)
      Launch(ctx context.Context, spec LaunchSpec) (Handle, error)
      Inspect(ctx context.Context, h Handle) (Status, error)
      Stop(ctx context.Context, h Handle, grace time.Duration) error
      List(ctx context.Context) ([]Listed, error)   // Handle + Labels + Status
      Health(ctx context.Context) error
  }
  ```

  Errors are sentinel values wrapped with context: `ErrUnsupportedProvider`,
  `ErrUnsupportedPolicy`, `ErrImagePull`, `ErrCapacity`, `ErrNotFound`,
  `ErrUnavailable`, so callers classify with `errors.Is`.
- **GW-2.** `Launch` SHALL be atomic from the caller's view: on any error
  after the runtime object was created, the gateway removes it before
  returning. `Stop` and a `Stop` after `gone` are idempotent. `Inspect` on
  an unknown handle returns `Status{State: "gone"}`, not an error.
- **GW-3.** Callers SHALL check `Capabilities` before `Launch`; a gateway
  SHALL nevertheless reject a spec it cannot honour (`ErrUnsupportedProvider`,
  `ErrUnsupportedPolicy`) rather than downgrade it.
- **GW-4.** Gateways SHALL be stateless with respect to the hub: everything
  needed to find a launched object again is in its labels and the handle.

### Registry

- **GW-5.** `Registry` holds gateways by name. `Get(name)` returns
  `ErrNotFound` for unknown names. `Health` runs every gateway's `Health`
  with a 5s deadline and reports per gateway; `/readyz` includes a
  `gateways` entry that is degraded (not failed) when any gateway is
  unhealthy, and `ok` when the registry is empty.
- **GW-6.** `GET /api/v1/gateways` (admin token only) returns
  `[{ "name", "kind": "local" | "remote", "healthy": bool, "error": string|null,
  "capabilities": {…} }]`.

### Local gateway

- **LG-1.** The local gateway SHALL be registered as `local` when
  `[sandbox.local] enabled` is not `false` and a podman binary is found at
  `[sandbox.local] podman` (default: `podman` on `PATH`). `Health` runs
  `podman info --format json` and fails on a non-zero exit. Startup logs
  the podman version and the runtimes it lists.
- **LG-2.** podman SHALL be run through a runner with the same hardening as
  `internal/gitcmd`: scrubbed environment (only `PATH`, `HOME`,
  `XDG_RUNTIME_DIR`, `CONTAINER_HOST`, `TMPDIR`), own process group killed
  on cancellation, and a deadline per call (`Launch` 10m to cover a pull,
  everything else 60s).
- **LG-3.** `container` provider `Launch` runs, in effect:

  ```
  podman run --detach --name sbx-<id> \
    --label af.sandbox=<id> --label af.workspace=<slug> --label af.gateway=local \
    --cpus <cpu> --memory <memory> --pids-limit <pids> \
    --cap-drop ALL --security-opt no-new-privileges --read-only \
    --tmpfs /tmp --user 1001 \
    --volume <anon>:<workdir> --volume <anon>:/opt/app-root/src \
    --workdir <workdir> --env-file <tmp> <image>
  ```

  The environment is written to a 0600 temp file removed after `run`
  returns, never passed on the command line. The image's `ENTRYPOINT`
  and `CMD` are used unchanged. The handle is the container id.
- **LG-4.** `microvm` provider: LG-3 with `--runtime krun`. `Capabilities`
  lists `microvm` only when `podman info` shows a `krun` runtime.
- **LG-5.** `Capabilities.NetworkEgress` is `["all"]`. A spec with
  `restricted` is `ErrUnsupportedPolicy`.
- **LG-6.** Before `run`, the image SHALL be present: `podman image
  exists`, else `podman pull` (its time counts against the caller's
  context). Pull failures are `ErrImagePull`. `[sandbox.local] prepull`
  (list of references, default empty) is pulled at startup in the
  background.
- **LG-7.** `Inspect` maps `podman inspect` (`.State.Status`, `.State.ExitCode`,
  timestamps) to `running`, `exited` or, when the container is missing,
  `gone`. `Stop` runs `podman stop -t <grace seconds>` then
  `podman rm -f`; both tolerate "no such container". `List` runs
  `podman ps -a --filter label=af.gateway=local --format json` and returns
  handles with labels and status.
- **LG-8.** A `podman` exit code that indicates the host is out of
  resources (cgroup or disk errors in stderr) maps to `ErrCapacity`; a
  socket or daemon error maps to `ErrUnavailable`; an unknown runtime to
  `ErrUnsupportedProvider`.

### Configuration

- **LG-9.** `config.toml`:

  ```toml
  [sandbox.local]
  enabled = true
  podman  = "podman"
  prepull = ["quay.io/agentfox/agents:latest"]
  ```

### Testing

- Unit: a `FakeGateway` in the package (scriptable states, errors and
  capability reports) for every consumer's tests; the podman argument
  builder; stderr classification (LG-8); registry health aggregation.
- Integration, behind a build tag or an environment check for a working
  podman: launch `quay.io/agentfox/sandbox` with `sleep`, inspect running,
  stop, inspect gone, list shows labels; unsupported policy and missing
  image produce the right sentinels.

## Technical Boundaries

- Go; new package `internal/sandbox/gateway` with sub-package `podman`.
  No podman Go bindings; the CLI is driven as a subprocess.
- Documentation: `docs/configuration.md` (`[sandbox.local]`), `docs/api.md`
  (`GET /api/v1/gateways`), `docs/architecture.md` (the gateway/provider
  split; create the file if it does not exist).

## Dependencies

- podman 5 on the hub host for the local gateway; libkrun for `microvm`.
- `internal/gitcmd`'s process-group and deadline handling, extracted or
  duplicated into a small shared subprocess helper.

## Design Decisions

### 1. Gateway × provider

The brief lists "container/podman, microvm/podman, pod/kubernetes" and,
separately, a gateway interface for remote hosts with a built-in local one.
A provider is a launch recipe; a gateway is who executes it and where. The
podman recipe is shared by the local gateway and, later, by `af-gateway` on
a remote host; a Kubernetes gateway adds one recipe without touching the
interface.

### 2. The podman CLI, not the Go bindings

The hub already runs git as a hardened subprocess. The bindings module is
large, and the CLI is what operators debug with (`podman ps` shows exactly
what the hub started). A future `af-gateway` stays a static binary.

### 3. Launch, inspect, stop, list; nothing more

A runtime abstraction could also offer exec, logs and file access. With
the outpost as the in-sandbox agent of the hub, the gateway only needs
lifecycle verbs, which keeps every implementation small and a remote
protocol trivial.

### 4. Fail closed on policy

A gateway that cannot enforce a requested network policy refuses the
launch. Silently running with `all` when `restricted` was asked for would
turn a security setting into a suggestion.

## Open Questions

- `--userns` handling on rootless podman: `keep-id` maps the container
  user to the host user, which matters for anonymous volumes' ownership.
  The draft runs as uid 1001 with anonymous volumes and lets podman pick
  the mapping; confirm on Fedora and macOS podman machine during the spec.
