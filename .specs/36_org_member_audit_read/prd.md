---
spec_id: "36"
spec_name: "org_member_audit_read"
title: "Let org members read a workspace's sessions, cost and audit"
status: "active"
created_at: "2026-10-21T13:20:46.080272Z"
updated_at: "2026-10-21T13:20:46.080272Z"
intent_hash: "d5dcc58b294895ce06eafe6aebe600632df653aedd61d2f636faddf037b9612b"
schema_version: 2
source: "docs/prd/04-let-org-members-read-org-workspaces.md"
---
## Intent

Members of a workspace's organization can see what agents did and spent on that workspace. This spec gives them read access to the workspace's agent sessions, token usage, cost, per-run audit records and transcripts, and includes the workspace in the unified audit query and the live event stream they see. Opening sessions, completing them, reporting usage and ingesting audit data stay with the workspace's owner and with admin tokens, and hub events that belong to no workspace stay visible to admin tokens only. Every decision goes through hub's shared workspace access rule.

## Goals

1. A user U2 who is a member of org O can read workspace W, which U1 owns in O. Each of these answers 200 for W with the scope it needs today:
   - `GET /api/v1/workspaces/:slug/runs/:run_id/{events,sessions/outcomes,tools/calls,tools/errors,traces,postmortem}`
   - `GET .../runs/:run_id/transcript`
   - `GET /api/v1/workspaces/:slug/cost`
   - `GET /api/v1/sessions/:id` and `GET /api/v1/sessions/:id/usage` for a session in W
   - `GET /api/v1/sessions?workspace_slug=W`
2. `GET /api/v1/sessions`, `GET /api/v1/audit` and `GET /api/v1/events` show U2 the sessions and events of W next to U2's own. They never show a workspace U2 may not read, and never a hub event that has no workspace.
3. U2 changes nothing in W. `POST /api/v1/sessions` for W answers `403` with `error_type: forbidden`. Every audit ingestion route of W answers `403 workspace_access_denied`. Completing U1's session, or reporting usage on it, answers `403 not session owner`. None of these writes a row.
4. A caller who is not W's owner, not a member of W's org and not an admin token gets exactly the answer they get today on every route in this spec.
5. Every existing test in `internal/audit` passes with no assertion changed. Fixtures may gain columns or tables. This includes the binding tests (TS-33-35 to 39, 46 to 53) and the refusal-report test TS-34-58.
6. No read decision in `internal/audit` compares `owner_id` with the caller. Single-workspace reads go through `wsaccess.AuthorizeWorkspace` with the read need. Workspace sets come from `wsaccess.ReadableSlugs`.

## Non-goals

- **The access rule itself, the workspace REST API and the listing.** Spec `org_member_workspace_access` (35) delivers these: `Access` levels, `AuthorizeWorkspace` with a need, `ReadableSlugs`, `access`/`org_slug` on workspace objects, and every non-audit REST route. This spec only uses them.
- **The git server.** Clone and fetch for members, and refusing their push, are the scope of `org_member_git_read`.
- **Members opening or completing sessions, reporting usage or ingesting audit data.** These stay owner-only. Ingestion's checks and answers do not change at all.
- **Changing how session reads refuse.** They keep `403 workspace access denied` for callers who may not read. Making them anti-enumerating `404`s is not part of this work.
- **Session reads on archived workspaces.** They stay refused, for members as for owners, as they are today.
- **Re-checking access during a live SSE connection.** The set of readable workspaces is fixed when the stream opens.
- **Redacting individual event types for members.** Every event that carries the workspace is visible to anyone who may read it.
- **Org roles**, ownership transfer, and any change to what a scope means.
- **The Prometheus metrics endpoint** and its workspace labels.

## Background

Today the audit package decides access with its own owner comparisons:

| Helper | File | Today | Used by |
|---|---|---|---|
| `requireWorkspaceRead` | `internal/audit/auth.go` | `SELECT owner_id FROM workspaces WHERE slug=?`. Admin, or owner, then `DeniesWorkspace(..., SiteOf(c,404))`. Otherwise `404 workspace not found`. Skipped when `sqliteDB` is nil. | the 6 per-run GETs, transcript, **and** `POST /sessions` plus its replay check |
| `ownedWorkspaces` / `readableWorkspaces` | `auth.go` | the owned slug set (nil for admin), confined to the bound slug for a bound PAT. A requested `workspace` outside the set gets `DeniesWorkspace` (report) and then `404`. | `GET /audit`, `GET /events` |
| `hasWorkspaceAccess` | `internal/audit/handlers.go` | `owner_id` of an **active** workspace equals the caller, then `DeniesWorkspace(..., SiteOf(c,403))`. Any error means false. | session get, usage and cost, and the list with `workspace_slug`. A false result is `403 workspace access denied`. |
| `getAccessibleWorkspaces` | `handlers.go` | owned **active** slugs, never nil, confined for a bound PAT | session list without a slug |
| `checkWorkspaceAccess` | `auth.go` | existence 404, then binding 404, then archived 409, then owner, else `403 workspace_access_denied` | the 8 ingestion POSTs |
| the session-creator check | `handlers.go` | `auth.UserID != sess.CredentialID` gives `403 not session owner` | complete, usage report |

The routes are registered in `internal/audit/routes.go` (`RegisterSessionRoutes`, `RegisterRoutes`) and `internal/audit/query_routes.go` (`RegisterAuditQueryRoutes`). `cmd/af-hub/main.go` wires them with `database.SqlDB`. The SSE filter is `sseFilters.allowedWorkspaces` in `internal/audit/sse.go`: a nil map means unrestricted, and events with an empty workspace go only to unrestricted connections. The unified query turns the set into `workspace IN (...)` (`allowedWorkspacesCondition`), which also leaves out workspace-less hub events. Secret and variable events (`internal/secrets/handler_audit_config.go`) carry no workspace, so they are already admin-only. A member therefore never sees a secret's name through audit.

Membership lives in apikit's `org_members(org_id, user_id)` and `workspaces.org_id`. Spec 35 makes `wsaccess` compute the caller's level (`admin`, `owner`, `member`, none) from these and exposes `ReadableSlugs`. The documentation describes all of the above as owner-only: `docs/api.md` (Workspace Ownership, the sessions list, `GET /audit`, `GET /events`), `docs/permissions.md` (`sessions:read`, and the audit "Ownership notes", which wrongly say audit reads are not enforced), `docs/openapi.yaml` (`listSessions`, and `queryRunAuditEvents`, which wrongly says "Ownership is not enforced"), `docs/architecture.md` (the helper list under token workspace bindings) and `docs/errata/ownership_enforcement.md`.

## Requirements

**R1. Per-run audit reads and the transcript.**
- Routes: `GET /workspaces/:slug/runs/:run_id/events`, `/sessions/outcomes`, `/tools/calls`, `/tools/errors`, `/traces`, `/postmortem`, and `/transcript`.
- Each grants access to an admin token, the owner and a member of the workspace's org, and to nobody else. The decision goes through `wsaccess.AuthorizeWorkspace` with the read need.
- The `audit:read` scope check stays first, and its 403 is unchanged.
- A workspace that does not exist, or that the caller may not read, answers `404 workspace not found`, as today.
- The workspace's status does not matter: an archived workspace stays readable, as it is for owners today.
- Validation after the check is unchanged: the `run_id` format, `node_id is required` on the transcript, and `postmortem_not_found`.
- With no SQLite handle the check is skipped, as today.

**R2. Single-workspace session and cost reads.**
- Routes: `GET /sessions/:id`, `GET /sessions/:id/usage`, `GET /workspaces/:slug/cost`, and `GET /sessions?workspace_slug=W`.
- Each grants access when the caller may read the workspace (admin, owner or member) **and** the workspace is `active`.
- Otherwise the answer is `403 workspace access denied`, as today. That covers a missing workspace, an archived one, one the caller may not read, and one outside a bound token's binding.
- `404 session not found` stays ahead of the workspace check on get and usage.
- The `sessions:read` scope check stays first.
- With no SQLite handle: get and usage skip the check, and cost refuses non-admins with 403, as today.

**R3. Session list without a workspace.**
- `GET /sessions` with no `workspace_slug` lists, for a non-admin, the sessions of every active workspace the caller may read: owned plus member.
- The restriction list is never nil. A caller with no such workspace gets an empty list.
- A bound PAT lists at most its bound workspace's sessions.
- Admin tokens stay unrestricted.
- Filters, ordering and cursors are unchanged.

**R4. Unified audit query.**
- `GET /audit` restricts a non-admin to the events of every workspace the caller may read, whatever its status. The set comes from `wsaccess.ReadableSlugs`.
- Hub events with an empty workspace stay admin-only.
- A `workspace` filter naming a workspace outside the set answers `404 workspace not found`. When the caller's binding is the cause, it is reported as today.
- Admin tokens stay unrestricted.

