# Push a workspace branch to the forge

Status: **proposed** (2026-10-20). Nothing here is implemented. This is
item 6 of what the agent-fox service PRD asks of hub
([agent-fox `docs/prd/01`](https://github.com/agent-fox-dev/agent-fox/blob/main/docs/prd/01-triage-and-fix-forge-issues-as-a-service.md),
§9 and §4.2, the `standard` landing). Details left to settle at spec
time are marked *spec time*.

## Intent

A branch pushed to hub's git server lives in the workspace's trunk and
nowhere else until the workspace is archived, when hub pushes every local
branch to `origin`. For carry-patch workspaces that is by design, and
`PUSH_PATCHES_TO_ORIGIN` mirrors or forwards registered patch branches.
For a `standard` workspace there is no way to get a branch from hub to
the forge short of archiving, and a pull request on the forge needs the
branch there.

The agent-fox service lands a fix on a `standard` workspace as a branch
pushed to hub (the only remote it has a credential for, decision D4)
followed by a pull request through hub's forge API (hub PRD 02). Between
the two, the branch has to reach the forge, with the workspace's
credential, which only hub holds.

This PRD adds **one operation**: push a named branch of the workspace's
trunk to the workspace's `origin`, on request, with the stored
credential, serialized like every other git operation on the trunk. It
reuses the push the archive already performs, for one branch instead of
all.

## Goals

1. The owner, or a credential with `workspaces:write`, can push one branch
   of a `standard` workspace from hub to `origin` in one request, and
   learns the SHA that went and whether `origin` already had it.
2. The push uses the workspace's `GIT_PAT` or username and password,
   resolved as the archive's push resolves them, and the credential
   appears nowhere in the response, the audit event or the log.
3. A push never rewrites history on the forge unless asked: it is a
   fast-forward push by default and `force` is explicit, audited and
   refused for the workspace's own branch.
4. The operation respects the carry-patch rules: on a `carry_patch`
   workspace a registered patch branch is not pushed by this route
   (the push-control and mirroring rules own it), and the integration
   branch never is.
5. A person can do it from `afc`.

## Non-goals

- **Automatic mirroring of every branch.** A variable that forwards
  every push to hub on to `origin` is tempting and is not this PRD; it
  would make hub's git server a proxy for the forge and change what
  "pushed to hub" means. A later PRD may add `PUSH_BRANCHES_TO_ORIGIN`
  with a prefix list once the explicit operation has been used.
- **Pushing to `upstream`.** The upstream of a carry-patch workspace is
  never pushed to by hub.
- **Deleting a branch on the forge.** `afc` users have the forge.
- **Pull requests.** Hub PRD 02.

## Background: what exists

| Piece | Where | What it does |
|---|---|---|
| The archive's push | `workspace.defaultArchiveOpenAndPushFn(repoPath, gitURL, auth)` in `internal/workspace/clone.go` | opens the trunk with go-git and pushes `refs/heads/*` to `git_url` with the resolved credential; a failure is logged and the archive proceeds |
| Credential resolution | `workspace.ResolveCloneAuth(store, slug)` | `GIT_PAT`, else `GIT_USERNAME` and `GIT_PASSWORD`, else none |
| The workspace lock | `wslock` | every operation that touches the trunk is serialized; handlers answer 409 `workspace_busy` when it is held |
| Carry-patch push control | `internal/carrypatch/push_control.go`, `push_mirror.go` | the pre-receive and post-push hooks that reject, forward or mirror pushes to registered patch branches |
| Branch resolution | `internal/gitcmd` (`branch.go`, `refname.go`) | ref-name validation and local branch lookup |
| Audit | `audit.Emitter`, `hub.git.push` for pushes received | the event shape and the SSE stream |

## 1. The operation

```
POST /api/v1/workspaces/:slug/branches/:name/push     {force?: false}   → 200
```

```jsonc
{
  "branch": "fix/42-nil-map",
  "sha": "a3f9c1e…",
  "remote": "origin",
  "remote_url": "https://github.com/acme/widgets.git",   // userinfo removed
  "result": "pushed" | "up_to_date" | "forced"
}
```

- `:name` is a branch name (`refs/heads/<name>` in the trunk); a name
  that is not a valid ref or does not exist in the trunk is 404
  `branch_not_found`. The name is taken as given; `fix/42-nil-map` is
  one path segment with a slash and is escaped as the secrets routes
  escape keys.
- The push is `refs/heads/<name>:refs/heads/<name>`, fast-forward only.
  A non-fast-forward refusal from the forge is 409 `non_fast_forward`
  with the remote's SHA when the forge reports it. With `force: true` the
  push is `+refs/heads/<name>:…`, the result is `forced`, and the audit
  event says so.
- Refused before anything is sent: a `carry_patch` workspace's
  integration branch (400 `integration_branch`: it is machine-generated;
  `REBUILD_PUSH_INTEGRATION_BRANCH` is the way); a registered patch
  branch of a `carry_patch` workspace (400 `patch_branch`, naming
  `PUSH_PATCHES_TO_ORIGIN`); the workspace's own `branch` with `force`
  (400 `protected_branch`); a workspace that is not `active` or whose
  clone is not `ready` (400); no credential and a forge that refuses the
  anonymous push (502 `forge_unauthenticated`).
- **Authentication:** API key, admin token, or a PAT with
  `workspaces:write`; the owner (hub PRD 04: owner, not member). A bound
  PAT (hub PRD 03) for this slug qualifies, which is how the service
  holds it.
- **The lock:** taken for the push, released after; 409 `workspace_busy`
  when held, including during a rebuild's fetch or final phase, as for
  sync.
- **The push itself:** the archive's go-git push with one refspec, the
  credential from `ResolveCloneAuth`, a 2-minute deadline (*spec time*:
  configurable with the other git deadlines). The function is shared with
  the archive, which gains nothing and loses nothing.
- **Audit:** `hub.branch.push` with `branch`, `sha`, `result`, `force`,
  the actor, `workspace`; the remote URL with userinfo removed; never a
  credential. On the SSE stream like every hub event.

## 2. The CLI

```
afc workspace push <slug> <branch> [--force]
```

One JSON document, apikit's exit codes; `--force` prints a one-line
warning on stderr before sending.

## 3. Acceptance criteria

Offline, with a bare repository standing in for the forge over the file
transport go-git supports in tests:

1. A branch pushed to hub's git server and then `POST
   .../branches/<name>/push` appears on the forge at the same SHA; the
   response says `pushed`; a second call says `up_to_date` and sends no
   objects.
2. The forge advanced the branch: the call is 409 `non_fast_forward`;
   with `{force: true}` it is 200 `forced` and the audit event carries
   `force: true`.
3. On a `carry_patch` workspace the integration branch is 400
   `integration_branch`, a registered patch branch is 400 `patch_branch`,
   and an unregistered branch pushes as on a standard workspace.
4. The workspace's own `branch` with `force` is 400 `protected_branch`;
   without `force` it pushes fast-forward.
5. While the lock is held the call is 409 `workspace_busy`; a bound PAT
   with `workspaces:write` for the slug succeeds and the same token on
   another slug is 404; a PAT with `workspaces:read` only is 403.
6. The request the stand-in forge receives carries the workspace's
   credential; the response, the audit event and the captured log carry
   neither the token nor the URL's userinfo.
7. `afc workspace push widgets fix/42-nil-map` prints the response and
   exits 0; with a non-fast-forward it exits non-zero with the error
   type.

## 4. What is given up, stated plainly

- **One branch per request.** A service landing one fix pushes once;
  nothing needs more, and a batch is a loop.
- **No automatic forwarding** on standard workspaces. The service calls
  the route; a person uses `afc` or the forge.
- **Fast-forward only by default**, which refuses a legitimate rewrite
  of a work-in-progress branch until the caller says `force`. That is
  the right default for a credential a service holds.

## Documentation

| Change | Update |
|---|---|
| the route, the errors, the event | `docs/api.md`, `docs/openapi.yaml` |
| `afc workspace push` | `docs/cli.md` |
| the shared push function and the lock | `docs/architecture.md` |
| the carry-patch refusals | `docs/carry_patch_workflow.md` (one paragraph under "Where patch branches live") |
