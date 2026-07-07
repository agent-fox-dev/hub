---
spec_id: '03'
spec_name: cli
title: Cli
status: draft
created_at: '2026-07-07T11:28:39.009488+00:00'
updated_at: '2026-07-07T11:28:39.009488+00:00'
owner: ''
source: ".agent-fox/specs/prd.md"
schema_version: 1
---
# CLI Client

## Intent

Build `afc`, the command-line client for af-hub. The CLI is the operator's primary interface for authentication and API key management. It is stateless — every command talks to the hub server, which owns all state.

After this spec, operators can authenticate via OAuth through the CLI, and create, list, refresh, and revoke API keys from the terminal.

## Goals

- Implement the `afc` CLI binary using Cobra with a clean subcommand structure.
- Implement `afc login` with the OAuth authorization code flow (GitHub provider).
- Implement `afc keys` subcommands: create, list, refresh, revoke.
- Support `--hub-url` flag and `AF_HUB_URL` env var for server targeting.
- Support `--api-key` flag and `AF_HUB_API_KEY` env var for authentication on key commands.
- Ship CLI documentation at `docs/cli.md`.

## Non-goals

- Workspace management commands (admin uses the API directly or a future CLI extension).
- User management commands.
- Interactive prompts or TUI elements.
- Token persistence or credential storage on the client side.

## Functional Requirements

### CLI structure

- The binary is `afc`, built with Cobra.
- All commands accept `--hub-url` (or `AF_HUB_URL` env var) to specify the hub server URL.
- Commands print JSON output to stdout for machine consumption. Human-readable messages go to stderr.

### Login command

- `afc login --provider <provider>` — Run the OAuth authorization code flow.
- First iteration: GitHub only. The `--provider` flag defaults to `github`.
- Flow:
  1. Fetch the provider list from `GET /api/v1/auth/providers` on the hub.
  2. Validate that the requested provider exists in the list.
  3. Open the provider's authorization URL in the user's default browser.
  4. Start a local HTTP callback server on a random available port.
  5. Wait for the OAuth callback with the authorization code (with a timeout).
  6. Send the code to `POST /api/v1/auth/callback` on the hub.
  7. Print the returned user object to stdout.
- Error cases:
  - Hub unreachable → error message with connection details.
  - Provider not found → error listing available providers.
  - OAuth callback timeout → error with retry suggestion.
  - Callback error from provider → error with provider's error description.

### Key management commands

All key commands accept `--api-key` (or `AF_HUB_API_KEY` env var) for authentication.

- `afc keys create --workspace <workspace-id> [--label <label>] [--expires 0|30|60|90]`
  - Creates an API key scoped to the workspace. Default expiry: 30 days.
  - Prints the full key (including plaintext secret) to stdout exactly once.
  - Error if the user is not a member of the workspace.

- `afc keys list`
  - Lists all keys for the authenticated user across all workspaces.
  - Prints a JSON array of key objects (key_id, label, workspace_id, expires_at, created_at, revoked status).

- `afc keys refresh <key-id>`
  - Generates a new secret for an existing key.
  - Prints the new full key to stdout.

- `afc keys revoke <key-id>`
  - Permanently revokes a key.
  - Prints confirmation to stderr.

### Error handling

- HTTP errors from the hub are parsed from the standard error envelope and printed as human-readable messages to stderr.
- Non-zero exit codes on failure.

### Documentation

- `docs/cli.md` — Complete CLI reference: all commands and subcommands, flags, environment variables, examples for each command. Update `README.md` to link to it.

## Technical Boundaries

- **Language:** Go (1.22+)
- **CLI framework:** Cobra (`github.com/spf13/cobra`)
- **HTTP client:** Standard library `net/http`
- **Browser opening:** `os/exec` with platform-appropriate commands (`open` on macOS, `xdg-open` on Linux)
- **Output:** JSON to stdout, messages to stderr

## Dependencies

| Spec | From Group | To Group | Relationship |
|------|-----------|----------|--------------|
| 02_auth_rbac_api | 3 | 1 | Requires all auth and API key endpoints to be functional |

## Design Decisions

1. **No credential storage:** The CLI does not persist tokens or credentials. The user provides `--api-key` or `AF_HUB_API_KEY` for each key management command. Login returns the user object but does not store a session.
2. **JSON output to stdout:** Machine-readable output enables scripting and piping. Human messages go to stderr.
3. **Random callback port:** Avoids port conflicts. The port is included in the `redirect_uri` sent to the hub.

