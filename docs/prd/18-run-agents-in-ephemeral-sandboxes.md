# Run Agents in Ephemeral Sandboxes

> **Status: draft for discussion.** This PRD is being workshopped. Sections
> marked *Recommendation* record the option the draft is written against;
> the Open Questions at the end list every decision that is still open.

## Intent

Agents that work through the hub today run wherever someone starts them: on
a developer laptop, in a codespace, in a container started by hand from
`containers/agents`. The hub owns the workspace, its branches, its secrets
and its audit trail, but it has no say over the environment the agent's tools
execute in. Nothing stops an agent from reading another workspace's clone,
using the operator's credentials, or exhausting the host.

This PRD adds the **sandbox** to the hub: an isolated, ephemeral execution
environment that the hub launches for a workspace at a specific revision,
that an agent (or a person with a shell) works inside, and that pushes its
work back to the hub before it disappears. The hub does not run the agent
itself. It provisions the environment, injects what the environment needs to
bootstrap (hub endpoint, a workspace-scoped token, variables and secrets),
and keeps a two-way control channel open to a small service inside the
sandbox, the **outpost**, for the lifetime of the sandbox.

Three things make this more than "run a container":

1. **Where the sandbox runs is pluggable.** A *provider* says how a sandbox
   is realised (an OCI container, a microVM, a Kubernetes pod). A *gateway*
   is the thing that realises it: the built-in local gateway drives podman on
   the hub host; remote gateways implement the same interface for other
   machines and clusters.
2. **Configuration is a document, not a pile of flags.** The hub ships a
   default `sandbox.json`; a workspace can replace it with its own.
3. **The hub can talk to the sandbox, not only start it.** The hub embeds a
   NATS server. The outpost connects to it and receives commands (`sync`,
   `shutdown`, ...) and reports state, so the hub can drive a sandbox session
   without SSH, exec, or polling the container runtime.

An earlier proposal, `docs/prd/prd15.md`, covered the same ground with a
different model (one long-lived sandbox per workspace, the hub's clone
bind-mounted into the container, control via `podman exec`). This PRD
supersedes it; the differences are listed under *Relationship to PRD 15*.

## Goals

- Let a caller launch a sandbox for a workspace at a chosen revision, on a
  chosen branch, and get back a record whose status the hub keeps current
  until the sandbox is gone.
- Define one JSON sandbox configuration document used at every level: a
  hub-wide default file, and an optional per-workspace document that
  replaces it.
- Inject a bootstrap environment into every sandbox: hub URL, a token scoped
  to that workspace and that sandbox, the workspace's resolved variables, and
  the secrets the configuration names.
- Ship the outpost as part of the existing `afc` binary so that the sandbox
  and agents images (`containers/sandbox`, `containers/agents`) need nothing
  new beyond the binary itself.
- Embed a NATS server in the hub and define the subjects, message envelope
  and command set the hub and outpost use.
- Define a gateway interface and ship the local gateway (podman) with two
  providers: `container` and `microvm`. Define how remote gateways attach.
- Bind sandbox lifetime to workspace lifetime (archive and delete stop every
  sandbox of the workspace), to explicit stop requests, and to time limits
  (boot, idle, maximum lifetime).
- Expose the whole thing through the REST API and `afc`, with permission
  scopes and audit events in the existing style.

## Non-goals

- **Running the agent.** The hub launches the environment and can tell it to
  sync or shut down. Which agent runs inside, with which prompt, and when, is
  the caller's business (a person, a campaign runner, a CI job).
- **Interactive exec, shell or port forwarding through the hub** in this
  iteration. `afc sandbox shell` and an `exec` command over NATS are natural
  follow-ups; the message envelope leaves room for them.
- **Persistent sandbox storage.** A sandbox's filesystem dies with it. Work
  survives only by being pushed to the hub.
- **Warm pools, snapshots, pause/resume.** Gateways may add them behind the
  capability report later.
- **Layer-7 egress filtering and credential proxies.** Network policy in this
  PRD is coarse (`all` or an allow-list of hosts) and only enforced by
  gateways that report the capability. The hub never silently downgrades.
- **A Kubernetes gateway implementation.** The `pod` provider and the remote
  gateway protocol are specified so that one can be written; shipping it is
  a separate PRD. The same holds for the `microvm` provider on hosts without
  libkrun.
- **Sandbox-to-sandbox communication** and multi-container sandboxes.
- **Billing.** Sandbox events feed the audit log; metering is out of scope.

## Current behaviour (for reference)

- The hub has no notion of a sandbox. `containers/sandbox/Containerfile`
  builds a toolchain image and `containers/agents/Containerfile` layers
  agent CLIs (pi, opencode, claude) on top; both are started by hand with
  `podman run` as `containers/agents/README.md` describes. A GitHub
  workflow builds them.
- Credentials are admin tokens, API keys and PATs. A PAT is scoped by
  permission but not by workspace: it can act on every workspace its owner
  owns. `docs/prd/prd14.md` described a workspace-scoped token; PRD 1 decided
  PATs replace it, and nothing workspace-bound exists today. The README's
  mention of "workspace-scoped tokens" is aspirational.
- Secrets and variables exist at user, org and workspace tiers, with
  `GET /workspaces/:slug/vars/resolved` merging the tiers. Secret values are
  base64 at rest.
- `afc` already ships a git credential helper that reads `~/.af/config.toml`,
  so a process holding a hub token can clone and push through the hub's git
  server without further setup.
