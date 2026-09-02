---
spec_id: 09
spec_name: git_credentials
title: Git Credentials for Workspace Creation
status: draft
created_at: '2026-07-29T16:26:48.914220+00:00'
updated_at: '2026-07-29T16:31:16.010895+00:00'
owner: candlekeep
source: docs/prd/prd7.md
schema_version: 1
---
# Git Credentials for Workspace Creation

## Intent

Enhance the `afc workspace create` command and the `POST /workspaces` API endpoint to accept optional git credentials (PAT or username/password) for authenticating against private upstream repositories. Credentials are stored as workspace-scoped secrets and used during clone and reclone operations.

## Background

Currently, when a user runs `afc workspace create` with a private git URL and no credentials, workspace creation succeeds immediately but the background clone job fails with an authentication error from go-git. The user must poll `GET /workspaces/<slug>` and observe `clone_status='failed'` along with a `clone_error` field containing the raw go-git auth rejection message to discover the problem. This delayed, poll-based failure discovery is a poor user experience and leaves orphaned workspace records in a permanently failed clone state.

This feature eliminates that delayed discovery by validating credentials against the upstream repository at creation time — before the workspace row is committed and the clone job is enqueued — providing immediate, actionable feedback to the user.

## Goals

1. Allow users to create workspaces from private git repositories by providing authentication credentials at creation time.
2. Store credentials securely as workspace-scoped secrets using the existing secrets infrastructure.
3. Use stored credentials during background clone and reclone (reactivation) operations.
4. Validate credentials against the upstream repository at workspace creation time before enqueuing the clone job.

## Non-Goals

- Syncing (pushing) back to the upstream repo is explicitly out of scope and will be addressed in a later spec.
- SSH key-based authentication is not covered.
- Credential rotation UI/workflow — users can already update credentials via `afc secrets update --workspace <slug> GIT_PAT=<new-token>`.
- Per-endpoint rate limiting for the ls-remote validation call — the existing 10-second timeout combined with authentication requirements and slug uniqueness constraints provides sufficient natural protection against abuse.
- A reconciliation job or retry logic for the rare compensating-DELETE failure case — this is handled via structured logging for manual cleanup.

## Tech Stack

- Go 1.26+
- go-git v5 (already a dependency) — `github.com/go-git/go-git/v5`
- go-git HTTP transport auth — `github.com/go-git/go-git/v5/plumbing/transport/http`
- go-git in-memory storage — `github.com/go-git/go-git/v5/storage/memory`
- SQLite (existing database)
- Echo v4 (existing HTTP framework)
- Cobra (existing CLI framework)
- apikit (existing auth/CLI library)
- testify (existing test assertion library)

## Credential Types

Two mutually exclusive credential types are supported:

### Personal Access Token (PAT)
- Single token used for authentication.
- Mapped to workspace secret key `GIT_PAT`.
- Passed to go-git as `http.BasicAuth{Username: "x-token-auth", Password: <pat>}`.

### Username/Password
- Pair of credentials used for HTTP basic auth.
- Mapped to workspace secret keys `GIT_USERNAME` and `GIT_PASSWORD`.
- Passed to go-git as `http.BasicAuth{Username: <username>, Password: <password>}`.

## CLI Changes

### `afc workspace create` — New Flags

| Flag | Type | Description |
|------|------|-------------|
| `--git-pat` | string | Personal access token for upstream repo |
| `--git-username` | string | Username for HTTP basic auth |
| `--git-password` | string | Password for HTTP basic auth |

### Validation Rules (CLI-side)

