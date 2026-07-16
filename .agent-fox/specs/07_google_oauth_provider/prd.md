---
spec_id: '07'
spec_name: google_oauth_provider
title: Google Oauth Provider
status: draft
created_at: '2026-07-16T14:26:39.167039+00:00'
updated_at: '2026-07-16T14:26:39.167039+00:00'
owner: ''
source: interactive
schema_version: 1
---
# Google OAuth Provider

## Background

af-hub currently supports GitHub as its sole OAuth identity provider. To broaden
the user base beyond GitHub-centric workflows, the hub needs to support Google
as a second authentication provider. Google accounts are ubiquitous and used
across organizations that may not use GitHub for identity.

The existing auth architecture in `internal/auth` is designed for pluggable
providers: a `Provider` interface, a `Registry`, and a provider-agnostic
callback handler. Adding Google requires implementing the `Provider` interface
for Google's OAuth 2.0 / OpenID Connect flow and wiring it into the startup
registration, following the same pattern as `GitHubProvider`.

## Intent

Add a Google OAuth authentication provider to af-hub that follows the same
code structure and patterns as the existing GitHub provider in `internal/auth`.
This includes a new `GoogleProvider` type, default Google OAuth URLs, token
exchange, user info retrieval, and registration in the server startup sequence.

## Goals

- Implement `GoogleProvider` in `internal/auth/google_provider.go` following the
  same structure as `github_provider.go`.
- Support Google's OAuth 2.0 authorization code flow with OpenID Connect
  userinfo endpoint.
- Default Google OAuth URLs: authorize, token, and userinfo endpoints.
- Default scopes: `openid email profile` (to retrieve email and display name).
- Derive username from the email local part (before `@`), stripping characters
  that don't match the existing username validation rules (alphanumeric +
  hyphens, max 39 chars).
- Map Google's `sub` field to `provider_id` and `name` to `full_name`.
- Register the Google provider in the server startup switch statement in
  `cmd/af-hub/main.go`.
- Update `docs/configuration.md` with Google OAuth app setup instructions.
- Write unit tests mirroring the GitHub provider test patterns.

## Non-goals

- Modifying the `Provider` interface or `Registry` — Google fits the existing
  abstraction.
- Modifying the callback handler (`handlers.go`) — it is already
  provider-agnostic.
- Modifying the CLI login command — it already works with any provider name
  dynamically via the providers API.
- PKCE support — not required for server-side (confidential client) flow.
- Google Workspace (G Suite) specific features like domain restriction.
- Refresh tokens or long-lived Google sessions.
- Account linking (merging a Google identity with an existing GitHub identity).

## Functional Requirements

### Google Provider implementation

The `GoogleProvider` struct implements the existing `Provider` interface with
these specifics:

#### Default URLs

| Endpoint | Default URL |
|----------|-------------|
| Authorize | `https://accounts.google.com/o/oauth2/v2/auth` |
| Token | `https://oauth2.googleapis.com/token` |
| UserInfo | `https://www.googleapis.com/oauth2/v2/userinfo` |

All three URLs are overridable via configuration (same as GitHub).

#### Default Scopes

`openid email profile` — this retrieves the user's email address and display
name from Google's userinfo endpoint.

#### Token Exchange

Google's token endpoint requires `grant_type=authorization_code` in addition to
the standard `client_id`, `client_secret`, `code`, and `redirect_uri` fields.
This is the only structural difference from GitHub's token exchange. The
request uses `application/x-www-form-urlencoded` content type (same as GitHub).
The response returns `access_token` in a JSON body (same shape as GitHub).

#### User Info Retrieval

Google's userinfo endpoint (`GET /oauth2/v2/userinfo` with Bearer token)
returns:

```json
{
  "id": "117730543842840592312",
  "email": "user@example.com",
  "verified_email": true,
  "name": "Jane Doe",
  "given_name": "Jane",
  "family_name": "Doe",
  "picture": "https://..."
}
```

Field mapping to `UserInfo`:

| Google field | UserInfo field | Notes |
|-------------|----------------|-------|
| `id` | `ID` (→ `provider_id`) | Stable opaque string; Google's unique user identifier |
| email local part | `Login` (→ `username`) | Derived from email; see username derivation below |
| `email` | `Email` | Direct mapping |
| `name` | `Name` (→ `full_name`) | Display name; may be empty |

#### Username Derivation from Email

Since Google has no `login` field equivalent to GitHub:

1. Extract the local part of the email (before `@`).
2. Remove any character that is not alphanumeric or hyphen (e.g., dots, underscores, plus signs).
3. Truncate to 39 characters if longer.
4. If the result is empty after sanitization, return an error (same as GitHub's
   invalid username path — HTTP 400).

Example: `jane.doe+work@gmail.com` → `janedoework`

The derived username goes through the same validation as GitHub usernames
(the existing `usernameRegexp` in `handlers.go`).

### Configuration

A Google provider entry in `config.toml`:

```toml
[[oauth.providers]]
name = "google"
client_id = "your-google-client-id"
client_secret = "your-google-client-secret"
```

Optional URL overrides (`authorize_url`, `token_url`, `userinfo_url`) work the
same as GitHub's.

### Registration

In `cmd/af-hub/main.go`, the provider registration switch statement adds a
`case "google"` that calls `auth.NewGoogleProvider(cfg)`.

### Documentation

Add a "Google OAuth" section to `docs/configuration.md` with:
- Steps to create a Google OAuth 2.0 Client ID in Google Cloud Console.
- Required OAuth consent screen configuration.
- Authorized redirect URIs setup (matching the localhost pattern for dev mode).
- Config.toml example.

## Technical Boundaries

- **Language:** Go
- **Package:** `internal/auth` (same as GitHub provider)
- **Interface:** Existing `Provider` interface — no changes
- **Config:** Existing `OAuthProvider` struct in `internal/serverconfig` — no changes needed; all fields (name, client_id, client_secret, authorize_url, token_url, userinfo_url) already exist and are reused
- **HTTP client:** `net/http` (same as GitHub; injectable via `SetHTTPClient` for tests)
- **Token exchange content type:** `application/x-www-form-urlencoded`
- **Token exchange extra field:** `grant_type=authorization_code` (Google requires this; GitHub does not)

## Design Decisions

1. **Username derivation uses email local part.** Google has no `login` field.
   Using the email local part (sanitized) is the most predictable derivation.
   The sanitization strips dots, underscores, and plus signs to match the
   existing `[0-9A-Za-z-]{1,39}` username constraint. Rationale: email is
   always present (required scope), the local part is usually recognizable as
   a username, and it avoids introducing a new "choose your username" flow.

2. **Default scopes include `profile`.** Using `openid email profile` ensures
   we get the user's display name (`name` field). Using only `openid email`
   would leave `full_name` empty for all Google users. Since the GitHub flow
   populates `full_name` from the `name` field, Google should do the same.

3. **Google `id` field used as `provider_id`.** Google's v2 userinfo returns
   `id` (a numeric string), which is functionally equivalent to GitHub's `id`.
   This is a stable, opaque identifier that uniquely identifies the Google
   account.

## Dependencies

| Spec | From Group | To Group | Relationship |
|------|-----------|----------|--------------|
| 01_server_foundation | 3 | 1 | Modifies the provider registration switch in main.go startup sequence |
| 02_oauth_and_users | 1 | 1 | Implements the Provider interface and uses the Registry defined in spec 02 |

## Clarifications

1. **Empty-username error path:** `GoogleProvider.GetUserInfo()` returns a plain
   `error` value when the derived username is empty (same pattern as GitHub's
   `fetchPrimaryEmail` returning an error for "no verified email found"). The
   callback handler in `handlers.go` already maps any `GetUserInfo` error to
   HTTP 502. The username validation (`usernameRegexp` check) happens in the
   callback handler after `GetUserInfo` returns, which maps to HTTP 400 — this
   is the existing GitHub path. So: `GetUserInfo` returns the sanitized username
   in `Login`; if sanitization yields empty, `GetUserInfo` returns an error. The
   callback handler never sees an empty `Login` field.

