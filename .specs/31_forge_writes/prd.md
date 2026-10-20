---
spec_id: "31"
spec_name: "forge_writes"
title: "Forge writes: issues, comments, labels and pull requests through hub"
status: "active"
created_at: "2026-10-20T14:09:33.958545Z"
updated_at: "2026-10-20T14:09:33.958545Z"
intent_hash: "f387dd2c3496b185d6e96d7e01409d82c27f6da30541fbfb3b2eaa4fa18da6d9"
schema_version: 2
source: "docs/prd/02-serve-the-forge-for-a-workspace.md"
---
## Intent

Let a hub caller change a workspace's repository on its forge through hub: create, update and close issues, comment, add and remove labels, ensure a repository label exists, open pull requests and comment on them. Hub does this with the credential it already holds for the workspace, under the `forge:write` scope, and only on active workspaces. Hub refuses a body the forge would refuse before sending it. Every write it sends is recorded as one `hub.forge.*` audit event that names the actor, the target and the outcome, and never carries a credential. A person gets the same writes through `afc`.

## Goals

1. A caller with an API key sends `POST /api/v1/workspaces/widgets/issues {"title":"t","body":"b","labels":["af:triage"]}` and gets 201 with the forge's issue. The forge receives the workspace's `GIT_PAT` and never the caller's bearer. Exactly one `hub.forge.issue.create` event is recorded with `workspace: widgets`, `target: origin`, the new `number`, the issue `url` and `ok: true`.
2. On both a GitHub and a GitLab stand-in forge:
   - `PATCH /issues/42 {"body":…}` rewrites the issue body.
   - `POST /issues/42/labels ["af:ready"]` and `DELETE /issues/42/labels/af:fix` each answer 204.
   - Each of these calls records exactly one event.
3. `POST /labels {"name":"af:triage"}` answers 201 the first time and 200 the second. The forge sees exactly one label-create request across both calls.
4. `POST /pulls` opens a pull request:
   - On a `standard` workspace it opens it against `git_url`.
   - On a `carry_patch` workspace, `?target=upstream` opens it against `upstream_url` with the upstream credential.
   - On a `standard` workspace, `?target=upstream` answers 400 `workspace_mode_mismatch`.
5. Each of the following answers on every write route without sending a forge request or recording an event:
   - a PAT holding only `forge:read`: 403
   - a workspace the caller does not own: 404
   - an archived workspace: 400
6. An issue body of 65,537 characters on a GitHub workspace answers 413 with `error_type: body_too_large`, the message names the limit, and no forge request is sent.
7. A forge failure on a write answers the error defined by `forge_client_reads` and records an event with `ok: false` and that `error_type`. No event metadata, response body or log line contains the credential or URL userinfo.
8. Each of `afc issue create|update|close|comment|label|unlabel`, `afc label create` and `afc pr create|comment` prints one JSON document and uses apikit's exit codes. `--body-file -` reads the body from stdin.
9. `make test` covers all of the above offline, with `httptest` servers standing in for the forge.
10. `docs/api.md`, `docs/openapi.yaml`, `docs/cli.md`, `docs/permissions.md` and `docs/architecture.md` describe the write routes, the new error type, the events and the commands.

## Non-goals

- **Everything `forge_client_reads` delivers.** That spec owns:
  - the forge client and its construction (credential, `FORGE_API_URL`, user agent, transport, timeout)
  - `?target` resolution
  - the scopes and their registration
  - the forge error mapping and the message scrubbing
  - all read routes, and `afc forge status`, `afc issue list|get` and `afc pr get|state|files|checks|reviews`

  This spec uses all of it and does not restate it.
- **The webhook ingress** (`forge_webhook_ingress`). That spec covers `FORGE_WEBHOOK_SECRET`, `PUT`/`DELETE /forge/webhook`, `GET /forge/deliveries`, `POST /hooks/forge/:slug`, the delivery log and the relayed `hub.forge.issue.labeled`-style events.
- **Merging, closing or updating pull requests**, approving reviews, and line-level review comments.
- **Editing or deleting comments**, and updating or deleting repository labels. `POST /labels` never changes a label that exists.
- **Idempotency for issue or pull-request creation.** Hub keeps no idempotency key and does not check for duplicates. A retried create can create twice.
- **Cross-project merge requests on GitLab.** These are fork-to-upstream MRs created on the source project. `head` is passed to `issuex` as given.
- **Length checks on titles, label names or label descriptions.** The forge rejects these, and its rejection goes through the existing error mapping.
- **Hub-side retries, metrics for forge calls, and Forgejo.** Forgejo arrives as an `issuex` backend later.
- **Changing `issuex`.** Anything hub needs that `issuex` lacks is a finding for aflib.

