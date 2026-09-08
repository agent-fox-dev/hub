# Errata: OpenAPI description of the REST API

`docs/openapi.yaml` is an OpenAPI 3.1 description of the hub's HTTP surface. It
was built by reading handler source -- both this repository and apikit
(`github.com/txsvc/apikit`, resolved through the `replace` directive in
`go.mod`) -- and cross-checking against `docs/api.md` and
`docs/permissions.md`. This erratum records where the prose docs disagree with
the code. In every case the OpenAPI document follows the code.

## 1. Every apikit endpoint is missing its `/api/v1` prefix in the prose docs

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
So the real paths are `/api/v1/user`, `/api/v1/user/keys`,
`/api/v1/user/tokens`, `/api/v1/users`, `/api/v1/orgs`, `/api/v1/auth/callback`
-- not the root-level `/user`, `/users`, `/orgs` that both `docs/api.md` and
`docs/permissions.md` show throughout.

Only `/healthz`, `/readyz`, and `/version` are registered on the root instance,
outside both the mount point and the auth middleware. Hub's own `/metrics` and
`/git/...` routes are likewise mounted on the root instance.

This also explains the hub's own paths: `/api/v1/user/secrets` and
`/api/v1/orgs/:slug/secrets` sit alongside apikit's `/api/v1/user` and
`/api/v1/orgs/:id`, which is why hub's org-scoped secrets and variables live
under `/orgs` at all.

Note the parameter mismatch that results: apikit addresses organizations by
**id** (`/api/v1/orgs/:id`) while hub's secrets and variables handlers address
them by **slug** (`/api/v1/orgs/:slug/secrets`). Echo stores parameter names
per route, so both resolve correctly, but a client cannot assume one identifier
works for both.

## 2. `docs/api.md`'s "Non-Workspace Endpoints (apikit-provided)" section is wrong

Beyond the missing prefix, most of that section does not correspond to any
route apikit registers.

| `docs/api.md` claims | Actually |
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

`docs/permissions.md` gets the route *names* right (it was clearly written
against apikit source) and additionally documents endpoints `docs/api.md` omits
entirely: `GET /user/tokens/:token_id`, the three
`/user/tokens/:token_id/permissions` verbs, `POST /user/keys/:key_id/refresh`,
the `promote`/`demote`/`block`/`unblock` user actions, and the organization
membership endpoints. Its only error is the missing prefix.

Two routes appear in neither document: `GET /version` and
`GET /api/v1/auth/providers`. Both are in `docs/openapi.yaml`.

## 3. `POST /api/v1/workspaces/:slug/sync` returns two different bodies

`docs/api.md` presents the carry-patch fields (`patches_merged`,
`rebuild_triggered`, `rebuild_job_id`, `force_push_detected`) as additions to
the workspace response body, and says they are "omitted from the response" for
standard workspaces -- implying one schema with optional fields.

The code returns two disjoint bodies. `handleSyncWorkspace`
(`internal/workspace/sync_handler.go`) delegates to the carry-patch sync hook
before the standard sync flow runs; when the hook handles the request the
handler returns immediately, so the workspace object is never serialised:

```go
if ws.WorkspaceMode == "carry_patch" && carryPatchSyncHook != nil {
    handled, hookErr := carryPatchSyncHook(c, slug, repoPath)
    ...
    if handled {
        return nil
    }
}
```

`handleCarryPatchSyncEndpoint` (`internal/carrypatch/sync_handlers.go`) then
writes a bare `CarryPatchSyncResponse`. So:

- `standard` workspace -> the full workspace object;
- `carry_patch` workspace -> only the four carry-patch fields.

The OpenAPI document models the `200` response as a `oneOf` over `Workspace`,
`CarryPatchSyncResponse`, and `SyncStatusAck`. The third arm covers
`handleCarryPatchSyncEndpoint`'s `{"status": "synced"}` early return, reachable
only if that handler is mounted directly as a route (via `RegisterSyncRoutes`)
rather than through the hook. The shipped binary wires the hook, so a standard
workspace always takes the workspace-object path.

## 4. `POST /api/v1/sessions/:id/complete` accepts two undocumented fields

`docs/api.md` lists `status`, `error_message`, `duration_ms`, and
`cache_creation_input_tokens`. `audit.CompleteSessionRequest`
(`internal/audit/types.go`) also binds `input_tokens` and `output_tokens`. Both
are in the OpenAPI request schema.

## 5. Scope and ownership notes that `docs/api.md` omits

`docs/api.md`'s scope table is correct as far as it goes, but three behaviours
documented only in `docs/permissions.md` are load-bearing for a client:

- `POST /api/v1/workspaces/:slug/sync` accepts `workspaces:write` as well as
  `workspaces:sync` **when the workspace is in `carry_patch` mode** -- the
  carry-patch handler checks `hasScope(auth, "workspaces:sync",
  "workspaces:write")`. The standard sync path and reclone require
  `workspaces:sync` exclusively.
- `GET /api/v1/workspaces/:slug/rerere`, `DELETE
  /api/v1/workspaces/:slug/rerere/*pathspec`, and
  `GET /api/v1/workspaces/:slug/patch-status` test for the **literal**
  `workspaces:read` / `workspaces:write` scope. The
  `workspaces:create`-implies-`workspaces:read` shortcut applies only to the
  workspace list and get handlers.
- Ownership is unenforced on the sync, reclone, patch, merge, rebuild, rerere,
  patch-status, and audit-read handlers. Only workspace CRUD, secrets,
  variables, session reads, and the git server check `owner_id`.

All three are recorded on the operations they affect.

## 6. `POST /api/v1/workspaces/:slug/rebuilds/:id/rollback` is not mounted

`docs/api.md` already flags this. `carrypatch.RegisterRebuildRollbackRoutes`
implements the handler but `cmd/af-hub/main.go` never calls it, so the route
answers `404` in the shipped binary. The operation is described and marked
`x-route-registered: false`.

## Verification

The document is valid OpenAPI 3.1, checked with `openapi-spec-validator` 0.9.0.
No validator runs in `make check`, because the repository carries no Python or
Node toolchain. To check it manually:

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
exactly in both directions: 84 hub-owned operations plus 40 apikit operations,
124 in total across 85 paths.
