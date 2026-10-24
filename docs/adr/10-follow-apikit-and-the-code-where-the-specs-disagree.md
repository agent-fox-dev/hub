# ADR 10: Follow apikit and the code where the specs disagree

- **Status:** Accepted
- **Date:** 2026-10-24 (records decisions taken between 2026-07 and 2026-10)

## Context

The hub was built spec by spec. A spec was written before the library it
named could be read, or before an earlier spec had landed, and its text then
disagreed with apikit, with AgentKit or aflib, or with the code already in the
repository. Each case was settled once and recorded in an erratum. The errata
are gone; the settlements that still bind the code are here.

## Decision

**apikit is the authority for what apikit provides.**

- Handlers read the credential with `apikit.GetAuthInfo` and write errors with
  `apikit.WriteAPIError`; the hub defines no auth structs, context keys or
  error envelopes of its own. `apikit.HealthChecker` is a function type
  (`func() error`), so the health check is a closure that pings the database.
- Every apikit route is under the API mount point `/api/v1` (`/api/v1/user`,
  `/api/v1/orgs/:id`, `/api/v1/user/tokens`, ...); only `/healthz`, `/readyz`
  and `/version`, and the hub's `/metrics` and `/git/...`, are on the root
  instance. apikit addresses organizations by id, the hub's secrets and
  variables routes by slug. A PAT is created with `{name, permissions,
  expires}` and revoked with `200` and its metadata; API keys come from the
  OAuth callback and are rotated, never created, by a route.
- `afc` writes every error, validation errors included, as the JSON envelope
  on stdout through `apikit.CLIHandleError`; the process exit code is 1 for
  an API or CLI error and 2 for a usage error, whatever `code` the envelope
  carries (the hub maps its own exit 3 for `--fail-on-diverged` itself).
  `DoRequest` prepends `/api/v1`, so commands pass bare paths.
- User and org deletion are apikit's and have no hooks, so secrets and
  variables cascade on workspace deletion only; rows of a deleted org are
  orphaned and unreachable. A hook in apikit is the fix.

**The code that landed first wins over a later spec's wording.**

- `DELETE /api/v1/workspaces/:slug` answers `204`.
- A PAT lacking the scope of a lifecycle route (archive, reactivate, delete)
  is answered `404`, not `403`, so that slugs are not disclosed.
- The job queue's `RetryPolicy` treats `MaxRetries: 0` as "unset, default
  20" when another field of the policy is set and as "no retries" when the
  policy is otherwise empty; `retry_count` is incremented before the check for
  a retryable error and not for a permanent one.
- The carry-patch sync answers the whole workspace object with the
  carry-patch fields merged in, not the carry-patch fields alone; the sync
  extension returns its fields and the workspace handler renders the record.
- `POST /sessions/:id/complete` takes no token counts; `POST
  /sessions/:id/usage` does.

**`docs/openapi.yaml` is kept by hand and checked by hand.** It is OpenAPI
3.1, validated with `openapi-spec-validator` when it changes (no validator runs
in `make check`), and its paths were checked against every route registration
of the hub and apikit. A route a handler implements but `main.go` does not
mount (the rebuild rollback once was) is a bug the document found.

## Consequences

- A reader of the code needs apikit's documentation, not the archived specs,
  to understand the auth, error and CLI conventions.
- Where a spec and the code disagree from here on, the code and the ADR of
  its feature say which won; no erratum is written (see
  `.specs/steering.md`).