## Background

The source PRD is `docs/prd/02-serve-the-forge-for-a-workspace.md`, §3 (the write routes, errors and audit) and §5 (the CLI). It was split into three specs, and this is the second.

**What `forge_client_reads` provides** (spec directory `.specs/30_forge_client_reads/`; its package is being generated, and `internal/forge` does not exist in the tree yet):

- the package `internal/forge`, with a `Config` and `RegisterRoutes(api *echo.Group, cfg Config)`, wired in `cmd/af-hub/main.go`
- the per-request `issuex.Client` built from the workspace's `git_url` or `upstream_url`, its stored git credential and `FORGE_API_URL`
- `?target=origin|upstream`, with `upstream` on a `standard` workspace answering 400 `workspace_mode_mismatch`
- the scopes `forge:read` and `forge:write`, and standalone auth helpers on `*apikit.AuthInfo`
- the single mapping from `issuex` failures to hub statuses and `error_type`s, including `forge_unknown` (422), `forge_not_found`, `forge_unauthenticated`, `forge_forbidden`, 429 with `Retry-After`, and the unreachable and timed-out classes, with messages scrubbed
- the `afc issue`, `afc pr` and `afc forge` parent commands

What does not exist: any forge write, any `hub.forge.*` event, the `body_too_large` error type, and the `afc label` command.

**Audit.** Audit events are built from `internal/audit`:

- `audit.Emitter` has one method, `Emit(ctx, HubEvent) error`.
- `audit.HubEvent` carries `EventType`, `ActorID`, `ActorType`, `ResourceType`, `ResourceID`, `Action`, `Workspace` and `Metadata`.
- `cmd/af-hub/main.go` builds a single emitter with `audit.NewEmitterWithBroadcast(auditStore, sseMgr)`. Every event therefore lands in `hub_audit_events`, which `GET /api/v1/audit` queries, and reaches the SSE stream filtered by workspace.
- Event type constants live in `internal/audit/types.go`, each with a doc comment, for example `EventSandboxLease`.
- Handlers emit with the caller's `auth.UserID` and `auth.CredentialType` (`internal/sandboxes/lease_service.go`). They use `context.WithoutCancel` and log an emit failure without changing the response (`internal/sandboxes/events.go`).
- The retention worker already prunes aged hub events (`RetentionStep3_DeleteAgedHubEvents`).

**Inactive workspaces.** Per-workspace writes that need an active workspace answer `400 workspace is not active` after authorization. This is the convention in `internal/sandboxes/handlers_issue.go`, `internal/merge/api.go` and `internal/carrypatch/api.go`.

**Body size.** apikit's global `server.max_body_size` (default `1MB`, `docs/configuration.md`) caps every request body before a handler runs.

**CLI conventions** (`internal/cli`):

- Required flags and mutually exclusive flags are checked in `RunE` and answered with `apikit.NewCLIError(2, …)`.
- A 204 answer is printed as a synthetic status document, as `newSandboxDestroyCmd` does.
- `apiPath` percent-encodes every path segment.

## Requirements

### 1. Who may write, and on which workspaces

The write routes, all under `/api/v1`:

| Route | Body | Success |
|---|---|---|
| `POST /workspaces/:slug/issues` | `{title, body?, labels?, assignees?}` | 201, the issue |
| `PATCH /workspaces/:slug/issues/:n` | `{title?, body?, state?, labels?, assignees?}` | 200, the issue |
| `POST /workspaces/:slug/issues/:n/close` | `{comment?}` or no body | 200, the issue |
| `POST /workspaces/:slug/issues/:n/comments` | `{body}` | 201, `{"url": …}` |
| `POST /workspaces/:slug/issues/:n/labels` | `["name", …]` | 204 |
| `DELETE /workspaces/:slug/issues/:n/labels/:name` | none | 204 |
| `POST /workspaces/:slug/labels` | `{name, color?, description?}` | 201, the label; 200 when it already exists |
| `POST /workspaces/:slug/pulls` | `{title, body?, head, base, draft?}` | 201, the pull request |
| `POST /workspaces/:slug/pulls/:n/comments` | `{body}` | 201, `{"url": …}` |

