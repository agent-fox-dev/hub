---
spec_id: "34"
spec_name: "bound_token_issuance"
title: "Issue, list and retire workspace-bound personal access tokens"
status: "active"
created_at: "2026-10-21T09:17:51.449682Z"
updated_at: "2026-10-21T09:17:51.449682Z"
intent_hash: "6857d7b10a0e7d0505c65475ba0eceb22cb9124f9278dd59b873903a6c95030f"
schema_version: 2
source: "docs/prd/03-bind-a-personal-access-token-to-a-workspace.md"
---
## Intent

Let a workspace owner issue a personal access token (PAT) bound to one of their workspaces, see which tokens are bound to it, and know those tokens stop working when the workspace is deleted. Every binding hub creates, and every request a binding refuses, is recorded in the hub audit log. The token itself is minted by apikit's own PAT path, so apikit's validation, expiry and privilege-escalation rules apply unchanged. Hub adds only the binding. This spec builds on the binding record and its enforcement from `33_token_workspace_binding`.

## Goals

1. The owner of an active workspace calls `POST /api/v1/workspaces/:slug/tokens` with an API key, or with a PAT holding `tokens:manage`. The answer is `201` with a PAT minted by apikit's `POST <mount>/user/tokens` and a binding row tying it to `:slug`. The plaintext token appears in that response and nowhere else.
2. When apikit refuses (a bad body, an unknown scope, a missing `tokens:manage`, a PAT asking for scopes it does not hold, a bad `expires`), the caller gets apikit's status and body unchanged.
3. A failed bind never leaves a live token. If the binding cannot be written, hub revokes the token before it answers, and the plaintext is never returned or logged.
4. `GET /api/v1/workspaces/:slug/tokens` lists every token bound to the workspace, revoked and expired ones included. It never shows a secret or a hash.
5. `DELETE /api/v1/workspaces/:slug` revokes every live token bound to the workspace and removes its bindings, in the same database transaction that deletes the workspace row. The token's next request is `401`. Archiving and reactivating change nothing.
6. A sweep runs at start and then hourly. It removes bindings whose token apikit no longer holds. For bindings whose workspace no longer exists, it revokes the token and removes the binding.
7. `afc tokens create --workspace <slug>` mints a bound token and prints it once. `afc tokens list --workspace <slug>` lists the workspace's bound tokens. Without `--workspace`, both commands behave exactly as apikit's do today.
8. `hub.token.bind` is recorded once for every successful mint. `hub.token.bound_refused` is recorded for refusals caused by a binding, at most once per token per minute.

## Non-goals

- **Enforcing the binding.** The 404/403 rule, the guards, the git server check and the session/audit filtering belong to `33_token_workspace_binding`. This spec only adds the notification call at that spec's refusal points (Requirement 10).
- **Admin tokens minting on an owner's behalf.** apikit mints a PAT only for the authenticated user, and this repository has no exported apikit function that creates a PAT for another user. Admin tokens are refused when minting (Decision 3). They may still list.
- **Showing the binding in apikit's `GET /user/tokens` or in `afc tokens list` without `--workspace`.** apikit's listing and its CLI command stay unchanged.
- **Rebinding, unbinding or moving a binding.** A binding is written once and removed only when its workspace is deleted or the sweep removes it.
- **A hub route that revokes one bound token.** apikit's `DELETE /api/v1/user/tokens/:token_id` and the admin `DELETE /api/v1/users/:id/tokens/:token_id` already do that.
- **New permission scopes.** No new scopes are added, and existing scopes keep their meaning.
- **Changes to apikit's code** (its token table, creation, revocation or CLI source).
- **Bound tokens minting or listing bound tokens.** Their `tokens:*` scopes are inert (Decision 5).
- **A `--token-file` option** for `afc tokens create`.

## Background

The agent-fox service holds one hub credential per workspace it watches. Spec 33 lets hub confine a PAT to one workspace through a `token_workspace_bindings` row keyed by apikit's token id. Nothing yet writes such a row.

