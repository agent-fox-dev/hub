---
spec_id: "04"
spec_name: "patch_branch_push_protection"
title: "Push Protection and Mirroring for Fork-Authoritative Patch Branches"
status: "active"
created_at: "2026-09-30T10:20:14.899402Z"
updated_at: "2026-09-30T10:20:14.899402Z"
intent_hash: "ac88ebdd47f8858c8f825a950dc784d86acc58733494abedb4a8d494f90222f2"
schema_version: 2
source: "docs/prd/17-sync-patch-branches-from-the-fork.md"
---
## Intent

A carry-patch workspace whose `PATCH_BRANCH_SOURCE` is `origin` (delivered by
`02_fork_authoritative_sync`) treats the fork as the single writer for every
registered patch branch. That guarantee is only real if the hub's own git
server enforces it: today `git push` to the hub's git server
(`internal/gitserver`) accepts a write to any branch unconditionally, which
would let a push through the hub silently diverge from the fork the next
sync then discards. This spec makes the hub's git server participate in the
authority model `PATCH_BRANCH_SOURCE` declares — refusing or forwarding a
direct push to a registered patch branch in fork-authoritative mode, and
offering an opt-in best-effort mirror of accepted pushes to the fork in
hub-authoritative mode — so that "the fork is the single writer" and "the
hub is the single writer, mirrored for durability" are both enforced where
writes actually happen, not just described in the workflow guide.

## Goals

- A workspace variable, `PUSH_PATCHES_TO_ORIGIN` (default off, enabled only
  by the exact string `"true"`), controls whether the hub's git server
  forwards or mirrors patch-branch pushes to the fork; its effect depends on
  `PATCH_BRANCH_SOURCE`.
- When `PATCH_BRANCH_SOURCE=origin` and `PUSH_PATCHES_TO_ORIGIN` is not
  `"true"`, a direct push to a registered patch branch through the hub's git
  server is refused, per ref, with a message naming the fork as the place to
  push instead; every other ref in the same push (including the integration
  branch and unregistered branches) is unaffected.
- When `PATCH_BRANCH_SOURCE=origin` and `PUSH_PATCHES_TO_ORIGIN="true"`, a
  push to a registered patch branch is forwarded to the fork before being
  accepted locally; the local branch only moves if the forward succeeds, so
  the hub and the fork never disagree because of a hub push.
- When `PATCH_BRANCH_SOURCE=hub` and `PUSH_PATCHES_TO_ORIGIN="true"`, an
  accepted push to a registered patch branch is mirrored to the fork with a
  best-effort, asynchronous force push; mirror failures are logged and
  audited but never fail the push or the auto-rebuild it may have triggered.
- The rejection/forward decision is implemented without `internal/gitserver`
  importing carry-patch code, following the existing post-push hook's
  pattern of package-level hook registration.
- Standard (non `carry_patch`) workspaces, unregistered branches, and the
  integration branch are completely unaffected in every mode.
- `docs/carry_patch_workflow.md` and `docs/examples/AGENTS_carry_patch.md`
  are updated so an operator or an agent knows, before pushing, whether the
  hub or the fork is the place to push a patch branch.

## Non-goals

- **The `PATCH_BRANCH_SOURCE` and `PATCH_DIVERGENCE_POLICY` variables, the
  sync-time fork fetch, and bringing a patch branch to the fork's tip during
  sync.** Delivered by `02_fork_authoritative_sync`, which this spec depends
  on and does not re-specify; this spec only reads `PATCH_BRANCH_SOURCE`.
- **Resolving a fork-only branch at patch registration time** (the
  `refs/remotes/origin/<name>` tracking-ref check and single-branch fetch in
  `POST /workspaces/:slug/patches`). That is `03_patch_registration_from_fork`
  and is unrelated to what happens when a branch is pushed through the git
  server.
- **`POST /workspaces/:slug/patches/:id/reset-to-origin`, `afc patch
  reset-to-origin`, `replaced_sha`, `refs/hub/replaced/*` advertisement,
  push-rejection and retention, `--fail-on-diverged`, and rewriting the
  workflow guide's Getting Started walkthrough and Remotes table to present
  the fork-authoritative flow as primary.** All `patch_divergence_recovery`.
  This spec does not write or read `refs/hub/replaced/*`; a rejected or
  forwarded push here never discards a commit that needs a backup ref — it
  either never touches the local ref (reject) or only moves it after the
  fork already has the new content (forward).
