# ADR 04: Hub is the workspace's forge client

- **Status:** Accepted
- **Date:** 2026-10-20

## Context

A workspace in the hub is a repository on a forge, GitHub or GitLab. The hub already
holds the credential for it (`GIT_PAT`, or a username and password; `UPSTREAM_GIT_PAT`
and its siblings for the upstream of a carry-patch fork) and uses it for git: clone,
fetch and push. The agent-fox service needs more of the forge than git: the issues, the
comments and the pull requests of the workspace's repository. Under the service's
decisions it may hold no forge credential of its own, and the hub owns every forge
access.

Three places could make those calls:

1. **The caller**, with a forge credential of its own. Every caller would hold one for
   every repository it reads, and the hub could neither restrict nor audit what it reads.
2. **A separate service** in front of the forge, to which the hub hands the credential.
   Another process would hold the credential, and the hub's ownership rules would have to
   be repeated there.
3. **The hub itself**, which builds the forge client for the workspace from the
   credential it already resolves, behind scopes of its own.

The forge client is `issuex`, a package of the aflib module that classifies a forge from
a remote URL or an API URL and implements the repository, issue and pull request
operations for GitHub and GitLab.

## Decision

1. **The hub is the forge client of its workspaces.** A caller reads a workspace's
   repository, issues and pull requests through the hub (`/workspaces/:slug/...`), with
   the workspace's credential and never one of its own. The only thing the request
   decides is which remote to read: the workspace's `git_url` (`origin`, the default) or,
   on a carry-patch fork, its `upstream_url` (`upstream`).

2. **The credential is the one the hub already resolves.** The origin uses the resolver
   of the clone (`ResolveCloneAuth`) and the upstream the one of the carry-patch sync
   (`ResolveUpstreamAuthMethod`), so git and API access share one precedence chain. A
   username and password is sent as the token: the hub cannot know in advance whether a
   password is token-shaped, and the forge's own 401 reports the failure with a hint.

3. **A client is built for every request and never kept.** A rotated secret or a changed
   variable applies to the next call. The request's `Authorization` header and body never
   reach the forge. A workspace without a credential sends no token, whatever the hub's
   own environment holds.

4. **Two scopes, `forge:read` and `forge:write`,** are registered at every start;
   `forge:write` implies `forge:read`. The scope check comes before the workspace lookup
   (403, then 404), as in the sandbox and merge handlers. This change builds only the
   reads, so no route needs `forge:write` yet.

5. **Every forge failure has one documented answer** (`forge_not_found`,
   `forge_unauthenticated`, `forge_forbidden`, `forge_rate_limited`, `forge_rejected`,
   `forge_error`, `forge_unreachable`, `forge_timeout`, plus `forge_unknown` and
   `forge_api_url_invalid` for what the hub can tell before it sends anything). The
   forge's message is returned with the token replaced, the userinfo of URLs removed and
   the text cut to 1024 bytes; no log line holds the token or a body.

6. **The package that talks to the forge is `internal/forge`, and only it imports
   `issuex`.** `afc` imports neither, so it stays a static `CGO_ENABLED=0` binary. The
   hub takes aflib the way it takes apikit and agentkit-go: a sibling checkout replaced in
   `go.mod`.

