---
spec_id: "30"
spec_name: "forge_client_reads"
title: "Forge API for a workspace: the client, the scopes and the reads"
status: "active"
created_at: "2026-10-20T14:00:59.997637Z"
updated_at: "2026-10-20T14:00:59.997637Z"
intent_hash: "54b31938d84df8115730e38545b376df387539462529376530844bdae1be0809"
schema_version: 2
source: "docs/prd/02-serve-the-forge-for-a-workspace.md"
---
## Intent

Let a hub caller read a workspace's repository on its forge (GitHub or GitLab) through hub, using the credential hub already holds for that workspace and never one of the caller's own. Hub builds the forge client itself, from the workspace's own record: `git_url` or, on a carry-patch workspace, `upstream_url`, the stored git credential, and an optional API URL variable. It serves `issuex`'s read operations (repository, issues, comments, pull requests) under the workspace, behind two new scopes, and maps every forge failure to one documented hub error. This is the foundation that forge writes and the webhook ingress build on.

## Goals

1. A caller with an API key calls `GET /api/v1/workspaces/widgets/issues?labels=af:triage&state=open` and gets the forge's issues as `issuex` encodes them. The request the forge receives carries the workspace's `GIT_PAT` and the user agent `af-hub/<version>`, and never the caller's `Authorization` header.
2. On a `carry_patch` workspace, `?target=upstream` reads the repository at `upstream_url` with the credential the carry-patch sync already uses for upstream. On a `standard` workspace the same parameter answers 400 with `error_type: workspace_mode_mismatch`.
3. A PAT without `forge:read` or `forge:write` gets 403 on every forge route. A workspace the caller does not own gets 404 on every route. A workspace whose host `issuex` cannot classify, and that has no `FORGE_API_URL`, gets 422 `forge_unknown`, and no request leaves hub.
4. Each class of forge failure (not found, unauthenticated, forbidden, rate limited, rejected, server error, unreachable, timed out) answers the status and `error_type` in the error table below, with a test for each. No response body or log line contains the credential or any URL userinfo.
5. `GET /workspaces/:slug/forge` reports the forge, the API URL, the repository and the kind of credential (`pat`, `basic` or `none`) for origin, and for upstream on a carry-patch workspace, without sending any request to the forge.
6. `afc forge status`, `afc issue list|get` and `afc pr get|state|files|checks|reviews` each print one JSON document and use apikit's exit codes.
7. `make test` covers all of this offline, with `httptest` servers standing in for the forge.

## Non-goals

- **Forge writes.** Creating, updating and closing issues, comments, labels, creating pull requests and review comments, the forge body-size limits (413), and the `hub.forge.*` write audit events all belong to the `forge_writes` spec. So do `afc issue create|update|close|comment|label|unlabel`, `afc label create` and `afc pr create|comment`. This spec registers `forge:write` but no route uses it yet.
- **The webhook ingress.** `FORGE_WEBHOOK_SECRET`, `PUT`/`DELETE /forge/webhook`, `GET /forge/deliveries`, `POST /hooks/forge/:slug`, delivery verification and deduplication, the `forge_deliveries` table and its retention, the relayed events on the SSE stream, the `webhook` field of `GET /forge`, and `afc forge webhook|deliveries` all belong to the `forge_webhook_ingress` spec.
- **Merging or closing pull requests**, and any operation outside the `issuex.Client` read methods named below. There is no REST passthrough, no GraphQL and no repository administration.
- **Any repository other than the workspace's** `git_url` or, on a carry-patch workspace, its `upstream_url`.
- **Forgejo**, GitHub Apps, GitLab OAuth applications and any credential other than the workspace's stored git secrets.
- **Changing `issuex`.** Anything hub needs that `issuex` lacks is reported to aflib, not worked around in hub.
- **A per-workspace forge-kind override variable.** Classification is `issuex`'s, from the host or from `FORGE_API_URL` (see Design Decisions).
- Caching clients or responses, conditional GET (ETag), pagination cursors, and Prometheus metrics for forge calls.

## Background

The input is `docs/prd/02-serve-the-forge-for-a-workspace.md`. It is item 2 of what the agent-fox service asks of hub: under that service's decisions, hub owns every forge access and the service holds no forge credential. The input covers three areas that can be built and tested independently: reads, writes and webhook ingress. This spec is the first and foundational of three.

What exists today, read in the code:

