---
spec_id: "22"
spec_name: "fork_push_control"
title: "Fork Push Control: Reject, Forward and Mirror Hub Pushes to Patch Branches"
status: "active"
created_at: "2026-10-01T14:36:10.824726Z"
updated_at: "2026-10-01T14:36:10.824726Z"
intent_hash: "de99dd08858059f8e028f358554ee75090e3c6111c541d217af5b402c3d0963b"
schema_version: 2
source: "https://github.com/agent-fox-dev/hub/issues/35"
---
## Intent

The hub's git server accepts a push to any branch, including a registered patch branch. Once the fork is declared authoritative for patch branches (`PATCH_BRANCH_SOURCE=origin`, spec `20_fork_patch_sync`), an accepted hub push is a second writer. The next sync then replaces or reports the pushed work. This spec makes the git server enforce a single writer per authority model. In `origin` mode a hub push to a registered patch branch is refused, or, when opted in, forwarded to the fork before the hub accepts it. In `hub` mode an opt-in mirror pushes accepted patch-branch pushes on to the fork. A workspace that sets none of this behaves exactly as before.

## Goals

- A workspace variable `PUSH_PATCHES_TO_ORIGIN` (only the exact string `true` enables it) selects, together with `PATCH_BRANCH_SOURCE`, one of three push behaviours for registered patch branches:
  - `origin` mode, variable not `true`: the push to that ref is rejected with a per-ref message naming the fork.
  - `origin` mode, variable `true`: the update is forwarded to the fork with a non-force push before the hub writes its own ref. If the forward fails, the hub ref is not written.
  - `hub` mode, variable `true`: after the push is accepted, the hub force-pushes each updated registered patch branch to the fork, best effort.
- Rejections and forward failures are reported to the git client as per-ref errors. Other refs in the same push are unaffected, and the client sees the per-ref message rather than a whole-push failure.
- The git server gains a pre-receive hook registration, in the style of `RegisterPostPushHook`. `internal/gitserver` does not import `internal/carrypatch`.
- Only ref updates the hub actually accepted drive `head_sha`, the `hub.git.push` audit event and the post-push hook.
- A failed hub-mode mirror is logged and emitted as `hub.patch.mirror_failed`. It never fails the push or the rebuild.
- Pushes to unregistered branches, to the integration branch, to tags and to other refs, and pushes to standard-mode workspaces, are unchanged.
- Tests cover every behaviour above with a local bare repository as `origin`.

## Non-goals

This is the third of five scopes. The others get their own specs, and nothing below is built here.

- **`fork_patch_sync`** (`20_fork_patch_sync`, written): `PATCH_BRANCH_SOURCE` and `PATCH_DIVERGENCE_POLICY` semantics, the origin fetch in sync and the patch refresh. This spec only reads `PATCH_BRANCH_SOURCE`.
- **`fork_patch_registration`** (`21_fork_patch_registration`, written): branch resolution at `POST /workspaces/:slug/patches`.
- **`patch_divergence_recovery`**: `reset-to-origin`, `replaced_sha`, rejecting pushes to `refs/hub/replaced/*`, and backup-ref cleanup.
- **`patch_authority_docs`**: the restructure of `docs/carry_patch_workflow.md`, `docs/examples/AGENTS_carry_patch.md`, and moving ADR 01 to `Accepted`. This spec makes only the narrow edits to the guide listed in Requirement 9.
- **Mirroring or forwarding anything other than registered patch branches.** The integration branch is never mirrored by this spec. It is already pushed by `REBUILD_PUSH_INTEGRATION_BRANCH`.
- **Forwarding or mirroring branch deletions** to the fork. The hub never deletes a fork branch.
- **Force-forwarding** in `origin` mode. A forward is never forced.
- **Taking the workspace lock** in the git server, **webhooks** from the fork's host, and **per-user permissions** beyond the existing `git:write` check.
- **Changing `AUTO_REBUILD_AFTER_PUSH`** semantics.
- **Updating `refs/remotes/origin/*` after a forward or mirror.** The next fork fetch does it.

## Background

**Git server today** (`internal/gitserver/handlers.go`):

