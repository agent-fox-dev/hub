# Codebase review, September 2026

A full review of the hub before it is relied on as infrastructure for
agentic services (agent-fox and others). Scope: every package under
`internal/`, `cmd/`, the container build, the Makefile and the docs, with
particular attention to security, the carry-patch workflow, and the parts
that matter for codespaces-style consumers. Findings were verified against
the code and, where git behaviour was involved, against git 2.43 directly.

Everything listed under *Fixed* landed in this branch, in five commits
(deployment blockers, authorization and injection, carry-patch correctness,
audit, git/CLI robustness) plus this documentation commit. Items under
*Recommended follow-ups* were deliberately left out of this pass.

## Summary

| Area | Critical | High | Medium | Low |
|------|----------|------|--------|-----|
| Deployment (container) | 2 | – | 1 | – |
| Security / authorization | – | 4 | 4 | 2 |
| Carry-patch and merge correctness | 3 | 7 | 6 | 5 |
| Audit subsystem | 1 | 4 | 6 | 1 |
| git subprocess handling and CLI | 1 | 6 | 12 | – |

The single most important conclusion: the previous version was **not**
deployable as a carry-patch hub. The runtime image had no `git` binary, the
bundled config was never found, git pushes above 1 MB failed, rebuilds
started from the wrong upstream branch, private upstreams could not be
fetched, and every workspace-scoped endpoint except CRUD was open to any
authenticated user. The early tests were promising because they ran the
binary outside the container against public repositories with a single
user.

## Fixed

### Deployment blockers

- **No git in the runtime image.** `ubi-minimal` ships without git; every
  carry-patch operation and every merge job failed at `gitcmd.New`. The
  Containerfile now installs `git-core` (and `libstdc++` for DuckDB).
- **Config never found.** The image set `XDG_CONFIG_HOME=/config` while the
  bundled config lived at `/config/af-hub/config.toml`. Image, Makefile
  `hub-runc` target, and `docs/configuration.md` now agree on
  `/config/af-hub` and `/data/af-hub`.
- **`git push` capped at 1 MB.** apikit installs its body-size limit
  globally with `e.Use`, so packfiles above `max_body_size` were rejected.
  The git server now stashes the request body in an `e.Pre` middleware for
  `/git/` paths and streams `receive-pack` instead of buffering it. See
  the apikit issue drafts (`docs/apikit_issues.md`, A1) for the upstream
  fix.

### Security

- **Cross-tenant access (high).** sync, reclone, patches, merges, batch
  rebase, rebuilds, rollback, rerere, patch-status, session creation and all
  audit reads checked scopes but not ownership. A shared
  `workspace.AuthorizeWorkspace` now enforces owner-or-admin with 404 for
  everyone else; the unified audit query and SSE stream are restricted to
  the caller's workspaces. Erratum: `docs/errata/ownership_enforcement.md`.
- **Shell execution with the hub's environment (high).** `CHECK_COMMAND`
  (settable with `vars:write`) ran with `os.Environ()`, i.e. with the hub's
  tokens and secrets. It now runs with a minimal base environment plus the
  workspace variables, and the docs state that `vars:write` is effectively
  shell access.
- **git option injection (high).** Ref names from the API were passed to
  git as positional arguments without `--end-of-options`; `--detach`,
  `--exec=...` or `--upload-pack=...` were accepted. Typed helpers now use
  `--end-of-options`/`--`, and branch names are validated with git's
  ref-name rules at every API boundary (merge, rebase, patch add,
  integration branch). URL userinfo is redacted from git errors.
- **Blocked users could still use the git server (medium).** The Basic
  auth path now rejects users with status `blocked`.
- **Admin tokens on user-scoped secrets (medium).** They created phantom
  rows with an empty owner; now 403.
- **Org-tier variables leaked to non-members (medium).** The resolved
  variables endpoint only consults the org tier when the workspace owner is
  a member.
- **Credential helper handed the API key to plain `http`** and ignored the
  port; it now matches scheme and host:port, and honours `ENDPOINT_URL` /
  `API_KEY`.

### Carry-patch and merge correctness

- **Wrong upstream base.** After `git fetch upstream`, `FETCH_HEAD` is the
  alphabetically first branch, not the default branch (verified). Rebuild,
  sync and preview used it. The hub now fetches
  `+refs/heads/*:refs/remotes/upstream/*` and `+HEAD:refs/remotes/upstream/HEAD`
  and resolves the base as `upstream/HEAD`, then `upstream/<branch>`, then
  `FETCH_HEAD`.
- **Unauthenticated upstream fetch.** Carry-patch fetches shelled out to
  git without credentials; private upstreams could never sync. Fetches now
  go through go-git with the resolved upstream credentials (PAT, then
  username/password, then origin credentials).
- **Rerere endpoints were non-functional.** Preimages carry no path, so the
  list was always empty and forget always 404. Entries are now listed by
  id with `resolved`, and forgotten by id. Erratum:
  `docs/errata/16_rerere_entries_by_id.md`.
- **Rebuild left the trunk detached and could never recover from a crash.**
  A stale `_rebuild_temp` or a half-finished cherry-pick made every later
  rebuild fail. The executor now aborts in-progress state, uses
  `checkout -B`, restores the original checkout on every exit path, skips
  empty cherry-picks, and selects patch commits with
  `--right-only --cherry-pick --no-merges`.
- **Merge jobs deleted the checked-out branch**, leaving HEAD dangling and
  the trunk unusable. The handler checks out the target first, restores the
  original checkout, aborts rebases on cancellation, and fetches with the
  job context.
- **No mutual exclusion on the shared trunk.** Rebuild, merge, sync
  fast-forward, push hard-reset, rollback and reclone could interleave. A
  per-workspace lock (`internal/wslock`) is held by jobs and tried by
  handlers (409 `workspace_busy`).