Every write route accepts `?target=origin|upstream`, with the default, meaning and validation `forge_client_reads` gives it. Issues, pull requests and labels in responses are `issuex`'s types encoded as they are, the same shapes the read routes return.

Hub applies these checks in order and answers with the first that fails:

1. No credential: 401.
2. A PAT that does not hold `forge:write`: 403 `PAT requires forge:write scope`. `forge:read` alone is not enough. API keys and admin tokens pass. A credential type hub does not know is refused with 403.
3. A workspace that does not exist, or that the caller does not own: 404 `workspace not found`, through `wsaccess.AuthorizeWorkspace`.
4. A workspace whose status is not `active`: 400 `workspace is not active`.
5. The request itself is invalid: 400. This covers a malformed path number, a bad `target` (including `workspace_mode_mismatch`), malformed JSON and the field rules below.
6. The client cannot be resolved, as `forge_client_reads` defines it: for example 422 `forge_unknown`, or a credential that cannot be resolved.
7. A text field over the forge's limit: 413 (requirement 7).

A request refused at any of these steps sends no forge request and records no audit event. Writes ignore `clone_status` and take no workspace lock.

### 2. Create and update issues

`POST /issues`:

- `title` is required and must be non-empty after trimming whitespace.
- `body` is optional and may be empty.
- `labels` and `assignees` are optional arrays of at most 100 strings, each non-empty after trimming.
- A violation answers 400 with a message naming the field.
- Unknown JSON fields are ignored.
- Success answers 201 with the created issue.

`PATCH /issues/:n` changes only the fields present:

- A field absent or `null` is left unchanged.
- `title`, when present, must be non-empty.
- `body: ""` clears the body.
- `state` must be `open` or `closed`.
- `labels` replaces the issue's whole label set, and `[]` removes every label. `assignees` works the same way.
- A body with no field to change answers 400 `nothing to update`.
- Success answers 200 with the issue as the forge returns it.
- An issue number on GitLab is the project `iid`, as in the reads.

### 3. Close an issue, and comment on one

`POST /issues/:n/close` accepts an empty body or `{"comment": "…"}`. When `comment` is present and non-empty after trimming:

1. Hub first posts the comment on the issue.
2. Hub then closes the issue.

If posting the comment fails, hub does not close the issue and answers that failure's mapped error. If the close fails after the comment was posted, hub answers the close's mapped error, and the audit event records the comment's URL. Closing an issue that is already closed answers 200. Success answers 200 with the issue as the forge reports it after the close.

`POST /issues/:n/comments` requires `body`, non-empty after trimming, and answers 201 with `{"url": "<the comment's URL>"}`.

### 4. Add and remove labels on an issue

`POST /issues/:n/labels`:

- The body is a JSON array of 1 to 100 label names, each non-empty after trimming. An object, an empty array or a non-string entry answers 400.
- The labels are added and the issue's other labels are kept.
- When a name does not exist in the repository, the forge decides what happens, and hub passes its answer through.
- Success answers 204.

`DELETE /issues/:n/labels/:name`:

- The label name is the last path segment, percent-decoded exactly once. `type%2Fbug` removes `type/bug`, and `af:fix` passes through unchanged.
- The route answers 204 when the issue no longer carries the label, including when it never did. If the forge answers "not found" to the removal, hub reads the issue once. If the issue exists, the answer is 204. Otherwise it is the 404 `forge_not_found`.

### 5. Ensure a repository label exists

`POST /labels`:

- `name` is required and must be non-empty after trimming.
- `color` is optional: six hex digits, with or without a leading `#`. Anything else answers 400. Absent, it defaults to `ededed`.
- `description` is optional.

Hub first looks the label up by name on the forge:

- **Found:** hub answers 200 with the label as the forge holds it. It sends no create request and never changes the existing label's color or description.
- **Not found:** hub creates the label and answers 201 with it.
- **The create is refused** (a concurrent create of the same name): hub looks the label up once more. If it is now found, hub answers 200 with it. Otherwise hub answers the create's mapped error.

### 6. Open a pull request, and comment on one

`POST /pulls`:

- `title`, `head` and `base` are required, each non-empty after trimming.
- `body` is optional.
- `draft` is a boolean and defaults to `false`.
- `head` is passed to the forge as given, so GitHub's `owner:branch` form opens a pull request from a fork.
- Success answers 201 with the pull request.
- A forge that refuses, for example because a pull request already exists for `head` and `base`, answers through the existing error mapping.

