---
spec_id: "38"
spec_name: "workspace_branch_push"
title: "Push a workspace branch to the forge"
status: "active"
created_at: "2026-10-21T17:58:09.636624Z"
updated_at: "2026-10-21T17:58:09.636624Z"
intent_hash: "b43bccb908b1558dedd7e5387697c973c142e10ac813f63d3609e3f13b61b4e5"
schema_version: 2
source: "docs/prd/05-push-a-workspace-branch-to-the-forge.md"
---
## Intent

Let the owner of a workspace, or a credential acting for the owner, push one named branch from the workspace's trunk on hub to the workspace's `origin` when they ask for it. The push uses the workspace's stored git credential, is serialized with the other operations on the trunk, never rewrites history on the forge unless the caller explicitly asks, and leaves alone the branches that carry-patch rules own. This is how a branch pushed to hub's git server gets to the forge, so that a pull request can be opened against it, without archiving the workspace.

## Goals

1. `POST /api/v1/workspaces/:slug/branches/:name/push` sends `refs/heads/<name>` of the trunk to `refs/heads/<name>` on `origin`. It answers 200 with `branch`, `sha`, `remote`, `remote_url` (with userinfo removed) and a `result` of `pushed`, `up_to_date` or `forced`. Calling it again for an unchanged branch answers `up_to_date` and sends no objects.
2. By default the push is fast-forward only. When the forge's branch has commits the trunk's branch does not contain, the answer is 409 `non_fast_forward`. `{"force": true}` overwrites the forge's branch. Using `force` on the workspace's own branch is refused with 400 `protected_branch`.
3. On a `carry_patch` workspace, the integration branch is refused (400 `integration_branch`), and so is a registered patch branch (400 `patch_branch`). In both cases nothing is sent to the forge. An unregistered branch pushes as it would on a standard workspace.
4. The push uses the workspace's stored git credential, resolved the way the archive's push resolves it: `GIT_PAT`, otherwise `GIT_USERNAME` and `GIT_PASSWORD`, otherwise none. That credential's secret, and any userinfo in `git_url`, never appear in the response, the audit event or the log.
5. Access works like every other workspace write. An API key or admin token is allowed, and so is a PAT with `workspaces:write` used by the owner. A PAT bound to another workspace gets 404 on that slug before its scope is looked at. Any other PAT without the scope gets 403. A member of the workspace's org gets 403 `forbidden`. A bound PAT with the scope works on its own slug.
6. While another operation holds the workspace lock, the answer is 409 `workspace_busy`.
7. Every push attempt (the outcomes R6 counts as attempts) produces exactly one `hub.branch.push` audit event that records what really happened, success or failure. This holds even when the caller has gone away before the answer.
8. `afc workspace push <slug> <branch> [--force]` prints the response as one JSON document and exits with apikit's exit codes. When hub refuses, the output names the error type.
9. The archive's push to `origin` behaves exactly as before, and the existing archive and reclone tests pass without changes.

## Non-goals

- **Automatic forwarding of pushes.** This spec adds no variable such as `PUSH_BRANCHES_TO_ORIGIN` that would forward pushes to hub's git server on to `origin`. The explicit operation comes first.
- **Pushing to `upstream`.** Hub never pushes to a carry-patch workspace's upstream.
- **Deleting branches on the forge, pushing tags, or pushing to a different branch name.** The source and destination are always `refs/heads/<name>`.
- **More than one branch per request.** Callers that need several branches call the route in a loop.
- **Pull requests.** Opening one is `POST /workspaces/:slug/pulls` (spec `31_forge_writes`).
- **Changes to carry-patch push control or mirroring.** `PUSH_PATCHES_TO_ORIGIN`, `REBUILD_PUSH_INTEGRATION_BRANCH` and `internal/carrypatch` stay as they are.
- **Changes to the archive or reclone push**, including adding a deadline to it.
- **A configurable push deadline.** It is fixed at two minutes.
- **SSH-specific handling.** An `ssh`/scp-style `git_url` is pushed the same way the archive pushes it, and its failures are mapped like any other transport failure.
- **Router and proxy timeouts.** `deploy/route.yaml` is not changed. If a caller's connection is cut before the push ends, the caller does not get the answer. The push still finishes and is recorded (R6, R8), and calling again shows where the branch stands.

