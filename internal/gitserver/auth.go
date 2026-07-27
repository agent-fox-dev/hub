package gitserver

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"
)

// Credential prefixes used by af-hub for token classification.
const (
	patPrefix   = "af_pat_"
	keyPrefix   = "af_key_"
	adminPrefix = "af_admin_"
)

// GitAuthMiddleware returns an echo middleware that performs HTTP Basic
// authentication for git clients. It extracts the credential from the Basic
// auth password field (ignoring the username), resolves it to a user or admin
// identity, and attaches the identity to the request context.
//
// If no Authorization header is present, it returns HTTP 401 with
// WWW-Authenticate: Basic realm="af-hub".
//
// If the credential is unrecognized, it returns HTTP 401 without a
// WWW-Authenticate header.
func GitAuthMiddleware(db *sql.DB) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			authHeader := c.Request().Header.Get("Authorization")
			if authHeader == "" {
				c.Response().Header().Set("WWW-Authenticate", `Basic realm="af-hub"`)
				return c.NoContent(http.StatusUnauthorized)
			}

			// Must be Basic auth.
			if !strings.HasPrefix(authHeader, "Basic ") {
				return c.NoContent(http.StatusUnauthorized)
			}

			// Decode Base64 credentials.
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(authHeader, "Basic "))
			if err != nil {
				return c.NoContent(http.StatusUnauthorized)
			}

			// Split into username:password. Per 06-REQ-2.E2 the username is
			// ignored; the password is the hub credential.
			parts := strings.SplitN(string(decoded), ":", 2)
			if len(parts) != 2 {
				return c.NoContent(http.StatusUnauthorized)
			}
			password := parts[1]

			// Classify and validate the credential by prefix.
			info, err := resolveCredential(db, password)
			if err != nil || info == nil {
				return c.NoContent(http.StatusUnauthorized)
			}

			apikit.SetAuthInfo(c, info)
			return next(c)
		}
	}
}

// resolveCredential classifies a hub credential by its prefix and validates
// it against the database, returning the resolved identity.
func resolveCredential(db *sql.DB, token string) (*apikit.AuthInfo, error) {
	switch {
	case strings.HasPrefix(token, patPrefix):
		return resolvePAT(db, token)
	case strings.HasPrefix(token, keyPrefix):
		return resolveAPIKey(db, token)
	case strings.HasPrefix(token, adminPrefix):
		return resolveAdminToken(db, token)
	default:
		return nil, nil
	}
}

// resolvePAT validates a PAT credential against the pats table.
//
// Supports two token formats:
//   - Simple:  af_pat_<identifier>        — identifier is both the lookup key and the secret
//   - Apikit:  af_pat_<token_id>_<secret>  — split by first underscore
//
// The simple format is tried first (full remainder as token_id). If not found,
// the function falls back to splitting into token_id + secret for
// apikit-compatible tokens.
func resolvePAT(db *sql.DB, token string) (*apikit.AuthInfo, error) {
	rest := strings.TrimPrefix(token, patPrefix)
	if rest == "" {
		return nil, nil
	}

	// Try 1: full remainder as token_id, hash of remainder as secret.
	info, err := lookupPAT(db, rest, rest)
	if err == nil && info != nil {
		return info, nil
	}

	// Try 2: split into token_id + secret (apikit production format).
	if idx := strings.Index(rest, "_"); idx > 0 && idx < len(rest)-1 {
		tokenID := rest[:idx]
		secret := rest[idx+1:]
		info, err := lookupPAT(db, tokenID, secret)
		if err == nil && info != nil {
			return info, nil
		}
	}

	return nil, nil
}

// lookupPAT queries the pats table by token_id and verifies the secret hash.
func lookupPAT(db *sql.DB, tokenID, secret string) (*apikit.AuthInfo, error) {
	var userID, secretHash, permissions string
	err := db.QueryRow(
		`SELECT user_id, secret_hash, permissions FROM pats
		 WHERE token_id = ? AND revoked_at IS NULL
		 AND (expires_at IS NULL OR datetime(expires_at) > datetime('now'))`,
		tokenID,
	).Scan(&userID, &secretHash, &permissions)
	if err != nil {
		return nil, err
	}

	computed := hashCredential(secret)
	if subtle.ConstantTimeCompare([]byte(computed), []byte(secretHash)) != 1 {
		return nil, nil
	}

	var perms []string
	_ = json.Unmarshal([]byte(permissions), &perms)

	role := lookupUserRole(db, userID)

	return &apikit.AuthInfo{
		CredentialType: "pat",
		UserID:         userID,
		Role:           role,
		TokenID:        tokenID,
		Permissions:    perms,
	}, nil
}

// resolveAPIKey validates an API key credential against the api_keys table.
func resolveAPIKey(db *sql.DB, token string) (*apikit.AuthInfo, error) {
	rest := strings.TrimPrefix(token, keyPrefix)
	if rest == "" {
		return nil, nil
	}

	// Try 1: full remainder as key_id.
	info, err := lookupAPIKey(db, rest, rest)
	if err == nil && info != nil {
		return info, nil
	}

	// Try 2: split into key_id + secret.
	if idx := strings.Index(rest, "_"); idx > 0 && idx < len(rest)-1 {
		keyID := rest[:idx]
		secret := rest[idx+1:]
		info, err := lookupAPIKey(db, keyID, secret)
		if err == nil && info != nil {
			return info, nil
		}
	}

	return nil, nil
}

// lookupAPIKey queries the api_keys table by key_id and verifies the secret hash.
func lookupAPIKey(db *sql.DB, keyID, secret string) (*apikit.AuthInfo, error) {
	var userID, secretHash string
	err := db.QueryRow(
		`SELECT user_id, secret_hash FROM api_keys
		 WHERE key_id = ? AND revoked_at IS NULL
		 AND (expires_at IS NULL OR datetime(expires_at) > datetime('now'))`,
		keyID,
	).Scan(&userID, &secretHash)
	if err != nil {
		return nil, err
	}

	computed := hashCredential(secret)
	if subtle.ConstantTimeCompare([]byte(computed), []byte(secretHash)) != 1 {
		return nil, nil
	}

	role := lookupUserRole(db, userID)

	return &apikit.AuthInfo{
		CredentialType: "api_key",
		UserID:         userID,
		Role:           role,
		KeyID:          keyID,
	}, nil
}

// resolveAdminToken validates an admin token by hashing the full token and
// comparing it against stored hashes in the admin_config table.
func resolveAdminToken(db *sql.DB, token string) (*apikit.AuthInfo, error) {
	tokenHash := hashCredential(token)

	var key string
	err := db.QueryRow(
		`SELECT key FROM admin_config WHERE key LIKE 'admin_token_hash%' AND value = ?`,
		tokenHash,
	).Scan(&key)
	if err != nil {
		return nil, err
	}

	return &apikit.AuthInfo{
		CredentialType: "admin_token",
		UserID:         "",
		Role:           "admin",
	}, nil
}

// lookupUserRole queries the users table for a user's role. Returns "user" if
// the lookup fails.
func lookupUserRole(db *sql.DB, userID string) string {
	var role string
	if err := db.QueryRow(`SELECT role FROM users WHERE id = ?`, userID).Scan(&role); err != nil {
		return "user"
	}
	return role
}

// hashCredential returns the hex-encoded SHA-256 hash of the input string.
// This matches apikit's internal hashing convention.
func hashCredential(input string) string {
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])
}
