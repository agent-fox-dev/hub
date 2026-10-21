---
spec_id: "37"
spec_name: "org_member_git_read"
title: "Let org members read org workspaces: clone and fetch through the git server"
status: "active"
created_at: "2026-10-21T13:28:29.600925Z"
updated_at: "2026-10-21T13:28:29.600925Z"
intent_hash: "d54bec30d9c1f87f3a8d64fd4fa87f759a0b3e564864ef6b39f4f7a763cabe88"
schema_version: 2
source: "docs/prd/04-let-org-members-read-org-workspaces.md"
---
## Intent

Hub's git server decides who may clone, fetch and push a workspace repository by applying hub's shared workspace access rule. A member of the workspace's organization can clone and fetch the repository. Only the workspace's owner and admin tokens can push to it. Every other caller gets the answer they get today. The git server has no ownership rule of its own.

## Goals

1. Take user U2, a member of org O, and workspace W in O, owned by U1 and with a ready clone. `git clone` and `git fetch` of `/git/<O>/W.git` with U2's API key, or with a U2 PAT holding `git:read` or `git:write`, succeed and advertise the same refs U1 sees.
2. `git push` by U2 to W fails. `GET /git/<O>/W.git/info/refs?service=git-receive-pack` and `POST /git/<O>/W.git/git-receive-pack` both answer HTTP 403 with a pkt-line `ERR org members have read-only access to this workspace` body. After the refusal, W's refs, objects, `head_sha` and audit log are unchanged.
3. A caller who is not the owner, not a member of W's org and not an admin token gets exactly today's status and body on all three git routes. So do the owner and admin tokens.
4. No code in `internal/gitserver` compares `owner_id` with the caller. The decision comes from `wsaccess.AuthorizeWorkspaceAt`, with the need taken from the git service the request asks for.
5. Every existing test in `internal/gitserver` passes with no assertion changed. That includes the spec 33 binding tests and the spec 34 refusal-report test.

## Non-goals

- **The access rule itself, the REST API and the workspace listing.** Spec `35_org_member_workspace_access` delivers `Access`, `Need`, `AuthorizeWorkspace`/`AuthorizeWorkspaceAt`, `ReadableSlugs`, `access`/`org_slug` on workspace objects, and every REST route. This spec only calls the rule.
- **Sessions, cost, audit, the transcript and the SSE stream.** These are the scope of `org_member_audit_read`.
- **Any write for members.** That includes per-branch push rights and a "maintainer" level. A member who may push needs org roles, which apikit does not have.
- **Changing what `git:read` and `git:write` mean**, or the implicit full git scope of API keys. `requireGitScope` and `hasGitScope` do not change.
- **Changing the resolver's other answers.** This covers the unknown slug, the org mismatch, `repository not available` for a clone that is not ready, and the plain 500 on a database error. The order in which they are checked does not change either. `repository not available` comes before the access check and tells a stranger that a not-ready slug exists. That leak exists today and stays.
- **Auditing refused git requests.** A member's refused push writes no audit event, as a stranger's refusal writes none today. The spec 34 bound-refusal report stays as it is.
- **CLI changes.** `afc credential-helper` already hands the caller's API key to git, so a member clones with it unchanged.
- **Caching membership.** Membership is read on every request (spec 35).

## Background

The git server (`internal/gitserver`) serves `/git/:org/:slug.git/{info/refs,git-upload-pack,git-receive-pack}`. `MountGitHandlers` (`handlers.go`) builds the chain:

- `GitAuthMiddleware` (`auth.go`) resolves the HTTP Basic password to an `*apikit.AuthInfo`. The PAT, API key and admin token all come from hub's own tables, and blocked users get 401.
- `requireDotGitSuffix` comes next.
- `gitResolverMiddleware` (`resolver.go`) comes last. It calls `resolveWorkspaceFromDB`, which reads `slug, owner_id, org_id, clone_status` and requires the URL's org slug to match the workspace's `org_id`. An unknown slug or a mismatched org gets pkt-line 404 `repository not found`. It then requires `clone_status = 'ready'`, else 404 `repository not available`. After that it calls `authorizeGitAccess`, then `git.PlainOpen`.

`authorizeGitAccess` (`authz.go`) is the git server's own copy of the ownership rule:

- An `admin_token` passes.
- Otherwise `info.UserID != ws.OwnerID` gets pkt-line 404 `repository not found`.
- Then `wsaccess.DeniesWorkspace` applies the spec 33 binding rule, with `RefusalSite{Method, Route: c.Path(), Status: 404, Surface: SurfaceGit}`. A denial is 404 and a lookup error is a pkt-line 500.

After the middleware, each handler runs `requireGitScope`. A PAT needs `git:read` for upload-pack and `git:write` for receive-pack, and `git:write` implies `git:read`. API keys and admin tokens pass. A failure is `echo.NewHTTPError(403, "insufficient git permissions")`. `handleInfoRefs` answers a missing or invalid `service` with a pkt-line 403 before the scope check.

So a member of W's org gets 404 from the git server today, although spec 35 shows them W in the listing with its `hub_url`. Spec 35 says so explicitly (Decision 1, 35-REQ-10.6, TS-35-56). The original PRD (`docs/prd/04-let-org-members-read-org-workspaces.md`, acceptance criterion 3) asks for clone and fetch for members and a pkt-line 403 on push. It leaves 403 versus the git server's usual 404 "to spec time".

One adjacent defect sits in the resolver. `resolveWorkspaceFromDB` scans `org_id`, a nullable column (`internal/workspace/schema.go`; `PATCH` can set it to null), into a plain `string`. For a workspace with no org this fails with a database error, so the answer is a 500 rather than the 404 that every other unaddressable repository gets.

## Requirements

### R1. One decision, made by wsaccess

`authorizeGitAccess` decides every git request with `wsaccess.AuthorizeWorkspaceAt(db, info, ws.Slug, need, site)`. `site` is `wsaccess.RefusalSite{Method: c.Request().Method, Route: c.Path(), Status: 404, Surface: wsaccess.SurfaceGit}`, the site it uses today. It no longer does any of the following itself:

- check `CredentialType == "admin_token"`;
- compare `UserID` with `OwnerID`;
- call `DeniesWorkspace`.

The one check left before the call is the one it has today: a nil `AuthInfo` returns `echo.NewHTTPError(401)`. No code in `internal/gitserver` compares `owner_id` with the caller.

### R2. What each git request needs

The need comes from the request. It uses the matched route template (`c.Path()`) and the first `service` query value, which is the value `handleInfoRefs` reads.

- **`NeedWrite`:**
  - `POST /git/:org/:slug.git/git-receive-pack`;
  - `GET /git/:org/:slug.git/info/refs` with `service=git-receive-pack`.
- **`NeedRead`:**
  - `POST /git/:org/:slug.git/git-upload-pack`;
  - `GET .../info/refs` with any other `service` value, missing included. The handler then answers its pkt-line 403 for an invalid service, as today.

The git server never uses `NeedPrivate`.

### R3. Members clone and fetch

