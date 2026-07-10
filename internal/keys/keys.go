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
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"github.com/agent-fox-dev/hub/internal/apierror"
	"github.com/agent-fox-dev/hub/internal/httpclient"
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
	if keyID == "" {
		return fmt.Errorf(`Error: key_id is not set. Run "afc login" first.`)
	}
	return nil
}

// ListKeys sends GET /api/v1/keys with Bearer authentication and returns the
// raw response body for pretty-printing, the HTTP status code, and any error.
// For a reachable server, err is nil even on non-2xx responses; the caller
// decides what to do based on statusCode.
func ListKeys(hubURL, apiKey string, client *http.Client) (body []byte, statusCode int, err error) {
	resp, err := httpclient.DoRequest(client, "GET", hubURL+"/api/v1/keys", apiKey, nil)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read response body: %w", err)
	}
	return data, resp.StatusCode, nil
}

// RefreshKey sends POST /api/v1/keys/:key_id/refresh with Bearer authentication.
// Returns the parsed response (for config update), the raw response body
// (for pretty-printing to stdout), and any error. On non-2xx responses,
// an error is returned so the caller can handle it appropriately.
func RefreshKey(hubURL, apiKey, keyID string, client *http.Client) (*RefreshResponse, []byte, error) {
	url := fmt.Sprintf("%s/api/v1/keys/%s/refresh", hubURL, keyID)
	resp, err := httpclient.DoRequest(client, "POST", url, apiKey, nil)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, data, fmt.Errorf("server returned HTTP %d: %s",
			resp.StatusCode, apierror.HandleResponseBody(resp.StatusCode, data))
	}

	var refreshResp RefreshResponse
	if err := json.Unmarshal(data, &refreshResp); err != nil {
		return nil, data, fmt.Errorf("failed to parse refresh response: %w", err)
	}
	return &refreshResp, data, nil
}

// RevokeKey sends DELETE /api/v1/keys/:key_id with Bearer authentication.
// Returns the HTTP status code, any response body, and any error.
// For a reachable server, err is nil even on non-2xx responses; the caller
// decides what to do based on statusCode.
func RevokeKey(hubURL, apiKey, keyID string, client *http.Client) (statusCode int, body []byte, err error) {
	url := fmt.Sprintf("%s/api/v1/keys/%s", hubURL, keyID)
	resp, err := httpclient.DoRequest(client, "DELETE", url, apiKey, nil)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("failed to read response body: %w", err)
	}
	return resp.StatusCode, data, nil
}

// ---------------------------------------------------------------------------
// Server handlers
// ---------------------------------------------------------------------------

// getAuthContext extracts the AuthContext from the Echo context.
func getAuthContext(c echo.Context) *AuthContext {
	ac, _ := c.Get(string(AuthContextKey)).(*AuthContext)
	return ac
}

// ListKeysHandler returns an Echo handler for GET /api/v1/keys.
// Admin token: returns all keys across all users ordered by created_at ASC.
// User API key: returns only the user's keys (including expired/revoked) ordered by created_at ASC.
// Workspace token: returns HTTP 403.
func ListKeysHandler(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		ac := getAuthContext(c)
		if ac == nil {
			return echo.NewHTTPError(http.StatusUnauthorized, "authentication required")
		}

		// Workspace tokens are forbidden from listing keys.
		if ac.CredentialType == CredentialTypeWorkspaceToken {
			return echo.NewHTTPError(http.StatusForbidden, "workspace tokens cannot list API keys")
		}

		var rows *sql.Rows
		var err error

		if ac.IsAdmin {
			rows, err = db.QueryContext(c.Request().Context(),
				`SELECT key_id, user_id, created_at, expires_at, revoked_at
				 FROM api_keys ORDER BY created_at ASC`)
		} else {
			rows, err = db.QueryContext(c.Request().Context(),
				`SELECT key_id, user_id, created_at, expires_at, revoked_at
				 FROM api_keys WHERE user_id = ? ORDER BY created_at ASC`,
				ac.UserID)
		}
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to query keys")
		}
		defer rows.Close()

		keys := make([]KeyRecord, 0)
		for rows.Next() {
			var k KeyRecord
			if err := rows.Scan(&k.KeyID, &k.UserID, &k.CreatedAt, &k.ExpiresAt, &k.RevokedAt); err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to scan key row")
			}
			keys = append(keys, k)
		}
		if err := rows.Err(); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to iterate key rows")
		}

		return c.JSON(http.StatusOK, map[string]any{
			"keys": keys,
		})
	}
}