## Background

Today a branch reaches `origin` only when the workspace is archived or recloned. `handleArchiveWorkspace` (`internal/workspace/handlers.go`) and `handleRecloneWorkspace` (`internal/workspace/reclone_handler.go`) call `archiveOpenAndPushFn`. That is `defaultArchiveOpenAndPushFn` in `internal/workspace/clone.go`. It opens the trunk with `git.PlainOpen` and pushes `refs/heads/*:refs/heads/*` to `RemoteName: "origin", RemoteURL: ws.GitURL`, using the credential from `resolveCloneAuth`. Archive logs a failure and keeps the clone on disk. `archiveOpenAndPushFn` is assigned in `InitCloneQueue`, and some handler test environments never call that function. About a dozen tests replace the function with the three-argument `ArchiveOpenAndPushFuncType`. A `standard` workspace has no other route to the forge, and the agent-fox service needs the branch on the forge before it can open a pull request through hub's forge API.

What already exists and is reused:

- **Credentials.** `ResolveCloneAuth(store, slug)` in `internal/workspace/clone_auth.go` returns `GIT_PAT` as `BasicAuth{"x-token-auth", pat}`, otherwise `GIT_USERNAME` and `GIT_PASSWORD`, otherwise nil.
- **Lock.** `internal/wslock` provides `TryLock(slug)`. When the lock is held, handlers answer 409 `workspace_busy` with the message `another operation is running on this workspace; retry later`, as sync does in `internal/workspace/sync_handler.go`. A rebuild holds the lock only during its fetch phase and its final phase.
- **The trunk's `HEAD` under the lock.** A merge checks out its source branch while it holds the lock and restores the original checkout afterwards (`internal/merge/merge.go`; `RebaseSource` in `internal/merge/handler.go`). While the lock is held, the trunk's `HEAD` can therefore point at a branch other than the workspace's branch.
- **The worktree after a push to hub.** After receive-pack, the git server's `updateHeadSHA` (`internal/gitserver/handlers.go`) hard-resets the trunk's worktree to `HEAD`. It skips the reset when the workspace lock is held, because it assumes the operation holding the lock "restores the tree when it finishes". receive-pack writes refs without taking the lock.
- **Access.** `lookupWorkspaceForAuth(c, db, slug, auth, wsaccess.NeedWrite)` answers 404 to a stranger and 403 `forbidden` to an org member, with the shared message `org members have read-only access to this workspace`. The binding guard (`wsaccess.BindingGuard`) runs before every route under `workspaces/:slug`. It gives a bound PAT 404 `workspace not found` on any other slug, before the handler runs and so before any scope check.
- **Carry-patch rules.** `internal/carrypatch/push_control.go` already treats the integration branch and registered patches (`patches.status != 'deleted'`) as special. The workspace package can read the same rows with `getPatchByBranch` (`internal/workspace/patch_store.go`). When the integration-branch column is empty, the integration branch defaults to `deploy` (`internal/carrypatch/api.go`).
- **Branch names.** `gitcmd.ValidateRefName` follows the rules of `git check-ref-format --branch` and also rejects a leading `-`. Its doc comment says invalid names are reported as 400, and the merge API answers 400 (`internal/merge/api.go`). The input asks for 404 on this route instead.
- **Error vocabulary.** The forge routes already use the error types `forge_unauthenticated`, `forge_forbidden`, `forge_not_found`, `forge_rejected`, `forge_unreachable`, `forge_timeout` and `forge_error` (`internal/forge/errors.go`, `docs/api.md`).
- **Detached cancellation.** The forge writes make their forge calls and emit their audit events on the request context with its cancellation removed (`context.WithoutCancel` in `internal/forge/write.go` and `write_events.go`). A write that has left hub therefore finishes and is recorded even when the caller goes away.
- **Audit.** `emitHubAudit` (`internal/workspace/audit_emit.go`) fills in the actor and sends the event through the emitter, which also feeds the SSE stream. Event constants are defined in `internal/audit/types.go`.
- **Scrubbing.** The `scrub` function in `internal/forge/scrub.go` is unexported. Its comment says hub's packages repeat the userinfo pattern rather than import each other for it. `internal/carrypatch/import_graph_test.go` forbids `workspace` and `carrypatch` from depending on each other.
- **Escaped path segments.** echo leaves a path segment containing `%2F` encoded in its path parameter and decodes a segment without one. The forge label route therefore reads its segment from `r.URL.EscapedPath()` and decodes it once (`internal/forge/write_labels.go`, `lastSegment`). On the client side, `afc` builds paths with `apiPath` (`internal/cli/cli_helpers.go`), which escapes `/` as `%2F`.
- **CLI.** `afc` commands use apikit's client: `CLIClientFromCmd`, `DoRequest`, `CLIPrintResult` and `CLIHandleError`. On an error answer the client keeps the code and the message and drops `error_type` (`internal/cli/sandbox_cmd_test.go`). An API error exits 1 and a client error exits 2 (`internal/cli/exit.go`).