- **Guarding branch deletion.** Only non-delete updates (a push whose new
  SHA is not the zero hash) to a registered patch branch are examined. A
  push that deletes a registered patch branch is accepted exactly as today,
  in both modes; the input this spec implements describes forwarding and
  refusing "updates," never mentions deleting a branch through this path,
  and forwarding a delete to the fork is a strictly more destructive
  operation than forwarding a content change. Left as a follow-up (see
  Design Decisions).
- **Standard (non `carry_patch`) workspaces.** `PATCH_BRANCH_SOURCE` and
  `PUSH_PATCHES_TO_ORIGIN` are only consulted for `carry_patch` workspaces,
  mirroring every other carry-patch-only variable.
- **Any new audit event for a plain rejection (PU-1) or a successful/failed
  forward (PU-2).** Only the mirror failure (hub-mode, PU-3) gets an audit
  event; rejections and forward failures are logged, not audited, matching
  the input literally.
- **Changing git:read/git:write PAT scopes or the authorization model.**
  `requireGitScope` and `hasGitScope` (`internal/gitserver/handlers.go`) are
  unaffected; this spec adds a business-rule rejection after authorization
  already passed, not a new permission.
- **Recording manually resolved conflicts into the hub's rerere cache from a
  fork push.** Unrelated to push protection; still an open question at the
  top level (ADR 01, `docs/adr/01-choose-the-authority-for-patch-branches.md`).
- **Rewriting `docs/api.md` or `docs/openapi.yaml`.** Nothing here changes
  an HTTP JSON response or adds an endpoint; the new behaviour is entirely
  in the git smart-HTTP protocol response to `git push`, and the new
  variable is documented in the workflow guide's existing Configuration
  section, the same place `AUTO_REBUILD_AFTER_PUSH` already lives.

## Background

`internal/gitserver/handlers.go`'s `handleReceivePack` creates a go-git
`server.ReceivePackSession` against the storer `WorkspaceLoader.Load`
returns (`internal/gitserver/resolver.go`, `gitserver.go`), decodes the
client's `packp.ReferenceUpdateRequest`, calls `sess.ReceivePack(ctx, req)`,
streams the resulting `*packp.ReportStatus` back to the client, updates
`head_sha`, emits a `hub.git.push` audit event
(`internal/gitserver/audit_emit.go`) covering every command in the request,
and — if a `PostPushHookFunc` is registered — calls it asynchronously
(`go postPushHook(db, slug, branches)`) with the branch names the push
updated (`extractPushedBranches`, which already skips deletes). Every push
that reaches `sess.ReceivePack` today is accepted or rejected as a whole;
there is no per-ref decision point. `internal/carrypatch/push_hook.go`'s
`NewPostPushRebuildHook` is the one hook registered today
(`cmd/af-hub/main.go`): it checks `workspace_mode='carry_patch'`, whether any
pushed branch matches a row in `patches`, and `AUTO_REBUILD_AFTER_PUSH`
before enqueuing a rebuild.

`resolver.go`'s `WorkspaceLoader.Load` already wraps the trunk's real
`storer.Storer` in `thinPackSafeStorer` for an unrelated reason (forcing the
delta-resolving unpack path); it is the existing, proven seam for making the
storer this session writes through behave differently from a plain
`git.PlainOpen(...).Storer`, and is the natural place to add a second
wrapper that intercepts a reference write instead of just hiding an
interface.

Fork credentials are resolved the same way everywhere in this codebase:
`workspace.ResolveCloneAuth` (`internal/workspace/clone_auth.go`) — `GIT_PAT`,
then `GIT_USERNAME`/`GIT_PASSWORD`, then no auth — used by the initial clone,
by `carrypatch.DefaultPushIntegrationFunc`'s `REBUILD_PUSH_INTEGRATION_BRANCH`
push (`internal/carrypatch/wire.go`, `git.PushOptions{RemoteName, RefSpecs,
Auth, Force}` via `repo.PushContext`), and — once `02_fork_authoritative_sync`
lands — by carry-patch sync's origin fetch. This spec's forward (non-force)
and mirror (force) pushes reuse the identical `git.PushOptions` shape against
`RemoteName: "origin"`.

