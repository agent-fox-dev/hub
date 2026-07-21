# afc CLI Reference

This document covers all `afc` commands, flags, argument formats, and exit
codes. `afc` is the command-line interface for interacting with the af-hub
API.

## Global Behavior

- All commands that interact with the API require authentication via an API
  key or personal access token (PAT), configured through `afc login`.
- On success, most commands print a JSON response to stdout and exit with
  code 0.
- On error, commands print an error message to stderr and exit with code 1.
- Network timeouts and connection failures result in exit code 1 with a
  descriptive error message on stderr.

---

## Workspace Commands

All workspace commands are subcommands of `afc workspace`.

### afc workspace create

Create a new workspace.

**Usage:**

```
afc workspace create --slug <slug> --git-url <url> [flags]
```

**Flags:**

| Flag | Required | Type | Description |
|------|----------|------|-------------|
| `--slug` | yes | string | Globally unique URL-safe identifier for the workspace |
| `--git-url` | yes | string | HTTPS or SSH URL of the git repository |
| `--branch` | no | string | Git ref to associate with the workspace |
| `--org` | no | string | Organization slug to associate the workspace with (resolved to UUID) |
| `--display-name` | no | string | Human-readable label; defaults to slug value if omitted |
| `--description` | no | string | Free-form text describing the workspace; defaults to empty string |

**Behavior:**

- Sends `POST /api/v1/workspaces` with the provided fields.
- When `--org` is provided, the org slug is resolved to its UUID via the
  user's org list before inclusion in the request.
- Prints the created workspace JSON to stdout.

**Exit Codes:**

| Code | Condition |
|------|-----------|
| 0 | Workspace created successfully |
| 1 | Missing required flags, API error (4xx/5xx), network error, or timeout |

---

### afc workspace list

List workspaces owned by the authenticated user.

**Usage:**

```
afc workspace list [flags]
```

**Flags:**

| Flag | Required | Type | Description |
|------|----------|------|-------------|
| `--include-archived` | no | boolean | Include archived workspaces in the listing |

**Behavior:**

- Sends `GET /api/v1/workspaces` with optional `?include_archived=true`.
- Prints a JSON array of workspace objects to stdout.

**Exit Codes:**

| Code | Condition |
|------|-----------|
| 0 | Workspaces listed successfully |
| 1 | API error, network error, or timeout |

---

### afc workspace get

Get a single workspace by slug.

**Usage:**

```
afc workspace get <slug>
```

**Arguments:**

| Argument | Description |
|----------|-------------|
| `<slug>` | The workspace slug to retrieve |

**Behavior:**

- Sends `GET /api/v1/workspaces/<slug>`.
- Prints the workspace JSON to stdout.

**Exit Codes:**

| Code | Condition |
|------|-----------|
| 0 | Workspace retrieved successfully |
| 1 | Workspace not found, API error, network error, or timeout |

---

### afc workspace update

Update mutable properties of an existing workspace. At least one update flag
must be provided.

**Usage:**

```
afc workspace update <slug> [flags]
```

**Arguments:**

| Argument | Description |
|----------|-------------|
| `<slug>` | The workspace slug to update |

**Flags:**

| Flag | Type | Description |
|------|------|-------------|
| `--display-name` | string | Set the workspace display name (max 128 characters) |
| `--description` | string | Set the workspace description (max 1024 characters) |
| `--org` | string | Set the organization association (by org slug, resolved to UUID) |
| `--clear-display-name` | boolean | Reset display_name to the server-side default (slug value) |
| `--clear-description` | boolean | Reset description to the server-side default (empty string) |
| `--clear-org` | boolean | Remove the organization association |

**Behavior:**

- If no update flags are provided, prints a usage hint to stderr and exits
  with exit code 1 without making any HTTP request.
- Constructs a `PATCH /api/v1/workspaces/<slug>` request body containing
  only the fields specified by the provided flags.
- Value flags (`--display-name`, `--description`, `--org`) set the field to
  the provided value. The `--org` slug is resolved to a UUID before sending.
- Clear flags (`--clear-display-name`, `--clear-description`, `--clear-org`)
  set the corresponding field to `null` in the PATCH body, which resets the
  field to its server-side default.
- Prints the updated workspace JSON to stdout.
- If the API returns a non-2xx status, prints the error message from the
  JSON error body to stderr and exits with code 1.
- If the API response body is malformed or missing expected fields, prints a
  descriptive parse error to stderr and exits with code 1.
- On timeout or network connection failure, exits with code 1 and prints the
  error to stderr.

**Exit Codes:**

| Code | Condition |
|------|-----------|
| 0 | Workspace updated successfully |
| 1 | No flags provided (usage hint printed); API error (4xx/5xx); malformed response body; network error or timeout |

---

### afc workspace archive

Archive a workspace, making it read-only.

**Usage:**

```
afc workspace archive <slug>
```

**Arguments:**

| Argument | Description |
|----------|-------------|
| `<slug>` | The workspace slug to archive |

**Behavior:**

- Sends `POST /api/v1/workspaces/<slug>/archive`.
- Prints the updated workspace JSON to stdout (status = `"archived"`).

**Exit Codes:**

| Code | Condition |
|------|-----------|
| 0 | Workspace archived successfully |
| 1 | Workspace already archived, not found, API error, network error, or timeout |

---

### afc workspace reactivate

Reactivate an archived workspace, restoring it to active status.

