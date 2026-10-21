---
spec_id: "35"
spec_name: "org_member_workspace_access"
title: "Let org members read org workspaces: the access rule and the REST API"
status: "active"
created_at: "2026-10-21T13:13:21.329552Z"
updated_at: "2026-10-21T13:13:21.329552Z"
intent_hash: "f7ab39efcc07b4c42ab3a114b15440e990e3952fbc73840d46f350d77099a3d0"
schema_version: 2
source: "docs/prd/04-let-org-members-read-org-workspaces.md"
---
## Intent

Hub decides who may see and who may change a workspace, and it decides that in one place. This spec makes membership of a workspace's organization grant read access to that workspace across hub's REST API, keeps every change with the workspace's owner and with admin tokens, and makes the workspace listing return every workspace the caller may read, each with the access the caller has.

## Goals

1. `wsaccess.AuthorizeWorkspace` works out the caller's access level (`admin`, `owner`, `member` or none) and takes the level the endpoint needs. No REST handler in `internal/workspace`, `internal/carrypatch`, `internal/merge`, `internal/sandboxes`, `internal/forge`, `internal/boundtoken` or `internal/secrets` compares `owner_id` with the caller itself.
2. A user U2 who is a member of org O can read workspace W, which U1 owns in O: every read route listed under Requirements answers 200, and a PAT still needs the scope the route needs today.
3. U2 gets `403` with `error_type: forbidden` on every write route of W, and `404 workspace not found` on every route of W's workspace secrets and bound tokens, the same answer a stranger gets.
4. A caller who is not the owner, not a member of the workspace's org and not an admin token gets exactly the answer they get today (404) on every route.
5. `GET /api/v1/workspaces` returns the caller's own workspaces plus the workspaces of the orgs the caller belongs to. Every workspace object in every response carries `access` and `org_slug`.
6. Every existing ownership test for a workspace in a personal org still passes. Test fixtures may gain columns or tables, but no assertion changes.
7. Bound-token confinement (spec 33) and bound-refusal reporting (spec 34) answer and report exactly as they do today.

## Non-goals

- **Org roles.** A member reads. A "maintainer" who may write is a later addition on the same seam (a new `Access` value) once apikit has the role.
- **Transferring ownership**, or an org that owns a workspace with no user as owner. `owner_id` stays required and stays one user.
- **Changing what a scope means.** PAT scope checks run as they do today, in the same order, with the same answers.
- **Showing secrets to members.** A member sees no workspace secret, not even its name.
- **Sessions, cost, per-run audit reads, the transcript, the unified audit query and the SSE stream.** These stay owner-only in this spec. They are the scope of `org_member_audit_read`, which builds on `wsaccess.ReadableSlugs` from this spec.
- **The git server** (clone and fetch for members, refused push). This is the scope of `org_member_git_read`.
- **Session and audit ingestion** (`POST /sessions`, complete, usage, the per-run POST routes). These stay owner-only.
- **CLI changes.** `afc workspace list|get` print the raw JSON, so the new fields show up without a code change.
- **Caching membership.** Membership is read on every request, so removing a member takes effect immediately.
- **Changing the rules for creating a workspace or moving it to another org.** The owner must still be a member of the target org.

## Background

Hub has orgs, org membership and a workspace `org_id`, but grants access by ownership alone:

