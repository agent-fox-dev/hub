# Bind a personal access token to a workspace

Status: **proposed** (2026-10-20). Nothing here is implemented. This is
item 4 of what the agent-fox service PRD asks of hub
([agent-fox `docs/prd/01`](https://github.com/agent-fox-dev/agent-fox/blob/main/docs/prd/01-triage-and-fix-forge-issues-as-a-service.md),
§9, decision D3). It answers the question that PRD left open: hub's
README names a "workspace-scoped token" as a third credential type, and
nothing in the API mints one. Details left to settle at spec time are
marked *spec time*.

## Intent

The agent-fox service holds one hub credential per workspace it watches,
for the work it does when nobody is calling: polling the forge through
hub, leasing a sandbox, pushing a branch, registering a patch, reporting
a session. Today the only credential it can hold is a personal access
token (PAT), and a PAT with `workspaces:read` sees every workspace its
user owns. A service subscribed to one workspace therefore holds a token
good for all of them, and a token that leaks from one subscription
exposes the rest.

This PRD lets a PAT be **bound to one workspace** at creation. A bound
token keeps its scopes and loses its reach: every workspace-scoped
endpoint answers 404 for any other slug, every user-scoped and org-scoped
endpoint answers 403, and the git server serves it for that one
repository. The binding is hub's record, keyed by apikit's token id, so
apikit's PAT model does not change.

## Goals

1. A user can mint a PAT bound to a workspace they own, with any subset
   of the scopes they may grant, and hand it to a service; the service
   can do with it exactly what the scopes allow on that workspace and
   nothing anywhere else.
2. Every existing ownership check, in the API and in the git server,
   honours the binding with one rule in one place, so a new endpoint
   cannot forget it.
3. A bound token is visible as such: listing tokens shows the workspace,
   and a refused call says the token is bound, not that the resource is
   missing, when the caller may know the resource exists.
4. Deleting the workspace revokes its bound tokens; archiving does not.
5. Nothing changes for an unbound PAT, an API key or an admin token.

## Non-goals

- **Binding to an org, to several workspaces, or to a pattern.** One
  workspace. A service watching three workspaces holds three tokens.
- **Changing what scopes exist or imply.** The binding narrows where a
  scope applies, never what it allows.
- **Changing apikit's token table, creation flow or privilege-escalation
  rule.** The binding is a hub table beside apikit's, and the creation
  endpoint is hub's, delegating to apikit for the token itself.
- **Binding API keys or admin tokens.** They are a person's or an
  operator's.

## Background: what exists

| Piece | Where | What it does |
|---|---|---|
| PATs | apikit: `POST /api/v1/user/tokens {name, permissions, expires}`, the `tokens` table, `AuthInfo{CredentialType: "pat", TokenID, UserID, Permissions}` | a scoped credential for the user who minted it; a PAT may only mint a PAT with a subset of its own scopes |
| The ownership rule | `wsaccess.AuthorizeWorkspace(db, auth, slug)` and `workspace.lookupWorkspaceForAuth` | admin tokens see every workspace; every other credential only the workspaces it owns; a miss is 404 |
| The git server's rule | `gitserver.authorizeGitAccess` | the same by `UserID`, a pkt-line 404 on a miss |
| The audit ingestion rule | `internal/audit/auth.go` | "workspace-scoped PATs must match the URL slug"; the one place a binding is already assumed |
| User- and org-scoped endpoints | `/api/v1/user/*`, `/api/v1/orgs/:slug/*`, secrets and variables at those scopes | ownership by `UserID` and org membership |
| `afc tokens` | apikit's CLI | create, list, get, delete, permissions |

What does not exist: a record that ties a token id to a slug, a creation
path that writes one, and a check that reads it.

## 1. The binding

A hub table:

```sql
CREATE TABLE IF NOT EXISTS token_workspace_bindings (
	token_id   TEXT PRIMARY KEY,   -- apikit's token id
	slug       TEXT NOT NULL REFERENCES workspaces(slug) ON DELETE CASCADE,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_token_workspace_bindings_slug ON token_workspace_bindings (slug);
```

One row per bound token; a token with no row is unbound and behaves as
today. The row is written once, at creation, and never updated: a
binding cannot be moved or removed, only the token revoked.

## 2. Minting a bound token

```
POST /api/v1/workspaces/:slug/tokens        {name, permissions, expires?}  → 201 {token_id, token, name, permissions, workspace, expires_at}
GET  /api/v1/workspaces/:slug/tokens        the bound tokens of the workspace: token_id, name, permissions, expires_at, revoked_at, created_at
```

- **Who may mint.** The workspace's owner, with an API key or with a PAT
  holding `tokens:manage`, and an admin token (binding a token to a
  workspace on the owner's behalf; the token's user is the owner). The
  ownership rule of §3 applies to the slug.
- **What is minted.** apikit's PAT for the caller's user with the
  requested scopes and expiry, through apikit's own creation path, so
  the privilege-escalation rule (a PAT mints only a subset of its scopes)
  and the expiry rules hold unchanged; then the binding row, in the same
  transaction as far as apikit's path allows (*spec time*: whether apikit
  exports a creation function hub can call inside a transaction, or hub
  creates through the handler and writes the binding after, revoking the
  token if the write fails).
- **Scopes a bound token may carry.** Any scope apikit registers, with
  one rule: the user- and org-scoped ones (`users:read`, `orgs:*`,
  `keys:*`, `tokens:*`, `secrets:*` and `vars:*` at user and org scope)
  are accepted but inert, because §3 refuses those endpoints to a bound
  token. The response lists them; the documentation says they do
  nothing. Refusing them at creation would make a service's scope list
  depend on whether the token is bound, which is one more thing to get
  wrong.
