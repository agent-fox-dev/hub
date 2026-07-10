---
spec_id: '04'
spec_name: workspaces_and_tokens
title: Workspaces And Tokens
status: draft
created_at: '2026-07-10T15:01:12.184502+00:00'
updated_at: '2026-07-10T15:11:46.133322+00:00'
owner: ''
source: docs/01_prd.md
schema_version: 1
---
# Workspaces and Tokens

## Intent

Implement workspace management and workspace token delegation for af-hub. Workspaces are the git-repo-scoped execution context where work is done. Workspace tokens enable delegated read-only access to workspaces for tools, agents, and programs.

This builds on server_foundation (spec 1) for auth middleware and database schema, and oauth_and_users (spec 2) for user identity and API key authentication.

## Goals

- Implement workspace CRUD endpoints: create (API key auth only), list (own workspaces), get (owner, admin, or valid token).
- Validate workspace slugs (same rules as team slugs: lowercase alphanumeric + hyphens, 3-64 chars, starts with letter, no trailing hyphen). Slugs are globally unique across all workspaces in the system.
- Accept git_url in HTTPS and SCP-style SSH formats without reachability validation.
- Implement workspace token CRUD: create (owner/admin), list (metadata only), revoke (permanent).
- Support workspace token expiry: 0 (indefinite), 30, 60, or 90 days. Default 30 days.
- Enforce workspace-scoped access control: owners have full access, token holders have read-only.

## Non-goals

- Workspace lifecycle beyond creation (archive, delete).
- Workspace update endpoint (PATCH/PUT) — planned but deferred to a future spec iteration.
- Git branch management, cloning, or checkout.
- Workspace token permissions beyond read-only.
- Storing workspace tokens in CLI config.
- Pagination for list endpoints (deferred to a future iteration).
- Rate limiting or quota enforcement on token creation (deferred to a future iteration).

## Dependencies

- **server_foundation (spec 01):** Provides auth middleware, the `workspaces` and `workspace_tokens` table schema, and the `teams` table schema used for team_id validation. Also establishes the standard JSON error envelope convention used by all endpoints in this spec. The auth middleware globally handles 401 Unauthorized for missing or invalid credentials before any handler in this spec runs. The auth middleware is also responsible for checking user block status on every authenticated request — blocked users' tokens are treated as inert (HTTP 401) uniformly across all endpoints.
- **oauth_and_users (spec 02):** Provides user identity and API key authentication, which workspace creation and ownership depend on.

> **Note — teams table:** The `team_id` field accepted on `POST /api/v1/workspaces` is validated with a self-contained direct SQL query against the `teams` table (checking existence and active status). The teams table schema is owned by server_foundation (spec 01). No formal dependency on the `teams` spec is required; the workspace creation handler performs the lookup independently.

> **Note — cli_client spec:** The `cli_client` spec (spec 05) is the primary consumer of this spec's API contracts for workspace registration and workspace token management. This spec is the authoritative source; the CLI spec depends on it. No cross-reference is required here — the relationship is established by spec ordering, and the CLI spec references this one.

## Functional Requirements

### Token authentication wire format

Workspace tokens are presented in HTTP requests using the standard `Authorization: Bearer <token>` header — the same header format used for user API keys and admin tokens. The auth middleware inspects the token prefix to determine the credential type:

- `af_wt_` — workspace token
- `af_key_` — user API key
- `af_at_` — admin token

This prefix-based dispatch is established in server_foundation (spec 01) and applies uniformly to all credential types. Implementers of endpoints in this spec do not need to inspect the `Authorization` header directly; the middleware resolves the credential and attaches the authenticated identity (and, for workspace tokens, the scoped workspace_id) to the request context before any handler runs.

### Request body parsing

POST endpoint request bodies are parsed as JSON using Echo's default JSON binding. Unknown or extra fields in the request body are silently ignored — this is consistent with Echo's default behavior and the convention established by existing specs. Content-Type enforcement is not required; JSON parsing is attempted regardless of the Content-Type header value.

