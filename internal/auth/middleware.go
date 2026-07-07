// Package auth — authentication middleware for af-hub.
package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/agent-fox/af-hub/internal/store"
	"github.com/labstack/echo/v4"
)

// AuthMiddleware returns an Echo middleware function that validates bearer
// tokens on all protected routes (/api/v1/* except /api/v1/auth/*).
//
// Token types:
//   - Admin tokens: prefixed with "af_admin_", hashed with SHA-256 and
//     compared against the admin_tokens table.
//   - API keys: format "af_<key_id>_<secret>", key_id is looked up in
//     api_keys, secret is hashed and compared against key_hash.
//
// On successful validation, the middleware populates the request context
// with user_id, role, workspace_id (for API keys), auth_method, and
// user_status. If the user's status is "blocked", the middleware returns
// HTTP 403 regardless of token validity.
func AuthMiddleware(s store.Store) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Extract the token from the Authorization header.
			token, err := extractBearerToken(c)
			if err != nil {
				return newAuthError(c, http.StatusUnauthorized, "missing or malformed token")
			}

			// Route based on token prefix.
			if strings.HasPrefix(token, "af_admin_") {
				return handleAdminToken(c, s, token, next)
			}

			if strings.HasPrefix(token, "af_") {
				return handleAPIKeyToken(c, s, token, next)
			}

			// Token format not recognized.
			return newAuthError(c, http.StatusUnauthorized, "missing or malformed token")
		}
	}
}

// extractBearerToken extracts the bearer token from the Authorization header.
// Returns an error if the header is missing or does not start with "Bearer ".
func extractBearerToken(c echo.Context) (string, error) {
	header := c.Request().Header.Get("Authorization")
	if header == "" {
		return "", fmt.Errorf("missing Authorization header")
	}

	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", fmt.Errorf("missing Bearer prefix")
	}

	token := strings.TrimPrefix(header, prefix)
	if token == "" {
		return "", fmt.Errorf("empty token")
	}

	return token, nil
}

// handleAdminToken processes admin token authentication. The full token is
// hashed with SHA-256 and compared against the admin_tokens table.
//
// Note: admin_tokens has no user_id FK (spec 01 schema gap). The token's
// record ID is used as user_id, and status is hardcoded to "active" since
// there is no way to look up the admin user's actual status.
func handleAdminToken(c echo.Context, s store.Store, token string, next echo.HandlerFunc) error {
	hash := sha256Hex(token)

	adminToken, err := s.GetAdminTokenByHash(hash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return newAuthError(c, http.StatusUnauthorized, "invalid token")
		}
		// Database error — do not leak details.
		return newAuthError(c, http.StatusInternalServerError, "internal server error")
	}

	// Populate context with admin auth info.
	// Since admin_tokens has no user_id FK, use the token record ID.
	c.Set(ContextKeyUserID, adminToken.ID)
	c.Set(ContextKeyRole, RoleAdmin)
	c.Set(ContextKeyAuthMethod, AuthMethodAdmin)
	c.Set(ContextKeyUserStatus, "active")
	c.Set(ContextKeyWorkspaceID, "")

	return next(c)
}

// handleAPIKeyToken processes API key token authentication. The token format
// is "af_<key_id>_<secret>". Since key_id and secret may both contain
// underscores, the function tries each possible split point from right to
// left, looking up the key_id in the database until a match is found.
func handleAPIKeyToken(c echo.Context, s store.Store, token string, next echo.HandlerFunc) error {
	// Strip "af_" prefix.
	rest := strings.TrimPrefix(token, "af_")
	if rest == token || rest == "" {
		return newAuthError(c, http.StatusUnauthorized, "missing or malformed token")
	}

	// Try each underscore position from right to left as the split point
	// between key_id and secret. This handles the case where key_id or
	// secret contain underscores.
	for i := len(rest) - 1; i > 0; i-- {
		if rest[i] != '_' {
			continue
		}

		keyID := rest[:i]
		secret := rest[i+1:]
		if keyID == "" || secret == "" {
			continue
		}

		apiKey, err := s.GetAPIKeyByKeyID(keyID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				// This key_id doesn't exist; try the next split point.
				continue
			}
			// Database error — do not leak details.
			return newAuthError(c, http.StatusInternalServerError, "internal server error")
		}

		// Found a key record. Verify the secret hash.
		secretHash := sha256Hex(secret)
		if apiKey.KeyHash != secretHash {
			return newAuthError(c, http.StatusUnauthorized, "invalid, revoked, or expired API key")
		}

		// Check if the key is revoked.
		if apiKey.RevokedAt != nil {
			return newAuthError(c, http.StatusUnauthorized, "invalid, revoked, or expired API key")
		}

		// Check if the key is expired.
		if apiKey.ExpiresAt != nil && apiKey.ExpiresAt.Before(time.Now()) {
			return newAuthError(c, http.StatusUnauthorized, "invalid, revoked, or expired API key")
		}

		// Look up the user to check their status.
		user, err := s.GetUserByID(apiKey.UserID)
		if err != nil {
			return newAuthError(c, http.StatusInternalServerError, "internal server error")
		}

		// Check if the user is blocked.
		if user.Status == "blocked" {
			return newAuthError(c, http.StatusForbidden, "user is blocked")
		}

		// Populate context with API key auth info.
		c.Set(ContextKeyUserID, apiKey.UserID)
		c.Set(ContextKeyRole, apiKey.Role)
		c.Set(ContextKeyWorkspaceID, apiKey.WorkspaceID)
		c.Set(ContextKeyAuthMethod, AuthMethodAPIKey)
		c.Set(ContextKeyUserStatus, user.Status)

		return next(c)
	}

	// No valid key_id found at any split position.
	return newAuthError(c, http.StatusUnauthorized, "invalid, revoked, or expired API key")
}

// sha256Hex computes the hex-encoded SHA-256 hash of a string.
func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// newAuthError creates a standard error response for authentication failures.
func newAuthError(c echo.Context, status int, message string) error {
	return c.JSON(status, map[string]any{
		"error": map[string]any{
			"code":    fmt.Sprintf("%d", status),
			"message": message,
		},
	})
}