- `handleReceivePack` decodes a `packp.ReferenceUpdateRequest` from the body, runs `sess.ReceivePack`, and writes the report status with `rs.Encode`.
- After that it calls `updateHeadSHA`, then `emitGitPushAudit(c, slug, req.Commands)`, then, in a goroutine, the registered `postPushHook(db, slug, extractPushedBranches(req.Commands))`.
- All three use `req.Commands` as decoded. They do not look at the per-command status in the report, so a ref that failed to update would still count.
- `RegisterPostPushHook(fn PostPushHookFunc)` stores a package-level `postPushHook` of type `func(db *sql.DB, slug string, branches []string)`. Deletes are excluded from `extractPushedBranches`.
- The go-git `server.Server` is created once in `MountGitHandlers` from `WorkspaceLoader`. `WorkspaceLoader.Load` opens `<workspaceRoot>/<slug>/trunk` and returns `thinPackSafeStorer{repo.Storer}`. That wrapper hides `PackfileWriter` so thin packs resolve. It is the one place that sits between go-git's receive-pack and the trunk's storer.
- Git routes run behind `GitAuthMiddleware`, which injects `*apikit.AuthInfo`. The git server does not take the workspace lock (only `updateHeadSHA` uses `wslock.TryLock` for the worktree reset).

**Carry-patch post-push hook** (`internal/carrypatch/push_hook.go`): `NewPostPushRebuildHook(queue, getVariable)` returns the hook. `postPushRebuildHook` skips non-carry-patch workspaces, checks that a pushed branch matches any `patches` row of the workspace (`workspace_slug`, `branch_name`), honours `AUTO_REBUILD_AFTER_PUSH`, and enqueues a `rebuild` job with `SubmittedBy` `system:push-hook`. `cmd/af-hub/main.go` registers it with `gitserver.RegisterPostPushHook`, and calls `gitserver.SetAuditEmitter(auditEmitter)` for push audit events.

**Origin access.** The trunk is a clone of the fork, so its `origin` remote points at the fork. `workspaces.git_url` holds the fork's clone URL. `carrypatch.DefaultPushIntegrationFunc` shows the existing origin-push pattern: `workspace.ResolveCloneAuth` credentials (`GIT_PAT`, then `GIT_USERNAME`/`GIT_PASSWORD`, then none), `git.PlainOpen(repoPath)`, `repo.PushContext` with `RemoteName: "origin"`, and `git.NoErrAlreadyUpToDate` treated as success.

**What spec 20 delivers that this spec uses.** `PATCH_BRANCH_SOURCE` with values `hub` (default for unset, unrecognised or a lookup error) and `origin`, read through `GetVariable("workspace", slug, ...)`. Its non-goals state that until this spec ships, hub pushes to patch branches are accepted in `origin` mode and the next sync replaces them (with a `refs/hub/replaced/<branch>` backup) or reports them. Spec 20's sync ref writes are compare-and-swap, because the git server does not hold the workspace lock. This spec relies on the same property.

**Why a hook inside the receive, not a check before it.** Forwarding must happen before the hub's ref is written. The commit must be readable from the trunk's object store to be pushed, so it must follow unpacking. Both conditions point to the ref-write step of the receive-pack session. go-git records a failed ref write as that command's status in the report, so a rejection there reaches the client as a per-ref error.

## Requirements

### 1. Configuration and scope of control

`PUSH_PATCHES_TO_ORIGIN` is a workspace variable. Only the exact, case-sensitive string `true` enables it. An unset variable, any other value or a lookup error means disabled. `PATCH_BRANCH_SOURCE` is read with the same helper that spec 20 provides, so unset, unrecognised and lookup-error values mean `hub`.

Both variables are read through `GetVariableFunc` for each ref update that the control applies to, so a change takes effect on the next push without restarting anything. Setting them needs no change to the variables API.

Push control applies to a ref update when all of these hold:

- The workspace's `workspace_mode` is `carry_patch`.
- The ref is `refs/heads/<name>`.
- `<name>` equals the `branch_name` of a `patches` row of that workspace whose status is not `deleted`.
- `<name>` is not the workspace's `integration_branch`, even if a row with that name exists.

All other ref updates go through exactly as today. For a standard-mode workspace neither variable is read.

Verification: unit tests cover the variable parsing (unset, `true`, `True`, `1`, lookup error) and every row of the applicability list, including a `deleted` patch, the integration branch and a standard-mode workspace.

### 2. Pre-receive hook in the git server

`gitserver` exposes `RegisterPreReceiveHook(fn PreReceiveHookFunc)`, in the same style as `RegisterPostPushHook`. Registering `nil` clears it. With no hook registered, the receive-pack flow behaves as today.