### Workspace management

- `POST /api/v1/workspaces` — Create a workspace. Requires user API key auth (not admin token, not workspace token). Accepts the following fields in the JSON request body:
  - `slug` (string, required) — workspace identifier
  - `git_url` (string, required) — git remote URL
  - `branch` (string, optional, nullable) — target branch
  - `team_id` (string, optional, nullable) — owning team identifier

  The creating user becomes the owner. Returns HTTP 201 with the workspace object (using exactly the fields defined in the Workspace object schema table — the same schema used by GET responses). Returns HTTP 409 on duplicate slug (slugs are globally unique). If team_id is provided, validate the team exists and is active via a direct SQL query against the teams table; return HTTP 400 if the team does not exist or is inactive.

- `GET /api/v1/workspaces` — List workspaces. Admin: all workspaces. User (API key): own workspaces only. Workspace tokens: not allowed (HTTP 403). Ordered by created_at ASC, with id ASC as the tiebreaker for records sharing the same created_at timestamp. Unbounded — no pagination in this iteration. Returns a bare JSON array of workspace objects.

- `GET /api/v1/workspaces/:slug` — Get workspace details by slug. Requires workspace ownership, admin, or a valid workspace token scoped to this workspace. Returns the full workspace object (all fields in the Workspace object schema table) for all authorized caller types — no fields are redacted for workspace token callers. Returns HTTP 404 if the slug does not exist in the database. This 404 behavior applies uniformly to all caller types — owner, admin, workspace token, and non-owner user alike. There is no distinction between "not found" and "not authorized" at this endpoint; a nonexistent slug always returns HTTP 404 regardless of what access the caller would have had if the workspace existed.

### Workspace object schema

All workspace endpoints that return a workspace (or list of workspaces) — including `POST /api/v1/workspaces` and all `GET` workspace endpoints — use the following fields. The schema is identical regardless of caller type (owner, admin, or workspace token):

| Field | Type | Notes |
|---|---|---|
| id | string/UUID | Internal workspace identifier |
| slug | string | URL-safe workspace identifier; globally unique across all workspaces |
| git_url | string | HTTPS or SCP-style SSH git remote URL |
| branch | string \| null | Target branch; null means repo default |
| team_id | string \| null | Owning team identifier; null if unset |
| owner_user_id | string | User ID of the workspace creator/owner |
| created_at | string (ISO 8601) | Workspace creation timestamp |
| updated_at | string (ISO 8601) | Last update timestamp; set equal to created_at explicitly in the INSERT statement. Maintained for future use — a future PATCH endpoint will be responsible for updating this field. |

### Workspace validation

- **Slug:** Lowercase alphanumeric + hyphens, 3-64 characters, starts with letter, no trailing hyphen. Must be globally unique across all workspaces in the system (enforced via a unique index in the database). Uniqueness is enforced against all currently existing records; if workspace deletion is introduced in a future iteration, the uniqueness policy will be addressed at that time.
- **git_url:** Accepts exactly two formats:
  - HTTPS: URL must start with `https://`. Example: `https://github.com/org/repo.git`
  - SCP-style SSH: Must match the pattern `git@<host>:<path>`. Example: `git@github.com:org/repo.git`
  - All other schemes are rejected with HTTP 400, including `ssh://`, `git://`, `http://`, and any other scheme.
  - Maximum length: 2048 characters. Requests exceeding this limit are rejected with HTTP 400.
  - Stored as-is; reachability is not validated.
- **branch:** Optional string, nullable. Null means the repo's default branch. When provided: maximum 255 characters, no ASCII whitespace characters. ASCII whitespace is defined as: space (0x20), tab (0x09), newline (0x0A), carriage return (0x0D). Stored as-is without git-ref validation — the caller is trusted to supply a valid git branch name, but basic sanity checks (length and no ASCII whitespace) are enforced.

### Workspace token management

