---
spec_id: "32"
spec_name: "forge_webhook_ingress"
title: "Forge webhook ingress: verify, deduplicate and relay a workspace's forge deliveries"
status: "active"
created_at: "2026-10-20T14:19:45.423882Z"
updated_at: "2026-10-20T14:19:45.423882Z"
intent_hash: "50b0457a887918d9d42c04920e0117baabe3962d52185197e25f0df99987e45d"
schema_version: 2
source: "docs/prd/02-serve-the-forge-for-a-workspace.md"
---
## Intent

Let a forge (GitHub or GitLab) tell hub what happened on a workspace's repository, so that a consumer learns of an issue, label, comment, pull request or push without polling. Hub receives the forge's webhook deliveries on a public endpoint. It checks each one against a secret it holds for the workspace, records each delivery once, and relays it as a `hub.forge.*` audit event carrying the workspace. The events then reach the existing SSE stream and audit query. Hub only verifies, records and relays deliveries. It never acts on them and never calls the forge. Operators manage the secret and read the delivery log through the API and `afc`.

## Goals

1. `PUT /api/v1/workspaces/widgets/forge/webhook` with no secret in the body answers 200 with a generated 64-character hex secret and the ingress URL `<external_url>/hooks/forge/widgets`. Afterwards, `GET /api/v1/workspaces/widgets/forge` shows `webhook.configured: true` and the URL, and never the secret. No later response shows the secret again.
2. A GitHub `issues` delivery with action `labeled` and a correct `X-Hub-Signature-256` answers 202 with `{"outcome":"relayed"}`. It records exactly one `hub.forge.issue.labeled` event with `workspace: widgets`, `target: origin`, `number`, `label` and `labels`. The event appears on an open `GET /api/v1/events?workspace=widgets` stream and in `GET /api/v1/audit?workspace=widgets`.
3. The same delivery id sent again answers 202 with `{"outcome":"duplicate"}`. It records no event and adds no row.
4. A delivery with a wrong or missing signature answers 401. It records no event and writes no delivery-log row. A GitLab `Issue Hook` whose `changes.labels` adds one label and removes another, sent with the correct `X-Gitlab-Token`, records one `hub.forge.issue.unlabeled` and one `hub.forge.issue.labeled`.
5. A delivery to a workspace that has no `FORGE_WEBHOOK_SECRET`, and a delivery to a slug that does not exist, both answer 404 with identical bodies. A body over 1 MiB answers 413.
6. A correctly signed delivery about a repository that is neither the workspace's origin nor, on a carry-patch workspace, its upstream answers 202 with `{"outcome":"repository_mismatch"}` and records no event.
7. `GET /api/v1/workspaces/widgets/forge/deliveries` lists the recorded deliveries, newest first. After a prune pass, rows older than 7 days and rows of deleted workspaces are gone. `cmd/af-hub/main.go` starts the pruning.
8. `afc forge webhook set|unset` and `afc forge deliveries` each print one JSON document and use apikit's exit codes.
9. `make test` covers all of this offline, with signed test deliveries and no forge.

## Non-goals

- **Everything in the earlier scopes.** The forge client, the scopes, `GET /forge` without its `webhook` block, the reads and the error mapping belong to `30_forge_client_reads`. Writes, write audit events and `body_too_large` on writes belong to `forge_writes`. This spec does not restate them.
- **Registering the webhook on the forge.** Hub returns the URL and the secret. A person enters them in the forge's webhook settings. Hub does not call the forge's hook API.
- **Acting on a delivery.** Hub enqueues no job, writes nothing to the forge and runs no hook. A consumer acts on the events.
- **Forgejo or Gitea deliveries.** This includes `X-Forgejo-Delivery`, `X-Forgejo-Signature` and `X-Gitea-Signature`. GitLab's signing-token header (`webhook-signature`), GitHub's legacy SHA-1 `X-Hub-Signature`, and GitHub's form-encoded payloads are also out of scope.
- **A separate upstream webhook secret.** One workspace secret covers deliveries from the origin repository and, on a carry-patch workspace, from the upstream repository.
- **Storing payloads.** Hub keeps only the reduced facts in the event and the delivery log.
- Replaying or redelivering events from hub, rate limiting the ingress, Prometheus counters for deliveries, and a configurable retention period.
- Org-scoped or user-scoped `FORGE_WEBHOOK_SECRET` values. They are never read.

