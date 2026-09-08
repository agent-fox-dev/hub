# Errata: OpenAPI description of the REST API

`docs/openapi.yaml` is an OpenAPI 3.1 description of the hub's HTTP surface. It
was built by reading handler source -- both this repository and apikit
(`github.com/txsvc/apikit`, resolved through the `replace` directive in
`go.mod`) -- and cross-checking against `docs/api.md` and
`docs/permissions.md`.

Writing it surfaced six divergences between the prose docs and the handlers.
All six have since been resolved: four were documentation errors, two were
implementation bugs. This erratum records what was wrong and how it was
settled, so the reasoning survives beyond the diff.

## Resolved

### 1. Every apikit endpoint was missing its `/api/v1` prefix — *docs fixed*

`apikit.Server.MountHandlers` registers **all** of its handlers on the server's
API group, not on the root Echo instance (`apikit/server.go`):

```go
api := s.APIGroup()
oauth.RegisterOAuthHandlers(api, ...)
handlers.RegisterUserHandlers(api, ...)
handlers.RegisterOrgHandlers(api, ...)
keys.RegisterKeyHandlers(api, ...)
handlers.NewPATHandler(database, permReg).RegisterRoutes(api)
```

That group is mounted at `cfg.Server.MountPoint`, which defaults to `/api/v1`.
The real paths are therefore `/api/v1/user`, `/api/v1/user/keys`,
`/api/v1/user/tokens`, `/api/v1/users`, `/api/v1/orgs`, and
`/api/v1/auth/callback` -- not the root-level `/user`, `/users`, `/orgs` that
both `docs/api.md` and `docs/permissions.md` showed throughout.

Only `GET /healthz`, `GET /readyz`, and `GET /version` are registered on the
root instance, outside both the mount point and the auth middleware. Hub's own
`/metrics` and `/git/...` routes are likewise on the root instance.

This also explains hub's own layout: `/api/v1/user/secrets` and
`/api/v1/orgs/:slug/secrets` sit alongside apikit's `/api/v1/user` and
`/api/v1/orgs/:id`.

**Resolution.** Both documents now carry the prefix, and each states the mount
point explicitly. One asymmetry is worth keeping in mind and is now called out
in `docs/api.md`: apikit addresses organizations by **id**
(`/api/v1/orgs/:id`) while hub's secrets and variables handlers address them by
**slug** (`/api/v1/orgs/:slug/secrets`). Echo stores parameter names per route
so both resolve correctly, but a client cannot assume one identifier works for
both.

### 2. `docs/api.md`'s apikit section described routes that do not exist — *docs fixed*

Beyond the missing prefix, most of the old "Non-Workspace Endpoints
(apikit-provided)" section did not correspond to any route apikit registers.

| The old text claimed | Actually |
|---|---|
| `POST /login` with email + password | No such route. API keys are issued **only** by `POST /api/v1/auth/callback`, the OAuth code exchange. There is no password authentication anywhere in apikit. |
| `PUT /user` | The route is `PATCH /api/v1/user`. |
| `POST /user/keys` creates an API key | No such route. `/api/v1/user/keys` is `GET` only; keys are created by the OAuth callback and rotated by `POST /api/v1/user/keys/:key_id/refresh`. |
| `DELETE /user/keys/:id` | The parameter is `:key_id`. |
| `POST /user/tokens` takes `{description, scopes}` | `CreatePATRequest` is `{name, permissions, expires}` (`apikit/internal/handlers/pat.go`). `expires` must be 0, 30, 60, or 90. |
| `DELETE /user/tokens/:id` returns `204` | The parameter is `:token_id`, and `revokePAT` returns `200` with the token metadata. |
| `GET /admin/users`, `GET /admin/stats`, `DELETE /admin/users/:id` | There is no `/admin` prefix and no stats endpoint. Administration lives at `/api/v1/users` and `/api/v1/orgs`. |
| `POST /orgs` takes `{name, slug}` | `CreateOrgRequest` is `{name, slug, url, owner_id}`. |
| `GET /orgs/:slug` | The route is `GET /api/v1/orgs/:id`. |

`docs/permissions.md` had the route *names* right -- it was clearly written
against apikit source -- and documented endpoints `docs/api.md` omitted
entirely: `GET /user/tokens/:token_id`, the three
`/user/tokens/:token_id/permissions` verbs, `POST /user/keys/:key_id/refresh`,
the `promote`/`demote`/`block`/`unblock` user actions, and the organization
membership endpoints. Its only error was the missing prefix.

Two routes appeared in neither document: `GET /version` and
`GET /api/v1/auth/providers`.

**Resolution.** `docs/api.md`'s section was rewritten from apikit source. It
now documents the real routes, request bodies, and status codes, including the
two that were missing. Both `/auth` routes are public: `RegisterOAuthHandlers`
runs before `api.Use(authMiddleware)`, and Echo snapshots a group's middleware
when a route is added, so neither carries the auth middleware.

### 3. `POST /api/v1/workspaces/:slug/sync` dropped the standard sync fields — *code fixed*