The hook is called once per ref update (create, update or delete), in command order. It runs after the pack has been unpacked into the trunk and before the hub writes that ref. It receives: a context bound to the request, the `*sql.DB`, the workspace slug, the pushing credential's actor identity (from `apikit.GetAuthInfo`, which is `nil`-safe), and the update (ref name, old hash, new hash). Returning a non-nil error rejects that update. The hub does not write the ref, and the error text is the per-ref status message the client sees. Returning nil lets the hub write the ref.

Placement: the hook is invoked from the ref-write boundary of the receive session. The wrapper around the trunk storer (`thinPackSafeStorer`, which must keep hiding `PackfileWriter`) is extended to consult the hook on reference writes and removals. The receive-pack handler supplies the per-request context and actor. A per-request loader or server is acceptable for this. `gitserver` imports no carry-patch code.

A rejected ref leaves its old value in place. Other refs of the same push are processed as usual and keep their own status. The client receives `unpack ok` and a per-ref `ng <ref> <message>` for the rejected ones, which `git push` shows as `remote rejected` with the message. Objects already unpacked for a rejected ref stay in the object store. They are unreachable and harmless.

Verification: a gitserver test registers a hook that rejects one of two refs in a single push and asserts that the other ref is written, the rejected ref is unchanged, and the client output contains the message for that ref only. The test uses a real `git push` against the handler.

### 3. Reject mode (`origin` source, forwarding off)

When the source is `origin` and `PUSH_PATCHES_TO_ORIGIN` is not `true`, the carry-patch hook rejects an update to a registered patch branch (Requirement 1) with the message `branch is synced from origin; push to <git_url> instead`.

- `<git_url>` is the workspace's `git_url` column with any userinfo (`user:password@`) removed.
- Deletes of a registered patch branch are rejected in the same way. A deleted hub branch would only be recreated by the next sync.

Each rejection is logged at info level with slug, branch and the pushing user's ID (or `unknown`). The push as a whole still returns HTTP 200, with the per-ref error in the report.

Verification: tests cover a rejected create, update and delete, a push mixing a registered branch, an unregistered branch and a tag where only the registered branch is rejected, the message text including userinfo removal, and the info log line.

### 4. Forward mode (`origin` source, forwarding on)

When the source is `origin` and `PUSH_PATCHES_TO_ORIGIN` is `true`, the carry-patch hook forwards each create or update of a registered patch branch to the fork before the hub writes its own ref:

- It resolves origin credentials with `workspace.ResolveCloneAuth` (`GIT_PAT`, then `GIT_USERNAME`/`GIT_PASSWORD`, then none). A credential failure rejects the ref with `failed to resolve origin credentials`.
- It pushes the new commit to `refs/heads/<name>` on the trunk's `origin` remote with `Force: false` and no `+` in the refspec. An already-up-to-date result counts as success.
- The pushed commit is not yet referenced by any hub ref, so the push source must be resolvable from the trunk. The implementation either uses a temporary ref under `refs/hub/forward/<branch>`, removed afterwards on success and failure, or a hash source if go-git's push supports one.
- The push is bounded by a timeout of 120 seconds.
- On success the hub writes its ref.
- On any failure (non-fast-forward, auth, network, timeout) the hub does not write its ref. The update is rejected with the text `origin rejected push: <error>`, so the fork and the hub never disagree because of a hub push.
- Error text is passed through with any URL userinfo removed.
- Failures are logged at warn level with slug, branch, user and error.

Deletes of a registered patch branch in forward mode are rejected with `branch is synced from origin; delete it on the fork instead`. They are never forwarded.

The forward lengthens the push. This is accepted because it applies only to registered patch branches in `origin` mode with forwarding on. The forward does not take the workspace lock, which is consistent with the git server today. A concurrent sync's compare-and-swap ref write (spec 20) turns a race into a visible error and not lost work.

