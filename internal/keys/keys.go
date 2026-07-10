// Package keys implements API key management commands for the afc CLI:
// list, refresh, and revoke operations against the hub's key management API.
package keys

import (
	"fmt"
	"net/http"
)

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