- **Workspace record.** `internal/workspace/store.go` `Workspace` has `GitURL`, `WorkspaceMode` (`standard` | `carry_patch`), `UpstreamURL *string` and `Status` (`active` | `archived`). `validateGitURL` in `internal/workspace/validate.go` accepts `https://host/path` and `git@host:path` only.
- **Credentials.** `workspace.ResolveCloneAuth(store, slug)` in `internal/workspace/clone_auth.go` returns `*githttp.BasicAuth{Username: "x-token-auth", Password: <GIT_PAT>}`, or `{Username: GIT_USERNAME, Password: GIT_PASSWORD}`, or nil. `workspace.ResolveUpstreamAuthMethod(store, slug)` tries `UPSTREAM_GIT_PAT`, then `UPSTREAM_GIT_USERNAME` with `UPSTREAM_GIT_PASSWORD`, then falls back to `ResolveCloneAuth`. `cmd/af-hub/main.go` already passes both to other packages as closures `func(slug string) (transport.AuthMethod, error)`.
- **Variables.** `(*secrets.Store).GetVariableValue("workspace", slug, key)` returns `*secrets.NotFoundError` when the variable is not set. Hub reads every workspace variable this way, for example `CHECK_COMMAND` and `PATCH_BRANCH_SOURCE`.
- **Ownership and scopes.** `wsaccess.AuthorizeWorkspace(db, auth, slug)` gives 404 for a workspace that is missing or not the caller's. `internal/sandboxes/routes.go` (`authorize`) and `auth.go` (`canReadSandboxes`, `canWriteSandboxes`) show the pattern the steering prescribes: a PAT without the scope gets 403 before the workspace lookup, and a write scope implies its read scope. Scopes are `apikit.Permission{Resource, Action}` values appended to `extraPerms` in `main.go`.
- **Errors.** `apikit.WriteAPIErrorWithType(c, status, msg, errorType)` writes the envelope with `error_type`. `workspace_mode_mismatch` (400) already exists for rebuilds on a non-carry-patch workspace (`internal/carrypatch/api.go`). Userinfo stripping is repeated per package by convention (`internal/carrypatch/push_control.go` `sanitizeErrorText`, `internal/sandboxes/scrub.go`).
- **CLI.** `internal/cli` builds commands on `apikit.CLIClientFromCmd` and `client.DoRequest`, with `apiPath(...)` for escaped paths. `afc` is built `CGO_ENABLED=0` (`internal/cli/afc_binary_test.go`), so it must not import packages that pull in `internal/audit` (duckdb).
- **What is missing.** No package calls a forge API. `issuex` is not in the repository: aflib is not in `go.mod`, and there is no vendored copy. It comes from aflib PRD 01.

## Requirements

### 1. The aflib dependency

Hub requires the aflib module the same way it requires apikit and agentkit-go: a `require github.com/agent-fox-dev/aflib v0.0.0` and `replace github.com/agent-fox-dev/aflib => ../aflib` in `go.mod`. The hub Containerfile copies a build context named `aflib`. `make build-hub-container` passes `--build-context aflib=../aflib`. The workflows that build hub and afc clone aflib next to the repository. Only the new package `internal/forge` imports `issuex`. Neither `internal/cli` nor `cmd/afc` imports `internal/forge`.

### 2. Building the forge client

For every forge request, hub builds a fresh `issuex` client for the workspace and the target. It uses only what hub holds. The only input taken from the request is which target to use.

- **Remote URL.** `git_url` for `origin`, `upstream_url` for `upstream`.
- **Repository.** Parsed from the remote URL by `issuex`'s remote parser, so GitLab subgroup paths are kept. A URL from which no repository can be parsed answers 422 `forge_unknown`, naming the URL with its userinfo removed.
- **API base URL.** For `origin`, the workspace variable `FORGE_API_URL` when set. For `upstream`, `UPSTREAM_FORGE_API_URL` when set; it does not fall back to `FORGE_API_URL`. Without the variable, `issuex` detects the API from the remote host. A set value must be an absolute `https://` URL with a host, no userinfo, no query and no fragment. Any other value answers 422 `forge_api_url_invalid`, with a message that names the variable and the rule but not the value.
- **Classification.** When `issuex` cannot classify the forge (`ErrAmbiguousForge`), hub answers 422 `forge_unknown` before any request leaves hub. The message names the host and the variable to set (`FORGE_API_URL` or `UPSTREAM_FORGE_API_URL`).
- **Token.** Hub resolves the credential through the existing resolvers: `ResolveCloneAuth` for `origin` and `ResolveUpstreamAuthMethod` for `upstream`. They are injected as functions. The credential has one of three kinds:
  - `pat`: a basic-auth result whose username is `x-token-auth`.
  - `basic`: any other username. Hub sends the password as the token.
  - `none`: no credential. Hub builds an unauthenticated client.

  A failure in the secrets store answers 500 and is logged without any secret value.
