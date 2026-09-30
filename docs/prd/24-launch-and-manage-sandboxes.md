# Launch and Manage Sandboxes

## Intent

With a NATS server (PRD 19), workspace-bound tokens (PRD 20), a
configuration document (PRD 21), a gateway (PRD 22) and a protocol (PRD 23)
in place, the hub can own the life of a sandbox: create a record, pin a
revision, issue a token and NATS credentials, ask a gateway to launch the
image, hand the outpost its bootstrap document once, drive it with
commands, watch its heartbeats, stop it when its work is done or its time
is up, and keep the record for the audit trail. This PRD specifies that
lifecycle, the REST API and `afc` commands that expose it, and how the hub
recovers after a restart. The outpost side of the same protocol is
`docs/prd/25-build-the-outpost.md`; both implement
`docs/prd/23-define-the-sandbox-control-protocol.md` and can be built in
parallel.

## Goals

- `POST /workspaces/:slug/sandboxes` launches a sandbox on a branch at a
  pinned revision and returns a record the hub keeps current.
- A state machine with well-defined transitions driven by gateway status,
  outpost events, timeouts and requests.
- The bootstrap endpoint that serves each sandbox its document exactly
  once.
- Hub-side handling of `ready`, `heartbeat`, `command_exited`, `exiting`,
  and the `sync` and `shutdown` commands.
- Stop on request, on workload exit (per `on_exit`), on maximum lifetime,
  on lost liveness, and on workspace archive or delete; a final push
  whenever the hub stops a sandbox on its own.
- Reconciliation on hub startup so sandboxes outlive hub restarts.
- Audit events, metrics, retention, API and CLI.

## Non-goals

- The outpost, the collector, telemetry ingestion, `refresh`, remote
  gateways (PRDs 25 to 28).
- Any limit on the number of sandboxes. Capacity is the gateway's.
- Idle detection. Liveness is heartbeats; lifetime is `timeouts.max` and
  the workload's exit.
- Opening agent sessions at launch. Whatever runs inside opens its own.

## Current behaviour (for reference)

- Nothing launches sandboxes. The durable job queue (`internal/jobqueue`)
  runs merge and rebuild jobs with per-key serialisation and crash
  recovery; `workspace.ReconcileStuckSyncs` shows the startup reconciliation
  pattern; the workspace lock (`internal/wslock`) serialises trunk access;
  archive and delete run hooks (`forceclose.go`) that this PRD extends.
- `audit.StartRetentionWorker` runs hourly and prunes by `AF_*_MAX_AGE_DAYS`
  variables.

## Functional Requirements

### Launch

- **LC-1.** `POST /api/v1/workspaces/:slug/sandboxes` with body
  `{ "branch"?, "ref"?, "ttl"?, "metadata"?, "overrides"? }` SHALL:
  require `sandboxes:write` and workspace ownership; require the workspace
  `active` with `clone_status = ready` (`409 workspace_not_ready`); require
  NATS enabled and the configured gateway registered
  (`503 sandbox_unavailable`); resolve the effective configuration (PRD 21)
  and apply `overrides` (only `image`, `resources`, `env`, `command`,
  `on_exit`, `timeouts`; ceilings re-checked; `400` otherwise); create the
  record in `pending`; enqueue `sandbox_launch` keyed by the sandbox id;
  answer `202` with the record.
- **LC-2.** `branch` defaults to the workspace branch. Under the workspace
  lock the hub SHALL resolve the tip of `branch` in the trunk and store it
  as `revision`. If `branch` does not exist it is created at `ref`
  (default: the workspace branch tip); `ref` with an existing branch is
  `400 ref_conflict`; an unresolvable `ref` is `404 ref_not_found`.
- **LC-3.** The launch job SHALL, in order: resolve the environment
  (PRD 21 `ResolveEnvironment`; `secret_not_found` fails the launch); issue
  a workspace-bound token (PRD 20) with scopes `git:read`, `git:write`,
  `sessions:write`, `audit:write`, `sandboxes:read`, subject
  `{ "sandbox", id }`, TTL `timeouts.max` (or `ttl` when shorter); add a
  NATS user `sbx-<id>` (PRD 19) with publish `af.sbx.<id>.evt.>`,
  `af.sbx.<id>.tel.>`, subscribe `af.sbx.<id>.cmd`, `allow_responses`;
  compute `AF_HUB_URL` and the NATS URL for the gateway (LC-4); check the
  gateway's capabilities (`422 gateway_unsupported` on the record for an
  unsupported provider or policy); call `Launch` with the image, resources,
  network, workdir, labels and exactly the three variables of PR-1; store
  the handle; move to `starting` → `booting`. Any failure moves to `failed`
  with the error, revokes the token and removes the NATS user. The
  plaintext token and the NATS password exist only in the job's memory
  and the gateway call; the persisted job payload holds ids only.
- **LC-4.** `AF_HUB_URL` and `nats.url` SHALL be computed per gateway:
  `[sandbox.local] hub_url` / `nats_url` when set (for example
  `http://host.containers.internal:8080` when the hub runs on the host),
  else `[server] external_url` and the NATS external URL (PRD 19). A launch
  with no usable URL fails with `hub_unreachable_config` before calling
  the gateway.