`docs/adr/01-choose-the-authority-for-patch-branches.md` (status `Proposed`)
already names this exact behaviour as part of the decision it records:
"In this mode the hub's git server does not accept direct pushes to
registered patch branches unless it can forward them to the fork first...
Mirroring to the fork is a separate opt-in, `PUSH_PATCHES_TO_ORIGIN`" and,
in Consequences, "The git server gains a pre-receive decision that depends
on workspace configuration. That is new coupling between the git server and
the carry-patch package, in the same style as the existing post-push hook" —
this spec is that coupling.

`docs/carry_patch_workflow.md`'s Configuration section documents each
workspace variable in its own subsection (`REBUILD_STRATEGY`,
`AUTO_REBUILD_AFTER_PUSH`, etc.) and its "How it works" section has an
"Auto-rebuild on push" subsection describing the existing post-push hook;
both are the model for this spec's documentation additions.
`docs/examples/AGENTS_carry_patch.md`'s Step 4 ("Push to the Hub") currently
tells every agent, unconditionally, to `git push origin patch/<name>`
against the hub's git server — correct only in `hub` mode, and exactly the
instruction that would earn an agent a rejected push once this spec ships
for an `origin`-mode workspace.

## Requirements

1. **`PUSH_PATCHES_TO_ORIGIN` workspace variable.** Read via
   `GetVariableFunc("workspace", slug, "PUSH_PATCHES_TO_ORIGIN")`. Only the
   exact string `"true"` enables it; unset, empty, or any other value means
   disabled. Read fresh for every push, never cached, exactly like
   `PATCH_BRANCH_SOURCE` and every other carry-patch on/off variable. A
   store-read error is treated as disabled (same fail-open convention used
   for every other carry-patch variable in this codebase).

2. **What is guarded.** For a `carry_patch` workspace, a ref update in an
   incoming push is a candidate for requirements 3–5 only when: the ref is
   `refs/heads/<name>`; `<name>` is not the workspace's `integration_branch`
   (never a candidate, even if a `patches` row happens to share that name);
   `<name>` matches the `branch_name` of a row in `patches` whose `status`
   is anything other than `deleted` (`active`, `conflict`, `disabled`, or
   `merged_upstream` are all still guarded — wider than the `active`/
   `conflict`/`disabled` set `02_fork_authoritative_sync`'s fork-refresh
   touches, because a patch record that exists and is not yet purged still
   needs one writer even after it is soft-deleted); and the update is not a
   delete (`New` is not the zero hash — see Non-goals). Standard workspaces,
   unregistered branches, tag refs, and any other non-`refs/heads/*` ref are
   never candidates and pay no cost — no variable lookup, no query — beyond
   what the push already does today.

