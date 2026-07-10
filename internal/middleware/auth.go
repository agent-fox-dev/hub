// Package middleware also provides authentication middleware for af-hub.
//
// The canonical auth context types (AuthContext, ContextKey, CredentialType)
// live in internal/authctx to avoid import cycles. This file provides the
// middleware function that validates Bearer tokens and sets AuthContext.
package middleware

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/agent-fox-dev/hub/internal/authctx"
	"github.com/labstack/echo/v4"
)

// Sentinel errors for auth middleware internal routing.
var (
	errAuthFailed  = errors.New("auth: invalid credentials")
	errServiceBusy = errors.New("auth: service busy")
	errUserBlocked = errors.New("auth: user blocked")
)

// authErrorMessage is the standard 401 error message (REQ-14.1).
const authErrorMessage = "missing or invalid authentication credentials"

// AuthMiddleware returns Echo middleware that authenticates requests by
// extracting the Bearer token from the Authorization header and validating
// it against admin_tokens, api_keys, or workspace_tokens tables.
//
// Returns HTTP 401 with {"error": {"code": 401, "message": "missing or
// invalid authentication credentials"}} for any authentication failure.
//
// Token format recognition (checked in this order):
//   - af_admin_<64 hex chars>:           admin token
//   - af_wt_<token_id>_<secret>:         workspace token
//   - af_<key_id>_<secret>:              user API key
//
// All structural validation is performed before any DB query (REQ-14.6).
// Hash comparisons use crypto/subtle.ConstantTimeCompare (REQ-14.7).
// Admin token auth bypasses the blocked-user check (REQ-14.8).
func AuthMiddleware(db *sql.DB) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Extract Bearer token from Authorization header.
			token, ok := extractBearerToken(c)
			if !ok {
				return writeErrorJSON(c, http.StatusUnauthorized, authErrorMessage)
			}

			// Route by token prefix (most specific first).
			var ac *authctx.AuthContext
			var err error

			switch {
			case strings.HasPrefix(token, "af_admin_"):
				ac, err = verifyAdminToken(db, token)
			case strings.HasPrefix(token, "af_wt_"):
				ac, err = verifyWorkspaceToken(db, token)
			case strings.HasPrefix(token, "af_"):
				ac, err = verifyAPIKey(db, token)
			default:
				return writeErrorJSON(c, http.StatusUnauthorized, authErrorMessage)
			}

			// Map sentinel errors to HTTP responses.
			switch err {
			case nil:
				// Success — continue below.
			case errServiceBusy:
				return writeErrorJSON(c, http.StatusServiceUnavailable, "service temporarily unavailable")
			case errUserBlocked:
				return writeErrorJSON(c, http.StatusForbidden, "forbidden")
			default:
				// Any other error (including errAuthFailed) → 401.
				return writeErrorJSON(c, http.StatusUnauthorized, authErrorMessage)
			}

			// Store AuthContext in Echo context for downstream handlers.
			c.Set(string(authctx.AuthContextKey), ac)

			return next(c)
		}
	}
}

// extractBearerToken extracts the token from the Authorization header.
// Returns the token and true if the header is present, uses Bearer scheme,
// and the token value is non-empty after trimming whitespace.
func extractBearerToken(c echo.Context) (string, bool) {
	header := c.Request().Header.Get("Authorization")
	if header == "" {
		return "", false
	}
	if !strings.HasPrefix(header, "Bearer ") {
		return "", false
	}
	token := strings.TrimSpace(header[7:]) // len("Bearer ") == 7
	if token == "" {
		return "", false
	}
	return token, true
}

