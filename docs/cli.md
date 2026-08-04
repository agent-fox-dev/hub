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
| `--sync-mode` | no | string | Upstream sync mode: `pull_only` (default) or `disabled`; invalid values are rejected client-side |
| `--git-pat` | no | string | Personal access token for authenticating against a private repository |
| `--git-username` | no | string | Git username for HTTP basic auth (must be paired with `--git-password`) |
| `--git-password` | no | string | Git password for HTTP basic auth (must be paired with `--git-username`) |

**Credential flag rules:**

- `--git-pat` and `--git-username`/`--git-password` are **mutually exclusive**.
- `--git-username` and `--git-password` must be provided **together**.
- Credential flags require `--git-url` to use the `https://` scheme.
- Empty credential values are rejected.

**Behavior:**

- Sends `POST /api/v1/workspaces` with the provided fields.
- When `--org` is provided, the org slug is resolved to its UUID via the
  user's org list before inclusion in the request.
- When `--org` is omitted, the server automatically assigns the workspace to
  the user's personal organization.
- When credential flags are provided, the server validates them against the
  remote repository before creating the workspace.
- Prints the created workspace JSON to stdout.

**Exit Codes:**

| Code | Condition |
|------|-----------|
| 0 | Workspace created successfully |
| 1 | Missing required flags, credential validation error, API error (4xx/5xx), network error, or timeout |

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
| 1 | Workspace already archived, clone in progress, not found, API error, network error, or timeout |

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
| 1 | Workspace is not archived, not found, API error, network error, or timeout |

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

### afc workspace sync

Trigger an upstream sync operation for a workspace. Fetches from the remote
repository and fast-forwards the local integration branch.

**Usage:**

```
afc workspace sync <slug> [flags]
```

**Arguments:**

| Argument | Description |
|----------|-------------|
| `<slug>` | The workspace slug to sync |

**Flags:**

| Flag | Required | Type | Description |
|------|----------|------|-------------|
| `--reset-to-upstream` | no | boolean | Force-reset the local integration branch to match upstream HEAD (recovery after force-push) |

**Behavior:**

- Sends `POST /api/v1/workspaces/<slug>/sync`.
- When `--reset-to-upstream` is provided, appends `?reset_to_upstream=true`
  to the request, which force-resets the local branch to upstream HEAD
  regardless of ancestry (useful for recovering from force-pushes).
- Prints the updated workspace JSON to stdout, including sync status fields
  (`sync_status`, `sync_mode`, `upstream_head_sha`, `last_sync_at`,
  `sync_error`).
- Requires `workspaces:sync` permission scope for PATs.

**Exit Codes:**

| Code | Condition |
|------|-----------|
| 0 | Sync completed successfully |
| 1 | Workspace not found, sync disabled, clone not ready, sync already in progress, API error, network error, or timeout |

---

### afc workspace reclone

Archive and re-clone a workspace from upstream. This is a nuclear recovery
operation that deletes the local clone and re-clones from scratch.

**Usage:**

```
afc workspace reclone <slug> --confirm
```

**Arguments:**

| Argument | Description |
|----------|-------------|
| `<slug>` | The workspace slug to reclone |

**Flags:**

| Flag | Required | Type | Description |
|------|----------|------|-------------|
| `--confirm` | yes | boolean | Confirm reclone operation (safety flag to prevent accidental reclone) |

**Behavior:**

- Sends `POST /api/v1/workspaces/<slug>/reclone`.
- The `--confirm` flag is a CLI-only safety check; it is required before the
  command will make any API call.
- The reclone operation attempts to push local commits to upstream (logging a
  warning if push fails), deletes the local clone directory, resets workspace
  clone state to `pending`, and enqueues a new clone job.
- The workspace status remains `active` throughout the reclone lifecycle.
- Prints the workspace JSON to stdout with `clone_status='pending'`.
- Requires `workspaces:sync` permission scope for PATs.

**Exit Codes:**

| Code | Condition |
|------|-----------|
| 0 | Reclone initiated successfully |
| 1 | `--confirm` flag not provided, workspace not found, clone already in progress, API error, network error, or timeout |

---

## Git Credential Helper

The `afc` CLI includes a built-in git credential helper that automatically
authenticates git operations against the hub using the API key stored in
`~/.af/config.toml` (set during `afc login`).

### Setup

Configure git to use the credential helper for your hub instance:

```
git config --global credential.http://localhost:8080.helper '!afc credential-helper'
```

Replace `http://localhost:8080` with your hub's URL.

### Usage

Once configured, plain git URLs work without embedded tokens:

```
git clone http://localhost:8080/git/mickume/my-workspace.git
git push origin main
```

Git calls `afc credential-helper get` behind the scenes whenever it needs
credentials for the hub host. The helper reads the API key from
`~/.af/config.toml` and supplies it as HTTP Basic auth. Requests to other
hosts are ignored, allowing git's default credential chain to handle them.