3. **Refusal in fork-authoritative mode.** When `PATCH_BRANCH_SOURCE=origin`
   and `PUSH_PATCHES_TO_ORIGIN` is not `"true"`, every candidate ref from
   requirement 2 is rejected with the per-ref message `branch is synced from
   origin; push to <git_url> instead`, where `<git_url>` is the workspace's
   own `git_url` column. The client sees this as an `ng` status for that ref
   in the same report-status response that has `ok` for every other ref in
   the push; the packfile is still unpacked and every non-rejected ref is
   still written normally. The rejected branch's ref is left completely
   unchanged — no partial write, no backup ref (there is nothing to back up:
   the hub's copy never changed).

4. **Forwarding in fork-authoritative mode.** When `PATCH_BRANCH_SOURCE=origin`
   and `PUSH_PATCHES_TO_ORIGIN="true"`, each candidate ref from requirement 2
   is, before its local ref is written: pushed to `origin` with a non-force
   push of `refs/heads/<name>:refs/heads/<name>` using the workspace's fork
   credentials (`workspace.ResolveCloneAuth`), attempted only while the
   per-workspace lock (`wslock.TryLock`, the same lock sync/reclone/rebuild
   already use) is held for that one ref. If the lock cannot be acquired,
   that ref is rejected with `workspace busy; try again` and no forward is
   attempted. If the lock is acquired and the forward succeeds, the local
   ref is written to the new SHA as part of the same push, exactly as if no
   guard existed, and the lock is released. If the forward fails
   (non-fast-forward, auth, network, or any other error from the push
   attempt), the local ref is left unchanged and the candidate is rejected
   with the per-ref message `push to origin rejected: <error>`, using the
   forward attempt's own error text, so the pushing developer can tell a
   stale local copy (fetch first) from a credentials or network problem
   without guessing. Every other ref in the same push is unaffected by a
   forward failure or a busy lock on one ref.

5. **Mirroring in hub-authoritative mode.** When `PATCH_BRANCH_SOURCE=hub`
   and `PUSH_PATCHES_TO_ORIGIN="true"`, the push is accepted exactly as
   today (no pre-receive guard runs in this mode), and — in the same
   goroutine that already runs the existing post-push hook, after it has
   run — every branch the push actually updated (requirement 8) that is
   also a registered, non-deleted, non-integration patch branch is
   force-pushed to `origin` (`refs/heads/<name>:refs/heads/<name>`, `Force:
   true`) using the workspace's fork credentials, attempted while the
   per-workspace lock is held via a non-blocking `TryLock`; if the lock is
   busy, the mirror for that branch is skipped and logged, mirroring the
   existing precedent in `updateHeadSHA`'s worktree reset ("workspace busy;
   skipping..."). A mirror failure (lock busy, forward push failure) is
   logged and additionally emitted as a `hub.patch.mirror_failed` audit
   event (`EventType: "hub.patch.mirror_failed"`, `ResourceType: "patch"`,
   `ActorType: "system"`, `Workspace: slug`, `Metadata: {"branch_name",
   "error"}`); it never fails the push (already accepted) and never
   prevents or delays the `AUTO_REBUILD_AFTER_PUSH` rebuild.

6. **The guard does not import carry-patch code into `internal/gitserver`.**
   The pre-receive decision (requirements 3–4) and the mirror (requirement
   5) are supplied to `internal/gitserver` as functions registered from
   `cmd/af-hub/main.go`, in the same style `RegisterPostPushHook` already
   uses: `internal/gitserver` knows only "call this function with the
   workspace's db, slug, trunk repo path, and the candidate ref updates; it
   returns a decision per branch name," never anything about `PATCH_BRANCH_SOURCE`,
   `PUSH_PATCHES_TO_ORIGIN`, or the `patches` table. The production
   implementation of that function lives in `internal/carrypatch` and reads
   the workspace row, the variable store, and the `patches` table itself,
   the same division of responsibility `NewPostPushRebuildHook` already has.

7. **Standard workspaces and non-branch refs are untouched.** A workspace
   whose `workspace_mode` is not `carry_patch` never has any candidate ref
   under requirement 2, so its pushes are byte-for-byte identical to before
   this spec, including their timing (no extra lookup is even attempted).
   The same is true for any push to a `carry_patch` workspace that touches
   no registered patch branch.

8. **Auto-rebuild and audit correctness.** `AUTO_REBUILD_AFTER_PUSH`'s
   existing check (`postPushRebuildHook`) and the new mirror (requirement 5)
   must only consider branches whose ref was actually written by this push —
   a branch rejected under requirement 3 or 4 must not trigger a rebuild or
   a mirror of content the hub never accepted. `extractPushedBranches` (or
   its replacement) is adjusted so the list of "branches this push updated,"
   passed to both the post-push hook and the `hub.git.push` audit event's
   `refs_updated`/`head_sha` metadata, reflects only the refs that were
   actually written — not every ref the client attempted — now that a single
   push can be partially accepted. This corrects `hub.git.push`'s existing
   metadata as a necessary side effect of adding partial acceptance; no
   other field of that event changes.

9. **Logging.** A rejection under requirement 3 is logged at info level with
   the workspace slug, the branch name, and the pushing user (`apikit
   .GetAuthInfo(c).UserID`, the same source `emitGitPushAudit` already
   uses). A forward failure under requirement 4 is logged at warn level with
   the same fields plus the forward error. Neither logs a full stack trace
   or the packfile contents — just the fields needed to act on the
   rejection.

10. **Documentation.** `docs/carry_patch_workflow.md`'s Configuration
    section gains a `PUSH_PATCHES_TO_ORIGIN` subsection (modelled on the
    existing `AUTO_REBUILD_AFTER_PUSH` one) describing both meanings — reject
    unless forwarded (`origin` mode) and best-effort mirror (`hub` mode) —
    and the "How it works" section's "Auto-rebuild on push" area gains a
    short description of the pre-receive refusal/forward and the mirror,
    including the exact rejection message text. `docs/examples
    /AGENTS_carry_patch.md`'s Step 4 ("Push to the Hub") is rewritten to
    tell the agent to check `PATCH_BRANCH_SOURCE` (e.g. `afc vars list
    --workspace <slug>` or equivalent) before pushing: in `hub` mode, push
    to the hub's git server as today; in `origin` mode, push the patch
    branch directly to the fork's own URL (not the hub's git server) unless
    the workspace has `PUSH_PATCHES_TO_ORIGIN=true`, in which case pushing
    to the hub still works but is forwarded. No change to `docs/api.md`,
    `docs/openapi.yaml`, `docs/cli.md`, or `docs/configuration.md` — see
    Non-goals.

