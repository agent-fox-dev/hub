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