7. **The HTTP client is one per process,** built from a clone of the default transport
   (so the environment's proxy settings apply, as for git), with the hub's user agent, a
   refusal of redirects to another host, port or scheme (GitLab's `PRIVATE-TOKEN` is not
   removed by `net/http` on a redirect) and a 30-second deadline for each call.

## Consequences

- A caller needs no forge access of its own; the credential stays in the hub, and the
  hub can say what a caller may read: a workspace it owns, with a PAT that holds the
  scope.
- The hub makes outbound calls to a forge, so a forge outage, a rate limit or a slow
  forge now shows up in the hub as 502, 429 or 504 answers. Each is logged once.
- `FORGE_API_URL` and `UPSTREAM_FORGE_API_URL` are the only configuration, and they are
  workspace variables. issuex classifies a host by the names `github` and `gitlab`, in
  the host or in an API URL, so a self-hosted forge whose names contain neither needs an
  aflib change to be reached; the hub does not guess the forge.
- The diagnostic (`GET /workspaces/:slug/forge`) is the way to see what the hub would
  use. It sends nothing to the forge. issuex has no accessor for the forge or the API URL
  of a client, so the hub states the default API URL of a host itself.
- Forge writes and the webhook ingress build on this: they reuse the client, the scopes,
  the target and the error table.

## Divergences from the specs

The forge specs listed every `issuex` signature as unverified. The code
follows `issuex` as it is and sends a request itself where `issuex` cannot do
what the API promises; the rules below are what it does.

**Reads.**

- `issuex.ParseRemote` reads only hosts whose name contains `github` or
  `gitlab`; the hub splits any other URL itself so that `GET /forge` can name
  the repository and a host that names no forge is `forge_unknown`. The API
  URL setting classifies a host only when it contains one of the two words.
  `issuex` has no accessor for a client's forge or API URL, so the hub uses
  `issuex.DetectForge` and states the default API URL of a host itself
  (`api.github.com`, `<host>/api/v3`, `<host>/api/v4`, with `/api/v4` appended
  for GitLab as `issuex` does).
- The list routes answer the issues and comments of `issuex`'s list structs
  as bare arrays (`[]`, never `null`); the `incomplete` and `truncated` flags
  of the lists are not exposed, the thread's `truncated` is.
- `Retry-After` is `issuex`'s wait, the forge's value plus a second; a wait of
  29 to 120 seconds meets the 30-second deadline first and is `504
  forge_timeout`, a longer one `429` at once. A 405 or 409 is `forge_rejected`;
  a `*url.Error` is `forge_unreachable` unless it is the deadline or the
  refused redirect; a body that cannot be read or decoded is `forge_error`.
- With no credential the transport removes `Authorization` and
  `PRIVATE-TOKEN`, so `issuex` never sends the hub's own `GITHUB_TOKEN`,
  `GH_TOKEN` or `GITLAB_TOKEN`. The redirect policy refuses another host, port
  or scheme. The error of an issue's comments is scrubbed like every other.
  The API URL setting is trimmed, and an empty one is invalid. An unparseable
  remote is shown cut to what follows its last `@`.

**Writes.**

- `issuex` cannot send an empty field, look a label up, return a created
  label, see the forge's 404 on a GitHub label removal, return the issue of a
  close or the URL of a pull request comment. The hub sends those requests
  itself (`write_raw.go`), through the shared HTTP client, with pointer fields
  so that a set-but-empty value is sent; GitLab's names are `description`,
  `state_event` and a comma-joined `labels`. A close is a comment (when not
  blank) and then the update to `closed`; a comment on a pull request is
  `AddComment` on the number. The read-back after a label removal is one GET
  of the issue, without its comments.
- GitLab assignees are validated and not sent (GitLab takes user ids); an
  update that sets nothing else is `400 nothing to update`. GitLab's body
  limit is 1,000,000 characters, the smaller of its limits.
- A write client is given the API root explicitly (the setting or the hub's
  statement for the host) and never reads the hub's environment; a host whose
  forge only the environment could tell is `422 forge_unknown` for a write.
  The forge is detected before the client is built, so an unknown forge is
  422 before 413.
- The hub's own requests are single attempts (no retry on 429, `Retry-After`
  passed through); `issuex`'s retry once. A write for a workspace with no
  credential is refused by `issuex` before any request and records no event;
  the hub's own requests go out without a token and the forge's 401 is
  `forge_unauthenticated` and recorded.
- The label name is read from the escaped path and decoded once; `.` and
  `..` are sent as `%2E` and `%2E%2E` so that a forge cannot resolve them as
  dot segments.
- Not handled: a label name with a comma on GitLab (split there), a numeric
  label name on GitLab (read as an id), a 301/302 of a write followed as a GET,
  a forge on a non-default port without an API URL setting, and URLs in the
  forge's success data, which are not scrubbed (only the ones the hub records
  in events are).

**The webhook ingress.**

- apikit's body limit is scoped to the API group, so the ingress enforces the
  1 MiB cap itself (`413 body_too_large`, by `Content-Length` when known, else
  a limited reader). A GitHub body is read before its signature is checked.
- `forge.Config.Secrets` is an interface the secrets store satisfies; a nil
  one is a misconfiguration answered 500. An empty stored secret is no
  secret (404 on the ingress, "not configured" in `GET /forge`). A signature
  or token header given twice is invalid, and a GitHub signature must be
  lowercase hex.
- Every delivery whose payload names the workspace's origin or upstream is
  bound to it with its `target`, relayed or not; one with events about another
  repository is `repository_mismatch`; one that would relay nothing is
  `ignored`. The fields a kind needs are its identity (repository path,
  number, label name, ref); the rest default to empty. A redelivered
  `malformed` delivery is a `202 duplicate`.
- The 202 carries `Content-Length` so that it is sent before the events; an
  answer that cannot be written is logged and the events relayed all the same
  (the row is `relayed` before the answer). The delivery list orders by
  `received_at` then `rowid`, and a `since` with a fraction starts at the next
  second. A delivery id is up to 255 printable ASCII characters.
- GitLab labels come from `labels` or `changes.labels.current`, a label
  listed twice is one event, `merged` is true for the `merge` action and the
  `merged` state, a note is relayed without an action or with `create`. An
  update relays at most 100 label events and an event lists at most 100
  labels (the unbounded form grew with the square of the labels).
- The webhook routes' events (`hub.forge.webhook.set`, `.unset`) have the
  caller as actor and the slug as resource; a scope-full store is told by
  its message. The retention interval is a variable (one hour) so a test can
  shorten it.
- Not handled: rate limiting of the ingress, form-encoded GitHub deliveries
  and legacy or other forges' headers (recorded `malformed`), secrets made
  with `afc secrets` outside the `PUT` rules, scheme-relative URLs in
  payloads, and the size of event strings other than `title`.