- The durable job queue, the audit emitter, agent sessions and Prometheus
  metrics are all in place and are the integration points this PRD uses.
- The hub is a single process with an HTTP listener. There is no message
  broker.

## Relationship to PRD 15

| Topic | PRD 15 (`prd15.md`) | This PRD |
|---|---|---|
| Cardinality | One sandbox per workspace, provisioned automatically when the clone is ready | Any number per workspace (capped), launched on request, each pinned to a revision |
| Lifetime | Long-lived; stopped on archive | Ephemeral; stopped on request, on timeout, on archive/delete |
| Code inside the sandbox | The hub's workspace clone bind-mounted at `/workspace` | The outpost clones from the hub's git server at the pinned revision; no host mounts |
| Control channel | `podman exec`, file read/write through the provider | NATS request/reply with the outpost; the provider only launches, inspects and stops |
| Runtime abstraction | Provider interface with podman bindings in-process | Gateway interface (local in-process, remote over NATS) × provider (`container`, `microvm`, `pod`) |
| Configuration | Hub-level defaults with per-org/per-workspace overrides in the DB | One `sandbox.json` document: hub default file, optional workspace replacement, hub-level ceilings |
| Token | Short-lived PAT with workspace read/write | PAT plus a hub-side binding to one sandbox and one workspace, revoked on stop |
| podman integration | `containers/podman/v5` Go bindings over the socket | The `podman` CLI, driven like `git` is today |

Everything in PRD 15 not listed here (audit events, job queue use, health
integration, security posture of the container) carries over in spirit.

## Concepts

- **Sandbox.** An ephemeral execution environment for one workspace at one
  revision, working on one branch. Identified by a UUID, recorded in the
  hub's database, realised by a gateway using a provider.
- **Provider.** How a sandbox is realised: `container` (OCI container),
  `microvm` (container image booted in a microVM, podman with libkrun), or
  `pod` (Kubernetes pod).
- **Gateway.** The component that realises sandboxes. The **local gateway**
  is built into the hub and drives podman on the hub host. A **remote
  gateway** is a separate process (`af-gateway`) on another host or in a
  cluster that speaks the same interface to the hub over NATS.
- **Outpost.** The process that runs as the sandbox's entrypoint
  (`afc outpost run`). It bootstraps the sandbox from the injected
  environment, connects to the hub's NATS server, and executes commands.
- **Sandbox configuration.** The JSON document (`sandbox.json`) that says
  which image, provider, gateway, resources, network policy, environment,
  secrets and timeouts a sandbox is launched with.
- **Sandbox token.** The hub credential injected into the sandbox. It is
  valid for exactly one workspace and one sandbox and dies with the sandbox.

## Functional Requirements

### Sandbox configuration

- **CFG-1.** The hub SHALL read a default sandbox configuration from
  `$XDG_CONFIG_HOME/sandbox.json` (next to `config.toml`), overridable with
  `[sandbox] config_path` in `config.toml`. If the file is missing the hub
  SHALL use a built-in default (image `quay.io/agentfox/agents:latest`,
  provider `container`, gateway `local`, 2 CPU, 4 GiB, 512 pids, egress
  `all`, boot 5m, idle 30m, max lifetime 8h) and log that it did so. A file
  that exists but does not validate SHALL abort startup.
- **CFG-2.** The document schema, version 1:

  ```json
  {
    "version": 1,
    "provider": "container",
    "gateway": "local",
    "image": "quay.io/agentfox/agents:latest",
    "command": ["afc", "outpost", "run"],
    "workdir": "/opt/app-root/workspace",
    "resources": { "cpu": "2", "memory": "4Gi", "pids": 512, "disk": "10Gi" },
    "network": { "egress": "all", "allow": [] },
    "env": { "AGENT_LOG_LEVEL": "info" },
    "vars": "all",
    "secrets": ["ANTHROPIC_API_KEY"],
    "timeouts": { "boot": "5m", "idle": "30m", "max": "8h" }
  }
  ```

  `provider` is one of `container`, `microvm`, `pod`. `gateway` names the
  built-in `local` gateway or a gateway registered in `config.toml`.
  `command` and `workdir` are optional and default to the image's. `cpu`
  and `memory` use Kubernetes quantity syntax; `disk` is advisory for
  gateways that can enforce it. `network.egress` is `all` or `restricted`;
  with `restricted`, `allow` lists `host[:port]` entries, and the hub's own
  HTTP and NATS endpoints are always implicitly allowed. `vars` is `"all"`
  or a list of variable keys to inject from the workspace's resolved
  variables. `secrets` is a list of secret keys resolved workspace > org >
  user, the same order as variables. `env` holds plain values. Timeouts use
  Go duration syntax.
- **CFG-3.** A workspace MAY store its own sandbox configuration through
  `PUT /api/v1/workspaces/:slug/sandbox-config`. When present it SHALL
  replace the default document entirely; there is no field-level merge.
  `GET` returns the effective document with `"origin": "workspace"` or
  `"origin": "default"`; `DELETE` removes the workspace document.
- **CFG-4.** Independently of which document applies, the hub SHALL enforce
  ceilings from `config.toml` at launch time: `[sandbox.limits] max_cpu`,
  `max_memory`, `max_pids`, `max_lifetime`, `allowed_images` (glob list),
  `allowed_providers`, `allowed_gateways`. A configuration that exceeds a
  ceiling is rejected at `PUT` time with `400 sandbox_config_invalid` naming
  the field, and again at launch time if the ceilings changed since.