U2 is a member of W's org, as decided by spec 35's rule (a row in `org_members` for W's `org_id` and U2), and W's clone is ready. `GET .../info/refs?service=git-upload-pack` and `POST .../git-upload-pack` answer U2 exactly as they answer the owner: 200, the same `Content-Type`, the same advertisement and the same pack.

The scope check then applies unchanged. A member's PAT needs `git:read` or `git:write`, and without either gets `403 insufficient git permissions` as an owner's PAT would. A member's API key has implicit scope. Membership is read on every request, so a member removed from the org gets the stranger's 404 on their next request.

### R4. Members cannot push

When a member's request has `NeedWrite`, the resolver answers HTTP 403 with `Content-Type: text/plain`. The body is the pkt-line `ERR org members have read-only access to this workspace\n`, using the message `AuthorizeWorkspaceAt` returns.

The refusal is written before `PlainOpen` and before the handler runs, so it is the same whatever the PAT's scopes. Nothing in the workspace changes:

- no object or ref is written;
- the pre-receive hook is not called;
- `head_sha` is not updated and the worktree is not reset;
- no `hub.git.push` audit event is emitted and the post-push hook does not run.

The `git` CLI's `git push` fails at ref discovery and prints the message.

### R5. How refusals are written

`authorizeGitAccess` turns the status `AuthorizeWorkspaceAt` returns into a pkt-line response:

| Status from wsaccess | What the git server writes |
|---|---|
| 404 | pkt-line 404 `ERR repository not found`. This is the git server's existing text, not wsaccess's `workspace not found`. |
| 403 | pkt-line 403 with the returned message |
| 500, or any other non-zero status | pkt-line 500 `ERR internal server error` |

A database error never grants access. After writing a refusal, the function returns `errAccessDenied`, and the resolver stops the chain without a second response, as today.

### R6. Everyone else answers as today

- **A stranger** (not the owner, not a member of W's org, not an admin token) gets pkt-line 404 `repository not found` on all three routes. There is no binding lookup and no refusal report.
- **The owner and admin tokens** get exactly today's answers on all three routes. That includes the scope 403 for an owner's PAT that lacks the scope.
- **An admin-role API key** is not an admin token. It is owner, member or stranger like any other API key.
- **API keys and admin tokens** never cause a binding query. TS-33-45 and TS-33-52 still pass with the binding table dropped.
- **Blocked users** still get 401 from `GitAuthMiddleware`.

### R7. Bound tokens

Spec 33's and spec 34's git behaviour holds:

- A PAT bound to S gets pkt-line 404 `repository not found` for every other repository it could otherwise reach. The refusal is reported exactly once with the git site (TS-34-58).
- A failed binding lookup is a pkt-line 500 and the repository is not served (TS-33-44).

The binding check follows the access step, so:

- A member's PAT bound to W reads W, and its push is refused with the member 403. This is the same answer an unbound PAT of the same user and scopes gets.
- A member's PAT bound to another workspace gets 404 on W, and the refusal is reported once.
- A stranger's bound PAT gets 404 on W without a report, as today.

### R8. Order of checks

The chain stays:

1. `GitAuthMiddleware`: 401.
2. `requireDotGitSuffix`: 404.
3. The resolver's own lookup:
   - unknown slug or org mismatch: 404 `repository not found`;
   - database error: plain 500;
   - clone not ready: 404 `repository not available`.
4. `authorizeGitAccess`: access, then binding, then need.
5. `PlainOpen`: 500.
6. The handler: invalid service 403, then scope 403.

The access decision takes the ownership check's place. The resolver keeps its own workspace read for the org match and the clone status, and `AuthorizeWorkspaceAt` reads the row again by primary key. If the row disappears between the two reads, the answer is 404 `repository not found`.

### R9. A workspace without an org

`resolveWorkspaceFromDB` reads a NULL `org_id` as "no org", for example with `COALESCE(org_id, '')`. A workspace with no org therefore matches no `/git/<org>/` URL. In the resolver, every caller gets pkt-line 404 `repository not found`. In `WorkspaceLoader.Load`, the result is `transport.ErrRepositoryNotFound`. Today both give a 500.

### R10. Existing tests and documentation

No assertion in an existing `internal/gitserver` test changes. Some tests are about a stranger's answer but seed that caller as a member of the workspace's org. They drop that one `seedOrgMember` row, so the caller is a stranger again:

- `TestAuthz_PATWrongOwner_Returns404` and `TestAuthz_APIKeyWrongOwner_Returns404` (user-b);
- `TestResolver_NonOwnerNonAdmin_Returns404` (user-2);
- `newBindingGitEnv` (user-b), which TS-33-42..45, 52, 53, 55, 60 and TS-34-58 use.

This spec supersedes the git clause of spec 35's TS-35-56 (35-REQ-10.6). Wherever that test lives, its git case changes to expect R3 and R4.

The documentation is updated in the same change, with no tests of the docs:

- **`docs/api.md`:**
  - the Workspace Ownership bullet for the git server;
  - the anti-enumeration paragraph;
  - Git Server Endpoints: the read/write rule and the 403 and 404 rows of the three routes' error tables.
- **`docs/permissions.md`:**
  - the git bullets under "Ownership enforcement" and the admin-role note;
  - the Git Endpoints table;
  - the anti-enumeration paragraph.
- **`docs/architecture.md`:**
  - the Git Server section gains an authorization step;
  - the list of places that "apply the rule themselves" no longer names `authorizeGitAccess`.
- **`docs/errata/ownership_enforcement.md`:** a paragraph on member read access through the git server.

`docs/openapi.yaml` has no git paths and `docs/cli.md` has no git command, so neither changes.

## Design Decisions

1. **A member's push gets a pkt-line 403, not the git server's usual 404.** The input left this open. The member may clone the repository, so a 404 hides nothing. A 403 tells the member and their tooling why the push failed, and it matches the REST API's 403 for a member's write (spec 35 R2).
2. **The 403 body carries wsaccess's message, and 404s keep `repository not found`.** One wording for "read-only" on both surfaces. Existing tests (TS-33-42, TS-33-55, TS-33-60) and git users read `repository not found`, so it stays.
3. **The need is decided in the resolver from the route template and the `service` value, before the scope check.** This puts the need check where the ownership check is today, as part of the single wsaccess call. As a result, a member's push is refused with the member 403 whatever the PAT's scopes. An owner's PAT that lacks `git:write` still gets the scope 403 from the handler.
4. **`info/refs` with a missing or invalid `service` needs read.** It serves nothing, and the handler refuses it with its own 403, as today. Treating it as a write would give members a different answer from owners for the same malformed request.
5. **The git server calls `AuthorizeWorkspaceAt` with its own `SurfaceGit` site, not `SiteOf`.** `SiteOf` builds an API-surface site. TS-34-58 requires git refusals to be reported with `Surface: git` and the git route template.
6. **The resolver keeps its own lookup and its order, at the cost of one more primary-key read.** Moving the access decision ahead of the org match and clone status would change strangers' answers (`repository not available` becomes `repository not found`). It would also start reporting bound refusals on URLs with the wrong org. Both are outside this spec's scope.
7. **A NULL `org_id` is "no org" in the resolver (R9).** The resolver is being rewritten around the org-membership rule, which already treats a NULL org as having no members. A 500 that tells a stranger a slug exists is the kind of answer anti-enumeration forbids. The fix is one expression.
8. **Existing tests about strangers drop the stranger's membership row rather than change their assertions.** Their subject is "neither owner nor admin gets 404". After this spec that also means "not a member", so the fixture changes and the assertion stays. Spec 35's TS-35-56 git clause is the one assertion this spec reverses, because its requirement (35-REQ-10.6) was written to hold only until this spec lands.
9. **A refused member push is not audited.** Today no git refusal is audited apart from spec 34's bound-refusal report. Adding one for members alone would be inconsistent.
10. **A member's bound PAT refused on another workspace is reported, and a stranger's is not.** This follows from spec 35's order (existence, access, binding, need), which reports a binding refusal only when the binding decided the answer.
11. **`authorizeGitAccess` keeps its name and its `errAccessDenied` contract.** The resolver, the smoke test comments and the docs refer to it. Only its body changes.
12. **`WorkspaceLoader.Load` gets no authorization.** It is reached only through the resolver middleware, which has already decided. Load keeps checking existence, the org and the clone status as today.

## Dependencies

| Spec | Relationship |
|---|---|
| `35_org_member_workspace_access` | Provides `wsaccess.Need` (`NeedRead`, `NeedWrite`), `AccessMember`, `AuthorizeWorkspaceAt(db, auth, slug, need, site)` and its 403 message, which this spec calls. This spec supersedes the git clause of its 35-REQ-10.6 / TS-35-56. |
| `33_token_workspace_binding` | The git binding rule (33-REQ-7.x, 9.x) must keep its answers: TS-33-42..45, 52, 53, 55 and 60, whose shared fixture `newBindingGitEnv` this spec adjusts. |
| `34_bound_token_issuance` | Git refusal reports with method, route and `Surface: git` (34-REQ-10.3, TS-34-58) must stay exactly once per refusal. |

## Verified External API

apikit is a `replace` to `../apikit`, outside the repository, so its source could not be read. The signatures below come from call sites in `internal/gitserver` and are **unverified**.

| Symbol | Assumed signature | Seen at |
|---|---|---|
| `apikit.AuthInfo` | struct with `CredentialType string` (`"pat"`, `"api_key"`, `"admin_token"`), `UserID string`, `Role string`, `TokenID string`, `KeyID string`, `Permissions []string` | `internal/gitserver/auth.go` (constructed there) |
| `apikit.GetAuthInfo` | `func(c echo.Context) *apikit.AuthInfo` | `internal/gitserver/authz.go`, `handlers.go` |
| `apikit.SetAuthInfo` | `func(c echo.Context, info *apikit.AuthInfo)` | `internal/gitserver/auth.go` |

The echo v4.16.0 calls `c.Path()` (the route template inside group middleware, as TS-34-58 relies on), `c.QueryParam(name) string` and `c.Request().Method` are already used in `internal/gitserver`.
