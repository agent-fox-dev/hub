# Issue Workspace-Bound Tokens

## Intent

The hub's credentials are admin tokens, API keys and PATs. A PAT is scoped
by permission but not by resource: a PAT with `git:write` can push to every
workspace its owner owns. There is nothing the hub can hand to a CI job, a
codespace or a sandbox that says "this workspace, and only this one, until
this time". The README promises workspace-scoped tokens and
`docs/prd/prd14.md` described them; PRD 1 decided PATs would cover the
delegation case and nothing workspace-bound was built.

This PRD adds the binding without adding a credential type. A
workspace-bound token is an apikit PAT, minted by the hub for a workspace's
owner, plus a hub-side binding that names the workspace, an optional subject
(for example a sandbox), an expiry at second granularity, and a revocation
flag. Every hub handler that resolves a workspace, the git server, and the
session and audit ingestion endpoints consult the binding and reject the
token anywhere but its workspace. The sandbox lifecycle
(`docs/prd/24-launch-and-manage-sandboxes.md`) is the first internal
consumer; `afc workspace token` makes the same thing available to people.

## Goals

- Let a workspace owner (or hub code acting for the owner) issue a token
  that works for exactly one workspace, with a chosen subset of scopes, a
  lifetime expressed in seconds, hours or days, and an optional subject.
- Enforce the binding on every workspace-scoped surface: REST handlers, the
  git server, agent sessions and audit ingestion.
- Attribute what a bound token does to its subject, so that sessions and
  audit rows created with it carry the subject.
- Revoke on demand and on expiry, with a Go API hub code can call.
- Expose issuance, listing and revocation through the REST API and `afc`.

## Non-goals

- A new credential format. Bound tokens are PATs (`af_pat_…`) and are
  validated by apikit's middleware like any PAT.
- Changing apikit. If apikit later grows resource-bound PATs, the binding
  table folds into it.
- Binding to organisations or to several workspaces. One token, one
  workspace.
- Tokens for users who do not own the workspace.

## Current behaviour (for reference)

- `docs/permissions.md`: PATs carry a permission list and are owned by a
  user; workspace handlers check `ws.OwnerID == auth.UserID` and answer
  `404` on mismatch; the git server does the same with a pkt-line 404.
- `apikit.AuthInfo` carries the credential type, user id and permissions,
  and (per the session records in `internal/audit`) a credential id.
- PAT expiry is 0, 30, 60 or 90 days, chosen at creation through
  `POST /api/v1/user/tokens`.
- Sessions (`POST /api/v1/sessions`) record `credential_id` and
  `credential_type` and accept free-form `metadata`.

## Functional Requirements

### Binding model

- **WT-1.** A table `workspace_tokens` SHALL hold
  `(token_id PRIMARY KEY, workspace_slug, owner_id, subject_kind NULL,
  subject_id NULL, scopes JSON, expires_at, revoked_at NULL, created_at,
  created_by)`. `token_id` is the apikit PAT id.
- **WT-2.** A bound token MAY hold only scopes from this allow-list:
  `workspaces:read`, `git:read`, `git:write`, `sessions:read`,
  `sessions:write`, `audit:read`, `audit:write`, `vars:read`,
  `patches:read`, `patches:write`, `rebuilds:read`, `rebuilds:write`,
  `merges:read`, `merges:write`, and any scope a later PRD registers as
  bindable (the sandbox PRDs add `sandboxes:read`). Requesting any other
  scope is `400 scope_not_bindable`.
- **WT-3.** The hub SHALL expose a Go API for internal consumers:

  ```go
  type IssueParams struct {
      WorkspaceSlug string
      Scopes        []string
      TTL           time.Duration      // > 0, ≤ [tokens] max_bound_ttl (default 30d)
      Subject       *Subject           // optional {Kind, ID}
      Name          string
      CreatedBy     string             // user id or "system:<component>"
  }
  func Issue(ctx, db, p IssueParams) (plaintext string, Binding, error)
  func Revoke(ctx, db, tokenID string) error
  func Lookup(ctx, db, tokenID string) (Binding, bool, error)
  ```

  `Issue` mints the PAT for the workspace's owner through apikit with no
  PAT-level expiry (or the longest available), records the binding with
  `expires_at = now + TTL`, and returns the plaintext once. `Revoke` marks
  the binding and revokes the PAT best-effort; the binding is
  authoritative.

### Enforcement

- **WT-4.** A helper `BoundTo(auth *apikit.AuthInfo) (Binding, bool)`
  SHALL resolve the binding for a PAT's credential id, cached per request.
  Admin tokens and API keys are never bound.
- **WT-5.** Every handler that resolves a workspace from the request path,
  and the git server's `authorizeGitAccess`, SHALL, when the credential is
  bound, require `binding.WorkspaceSlug == slug`, `revoked_at IS NULL` and
  `expires_at > now`; otherwise answer as for a non-owner (`404`, or the
  pkt-line 404 on the git server). The list endpoint
  `GET /api/v1/workspaces` SHALL return only the bound workspace.
