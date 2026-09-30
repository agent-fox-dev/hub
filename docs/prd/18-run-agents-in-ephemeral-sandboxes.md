# Run Agents in Ephemeral Sandboxes

> **Status: draft.** Two workshop rounds settled the configuration
> location, secrets delivery, the default branch, NATS exposure, limits,
> sessions, exec, liveness, telemetry collection, workload output, secret
> refresh and the single-fetch bootstrap; those decisions are folded into
> the text below. The Open Questions at the end are what remains.

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
itself. It provisions the environment, injects the minimum the environment
needs to phone home (hub endpoint, a workspace-scoped token, the sandbox
id), and keeps a two-way control channel open to a small service inside the
sandbox, the **outpost**, for the lifetime of the sandbox. The outpost
fetches everything else (code, variables, secrets, the workload command)
from the hub and starts the workload.

Three things make this more than "run a container":

1. **Where the sandbox runs is pluggable.** A *provider* says how a sandbox
   is realised (an OCI container, a microVM, a Kubernetes pod). A *gateway*
   is the thing that realises it: the built-in local gateway drives podman on
   the hub host; remote gateways implement the same interface for other
   machines and clusters.
2. **Configuration is a document, not a pile of flags.** The hub ships a
   default `sandbox.json`; a workspace can replace it with its own through
   the API.
3. **The hub can talk to the sandbox, not only start it.** The hub embeds a
   NATS server. The outpost connects to it and receives commands (`sync`,
   `shutdown`, ...) and reports liveness, state and telemetry, so the hub can
   drive a sandbox session without SSH, exec, or polling the container
   runtime.

An earlier proposal, `docs/prd/prd15.md`, covered the same ground with a
different model (one long-lived sandbox per workspace, the hub's clone
bind-mounted into the container, control via `podman exec`). This PRD
supersedes it; the differences are listed under *Relationship to PRD 15*.

## Goals

- Let a caller launch a sandbox for a workspace on a branch (the workspace
  branch by default), pinned to that branch's revision at launch time, and
  get back a record whose status the hub keeps current until the sandbox is
  gone.
- Define one JSON sandbox configuration document used at every level: a
  hub-wide default file, and an optional per-workspace document, managed
  through the API, that replaces it.
- Inject a bootstrap environment of exactly three variables into every
  sandbox (hub URL, sandbox token, sandbox id) and let the outpost fetch the
  rest, including secrets, over HTTPS with that token.
- Ship the outpost as part of the existing `afc` binary so that the sandbox
  and agents images (`containers/sandbox`, `containers/agents`) need nothing
  new beyond the binary itself.
- Embed a NATS server in the hub, reachable over WebSocket through the hub's
  existing HTTP endpoint, and define the subjects, message envelope and
  command set between hub and outpost, plus the telemetry the outpost
  collects from agents inside the sandbox and forwards to the hub.
- Define a gateway interface and ship the local gateway (podman) with two
  providers: `container` and `microvm`. Define how remote gateways attach.
- Bind sandbox lifetime to workspace lifetime (archive and delete stop every
  sandbox of the workspace), to explicit stop requests, to the workload's
  exit, to time limits (boot, maximum lifetime) and to liveness.
- Expose the whole thing through the REST API and `afc`, with permission
  scopes and audit events in the existing style.

## Non-goals

- **Running the agent.** The hub launches the environment, starts the
  configured workload command through the outpost, and can tell the sandbox
  to sync or shut down. Which agent runs, with which prompt, is the
  caller's business (a person, a campaign runner, a CI job), expressed as
  the workload command and its environment.
- **Interactive exec, shell or port forwarding through the hub.** Not
  planned. A person who needs a shell in a local sandbox uses `podman exec`
  on the gateway host.
- **Persistent sandbox storage.** A sandbox's filesystem dies with it. Work
  survives only by being pushed to the hub.
- **Warm pools, snapshots, pause/resume.** Gateways may add them behind the
  capability report later.
- **Network egress enforcement.** The configuration carries a network policy
  so that documents are forward-compatible, but no gateway in this PRD
  enforces `restricted`. Enforcement is planned through
  [NVIDIA OpenShell](https://github.com/NVIDIA/OpenShell) (L7 egress
  filtering, credential proxying) as a future provider or gateway; until
  then the hub never silently downgrades a `restricted` request.
- **A Kubernetes gateway implementation.** The `pod` provider and the remote
  gateway protocol are specified so that one can be written; shipping it is
  a separate PRD. The same holds for the `microvm` provider on hosts without
  libkrun.
- **Sandbox-to-sandbox communication** and multi-container sandboxes.
- **Hard caps on the number of sandboxes.** The hub does not limit how many
  sandboxes a workspace, user or org may have. Capacity is the gateway's to
  refuse (`ErrCapacity`).
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
- The durable job queue, the audit emitter, agent sessions, audit event
  ingestion and Prometheus metrics are all in place and are the integration
  points this PRD uses.
- The hub is a single process with an HTTP listener. There is no message
  broker.

## Relationship to PRD 15

| Topic | PRD 15 (`prd15.md`) | This PRD |
|---|---|---|
| Cardinality | One sandbox per workspace, provisioned automatically when the clone is ready | Any number per workspace, launched on request, each pinned to a revision |
| Lifetime | Long-lived; stopped on archive | Ephemeral; stopped on request, when the workload exits, on timeout, on lost liveness, on archive/delete |
| Code inside the sandbox | The hub's workspace clone bind-mounted at `/workspace` | The outpost clones from the hub's git server at the pinned revision; no host mounts |
| Secrets | Container environment variables set by the provider | Fetched by the outpost over HTTPS after boot; never in container metadata |
| Control channel | `podman exec`, file read/write through the provider | NATS request/reply with the outpost; the provider only launches, inspects and stops |
| Runtime abstraction | Provider interface with podman bindings in-process | Gateway interface (local in-process, remote over NATS) × provider (`container`, `microvm`, `pod`) |
| Configuration | Hub-level defaults with per-org/per-workspace overrides in the DB | One `sandbox.json` document: hub default file, optional workspace replacement via API, optional hub-level ceilings |
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
  (`afc outpost run`). It bootstraps the sandbox from the hub, connects to
  the hub's NATS server, starts the workload, executes commands and reports
  liveness.