With `?target=upstream` on a `carry_patch` workspace, the pull request is opened on `upstream_url` with the upstream credential.

`POST /pulls/:n/comments` requires `body`, non-empty after trimming. It posts a comment on the pull request's conversation through `issuex`'s `PostReviewComment` and answers 201 with `{"url": …}`.

### 7. Body-size limits

The limit applies to these text fields:

- an issue's `body` (create and update)
- the close `comment`
- a comment `body`
- a pull request's `body`
- a pull-request comment `body`

Each field's length is counted in Unicode code points. The limit depends on the forge of the resolved target:

| Forge | Limit |
|---|---|
| GitHub | 65,536 |
| GitLab | 1,000,000 |

A field over the limit answers 413 with `error_type: body_too_large`. The message names the field, its length, the forge and the limit, for example `body is 70000 characters; github accepts at most 65536`. Hub sends nothing to the forge and records no event.

The check runs after the client is resolved, because the limit depends on the forge. A request body larger than `server.max_body_size` is refused earlier by apikit, with its own 413.

### 8. Sending a write to the forge

Each write uses the per-request client from `forge_client_reads`, with its credential, base URL, user agent, transport and per-call timeout. Every forge failure answers the status and `error_type` that spec's mapping defines, with the message scrubbed. The mapping includes 429 with `Retry-After` passed through.

- **Retries:** hub never retries a write itself. Only `issuex`'s own retry behaviour applies, inside the per-call timeout.
- **Caller disconnects:** a forge call is not cancelled when the caller disconnects. It runs on the request context with cancellation detached, bounded by the per-call timeout, so a write that has left hub is always recorded.
- **Uncertain outcome:** a create that times out or cannot reach the forge may still have happened. Hub reports the mapped error and does not check.
- **Multi-request operations:** close-with-comment, the label lookup and create, and the label-removal read-back send several requests. Each request gets its own per-call timeout.

### 9. One audit event per write

Every write request that sent at least one request to the forge records exactly one event, whether it succeeded or failed. The event is emitted synchronously, before the response is written, on a context detached from the request's cancellation:

- with no emitter configured, nothing is recorded
- an emit failure is logged and does not change the response

Every event carries:

- `ActorID` set to the caller's `UserID`
- `ActorType` set to its `CredentialType`
- `Workspace` set to the slug

| Event type | resource_type | resource_id | action | Extra metadata |
|---|---|---|---|---|
| `hub.forge.issue.create` | `issue` | the new number (empty on failure) | `create` | none |
| `hub.forge.issue.update` | `issue` | `n` | `update` | `fields`: the names of the fields changed, sorted |
| `hub.forge.issue.close` | `issue` | `n` | `close` | `comment_url` when a comment was posted |
| `hub.forge.comment.create` | `comment` | `n` | `create` | none |
| `hub.forge.label.add` | `label` | `n` | `add` | `labels`: the names given |
| `hub.forge.label.remove` | `label` | `n` | `remove` | `label` |
| `hub.forge.label.create` | `label` | the label name | `create` | `label`, `created` (false when it already existed) |
| `hub.forge.pull_request.create` | `pull_request` | the new number (empty on failure) | `create` | `head`, `base`, `draft` |
| `hub.forge.pull_request.comment` | `pull_request` | `n` | `comment` | none |

Metadata on every event:

- `target` (`origin` or `upstream`)
- `ok` (bool)
- `error_type`, only when `ok` is false: the `error_type` hub answered with
- `number`, as a JSON number, when known
- `url`, when the forge returned one: the issue, pull-request or comment URL, with any userinfo removed

Metadata never holds a credential, URL userinfo, a title, a body or comment text, or a message from the forge. The nine event types are constants in `internal/audit/types.go`, each with a doc comment. With the emitter `main.go` already builds, the events reach `GET /api/v1/audit` and the SSE stream.

### 10. The CLI

New subcommands on the `afc issue` and `afc pr` parents from `forge_client_reads`, and a new `afc label` parent:

```
afc issue create <slug> --title t [--body b | --body-file f] [--label l]... [--assignee a]... [--target t]
afc issue update <slug> <n> [--title t] [--body b | --body-file f] [--state open|closed] [--label l]... [--assignee a]... [--target t]
afc issue close <slug> <n> [--comment c] [--target t]
afc issue comment <slug> <n> (--body b | --body-file f) [--target t]
afc issue label <slug> <n> <label>... [--target t]
afc issue unlabel <slug> <n> <label> [--target t]
afc label create <slug> <name> [--color hex] [--description d] [--target t]
afc pr create <slug> --title t --head h --base b [--body b | --body-file f] [--draft] [--target t]
afc pr comment <slug> <n> (--body b | --body-file f) [--target t]
```

