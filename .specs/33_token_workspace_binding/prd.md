---
spec_id: "33"
spec_name: "token_workspace_binding"
title: "Bind a personal access token to a workspace: the binding and its enforcement"
status: "active"
created_at: "2026-10-21T09:07:49.489558Z"
updated_at: "2026-10-21T09:07:49.489558Z"
intent_hash: "f937fdf55704946c830eea5e7c72f1f8b10bdc13f1852ebab18f2298822d7618"
schema_version: 2
source: "docs/prd/03-bind-a-personal-access-token-to-a-workspace.md"
---
## Intent

Let a personal access token (PAT) be confined to one workspace. Hub keeps its own record of which workspace a PAT is bound to, keyed by apikit's token id, and enforces it in the REST API and the git server. A bound token reaches its own workspace under its scopes and nothing else. Every unbound credential behaves exactly as it does today. This spec covers the binding record and its enforcement. Issuing bound tokens is a separate spec (`bound_token_issuance`).

## Goals

1. Take a binding row that ties token `T` to workspace `S`. With it, every API route whose path template contains `/workspaces/:slug` answers `404 workspace not found` to `T` for any slug other than `S`, and does so before any scope check. For `S`, the route behaves exactly as it would for an unbound PAT with the same user and scopes.
2. Every route apikit mounts (`/user`, `/user/keys`, `/user/tokens`, `/users`, `/orgs`, …), every user- and org-scoped hub route and `POST /workspaces` answer `T` with `403` and `error_type: token_bound`. So does every other non-workspace hub route that is not on the bound-token allowlist.
3. The git server serves `T` on `/git/<org>/S.git` under its `git:*` scopes and gives it a pkt-line `404 repository not found` for every other repository.
4. Wherever a route is allowed to look across workspaces, `T` sees only `S`. This covers `GET /workspaces`, the session routes, `GET /audit` and `GET /events`. `POST /sessions` with a `workspace_slug` other than `S` answers 404.
5. The binding check is written once, in `internal/wsaccess`. A route added later is covered with no change: a workspace-scoped route by its path template, and any other route by default-deny.
6. Unbound PATs, API keys and admin tokens get byte-identical responses. The existing ownership suites (`internal/workspace`, `internal/gitserver`, `internal/carrypatch`, `internal/merge`, `internal/sandboxes`, `internal/forge`, `internal/secrets`, `internal/audit`) pass unchanged. The only exception is the two audit tests that encode the retired `KeyID` check (Requirement 8).
7. API keys and admin tokens never trigger a binding query. For a PAT, each layer that checks costs one primary-key lookup.

## Non-goals

- **Issuing bound tokens.** `POST`/`GET /api/v1/workspaces/:slug/tokens` are out of this spec. So are the `afc` commands, revoking bound tokens when their workspace is deleted, sweeping bindings whose token apikit no longer holds, and the `hub.token.bind` / `hub.token.bound_refused` audit events. All of these belong to the next spec of the split, `bound_token_issuance`. In this spec, binding rows are written only by tests.
- **Binding to an org, to several workspaces or to a pattern.** A token is bound to one workspace.
- **Changing scopes or what they imply.** A binding narrows where a scope applies. It never changes what the scope allows.
- **Changing apikit.** Its `pats` table, PAT creation flow, privilege-escalation rule and `AuthInfo` stay as they are. Hub wraps apikit's routes and does not modify them.
- **Binding API keys or admin tokens.** These credentials belong to a person or an operator.
- **Org-member read access** (hub PRD 04). Whatever widening it brings later, the binding narrows it again.
- **Rebinding, unbinding or moving a binding.** There is no API for any of them.

## Background

The agent-fox service holds one hub credential per workspace it watches. Today that credential can only be a PAT. A PAT with `workspaces:read` sees every workspace its user owns, so a leaked token from one subscription exposes all the others. The README already advertises "workspace-scoped tokens" as a credential type, but nothing implements one. The source material is `docs/prd/03-bind-a-personal-access-token-to-a-workspace.md`.

What exists today:

- **The ownership rule** is in `internal/wsaccess/wsaccess.go`. `AuthorizeWorkspace(db, auth, slug) (*WorkspaceAccess, int, string)` lets an admin token see any workspace and every other credential only the workspaces it owns. A miss is `404 workspace not found`. `internal/carrypatch`, `internal/merge` (through `workspace.AuthorizeWorkspace`), `internal/forge` and `internal/sandboxes` all call it.
- **The workspace package has its own copy of the rule.** `lookupWorkspaceForAuth` in `internal/workspace/handlers.go` serves CRUD, patches and reclone. `handleListWorkspaces` filters through `listWorkspacesByOwner` in `store.go`.
- **The git server has another copy.** `authorizeGitAccess(c, ws)` in `internal/gitserver/authz.go` compares `UserID` with `ws.OwnerID` and writes a pkt-line 404. `lookupPAT` in `internal/gitserver/auth.go` already sets `AuthInfo.TokenID` for PATs.
- **Audit has four more copies,** in `internal/audit/auth.go` and `handlers.go`: `requireWorkspaceRead`, `ownedWorkspaces`, `hasWorkspaceAccess` and `getAccessibleWorkspaces`. `checkWorkspaceAccess` also contains the one place a binding is already assumed. It reads `auth.KeyID` as a workspace slug ("workspace-scoped PATs must match the URL slug") and answers `403 workspace_mismatch`. Nothing in hub or apikit puts a slug into `KeyID`. Only the test fixture `workspacePatAuth` does, so in production the branch can never match a slug (inferred).
- **Secrets and variables** check ownership in `internal/secrets/handlers.go` (`lookupWorkspaceOwner`, `checkOrgMembership`).
- **apikit's routes.** apikit's `MountHandlers` registers every built-in route (OAuth, `/user`, `/user/keys`, `/user/tokens`, `/users`, `/orgs`) on the server's API group, according to `docs/errata/openapi_description.md`. Hub's routes are registered on the same group afterwards. Hub has never wrapped apikit's routes. A bound PAT that holds `tokens:manage` could call apikit's `POST /user/tokens` and mint an unbound PAT, so those routes must be closed to bound tokens at request time. Refusing those scopes when the token is created would not be enough.
- **Workspace deletion** (`deleteWorkspace` in `internal/workspace/store.go`) removes dependent rows explicitly inside one transaction. Hub does not rely on SQLite foreign-key cascades.

## Requirements

### 1. The binding record

Hub stores bindings in its SQLite database, in a table `token_workspace_bindings` with columns `token_id TEXT PRIMARY KEY`, `slug TEXT NOT NULL` and `created_at TEXT NOT NULL`. `created_at` is an apikit UTC timestamp. The table has an index `idx_token_workspace_bindings_slug` on `slug`. Creation is idempotent (`CREATE … IF NOT EXISTS`). `MountWorkspaceHandlers` creates the table right after the workspaces schema, and `MountGitHandlers` ensures it too, so the git server never runs without it. The table has no foreign key to `workspaces`.

A PAT with no row is unbound. Archiving the workspace does not touch its rows, and in this spec neither does deleting it (Design Decision 4).

### 2. Resolving a binding

A binding applies only to credentials with `CredentialType == "pat"`. The binding key is the PAT's token id, which is `AuthInfo.TokenID`.

On API routes, the post-authentication guard (Requirement 3) handles a PAT whose `AuthInfo.TokenID` is empty. It takes the id from the `Authorization: Bearer af_pat_<token_id>_<secret>` header that apikit has just authenticated: the text between `af_pat_` and the next `_`, with the scheme matched case-insensitively. It then writes the enriched `AuthInfo` back with `apikit.SetAuthInfo`, so every later check reads the same id. If no id can be found, the PAT is unbound.

Resolving a binding costs one primary-key lookup. API keys and admin tokens never cause one. If the lookup fails (a database error), the request fails closed: API routes answer `500 internal server error`, the git server writes a pkt-line 500, and the token is never treated as unbound.

### 3. Workspace-scoped API routes

A guard middleware runs on the API group after apikit's authentication. For a bound token it handles every route whose path template contains the segments `workspaces/:slug`. If `:slug` is not the bound slug, the guard answers `404` with `workspace not found`, the same body a non-owner gets today. It does this before the handler runs, so before any scope, existence or status check. If the slug matches, the request continues unchanged into the handler's own checks.