## Requirements

### R1. The route and its answer

Hub serves `POST /api/v1/workspaces/:slug/branches/:name/push`. `:name` is the branch name sent as a single path segment, with any `/` escaped as `%2F`. Hub percent-decodes the segment, as sent, exactly once. For example, `fix%2F42-nil-map` names `fix/42-nil-map`, and `a%2525b` names `a%25b`, not `a%b`. A path with an unescaped `/` inside the name matches no route.

The body is optional. If present, it must be a JSON object, and `force`, if given, must be a boolean (default `false`). A body that is not valid JSON, or a `force` that is not a boolean, gets 400. Unknown fields are ignored.

On success the answer is 200 with:

```json
{
  "branch": "fix/42-nil-map",
  "sha": "<40-hex local tip that was offered to origin>",
  "remote": "origin",
  "remote_url": "https://github.com/acme/widgets.git",
  "result": "pushed"
}
```

`result` is `up_to_date` when origin already had that commit on that branch and nothing was sent, even if `force` was given. It is `forced` when `force` was given and the forge accepted an update. Every other success is `pushed`. `remote` is always `origin`. `remote_url` is the workspace's `git_url`: a URL with a scheme has its userinfo removed, and an scp-style address (`git@host:path`) is returned unchanged.

**Error messages.** Some error answers of this route carry one of these error types: `branch_not_found`, `integration_branch`, `patch_branch`, `protected_branch`, `non_fast_forward`, or one of the `forge_*` types. Their message begins with the error type followed by `: `, for example `non_fast_forward: origin's fix/42-nil-map is at …`. `afc` shows only the message, so this is how a CLI user learns the error type. Answers shared with the other workspace routes keep their shared messages. These are the 404 `workspace not found`, the 403 `forbidden` for org members, the scope 403 of R2, and the 409 `workspace_busy`.

### R2. Who may push

The checks run in this order:

1. A request with no credential gets 401.
2. A PAT bound to a different workspace gets 404 `workspace not found`, whatever its scopes. The existing binding guard gives this answer before the route's own checks.
3. A PAT without `workspaces:write` gets 403 with the message `PAT requires workspaces:write scope to push branches` and no `error_type`. This happens before the workspace is looked up.
4. The workspace is looked up with write access required, as for every other workspace write.
   - An unknown slug gets 404 `workspace not found`, and so does a caller who is not the owner, not an admin token and not an org member.
   - A member of the workspace's org gets 403 `forbidden`.
   - A database error gets 500.

The owner and admin tokens get past these checks. The owner may use an API key or a PAT with the scope, including a PAT bound to this slug.

### R3. Name and workspace preconditions

After access is decided, and before the lock is taken:

- A name that breaks hub's ref-name rules gets 404 `branch_not_found`, the same answer as a valid name that is missing from the trunk. The rules are those of `git check-ref-format --branch`, plus no leading `-`. The message names the rule the name broke, for example `branch_not_found: "-x" is not a valid branch name: ref name must not start with '-'`.
- A workspace whose `status` is not `active` gets 400 (`workspace is not active; push requires an active workspace`).
- A workspace whose `clone_status` is not `ready` gets 400 (`workspace clone is not ready; clone_status must be 'ready' to push`).