// verifyAdminToken validates an admin token (af_admin_<64 hex chars>).
// Structural validation is performed before any DB query.
// Admin tokens bypass the blocked-user check entirely (REQ-14.8).
func verifyAdminToken(db *sql.DB, token string) (*authctx.AuthContext, error) {
	// Strip prefix and validate suffix structure.
	suffix := token[9:] // len("af_admin_") == 9
	if len(suffix) != 64 || !isHexString(suffix) {
		return nil, errAuthFailed
	}

	// Compute SHA-256 of the suffix only (not the full token).
	computedHash := computeSHA256Hex(suffix)

	// Query admin_tokens for the stored hash.
	var storedHash string
	err := db.QueryRow("SELECT token_hash FROM admin_tokens LIMIT 1").Scan(&storedHash)
	if err != nil {
		if isSQLiteBusyError(err) {
			return nil, errServiceBusy
		}
		return nil, errAuthFailed
	}

	// Constant-time comparison to prevent timing attacks (REQ-14.7).
	if subtle.ConstantTimeCompare([]byte(computedHash), []byte(storedHash)) != 1 {
		return nil, errAuthFailed
	}

	// No users table lookup for admin tokens (REQ-14.8).
	return &authctx.AuthContext{
		CredentialType: authctx.CredentialTypeAdmin,
		IsAdmin:        true,
		UserID:         "",
		WorkspaceID:    "",
	}, nil
}

// verifyAPIKey validates a user API key (af_<key_id>_<secret>).
// The full token is split on "_" expecting exactly 3 parts: [af, key_id, secret].
// key_id must be exactly 8 alphanumeric characters, secret must be exactly 32
// alphanumeric characters.
func verifyAPIKey(db *sql.DB, token string) (*authctx.AuthContext, error) {
	// Split full token on "_" → ["af", key_id, secret].
	parts := strings.Split(token, "_")
	if len(parts) != 3 {
		return nil, errAuthFailed
	}

	keyID := parts[1]
	secret := parts[2]

	// Structural validation before any DB query (REQ-14.6).
	if len(keyID) != 8 || !isAlphanumeric(keyID) {
		return nil, errAuthFailed
	}
	if len(secret) != 32 || !isAlphanumeric(secret) {
		return nil, errAuthFailed
	}

	// Compute SHA-256 of the raw secret component only (not the full token
	// and not the key_id) — PROP-2.
	computedHash := computeSHA256Hex(secret)

	// Query api_keys by key_id.
	var secretHash, userID string
	var expiresAt, revokedAt sql.NullString
	err := db.QueryRow(
		"SELECT secret_hash, user_id, expires_at, revoked_at FROM api_keys WHERE key_id = ?",
		keyID,
	).Scan(&secretHash, &userID, &expiresAt, &revokedAt)
	if err != nil {
		if isSQLiteBusyError(err) {
			return nil, errServiceBusy
		}
		return nil, errAuthFailed
	}

	// Constant-time hash comparison (REQ-14.7).
	if subtle.ConstantTimeCompare([]byte(computedHash), []byte(secretHash)) != 1 {
		return nil, errAuthFailed
	}

	// Check revoked_at — non-null means revoked → 401 (REQ-14.E4).
	// No distinction is made between revoked and expired states.
	if revokedAt.Valid {
		return nil, errAuthFailed
	}

	// Check expires_at with exclusive boundary (REQ-14.E5):
	// expires_at < now → expired; expires_at == now → NOT expired.
	if expiresAt.Valid {
		expTime, parseErr := parseTimestamp(expiresAt.String)
		if parseErr == nil && expTime.Before(time.Now().UTC()) {
			return nil, errAuthFailed
		}
	}

	// Look up user status — blocked users are rejected with 403 (REQ-14.3).
	return checkUserStatus(db, userID, authctx.CredentialTypeAPIKey, "")
}