**Body input:**

- `--body-file -` reads stdin, and any other value names a file.
- The content is sent verbatim, with no trimming.
- `--body` and `--body-file` together are a usage error.
- A body file that cannot be read is a usage error.

**Usage errors:**

- These are a missing required flag (`--title`, `--head`, `--base`, or a comment body), an issue number that is not a positive integer, and `afc issue update` with nothing to change.
- They exit with code 2 and send no request, following the existing `--x flag is required` convention.

**Flag mapping:**

- `--label` and `--assignee` are repeatable string-array flags.
- On `update` they replace the issue's labels or assignees, and the help text says so.
- A flag the user did not set is not sent, and `--body ""` on `update` clears the body.
- `--target` is sent as `?target=` only when the user sets it.

**Output:**

- Each command prints one JSON document on stdout: the hub's response body.
- `afc issue label` (204) prints `{"status":"labeled","number":n,"labels":[…]}`.
- `afc issue unlabel` (204) prints `{"status":"unlabeled","number":n,"label":"…"}`.
- Hub errors go through `apikit.CLIHandleError`, with apikit's exit codes.

## Design Decisions

1. **Writes require the literal `forge:write`.** `forge:read` grants reads only, as in the source PRD. API keys and admin tokens are not scope-checked, matching `canWriteSandboxes`.
2. **An inactive workspace answers 400 `workspace is not active`, after ownership.** This is the convention of `sandboxes`, `merge` and `carrypatch` for per-workspace writes. The audit package's 409 `workspace_archived` belongs to ingestion and is not followed here.
3. **Writes need neither a ready clone nor the workspace lock.** The forge API never touches the local clone.
4. **An event is recorded only when a request reached the forge.** Every rejection hub makes on its own (401, 403, 404, 400, 413, 422 and credential errors) records nothing. "Forge write" in the source PRD means a write sent to the forge; a rejection hub makes alone changed nothing anywhere and is already visible in access logs.
5. **One event per HTTP request, even when the request sends several forge requests.** Close-with-comment records `hub.forge.issue.close` with `comment_url`, not a separate comment event, which keeps "one write, one event" true.
6. **Event metadata holds names, numbers and URLs, never free text.** No title, body, comment or forge message is stored. This keeps credentials and content out of the audit store by construction, rather than by scrubbing.
7. **Events are emitted before the response is written,** so a client holding the response can already find the event in the audit query or the stream.
8. **Write events use present-tense names (`hub.forge.comment.create`); the relayed webhook events of `forge_webhook_ingress` use past tense (`hub.forge.comment.created`).** Both come from the source PRD. A write through hub on a workspace with a webhook produces both: one says who asked hub, the other what the forge reports.
9. **`POST /labels` on an existing label answers 200 with the label as the forge holds it, and never modifies it.** Whether color or description differ does not matter. The source's "200 when it exists unchanged" is read as "hub leaves it unchanged". A 409 would break an idempotent bootstrap of the agent-fox label protocol, and updating labels is out of scope.
10. **The idempotent label create looks the label up first, then creates.** A second call sends no create request, as the source's acceptance criterion requires. A refused create is followed by one more lookup, which covers a concurrent create without hub having to recognise each forge's "already exists" error.
11. **The default label color is `ededed`.** GitLab requires a color on create. `ededed` is GitHub's default.
12. **Removing a label the issue does not carry answers 204 on both forges.** GitHub answers 404 for this and GitLab ignores it. Hub normalises with one read-back of the issue, done only on the 404 path, so a caller gets one behaviour from both forges.
13. **`POST /issues/:n/labels` takes a bare JSON array,** exactly as the source PRD and its acceptance criterion write it. The agent-fox service is written against that shape.
14. **`PATCH` treats absent or `null` as unchanged and `[]` as clear.** Label and assignee arrays replace the whole set, which is what both forges' issue-update APIs do. `body: ""` is a legitimate way to clear a body.
15. **Close posts the comment first, then closes.** If the comment fails, nothing has changed. This is the order `gh issue close --comment` uses. A close that fails after the comment leaves the comment in place and records its URL in the event.
16. **Close answers 200 with the issue** (the source PRD gave no response). This is consistent with `PATCH`, and the CLI can print it.
17. **Comment creation answers `{"url": …}`, as the source PRD specifies.**
18. **Body limits count Unicode code points.** GitHub's limit is 65,536, from the source PRD. GitLab's is 1,000,000, the smaller of GitLab's note and description limits, so one value is never above either. Only body-like fields are checked. Title and label-name limits are left to the forge's own rejection.
19. **The 413 check runs after client resolution,** because the limit depends on the forge kind. An unknown forge therefore answers 422 even for an oversized body.
20. **Hub adds no retries, and does not let a caller's disconnect cancel a forge write in flight.** Retrying a non-idempotent create can duplicate it. A write that left hub must still be recorded.
21. **`head` is passed through verbatim.** Cross-fork pull requests work on GitHub with `owner:branch`. Cross-project merge requests on GitLab need source-project semantics that `issuex` is not known to have, so they are a non-goal.
22. **The CLI sends body files verbatim.** A body is a document, not a single secret value, so the trailing-newline trim of `readValueFromStdin` is not applied. A file and stdin with the same content send the same body.
23. **The CLI's `--label` and `--assignee` use string-array flags,** like `afc sandbox create --label`, so a comma inside a name is not split.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `30_forge_client_reads` | depends on | Provides the following, which this spec extends and does not restate:<br>• `internal/forge`, `forge.Config` and `forge.RegisterRoutes`<br>• the per-request `issuex` client and `?target` resolution<br>• the `forge:write` scope and the auth helpers<br>• the forge error mapping and the scrubber<br>• the aflib module requirement<br>• the `afc issue` and `afc pr` parents |

