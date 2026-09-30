# Refresh Secrets in Running Sandboxes

## Intent

A sandbox (`docs/prd/18-run-agents-in-ephemeral-sandboxes.md`) fetches its
bootstrap document exactly once, at boot. That is the right default: a
token stolen after boot cannot fetch secrets, and anything structural that
changes means a new sandbox. Long-running sandboxes are the exception: an
API key rotated after boot, or a variable changed to steer an agent,
should not require throwing away a sandbox with hours of state in it.

This PRD adds one command, `refresh`, that lets the hub arm exactly one
additional bootstrap fetch, tell the outpost to take it over HTTPS, and
optionally restart the workload with the new environment. Only `env`,
`vars` and `secrets` are refreshable; everything else still means a new
sandbox.

## Goals

- `POST …/sandboxes/:id/refresh` on the hub and `afc sandbox refresh`.
- A short-lived nonce that allows one more bootstrap fetch, audited.
- Outpost handling of `refresh`: re-fetch, replace the environment,
  optionally restart the workload, report what changed.

## Non-goals

- Automatic refresh when a secret is rotated. Refresh is explicit.
- Refreshing `command`, `branch`, `image`, `nats` or timeouts.
- Secrets over NATS. The re-fetch is HTTPS, like the first.

## Current behaviour (for reference)

- PRD 24's bootstrap endpoint marks `bootstrapped_at` and answers `404`
  afterwards; PRD 25's outpost keeps the environment in memory, starts the
  workload once and never restarts it; PRD 23 reserves the `refresh`
  command and the `?nonce=` query parameter.

## Functional Requirements

- **RF-1.** `POST /api/v1/workspaces/:slug/sandboxes/:id/refresh` with
  body `{ "restart_command"?: bool }` (`sandboxes:write`, workspace
  ownership) SHALL, for a sandbox in `ready`: generate a 32-byte random
  nonce, store its hash and `now + 60s` on the record, send `refresh`
  with the nonce and the flag over NATS, and return the reply (`200` with
  `{ "changed_keys": [...], "restarted": bool }`), `409 sandbox_not_ready`
  otherwise, `504 sandbox_timeout` if no reply within 60s. The nonce is
  cleared when used, when it expires, or when the sandbox ends.
- **RF-2.** The bootstrap endpoint SHALL accept `?nonce=<value>` and serve
  the document once more when the hash matches, the nonce has not
  expired, and the sandbox is `ready`; it clears the nonce in the
  responding transaction. The re-served document contains the current
  `env` (re-resolved), the same `secret_keys` set as at launch (the key set
  is fixed at launch; a key removed from the configuration since is still
  served, a key added since is not), and the same structural fields. Every
  attempt emits `hub.sandbox.bootstrap` with outcome `refresh`.
- **RF-3.** The outpost, on `refresh`, SHALL fetch with the nonce, reply
  `bootstrap_failed` on any error, else replace its in-memory `env` and
  `secret_keys` (extending the collector's redaction set), ignore every
  other field, compute `changed_keys` (names only), and when
  `restart_command` is true and a workload is running, SIGTERM it, wait
  up to 30s, then SIGKILL, start `command` again with the new environment,
  and publish `command_exited` for the old process followed by a heartbeat
  with `command_running: true`. Without restart, the new values apply only
  to processes started later.
- **RF-4.** `hub.sandbox.refresh` audit event: actor, sandbox, changed
  keys, restarted. Metric `af_sandbox_refresh_total{result}`.
- **RF-5.** `afc sandbox refresh <slug> <id> [--restart]` prints the
  changed keys.

### Testing

- Nonce lifecycle (single use, expiry, wrong sandbox); document
  composition on refresh (fixed key set); outpost replace and restart
  paths; end-to-end in the PRD 25 integration test with a rotated secret.

## Technical Boundaries

- Go; extends `internal/sandbox` and `internal/outpost`. Two columns on
  `sandboxes` (`refresh_nonce_hash`, `refresh_nonce_expires_at`).
- Documentation: `docs/api.md`, `docs/cli.md`, `docs/sandbox_protocol.md`.

## Dependencies

- PRD 24 (bootstrap endpoint, command plumbing), PRD 25 (outpost),
  PRD 23 (message shapes). PRD 26 for the redaction-set update, if
  present.

## Design Decisions

### 1. One-shot re-fetch rather than a second fetch policy

Single-fetch stays the rule; refresh is a hub-armed, audited, 60-second
exception. A stolen token still cannot fetch secrets unless someone with
`sandboxes:write` decides so, and that decision is on record.

### 2. Names, not values, in replies and audit

The reply and the audit event list changed keys only. Values never leave
the HTTPS response.

### 3. Restart is opt-in

Most workloads read their environment at start. Restarting is the only
way to make a rotated key take effect for them, but it also loses any
in-process state, so the caller chooses.

## Open Questions

- Should a refresh be allowed to widen the secret key set when the
  configuration changed since launch? The draft freezes the set at launch
  for predictability.
