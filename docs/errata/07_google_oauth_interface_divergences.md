# Errata: Google OAuth Provider Interface Divergences (Spec 07)

## AuthorizeURL Signature Mismatch

**Spec reference:** TS-07-5, Task 1.4, Task 4.3

**Spec states:** `AuthorizeURL(state, redirectURI string) string` — the method
accepts state and redirect URI parameters and returns a full URL with query
parameters (`response_type=code`, `client_id`, `redirect_uri`, `state`, `scope`).

**Actual interface:** `AuthorizeURL() string` — zero parameters, returns only
the base authorization URL (e.g. `https://accounts.google.com/o/oauth2/v2/auth`).

**How it actually works:**
- `Provider.AuthorizeURL()` returns the base URL only.
- `Registry.List()` appends `client_id` and `scope` query parameters.
- The CLI client appends `state`, `redirect_uri`, and `response_type=code`.

**Test adaptation:** `TestGoogleProvider_AuthorizeURL` tests the base URL return.
`TestGoogleProvider_AuthorizeURL_ViaRegistry` tests the integration with
`Registry.List()` to verify `client_id` and `scope` are appended correctly.

## ExchangeCode Return Type

**Spec reference:** REQ-3.2, REQ-3.3, TS-07-6 through TS-07-E3

**Spec states:** `ExchangeCode` returns `(string, error)` — the access token
as a bare string.

**Actual interface:** `ExchangeCode(ctx context.Context, code, redirectURI string) (*TokenResponse, error)` — returns a `*TokenResponse` struct wrapping `AccessToken string`.

**Impact:** Test assertions must use `resp.AccessToken` instead of directly
comparing the string return value. Error cases return `(nil, error)` not
`("", error)`.

## Constructor Parameter Type

**Spec reference:** REQ-1.2

**Spec states:** `NewGoogleProvider` accepts an `OAuthProvider` config struct
(from `internal/serverconfig`).

**Actual pattern:** `NewGitHubProvider` (and therefore `NewGoogleProvider`)
accepts `ProviderConfig` (from `internal/auth/provider.go`). In `main.go`,
`serverconfig.OAuthProvider` fields are mapped to `auth.ProviderConfig`
manually. Note the field name difference: `OAuthProvider.UserinfoURL`
(lowercase 'i') vs `ProviderConfig.UserInfoURL` (uppercase 'I').