## Verified External API

**aflib `issuex`.** All of it is **unverified**. aflib is not in `go.mod` until `forge_client_reads` adds it, and `../aflib` is outside the repository. No vendored copy exists, and no call site exists yet. The input names `issuex.Client`, `NewWithOptions(Options{…})`, `ParseRemote`, `ErrAmbiguousForge`, `ReadIssue` and `PostReviewComment`, and the types `Issue`, `IssueThread` and `PullRequest`. This spec additionally assumes `issuex.Client` offers the following, under whatever names `issuex` uses:

| Capability hub needs | Assumed shape (unverified) |
|---|---|
| create an issue | `CreateIssue(ctx, title, body string, labels, assignees []string) (*Issue, error)` or an input struct |
| update an issue (title, body, state, labels, assignees) | `UpdateIssue(ctx, number int, update) (*Issue, error)` |
| close an issue | `CloseIssue(ctx, number int) (*Issue, error)`, or an update with state `closed` |
| comment on an issue | `PostComment(ctx, number int, body string) (url string, error)` |
| add labels / remove one label | `AddLabels(ctx, number int, names []string) error`; `RemoveLabel(ctx, number int, name string) error` |
| look a repository label up by name | `GetLabel(ctx, name string) (*Label, error)` returning a not-found error, or `ListLabels` |
| create a repository label | `CreateLabel(ctx, name, color, description string) (*Label, error)` |
| read an issue (removal read-back) | `ReadIssue(ctx, number int)`, named in the input and used by the reads |
| open a pull request | `CreatePullRequest(ctx, title, body, head, base string, draft bool) (*PullRequest, error)` |
| comment on a pull request | `PostReviewComment(ctx, number int, body string)`, named in the input, assumed to return the comment's URL |

The implementation pins the real signatures in a compile-time test, as `internal/sandboxes/imports_test.go` does for `gatewayd`. If the label lookup does not exist, see the open questions.

**apikit** (`../apikit`, outside the repository). These signatures are verified from call sites in this repository:
- `apikit.GetAuthInfo(c) *apikit.AuthInfo`
- `apikit.WriteAPIError(c, status int, msg string) error`
- `apikit.WriteAPIErrorWithType(c, status int, msg, errorType string) error` (`internal/sandboxes/handlers_issue.go`)
- `apikit.CLIClientFromCmd(cmd)`
- `(*CLIClient).DoRequest(ctx, method, path string, body any) (any, error)`
- `apikit.CLIPrintResult(cmd, v)`
- `apikit.CLIHandleError(cmd, err)`
- `apikit.NewCLIError(code int, msg string)` (`internal/cli/sandbox_cmd.go`, `merge_cmd.go`)