// verifyWorkspaceToken validates a workspace token (af_wt_<token_id>_<secret>).
// The "af_wt_" prefix is stripped, then the remainder is split on "_" expecting
// exactly 2 parts: [token_id, secret]. token_id must be exactly 8 alphanumeric
// characters, secret must be exactly 32 alphanumeric characters.
func verifyWorkspaceToken(db *sql.DB, token string) (*authctx.AuthContext, error) {
	// Strip "af_wt_" prefix and split remainder on "_".
	remainder := token[6:] // len("af_wt_") == 6

	// SplitN with limit 3 to detect extra parts beyond 2.
	parts := strings.SplitN(remainder, "_", 3)
	if len(parts) != 2 {
		return nil, errAuthFailed
	}

	tokenID := parts[0]
	secret := parts[1]

	// Structural validation before any DB query (REQ-14.6).
	if len(tokenID) != 8 || !isAlphanumeric(tokenID) {
		return nil, errAuthFailed
	}
	if len(secret) != 32 || !isAlphanumeric(secret) {
		return nil, errAuthFailed
	}

	// Compute SHA-256 of the raw secret component only — PROP-3.
	computedHash := computeSHA256Hex(secret)

	// Query workspace_tokens by token_id.
	var secretHash, wsID, userID string
	var expiresAt, revokedAt sql.NullString
	err := db.QueryRow(
		"SELECT secret_hash, workspace_id, user_id, expires_at, revoked_at FROM workspace_tokens WHERE token_id = ?",
		tokenID,
	).Scan(&secretHash, &wsID, &userID, &expiresAt, &revokedAt)
	if err != nil {
		if isSQLiteBusyError(err) {
			return nil, errServiceBusy
		}
		return nil, errAuthFailed
	}

	// Constant-time hash comparison (REQ-14.7).
	if subtle.ConstantTimeCompare([]byte(computedHash), []byte(secretHash)) != 1 {
		return nil, errAuthFailed
	}

	// Check revoked_at — non-null means revoked → 401 (REQ-14.E4).
	if revokedAt.Valid {
		return nil, errAuthFailed
	}

	// Check expires_at with exclusive boundary (REQ-14.E5).
	if expiresAt.Valid {
		expTime, parseErr := parseTimestamp(expiresAt.String)
		if parseErr == nil && expTime.Before(time.Now().UTC()) {
			return nil, errAuthFailed
		}
	}

	// Look up user status — blocked users are rejected with 403 (REQ-14.4).
	return checkUserStatus(db, userID, authctx.CredentialTypeWorkspaceToken, wsID)
}

// checkUserStatus queries the users table for the given userID and checks
// if the user is blocked. Returns an AuthContext on success or errUserBlocked
// if the user has status "blocked".
func checkUserStatus(db *sql.DB, userID string, credType authctx.CredentialType, workspaceID string) (*authctx.AuthContext, error) {
	var status string
	err := db.QueryRow("SELECT status FROM users WHERE id = ?", userID).Scan(&status)
	if err != nil {
		if isSQLiteBusyError(err) {
			return nil, errServiceBusy
		}
		return nil, errAuthFailed
	}

	if status == "blocked" {
		return nil, errUserBlocked
	}

	return &authctx.AuthContext{
		CredentialType: credType,
		UserID:         userID,
		WorkspaceID:    workspaceID,
		IsAdmin:        false,
	}, nil
}

// writeErrorJSON writes a JSON error envelope directly to the response.
// The envelope format is: {"error": {"code": <int>, "message": "<string>"}}.
//
// This writes the response directly rather than returning an echo.HTTPError
// because the custom error handler (task group 13) is not yet implemented.
func writeErrorJSON(c echo.Context, code int, message string) error {
	return c.JSON(code, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

// computeSHA256Hex computes the SHA-256 hash of s and returns the hex-encoded
// digest string.
func computeSHA256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// parseTimestamp parses an ISO 8601 timestamp string as used in the database.
// Tries RFC3339Nano first (nanosecond precision), then RFC3339 as fallback.
func parseTimestamp(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, s)
	}
	return t, err
}

// isHexString returns true if s consists entirely of hexadecimal characters
// (0-9, a-f, A-F).
func isHexString(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// isAlphanumeric returns true if s consists entirely of alphanumeric
// characters (0-9, a-z, A-Z).
func isAlphanumeric(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			return false
		}
	}
	return true
}

// isSQLiteBusyError returns true if the error indicates a SQLite busy/locked
// condition (SQLITE_BUSY or database is locked), which occurs when write
// contention exceeds the busy_timeout window.
func isSQLiteBusyError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "SQLITE_BUSY") ||
		strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked")
}