- **CFG-5.** Keys in `env` SHALL NOT start with `AF_`; `vars` and `secrets`
  keys that collide with a reserved `AF_*` name are ignored with a warning in
  the launch record. A `secrets` key that does not resolve at launch SHALL
  fail the launch with `secret_not_found` naming the key (never launch with
  a silently missing credential).
- **CFG-6.** The launch request MAY override `image`, `resources`, `env`,
  and `timeouts` for that one sandbox, subject to CFG-4. It MAY NOT override
  `secrets`, `provider`, `gateway` or `network`.
- **CFG-7.** The hub SHALL store the configuration it actually launched with
  in the sandbox record, with secret values replaced by their key names, so
  that a record is self-describing after the workspace document changes.

### Lifecycle

- **LC-1.** `POST /api/v1/workspaces/:slug/sandboxes` SHALL create a sandbox
  record in status `pending` and enqueue a `sandbox_launch` job keyed by the
  sandbox id. The request body is
  `{ "ref": "...", "branch": "...", "ttl": "...", "metadata": {}, "overrides": {} }`.
  `ref` defaults to the workspace branch; `branch` defaults to
  `sandbox/<short id>`; `ttl` caps `timeouts.max` for this sandbox.
- **LC-2.** Before enqueueing, under the workspace lock, the hub SHALL
  resolve `ref` to a commit SHA in the workspace trunk and store it as
  `revision`. A `ref` that does not resolve fails with `404 ref_not_found`.
  If `branch` does not exist the hub SHALL create it at `revision` in the
  trunk so that the sandbox's first push is a plain fast-forward. If it
  exists and is not at `revision`, the launch fails with
  `409 branch_diverged` unless `"reset_branch": true` is passed.
- **LC-3.** The workspace SHALL be `active` with `clone_status = ready`;
  otherwise `409 workspace_not_ready`. The number of sandboxes in a
  non-terminal status per workspace SHALL NOT exceed
  `[sandbox] max_per_workspace` (default 4) and the hub-wide total SHALL NOT
  exceed `[sandbox] max_total` (default 32); exceeding either returns
  `429 sandbox_limit`.
- **LC-4.** Statuses and transitions:

  | From | To | When |
  |---|---|---|
  | `pending` | `starting` | the launch job is picked up |
  | `starting` | `booting` | the gateway reports the sandbox running |
  | `starting` | `failed` | the gateway cannot launch (image pull, capacity, unsupported provider or policy) |
  | `booting` | `ready` | the outpost's `ready` event arrives |
  | `booting` | `failed` | no `ready` within `timeouts.boot` (`exit_reason: boot_timeout`) |
  | `ready` | `stopping` | stop request, idle timeout, max lifetime, workspace archive/delete |
  | `ready` | `failed` | three consecutive missed heartbeats and the gateway reports the sandbox gone (`exit_reason: lost`), or the gateway reports an unexpected exit (`exit_reason: exited`, with exit code) |
  | `stopping` | `stopped` | the gateway confirms the sandbox is gone |
  | `stopping` | `failed` | the gateway cannot stop it after the grace period plus force |

  `stopped` and `failed` are terminal. Every terminal transition records
  `ended_at` and `exit_reason`.
- **LC-5.** Stopping SHALL be graceful: the hub sends `shutdown` over NATS
  with the requested grace (default 30s) and, if requested, `push: true`;
  waits for the outpost's `exiting` reply or the grace period; then asks the
  gateway to stop, which sends SIGTERM and then SIGKILL after 10s. A sandbox
  whose outpost never connected skips the NATS step.
- **LC-6.** Workspace archive and delete SHALL stop every non-terminal
  sandbox of the workspace with `push: false` before proceeding, and SHALL
  wait for them to reach a terminal status (bounded by the grace period plus
  force). Reactivate and reclone do not launch anything.
- **LC-7.** Time limits: `timeouts.boot` (LC-4), `timeouts.idle` (no
  activity, see OP-7, for that long), `timeouts.max` (since `started_at`) and
  the launch `ttl` all cause a graceful stop with the matching
  `exit_reason`. A stop caused by a limit SHALL use `push: true` so that
  work is not lost silently; the outcome of that push is recorded on the
  record.
- **LC-8.** The record exposed by the API:

  ```json
  {
    "id": "uuid",
    "workspace_slug": "my-project",
    "status": "ready",
    "provider": "container",
    "gateway": "local",
    "image": "quay.io/agentfox/agents:latest",
    "ref": "main",
    "revision": "abc123…",
    "branch": "sandbox/1a2b3c",
    "head_sha": "def456…",
    "dirty": false,
    "last_heartbeat_at": "…",
    "created_by": "uuid",
    "created_at": "…", "started_at": "…", "ready_at": "…", "ended_at": null,
    "exit_reason": null, "error": null,
    "final_push": null,
    "metadata": {},
    "config": { "…redacted launch configuration…" }
  }
  ```

  `head_sha` and `dirty` are the outpost's last reported working-tree state.
  `final_push` records the result of a shutdown-time push (`{ "ok": true,
  "sha": "…" }` or `{ "ok": false, "error": "…" }`). The gateway's handle
  (container id, pod name) is visible to admin tokens only.
- **LC-9.** Sandbox records SHALL be kept after they end, listable with
  `?status=`, and pruned by the existing retention worker after
  `AF_SANDBOX_MAX_AGE_DAYS` (default 30).

### Sandbox token