**R5. SSE stream.**
- `GET /events` uses the same readable set as R4. It is computed once when the connection opens and kept for the life of the connection.
- A `workspace` filter outside the set answers `404 workspace not found` before any stream header is written.
- Events with an empty workspace reach only admin tokens.
- The connection limit, heartbeat and stale reaping are unchanged.

**R6. Opening a session.**
- `POST /sessions` needs **write** access to `workspace_slug`, decided through `wsaccess.AuthorizeWorkspace` with the write need.
  - The owner and admin tokens may open.
  - A member gets the rule's refusal for a reader asking to write: `403` with `error_type: forbidden`.
  - Everyone else, and an unknown slug, gets `404 workspace not found`, as today.
- Idempotent replay of an existing id whose session belongs to another workspace returns that session only when the caller may **read** that workspace (owner or member). Otherwise the answer is `404 workspace not found`, as today.
- Status is not checked, as today.

**R7. Writes that do not change.**
- `POST /sessions/:id/complete` and `POST /sessions/:id/usage` keep their session-creator check and their answers (`403 not session owner`).
- The eight ingestion routes keep `checkWorkspaceAccess` with its order and answers. A member is refused there like any non-owner: `403 workspace_access_denied`.
- No member can be a session's creator, because opening a session needs ownership.

**R8. Bound tokens and refusal reports.**
- A bound PAT is confined exactly as today, whether its user is an owner or a member.
- It reads its bound workspace if its user may read that workspace.
- Every other workspace is refused with the route's usual refusal: 404 on per-run reads, transcript, `POST /sessions` and the audit `workspace` filter; `403 workspace access denied` on session reads.
- Each refused request produces exactly one `wsaccess` refusal report. The report carries the request's method, route template and the status answered, as TS-34-58 asserts today.
- List filtering produces no report.

**R9. Failures.**
- A database error while deciding access never grants access:
  - per-run reads, the transcript and `POST /sessions` answer `500 internal server error`;
  - session reads keep their fail-closed `403 workspace access denied`;
  - `GET /audit` answers `500`;
  - `GET /events` answers `500` before any stream header is written.
- A failed binding lookup is treated the same way.
- The membership read for a caller who is not the owner is the one extra query this spec adds per request.

**R10. Compatibility.**
- A workspace in a personal org, whose only member is its owner, answers every existing test unchanged. So does a workspace without an `org_id`.
- The shared SQLite test fixture (`openTestSQLiteDB` in `internal/audit/testhelpers_test.go`) gains the columns and tables the spec-35 rule reads: at least `org_id` and the `org_members` table.
- No existing assertion changes.
- `docs/api.md`, `docs/permissions.md`, `docs/openapi.yaml`, `docs/architecture.md` and `docs/errata/ownership_enforcement.md` describe members' read access to sessions, cost and audit. They also correct the stale notes that audit reads do not enforce ownership.

## Design Decisions