**Usage:**

```
afc workspace reactivate <slug>
```

**Arguments:**

| Argument | Description |
|----------|-------------|
| `<slug>` | The workspace slug to reactivate |

**Behavior:**

- Sends `POST /api/v1/workspaces/<slug>/reactivate`.
- Prints the updated workspace JSON to stdout (status = `"active"`).

**Exit Codes:**

| Code | Condition |
|------|-----------|
| 0 | Workspace reactivated successfully |
| 1 | Workspace already active, not found, API error, network error, or timeout |

---

### afc workspace delete

Permanently delete a workspace. Only archived workspaces can be deleted.

**Usage:**

```
afc workspace delete <slug> --confirm
```

**Arguments:**

| Argument | Description |
|----------|-------------|
| `<slug>` | The workspace slug to delete |

**Flags:**

| Flag | Required | Type | Description |
|------|----------|------|-------------|
| `--confirm` | yes | boolean | Confirm deletion (safety flag to prevent accidental deletion) |

**Behavior:**

- Sends `DELETE /api/v1/workspaces/<slug>`.
- On success, prints a confirmation message to stderr.

**Exit Codes:**

| Code | Condition |
|------|-----------|
| 0 | Workspace deleted successfully |
| 1 | Workspace not archived, `--confirm` flag not provided, not found, API error, network error, or timeout |

---

## apikit-Provided Commands

The following commands are provided by the `apikit` library and manage
authentication, user profiles, API keys, tokens, organizations, and
administration.

### afc login

Authenticate with the af-hub server and store credentials locally.

**Usage:**

```
afc login [flags]
```

**Behavior:**

- Prompts for server URL and credentials (email and password).
- On successful authentication, stores the API key locally for use by
  subsequent commands.

**Exit Codes:**

| Code | Condition |
|------|-----------|
| 0 | Login successful |
| 1 | Invalid credentials, network error, or server unreachable |

---

### afc user

View or manage the authenticated user's profile.

**Usage:**

```
afc user [flags]
```

**Behavior:**

- Retrieves the current user's profile from the server.
- Prints user profile information to stdout.

**Exit Codes:**

| Code | Condition |
|------|-----------|
| 0 | User profile retrieved successfully |
| 1 | Unauthenticated, API error, or network error |

---

### afc keys

Manage API keys for the authenticated user.

**Usage:**

```
afc keys [subcommand] [flags]
```

**Subcommands:**

| Subcommand | Description |
|------------|-------------|
| `list` | List all API keys |
| `create` | Create a new API key |
| `revoke` | Revoke an API key by ID |

**Behavior:**

- `list`: Retrieves and displays all API keys for the authenticated user.
- `create`: Creates a new API key and displays the full key value (shown
  only once at creation time).
- `revoke`: Revokes the specified API key.

**Exit Codes:**

| Code | Condition |
|------|-----------|
| 0 | Operation completed successfully |
| 1 | Unauthenticated, key not found, API error, or network error |

---

### afc tokens

Manage personal access tokens (PATs) for the authenticated user.

**Usage:**

```
afc tokens [subcommand] [flags]
```

**Subcommands:**

| Subcommand | Description |
|------------|-------------|
| `list` | List all personal access tokens |
| `create` | Create a new personal access token with specified scopes |
| `revoke` | Revoke a personal access token by ID |

**Flags (create):**

| Flag | Type | Description |
|------|------|-------------|
| `--scopes` | string[] | Permission scopes to grant to the token |
| `--description` | string | Human-readable label for the token |

**Behavior:**

- `list`: Retrieves and displays all PATs for the authenticated user,
  including their granted scopes.
- `create`: Creates a new PAT with the specified scopes and displays the
  full token value (shown only once at creation time).
- `revoke`: Revokes the specified PAT.

**Exit Codes:**

| Code | Condition |
|------|-----------|
| 0 | Operation completed successfully |
| 1 | Invalid scopes, unauthenticated, token not found, API error, or network error |

---

### afc orgs

Manage organizations.

**Usage:**

```
afc orgs [subcommand] [flags]
```

**Subcommands:**

| Subcommand | Description |
|------------|-------------|
| `list` | List organizations the user belongs to |
| `create` | Create a new organization |
| `get` | Get organization details by slug |

**Behavior:**

- `list`: Retrieves and displays all organizations the authenticated user
  belongs to.
- `create`: Creates a new organization with the specified name and slug.
- `get`: Retrieves and displays details for a specific organization.

**Exit Codes:**

| Code | Condition |
|------|-----------|
| 0 | Operation completed successfully |
| 1 | Org not found, slug conflict, unauthenticated, API error, or network error |

---

### afc admin

Administrative commands for managing the af-hub instance. Requires admin
authentication.

**Usage:**

```
afc admin [subcommand] [flags]
```

**Subcommands:**

| Subcommand | Description |
|------------|-------------|
| `users` | List all users |
| `stats` | View system statistics |
| `delete-user` | Delete a user account |

**Behavior:**

- `users`: Lists all registered users in the system.
- `stats`: Displays system-wide statistics.
- `delete-user`: Permanently deletes a user account.
- All admin subcommands require an admin token; non-admin credentials
  receive a 403 error.

**Exit Codes:**

| Code | Condition |
|------|-----------|
| 0 | Operation completed successfully |
| 1 | Non-admin credentials, user not found, API error, or network error |