- **HTTP client.** One client per process, shared by every forge call:
  - Its transport is a clone of `http.DefaultTransport`, so it uses the environment's proxy settings as hub's git transport does.
  - A round-tripper sets `User-Agent: af-hub/<apikit.Version>` on every request.
  - Its redirect policy refuses any redirect to a different host than the one the request was sent to.
  - Every call runs under a 30-second deadline derived from the request context. `issuex`'s own retries happen inside that deadline.
  - Tests can replace the base transport.
- **Lifetime.** Clients are never cached, so a rotated secret or a changed variable takes effect on the next call.

The incoming request's `Authorization` header and body never reach the forge. Forge calls take no workspace lock and do not need a clone, so `clone_status` does not matter.

### 3. Scopes and authorization

Hub registers two scopes, `forge:read` and `forge:write`, at every start. They are exposed as `forge.Permissions()` and appended to `extraPerms` in `cmd/af-hub/main.go`. `forge:write` implies `forge:read`. API keys and admin tokens hold both implicitly. A credential type hub does not know gets no access.

Every forge route checks in this order:

1. No credential: 401.
2. A PAT without the route's scope: 403, with a message naming the scope.
3. `wsaccess.AuthorizeWorkspace`: a workspace that is missing or not the caller's answers 404.
4. The target parameter (section 4).
5. Path and query parameters (section 6).
6. Building the client (section 2).

Every route in this spec needs `forge:read`. Read routes work on `active` and `archived` workspaces alike.

### 4. The target

Every route except `GET /forge` takes `?target=`, with the following rules:

- Absent or `origin`: the workspace's `git_url`.
- `upstream`: the workspace's `upstream_url`. This is allowed only when `workspace_mode` is `carry_patch` and `upstream_url` is non-empty. Otherwise hub answers 400 `workspace_mode_mismatch`.
- Any other value answers 400, with a message listing the allowed values.

### 5. `GET /workspaces/:slug/forge`

This route describes the forge without contacting it. It does not take `target`.

```jsonc
{
  "forge": "github",                       // github | gitlab | unknown
  "api_url": "https://api.github.com",     // null when forge is unknown
  "repository": { "host": "github.com", "owner": "acme", "name": "widgets" },  // null when unparseable
  "credential": "pat",                     // pat | basic | none
  "upstream": null                         // same four fields for upstream on a carry_patch workspace
}
```

`owner` carries the full namespace path, for example `group/subgroup` on GitLab.

- An unclassifiable host is reported as `"forge": "unknown"` with `api_url` null. On this route it is not an error.
- An invalid `FORGE_API_URL` (or `UPSTREAM_FORGE_API_URL` for the upstream block) answers 422 `forge_api_url_invalid`, as on every route.
- The response never contains a secret value, only the credential kind.

### 6. The read routes

Each route calls the `issuex.Client` method named here and returns its result encoded as `issuex`'s type, unchanged. A list is a JSON array, and an empty list is `[]`, not `null`.

| Route | `issuex` method |
|---|---|
| `GET /workspaces/:slug/forge/repository` | `GetRepository` |
| `GET /workspaces/:slug/issues` | `ListIssues` |
| `GET /workspaces/:slug/issues/:n` | `ReadIssue`: the issue with its comments, and `issuex`'s truncation flag |
| `GET /workspaces/:slug/issues/:n/comments` | `ListComments` |
| `GET /workspaces/:slug/pulls/:n` | `ReadPullRequest` |
| `GET /workspaces/:slug/pulls/:n/state` | `GetPRState` |
| `GET /workspaces/:slug/pulls/:n/files` | `ReadChangedFiles` |
| `GET /workspaces/:slug/pulls/:n/checks` | `GetCIChecks` |
| `GET /workspaces/:slug/pulls/:n/reviews` | `GetPRReviews` |

Hub validates parameters before it builds a client, and any violation answers 400 naming the parameter:

- `:n` must be a positive decimal integer. On GitLab it is the project `iid`, as `issuex` has it.
- On `GET /issues`, `state` must be `open`, `closed` or `all`. `labels` is a comma-separated list, and empty items are dropped. `assignee` is free text. `sort` must be `created` or `updated`. `direction` must be `asc` or `desc`. `limit` must be an integer from 1 to 100 and defaults to 30.
- A parameter that is absent is not passed to `issuex`, which then uses its own default. The exception is `limit`, which is always passed.

Reads emit no audit event.

### 7. Error mapping

Every forge route maps `issuex` and transport failures in one place:

| Condition | Status | `error_type` |
|---|---|---|
| `target=upstream` on a non-carry-patch workspace | 400 | `workspace_mode_mismatch` |
| invalid `target`, number or query parameter | 400 | — |
| `FORGE_API_URL` / `UPSTREAM_FORGE_API_URL` invalid | 422 | `forge_api_url_invalid` |
| forge not classifiable, or repository not parseable | 422 | `forge_unknown` |
| forge answered 404 | 404 | `forge_not_found` |
| forge answered 401 | 502 | `forge_unauthenticated` |
| forge answered 403 (not a rate limit) | 502 | `forge_forbidden` |
| rate limit `issuex` could not wait out | 429 | `forge_rate_limited`, with `Retry-After` in whole seconds (rounded up) when the forge gave one |
| forge answered another 4xx | 422 | `forge_rejected` |
| forge answered 5xx, a redirect to another host, or an unreadable response | 502 | `forge_error` |
| connection failure (DNS, TCP, TLS) | 502 | `forge_unreachable` |
| the 30-second deadline passed | 504 | `forge_timeout` |
| secrets or variables store failure | 500 | — |

The forge's own message goes in `message`, after three steps: the resolved token is replaced with `[redacted]`, URL userinfo is removed, and the text is cut to 1024 bytes on a UTF-8 boundary. A 401 for a `basic` credential adds a note that the forge API needs a token: either `GIT_PAT`, or a token stored as `GIT_PASSWORD`. Each 5xx and 429 answer is logged with the workspace, the target, the operation and the forge status. The log line never includes the token, a request body or a response body.

### 8. The CLI

Hub adds three parent commands to `BuildRootCommand`: `forge`, `issue` and `pr`. Each subcommand prints the response as one JSON document through `apikit.CLIPrintResult`, returns errors through `apikit.CLIHandleError`, and builds paths with `apiPath`.

```
afc forge status <slug>                                                       GET /forge
afc issue list <slug> [--state s] [--label l]... [--assignee a] [--limit n] [--target t]
afc issue get <slug> <n> [--target t]                                         GET /issues/:n
afc pr get|state|files|checks|reviews <slug> <n> [--target t]                 GET /pulls/:n[/...]
```

The CLI sends flag values as given, and hub validates them. Repeated `--label` flags are joined with commas. A flag that is not set is left out of the query.

## Design Decisions

1. **The input is split three ways, and the upstream target is folded into this foundation.** Reads, writes and webhook ingress can each be built and tested alone. `?target=upstream` is one parameter of client construction, and the credential resolver it needs already exists, so it does not need a spec of its own.
2. **New package `internal/forge`, with its dependencies injected through a config struct.** It needs the DB, the two credential resolvers, `GetVariable`, an optional base transport and an optional client factory. This follows `merge.MergeAPIConfig`, `carrypatch.SyncAPIConfig` and the closures already in `main.go`, and it keeps the package testable without `internal/workspace`.
3. **The credential comes from the existing resolvers, not a second lookup.** This keeps one precedence chain for git and API access, including the partial-credential warning. The `pat`/`basic` kind is read from the `x-token-auth` username that `resolveCloneAuth` sets.
4. **For a username/password pair, the password is sent as the token; there is no `credential_unsupported` code.** Hub cannot know in advance whether a password is token-shaped. The forge's 401 already reports the failure (`forge_unauthenticated`), and hub adds a hint.
5. **Upstream uses the full `ResolveUpstreamAuthMethod` chain.** That is `UPSTREAM_GIT_PAT`, then the upstream username and password, then origin's credential. It is the same credential sync uses, which is what the input means by "the upstream credential hub already resolves for sync".
6. **`FORGE_API_URL` is read from the workspace scope only, must be `https`, and has an upstream sibling `UPSTREAM_FORGE_API_URL` with no fallback.** Workspace variables are read with `GetVariableValue("workspace", …)` everywhere in hub. A token must not travel over plain HTTP. An upstream on a different host from the fork must not inherit the fork's API URL.
7. **No forge-kind override variable.** The input names `FORGE_API_URL` as the override for hosts whose names say neither forge. Classifying is `issuex`'s job, and hub does not reimplement detection.
8. **`GET /forge` sends nothing and reports `unknown` instead of failing.** It is the diagnostic a person runs when the other routes answer `forge_unknown`.
9. **Reads work on archived workspaces, take no workspace lock and ignore `clone_status`.** They do not touch the trunk, and sandbox lease reads already work on archived workspaces.
10. **`forge:write` implies `forge:read`, both are registered now, and the scope check comes before ownership (403, then 404).** This matches `sandboxes:read`/`sandboxes:write` and the merge and sandbox handlers. Registering both now means PATs can be issued once.
11. **`target=upstream` on a standard workspace reuses `workspace_mode_mismatch`.** The error type and its 400 status already mean exactly that in `internal/carrypatch/api.go`.
12. **The error table adds `forge_rejected` (422), `forge_error` (502), `forge_timeout` (504) and `forge_api_url_invalid` (422) to the input's codes.** Every failure class needs exactly one documented answer. A deadline is a gateway timeout, not an unreachable host.
13. **The HTTP client follows the environment's proxy settings, which departs from the input.** Hub's go-git transport already uses them for clone, fetch and push to the same host. Ignoring them would leave the API unreachable while git works.
14. **Cross-host redirects are refused.** GitLab's `PRIVATE-TOKEN` header is not one that `net/http` strips on a redirect, so following one could send the token to another host.
15. **The user agent is set by a round-tripper, not an `issuex` option.** It works whether or not `issuex` has a user-agent option, and it wins over any default `issuex` sets.
16. **Responses are `issuex` types encoded unchanged, and lists are bare arrays.** A client of hub and a user of the package then read the same shape, as the input asks.
17. **Hub validates list parameters, and `limit` is 1 to 100 with a default of 30.** A bad value then fails as a 400 in hub, not as a forge error. 100 is GitHub's and GitLab's page maximum.
18. **aflib is a sibling `replace` like apikit and agentkit-go.** This is how the repository takes every first-party Go dependency, and the Containerfile, the Makefile and CI already have the mechanism.
19. **Hub's architectural decision to act as the workspace's forge client is recorded as ADR `docs/adr/04-hub-is-the-workspaces-forge-client.md`.** The input asks for it, and this spec is where the client is introduced.

