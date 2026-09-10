---
spec_id: '07'
spec_name: secrets_variables
title: Secrets and Variables
status: draft
created_at: '2026-07-29T09:14:08.792929+00:00'
updated_at: '2026-07-29T09:21:20.388811+00:00'
owner: ''
source: docs/prd/prd6.md
schema_version: 1
---
# Secrets and Variables

## Intent

Hub needs a subsystem to store and retrieve secrets and variables as key/value
pairs scoped to users, organizations, and workspaces. Secrets are sensitive
values (API tokens, certificates, connection strings) whose values are never
exposed to external clients — only the hub itself reads them internally.
Variables are non-sensitive configuration values that clients can read and list
freely.

Both resource types follow the same three-tier ownership model (user → org →
workspace) with a resolution order where more specific scopes override less
specific ones. This spec covers the database schema, store layer, REST API
endpoints, resolution logic, and new PAT permission scopes. CLI commands are
covered by a separate spec.

## Goals

- Establish a storage model for secrets and variables as key/value pairs with
  three ownership tiers: user, organization, and workspace.
- Provide CRUD REST API endpoints for managing secrets and variables at each
  ownership level, following the nesting patterns of existing hub endpoints.
- Implement variable resolution that merges values across the three tiers for
  a workspace context, returning the fully resolved set via API.
- Register 8 new PAT permission scopes for fine-grained access control over
  secrets and variables.
- Store values as base64-encoded strings in the database to preserve formatting
  of multi-line content (YAML files, certificates, JSON documents) and to
  prepare for future encryption at rest.

## Non-goals

- **CLI commands.** Covered by a separate spec (`08_secrets_variables_cli`).
- **Encryption at rest.** Secrets are stored as base64-encoded plaintext for
  now. Cryptographic protection is future work.
- **Secret value readability by clients.** Secret values are never returned
  by any API endpoint. They are only read internally by the hub. This may
  change in the future.
- **Resolution for secrets.** Only variables have a public resolution endpoint.
  Secret resolution is an internal hub operation not exposed via API.
- **Pagination.** Data volume is expected to be small; pagination is deferred.
- **Audit logging.** Tracking who created/modified secrets is future work.
- **Webhooks or notifications** on secret/variable changes.
- **Foreign key constraints on `owner_id`.** Referential integrity is enforced
  at the application layer only (see Database Schema notes).
- **Workspace slug migration.** Workspace slugs are immutable (they are the
  PRIMARY KEY in the workspaces table and PATCH rejects changes to immutable
  fields). Migration of `owner_id` values on slug rename is therefore not a
  concern for this spec.

## Functional Requirements

### Database Schema

Two tables with identical structure, one for each resource type:

**`secrets` table:**

| Column | Type | Constraints |
|--------|------|-------------|
| `owner_type` | TEXT | NOT NULL, CHECK IN ('user', 'org', 'workspace') |
| `owner_id` | TEXT | NOT NULL |
| `key` | TEXT | NOT NULL |
| `value` | TEXT | NOT NULL (base64-encoded) |
| `created_at` | TEXT | NOT NULL (RFC 3339) |
| `updated_at` | TEXT | NOT NULL (RFC 3339) |
| | | PRIMARY KEY (`owner_type`, `owner_id`, `key`) |

**`variables` table:** Same schema as `secrets`.

`owner_id` contains: user UUID for `owner_type='user'`, org ID (UUID) for
`owner_type='org'`, workspace slug for `owner_type='workspace'`.

> **Referential integrity note:** There are no database-level foreign key
> constraints on `owner_id`. Integrity is enforced at the application layer:
> when a user, org, or workspace is deleted, the application must delete all
> associated secrets and variables for that owner **inside the same database
> transaction as the parent resource deletion**. This prevents orphaned rows
> in the event of a crash between parent and child deletions. This matches the
> existing hub pattern used for workspaces and other owned resources.

### Key Naming Rules

Following GitHub's conventions:

- Must contain only alphanumeric characters (`[a-zA-Z0-9]`) and underscores
  (`_`).
- Must not start with a digit.
- Case-insensitive uniqueness within a scope (stored as provided, compared
  case-insensitively).
- Maximum length: 255 characters.

### Value Constraints

- Values are base64-encoded before storage and decoded on read. The API
  accepts and returns raw (unencoded) values — encoding is transparent.
- Minimum raw value size: 0 bytes (empty string is a valid value — e.g., a
  feature-flag variable that is intentionally empty).
- Maximum raw value size: 256 KB (262144 bytes). This accommodates
  certificates, YAML files, and other large configuration documents.