`wsaccess.AuthorizeWorkspace` and `workspace.lookupWorkspaceForAuth` apply the same rule, after the existence and ownership checks, so that a code path reached without the guard is confined as well.

### 4. Other hub API routes: default-deny

For a bound token, the guard refuses every API-group route that is not workspace-scoped and not explicitly allowed. It answers `403` with `error_type: token_bound` and the message `this token is bound to workspace <S>`.

The allowed routes are declared where they are registered, by marking the `*echo.Route` values: `GET /workspaces`, `POST /sessions`, `GET /sessions`, `GET /sessions/:id`, `POST /sessions/:id/complete`, `POST /sessions/:id/usage`, `GET /sessions/:id/usage`, `GET /audit`, `GET /events` and `GET /sandbox-profiles`.

Everything else is refused. That covers `POST /workspaces`, `/user/secrets*`, `/user/vars*`, `/orgs/:slug/secrets*`, `/orgs/:slug/vars*`, `GET /sandboxes`, unmatched paths under the mount point and any route added later. Requests without credentials, and requests from unbound credentials, pass the guard without a query.

### 5. apikit's own routes

apikit registers its routes before hub can add middleware to the API group. They are therefore guarded by a check on the root Echo instance that runs before authentication and can only refuse.

At startup hub records the routes that `MountHandlers` adds by comparing the Echo route table before and after the call. It leaves out `/healthz`, `/readyz` and `/version` if they turn up in that difference.

For a request whose method and path template match one of those routes and which carries `Authorization: Bearer af_pat_<token_id>_…`, hub looks up `<token_id>`. If a binding exists, the answer is `403` with `error_type: token_bound` and the message `this token is bound to a workspace`. The message does not name the workspace, because the credential has not been authenticated yet. Otherwise the request goes on to apikit unchanged.

In practice, a bound token gets `403 token_bound` on `GET /api/v1/user`, `POST /api/v1/user/tokens`, `DELETE /api/v1/user/keys/:key_id`, `GET /api/v1/orgs/:id` and every other apikit route, whatever scopes it holds.

### 6. Cross-workspace views and sessions

Where an allowed route spans workspaces, hub treats every workspace other than `S` exactly like a workspace the caller does not own, and reuses that route's existing non-owner response:

- `GET /workspaces` returns at most `S`, subject to the existing scope check and the `include_archived` filter.
- `POST /sessions` with `workspace_slug ≠ S` answers `404 workspace not found` (through `requireWorkspaceRead`). The same applies to an idempotent replay of a session id that belongs to another workspace.
- `GET /sessions/:id`, `GET /sessions/:id/usage` and `GET /sessions?workspace_slug=…` answer `403 workspace access denied` for a session or slug outside `S`. `GET /sessions` without a slug lists only `S`'s sessions.
- `POST /sessions/:id/complete` and `POST /sessions/:id/usage` answer `403 not session owner` when the session's workspace is not `S`.
- `GET /audit` and `GET /events` return only events of `S`. `GET /audit?workspace=<other>` answers `404 workspace not found`.

### 7. The git server

`authorizeGitAccess` applies the binding after the admin and ownership checks. A bound token requesting any repository other than `S`'s gets a pkt-line `404 repository not found`, the same response a non-owner gets. On `S` the existing `git:read`/`git:write` scope checks decide. A failed lookup writes a pkt-line 500.

### 8. Audit ingestion

The branch of `checkWorkspaceAccess` that compares `auth.KeyID` with the URL slug is removed, and with it the error type `workspace_mismatch`. A bound token posting to a run of another workspace now answers `404 workspace not found`, through the shared rule. All other outcomes stay as they are:

- an unbound non-owner gets `403 workspace_access_denied`
- an archived workspace gets `409 workspace_archived`
- an unknown workspace gets `404`
- a nil SQLite handle still skips the checks.

### 9. Unchanged behaviour and lifecycle

Nothing changes for unbound PATs, API keys or admin tokens: no new query and no new response. If a PAT's binding names an archived workspace, the token still reaches it and gets that workspace's usual `400`/`409` responses. If the bound workspace has been deleted, the binding row stays, so the token answers `404` on every workspace-scoped route, `403 token_bound` everywhere else and a pkt-line 404 in git. The token fails closed until the issuance spec revokes it.