### State machine

- **LC-5.**

  | From | To | When |
  |---|---|---|
  | `pending` | `starting` | launch job picked up |
  | `starting` | `booting` | gateway `Launch` returned a handle |
  | `starting` | `failed` | launch job failed (reason on the record) |
  | `booting` | `ready` | `ready` event |
  | `booting` | `failed` | `boot_failed` event, or no `ready` within `timeouts.boot` (`boot_timeout`) |
  | `ready` | `stopping` | stop request, `command_exited` with `on_exit: stop`, `timeouts.max`, workspace archive/delete |
  | `ready` | `failed` | three consecutive missed heartbeats and `Inspect` says `gone` (`lost`); `Inspect` says `exited` (`exited`, exit code) |
  | `stopping` | `stopped` | gateway `Inspect` says `gone` after `Stop` |
  | `stopping` | `failed` | `Stop` failed after grace plus force |

  `stopped` and `failed` are terminal: `ended_at` and `exit_reason` are
  set, the token binding is revoked, the NATS user removed.
- **LC-6.** Events SHALL be applied idempotently by envelope id and never
  regress a status (a late `ready` after `stopping` is ignored and
  logged). `heartbeat` updates `head_sha`, `dirty`, `stats`,
  `command_running`, `last_heartbeat_at`. `command_exited` records
  `command_exit_code` and applies `on_exit`. `exiting` records the final
  push result.

### Stopping