- `internal/wsaccess/wsaccess.go`: `AuthorizeWorkspace(db, auth, slug) (*WorkspaceAccess, int, string)` loads `slug, owner_id, status, clone_status`. It allows admin tokens and the owner, answers 404 to everyone else, and then applies the spec 33 binding rule through `DeniesWorkspace`. `workspace.AuthorizeWorkspace` (`internal/workspace/authz.go`) is a thin wrapper around it.
- `internal/workspace/handlers.go` `lookupWorkspaceForAuth` applies the same rule a second time, inline, for workspace CRUD, sync (`sync_handler.go:92`), reclone (`reclone_handler.go:47`) and every patch route (`patch_handlers.go` `lookupPatchWorkspace`, `patch_reset_handler.go`). It passes `wsaccess.SiteOf(c, 404)` so that a binding refusal is reported with the request's method and route (TS-34-58).
- `handleListWorkspaces` uses `listWorkspacesByOwner` (`WHERE owner_id = ?`), or `listAllWorkspaces` for an admin token, and confines a bound PAT with `wsaccess.BoundSlug`.
- `internal/carrypatch/api.go` `authorizeWorkspace`, `internal/merge/api.go` `authorizeMergeWorkspace` / `validateWorkspaceForMerge`, `internal/sandboxes/routes.go` `(*LeaseService).authorize(c, write bool)` and `internal/forge/routes.go` `(*handlers).authorize(c, access)` all call `AuthorizeWorkspace`.
- `internal/boundtoken/mint.go` and `list.go` call `AuthorizeWorkspace` and then repeat an owner check, as spec 34 Decision 4 requires. That decision was written so that this PRD would not widen who can mint or list tokens.
- `internal/secrets/handlers.go` has its own `lookupWorkspaceOwner` and `lookupWorkspaceForResolution` (owner or admin, else 404 `not found`) for workspace secrets and variables. It does not use `wsaccess`.
- Membership lives in apikit's `org_members (org_id, user_id)`. Hub already reads it in `workspace.checkOrgMembership` (create and update, where `org_id` must be an org the owner belongs to) and in `secrets.checkOrgMembership` / `isOrgMember`.
- `docs/permissions.md` ("Ownership enforcement") still says that sync and reclone do not enforce ownership. Both call `lookupWorkspaceForAuth`. `docs/errata/ownership_enforcement.md` records the owner-only rule.

So a team whose workspaces belong to one org cannot see each other's patch stack, rebuild results, merges, sandboxes or forge state on hub. This spec opens that read path.

## Requirements

### R1. Access levels

`internal/wsaccess` defines `type Access string` with `AccessNone` (`""`), `AccessMember` (`"member"`), `AccessOwner` (`"owner"`) and `AccessAdmin` (`"admin"`). A caller's access to a workspace is decided in this order:

1. An admin token (`CredentialType == "admin_token"`) has `AccessAdmin`.
2. A credential with a non-empty `UserID` equal to the workspace's `owner_id` has `AccessOwner`.
3. A credential with a non-empty `UserID` has `AccessMember` when the workspace's `org_id` is non-empty and `org_members` has a row `(org_id, user_id)` for it.
4. Every other caller has `AccessNone`.

Admins and owners are decided without reading `org_members`. A workspace with a NULL or empty `org_id` has no members. A credential with an empty `UserID` that is not an admin token is never owner or member. An admin-role API key is not an admin token, as today.

### R2. What an endpoint needs and what it answers

`internal/wsaccess` defines `type Need string` with three values:

- `NeedRead`: member, owner or admin.
- `NeedWrite`: owner or admin. A member gets 403.
- `NeedPrivate`: owner or admin. A member gets 404, as a stranger does.

`AuthorizeWorkspace(db *sql.DB, auth *apikit.AuthInfo, slug string, need Need) (*WorkspaceAccess, int, string)` and `AuthorizeWorkspaceAt(db, auth, slug, need, site RefusalSite)` apply these checks in order:

1. A nil `auth` gets `401 authentication required`.
2. An unknown slug gets `404 workspace not found`.
3. `AccessNone` gets `404 workspace not found`, with no binding lookup and no refusal report.
4. A PAT bound to another workspace (`DeniesWorkspace`) gets `404 workspace not found`. The refusal is reported once with `site`. `AuthorizeWorkspace` uses the site it uses today, which has no method or route.
5. The need check: a member asking for `NeedWrite` gets `403` with the message `org members have read-only access to this workspace`, and a member asking for `NeedPrivate` gets `404 workspace not found`.
6. On success it returns `WorkspaceAccess{Slug, OwnerID, OrgID, Status, CloneStatus, Access}` and `(0, "")`. `OrgID` is `""` when the column is NULL.

A database error while reading the workspace, the membership or the binding gets `500 internal server error`, and access is never granted. `wsaccess.WriteRefusal(c echo.Context, status int, msg string) error` writes a refusal from `AuthorizeWorkspace` in the apikit error envelope, with `error_type: forbidden` on a 403 (exported as `ErrorTypeForbidden`) and without an `error_type` on any other status. `workspace.AuthorizeWorkspace` keeps delegating, with the new parameter.

### R3. The readable set

`wsaccess.ReadableSlugs(db *sql.DB, auth *apikit.AuthInfo) (slugs map[string]Access, all bool, err error)` returns every workspace the caller may read, with the caller's access to each:

- An admin token gets `all == true` and a nil map, without a query.
- A nil `auth`, or a non-admin credential with an empty `UserID`, gets an empty non-nil map without a query.
- Any other caller gets the workspaces they own (`AccessOwner`) plus the workspaces whose `org_id` is an org they belong to (`AccessMember`), every status included. This is one query.
- A bound PAT gets at most its bound slug, and only if that slug is in the set. The binding is resolved before any rows are opened, because the hub's test databases have a single connection.

A query error is returned to the caller.

### R4. The listing

`GET /api/v1/workspaces` returns the following:

- An admin token gets every workspace, as today, with `access: "admin"` on each.
- Any other caller gets the workspaces in `ReadableSlugs`, each with the access recorded there.

The PAT scope check (404 without a read-implying scope), `include_archived`, bound-PAT confinement and the order (`created_at` descending) do not change. The listing takes membership from `ReadableSlugs` and has no copy of the rule. If `ReadableSlugs` fails, the answer is `500 internal server error`.

### R5. Workspace objects carry the caller's access

Every response body that renders a workspace object adds two fields: create, get, list, update, archive, reactivate, sync (including the carry-patch extras) and reclone.

- `access`: the caller's level, `"admin"`, `"owner"` or `"member"`. It describes the caller's identity, not the PAT's scopes. The create response always says `"owner"`.
- `org_slug`: the slug of the workspace's org, or null when `org_id` is null or the org row is missing.

A member sees the same fields the owner sees, including `hub_url`. No workspace field holds a credential.

### R6. Workspace, sync and patch routes

`lookupWorkspaceForAuth` takes a `Need` and gets its decision from `wsaccess.AuthorizeWorkspaceAt` with `SiteOf(c, 404)`. It then loads the full row. It no longer compares `owner_id` itself.

| Route | Need |
|---|---|
| `GET /workspaces/:slug` | read |
| `PATCH /workspaces/:slug`, `POST .../archive`, `POST .../reactivate`, `DELETE /workspaces/:slug`, `POST .../sync`, `POST .../reclone` | write |
| `GET .../patches`, `GET .../patches/:id` | read |
| `POST .../patches`, `PATCH .../patches/:id`, `DELETE .../patches/:id`, `POST .../patches/reorder`, `POST .../patches/:id/restore`, `POST .../patches/:id/reset-to-origin` | write |

### R7. Carry-patch and merge routes

| Route | Need |
|---|---|
| `GET .../rebuilds`, `GET .../rebuilds/:id`, `GET .../rebuild-preview`, `GET .../patch-status`, `GET .../rerere` | read |
| `POST .../rebuild`, `DELETE .../rebuilds/:id`, `POST .../rebuilds/:id/requeue`, `POST .../rebuilds/:id/rollback`, `DELETE .../rerere/*`, the carry-patch sync (`runCarryPatchSync`) | write |
| `GET .../merges`, `GET .../merges/:id` | read |
| `POST .../merges`, `DELETE .../merges/:id`, `POST .../merges/:id/requeue`, `POST .../rebase` | write |

### R8. Sandbox lease and forge routes

| Route | Need |
|---|---|
| `GET .../sandboxes`, `GET .../sandboxes/:id` | read |
| `POST .../sandboxes`, `DELETE .../sandboxes/:id` | write |
| `GET .../forge`, `.../forge/repository`, `.../forge/deliveries`, `.../issues`, `.../issues/:n`, `.../issues/:n/comments`, `.../pulls/:n`, `.../pulls/:n/state`, `.../pulls/:n/files`, `.../pulls/:n/checks`, `.../pulls/:n/reviews` | read |
| `POST .../issues`, `PATCH .../issues/:n`, `POST .../issues/:n/close`, `POST .../issues/:n/comments`, `POST .../issues/:n/labels`, `DELETE .../issues/:n/labels/:name`, `POST .../labels`, `POST .../pulls`, `POST .../pulls/:n/comments`, `PUT .../forge/webhook`, `DELETE .../forge/webhook` | write |

The lease's `write bool` and the forge `access` value carry the need. A forge read by a member uses the workspace's configured forge credential, as the owner's read does, and returns only the credential's kind, never its value.

### R9. Secrets, variables and bound tokens