### R4. Branches this route will not push

Nothing is sent to the forge for any of these refusals.

These are decided from the database and the workspace row, before the lock is taken:

- On a `carry_patch` workspace, a name equal to the integration branch gets 400 `integration_branch`. The integration branch is the `integration_branch` column, or `deploy` when that column is empty. The message says that hub builds this branch and that it reaches the fork through `REBUILD_PUSH_INTEGRATION_BRANCH`.
- On a `carry_patch` workspace, a name that has a patch row with a status other than `deleted` gets 400 `patch_branch`. The message points to `PUSH_PATCHES_TO_ORIGIN`.
- When `force` is true and the workspace's `branch` column is set, a name equal to `branch` gets 400 `protected_branch`.
- A database error while making these decisions gets 500.

This one is decided after the lock is taken (R5), before the branch lookup and the push:

- When `force` is true and the workspace's `branch` column is null, the workspace's own branch is the branch the trunk's `HEAD` points to, read while the lock is held. A name equal to it gets 400 `protected_branch`. If `HEAD` cannot be read, the answer is 500.

The protected-branch rule applies to both workspace modes. Without `force`, the workspace's own branch pushes fast-forward like any other branch.

### R5. The lock, the branch and the worktree

The push is serialized with every other operation on the trunk through the workspace lock. The route tries to take the lock without waiting. If another operation holds it, the answer is 409 `workspace_busy` with the message the other lock-holding routes use. That includes a rebuild in its fetch or final phase. A rebuild between those phases does not hold the lock and does not block the push, because the push deletes nothing.

While holding the lock, hub first applies the `HEAD`-based refusal of R4. It then resolves `refs/heads/<name>` in the workspace's trunk. If the ref is missing, the answer is 404 `branch_not_found` (`branch_not_found: branch "<name>" does not exist in the workspace`). If it exists, its commit becomes `sha`.

The lock is held until the push has ended (R6), even if the caller has disconnected. It is released on every path.

When hub takes the lock, it notes the commit the trunk's `HEAD` resolves to. Before releasing the lock, it checks `HEAD` again. If `HEAD` now resolves to a different commit, a push to hub's git server moved the checked-out branch while this route held the lock, and the git server skipped its worktree reset because of that. Hub then hard-resets the trunk's worktree to the new `HEAD` commit, as the git server would have done. A failed reset is logged at warning level and does not change the answer. If `HEAD` has not moved, or cannot be read at either point, the worktree is not touched.

### R6. The push and what its outcome means

The credential is resolved the way the archive's push resolves it (Goal 4). If the secrets store fails, the answer is 500 `internal server error`, and the failure is logged without any secret.

The archive and the branch push share one push implementation (Design Decision 7). The branch push sends to the trunk's remote `origin` at the URL `git_url`. The refspec is exactly `refs/heads/<name>:refs/heads/<name>`, or the forced form `+refs/heads/<name>:refs/heads/<name>` when `force` is true. The push has a two-minute deadline from when it starts. It is not cancelled when the caller disconnects: it runs until it ends or the deadline passes. Its outcome is logged (R7) and recorded (R8) exactly as if the caller were still connected.

| Outcome | Answer |
|---|---|
| Success | 200 `pushed`, or `forced` when `force` was given |
| go-git reports already up to date | 200 `up_to_date` |
| Not a fast-forward: go-git's client-side check refuses, or the forge's per-ref status reports `non-fast-forward` / `fetch first` | 409 `non_fast_forward` |
| Forge answers 401, or authentication is required | 502 `forge_unauthenticated` |
| Forge answers 403, or authorization failed | 502 `forge_forbidden` |
| Repository not found | 404 `forge_not_found` |
| Forge refuses the ref for another reason (protected branch, hook declined) | 422 `forge_rejected` |
| Deadline passes | 504 `forge_timeout` |
| DNS, TCP or TLS failure | 502 `forge_unreachable` |
| Any other push failure | 502 `forge_error` |
| The trunk cannot be opened, or `origin` is not configured in it | 500 |

