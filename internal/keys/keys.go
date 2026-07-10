// Package keys implements API key management for af-hub and the afc CLI:
// server-side handlers for key listing, refresh (secret rotation), and
// revocation; and client-side operations against the hub's key management API.
//
// Server endpoints:
//   - GET    /api/v1/keys                    (admin or user API key)
//   - POST   /api/v1/keys/:key_id/refresh    (admin or key owner)
//   - DELETE /api/v1/keys/:key_id            (admin or key owner)
package keys

import (
	"database/sql"
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
)

// ---------------------------------------------------------------------------
// Client types
// ---------------------------------------------------------------------------

// RefreshResponse represents the response from POST /api/v1/keys/:key_id/refresh.
// Per spec 02, this is a flat object (not wrapped in an api_key envelope).
// The Token field contains the full composite key (af_<key_id>_<secret>)
// suitable for Bearer authentication.
type RefreshResponse struct {
	KeyID     string `json:"key_id"`
	UserID    string `json:"user_id"`
	Secret    string `json:"secret"`
	Token     string `json:"token"`
	CreatedAt string `json:"created_at,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// ---------------------------------------------------------------------------
// Server types
// ---------------------------------------------------------------------------

// CredentialType identifies the kind of authentication credential used.
type CredentialType string

const (
	// CredentialTypeAdmin is an admin token credential.
	CredentialTypeAdmin CredentialType = "admin"
	// CredentialTypeAPIKey is a user API key credential.
	CredentialTypeAPIKey CredentialType = "api_key"
	// CredentialTypeWorkspaceToken is a workspace token credential.
	CredentialTypeWorkspaceToken CredentialType = "workspace_token"
)

// ContextKey is a typed key for Echo context values.
type ContextKey string

// AuthContextKey is the Echo context key for AuthContext.
const AuthContextKey ContextKey = "auth_context"

// AuthContext holds the authentication state set by auth middleware.
type AuthContext struct {
	CredentialType CredentialType
	UserID         string
	WorkspaceID    string
	IsAdmin        bool
}

// KeyRecord represents an API key row in the api_keys table.
type KeyRecord struct {
	KeyID     string  `json:"key_id"`
	UserID    string  `json:"user_id"`
	CreatedAt string  `json:"created_at"`
	ExpiresAt *string `json:"expires_at"`
	RevokedAt *string `json:"revoked_at"`
}

// KeyResponse is the response for key creation and refresh operations,
// which includes the plaintext secret and token.
type KeyResponse struct {
	KeyID     string  `json:"key_id"`
	UserID    string  `json:"user_id"`
	Secret    string  `json:"secret"`
	Token     string  `json:"token"`
	CreatedAt string  `json:"created_at"`
	ExpiresAt *string `json:"expires_at"`
	RevokedAt *string `json:"revoked_at"`
}

// ---------------------------------------------------------------------------
// Client functions
// ---------------------------------------------------------------------------

// ValidateKeyID checks that the key_id value is non-empty. Returns an error
// with the exact message if key_id is absent or empty.
func ValidateKeyID(keyID string) error {
	// Stub: not implemented yet.
	return nil
}

// ListKeys sends GET /api/v1/keys with Bearer authentication and returns the
// raw response body for pretty-printing, the HTTP status code, and any error.
func ListKeys(hubURL, apiKey string, client *http.Client) (body []byte, statusCode int, err error) {
	// Stub: not implemented yet.
	return nil, 0, fmt.Errorf("ListKeys not implemented")
}

// RefreshKey sends POST /api/v1/keys/:key_id/refresh with Bearer authentication.
// Returns the parsed response (for config update), the raw response body
// (for pretty-printing to stdout), and any error.
func RefreshKey(hubURL, apiKey, keyID string, client *http.Client) (*RefreshResponse, []byte, error) {
	// Stub: not implemented yet.
	return nil, nil, fmt.Errorf("RefreshKey not implemented")
}

// RevokeKey sends DELETE /api/v1/keys/:key_id with Bearer authentication.
// Returns the HTTP status code, any response body, and any error.
func RevokeKey(hubURL, apiKey, keyID string, client *http.Client) (statusCode int, body []byte, err error) {
	// Stub: not implemented yet.
	return 0, nil, fmt.Errorf("RevokeKey not implemented")
}

// ---------------------------------------------------------------------------
// Server handlers
// ---------------------------------------------------------------------------

// ListKeysHandler returns an Echo handler for GET /api/v1/keys.
// Admin token: returns all keys across all users ordered by created_at ASC.
// User API key: returns only the user's keys (including expired/revoked) ordered by created_at ASC.
// Workspace token: returns HTTP 403.
func ListKeysHandler(_ *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return echo.NewHTTPError(501, "not implemented")
	}
}

// RefreshKeyHandler returns an Echo handler for POST /api/v1/keys/:key_id/refresh.
// Generates a new secret for an existing non-revoked key, reusing the original
// expiry duration.
func RefreshKeyHandler(_ *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return echo.NewHTTPError(501, "not implemented")
	}
}

// RevokeKeyHandler returns an Echo handler for DELETE /api/v1/keys/:key_id.
// Sets revoked_at on the key. Idempotent for already-revoked keys.
func RevokeKeyHandler(_ *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return echo.NewHTTPError(501, "not implemented")
	}
}
