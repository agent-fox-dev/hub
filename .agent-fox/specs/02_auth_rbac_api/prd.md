---
spec_id: '02'
spec_name: auth_rbac_api
title: Auth Rbac Api
status: draft
created_at: '2026-07-07T11:22:40.064077+00:00'
updated_at: '2026-07-07T11:22:40.064077+00:00'
owner: ''
source: ".agent-fox/specs/prd.md"
schema_version: 1
---
# Auth, RBAC & API Endpoints

## Intent

Build the authentication, authorization, and complete REST API surface for af-hub. This spec adds the OAuth provider registry, auth middleware, role-based access control, all HTTP handlers for user management, workspace management, workspace lifecycle, workspace membership, and API key management, plus the standardized error response envelope.

After this spec, af-hub is a fully functional API server: operators can authenticate via OAuth, manage users and workspaces, assign roles, and issue scoped API keys — all enforced by RBAC on every endpoint.

## Goals

- Implement a pluggable OAuth provider registry with a common interface, shipping GitHub as the first provider.
- Implement auth middleware that validates admin tokens and API keys on every protected request.
- Implement RBAC enforcement based on the three-role model (admin, editor, reader).
- Implement all HTTP handlers and route registration for: users, workspaces, workspace lifecycle (archive/reactivate/delete), workspace members, and API keys.
- Implement the standardized error response envelope for all API errors.
- Reject blocked users at the middleware level with HTTP 403.
- Ship API documentation at `docs/api.md`.

## Non-goals

- Session-based authentication for the web UI (future spec).
- CLI login or key management commands (covered by spec 03).
- Additional OAuth providers beyond GitHub (extensible but not shipped).
- Rate limiting.
- CORS middleware.

## Functional Requirements

### OAuth provider registry

- The system authenticates users via a pluggable OAuth provider registry. Each provider implements a common interface: authorize URL construction, authorization code exchange for tokens, and user info extraction.
- The first iteration ships with GitHub only. GitHub's well-known URLs are built-in defaults; `authorize_url`, `token_url`, and `userinfo_url` in config are optional overrides.
- Adding a new provider requires registering it in the registry with its URLs and field mappings — no changes to auth middleware or handlers.
- If a provider is removed from `config.toml`, existing users retain their records and API keys. Those users cannot re-authenticate via OAuth until they link to another provider.

### OAuth endpoints

- `GET /api/v1/auth/providers` — List configured OAuth providers. Returns an array of `{"name": "github", "authorize_url": "..."}`. No secrets exposed.
- `POST /api/v1/auth/callback` — Exchange an OAuth authorization code for a user record:
  - Accepts: `{"provider": "github", "code": "abc123", "redirect_uri": "http://localhost:9999/callback"}`.
  - The hub exchanges the code with the identity provider, retrieves user info, and upserts the user: creates if new (`status: active`), updates `username`/`email` if existing.
  - Blocked users are NOT re-activated on OAuth login. The callback returns the user object but the user's `status` remains `blocked`.
  - Returns HTTP 200 with the user object on success.
  - Admin-created users and OAuth-upserted users are the same population. Matching is by `(provider, provider_id)`.

### Auth middleware

- All `/api/v1/*` endpoints (except `/api/v1/auth/*` and health probes) require a Bearer token in the `Authorization` header.
- The middleware identifies the token type by prefix:
  - `af_admin_...` → admin token: hash with SHA-256, compare against `admin_tokens` table. Grants global admin access.
  - `af_<key_id>_<secret>` → API key: extract `key_id`, look up in `api_keys` table, hash `secret` with SHA-256, compare against stored `key_hash`.
- Rejection conditions:
  - Missing or malformed token → HTTP 401.
  - Admin token hash mismatch → HTTP 401.
  - API key not found, hash mismatch, revoked (`revoked_at` set), or expired (`expires_at` in the past) → HTTP 401.
  - Valid token but user `status` is `blocked` → HTTP 403.
- On success, the middleware populates a request context with: user ID, user status, authentication method (admin/api_key), and for API keys: workspace ID and role.

### RBAC enforcement

Three roles:

| Role | Scope | Description |
|------|-------|-------------|
| **admin** | Global | Full access to all endpoints and all workspaces |
| **editor** | Per-workspace | Read/write on resources within assigned workspaces |
| **reader** | Per-workspace | Read-only access within assigned workspaces |

Permission matrix:

| Endpoint | Admin | Editor | Reader |
|----------|-------|--------|--------|
| POST /api/v1/keys (create key) | yes | yes | no |
| GET /api/v1/keys (list keys) | yes | yes | yes |
| POST /api/v1/keys/:key_id/refresh | yes | yes | no |
| DELETE /api/v1/keys/:key_id (revoke) | yes | yes | no |
| POST /api/v1/users (create user) | yes | no | no |
| GET /api/v1/users (list users) | yes | no | no |
| GET /api/v1/users/:id | yes | no | no |
| PUT /api/v1/users/:id (update user) | yes | self | self |
| POST /api/v1/workspaces (create) | yes | no | no |
| GET /api/v1/workspaces (list) | yes | no | no |
| POST /api/v1/workspaces/:id/archive | yes | no | no |
| POST /api/v1/workspaces/:id/reactivate | yes | no | no |
| DELETE /api/v1/workspaces/:id | yes | no | no |
| POST /api/v1/workspaces/:id/members | yes | no | no |
| GET /api/v1/workspaces/:id/members | yes | no | no |