- **WT-6.** Endpoints that are not workspace-scoped SHALL reject bound
  tokens outright with `403 token_bound`: everything under
  `/api/v1/user/*` (keys, tokens, secrets, vars, orgs), `/api/v1/orgs/*`,
  `POST /api/v1/workspaces`, and admin endpoints. A bound token cannot
  create PATs, refresh keys or read secrets anywhere.
- **WT-7.** `POST /api/v1/sessions` with a bound token SHALL require
  `workspace_slug` to equal the binding's workspace, and SHALL set
  `metadata.token_subject = { "kind": …, "id": … }` on the session when the
  binding has a subject. Audit ingestion under
  `/api/v1/workspaces/:slug/runs/:run_id/*` SHALL attach the same subject to
  stored events. Callers cannot override these fields.
- **WT-8.** Expired bindings SHALL be revoked (binding and PAT) by the
  existing retention worker on its hourly pass, and pruned after
  `AF_TOKEN_BINDING_MAX_AGE_DAYS` (default 30) past revocation.

### API

- **WT-9.** Endpoints, requiring workspace ownership (admin bypasses) and,
  for PATs, `tokens:manage` (issue, revoke) or `tokens:read` (list):

  | Method and path | Effect |
  |---|---|
  | `POST /api/v1/workspaces/:slug/tokens` | issue; body `{ "name", "scopes": [], "ttl": "8h", "subject": { "kind", "id" } }`; `201` with the binding and `token` (plaintext, once) |
  | `GET /api/v1/workspaces/:slug/tokens` | list bindings (no plaintext), `?include_revoked=true` |
  | `GET /api/v1/workspaces/:slug/tokens/:token_id` | one binding |
  | `DELETE /api/v1/workspaces/:slug/tokens/:token_id` | revoke; `200` with the binding; `400` if already revoked |

  A bound token cannot call any of these (WT-6). `ttl` uses Go duration
  syntax with `d` accepted for days; it is capped by
  `[tokens] max_bound_ttl`.
- **WT-10.** The binding as returned:

  ```json
  {
    "token_id": "…", "name": "ci", "workspace_slug": "my-project",
    "scopes": ["git:read", "git:write"],
    "subject": { "kind": "sandbox", "id": "uuid" },
    "expires_at": "…", "revoked_at": null, "created_at": "…", "created_by": "uuid"
  }
  ```

### CLI

- **WT-11.** `afc workspace token create <slug> --name <n> --scope <s>...
  --ttl 8h [--subject kind:id]` prints the plaintext once (and `--json`
  the full response); `afc workspace token list <slug> [--include-revoked]`;
  `afc workspace token revoke <slug> <token_id>`.

### Testing

- Unit: `Issue`/`Revoke`/`Lookup`; scope allow-list; TTL cap; WT-5 in the
  workspace authz matrix tests and the git server authz tests (bound token
  on its workspace succeeds, on another `404`, after expiry `404`, after
  revoke `404`); WT-6 on a sample of non-workspace endpoints; WT-7 subject
  propagation into sessions and audit rows.
- Integration: clone and push with a bound token through the git server
  test helpers.

## Technical Boundaries

- Go; new package `internal/wstoken`. Enforcement helpers are plain
  functions taking `*apikit.AuthInfo`, per the steering rules; the
  workspace, gitserver, audit and secrets packages call them.
- State in the existing SQLite database.
- Documentation: `docs/api.md` (new endpoints, a "Bound tokens" paragraph
  under Authentication), `docs/permissions.md` (a fourth row in the
  credential table describing the binding, and WT-6), `docs/cli.md`,
  `docs/configuration.md` (`[tokens] max_bound_ttl`,
  `AF_TOKEN_BINDING_MAX_AGE_DAYS`), `README.md` (the sentence about
  workspace-scoped tokens becomes true).

## Dependencies

- apikit: a server-side way to create a PAT for a given user id (the
  HTTP handler does it for the caller; the hub needs the same for the
  workspace owner), the credential id on `AuthInfo`, and PAT revocation.
  The sibling checkout was not available when this was written; if
  server-side minting is missing, it is a small apikit addition and the
  first task of the spec.
- `internal/audit` retention worker for WT-8.

## Design Decisions

### 1. Binding in the hub, PAT in apikit

apikit owns credentials and the steering rules forbid custom credential
resolution. A PAT plus a hub-side binding gives resource scoping, a
second-granular expiry and a subject without touching apikit's format or
middleware. The binding is authoritative: revoking it is enough even if the
PAT revocation fails.

### 2. Allow-list of bindable scopes

A bound token is meant to be handed to something less trusted than its
owner. Listing what it may hold, rather than what it may not, keeps a new
scope from becoming bindable by accident.

### 3. Subject on the binding, not on the token

The subject (`sandbox`, `ci-run`, …) is what the token was issued *for*.
Recording it once on the binding and stamping it onto sessions and audit
rows gives attribution without asking every client to remember to send it.

## Open Questions

- Should `ttl` also be allowed to be unbounded (`0`) for long-lived CI
  tokens, given the binding can be revoked at any time? The draft caps it
  at `[tokens] max_bound_ttl`, default 30 days.
