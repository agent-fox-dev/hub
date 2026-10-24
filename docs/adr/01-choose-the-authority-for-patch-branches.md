# ADR 01: Choose the authority for patch branches in carry-patch workspaces

- **Status:** Accepted
- **Date:** 2026-09-29
- **Accepted:** 2026-10-04

## Context

> This section describes the code before the decision was implemented.
> The behaviour it documents has since been changed by specs 20 to 23.

A carry-patch workspace has two remotes in its trunk clone: `origin` (the
fork, `git_url`) and `upstream` (the project being tracked, `upstream_url`).
The hub rebuilds an integration branch as "upstream HEAD plus the registered
patch branches in order".

The code and the documentation disagree about where patch branches live.

- The workflow guide's Remotes table describes `origin` as "where patch
  branches live; push target for local work".
- The code never reads a patch branch from `origin`. The initial clone brings
  the fork's branches in only as `refs/remotes/origin/*`; a patch branch is
  usable by the rebuild only once `refs/heads/<name>` exists in the trunk, and
  the only operation that creates that ref is a `git push` to the hub's git
  server. Sync in carry-patch mode fetches `upstream` only. `afc patch add`
  validates with `rev-parse --verify <name>`, which does not resolve a
  remote-tracking ref, so a branch that exists only on the fork cannot be
  registered without `--skip-branch-check`, and the rebuild then skips it as
  `branch_not_found`.
- Patch branches reach the fork in two ways only: a person pushes them there
  by hand, or the workspace is archived, which pushes `refs/heads/*` to
  `origin` before deleting the clone.

So the hub is, in practice, the sole authority for patch branches, and that
authority is undocumented. It has three consequences that teams run into:

1. **Every upstream PR needs a second push.** A patch that is submitted as a
   pull request to upstream must exist on the GitHub fork. The developer
   pushes it to the hub for the rebuild and to the fork for the PR, and
   nothing keeps the two in step.
2. **Branches that already exist on the fork are invisible.** Teams adopting
   the hub for an existing fork have to re-push every branch through the hub.
3. **Patch branches have no copy outside the hub host** until archive. A lost
   data directory loses every unpublished patch branch.

The alternative model, in which the fork is the source of truth and the hub
pulls patch branches from it, fits how teams already work with GitHub: review
happens on pull requests against the fork, upstream PRs are opened from the
same branches, and the hub's copy is a mirror. It also has costs: the hub
needs to fetch `origin` on every sync, it has to define what happens when the
hub's copy and the fork's copy diverge, and the current "resolve conflicts in
the trunk" recovery path stops making sense because the trunk is no longer
where the branch is edited.

Neither model is right for every workspace. A team whose agents push directly
to the hub and never open upstream PRs is well served by hub authority. A team
that reviews on GitHub and upstreams patches wants fork authority.

## Decision

1. **The authority for patch branches is an explicit, per-workspace setting,
   not an accident of implementation.** It is the workspace variable
   `PATCH_BRANCH_SOURCE`, with values `hub` and `origin`.

2. **`hub` is the default and describes today's behaviour.** Patch branches
   are refs in the trunk, written only by pushes to the hub's git server.
   Sync does not touch them. This keeps every existing workspace working
   unchanged.

3. **`origin` makes the fork authoritative.** On every sync the hub fetches
   `origin` and brings each registered patch branch to the fork's tip:
   creating the local branch if it is missing, fast-forwarding it when the
   fork is ahead, and, when the two have diverged, replacing the hub's copy
   with the fork's while keeping the replaced tip under a backup ref. A patch
   branch that changed during sync counts as "advanced" and triggers the
   same auto-rebuild as an upstream advance. In this mode the hub's git
   server does not accept direct pushes to registered patch branches unless
   it can forward them to the fork first, so there is exactly one writer.