## Design Decisions

1. **Per-ref interception at the storer, not by splitting the command list
   before calling go-git's `ReceivePack`.** Requirement 4 needs the forward
   to complete, and possibly fail, strictly before that one ref's local
   write — but the packfile must still be unpacked (so the forward has
   objects to push) regardless of the outcome for any single ref. Wrapping
   the `storer.Storer` returned by `WorkspaceLoader.Load` (already done once,
   for `thinPackSafeStorer`) with a second layer that intercepts the
   reference-write call for exactly the candidate refs, running the guard
   function synchronously inside that call and only delegating to the real
   write on success, satisfies both constraints without touching how
   `handleReceivePack` invokes `sess.ReceivePack`. This assumes go-git's
   server reports a per-command failure from an individual reference write
   as that command's own status in the final `ReportStatus`, rather than
   failing the whole session — asserted by the ADR this spec implements and
   consistent with the git wire protocol's report-status capabilities, but
   not independently confirmed against the installed `go-git/v5` source
   (see Verified External API). Flagged for implementation-time
   confirmation.
2. **A new hook registration, shaped like `RegisterPostPushHook`.** Keeps
   the existing "gitserver knows nothing about carry-patch" boundary
   (requirement 6) and follows a pattern already reviewed and shipped once.
3. **Only `refs/heads/*`, non-delete candidates are examined (requirement
   2).** Tags and other namespaces were never in scope; excluding deletes
   keeps this spec from having to define a "forward a branch deletion to
   the fork" behaviour the input never asked for, and keeps the blast
   radius of a wrong guess small — a future spec can add delete handling
   without revisiting the accept/reject/forward machinery here.
4. **Widen the guarded status set beyond `active`/`conflict`/`disabled` to
   include `merged_upstream`.** `02_fork_authoritative_sync`'s fork-refresh
   only touches branches the rebuild still applies; this guard's job is
   "does the hub have exactly one writer for this branch," which is still
   true for a patch waiting to be soft-deleted after its next successful
   rebuild. Only `deleted` (soft-deleted, ref no longer meaningfully
   tracked) opts a branch out, matching the input's literal wording ("any
   status other than `deleted`").
5. **`workspace busy; try again` for a lock conflict during forwarding,
   not the JSON API's `workspace_busy` error code.** This is a git
   protocol response read by a human or a git client, not a JSON body a
   program parses; a short, self-explanatory string in the same style as
   the other per-ref rejection messages is more useful here than a machine
   code with no schema to put it in.
6. **`hub.patch.mirror_failed` is the only new audit event.** The input's
   own AU-2 asks only for logs on the reject and forward paths, and
   explicitly asks for an audit event on mirror failure; adding audit
   events the input did not ask for would grow the audit schema without a
   stated consumer.
7. **Correcting `hub.git.push`'s metadata to reflect only accepted refs is
   in scope, not a separate fix.** Before this spec, every push was
   accepted or rejected as a whole, so "every command in the request" and
   "every ref actually written" were the same list; this spec is what makes
   them diverge, so leaving the audit event as-is would make it silently
   wrong the first time a ref is rejected under requirement 3 or 4.
8. **Excluding branch deletion from the guard is a real, flaggable choice,
   not an oversight.** A reviewer who wants "one writer" enforced even for
   deleting a patch branch should treat this as a follow-up rather than an
   assumption already settled — see Non-goals.

## Dependencies