1. `--git-pat` and `--git-username`/`--git-password` are mutually exclusive. If both are provided, the CLI must reject the command with an error.
2. `--git-username` and `--git-password` must be provided together. If only one is given, the CLI must reject the command with an error.
3. If any credential flag is provided, `--git-url` must start with `https://`. Non-HTTPS URLs (SSH, HTTP, file://) with credentials are rejected with an error.

### CLI Request Body

When credential flags are provided, the CLI includes them in the `POST /workspaces` JSON request body:

- PAT: `{"slug": "...", "git_url": "...", "git_pat": "<token>"}`
- Username/password: `{"slug": "...", "git_url": "...", "git_username": "<user>", "git_password": "<pass>"}`

### CLI Error Rendering

When the API returns HTTP 400 (e.g. for credential validation failure), the CLI delegates entirely to the existing `apikit.CLIHandleError` middleware, which formats API error responses consistently across all commands. No special-case rendering is required for this feature.

## API Changes

### `POST /workspaces` — Extended Request Body

The `createWorkspaceRequest` struct gains three optional fields:

| Field | Type | JSON | Description |
|-------|------|------|-------------|
| GitPAT | *string | `git_pat` | Personal access token |
| GitUsername | *string | `git_username` | HTTP basic auth username |
| GitPassword | *string | `git_password` | HTTP basic auth password |

### API Validation Rules

1. **Mutual exclusion:** If `git_pat` is provided, `git_username` and `git_password` must be absent (and vice versa). Violation returns HTTP 400.
2. **Pair completeness:** If `git_username` is provided, `git_password` must also be provided (and vice versa). Violation returns HTTP 400.
3. **HTTPS requirement:** If any credential field is provided, `git_url` must start with `https://`. Violation returns HTTP 400 with message `"git credentials require an HTTPS git_url"`.
4. **Credential validation:** Before creating the workspace, the server performs a lightweight ls-remote check against the upstream repository using the provided credentials (see [Credential Validation](#credential-validation) below). If the check fails, return HTTP 400 with a sanitised error message (see [Error Message Format](#error-message-format)). This validates that the credentials are correct before committing the workspace record.

### Credential Validation

The handler performs an ephemeral `git ls-remote`-equivalent check using `go-git` before creating the workspace. To enable unit testing, the validation logic is extracted behind an injectable interface following the same pattern as `CloneFuncType` and `ArchiveHeadFuncType`:

```go
// ValidateCredentialsFuncType is the injectable function type for credential validation.
// In production, this wraps Remote.ListContext. In tests, it is replaced with a stub.
type ValidateCredentialsFuncType func(ctx context.Context, gitURL string, auth transport.AuthMethod) error
```

The default production implementation:

```go
remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
    URLs: []string{gitURL},
})
ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
defer cancel()
_, err := remote.ListContext(ctx, &git.ListOptions{Auth: authMethod})
```

- **Storage backend:** `memory.NewStorage()` (from `github.com/go-git/go-git/v5/storage/memory`) is used as the storer, making the check fully ephemeral with no disk I/O.
- **Timeout:** A **10-second** context deadline is applied. If the deadline is exceeded (slow or unreachable upstream), the handler returns HTTP 400 with message `"credential validation failed for <url>: unable to authenticate"`.
- **Public repos:** If no credentials are provided, this check is skipped entirely.

#### Error Message Format

When credential validation fails (authentication rejected, timeout, or network error), the handler returns HTTP 400 with a sanitised message body. The message includes the upstream URL but never the raw go-git error string:

```
credential validation failed for <git_url>: unable to authenticate
```

Example:
```json
{"error": "credential validation failed for https://github.com/acme/private-repo: unable to authenticate"}
```

The raw go-git error is logged server-side (at ERROR level) but never surfaced to the client, avoiding leakage of internal error details.

### Atomic Storage

When credentials pass validation, the handler stores them and creates the workspace using a **compensating DELETE** pattern:

1. Creates the workspace row in the database.
2. Calls `Store.CreateSecrets()` to store the credentials as workspace-scoped secrets. `CreateSecrets` opens its own internal transaction.
3. Enqueues the clone job.

**Failure handling:** If `CreateSecrets` fails after the workspace row has been inserted, the handler issues a compensating `DELETE` on the workspace row to restore a clean state, then returns an HTTP 500 error to the client.

**Compensating DELETE failure:** If the compensating `DELETE` itself fails (e.g. due to a DB connection error), the handler logs a `CRITICAL`-level structured log entry including the workspace slug to aid manual cleanup, then returns HTTP 500 to the client. No retry logic or reconciliation job is implemented at this time — the CRITICAL log is the signal for operator intervention.

A true shared `sql.Tx` across both operations is not used because `CreateSecrets` manages its own transaction internally; extending it to accept an external `*sql.Tx` would require a significant API change that is out of scope for this spec. The failure window between the two back-to-back DB operations is minimal (limited to network/IO errors).

### Audit Log Safety

Credential values (`git_pat`, `git_username`, `git_password`) must never appear in request/response logs. The existing logging middleware must treat these JSON fields as sensitive and scrub their values before writing to any log sink. If the existing middleware does not support field-level scrubbing, the handler must zero out credential fields on the request struct before any logging occurs.

### Secret Key Mapping

| Flag / Field | Secret Key |
|-------------|------------|
| `git_pat` | `GIT_PAT` |
| `git_username` | `GIT_USERNAME` |
| `git_password` | `GIT_PASSWORD` |

Secrets are stored with `owner_type = "workspace"` and `owner_id = <workspace_slug>`.

## Clone Worker Changes

### Credential Lookup

Before performing a clone, the queue worker must:

1. Query the secrets store for the workspace's git credentials using the new `GetSecretValue(ownerType, ownerID, key)` method on the secrets store.
2. Check for `GIT_PAT` first. If found, use PAT auth.
3. If no PAT, check for `GIT_USERNAME` and `GIT_PASSWORD`. If both found, use basic auth.
4. If no credentials found, clone without authentication (public repo).

### `CloneFuncType` Signature Change

The `CloneFuncType` signature is extended to accept an optional `transport.AuthMethod` parameter as the last argument before the return values:

**Before:**
```go
type CloneFuncType func(
    ctx        context.Context,
    path       string,
    url        string,
    depth      int,
    singleBranch bool,
    refName    string,
) (headSHA string, err error)
```

**After:**
```go
type CloneFuncType func(
    ctx          context.Context,
    path         string,
    url          string,
    depth        int,
    singleBranch bool,
    refName      string,
    auth         transport.AuthMethod, // nil for public repos
) (headSHA string, err error)
```

The `defaultCloneFn` passes `auth` to `git.CloneOptions.Auth`. When `auth` is `nil`, no authentication is used (public repo behaviour is unchanged).

### go-git Auth Integration

For PAT: `&http.BasicAuth{Username: "x-token-auth", Password: pat}`
For username/password: `&http.BasicAuth{Username: username, Password: password}`

### Reclone on Reactivation

When a workspace is reactivated, the reclone job must follow the same credential lookup logic. The credentials persist in the secrets table across archive/reactivate cycles.

## Secrets Store Extension

### New Method: `GetSecretValue`

```go
func (s *Store) GetSecretValue(ownerType, ownerID, key string) (string, error)
```

- Performs case-insensitive key lookup.
- Returns the decoded (base64-decoded) raw value. `CreateSecrets` base64-encodes values at write time; `GetSecretValue` reverses this encoding so callers receive the plaintext value directly.
- Returns `NotFoundError` if the key does not exist.
- This method is internal-only — not exposed via any HTTP endpoint. Secret values must never be returned through the API.

## Secret Lifecycle

### Deletion on Workspace Removal

When a workspace is deleted, all associated workspace-scoped secrets — including `GIT_PAT`, `GIT_USERNAME`, and `GIT_PASSWORD` — are automatically cascade-deleted by the existing `deleteWorkspace()` implementation in `internal/workspace/store.go` (lines 199–230), which deletes from both the `secrets` and `variables` tables where `owner_type='workspace' AND owner_id=slug`. No additional credential cleanup logic is required for this feature.

## Testing Strategy

### Unit Tests

The credential validation path is isolated via `ValidateCredentialsFuncType` injection, following the same pattern as `CloneFuncType` and `ArchiveHeadFuncType`. Unit tests replace the production implementation with a stub:

- **Happy path:** stub returns `nil`; assert workspace is created and secrets stored.
- **Auth failure:** stub returns an error; assert HTTP 400 with the sanitised error message.
- **Timeout:** stub returns a `context.DeadlineExceeded` error; assert HTTP 400 with the sanitised error message.
- **Mutual exclusion / pair completeness / HTTPS-only:** no stub needed; validated before the credential check is invoked.
- **Compensating DELETE success:** stub `CreateSecrets` to fail; assert the workspace row is deleted and HTTP 500 is returned.
- **Compensating DELETE failure:** stub both `CreateSecrets` and `DELETE` to fail; assert a CRITICAL log entry is emitted and HTTP 500 is returned.

Clone worker credential lookup is unit-tested by stubbing `GetSecretValue` responses for all three cases (PAT, username/password, no credentials) and asserting the correct `transport.AuthMethod` is passed to `CloneFuncType`.

### Integration Tests

Integration tests against real upstream repositories are optional and kept in a separate `_integration_test.go` file gated by a build tag (e.g. `//go:build integration`). They are not run in CI by default.

### Test Tooling

- `testify/assert` and `testify/require` for assertions (existing project convention).
- `net/http/httptest` for handler-level HTTP tests.
- In-memory SQLite for store-level tests (existing project convention).

## Verified External API

### `go-git/go-git/v5` (v5.19.1, Go)

| Symbol | Package | Signature | Notes |
|--------|---------|-----------|-------|
| `PlainCloneContext` | `github.com/go-git/go-git/v5` | `func PlainCloneContext(ctx context.Context, path string, isBare bool, o *CloneOptions) (*Repository, error)` | |
| `CloneOptions.Auth` | `github.com/go-git/go-git/v5` | `Auth transport.AuthMethod` | Optional auth for clone |
| `NewRemote` | `github.com/go-git/go-git/v5` | `func NewRemote(s storage.Storer, c *config.RemoteConfig) *Remote` | For ls-remote check; pass `memory.NewStorage()` as storer |
| `Remote.ListContext` | `github.com/go-git/go-git/v5` | `func (r *Remote) ListContext(ctx context.Context, o *ListOptions) ([]*plumbing.Reference, error)` | ls-remote equivalent; use with 10 s context deadline |
| `ListOptions.Auth` | `github.com/go-git/go-git/v5` | `Auth transport.AuthMethod` | Auth for ls-remote |
| `BasicAuth` | `github.com/go-git/go-git/v5/plumbing/transport/http` | `type BasicAuth struct { Username, Password string }` | HTTP basic auth |
| `NewStorage` | `github.com/go-git/go-git/v5/storage/memory` | `func NewStorage() *Storage` | Ephemeral in-memory storer for ls-remote validation check |

### `secrets.Store` (internal)

| Symbol | Package | Signature | Notes |
|--------|---------|-----------|-------|
| `CreateSecrets` | `internal/secrets` | `func (s *Store) CreateSecrets(ownerType, ownerID string, entries []EntryInput) ([]SecretEntry, error)` | Opens its own internal transaction; base64-encodes values at write time |
| `GetSecretValue` | `internal/secrets` | `func (s *Store) GetSecretValue(ownerType, ownerID, key string) (string, error)` | NEW — to be added; returns base64-decoded plaintext value |

## Dependencies

| Spec | From Group | To Group | Relationship |
|------|-----------|----------|--------------|
| 07_secrets_variables | 3 | 1 | Uses secrets Store and CreateSecrets/GetSecretValue for credential storage |
| 05_workspace_checkout | 3 | 2 | Extends CloneFuncType and clone worker with auth parameter |

## Design Decisions

1. **API-level atomic storage (Issue 1):** The `POST /workspaces` endpoint handles credential storage atomically rather than the CLI orchestrating separate API calls. This ensures no partial state (workspace without its required credentials).

2. **Create-time credential validation (Issue 2):** Credentials are tested against the upstream repo before accepting the workspace creation request. This provides immediate feedback to the user rather than a delayed clone failure (the current behaviour where the user must poll `GET /workspaces/<slug>` to discover `clone_status='failed'`).

3. **Reclone uses stored credentials (Issue 3):** Yes, reclone after reactivation uses the same credential lookup as the initial clone.

4. **Both CLI and API enforce mutual exclusion (Issue 4):** Since the API handles credentials atomically, both layers validate mutual exclusion.

5. **Require both username and password (Issue 5):** If one is given without the other, the CLI and API both reject the request.

6. **Compensating DELETE instead of shared transaction (Issue 6):** `CreateSecrets` manages its own internal transaction, so extending it to accept an external `*sql.Tx` would be a significant API change out of scope. Instead, if `CreateSecrets` fails after the workspace INSERT, the handler issues a compensating DELETE on the workspace row. If the compensating DELETE also fails, a CRITICAL-level structured log entry (including the workspace slug) is emitted for manual cleanup, and HTTP 500 is returned. The failure window is minimal (limited to back-to-back DB operations).

7. **New GetSecretValue store method (Issue 7):** A new method on the secrets Store provides internal access to decoded secret values. This keeps the clone worker using the store abstraction.

8. **PAT format (Issue 8):** PAT is passed as `BasicAuth{Username: "x-token-auth", Password: <pat>}`, which is portable across GitHub, GitLab, and Bitbucket.

9. **HTTPS-only with credentials (Issue 9):** The CLI and API both reject credential-bearing requests when `git_url` is not HTTPS.

10. **No separate credential update flow (Issue 10):** Existing `afc secrets update --workspace <slug> KEY=VALUE` already covers credential updates post-creation.

11. **Ephemeral ls-remote uses memory.NewStorage() with 10 s timeout (Issue 11):** `memory.NewStorage()` is the idiomatic go-git storer for a connectivity-only check — it requires no disk I/O and is discarded after the check. A 10-second context deadline prevents slow or unreachable upstreams from blocking the HTTP handler indefinitely. No additional rate limiting is applied beyond the existing authentication and slug-uniqueness constraints.

12. **Sanitised credential-validation error message (Issue 12):** The HTTP 400 response for a failed credential check uses the fixed format `"credential validation failed for <url>: unable to authenticate"`. The raw go-git error is logged server-side (ERROR level) but never surfaced to the client.

13. **Injectable ValidateCredentialsFuncType (Issue 13):** The ls-remote validation is wrapped in a `ValidateCredentialsFuncType` function type, following the same injection pattern as `CloneFuncType` and `ArchiveHeadFuncType`. This enables deterministic unit tests without a real upstream repository.

14. **CLI error rendering delegated to apikit (Issue 14):** The CLI uses `apikit.CLIHandleError` for all API error responses, including credential validation failures. No special-case rendering is needed.

15. **Credential secrets cascade-deleted on workspace deletion (Issue 15):** The existing `deleteWorkspace()` implementation already cascade-deletes all workspace-scoped secrets. No additional code is required.

16. **Audit log safety (Issue 16):** Credential field values must be scrubbed from request/response logs before writing to any log sink, either via middleware configuration or by zeroing out credential fields on the request struct prior to logging.