### How It Works

The credential helper implements the standard
[git credential helper protocol](https://git-scm.com/docs/gitcredentials).
It only responds to `get` requests where the host matches the configured
`endpoint_url`. The `store` and `erase` actions are no-ops since credentials
are managed by `afc login`.

---

## Secrets Commands

All secrets commands are subcommands of `afc secrets`. They use scope flags to
target user, organization, or workspace scope:

| Flag | Type | Description |
|------|------|-------------|
| `--user` | boolean | Target user scope |
| `--org` | string | Target organization scope (by slug) |
| `--workspace` | string | Target workspace scope (by slug) |

For `list`, `update`, and `delete`: only one scope flag may be specified. If
none is given, the command defaults to user scope. If multiple flags are
provided, the command exits with code 2.

For `create`: multiple scope flags can be specified to write to multiple scopes
sequentially. If none is given, the command defaults to user scope.

### afc secrets create

Create one or more secrets.

**Usage:**

```
afc secrets create <KEY=VALUE[,KEY2=VALUE2,...]> [flags]
```

**Arguments:**

| Argument | Description |
|----------|-------------|
| `<KEY=VALUE[,...]>` | Comma-separated KEY=VALUE pairs |

Keys may contain alphanumeric characters and underscores. Values may contain
additional `=` characters. An empty or whitespace-only key is rejected.

**Flags:**

| Flag | Type | Description |
|------|------|-------------|
| `--user` | boolean | Target user scope |
| `--org` | string | Target organization scope (by slug) |
| `--workspace` | string | Target workspace scope (by slug) |

**Behavior:**

- Sends `POST <scope>/secrets` with an `entries` array in the request body.
- When multiple scope flags are specified, creates in each scope sequentially.
  If one scope fails, the error is printed to stderr and the remaining scopes
  are still attempted.
- Prints the created entry JSON to stdout for each scope.

**Exit Codes:**

| Code | Condition |
|------|-----------|
| 0 | Secrets created successfully in all targeted scopes |
| 1 | API error (4xx/5xx), network error, or timeout in any scope |
| 2 | Missing argument or invalid KEY=VALUE format |

---

### afc secrets list

List secrets for a scope.

**Usage:**

```
afc secrets list [flags]
```

**Flags:**

| Flag | Type | Description |
|------|------|-------------|
| `--user` | boolean | Target user scope |
| `--org` | string | Target organization scope (by slug) |
| `--workspace` | string | Target workspace scope (by slug) |

**Behavior:**

- Sends `GET <scope>/secrets`.
- Prints a JSON array of secret entries (keys and timestamps, no values) to
  stdout.
- Defaults to user scope when no flags are provided.

**Exit Codes:**

| Code | Condition |
|------|-----------|
| 0 | Secrets listed successfully |
| 1 | API error, network error, or timeout |
| 2 | Multiple scope flags specified |

---

### afc secrets update

Update a secret value.

**Usage:**

```
afc secrets update <KEY=VALUE> [flags]
```

**Arguments:**

| Argument | Description |
|----------|-------------|
| `<KEY=VALUE>` | The secret key and its new value |

**Flags:**

| Flag | Type | Description |
|------|------|-------------|
| `--user` | boolean | Target user scope |
| `--org` | string | Target organization scope (by slug) |
| `--workspace` | string | Target workspace scope (by slug) |

**Behavior:**

- Sends `PATCH <scope>/secrets/<key>` with the new value in the request body.
- Prints the updated entry JSON to stdout.
- Defaults to user scope when no flags are provided.

**Exit Codes:**

| Code | Condition |
|------|-----------|
| 0 | Secret updated successfully |
| 1 | API error (4xx/5xx), network error, or timeout |
| 2 | Missing argument, invalid KEY=VALUE format, or multiple scope flags specified |

---

### afc secrets delete

Delete a secret.

**Usage:**

```
afc secrets delete <KEY> [flags]
```

**Arguments:**

| Argument | Description |
|----------|-------------|
| `<KEY>` | The secret key to delete |

**Flags:**

| Flag | Type | Description |
|------|------|-------------|
| `--user` | boolean | Target user scope |
| `--org` | string | Target organization scope (by slug) |
| `--workspace` | string | Target workspace scope (by slug) |

**Behavior:**

- Sends `DELETE <scope>/secrets/<key>`.
- On success, prints a confirmation message to stderr.
- Defaults to user scope when no flags are provided.

**Exit Codes:**

| Code | Condition |
|------|-----------|
| 0 | Secret deleted successfully |
| 1 | API error (4xx/5xx), network error, or timeout |
| 2 | Missing argument or multiple scope flags specified |

---

## Variables Commands

All variables commands are subcommands of `afc vars`. They use the same scope
flags as the secrets commands:

| Flag | Type | Description |
|------|------|-------------|
| `--user` | boolean | Target user scope |
| `--org` | string | Target organization scope (by slug) |
| `--workspace` | string | Target workspace scope (by slug) |

For `list`, `update`, and `delete`: only one scope flag may be specified. If
none is given, the command defaults to user scope. If multiple flags are
provided, the command exits with code 2.

For `create`: multiple scope flags can be specified to write to multiple scopes
sequentially. If none is given, the command defaults to user scope.

### afc vars create

Create one or more variables.

**Usage:**

```
afc vars create <KEY=VALUE[,KEY2=VALUE2,...]> [flags]
```

**Arguments:**

| Argument | Description |
|----------|-------------|
| `<KEY=VALUE[,...]>` | Comma-separated KEY=VALUE pairs |

Keys may contain alphanumeric characters and underscores. Values may contain
additional `=` characters. An empty or whitespace-only key is rejected.

**Flags:**

| Flag | Type | Description |
|------|------|-------------|
| `--user` | boolean | Target user scope |
| `--org` | string | Target organization scope (by slug) |
| `--workspace` | string | Target workspace scope (by slug) |

**Behavior:**

- Sends `POST <scope>/vars` with an `entries` array in the request body.
- When multiple scope flags are specified, creates in each scope sequentially.
  If one scope fails, the error is printed to stderr and the remaining scopes
  are still attempted.
- Prints the created entry JSON to stdout for each scope.

**Exit Codes:**

| Code | Condition |
|------|-----------|
| 0 | Variables created successfully in all targeted scopes |
| 1 | API error (4xx/5xx), network error, or timeout in any scope |
| 2 | Missing argument or invalid KEY=VALUE format |

---

### afc vars list

List variables for a scope.

**Usage:**

```
afc vars list [flags]
```

**Flags:**

| Flag | Type | Description |
|------|------|-------------|
| `--user` | boolean | Target user scope |
| `--org` | string | Target organization scope (by slug) |
| `--workspace` | string | Target workspace scope (by slug) |

**Behavior:**

- Sends `GET <scope>/vars`.
- Prints a JSON array of variable entries (keys, values, and timestamps) to
  stdout.
- Defaults to user scope when no flags are provided.

**Exit Codes:**

| Code | Condition |
|------|-----------|
| 0 | Variables listed successfully |
| 1 | API error, network error, or timeout |
| 2 | Multiple scope flags specified |

---

### afc vars update

Update a variable value.

**Usage:**

```
afc vars update <KEY=VALUE> [flags]
```

**Arguments:**

| Argument | Description |
|----------|-------------|
| `<KEY=VALUE>` | The variable key and its new value |

**Flags:**

| Flag | Type | Description |
|------|------|-------------|
| `--user` | boolean | Target user scope |
| `--org` | string | Target organization scope (by slug) |
| `--workspace` | string | Target workspace scope (by slug) |

**Behavior:**

- Sends `PATCH <scope>/vars/<key>` with the new value in the request body.
- Prints the updated entry JSON to stdout.
- Defaults to user scope when no flags are provided.

**Exit Codes:**

| Code | Condition |
|------|-----------|
| 0 | Variable updated successfully |
| 1 | API error (4xx/5xx), network error, or timeout |
| 2 | Missing argument, invalid KEY=VALUE format, or multiple scope flags specified |

---

### afc vars delete

Delete a variable.

**Usage:**

```
afc vars delete <KEY> [flags]
```

**Arguments:**

| Argument | Description |
|----------|-------------|
| `<KEY>` | The variable key to delete |

**Flags:**

| Flag | Type | Description |
|------|------|-------------|
| `--user` | boolean | Target user scope |
| `--org` | string | Target organization scope (by slug) |
| `--workspace` | string | Target workspace scope (by slug) |

**Behavior:**

- Sends `DELETE <scope>/vars/<key>`.
- On success, prints a confirmation message to stderr.
- Defaults to user scope when no flags are provided.

**Exit Codes:**

| Code | Condition |
|------|-----------|
| 0 | Variable deleted successfully |
| 1 | API error (4xx/5xx), network error, or timeout |
| 2 | Missing argument or multiple scope flags specified |

---

### afc vars resolve

Resolve variables for a workspace using the three-tier hierarchy
(workspace > org > user).

**Usage:**

```
afc vars resolve <workspace-slug>
```

**Arguments:**

| Argument | Description |
|----------|-------------|
| `<workspace-slug>` | The workspace slug to resolve variables for |

This command does not use scope flags. The workspace is specified as a
positional argument.

**Behavior:**

- Sends `GET /api/v1/workspaces/<slug>/vars/resolved`.
- Prints the resolved variables JSON to stdout, with an origin field
  indicating which tier each value came from.

**Exit Codes:**

| Code | Condition |
|------|-----------|
| 0 | Variables resolved successfully |
| 1 | API error, network error, or timeout |
| 2 | Missing workspace slug argument |

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