- Maximum entries per owner scope: 100 secrets and 100 variables per
  (owner_type, owner_id) combination.

### Per-Scope Entry Limit Enforcement

The 100-entry-per-scope cap is the sole numeric limit on POST requests — there
is no separate per-request `entries` array size limit. Hub uses a single-writer
pattern with SQLite's serialized write mode, which serializes all concurrent
writes at the database level. This means the count check and the subsequent
INSERT within the same write transaction are effectively atomic: no two
concurrent POST requests can simultaneously pass the count check and both
succeed in pushing a scope past 100 entries.

### Intra-request Duplicate Key Handling

If a POST `entries` array contains two or more objects with the same key
(case-insensitively), the entire request is rejected with HTTP 400 before any
entries are written. This fail-fast approach is consistent with the
error-on-duplicate philosophy applied to cross-request duplicate detection
(HTTP 409).

### Ownership and Authorization

Secrets and variables follow the same authorization model as workspaces:

- **Admin tokens** bypass ownership checks and can access secrets/variables
  at any scope, including the `/workspaces/:slug/vars/resolved` endpoint.
- **API keys** have implicit full access to secrets/variables owned by the
  authenticated user, their org memberships, and their workspaces, including
  the `/workspaces/:slug/vars/resolved` endpoint.
- **PATs** require explicit permission scopes (see Permissions section).

**Org membership check:** For org-scoped operations, the caller must be a
member of the organization. Non-members receive HTTP 404 (anti-enumeration).

**Workspace ownership check:** For workspace-scoped operations, the caller
must own the workspace. Non-owners receive HTTP 404.

### Permissions

Eight new PAT permission scopes are registered with apikit's permission
registry. These scopes are fully independent of the workspace scopes defined
in `workspace_write_delete` (`workspaces:read`, `workspaces:write`,
`workspaces:delete`, etc.) — they cover different resource types (`secrets`,
`vars`) and do not overlap. Registration order does not matter for apikit's
permission registry.

**Secret scopes:**

| Scope | Grants | Implies |
|-------|--------|---------|
| `secrets:manage` | Create, list, update, and delete secrets | `secrets:list`, `secrets:write`, `secrets:delete` |
| `secrets:list` | List secret names (values never returned) | — |
| `secrets:write` | Update existing secret values | — |
| `secrets:delete` | Delete secrets | — |

**Variable scopes:**

| Scope | Grants | Implies |
|-------|--------|---------|
| `vars:manage` | Create, list, read, update, and delete variables | `vars:read`, `vars:write`, `vars:delete` |
| `vars:read` | List and read variable values | — |
| `vars:write` | List, read, and update variable values | `vars:read` |
| `vars:delete` | Delete variables | — |

Note: `secrets:manage` is required to create secrets (there is no standalone
`secrets:create` scope). Similarly, `vars:manage` is required to create
variables.

### REST API Endpoints

Endpoints are nested under the owner resource, following the existing hub
routing pattern.

#### Secret Endpoints

| Method | Path | Permission (PAT) | Description |
|--------|------|-------------------|-------------|
| POST | `/api/v1/user/secrets` | `secrets:manage` | Create user-level secret(s) |
| GET | `/api/v1/user/secrets` | `secrets:manage` or `secrets:list` | List user-level secret names |
| PATCH | `/api/v1/user/secrets/:key` | `secrets:manage` or `secrets:write` | Update user-level secret |
| DELETE | `/api/v1/user/secrets/:key` | `secrets:manage` or `secrets:delete` | Delete user-level secret |
| POST | `/api/v1/orgs/:slug/secrets` | `secrets:manage` | Create org-level secret(s) |
| GET | `/api/v1/orgs/:slug/secrets` | `secrets:manage` or `secrets:list` | List org-level secret names |
| PATCH | `/api/v1/orgs/:slug/secrets/:key` | `secrets:manage` or `secrets:write` | Update org-level secret |
| DELETE | `/api/v1/orgs/:slug/secrets/:key` | `secrets:manage` or `secrets:delete` | Delete org-level secret |
| POST | `/api/v1/workspaces/:slug/secrets` | `secrets:manage` | Create workspace-level secret(s) |
| GET | `/api/v1/workspaces/:slug/secrets` | `secrets:manage` or `secrets:list` | List workspace-level secret names |
| PATCH | `/api/v1/workspaces/:slug/secrets/:key` | `secrets:manage` or `secrets:write` | Update workspace-level secret |
| DELETE | `/api/v1/workspaces/:slug/secrets/:key` | `secrets:manage` or `secrets:delete` | Delete workspace-level secret |