- **LC-7.** A stop (request, timeout, `on_exit`, archive, delete) SHALL:
  move to `stopping`; if the outpost ever connected, send `shutdown` with
  the grace (default 30s) and `push` (default `true` for hub-initiated
  stops, `false` for archive/delete, caller's choice on `DELETE`); wait for
  `exiting` or the grace; call gateway `Stop` with a 10s grace; poll
  `Inspect` until `gone` (bounded, then `failed`).
- **LC-8.** Workspace archive and delete hooks SHALL stop every
  non-terminal sandbox of the workspace and wait for terminal states
  before proceeding, so that a sandbox never pushes into a workspace that
  is being archived.
- **LC-9.** `DELETE /api/v1/workspaces/:slug/sandboxes/:id` with body
  `{ "push"?: bool, "grace"?: "30s" }` requests a stop and answers `202`;
  on a terminal sandbox it is `409 sandbox_ended`.

### Commands from the API

- **LC-10.** `POST …/sandboxes/:id/sync` with `{ "direction", "ref"?,
  "force"? }` forwards `sync` over NATS and returns the reply (`200`), or
  `409 sandbox_not_ready` when the status is not `ready`, or
  `504 sandbox_timeout` when the outpost does not answer.

### Liveness, timeouts, reconciliation

- **LC-11.** A sweep every minute SHALL: mark `booting` records past
  `timeouts.boot` as failed; stop `ready` records past `started_at +
  timeouts.max` (or `ttl`); for `ready` records with no heartbeat for 90s,
  `Inspect` and apply LC-5; requeue `stopping` records older than their
  grace plus 60s.
- **LC-12.** On startup, after schema init and before HTTP starts, the hub
  SHALL: for every non-terminal record, re-add its NATS user with the
  password stored on the record, encrypted at rest with
  `AF_SANDBOX_SECRET_KEY` (a new password would need a new bootstrap,
  which single-fetch forbids); if the key is absent or decryption fails,
  the record is failed with `lost`. Then `Inspect` and apply LC-5; then
  `List` every gateway and `Stop` any labelled object without a record.
  The hub SHALL NOT stop sandboxes on its own shutdown.

### Record and API

- **LC-13.** The record:

  ```json
  {
    "id": "uuid", "workspace_slug": "my-project", "status": "ready",
    "provider": "container", "gateway": "local", "image": "…",
    "branch": "main", "revision": "abc…", "head_sha": "def…", "dirty": false,
    "command_running": true, "command_exit_code": null,
    "last_heartbeat_at": "…",
    "stats": { "cpu_pct": 12.5, "mem_bytes": 734003200, "disk_bytes": 91000000, "procs": 14 },
    "created_by": "uuid", "created_at": "…", "started_at": "…", "ready_at": "…",
    "ended_at": null, "exit_reason": null, "error": null, "final_push": null,
    "bootstrapped_at": null, "metadata": {},
    "config": { "…launch configuration, secrets as key names…" }
  }
  ```

  The gateway handle is included for admin tokens only.
- **LC-14.** Endpoints (workspace ownership; admin bypasses):

  | Method and path | Scope | Effect |
  |---|---|---|
  | `POST …/workspaces/:slug/sandboxes` | `sandboxes:write` | LC-1 |
  | `GET …/workspaces/:slug/sandboxes` | `sandboxes:read` | list, `?status=` |
  | `GET …/workspaces/:slug/sandboxes/:id` | `sandboxes:read` | record, ETag |
  | `POST …/workspaces/:slug/sandboxes/:id/sync` | `sandboxes:write` | LC-10 |
  | `DELETE …/workspaces/:slug/sandboxes/:id` | `sandboxes:write` | LC-9 |
  | `GET /api/v1/sandboxes/:id/bootstrap` | bound sandbox token for `:id` | LC-15 |
  | `GET /api/v1/sandboxes` | admin | all sandboxes |

  A sandbox token may `GET` its own record and nothing else here.
- **LC-15.** The bootstrap endpoint SHALL build the PR-2 document from the
  record and the current stores (secrets resolved now), mark
  `bootstrapped_at` in the same transaction it responds from, and answer
  `404` to any later request without a valid refresh nonce (PRD 27), to any
  credential other than the bound token, and to a terminal sandbox. Every
  attempt emits `hub.sandbox.bootstrap` with its outcome.

### CLI

- **LC-16.** `afc sandbox launch <slug> [--branch] [--ref] [--ttl]
  [--image] [--cpu] [--memory] [--env K=V]... [--command "..."] [--wait]`,
  `afc sandbox list <slug> [--status]`, `afc sandbox get <slug> <id>
  [--wait-for ready]`, `afc sandbox sync <slug> <id> --pull|--push [--ref]
  [--force]`, `afc sandbox stop <slug> <id> [--no-push] [--grace] [--wait]`,
  `afc sandbox ls` (admin). `--wait` exits non-zero on `failed`, as
  `afc rebuild wait` does.

### Audit, metrics, retention

- **LC-17.** Events: `hub.sandbox.launch` (actor, workspace, provider,
  gateway, image, branch, revision), `hub.sandbox.bootstrap` (outcome),
  `hub.sandbox.ready`, `hub.sandbox.sync` (direction, result),
  `hub.sandbox.command_exited` (code), `hub.sandbox.stop` (reason, final
  push), `hub.sandbox.failed` (reason, error). Sweeps and reconciliation
  use actor `system`.
- **LC-18.** Metrics: `af_sandboxes{gateway,provider,status}`,
  `af_sandbox_launch_seconds` (pending to ready),
  `af_sandbox_ended_total{exit_reason}`.
- **LC-19.** Ended records are pruned after `AF_SANDBOX_MAX_AGE_DAYS`
  (default 30) by the retention worker.

### Testing

- Unit: state machine with the `FakeGateway` and injected events (every
  row of LC-5, idempotency, late events); launch job failure paths with
  token and NATS user cleanup; URL computation; bootstrap single-fetch and
  access control; sweep rules; reconciliation cases.
- Handler tests in the existing style; an end-to-end test with an embedded
  NATS server and a fake outpost goroutine speaking PRD 23.

## Technical Boundaries

- Go; new package `internal/sandbox` (records, jobs, handlers, hub-side
  protocol client). Tables `sandboxes` and, for LC-12, encrypted NATS
  secrets on the record (`AF_SANDBOX_SECRET_KEY` from the environment).
- Documentation: `docs/api.md`, `docs/cli.md`, `docs/permissions.md`,
  `docs/configuration.md` (`[sandbox.local] hub_url`, `nats_url`,
  `AF_SANDBOX_MAX_AGE_DAYS`, `AF_SANDBOX_SECRET_KEY`).

## Dependencies

- PRD 19 (`AddUser`, `RemoveUser`, `Client`, external URL), PRD 20
  (`Issue`, `Revoke`), PRD 21 (`Effective`, `ResolveEnvironment`, the
  `sandboxes:*` scopes), PRD 22 (`Registry`, `Gateway`), PRD 23 (types and
  subjects).
- `internal/jobqueue`, `internal/wslock`, `internal/workspace` hooks,
  `internal/audit`.

## Design Decisions

### 1. Work on the workspace branch by default

A sandbox is a place to do the workspace's work, so it checks out the
workspace branch and pushes back to it. Isolation per sandbox is one
launch parameter away (`branch`) and the merge queue already exists.

### 2. Liveness by heartbeat, not idleness by inference

An earlier draft inferred idleness from working-tree changes, which
misreads an agent that is thinking. Heartbeats say "alive"; `timeouts.max`
and the workload's exit bound the lifetime.

### 3. Sandboxes outlive the hub process

Stopping every sandbox on hub shutdown makes a hub upgrade kill running
agent work. Reconciliation plus outpost reconnect is a small amount of
code for a large operational win. The price is keeping the NATS password
restorable across restarts (LC-12).

### 4. Single-fetch bootstrap, enforced here

The document is the only place secrets cross the network. Marking
`bootstrapped_at` in the responding transaction makes a stolen token after
boot worth a git push and some audit rows, not the secrets.

### 5. Hub-initiated stops push

A sandbox stopped by a timeout or by `on_exit` may hold unpushed work.
Pushing first, and recording the result, makes "the hub killed my work"
impossible to say without evidence.

## Open Questions

- LC-12 needs the NATS password restorable after a restart. Encrypting it
  at rest with an environment key is the simplest option; the alternative
  is to make the outpost reconnect with a fresh credential fetched over
  HTTPS, which contradicts single-fetch. Which?