- **Workload.** The command the outpost starts after bootstrap, from the
  sandbox configuration: an agent, a script, or nothing.
- **Collector.** The outpost's local endpoint inside the sandbox where
  agents and tools hand over telemetry (events, token usage, metrics,
  logs). The outpost batches it and forwards it to the hub over NATS, so
  nothing but the outpost ever holds NATS credentials.
- **Bootstrap document.** What the outpost fetches from the hub with the
  sandbox token: workspace, branch, revision, clone URL, NATS credentials,
  workload command, environment (plain values, variables and secrets).
- **Sandbox configuration.** The JSON document (`sandbox.json`) that says
  which image, provider, gateway, resources, network policy, environment,
  secrets, workload and timeouts a sandbox is launched with.
- **Sandbox token.** The hub credential injected into the sandbox. It is
  valid for exactly one workspace and one sandbox and dies with the sandbox.

## Functional Requirements

### Sandbox configuration

- **CFG-1.** The hub SHALL read a default sandbox configuration from
  `$XDG_CONFIG_HOME/sandbox.json` (next to `config.toml`), overridable with
  `[sandbox] config_path` in `config.toml`. If the file is missing the hub
  SHALL use a built-in default (image `quay.io/agentfox/agents:latest`,
  provider `container`, gateway `local`, 2 CPU, 4 GiB, 512 pids, egress
  `all`, no workload, boot 5m, max lifetime 8h) and log that it did so. A
  file that exists but does not validate SHALL abort startup.
- **CFG-2.** The document schema, version 1:

  ```json
  {
    "version": 1,
    "provider": "container",
    "gateway": "local",
    "image": "quay.io/agentfox/agents:latest",
    "workdir": "/opt/app-root/workspace",
    "command": ["claude", "-p", "Implement the spec in .specs/"],
    "on_exit": "stop",
    "resources": { "cpu": "2", "memory": "4Gi", "pids": 512, "disk": "10Gi" },
    "network": { "egress": "all", "allow": [] },
    "env": { "AGENT_LOG_LEVEL": "info" },
    "vars": "all",
    "secrets": ["ANTHROPIC_API_KEY"],
    "timeouts": { "boot": "5m", "max": "8h" }
  }
  ```

  `provider` is one of `container`, `microvm`, `pod`. `gateway` names the
  built-in `local` gateway or a gateway registered in `config.toml`.
  `workdir` defaults to the image's working directory. `command` is the
  workload the outpost starts after bootstrap (OP-6); when absent the
  outpost idles and the sandbox stays up until stopped. `on_exit` is what
  happens when the workload exits: `stop` (default, graceful stop with a
  final push) or `keep`. `cpu` and `memory` use Kubernetes quantity syntax;
  `disk` is advisory for gateways that can enforce it. `network.egress` is
  `all` or `restricted`; with `restricted`, `allow` lists `host[:port]`
  entries, and the hub's own endpoints are always implicitly allowed. `vars`
  is `"all"` or a list of variable keys to take from the workspace's
  resolved variables. `secrets` is a list of secret keys resolved workspace
  > org > user, the same order as variables. `env` holds plain values.
  Timeouts use Go duration syntax.
- **CFG-3.** A workspace MAY store its own sandbox configuration through
  `PUT /api/v1/workspaces/:slug/sandbox-config`. When present it SHALL
  replace the default document entirely; there is no field-level merge, and
  the document is not read from the repository. `GET` returns the effective
  document with `"origin": "workspace"` or `"origin": "default"`; `DELETE`
  removes the workspace document.
- **CFG-4.** Optionally, `config.toml` MAY set ceilings that apply
  regardless of which document is in force: `[sandbox.limits] max_cpu`,
  `max_memory`, `max_pids`, `max_lifetime`, `allowed_images` (glob list),
  `allowed_providers`, `allowed_gateways`. None is set by default. A
  configuration that exceeds a set ceiling is rejected at `PUT` time with
  `400 sandbox_config_invalid` naming the field, and again at launch time if
  the ceilings changed since.
- **CFG-5.** Keys in `env` SHALL NOT start with `AF_`; `vars` and `secrets`
  keys that collide with a reserved `AF_*` name are dropped with a warning
  recorded on the launch. A `secrets` key that does not resolve at launch
  SHALL fail the launch with `secret_not_found` naming the key (never launch
  with a silently missing credential).
- **CFG-6.** The launch request MAY override `image`, `resources`, `env`,
  `command`, `on_exit` and `timeouts` for that one sandbox, subject to
  CFG-4. It MAY NOT override `secrets`, `vars`, `provider`, `gateway` or
  `network`.
- **CFG-7.** The hub SHALL store the configuration it actually launched with
  in the sandbox record, with secret values replaced by their key names, so
  that a record is self-describing after the workspace document changes.

### Lifecycle

- **LC-1.** `POST /api/v1/workspaces/:slug/sandboxes` SHALL create a sandbox
  record in status `pending` and enqueue a `sandbox_launch` job keyed by the
  sandbox id. The request body is
  `{ "branch": "...", "ref": "...", "ttl": "...", "metadata": {}, "overrides": {} }`.
  `branch` defaults to the workspace branch; the sandbox works directly on
  it. `ttl` caps `timeouts.max` for this sandbox.
- **LC-2.** Before enqueueing, under the workspace lock, the hub SHALL
  resolve the tip of `branch` in the workspace trunk and store it as
  `revision`. If `branch` does not exist it SHALL be created at `ref`
  (default: the workspace branch tip); passing `ref` for a branch that
  already exists is `400 ref_conflict`. A `ref` that does not resolve is
  `404 ref_not_found`.
- **LC-3.** The workspace SHALL be `active` with `clone_status = ready`;
  otherwise `409 workspace_not_ready`. There is no cap on concurrent
  sandboxes; a gateway that is out of capacity fails the launch with
  `ErrCapacity`, which surfaces as `failed` / `capacity` on the record.