1. **Session reads keep `403 workspace access denied` for non-readers.** TS-33-36, TS-34-58 and published clients depend on that answer. Changing it to an anti-enumerating 404 is a separate decision from letting members read.
2. **Session reads keep the `active` restriction, for members too.** Owners cannot read the sessions of an archived workspace today. Members get exactly what owners get, no more.
3. **Per-run reads and the transcript use `AuthorizeWorkspace` with the read need, with no status check.** This keeps today's behaviour (archived workspaces readable) and puts the rule in one place.
4. **A member opening a session gets `403 forbidden`, not 404.** The member already knows the workspace exists. Spec 35's rule answers a reader asking to write with 403 `forbidden`, and this route should match it.
5. **Replay disclosure uses the read need.** Returning an existing session record is a read, and a member could fetch the same record with `GET /sessions/:id`.
6. **Complete and usage-report keep the session-creator check.** Only the workspace's owner or an admin token can open a session, so only they can be its creator. Members are already refused.
7. **Audit ingestion keeps `checkWorkspaceAccess` unchanged.** Its answers (archived 409 before ownership, `403 workspace_access_denied` for strangers) are a published contract asserted by TS-17 and TS-33 tests. `AuthorizeWorkspace` would turn the stranger's 403 into a 404. It is a write path and already refuses members with a 403.
8. **The unified query and the SSE stream use `wsaccess.ReadableSlugs`, including archived workspaces.** Owners see archived workspaces' events today, and members see what owners see.
9. **The SSE readable set is fixed at connect.** apikit authenticates a stream only when it opens, so a revoked credential already keeps an open stream alive. Re-checking membership mid-stream would add per-connection database work under the fan-out lock, and nothing asks for it.
10. **Audit helpers check the binding first, with the request's site.** For a single workspace the helper calls `wsaccess.DeniesWorkspace(db, auth, slug, wsaccess.SiteOf(c, status))` before it asks the rule. A bound token for another slug is then refused and reported with the method, route and status TS-34-58 expects. The rule's own binding check afterwards passes silently (bound to this slug, or unbound), so there is no second report. The input asks for exactly this order ("a bound PAT is checked first"). One consequence: a bound token naming an unknown or unreadable slug is also reported. `GET /audit` already does this today.
11. **No per-event redaction.** Hub events are designed to carry no secret values, and the forge, sandbox and token events say so. Secret and variable events carry no workspace and stay admin-only. Members see actor ids, bound-token ids and cost, which the input accepts as the point of team visibility.
12. **Failures fail closed with each route's existing refusal shape.** Per-run lookups that used to turn a database error into 404 now answer 500, because they now come from `AuthorizeWorkspace`. This changes only real database failures, and it matches the rest of hub.
13. **Helper shape.**
    - `ownedWorkspaces` is removed and `readableWorkspaces` is built on `ReadableSlugs`.
    - `hasWorkspaceAccess` and `getAccessibleWorkspaces` keep their names and contracts (bool; never-nil list) and are rebuilt over the rule.
    - `POST /sessions` gets its own write helper. It no longer reuses `requireWorkspaceRead`, which now means read.
    - `checkWorkspaceAccess` and the session-creator check stay as they are.
14. **A nil SQLite handle keeps today's behaviour** on every route. Unit tests that register routes without SQLite rely on it.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `35_org_member_workspace_access` | depends on | Provides `wsaccess.AuthorizeWorkspace` with a need (read/write), the member level, the `403 forbidden` refusal for a reader asking to write, and `wsaccess.ReadableSlugs`. This spec calls them from `internal/audit`. |
| `33_token_workspace_binding` | preserves | Bound-token confinement on the session and audit routes (TS-33-35 to 39, 46 to 53) must answer exactly as today. |
| `34_bound_token_issuance` | preserves | Binding refusals on session and audit routes must still be reported once, with method, route and status (34-REQ-10.3, TS-34-58). |

## Verified External API

apikit is replaced by `../apikit` (`go.mod`: `replace github.com/txsvc/apikit => ../apikit`), outside the repository. These signatures come from existing call sites in `internal/audit` and are **unverified** against the source:

- `apikit.GetAuthInfo(c echo.Context) *apikit.AuthInfo`
- `apikit.WriteAPIError(c echo.Context, status int, message string) error`
- `apikit.WriteAPIErrorWithType(c echo.Context, status int, message, errorType string) error`
- `apikit.AuthInfo` fields used: `CredentialType string` (`"admin_token"`, `"api_key"`, `"pat"`), `UserID string`, `TokenID string`, `Permissions []string`

In-repo `internal/wsaccess`, verified in the tree today:

- `DeniesWorkspace(db *sql.DB, auth *apikit.AuthInfo, slug string, site RefusalSite) (bool, error)`
- `SiteOf(c echo.Context, status int) RefusalSite`
- `BoundSlug(db *sql.DB, auth *apikit.AuthInfo) (string, bool, error)`
- `type RefusalSite struct{ Method, Route string; Status int; Surface string }`

Delivered by spec 35, **NOT FOUND** in the tree yet. The assumed shapes are:

- `AuthorizeWorkspace(db *sql.DB, auth *apikit.AuthInfo, slug string, need <read|write need>) (*WorkspaceAccess, int, string)`. `WorkspaceAccess` gains `OrgID` and `Access`, and keeps `Status`. A reader asking for write gets `403` with `error_type: forbidden`.
- `ReadableSlugs(db *sql.DB, auth *apikit.AuthInfo) (map[string]bool, error)`. It returns nil for an admin token. Otherwise it returns owned plus member-org workspaces of any status, at most the bound slug for a bound PAT. The binding is resolved before rows are opened, and it never reports a refusal.