- `POST /api/v1/workspaces/:slug/tokens` — Create a workspace token. Requires workspace ownership or admin. Accepts the following fields in the JSON request body:
  - `label` (string, optional) — human-readable token label; max 128 characters. An empty string (`""`) is normalized to null and stored as null.
  - `expires` (integer, optional) — expiry in days; must be one of `0`, `30`, `60`, or `90`; defaults to `30` if omitted.

  Returns HTTP 201 with the full token creation response including the plaintext secret (returned exactly once). Token labels are optional and uniqueness is not enforced — multiple tokens within the same workspace may share a label or have no label. Token creation is unbounded — no maximum token count per workspace is enforced in this iteration.

- `GET /api/v1/workspaces/:slug/tokens` — List all tokens for the workspace. Returns a bare JSON array of token metadata objects (never the secret). Ordered by created_at ASC, with id ASC as the tiebreaker for tokens sharing the same created_at timestamp. Requires workspace ownership or admin. Workspace tokens are not permitted to call this endpoint (HTTP 403).

- `DELETE /api/v1/workspaces/:slug/tokens/:token_id` — Permanently revoke a workspace token. Requires workspace ownership or admin. Returns HTTP 204 No Content with no response body. The handler looks up the token scoped to the workspace identified by :slug — if the token_id does not exist in the database at all, or if it exists but belongs to a different workspace, HTTP 404 is returned (information hiding: cross-workspace token_ids are treated as not found). Idempotent with respect to already-revoked tokens: revoking a token that exists and belongs to this workspace but is already revoked also returns HTTP 204.

### Token creation response schema

`POST /api/v1/workspaces/:slug/tokens` returns HTTP 201 with the following fields:

| Field | Type | Notes |
|---|---|---|
| token | string | Full plaintext token string: `af_wt_<token_id>_<secret>`. Returned exactly once — not stored or retrievable again. |
| token_id | string | 8-character alphanumeric identifier, generated using crypto/rand with base62 encoding (alphabet: `0-9A-Za-z`) |
| label | string \| null | Token label as provided; null if not supplied or if an empty string was supplied |
| expires_at | string \| null (ISO 8601) | Expiry timestamp; null if expires=0 (indefinite) |
| created_at | string (ISO 8601) | Token creation timestamp |

### Token list response schema

`GET /api/v1/workspaces/:slug/tokens` returns HTTP 200 with a bare JSON array of objects ordered by created_at ASC (tiebroken by id ASC), each containing:

| Field | Type | Notes |
|---|---|---|
| token_id | string | 8-character alphanumeric token identifier |
| label | string \| null | Token label; null if not supplied or if an empty string was supplied at creation time |
| created_at | string (ISO 8601) | Token creation timestamp |
| expires_at | string \| null (ISO 8601) | Expiry timestamp; null if indefinite |
| revoked_at | string \| null (ISO 8601) | Revocation timestamp; null if not revoked |

Expired and revoked tokens are included in the listing (they are not removed). The secret is never returned in this endpoint.

### Workspace token lifecycle

- **Format:** `af_wt_<token_id>_<secret>` — token_id is 8 alphanumeric chars (base62: `0-9A-Za-z`), secret is 32 alphanumeric chars (base62: `0-9A-Za-z`). Because both components use base62 encoding (which excludes underscores), the `_` separator characters in the token string are unambiguous — token_id and secret will never themselves contain underscores.
- **Generation:** Both token_id and secret are generated using `crypto/rand` with base62 encoding (alphabet: `0-9A-Za-z`), yielding 8 and 32 base62 characters respectively. The implementer is responsible for consuming sufficient random bytes from `crypto/rand` to produce output of the required character count. The spec does not prescribe the number of raw bytes consumed.
- **token_id uniqueness and collision handling:** token_id must be unique within the `workspace_tokens` table. If a generated token_id collides with an existing record, the implementation must silently retry generation up to 3 times. If all 3 retries result in collisions, the handler returns HTTP 500. Given the collision probability is astronomically low with 8 base62 characters (~218 trillion combinations), 3 retries is more than sufficient in practice.
- **Secret storage:** The SHA-256 hash of the secret is stored in the database column designated for hashed secrets in the `workspace_tokens` table schema (defined in server_foundation spec 01); the plaintext secret is never persisted.
- Scoped to a single workspace, references the creating user.
- Read-only access to the workspace.
- Expired or revoked tokens cannot authenticate — the auth middleware rejects them with HTTP 401 Unauthorized before any handler runs, treating them the same as an invalid or missing token.
- Expired and revoked tokens remain visible in token listing responses.
- Tokens of blocked users are inert (behave as invalid, returning HTTP 401); this check is performed by the server_foundation auth middleware on every authenticated request. Tokens resume functioning if the user is unblocked.