## Background

The input is `docs/prd/02-serve-the-forge-for-a-workspace.md`, section 4 and part of section 5. The input was split three ways. This is the third scope: it adds the inbound half to the forge client that `30_forge_client_reads` introduces. Under the agent-fox service's decision D11, forge webhooks reach the service through hub's event stream.

What exists today, read in the code:

- **Public routes outside the API group.** `gitserver.MountGitHandlers(server.Echo(), …)` mounts `/git/:org/:slug.git/*` on the Echo root with its own authentication. `main.go` also mounts `GET /metrics` on the root. apikit attaches bearer authentication to the API group only (`docs/api.md`: "Echo captures a group's middleware when a route is added"). apikit's body-size limit (`server.max_body_size`, 1MB by default) is installed with `e.Use` and applies to every route on the instance (see `gitBodyPassthrough` in `internal/gitserver/handlers.go`).
- **External URL.** `(*apikit.Server).ExternalURL()` returns `server.external_url`. `internal/workspace/routes.go` reads it to build `hub_url`, which is null when the value is unset.
- **Secrets.** `internal/secrets.Store` keeps base64-encoded values in SQLite. It provides `GetSecretValue(ownerType, ownerID, key)`, `CreateSecrets`, `UpdateSecret` and `DeleteSecret`, and returns `*NotFoundError` and `*ConflictError`. Keys are case-insensitive, and a scope holds at most 100 entries. "Reserved" names (`GIT_PAT`, `UPSTREAM_GIT_PAT`, …) are a documentation convention in `docs/cli.md` § Reserved secret names. The code does not enforce them. Deleting a workspace removes its secrets in `deleteWorkspace` (`internal/workspace/store.go`).
- **Audit and SSE.** `audit.NewEmitterWithBroadcast(auditStore, sseMgr)` is built in `main.go`. `Emit` inserts into `hub_audit_events`, swallows insert errors, and broadcasts only after a successful insert. `GET /events` and `GET /audit` filter by `workspace` and show non-admin callers only the workspaces they own. Event types starting with `hub.` are category `hub`. Event constants with doc comments live in `internal/audit/types.go`.
- **Retention precedent.** `audit.StartRetentionWorker` runs one pass at start and then one every hour, against DuckDB. `carrypatch.PurgeExpiredDeletedPatchesWithRefs` uses a 7-day cutoff built with `apikit.FormatUTC`, but nothing in production calls it (`internal/carrypatch/docs_test.go` TS-24-17). A pruner that is never scheduled is a known trap in this repository.
- **Forge package.** `internal/forge` comes from `30_forge_client_reads`. It provides `Config`, `RegisterRoutes(api, cfg)`, the scope helpers, the workspace lookup of `git_url`/`workspace_mode`/`upstream_url`, repository parsing through `issuex` (owner as the full namespace path), and `GET /forge`. `forge_writes` adds `Audit audit.Emitter` to `forge.Config`.
- **What is missing.** Hub has no webhook endpoint, no per-workspace webhook secret, no delivery log, and no `afc forge webhook` or `afc forge deliveries`.

## Requirements

### 1. The webhook secret

The webhook secret is the workspace secret `FORGE_WEBHOOK_SECRET`. Hub reads it only from the workspace scope: `GetSecretValue("workspace", slug, "FORGE_WEBHOOK_SECRET")`. It can be set through `PUT …/forge/webhook` or with `afc secrets create FORGE_WEBHOOK_SECRET --workspace <slug>`. Hub reads it again on every delivery and caches nothing, so a rotated secret takes effect from the next delivery. The value never appears in a log line, in audit metadata, in the delivery log, or in any response except the response to the `PUT` that generated it. Deleting a workspace already deletes its secrets.

### 2. Setting and removing the secret

`PUT /api/v1/workspaces/:slug/forge/webhook` checks, in this order:

1. No credential: 401.
2. A PAT without `forge:write`: 403, with a message naming `forge:write`.
3. `wsaccess.AuthorizeWorkspace`: 404 for a workspace that is missing or not the caller's.
4. A workspace that is not `active`: 400 `workspace is not active`.
5. The body.

The body is an optional JSON object `{"secret": string}`. An absent body, `{}` or `"secret": null` makes hub generate a secret: 32 bytes from `crypto/rand`, hex-encoded to 64 characters. A supplied secret must be 16 to 255 characters, each a printable ASCII character from `!` to `~` (no spaces or control characters, so it is safe in GitLab's header). Any other value answers 400 naming `secret` and the rule, without repeating the value.

Hub updates an existing `FORGE_WEBHOOK_SECRET`, or creates it. If a concurrent create causes a `ConflictError`, hub updates once. A full scope answers 400 with the store's message, and any other store failure answers 500.

The answer is 200 with `{"configured": true, "url": <ingress URL or null>, "secret": <generated value>}`. The `secret` key is present only when hub generated the value. The ingress URL is `server.external_url` with any trailing `/` removed, followed by `/hooks/forge/<slug>`. It is null when `external_url` is unset; the secret is stored all the same. Hub emits `hub.forge.webhook.set` with metadata `{generated: bool}`.

`DELETE /api/v1/workspaces/:slug/forge/webhook` uses the same order without the `active` check, so it works on archived workspaces too. It deletes the secret when one is present and answers 204 either way. It emits `hub.forge.webhook.unset` with metadata `{existed: bool}`. Deliveries answer 404 afterwards. Existing delivery-log rows stay until they are pruned.

Neither route reads `target`.

### 3. The `webhook` block of `GET /forge`

The response of `GET /api/v1/workspaces/:slug/forge`, defined in `30_forge_client_reads`, gains:

```jsonc
"webhook": { "configured": true, "url": "https://hub.example.com/hooks/forge/widgets", "last_delivery_at": "2026-10-21T09:12:03Z" }
```

- `configured` is true when the workspace-scope secret exists.
- `url` is the ingress URL, or null as in section 2.
- `last_delivery_at` is the latest `received_at` among this workspace's delivery-log rows, of any outcome. It is null when there are none.

The route still sends nothing to the forge. A failure in the secrets store or the delivery log answers 500.

### 4. The ingress endpoint

`POST /hooks/forge/:slug` is mounted on the Echo root, beside `/git/...`, outside the API group and outside apikit's bearer authentication. The forge proves who it is with the signature. The handler steps are, in order:

1. **Lookup.** Hub loads the workspace and its secret. An unknown slug and a workspace without the secret both answer 404 with the same body. A database or secrets failure answers 500. Workspace status does not matter: an archived workspace with a secret still relays.
2. **Choose the scheme by header.** With `X-Hub-Signature-256` present, the delivery is GitHub's. Otherwise, with `X-Gitlab-Token` present, it is GitLab's. With neither, the answer is 401, before the body is read.
3. **GitLab token.** Hub compares `X-Gitlab-Token` to the secret in constant time (`crypto/subtle`), before reading the body.
4. **Body.** Hub reads the body, capped at 1 MiB. apikit's `max_body_size` check runs before the handler. A body over either limit answers 413. The handler's own 413 carries `error_type: body_too_large`.
5. **GitHub signature.** The header must be `sha256=` followed by 64 hex digits. Hub compares it with HMAC-SHA256 of the raw body under the secret, using `hmac.Equal`.

A missing, malformed or wrong signature or token answers 401 with the same message in every case: `delivery signature missing or invalid`. Such a request writes no delivery-log row and emits no event. It is logged at warn level with the slug and the scheme, never with a header value or the body. Error answers use `apikit.WriteAPIError` / `WriteAPIErrorWithType`. The ingress never contacts the forge and takes no workspace lock.

### 5. Delivery identity and deduplication

After verification, hub reads the delivery id and the event name:

| Forge | Delivery id | Event name |
|---|---|---|
| GitHub | `X-GitHub-Delivery` | `X-GitHub-Event` |
| GitLab | `Idempotency-Key`, else `X-Gitlab-Event-UUID` | `X-Gitlab-Event` |

The answer is 400, with no row written and no event, in either of these cases:

- the id or the event name is missing;
- the id is longer than 255 characters or contains a character outside printable ASCII.

Hub stores the event name cut to 64 bytes.

Deliveries are recorded in a new SQLite table `forge_deliveries`. `forge.InitSchema(db)` creates it at every start; it is idempotent and leaves existing rows alone. Its columns:

| Column | Content |
|---|---|
| `workspace_slug` | |
| `delivery_id` | |
| `forge` | `github` or `gitlab` |
| `event` | |
| `action` | the payload's action, `""` when there is none |
| `target` | `origin`, `upstream` or NULL |
| `outcome` | |
| `relayed` | JSON array of the hub event types handed to the emitter |
| `received_at` | `apikit.NowUTC()` |

The primary key is `(workspace_slug, delivery_id)`, with an index on `(workspace_slug, received_at)`.

Hub first reduces the payload (section 6), so it knows the outcome. It then inserts the row with `ON CONFLICT DO NOTHING`:

- If no row was inserted, the delivery is a duplicate. Hub answers 202 `{"outcome":"duplicate"}` and emits nothing.
- If the insert fails, hub answers 500 and emits nothing, so a redelivery is processed in full.

### 6. Reduction to events

Hub parses the body as a JSON object. When it is not one, or a field the relayed kind needs is missing, the outcome is `malformed`. The row is written and the answer is 400.

A delivery whose kind and action are not in the tables below has outcome `ignored`. Examples: GitHub `ping`, `pull_request.labeled`, `issue_comment.edited`, GitLab `Confidential Issue Hook`, `Confidential Note Hook`, and notes on commits or snippets. It is answered 202 and emits nothing.

**Repository binding.** For a relayed kind, hub compares the payload's repository path with the workspace's repositories, ignoring case:

- GitHub: `repository.full_name`.
- GitLab: `project.path_with_namespace`.
- The workspace's repositories are the origin repository (owner path and name parsed from `git_url`) and, on a `carry_patch` workspace with an `upstream_url`, the upstream repository.

A match with origin sets `target: origin`. A match only with upstream sets `target: upstream`. No match, or a remote that cannot be parsed, gives outcome `repository_mismatch`: the answer is 202 and no event is emitted.

**GitHub mapping** (event header and action, then the hub event):

| Delivery | Hub event(s) |
|---|---|
| `issues` opened / edited / closed / reopened | `hub.forge.issue.opened` / `.edited` / `.closed` / `.reopened` |
| `issues` labeled / unlabeled | `hub.forge.issue.labeled` / `.unlabeled` |
| `issue_comment` created | `hub.forge.comment.created` (`pull_request: true` when `issue.pull_request` is present) |
| `pull_request` opened / reopened / synchronize | `hub.forge.pull_request.opened` / `.reopened` / `.synchronize` |
| `pull_request` closed | `.merged` when `pull_request.merged` is true, else `.closed` |
| `push` | `hub.forge.push` |

**GitLab mapping** (`X-Gitlab-Event` and `object_attributes.action`):

| Delivery | Hub event(s) |
|---|---|
| `Issue Hook` open / close / reopen | `hub.forge.issue.opened` / `.closed` / `.reopened` |
| `Issue Hook` update | `hub.forge.issue.edited` when `changes` has `title` or `description`. Then one `.unlabeled` per label in `changes.labels.previous` and not in `current`, then one `.labeled` per label in `current` and not in `previous`, each group sorted by name. No such change: outcome `ignored` |
| `Note Hook` on `Issue` / `MergeRequest` (action absent or `create`) | `hub.forge.comment.created`, with `pull_request: true` for a merge request |
| `Merge Request Hook` open / close / reopen / merge | `hub.forge.pull_request.opened` / `.closed` / `.reopened` / `.merged` |
| `Merge Request Hook` update with `oldrev` present | `hub.forge.pull_request.synchronize` (an update without `oldrev` is ignored) |
| `Push Hook`, `Tag Push Hook` | `hub.forge.push` |

**Event contents.** Every relayed event has:

- `Workspace` set to the slug.
- `ActorType` set to `forge` and `ActorID` empty.
- `ResourceType` set to `issue`, `comment`, `pull_request` or `repository` (for push).
- `ResourceID` set to the number in decimal, or the ref for a push.
- `Action` set to the last segment of the event type (`push` for a push).

Metadata always has `forge`, `target`, `delivery_id` and `actor` (the forge login, `""` when absent). By kind, it adds:

| Kind | Additional metadata |
|---|---|
| issue opened/edited/closed/reopened | `number`, `title`, `url`, `labels` (current label names) |
| issue labeled/unlabeled | `number`, `label`, `labels` (all current), `url` |
| comment created | `number`, `comment_url`, `pull_request` |
| pull request | `number`, `url`, `head`, `base`, `merged` |
| push | `ref`, `before`, `after` |

`title` is cut to 256 bytes on a UTF-8 boundary. URLs are taken from the payload with any userinfo removed. Hub emits nothing else from the payload. The payload is never stored and never logged. For GitLab, `number` is the `iid`. Each event type is a constant in `internal/audit/types.go`, with a doc comment in the style of the sandbox events.

### 7. Answer, then relay

Accepted deliveries are answered 202 with `{"outcome": "relayed" | "ignored" | "repository_mismatch" | "duplicate"}`. `relayed` means at least one event was handed to the emitter. Hub writes and flushes the answer before it emits.

Events are emitted in order through `forge.Config.Audit` with `context.WithoutCancel`. The broadcast emitter in `main.go` stores them in `hub_audit_events` and puts them on the SSE stream. An emit error is logged and changes nothing about the answer. A nil emitter skips emission.

### 8. The delivery log API

`GET /api/v1/workspaces/:slug/forge/deliveries?since=&limit=` uses the same order as section 2, with the scope `forge:read` (which `forge:write` implies) and no status check.

- `since` is an RFC 3339 timestamp and is inclusive.
- `limit` is an integer from 1 to 100 and defaults to 30.
- A bad value answers 400 naming the parameter.

The answer is a bare JSON array, newest first, `[]` when empty, of `{delivery_id, forge, event, action, target, outcome, relayed, received_at}`.

### 9. Retention

`forge.PruneDeliveries(ctx, db)` deletes rows whose `received_at` is more than 7 days old, compared with an `apikit.FormatUTC` cutoff, and rows whose `workspace_slug` no longer exists in `workspaces`. It returns the number of rows deleted.

`forge.StartDeliveryRetention(ctx, db)` runs one pass at once, then one every hour, and returns when `ctx` is cancelled. It logs a failed pass and tries again at the next tick. `cmd/af-hub/main.go` starts it in a goroutine after `forge.InitSchema`.

### 10. The CLI

```
afc forge webhook set <slug> [--secret s | --from-stdin | --generate]   PUT /forge/webhook
afc forge webhook unset <slug>                                          DELETE /forge/webhook
afc forge deliveries <slug> [--since t] [--limit n]                     GET /forge/deliveries
```

`set`:

- `--secret`, `--from-stdin` and `--generate` are mutually exclusive. Combining them fails with exit 2 before any request.
- With none of them, `set` behaves as `--generate`.
- `--from-stdin` reads the secret with `readValueFromStdin`, which drops one trailing newline.
- `--generate` sends `{}`.
- When the answer contains a generated secret, `set` also writes one line to stderr saying that hub will not show it again.

`unset` prints `{"status":"unset","workspace":<slug>}`.

`deliveries` sends only the flags that are set, as given, and hub validates them.

The commands go under the `afc forge` parent that `30_forge_client_reads` creates. They follow `internal/cli` conventions: `apikit.CLIClientFromCmd`, `apiPath`, `CLIPrintResult` and `CLIHandleError`. `internal/cli` does not import `internal/forge`.

## Design Decisions

1. **The delivery log is a SQLite table owned by `internal/forge`, keyed by `(workspace_slug, delivery_id)`.** `INSERT … ON CONFLICT DO NOTHING` gives atomic deduplication in the transactional store where workspaces and secrets live. DuckDB is the analytics store.
2. **A forge-owned pruning loop (one pass at start, then hourly) prunes the log. Audit's retention worker does not.** The input says "the retention worker". The table is in SQLite and belongs to forge, while the audit worker's steps all target DuckDB. A step there would make `internal/audit` depend on a forge table.
3. **Retention is a fixed 7 days, and pruning also removes rows of deleted workspaces.** The input fixes the period. Removing orphans keeps a re-created slug from showing an earlier owner's deliveries for longer than an hour.
4. **`FORGE_WEBHOOK_SECRET` is read from the workspace scope only.** An org-scoped value would make one secret valid for every workspace in the org.
5. **The scheme is chosen by header: `X-Hub-Signature-256` first, then `X-Gitlab-Token`. It is not tied to the workspace's classified forge.** Both schemes need the secret. Tying the scheme to the forge would put `issuex` classification and `FORGE_API_URL` on the unauthenticated path for no added security.
6. **The GitLab delivery id is `Idempotency-Key` when present, else `X-Gitlab-Event-UUID`.** `Idempotency-Key` is GitLab's header that stays the same across retries. `X-Gitlab-Event-UUID`, the header the input names, is the fallback for older GitLab.
7. **Deliveries are bound to the workspace's own repositories, and events carry `target`.** An org-level webhook or a reused secret would otherwise relay another repository's issue numbers as this workspace's, and a consumer would act on the wrong issue. `target` tells a carry-patch consumer which `?target=` to use.
8. **Unverified requests are never recorded.** An anonymous caller must not be able to write rows.
9. **A missing id or event header answers 400 with nothing recorded. A body that is not JSON is recorded as `malformed` and answered 400.** A forge always sends the headers, so their absence means a hand-made request. A recorded `malformed` row shows the operator what arrived.
10. **A delivery-log failure answers 500 with no event. An event-store failure still answers 202.** The 500 lets a redelivery be processed in full. The event-store rule is the input's: the emitter swallows insert errors, and a 5xx after recording would turn the retry into a duplicate.
11. **The answer is flushed before emitting.** A slow audit store then cannot hold the forge's connection open past its delivery timeout.
12. **A merged pull request emits `merged`, not `closed`.** GitHub's `closed` with `merged: true` and GitLab's `merge` mean the same thing, and the input lists `merged` as its own event.
13. **GitLab `update` emits `edited` only for title or description changes, and emits one `labeled` or `unlabeled` per changed label.** This matches what GitHub's `edited`, `labeled` and `unlabeled` mean, as the input asks for label changes.
14. **Comments on pull requests and merge requests are relayed as `comment.created` with `pull_request: true`.** GitHub sends them as `issue_comment`. The flag lets a consumer tell them apart without a second event type.
15. **Confidential GitLab issues and notes are ignored.** Their content should not reach the audit log or agents without a separate decision.
16. **The actor of a relayed event is `ActorType: "forge"` with an empty `ActorID`, and the forge login goes in `metadata.actor`.** `ActorID` holds hub identities everywhere else.
17. **Delivery events use the forge's past tense (`comment.created`). Write events from `forge_writes` use the imperative (`comment.create`).** The input defines both sets. The tense separates what the forge reported from what hub did.
18. **`PUT` generates a 64-hex secret when none is given, and validates a supplied one as 16 to 255 printable non-space ASCII characters.** GitLab sends the secret in a header, and a short secret is guessable.
19. **`PUT` needs an active workspace. `DELETE` works in any state and is idempotent (204).** This matches `forge_writes` for changes. Turning ingress off must always be possible.
20. **Both webhook changes emit audit events (`hub.forge.webhook.set` / `.unset`) without the value.** Every configuration change in hub is audited. These routes bypass the secrets handlers, so the secrets events would not fire.
21. **The ingress URL comes from `server.external_url` and is null when that is unset.** This follows `hub_url`. The secret is stored anyway, because an operator behind a proxy knows the public address.
22. **Archived workspaces still relay deliveries.** The secret is the only switch. Relaying is harmless, because writes on archived workspaces are refused by `forge_writes`.
23. **The handler caps the body at 1 MiB on top of `max_body_size`.** This is the input's limit, and it holds even when an operator raises the global limit. The 413 reuses `body_too_large`.
24. **The ingress answers with a small JSON outcome document.** Forges show the response body in their delivery logs, which helps an operator debug.
25. **The delivery log list uses `limit` 1 to 100 with a default of 30, newest first, as a bare array.** This matches the list conventions of `30_forge_client_reads`.
26. **`afc forge webhook set` adds `--from-stdin`.** A secret on the command line ends up in shell history. The flag name matches `afc secrets`.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `30_forge_client_reads` | depends on, modifies | Uses `internal/forge` (`Config`, `RegisterRoutes`, scope helpers, workspace remote lookup, repository parsing) and the `afc forge` parent command. Adds the `webhook` block to its `GET /forge` response. |
| `31_forge_writes` | depends on | Shares `forge.Config.Audit`, the `hub.forge.*` event namespace in `internal/audit/types.go`, and the `body_too_large` error type. Follows it in the split order. |

## Verified External API

| Symbol | Signature | Status |
|---|---|---|
| `(*apikit.Server).ExternalURL` | `func (s *Server) ExternalURL() string` | verified from the call site in `internal/workspace/routes.go` |
| `(*apikit.Server).Echo` | returns `*echo.Echo` | verified from `cmd/af-hub/main.go` |
| `apikit.WriteAPIError` / `WriteAPIErrorWithType` | `(c echo.Context, status int, msg string) error` / `(…, msg, errorType string) error` | verified from call sites |
| `apikit.NowUTC` | `func NowUTC() string` | verified from call sites (`internal/audit/store.go`) |
| `apikit.FormatUTC` | `func FormatUTC(t time.Time) string` | verified from call sites (`internal/carrypatch/purge_refs.go`) |
| `apikit.ParseUTC` | `func ParseUTC(s string) (time.Time, error)` | **unverified** (named in the steering; no call site in the repository) |
| `apikit.GetAuthInfo`, `apikit.AuthInfo` | `func GetAuthInfo(c echo.Context) *AuthInfo`; fields `UserID`, `CredentialType`, `Permissions` | verified from call sites |
| `audit.Emitter`, `audit.HubEvent` | `Emit(ctx, HubEvent) error`; fields as in `internal/audit/types.go` | verified |
| `secrets.Store` methods | `GetSecretValue(ownerType, ownerID, key string) (string, error)`, `CreateSecrets(ownerType, ownerID string, []EntryInput) ([]SecretEntry, error)`, `UpdateSecret(ownerType, ownerID, key, value string) (*SecretEntry, error)`, `DeleteSecret(ownerType, ownerID, key string) error` | verified (`internal/secrets`) |
| GitHub webhook protocol | headers `X-Hub-Signature-256: sha256=<hex HMAC-SHA256>`, `X-GitHub-Delivery`, `X-GitHub-Event`; payload fields `action`, `sender.login`, `repository.full_name`, `issue.{number,title,html_url,labels[].name,pull_request}`, `label.name`, `comment.html_url`, `pull_request.{number,html_url,head.ref,base.ref,merged}`, `ref`, `before`, `after` | external protocol, **unverified** from the repository |
| GitLab webhook protocol | headers `X-Gitlab-Token`, `X-Gitlab-Event`, `X-Gitlab-Event-UUID`, `Idempotency-Key`; payload fields `object_attributes.{action,iid,title,url,noteable_type,source_branch,target_branch,oldrev,state}`, `user.username`, `user_username`, `project.path_with_namespace`, `labels[].title`, `changes.{title,description,labels.previous,labels.current}`, `issue.iid`, `merge_request.iid`, `ref`, `before`, `after` | external protocol, **unverified** from the repository |
| `issuex` repository parsing | used only through the `internal/forge` helper from `30_forge_client_reads` | **unverified** (see that spec) |
