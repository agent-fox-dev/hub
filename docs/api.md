# af-hub REST API Reference

This document covers every HTTP endpoint exposed by af-hub, including
authentication requirements, request and response body formats, status codes,
and error handling.

## Table of Contents

- [Authentication](#authentication)
- [Error Format](#error-format)
- [Health Check Endpoints](#health-check-endpoints)
- [Auth Endpoints](#auth-endpoints)
- [User Management Endpoints](#user-management-endpoints)
- [Workspace Management Endpoints](#workspace-management-endpoints)
- [API Key Management Endpoints](#api-key-management-endpoints)

---

## Authentication

All endpoints under `/api/v1/*` (except `/api/v1/auth/*`) require a Bearer
token in the `Authorization` header:

```
Authorization: Bearer <token>
```

Two token types are supported:

| Type | Format | Scope |
|------|--------|-------|
| Admin token | `af_admin_<hex>` | Full access to all endpoints and all workspaces |
| API key | `af_<key_id>_<secret>` | Scoped to a workspace with a role (admin, editor, or reader) |

**Roles:**

| Role | Permissions |
|------|-------------|
| `admin` | Full access to all endpoints across all workspaces |
| `editor` | Read/write within assigned workspace; can create and manage API keys |
| `reader` | Read-only within assigned workspace; can list own API keys |

Blocked users are rejected with `403 Forbidden` regardless of token validity.

---

## Error Format

All API errors use a standardized JSON envelope:

```json
{
  "error": {
    "code": "<HTTP_STATUS>",
    "message": "<human-readable description>"
  }
}
```

**Status code reference:**

| Code | Meaning |
|------|---------|
| `400` | Bad request — malformed input, missing required fields, or invalid values |
| `401` | Unauthorized — missing, malformed, invalid, revoked, or expired token |
| `403` | Forbidden — valid token but insufficient permissions or user is blocked |
| `404` | Not found — referenced resource does not exist |
| `409` | Conflict — duplicate resource (username, slug, provider identity, etc.) |
| `413` | Payload too large — request body exceeds the configured size limit |
| `500` | Internal server error — generic message; internal details are never exposed |

**Example error response (401):**

```json
{
  "error": {
    "code": "401",
    "message": "missing or malformed token"
  }
}
```

---

## Health Check Endpoints

### GET /healthz

Liveness probe. Returns OK unconditionally without touching the database.

**Auth required:** No

**Response:**

```json
{
  "status": "ok"
}
```

| Status | Description |
|--------|-------------|
| `200` | Server is alive |

---

### GET /readyz

Readiness probe. Checks database connectivity with a 2-second timeout.

**Auth required:** No

**Response (ready):**

```json
{
  "status": "ready"
}
```

**Response (not ready):**

```json
{
  "status": "not ready"
}
```

| Status | Description |
|--------|-------------|
| `200` | Server is ready to accept requests |
| `503` | Database is unreachable |

---

## Auth Endpoints

Auth endpoints are public and do not require a Bearer token.

### GET /api/v1/auth/providers

Lists all configured OAuth providers. No secrets or credentials are exposed.

**Auth required:** No

**Response:**

```json
[
  {
    "name": "github",
    "authorize_url": "https://github.com/login/oauth/authorize"
  }
]
```

| Status | Description |
|--------|-------------|
| `200` | Array of provider objects |

---

### POST /api/v1/auth/callback

Exchanges an OAuth authorization code for a user record. Creates a new user
with `status=active` if no matching `(provider, provider_id)` exists, or
updates the existing user's `username` and `email`. A blocked user's status
is never changed to active by this endpoint.

**Auth required:** No

**Request body:**

```json
{
  "provider": "github",
  "code": "authorization_code_from_oauth",
  "redirect_uri": "http://localhost:9999/callback"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `provider` | string | Yes | Name of the configured OAuth provider |
| `code` | string | Yes | Authorization code received from the OAuth provider |
| `redirect_uri` | string | Yes | Redirect URI used during the OAuth flow |

**Response (200 — new user):**

```json
{
  "id": "uuid",
  "username": "octocat",
  "email": "octocat@example.com",
  "full_name": "",
  "provider": "github",
  "provider_id": "12345",
  "status": "active",
  "created_at": "2026-01-15T09:00:00Z",
  "updated_at": "2026-01-15T09:00:00Z"
}
```

**Response (200 — blocked user):**

```json
{
  "id": "uuid",
  "username": "blocked_user",
  "email": "blocked@example.com",
  "full_name": "",
  "provider": "github",
  "provider_id": "67890",
  "status": "blocked",
  "created_at": "2026-01-10T09:00:00Z",
  "updated_at": "2026-01-15T09:00:00Z"
}
```

| Status | Description |
|--------|-------------|
| `200` | User object (new or existing) |
| `400` | Missing required fields, unsupported provider, or authorization code exchange failed |
| `413` | Request body exceeds configured size limit |
| `500` | Identity provider timeout or internal server error |

**Error examples:**

```json
{"error": {"code": "400", "message": "missing required fields"}}
{"error": {"code": "400", "message": "unsupported provider"}}
{"error": {"code": "400", "message": "authorization code exchange failed"}}
{"error": {"code": "500", "message": "identity provider timeout"}}
```

---

## User Management Endpoints

### POST /api/v1/users

Creates a new user record with `status=active`.

**Auth required:** Admin token

**Request body:**

```json
{
  "username": "newuser",
  "email": "newuser@example.com",
  "provider": "github",
  "provider_id": "gh_99999"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `username` | string | Yes | Unique username |
| `email` | string | Yes | User's email address |
| `provider` | string | Yes | Identity provider name |
| `provider_id` | string | Yes | Unique ID from the identity provider |

**Response (201):**

```json
{
  "id": "uuid",
  "username": "newuser",
  "email": "newuser@example.com",
  "full_name": "",
  "provider": "github",
  "provider_id": "gh_99999",
  "status": "active",
  "created_at": "2026-01-15T09:00:00Z",
  "updated_at": "2026-01-15T09:00:00Z"
}
```

| Status | Description |
|--------|-------------|
| `201` | User created |
| `400` | Missing required fields |
| `409` | Duplicate username or `(provider, provider_id)` pair |
| `500` | Internal server error |

**Error examples:**

```json
{"error": {"code": "400", "message": "missing required fields"}}
{"error": {"code": "409", "message": "duplicate username or provider identity"}}
```

---

### GET /api/v1/users

Lists all user records.

**Auth required:** Admin token

**Response (200):**

```json
[
  {
    "id": "uuid",
    "username": "alice",
    "email": "alice@example.com",
    "full_name": "Alice Smith",
    "provider": "github",
    "provider_id": "gh_001",
    "status": "active",
    "created_at": "2026-01-15T09:00:00Z",
    "updated_at": "2026-01-15T09:00:00Z"
  }
]
```

| Status | Description |
|--------|-------------|
| `200` | Array of user objects |
| `500` | Internal server error |

---

### GET /api/v1/users/:id

Retrieves a user by ID, including their workspace memberships and roles.

**Auth required:** Admin token

**Path parameters:**

| Parameter | Description |
|-----------|-------------|
| `id` | User ID (UUID) |

**Response (200):**

```json
{
  "id": "uuid",
  "username": "alice",
  "email": "alice@example.com",
  "full_name": "Alice Smith",
  "provider": "github",
  "provider_id": "gh_001",
  "status": "active",
  "created_at": "2026-01-15T09:00:00Z",
  "updated_at": "2026-01-15T09:00:00Z",
  "memberships": [
    {
      "user_id": "uuid",
      "workspace_id": "ws_uuid",
      "role": "editor",
      "created_at": "2026-01-15T10:00:00Z",
      "granted_by": "admin_uuid"
    }
  ]
}
```

| Status | Description |
|--------|-------------|
| `200` | User object with memberships |
| `404` | User not found |
| `500` | Internal server error |

**Error example:**

```json
{"error": {"code": "404", "message": "user not found"}}
```

---

### PUT /api/v1/users/:id

Updates a user's `full_name` and/or `status`. Non-admin users can only update
their own `full_name`; attempts to change `status` are rejected with `403`.
Only admins can change the `status` field.

**Auth required:** Admin token, or API key where `:id` matches the
authenticated user's own ID (self-update of `full_name` only)

**Path parameters:**

| Parameter | Description |
|-----------|-------------|
| `id` | User ID (UUID) |

**Request body:**

```json
{
  "full_name": "New Display Name",
  "status": "blocked"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `full_name` | string | No | Display name (any authenticated user can update their own) |
| `status` | string | No | `active` or `blocked` (admin only) |

**Response (200):**

```json
{
  "id": "uuid",
  "username": "alice",
  "email": "alice@example.com",
  "full_name": "New Display Name",
  "provider": "github",
  "provider_id": "gh_001",
  "status": "blocked",
  "created_at": "2026-01-15T09:00:00Z",
  "updated_at": "2026-01-15T12:00:00Z"
}
```

| Status | Description |
|--------|-------------|
| `200` | Updated user object |
| `403` | Non-admin attempting to change status |
| `404` | User not found |
| `500` | Internal server error |

**Error examples:**

```json
{"error": {"code": "403", "message": "insufficient permissions"}}
{"error": {"code": "404", "message": "user not found"}}
```

---

## Workspace Management Endpoints

All workspace endpoints require admin authentication.

### POST /api/v1/workspaces

Creates a new workspace. Validates slug format and URL scheme.

**Auth required:** Admin token

**Request body:**

```json
{
  "name": "My Workspace",
  "slug": "my-workspace",
  "url": "https://myworkspace.example.com"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Human-readable workspace name (must be unique) |
| `slug` | string | Yes | URL-safe identifier: lowercase alphanumeric and hyphens, 3-64 characters, starts with a letter, does not end with a hyphen (must be unique) |
| `url` | string | Yes | Workspace URL with `http` or `https` scheme |

**Response (201):**

```json
{
  "id": "uuid",
  "name": "My Workspace",
  "slug": "my-workspace",
  "url": "https://myworkspace.example.com",
  "status": "active",
  "created_at": "2026-01-15T09:00:00Z",
  "created_by": "admin_uuid"
}
```

| Status | Description |
|--------|-------------|
| `201` | Workspace created |
| `400` | Missing fields, invalid slug format, or invalid URL (non-http/https scheme or missing host) |
| `409` | Workspace name or slug already exists |
| `500` | Internal server error |

**Error examples:**

```json
{"error": {"code": "400", "message": "invalid slug or URL format"}}
{"error": {"code": "409", "message": "workspace name or slug already exists"}}
```

---

### GET /api/v1/workspaces

Lists workspaces. Excludes archived workspaces by default.

**Auth required:** Admin token

**Query parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `include_archived` | string | `false` | Set to `true` to include archived workspaces |

**Response (200):**

```json
[
  {
    "id": "uuid",
    "name": "My Workspace",
    "slug": "my-workspace",
    "url": "https://myworkspace.example.com",
    "status": "active",
    "created_at": "2026-01-15T09:00:00Z",
    "created_by": "admin_uuid"
  }
]
```

| Status | Description |
|--------|-------------|
| `200` | Array of workspace objects |
| `500` | Internal server error |

---

### POST /api/v1/workspaces/:id/archive

Marks an active workspace as archived.

**Auth required:** Admin token

**Path parameters:**

| Parameter | Description |
|-----------|-------------|
| `id` | Workspace ID (UUID) |

**Response (200):**

```json
{
  "id": "uuid",
  "name": "My Workspace",
  "slug": "my-workspace",
  "url": "https://myworkspace.example.com",
  "status": "archived",
  "created_at": "2026-01-15T09:00:00Z",
  "created_by": "admin_uuid"
}
```

| Status | Description |
|--------|-------------|
| `200` | Updated workspace object with `status=archived` |
| `400` | Workspace is already archived |
| `404` | Workspace not found |
| `500` | Internal server error |

**Error examples:**

```json
{"error": {"code": "400", "message": "workspace is already archived"}}
{"error": {"code": "404", "message": "workspace not found"}}
```

---

### POST /api/v1/workspaces/:id/reactivate

Reactivates an archived workspace.

**Auth required:** Admin token

**Path parameters:**

| Parameter | Description |
|-----------|-------------|
| `id` | Workspace ID (UUID) |

**Response (200):**

```json
{
  "id": "uuid",
  "name": "My Workspace",
  "slug": "my-workspace",
  "url": "https://myworkspace.example.com",
  "status": "active",
  "created_at": "2026-01-15T09:00:00Z",
  "created_by": "admin_uuid"
}
```

| Status | Description |
|--------|-------------|
| `200` | Updated workspace object with `status=active` |
| `400` | Workspace is not archived |
| `404` | Workspace not found |
| `500` | Internal server error |

**Error examples:**

```json
{"error": {"code": "400", "message": "workspace is not archived"}}
{"error": {"code": "404", "message": "workspace not found"}}
```

---

### DELETE /api/v1/workspaces/:id

Permanently deletes an archived workspace. Cascades deletion to all
memberships and API keys scoped to the workspace. The deletion is
transactional — either all records are removed, or the operation is fully
rolled back.

**Auth required:** Admin token

**Path parameters:**

| Parameter | Description |
|-----------|-------------|
| `id` | Workspace ID (UUID) |

**Response (200):**

```json
{
  "message": "workspace deleted"
}
```

| Status | Description |
|--------|-------------|
| `200` | Workspace and all associated records deleted |
| `400` | Workspace must be archived before deletion |
| `404` | Workspace not found |
| `500` | Internal server error (transaction rolled back, no partial state) |

**Error examples:**

```json
{"error": {"code": "400", "message": "workspace must be archived before deletion"}}
{"error": {"code": "404", "message": "workspace not found"}}
```

---

### POST /api/v1/workspaces/:id/members

Adds a user to the workspace or updates an existing membership's role.

**Auth required:** Admin token

**Path parameters:**

| Parameter | Description |
|-----------|-------------|
| `id` | Workspace ID (UUID) |

**Request body:**

```json
{
  "user_id": "user_uuid",
  "role": "editor"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `user_id` | string | Yes | ID of the user to add or update |
| `role` | string | Yes | Role to assign: `admin`, `editor`, or `reader` |

**Response (200):**

```json
{
  "user_id": "user_uuid",
  "workspace_id": "ws_uuid",
  "role": "editor",
  "created_at": "2026-01-15T10:00:00Z",
  "granted_by": "admin_uuid"
}
```

| Status | Description |
|--------|-------------|
| `200` | Membership created or updated |
| `400` | Missing required fields |
| `404` | Workspace or user not found |
| `500` | Internal server error |

**Error examples:**

```json
{"error": {"code": "404", "message": "workspace not found"}}
{"error": {"code": "404", "message": "user not found"}}
```

---

### GET /api/v1/workspaces/:id/members

Lists all members of a workspace.

**Auth required:** Admin token

**Path parameters:**

| Parameter | Description |
|-----------|-------------|
| `id` | Workspace ID (UUID) |

**Response (200):**

```json
[
  {
    "user_id": "user_uuid",
    "workspace_id": "ws_uuid",
    "role": "editor",
    "created_at": "2026-01-15T10:00:00Z",
    "granted_by": "admin_uuid"
  }
]
```

| Status | Description |
|--------|-------------|
| `200` | Array of membership objects |
| `404` | Workspace not found |
| `500` | Internal server error |

---

## API Key Management Endpoints

### POST /api/v1/keys

Creates a new API key scoped to a workspace. The authenticated user must be
a member of the workspace. The plaintext key is returned exactly once in this
response and is never retrievable again.

**Auth required:** Admin token or API key with `editor` or `admin` role

**Request body:**

```json
{
  "workspace_id": "ws_uuid",
  "label": "ci-deploy-key",
  "expires": 30
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `workspace_id` | string | Yes | Target workspace ID |
| `label` | string | Yes | Human-readable name for the key |
| `expires` | integer | Yes | Expiry in days: `0` (never), `30`, `60`, or `90` |

**Response (201):**

```json
{
  "key": "af_a1b2c3d4_e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4",
  "key_id": "a1b2c3d4",
  "role": "editor",
  "expires_at": "2026-02-14T09:00:00Z"
}
```

> **Note:** The `key` field contains the full plaintext token. Store it
> securely — it cannot be retrieved again. Only the SHA-256 hash of the
> secret is stored in the database.

| Status | Description |
|--------|-------------|
| `201` | API key created; plaintext returned once |
| `400` | Missing fields or invalid `expires` value (must be 0, 30, 60, or 90) |
| `403` | Not a member of the specified workspace, or insufficient role |
| `404` | Workspace not found |
| `500` | Internal server error |

**Error examples:**

```json
{"error": {"code": "400", "message": "expires must be 0, 30, 60, or 90"}}
{"error": {"code": "403", "message": "not a member of this workspace"}}
{"error": {"code": "403", "message": "insufficient permissions"}}
{"error": {"code": "404", "message": "workspace not found"}}
```

---

### GET /api/v1/keys

Lists API keys. Admin tokens see all keys across all users; API key tokens
see only the authenticated user's own keys. Expired and revoked keys are
included. Plaintext secrets are never returned.

**Auth required:** Any authenticated user

**Response (200):**

```json
[
  {
    "id": "uuid",
    "key_id": "a1b2c3d4",
    "user_id": "user_uuid",
    "workspace_id": "ws_uuid",
    "role": "editor",
    "label": "ci-deploy-key",
    "expires_at": "2026-02-14T09:00:00Z",
    "revoked_at": null,
    "created_at": "2026-01-15T09:00:00Z"
  }
]
```

| Status | Description |
|--------|-------------|
| `200` | Array of API key objects (no plaintext secrets) |
| `500` | Internal server error |

---

### POST /api/v1/keys/:key_id/refresh

Generates a new secret for an existing API key. The old secret is
immediately invalidated. The new plaintext key is returned exactly once.

**Auth required:** Admin token or API key with `editor` or `admin` role
(non-admin users can only refresh their own keys)

**Path parameters:**

| Parameter | Description |
|-----------|-------------|
| `key_id` | The key identifier (not the full token) |

**Response (200):**

```json
{
  "key": "af_a1b2c3d4_new_secret_hex_string",
  "key_id": "a1b2c3d4"
}
```

| Status | Description |
|--------|-------------|
| `200` | New plaintext key returned once |
| `404` | API key not found or belongs to another user |
| `500` | Internal server error |

**Error example:**

```json
{"error": {"code": "404", "message": "API key not found"}}
```

---

### DELETE /api/v1/keys/:key_id

Permanently revokes an API key by setting `revoked_at` to the current
timestamp. The key can no longer be used for authentication.

**Auth required:** Admin token or API key with `editor` or `admin` role
(non-admin users can only revoke their own keys)

**Path parameters:**

| Parameter | Description |
|-----------|-------------|
| `key_id` | The key identifier (not the full token) |

**Response (200):**

```json
{
  "message": "key revoked"
}
```

| Status | Description |
|--------|-------------|
| `200` | Key revoked |
| `404` | API key not found or belongs to another user |
| `500` | Internal server error |

**Error example:**

```json
{"error": {"code": "404", "message": "API key not found"}}
```
