# Errata: Spec 37 — Org Member Git Read Divergences

## Spec Expectation

Spec 37 (`37_org_member_git_read`) makes hub's git server decide every request through
`wsaccess.AuthorizeWorkspaceAt`, with the need taken from the git service: members of the
workspace's org clone and fetch, only the owner and admin tokens push, and everyone else gets
today's answer. The entries below record where the delivered code or its tests differ from what
the spec's text says, with the code and the test of what was delivered.

## Implementation Reality

1. **TS-35-56's git case changed in task 2, not task 7.** Task 7 owns 37-REQ-10.2 (supersede the
   git clause of TS-35-56), but the git server starts granting members their reads in task 2,
   which `TestMemberGitOwnerOnly_TS_35_56` (`internal/gitserver/org_member_unchanged_test.go`)
   asserted against: its gate would otherwise be red. The test changed there, as 37-REQ-10.2
   says: a member's upload-pack requests get the owner's answer and its receive-pack requests
   the read-only 403. Task 7 adds TS-37-35 for the same expectation.