What exists today:

- **apikit's PAT handler** is `apikit/internal/handlers/pat.go`. It is internal, so hub cannot import it. It is mounted by `(*apikit.Server).MountHandlers` on the server's API group (`docs/errata/openapi_description.md`).
  - `POST <mount>/user/tokens` takes `{name, permissions, expires}`, where `expires` is 0, 30, 60 or 90. It answers `201 {token_id, name, token, permissions, expires_at, created_at}`.
  - For a PAT caller it requires `tokens:manage`, and it refuses scopes the creating PAT does not hold.
  - `DELETE <mount>/user/tokens/:token_id` revokes a token: it sets `revoked_at` and does not delete the row (`docs/api.md`, Tokens).
  - In-process `server.Echo().ServeHTTP` against apikit's real PAT route already works in `internal/sandboxes/smoke_test.go` (`newRealHub`).
- **apikit's PAT table** is `pats(token_id, user_id, name, secret_hash, permissions JSON, expires_days, expires_at, revoked_at, created_at)`. Hub already reads it in production in `internal/gitserver/auth.go` (`lookupPAT`: `revoked_at IS NULL AND (expires_at IS NULL OR datetime(expires_at) > datetime('now'))`).
- **Workspace deletion** is `handleDeleteWorkspace` (`internal/workspace/handlers.go`). It requires an archived workspace, then calls `deleteWorkspace(db, slug)` (`internal/workspace/store.go`). That function deletes the workspace row, patches, secrets and variables in one transaction, with no reliance on SQLite foreign keys. It then emits `hub.workspace.delete` with empty metadata.
- **Hub audit events** go through `audit.Emitter.Emit(ctx, audit.HubEvent)`. Event-type constants live in `internal/audit/types.go`. Events carrying a workspace are visible to its owner on `GET /api/v1/audit` and `/events`.
- **Periodic cleanup** follows the pattern of `forge.StartDeliveryRetention` (`internal/forge/retention.go`): one pass at once, then hourly, with errors logged.
- **The CLI** builds its tree in `cli.BuildRootCommand` (`internal/cli/workspace_cmd.go`), which adds `apikit.TokensCmd()` as is. Its `create` subcommand takes `--name`, `--permissions` (comma-separated) and `--expires` (default 90), and it also has a `list` subcommand (`docs/cli.md`).

## Requirements

### R1. Who may mint, and in what order the checks run

`POST /api/v1/workspaces/:slug/tokens` is mounted on the API group, behind apikit's authentication and spec 33's guard. It applies these checks in order:

1. No credential: `401 authentication required`.
2. A PAT bound to any workspace, the path's own included: `403` with `error_type: token_bound` and spec 33's message `this token is bound to workspace <S>`. The refusal is reported (R10). If the binding lookup fails, the answer is `500 internal server error`.
3. An admin token: `403 admin tokens cannot mint bound tokens; a real user is required as the token's owner`.
4. A workspace that does not exist, or that the caller does not own, according to spec 33's `wsaccess.AuthorizeWorkspace` plus an explicit `owner_id == auth.UserID` check: `404 workspace not found`.
5. A workspace that is not `active`: `400 workspace is not active`.
6. Otherwise the request is delegated to apikit (R2).

Hub does not check `tokens:manage` itself. apikit does, so its refusal reaches the caller unchanged.

### R2. Minting through apikit's own route

Hub builds an in-process request and serves it with the configured apikit handler, which is `server.Echo()` in production.

- **Method and path:** `POST <mount>/user/tokens`. `<mount>` is the matched route template (`c.Path()`) with the suffix `/workspaces/:slug/tokens` removed.
- **Body:** the caller's original request body, unchanged.
- **Headers:** the caller's `Authorization` header verbatim, plus `Content-Type` and `Content-Length`.
- **Context:** the caller's request context.

