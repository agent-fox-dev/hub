package auth

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

// errorResponse is the standard JSON error envelope used by all endpoints.
type errorResponse struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// writeError writes a JSON error envelope response.
func writeError(c echo.Context, code int, message string) error {
	return c.JSON(code, errorResponse{
		Error: errorDetail{
			Code:    code,
			Message: message,
		},
	})
}

// hashSHA256 returns the hex-encoded SHA-256 hash of the given string.
func hashSHA256(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// Middleware returns Echo middleware that authenticates requests by validating
// Bearer tokens from the Authorization header. It identifies credential type
// by prefix (af_admin_, af_wt_, af_), verifies the hashed secret, resolves
// the associated user and workspace, checks expiry/revocation, and rejects
// blocked users.
//
// Supported token formats:
//   - af_admin_<64-hex-char secret>  — admin token
//   - af_wt_<8-char tokenID>_<32-char secret> — workspace token
//   - af_<8-char keyID>_<32-char secret> — user API key
func Middleware(db *sql.DB) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			authHeader := c.Request().Header.Get("Authorization")
			if authHeader == "" {
				return writeError(c, http.StatusUnauthorized, "missing authorization header")
			}

			token := strings.TrimPrefix(authHeader, "Bearer ")
			if token == authHeader {
				return writeError(c, http.StatusUnauthorized, "invalid authorization format")
			}

			var authCtx AuthContext

			switch {
			case strings.HasPrefix(token, "af_admin_"):
				// Admin token: af_admin_<secret>
				secret := strings.TrimPrefix(token, "af_admin_")
				if secret == "" {
					return writeError(c, http.StatusUnauthorized, "invalid admin token")
				}
				secretHash := hashSHA256(secret)

				var id string
				err := db.QueryRowContext(c.Request().Context(),
					`SELECT id FROM admin_tokens WHERE token_hash = ?`,
					secretHash,
				).Scan(&id)
				if err != nil {
					if err == sql.ErrNoRows {
						return writeError(c, http.StatusUnauthorized, "invalid admin token")
					}
					return writeError(c, http.StatusInternalServerError, "internal server error")
				}

				authCtx = AuthContext{
					CredentialType: CredentialTypeAdmin,
					IsAdmin:        true,
				}

			case strings.HasPrefix(token, "af_wt_"):
				// Workspace token: af_wt_<tokenID>_<secret>
				remainder := strings.TrimPrefix(token, "af_wt_")
				parts := strings.SplitN(remainder, "_", 2)
				if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
					return writeError(c, http.StatusUnauthorized, "invalid workspace token format")
				}
				tokenID := parts[0]
				secret := parts[1]
				secretHash := hashSHA256(secret)

				var wsID, userID string
				var expiresAt, revokedAt sql.NullString
				err := db.QueryRowContext(c.Request().Context(),
					`SELECT workspace_id, user_id, expires_at, revoked_at
					 FROM workspace_tokens
					 WHERE token_id = ? AND secret_hash = ?`,
					tokenID, secretHash,
				).Scan(&wsID, &userID, &expiresAt, &revokedAt)
				if err != nil {
					if err == sql.ErrNoRows {
						return writeError(c, http.StatusUnauthorized, "invalid workspace token")
					}
					return writeError(c, http.StatusInternalServerError, "internal server error")
				}

				// Check revocation.
				if revokedAt.Valid && revokedAt.String != "" {
					return writeError(c, http.StatusUnauthorized, "workspace token has been revoked")
				}

				// Check expiry.
				if expiresAt.Valid && expiresAt.String != "" {
					expTime, err := time.Parse(time.RFC3339, expiresAt.String)
					if err == nil && time.Now().UTC().After(expTime) {
						return writeError(c, http.StatusUnauthorized, "workspace token has expired")
					}
				}

				// Check blocked user.
				var userStatus string
				err = db.QueryRowContext(c.Request().Context(),
					`SELECT status FROM users WHERE id = ?`,
					userID,
				).Scan(&userStatus)
				if err != nil {
					return writeError(c, http.StatusUnauthorized, "user not found")
				}
				if userStatus == "blocked" {
					return writeError(c, http.StatusForbidden, "user account is blocked")
				}

				authCtx = AuthContext{
					CredentialType: CredentialTypeWorkspaceToken,
					UserID:         userID,
					WorkspaceID:    wsID,
				}

			case strings.HasPrefix(token, "af_"):
				// User API key: af_<keyID>_<secret>
				remainder := strings.TrimPrefix(token, "af_")
				parts := strings.SplitN(remainder, "_", 2)
				if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
					return writeError(c, http.StatusUnauthorized, "invalid API key format")
				}
				keyID := parts[0]
				secret := parts[1]
				secretHash := hashSHA256(secret)

				var userID string
				var revokedAt, apiExpiresAt sql.NullString
				err := db.QueryRowContext(c.Request().Context(),
					`SELECT user_id, revoked_at, expires_at FROM api_keys WHERE key_id = ? AND secret_hash = ?`,
					keyID, secretHash,
				).Scan(&userID, &revokedAt, &apiExpiresAt)
				if err != nil {
					if err == sql.ErrNoRows {
						return writeError(c, http.StatusUnauthorized, "invalid API key")
					}
					return writeError(c, http.StatusInternalServerError, "internal server error")
				}

				// Check revocation.
				if revokedAt.Valid && revokedAt.String != "" {
					return writeError(c, http.StatusUnauthorized, "API key has been revoked")
				}

				// Check expiry.
				if apiExpiresAt.Valid && apiExpiresAt.String != "" {
					expTime, err := time.Parse(time.RFC3339, apiExpiresAt.String)
					if err == nil && time.Now().UTC().After(expTime) {
						return writeError(c, http.StatusUnauthorized, "API key has expired")
					}
				}

				// Check blocked user.
				var userStatus string
				err = db.QueryRowContext(c.Request().Context(),
					`SELECT status FROM users WHERE id = ?`,
					userID,
				).Scan(&userStatus)
				if err != nil {
					return writeError(c, http.StatusUnauthorized, "user not found")
				}
				if userStatus == "blocked" {
					return writeError(c, http.StatusForbidden, "user account is blocked")
				}

				authCtx = AuthContext{
					CredentialType: CredentialTypeAPIKey,
					UserID:         userID,
				}

			default:
				return writeError(c, http.StatusUnauthorized, "unrecognized token format")
			}

			// Store the auth context for handlers to read.
			c.Set(string(AuthContextKey), authCtx)

			return next(c)
		}
	}
}