Verification: tests with a local bare repository as `origin` (the trunk's `origin` remote) cover:
- a forwarded create, which exists on both sides;
- a forwarded fast-forward update;
- a forward rejected because the fork moved ahead (non-fast-forward), which leaves the hub ref unchanged and returns the origin error text to the client;
- an unreachable `origin`, which leaves the hub ref unchanged;
- a mixed push where one ref forwards and another is not a patch branch;
- the temporary ref being absent after success and after failure;
- a delete being rejected.

### 5. Accepted refs only drive side effects

After the receive session, `handleReceivePack` determines which ref updates were accepted. An update is accepted when its entry in the report status is `ok`. If no report status is available, an update is accepted unless the pre-receive hook rejected it.

- `emitGitPushAudit` is given only the accepted commands. `refs_updated` therefore lists accepted refs only.
- The post-push hook is invoked with `extractPushedBranches` of the accepted commands only. Deletes stay excluded.
- If no update was accepted, no post-push hook runs and no `hub.git.push` event is emitted.
- `updateHeadSHA` is unchanged. It reads the trunk's HEAD and is harmless when nothing changed.

This corrects an existing gap: a ref that failed to update no longer counts as pushed. It applies to every push, not only to patch branches.

Verification: tests assert that a rejected-only push enqueues no rebuild, emits no audit event and calls no post-push hook. They also assert that a mixed push calls the hook with the accepted branch only and audits only the accepted ref.

### 6. Hub-mode mirror (`hub` source, forwarding on)

When the source is `hub` and `PUSH_PATCHES_TO_ORIGIN` is `true`, the post-push hook mirrors the push. After the push has been accepted, and in the same goroutine that enqueues the rebuild, it force-pushes each registered patch branch (Requirement 1 applicability) that the push created or updated to `origin`.

- The refspec is `+refs/heads/<name>:refs/heads/<name>`, with credentials resolved as in Requirement 4 and the same 120 second timeout.
- It pushes the branch's current hub tip at that moment.
- The rebuild enqueue runs first, unchanged. The mirror follows, whether or not a rebuild was enqueued, suppressed by `AUTO_REBUILD_AFTER_PUSH=false`, or deduplicated. The mirror runs even when `AUTO_REBUILD_AFTER_PUSH` is `false`.
- A mirror failure for one branch does not stop the others and does not fail the push or the rebuild. An enqueue failure does not stop the mirror.
- The mirror is best effort. There is no retry and no persisted state. The next push of the branch mirrors again.
- Branch deletions are not mirrored. This follows from `extractPushedBranches` excluding deletes.
- The mirror needs the post-push hook to carry out a push through the origin. `NewPostPushRebuildHook` is extended with the dependencies it needs (an origin-credentials resolver, a workspace root, and an audit emitter), wired in `cmd/af-hub/main.go`. Nil dependencies disable the mirror and leave the rebuild behaviour as today.

In `origin` mode this mirror never runs, because forwarding already covered the push.

Verification: tests with a local bare repository as `origin` cover:
- a mirrored create and a mirrored forced update after a rewrite;
- no mirror when the variable is not `true`;
- no mirror for an unregistered branch, the integration branch, or a standard workspace;
- the mirror running when `AUTO_REBUILD_AFTER_PUSH=false`;
- a failing mirror not preventing the rebuild enqueue;
- two branches in one push where one fails.

### 7. Mirror failure event and logging

A mirror failure is logged and emitted as the audit event `hub.patch.mirror_failed`, with:

- `resource_type` `patch`;
- `action` `mirror`;
- `actor_type` `system`;
- the workspace slug;
- metadata `{ branch_name, error }`, with any URL userinfo removed from `error`.

The event type is added as a constant in `internal/audit/types.go` next to `EventRebuildFollowup`. A nil emitter skips emission, and an emit error is logged without affecting anything else. Mirror failures are logged at warn level with slug, branch and error. A successful mirror is logged at info level.

Verification: tests assert the event type and metadata, nothing emitted on success, and a failing emitter not changing the outcome.

### 8. Failure and boundary behaviour

- A failure while determining whether control applies (workspace-mode query, variable lookup beyond the defaults above, patch registration query) lets the update through and logs a warning. The hub-mode default must not turn a database hiccup into blocked pushes. In `origin` mode the next sync's divergence policy and backup ref are the safety net.
- A failure in the forward itself is a rejection (Requirement 4), never a silent accept.
- A pre-receive hook that panics is not recovered specially. The existing handler behaviour applies.
- A client that does not request `report-status` still has the update rejected by the hub, but gets no per-ref message. Normal git clients always request it.
- The no-op push path (empty request body) does not call the pre-receive hook.
- The `info/refs` advertisement, clone and fetch are unchanged.

### 9. Wiring, documentation and tests

`cmd/af-hub/main.go` registers the carry-patch pre-receive hook with `gitserver.RegisterPreReceiveHook` and extends the post-push hook construction as in Requirement 6. Both use `store.GetVariableValue`, `workspace.ResolveCloneAuth` and the audit emitter that main already builds. Wiring follows the steering rule on library reuse. It uses apikit helpers for timestamps and auth info, and defines no local auth structs.

Documentation, in the same change, per project steering:

- `docs/api.md`: the "Workspace Variables Reference" gains `PUSH_PATCHES_TO_ORIGIN`. The git-server push section gains the three behaviours, the per-ref messages and the accepted-refs-only rule. `docs/api.md` also documents the `hub.patch.mirror_failed` event.
- `docs/openapi.yaml`: the descriptions of the git receive-pack route and the workspace-variable documentation mention the variable and the rejections where those are described today.
- `docs/architecture.md`: the git server section gains the pre-receive hook.
- `docs/carry_patch_workflow.md`: only two edits. The Configuration section gains a `PUSH_PATCHES_TO_ORIGIN` entry, and the note that spec 20 added (hub pushes in `origin` mode are replaced by the next sync) is updated to describe rejection, forwarding and mirroring. The wider restructure belongs to `patch_authority_docs`.
- `docs/configuration.md` does not list workspace variables and is unchanged. `docs/cli.md` is unchanged, because no CLI surface is added.

Tests: gitserver tests use the existing `newGitTestEnv` helpers. Tests that need `origin` use a local bare repository added as the trunk's `origin` remote. go-git's file transport needs the `git` binary on `PATH`, as the existing integration tests already assume.

## Design Decisions

1. **The control sits at the ref-write step of the receive, through the trunk storer wrapper.** Forwarding needs the unpacked commit but must precede the hub's ref write. That is the one point where both hold, and go-git reports a failed ref write as a per-ref status. Checking before `ReceivePack` could reject but could not forward. Checking after it would have to roll back a ref that was already visible.
2. **The hook has a single signature for reject and forward.** The hook returns nil or an error per ref update, and the carry-patch package decides which mode applies. The git server learns nothing about patch branches, authority or origin, which satisfies PU-4 (no `gitserver` → `carrypatch` import).
3. **Control applies to registered patches with a status other than `deleted`, and never to the integration branch.** This matches the issue and spec 20's exclusion (PB-7). The existing post-push hook counts any status, which is left alone.
4. **Deletes are always rejected in `origin` mode.** A hub-side delete would be undone by the next sync (`created`). Forwarding a delete would let a hub client delete a fork branch, which this spec does not allow.
5. **The forward is non-force and the mirror is force.** In `origin` mode the fork is the truth, so the hub must not overwrite it. In `hub` mode the hub is the truth and the fork is a copy, so a rewritten branch must be able to replace it.
6. **The forward pushes from a temporary ref, or a hash source if go-git supports one.** The commit is unreferenced at forward time. A temporary `refs/hub/forward/<branch>` ref is plain go-git push semantics and is cleaned up on every path.
7. **Failures while deciding whether to control a push fail open; forward failures fail closed.** Pushes in the default `hub` mode must not break on a database error. A forward that fails must reject, or the fork and hub would disagree, which is the exact harm the mode prevents.
8. **Side effects count accepted refs only.** The gate would otherwise leave a rejected push triggering rebuilds and audit entries for refs that never changed. The same fix covers pre-existing per-ref failures.
9. **The mirror runs in the post-push goroutine after the rebuild enqueue.** This is what the issue asks for. Enqueue first means a slow or hung fork never delays the rebuild job. The mirror is not tied to `AUTO_REBUILD_AFTER_PUSH`, because that variable controls rebuilds only.
10. **`<git_url>` in the rejection message has its userinfo removed.** The message goes to any client with `git:write`. Credentials in a stored URL must not be echoed.
11. **Error text from origin is passed through, minus URL userinfo.** Operators need the real reason (non-fast-forward, auth), and the client already holds `git:write` on the workspace.
12. **No retry, queue or persistence for the mirror.** It is best effort by definition. The next push of the branch mirrors again, and `hub.patch.mirror_failed` makes failures visible.
13. **120 second timeout for origin pushes.** Pushes are interactive for forwards and background for mirrors. This bounds both, and is generous enough for a large pack on a slow link.
14. **The forward does not take the workspace lock.** The git server never does. Spec 20's compare-and-swap ref writes are what protect a concurrent sync, and holding a lock during a network push would block sync, rebuild and merge.
15. **`refs/remotes/origin/*` is not updated after a forward or mirror.** The next sync's fork fetch brings it up to date. Keeping that single writer means one place owns the tracking refs.

## Dependencies

| Spec | Relationship | Why |
|------|--------------|-----|
| `20_fork_patch_sync` | depends | Defines `PATCH_BRANCH_SOURCE` and its parsing, which this spec reuses. The compare-and-swap sync writes make an unlocked hub push safe. `internal/audit/types.go` gains constants next to its events. The sync note in `docs/carry_patch_workflow.md` and the variable reference in `docs/api.md` are updated here. |
| `21_fork_patch_registration` | depends | A registered patch row is the criterion for control. Registration now finds fork-only branches, so more branches become registered. No code is shared. |
| `01_rebuild_worktree` | depends | The post-push hook still enqueues the rebuild exactly as before, and the follow-up logic is unchanged. |
| `06_git_server` (archived) | modifies | The receive-pack handler, the trunk storer wrapper and the hook registration. |
| `16_carry_patch_operations` (archived) | modifies | `NewPostPushRebuildHook` gains the mirror, and its registration in `main.go` changes. |
| `09_git_credentials` (archived) | depends | `ResolveCloneAuth` for origin credentials. |
| `18_hub_audit_query` (archived) | modifies | New hub event type `hub.patch.mirror_failed`, and `hub.git.push` now lists accepted refs only. |

## Verified External API

`github.com/go-git/go-git/v5` is the only external package used. Its source is outside the repository and could not be read, so every row below is taken from call sites in this repo or from the issue, and marked accordingly.

| Symbol | Signature | Status |
|---|---|---|
| `git.PlainOpen` | `func PlainOpen(path string) (*Repository, error)` (`internal/carrypatch/wire.go`, `internal/gitserver/handlers.go`) | verified by call site |
| `(*Repository).PushContext` | `func (r *Repository) PushContext(ctx context.Context, o *git.PushOptions) error` (`internal/carrypatch/wire.go`) | verified by call site |
| `git.PushOptions` fields `RemoteName`, `RefSpecs []config.RefSpec`, `Auth transport.AuthMethod`, `Force bool` | all four set in `DefaultPushIntegrationFunc` | verified by call site |
| `git.NoErrAlreadyUpToDate` | treated as success in `wire.go` | verified by call site |
| `packp.ReferenceUpdateRequest` with `Commands []*packp.Command`, `Decode`; `packp.Command` fields `Name plumbing.ReferenceName`, `Old`, `New plumbing.Hash` | used in `internal/gitserver/handlers.go` | verified by call site |
| `transport.ReceivePackSession.ReceivePack` | `ReceivePack(ctx, *packp.ReferenceUpdateRequest) (*packp.ReportStatus, error)`, with `rs.Encode(io.Writer)` | verified by call site |
| `packp.ReportStatus` fields `UnpackStatus string` and `CommandStatuses []*packp.CommandStatus` (each with `ReferenceName` and `Status string`, `"ok"` on success) | not used in this repo | **unverified.** Needed to compute accepted refs (Requirement 5). The implementation confirms the field names. |
| go-git's receive-pack records an error from the storer's reference write as that command's status in the report | described in the issue ("go-git's server supports per-command status") | **unverified.** Requirement 2 depends on it, and the first test written must prove it with a real `git push`. |
| Which storer method (`SetReference` or `CheckAndSetReference`) go-git's server calls on a create or update, and `RemoveReference` for a delete | `storer.ReferenceStorer` methods | **unverified.** The wrapper intercepts every reference-writing method, so the choice does not change the design. |
| Push from a hash source (`<sha>:refs/heads/x`) in go-git | not used in this repo | **unverified.** If unsupported, the temporary-ref route of Requirement 4 is used. |

Repository symbols used, read in this repo: `gitserver.RegisterPostPushHook`, `gitserver.SetAuditEmitter`, `WorkspaceLoader.Load`, `thinPackSafeStorer`, `extractPushedBranches`, `emitGitPushAudit`, `carrypatch.NewPostPushRebuildHook`, `carrypatch.GetVariableFunc`, `workspace.ResolveCloneAuth(store *secrets.Store, slug string) (transport.AuthMethod, error)`, `audit.HubEvent`, `audit.Emitter.Emit`, `apikit.GetAuthInfo`.
