# 07: Cascade Deletion Limited to Workspace Scope

**Spec:** 07_secrets_variables
**Requirements:** 07-REQ-17.1, 07-REQ-17.2

## Divergence

Requirement 07-REQ-17.1 states that when a user, org, or workspace is deleted,
all associated secrets and variables must be deleted within the same database
transaction. This is fully implemented for **workspace deletion** (the
`deleteWorkspace` function in `internal/workspace/store.go` wraps workspace,
secrets, and variables deletion in a single transaction).

However, **user deletion** and **org deletion** are handled by the apikit
library (`github.com/txsvc/apikit`), which does not provide deletion hooks:

- **Org deletion:** apikit's `deleteOrg` handler (`DELETE /orgs/:id`) performs
  a direct `DELETE FROM orgs WHERE id = ?` with no transaction wrapping and no
  hook mechanism for the hub to add cascade cleanup. The only cascade is the
  database-level `ON DELETE CASCADE` on `org_members`.

- **User deletion:** No user deletion endpoint exists in apikit. If one is
  added in the future, a deletion hook would be needed to cascade secrets and
  variables.

## Impact

If an org is deleted via the admin API, its secrets and variables become
orphaned rows in the database. These orphaned rows are harmless (they cannot
be accessed via any API endpoint because the org no longer exists), but they
waste storage.

## Mitigation

The `secrets.Store` provides `DeleteUserCascade` and `DeleteOrgCascade`
methods that correctly wrap parent and child deletions in a single transaction.
These methods are tested (TS-07-39, TS-07-40) but cannot be wired into
production until apikit provides deletion hooks.

Possible future solutions:
1. Add `OnBeforeOrgDelete` / `OnBeforeUserDelete` hooks to apikit (preferred)
2. Override apikit's org deletion handler in the hub
3. Run a periodic cleanup job to remove orphaned secrets/variables rows