- **LC-4.** Statuses and transitions:

  | From | To | When |
  |---|---|---|
  | `pending` | `starting` | the launch job is picked up |
  | `starting` | `booting` | the gateway reports the sandbox running |
  | `starting` | `failed` | the gateway cannot launch (image pull, capacity, unsupported provider or policy) |
  | `booting` | `ready` | the outpost's `ready` event arrives |
  | `booting` | `failed` | no `ready` within `timeouts.boot` (`exit_reason: boot_timeout`), or the outpost reports `boot_failed` |
  | `ready` | `stopping` | stop request, workload exit with `on_exit: stop`, max lifetime, workspace archive/delete |
  | `ready` | `failed` | three consecutive missed heartbeats and the gateway reports the sandbox gone (`exit_reason: lost`), or the gateway reports an unexpected container exit (`exit_reason: exited`, with exit code) |
  | `stopping` | `stopped` | the gateway confirms the sandbox is gone |
  | `stopping` | `failed` | the gateway cannot stop it after the grace period plus force |

  `stopped` and `failed` are terminal. Every terminal transition records
  `ended_at` and `exit_reason`.
- **LC-5.** Stopping SHALL be graceful: the hub sends `shutdown` over NATS
  with the requested grace (default 30s) and `push: true` unless the caller
  says otherwise; waits for the outpost's `exiting` reply or the grace
  period; then asks the gateway to stop, which sends SIGTERM and then
  SIGKILL after 10s. A sandbox whose outpost never connected skips the
  NATS step.
- **LC-6.** Workspace archive and delete SHALL stop every non-terminal
  sandbox of the workspace with `push: false` before proceeding, and SHALL
  wait for them to reach a terminal status (bounded by the grace period plus
  force). Reactivate and reclone do not launch anything.
- **LC-7.** Time limits: `timeouts.boot` (LC-4), `timeouts.max` (since
  `started_at`) and the launch `ttl` cause a graceful stop with the matching
  `exit_reason`. There is no idle timeout; liveness comes from heartbeats
  (OP-7), and a sandbox with a live outpost stays up until its workload
  exits, it is stopped, or its lifetime ends. Every stop the hub initiates
  on its own uses `push: true`, and the outcome of that push is recorded on
  the record.
- **LC-8.** When the outpost reports `command_exited` and the effective
  `on_exit` is `stop`, the hub SHALL initiate a graceful stop with
  `push: true` and `exit_reason: command_exited`; with `keep` it records the
  exit code and leaves the sandbox running.
- **LC-9.** The record exposed by the API:

  ```json
  {
    "id": "uuid",
    "workspace_slug": "my-project",
    "status": "ready",
    "provider": "container",
    "gateway": "local",
    "image": "quay.io/agentfox/agents:latest",
    "branch": "main",
    "revision": "abc123…",
    "head_sha": "def456…",
    "dirty": false,
    "command_exit_code": null,
    "last_heartbeat_at": "…",
    "stats": { "cpu_pct": 12.5, "mem_bytes": 734003200, "disk_bytes": 91000000, "procs": 14 },
    "created_by": "uuid",
    "created_at": "…", "started_at": "…", "ready_at": "…", "ended_at": null,
    "exit_reason": null, "error": null,
    "final_push": null,
    "metadata": {},
    "config": { "…redacted launch configuration…" }
  }
  ```

  `head_sha`, `dirty` and `stats` are the outpost's last reported state.
  `final_push` records the result of a shutdown-time push (`{ "ok": true,
  "sha": "…" }` or `{ "ok": false, "error": "…" }`). The gateway's handle
  (container id, pod name) is visible to admin tokens only.
- **LC-10.** Sandbox records SHALL be kept after they end, listable with
  `?status=`, and pruned by the existing retention worker after
  `AF_SANDBOX_MAX_AGE_DAYS` (default 30).

### Sandbox token

- **TK-1.** At launch the hub SHALL mint a PAT on behalf of the workspace
  owner with exactly the scopes `git:read`, `git:write`, `sessions:write`,
  `audit:write`, `sandboxes:read`, no expiry at the PAT level, and SHALL
  record a binding `(token_id, sandbox_id, workspace_slug, expires_at)` in
  a hub table. `expires_at` is `started_at + timeouts.max` (or the launch
  `ttl` when shorter).
- **TK-2.** Every hub handler that resolves a workspace from the path, and
  the git server's authorisation, SHALL consult the binding for the
  credential id in `AuthInfo`: a bound credential used against another
  workspace, or after `expires_at`, or after revocation, is rejected with
  `404` (workspace endpoints, git server) or `403 token_scope` (session and
  audit ingestion endpoints), in line with the anti-enumeration policy.
  Session records opened with a bound token SHALL carry the sandbox id in
  their metadata, added by the hub.
- **TK-3.** The binding SHALL be revoked, and the PAT revoked best-effort,
  when the sandbox reaches a terminal status. A bound token SHALL NOT be
  able to create PATs, list tokens, or use the secrets endpoints,
  regardless of what the underlying PAT could do. Its only route to secret
  values is the bootstrap document (BS-1), which contains exactly the
  secrets its configuration names.
- **TK-4.** The plaintext token SHALL exist only in the launch job's
  in-memory payload and the sandbox's environment. It SHALL NOT be written
  to the job's persisted payload, the sandbox record, logs or audit events.

### Container environment and bootstrap document

- **ENV-1.** The gateway SHALL set exactly three environment variables on
  the sandbox and nothing else of the hub's:

  | Variable | Value |
  |---|---|
  | `AF_HUB_URL` | HTTP base URL of the hub as reachable from the gateway's network |
  | `AF_HUB_TOKEN` | the sandbox token |
  | `AF_SANDBOX_ID` | sandbox id |

  Variables, secrets, the workload command, NATS credentials and everything
  else arrive through the bootstrap document. Container metadata on the
  gateway host therefore never carries a secret other than the token.
- **ENV-2.** `AF_HUB_URL` SHALL be computed per gateway: the local gateway
  uses `[sandbox.local] hub_url` when set (for example
  `http://host.containers.internal:8080` when the hub runs on the host) and
  falls back to `[server] external_url`; a remote gateway uses the
  `hub_url` in its `[[sandbox.gateways]]` entry with the same fallback. A
  launch with no usable URL fails with `hub_unreachable_config` rather than
  launching a sandbox that cannot phone home.
