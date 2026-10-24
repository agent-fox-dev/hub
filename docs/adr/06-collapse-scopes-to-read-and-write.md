# ADR 06: Collapse permission scopes to read and write

- **Status:** Accepted
- **Date:** 2026-10-24

## Context

The hub registered 29 permission scopes for eleven resources. Eight resources
had a `read` and a `write` scope. Three did not: `workspaces` had `read`,
`create`, `write`, `delete` and `sync`; `secrets` had `list`, `write`, `delete`
and `manage`; `vars` had `read`, `write`, `delete` and `manage`. The naming
differed between the two (`secrets:list` against `vars:read`), the
implications differed (`workspaces:write` implied `workspaces:read` on the
workspace routes but not on the rerere and patch-status routes, which tested
the literal string; `workspaces:delete` and `workspaces:sync` implied nothing;
`manage` implied the other three), and three documents had to explain the
exceptions. Every handler carried its own OR-list of the scopes it accepted.

## Decision

1. **One `read` and one `write` scope per resource.** The hub registers 22
   scopes: `audit`, `forge`, `git`, `merges`, `patches`, `rebuilds`,
   `sandboxes`, `secrets`, `sessions`, `vars` and `workspaces`, each with
   `read` and `write`. apikit's own eight scopes are apikit's and unchanged.

2. **`write` implies `read`, everywhere.** A PAT with `resource:write` passes
   every read check of that resource; `resource:read` never passes a write
   check. `wsaccess.CanRead` and `wsaccess.CanWrite` state the rule; a package
   that keeps a helper of its own for its message checks both scopes of the
   resource. The literal-string checks are gone.

3. **Stored tokens are migrated once.** `wsaccess.MigrateScopes` runs at hub
   start, before any request, and rewrites the permissions of every PAT that
   names an old scope: `workspaces:create`, `workspaces:delete` and
   `workspaces:sync` become `workspaces:write`; `secrets:list` becomes
   `secrets:read`; `secrets:manage`, `secrets:delete`, `vars:manage` and
   `vars:delete` become `*:write`. Duplicates the rewrite produces are dropped;
   the order of the remaining scopes is kept. The old names are not registered
   any more, so a new PAT that asks for one is refused with 400 like any
   unknown scope.

4. **The rewrite widens three tokens' reach, knowingly.** A token that held
   `workspaces:delete` or `workspaces:sync` alone could not list workspaces;
   one that held `secrets:delete` alone could not list secret names. After the
   rewrite they hold the `write` scope of the resource and can. The reverse,
   a token losing an ability it had, does not happen.

## Consequences

- The scope tables in `docs/permissions.md`, `docs/api.md` and
  `docs/openapi.yaml` shrink to one row per scope and one implication rule.
- A client that minted tokens with the old names must use the new ones; its
  existing tokens keep working after the hub restarts once.
- Later work on the secrets and variables handlers (one handler per operation
  across the three owner scopes) builds on two scope checks instead of eight.