- **Archive lost local-only branches.** Archiving a ready workspace deleted
  the clone without pushing. It now pushes with the workspace credentials
  and keeps the directory if the push fails.
- **Soft-deleted patches collided on `UNIQUE(workspace_slug, position)`**
  during compaction and restore. Deleted rows are parked at a negative
  position; all renumbering goes through a parking step.
- **Sync TOCTOU** on `sync_status`, **auto-rebuild ignored
  `REBUILD_FAIL_MODE`**, **PR-number squash detection matched `(#42)`
  anywhere in a subject** (false positives soft-deleted live patches), and
  **`PATCH status=deleted`** created never-purged rows; all fixed.
- **Check command output was discarded** and a store error during the check
  step was treated as "no check configured"; the merge job now records the
  output and fails closed.
- **Merge list** applied `LIMIT 50` before the workspace filter and
  **progress updates** had no status guard; fixed in the job queue. The
  job-queue goroutine-count tests sampled the process-wide count once and
  failed under load; they now wait for the expected delta.

### Audit subsystem

- **Sessions with metadata could not be read back** (`unsupported Scan
  ... map[string]interface {} into *string`): every create with metadata
  answered 500 and `GET /sessions` failed for everyone once one such row
  existed. Verified with go-duckdb; fixed by decoding the driver value.
- **Hub events had no timestamp** and the unified query compared DuckDB's
  `CAST(TIMESTAMPTZ AS VARCHAR)` (`2026-09-01 13:00:00+00`) with RFC 3339
  strings, so ordering, `since`/`until` and cursors were wrong for mixed
  results. Both sources are now compared as epoch microseconds and rendered
  as RFC 3339 UTC.
- **Client timestamps were not validated** (500 on garbage), the
  `session_id` filter was ignored, retention never aged
  `session_outcomes`/`tool_calls`/`tool_errors`, workspace-less hub events
  were purged as orphans after 30 days, `AF_*` overrides of `0` were
  accepted, and the SSE gauge and audit counter were never updated. All
  fixed.

### git subprocesses and the CLI

- git now runs in its own process group and the group is killed on
  cancellation (a hung credential helper or ssh no longer pins a workspace
  lock); commands without a deadline are bounded to 10 minutes.
- Repository-redirecting and config-injecting variables are stripped from
  the inherited environment; `LC_ALL=C`, `GIT_EDITOR=true` and a default
  committer identity are set. **Without an identity git refuses to commit
  in the container** (`unable to auto-detect email address` when the
  hostname has no domain), which would have broken every rebuild and merge
  in production.
- `afc --wait` (and the new `merge wait` / `rebuild wait`) poll immediately
  and exit non-zero on `failed`/`dead_letter`/`cancelled`; path segments
  are percent-encoded; `secrets`/`vars`/`credential set` accept values from
  stdin; `credential set` requires the username/password pair; `patch
  update` rejects a call without a change.

## Recommended follow-ups (not done here)

1. **Secrets at rest are base64, not encrypted.** PRD 07 declares this a
   non-goal, but a hub that holds upstream PATs for many workspaces is an
   attractive target. Envelope encryption with a key from the environment
   (or a KMS) is a contained change in `internal/secrets`.
2. **Cross-tenant id collisions in audit ingestion.** `INSERT OR IGNORE`
   keys on the client-supplied `id` alone; a colliding id from another
   workspace is silently reported as a duplicate. Scope uniqueness to
   `(workspace, id)` or reject foreign ids.
3. **SSE hardening.** No per-credential connection limit and no write
   timeout on the response; the `run_id` filter is documented as
   unsupported until events carry a run id.
4. **SQLite `SetMaxOpenConns(1)`** (apikit) serialises every request behind
   long transactions such as clone bookkeeping; WAL with a small pool and
   `busy_timeout` would remove a latency cliff under load.
5. **Outbound URL validation.** `git_url`/`upstream_url` accept any https
   host; if the hub ever runs inside a network with internal services,
   add an allow-list.
6. **apikit issues** listed in `docs/apikit_issues.md` could not be filed
   from this session (no write access to `txsvc/apikit`); please file them.

## Optimisations for codespaces and carry-patch consumers

- `REBUILD_PUSH_INTEGRATION_BRANCH=true` pushes the rebuilt integration
  branch to `origin`, so a codespace or CI cloning the GitHub fork gets the
  current `deploy` branch without talking to the hub.
- Carry-patch workspaces fetch upstream at clone time and create the
  integration branch at the upstream base, removing the "first sync and
  rebuild required" limitation.
- The trunk is left on the branch it was on before a rebuild or merge, so
  clones taken from the hub are no longer detached.
- `afc rebuild wait` / `afc merge wait` and non-zero exit codes make the
  CLI usable from scripts; `--from-stdin` keeps tokens out of shell history.

## How the review was done

Own reading of every package, plus four focused audits (audit subsystem,
gitcmd/CLI, merge/jobqueue, secrets) whose findings were re-verified before
being acted on. git behaviour (FETCH_HEAD selection, rerere cache layout,
empty cherry-pick state, `checkout` option parsing, `rev-parse` without
`--verify`) and DuckDB behaviour (JSON column scanning, TIMESTAMPTZ casts,
`epoch_us`, `TRY_CAST`) were verified with throwaway probes. All changes
are covered by new regression tests (`*_test.go` files named `review_*`,
`ownership_test.go`, `refname_test.go`, `soft_delete_positions_test.go`,
`rerere_entries_test.go`, `worktree_test.go`, `body_limit_test.go`,
`credential_helper_test.go`), and `make check` passes.