- **TK-1.** At launch the hub SHALL mint a PAT on behalf of the workspace
  owner with exactly the scopes `git:read`, `git:write`, `sessions:write`,
  `audit:write`, `vars:read`, `sandboxes:read`, no expiry at the PAT level,
  and SHALL record a binding `(token_id, sandbox_id, workspace_slug,
  expires_at)` in a hub table. `expires_at` is `started_at + timeouts.max`.
- **TK-2.** Every hub handler that resolves a workspace from the path, and
  the git server's authorisation, SHALL consult the binding for the
  credential id in `AuthInfo`: a bound credential used against another
  workspace, or after `expires_at`, or after revocation, is rejected with
  `404` (workspace endpoints, git server) or `403 token_scope` (session and
  audit ingestion endpoints), in line with the anti-enumeration policy.
  Session records opened with a bound token SHALL carry the sandbox id in
  their metadata.
- **TK-3.** The binding SHALL be revoked, and the PAT revoked best-effort,
  when the sandbox reaches a terminal status. A bound token SHALL NOT be
  able to create PATs, list tokens, or read secrets, regardless of what the
  underlying PAT could do.
- **TK-4.** The plaintext token SHALL exist only in the launch job's
  in-memory payload and the sandbox's environment. It SHALL NOT be written
  to the job's persisted payload, the sandbox record, logs or audit events.

### Bootstrap environment

- **ENV-1.** The gateway SHALL inject, in this order with later entries
  losing to earlier ones on collision: the reserved `AF_*` variables below;
  the workspace's resolved variables selected by `vars`; the secrets named
  in `secrets`; the `env` map.

  | Variable | Value |
  |---|---|
  | `AF_HUB_URL` | HTTP base URL of the hub as reachable from the gateway's network |
  | `AF_HUB_TOKEN` | the sandbox token |
  | `AF_NATS_URL` | NATS URL as reachable from the gateway's network |
  | `AF_NATS_USER`, `AF_NATS_PASSWORD` | per-sandbox NATS credentials (MSG-3) |
  | `AF_SANDBOX_ID` | sandbox id |
  | `AF_WORKSPACE` | workspace slug |
  | `AF_WORKSPACE_GIT_URL` | clone URL on the hub's git server (`hub_url`) |
  | `AF_WORKSPACE_BRANCH` | the branch the sandbox works on |
  | `AF_REVISION` | the pinned commit SHA |
  | `AF_WORKDIR` | the checkout directory |

- **ENV-2.** URLs in `AF_HUB_URL`, `AF_NATS_URL` and `AF_WORKSPACE_GIT_URL`
  SHALL be computed per gateway: the local gateway uses
  `[sandbox.local] hub_url` and `nats_url` when set (for example
  `host.containers.internal` when the hub runs on the host) and falls back
  to `[server] external_url` and `[nats] external_url`; a remote gateway
  uses the values in its `[[sandbox.gateways]]` entry with the same
  fallback. A launch with no usable URL fails with `hub_unreachable_config`
  rather than launching a sandbox that cannot phone home.
- **ENV-3.** Secrets are injected as environment variables at creation time,
  as the brief specifies. They are visible to anyone who can inspect the
  container on the gateway host; SEC-3 covers the consequences. Rotating a
  secret does not affect running sandboxes.

### Outpost

- **OP-1.** `afc outpost run` SHALL be the sandbox entrypoint. It reads the
  `AF_*` environment, fails fast with a clear message if any required
  variable is missing, and never reads `~/.af/config.toml` for its own
  credentials.
- **OP-2.** Bootstrap: write `~/.af/config.toml` with `endpoint_url =
  AF_HUB_URL` and `api_key = AF_HUB_TOKEN` (mode 0600) so that `afc` and
  the existing credential helper work for agents and for git; configure
  `credential.<AF_HUB_URL>.helper = "!afc credential-helper"` globally;
  clone `AF_WORKSPACE_GIT_URL` into `AF_WORKDIR` with `--branch
  AF_WORKSPACE_BRANCH`; verify `HEAD == AF_REVISION` (fail otherwise: the
  branch moved between launch and boot, which LC-2 makes unlikely but not
  impossible); set a commit identity from `AF_SANDBOX_ID` unless
  `GIT_AUTHOR_*` is injected.
- **OP-3.** Connect to `AF_NATS_URL` with the injected credentials, with
  unlimited reconnect and exponential backoff capped at 30s. Publish `ready`
  once the clone is verified and the subscription to the command subject is
  live. A bootstrap failure before `ready` SHALL be published as
  `boot_failed` with the error when NATS is reachable, then the process
  exits non-zero.
- **OP-4.** Commands (MSG-4) are handled one at a time in arrival order.
  `sync` with `direction: pull` runs `git fetch origin` and then
  `git checkout -B <branch> <ref>` (default `origin/<branch>`); if the
  working tree is dirty it replies `dirty_worktree` unless `force: true`, in
  which case it discards local changes. `sync` with `direction: push` runs
  `git push origin <branch>` (`--force-with-lease` when `force: true`) and
  replies with the pushed SHA or the git error. `shutdown` performs the
  push first when `push: true`, replies `exiting` with the push result,
  and exits 0. `ping` replies with the status block of OP-7.
- **OP-5.** The outpost does nothing else with the working tree: no
  automatic commits, no automatic pushes except at shutdown when asked.
  Whatever runs inside the sandbox (an agent, a shell) commits and pushes
  through the normal git remote; the hub's post-push hooks fire as for any
  other push.
