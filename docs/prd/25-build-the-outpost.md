# Build the Outpost

## Intent

Inside every sandbox (`docs/prd/18-run-agents-in-ephemeral-sandboxes.md`)
something has to turn three environment variables into a working
checkout, keep a control channel to the hub open, start the workload, and
answer the hub's commands. That something is the **outpost**. This PRD
builds it as a subcommand of the existing `afc` binary, `afc outpost run`,
and makes it the entrypoint of the sandbox and agents images. It
implements the outpost side of
`docs/prd/23-define-the-sandbox-control-protocol.md` and can be built and
tested against a fake hub before the lifecycle PRD exists.

## Goals

- `afc outpost run`: fetch the bootstrap document, configure credentials
  for `afc` and git, clone the branch at the pinned revision, connect to
  NATS, report `ready`, handle `ping`, `sync` and `shutdown`, send
  heartbeats with resource stats, start and supervise the workload, and
  exit cleanly on `shutdown` or SIGTERM.
- Make `afc` part of the sandbox and agents images with
  `ENTRYPOINT ["afc", "outpost", "run"]`.
- Keep secrets in memory and out of logs.

## Non-goals

- The collector and telemetry (`docs/prd/26-collect-telemetry-from-sandboxes.md`),
  and `refresh` (`docs/prd/27-refresh-secrets-in-running-sandboxes.md`).
  Both extend this outpost; this PRD leaves hooks (an unsupported-command
  reply and a workload restart function) for them.
- Automatic commits or pushes other than the shutdown push when asked.
- Restarting the workload on its own, or any process management beyond one
  child.
- Running outside a sandbox. The command refuses to start without the three
  variables.

## Current behaviour (for reference)

- `afc` is a static Go binary built with `CGO_ENABLED=0` and installed to
  `$GOBIN`; it has a credential helper (`afc credential-helper`) that reads
  `~/.af/config.toml` and answers git with the API key
  (`docs/permissions.md`, "Git credential helper").
- `containers/sandbox/Containerfile` builds a toolchain image (uid 1001,
  `WORKSPACE=/opt/app-root/workspace`, `HOME=/opt/app-root/src`);
  `containers/agents/Containerfile` layers agent CLIs on it. Neither
  contains `afc` nor sets an entrypoint.

## Functional Requirements

### Startup and bootstrap

- **OP-1.** `afc outpost run` SHALL read `AF_HUB_URL`, `AF_HUB_TOKEN` and
  `AF_SANDBOX_ID`, exit 2 with a clear message if any is missing, and never
  read `~/.af/config.toml` for its own credentials.
- **OP-2.** It SHALL fetch `GET {AF_HUB_URL}/api/v1/sandboxes/{id}/bootstrap`
  with the token, retrying transport errors and `5xx` with backoff (1s
  doubling to 30s) until success or `404`/`401`/`403`, which are fatal. It
  SHALL log the document with `env` values under `secret_keys` and
  `nats.password` masked, and keep those values only in memory.
- **OP-3.** It SHALL write `~/.af/config.toml` (`endpoint_url`, `api_key`,
  mode 0600 in a 0700 directory) so that `afc` and the credential helper
  work for the workload and for git; run `git config --global
  credential.<AF_HUB_URL>.helper "!afc credential-helper"`; and set a
  commit identity `sandbox <sandbox-<id>@af-hub>` unless `GIT_AUTHOR_*` is
  present in the bootstrap `env`.
- **OP-4.** It SHALL clone `git_url` into `workdir` with `--branch
  <branch>` (`--single-branch` is not used, so `sync` can reach other
  refs). If the branch tip is not `revision`, it proceeds on the tip and
  reports both in `ready`. A clone failure is `boot_failed` with
  `stage: clone`.
