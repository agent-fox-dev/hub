# Errata: 16 rerere entries are listed and forgotten by id, not by path

## Spec Expectation

Spec 16 describes `GET /api/v1/workspaces/:slug/rerere` as returning
`{path, recorded_at}` per resolution and
`DELETE /api/v1/workspaces/:slug/rerere/*pathspec` as running
`git rerere forget <pathspec>`.

## Implementation Reality

git keys `rr-cache` entries by a hash of the conflict hunks. The mapping from
entry to file path exists only in `.git/MERGE_RR` while a conflict is in
progress; after the rebuild finishes (or aborts) nothing in the repository
records which path an entry belonged to. The original implementation derived
`path` from the preimage content, which never contains it, so every entry was
listed with `path: null`, and `git rerere forget <path>` outside of a conflict
always failed with 404. The endpoints were unusable for housekeeping.

## Resolution

- The list response now carries `id` (the rr-cache entry name), `path`
  (from `MERGE_RR` when a conflict is in progress, otherwise null),
  `recorded_at`, and `resolved` (whether a `postimage` exists).
- `DELETE .../rerere/<id>` removes the entry directory. A path is still
  accepted and delegates to `git rerere forget`, which works only during a
  conflict.
- `afc rerere forget` accepts either form.

See `docs/api.md` (Rerere Management Endpoints) and `docs/cli.md`.
