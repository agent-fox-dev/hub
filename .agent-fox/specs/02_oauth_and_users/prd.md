---
spec_id: '02'
spec_name: oauth_and_users
title: Oauth And Users
status: draft
created_at: '2026-07-10T14:45:29.172376+00:00'
updated_at: '2026-07-10T14:57:18.516107+00:00'
owner: ''
source: docs/01_prd.md
schema_version: 1
---
# OAuth and User Management

## Background

af-hub requires user identity before any platform capability can operate. Without authenticated users, no access control, audit trail, or team-based permission model is possible. OAuth is the standard mechanism for delegated authentication, allowing af-hub to rely on an established identity provider rather than managing passwords directly.

GitHub is the first provider because it is the primary git hosting platform for the target users of af-hub. The authorization code flow is used as a server-side (confidential client) flow where the client secret is securely stored on the server; PKCE is therefore not required and would add unnecessary complexity for this deployment model.

One active key per user is a deliberate design decision that simplifies the mental model and prevents key sprawl — the user always knows which credential is currently active. When a new key is generated (e.g. on login), the previous key is automatically revoked.

## Intent

Implement the OAuth authentication flow and user management endpoints for af-hub. This spec delivers the ability for users to authenticate via GitHub OAuth, the server to create and manage user records, and the API key lifecycle tied to login.

This builds on the server foundation (spec `server_foundation`) which provides the auth middleware, database schema, and error handling infrastructure.

## Dependencies

- **`server_foundation`** — This spec depends on `server_foundation` for:
  - Authentication middleware (recognition and validation of admin tokens, user API keys, and workspace tokens).
  - Database schema (users, api_keys tables and associated indexes).
  - Error handling infrastructure (structured error responses, HTTP error helpers).

> **Boundary clarification:** `server_foundation` owns key *recognition and validation* (middleware layer). `oauth_and_users` owns key *generation, refresh, and revocation* (lifecycle layer). There is no overlap in implementation responsibility.

## Goals

- Implement a pluggable OAuth provider registry, shipping with GitHub as the first provider.
- Implement the OAuth authorization code flow: provider listing, callback with code exchange, user upsert, and API key generation on login.
- Validate `redirect_uri` against a configured allowlist using exact scheme+host+port matching. Fail login if the provider returns a null or empty email.
- The OAuth `state` parameter is generated and managed by the CLI client (not the server). The server does not store or validate state. CSRF protection via state is the client's responsibility.
- Implement user CRUD endpoints: create (admin only), list (admin only), get (admin only, includes team memberships), update (mixed-auth — any authenticated user may update their own `full_name`; only admins may change `status`).
- Implement API key management endpoints: list (admin sees all keys across all users; user sees all of their own keys including historical revoked/expired ones), refresh (user: own key; admin: any key; expiry duration is always reused from original, not overridable at refresh), revoke (permanent, idempotent).
- Support API key expiry: 0 (indefinite), 30, 60, or 90 days. Default 90 days on login. Expiry calculated as 24h × N rolling forward from the time of creation or refresh. Expired keys visible in listings but cannot authenticate.
- All list endpoints return a flat array ordered by `created_at ASC`; no pagination is required for this spec.

## Non-goals

- Team management (spec 3).
- Workspace and token endpoints (spec 4).
- CLI implementation (spec 5).
- Additional OAuth providers beyond GitHub.
- Multi-instance / horizontally scaled deployment (in-memory state cache is single-instance only).
- Rate limiting or brute-force protection on OAuth callback or admin user-creation endpoint (out of scope for this iteration).
- Server-side OAuth state (CSRF) storage — state is owned by the CLI client.
- Pagination or sorting controls on list endpoints (ordered by `created_at ASC`; flat array).
- Overriding expiry duration at key refresh time.

## Functional Requirements

### Identifiers and ID format

- **User IDs:** UUID v4, generated using the `google/uuid` package. UUIDs are stored and returned as lowercase hyphenated strings (e.g., `"a1b2c3d4-e5f6-4789-abcd-ef0123456789"`).
- **key_id:** 8 alphanumeric characters (base62-encoded, `crypto/rand` entropy source).
- **secret:** 32 alphanumeric characters (base62-encoded, `crypto/rand` entropy source). This is a **security requirement** — the entropy source must be `crypto/rand`; `math/rand` or any deterministic source is not acceptable.
- The full API key token format is: `af_<key_id>_<secret>`. Only the SHA-256 hash of the secret is persisted; the plaintext secret is returned solely at creation or refresh time.

