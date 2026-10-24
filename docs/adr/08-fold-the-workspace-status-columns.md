# ADR 08: Fold the workspace status columns

- **Status:** Accepted
- **Date:** 2026-10-24

## Context

A workspace row carried four state columns: `status` (`active` or
`archived`), `clone_status` (`pending`, `cloning`, `ready`, `failed`,
`archived`), `sync_status` (`idle`, `syncing`, `error`) and `sync_mode`
(`pull_only` or `disabled`). Every handler that needed a usable workspace
checked two of them (`status == active && clone_status == ready`), every
reader of the row scanned all four, the API showed four fields whose
combinations mostly could not occur (`archived` was in two of them), and the
hub kept a state machine for `sync_status` whose only job was to refuse a
second sync and to recover from a crash during one. `sync_mode` was a two
value enumeration standing in for a boolean, beside the settings object that
ADR 07 introduced for exactly such switches.

## Decision

1. **One `status` column:** `pending`, `cloning`, `ready`, `failed` or
   `archived`, with a CHECK constraint. `active` is gone: a workspace that is
   not archived is in one of the clone states, and "usable" is `ready`. The
   handlers that needed an active and ready workspace (push, sync, merge,
   rebuild, patch reset, the git server) check `status == ready`; those that
   only refused an archived one (patch add and restore, forge writes,
   sandbox leases, bound token mints) check `status != archived`.

2. **A sync in progress is not stored.** The sync handler already held the
   workspace lock for its duration; the lock refuses the second sync (`409`
   `workspace_busy`) and nothing is left behind by a crash. `sync_error`
   records a failed sync and the next successful sync clears it;
   `last_sync_at` and `upstream_head_sha` are unchanged. The restart
   reconciliation of stuck syncs goes with the state.

3. **`sync_mode` is the `sync_enabled` setting**, a boolean in the settings
   object with the default `true`. The `--sync-mode` flag of
   `afc workspace create` goes; `--setting sync_enabled=false` replaces it.
   A PATCH body that still names `sync_mode` is refused with a message that
   names the setting.

4. **The table is rebuilt once at start.** SQLite cannot change a CHECK
   constraint, so `InitSchema` detects the `clone_status` column, creates
   the table in its new shape, copies the rows (`status` takes the clone
   state, `archived` stays, `sync_mode = disabled` becomes the setting, a
   sync left `syncing` becomes the `sync_error` the reconciliation would
   have written) and swaps the tables in one transaction.

## Consequences

- The workspace object loses `clone_status`, `sync_status` and `sync_mode`;
  its `status` now says what the four columns said together. Clients that
  polled `clone_status` poll `status`; the CLI's `--wait` does.
- The carry-patch module's `workspaceRow`, the git server's resolver, the
  access record of `wsaccess` and the patch-status response each lose a
  field and a column read.
- A sync that fails leaves the workspace `ready` with a `sync_error`, as
  before in effect: the old `error` sync state never blocked anything but
  reading the error.
