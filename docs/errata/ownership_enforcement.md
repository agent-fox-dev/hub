# Errata: workspace ownership is enforced on all workspace-scoped endpoints

## Previous Behaviour

Specs 12, 13, 15, 16, 17 and 18 describe the sync, reclone, patch, merge,
rebuild, rerere, patch-status, session-creation and audit endpoints in terms
of permission scopes only. The implementations looked the workspace up by
slug without checking `owner_id`, so any authenticated caller holding the
right scope (every API key has all scopes implicitly) could sync, rebuild,
merge, read audit data of, or push patches into any workspace on the hub.
The unified audit query and the SSE stream returned every workspace's
events to every `audit:read` holder.

## Current Behaviour

All of these endpoints now resolve the workspace through
`workspace.AuthorizeWorkspace`, which applies the same rule as the CRUD
endpoints: owner or admin token, otherwise `404` (anti-enumeration). The
unified audit query and SSE stream are restricted to the caller's own
workspaces; hub events without a workspace are admin-only. `POST
/api/v1/sessions` answers `404` for a workspace the caller does not own.

Tests that previously expected `400` for an unknown workspace on patch
creation now expect `404`, and admin tokens are rejected with `403` on the
user-scoped secrets and variables endpoints because they have no user
identity. See `docs/permissions.md` and `docs/api.md`.

## Org Member Read Access (spec 35)

Spec 35 (`35_org_member_workspace_access`) widens the rule from "owner or
admin token" to three levels, decided in one place, `wsaccess.AuthorizeWorkspace`:
an admin token, the workspace's owner, and a member of the workspace's org
(a row in apikit's `org_members` for the workspace's `org_id`). Every REST
route that names a workspace states what it needs. Reads (the workspace, its
patch stack, rebuilds, rebuild-preview, patch-status, rerere, merges, leases,
forge reads and deliveries, variables and resolved variables) admit members.
Writes stay with the owner and admin tokens, and a member gets `403` with
`error_type: forbidden` (`org members have read-only access to this
workspace`). Workspace secrets and bound tokens are private: a member gets the
`404` a stranger gets. Everyone else still gets `404`, with the body an
unknown slug gets. `GET /api/v1/workspaces` lists the caller's own workspaces
and those of the caller's orgs, and every workspace object carries `access`
and `org_slug`.

Spec 35 left sessions, cost, per-run audit reads and ingestion, the unified
audit query, the SSE stream, transcripts and the git server with the owner-only
rule above. Existing ownership tests for workspaces in a personal org pass
unchanged, because a personal org's only member is normally its owner. See
`docs/errata/35_org_member_workspace_access_divergences.md` for where the
delivered behaviour differs from the spec.

Spec 36 (`36_org_member_audit_read`) moves the audit package onto the same
rule, keeping each route's refusal answer. A member of the workspace's org
reads the per-run audit records and transcripts (any workspace status;
`404 workspace not found` for everyone else), the sessions, usage and cost of
an active workspace (`403 workspace access denied` for everyone else), and
sees the workspace's events in `GET /api/v1/audit` and `GET /api/v1/events`;
hub events without a workspace stay with admin tokens. Opening a session is a
write (a member gets `403 forbidden`); completing a session, reporting usage
and ingesting audit data stay with the owner and admin tokens. Audit reads
were already restricted to owners before spec 36; notes saying they were not
enforced were stale. See `docs/errata/36_org_member_audit_read_divergences.md`.

Spec 37 (`37_org_member_git_read`) moves the git server onto the same rule:
`authorizeGitAccess` makes one call to `wsaccess.AuthorizeWorkspaceAt`, with
the read level for clone and fetch and the write level for push, and has no
ownership check of its own. A member of the workspace's org clones and fetches
the repository exactly as the owner does; a member's push is refused with a
pkt-line `403 org members have read-only access to this workspace` before
anything is written or audited. Everyone else gets the pkt-line `404
repository not found` they got before, and a workspace with no org is now a
404 rather than a 500. See `docs/errata/37_org_member_git_read_divergences.md`.