### Input validation

#### `username` validation rules

- **Allowed characters:** Alphanumeric characters and hyphens only (matching GitHub's username rules). No spaces, underscores, or other special characters are permitted.
- **Maximum length:** 39 characters.
- **Case sensitivity:** Usernames are stored as-is (preserving the original casing supplied by the provider or admin) but uniqueness is enforced case-insensitively (i.e., `Alice` and `alice` are considered the same username). Comparisons use lowercased forms.
- **Validation scope:** This rule applies both to the `username` field on `POST /api/v1/users` and to the GitHub `login` field derived during `POST /api/v1/auth/callback`. If the derived GitHub login does not conform to these rules, the callback returns HTTP 400.

#### `full_name` validation rules

- **Maximum length:** Not constrained beyond what the underlying database column supports (no explicit application-level limit).
- **Null / empty handling on `PUT /api/v1/users/:id`:** Both `null` and `""` (empty string) are treated as a request to clear the field — `full_name` is set to `null` in the database. A user may remove their display name this way.

#### `email` validation rules

- No format validation is performed beyond checking that the value is non-null and non-empty. Email uniqueness is not enforced.

#### `provider_id` validation rules

- Must be a non-empty string. No further format validation is applied; the value is treated as an opaque string from the provider.

#### `expires` validation rules (callback endpoint)

- Accepted values: `0`, `30`, `60`, `90` (integers).
- If an invalid value is supplied (e.g., `45`, `-1`, `"ninety"`, or a non-integer), the server returns **HTTP 400** with a structured error body describing the invalid field and accepted values.
- If `expires` is omitted, it defaults to `90`.

### Email uniqueness

- Email is **not globally unique**. Multiple users from different providers may share the same email address. This mirrors GitHub's model where email privacy settings vary by user.
- No unique constraint is enforced on the email column. Duplicate emails across different `(provider, provider_id)` pairs are permitted and expected.

### `updated_at` semantics

- `updated_at` is updated **only when at least one field value actually changes**. A no-op write (e.g., a `PUT /api/v1/users/:id` body that contains no recognized fields, or recognized fields whose values are identical to the stored values) does **not** bump `updated_at`. This prevents spurious cache invalidation and keeps the audit trail meaningful.

### OAuth provider registry

- Each provider implements: authorize URL construction, code-for-token exchange, user info extraction.
- GitHub ships as the first provider with well-known default URLs.
- Config allows optional overrides for `authorize_url`, `token_url`, `userinfo_url`.
- Each provider has a configurable **`scopes`** field in the server config. For GitHub, the default scope is `"user:email"`. This default is required because GitHub does not return a user's email unless this scope is explicitly requested; since the spec fails login on null or empty email, the scope must be present.
- The `scopes` value is included in the provider registry entry and returned by `GET /api/v1/auth/providers` so the CLI can append it to the authorize URL without needing out-of-band knowledge.
- Adding a new provider requires only registering it in the registry.

### OAuth state parameter (CSRF protection)

> **Clarification (from design decision):** The `state` parameter for CSRF protection is generated and managed entirely by the **CLI client**, not the server. The server does not generate, store, or validate the `state` value. The CLI generates a random state, embeds it in the authorize URL it constructs from the base URL returned by `GET /api/v1/auth/providers`, stores it locally, and validates the returned state value when the OAuth provider redirects back. This approach is appropriate because the CLI is the OAuth initiator, not a browser-based frontend.
>
> Consequently, there is no in-memory state cache on the server side for this spec. The state field in the `POST /api/v1/auth/callback` request body is passed through from the CLI and forwarded as-is; the server does not validate it.

### Redirect URI allowlist

- Matching is performed on **exact scheme + host + port** (the URI's origin). The path component is not considered in the allowlist check.
- **Dev environment (no `external_url` configured):** If `external_url` is absent from configuration, the server operates in dev mode. Any URI with `http://localhost` as the host is allowed, regardless of port (e.g., `http://localhost:3000`, `http://localhost:8080` are both allowed). URIs such as `http://localhost.evil.com` do not match.
- **Production environment (`external_url` is set):** Only URIs whose scheme + host + port exactly match the configured `[server] external_url` value are allowed.
- **Missing `external_url` in production:** If `external_url` is not configured and the server is running in production mode, the callback endpoint returns HTTP 500 (configuration error) — the server must not fall back to an open or permissive allowlist.
- **Runtime detection:** The server infers its environment from whether `external_url` is configured. If `external_url` is absent, the server is in dev mode (localhost permissive). If `external_url` is present, the server is in production mode (strict matching). No separate environment flag or variable is required.
- The allowlist is implemented as a set of allowed origin strings; the incoming `redirect_uri`'s origin is extracted and checked against this set.

### OAuth endpoints

#### `GET /api/v1/auth/providers`

- Returns list of configured providers with name, **base** authorize URL (without a `state` parameter embedded), and the configured `scopes` string. No secrets exposed. Public endpoint (no auth required).
- The CLI client is responsible for appending `state`, `client_id`, `redirect_uri`, and `scope` query parameters to the base URL before redirecting the user. The `scopes` field in the response provides the correct scope value to append.

**Response shape:**
```json
{
  "providers": [
    {
      "name": "github",
      "authorize_url": "https://github.com/login/oauth/authorize",
      "scopes": "user:email"
    }
  ]
}
```

#### `POST /api/v1/auth/callback`

Accepts a JSON body:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `provider` | string | yes | Provider name (e.g. `"github"`). Must match a registered provider; returns HTTP 400 if unrecognized. |
| `code` | string | yes | Authorization code from the provider |
| `state` | string | no | CSRF state token (generated and validated by CLI; server passes through) |
| `redirect_uri` | string | yes | Must match the URI used in the authorize request |
| `expires` | integer | no | Key expiry in days: `0`, `30`, `60`, or `90`. Default `90`. Returns HTTP 400 for any other value. |

**Processing steps:**
1. Validates `provider` against the registered provider registry. Returns HTTP 400 if unrecognized.
2. Validates `expires` value. Returns HTTP 400 if supplied but not one of `0`, `30`, `60`, `90`.
3. Validates `redirect_uri` origin against allowlist (see above). Returns HTTP 400 if not on allowlist.
4. Exchanges `code` with identity provider; retrieves user info (including GitHub `login` field as `username` and `provider_id`).
5. Returns HTTP 502 if the code exchange fails (provider is unreachable, returns an error, or the response cannot be parsed). This clearly signals an upstream provider failure distinct from a client error.
6. Returns HTTP 400 if provider returns a null or empty email.
7. Validates derived `username` against username rules (alphanumeric + hyphens, max 39 chars). Returns HTTP 400 if invalid.
8. Upserts user using `(provider, provider_id)` as the unique key:
   - **Create:** If no user exists for `(provider, provider_id)`, creates a new user record with `status: active`. A new UUID v4 is generated as the user ID. `provider_id` is extracted from the provider user info and stored at creation time.
   - **Update:** If a user already exists for `(provider, provider_id)`, updates `username` and `email` from the current provider response. `provider_id` is **not** updated after initial creation.
   - **Username conflict:** If the derived `username` (GitHub `login` field) conflicts with an existing user record belonging to a **different** `(provider, provider_id)` (compared case-insensitively), returns HTTP 409. The admin must resolve the conflict manually.
9. Does **not** reactivate blocked users (blocked users receive HTTP 403).
10. Generates a new API key (revoking any existing active key for that user — if no active key exists, this step is a no-op and a new key is still created).
11. Steps 8–10 (user upsert + previous key revocation + new key creation) execute within a **single database transaction**. Any failure rolls back all steps, leaving the database in a consistent state.
12. Returns user object and new API key (including the full plaintext secret — only time it is returned).

**Success response (HTTP 200):**
```json
{
  "user": {
    "id": "UUID v4 string",
    "username": "string",
    "email": "string",
    "full_name": "string | null",
    "status": "active",
    "provider": "string",
    "created_at": "ISO8601",
    "updated_at": "ISO8601"
  },
  "api_key": {
    "key_id": "string (8 alphanumeric chars, base62/crypto/rand)",
    "secret": "string (32 alphanumeric chars, plaintext, only returned here)",
    "token": "af_<key_id>_<secret>",
    "user_id": "UUID v4 string",
    "created_at": "ISO8601",
    "expires_at": "ISO8601 | null"
  }
}
```

**Error responses for this endpoint:**

| Condition | HTTP Status |
|-----------|-------------|
| Unrecognized `provider` value | 400 |
| Invalid `expires` value (not 0, 30, 60, or 90) | 400 |
| `redirect_uri` origin not on allowlist | 400 |
| Provider code exchange fails (upstream error or unreachable) | 502 |
| Provider returns null or empty email | 400 |
| Derived `username` fails validation rules | 400 |
| `username` derived from provider conflicts with a different `(provider, provider_id)` | 409 |
| User is blocked | 403 |
| `external_url` absent in production (config error) | 500 |

> **Error response body:** All error responses use the structured error format defined by `server_foundation`'s error handling infrastructure. This spec does not redefine the error body shape; implementers should refer to `server_foundation` for the canonical error response schema.

### User management endpoints

#### Initial `status` on creation

- Users created via OAuth login (`POST /api/v1/auth/callback`) start with `status: active`.
- Users created via admin endpoint (`POST /api/v1/users`) start with `status: active`.

#### Admin-only endpoints

##### `POST /api/v1/users` — Create user

**Request body:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `username` | string | yes | Unique username. Must conform to username validation rules (alphanumeric + hyphens, max 39 chars). Uniqueness enforced case-insensitively. |
| `email` | string | yes | User's email (not required to be globally unique; multiple users may share the same email) |
| `full_name` | string | no | Display name |
| `provider` | string | yes | OAuth provider name (e.g. `"github"`). Any non-empty string is accepted. If the value does not match a registered provider, the server logs a warning but still creates the user — admins may pre-provision users for providers not yet configured. |
| `provider_id` | string | yes | Unique identifier from the provider. Must be non-empty. Stored as TEXT; format is opaque (e.g., GitHub integer IDs are passed as strings). Uniqueness enforced per `(provider, provider_id)` pair. |

- **No API key is generated** for admin-created users. The user record starts with no associated key. The user must log in via OAuth to receive an API key. This supports pre-provisioning workflows.
- When an admin-created user subsequently logs in via OAuth, the standard login flow applies: the `expires` field in the callback request defaults to `90` days if not specified, the same as any other OAuth login.
- Initial `status`: **`active`**.
- Returns HTTP 201 with the created user object.
- Returns HTTP 409 on duplicate `username` (case-insensitive) or duplicate `(provider, provider_id)`.
- Returns HTTP 400 if `username` fails validation rules or `provider_id` is empty.

**Response (HTTP 201):**
```json
{
  "id": "UUID v4 string",
  "username": "string",
  "email": "string",
  "full_name": "string | null",
  "status": "active",
  "provider": "string",
  "created_at": "ISO8601",
  "updated_at": "ISO8601"
}
```
> Note: `provider_id` is **omitted** from the response (sensitive external identifier).

##### `GET /api/v1/users` — List all users

- Returns a flat array of user objects ordered by `created_at ASC`. No pagination.
- Returns HTTP 200 with an empty `users` array if no users exist.
- Each object includes: `id`, `username`, `email`, `full_name`, `status`, `provider`, `created_at`, `updated_at`.
- `provider_id` is **omitted** from list responses.

**Response (HTTP 200):**
```json
{
  "users": [
    {
      "id": "UUID v4 string",
      "username": "string",
      "email": "string",
      "full_name": "string | null",
      "status": "active | blocked",
      "provider": "string",
      "created_at": "ISO8601",
      "updated_at": "ISO8601"
    }
  ]
}
```

##### `GET /api/v1/users/:id` — Get user by ID

- Returns the full user object (including `provider_id`) plus an array of team memberships for that user.
- The `role` field within `team_memberships` reflects the membership role as defined by spec 3 (team management). The allowed values for `role` are owned by that spec; this endpoint surfaces whatever values are stored in the database. Implementers should treat `role` as an opaque string at this layer.
- Returns **HTTP 404** with a structured error body (per `server_foundation` format) if no user exists for the given ID.

**Response (HTTP 200):**
```json
{
  "id": "UUID v4 string",
  "username": "string",
  "email": "string",
  "full_name": "string | null",
  "status": "active | blocked",
  "provider": "string",
  "provider_id": "string",
  "created_at": "ISO8601",
  "updated_at": "ISO8601",
  "team_memberships": [
    {
      "team_id": "string",
      "team_name": "string",
      "role": "string"
    }
  ]
}
```

#### Mixed-auth endpoint

##### `PUT /api/v1/users/:id` — Update user

Any authenticated user may call this endpoint; field-level permissions apply:

| Field | Who may set it |
|-------|---------------|
| `full_name` | Any authenticated user, on their own record only |
| `status` | Admins only (including on their own record) |

- A non-admin updating another user's record receives HTTP 403.
- A non-admin sending a `status` field (regardless of other fields present) receives HTTP 403.
- **Admin self-update:** Admins always operate with admin-level permissions, including when updating their own user record. An admin may set `status` on themselves.
- **`full_name` clearing:** Sending `"full_name": null` or `"full_name": ""` sets `full_name` to `null` in the database. Users may remove their display name this way.
- **Empty / no-op update body:** If the request body contains no recognized fields (or all recognized fields are absent), the server returns HTTP 200 with the current unmodified user object. No error is returned for a no-op update. `updated_at` is **not** bumped for no-op updates.
- **`updated_at` behavior:** `updated_at` is updated only if at least one field value actually changes (i.e., the new value differs from the stored value). If the supplied field values are identical to the current stored values, `updated_at` is not bumped.
- Returns HTTP 404 with a structured error body (per `server_foundation` format) if no user exists for the given ID.
- Returns the updated user object on success. The response shape matches `GET /api/v1/users/:id` **minus `team_memberships`**, and **includes `provider_id`**.

**Request body:**
```json
{
  "full_name": "string | null (optional — null or empty string clears the field)",
  "status": "active | blocked (optional, admin only)"
}
```

**Response (HTTP 200):**
```json
{
  "id": "UUID v4 string",
  "username": "string",
  "email": "string",
  "full_name": "string | null",
  "status": "active | blocked",
  "provider": "string",
  "provider_id": "string",
  "created_at": "ISO8601",
  "updated_at": "ISO8601"
}
```

### API key management endpoints

#### `GET /api/v1/keys` — List API keys

- Admin token: returns all keys across all users, ordered by `created_at ASC`.
- User API key: returns **all keys ever associated with the requesting user** (including expired and revoked historical keys), ordered by `created_at ASC`. Because the one-active-key-per-user model revokes previous keys on each login, a user may have multiple historical keys in this listing.
- Workspace token: not authorized (HTTP 403).
- The admin token is not associated with a user record and does not appear in key listings.
- The **secret is never returned** in list responses.
- Key validity is intentionally **not** computed server-side. Consumers determine whether a key is currently valid by inspecting `expires_at` (null = indefinite; non-null = expired if before current time) and `revoked_at` (null = not revoked). No computed `status` or `is_active` field is included in list responses.

**Response (HTTP 200):**
```json
{
  "keys": [
    {
      "key_id": "string",
      "user_id": "UUID v4 string",
      "created_at": "ISO8601",
      "expires_at": "ISO8601 | null",
      "revoked_at": "ISO8601 | null"
    }
  ]
}
```

#### `POST /api/v1/keys/:key_id/refresh` — Refresh a key's secret

- Generates a new secret for an existing key (`key_id` remains the same). The new secret is generated using `crypto/rand` with base62 encoding (same mechanism as initial key creation).
- Authorization: a user may only refresh their own key; an admin may refresh any user's key (including keys belonging to blocked users). Returns HTTP 403 if a user attempts to refresh another user's key.
- Returns **HTTP 404** with a structured error body (per `server_foundation` format) if `key_id` does not exist **or if the key has been revoked** (revoked keys are treated as non-existent for refresh operations; revocation is permanent and cannot be undone via refresh).
- **Expired keys:** An expired (but not revoked) key **can** be refreshed. The refresh generates a new secret and recalculates `expires_at` using the original expiry duration. This avoids forcing the user to re-authenticate via OAuth solely due to key expiry.
- **Blocked user keys:** An admin may refresh the key of a blocked user. User status does not gate administrative key management operations.
- **Expiry recalculation:**
  - Indefinite keys (`expires_at` is null) remain indefinite (`expires_at` stays null).
  - Timed keys: `expires_at = now + original N days`. The expiry **duration is always reused** from the original key creation — the caller cannot override the duration at refresh time.
- Returns the full key object including the new plaintext secret (only time the new secret is returned after key creation).

**Response (HTTP 200):**
```json
{
  "key_id": "string",
  "user_id": "UUID v4 string",
  "secret": "string (new plaintext secret, only returned here)",
  "token": "af_<key_id>_<new_secret>",
  "created_at": "ISO8601",
  "expires_at": "ISO8601 | null",
  "revoked_at": null
}
```

#### `DELETE /api/v1/keys/:key_id` — Revoke a key

- Sets `revoked_at` timestamp on first revocation.
- **Idempotent:** Revoking an already-revoked key returns HTTP 204 (no-op, no error).
- Returns **HTTP 404** with a structured error body (per `server_foundation` format) if `key_id` does not exist (i.e., no row in the database for that `key_id`).
- Authorization: a user may only revoke their own key; an admin may revoke any key (including keys belonging to blocked users). Returns HTTP 403 otherwise.
- **Blocked user keys:** An admin may revoke the key of a blocked user. User status does not gate administrative key management operations.
- Returns HTTP 204 on success (no body).

### API key lifecycle

- **Format:** `af_<key_id>_<secret>` — `key_id` is 8 alphanumeric characters, `secret` is 32 alphanumeric characters.
- **Generation:** Both `key_id` and `secret` are generated using `crypto/rand` as the entropy source, base62-encoded to produce alphanumeric output. Using `math/rand` or any deterministic source is **not acceptable** — `crypto/rand` is a security requirement.
- One active key per user. Login generates a new key, revoking the previous one (if any active key exists; otherwise the revocation step is a no-op and a new key is still created).
- Expiry options: 0 (indefinite), 30, 60, 90 days. Default 90 on login. `expires_at` is nullable (null when `expires=0`). Any other value supplied by the caller returns HTTP 400.
- Expired keys cannot authenticate but remain in listings. Expired keys **can** be refreshed.
- SHA-256 hash of the secret is stored in the database; the plaintext secret is returned only at creation or refresh time and never stored.
- Refresh always reuses the original expiry duration (N days); callers cannot override duration at refresh time.
- Refresh on a revoked key returns HTTP 404 — revocation is permanent and cannot be undone via the refresh operation.
- Refresh on an expired (non-revoked) key is permitted and resets the expiry forward from the time of refresh.
- All three steps of the login flow (user upsert + previous key revocation + new key creation) execute within a single database transaction. Any failure rolls back all steps.

## Technical Boundaries

- **Language:** Go (1.22+)
- **HTTP framework:** Echo
- **Database:** SQLite (schema from `server_foundation`)
- **User ID format:** UUID v4, generated via `google/uuid`. Stored and returned as lowercase hyphenated strings.
- **key_id / secret generation:** `crypto/rand` entropy source, base62-encoded. This is a security requirement.
- **OAuth:** Standard authorization code flow; PKCE not required for server-side (confidential client) flow
- **State storage:** None on the server — CSRF state is owned and validated by the CLI client
- **Deployment model:** Single-instance only for this spec; multi-instance support is a future concern
- **List ordering:** All list endpoints return records ordered by `created_at ASC`; no pagination
- **provider_id type:** Stored as TEXT; opaque string; must be non-empty; no format validation beyond non-empty check
- **Email uniqueness:** Email is not unique; no unique constraint on the email column. Multiple users from different providers may share an email.
- **Username uniqueness:** Case-insensitive; stored as-is; compared using lowercased forms. Alphanumeric + hyphens only; max 39 characters.
- **`updated_at` update trigger:** Updated only when at least one field value actually changes. No-op writes do not bump `updated_at`.
- **Environment detection:** Inferred from `external_url` configuration — absent means dev mode (localhost permissive), present means production mode (strict matching)
- **Error response format:** Structured error bodies are defined and owned by `server_foundation`; this spec uses those helpers without redefining the schema. HTTP 404 is returned for missing users and missing/revoked keys using this format.
- **GitHub default scope:** `"user:email"` — required to retrieve email from the GitHub API. Configurable per provider; included in the `GET /api/v1/auth/providers` response.
- **Transaction atomicity:** The OAuth callback login flow (user upsert + key revocation + key creation) must execute within a single database transaction.
- **Admin key operations on blocked users:** Admins may refresh or revoke keys belonging to blocked users. User status does not restrict administrative key management.
- **Provider validation on admin create:** Any non-empty `provider` string is accepted on `POST /api/v1/users`; a warning is logged if the value does not match a registered provider.
- **Owner:** Not assigned — greenfield project