Every outcome in this table except the last row is a **push attempt**, whether or not the request reached the forge. The last row, the secrets-store 500 and every refusal in R2 to R5 are not push attempts.

Details for some of these answers:

- **`non_fast_forward`.** Hub then lists origin's refs once, with the same URL and credential and within the same deadline. If the listing finds the branch, the message names origin's commit. When the branch is not the workspace's own branch (the rule of R4, with `HEAD` read under the lock when `branch` is null), the message ends with a force hint, for example: `non_fast_forward: origin's fix/42-nil-map is at <sha>, which is not in the workspace's branch; push with force to overwrite it`. When the branch is the workspace's own branch, force would be refused, so the message points to syncing the workspace instead, for example: `non_fast_forward: origin's main is at <sha>, which is not in the workspace's branch; sync the workspace to bring it up to date`. If the listing fails, the message leaves the commit out. The answer is 409 either way.
- **`forge_unauthenticated`.** The message says whether the workspace has no stored credential or the forge rejected the one it has. It names the credential's kind (`GIT_PAT` or `GIT_USERNAME`/`GIT_PASSWORD`), never its value.
- **`forge_rejected`.** The message carries the forge's reason, scrubbed.

### R7. Keeping the credential out

Every error message this route returns or logs goes through a scrub, and so does every value in its audit event. The scrub:

- replaces each occurrence of the credential's password or token with `[redacted]`;
- removes URL userinfo;
- repeats those two steps until the text stops changing;
- cuts the result to 1024 bytes on a UTF-8 boundary.

The response never contains the credential, the userinfo in `git_url`, or the full error chain from go-git. Each failed push attempt (R6) is logged exactly once at warning level, with `workspace`, `branch`, `error_type` and the scrubbed error. A successful push attempt is not logged by this rule. The 500 answers are logged the way hub logs its internal errors, scrubbed in the same way.

### R8. The audit event

A new hub event type, `hub.branch.push`, is defined next to the other hub event types, with a doc comment like theirs. Hub emits it exactly once for every push attempt (R6):

- 200 `pushed`, `forced` or `up_to_date`;
- 409 `non_fast_forward`;
- 404 `forge_not_found` and 422 `forge_rejected`;
- 502 `forge_unauthenticated`, `forge_forbidden`, `forge_unreachable` or `forge_error`;
- 504 `forge_timeout`.

It emits nothing for any other answer: 401, 403, 404 `workspace not found` or `branch_not_found`, 400, 409 `workspace_busy`, and every 500. That includes a secrets store failure and a trunk that cannot be opened or has no `origin`. The event is emitted with the request's cancellation removed, so it is not lost when the caller has gone away.

The event fields are:

- `ResourceType "branch"`, `ResourceID` the branch name, `Action "push"`, `Workspace` the slug.
- The actor comes from the credential, as for every hub event.
- Metadata:
  - `branch`, `sha`, `force` (bool), `ok` (bool), `remote` (`"origin"`) and `remote_url` (userinfo removed);
  - `result` when `ok` is true;
  - `error_type` when `ok` is false.

The event never holds a credential or the forge's message text. Like every hub event, it appears on the SSE stream.

### R9. `afc workspace push`

`afc workspace push <slug> <branch> [--force]` takes exactly two arguments. It sends `POST /api/v1/workspaces/<slug>/branches/<branch>/push` with the body `{"force": <bool>}`. Each path segment is escaped, so a `/` in the branch name is sent as `%2F`. With `--force`, the command first writes one warning line to stderr, saying that origin's branch may be overwritten.

- On success it prints the response as one JSON document on stdout and exits 0.
- When hub answers with an error, it prints apikit's error envelope (the hub's code and message) and exits 1. A non-fast-forward, for example, exits 1, and the output contains `non_fast_forward` (R1, Error messages).
- A client error exits 2.
- A wrong number of arguments fails with exit 2 without contacting hub.

### R10. Documentation

