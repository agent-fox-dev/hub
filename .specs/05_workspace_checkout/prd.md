---
spec_id: '05'
spec_name: workspace_checkout
title: Workspace Checkout
status: draft
created_at: '2026-07-27T14:37:27.974707+00:00'
updated_at: '2026-07-27T14:37:27.974707+00:00'
owner: ''
source: docs/prd/prd4.md
schema_version: 1
---
# Workspace Code Checkout

## Intent

When a workspace is created via `afc workspace create`, the system clones the upstream git repository into a local directory so that agents and tools can operate on the code. The clone happens asynchronously via a simple in-memory job queue — workspace creation returns immediately with the metadata record, and the clone proceeds in the background.

This spec also extends the workspace lifecycle: archiving a workspace pushes any local commits to the upstream remote, records the HEAD revision SHA, and deletes the local clone to reclaim disk space. Reactivating an archived workspace re-clones the code. Deleting an archived workspace removes any remaining local files along with the database record.

## Goals

- Extend apikit's configuration with a `[workspace]` section defining the root directory for workspace code storage.
- Clone the upstream git repository into a `trunk` subdirectory within each workspace's directory on creation.
- Implement a simple in-memory job queue for asynchronous clone operations.
- Track clone status and HEAD revision SHA on workspace records.
- Extend archive lifecycle: push local changes to upstream, record revision, delete local files.
- Extend reactivate lifecycle: re-clone from upstream at the stored git URL and branch.
- Extend delete lifecycle: clean up any remaining workspace directory.

## Non-goals

- **Internal git server.** Running a git server inside hub with push/pull capability for git clients, authenticated via hub-managed credentials, is deferred to a separate spec.
- **Authentication for upstream private repos.** Credential management for cloning private repositories is future work. This spec assumes the hub process has network access to the upstream repo (public repos or pre-configured system-level git credentials).
- **Full (deep) clone.** All clones are shallow (depth 1) to minimize disk usage and clone time. Deep clone support is future work.
- **Disk quota management.** No limits on total workspace storage.
- **Clone retry.** If a clone fails, the user deletes and recreates the workspace. A dedicated retry command is future work.
- **Persisted job queue.** Jobs are in-memory only. If the server restarts, workspaces stuck in `pending` or `cloning` must be manually deleted and recreated.

## Functional Requirements

### Configuration

A new `[workspace]` section is added to apikit's `Config` struct and `config.toml`:

```toml
[workspace]
path = "./workspace"
workers = 4
```