The response is captured in memory.

- **Any status other than `201`:** hub copies the status, `Content-Type` and body to the caller unchanged. It writes no binding and records no event.
- **A `201` whose body cannot be decoded, or has an empty `token_id` or `token`:** hub revokes the token if a `token_id` is known, answers `500 failed to bind the token to the workspace`, and logs the `token_id` only.

### R3. Writing the binding, answering, and rolling back

After a decodable `201`, hub inserts the binding row `(token_id, slug, created_at = apikit.NowUTC())`, but only if the workspace row still exists.

**On success** the answer is `201` with `Cache-Control: no-store` and this body:

```json
{"token_id","token","name","permissions","workspace","expires_at","created_at"}
```

All values except `workspace` come from apikit's response; `workspace` is the slug.

**On failure** hub revokes the token before answering. Failure means the insert errored, the key conflicted, or the workspace no longer exists. Revoking means setting `revoked_at = apikit.NowUTC()` on its `pats` row where `revoked_at IS NULL`. The answer is then:

- `404 workspace not found` if the workspace vanished;
- otherwise `500 failed to bind the token to the workspace`.

If the revocation also fails, hub logs both errors at error level with the `token_id`, and the answer stays the same. On no path does hub log the plaintext token, the response body of apikit's `201`, or the `Authorization` header.

### R4. Listing a workspace's bound tokens

`GET /api/v1/workspaces/:slug/tokens` applies these checks in order:

1. `401` without a credential.
2. `403 token_bound` for a bound PAT (reported per R10).
3. `403 PAT requires tokens:read scope` for a PAT holding neither `tokens:read` nor `tokens:manage`.
4. `404 workspace not found` unless the caller is an admin token or the workspace's owner. Archived workspaces are listed.

The answer is `200` with a JSON array, `[]` when there is nothing to list. Each entry is:

```json
{"token_id","name","permissions","workspace","expires_at","revoked_at","created_at"}
```

- The values come from the binding joined with apikit's `pats` row, ordered by the token's `created_at` and then `token_id`.
- `permissions` is the decoded JSON array: `[]` if it cannot be decoded.
- `expires_at` and `revoked_at` are `null` when unset.
- A binding whose `pats` row is gone is left out.
- A database error is `500 internal server error`.

### R5. Deleting a workspace retires its bound tokens

Inside `deleteWorkspace`'s existing transaction, before the workspace row is deleted, hub does three things:

1. It reads the token ids bound to the slug.
2. If there are any, it sets `revoked_at = apikit.NowUTC()` on those `pats` rows where `revoked_at IS NULL`.
3. It deletes every binding row of the slug.

If any step fails, the whole transaction rolls back. The workspace is not deleted, and the handler answers its existing `500 failed to delete workspace`. A workspace with no bindings never touches `pats`.

The `hub.workspace.delete` event gains the metadata key `revoked_tokens`: the sorted ids this deletion revoked, `[]` when none.

Archive and reactivate leave bindings and tokens untouched. After deletion, the revoked token gets `401` from apikit's REST authentication and from the git server.

### R6. The binding sweep

`StartBindingSweep(ctx, db)` runs one pass at once and then one every hour until `ctx` is cancelled. Each pass runs in one transaction:

- **(a)** For bindings whose slug has no `workspaces` row, it revokes the tokens that are still live, as in R5, and deletes those bindings.
- **(b)** It deletes bindings whose `token_id` has no `pats` row.

Bindings of revoked or expired tokens whose workspace still exists are kept, so R4 keeps listing them.

A failed pass is logged at error level and retried at the next tick. A pass cut short by shutdown is not logged as a failure. The sweep never stops hub from starting. `cmd/af-hub/main.go` starts it with `go` after the binding table exists.

### R7. `afc tokens create --workspace`

Hub adds `--workspace <slug>` to apikit's `tokens create` subcommand. Without the flag, apikit's own behaviour runs unchanged. With the flag:

- An empty `--workspace` value, or a missing or empty `--name` or `--permissions`, exits with code 2 before any request is sent.
- `--permissions` is split on commas, trimmed, and stripped of empty entries. If nothing is left, the exit code is 2.
- The command sends `POST /workspaces/<slug>/tokens` with `{name, permissions, expires}`, where `expires` is the value of `--expires`.
- On success it prints the response as one JSON document on stdout and writes one warning line on stderr saying the token will not be shown again.
- When the hub refuses, the exit code is 1 and the hub's message is shown. A missing API key or an unreachable hub is handled as in apikit's other commands (exit 2).

### R8. `afc tokens list --workspace`

Hub adds `--workspace <slug>` to apikit's `tokens list` subcommand. Without the flag, apikit's behaviour runs unchanged.

With the flag, the command sends `GET /workspaces/<slug>/tokens` and prints the array as one JSON document. An empty value exits with code 2, and a hub refusal exits with code 1. The output never contains a secret.

### R9. `hub.token.bind`

After a binding commits, hub emits one event with these fields:

| Field | Value |
|---|---|
| `event_type` | `hub.token.bind` |
| `resource_type` | `token` |
| `resource_id` | the `token_id` |
| `action` | `bind` |
| `workspace` | the slug |
| actor | the caller's user id and credential type |
| `metadata` | `token_id`, `workspace`, `permissions` and `expires_at` (`null` when the token has no expiry), all taken from apikit's response |

The event is emitted on a context the caller's disconnect does not cancel. It is never emitted for a refusal or a rollback. With a nil emitter, emission is skipped. An emit error is logged and does not change the response. The constant goes in `internal/audit/types.go`.

### R10. `hub.token.bound_refused`

`internal/wsaccess` gains a process-wide refusal observer: one function to register it and one to report a refusal. Reporting is a no-op when nothing is registered, and both functions are safe for concurrent use.

The report carries:

- the token id;
- the token's user id, when the refusal point has authenticated the token;
- the bound slug;
- the requested slug, when the request named one;
- the method;
- the route template (for git, the git route);
- the status (403 or 404);
- the surface (`api` or `git`).

Every refusal point of spec 33 calls the report function, and so do R1 and R4 of this spec. Filtering a list is not a refusal and is not reported.

`cmd/af-hub/main.go` registers a reporter that emits one event per report, subject to a rate limit:

| Field | Value |
|---|---|
| `event_type` | `hub.token.bound_refused` |
| `resource_type` | `token` |
| `resource_id` | the `token_id` |
| `action` | `refuse` |
| `workspace` | the bound slug |
| actor type | `pat` |
| actor id | the user id, empty when not supplied |
| `metadata` | `token_id`, `bound_workspace`, `requested_workspace` (only when present), `method`, `route`, `status`, `surface`, `suppressed` |

The rate limit is one event per token per rolling minute, measured from the last event emitted for that token. Reports inside the window are counted, and the count goes into the next event's `suppressed`, which is 0 when nothing was held back. A count that is never followed by another refusal is never recorded.

The limiter's memory is bounded: entries older than the window are dropped. Its clock can be injected for tests. Neither the token nor the `Authorization` header ever appears in the event or in a log.

### Wiring and documentation

A new package `internal/boundtoken` holds the handlers, the delegation, the sweep loop and the rate-limited reporter. It exposes `RegisterRoutes(api *echo.Group, cfg Config)`, where `Config` holds `DB *sql.DB`, `APIKit http.Handler` and `Audit audit.Emitter`. `main.go` calls it right after `workspace.MountWorkspaceHandlers`, with `server.APIGroup()`, `server.Echo()` and the audit emitter.

The SQL on `token_workspace_bindings` and the `pats` revocation live in `internal/wsaccess`, beside spec 33's table and lookup, so that `internal/workspace` can use them. The workspace schema initialisation makes sure the binding table exists, using spec 33's idempotent schema function.