## Design Decisions

1. **The input is split into two specs.** This first spec is the binding and its enforcement. `bound_token_issuance` follows with minting and listing, lifecycle, CLI and audit events. Enforcement comes first because a bound token issued without it could escape through apikit's `/user/tokens`. Together the input needs more than 10 requirements and 12 tasks.
2. **The binding table has no foreign key or `ON DELETE CASCADE`,** contrary to the input. Hub deletes dependent rows explicitly in `deleteWorkspace`, and whether SQLite foreign keys are enforced depends on apikit's `OpenDatabase`, which could not be checked. A cascade would also silently *unbind* a live token.
3. **The rule's code lives in `internal/wsaccess`.** It is the existing leaf package every side already imports. The new parts are the binding lookup, the post-auth guard, the pre-auth apikit-route guard and the allowlist registry. `AuthorizeWorkspace`, `lookupWorkspaceForAuth`, the git server and the audit helpers all call the same function.
4. **Workspace deletion and archival do not touch bindings in this spec.** Keeping the row fails closed: a token bound to a deleted slug reaches nothing. Revoking the token at deletion needs apikit's revocation path and belongs to `bound_token_issuance`, which must also close the slug-reuse case: a new workspace with the same slug and owner.
5. **The token id comes from `AuthInfo.TokenID`.** If that field is empty, the guard fills it in from the authenticated Bearer header with `apikit.SetAuthInfo`. Whether apikit's REST middleware sets `TokenID` could not be checked, but the git server sets it. If neither source gives an id, the PAT is treated as unbound, which keeps every existing test fixture (they set no `TokenID`) unchanged.
6. **A failed binding lookup fails closed with a 500.** A database error must never widen a bound token's reach.
7. **Bound tokens are confined by a path-template guard on the API group, installed right after `MountHandlers`.** It is installed with `s.APIGroup().Use(...)` inside `MountWorkspaceHandlers`, before any hub route or subgroup exists. Echo copies group middleware when a route or subgroup is added, so every hub route registered later is covered automatically. That is how Goal 5 is met.
8. **Other non-workspace routes are default-deny for bound tokens, with an allowlist declared by marking routes.** The allowlist is a registry in `wsaccess` fed with the `*echo.Route` values returned at registration. This avoids depending on the configurable mount point. A route someone forgets to mark is refused, which is the safe way to fail.
9. **`GET /workspaces`, the session routes, `GET /audit`, `GET /events` and `GET /sandbox-profiles` are allowed and filtered, not refused.** The input classed listings as user-scoped (403). A service needs to discover its workspace, report sessions and read its own audit stream, and filtering these to `S` keeps "a bound token sees exactly one workspace" true.
10. **For workspaces other than `S`, cross-workspace routes reuse their existing non-owner responses** (403 or 404 as each already does) instead of new codes. That way a bound token is indistinguishable from a non-owner there, and unbound behaviour does not move.
11. **apikit's routes are guarded before authentication, by a check that can only refuse.** It keys on the token id parsed from the Bearer header. No apikit hook to run hub code between apikit's authentication and its handlers could be found. The check never grants access and never hashes a secret, so it does not re-implement apikit's authentication (steering). The cost is that someone who knows a token id but not its secret can learn that the id is bound, so the message there omits the slug.
12. **Status codes follow the existing anti-enumeration rule.** Another workspace answers `404 workspace not found` (pkt-line `404 repository not found` in git). A route that cannot be workspace-scoped answers `403` with `error_type: token_bound`, because a caller holding the token knows it is bound.
13. **For bound tokens, the binding is checked before scopes.** The guard runs before the handlers, so another workspace answers 404 even when the scope is missing. To a bound token, other workspaces do not exist.
14. **`checkWorkspaceAccess` loses its `KeyID` branch and the `workspace_mismatch` error type.** Nothing ever puts a slug in `KeyID`, so the branch was a fiction, and the input asked for the audit copy of the rule to go. The unbound responses `403 workspace_access_denied` and `409 workspace_archived` stay, to honour Goal 6. `TS-17-51` is rewritten to expect a binding row and a 404.
15. **No per-request memo of the binding lookup.** The guard and `AuthorizeWorkspace` each do one primary-key lookup. Steering forbids custom context keys, and `AuthorizeWorkspace` has no context parameter. One more indexed SQLite read per PAT request is negligible.
16. **The binding lookup runs before any rows are opened.** It never runs inside an open `rows` iteration or transaction, because the test databases use single-connection pools (`SetMaxOpenConns(1)`).
17. **This spec adds no audit events.** `hub.token.bound_refused`, with its one-per-token-per-minute rate limit, ships with `hub.token.bind` in `bound_token_issuance`.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `25_sandbox_profiles` | modifies | `sandboxes.RegisterRoutes` marks `GET /sandbox-profiles` as allowed for bound tokens. |
| `26_sandbox_leases` | modifies (behaviour) | The lease routes go through `wsaccess.AuthorizeWorkspace` and now honour bindings. `GET /sandboxes` is refused to bound tokens with `403 token_bound` (it was already admin-only). |
| `30_forge_client_reads` | modifies (behaviour) | Forge read routes are workspace-scoped and confined by the guard and `AuthorizeWorkspace`. Code is unchanged. |
| `31_forge_writes` | modifies (behaviour) | The same applies to the forge write and webhook routes. The ingress (`32`) sits on the root instance, takes no bearer credential and is unaffected. |