| Route | Need |
|---|---|
| `POST`, `GET .../secrets`; `PATCH`, `DELETE .../secrets/:key` | private |
| `GET .../vars`, `GET .../vars/resolved` | read |
| `POST .../vars`, `PATCH .../vars/:key`, `DELETE .../vars/:key` | write |
| `POST .../tokens`, `GET .../tokens` | private |

`internal/secrets` replaces `lookupWorkspaceOwner` and `lookupWorkspaceForResolution` with `wsaccess.AuthorizeWorkspace` and writes its refusals with `wsaccess.WriteRefusal`. `GET .../vars/resolved` gives a member the same document the owner gets: the owner's user tier, the org tier when the owner is still a member of the org (as today) and the workspace tier. `internal/boundtoken` uses `NeedPrivate` instead of its own owner comparison. Mint still refuses admin tokens and bound tokens before the workspace lookup, as spec 34 requires.

### R10. What does not change

- On every route the checks run in the same order as today. The access step sits exactly where the ownership step sits now: after the PAT scope check, and after any body validation that comes before the ownership check today, as in `PATCH /workspaces/:slug`. It comes before status and precondition checks, so a member writing to an archived workspace gets 403, not 400.
- The workspace CRUD routes still answer 404 to a PAT that lacks the scope.
- The binding guard and the apikit route guard do not change.
- An owner or admin token gets exactly the answers it gets today.
- Sessions, cost, audit, SSE, transcript and the git server keep their owner-only checks in this spec.

## Design Decisions

1. **The input is split into three specs.** The input covers the REST rule, the audit and session package, and the git server. These are three areas that can be built and tested on their own, and together they exceed one spec. This spec is the foundation (the rule, `ReadableSlugs`, the listing and every REST caller of `AuthorizeWorkspace`). `org_member_audit_read` and `org_member_git_read` follow. Until they land, a member sees W in the listing but gets the owner-only answers from cost, audit and git.
2. **The need is a separate `Need` type, not an `Access` value.** The input proposed `need Access`. That allows meaningless arguments (`AccessNone`, `AccessAdmin`), and it has no way to say "owner only, and invisible to members". Three named needs keep every handler declarative.
3. **Secrets and bound tokens are private: a member gets 404 on every route of these families.** The input says a member "sees no secret, not even its name" and that listing W's secrets is "404, as if none". Spec 34 (34-REQ-1.6, TS-34-6) already requires a reader who is not the owner to get 404 on mint. 403 would contradict both.
4. **The 403 carries `error_type: forbidden` and the message `org members have read-only access to this workspace`.** The input names the error type. Scope 403s carry no `error_type`, so a client can tell the two apart.
5. **Checks run as existence, then access, then binding, then need.** The binding refusal is still reported only when the binding is what decides the answer, so spec 34's reports (TS-34-58) do not change, and a bound PAT for another slug still gets 404 whatever its membership.
6. **Membership is a row in `org_members`.** The org's status and kind are ignored, as in the existing `checkOrgMembership` helpers. A personal org gets no special case: its only member is normally its owner, which keeps today's answers.
7. **Owners and admins never read `org_members`.** The workspace is loaded first and membership is read only for a non-owner, non-admin caller on a workspace that has an org. This is the "one more query for non-owners" the input accepts, and owner and admin code paths keep working against test databases that have no `org_members` table.
8. **A failed membership read is a 500, never a grant and never a silent 404.** This matches how `DeniesWorkspace` fails closed.
9. **`AuthorizeWorkspaceAt` takes a `RefusalSite`.** `lookupWorkspaceForAuth` must keep reporting refusals with the request's method and route (34-REQ-10.3), so the request-bearing callers need a variant that passes the site. `AuthorizeWorkspace` keeps its request-less site.
10. **`lookupWorkspaceForAuth` delegates to `wsaccess` and then loads the full row.** This puts the rule in one place, at the cost of one extra primary-key read. If the row disappears between the two reads, the answer is 404.
11. **`ReadableSlugs` returns an explicit `all` flag.** The audit code's convention of "nil means unrestricted" has already needed a "never nil" comment (`getAccessibleWorkspaces`). An explicit flag removes that trap for the audit spec that consumes it.
12. **`ReadableSlugs` includes every status.** The listing filters by `include_archived`, and the audit queries filter as they do today.
13. **The listing loads rows by slug.** The workspace store gets a by-slug list query (`WHERE slug IN (...)`, in chunks of at most 500 slugs) and sorts the merged result by `created_at` descending. The membership predicate is not copied into the workspace package.
14. **`access` and `org_slug` appear on every workspace object, not only in the listing.** Acceptance criterion 1 of the input expects `access: member` on `GET /workspaces/W`, and one response schema is simpler for clients. `org_slug` reuses the org lookup that `hub_url` already does.
15. **The position of the check is unchanged.** Moving it would change which of 400, 403 and 404 existing callers get, which this spec does not intend.
16. **Lease bodies are not redacted for members.** Their `secrets` and `optional_secrets` are names from the sandbox profile, which every caller can already list on `GET /sandbox-profiles`. The lease token is returned only by the issuing `POST`, which is a write.
17. **Forge reads by a member use the workspace's credential.** That is what reading the workspace's forge means. The credential's value is never returned (`credential` is only its kind), and every forge write stays the owner's.
18. **A member's resolved variables are the owner's document.** Resolution describes what the workspace runs with. Variables are not secrets by definition. Workspace secrets stay private (Decision 3).
19. **Variables are read through two routes.** The input lists a single-variable "get", but there is no such route (`internal/secrets/routes.go`). The read level applies to list and resolved.
20. **Secrets and variables refusals use `wsaccess` messages.** These handlers answered `404 not found`; they now answer `404 workspace not found`, written in the apikit envelope through `WriteRefusal`. One message everywhere, and the existing secrets tests assert only status codes.
21. **Bound-token mint and list use `NeedPrivate` instead of a second owner comparison.** The rule lives in one place. The refusals of admin tokens and bound tokens on mint stay ahead of it, unchanged.
22. **No CLI change.** `afc workspace list` and `afc workspace get` print the server's JSON through `apikit.CLIPrintResult`.
23. **The documentation is corrected in the same change.** The stale sync and reclone exception in `docs/permissions.md` is removed when the ownership section is rewritten around the three levels. The stale "Ownership: Not enforced" rows for merges, rebuilds, rerere and patch-status are corrected in the same pass, and `docs/errata/ownership_enforcement.md` gets a paragraph on member read access.

