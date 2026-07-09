# Errata: Spec 06 team rename divergences from codebase

## HTTP method for archive/reactivate endpoints

**Spec says:** PUT /api/v1/teams/:id/archive (06-REQ-3.4) and
PUT /api/v1/teams/:id/reactivate (06-REQ-3.5).

**Codebase uses:** POST for both archive and reactivate endpoints.

**Resolution:** Tests use POST to match existing behavior. Changing from POST
to PUT would be a behavioral change that violates the zero-behavioral-change
guarantee (06-REQ-7). The implementation should keep POST.

## GET single team endpoint does not exist

**Spec says:** GET /api/v1/teams/:id (06-REQ-3.3) with a GetTeam handler
method (06-REQ-2.4).

**Codebase has:** No GET /api/v1/workspaces/:id endpoint or GetWorkspace
handler method exists. This is new functionality, not a rename.

**Resolution:** Test is written for spec compliance. Implementing this endpoint
would add new behavior unless it is accepted as an intentional addition
alongside the rename.

## DELETE member endpoint does not exist

**Spec says:** DELETE /api/v1/teams/:id/members/:user_id (06-REQ-3.8) with
a RemoveTeamMember handler method.

**Codebase has:** No DELETE /workspaces/:id/members/:user_id endpoint or
RemoveWorkspaceMember/RemoveMember handler method. This is new functionality.

**Resolution:** Test is written for spec compliance. Implementing this endpoint
would add new behavior.

## HTTP status codes diverge from spec

**Spec says:** DELETE /api/v1/teams/:id returns 204 (06-REQ-3.6). POST
/api/v1/teams/:id/members returns 201 (06-REQ-3.7).

**Codebase returns:** DELETE returns 200 with JSON body `{"message":
"workspace deleted"}`. POST members returns 200 with the upserted membership
object.

**Resolution:** Tests use 200 to match existing behavior. The handler name is
AddOrUpdateMember (upsert semantics), not AddTeamMember.

## Store methods not listed in spec

**Spec lists (06-REQ-2.2):** 8 store methods to rename.

**Codebase has:** 13 Workspace-prefixed methods on the Store interface,
including GetWorkspaceBySlug, UpdateWorkspace, DeleteWorkspaceWithCascade,
CreateWorkspaceMember, GetWorkspaceMember, ListWorkspaceMembers,
UpsertWorkspaceMember, and CountAPIKeysByWorkspaceID.

**Resolution:** All 13 methods must be renamed, not just the 8 in the spec.
Tests from group 2 cover all 13.

## ArchiveTeam/ReactivateTeam store methods do not exist

**Spec says:** ArchiveTeam and ReactivateTeam are store methods (06-REQ-2.2).

**Codebase has:** No ArchiveWorkspace or ReactivateWorkspace store methods.
The handler calls GetWorkspaceByID, mutates the status field, then calls
UpdateWorkspace.

**Resolution:** The rename creates UpdateTeam (from UpdateWorkspace), not
separate ArchiveTeam/ReactivateTeam store methods.