## Verified External API

| Symbol | Signature | Status |
|---|---|---|
| `github.com/agent-fox-dev/aflib/issuex` | module and package path | **NOT FOUND** in the repository. aflib is not in `go.mod` and there is no vendored copy. The path is assumed from the input. |
| `issuex.NewWithOptions` | `func NewWithOptions(opts issuex.Options) (issuex.Client, error)` | unverified (from the input) |
| `issuex.Options` | fields `BaseURL string`, `Token string`, `RemoteURL string`, `Repo <repo type>`, `HTTPClient *http.Client` | unverified (from the input). A `UserAgent` field may exist; hub does not depend on it. |
| `issuex.ParseRemote` | `func ParseRemote(remote string) (<repo type>, error)`, keeping GitLab subgroups | unverified |
| `issuex.ErrAmbiguousForge` | sentinel error returned when the host cannot be classified, before any request | unverified (named in the input) |
| forge kind and resolved API URL of a built client | needed by `GET /forge` | unverified; the accessor's name is not known |
| `issuex.Client` read methods | `GetRepository`, `ListIssues` (with state, labels, assignee, sort, direction, limit), `ReadIssue`, `ListComments`, `ReadPullRequest`, `GetPRState`, `ReadChangedFiles`, `GetCIChecks`, `GetPRReviews`; each takes a `context.Context` and, where relevant, the issue or PR number | unverified (names from the input) |
| `issuex` result types | `Repository`, `Issue`, `IssueThread`, `Comment`, `PullRequest` and the PR state, file, check and review types, JSON-encodable | unverified |
| `issuex` HTTP error | exposes the forge status code, the forge's message and, for rate limits, the retry-after duration | unverified; the type's name is not known |
| `apikit.GetAuthInfo` | `func GetAuthInfo(c echo.Context) *apikit.AuthInfo` | verified from call sites (`internal/sandboxes/routes.go`) |
| `apikit.AuthInfo` | fields `UserID`, `CredentialType`, `Permissions []string`, `KeyID` | verified from call sites |
| `apikit.WriteAPIError` / `WriteAPIErrorWithType` | `(c echo.Context, status int, msg string) error` / `(c echo.Context, status int, msg, errorType string) error` | verified from call sites |
| `apikit.Permission` | `struct{ Resource, Action string }` | verified (`internal/sandboxes/permissions.go`) |
| `apikit.Version` | package-level `string` var set by `-X` ldflags | verified from `Makefile` and `containers/hub/Containerfile` |
| `apikit.CLIClientFromCmd`, `(client).DoRequest(ctx, method, path string, body any)`, `apikit.CLIHandleError`, `apikit.CLIPrintResult`, `apikit.NewCLIError(code int, msg string)` | as used in `internal/cli/sandbox_cmd.go` and `secrets_cmd.go` | verified from call sites |
| go-git `githttp.BasicAuth` | `struct{ Username, Password string }`, returned as `transport.AuthMethod` by the resolvers | verified (`internal/workspace/clone_auth.go`) |
