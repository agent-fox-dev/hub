# ADR 02: Run rebuilds with the git CLI

- **Status:** Accepted
- **Date:** 2026-10-01

## Context

A carry-patch rebuild applies patch branches onto the upstream base to produce
the integration branch. Branches are refs and need no working tree, but
cherry-pick and merge do. Until now the rebuild borrowed the workspace's single
trunk checkout for them and held the per-workspace lock for the whole run.

The rebuild now applies patches in a detached, per-run `git worktree` at
`<workspace_root>/<slug>/rebuild/<job id>`. The worktree shares the trunk's
objects, refs, configuration and rerere cache but has its own HEAD, index and
files, so the trunk's checkout is never moved and the workspace lock is held
only for the upstream fetch and for the final integration-ref update.

That raises the question of how to implement the worktree flow: keep using the
git CLI through `gitcmd.GitRunner` (as the rebuild does today), or move the
rebuild to go-git, which the hub already uses for fetch, push, the post-push
reset and the sync fast-forward.

The decision rests on what go-git v5.19.1, the version in `go.mod`, supports.
The table below is taken from that release's `COMPATIBILITY.md` (read from the
module source, not from the issue that proposed this change).

| Capability the rebuild needs | git CLI | go-git v5.19.1 (`COMPATIBILITY.md`) |
|------------------------------|---------|--------------------------------------|
| `git worktree add` (create a linked worktree) | yes | `worktree`: not supported (❌). `PlainOpenOptions.EnableDotGitCommonDir` only opens an existing linked worktree; it cannot create one |
| `git worktree remove` | yes | `worktree`: not supported (❌) |
| `git worktree prune` | yes | `worktree`: not supported (❌) |
| `cherry-pick` | yes | not supported (❌) |
| non-FF merge (`merge --no-ff`) | yes | `merge`: partial (⚠️), "Fast-forward only" |
| `rerere` | yes | not listed; no support |
| `gc` / `prune` (and `reflog`, `repack`) | yes | not supported (❌) |
| `update-ref` | yes | listed as not supported (❌) as a command; refs can be written through the `Storer` API |

Of these, a rebuild needs the first four directly. The last two matter for
safety: objects reachable only from a linked worktree's HEAD are protected by
the git CLI's own `gc`, which treats every worktree's HEAD as a root, and the
rebuild never runs `gc --prune=now` or `reflog expire`.

## Decision

1. **The rebuild is implemented with the git CLI, through `gitcmd.GitRunner`.**
   Patch application (`log`, `rev-parse`, cherry-pick, merge, `rerere`, `diff`,
   `commit`, `reset`), worktree management and the final `update-ref` all go
   through the runner, so the existing safety environment, timeouts and
   context handling apply.

2. **Worktree commands sit behind typed methods.** `WorktreeAdd`,
   `WorktreeRemove` and `WorktreePrune` are the only place `git worktree ...` is
   spelled: they exist on `gitcmd.GitRunner`, on the `carrypatch.GitRunner`
   interface, on `GitRunnerAdapter` and on the test mock. A later move to
   go-git touches one adapter, not the executor.

3. **Existing go-git use is neither extended nor removed.** Fetch, push, the
   post-push hard reset, the sync fast-forward and `DefaultPushIntegrationFunc`
   keep using go-git. An integration test exercises each of those paths on the
   trunk while a linked rebuild worktree is registered and asserts they succeed
   and leave the worktree usable.

4. **Revisit condition.** Reconsider this decision when go-git gains all of:
   linked-worktree support (create, remove, prune), `cherry-pick`, `--no-ff`
   merge and `rerere` with an `rr-cache` compatible with the git CLI's (for
   example in go-git v6). Until all four exist, a go-git rebuild would
   re-implement algorithms this design keeps fixed and put `rr-cache`
   compatibility at risk.

## Consequences

**Positive**

- The rebuild keeps its current algorithm, rerere behaviour and conflict
  semantics; only where it runs changes.
- Rerere keeps working across worktrees: `rr-cache` and `rerere.*` configuration
  live in the common git directory, so resolutions recorded in one rebuild
  replay in the next, and the rerere endpoints read the same cache.
- The go-git dependency stays at v5.19.1 and no module is added.
- One adapter spells the worktree commands, so the CLI dependency is contained.

**Negative / costs**

- The rebuild depends on the `git` binary being present and at least the
  version `gitcmd.New` already enforces (2.38), which covers
  `worktree add --detach` and `worktree remove`.
- The hub keeps two git implementations (CLI and go-git) operating on the same
  repository. The coexistence test is the evidence that they do not interfere;
  if it fails, the design has to change.
- A crash can leave a registered worktree directory behind. It is removed
  before the next rebuild of that workspace and once at hub startup, using
  `git worktree prune`.

## Alternatives considered

- **Implement the rebuild with go-git.** Rejected: go-git v5.19.1 cannot create
  linked worktrees, cherry-pick, merge without fast-forward, or run rerere, so
  the rebuild would have to re-implement each of them, changing an algorithm
  this work keeps fixed.
- **Keep applying patches in the trunk checkout and only shorten the lock.**
  Rejected: cherry-pick and merge move the trunk's HEAD, index and files, so
  other operations would see half-applied state.
- **A persistent rebuild worktree.** Rejected: it would carry half-finished
  state between runs and bring back the cleanup problem. A per-run detached
  worktree makes crash recovery "delete the directory".
