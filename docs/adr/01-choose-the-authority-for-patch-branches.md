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

## Implementation notes

The Decision section stands as written. The following errata record where
the delivered implementation diverges from the original proposal:

- [`docs/errata/20_fork_patch_sync_divergences.md`](../errata/20_fork_patch_sync_divergences.md)
- [`docs/errata/21_fork_patch_registration_divergences.md`](../errata/21_fork_patch_registration_divergences.md)
- [`docs/errata/22_fork_push_control_divergences.md`](../errata/22_fork_push_control_divergences.md)
- [`docs/errata/23_patch_divergence_recovery_divergences.md`](../errata/23_patch_divergence_recovery_divergences.md)

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