- `docs/api.md` and `docs/openapi.yaml`:
  - the route, its request and response, and its error table;
  - the new error types (`branch_not_found`, `non_fast_forward`, `integration_branch`, `patch_branch`, `protected_branch`), and the rule that this route's messages for its own error types begin with the error type;
  - this route added to the existing `forge_*` and `workspace_busy` rows, with a note on the two-minute deadline for `forge_timeout`;
  - the route added to the Workspace Lock list;
  - the `hub.branch.push` event.
- `docs/cli.md`: `afc workspace push`.
- `docs/permissions.md`: the route under `workspaces:write`, and in the workspace endpoint access matrix (write).
- `docs/architecture.md`: the push implementation that archive and branch push share, the lock, and the worktree reset after a concurrent push to hub's git server.
- `docs/carry_patch_workflow.md`: one paragraph under "Where patch branches live" on the two carry-patch refusals.

## Design Decisions

1. **The branch name goes in the path and is percent-decoded exactly once.** The input asks for a path segment. echo leaves a segment containing `%2F` encoded and decodes others. Decoding the segment as sent exactly once, as the forge label route does, keeps a name that contains `%` intact.
2. **An invalid name is 404 `branch_not_found`, as the input asks.** The input settles it: "a name that is not a valid ref or does not exist in the trunk is 404 `branch_not_found`". `gitcmd.ValidateRefName`'s doc comment and the merge API use 400, but that is a convention, not a constraint, and a name that breaks the rules cannot name a branch of the trunk anyway. The message still names the broken rule, so a caller can tell a malformed name from a missing one. No `invalid_branch` type is added. The check runs before the lock, because it needs no trunk state.
3. **A PAT missing `workspaces:write` gets 403 with no `error_type`.** This is what the input asks for. It matches sync, the forge writes and the documented rule "the other endpoint families also return 403 for a missing scope". The 404 of errata 03 is limited to the workspace CRUD endpoints.
4. **Access is the write access every other workspace write requires.** That means the owner or an admin token, with an org member getting 403 `forbidden` (spec 35). Confining a bound PAT to its slug comes from the existing binding guard, which answers 404 for another slug before the handler runs and so before the scope check. R2 lists it as step 2 for that reason.
5. **Checks that read only the database and the workspace row come before the lock. Every read of the trunk comes after it.** Refusals that do not depend on trunk state give the same answer whether the workspace is busy or not. The branch lookup runs under the lock. So does the protected-branch check when `branch` is null, because it reads `HEAD`. A merge checks out its source branch while it holds the lock, so a `HEAD` read without the lock could name the wrong branch and give a wrong 400 `protected_branch` where 409 `workspace_busy` is right. The `sha` is read under the lock the push holds.
6. **Take the lock without waiting, and do not wait for a rebuild that is between its phases.** The push deletes no directory, so it follows sync, not archive.
7. **Archive and branch push share one push implementation, and the archive's behaviour does not change.** The input asks for the archive's push to be reused for one branch. How the implementation is factored, and how tests replace it, is left to the design. The bound is that the existing archive and reclone tests pass unchanged (Goal 9).
8. **The deadline is a constant two minutes.** It matches `forwardTimeout` and `mirrorTimeout` (120 s) in `internal/carrypatch`, and hub has no configurable git deadline for it to join.
9. **`forced` means force was requested and the forge accepted an update.** It does not claim that history was actually rewritten. When nothing was sent, `up_to_date` takes precedence. This keeps the operation to one push with no extra round trip.
10. **A refusal from go-git's client-side check counts as non-fast-forward.** go-git looks for origin's tip among the ancestors of the local tip. A forge commit the trunk does not have is therefore not a fast-forward, which is the case the 409 exists for. A per-ref `non-fast-forward` or `fetch first` status from the forge is treated the same way.
11. **Origin's commit goes in the 409 message, not in a field.** The steering requires apikit's error writer and forbids hand-built error maps, so the envelope cannot carry extra fields. The commit is looked up best-effort after the refusal, with one listing of origin's refs that uses the same URL, credential and deadline.
12. **Forge failures reuse the forge routes' error types and statuses**, including 404 for `forge_not_found`. A client that already handles them needs nothing new.
13. **A secrets store failure is 500.** It is hub's own store, as with the forge package's `errInternal`. Sync's 502 for the same case is not followed.
14. **The protected branch is `branch`, or the trunk's `HEAD` branch when `branch` is null.** That is the rule sync uses to find the workspace branch. `HEAD` is read under the lock (Decision 5).
15. **The integration branch is `integration_branch`, or `deploy` when that column is empty**, matching `internal/carrypatch/api.go`.
16. **A registered patch is a patch row whose status is not `deleted`**, matching push control's `isRegisteredPatchBranch`.
17. **The audit event is emitted for failed push attempts too.** One event per attempt, with `ok` and `error_type`, is the forge-write family's convention, and a refused force-push is worth having in the record. Decision 24 defines a push attempt.
18. **The scrub helper is local to `internal/workspace`.** `scrub` in `internal/forge` is unexported. Its comment records the repository's convention that hub's packages repeat the userinfo pattern instead of importing each other for it. Nothing in the import graph would stop `workspace` from importing `forge`, because `forge`'s production code does not import `workspace`. Exporting `forge`'s scrub for this would go against that convention, though. `internal/carrypatch`'s helpers are ruled out for a firmer reason: `carrypatch/import_graph_test.go` forbids `workspace` and `carrypatch` from depending on each other.
19. **scp-style remotes come back unchanged in `remote_url`.** The `git` login name is not a secret, which is the position `carrypatch`'s `sanitizeErrorText` takes.
20. **Error messages carry their error type, because the CLI shows only the message.** The input wants a non-fast-forward to exit non-zero "with the error type". apikit's CLI client keeps the code and message of an error answer and drops `error_type`, and `afc` uses apikit's client rather than a hand-built one. So the messages for this route's own error types begin with `<error_type>: ` (R1), and `afc` shows them through apikit's error envelope with exit 1. Answers shared with other workspace routes keep their shared text, so existing clients and tests that match on it are unaffected.
21. **The CLI always sends `{"force": <bool>}`, and the server accepts an empty body.** Both a scripted caller and a bare `curl -X POST` work.
22. **A concurrent push to hub's git server for the same branch is not serialized with this route.** receive-pack writes refs without the workspace lock, which is also true for the archive. `sha` is the tip read under the lock just before the push. Decision 27 covers what such a push does to the worktree.
23. **The push runs to its end even if the caller disconnects.** It runs on the request context with its cancellation removed and the two-minute deadline, and its event is emitted the same way, as the forge writes do. Cancelling mid-push would leave the forge's ref in an unknown state and could record a failure that was not one. Finishing keeps the record true. A caller that lost the answer calls again and gets `up_to_date` or the real refusal. The lock stays held until the push ends.
24. **The audit event and the warning log share one trigger: a push attempt.** A push attempt is every outcome after hub has handed the push to the transport. That covers success, `up_to_date`, and every `non_fast_forward` and `forge_*` failure, including `forge_unreachable` and `forge_timeout`, which may never reach the forge. Not attempts: the 500 for a trunk that cannot be opened or has no `origin`, the secrets-store 500, and every refusal before them, because they are hub's own failures. Goal 7, R6, R7 and R8 all use this definition.
25. **No force hint for the workspace's own branch.** On that branch the 409 message points to syncing the workspace instead, because R4 refuses force there.
26. **Requirements state behaviour. File, function and seam choices are left to the design.** The Background and the codebase notes name the existing helpers to reuse, as the steering's reuse rule asks, but no requirement depends on them.
27. **The route restores the worktree when a push to hub moved `HEAD` while it held the lock.** The git server skips its worktree reset while the lock is held and relies on the lock holder to restore the tree. The branch push does not touch the worktree otherwise. So it compares `HEAD`'s commit when it takes the lock and before it releases it, and hard-resets only if `HEAD` moved. Leaving the worktree stale until the next trunk operation was rejected: the git server's comment makes the lock holder responsible, and the check costs one comparison per push.