## Verified External API

**github.com/txsvc/apikit** (replaced to `../apikit`, which is outside the repository; signatures come from compiled call sites in this repository):

- `type AuthInfo` with fields `CredentialType string`, `UserID string`, `Role string`, `KeyID string`, `TokenID string` and `Permissions []string`. Verified by the struct literals in `internal/gitserver/auth.go` (`TokenID: tokenID`) and in the tests. **Unverified:** whether apikit's REST auth middleware fills in `TokenID` for PATs.
- `func GetAuthInfo(c echo.Context) *AuthInfo` and `func SetAuthInfo(c echo.Context, info *AuthInfo)`. Used throughout; documented in `docs/errata/01_apikit_auth_exports.md`.
- `func WriteAPIError(c echo.Context, code int, message string) error` and `func WriteAPIErrorWithType(c echo.Context, code int, message, errorType string) error`. Used in `internal/audit/auth.go` and `internal/workspace/handlers.go`.
- `func NowUTC() string`. Used as a TEXT timestamp in `internal/sandboxes/smoke_test.go` and `internal/audit/store.go`.
- `func NewServer(cfg, healthCheck) *Server`; `(*Server).MountHandlers(db *DB, perms ...Permission) error`; `(*Server).APIGroup() *echo.Group`; `(*Server).Echo() *echo.Echo`; `(*Server).ExternalURL() string`. Used in `cmd/af-hub/main.go`, `internal/workspace/routes.go` and `internal/sandboxes/smoke_test.go`. **Unverified:** that `APIGroup()` returns the same group on every call and that apikit's auth middleware is a middleware of that group. `docs/errata/openapi_description.md` quotes `MountHandlers` registering every apikit route on `s.APIGroup()`.
- `func LoadConfig()`, `func OpenDatabase(path) (*DB, error)` with field `(*DB).SqlDB *sql.DB`, `func GenerateAPIKey(db *sql.DB, userID string, expiresDays int, logger) (key, error)` with field `key.FullKey`. Used by the real-server test harness in `internal/sandboxes/smoke_test.go`.
- PAT wire format `af_pat_<token_id>_<secret>`. Verified from `docs/permissions.md` and the split in `internal/gitserver/auth.go:resolvePAT`.

**github.com/labstack/echo/v4 v4.16.0** (well-known; the module cache is unreachable, so these are **unverified** in-repo):

- `(*Echo).Routes() []*Route` with `Route{Method, Path, Name}`; `(*Echo).Use(...MiddlewareFunc)`; `(*Group).Use(...MiddlewareFunc)`; `(*Group).GET/POST(...) *Route`; `Context.Path()` (the matched route template); `Context.Param(name)`.
- Echo semantics the design relies on:
  - Group middleware is captured when a route or subgroup is added.
  - Root `Use` middleware runs after routing, so `c.Path()` is already set, and before group middleware, so authentication has not happened yet.
  - Re-adding a method and path replaces the earlier handler. The repository already relies on this: `POST /workspaces/:slug/sync` is registered by both `workspace` and `carrypatch`.