- **BS-1.** `GET /api/v1/sandboxes/:id/bootstrap` SHALL return, to the
  sandbox token bound to `:id` and to nobody else, while the sandbox is
  non-terminal:

  ```json
  {
    "sandbox_id": "uuid",
    "workspace": "my-project",
    "git_url": "https://hub.example.com/git/org/my-project.git",
    "branch": "main",
    "revision": "abc123…",
    "workdir": "/opt/app-root/workspace",
    "command": ["claude", "-p", "…"],
    "on_exit": "stop",
    "nats": { "url": "wss://hub.example.com/nats", "user": "sbx-uuid", "password": "…" },
    "env": { "AGENT_LOG_LEVEL": "info", "MY_VAR": "…", "ANTHROPIC_API_KEY": "…" },
    "secret_keys": ["ANTHROPIC_API_KEY"],
    "timeouts": { "boot": "5m", "max": "8h" }
  }
  ```

  `env` is the merge of the configuration's `env`, the selected variables
  and the named secrets, with secrets overriding variables overriding
  `env`; `secret_keys` tells the outpost which values must never be logged.
  `nats.url` is computed per gateway like `AF_HUB_URL` (from
  `[sandbox.local] nats_url`, the gateway entry, or `[nats] external_url`).
- **BS-2.** The bootstrap document SHALL be fetchable exactly once. The
  first successful response marks the sandbox `bootstrapped`; every later
  request is `404`, as is a request against a terminal sandbox or with any
  credential other than the bound token. Each fetch, successful or refused,
  SHALL emit `hub.sandbox.bootstrap` (actor: the sandbox) with the outcome,
  so that a second attempt from an unexpected place is visible. An outpost
  that loses the response after the hub sent it cannot recover: it reports
  `boot_failed`, the sandbox fails, and the caller launches a new one.
  Configuration changes after launch are never picked up by a running
  sandbox other than through `refresh` (RF-1); anything else means
  re-creating the sandbox.
- **BS-3.** Secret values SHALL be resolved at fetch time, not at launch
  time, so the document reflects the current secret when the outpost
  boots; the set of keys is fixed at launch (CFG-7).

### Outpost

- **OP-1.** `afc outpost run` SHALL be the sandbox entrypoint. It reads
  `AF_HUB_URL`, `AF_HUB_TOKEN` and `AF_SANDBOX_ID`, fails fast with a clear
  message if any is missing, and never reads `~/.af/config.toml` for its own
  credentials.
- **OP-2.** Bootstrap: fetch BS-1 (retrying with backoff for up to
  `timeouts.boot`); write `~/.af/config.toml` with `endpoint_url` and
  `api_key = AF_HUB_TOKEN` (mode 0600) so that `afc` and the existing
  credential helper work for the workload and for git; configure
  `credential.<AF_HUB_URL>.helper = "!afc credential-helper"` globally;
  clone `git_url` into `workdir` with `--branch <branch>`; if the branch tip
  is no longer `revision` (a push landed between launch and boot, which is
  normal when sandboxes share the workspace branch) proceed on the new tip
  and report both SHAs in `ready`; set a commit identity from the sandbox
  id unless `GIT_AUTHOR_*` arrives in `env`.
- **OP-3.** Connect to `nats.url` with the bootstrap credentials, with
  unlimited reconnect and exponential backoff capped at 30s. Publish `ready`
  once the clone is in place and the subscription to the command subject is
  live. A bootstrap failure before `ready` SHALL be published as
  `boot_failed` with the error when NATS is reachable, then the process
  exits non-zero.
- **OP-4.** Commands (MSG-4) are handled one at a time in arrival order.
  `sync` with `direction: pull` runs `git fetch origin` and then
  `git checkout -B <branch> <ref>` (default `origin/<branch>`); if the
  working tree is dirty it replies `dirty_worktree` unless `force: true`, in
  which case it discards local changes. `sync` with `direction: push` runs
  `git push origin <branch>` (`--force-with-lease` when `force: true`) and
  replies with the pushed SHA, or `non_fast_forward` / the git error.
  `shutdown` performs the push first when `push: true`, sends SIGTERM to the
  workload and waits up to the grace period, replies `exiting` with the push
  result, and exits 0. `ping` replies with the heartbeat block of OP-7.
- **OP-5.** The outpost does nothing else with the working tree: no
  automatic commits, no automatic pushes except at shutdown when asked.
  Whatever runs inside the sandbox commits and pushes through the normal git
  remote; the hub's post-push hooks fire as for any other push. Because
  sandboxes work on the workspace branch by default, two sandboxes on the
  same branch race at push time exactly as two developers would; the
  `non_fast_forward` reply and `force: true` are the tools for it, and a
  caller who wants isolation passes a different `branch` and merges through
  the merge queue.
- **OP-6.** After `ready`, when `command` is set, the outpost SHALL start it
  as a child process in `workdir` with the process environment plus the
  bootstrap `env` plus `AF_HUB_URL`, `AF_HUB_TOKEN`, `AF_SANDBOX_ID`,
  `AF_WORKSPACE`, `AF_WORKSPACE_BRANCH`, `AF_REVISION` and
  `AF_COLLECTOR_URL` (OP-10); forward its stdout and stderr to the
  container's own stdout and stderr, and nowhere else; and publish
  `command_exited` with the exit code when it ends. Without `command` the
  outpost idles. The NATS credentials SHALL NOT be passed to the workload.
  The outpost SHALL NOT restart the workload on its own; `on_exit` decides
  what the hub does next (LC-8), and `refresh` (RF-2) is the one command
  that may restart it.
- **OP-7.** Every 30s the outpost publishes `heartbeat`: an "I'm alive" with
  `{ "head_sha", "dirty", "uptime_s", "command_running", "stats": { "cpu_pct",
  "mem_bytes", "disk_bytes", "procs" } }`, where `stats` are read from the
  container's cgroup and `workdir`. The hub stores the latest heartbeat on
  the record (LC-9) and uses missed heartbeats for liveness (LC-4).
