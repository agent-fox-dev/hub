# API Reference

Base URL: `http://localhost:8080` (default)

All protected endpoints require an `Authorization: Bearer <token>` header.
Error responses use a consistent envelope:

```json
{"error": {"code": 400, "message": "description"}}
```

Request bodies are JSON. The server enforces a 1 MB body size limit (HTTP 413
if exceeded). Every response includes an `X-Request-ID` header for tracing.

## Authentication

Three credential types are accepted:

| Type | Format | Scope |
|------|--------|-------|
| Admin token | `af_admin_<64 hex chars>` | Full access to all endpoints |
| User API key | `af_<key_id>_<secret>` | User-scoped access to own resources |
| Workspace token | `af_wt_<token_id>_<secret>` | Read-only access to one workspace |

Tokens are validated structurally (prefix, length, charset) before any database
lookup. Secrets are stored as SHA-256 hashes. Expired and revoked tokens are
rejected. Blocked users are denied access (admin tokens bypass this check).

---

## Health Probes

These endpoints require no authentication.

### `GET /healthz`

Liveness probe.

**Response:** `200`
```json
{"status": "ok"}
```

### `GET /readyz`

Readiness probe. Pings the database with a 2-second timeout.

**Response:** `200`
```json
{"status": "ready"}
```

**Error:** `503`
```json
{"status": "not ready"}
```

---

## OAuth

### `GET /api/v1/auth/providers`

List configured OAuth providers and their authorize URLs.

**Auth:** None

**Response:** `200`
```json
{
  "providers": [
    {
      "name": "github",
      "authorize_url": "https://github.com/login/oauth/authorize",
      "scopes": ["read:user", "user:email"]
    }
  ]
}
```

### `POST /api/v1/auth/callback`

Exchange an OAuth authorization code for user credentials and an API key.

**Auth:** None

**Request:**
```json
{
  "provider": "github",
  "code": "authorization_code",
  "state": "random_state",
  "redirect_uri": "http://localhost:PORT/callback",
  "expires": 90
}
```

`expires` controls API key lifetime in days. Allowed values: `0` (no expiry),
`30`, `60`, `90` (default).

**Response:** `200`
```json
{
  "user": {
    "id": "uuid",
    "username": "octocat",
    "email": "user@example.com",
    "full_name": "The Octocat",
    "status": "active",
    "provider": "github",
    "created_at": "2026-01-01T00:00:00Z",
    "updated_at": "2026-01-01T00:00:00Z"
  },
  "api_key": {
    "key_id": "abcd1234",
    "secret": "plaintext_secret",
    "token": "af_abcd1234_plaintext_secret",
    "user_id": "uuid",
    "created_at": "2026-01-01T00:00:00Z",
    "expires_at": "2026-04-01T00:00:00Z"
  }
}
```

The `secret` and `token` fields are returned only once. The redirect URI must
match the server's allowlist: in dev mode any `http://localhost:*` is accepted;
in production it must match `[server] external_url`.

---

## Users

All user endpoints require admin authentication unless noted.

### `POST /api/v1/users`

Create a user.

**Auth:** Admin

**Request:**
```json
{
  "username": "octocat",
  "email": "user@example.com",
  "full_name": "The Octocat",
  "provider": "github",
  "provider_id": "12345"
}
```

**Response:** `201` -- User object (excludes `provider_id`)

**Errors:**
- `409` -- Duplicate username or `(provider, provider_id)` pair

### `GET /api/v1/users`

List all users, ordered by `created_at` ascending.

**Auth:** Admin

**Response:** `200`
```json
{
  "users": [
    {
      "id": "uuid",
      "username": "octocat",
      "email": "user@example.com",
      "full_name": "The Octocat",
      "status": "active",
      "provider": "github",
      "created_at": "2026-01-01T00:00:00Z",
      "updated_at": "2026-01-01T00:00:00Z"
    }
  ]
}
```

### `GET /api/v1/users/:id`

Get a user by ID. Includes `provider_id` and `team_memberships`.

**Auth:** Admin

**Response:** `200`
```json
{
  "id": "uuid",
  "username": "octocat",
  "email": "user@example.com",
  "full_name": "The Octocat",
  "status": "active",
  "provider": "github",
  "provider_id": "12345",
  "created_at": "2026-01-01T00:00:00Z",
  "updated_at": "2026-01-01T00:00:00Z",
  "team_memberships": [
    {
      "team_id": "uuid",
      "team_name": "backend",
      "team_slug": "backend",
      "joined_at": "2026-01-01T00:00:00Z"
    }
  ]
}
```

### `PUT /api/v1/users/:id`

Update a user. Non-admin users can only update their own `full_name`.

**Auth:** Admin or self

**Request:**
```json
{
  "full_name": "New Name",
  "status": "blocked"
}
```

Both fields are optional. Only admins can set `status`.

**Response:** `200` -- Updated user object

**Errors:**
- `403` -- Non-admin updating another user, or non-admin setting `status`

---

## API Keys

### `GET /api/v1/keys`

List API keys. Admins see all keys; users see their own. Workspace tokens
cannot use this endpoint.

**Auth:** Admin or User API key

