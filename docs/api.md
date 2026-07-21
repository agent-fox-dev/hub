# af-hub REST API Reference

This document covers all REST API endpoints provided by the af-hub service,
including workspace management endpoints and apikit-provided endpoints for
authentication, user management, API keys, tokens, organizations, and
administration.

## Authentication

All API endpoints require authentication via one of the following methods:

- **API Key**: A server-side credential associated with a user account. Passed
  in the `Authorization: Bearer <api-key>` header. API keys have full access
  to the owner's resources.
- **Personal Access Token (PAT)**: A scoped credential issued to a user for
  delegated API access. Passed in the `Authorization: Bearer <token>` header.
  PATs are restricted to the permission scopes granted at creation time.
- **Admin Token**: A special credential with cross-user administrative access.
  Passed in the `Authorization: Bearer <admin-token>` header.

Unauthenticated requests receive HTTP 401.

---

## Permission Scopes

PATs are granted one or more permission scopes that control which endpoints
they can access. The following scopes are available for workspace operations:

| Scope | Description | Authorized Endpoints |
|-------|-------------|---------------------|
| `workspaces:read` | List and view access to the PAT owner's workspaces | GET /api/v1/workspaces, GET /api/v1/workspaces/:slug |
| `workspaces:create` | Create workspaces; implies read access | POST /api/v1/workspaces, GET /api/v1/workspaces, GET /api/v1/workspaces/:slug |
| `workspaces:write` | Update, archive, and reactivate workspaces; implies read access | PATCH /api/v1/workspaces/:slug, POST /api/v1/workspaces/:slug/archive, POST /api/v1/workspaces/:slug/reactivate, GET /api/v1/workspaces, GET /api/v1/workspaces/:slug |
| `workspaces:delete` | Delete archived workspaces owned by the PAT's user; does **not** imply read access | DELETE /api/v1/workspaces/:slug |

### Implied Permissions

- `workspaces:create` implies `workspaces:read` — a PAT with create scope can
  also list and view workspaces.
- `workspaces:write` implies `workspaces:read` — a PAT with write scope can
  also list and view workspaces.
- `workspaces:delete` does **not** imply read access — a PAT with only
  `workspaces:delete` cannot list or view workspaces.

### Anti-Enumeration Policy

When a PAT lacks the required scope for an endpoint, or the requested
workspace is not owned by the PAT's user, the API returns HTTP 404 (not 403)
to avoid disclosing the existence of resources.

---

## Workspace Response Schema

All workspace endpoints that return workspace data use the following JSON
schema:

```json
{
  "slug": "my-project",
  "git_url": "https://github.com/user/repo.git",
  "branch": "main",
  "owner_id": "uuid-string",
  "org_id": "uuid-string-or-null",
  "status": "active",
  "display_name": "My Project",
  "description": "A description of the workspace",
  "created_at": "2024-01-01T00:00:00Z",
  "updated_at": "2024-01-01T00:00:00Z"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `slug` | string | Immutable globally unique URL-safe identifier |
| `git_url` | string | HTTPS or SSH URL of the git repository; immutable after creation |
| `branch` | string or null | Git ref associated with the workspace; immutable after creation |
| `owner_id` | string (UUID) | User who owns the workspace |
| `org_id` | string (UUID) or null | Organization the workspace is associated with; nullable |
| `status` | string | Lifecycle state: `"active"` or `"archived"` |
| `display_name` | string | Human-readable label; defaults to slug value when not set; never null or empty |
| `description` | string | Free-form text describing the workspace; defaults to empty string; never null |
| `created_at` | string (RFC 3339) | Timestamp of workspace creation; immutable |
| `updated_at` | string (RFC 3339) | Timestamp of last modification |

### Error Response Schema

Error responses use the apikit error envelope format:

```json
{
  "error": {
    "code": 400,
    "message": "description of the error"
  }
}
```

---

## Workspace Endpoints

### POST /api/v1/workspaces

Create a new workspace.

**Authentication:** API Key, or PAT with `workspaces:create` scope.
Admin tokens are forbidden from creating workspaces (returns 403) because a
workspace requires a real user as owner.

**Request Body:**

```json
{
  "slug": "my-project",
  "git_url": "https://github.com/user/repo.git",
  "branch": "main",
  "org_id": "uuid-string",
  "display_name": "My Project",
  "description": "A description of the workspace"
}
```

| Field | Required | Type | Constraints |
|-------|----------|------|-------------|
| `slug` | yes | string | Globally unique, URL-safe identifier |
| `git_url` | yes | string | Valid HTTPS or SSH git URL |
| `branch` | no | string | Git ref; defaults to null |
| `org_id` | no | string (UUID) | Must reference an org the owner is a member of |
| `display_name` | no | string | Max 128 characters; defaults to slug value if omitted or empty |
| `description` | no | string | Max 1024 characters; defaults to empty string if omitted |

**Response:** HTTP 201 Created with workspace JSON.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Missing required fields (`slug`, `git_url`), or `display_name` exceeds 128 characters, or `description` exceeds 1024 characters |
| 401 | Unauthenticated request |
| 403 | Admin token attempted to create a workspace; PAT lacks `workspaces:create` scope |
| 409 | A workspace with the given `slug` already exists |
| 500 | Internal server error (e.g., database error, org membership check failure) |

---

### GET /api/v1/workspaces

List workspaces accessible to the authenticated user.

**Authentication:** API Key, or PAT with read access (`workspaces:read`,
`workspaces:create`, or `workspaces:write`). Admin tokens list all workspaces.

**Query Parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `include_archived` | boolean | `false` | When `true`, includes archived workspaces in the listing |

**Response:** HTTP 200 OK with a JSON array of workspace objects.

- Non-admin users see only their own workspaces.
- Admin users see all workspaces.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 404 | PAT lacks read access (anti-enumeration) |

---

### GET /api/v1/workspaces/:slug

Get a single workspace by slug.

**Authentication:** API Key, or PAT with read access (`workspaces:read`,
`workspaces:create`, or `workspaces:write`). Admin tokens can access any
workspace.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug to retrieve |

**Response:** HTTP 200 OK with workspace JSON.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 404 | Workspace not found, or PAT lacks read access, or workspace not owned by the authenticated user (anti-enumeration) |

---

### PATCH /api/v1/workspaces/:slug

Update mutable properties of an existing workspace. This endpoint uses
**partial update semantics**: only fields included in the request body are
modified. Omitted fields retain their current values.

**Authentication:** API Key, or PAT with `workspaces:write` scope. Admin
tokens can update any workspace.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug to update |

**Request Body:**

The request body is a JSON object containing one or more of the following
mutable fields. At least one field must be provided.

```json
{
  "display_name": "New Display Name",
  "description": "Updated description",
  "org_id": "uuid-string"
}
```

| Field | Type | Constraints | Null Behavior |
|-------|------|-------------|---------------|
| `display_name` | string or null | Max 128 characters | Setting to `null` clears the display name back to the workspace slug |
| `description` | string or null | Max 1024 characters | Setting to `null` clears the description to an empty string |
| `org_id` | string (UUID) or null | Must reference an org the owner is a member of | Setting to `null` removes the organization association |

**Partial Update Behavior:**

- Only explicitly provided fields are updated; omitted fields are not modified.
- Immutable fields (`slug`, `git_url`, `branch`, `owner_id`) cannot be set
  via this endpoint. Including an immutable field in the request body returns
  HTTP 400.
- The `updated_at` timestamp is automatically advanced on every successful
  update.

**Response:** HTTP 200 OK with the full updated workspace JSON.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Empty body (no fields provided); workspace is archived (must reactivate first); `display_name` exceeds 128 characters; `description` exceeds 1024 characters; request includes immutable fields |
| 401 | Unauthenticated request |
| 403 | Workspace owner is not a member of the organization specified in `org_id` |
| 404 | Workspace not found; PAT lacks `workspaces:write` scope; workspace not owned by the authenticated user (anti-enumeration) |
| 500 | Internal server error (e.g., org membership check timeout or failure) |

---

### POST /api/v1/workspaces/:slug/archive

Archive a workspace. Archived workspaces are read-only; all state is
preserved and the workspace can be reactivated later.

**Authentication:** API Key, or PAT with `workspaces:write` scope. Admin
tokens can archive any workspace.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug to archive |

**Response:** HTTP 200 OK with the updated workspace JSON (status = `"archived"`).

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Workspace is already archived |
| 401 | Unauthenticated request |
| 404 | Workspace not found; PAT lacks `workspaces:write` scope; workspace not owned by the authenticated user (anti-enumeration) |

---

### POST /api/v1/workspaces/:slug/reactivate

Reactivate an archived workspace, restoring it to active status.

**Authentication:** API Key, or PAT with `workspaces:write` scope. Admin
tokens can reactivate any workspace.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug to reactivate |

**Response:** HTTP 200 OK with the updated workspace JSON (status = `"active"`).

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Workspace is already active |
| 401 | Unauthenticated request |
| 404 | Workspace not found; PAT lacks `workspaces:write` scope; workspace not owned by the authenticated user (anti-enumeration) |

---

### DELETE /api/v1/workspaces/:slug

Permanently delete a workspace. Only archived workspaces can be deleted.

**Authentication:** API Key, or PAT with `workspaces:delete` scope. Admin
tokens can delete any workspace.

**Path Parameters:**

| Parameter | Description |
|-----------|-------------|
| `:slug` | The workspace slug to delete |

**Response:** HTTP 204 No Content on successful deletion.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Workspace is not archived (must archive before deleting) |
| 401 | Unauthenticated request |
| 404 | Workspace not found; PAT lacks `workspaces:delete` scope; workspace not owned by the authenticated user (anti-enumeration) |

---

## Non-Workspace Endpoints (apikit-provided)

The following endpoints are provided by the `apikit` library and are available
alongside the workspace endpoints. All endpoints use the same authentication
mechanisms and error envelope format described above.

### Login

#### POST /login

Authenticate a user and obtain an API key.

**Authentication:** None (public endpoint).

**Request Body:**

```json
{
  "email": "user@example.com",
  "password": "secret"
}
```

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `email` | yes | string | User's email address |
| `password` | yes | string | User's password |

**Response:** HTTP 200 OK with a JSON object containing the API key and user
information.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Missing or invalid fields |
| 401 | Invalid credentials |

---

### User

#### GET /user

Get the profile of the authenticated user.

**Authentication:** API Key, PAT, or Admin Token.

**Response:** HTTP 200 OK with user profile JSON.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |

#### PUT /user

Update the authenticated user's profile.

**Authentication:** API Key or Admin Token.

**Response:** HTTP 200 OK with updated user profile JSON.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Invalid request body |
| 401 | Unauthenticated request |

---

### Keys

#### GET /user/keys

List API keys for the authenticated user.

**Authentication:** API Key or Admin Token.

**Response:** HTTP 200 OK with a JSON array of API key metadata.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |

#### POST /user/keys

Create a new API key for the authenticated user.

**Authentication:** API Key or Admin Token.

**Request Body:**

```json
{
  "description": "My API Key"
}
```

**Response:** HTTP 201 Created with the new API key (the full key value is
only returned once at creation time).

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Invalid request body |
| 401 | Unauthenticated request |

#### DELETE /user/keys/:id

Revoke an API key.

**Authentication:** API Key or Admin Token.

**Response:** HTTP 204 No Content.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 404 | Key not found or not owned by user |

---

### Tokens

#### GET /user/tokens

List personal access tokens (PATs) for the authenticated user.

**Authentication:** API Key or Admin Token.

**Response:** HTTP 200 OK with a JSON array of token metadata, including
scopes granted to each token.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |

#### POST /user/tokens

Create a new personal access token with specific permission scopes.

**Authentication:** API Key or Admin Token.

**Request Body:**

```json
{
  "description": "CI token",
  "scopes": ["workspaces:read", "workspaces:create"]
}
```

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `description` | yes | string | Human-readable label for the token |
| `scopes` | yes | string[] | Permission scopes to grant (see Permission Scopes section) |

**Response:** HTTP 201 Created with the new token (the full token value is
only returned once at creation time).

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Invalid request body, missing fields, or invalid scopes |
| 401 | Unauthenticated request |

#### DELETE /user/tokens/:id

Revoke a personal access token.

**Authentication:** API Key or Admin Token.

**Response:** HTTP 204 No Content.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 404 | Token not found or not owned by user |

---

### Orgs

#### GET /user/orgs

List organizations the authenticated user belongs to.

**Authentication:** API Key, PAT, or Admin Token.

**Response:** HTTP 200 OK with a JSON array of organization objects.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |

#### POST /orgs

Create a new organization.

**Authentication:** API Key or Admin Token.

**Request Body:**

```json
{
  "name": "My Organization",
  "slug": "my-org"
}
```

**Response:** HTTP 201 Created with the new organization JSON.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 400 | Invalid request body or missing fields |
| 401 | Unauthenticated request |
| 409 | Organization slug already exists |

#### GET /orgs/:slug

Get organization details by slug.

**Authentication:** API Key, PAT, or Admin Token.

**Response:** HTTP 200 OK with organization JSON.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 404 | Organization not found |

---

### Admin

#### GET /admin/users

List all users (admin only).

**Authentication:** Admin Token.

**Response:** HTTP 200 OK with a JSON array of user objects.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | Non-admin credential |

#### GET /admin/stats

Get system statistics (admin only).

**Authentication:** Admin Token.

**Response:** HTTP 200 OK with system statistics JSON.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | Non-admin credential |

#### DELETE /admin/users/:id

Delete a user account (admin only).

**Authentication:** Admin Token.

**Response:** HTTP 204 No Content.

**Error Codes:**

| Status | Condition |
|--------|-----------|
| 401 | Unauthenticated request |
| 403 | Non-admin credential |
| 404 | User not found |