- **OP-8.** The outpost SHALL treat SIGTERM like a `shutdown` with
  `push: false` and a 10s budget, so that a gateway-initiated stop still
  produces an `exiting` event when NATS is reachable.
- **OP-9.** `afc outpost emit --type <type> [--json <payload> | -]` SHALL
  hand a telemetry record (TEL-1) to the collector at `AF_COLLECTOR_URL`,
  for shell scripts and agents without an HTTP client of their own.
- **OP-10.** The outpost SHALL run a **collector**: an HTTP listener bound
  to loopback inside the sandbox (`AF_COLLECTOR_URL`, default
  `http://127.0.0.1:9911`) accepting `POST /v1/telemetry` with one record
  or a JSON array of records in the TEL-1 shape. The collector validates
  the shape, stamps `sandbox_id` and receipt time, buffers up to 1000
  records or 5s, and publishes batches on `af.sbx.<id>.tel.<type>`. When
  NATS is disconnected it buffers up to 10 MB and then drops the oldest,
  counting drops in the next heartbeat (`telemetry_dropped`). It answers
  `202` immediately; delivery is best-effort.
- **RF-1.** `refresh` (hub → outpost) SHALL re-deliver a rotated
  environment to a long-running sandbox. `POST …/sandboxes/:id/refresh`
  arms exactly one additional bootstrap fetch (a nonce with a 60s life,
  recorded on the record), then sends the `refresh` command with the
  nonce. The outpost fetches `GET /api/v1/sandboxes/:id/bootstrap?nonce=…`
  over HTTPS (secrets never travel over NATS), replaces its copy of `env`,
  `secret_keys` and `timeouts`, ignores structural fields (`git_url`,
  `branch`, `command`, `nats`), and replies with the set of keys whose
  values changed. Only `env`, `vars` and `secrets` are refreshable;
  anything else requires a new sandbox.
- **RF-2.** `refresh` carries `restart_command: bool` (default `false`).
  When `true` the outpost sends SIGTERM to the workload, waits up to the
  grace period, then starts `command` again with the new environment, and
  publishes `command_exited` followed by a heartbeat with
  `command_running: true`. When `false` the new values apply only to
  processes started after the refresh (for example a workload that re-reads
  its environment, or the next `refresh` with a restart).

### Hub ↔ sandbox messaging

- **MSG-1.** The hub SHALL embed `nats-server` (`github.com/nats-io/nats-server/v2`)
  in the `hub` process, started before the HTTP server and stopped after it.
  Configuration in `config.toml`:

  ```toml
  [nats]
  enabled      = true
  port         = 4222         # plain NATS TCP for clients on the hub's own network; 0 disables
  bind         = "0.0.0.0"
  external_url = ""           # default: [server] external_url with ws/wss scheme and path /nats
  ```

  The embedded server SHALL always run a WebSocket listener bound to
  loopback, and the hub SHALL reverse-proxy `GET /nats` (WebSocket upgrade)
  on its HTTP listener to it. Outposts and remote gateways therefore reach
  NATS through the same host, port and TLS termination as the REST API
  (`wss://hub.example.com/nats`), which is what passes firewalls and the
  existing Kubernetes Route; the TCP port is a convenience for in-network
  clients. With `enabled = false` the sandbox subsystem is off and its
  endpoints return `404`.
- **MSG-2.** Subjects:

  | Subject | Direction | Pattern |
  |---|---|---|
  | `af.sbx.<id>.cmd` | hub → outpost | request/reply |
  | `af.sbx.<id>.evt.<type>` | outpost → hub | publish |
  | `af.sbx.<id>.tel.<type>` | outpost collector → hub | publish (TEL-1) |
  | `af.gw.<name>.cmd` | hub → remote gateway | request/reply |
  | `af.gw.<name>.evt.<type>` | remote gateway → hub | publish |

- **MSG-3.** Each sandbox SHALL get its own NATS user (`sbx-<id>`, random
  password) with permissions: subscribe `af.sbx.<id>.cmd`, publish
  `af.sbx.<id>.evt.>` and `af.sbx.<id>.tel.>`, and `allow_responses` so it
  can answer requests without a blanket `_INBOX.>` grant. The credentials
  are delivered in the bootstrap document, never in container metadata.
  Users are added at launch and removed at terminal status through the
  embedded server's options reload. Remote gateways get the same treatment
  on `af.gw.<name>.>` with a static token from `config.toml`. The hub's own
  connection is an in-process client with full permissions; nothing else
  can connect without credentials.
- **MSG-4.** Every message is a JSON envelope
  `{ "id": "uuid", "type": "sync", "sent_at": "…", "sandbox_id": "…", "payload": {} }`.
  Replies reuse `id` and carry `{ "ok": true, "payload": {} }` or
  `{ "ok": false, "error": { "code": "dirty_worktree", "message": "…" } }`.
  Command types in this PRD: `ping`, `sync`, `shutdown`, `refresh`. Event
  types:
  `ready`, `boot_failed`, `heartbeat`, `command_exited`, `exiting`. Unknown
  command types are answered with `unsupported`; unknown event types are
  logged and dropped. A command that receives no reply within its timeout
  (`ping` 5s, `sync` 10m, `shutdown` grace + 5s) is reported to the caller
  as `timeout`.
- **MSG-5.** The hub SHALL consume events on `af.sbx.>` from one
  subscription and apply them to records idempotently, keyed by envelope
  id, so that a redelivered or reordered event cannot regress a status.

### Telemetry from inside the sandbox