- **OP-6.** The outpost SHALL keep running after `ready` for the life of the
  sandbox. It SHALL NOT spawn the agent. A caller who wants a process
  started at boot puts it in `command` (for example a wrapper that starts
  `afc outpost run` in the background and then the agent).
- **OP-7.** Every 30s the outpost publishes `heartbeat` with
  `{ "head_sha", "dirty", "uptime_s", "last_activity_at" }`.
  `last_activity_at` advances when a command is handled or when the
  working-tree state (HEAD plus a hash of `git status --porcelain`) changed
  since the previous heartbeat. The hub uses it for `timeouts.idle`.
- **OP-8.** The outpost SHALL treat SIGTERM like a `shutdown` with
  `push: false` and a 10s budget, so that a gateway-initiated stop still
  produces an `exiting` event when NATS is reachable.

### Hub ↔ outpost messaging

- **MSG-1.** The hub SHALL embed `nats-server` (`github.com/nats-io/nats-server/v2`)
  in the `hub` process, started before the HTTP server and stopped after it.
  Configuration in `config.toml`:

  ```toml
  [nats]
  enabled      = true
  bind         = "0.0.0.0"
  port         = 4222
  external_url = "nats://hub.example.com:4222"
  websocket_port = 0              # >0 enables a WebSocket listener for ingress-only deployments
  tls_cert     = ""               # optional; when set, tls_key is required
  tls_key      = ""
  ```

  With `enabled = false` the sandbox subsystem is off and its endpoints
  return `404`.
- **MSG-2.** Subjects:

  | Subject | Direction | Pattern |
  |---|---|---|
  | `af.sbx.<id>.cmd` | hub → outpost | request/reply |
  | `af.sbx.<id>.evt.<type>` | outpost → hub | publish |
  | `af.gw.<name>.cmd` | hub → remote gateway | request/reply |
  | `af.gw.<name>.evt.<type>` | remote gateway → hub | publish |

- **MSG-3.** Each sandbox SHALL get its own NATS user (`sbx-<id>`, random
  password) with permissions: subscribe `af.sbx.<id>.cmd`, publish
  `af.sbx.<id>.evt.>`, and `allow_responses` so it can answer requests
  without a blanket `_INBOX.>` grant. Users are added at launch and removed
  at terminal status through the embedded server's options reload. Remote
  gateways get the same treatment on `af.gw.<name>.>` with a static token
  from `config.toml`. The hub's own connection is an in-process client with
  full permissions; nothing else can connect without credentials.
- **MSG-4.** Every message is a JSON envelope
  `{ "id": "uuid", "type": "sync", "sent_at": "…", "sandbox_id": "…", "payload": {} }`.
  Replies reuse `id` and carry `{ "ok": true, "payload": {} }` or
  `{ "ok": false, "error": { "code": "dirty_worktree", "message": "…" } }`.
  Command types in this PRD: `ping`, `sync`, `shutdown`. Event types:
  `ready`, `boot_failed`, `heartbeat`, `exiting`. Unknown command types are
  answered with `unsupported`; unknown event types are logged and dropped.
  A command that receives no reply within its timeout (`ping` 5s, `sync`
  10m, `shutdown` grace + 5s) is reported to the caller as `timeout`.
- **MSG-5.** The hub SHALL consume events on `af.sbx.>` from one durable
  subscription and apply them to records idempotently, keyed by envelope
  id, so that a redelivered or reordered event cannot regress a status.

### Gateway interface

- **GW-1.** The Go interface every gateway implements:

  ```go
  type Gateway interface {
      Name() string
      Capabilities(ctx) (Capabilities, error) // providers, network policies, resource ceilings
      Launch(ctx, LaunchSpec) (Handle, error)
      Inspect(ctx, Handle) (Status, error)     // running | exited(code) | gone
      Stop(ctx, Handle, grace time.Duration) error
      List(ctx) ([]Handle, error)              // handles carrying the hub's labels
      Health(ctx) error
  }
  ```

  `LaunchSpec` carries the sandbox id, provider, image, command, workdir,
  resources, network policy, the full environment, and labels
  (`af.sandbox=<id>`, `af.workspace=<slug>`). Errors are typed:
  `ErrUnsupportedProvider`, `ErrUnsupportedPolicy`, `ErrImagePull`,
  `ErrCapacity`, `ErrNotFound`, `ErrUnavailable`.
- **GW-2.** The launch job SHALL check `Capabilities` before `Launch`: a
  provider or a `restricted` network policy the gateway does not support
  fails the launch with `422 gateway_unsupported`; it is never downgraded.
- **GW-3.** Gateways SHALL be stateless with respect to the hub: everything
  the hub needs to find a sandbox again after a restart is in the labels and
  the handle stored on the record.

### Local gateway

- **LG-1.** The local gateway SHALL drive the `podman` CLI as a subprocess
  with the same hardening the hub applies to `git` (own process group,
  bounded deadlines, scrubbed environment). It SHALL be registered
  automatically when `podman` is on `PATH` (or at `[sandbox.local] podman`),
  and its `Health` runs `podman info`.
- **LG-2.** `container` provider: `podman run --detach --rm=false
  --name sbx-<id> --label … --cpus … --memory … --pids-limit …
  --cap-drop ALL --security-opt no-new-privileges --read-only
  --tmpfs /tmp --user 1001 …` with the working directory
  as an anonymous volume so it is writable while the root filesystem is
  not. Environment is passed with `--env-file` from a 0600 temp file
  deleted after `run` returns, never on the command line.