4. **Registering a patch resolves the fork's copy in both modes.** `afc patch
   add` accepts a branch that exists only as `refs/remotes/origin/<name>` and
   creates the local branch from it. This removes the `--skip-branch-check`
   workaround regardless of authority.

5. **Mirroring to the fork is a separate opt-in, `PUSH_PATCHES_TO_ORIGIN`.**
   In `hub` mode it pushes a patch branch to `origin` after the hub accepts
   it, for durability and for upstream PRs. In `origin` mode it is the
   forwarding described in point 3.

6. **The recommended configuration for teams that upstream patches is
   `PATCH_BRANCH_SOURCE=origin` with `REBUILD_PUSH_INTEGRATION_BRANCH=true`.**
   The fork then holds every branch the team cares about, the hub is a
   rebuild engine with a mirror, and GitHub is the only place people push.
   The workflow guide will present this as the primary workflow and the hub
   model as the alternative for agent-driven workspaces.

## Consequences

**Positive**

- The workflow guide and the code say the same thing, and the choice is
  visible in the workspace's variables rather than inferred from behaviour.
- Fork-authoritative workspaces need one push per change, to GitHub. Upstream
  PRs, fork PRs and the rebuild all read the same branch.
- Existing forks can be adopted without re-pushing branches through the hub.
- Patch branches on a fork-authoritative workspace survive a lost hub data
  directory; the hub can be recloned and re-synced.
- The mirroring option gives hub-authoritative workspaces the same durability
  without changing who writes.

**Negative / costs**

- Sync in `origin` mode does two fetches, so it takes longer and needs
  working `origin` credentials on every sync, not only at clone time.
- Divergence needs a policy and a recovery path. Replacing the hub's copy is
  the only rule consistent with "authoritative", but it can discard commits
  that were pushed to the hub by mistake; the backup ref is the safety net,
  and it needs documentation and a retention rule.
- The "resolve conflicts in the trunk" instructions in the workflow guide do
  not apply in `origin` mode. Conflict resolution becomes: fix the branch in
  a local clone, push to the fork, sync. Recording manual resolutions into
  the hub's rerere cache is not available in this mode without further work.
- Two more workspace variables to document and reason about. A workspace
  column would be more discoverable; we chose a variable for consistency with
  every other carry-patch setting and because it does not need a schema
  migration or an API change on the workspace object.
- The git server gains a pre-receive decision that depends on workspace
  configuration. That is new coupling between the git server and the
  carry-patch package, in the same style as the existing post-push hook.

## Divergences from the proposals

The Decision section stands as written. The implementation differs from the
proposals it was built from in the following ways, which are the rules the
code follows:

**Sync in `origin` mode.**

- The origin fetch prunes tracking refs whose branch no longer exists on the
  fork. A credential failure answers `502 failed to resolve origin
  credentials`; a fetch failure `502 origin fetch failed` with error type
  `origin_fetch_failed`. A nil origin credential resolver means anonymous
  access (a public fork); a missing fetch function is
  `500 origin fetch is not configured`.
- Patch ref writes are compare-and-swap (`git update-ref <ref> <new> <old>`)
  so a concurrent write is detected. A failed ref write answers
  `500 failed to update patch branch <b>`, a failed ancestry check
  `500 failed to compare patch branch <b> with origin`, and a failed work-tree
  reset after the ref moved `500 failed to reset working tree for patch branch
  <b>`; the moved branch keeps its outcome (persisted, audited, counted as
  advanced). `last_sync_at` is not written when the refresh fails.
- Only a moved `active` or `conflict` patch counts as advanced for the
  rebuild trigger; a moved `disabled` patch does not. Merge detection also
  runs when only patch tips changed. The failure path of the refresh honours
  the `auto_rebuild_after_sync` setting as the success path does.
- The sync runs: fetch origin, resolve the upstream base, detect a
  force-push, refresh the patch branches (not refresh before detection).
- `origin_fetched` is always in the sync response; `patches_synced` and
  `patches_diverged` only in `origin` mode. A patch refreshed and then
  detected as merged in the same sync has its origin columns cleared again
  and appears in `patches_diverged` once if it was diverged.
- `hub.patch.replace` carries the branch as `resource_id` and `replace` as
  action; `hub.patch.sync` carries the slug and `sync`. Replacements are
  logged at info level even without an audit emitter.
- `afc workspace sync --fail-on-diverged` exits 3 through the hub's own exit
  code mapping (`internal/cli/exit.go`), since apikit maps every CLI error
  code but 1 and HTTP codes to 2; stdout holds exactly one JSON document.

**Registration.**

- A branch exists when `refs/heads/<name>` does; a tag, SHA or remote-qualified
  name is not accepted. A fork fetch failure other than a missing branch is
  `502 origin_fetch_failed`; a registration that must write while the lock is
  held is `409 workspace_busy`.
- `branch_resolution` in the audit metadata is `local`, `origin_tracking`,
  `origin_fetch` or `skipped` (`skip_branch_check`). Batch registration emits
  no `hub.patch.create` event; it validates the syntax of every element before
  it resolves any, because resolution has side effects.

**Push control.**

- A delete of a registered patch branch is rejected in `origin` mode in both
  reject and forward modes (`branch is synced from origin; delete it on the
  fork instead`).
- A forward goes through a temporary `refs/hub/forward/<branch>` ref with a
  120 s timeout; error text loses URL userinfo by pattern replacement.
- Only accepted ref updates drive the `hub.git.push` event and the post-push
  hook; `head_sha` is refreshed after every push, rejected refs included.
- The pre-receive control fails open on a database lookup failure (the push
  is allowed and a warning logged); a settings lookup failure resolves to the
  setting's default instead, because an unforwarded `origin`-mode push would
  make the hub and the fork diverge.
- The mirror runs after the post-push rebuild enqueue even when
  `auto_rebuild_after_push` is false. A forward or mirror updates
  `refs/remotes/origin/<branch>`, which go-git writes once the push succeeds;
  harmless and left as is.

**Divergence recovery.**

- The reset fetches the branch from the fork first, writes a backup ref only
  when it discards commits (action `replaced`), enqueues a rebuild when the
  branch moved and the patch is `active` or `conflict`, and applies only to
  `active`, `conflict` or `disabled` patches. It persists the origin sync
  columns only in `origin` mode. It needs a lock function and an origin fetch
  (else `500` before any write); a nil credential resolver means anonymous
  access.
- A failed `git reset --hard` after the branch moved is logged, not an
  error: the ref move is the effect, and a retry would find nothing to do.
- `hub.patch.reset` (and `hub.patch.replace` for `replaced`) is emitted as
  soon as the reset succeeds, before the patch is read back for the response.
- Only `refs/hub/replaced/` is protected from client pushes, by prefix.
- The purge of expired soft-deleted patches and their backup refs exists but
  nothing schedules it; it counts only the rows it deleted, and a patch
  restored while a purge runs loses its backup.

**Adjacent commands.**

- `afc credential set` was removed: upstream credentials are the reserved
  workspace secrets `UPSTREAM_GIT_PAT`, `UPSTREAM_GIT_USERNAME`,
  `UPSTREAM_GIT_PASSWORD` (`GIT_*` for origin), managed with `afc secrets`.
  The server ignores an incomplete username/password pair and falls back to
  the next credential source.
- rerere entries are listed and forgotten by `id` (the rr-cache entry name),
  with `path` known only while a conflict is in progress, `recorded_at` and
  `resolved`; `DELETE .../rerere/<id>` removes the entry, and a path still
  delegates to `git rerere forget`, which works only during a conflict.

## Alternatives considered

- **Keep hub authority and only fix the documentation.** Cheapest. It leaves
  the double-push problem and the adoption problem in place, and leaves patch
  branches with no copy outside the hub until archive. Rejected as the sole
  outcome, but its documentation fix is part of the work either way.
- **Make the fork authoritative for everyone, no setting.** Simplest mental
  model, but it breaks workspaces where agents push to the hub and there is no
  human on GitHub, and it forces an `origin` fetch on every sync for teams
  that do not need it. Rejected.
- **Bidirectional sync with merge on divergence.** Would let both sides be
  written. Divergence resolution on refs that other people are rebasing is
  exactly the class of problem this workflow exists to avoid; two writers
  without a merge policy is worse than one writer. Rejected.
- **Make the fork the hub's storage (bare mirror, no local branches).** The
  hub would rebuild directly from `refs/remotes/origin/*`. It removes the
  duplicate ref namespace but also removes the ability to push to the hub at
  all and makes every existing test and every `refs/heads/<patch>` reference
  in the code wrong. Too large a change for the benefit. Rejected.
- **Report divergence and never replace.** Safer for the hub's copy but the
  mode would then not be authoritative: a diverged branch would stay diverged
  until an operator acts, and rebuilds would keep applying the stale tip.
  Kept as an explicit `PATCH_DIVERGENCE_POLICY=report` option for teams that
  prefer it; not the default.