- **TEL-1.** Agents and tools running in a sandbox report telemetry to the
  outpost's collector (OP-10), never to the hub directly. Each record is
  `{ "type": "...", "at": "...", "payload": {} }` with these types:

  | Type | Payload | Stored as |
  |---|---|---|
  | `event` | an agent audit event in the format `POST /workspaces/:slug/runs/:run_id/events` accepts | agent audit event in the audit store, workspace and sandbox id attached by the hub |
  | `usage` | `{ "session_id", "model", "input_tokens", "output_tokens", … }` as `POST /sessions/:id/usage` accepts | token usage row |
  | `metrics` | `{ "name", "value", "labels": {} }` samples | persisted in the audit store as sandbox metric samples, and the latest value per name kept on the sandbox record next to `stats` |
  | `log` | `{ "level", "message", "fields": {} }` | persisted in the audit store alongside agent events, queryable through the unified audit query with `sandbox_id` as a filter |

  The hub SHALL ingest `event` and `usage` through the same validation and
  storage paths as their HTTP counterparts, attributing them to the sandbox
  token's identity, so that an agent can report through the collector or
  over HTTPS interchangeably. Malformed records are counted
  (`af_sandbox_telemetry_rejected_total{type}`) and dropped. Persisted
  telemetry follows the existing audit retention settings.
- **TEL-2.** Telemetry SHALL NOT influence lifecycle; only `evt` messages
  from the outpost do.
- **TEL-3.** Only what an agent deliberately hands to the collector reaches
  the hub. The outpost SHALL NOT capture, tail or stream the workload's
  stdout or stderr, files in `workdir`, or shell history as telemetry;
  those stay in the container and are the gateway host's to inspect.

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

  `LaunchSpec` carries the sandbox id, provider, image, workdir, resources,
  network policy, the three bootstrap variables (ENV-1) and labels
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
- **LG-2.** `container` provider: `podman run --detach --name sbx-<id>
  --label … --cpus … --memory … --pids-limit … --cap-drop ALL
  --security-opt no-new-privileges --read-only --tmpfs /tmp --user 1001 …`
  with the working directory and `$HOME` as anonymous volumes so they are
  writable while the root filesystem is not. The three environment
  variables are passed with `--env-file` from a 0600 temp file deleted
  after `run` returns, so the token never appears on a command line.
- **LG-3.** `microvm` provider: the same invocation with
  `--runtime krun` (libkrun). `Capabilities` reports `microvm` only when
  `podman info` lists that runtime.
- **LG-4.** Network policy: `all` uses podman's default network.
  `restricted` is reported as unsupported by the local gateway; enforcement
  arrives with the OpenShell integration (Non-goals).
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
  nats_url = "wss://hub.example.com/nats"
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
  later PRD, the Kubernetes one; it connects out to the hub's NATS WebSocket
  endpoint, so a remote host needs no inbound ports. Sandboxes it launches
  reach the hub through the URLs in the gateway's entry (ENV-2, BS-1).

### HTTP API

- **API-1.** Endpoints, all requiring workspace ownership (admin bypasses):

  | Method and path | Scope | Effect |
  |---|---|---|
  | `POST /api/v1/workspaces/:slug/sandboxes` | `sandboxes:write` | launch (LC-1), `202` with the record |
  | `GET /api/v1/workspaces/:slug/sandboxes` | `sandboxes:read` | list, `?status=` filter |
  | `GET /api/v1/workspaces/:slug/sandboxes/:id` | `sandboxes:read` | record |
  | `POST /api/v1/workspaces/:slug/sandboxes/:id/sync` | `sandboxes:write` | `sync` command; `200` with the reply, or `409 sandbox_not_ready` |
  | `DELETE /api/v1/workspaces/:slug/sandboxes/:id` | `sandboxes:write` | graceful stop, body `{ "push": bool, "grace": "30s" }`, `202` |
  | `POST /api/v1/workspaces/:slug/sandboxes/:id/refresh` | `sandboxes:write` | `refresh` command (RF-1), body `{ "restart_command": bool }`; `200` with the changed keys, or `409 sandbox_not_ready` |
  | `GET/PUT/DELETE /api/v1/workspaces/:slug/sandbox-config` | `sandboxes:read` / `sandboxes:write` | CFG-3 |
  | `GET /api/v1/sandboxes/:id/bootstrap` | bound sandbox token only | BS-1, once (BS-2) or with a `refresh` nonce (RF-1) |
  | `GET /api/v1/sandboxes` | admin | all sandboxes across workspaces |
  | `GET /api/v1/gateways` | admin | gateways, connection state, capabilities |

  `sandboxes:write` implies `sandboxes:read`. Error envelopes and `404`
  anti-enumeration follow `docs/api.md`.
- **API-2.** `GET …/sandboxes/:id` SHALL support ETags via apikit so that
  `afc sandbox get --wait` can poll cheaply.
- **API-3.** A bound sandbox token (TK-1) may call `GET …/sandboxes/:id`
  for its own sandbox only, so a process inside can learn its own status.

### CLI

- **CLI-1.** `afc sandbox launch <slug> [--branch] [--ref] [--ttl]
  [--image] [--cpu] [--memory] [--env K=V]... [--command "..."] [--wait]`,
  `afc sandbox list <slug> [--status]`, `afc sandbox get <slug> <id>
  [--wait-for ready]`, `afc sandbox sync <slug> <id> --pull|--push [--ref]
  [--force]`, `afc sandbox refresh <slug> <id> [--restart]`,
  `afc sandbox stop <slug> <id> [--no-push] [--grace] [--wait]`,
  `afc sandbox config get|set|delete <slug> [--file sandbox.json]`,
  `afc sandbox gateways` (admin). `--wait` variants exit non-zero on
  `failed`, as `afc rebuild wait` does.
- **CLI-2.** `afc outpost run` (OP-1) and `afc outpost emit` (OP-9). Hidden
  from the top-level help; documented in `docs/cli.md` under a "Sandbox
  internals" heading.

### Security

- **SEC-1.** Sandboxes SHALL never receive host mounts, the hub's data
  directory, or another workspace's credentials. The only way into a
  workspace is the git server with the sandbox token.
- **SEC-2.** Containers run as a non-root user with all capabilities
  dropped, `no-new-privileges`, a read-only root filesystem and a pid limit
  (LG-2). The hub does not claim VM-level isolation from the `container`
  provider; operators who need it choose `microvm` or a `pod` provider with
  a hardened RuntimeClass.