| Spec | Reason |
|------|--------|
| `02_fork_authoritative_sync` | Delivers the `PATCH_BRANCH_SOURCE` workspace variable this spec reads to decide whether a push is guarded at all (requirements 3–5), and establishes the fork-credential and go-git push/fetch conventions (`workspace.ResolveCloneAuth`, the `git.PushOptions{RemoteName, RefSpecs, Auth, Force}` shape) this spec's forward and mirror pushes reuse rather than re-deriving. |

## Verified External API

`github.com/go-git/go-git/v5` (`v5.19.1`, per `go.mod`). Symbols already
exercised by existing, tested code in this repository:

- `git.PlainOpen(path string) (*git.Repository, error)`,
  `(*git.Repository).Remote(name string) (*git.Remote, error)`,
  `(*git.Remote).PushContext(ctx, *git.PushOptions) error` with
  `git.PushOptions{RemoteName string, RefSpecs []config.RefSpec, Auth
  transport.AuthMethod, Force bool}` and `config.RefSpec(string)` — verified
  in `internal/carrypatch/wire.go`'s `DefaultPushIntegrationFunc`. This
  spec's forward (requirement 4, `Force: false`) and mirror (requirement 5,
  `Force: true`) pushes use the identical shape against `RemoteName:
  "origin"`.
- `errors.Is(err, git.NoErrAlreadyUpToDate)` to treat an up-to-date push/fetch
  as success — same file.
- `packp.Command{Name plumbing.ReferenceName, Old, New plumbing.Hash}` and
  `packp.ReferenceUpdateRequest.Commands []*packp.Command` — verified in
  `internal/gitserver/handlers.go`'s `extractPushedBranches` and
  `handleReceivePack`.
- `server.Session.ReceivePack(ctx context.Context, req
  *packp.ReferenceUpdateRequest) (*packp.ReportStatus, error)` and
  `(*packp.ReportStatus).Encode(io.Writer) error` — verified by the existing,
  working call site in `handleReceivePack` (`rs, err := sess.ReceivePack(...)`,
  `rs.Encode(c.Response())`).
- `wslock.TryLock(slug string) (unlock func(), ok bool)` — verified in
  `internal/gitserver/handlers.go`'s `updateHeadSHA`, the model for this
  spec's non-blocking lock use in requirements 4 and 5.

**Not independently verified** (the installed module source under
`go-git/v5/plumbing/transport/server` and
`go-git/v5/plumbing/storer` was not reachable with the tools available for
this PRD): the exact method set of `storer.Storer` /
`storer.ReferenceStorer` (assumed to include something equivalent to
`SetReference(*plumbing.Reference) error` and/or
`CheckAndSetReference(new, old *plumbing.Reference) error`, per go-git's
public API as of this major version), and whether
`transport/server`'s `ReceivePack` implementation genuinely reports a
storer error for one command as that command's own failed status in the
returned `*packp.ReportStatus` rather than failing the whole call. Design
Decision 1 depends on this; an implementer must confirm both against the
vendored source before relying on them, and adjust the interception point
(for example, splitting the command list instead of wrapping the storer) if
the assumption does not hold.

## Open Questions

- **Should branch deletion through the hub's git server be guarded the same
  as a content update?** Decision: no — deletions are excluded from the
  guard entirely in this spec (Non-goals, Design Decision 8). Flagged
  because "one writer" is arguably incomplete if a hub-side delete of a
  registered patch branch is still unguarded in `origin` mode; the input
  never mentions this case, and forwarding a delete to the fork is
  materially more destructive than forwarding a content change, so treating
  it as a deliberate follow-up rather than assuming an answer seemed safer
  than guessing either "guard it like anything else" or "force-push a
  fork-side delete."
- **Does go-git's `transport/server` actually support the per-command
  partial-acceptance this spec's whole mechanism depends on?** Decision:
  assumed yes, per the ADR and the git wire protocol's report-status
  capability, implemented via a storer wrapper (Design Decision 1). Flagged
  because it could not be confirmed by reading the installed `go-git/v5`
  source with the tools available while writing this PRD; if the assumption
  is wrong, the interception point needs to move (e.g., to splitting
  `req.Commands` before calling `sess.ReceivePack` and manually merging two
  `ReportStatus` results), which is a different, larger implementation than
  the one this spec's Design Decisions describe, though the observable
  behaviour in Requirements 2–5 would not need to change.
