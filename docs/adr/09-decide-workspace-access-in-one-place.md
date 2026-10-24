# ADR 09: Decide workspace access in one place

- **Status:** Accepted
- **Date:** 2026-10-24 (records decisions taken between 2026-08 and 2026-10)

## Context

The first specs described the workspace-scoped routes (sync, reclone, patches,
merges, rebuilds, rerere, patch status, sessions, audit) in terms of permission
scopes only. Their handlers looked the workspace up by slug without checking
`owner_id`, so any authenticated caller with the right scope, and every API key
implicitly, could act on any workspace, and the audit query and the SSE stream
returned every workspace's events to every reader. Later specs added org
members as readers, tokens bound to one workspace, and tokens minted for a
workspace. Each would have been another rule in another place.

## Decision

1. **One rule, `wsaccess.AuthorizeWorkspace`, decides every REST route that
   names a workspace, and the git server.** It knows three levels: an admin
   token, the workspace's owner, and a member of the workspace's org (a row in
   apikit's `org_members`). A route states what it needs: `read` admits all
   three; `write` admits the owner and admin tokens and answers a member `403`
   with `error_type: forbidden` (`org members have read-only access to this
   workspace`); `private` answers a member the `404` a stranger gets. Everyone
   else gets `404 workspace not found`, the body an unknown slug gets, so that
   slugs are not disclosed. A failed lookup is `500`, never a grant.

2. **What each route needs.** Reads (the workspace, its patch stack, rebuilds
   and previews, patch status, rerere, merges, leases, forge reads and
   deliveries, variables, sessions, usage, cost, per-run audit records,
   transcripts, the audit query and the event stream for that workspace) admit
   members. Writes stay with the owner and admin tokens: a member's write
   enqueues and changes nothing. Workspace secrets and bound tokens are
   private. Opening a session is a write; completing one, reporting usage and
   ingesting audit data stay with the owner and admin tokens; hub events
   without a workspace stay with admin tokens. The git server takes the read
   level for clone and fetch and the write level for push (a member's push is
   a pkt-line `403` before anything is written or audited; a stranger's is the
   pkt-line `404 repository not found`).

3. **A PAT without the scope a route needs is refused before the workspace is
   looked up**, with `403` and the scope named, except on the workspace
   handlers, which answer `404` (anti-enumeration) to a PAT lacking a
   read-implying scope. The scope check comes before the access check
   everywhere, so a refusal is one answer and never followed by data.

4. **A token bound to a workspace reaches that workspace and nothing else.**
   The binding guard runs on the root Echo instance before apikit
   authenticates and refuses every other slug with the same `404`; on its own
   slug the route's own checks decide, as for an unbound PAT. The token id is
   parsed with apikit's build-time prefix (`ak` in a plain build, `af` in a
   release), which is the form apikit accepts. Deleting a workspace revokes
   the tokens bound to it and deletes their bindings in the deletion's
   transaction (archiving changes nothing). Each refusal of a bound token is
   reported once, with the method and route where the refusal point has a
   request and with the status and surface where it has none, through a
   limiter that keeps an entry holding a count until the token's next refusal.

5. **Minting a bound token is the owner's, not a member's, and not an admin
   token's**, which has no user identity; an archived workspace refuses it.

6. **Audit ingestion refuses before it stores.** A refused request (unknown or
   archived workspace, a non-owner, a bound token on another workspace) writes
   its status and nothing else; before this rule it stored the event and
   appended a second body. The binding is checked right after the existence
   check, before archive and ownership. A session list for a caller with no
   readable workspace is empty, never "no restriction".

7. **Admin tokens go through the rule too**, so an admin naming a slug that
   names no workspace gets `404` rather than an empty answer or a session in
   a workspace that does not exist.

## Consequences

- Every refusal is one of three answers (`404` as for an unknown slug, `403
  forbidden` for a member's write, `403` with the scope for a PAT), and a
  change to the rule is one change. `docs/permissions.md` is the table of
  what each route needs.
- `DELETE /api/v1/workspaces/:slug` answers `204`; an earlier spec said `200`
  and the first implementation won.
- Members see every field the owner sees. `git_url` and `upstream_url` are
  validated as URLs and may carry userinfo, so a token written into the URL
  itself would reach members; redacting it is a change of its own.
- Cross-package tests of the rule live in each package, because the packages
  that mount the routes import `workspace` and a test in `workspace` cannot
  mount them.