16-REQ-5.1 specifies the return contract as:

> sync response includes patches_merged (list of branch names) and
> rebuild_triggered (boolean) **in addition to standard sync fields**

The implementation returned only the carry-patch fields. `handleSyncWorkspace`
delegated to the carry-patch hook, and the hook wrote its own body and returned
`(true, nil)`, so the handler returned immediately and the workspace record was
never serialised. `docs/cli.md` had it right -- `afc workspace sync` claims to
print "the updated workspace JSON" -- but for a carry-patch workspace it
printed four fields and nothing else.

**Resolution.** The hook contract changed: `CarryPatchSyncFunc` now returns the
carry-patch fields as a `map[string]any` rather than writing a response, and
`handleSyncWorkspace` merges them into the workspace JSON via
`respondWorkspaceWithExtras`. The workspace row is re-read first, so
`upstream_head_sha` and `last_sync_at` reflect the hook's writes.

The old code also had a `{"status": "synced"}` stub for the standard-workspace
case, which is not the "standard sync response" 16-REQ-5.E4 calls for. The hook
now reports "not handled" for a non-carry-patch workspace and the request falls
through to the standard sync flow.

`RegisterSyncRoutes` still mounts the carry-patch logic as a standalone route
for the carrypatch package's own tests. The server binary does not use it --
only the hook -- because the workspace record is rendered by the workspace
package. That is documented on the function.

Covered by `internal/workspace/sync_carry_patch_response_test.go`.

### 4. `POST /api/v1/sessions/:id/complete` bound two dead fields — *code fixed*

`audit.CompleteSessionRequest` declared `input_tokens` and `output_tokens`
alongside `cache_creation_input_tokens`. Nothing read them: `CompleteSession`
persists only status, `ended_at`, `error_message`, `duration_ms`, and
`cache_creation_input_tokens`, which is exactly what 19-REQ-2.1 specifies. A
client sending token counts to this endpoint got a silent no-op.

**Resolution.** Both fields were removed from the struct. Callers with token
counts to report should use `POST /api/v1/sessions/:id/usage`, which records
them. Existing clients are unaffected -- `encoding/json` already ignored the
extra keys.

### 5. Scope and ownership behaviour was undocumented in `docs/api.md` — *docs fixed*

Three behaviours documented only in `docs/permissions.md` are load-bearing for
a client:

- `POST /api/v1/workspaces/:slug/sync` accepts `workspaces:write` as well as
  `workspaces:sync` **when the workspace is in `carry_patch` mode**. The
  standard sync path and reclone require `workspaces:sync` exclusively.
- `GET /api/v1/workspaces/:slug/rerere`, `DELETE
  /api/v1/workspaces/:slug/rerere/*pathspec`, and
  `GET /api/v1/workspaces/:slug/patch-status` test for the **literal**
  `workspaces:read` / `workspaces:write` scope. The
  `workspaces:create`-implies-`workspaces:read` shortcut applies only to the
  workspace list and get handlers.
- Ownership is unenforced on the sync, reclone, patch, merge, rebuild, rerere,
  patch-status, and audit-read handlers. Only workspace CRUD, secrets,
  variables, session reads, and the git server check `owner_id`.

**Resolution.** `docs/api.md` gained "Scopes Checked Literally" and "Workspace
Ownership" sections covering all three, and each operation in
`docs/openapi.yaml` records the scopes it accepts in `x-required-scopes`.

The unenforced ownership is **behaviour, not a documentation gap**, and it was
left as-is deliberately: no spec requires ownership on those handlers, and
changing it would break any caller that relies on cross-workspace access
today. It is now written down rather than implied. Whether it should stay that
way is a design question worth deciding on its own.

### 6. `POST /api/v1/workspaces/:slug/rebuilds/:id/rollback` was not mounted — *code fixed*

`carrypatch.RegisterRebuildRollbackRoutes` implemented the handler, and it had
seven passing tests, but `cmd/af-hub/main.go` never called it. The route
answered `404` in the shipped binary, which also meant the documented
`afc rebuild rollback` CLI command could not work.

**Resolution.** The route is registered in `cmd/af-hub/main.go` alongside the
other carry-patch routes, wired with the same DB, queue, workspace root, git
runner factory, and audit emitter. The "not registered in production" warning
is gone from `docs/api.md` and `docs/openapi.yaml`.

## Verification

`docs/openapi.yaml` is valid OpenAPI 3.1, checked with
`openapi-spec-validator` 0.9.0. No validator runs in `make check`, because the
repository carries no Python or Node toolchain. To check it manually:

```sh
pip install openapi-spec-validator
python3 -c "
from openapi_spec_validator import validate
from openapi_spec_validator.readers import read_from_filename
validate(read_from_filename('docs/openapi.yaml')[0])
print('valid')
"
```

Route coverage was verified by extracting every `(*echo.Group).VERB("path")`
and `(*echo.Echo).VERB("path")` registration from the non-test Go sources of
both repositories and diffing against the document's paths. The sets match
exactly in both directions: 84 hub operations plus 40 apikit operations, 124 in
total across 85 paths.