- **SEC-3.** Secrets are never part of container metadata: the gateway host
  sees only the sandbox token, and `podman inspect` reveals nothing an
  attacker could not already get by holding that token for the sandbox's
  lifetime. Secrets live in the outpost's memory and in the workload's
  process environment inside the sandbox. They cross the network exactly
  once per sandbox (BS-2), plus once per hub-initiated `refresh` (RF-1),
  always over HTTPS and never over NATS.
- **SEC-4.** The sandbox token's scopes (TK-1) do not include `secrets:*`,
  `tokens:*`, `workspaces:*`, `vars:*` or `sandboxes:write`. A compromised
  sandbox can push to its workspace, open sessions, ingest audit data, and
  fetch its own bootstrap document once (with exactly the secrets its
  configuration names), nothing else, and only until it is stopped. A
  token stolen after boot cannot fetch the document again unless the hub
  arms a `refresh`, which is audited.
- **SEC-5.** Audit events and logs SHALL redact `AF_HUB_TOKEN`, NATS
  passwords and every value listed in `secret_keys`. The collector SHALL
  apply the same redaction to `log` and `event` records before forwarding
  them, so a secret an agent prints never reaches the hub verbatim.

### Audit, sessions and metrics

- **AU-1.** Events through the existing emitter: `hub.sandbox.launch`
  (actor, workspace, provider, gateway, image, branch, revision),
  `hub.sandbox.bootstrap` (outcome), `hub.sandbox.ready`, `hub.sandbox.sync`
  (direction, result), `hub.sandbox.refresh` (actor, changed keys,
  restarted), `hub.sandbox.command_exited` (code),
  `hub.sandbox.stop` (reason, final push result), `hub.sandbox.failed`
  (reason, error). Timeouts and reconciliation use actor `system`.
- **AU-2.** Metrics: `af_sandboxes{gateway,provider,status}` gauge,
  `af_sandbox_launch_seconds` histogram (`pending` to `ready`),
  `af_sandbox_ended_total{exit_reason}`, `af_nats_connections`,
  `af_sandbox_metric{…}` (TEL-1), `af_sandbox_telemetry_rejected_total{type}`.
- **AU-3.** Sessions are opened by whatever runs inside the sandbox, not by
  the hub at launch. Sessions and usage reported with a sandbox token, over
  HTTPS or NATS, carry `metadata.sandbox_id` (TK-2), so
  `GET /workspaces/:slug/cost` can be broken down per sandbox later without
  a schema change.

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

- Unit: configuration validation and ceilings (CFG); status machine (LC-4,
  LC-8) with a fake gateway and injected events; token binding checks (TK)
  in the workspace and git server authz tests; bootstrap document assembly,
  single-fetch and nonce handling (BS, RF); envelope encode/decode; the
  outpost command handlers, workload supervision and refresh against a
  temporary git repository; collector batching, buffering and redaction
  (OP-10, SEC-5); telemetry ingestion parity with the HTTP endpoints
  (TEL-1).
- Integration: an embedded `nats-server` on a random port in tests, reached
  both over TCP and through the `/nats` WebSocket proxy; the outpost run as
  a goroutine against a test hub with the existing git server test helpers,
  covering bootstrap (and its refusal on a second fetch), `ready`, `sync`
  both ways, `dirty_worktree`, `non_fast_forward`, workload exit with
  `on_exit: stop`, `refresh` with and without restart, telemetry from a
  workload through the collector to the audit store, `shutdown` with push,
  reconnect after the test server restarts.
- Manual/CI: `make hub-run` plus a local podman launching
  `quay.io/agentfox/agents` with a workload that commits and pushes, as the
  acceptance check for the local gateway.

## Technical Boundaries

- Go, in the existing `hub` and `afc` binaries plus a new `af-gateway`
  binary. New packages: `internal/sandbox` (records, config, lifecycle,
  bootstrap, API), `internal/sandbox/gateway` (interface, local podman
  implementation, NATS remote client), `internal/outpost` (used by `afc`),
  `internal/nats` (embedded server, WebSocket proxy, hub client, telemetry
  ingestion).
- New dependencies: `github.com/nats-io/nats-server/v2` and
  `github.com/nats-io/nats.go`. No podman Go bindings; the CLI is driven as
  a subprocess like git.
- State in the existing SQLite database: `sandboxes`, `sandbox_tokens`,
  `workspace_sandbox_configs`. Sandbox records use the durable job queue
  for `sandbox_launch` and `sandbox_stop`, grouped by sandbox id.
- Documentation to update when implementing: `docs/api.md`, `docs/cli.md`,
  `docs/configuration.md` (`[sandbox]`, `[nats]`, `AF_SANDBOX_MAX_AGE_DAYS`),
  `docs/permissions.md`, `README.md` (third binary, `/nats` endpoint),
  `deploy/` (nothing new to expose: NATS rides the existing Route),
  `containers/*/Containerfile`.

## Dependencies

- `../apikit`: PAT minting on behalf of a user from server code, credential
  id on `AuthInfo`, ETag helpers. Whether apikit exposes a server-side
  "create PAT for user" call, and whether it accepts sub-day expiries, could
  not be verified from this checkout (the sibling repository is not
  present); TK-1 is written so that neither is required.
- `internal/workspace` (lock, trunk rev-parse, archive/delete hooks),
  `internal/gitserver` (authz binding check), `internal/secrets` (resolved
  variables and secret values), `internal/jobqueue`, `internal/audit`
  (event and usage ingestion reused by TEL-1).
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

### 3. Three variables in, everything else fetched

Only what the outpost needs to reach the hub goes into the container
(`AF_HUB_URL`, `AF_HUB_TOKEN`, `AF_SANDBOX_ID`). Secrets, variables, NATS
credentials and the workload command come from the bootstrap document over
HTTPS, exactly once. This keeps secrets out of `podman inspect`, out of
remote gateway hosts, out of the launch job's payload and out of the NATS
protocol; a gateway is trusted with a token whose blast radius is one
sandbox, not with an API key for an LLM provider. Single fetch means a
token captured after boot is worth a git push and some audit rows, not the
secrets; the price is that anything structural that changes after launch
means a new sandbox, which is what "ephemeral" was supposed to mean anyway.
The cost is also that the outpost, not the container runtime, has to start
the workload with that environment, which is why the outpost supervises
the workload (OP-6).