#### Variable Endpoints

| Method | Path | Permission (PAT) | Description |
|--------|------|-------------------|-------------|
| POST | `/api/v1/user/vars` | `vars:manage` | Create user-level variable(s) |
| GET | `/api/v1/user/vars` | `vars:manage`, `vars:write`, or `vars:read` | List user-level variables |
| PATCH | `/api/v1/user/vars/:key` | `vars:manage` or `vars:write` | Update user-level variable |
| DELETE | `/api/v1/user/vars/:key` | `vars:manage` or `vars:delete` | Delete user-level variable |
| POST | `/api/v1/orgs/:slug/vars` | `vars:manage` | Create org-level variable(s) |
| GET | `/api/v1/orgs/:slug/vars` | `vars:manage`, `vars:write`, or `vars:read` | List org-level variables |
| PATCH | `/api/v1/orgs/:slug/vars/:key` | `vars:manage` or `vars:write` | Update org-level variable |
| DELETE | `/api/v1/orgs/:slug/vars/:key` | `vars:manage` or `vars:delete` | Delete org-level variable |
| POST | `/api/v1/workspaces/:slug/vars` | `vars:manage` | Create workspace-level variable(s) |
| GET | `/api/v1/workspaces/:slug/vars` | `vars:manage`, `vars:write`, or `vars:read` | List workspace-level variables |
| PATCH | `/api/v1/workspaces/:slug/vars/:key` | `vars:manage` or `vars:write` | Update workspace-level variable |
| DELETE | `/api/v1/workspaces/:slug/vars/:key` | `vars:manage` or `vars:delete` | Delete workspace-level variable |
| GET | `/api/v1/workspaces/:slug/vars/resolved` | `vars:manage`, `vars:write`, or `vars:read` (PAT); admin tokens and API keys have full access | Resolved variables for workspace |

#### Request/Response Formats

**Create (POST):**

Request body:
```json
{
  "entries": [
    {"key": "DB_HOST", "value": "localhost:5432"},
    {"key": "DB_NAME", "value": "mydb"}
  ]
}
```

Validation rules applied before any writes:
- The `entries` array must not be empty (HTTP 400).
- All keys in the `entries` array must be unique (case-insensitively). If any
  two entries share the same key, the entire request is rejected with HTTP 400
  before any entries are written.
- Each key must conform to the key naming rules (HTTP 400).
- Each value must not exceed 256 KB raw (HTTP 400). Empty string (`""`) is a
  valid value.
- The total number of existing entries for this (owner_type, owner_id) plus
  the number of new entries in this request must not exceed 100. If the limit
  would be exceeded, the entire request is rejected with HTTP 400 and a
  descriptive message (e.g. `"maximum of 100 entries per scope exceeded"`).
  No entries are written. There is no separate per-request array size cap;
  the per-scope limit of 100 is the only numeric constraint.

Response: HTTP 201 with the created entries. Secrets omit `value`; the response
mirrors the GET list shape:

*Secrets POST 201 response:*
```json
[
  {"key": "DB_HOST", "created_at": "2026-07-29T09:14:08Z", "updated_at": "2026-07-29T09:14:08Z"},
  {"key": "DB_NAME", "created_at": "2026-07-29T09:14:08Z", "updated_at": "2026-07-29T09:14:08Z"}
]
```

*Variables POST 201 response:*
```json
[
  {"key": "DB_HOST", "value": "localhost:5432", "created_at": "2026-07-29T09:14:08Z", "updated_at": "2026-07-29T09:14:08Z"},
  {"key": "DB_NAME", "value": "mydb", "created_at": "2026-07-29T09:14:08Z", "updated_at": "2026-07-29T09:14:08Z"}
]
```

**List (GET):**

Results are sorted alphabetically ascending by key, case-insensitively. This
ordering is deterministic and consistent across all list and resolved endpoints.

Secrets response (names and timestamps only — values are never returned):
```json
[
  {"key": "DB_HOST", "created_at": "2026-07-29T09:14:08Z", "updated_at": "2026-07-29T09:14:08Z"},
  {"key": "DB_NAME", "created_at": "2026-07-29T09:14:08Z", "updated_at": "2026-07-29T09:14:08Z"}
]
```

Variables response (names, values, and timestamps):
```json
[
  {"key": "DB_HOST", "value": "localhost:5432", "created_at": "2026-07-29T09:14:08Z", "updated_at": "2026-07-29T09:14:08Z"},
  {"key": "DB_NAME", "value": "mydb", "created_at": "2026-07-29T09:14:08Z", "updated_at": "2026-07-29T09:14:08Z"}
]
```

