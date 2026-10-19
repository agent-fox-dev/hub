# Serve the forge for a workspace

Status: **proposed** (2026-10-19). Nothing here is implemented. This is
item 2 of what the agent-fox service PRD asks of hub
([agent-fox `docs/prd/01`](https://github.com/agent-fox-dev/agent-fox/blob/main/docs/prd/01-triage-and-fix-forge-issues-as-a-service.md),
§9), written to its decisions D4 (hub owns every forge access), D5 (the
label protocol is created through hub), D6 (triage rewrites the issue
body through hub) and D11 (forge webhooks reach the service through hub's
event stream). It is built on `issuex` once that package lives in
[`aflib`](https://github.com/agent-fox-dev/aflib) (aflib PRD 01), which
is this PRD's one dependency. Details left to settle at spec time are
marked *spec time*.

## Intent

A workspace in hub is a repository on a forge: GitHub, GitLab, later
Forgejo. Hub already holds the credential for that repository (`GIT_PAT`,
or a username and password) and uses it to clone, fetch and push. It does
not use it for anything the forge offers beyond git: issues, labels,
comments, pull requests, webhooks. The agent-fox service needs exactly
those, on the workspace's repository, and under the service PRD's
decisions it may hold no forge credential of its own.

This PRD gives hub a **forge API** per workspace: the issue, label,
comment and pull-request operations `issuex` implements, served under the
workspace with the workspace's own credential, under two new scopes; a
**webhook ingress** that verifies a forge's deliveries for a workspace and
relays them as hub audit events on the SSE stream, so a consumer learns
of a label without polling; and `afc issue`, `afc pr` and `afc forge`
for a person. Hub stays what it is: it stores, relays and authorizes; it
has no opinion about what an issue says.

## Goals

1. A caller with a hub credential and `forge:read` or `forge:write` on a
   workspace can list, read, create and update issues, add and remove
   labels, create labels, post comments, and create and read pull
   requests on the workspace's repository, with hub's credential and
   never its own.
2. On a `carry_patch` workspace the same calls can target the upstream
   repository, with the upstream credential hub already resolves for
   sync, so a patch can be proposed upstream through hub.
3. A forge webhook for a workspace is received by hub, verified against a
   per-workspace secret, deduplicated by delivery id, and relayed as a
   `hub.forge.*` audit event that carries the workspace, so it reaches
   `GET /api/v1/events?workspace=` and the audit query.
4. Every forge write is an audit event with the actor, the target and the
   outcome; no event carries a credential.
5. A person can do the same from `afc`, and `afc forge status` says which
   forge a workspace is on, whether hub has a credential for it, and
   whether its webhook is configured.
6. Everything is testable offline against `issuex`'s `httptest` forges and
   hub's `httptest` handlers.

## Non-goals

- **A general forge proxy.** The surface is the `issuex.Client` interface
  and no more: no arbitrary REST passthrough, no GraphQL, no repository
  administration.
- **Merging or closing pull requests through hub** in the first release.
  `issuex` has `MergePullRequest` and `ClosePullRequest`; the agent-fox
  service does not merge, and a person merges on the forge. A later PRD
  may add them under a scope of their own.
- **Reading or writing a forge the workspace is not on.** The target is
  the workspace's `git_url` repository or, on a carry-patch workspace,
  its `upstream_url`. Nothing else.
- **A GitHub App, GitLab OAuth application or Forgejo application.** The
  credential is the workspace's reserved secret. An app installation is a
  later credential type on the same seam.
- **Forgejo**, until `issuex` has its backend (a later aflib PRD). The
  ingress and the API are written so that it is a backend, not a change.
- **Changing `issuex`.** Hub uses it as it is; what hub needs and it lacks
  is a finding for aflib.

## Background: what exists

| Need | Exists | Where |
|---|---|---|
| The forge client | `issuex.NewWithOptions(Options{BaseURL, Token, RemoteURL, Repo, HTTPClient})`; detection from the remote URL's host; `ErrAmbiguousForge` sends nothing to an unknown host | aflib `issuex` |
| The workspace's credential | `GIT_PAT`, or `GIT_USERNAME` + `GIT_PASSWORD`, resolved by `workspace.ResolveCloneAuth(store, slug)`; the upstream's by `ResolveUpstreamAuthMethod` | hub `internal/workspace`, `internal/secrets` |
| The workspace's repository and upstream | `git_url`, `upstream_url`, `workspace_mode`, `branch`, `integration_branch` | hub `internal/workspace` |
| Scopes and ownership | `apikit.Permission` registration; `wsaccess.AuthorizeWorkspace`; 404 for a workspace the caller does not own | hub |
| Audit events and SSE | `audit.Emitter`, `HubEvent`, `GET /api/v1/events` with a `workspace` filter, `GET /api/v1/audit` | hub `internal/audit` |
| Unauthenticated ingress outside `/api/v1` | the git server at `/git/...` with its own authentication | hub `internal/gitserver` |
| Workspace variables | `GET /workspaces/:slug/vars/resolved`; `store.GetVariableValue` | hub `internal/secrets`, `internal/vars` |

What does not exist: any call to a forge API from hub, a webhook
endpoint, a per-workspace webhook secret, a delivery log, or a `forge`
command in `afc`.

## 1. Vocabulary

| Term | Meaning |
|---|---|
| **Forge** | The service hosting the workspace's repository: GitHub (including GitHub Enterprise), GitLab (including self-hosted), later Forgejo. Detected from `git_url`'s host, overridable per workspace. |
| **Target** | Which repository a call addresses: `origin` (the workspace's `git_url`, the default) or `upstream` (`upstream_url`, carry-patch workspaces only). |
| **Delivery** | One webhook HTTP request from the forge, identified by the forge's delivery id. |
| **Forge event** | The hub audit event hub emits for a delivery it accepted, `hub.forge.<resource>.<action>`. |

## 2. The forge client hub builds

For every call, hub builds an `issuex.Client` for the workspace and the
target, from facts it holds and nothing from the request:

| Field | From |
|---|---|
| `RemoteURL` | `git_url` for `origin`; `upstream_url` for `upstream` |
| `Repo` | parsed from the remote URL (`issuex.ParseRemote`), so GitLab subgroups are kept |
| `Token` | `GIT_PAT` for `origin`; `UPSTREAM_GIT_PAT` falling back to `GIT_PAT` for `upstream`; a username and password pair is sent as the token the forge accepts (*spec time*: `issuex` takes a token only; a basic-auth pair becomes the password, which both forges accept for a PAT-shaped password, else 422 `credential_unsupported`) |
| `BaseURL` | the workspace variable `FORGE_API_URL` when set (a self-hosted GitLab, a GitHub Enterprise host whose name says neither), else `issuex`'s detection from the host |
| `HTTPClient` | hub's, with a per-call timeout (30 s; `issuex` adds its retries inside it) and no proxy from the environment |
| `UserAgent` | `af-hub/<version>` |

A workspace with no credential gets an unauthenticated client: reads of a
public repository work, writes answer the forge's 401 as 502
`forge_unauthenticated`. A host `issuex` cannot classify, and no
`FORGE_API_URL`, answers 422 `forge_unknown` naming the host and the
variable, before any request leaves hub. Clients are built per request
and not cached: a rotated secret is used on the next call.

## 3. The API

Under `/api/v1`, hub authentication, ownership as everywhere (404 for a
workspace the caller does not own), in `openapi.yaml`. Two new scopes:
`forge:read` and `forge:write`; API keys and admin tokens have both. Every
route takes `?target=origin|upstream`; `upstream` on a `standard`
workspace is 400.

```
GET    /workspaces/:slug/forge                          the forge, repository, credential presence, webhook state
GET    /workspaces/:slug/forge/repository               issuex.GetRepository: default branch, private, permissions, merge methods

GET    /workspaces/:slug/issues?state=&labels=&assignee=&sort=&direction=&limit=     issuex.ListIssues
POST   /workspaces/:slug/issues                         {title, body?, labels?, assignees?}          → 201 Issue
GET    /workspaces/:slug/issues/:n                      issuex.ReadIssue: the issue and its comments (truncation flagged)
PATCH  /workspaces/:slug/issues/:n                      {title?, body?, state?, labels?, assignees?} → Issue
POST   /workspaces/:slug/issues/:n/close                {comment?}
GET    /workspaces/:slug/issues/:n/comments             issuex.ListComments
POST   /workspaces/:slug/issues/:n/comments             {body}                                       → 201 {url}
POST   /workspaces/:slug/issues/:n/labels               [names]                                      → 204
DELETE /workspaces/:slug/issues/:n/labels/:name                                                      → 204
POST   /workspaces/:slug/labels                         {name, color?, description?}                 → 201; 200 when it exists unchanged

POST   /workspaces/:slug/pulls                          {title, body?, head, base, draft?}           → 201 PullRequest
GET    /workspaces/:slug/pulls/:n                       issuex.ReadPullRequest
GET    /workspaces/:slug/pulls/:n/state                 issuex.GetPRState
GET    /workspaces/:slug/pulls/:n/files                 issuex.ReadChangedFiles
GET    /workspaces/:slug/pulls/:n/checks                issuex.GetCIChecks
GET    /workspaces/:slug/pulls/:n/reviews               issuex.GetPRReviews
POST   /workspaces/:slug/pulls/:n/comments              {body}: issuex.PostReviewComment              → 201

PUT    /workspaces/:slug/forge/webhook                  {secret}: set the delivery secret; → the ingress URL
DELETE /workspaces/:slug/forge/webhook                  forget it; deliveries answer 404 afterwards
GET    /workspaces/:slug/forge/deliveries?since=&limit= the delivery log: id, event, received_at, outcome
```

Reads need `forge:read`; `POST`, `PATCH`, `DELETE` and the webhook routes
need `forge:write`. Bodies and responses are `issuex`'s types encoded as
they are (`Issue`, `IssueThread`, `PullRequest`, …), so a client of hub
and a user of the package read the same shape. An issue number on GitLab
is the project `iid`, as `issuex` already has it.

**`GET /forge`:**

```jsonc
{
  "forge": "github",                         // github | gitlab | unknown
  "api_url": "https://api.github.com",
  "repository": { "owner": "acme", "name": "widgets", "host": "github.com" },
  "credential": "pat",                       // pat | basic | none
  "upstream": { "forge": "github", "repository": { … }, "credential": "pat" },   // carry-patch only
  "webhook": { "configured": true, "url": "https://hub.example.com/hooks/forge/widgets", "last_delivery_at": "…" }
}
```

**Errors**, mapped once from `issuex`'s: a forge 404 is 404
`forge_not_found`; a forge 401 or 403 is 502 `forge_unauthenticated` or
`forge_forbidden`; a rate limit `issuex` could not wait out is 429 with
the forge's `Retry-After`; a body over the forge's limit is 413
`body_too_large` naming the limit (`issuex` does not cut it, hub refuses
it); a network failure is 502 `forge_unreachable`; `ErrAmbiguousForge`
is 422 `forge_unknown`. The forge's own message is in `message` with any
URL userinfo removed.

**Audit.** Every write emits one event: `hub.forge.issue.create`,
`hub.forge.issue.update`, `hub.forge.issue.close`, `hub.forge.comment.create`,
`hub.forge.label.add`, `hub.forge.label.remove`, `hub.forge.label.create`,
`hub.forge.pull_request.create`, `hub.forge.pull_request.comment`, with
`workspace`, `target`, the number, the URL the forge returned, and `ok`
with the error type on failure. Reads emit nothing.

## 4. The webhook ingress

```
POST /hooks/forge/:slug
```

Outside `/api/v1` and outside hub's bearer authentication, like
`/git/...`: the forge is the caller and it authenticates with the
signature. Hub verifies, records and relays; it never acts.

- **The secret** is the workspace secret `FORGE_WEBHOOK_SECRET`, a
  reserved name like `GIT_PAT`, set by `PUT /forge/webhook` (which
  generates one when the body has none and returns it once) or by `afc
  secrets create`. A workspace without it answers 404 to every delivery,
  so an unconfigured slug cannot be probed.
- **Verification** per forge, chosen by the headers present:
  `X-Hub-Signature-256` (HMAC-SHA256 of the body, GitHub and Forgejo),
  `X-Gitlab-Token` (the secret itself, GitLab). A missing or wrong
  signature is 401 with no body read beyond the limit; the body is held
  to 1 MiB (`server.max_body_size` applies).
- **Deduplication** by the forge's delivery id (`X-GitHub-Delivery`,
  `X-Gitlab-Event-UUID`, Forgejo's `X-Forgejo-Delivery`): a repeat is 202
  and emits nothing. Ids are kept for 7 days in `forge_deliveries` with
  the event name, the outcome and `received_at`; the retention worker
  prunes them.
- **The events relayed**, as `hub.forge.*` with `workspace` set and the
  forge's payload reduced to facts:

  | Delivery | Event | Metadata |
  |---|---|---|
  | issue opened, edited, closed, reopened | `hub.forge.issue.opened` … | `number`, `title`, `url`, `labels`, `actor` |
  | issue labeled, unlabeled | `hub.forge.issue.labeled`, `.unlabeled` | `number`, `label`, `labels` (all current), `url`, `actor` |
  | issue comment created | `hub.forge.comment.created` | `number`, `comment_url`, `actor` |
  | pull request opened, closed, merged, reopened, synchronize | `hub.forge.pull_request.opened` … | `number`, `url`, `head`, `base`, `merged`, `actor` |
  | push | `hub.forge.push` | `ref`, `before`, `after`, `actor` |
  | anything else | none; 202, recorded as `ignored` | |

  GitLab's issue hook carries label changes in `changes.labels`; hub
  emits one `labeled` or `unlabeled` per label that changed. The payload
  is never stored whole; the delivery log keeps the id, the event name
  and the outcome.
- **The response** is 202 for an accepted or ignored delivery, 401 for a
  bad signature, 404 for an unknown or unconfigured workspace, 413 for a
  body over the limit. Hub answers before it emits; a delivery whose
  event could not be stored is logged and still 202, because the forge
  retries on 5xx and a retry would be deduplicated into nothing.

The same `audit.Emitter` with broadcast carries the event to the SSE
stream, so a subscriber on `GET /api/v1/events?workspace=widgets` sees
`hub.forge.issue.labeled` within the stream's buffer limits, and `GET
/api/v1/audit` finds it afterwards. Forge events are category `hub`.

## 5. The CLI

```
afc forge status <slug>                                     GET /forge
afc forge webhook set <slug> [--secret s | --generate]       PUT /forge/webhook; prints the URL and, once, the secret
afc forge webhook unset <slug>
afc forge deliveries <slug> [--since t]

afc issue list <slug> [--state open] [--label l]... [--limit n] [--target upstream]
afc issue get <slug> <n>
afc issue create <slug> --title t [--body-file f | --body b] [--label l]...
afc issue update <slug> <n> [--title t] [--body-file f] [--state closed] [--label l]...
afc issue close <slug> <n> [--comment c]
afc issue comment <slug> <n> --body-file f | --body b
afc issue label <slug> <n> <label>...
afc issue unlabel <slug> <n> <label>
afc label create <slug> <name> [--color hex] [--description d]

afc pr create <slug> --title t --head h --base b [--body-file f] [--draft] [--target upstream]
afc pr get <slug> <n> | checks | reviews | files | state
afc pr comment <slug> <n> --body-file f | --body b
```

One JSON document on stdout, apikit's exit codes, `--body-file -` reads
stdin, as `afc secrets` does.

## 6. Order of work

1. **aflib PRD 01**: `issuex` in `aflib`; hub requires it.
2. **The client and the reads.** `GET /forge`, `/repository`, issue and
   pull-request reads, `forge:read`, the error mapping, `afc forge status`,
   `afc issue list|get`, `afc pr get|…`. Tests against `issuex`'s
   `httptest` forges through hub's handlers.
3. **The writes.** Issues, comments, labels, pull requests, `forge:write`,
   the audit events, the rest of `afc issue`, `afc pr`, `afc label`.
4. **The ingress.** `FORGE_WEBHOOK_SECRET`, `PUT`/`DELETE /forge/webhook`,
   `/hooks/forge/:slug`, verification for GitHub and GitLab, the delivery
   log and its retention, the relayed events on the stream, `afc forge
   webhook`, `afc forge deliveries`.
5. **Upstream targets** on carry-patch workspaces: `?target=upstream`
   with the upstream credential.
6. **Documentation:** `docs/api.md`, `openapi.yaml`, `docs/cli.md`,
   `docs/configuration.md` (`FORGE_API_URL`), `docs/permissions.md` (the
   scopes), `docs/architecture.md` (the ingress beside the git server),
   the reserved secret names in `docs/cli.md`, `README.md`.

Steps 4 and 5 are independent once 3 has landed.

## 7. Acceptance criteria

Offline, with `issuex`'s `httptest` GitHub and GitLab standing in for the
forge and the workspace's `git_url` pointing at them:

1. `GET /workspaces/widgets/issues?labels=af:triage&state=open` with an
   API key returns the `httptest` forge's issues, and the request the
   forge saw carried the workspace's `GIT_PAT` and hub's user agent, not
   the caller's bearer.
2. `PATCH /issues/42 {body}` rewrites the body on both forges; `POST
   /issues/42/labels ["af:ready"]` and `DELETE /issues/42/labels/af:fix`
   produce the forge requests `issuex`'s tests expect; each emits its
   audit event with `workspace: widgets` and no credential in its
   metadata.
3. `POST /labels {name: "af:triage"}` creates the label and a second call
   answers 200 with no second forge request.
4. `POST /pulls` on a `standard` workspace opens a pull request against
   `git_url`; with `?target=upstream` on a `carry_patch` workspace it
   opens it against `upstream_url` with the upstream credential, and on a
   `standard` workspace it answers 400.
5. A PAT with `forge:read` only answers 403 on every write; a workspace
   the caller does not own answers 404 on every route; a workspace whose
   host names neither forge and has no `FORGE_API_URL` answers 422
   `forge_unknown` with no request sent.
6. A signed GitHub `issues.labeled` delivery to `/hooks/forge/widgets`
   answers 202 and puts `hub.forge.issue.labeled` with `number`, `label`
   and `workspace` on an open SSE stream for the workspace and in `GET
   /api/v1/audit`; the same delivery id again answers 202 and emits
   nothing; a wrong signature answers 401; a GitLab issue hook with a
   label change does the same with `X-Gitlab-Token`.
7. A delivery for a workspace with no `FORGE_WEBHOOK_SECRET` answers 404;
   `PUT /forge/webhook` with `--generate` returns a secret once and `GET
   /forge` shows the webhook configured without it.
8. `afc issue create widgets --title t --body-file -` with the body on
   stdin prints the created issue; `afc forge status widgets` prints the
   forge, the repository, `credential: pat` and the webhook state.
9. A forge answering 429 with `Retry-After` beyond what `issuex` waits
   out is answered 429 with the header passed through; a forge answering
   401 is 502 `forge_unauthenticated`; a body over GitHub's 65 536
   characters is 413 before any request.

## 8. What is given up, stated plainly

- **Hub becomes a forge client**, with a public ingress, a delivery log
  and a credential it now uses for API calls as well as git. The blast
  radius of a leaked `GIT_PAT` was always the repository; it is unchanged,
  and now it is exercised more.
- **Reads use the workspace's credential**, so the forge's rate limit is
  spent by every reader of the workspace together. The agent-fox service
  polls through here; its interval is the lever.
- **The webhook secret is one more reserved secret**, and a forge must be
  able to reach hub's `external_url`. A hub behind a firewall runs on
  polling alone, which the service PRD already allows.
- **No merge, no close of pull requests, no Forgejo** in the first
  release. Each is a backend or a scope away, on the same seam.
- **The agent-fox service PRD names the events `forge.*`**; this PRD
  names them `hub.forge.*` to stay in hub's event namespace and category.
  A one-line erratum there follows this PRD's spec.

## Documentation

| Change | Update |
|---|---|
| the endpoints, the schemas, the errors, the events | `docs/api.md`, `docs/openapi.yaml` |
| `afc forge`, `afc issue`, `afc pr`, `afc label` | `docs/cli.md` |
| `FORGE_API_URL`, `FORGE_WEBHOOK_SECRET` | `docs/configuration.md`, the reserved names in `docs/cli.md` |
| `forge:read`, `forge:write` | `docs/permissions.md` |
| the ingress beside the git server, the forge client's construction | `docs/architecture.md`, an ADR for "hub is the workspace's forge client" |
| the aflib requirement | `go.mod`, `README.md`, `AGENTS.md` |