## Dependencies

| Spec | Relationship | Reason |
|---|---|---|
| `33_token_workspace_binding` | depends on | The binding guard confines a bound PAT to its slug on every `workspaces/:slug` route. The new route relies on it for "bound PAT on another slug is 404", which the guard answers before the scope check. |
| `35_org_member_workspace_access` | depends on | Write access, and the 403 `forbidden` answer for org members, come from this spec. |
| `31_forge_writes` | depends on | The route reuses the forge error types, the convention of one audit event per request that reached the forge, and the detached-cancellation pattern for forge calls and their events. The pull request that follows a push is opened through this spec's `POST .../pulls`. |

## Verified External API

go-git is `github.com/go-git/go-git/v5 v5.19.3` (go.mod). Its source is in the module cache, which these tools cannot reach, so the signatures below come from call sites in the repository.

- `git.PlainOpen(path string) (*git.Repository, error)`, from `internal/workspace/clone.go`.
- `(*git.Repository).PushContext(ctx context.Context, o *git.PushOptions) error`, from `internal/carrypatch/push_mirror.go`.
- `git.PushOptions{RemoteName string, RemoteURL string, RefSpecs []config.RefSpec, Auth transport.AuthMethod, Force bool}`, from `clone.go` (`RemoteURL`) and `push_mirror.go` (`Force`).
- `config.RefSpec` (a string type), from `clone.go` and `push_mirror.go`.
- `git.NoErrAlreadyUpToDate`, from `clone.go`.
- `git.NewRemote(memory.NewStorage(), &config.RemoteConfig{Name, URLs})` and `(*git.Remote).ListContext(ctx, &git.ListOptions{Auth}) ([]*plumbing.Reference, error)`, from `internal/workspace/credentials.go`. The return value is discarded there, so its type is **unverified**.
- `(*git.Repository).Reference(name plumbing.ReferenceName, resolved bool) (*plumbing.Reference, error)`, `(*git.Repository).Head()`, `plumbing.NewBranchReferenceName(string)`, `(*plumbing.Reference).Hash()` and `.Name().Short()`, from `internal/workspace/sync_git.go`.
- `(*git.Repository).Worktree()` and `(*git.Worktree).Reset(&git.ResetOptions{Commit: plumbing.Hash, Mode: git.HardReset}) error`, from `updateHeadSHA` in `internal/gitserver/handlers.go`.
- `transport.ErrRepositoryNotFound`, from `internal/gitserver/gitserver.go`.
- `githttp.BasicAuth{Username, Password}`, from `internal/workspace/clone_auth.go`.
- **unverified**: `git.ErrNonFastForwardUpdate` (assumed to be wrapped by the error of the client-side fast-forward check), `transport.ErrAuthenticationRequired` and `transport.ErrAuthorizationFailed` (assumed to be go-git's errors for HTTP 401 and 403), and the text of a per-ref refusal from report-status (assumed to contain the forge's reason, such as `non-fast-forward`). The mapping should also fall back to matching the error text.

Go standard library (go.mod: `go 1.27`):

- `context.WithoutCancel(parent context.Context) context.Context`, used in `internal/forge/write.go` and `write_events.go`.

apikit lives at `../apikit` through a `replace` directive, outside the repository. These signatures come from call sites.

- `apikit.GetAuthInfo(c echo.Context) *apikit.AuthInfo`
- `apikit.WriteAPIError(c, code int, message string) error`
- `apikit.WriteAPIErrorWithType(c, code int, message, errorType string) error`, from `internal/workspace/handlers.go`.
- `apikit.CLIClientFromCmd(cmd)`
- `client.DoRequest(ctx, method, path string, body any) (any, error)`
- `apikit.CLIPrintResult(cmd, result) error`
- `apikit.CLIHandleError(cmd, err) error`
- `apikit.NewCLIError(code int, msg string)`, from `internal/cli/workspace_cmd.go`.

echo is `github.com/labstack/echo/v4 v4.16.0`. That it routes on the raw path and leaves `%2F` encoded in path parameters is shown in practice by `internal/forge/write_labels.go` (`lastSegment`).