**Response:** `200`
```json
{
  "keys": [
    {
      "key_id": "abcd1234",
      "user_id": "uuid",
      "created_at": "2026-01-01T00:00:00Z",
      "expires_at": "2026-04-01T00:00:00Z",
      "revoked_at": null
    }
  ]
}
```

### `POST /api/v1/keys/:key_id/refresh`

Rotate an API key. Generates a new secret while keeping the same `key_id`.

**Auth:** Admin or key owner

**Response:** `200`
```json
{
  "key_id": "abcd1234",
  "user_id": "uuid",
  "secret": "new_plaintext_secret",
  "token": "af_abcd1234_new_plaintext_secret",
  "created_at": "2026-01-01T00:00:00Z",
  "expires_at": "2026-04-01T00:00:00Z"
}
```

### `DELETE /api/v1/keys/:key_id`

Revoke an API key. Idempotent for already-revoked keys.

**Auth:** Admin or key owner

**Response:** `204` No Content

---

## Teams

All team endpoints require admin authentication.

### `POST /api/v1/teams`

Create a team.

**Auth:** Admin

**Request:**
```json
{
  "name": "Backend",
  "slug": "backend",
  "url": "https://github.com/orgs/example/teams/backend"
}
```

`name` and `slug` are required. `url` is optional (must be a valid HTTP/HTTPS
URL if provided).

**Response:** `201`

**Errors:**
- `409` -- Duplicate `name` or `slug`
- `422` -- Validation failure

### `GET /api/v1/teams`

List teams. Returns active teams by default.

**Auth:** Admin

**Query parameters:**
- `include_archived=true` -- Include archived teams

**Response:** `200` -- Array of team objects

### `GET /api/v1/teams/:id`

Get a team by ID.

**Auth:** Admin

**Response:** `200`

### `POST /api/v1/teams/:id/archive`

Archive a team.

**Auth:** Admin

**Response:** `200` -- Updated team

**Errors:** `409` -- Already archived

### `POST /api/v1/teams/:id/reactivate`

Reactivate an archived team.

**Auth:** Admin

**Response:** `200` -- Updated team

**Errors:** `409` -- Already active

### `DELETE /api/v1/teams/:id`

Delete a team. The team must be archived first.

**Auth:** Admin

**Response:** `204` No Content

**Errors:** `409` -- Not archived

### `POST /api/v1/teams/:id/members`

Add a member to a team. Idempotent.

**Auth:** Admin

**Request:**
```json
{"user_id": "uuid"}
```

**Response:** `200`

**Errors:**
- `404` -- Team or user not found
- `409` -- Team is archived

### `GET /api/v1/teams/:id/members`

List team members, ordered by `joined_at` ascending.

**Auth:** Admin

**Response:** `200` -- Array of member objects

---

## Workspaces

### `POST /api/v1/workspaces`

Create a workspace. Only user API keys can create workspaces (admin tokens and
workspace tokens are rejected with 403).

**Auth:** User API key

**Request:**
```json
{
  "slug": "my-workspace",
  "git_url": "https://github.com/org/repo.git",
  "branch": "feature/work",
  "team_id": "uuid"
}
```

`slug` and `git_url` are required. `branch` and `team_id` are optional.

Slug validation: 3-64 characters, starts with a lowercase letter, lowercase
alphanumeric and hyphens only, no trailing hyphen.

Git URL validation: HTTPS (`https://...`) or SCP-style SSH (`git@host:path`),
max 2048 characters.

**Response:** `201`

**Errors:** `409` -- Duplicate slug

### `GET /api/v1/workspaces`

List workspaces. Admins see all; users see their own. Workspace tokens cannot
use this endpoint.

**Auth:** Admin or User API key

**Response:** `200` -- Array of workspace objects

### `GET /api/v1/workspaces/:slug`

Get a workspace by slug.

**Auth:** Admin (any workspace), Owner (own workspace), or Workspace token
(scoped to that workspace)

**Response:** `200`

---

## Workspace Tokens

### `POST /api/v1/workspaces/:slug/tokens`

Create a workspace token.

**Auth:** Workspace owner or Admin

**Request:**
```json
{
  "label": "ci-read",
  "expires": 30
}
```

Both fields are optional. `expires` is in days; allowed values: `0` (no
expiry), `30` (default), `60`, `90`.

**Response:** `201`
```json
{
  "token": "af_wt_abcd1234_plaintext_secret",
  "token_id": "abcd1234",
  "label": "ci-read",
  "expires_at": "2026-02-01T00:00:00Z",
  "created_at": "2026-01-01T00:00:00Z"
}
```

The `token` field (including the plaintext secret) is returned only once.

### `GET /api/v1/workspaces/:slug/tokens`

List workspace tokens. Returns metadata only (no secrets).

**Auth:** Workspace owner or Admin

**Response:** `200`
```json
[
  {
    "token_id": "abcd1234",
    "label": "ci-read",
    "created_at": "2026-01-01T00:00:00Z",
    "expires_at": "2026-02-01T00:00:00Z",
    "revoked_at": null
  }
]
```

### `DELETE /api/v1/workspaces/:slug/tokens/:token_id`

Revoke a workspace token. Idempotent for already-revoked tokens.

**Auth:** Workspace owner or Admin

**Response:** `204` No Content