- **Listing.** `GET /api/v1/user/tokens` (apikit's) is unchanged in shape
  and gains nothing; the hub route above lists a workspace's bound
  tokens, and `afc tokens list` shows `workspace` when the hub route
  reports one (*spec time*: a hub-side join in `afc`, since apikit's
  listing cannot know the binding).

## 3. The rule

`wsaccess.AuthorizeWorkspace` gains the binding check and stays the one
place:

```
admin token                      → any workspace
api key                          → the workspaces the user owns (today's rule; org members per hub PRD 04)
pat, unbound                     → the same as an api key, under its scopes
pat, bound to slug S             → the workspace S only, under its scopes; any other slug is 404
```

And two more places honour the same row:

- **The git server** (`authorizeGitAccess`): a bound token is served for
  `/git/<org>/<S>.git` under its `git:*` scopes and gets a pkt-line 404
  for any other repository.
- **User- and org-scoped endpoints**: a bound token answers 403 with
  `error_type: token_bound` and the message `this token is bound to
  workspace <S>`, because there is no resource to hide (the caller holds
  the token and knows it is bound). apikit's own handlers (`/user`,
  `/user/keys`, `/user/tokens`, `/orgs`) do not know the binding; hub
  mounts a middleware ahead of them that reads the binding for
  `CredentialType == "pat"` and refuses. *Spec time:* whether apikit
  offers a hook for that, or hub's middleware wraps apikit's group.
- **Sessions and audit**: `POST /sessions` with a bound token refuses a
  `workspace_slug` other than `S` with 404; the audit ingestion rule that
  already says "workspace-scoped PATs must match the URL slug" becomes a
  call to the same `AuthorizeWorkspace` and loses its own copy.
- **Workspace creation**: `POST /workspaces` with a bound token is 403
  `token_bound`; a bound token creates nothing.

The check is one indexed lookup by `token_id`, done once per request
where `AuthorizeWorkspace` already queries the workspace.

## 4. Lifecycle

- Revoking the token (`DELETE /user/tokens/:id`, admin's, or expiry)
  ends the binding's effect; the row is removed when apikit removes the
  token, or by a periodic sweep of bindings whose token no longer exists
  (*spec time*: which, depending on whether apikit deletes or marks
  revoked).
- Deleting the workspace cascades: every bound token is revoked through
  apikit's revocation and the rows go with the workspace. Archiving
  changes nothing: an archived workspace answers its usual 400s and 409s,
  and the token is still bound to it.
- Re-login mass-revokes the user's API keys today, not their PATs; a
  bound PAT survives a re-login, which is the property a service needs.

## 5. The CLI

```
afc tokens create --name svc --workspace widgets --permissions workspaces:read,git:write,...   hub's route; prints the token once
afc tokens list [--workspace widgets]                                                          shows workspace on bound tokens
```

`afc tokens create` without `--workspace` is apikit's command unchanged.
*Spec time:* whether `--workspace` is added to apikit's command through a
hook, or hub registers `afc workspace tokens create|list` beside it; the
first reads better, the second needs nothing from apikit.

## 6. Audit

`hub.token.bind` on creation (`token_id`, `workspace`, `permissions`,
`expires_at`, actor), `hub.token.bound_refused` on a 403 or 404 caused by
the binding (`token_id`, the slug or endpoint refused; never the token),
rate-limited to one per token per minute so a misconfigured service does
not flood the log.

## 7. Acceptance criteria

1. `POST /workspaces/widgets/tokens` by the owner mints a PAT whose
   `GET /workspaces/widgets` answers 200 and whose `GET /workspaces/gears`
   (also the owner's) answers 404; the same token's `GET /api/v1/user`
   answers 403 `token_bound`.
2. The bound token clones `/git/<org>/widgets.git` with `git:read` and
   gets a pkt-line 404 on `/git/<org>/gears.git`; with `git:write` it
   pushes to `widgets` and nowhere else.
3. `POST /sessions {workspace_slug: "gears"}` with the bound token is 404;
   with `"widgets"` it is 201; `POST /workspaces/widgets/runs/:id/events`
   is accepted and `.../gears/runs/...` is 404, through the one rule.
4. A PAT bound by a PAT: the creating PAT must hold `tokens:manage` and
   the new token's scopes must be a subset of its own, else apikit's
   refusal passes through unchanged.
5. `DELETE /workspaces/widgets` (after archive) leaves the bound token
   revoked: its next request is 401.
6. An unbound PAT, an API key and an admin token pass every existing
   test unchanged; the ownership tests of specs 01, 06, 15 and 24 are the
   regression suite.
7. `afc tokens create --workspace widgets` prints the token once and
   `afc tokens list --workspace widgets` shows it with `workspace:
   widgets` and no secret.

## 8. What is given up, stated plainly

- **Inert scopes.** A bound token can be minted with `orgs:read` and
  never use it. The alternative, refusing them, couples the scope list to
  the binding; the documentation names the inert set instead.
- **One more table and one more middleware** in front of apikit's own
  routes, which until now hub never wrapped. It is the price of not
  changing apikit's token model.
- **No rebinding.** A service moved to another workspace mints a new
  token. That is what a service should do anyway.

## Documentation

| Change | Update |
|---|---|
| the two routes, `token_bound`, the sessions and audit rule | `docs/api.md`, `docs/openapi.yaml` |
| `afc tokens create --workspace`, `afc tokens list` | `docs/cli.md` |
| the binding in the credential table, the inert scopes, the rule | `docs/permissions.md`, `README.md` (the credential types paragraph) |
| the table and the middleware | `docs/architecture.md` |
| the events | `docs/api.md` (Hub Audit Events) |