- **OP-5.** It SHALL connect to `nats.url` with `nats.user`/`nats.password`,
  unlimited reconnects, backoff capped at 30s, subscribe to
  `af.sbx.<id>.cmd`, and then publish `ready`. If NATS is unreachable for
  `timeouts.boot`, it exits 1 after logging (`boot_failed` cannot be
  delivered without NATS; the hub's boot timeout covers it). A bootstrap
  or clone failure with NATS reachable publishes `boot_failed` first.

### Commands

- **OP-6.** Commands are handled one at a time in arrival order; replies
  follow PR-5. `sync pull`: `git fetch origin`, then `git checkout -B
  <branch> <ref>` (default `origin/<branch>`); `dirty_worktree` unless
  `force`, which discards local changes. `sync push`: `git push origin
  <branch>` (`--force-with-lease` when `force`); reply with the SHA, or
  `non_fast_forward` / `git_error` with git's message. `ping`: the
  heartbeat payload. `shutdown`: push first when asked, SIGTERM the
  workload and wait up to the grace, reply, publish `exiting`, exit 0.
  Unknown types: `unsupported`.
- **OP-7.** The outpost does nothing else with the working tree. Whatever
  runs inside commits and pushes through the normal remote; the hub's
  post-push hooks fire as for any push.

### Workload

- **OP-8.** After `ready`, when `command` is present, the outpost SHALL
  start it in `workdir` with the process environment plus the bootstrap
  `env` plus `AF_HUB_URL`, `AF_HUB_TOKEN`, `AF_SANDBOX_ID`, `AF_WORKSPACE`,
  `AF_WORKSPACE_BRANCH`, `AF_REVISION`; forward its stdout and stderr to
  the container's own, and nowhere else; and publish `command_exited` when
  it ends. NATS credentials are not passed to the workload. Without
  `command` the outpost idles. It SHALL NOT restart the workload; the hub
  decides what follows from `on_exit`.
- **OP-9.** SIGTERM to the outpost SHALL be treated as `shutdown` with
  `push: false` and a 10s budget: forward SIGTERM to the workload, wait,
  publish `exiting` with `reason: sigterm` if NATS is connected, exit 0.
  SIGKILL is the gateway's last resort and needs nothing from us.

### Heartbeat

- **OP-10.** Every 30s the outpost SHALL publish `heartbeat` per PR-6:
  `head_sha` and `dirty` from git, `uptime_s`, `command_running`, and
  `stats` read from the container's cgroup (v2 `cpu.stat`, `memory.current`,
  `pids.current`) and a `du` of `workdir` sampled at most every 5 minutes.
  Missing cgroup files yield zeros, not errors.

### Images

- **OP-11.** `containers/sandbox/Containerfile` SHALL copy `afc` (built for
  the target architecture) to `/usr/local/bin/afc` and set
  `ENTRYPOINT ["afc", "outpost", "run"]`; the agents image inherits both.
  `make build-sandbox-container` builds `afc` for `linux/<arch>` first. The
  CI workflow that builds images does the same.
- **OP-12.** `containers/agents/README.md` SHALL document running the
  image by hand with the three variables pointed at a local hub, and the
  `podman run --entrypoint bash` escape hatch for interactive use.

### Testing

- Unit: environment parsing, config file writing, git operations against a
  temporary bare repository, command handling table (OP-6), workload
  supervision (exit codes, signals), heartbeat stats parsing from fixture
  cgroup files, secret masking in logs.
- Integration: `afc outpost run` as a goroutine against an `httptest`
  hub serving a bootstrap document once and an embedded NATS server
  (PRD 19's test helper), a bare repository served by the git server test
  helpers; covers `ready`, both `sync` directions, `dirty_worktree`,
  `non_fast_forward`, `command_exited`, `shutdown` with push, SIGTERM,
  reconnect after the NATS server restarts.

## Technical Boundaries

- Go; new package `internal/outpost` used by `cmd/afc`. Imports
  `internal/sandbox/protocol` and `nats.go`; no hub server packages, so
  `afc` stays cgo-free and small.
- Documentation: `docs/cli.md` ("Sandbox internals" section, hidden
  command), `containers/agents/README.md`, `README.md` (the image
  entrypoint).

## Dependencies

- PRD 23 for every message and the bootstrap document.
- `nats.go` client library (already introduced for the hub by PRD 19).
- End-to-end use needs PRD 24; development does not.

## Design Decisions

### 1. The outpost is `afc`

A separate binary would need its own build, release and image plumbing.
The outpost is small, needs the same HTTP client and credential helper
`afc` already has, and `afc` is already a static binary.

### 2. The outpost supervises the workload

Only the outpost holds the fetched environment (secrets never touch the
container runtime), so it must be the one to start the workload. That
also yields a clean "the agent finished" signal that neither container
exit codes nor heartbeats provide.

### 3. Output to the container's stdout, nowhere else

A person debugging a local sandbox has `podman logs`. Shipping output to
the hub is the most likely place for a secret to leak and is deliberately
not done; agents report what they choose through the collector (PRD 26).

## Open Questions

- Should the outpost refuse to start the workload when `head_sha` differs
  from `revision` (OP-4), or is proceeding on the moved tip always right
  given sandboxes share the workspace branch by default? The draft
  proceeds and reports.