Per the steering, update these documents:

- `docs/api.md` and `docs/openapi.yaml`: the two routes, both events, and `revoked_tokens` on `hub.workspace.delete`;
- `docs/cli.md`: `--workspace` on `tokens create|list`;
- `docs/permissions.md`: who may mint and list, and that `tokens:*` are inert on a bound token;
- `docs/architecture.md`: the package, the sweep, and the in-process delegation.

## Design Decisions

1. **Mint by in-process delegation to apikit's own `POST <mount>/user/tokens`, served by `server.Echo()`.** apikit's PAT handler is internal, and no exported creation function exists in this repository. Delegation keeps apikit's validation, expiry and escalation rules exactly as they are, and `newRealHub` already shows the in-process call works. A known side effect: the inner request passes through root middleware, so Prometheus counts it as a second request to `/user/tokens`.
2. **Derive `<mount>` from the matched route template instead of from configuration.** Both routes sit on the same API group, so trimming `/workspaces/:slug/tokens` from `c.Path()` is always right. It works the same in tests that mount at `/api/v1`.
3. **Refuse admin tokens when minting.** An admin token has no user, and apikit's route mints only for the authenticated user, so it cannot mint for the owner. This matches `handleCreateWorkspace`, which refuses admin tokens for the same reason. Admin tokens may list.
4. **Require the caller to be the owner, not just to pass `AuthorizeWorkspace`.** If org members are later given read access (hub PRD 04), that must not widen who can mint or see a workspace's tokens.
5. **Bound tokens cannot mint or list bound tokens (`403 token_bound`).** Their `tokens:*` scopes are inert, as the input states. A leaked bound token must not be able to mint replacements that outlive its own revocation.
6. **Do not pre-check `tokens:manage` on `POST`.** The input requires apikit's refusal to pass through unchanged. The ownership 404 still runs first, so nothing about other users' workspaces leaks.
7. **Forward the request body unchanged and parse only apikit's response.** Hub needs nothing from the request. Forwarding it adds no second validator and no extra body limit.
8. **Hub revokes by setting `pats.revoked_at` directly, both when rolling back and when deleting a workspace.** Two reasons rule out apikit's HTTP revocation. First, the deletion may run under a credential apikit's revocation would refuse (a PAT with `workspaces:delete` but no `tokens:manage`). Second, the revocation must commit atomically with removing the binding: a deleted binding beside a live token would turn it into an unbound PAT with reach over every workspace. Revoked is the same state apikit's own revocation leaves, and hub already depends on that column in `gitserver/auth.go`.
9. **The binding insert requires the workspace to still exist, and a failed insert revokes the token.** Between apikit's commit and hub's insert the token is live and unbound, but its plaintext exists only in hub's memory. Revoking on every failure path closes that window, and SQLite's serialised writes order the insert against a concurrent delete.
10. **Workspace deletion touches `pats` only when bindings exist, and does the binding steps before deleting the workspace row.** Existing test databases have no `pats` table, and if foreign keys were enabled a cascade on the workspace row would remove the bindings before they could be read.
11. **Keep bindings of revoked and expired tokens until their workspace is deleted.** Both states are final in apikit, so keeping the rows is safe, and it lets the listing show `revoked_at`. The sweep removes only rows whose token apikit no longer holds.
12. **The sweep also revokes and unbinds bindings whose workspace is gone.** It is a cheap safety net. Without it, a binding whose workspace vanished by some path other than `deleteWorkspace` would make a recreated slug reachable.
13. **Read the listing straight from a join with `pats`.** apikit's `GET /user/tokens` lists only the caller's own tokens, and admins have no per-workspace view. Hub already reads `pats` in production.
14. **Add `--workspace` to apikit's `tokens create` and `tokens list` by wrapping those cobra subcommands.** The input prefers this form, and it needs nothing from apikit beyond the command tree it already returns. A unit test fails if apikit's tree no longer has both subcommands.
15. **Add `revoked_tokens` to `hub.workspace.delete`, not a new per-token event.** The deletion is the act being audited, and one key records its effect on tokens.
16. **Rate-limit `hub.token.bound_refused` in memory, per token, carrying a `suppressed` count.** Hub is one process, a restart resetting the window does no harm, and the count keeps the information a flood would have carried.
17. **Put the refusal observer in `internal/wsaccess`.** It is the leaf package every refusal point already imports (workspace, gitserver, audit). The reporter lives in `boundtoken`, so wsaccess never imports audit.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `33_token_workspace_binding` | depends on, modifies | Uses its `token_workspace_bindings` table and idempotent schema function, its bound-slug lookup for a PAT, its `token_bound` error type and message, and its guard on the API group. Adds the binding-store functions beside its lookup in `internal/wsaccess`, and the refusal-report call at each of its refusal points. |