## Dependencies

| Spec | Relationship |
|---|---|
| `33_token_workspace_binding` | Modifies `AuthorizeWorkspace` and `lookupWorkspaceForAuth`, which apply the binding rule. A bound PAT for another slug must still get 404 whatever its membership. |
| `34_bound_token_issuance` | Mint and list keep their owner-only answer (34-REQ-1.6, Decision 4) through `NeedPrivate`. Refusal reporting with method and route (34-REQ-10.3) is preserved. |
| `26_sandbox_leases` | The lease routes' ownership 404 becomes read access for members on list and get, and 403 on issue and delete. |
| `30_forge_client_reads` | The forge read routes open to members. |
| `31_forge_writes` | The forge write routes answer 403 to members. |
| `32_forge_webhook_ingress` | Deliveries become readable by members. Webhook set and unset answer 403 to members. |

## Verified External API

apikit is a `replace` to `../apikit`, outside the repository, so its source could not be read. The signatures below are taken from call sites in this repository and are **unverified** against the source.

| Symbol | Assumed signature | Seen at |
|---|---|---|
| `apikit.AuthInfo` | struct with `CredentialType string` (`"admin_token"`, `"api_key"`, `"pat"`), `UserID string`, `Permissions []string`, `TokenID string` | `internal/wsaccess/binding.go`, `internal/workspace/testhelpers_test.go` |
| `apikit.GetAuthInfo` | `func(c echo.Context) *apikit.AuthInfo` (nil when unauthenticated) | every handler |
| `apikit.SetAuthInfo` | `func(c echo.Context, info *apikit.AuthInfo)` | `internal/wsaccess/guard.go` |
| `apikit.WriteAPIError` | `func(c echo.Context, code int, message string) error` | `internal/carrypatch/api.go` |
| `apikit.WriteAPIErrorWithType` | `func(c echo.Context, code int, message, errorType string) error` | `internal/workspace/handlers.go` `respondErrorWithType` |
| `apikit.NowUTC` | `func() string` | `internal/wsaccess/binding_test.go` |

The `org_members` table belongs to apikit. Its columns as used by hub and recreated in test fixtures are `org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE, user_id TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY (org_id, user_id)` (`internal/carrypatch/push_control_reject_test.go`).