### Workspace token scope enforcement

The auth middleware is responsible for validating the token (checking format, existence, expiry, revocation, and the owning user's block status) and attaching the resolved workspace_id to the request context. The route handler is responsible for comparing the token's scoped workspace_id against the workspace identified by the :slug parameter. If there is a mismatch (i.e., the token is valid but scoped to a different workspace), the handler returns HTTP 403 Forbidden. This division keeps the middleware general-purpose while handlers enforce resource-level authorization.

### Access control

| Caller | POST /api/v1/workspaces | GET /api/v1/workspaces | GET /api/v1/workspaces/:slug | POST .../tokens | GET .../tokens | DELETE .../tokens/:id |
|---|---|---|---|---|---|---|
| Admin token | ✗ (403) | ✓ (all workspaces) | ✓ | ✓ | ✓ | ✓ |
| User API key (owner) | ✓ | ✓ (own only) | ✓ | ✓ | ✓ | ✓ |
| User API key (non-owner) | ✓ (creates new) | ✓ (own only) | ✗ (403) | ✗ (403) | ✗ (403) | ✗ (403) |
| Workspace token (valid, scoped) | ✗ (403) | ✗ (403) | ✓ | ✗ (403) | ✗ (403) | ✗ (403) |
| Workspace token (expired/revoked) | ✗ (401) | ✗ (401) | ✗ (401) | ✗ (401) | ✗ (401) | ✗ (401) |

> **Note:** 401 Unauthorized for unauthenticated or invalid credentials (including expired/revoked tokens and blocked-user tokens) is handled globally by the auth middleware from server_foundation and is not re-stated per-handler.

### Error responses

401 Unauthorized responses (missing credentials, invalid API key, expired/revoked/invalid workspace token, tokens belonging to blocked users) are handled globally by the auth middleware from server_foundation (spec 01) and are out of scope for per-endpoint documentation in this spec.

All handler-level errors use the standard JSON error envelope established in server_foundation (spec 01) and oauth_and_users (spec 02):

```json
{
  "error": {
    "code": <integer>,
    "message": "<string>"
  }
}
```

| HTTP Status | Scenario |
|---|---|
| 400 | Validation failure (invalid slug format, invalid git_url format or scheme, git_url exceeds 2048 characters, invalid expires value, team_id references nonexistent or inactive team, label exceeds 128 characters, branch exceeds 255 characters or contains ASCII whitespace, etc.) |
| 403 | Caller lacks permission (e.g., workspace token calling a write endpoint or list endpoint, admin token attempting workspace creation, non-owner user accessing another user's workspace, valid workspace token scoped to a different workspace than :slug) |
| 404 | Workspace slug not found (returned uniformly for all caller types — no distinction between not found and not authorized); or token_id not found in the database or belongs to a different workspace (never exposed as cross-workspace) |
| 409 | Duplicate slug on workspace creation |
| 500 | Unexpected internal errors, including token_id generation failure after 3 retries due to collision (astronomically rare). General database or server errors also return HTTP 500 with the standard error envelope. |

## Technical Boundaries

- **Language:** Go (1.22+)
- **HTTP framework:** Echo
- **Database:** SQLite (workspaces and workspace_tokens tables from spec 1)