- **LG-3.** `microvm` provider: the same invocation with
  `--runtime krun` (libkrun). `Capabilities` reports `microvm` only when
  `podman info` lists that runtime.
- **LG-4.** Network policy: `all` uses podman's default network.
  `restricted` is reported as unsupported by the local gateway in this
  iteration (see Open Questions for the options).
- **LG-5.** `Inspect` maps `podman inspect` state to `running`, `exited`
  (with exit code) or `gone`; `Stop` runs `podman stop -t <grace>` then
  `podman rm -f`; `List` uses `podman ps -a --filter label=af.sandbox`.
- **LG-6.** Images named in configuration SHALL be pulled at launch if
  absent (`podman pull`), with the pull's duration counted against
  `timeouts.boot`. `[sandbox.local] prepull = true` pulls the default
  image at hub startup.

### Remote gateways

- **RG-1.** A remote gateway is declared in `config.toml`:

  ```toml
  [[sandbox.gateways]]
  name     = "cloud-1"
  token    = "${GW_CLOUD1_TOKEN}"   # NATS credential for af.gw.cloud-1.>
  hub_url  = "https://hub.example.com"
  nats_url = "nats://hub.example.com:4222"
  ```

  It is `connected` when a client authenticated with its token has
  published `af.gw.<name>.evt.registered` (carrying its `Capabilities`) and
  `disconnected` after a missed heartbeat interval. Launching on a
  disconnected gateway fails with `503 gateway_unavailable`.
- **RG-2.** The remote gateway protocol is the `Gateway` interface as NATS
  request/reply on `af.gw.<name>.cmd` with the MSG-4 envelope: `launch`,
  `inspect`, `stop`, `list`, `health`. The hub-side implementation of
  `Gateway` for a remote entry is a thin NATS client.
- **RG-3.** `af-gateway` SHALL be a new static binary (`cmd/af-gateway`)
  that hosts the same podman implementation the local gateway uses and, in a
  later PRD, the Kubernetes one; it connects out to the hub's NATS server,
  so a remote host needs no inbound ports. Sandboxes it launches reach the
  hub through the URLs in the gateway's entry (ENV-2).

### HTTP API

- **API-1.** Endpoints, all requiring workspace ownership (admin bypasses):

  | Method and path | Scope | Effect |
  |---|---|---|
  | `POST /api/v1/workspaces/:slug/sandboxes` | `sandboxes:write` | launch (LC-1), `202` with the record |
  | `GET /api/v1/workspaces/:slug/sandboxes` | `sandboxes:read` | list, `?status=` filter |
  | `GET /api/v1/workspaces/:slug/sandboxes/:id` | `sandboxes:read` | record |
  | `POST /api/v1/workspaces/:slug/sandboxes/:id/sync` | `sandboxes:write` | `sync` command; `200` with the reply, or `409 sandbox_not_ready` |
  | `DELETE /api/v1/workspaces/:slug/sandboxes/:id` | `sandboxes:write` | graceful stop, body `{ "push": bool, "grace": "30s" }`, `202` |
  | `GET/PUT/DELETE /api/v1/workspaces/:slug/sandbox-config` | `sandboxes:read` / `sandboxes:write` | CFG-3 |
  | `GET /api/v1/sandboxes` | admin | all sandboxes across workspaces |
  | `GET /api/v1/gateways` | admin | gateways, connection state, capabilities |

  `sandboxes:write` implies `sandboxes:read`. Error envelopes and `404`
  anti-enumeration follow `docs/api.md`.
- **API-2.** `GET …/sandboxes/:id` SHALL support ETags via apikit so that
  `afc sandbox get --wait` can poll cheaply.
- **API-3.** A bound sandbox token (TK-1) may call `GET …/sandboxes/:id`
  for its own sandbox only, so a process inside can learn its own status.

### CLI

- **CLI-1.** `afc sandbox launch <slug> [--ref] [--branch] [--ttl]
  [--image] [--cpu] [--memory] [--env K=V]... [--wait]`, `afc sandbox list
  <slug> [--status]`, `afc sandbox get <slug> <id> [--wait-for ready]`,
  `afc sandbox sync <slug> <id> --pull|--push [--ref] [--force]`,
  `afc sandbox stop <slug> <id> [--push] [--grace] [--wait]`,
  `afc sandbox config get|set|delete <slug> [--file sandbox.json]`,
  `afc sandbox gateways` (admin). `--wait` variants exit non-zero on
  `failed`, as `afc rebuild wait` does.
- **CLI-2.** `afc outpost run` (OP-1). Hidden from the top-level help;
  documented in `docs/cli.md` under a "Sandbox internals" heading.

### Security

- **SEC-1.** Sandboxes SHALL never receive host mounts, the hub's data
  directory, or another workspace's credentials. The only way into a
  workspace is the git server with the sandbox token.
- **SEC-2.** Containers run as a non-root user with all capabilities
  dropped, `no-new-privileges`, a read-only root filesystem and a pid limit
  (LG-2). The hub does not claim VM-level isolation from the `container`
  provider; operators who need it choose `microvm` or a `pod` provider with
  a hardened RuntimeClass.