**Update (PATCH):**

Request body:
```json
{"value": "newvalue"}
```

The `value` field is required. A missing or null `value` field is rejected with
HTTP 400. An empty string (`""`) is a valid value and will overwrite the
existing stored value.

Response: HTTP 200 with the updated entry (same shape as a single list entry).

*Secrets PATCH 200 response:*
```json
{"key": "DB_HOST", "created_at": "2026-07-29T09:14:08Z", "updated_at": "2026-07-29T09:14:08Z"}
```

*Variables PATCH 200 response:*
```json
{"key": "DB_HOST", "value": "newvalue", "created_at": "2026-07-29T09:14:08Z", "updated_at": "2026-07-29T09:14:08Z"}
```

If the key does not exist at the given scope, the handler returns HTTP 404
(consistent with other hub endpoints that return 404 for non-existent
resources).

**Delete (DELETE):**

Response: HTTP 204 No Content.

**Resolve (GET /workspaces/:slug/vars/resolved):**

The handler looks up the workspace record (via the workspaces store, per the
`01_workspaces` dependency) to obtain the workspace's `org_id` and `user_id`.
If `org_id` is null/empty, the org tier is skipped. Variables are then fetched
for all applicable tiers and merged in resolution order.

Response: same format as the variable list response (including `created_at`
and `updated_at` timestamps), merged across user → org → workspace, sorted
alphabetically ascending by key (case-insensitive). Each entry additionally
includes an `"origin"` field indicating which tier the value came from:

```json
[
  {"key": "APP_NAME", "value": "myapp", "origin": "user", "created_at": "2026-07-28T10:00:00Z", "updated_at": "2026-07-28T10:00:00Z"},
  {"key": "DB_HOST", "value": "wsdb.example.com", "origin": "workspace", "created_at": "2026-07-29T09:14:08Z", "updated_at": "2026-07-29T09:14:08Z"}
]
```

Authorization: admin tokens and API keys have full access, consistent with all
other endpoints. PATs require `vars:manage`, `vars:write`, or `vars:read`.

#### Error Handling

| Status | Condition |
|--------|-----------|
| 400 | Invalid key name; value exceeds 256 KB; empty `entries` array; missing required fields (including missing `value` in PATCH body); duplicate keys within a single POST `entries` array; POST would exceed the 100-entry-per-scope limit (`"maximum of 100 entries per scope exceeded"`) |
| 401 | Unauthenticated request |
| 403 | PAT lacks required scope; org membership check failed for non-admin |
| 404 | Key not found (including PATCH or DELETE targeting a non-existent key); workspace/org not found or not owned (anti-enumeration) |
| 409 | Key already exists at this scope (create only — cross-request duplicate) |

### Variable Resolution

Resolution merges variables from all three tiers for a workspace context.
The resolution order (most specific wins):

