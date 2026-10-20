# Let org members read org workspaces

Status: **proposed** (2026-10-20). Nothing here is implemented. This is
item 5 of what the agent-fox service PRD asks of hub
([agent-fox `docs/prd/01`](https://github.com/agent-fox-dev/agent-fox/blob/main/docs/prd/01-triage-and-fix-forge-issues-as-a-service.md),
§9, decision D2), and the prerequisite agent-fox's inbox draft named
first for any team use of hub. Details left to settle at spec time are
marked *spec time*.

## Intent

Hub has orgs, org membership and a workspace's `org_id`, and uses none
of them for access: every workspace-scoped endpoint, the git server and
the listing answer only to the workspace's owner, and to admin tokens.
`docs/permissions.md` says so under "Ownership enforcement", and the
anti-enumeration rule turns a colleague's request into a 404. A team
whose workspaces belong to one org therefore cannot see each other's
work on hub: not the patch stack, not a rebuild's result, not a running
session's cost, not the audit stream, and soon not the sandboxes, the
forge events and the jobs the agent-fox service will report there.

This PRD makes **membership of a workspace's org grant read access to
it**, and keeps every write with the owner. The listing shows what the
caller may read, with the access they have. Nothing changes for a
workspace in a personal org, whose only member is its owner.

## Goals

1. A member of the org a workspace belongs to can read it: the record,
   the patch list and status, rebuild and merge jobs, rerere, sessions and
   cost, audit and the SSE stream, variables, and clone and fetch from
   the git server, each under the scope it needs today.
2. Every write stays the owner's and the admin token's: update, archive,
   reactivate, delete, sync, reclone, patches, rebuilds, merges, rerere
   forget, secrets, variables, git push, and the sandbox and forge writes
   hub PRDs 01 and 02 add.
3. The rule lives in one place, `wsaccess.AuthorizeWorkspace`, with the
   access level a caller has, so every handler says what it needs and
   none decides for itself.
4. Anti-enumeration holds: a workspace the caller may not read is 404; a
   workspace the caller may read but not write answers 403 to a write,
   because the caller already knows it exists.
5. The listing is complete and honest: `GET /workspaces` returns the
   workspaces the caller may read with `access: owner | member`.

## Non-goals

- **Org roles.** apikit's orgs have members and no roles beyond admin.
  Member means read; a "maintainer" who may write is a later addition
  on the same seam (`access: maintainer`), once apikit has the role.
- **Transferring ownership**, or an org owning a workspace with no user
  as owner. `owner_id` stays required.
- **Changing what a scope means.** `workspaces:read` is still read; a
  member with a PAT needs the scope as the owner does.
- **Secrets.** Secret names are listable by the owner only, as today; a
  member sees no secret, not even its name.

## Background: what exists

| Piece | Where | What it does |
|---|---|---|
| The rule | `wsaccess.AuthorizeWorkspace(db, auth, slug) (*WorkspaceAccess, int, string)`; `workspace.lookupWorkspaceForAuth` | owner or admin, else 404; returns `Slug`, `OwnerID`, `Status`, `CloneStatus` |
| The git server's rule | `gitserver.authorizeGitAccess` | `UserID == OwnerID` or admin, else pkt-line 404; `git:read` and `git:write` scopes for PATs |
| Org membership | apikit's `orgs` and `org_members`; hub's `checkOrgMembership(db, userID, orgID)` in `internal/workspace` and `internal/secrets` | exists, used at workspace creation and update (`org_id` must be an org the owner belongs to) and for org-scoped secrets and variables |
| The listing | `handleListWorkspaces` | `WHERE owner_id = ?`, or everything for an admin |
| Sessions and audit | `internal/audit` | "non-admin callers see only sessions in workspaces they own"; the SSE stream and the unified query filter by owned workspaces |
| The documentation of the gap | `docs/permissions.md` ("Ownership enforcement", "Non-obvious behaviors"), `docs/errata/ownership_enforcement.md` | records that the rule is owner-only, and a stale note that sync and reclone do not enforce it (they do: both call `lookupWorkspaceForAuth`) |

## 1. The rule

```go
package wsaccess

type Access string

const (
	AccessNone   Access = ""        // not visible: 404
	AccessMember Access = "member"  // read
	AccessOwner  Access = "owner"   // read and write
	AccessAdmin  Access = "admin"   // an admin token: everything
)

type WorkspaceAccess struct {
	Slug, OwnerID, OrgID, Status, CloneStatus string
	Access Access
}

// AuthorizeWorkspace loads the workspace and decides the caller's access:
// admin tokens are AccessAdmin; the owner is AccessOwner; a member of the
// workspace's org is AccessMember; anyone else gets 404. need says what
// the endpoint requires; a caller with read access asking for write gets
// 403 with error_type "forbidden", not 404.
func AuthorizeWorkspace(db *sql.DB, auth *apikit.AuthInfo, slug string, need Access) (*WorkspaceAccess, int, string)
```

- Membership is one query joining `workspaces.org_id` to apikit's
  `org_members` for `auth.UserID`; a workspace with no `org_id` has no
  members. The personal org every user gets at first login has one
  member, so a personal workspace keeps today's answers exactly.
- A bound PAT (hub PRD 03) is checked first: the wrong slug is 404
  whatever the membership.
- Every caller of `AuthorizeWorkspace` and of `lookupWorkspaceForAuth`
  passes `need`. The spec lists each endpoint with its level; the table
  below is the rule.

| Endpoint family | Read (member) | Write (owner) |
|---|---|---|
| workspace get, list | yes | update, archive, reactivate, delete |
| sync, reclone | | yes |
| patches | list, get | add, update, remove, restore, reorder, reset |
| rebuilds, rebuild-preview, patch-status | list, get, preview, status | submit, cancel, requeue, rollback |
| merges, rebase | list, get | submit, cancel, requeue, rebase |
| rerere | list | forget |
| secrets (workspace scope) | | list, create, update, delete |
| variables (workspace scope) | list, get, resolved | create, update, delete |
| sessions, cost | list, get, usage, cost | open, complete, usage |
| audit, SSE, transcript | yes | ingestion |
| git server | upload-pack (clone, fetch) | receive-pack (push) |
| sandboxes (hub PRD 01) | list, get | create, destroy |
| forge (hub PRD 02) | repository, issues, pulls, forge status, deliveries | issues and pulls writes, labels, webhook |
| tokens (hub PRD 03) | | mint, list |

Scopes apply on top for PATs, as today: a member's PAT needs
`patches:read` to list patches.

## 2. The listing

`GET /workspaces` returns the union of the caller's own workspaces and
the workspaces of orgs the caller belongs to, each with `access`
(`owner`, `member`; `admin` for every row under an admin token) and the
org's slug, sorted as today. `include_archived` applies to both. A
member sees the same workspace record the owner sees except the fields
that name credentials, which none do; `hub_url` is shown, since the
member may clone.

## 3. Sessions, audit and the stream

The audit package's "workspaces the caller owns" becomes "workspaces the
caller may read", through `AuthorizeWorkspace` for a single workspace and
through one shared query (`wsaccess.ReadableSlugs(db, auth)`) for the
unified audit query and the SSE stream, which filter by a set of slugs.
Hub events without a workspace stay admin-only.

## 4. The documentation gap

`docs/permissions.md` still says sync and reclone do not enforce
ownership; both handlers call `lookupWorkspaceForAuth`
(`sync_handler.go:92`, `reclone_handler.go:47`), as `docs/api.md` and
`docs/errata/ownership_enforcement.md` say. The spec corrects the
paragraph when it rewrites the section for the new rule.

## 5. Acceptance criteria

1. A user U2 who is a member of org O, where U1 owns workspace W in O:
   `GET /workspaces/W` as U2 is 200 with `access: member`; `PATCH
   /workspaces/W` as U2 is 403 `forbidden`; `GET /workspaces/X` for a
   workspace X in an org U2 does not belong to is 404.
2. `GET /workspaces` as U2 lists W with `access: member` and U2's own
   with `access: owner`; as U1 it lists W with `access: owner`.
3. U2 clones and fetches `/git/<O>/W.git` and gets a pkt-line 403 on
   push (*spec time*: 403 versus the pkt-line 404 the server uses today
   for every refusal; 403 is right once the caller may read).
4. U2 lists W's patches, rebuilds, sessions and cost, and the unified
   audit query and the SSE stream as U2 include W's events; U2 cannot
   list W's secrets (404, as if none) and cannot submit a rebuild (403).
5. A personal workspace answers every existing ownership test unchanged:
   the owner is the only member of the personal org.
6. A bound PAT (hub PRD 03) of a member for W reads W and nothing else;
   a bound PAT for X held by U2 is 404 on X whatever O.
7. `docs/permissions.md`'s sync and reclone note is gone and the
   ownership section describes the three levels.

## 6. What is given up, stated plainly

- **Members see cost and audit.** What a colleague's agent spent and did
  on an org workspace is visible to the org. That is the point of a team
  inbox, and it is new.
- **Read is all a member gets.** A team that wants two owners waits for
  roles. `owner_id` stays one user.
- **One more query per request** for non-owners: the membership join.
  Owners short-circuit before it.

## Documentation

| Change | Update |
|---|---|
| the three levels, every endpoint's level, 403 versus 404 | `docs/permissions.md` (the ownership section rewritten), `docs/api.md` (Workspace Ownership, the listing's `access`), `docs/openapi.yaml` |
| the git server's read for members | `docs/api.md` (Git Server Endpoints) |
| `wsaccess.Access` and `ReadableSlugs` | `docs/architecture.md` |
| the stale sync and reclone note | `docs/permissions.md`, `docs/errata/ownership_enforcement.md` |