// RefreshKeyHandler returns an Echo handler for POST /api/v1/keys/:key_id/refresh.
// Generates a new secret for an existing non-revoked key, reusing the original
// expiry duration.
func RefreshKeyHandler(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		ac := getAuthContext(c)
		if ac == nil {
			return echo.NewHTTPError(http.StatusUnauthorized, "authentication required")
		}

		keyID := c.Param("key_id")

		// Look up the key; revoked keys are treated as non-existent.
		var userID, createdAt string
		var expiresAt, revokedAt sql.NullString
		err := db.QueryRowContext(c.Request().Context(),
			`SELECT user_id, created_at, expires_at, revoked_at
			 FROM api_keys WHERE key_id = ?`,
			keyID,
		).Scan(&userID, &createdAt, &expiresAt, &revokedAt)
		if err != nil {
			if err == sql.ErrNoRows {
				return echo.NewHTTPError(http.StatusNotFound, "API key not found")
			}
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to query key")
		}

		// Revoked keys are treated as non-existent for refresh.
		if revokedAt.Valid && revokedAt.String != "" {
			return echo.NewHTTPError(http.StatusNotFound, "API key not found")
		}

		// Authorization: non-admin can only refresh their own keys.
		if !ac.IsAdmin && ac.UserID != userID {
			return echo.NewHTTPError(http.StatusForbidden, "you can only refresh your own API keys")
		}

		// Compute original expiry duration in days.
		originalDays := 0
		if expiresAt.Valid && expiresAt.String != "" {
			createdTime, err := time.Parse(time.RFC3339, createdAt)
			if err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to parse created_at")
			}
			expiresTime, err := time.Parse(time.RFC3339, expiresAt.String)
			if err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to parse expires_at")
			}
			durationHours := expiresTime.Sub(createdTime).Hours()
			originalDays = max(int(math.Ceil(durationHours/24)), 1)
		}

		// Generate new secret.
		newSecret, err := GenerateSecret()
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to generate secret")
		}

		newHash := HashSecret(newSecret)
		now := time.Now().UTC()

		// Compute new expires_at.
		newExpiresAt := ComputeExpiresAt(originalDays, now)
		var newExpiresAtStr *string
		if newExpiresAt != nil {
			s := newExpiresAt.Format(time.RFC3339)
			newExpiresAtStr = &s
		}

		// Update the key in the database.
		_, err = db.ExecContext(c.Request().Context(),
			`UPDATE api_keys SET secret_hash = ?, expires_at = ? WHERE key_id = ?`,
			newHash, newExpiresAtStr, keyID,
		)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to update key")
		}

		token := BuildToken(keyID, newSecret)

		return c.JSON(http.StatusOK, KeyResponse{
			KeyID:     keyID,
			UserID:    userID,
			Secret:    newSecret,
			Token:     token,
			CreatedAt: createdAt,
			ExpiresAt: newExpiresAtStr,
			RevokedAt: nil,
		})
	}
}

// RevokeKeyHandler returns an Echo handler for DELETE /api/v1/keys/:key_id.
// Sets revoked_at on the key. Idempotent for already-revoked keys.
func RevokeKeyHandler(db *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		ac := getAuthContext(c)
		if ac == nil {
			return echo.NewHTTPError(http.StatusUnauthorized, "authentication required")
		}

		keyID := c.Param("key_id")

		// Look up the key.
		var userID string
		var revokedAt sql.NullString
		err := db.QueryRowContext(c.Request().Context(),
			`SELECT user_id, revoked_at FROM api_keys WHERE key_id = ?`,
			keyID,
		).Scan(&userID, &revokedAt)
		if err != nil {
			if err == sql.ErrNoRows {
				return echo.NewHTTPError(http.StatusNotFound, "API key not found")
			}
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to query key")
		}

		// Authorization: non-admin can only revoke their own keys.
		if !ac.IsAdmin && ac.UserID != userID {
			return echo.NewHTTPError(http.StatusForbidden, "you can only revoke your own API keys")
		}

		// If already revoked, return 204 (idempotent no-op).
		if revokedAt.Valid && revokedAt.String != "" {
			return c.NoContent(http.StatusNoContent)
		}

		// Set revoked_at.
		now := time.Now().UTC().Format(time.RFC3339)
		_, err = db.ExecContext(c.Request().Context(),
			`UPDATE api_keys SET revoked_at = ? WHERE key_id = ? AND revoked_at IS NULL`,
			now, keyID,
		)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to revoke key")
		}

		return c.NoContent(http.StatusNoContent)
	}
}
