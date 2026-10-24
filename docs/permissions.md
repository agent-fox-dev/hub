# Permissions Reference

This document says who may do what on the hub. It is for operators and
integrators who decide which credential to hand to a person, a CI job or an
agent, and which scopes to put on a personal access token (PAT). It covers the
credential types, the order in which a request is checked, every permission
scope and, endpoint by endpoint, what a caller needs. The endpoints themselves
are described in [api.md](api.md), the `afc` commands in [cli.md](cli.md), the
server's settings in [configuration.md](configuration.md), and how the checks
are implemented in [architecture.md](architecture.md).

## Contents

- [Credential types](#credential-types)
  - [Admin token](#admin-token)
  - [API keys](#api-keys)
  - [Personal access tokens](#personal-access-tokens)
  - [Workspace-Bound PATs](#workspace-bound-pats)
  - [Presenting a credential](#presenting-a-credential)
- [How a request is authorized](#how-a-request-is-authorized)
  - [Scope check](#scope-check)
  - [Workspace access levels](#workspace-access-levels)
  - [Admin role](#admin-role)
  - [Anti-Enumeration Policy](#anti-enumeration-policy)
- [Scope reference](#scope-reference)
  - [Account and token scopes](#account-and-token-scopes): [users:read](#usersread), [orgs:read](#orgsread),
    [orgs:write](#orgswrite), [keys:read](#keysread),
    [keys:manage](#keysmanage), [tokens:read](#tokensread),
    [tokens:write](#tokenswrite), [tokens:manage](#tokensmanage)
  - [Workspace scopes](#workspace-scopes): [workspaces:read](#workspacesread),
    [workspaces:write](#workspaceswrite)
  - [Carry-patch scopes](#carry-patch-scopes): [patches:read](#patchesread), [patches:write](#patcheswrite),
    [rebuilds:read](#rebuildsread), [rebuilds:write](#rebuildswrite)
  - [Merge scopes](#merge-scopes): [merges:read](#mergesread), [merges:write](#mergeswrite)
  - [Git scopes](#git-scopes): [git:read](#gitread), [git:write](#gitwrite)
  - [Secrets and variables scopes](#secrets-and-variables-scopes): [secrets:read](#secretsread),
    [secrets:write](#secretswrite), [vars:read](#varsread),
    [vars:write](#varswrite)
  - [Session and audit scopes](#session-and-audit-scopes): [sessions:read](#sessionsread),
    [sessions:write](#sessionswrite), [audit:read](#auditread),
    [audit:write](#auditwrite)
  - [Sandbox scopes](#sandbox-scopes): [sandboxes:read](#sandboxesread),
    [sandboxes:write](#sandboxeswrite)
  - [Forge scopes](#forge-scopes): [forge:read](#forgeread), [forge:write](#forgewrite)
- [Complete Permission List](#complete-permission-list)
- [Endpoint Access Matrix](#endpoint-access-matrix)
  - [Account and token endpoints](#account-and-token-endpoints)
  - [Admin endpoints](#admin-endpoints)
  - [Workspace endpoints](#workspace-endpoints)
  - [Carry-patch endpoints](#carry-patch-endpoints)
  - [Merge endpoints](#merge-endpoints)
  - [Git endpoints](#git-endpoints)
  - [Secrets and variables endpoints](#secrets-and-variables-endpoints)
  - [Session, cost and audit endpoints](#session-cost-and-audit-endpoints)
  - [Sandbox endpoints](#sandbox-endpoints)
  - [Forge endpoints](#forge-endpoints)
- [Retired scope names](#retired-scope-names)
- [Extending the Permission Registry](#extending-the-permission-registry)

---

## Credential types

| Type | Format | Issued by | Access |
|------|--------|-----------|--------|
| Admin token | `af_admin_<64-hex>` | the hub, at first boot or on rotation | Everything, in every workspace. No scope or ownership check applies. It has no user behind it. |
| API key | `af_<key_id>_<secret>` | the OAuth login (`afc login`) | Everything the owning user may reach. No scope check applies; workspace access still does. |
| PAT | `af_pat_<token_id>_<secret>` | `POST /api/v1/user/tokens` | Only what its scopes allow, within what its user may reach. Never admin. |

Scopes constrain PATs only: admin tokens and API keys are never scope-checked.
A PAT can also be bound to one workspace, which narrows where its scopes apply
and never widens what they allow (see [Workspace-Bound PATs](#workspace-bound-pats)).

### Admin token

The operator receives the admin token when the hub is first started with
`--admin-email`, and a new one with `--reset-admin-token` (see
[First boot](configuration.md#first-boot)). It may do everything, in every
workspace, and passes every admin check. Because it has no user identity it
cannot:

- create a workspace (`403`; a workspace needs a real user as its owner);
- use the user-scoped secrets and variables routes (`/api/v1/user/...`, `403`);
- mint workspace-bound tokens (a token needs a user as its owner).

### API keys

API keys are the credential of interactive users and of the CLI. They carry no
scope list: what they reach is decided by who owns the key.

- **Creation.** Only through the OAuth login flow (`POST /api/v1/auth/callback`,
  which `afc login` drives through a browser). The hub creates or updates the
  user, **revokes every existing API key of that user**, and returns the new key
  once. Only a hash of the secret is stored. The expiry is 90 days by default;
  the allowed values are 0, 30, 60 and 90.
- **Refresh.** `POST /api/v1/user/keys/:key_id/refresh` issues a new secret for
  the same key id and recalculates the expiry. It needs an API key; a PAT is
  refused.
- **Revocation.** By the owner (`DELETE /api/v1/user/keys/:key_id`; a key may
  revoke itself), by an admin (`DELETE /api/v1/users/:id/keys/:key_id`), or
  automatically by the next OAuth login of the same user. Automated systems that
  use an API key therefore lose it when the user logs in again.
- **Blocked users.** Blocking a user disables all their API keys when they
  authenticate, without revoking them; the git server refuses every credential
  of a blocked user. Unblocking re-enables every key that is neither revoked nor
  expired.
- **PATs.** An API key may create a PAT with any registered scope; the subset
  rule applies only when a PAT creates a PAT.

### Personal access tokens

PATs are for programmatic use: CI, agents, scripts. A PAT is created with
`POST /api/v1/user/tokens` and an explicit list of scopes. Every scope must be
registered (see the [Complete Permission List](#complete-permission-list)); an
unknown scope is refused with `400`. A PAT that creates another PAT (it needs
`tokens:manage`) can grant only scopes it holds itself. A PAT is never treated as
admin, even when its user has the admin role.

| Aspect | API key | PAT |
|--------|---------|-----|
| Permissions | All of the user's (no scope check) | Only its scopes |
| Admin access | An admin-role key passes the admin checks (see [Admin role](#admin-role)) | Never |
| Creating PATs | With any registered scope | Only with a subset of its own scopes |
| Refresh | `POST /api/v1/user/keys/:key_id/refresh` | Not supported |
| Changing permissions | — | `PUT`, `PATCH`, `DELETE /api/v1/user/tokens/:token_id/permissions` |
| Intended use | Interactive, CLI | Programmatic, CI, agents |

### Workspace-Bound PATs

A PAT can be bound to exactly one workspace `S`. The binding narrows where the
token works; on `S` its scopes decide as for an unbound PAT of the same user.
API keys and admin tokens cannot be bound.

**Minting and listing.** `POST /api/v1/workspaces/:slug/tokens` mints a bound PAT
for the owner of an active workspace, with an API key or with a PAT holding
`tokens:manage`; the scopes granted must be a subset of the creating PAT's.
`GET /api/v1/workspaces/:slug/tokens` lists them for the owner (a PAT needs
`tokens:read` or `tokens:manage`) and for admin tokens. Both routes need the
*private* access level: a member of the workspace's org gets the stranger's
`404 workspace not found`. Admin tokens cannot mint. Deleting a workspace revokes
its bound tokens.

For a bound PAT:

- **Routes that name a workspace** answer `404 workspace not found` for every
  slug other than `S`. This comes before the scope check: to a bound PAT, other
  workspaces do not exist.
- **Cross-workspace routes** that allow bound tokens (`GET /workspaces`, the
  session routes, `GET /audit`, `GET /events`, `GET /sandbox-profiles`) see only
  `S`. For any other workspace they give the answer a non-owner gets.
- **Every other route** is refused with `403` and `error_type: token_bound`:
  `POST /workspaces`, user and org secrets and variables, `GET /sandboxes`, every
  account, token and admin route, and unknown paths. So `tokens:manage` on a bound
  PAT cannot mint an unbound PAT through `POST /user/tokens`.
- **`tokens:*` scopes do nothing.** On its own workspace's `/tokens` routes a bound
  PAT gets `403 token_bound`, on any other workspace's `404 workspace not found`.
- **Git** serves `S`'s repository under the `git:read` and `git:write` scopes and
  the user's access (a member's bound PAT still cannot push), and answers a
  pkt-line `404 repository not found` for every other repository.
- **Refusals are audited** as `hub.token.bound_refused`, at most once per token
  per minute.

[Workspace-Bound Tokens](api.md#workspace-bound-tokens) in the API reference lists
every answer.

### Presenting a credential

- **REST:** `Authorization: Bearer <credential>`, for all three types.
- **Git:** the credential is the HTTP Basic password; the username is ignored.
  All three types are accepted. A request without credentials gets `401` with
  `WWW-Authenticate: Basic realm="af-hub"`; an unknown, revoked or expired
  credential, or one of a blocked user, gets `401`.
- **CLI:** `afc login --provider github --expires 90` opens a browser for the
  OAuth login and saves the API key in `~/.af/config.toml`. `afc` sends it as a
  bearer token on every request and checks no expiry itself. The precedence of
  `--api-key`, `API_KEY` and the file is described under
  [CLI Configuration](configuration.md#cli-configuration-afconfigtoml).
- **Git through the CLI:** `afc credential-helper get` hands the stored API key
  to git as the HTTP Basic password (username `x-token-auth`). Set it up with:

  ```
  git config --global credential.<hub-url>.helper "!afc credential-helper"
  ```

---

## How a request is authorized

Every request below the mount point goes through the same checks, in this
order. The first one that fails decides the answer.

1. **Authentication.** A request without a valid credential (missing, unknown,
   revoked or expired) is refused with `401`.
2. **Token binding.** A bound PAT on a route it may not use: `403 token_bound`;
   on another workspace: `404 workspace not found`.
3. **Scope** (PATs only). A PAT without the scope the route needs: `403`, or
   `404` on the workspace CRUD routes (see
   [Anti-Enumeration Policy](#anti-enumeration-policy)). This check comes before
   the workspace is looked up, so a PAT without the scope gets the same answer
   for a slug that does not exist.
4. **Workspace access.** An unknown slug, or a workspace the caller may not see:
   `404 workspace not found`. A member of the workspace's org on a write: `403`
   with `error_type: forbidden`.
5. **State and preconditions** of the workspace and the resource, for example
   `400` on a write to an archived workspace. Because access comes first, a
   member's write to an archived workspace is `403`, not `400`.

A database error during these checks is `500`, never a grant.

### Scope check

A scope has the form `resource:action`; both parts use only lowercase ASCII
letters, digits and underscores. For every hub resource, `resource:write`
implies `resource:read`, so a route that needs `resource:read` also accepts
`resource:write`; `resource:read` never implies a write. The account scopes keep
their own shape: `tokens:manage` satisfies a check for `tokens:write`, and
`keys:manage`, `orgs:write` and `users:read` stand alone.

A scope refusal is a `403` without `error_type`, so a client can tell it from a
member's `forbidden`. The messages are:

| Routes | Answer to a PAT without the scope |
|--------|-----------------------------------|
| Workspace get, list, update, archive, reactivate, delete | `404 workspace not found` |
| `POST /workspaces` | `403 PAT requires workspaces:write scope to create workspaces` |
| Sync | `403 PAT requires workspaces:write scope to sync workspaces`; on a carry-patch workspace `403 missing required scope: workspaces:write` |
| Reclone | `403 PAT requires workspaces:write scope to reclone workspaces` |
| Branch push | `403 PAT requires workspaces:write scope to push branches` |
| Rerere list, patch-status | `403 missing required scope: workspaces:read` |
| Rerere forget | `403 missing required scope: workspaces:write` |
| Patches | `403 PAT requires patches:read scope` (or `patches:write`) |
| Rebuilds | `403 missing required scope: rebuilds:read` (or `rebuilds:write`) |
| Merges | `403 PAT requires merges:read scope` (or `merges:write`) |
| Secrets and variables | `403 insufficient permission scope` |
| Sessions and cost | `403 sessions:read scope required` (or `sessions:write`) |
| Audit | `403 insufficient scope: audit:read required` (or `audit:write`) |
| Sandboxes | `403 PAT requires sandboxes:read scope` (or `sandboxes:write`) |
| Forge | `403 PAT requires forge:read scope` (or `forge:write`) |
| Git | `403 insufficient git permissions` |

### Workspace access levels

Every route that names a workspace decides the caller's access to it the same
way, and each route needs one of three levels.

| Access | Who |
|--------|-----|
| `admin` | an admin token (an admin-role API key is not one) |
| `owner` | a credential of the user who owns the workspace |
| `member` | a credential of a user who is a member of the workspace's org, whatever the org's status or kind; membership is read on every request, so removing a member takes effect at once |
| none | everyone else |

| Need | Admitted | A member gets | Anyone else gets |
|------|----------|---------------|------------------|
| read | member, owner, admin | the answer the owner gets | `404 workspace not found` |
| write | owner, admin | `403 org members have read-only access to this workspace`, `error_type: forbidden` | `404 workspace not found` |
| private | owner, admin | `404 workspace not found`, as a stranger | `404 workspace not found` |

The level a PAT has is its user's, whatever its scopes: a PAT without the scope
gets the scope answer whatever its membership. Every workspace object carries
`access` (the caller's level: `admin`, `owner` or `member`) and `org_slug`. The
workspace listing returns the caller's own workspaces and those of the orgs the
caller belongs to; an admin token sees all of them.

The git server uses the same levels: clone and fetch need read, push needs
write. A member's push is refused with a pkt-line
`403 org members have read-only access to this workspace`.

Which routes need which level is listed in the
[Endpoint Access Matrix](#endpoint-access-matrix). The session, cost and audit
routes apply the same levels but keep refusal answers of their own (see
[Session, cost and audit endpoints](#session-cost-and-audit-endpoints)).

### Admin role

A user can have the admin role. Their API keys then pass the admin checks of the
user and org management routes (see [Admin endpoints](#admin-endpoints)), and a
user-role key gets `403` there. An admin-role API key is **not** admin for
workspaces or git: it is owner, member or stranger like any other API key and
sees only the workspaces of its user and its user's orgs. Only an admin token
reaches every workspace. PATs are never admin.

### Anti-Enumeration Policy

The hub does not tell a caller that a workspace exists unless the caller may
read it:

- A workspace the caller may not read (neither owner, nor member of its org, nor
  an admin token) gets the same `404 workspace not found` as a slug that does
  not exist, on every route that names a workspace: workspace CRUD, sync,
  reclone, branch push, patches, rebuilds, rerere, patch-status, merges, leases,
  forge, secrets, variables, bound tokens and the per-run audit reads.
- On the workspace CRUD routes (list, get, update, archive, reactivate, delete) a
  PAT without the workspace scope also gets `404`.
- `POST /workspaces` answers a missing scope with `403`: there is nothing to
  enumerate.
- The git server answers a caller who may not read the repository with a
  pkt-line `404 repository not found`.
- A member of the workspace's org already knows it exists, so a member's write is
  `403` with `error_type: forbidden`, and a member's push is a pkt-line `403`.
- A workspace-bound PAT gets the stranger's answer for every workspace other
  than its own.
- Other refusals of a missing scope are `403` (see [Scope check](#scope-check)).

Three groups answer differently:

- The session and cost reads answer a workspace the caller may not read with
  `403 workspace access denied`.
- The audit ingestion routes answer a caller who does not own an existing
  workspace with `403 token owner does not have write access to this workspace`
  (`error_type: workspace_access_denied`), and an archived workspace with
  `409 workspace_archived` before the ownership check; an unknown slug is `404`.
- Org-scoped secrets and variables answer a non-member with `404`.

---

## Scope reference

Every scope is described with the same rows:

- **Grants** — what a PAT holding the scope may do.
- **Endpoints** — the routes that check it, relative to the mount point
  (`/api/v1` by default); git routes and `afc` commands are named in full.
- **Implies / implied by** — the scopes it satisfies or that satisfy it.
- **Workspace access** — the [access level](#workspace-access-levels) the
  routes need, besides the scope.
- **Notes** — anything else a caller sees.

API keys and admin tokens are never scope-checked; for them only the workspace
access applies.

### Account and token scopes

These eight scopes are built into the server framework the hub runs on and
cover the caller's own account.

#### users:read

| | |
|---|---|
| **Grants** | Read and update the caller's own profile |
| **Endpoints** | `GET /user`, `PATCH /user` |
| **Implies / implied by** | — |
| **Workspace access** | — |
| **Notes** | — |

#### orgs:read

| | |
|---|---|
| **Grants** | List the caller's own org memberships |
| **Endpoints** | `GET /user/orgs` |
| **Implies / implied by** | — |
| **Workspace access** | — |
| **Notes** | — |

#### orgs:write

| | |
|---|---|
| **Grants** | Reserved for org write operations |
| **Endpoints** | None: no route checks it yet |
| **Implies / implied by** | — |
| **Workspace access** | — |
| **Notes** | Org management is admin-only (see [Admin endpoints](#admin-endpoints)). |

#### keys:read

| | |
|---|---|
| **Grants** | Reserved for reading API key metadata |
| **Endpoints** | None: no route checks it yet |
| **Implies / implied by** | — |
| **Workspace access** | — |
| **Notes** | — |

#### keys:manage

| | |
|---|---|
| **Grants** | Revoke the caller's own API keys |
| **Endpoints** | `DELETE /user/keys/:key_id` |
| **Implies / implied by** | — |
| **Workspace access** | — |
| **Notes** | Refreshing a key needs an API key; a PAT is refused whatever its scopes. |

#### tokens:read

| | |
|---|---|
| **Grants** | List and read the caller's own PATs (metadata only), and the bound tokens of a workspace the caller owns |
| **Endpoints** | `GET /user/tokens`, `GET /user/tokens/:token_id`, `GET /workspaces/:slug/tokens` |
| **Implies / implied by** | Implied by `tokens:manage` on `GET /workspaces/:slug/tokens` |
| **Workspace access** | private, on `GET /workspaces/:slug/tokens` |
| **Notes** | Does nothing on a [workspace-bound PAT](#workspace-bound-pats). |

#### tokens:write

| | |
|---|---|
| **Grants** | Change the permissions of the caller's own PATs |
| **Endpoints** | `PUT /user/tokens/:token_id/permissions`, `PATCH /user/tokens/:token_id/permissions`, `DELETE /user/tokens/:token_id/permissions` |
| **Implies / implied by** | Implied by `tokens:manage` |
| **Workspace access** | — |
| **Notes** | Does nothing on a workspace-bound PAT. |

#### tokens:manage

| | |
|---|---|
| **Grants** | Create and revoke the caller's own PATs, change their permissions, and mint and list the bound tokens of a workspace the caller owns. A new PAT's scopes must be a subset of the creating PAT's. |
| **Endpoints** | `POST /user/tokens`, `DELETE /user/tokens/:token_id`, `PUT /user/tokens/:token_id/permissions`, `PATCH /user/tokens/:token_id/permissions`, `DELETE /user/tokens/:token_id/permissions`, `POST /workspaces/:slug/tokens`, `GET /workspaces/:slug/tokens` |
| **Implies / implied by** | Satisfies `tokens:write`, and `tokens:read` on `GET /workspaces/:slug/tokens` |
| **Workspace access** | private, on the `/workspaces/:slug/tokens` routes; minting also needs an active workspace |
| **Notes** | Does nothing on a workspace-bound PAT. |

### Workspace scopes

#### workspaces:read

| | |
|---|---|
| **Grants** | List and view workspaces; list rerere resolutions; view the patch-status dashboard |
| **Endpoints** | `GET /workspaces`, `GET /workspaces/:slug`, `GET /workspaces/:slug/rerere`, `GET /workspaces/:slug/patch-status` |
| **Implies / implied by** | Implied by `workspaces:write` |
| **Workspace access** | read |
| **Notes** | The listing returns the caller's own workspaces and those of the caller's orgs. A PAT without the scope gets `404` on list and get. |

#### workspaces:write

| | |
|---|---|
| **Grants** | Create workspaces; update, archive, reactivate, delete, sync and reclone them; push a branch to the workspace's `origin`; forget rerere resolutions |
| **Endpoints** | `POST /workspaces`, `PATCH /workspaces/:slug`, `DELETE /workspaces/:slug`, `POST /workspaces/:slug/archive`, `POST /workspaces/:slug/reactivate`, `POST /workspaces/:slug/sync`, `POST /workspaces/:slug/reclone`, `POST /workspaces/:slug/branches/:name/push`, `DELETE /workspaces/:slug/rerere/*pathspec`, and every `workspaces:read` endpoint |
| **Implies / implied by** | Implies `workspaces:read` |
| **Workspace access** | write (creation: none; the caller becomes the owner) |
| **Notes** | Admin tokens cannot create workspaces. Delete needs an archived workspace. |

### Carry-patch scopes

#### patches:read

| | |
|---|---|
| **Grants** | List and view the patches of a workspace |
| **Endpoints** | `GET /workspaces/:slug/patches`, `GET /workspaces/:slug/patches/:id` |
| **Implies / implied by** | Implied by `patches:write` |
| **Workspace access** | read |
| **Notes** | — |

#### patches:write

| | |
|---|---|
| **Grants** | Add, update, remove, restore, reorder and reset the patches of a workspace |
| **Endpoints** | `POST /workspaces/:slug/patches`, `PATCH /workspaces/:slug/patches/:id`, `DELETE /workspaces/:slug/patches/:id`, `POST /workspaces/:slug/patches/:id/restore`, `POST /workspaces/:slug/patches/:id/reset-to-origin`, `POST /workspaces/:slug/patches/reorder` |
| **Implies / implied by** | Implies `patches:read` |
| **Workspace access** | write |
| **Notes** | Access is checked before any git operation. |

#### rebuilds:read

| | |
|---|---|
| **Grants** | List and view rebuild jobs and their history; preview the conflicts of a rebuild |
| **Endpoints** | `GET /workspaces/:slug/rebuilds`, `GET /workspaces/:slug/rebuilds/:id`, `GET /workspaces/:slug/rebuild-preview` |
| **Implies / implied by** | Implied by `rebuilds:write` |
| **Workspace access** | read |
| **Notes** | — |

#### rebuilds:write

| | |
|---|---|
| **Grants** | Submit, cancel and requeue rebuild jobs, and roll the integration branch back to an earlier rebuild |
| **Endpoints** | `POST /workspaces/:slug/rebuild`, `DELETE /workspaces/:slug/rebuilds/:id`, `POST /workspaces/:slug/rebuilds/:id/requeue`, `POST /workspaces/:slug/rebuilds/:id/rollback` |
| **Implies / implied by** | Implies `rebuilds:read` |
| **Workspace access** | write |
| **Notes** | After the access check the routes check the workspace's mode, status and clone. |

### Merge scopes

#### merges:read

| | |
|---|---|
| **Grants** | List and view merge jobs |
| **Endpoints** | `GET /workspaces/:slug/merges`, `GET /workspaces/:slug/merges/:id` |
| **Implies / implied by** | Implied by `merges:write` |
| **Workspace access** | read |
| **Notes** | — |

#### merges:write

| | |
|---|---|
| **Grants** | Submit merges, cancel queued merge jobs, requeue dead-lettered ones, and start a batch rebase |
| **Endpoints** | `POST /workspaces/:slug/merges`, `DELETE /workspaces/:slug/merges/:id`, `POST /workspaces/:slug/merges/:id/requeue`, `POST /workspaces/:slug/rebase` |
| **Implies / implied by** | Implies `merges:read` |
| **Workspace access** | write |
| **Notes** | Submit and rebase then need an active workspace with a ready clone (`400` otherwise). |

### Git scopes

#### git:read

| | |
|---|---|
| **Grants** | Clone and fetch a workspace's repository over git smart HTTP |
| **Endpoints** | `GET /git/:org/:slug.git/info/refs?service=git-upload-pack`, `POST /git/:org/:slug.git/git-upload-pack` |
| **Implies / implied by** | Implied by `git:write` |
| **Workspace access** | read |
| **Notes** | A caller who may not read the repository gets a pkt-line `404 repository not found`. |

#### git:write

| | |
|---|---|
| **Grants** | Push to a workspace's repository over git smart HTTP |
| **Endpoints** | `GET /git/:org/:slug.git/info/refs?service=git-receive-pack`, `POST /git/:org/:slug.git/git-receive-pack`, and every `git:read` endpoint |
| **Implies / implied by** | Implies `git:read` |
| **Workspace access** | write |
| **Notes** | A member's push is a pkt-line `403`. |

### Secrets and variables scopes

The same scopes cover the three tiers: the caller's user, an org, and a
workspace. See [Secrets and variables endpoints](#secrets-and-variables-endpoints)
for who may reach each tier.

#### secrets:read

| | |
|---|---|
| **Grants** | List secret names; values are never returned |
| **Endpoints** | `GET /user/secrets`, `GET /orgs/:slug/secrets`, `GET /workspaces/:slug/secrets` |
| **Implies / implied by** | Implied by `secrets:write` |
| **Workspace access** | private |
| **Notes** | — |

#### secrets:write

| | |
|---|---|
| **Grants** | Create, update and delete secrets |
| **Endpoints** | `POST`, `PATCH .../:key` and `DELETE .../:key` on `/user/secrets`, `/orgs/:slug/secrets` and `/workspaces/:slug/secrets`, and every `secrets:read` endpoint |
| **Implies / implied by** | Implies `secrets:read` |
| **Workspace access** | private |
| **Notes** | — |

#### vars:read

| | |
|---|---|
| **Grants** | List and read variable values, and a workspace's resolved variables |
| **Endpoints** | `GET /user/vars`, `GET /orgs/:slug/vars`, `GET /workspaces/:slug/vars`, `GET /workspaces/:slug/vars/resolved` |
| **Implies / implied by** | Implied by `vars:write` |
| **Workspace access** | read |
| **Notes** | A member's `GET /workspaces/:slug/vars/resolved` is the owner's document: the owner's user tier, the org tier while the owner is a member of the org, and the workspace tier. |

#### vars:write

| | |
|---|---|
| **Grants** | Create, update and delete variables |
| **Endpoints** | `POST`, `PATCH .../:key` and `DELETE .../:key` on `/user/vars`, `/orgs/:slug/vars` and `/workspaces/:slug/vars`, and every `vars:read` endpoint |
| **Implies / implied by** | Implies `vars:read` |
| **Workspace access** | write |
| **Notes** | — |

### Session and audit scopes

#### sessions:read

| | |
|---|---|
| **Grants** | Read agent sessions, their token usage, and workspace cost summaries |
| **Endpoints** | `GET /sessions`, `GET /sessions/:id`, `GET /sessions/:id/usage`, `GET /workspaces/:slug/cost` |
| **Implies / implied by** | Implied by `sessions:write` |
| **Workspace access** | read, and the workspace must be active |
| **Notes** | Any other workspace is `403 workspace access denied`. `GET /sessions` returns the sessions of the active workspaces the caller may read; an admin token sees all. |

#### sessions:write

| | |
|---|---|
| **Grants** | Open and complete agent sessions, and report their token usage |
| **Endpoints** | `POST /sessions`, `POST /sessions/:id/complete`, `POST /sessions/:id/usage` |
| **Implies / implied by** | Implies `sessions:read` |
| **Workspace access** | write, to open a session; completing and reporting usage need the session's creator or an admin token |
| **Notes** | A caller who is not the session's creator gets `403 not session owner`. |

#### audit:read

| | |
|---|---|
| **Grants** | Query audit events, traces, session outcomes, tool calls, tool errors, postmortems and transcripts; run unified audit queries; follow the event stream |
| **Endpoints** | `GET /workspaces/:slug/runs/:run_id/events`, `GET .../traces`, `GET .../sessions/outcomes`, `GET .../tools/calls`, `GET .../tools/errors`, `GET .../postmortem`, `GET .../transcript`, `GET /audit`, `GET /events` |
| **Implies / implied by** | Implied by `audit:write` |
| **Workspace access** | read, on a workspace of any status |
| **Notes** | `GET /audit` and `GET /events` return the events of the workspaces the caller may read; hub events without a workspace go to admin tokens only. |

#### audit:write

| | |
|---|---|
| **Grants** | Ingest audit events, traces, session outcomes, tool calls, tool errors and postmortems |
| **Endpoints** | `POST /workspaces/:slug/runs/:run_id/events`, `POST .../events/batch`, `POST .../traces`, `POST .../traces/batch`, `POST .../sessions/outcomes`, `POST .../tools/calls`, `POST .../tools/errors`, `POST .../postmortem` |
| **Implies / implied by** | Implies `audit:read` |
| **Workspace access** | owner (or an admin token) |
| **Notes** | Anyone else, a member included, gets `403` with `error_type: workspace_access_denied`; an archived workspace is `409 workspace_archived` first. |

### Sandbox scopes

Both scopes are registered on every start, whether or not sandboxes are
configured, so PATs can carry them before an operator turns sandboxes on.
Without a `[sandbox]` section every sandbox route answers `404` with
`error_type: sandboxes_disabled` to every authenticated caller, before the scope
is checked.

#### sandboxes:read

| | |
|---|---|
| **Grants** | List the deployment's sandbox profiles (names, images, resources, network, TTL, secret names and env key names, never a value); list and read sandbox leases |
| **Endpoints** | `GET /sandbox-profiles`, `GET /workspaces/:slug/sandboxes`, `GET /workspaces/:slug/sandboxes/:id`; `afc sandbox profiles`, `list`, `get` |
| **Implies / implied by** | Implied by `sandboxes:write` |
| **Workspace access** | profiles: none, every caller sees the same list; leases: read |
| **Notes** | `GET /sandboxes` (the leases of every workspace) needs an admin token: an API key or a PAT holding both scopes gets `403`. |

#### sandboxes:write

| | |
|---|---|
| **Grants** | Issue sandbox leases and end them. The lease token is returned only in the response that issues the lease. |
| **Endpoints** | `POST /workspaces/:slug/sandboxes`, `DELETE /workspaces/:slug/sandboxes/:id`; `afc sandbox create`, `destroy` |
| **Implies / implied by** | Implies `sandboxes:read` |
| **Workspace access** | write |
| **Notes** | Issuing needs an active workspace; ending works on an archived one. |

### Forge scopes

The hub reads and writes the forge with the workspace's own stored git
credential, never with the caller's, a member's reads included. Responses name
the credential's kind, never its value. Both scopes are registered on every
start.

#### forge:read

| | |
|---|---|
| **Grants** | Read a workspace's repository, issues, comments and pull requests (state, files, CI checks, reviews) on its forge; see how the hub reaches the forge and whether its webhook is set up; list the webhook deliveries the hub received |
| **Endpoints** | `GET /workspaces/:slug/forge`, `GET .../forge/deliveries`, `GET .../forge/repository`, `GET .../issues`, `GET .../issues/:n`, `GET .../issues/:n/comments`, `GET .../pulls/:n`, `GET .../pulls/:n/state`, `GET .../pulls/:n/files`, `GET .../pulls/:n/checks`, `GET .../pulls/:n/reviews` |
| **Implies / implied by** | Implied by `forge:write` |
| **Workspace access** | read |
| **Notes** | Reads work on archived workspaces and on workspaces that are not cloned. |

#### forge:write

| | |
|---|---|
| **Grants** | Change a workspace's repository on its forge: create, update and close issues, comment on issues and pull requests, add and remove issue labels, make sure a repository label exists, open pull requests; set and remove the secret the forge signs its webhook deliveries with |
| **Endpoints** | `POST /workspaces/:slug/issues`, `PATCH .../issues/:n`, `POST .../issues/:n/close`, `POST .../issues/:n/comments`, `POST .../issues/:n/labels`, `DELETE .../issues/:n/labels/:name`, `POST .../labels`, `POST .../pulls`, `POST .../pulls/:n/comments`, `PUT .../forge/webhook`, `DELETE .../forge/webhook`, and every `forge:read` endpoint |
| **Implies / implied by** | Implies `forge:read` |
| **Workspace access** | write |
| **Notes** | A PAT with only `forge:read` gets `403 PAT requires forge:write scope` on every write. The nine writes and `PUT .../forge/webhook` need a workspace that is not archived (`400 workspace is archived`, after the access check); `DELETE .../forge/webhook` works on any status. A write that reaches the forge is recorded as a `hub.forge.*` audit event naming the caller; `PUT` and `DELETE .../forge/webhook` record `hub.forge.webhook.set` and `.unset`, never the secret. |

---

## Complete Permission List

All 30 registered scopes. "Framework" scopes are the account scopes built into
the server framework; "hub" scopes belong to the hub.

| Scope | Source | Implies | Grants |
|-------|--------|---------|--------|
| [`audit:read`](#auditread) | hub | — | Read audit data, transcripts and the event stream |
| [`audit:write`](#auditwrite) | hub | `audit:read` | Ingest audit data |
| [`forge:read`](#forgeread) | hub | — | Read a workspace's forge |
| [`forge:write`](#forgewrite) | hub | `forge:read` | Write to a workspace's forge; manage its webhook secret |
| [`git:read`](#gitread) | hub | — | Clone and fetch |
| [`git:write`](#gitwrite) | hub | `git:read` | Push |
| [`keys:manage`](#keysmanage) | framework | — | Revoke own API keys |
| [`keys:read`](#keysread) | framework | — | Reserved |
| [`merges:read`](#mergesread) | hub | — | Read merge jobs |
| [`merges:write`](#mergeswrite) | hub | `merges:read` | Submit, cancel, requeue merges; rebase |
| [`orgs:read`](#orgsread) | framework | — | List own org memberships |
| [`orgs:write`](#orgswrite) | framework | — | Reserved |
| [`patches:read`](#patchesread) | hub | — | Read patches |
| [`patches:write`](#patcheswrite) | hub | `patches:read` | Change patches |
| [`rebuilds:read`](#rebuildsread) | hub | — | Read rebuilds; preview |
| [`rebuilds:write`](#rebuildswrite) | hub | `rebuilds:read` | Submit, cancel, requeue, roll back rebuilds |
| [`sandboxes:read`](#sandboxesread) | hub | — | List profiles; read leases |
| [`sandboxes:write`](#sandboxeswrite) | hub | `sandboxes:read` | Issue and end leases |
| [`secrets:read`](#secretsread) | hub | — | List secret names |
| [`secrets:write`](#secretswrite) | hub | `secrets:read` | Change secrets |
| [`sessions:read`](#sessionsread) | hub | — | Read sessions, usage, cost |
| [`sessions:write`](#sessionswrite) | hub | `sessions:read` | Open and complete sessions; report usage |
| [`tokens:manage`](#tokensmanage) | framework | `tokens:write` | Create and revoke PATs; mint bound tokens |
| [`tokens:read`](#tokensread) | framework | — | Read own PATs |
| [`tokens:write`](#tokenswrite) | framework | — | Change PAT permissions |
| [`users:read`](#usersread) | framework | — | Read and update own profile |
| [`vars:read`](#varsread) | hub | — | Read variables |
| [`vars:write`](#varswrite) | hub | `vars:read` | Change variables |
| [`workspaces:read`](#workspacesread) | hub | — | List and view workspaces |
| [`workspaces:write`](#workspaceswrite) | hub | `workspaces:read` | Create and change workspaces |

---

## Endpoint Access Matrix

Every route, what a PAT needs and which [workspace access](#workspace-access-levels)
the caller needs. Paths are relative to the mount point, `/api/v1` by default
(`server.mount_point` in [configuration.md](configuration.md#server)), except
`GET /healthz`, `GET /readyz`, `GET /version`, `GET /metrics`, the `/git/...`
routes and `POST /hooks/forge/:slug`, which sit outside it.

API keys need no scope; admin tokens need neither scope nor access, except where
a row says otherwise.

### Account and token endpoints

These routes act on the caller's own account.

| Endpoint | PAT scope | Notes |
|----------|-----------|-------|
| `GET /user`, `PATCH /user` | `users:read` | own profile |
| `GET /user/orgs` | `orgs:read` | own memberships |
| `GET /user/keys` | not checked (`keys:read` is reserved) | own keys |
| `POST /user/keys/:key_id/refresh` | refused: API key only | own key; `404` for another user's |
| `DELETE /user/keys/:key_id` | `keys:manage` | own key; a key may revoke itself |
| `POST /user/tokens` | `tokens:manage` | a PAT grants only a subset of its own scopes |
| `GET /user/tokens`, `GET /user/tokens/:token_id` | `tokens:read` | own PATs; `404` for another user's |
| `DELETE /user/tokens/:token_id` | `tokens:manage` | own PAT; `404` for another user's |
| `PUT`, `PATCH`, `DELETE /user/tokens/:token_id/permissions` | `tokens:write` or `tokens:manage` | own PAT |

### Admin endpoints

Open to admin tokens and admin-role API keys (see [Admin role](#admin-role)); a
user-role API key and every PAT get `403`.

| Endpoint | Description |
|----------|-------------|
| `POST /users`, `GET /users`, `GET /users/:id`, `PATCH /users/:id` | create, list, read and update any user |
| `POST /users/:id/promote`, `POST /users/:id/demote` | grant and remove the admin role |
| `POST /users/:id/block`, `POST /users/:id/unblock` | block and unblock a user |
| `GET /users/:id/keys`, `DELETE /users/:id/keys/:key_id` | list and revoke any user's API keys |
| `GET /users/:id/tokens`, `DELETE /users/:id/tokens/:token_id` | list and revoke any user's PATs |
| `POST /orgs`, `GET /orgs`, `GET /orgs/:id`, `PATCH /orgs/:id`, `DELETE /orgs/:id` | create, list, read, update and delete any org, member or not |
| `POST /orgs/:id/block`, `POST /orgs/:id/unblock` | block and unblock an org |
| `GET /orgs/:id/members`, `PUT /orgs/:id/members/:user_id`, `DELETE /orgs/:id/members/:user_id` | list, add and remove org members |

### Workspace endpoints

| Endpoint | PAT scope | Access |
|----------|-----------|--------|
| `POST /workspaces` | `workspaces:write` | none; the caller becomes the owner; admin tokens `403` |
| `GET /workspaces` | `workspaces:read` | lists the readable workspaces |
| `GET /workspaces/:slug` | `workspaces:read` | read |
| `PATCH /workspaces/:slug` | `workspaces:write` | write |
| `POST /workspaces/:slug/archive`, `POST .../reactivate` | `workspaces:write` | write |
| `DELETE /workspaces/:slug` | `workspaces:write` | write; the workspace must be archived |
| `POST /workspaces/:slug/sync`, `POST .../reclone` | `workspaces:write` | write |
| `POST /workspaces/:slug/branches/:name/push` | `workspaces:write` | write |
| `GET /workspaces/:slug/rerere` | `workspaces:read` | read |
| `DELETE /workspaces/:slug/rerere/*pathspec` | `workspaces:write` | write |
| `GET /workspaces/:slug/patch-status` | `workspaces:read` | read |
| `POST /workspaces/:slug/tokens` | `tokens:manage` | private; active workspace; admin tokens cannot mint |
| `GET /workspaces/:slug/tokens` | `tokens:read` or `tokens:manage` | private |

### Carry-patch endpoints

| Endpoint | PAT scope | Access |
|----------|-----------|--------|
| `GET /workspaces/:slug/patches`, `GET .../patches/:id` | `patches:read` | read |
| `POST .../patches`, `PATCH .../patches/:id`, `DELETE .../patches/:id`, `POST .../patches/:id/restore`, `POST .../patches/:id/reset-to-origin`, `POST .../patches/reorder` | `patches:write` | write |
| `GET /workspaces/:slug/rebuilds`, `GET .../rebuilds/:id`, `GET .../rebuild-preview` | `rebuilds:read` | read |
| `POST .../rebuild`, `DELETE .../rebuilds/:id`, `POST .../rebuilds/:id/requeue`, `POST .../rebuilds/:id/rollback` | `rebuilds:write` | write |

### Merge endpoints

| Endpoint | PAT scope | Access |
|----------|-----------|--------|
| `GET /workspaces/:slug/merges`, `GET .../merges/:id` | `merges:read` | read |
| `POST .../merges`, `DELETE .../merges/:id`, `POST .../merges/:id/requeue`, `POST /workspaces/:slug/rebase` | `merges:write` | write |

### Git endpoints

Outside the mount point; authenticated with HTTP Basic (see
[Presenting a credential](#presenting-a-credential)).

| Endpoint | PAT scope | Access |
|----------|-----------|--------|
| `GET /git/:org/:slug.git/info/refs?service=git-upload-pack` | `git:read` | read |
| `POST /git/:org/:slug.git/git-upload-pack` | `git:read` | read |
| `GET /git/:org/:slug.git/info/refs?service=git-receive-pack` | `git:write` | write (member: pkt-line `403`) |
| `POST /git/:org/:slug.git/git-receive-pack` | `git:write` | write (member: pkt-line `403`) |

Anyone without read access gets a pkt-line `404 repository not found`. An
admin-role API key is not admin here.

### Secrets and variables endpoints

| Operation | Secrets scope | Variables scope |
|-----------|---------------|-----------------|
| List (`GET`) | `secrets:read` | `vars:read` |
| Resolved (`GET /workspaces/:slug/vars/resolved`) | — | `vars:read` |
| Create (`POST`) | `secrets:write` | `vars:write` |
| Update (`PATCH .../:key`) | `secrets:write` | `vars:write` |
| Delete (`DELETE .../:key`) | `secrets:write` | `vars:write` |

Who may reach each tier, with the scope above when the credential is a PAT:

| Tier | Admin token | API key or PAT |
|------|-------------|----------------|
| User (`/user/secrets`, `/user/vars`) | `403`: an admin token has no user | the caller's own |
| Org (`/orgs/:slug/secrets`, `/orgs/:slug/vars`) | any org | members of the org; anyone else `404` |
| Workspace secrets (`/workspaces/:slug/secrets`) | any workspace | private: the owner; a member and anyone else `404 workspace not found` |
| Workspace variables (`/workspaces/:slug/vars`) | any workspace | reads (list, resolved): read; writes: write (a member gets `403 forbidden`) |

### Session, cost and audit endpoints

These routes use the same access levels but answer refusals in their own way.

| Endpoint | PAT scope | Access | A member gets | Anyone else gets |
|----------|-----------|--------|---------------|------------------|
| `POST /sessions` | `sessions:write` | write | `403 org members have read-only access to this workspace`, `error_type: forbidden` | `404 workspace not found` |
| `POST /sessions/:id/complete`, `POST /sessions/:id/usage` | `sessions:write` | the session's creator or an admin token | `403 not session owner` | `403 not session owner` |
| `GET /sessions/:id`, `GET /sessions/:id/usage`, `GET /workspaces/:slug/cost`, `GET /sessions?workspace_slug=` | `sessions:read` | read, active workspace | the owner's answer | `403 workspace access denied` |
| `GET /sessions` | `sessions:read` | the active workspaces the caller may read | the sessions of owned and member workspaces | the sessions of its own workspaces |
| per-run reads (`events`, `traces`, `sessions/outcomes`, `tools/calls`, `tools/errors`, `postmortem`) and `GET .../transcript` | `audit:read` | read, any workspace status | the owner's answer | `404 workspace not found` |
| `GET /audit`, `GET /events` | `audit:read` | the workspaces of any status the caller may read (the stream fixes the set when it opens) | the events of owned and member workspaces, never a hub event without a workspace | `404 workspace not found` for a `workspace` filter outside the set |
| the eight ingestion routes (`POST /workspaces/:slug/runs/:run_id/...`) | `audit:write` | owner | `403`, `error_type: workspace_access_denied` | `403`, `error_type: workspace_access_denied` |

On the ingestion routes an archived workspace is `409 workspace_archived` before
the ownership check, and an unknown slug is `404`.

### Sandbox endpoints

| Endpoint | PAT scope | Access |
|----------|-----------|--------|
| `GET /sandbox-profiles` | `sandboxes:read` | none; every caller sees the same list |
| `POST /workspaces/:slug/sandboxes` | `sandboxes:write` | write; the workspace must not be archived |
| `GET /workspaces/:slug/sandboxes`, `GET .../sandboxes/:id` | `sandboxes:read` | read |
| `DELETE /workspaces/:slug/sandboxes/:id` | `sandboxes:write` | write; works on an archived workspace |
| `GET /sandboxes` | admin token only | every workspace; API keys and PATs `403` |

Without a `[sandbox]` section every route above answers `404` with
`error_type: sandboxes_disabled`, before the scope is checked.

### Forge endpoints

| Endpoint | PAT scope | Access |
|----------|-----------|--------|
| `GET /workspaces/:slug/forge` and the nine reads (`.../forge/repository`, `.../issues`, `.../issues/:n`, `.../issues/:n/comments`, `.../pulls/:n`, `.../pulls/:n/state`, `.../pulls/:n/files`, `.../pulls/:n/checks`, `.../pulls/:n/reviews`) | `forge:read` | read |
| `GET /workspaces/:slug/forge/deliveries` | `forge:read` | read; works on archived workspaces |
| the nine writes (`POST .../issues`, `PATCH .../issues/:n`, `POST .../issues/:n/close`, `POST .../issues/:n/comments`, `POST .../issues/:n/labels`, `DELETE .../issues/:n/labels/:name`, `POST .../labels`, `POST .../pulls`, `POST .../pulls/:n/comments`) | `forge:write` | write; not archived (`400`) |
| `PUT /workspaces/:slug/forge/webhook` | `forge:write` | write; not archived (`400`) |
| `DELETE /workspaces/:slug/forge/webhook` | `forge:write` | write; works on archived workspaces |
| `POST /hooks/forge/:slug` | no credential | see below |

The order of the checks on a forge route: no credential is `401`; a PAT without
the scope, or a credential type the hub does not know, is `403` before the
workspace is looked up; a workspace that is missing or that the caller may not
read is `404`; on a write, a member of its org is `403 forbidden` and an archived
workspace is `400`.

The webhook ingress `POST /hooks/forge/:slug` takes no bearer credential and no
scope. It sits outside the mount point, beside `/git/...`, and accepts a delivery
only when its signature (GitHub) or token (GitLab) matches the workspace's
`FORGE_WEBHOOK_SECRET`. A workspace without the secret and a slug that does not
exist answer the same `404`. The secret is managed with `forge:write` and the
deliveries are read with `forge:read`. See
[The webhook ingress](api.md#the-webhook-ingress).

---

## Retired scope names

Earlier releases had finer-grained scopes; each was folded into the `read` or
`write` scope of its resource ([ADR 06](adr/06-collapse-scopes-to-read-and-write.md)).
`POST /user/tokens` refuses the old names with `400` like any unknown scope. At
every start the hub rewrites stored PATs that still carry one:

| Old scope | Rewritten to |
|-----------|--------------|
| `workspaces:create`, `workspaces:delete`, `workspaces:sync` | `workspaces:write` |
| `secrets:list` | `secrets:read` |
| `secrets:manage`, `secrets:delete` | `secrets:write` |
| `vars:manage`, `vars:delete` | `vars:write` |

Because `workspaces:write` implies `workspaces:read`, a rewritten token may list
workspaces even if it could not before.

---

## Extending the Permission Registry

This section is for developers adding a hub module. A module registers its
scopes with the server at start, alongside the scopes above, and checks them in
its handlers; PAT creation then accepts the new names. Follow the
`resource:read` / `resource:write` shape, with write implying read. How
registration and the checks are wired is described in
[architecture.md](architecture.md).