1. **Workspace-level** variables (highest priority)
2. **Organization-level** variables (from the workspace's org, if any)
3. **User-level** variables (from the workspace owner)

If the workspace has no `org_id`, the org tier is skipped. The resolved
set contains every unique key, with the most specific value winning on
conflict. The `origin` field in the response indicates which tier each
value came from. The response includes `created_at` and `updated_at`
timestamps for each resolved entry, taken from the winning tier's record.
Results are sorted alphabetically ascending by key (case-insensitive),
consistent with list endpoints.

The workspace's `org_id` and `user_id` (workspace owner) are retrieved from
the workspaces store using the existing workspace lookup path established by
the `01_workspaces` dependency.

### Cascading Deletion

When a user, org, or workspace is deleted, all associated secrets and variables
for that owner must be deleted **within the same database transaction** as the
parent resource deletion. This transactional guarantee prevents orphaned rows
in the secrets and variables tables in the event of a crash or error between
the parent deletion and the child cleanup.

This matches the existing hub pattern used for owned resources. The store
method responsible for deleting the parent resource accepts a transaction
context and performs the child deletions before (or as part of) committing.

## Tech Stack

- **Language:** Go (same as hub)
- **Database:** SQLite via modernc.org/sqlite (same as hub)
- **Framework:** Echo v4 via apikit (same as hub)
- **Encoding:** Standard library `encoding/base64`
- **Testing:** Go standard `testing` package + `net/http/httptest` for handler
  tests, consistent with the rest of the hub codebase. Unit tests use an
  in-memory SQLite database.

## Dependencies

| Spec | From Group | To Group | Relationship |
|------|-----------|----------|--------------|
| 01_workspaces | 5 | 1 | Uses workspace schema and store for ownership lookups; also provides `org_id` and `user_id` needed by the variable resolution endpoint |
| 04_personal_org | 3 | 1 | Uses org membership checks for org-scoped authorization |

## Clarifications

1. **Permission hierarchy:** `secrets:manage` is a superset that implies
   `secrets:list`, `secrets:write`, and `secrets:delete`. `vars:manage` is a
   superset that implies `vars:read`, `vars:write`, and `vars:delete`.
   `vars:write` also implies `vars:read`.
2. **Secret values are write-only to external clients.** List endpoints return
   key names and timestamps, never values. Only the hub reads values internally.
   This may change in the future.
3. **Multi-scope create:** The CLI can create the same key/value at multiple
   ownership levels in one command by making multiple API calls. Each API call
   targets a single ownership level.
4. **Duplicate keys within a single request are errors (HTTP 400).** If the
   POST `entries` array contains two or more entries with the same key
   (case-insensitively), the entire request is rejected with HTTP 400 before
   any entries are written. Creating a key that already exists at the same
   (owner_type, owner_id) scope in a subsequent request returns HTTP 409. Use
   PATCH to update existing keys.
5. **Value encoding:** Values are base64-encoded in the database to preserve
   formatting of multi-line content and to prepare for future encryption.
   The API handles encoding/decoding transparently.
6. **Key naming follows GitHub conventions.** Alphanumeric + underscores,
   no leading digits, case-insensitive uniqueness within scope, max 255 chars.
7. **`vars` not `variables`** for scope naming — consistent with CLI command
   naming and avoids overly long permission strings.
8. **No standalone `secrets:create` permission.** Secret creation requires
   `secrets:manage`.
9. **No foreign key constraints on `owner_id`.** Referential integrity is
   enforced at the application layer. When a user, org, or workspace is
   deleted, the application deletes all associated secrets and variables
   within the same database transaction as the parent resource deletion.
   This matches the existing hub pattern.
10. **`secrets:*` and `vars:*` scopes are independent of workspace scopes.**
    They do not overlap with `workspaces:*` scopes registered by
    `workspace_write_delete`. Registration order in apikit's permission
    registry is irrelevant.
11. **Testing pattern:** Go standard `testing` package + `net/http/httptest`
    for handler tests, in-memory SQLite for unit tests — consistent with
    the rest of the hub codebase.
12. **POST 201 response shape for secrets** mirrors the GET list shape:
    `[{"key": "...", "created_at": "...", "updated_at": "..."}]`. The `value`
    field is never included in any secrets response.
13. **PATCH on a non-existent key returns HTTP 404**, consistent with other
    hub endpoints that return 404 for non-existent resources.
14. **Exceeding the 100-entry-per-scope limit returns HTTP 400** with the
    message `"maximum of 100 entries per scope exceeded"`. The check is
    performed atomically before any entries are written. SQLite's serialized
    write mode ensures the count check and INSERT are atomic — hub's
    single-writer pattern means no two concurrent POSTs can race past the cap.
15. **Cascading deletion is transactional.** Parent resource deletion and
    child secrets/variables deletion are wrapped in a single database
    transaction.
16. **Workspace slugs are immutable.** They are the PRIMARY KEY in the
    workspaces table and PATCH rejects changes to immutable fields. Migration
    of `owner_id` values on slug rename is not a concern.
17. **Resolved variables response includes timestamps.** The
    `/workspaces/:slug/vars/resolved` response includes `created_at` and
    `updated_at` for each entry, taken from the winning tier's record,
    consistent with the standard list response format.
18. **List and resolved endpoints sort by key alphabetically ascending
    (case-insensitive).** This ordering is deterministic and makes tests
    predictable.
19. **Empty string is a valid value.** The minimum raw value size is 0 bytes.
    A PATCH body with `"value": ""` is accepted and overwrites the existing
    value. A PATCH body that omits `value` entirely or sets it to null is
    rejected with HTTP 400.
20. **Admin tokens and API keys have full access to the resolved endpoint.**
    The `/workspaces/:slug/vars/resolved` endpoint follows the same
    authorization model as all other secrets/variables endpoints: admin tokens
    bypass ownership checks, API keys have implicit access, and PATs require
    `vars:read`, `vars:write`, or `vars:manage`.
21. **No separate per-request `entries` array size limit.** The per-scope cap
    of 100 entries is the only numeric constraint. A single POST may contain
    up to 100 entries (or fewer, depending on how many already exist in the
    scope). Requests that would push the scope over 100 are rejected with
    HTTP 400.