Exception: Any authenticated user can update their own `full_name` via `PUT /api/v1/users/:id`, but only admins can change `status`.

### User management endpoints (admin only)

- `POST /api/v1/users` — Create a user. Accepts `username`, `email`, `provider`, `provider_id`. Returns HTTP 201. Returns HTTP 409 on duplicate `username` or duplicate `(provider, provider_id)`.
- `GET /api/v1/users` — List all users. Returns HTTP 200.
- `GET /api/v1/users/:id` — Get user by ID, including workspace memberships and roles. Returns HTTP 200 or 404.
- `PUT /api/v1/users/:id` — Update `full_name` or `status` (`active` | `blocked`). Returns HTTP 200 or 404.

### Workspace management endpoints (admin only)

- `POST /api/v1/workspaces` — Create a workspace. Accepts `name`, `slug`, `url`. Validates: slug format (lowercase alphanumeric + hyphens, 3–64 chars, starts with letter, doesn't end with hyphen), URL format (scheme + host required, http/https only), name and slug uniqueness. Returns HTTP 201 or 409.
- `GET /api/v1/workspaces` — List all workspaces. Archived workspaces excluded by default; include with `?include_archived=true`. Returns HTTP 200.
- `POST /api/v1/workspaces/:id/archive` — Archive a workspace. Returns HTTP 200 or 404. Returns 400 if already archived.
- `POST /api/v1/workspaces/:id/reactivate` — Reactivate an archived workspace. Returns HTTP 200 or 404. Returns 400 if not archived.
- `DELETE /api/v1/workspaces/:id` — Delete a workspace. Returns HTTP 200 or 404. Returns 400 if workspace is not archived. Cascades: deletes memberships and API keys scoped to the workspace.
- `POST /api/v1/workspaces/:id/members` — Add or update membership. Accepts `user_id`, `role`. Returns HTTP 200 or 404.
- `GET /api/v1/workspaces/:id/members` — List members. Returns HTTP 200 or 404.

### API key management endpoints (authenticated)

- `POST /api/v1/keys` — Create an API key. Accepts `workspace_id`, `label`, `expires` (0/30/60/90 days, default 30). User must be a member of the workspace. Key inherits user's role. Generates `af_<key_id>_<secret>` format. Stores SHA-256 hash of secret. Returns the full key (plaintext secret) exactly once. Expiry calculated as `24h × N` from creation timestamp. `expires: 0` means no expiry (null `expires_at`). Returns HTTP 201 or 400/403/404.
- `GET /api/v1/keys` — List keys. Admin token: all keys across all users. API key: only the authenticated user's keys. Includes expired keys (visible for reference). Returns HTTP 200.
- `POST /api/v1/keys/:key_id/refresh` — Generate new secret for existing key (same key_id). Returns the new full key. Returns HTTP 200 or 404.
- `DELETE /api/v1/keys/:key_id` — Permanently revoke a key (sets `revoked_at`). Returns HTTP 200 or 404.

### Error response envelope

All API errors use: `{"error": {"code": "<HTTP_STATUS>", "message": "Human-readable description"}}`.

| Status | Meaning |
|--------|---------|
| 400 | Bad request — malformed JSON, missing required fields, validation failure |
| 401 | Unauthorized — missing, invalid, expired, or revoked token |
| 403 | Forbidden — valid token but insufficient role, or user is blocked |
| 404 | Not found — resource does not exist |
| 409 | Conflict — unique constraint violation |
| 413 | Payload too large — request body exceeds limit |
| 500 | Internal server error |

### Documentation

- `docs/api.md` — Complete REST API documentation: every endpoint with method, path, auth requirements, request/response bodies (JSON examples), status codes, and error format. Update `README.md` to link to it.

## Technical Boundaries

- **Language:** Go (1.22+)
- **HTTP framework:** Echo (`github.com/labstack/echo/v4`)
- **OAuth token exchange:** Standard HTTP client, no external OAuth library required
- **Token hashing:** SHA-256 for admin tokens and API key secrets
- **Slug validation:** Lowercase alphanumeric + hyphens, 3–64 characters, starts with letter, doesn't end with hyphen
- **URL validation:** Must have scheme (http/https) + host

## Dependencies

| Spec | From Group | To Group | Relationship |
|------|-----------|----------|--------------|
| 01_backend_foundation | 3 | 1 | Uses store layer, config, database schema, Echo server, and health probe infrastructure |

## Design Decisions

1. **Auth middleware placement:** Runs as Echo middleware on the `/api/v1` group, excluding `/api/v1/auth/*` routes.
2. **Blocked user handling:** Checked at middleware level on every request, not just at login. API keys belonging to blocked users are inert but not deleted.
3. **User population unity:** Admin-created and OAuth-upserted users share one population. Matching by `(provider, provider_id)`.
4. **Workspace name uniqueness:** Both `name` and `slug` must be unique.
5. **Expired keys in listings:** Visible in `GET /api/v1/keys` but cannot authenticate.
6. **Delete cascade:** Workspace deletion removes memberships and API keys scoped to that workspace.