Path resolution follows the same logic as the database path (`resolveDataPath` in apikit's config package):

1. If `path` contains a directory component (e.g. `./workspace`, `/data/workspaces`), it is used as-is.
2. If `path` is a bare name (e.g. `workspace`) and `XDG_DATA_HOME` is set, it resolves to `$XDG_DATA_HOME/workspace`.
3. If `path` is empty and `XDG_DATA_HOME` is set, it defaults to `$XDG_DATA_HOME/workspaces`.
4. If `path` is empty and `XDG_DATA_HOME` is not set, it defaults to `./data/workspaces`.

The resolved path is the **WORKSPACE_ROOT** — the parent directory for all workspace subdirectories. The directory is created on server boot if it does not exist. If the directory cannot be created (e.g., insufficient permissions), the server exits with a fatal error — workspace operations are a core function and cannot be deferred.

apikit's `Config` struct gains a new field:

```go
type WorkspaceConfig struct {
    Path    string `toml:"path"`
    Workers int    `toml:"workers"`
}
```

```go
type Config struct {
    Server    ServerConfig    `toml:"server"`
    Database  DatabaseConfig  `toml:"database"`
    Logging   LoggingConfig   `toml:"logging"`
    OAuth     OAuthConfig     `toml:"oauth"`
    Workspace WorkspaceConfig `toml:"workspace"`
}
```

The `Load()` function resolves `cfg.Workspace.Path` using the same resolution logic as `cfg.Database.Path`. The `Workers` field defaults to 4 if omitted or set to 0.

### Workspace directory structure

Each workspace owns a subdirectory under WORKSPACE_ROOT matching the workspace slug:

```
WORKSPACE_ROOT/
  <workspace-slug>/
    trunk/           # shallow git clone of the upstream repo
```

The workspace directory is created when the clone job executes. The `trunk/` subdirectory is created by go-git during the clone.

### Async clone via job queue

When a workspace is created via `POST /api/v1/workspaces`:

1. The workspace metadata record is inserted into SQLite with `clone_status = "pending"`. The API returns HTTP 201 immediately.
2. A clone job is enqueued on the in-memory job queue.
3. A worker goroutine picks up the job and:
   a. Sets `clone_status = "cloning"` on the workspace record.
   b. Creates the workspace directory under WORKSPACE_ROOT.
   c. Performs a shallow clone (`depth: 1`) of the git URL into `<workspace-dir>/trunk/` using `git.PlainCloneContext`.
   d. If `branch` is specified, clones that specific branch using `SingleBranch: true` and `ReferenceName: plumbing.NewBranchReferenceName(branch)`. If `branch` is null, clones the remote's default branch (HEAD) by omitting `ReferenceName`.
   e. On success: sets `clone_status = "ready"`, records the HEAD commit SHA in `head_sha`, clears `clone_error`.
   f. On failure: sets `clone_status = "failed"`, records the error message in `clone_error`. Removes any partially created workspace directory.

#### Job queue design

The job queue is an in-memory FIFO queue backed by a Go channel.

- **Workers:** The number of goroutines consuming jobs is set by `[workspace] workers` in `config.toml` (default: 4).
- **Job types:** `clone` (initial workspace creation) and `reclone` (workspace reactivation). Both perform the same clone operation.
- **Context:** Each job receives a context derived from the server's root context. On graceful shutdown, the context is cancelled, in-progress clones are interrupted, and pending jobs are discarded.
- **No persistence:** Jobs are not saved to disk. If the server restarts, workspaces with `clone_status = "pending"` or `"cloning"` remain in that state. Recovery requires deleting and recreating the workspace.
- **Idempotency:** The clone job checks whether the workspace directory already exists before cloning. If the directory exists, the job skips the clone and sets `clone_status = "ready"` (handles server restart where clone completed but status wasn't updated).

### New workspace fields

Three fields are added to the workspace schema:

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `clone_status` | TEXT NOT NULL | `'pending'` | Clone state: `pending`, `cloning`, `ready`, `failed`, `archived` |
| `head_sha` | TEXT (nullable) | NULL | HEAD commit SHA of the local clone. Set after successful clone, updated before archive. |
| `clone_error` | TEXT (nullable) | NULL | Error message from the most recent failed clone. NULL when not failed. |

**Updated workspace table schema:**

```sql
CREATE TABLE IF NOT EXISTS workspaces (
    slug          TEXT PRIMARY KEY,
    git_url       TEXT NOT NULL,
    branch        TEXT,
    display_name  TEXT NOT NULL DEFAULT '',
    description   TEXT NOT NULL DEFAULT '',
    owner_id      TEXT NOT NULL,
    org_id        TEXT,
    status        TEXT NOT NULL DEFAULT 'active',
    clone_status  TEXT NOT NULL DEFAULT 'pending',
    head_sha      TEXT,
    clone_error   TEXT,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);
```

### Clone status state machine

```
               ┌─────────────────────────────────────────┐
               │                                         │
               ▼                                         │
Create ──► pending ──► cloning ──► ready ──► archived ───┘ (reactivate)
                          │                    ▲
                          ▼                    │
                        failed ──► archived ───┘ (reactivate)
                                   (no sync)
```

- `pending` → `cloning`: Worker picks up the job.
- `cloning` → `ready`: Clone succeeds; `head_sha` recorded.
- `cloning` → `failed`: Clone fails; `clone_error` recorded.
- `ready` → `archived`: Archive succeeds (push, record SHA, delete local).
- `pending` / `failed` → `archived`: Archive without sync (no local code to push).
- `archived` → `pending`: Reactivate; new clone job enqueued.

### Updated response schema

The workspace response object gains three new fields:

```json
{
  "slug": "string",
  "git_url": "string",
  "branch": "string | null",
  "display_name": "string",
  "description": "string",
  "owner_id": "string (UUID)",
  "org_id": "string (UUID) | null",
  "status": "active | archived",
  "clone_status": "pending | cloning | ready | failed | archived",
  "head_sha": "string (40-char hex SHA) | null",
  "clone_error": "string | null",
  "created_at": "string (RFC 3339)",
  "updated_at": "string (RFC 3339)"
}
```

The three new fields appear in all workspace response objects (create, list, get, update, archive, reactivate).

### Archive behavior changes

When archiving a workspace (`POST /api/v1/workspaces/:slug/archive`), the existing authorization and state checks remain. The following steps are added:

**If `clone_status` is `ready`:**

1. Open the local git repository at `<WORKSPACE_ROOT>/<slug>/trunk/` via `git.PlainOpen`.
2. Push local commits to the upstream remote via `repo.Push(&git.PushOptions{RemoteName: "origin"})`. No `Auth` field — relies on system-level git credentials or public repo access. If the remote is already up-to-date (`git.NoErrAlreadyUpToDate`), treat as success. If the push fails for any other reason, the archive fails with HTTP 500 and the error message.
3. Record the HEAD commit SHA in `head_sha` via `repo.Head().Hash().String()`.
4. Delete the workspace directory (`<WORKSPACE_ROOT>/<slug>/`) from disk.
5. Set `clone_status = "archived"`.
6. Set `status = "archived"` and update `updated_at`.

**If `clone_status` is `cloning`:**

Reject the archive with HTTP 409 and message `"clone in progress; try again after it completes"`. This avoids complex job cancellation and prevents data loss.

**If `clone_status` is `pending` or `failed`:**

No local code to sync. Clean up any partial workspace directory if it exists. Set `clone_status = "archived"`, set `status = "archived"`, update `updated_at`.

### Reactivate behavior changes

When reactivating a workspace (`POST /api/v1/workspaces/:slug/reactivate`), the existing authorization and state checks remain. The following steps are added:

1. Set `status = "active"`.
2. Set `clone_status = "pending"`, clear `clone_error`.
3. Enqueue a clone job (same parameters: workspace's `git_url` and `branch`).
4. Return HTTP 200 with the updated workspace (status: active, clone_status: pending).

The clone proceeds asynchronously, identical to initial creation.

### Delete behavior changes

When deleting a workspace (`DELETE /api/v1/workspaces/:slug`), the existing requirement that the workspace must be archived (status = "archived") remains. The following step is added:

1. If the workspace directory still exists under WORKSPACE_ROOT (defensive — it should have been removed during archiving), delete it. Log a warning if directory deletion fails but proceed with the database row deletion.
2. Physically delete the database row (unchanged from spec 01).

### CLI changes

No new CLI commands. Existing commands work as before. The JSON response objects now include `clone_status`, `head_sha`, and `clone_error`, allowing users to monitor clone progress via `afc workspace get <slug>`.

### Error responses

Additional error conditions, using apikit's standard JSON envelope:

| Condition | HTTP Status |
|-----------|-------------|
| Archive while `clone_status` is `cloning` | 409 |
| Push to upstream fails during archive | 500 |

## Dependencies

| Spec | From Group | To Group | Relationship |
|------|-----------|----------|--------------|
| 01_workspaces | 8 | 1 | Requires fully implemented workspace infrastructure (table, handlers, routes, CLI) |
| 03_workspace_write_delete | 6 | 1 | Requires display_name and description fields, update endpoint |

## Technical Boundaries

- **Language:** Go (1.26+)
- **Repos affected:** Both `apikit` (config extension with `WorkspaceConfig`) and `hub` (clone logic, job queue, lifecycle changes).
- **Foundation:** `github.com/txsvc/apikit` — local replace at `../apikit`.
- **New dependency:** `github.com/go-git/go-git/v5` for git clone, push, and repository operations.
- **Schema migration:** Pre-production; schema changes are applied as DDL updates (no migration framework).

## Verified External API

### `github.com/go-git/go-git/v5` (Go)

| Symbol | Package | Signature | Notes |
|--------|---------|-----------|-------|
| `PlainClone` | `go-git/v5` | `func PlainClone(path string, isBare bool, o *CloneOptions) (*Repository, error)` | |
| `PlainCloneContext` | `go-git/v5` | `func PlainCloneContext(ctx context.Context, path string, isBare bool, o *CloneOptions) (*Repository, error)` | Preferred; supports cancellation |
| `PlainOpen` | `go-git/v5` | `func PlainOpen(path string) (*Repository, error)` | Open existing repo |
| `CloneOptions.Depth` | `go-git/v5` | `Depth int` | Set to 1 for shallow clone |
| `CloneOptions.SingleBranch` | `go-git/v5` | `SingleBranch bool` | Clone only the target branch |
| `CloneOptions.ReferenceName` | `go-git/v5` | `ReferenceName plumbing.ReferenceName` | Target branch; omit for default |
| `CloneOptions.URL` | `go-git/v5` | `URL string` | Repository URL |
| `Repository.Push` | `go-git/v5` | `func (r *Repository) Push(o *PushOptions) error` | Returns `NoErrAlreadyUpToDate` if nothing to push |
| `Repository.Head` | `go-git/v5` | `func (r *Repository) Head() (*plumbing.Reference, error)` | |
| `Reference.Hash` | `plumbing` | `func (r *Reference) Hash() Hash` | |
| `Hash.String` | `plumbing` | `func (h Hash) String() string` | 40-char hex SHA |
| `NewBranchReferenceName` | `plumbing` | `func NewBranchReferenceName(name string) ReferenceName` | Returns `refs/heads/<name>` |
| `NoErrAlreadyUpToDate` | `go-git/v5` | `var NoErrAlreadyUpToDate error` | Sentinel; not a real error |

### `github.com/txsvc/apikit` (v0.0.0, Go, local replace at `../apikit`)

| Symbol | Package | Signature | Notes |
|--------|---------|-----------|-------|
| `Config` | `config` (internal) | `type Config struct { Server, Database, Logging, OAuth, Workspace }` | `Workspace` field must be added |
| `WorkspaceConfig` | `config` (internal) | — | **NOT FOUND.** Must be added by this spec. |
| `resolveDataPath` | `config` (internal) | `func resolveDataPath(dbPath string) string` | Reused for workspace path resolution |
| `Load` | `config` (internal) | `func Load() (*Config, error)` | Must resolve `cfg.Workspace.Path` |
| `LoadConfig` | `apikit` | `func LoadConfig() (*Config, error)` | Public entry point; delegates to `config.Load()` |

## Design Decisions

1. **Async clone via in-memory job queue.** Git clones can take seconds to minutes depending on repo size. Making the clone synchronous would block the API response and risk timeouts. An in-memory job queue with worker goroutines provides simple, concurrent, non-blocking clone execution. Jobs are not persisted across restarts — acceptable for pre-production.

2. **Shallow clone (depth 1).** Full clones waste disk space and time for the primary use case (agents working on the latest code). Shallow clones provide the working tree and latest commit. Deep clone support can be added later if needed.

3. **Clone failure creates workspace in "failed" state, not rollback.** Rolling back workspace metadata on clone failure would lose the configuration (git URL, branch, org association) and force the user to re-enter it. Instead, the workspace persists with `clone_status = "failed"` and `clone_error`. The user can inspect the error and delete/recreate.

4. **Archive pushes to upstream before deleting local code.** Archiving must push any local commits to the upstream remote to prevent data loss. If the push fails, the archive fails. The HEAD SHA is recorded so the workspace can be re-cloned at a known point. Once synced, the local clone is deleted to reclaim disk.

5. **Reactivate enqueues a new clone job.** Since archiving deletes local code, reactivation must re-clone. The clone uses the workspace's stored `git_url` and `branch`. The workspace transitions to `clone_status = "pending"` and the clone proceeds asynchronously.

6. **Config extension in apikit.** The `[workspace]` section is added to apikit's `Config` struct because hub reads config via `apikit.LoadConfig()`. The workspace path resolution reuses `resolveDataPath` logic, keeping config behavior consistent across all data paths.

7. **Default branch from upstream.** When `--branch` is not specified, go-git clones the remote's HEAD reference (the default branch). The `branch` field remains null in the database, meaning "whatever the default is at clone time."

8. **Git server deferred to separate spec.** The internal git server (push/pull capability, authentication via hub-managed credentials) is an independent feature with its own protocol, auth, and transport concerns. It will be specified separately.

9. **Archive rejects workspaces mid-clone (HTTP 409).** Archiving a workspace while a clone is in progress would require cancelling the job and handling partial state. Returning 409 is simpler and safer — the user waits for the clone to finish or fail, then archives.

10. **Configurable worker count.** The job queue worker count is configurable via `[workspace] workers` in `config.toml` (default: 4). This lets operators tune concurrency based on their server's I/O and network capacity.

11. **Lifecycle handlers extended in-place.** The archive, reactivate, and delete handlers from spec 03 are modified in-place to add git operations. This spec layers on top of the existing handler logic (authorization checks, state validation) rather than replacing it. All changes land in the same `internal/workspace/` package.

12. **WORKSPACE_ROOT creation is fatal on failure.** If the server cannot create the workspace root directory on boot, it exits with a fatal error. Starting without workspace support would produce confusing behavior — every workspace create would fail with an opaque filesystem error.