- **SEC-3.** Because secrets are container environment variables, anyone
  with podman access on the gateway host can read them. The PRD accepts
  this for the local gateway (the host already holds the hub's database).
  Remote gateway hosts are trusted to the same degree; the alternative
  (outpost fetches secrets over HTTPS after boot) is an open question.
- **SEC-4.** The sandbox token's scopes (TK-1) do not include
  `secrets:*`, `tokens:*`, `workspaces:*` or `sandboxes:write`; a
  compromised sandbox can push to its workspace and open sessions, nothing
  else, and only until it is stopped.
- **SEC-5.** Audit events and logs SHALL redact `AF_HUB_TOKEN`,
  `AF_NATS_PASSWORD` and every value injected from `secrets`.

### Audit, sessions and metrics

- **AU-1.** Events through the existing emitter: `hub.sandbox.launch`
  (actor, workspace, provider, gateway, image, ref, revision, branch),
  `hub.sandbox.ready`, `hub.sandbox.sync` (direction, result),
  `hub.sandbox.stop` (reason, final push result), `hub.sandbox.failed`
  (reason, error). Timeouts and reconciliation use actor `system`.
- **AU-2.** Metrics: `af_sandboxes{gateway,provider,status}` gauge,
  `af_sandbox_launch_seconds` histogram (`pending` to `ready`),
  `af_sandbox_ended_total{exit_reason}`, `af_nats_connections`.
- **AU-3.** Sessions opened with a sandbox token carry
  `metadata.sandbox_id` (TK-2); `GET /workspaces/:slug/cost` can therefore
  be broken down per sandbox later without a schema change.

### Operations

- **OPS-1.** On startup the hub SHALL reconcile: for every record in a
  non-terminal status, `Inspect` through its gateway; `gone` becomes
  `failed`/`lost`; `exited` becomes `failed`/`exited`; `running` records
  keep their status and get a fresh boot or heartbeat deadline. Then
  `List` on every available gateway and stop any labelled sandbox with no
  record. This runs after schema init and before the HTTP server starts,
  like `ReconcileStuckSyncs`.
- **OPS-2.** Sandboxes SHALL survive a hub restart. The hub does not stop
  them on shutdown; outposts reconnect (OP-3) and the deadlines resume.
- **OPS-3.** A periodic sweep (every minute) SHALL apply LC-7 deadlines and
  the missed-heartbeat rule, and SHALL requeue a stuck `stopping` record.
- **OPS-4.** `/readyz` SHALL include `nats` and each gateway's `Health`;
  a failing gateway marks the sandbox subsystem degraded without failing
  the hub.
- **OPS-5.** The sandbox and agents images SHALL include `afc` at
  `/usr/local/bin/afc` and set `ENTRYPOINT ["afc", "outpost", "run"]`;
  `make build-sandbox-container` copies the binary built by `make build`.

### Testing

- Unit: configuration validation and ceilings (CFG); status machine (LC-4)
  with a fake gateway and injected events; token binding checks (TK) in
  the workspace and git server authz tests; envelope encode/decode and the
  outpost command handlers against a temporary git repository.
- Integration: an embedded `nats-server` on a random port in tests; the
  outpost run as a goroutine against a test hub with the existing git
  server test helpers, covering `ready`, `sync` both ways, `dirty_worktree`,
  `shutdown` with push, reconnect after the test server restarts.
- Manual/CI: `make hub-run` plus a local podman launching
  `quay.io/agentfox/agents` and pushing a commit back, as the acceptance
  check for the local gateway.

## Technical Boundaries

- Go, in the existing `hub` and `afc` binaries plus a new `af-gateway`
  binary. New packages: `internal/sandbox` (records, config, lifecycle,
  API), `internal/sandbox/gateway` (interface, local podman implementation,
  NATS remote client), `internal/outpost` (used by `afc`), `internal/nats`
  (embedded server and hub client).
- New dependencies: `github.com/nats-io/nats-server/v2` and
  `github.com/nats-io/nats.go`. No podman Go bindings; the CLI is driven as
  a subprocess like git.
- State in the existing SQLite database: `sandboxes`, `sandbox_tokens`,
  `workspace_sandbox_configs`. Sandbox records use the durable job queue
  for `sandbox_launch` and `sandbox_stop`, grouped by sandbox id.
- Documentation to update when implementing: `docs/api.md`, `docs/cli.md`,
  `docs/configuration.md` (`[sandbox]`, `[nats]`, `AF_SANDBOX_MAX_AGE_DAYS`),
  `docs/permissions.md`, `README.md` (third binary, NATS port),
  `deploy/` (NATS port on the Service, and a Route or the WebSocket listener),
  `containers/*/Containerfile`.

## Dependencies

- `../apikit`: PAT minting on behalf of a user from server code, credential
  id on `AuthInfo`, ETag helpers. Whether apikit exposes a server-side
  "create PAT for user" call, and whether it accepts sub-day expiries, could
  not be verified from this checkout (the sibling repository is not
  present); TK-1 is written so that neither is required.
- `internal/workspace` (lock, trunk rev-parse, archive/delete hooks),
  `internal/gitserver` (authz binding check), `internal/secrets` (resolved
  variables and secret values), `internal/jobqueue`, `internal/audit`.
- podman 5 on the hub host for the local gateway; libkrun for `microvm`.
- The `containers/sandbox` and `containers/agents` images gain `afc`.

## Design Decisions

### 1. Gateway × provider, not one provider enum

The brief lists "container/podman, microvm/podman, pod/kubernetes" and
separately "gateway interface for remote hosts" and "local gateway using
podman". Splitting the two axes keeps each small: a provider is a launch
recipe, a gateway is who executes it and where. The podman recipe is shared
by the in-process local gateway and by `af-gateway` on a remote host, so
"remote podman" is free, and a Kubernetes gateway adds one recipe without
touching the protocol.

### 2. Clone through the git server, no host mounts

PRD 15 bind-mounted the hub's clone into the container. That ties a sandbox
to the hub host, makes remote gateways impossible, and lets a sandbox mutate
the trunk that sync, rebuild and merge jobs operate on. Cloning at a pinned
revision through the git server costs a clone per sandbox but makes every
sandbox self-contained and every gateway equal.

### 3. NATS as the only control channel, including for remote gateways

*Recommendation.* The brief calls for NATS between hub and outpost. Using it
for remote gateways too means remote hosts dial out and need no inbound
ports, the hub needs one listener, and the same envelope and auth model
cover both. The alternative, an HTTP API on each gateway, would need TLS and
credentials per host and reachability from the hub.

### 4. Per-sandbox NATS users through options reload

*Recommendation.* Embedded `nats-server` supports per-user publish and
subscribe permissions and `allow_responses`, and `ReloadOptions` adds and
removes users at runtime. It is the simplest thing that gives each sandbox a
credential that can only touch its own subjects. Auth callout (the hub
validating the sandbox token itself when NATS asks) is cleaner in the long
run, since one credential would serve both HTTP and NATS, but needs NKey
signing and a callout service; it is deferred.

### 5. Token binding in the hub instead of a new credential type

*Recommendation.* apikit owns credentials, and the steering rules forbid
custom credential resolution. A PAT minted for the workspace owner plus a
hub-side binding to one sandbox gives a workspace-scoped, sandbox-scoped,
short-lived token without an apikit change; if apikit later grows
resource-bound PATs, the binding table folds into it.

### 6. The podman CLI, not the Go bindings

The hub already runs git as a hardened subprocess. The podman bindings
module is large, and the CLI is what operators debug with (`podman ps`
shows exactly what the hub started). `af-gateway` stays a static binary.

### 7. Whole-document replacement for the workspace configuration

The brief says the workspace document "replaces" the default. A merge would
be friendlier for one-field changes but makes the effective configuration
hard to reason about and audit. Ceilings (CFG-4) are the one thing a
workspace cannot escape.

### 8. Secrets as environment variables

As the brief specifies. The trade-off (SEC-3) is accepted for now; the
outpost-fetches-secrets alternative is listed under Open Questions because
it also removes secrets from remote gateway hosts.

### 9. Sandboxes outlive the hub process

Stopping every sandbox on hub shutdown (PRD 15's choice) makes a hub upgrade
kill running agent work. Reconciliation on startup plus outpost reconnect is
a small amount of code for a large operational win.

### 10. The outpost is `afc`

A separate binary would need its own build, release and image plumbing. The
outpost is small, needs the same HTTP client and credential helper `afc`
already has, and `afc` is already a static binary.

## Open Questions

1. **Workspace configuration: hub-stored or in-repo?** This draft stores it
   on the hub (`PUT …/sandbox-config`). The alternative is
   `.af/sandbox.json` at the pinned revision, versioned with the code and
   visible to agents, at the cost of letting a push change the next
   sandbox's image or resources (ceilings would still apply). Both could
   coexist with the hub document winning.
2. **Restricted egress on the local gateway.** Options: (a) a podman
   internal network shared with a containerised hub, which only works when
   the hub itself runs in podman; (b) `pasta`/`slirp4netns` with host
   firewall rules, which needs root or nftables delegation; (c) an egress
   proxy sidecar and `HTTPS_PROXY` in the sandbox, which only covers
   well-behaved clients. The draft marks `restricted` unsupported locally
   and honest about it (GW-2). Which, if any, is worth doing first?
3. **Should the outpost fetch secrets after boot** instead of receiving them
   as container env? It keeps secrets off the gateway host and out of
   `podman inspect`, at the cost of an extra endpoint and a token scope
   that can read secret values, which SEC-4 currently denies.
4. **Default `branch` and push target.** The draft creates
   `sandbox/<short id>` at the pinned revision and pushes there, leaving
   merge to the merge queue. Should a launch be allowed to work directly on
   the workspace branch?
5. **NATS auth: per-user reload now, auth callout later, or callout from
   the start?** See decision 4.
6. **NATS exposure in Kubernetes.** `deploy/` only routes HTTP. Sandboxes in
   the same cluster can use the Service; remote gateways and out-of-cluster
   sandboxes need a TCP route or the WebSocket listener (`[nats]
   websocket_port`) behind the existing edge-TLS Route. Is WebSocket the
   default for anything outside the cluster?
7. **Config file location.** The brief says "data/config folder". The draft
   uses `$XDG_CONFIG_HOME/sandbox.json` because it is configuration, not
   data, and lives next to `config.toml` in the container image and the
   ConfigMap. Confirm.
8. **Limits per workspace and hub-wide** (`max_per_workspace = 4`,
   `max_total = 32`): right defaults? Should the limit be per user or per
   org rather than per workspace?
9. **Should a launch be able to open the agent session** (POST /sessions)
   and pass `AF_SESSION_ID` in, so cost is attributed even if the agent
   inside never opens one?
10. **`exec` over NATS and `afc sandbox shell`.** Left out of this
    iteration. If a shell is a day-one need for humans, the command set and
    the token scopes need to grow now rather than later.
11. **Idle detection** relies on working-tree changes and commands (OP-7).
    An agent that only reads or only calls an LLM looks idle. Is a longer
    default (`idle = 30m`) enough, or should the sandbox be able to declare
    itself busy?