### 4. The outpost supervises the workload

A consequence of decision 3. The outpost starts `command` as a child,
forwards signals and output, and reports the exit. It does not restart the
workload; `on_exit` lets the hub decide between stopping the sandbox (the
default for agent runs) and keeping it (for a sandbox someone will
`podman exec` into). This also gives the hub a clean "the agent finished"
signal that neither container exit codes nor heartbeats provide.

### 5. NATS over WebSocket on the hub's own endpoint

NATS' native TCP port is one more thing to open in firewalls, Routes and
ingress controllers, and PRD 15's deployment manifests only route HTTP.
Serving the embedded server's WebSocket listener at `/nats` behind the
hub's existing listener means one hostname, one port, one TLS certificate,
and remote gateways that dial out. The TCP port stays available for
clients on the hub's own network.

### 6. NATS as the only control channel, including for remote gateways

Using NATS for remote gateways as well as outposts means remote hosts dial
out and need no inbound ports, the hub needs one listener, and the same
envelope and auth model cover both. The alternative, an HTTP API on each
gateway, would need TLS and credentials per host and reachability from the
hub.

### 7. Per-sandbox NATS users through options reload

*Recommendation, still open (Open Question 1).* Embedded `nats-server`
supports per-user publish and subscribe permissions and `allow_responses`,
and `ReloadOptions` adds and removes users at runtime. It is the simplest
thing that gives each sandbox a credential that can only touch its own
subjects. Auth callout (the hub validating the sandbox token itself when
NATS asks) is cleaner in the long run, since one credential would serve
both HTTPS and NATS, but needs NKey signing and a callout service.

### 8. Token binding in the hub instead of a new credential type

apikit owns credentials, and the steering rules forbid custom credential
resolution. A PAT minted for the workspace owner plus a hub-side binding to
one sandbox gives a workspace-scoped, sandbox-scoped, short-lived token
without an apikit change; if apikit later grows resource-bound PATs, the
binding table folds into it.

### 9. The podman CLI, not the Go bindings

The hub already runs git as a hardened subprocess. The podman bindings
module is large, and the CLI is what operators debug with (`podman ps`
shows exactly what the hub started). `af-gateway` stays a static binary.

### 10. Whole-document replacement, stored on the hub

The workspace document "replaces" the default, as the brief says. A merge
would be friendlier for one-field changes but makes the effective
configuration hard to reason about and audit. Storing it on the hub rather
than in the repository keeps a push from changing the next sandbox's image
or resources and keeps secret names out of the code. Ceilings (CFG-4) are
opt-in for operators who want a floor under that freedom.

### 11. Work on the workspace branch by default

A sandbox is a place to do the workspace's work, so it checks out the
workspace branch and pushes back to it. Isolation per sandbox is one
launch parameter away (`branch`) and the merge queue already exists for
bringing such branches back. Concurrent sandboxes on one branch resolve
their races the way git users do: `non_fast_forward`, pull, retry.

### 12. Liveness by heartbeat, not idleness by inference

An earlier draft inferred idleness from working-tree changes, which
misreads an agent that is thinking or reading. The outpost's heartbeat is
an "I'm alive" with resource stats; lifetime is bounded by `timeouts.max`
and by the workload's own exit. Anything richer (token usage, agent
events, custom metrics, logs) is handed to the outpost's collector and
forwarded on the telemetry subjects, so that the hub can show what a
sandbox is doing without guessing.

### 13. No hard caps

No per-workspace, per-user or hub-wide sandbox count. The gateway is the
authority on capacity and says so with `ErrCapacity`; operators who want a
floor use the optional ceilings on resources.

### 14. Sandboxes outlive the hub process

Stopping every sandbox on hub shutdown (PRD 15's choice) makes a hub upgrade
kill running agent work. Reconciliation on startup plus outpost reconnect is
a small amount of code for a large operational win.

### 15. The outpost is `afc`

A separate binary would need its own build, release and image plumbing. The
outpost is small, needs the same HTTP client and credential helper `afc`
already has, and `afc` is already a static binary.

### 16. The outpost is the collector

Agents could publish to NATS themselves, but then every agent would need
the sandbox's NATS credentials, its own client library and its own
reconnect logic, and a misbehaving agent could flood the hub. A loopback
collector in the outpost gives agents a one-line HTTP call (or
`afc outpost emit`), keeps NATS credentials in one process, and puts
batching, buffering, redaction and back-pressure in one place. The hub
sees one publisher per sandbox.

### 17. Agent-provided telemetry only, no output capture

The hub stores what an agent chose to report: events, usage, metrics and
log lines. It does not tail stdout or stderr. Captured output is noisy,
hard to attribute to an agent action, and the most likely place for a
secret to leak; a person debugging a local sandbox has the container's
own stdout on the gateway host.

### 18. Secret refresh as an explicit, audited, one-shot re-fetch

Long-running sandboxes will outlive a rotated key. Rather than weakening
the single-fetch rule, `refresh` has the hub arm one more fetch with a
short-lived nonce and tell the outpost to take it, over HTTPS, with an
optional workload restart. The same mechanism covers changed variables.
Everything structural still means a new sandbox.

### 19. Network policy waits for OpenShell

Enforcing egress on plain podman means firewall rules on the host or a
proxy sidecar, both partial and both replaced the moment OpenShell lands.
The configuration keeps a `network` block so documents are ready, the
capability report keeps gateways honest, and enforcement is a future
provider or gateway built on OpenShell.

## Open Questions

1. **NATS auth: per-user reload now, auth callout later, or callout from
   the start?** See decision 7.
2. **Collector transport.** OP-10 uses loopback HTTP because every agent
   runtime can `POST` JSON. A Unix socket would stop other processes in a
   shared-network sandbox from reaching it, at the cost of a less
   universal client story. Loopback inside the sandbox's own network
   namespace is proposed as good enough.
3. **Log volume.** `log` telemetry is persisted with the audit retention
   settings. Should the collector rate-limit or cap logs per sandbox (for
   example 10 MB per sandbox lifetime) before the hub has to?