## Verified External API

apikit (`github.com/txsvc/apikit`, replaced by `../apikit`, outside the repository) cannot be read here. The signatures below come from call sites in this repository, or from the docs written against apikit's source, as marked.

| Symbol | Signature / behaviour | Source |
|---|---|---|
| `apikit.GetAuthInfo` | `func(c echo.Context) *apikit.AuthInfo` (nil when unauthenticated) | call sites throughout `internal/` |
| `apikit.AuthInfo` | fields `CredentialType`, `UserID`, `Role`, `TokenID`, `KeyID`, `Permissions []string` | constructed in `internal/gitserver/auth.go` |
| `apikit.WriteAPIError` / `WriteAPIErrorWithType` | `(c echo.Context, code int, msg string) error` / `(c, code, msg, errorType string) error` | `internal/sandboxes/routes.go`, `internal/audit/auth.go` |
| `apikit.NowUTC` / `apikit.FormatUTC` | `func() string` / `func(time.Time) string` | `internal/workspace/handlers.go`, `internal/forge/retention.go` |
| `(*apikit.Server).Echo` / `APIGroup` | `func() *echo.Echo` / `func() *echo.Group` | `cmd/af-hub/main.go` |
| `apikit.GenerateAPIKey` | `func(db *sql.DB, userID string, expiresDays int, logger) (key, error)`, where `key.FullKey` is the plaintext | `internal/sandboxes/smoke_test.go` |
| `apikit.TokensCmd` | `func() *cobra.Command`; subcommands `create` (`--name`, `--permissions`, `--expires`, default 90) and `list` | `internal/cli/workspace_cmd.go`; subcommand and flag names from `docs/cli.md`, **unverified** against source |
| `apikit.CLIClientFromCmd`, `client.DoRequest`, `apikit.CLIPrintResult`, `apikit.CLIHandleError`, `apikit.NewCLIError` | `(cmd) (client, error)`; `(ctx, method, path string, body any) (result, error)`; `(cmd, v) error`; `(cmd, err) error`; `(code int, msg string) error` | `internal/cli/sandbox_cmd.go` |
| `POST <mount>/user/tokens` | body `{name, permissions, expires}` → `201 {token_id, name, token, permissions, expires_at, created_at}`; 400/401/403 otherwise | `docs/api.md`, `docs/errata/openapi_description.md` (written from apikit source) |
| `pats` table | `token_id` PK, `user_id`, `name`, `secret_hash`, `permissions` (JSON), `expires_days`, `expires_at`, `revoked_at`, `created_at` | production query in `internal/gitserver/auth.go`; mirror in `internal/gitserver/testhelpers_test.go` |
| apikit function that creates a PAT for a given user | **NOT FOUND**; minting is delegated over the in-process route instead (Decision 1) | — |
| `(*echo.Echo).ServeHTTP`, `echo.Context.Path()` | standard echo v4 (`github.com/labstack/echo/v4 v4.16.0`); `Path()` returns the full template including the group prefix | `go.mod`; spec 33's notes |
