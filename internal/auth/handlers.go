package auth

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/keys"
)

// ProvidersResponse is the response body for GET /api/v1/auth/providers.
type ProvidersResponse struct {
	Providers []ProviderEntry `json:"providers"`
}

// GetProvidersHandler returns an Echo handler for GET /api/v1/auth/providers.
// This is a public endpoint (no authentication required).
//
// It returns the list of registered OAuth providers with their name,
// authorize_url, and scopes. No client secrets, token URLs, or userinfo
// URLs are included in the response.
//
// Returns HTTP 200 with {"providers": [...]}. If no providers are registered,
// returns HTTP 200 with {"providers": []}.
func GetProvidersHandler(registry *Registry) echo.HandlerFunc {
	return func(c echo.Context) error {
		entries := registry.List()

		// Ensure we always return an empty array, not null.
		if entries == nil {
			entries = []ProviderEntry{}
		}

		return c.JSON(http.StatusOK, ProvidersResponse{
			Providers: entries,
		})
	}
}

// ---------------------------------------------------------------------------
// POST /api/v1/auth/callback
// ---------------------------------------------------------------------------

// CallbackRequest is the JSON body for POST /api/v1/auth/callback.
type CallbackRequest struct {
	Provider    string `json:"provider"`
	Code        string `json:"code"`
	State       string `json:"state"` // Passed through; not validated by server.
	RedirectURI string `json:"redirect_uri"`
	Expires     *int   `json:"expires"` // Pointer to distinguish omitted from 0.
}

// callbackUserResponse is the user portion of the callback response.
type callbackUserResponse struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Email     string `json:"email"`
	FullName  string `json:"full_name"`
	Status    string `json:"status"`
	Provider  string `json:"provider"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// callbackKeyResponse is the api_key portion of the callback response.
type callbackKeyResponse struct {
	KeyID     string  `json:"key_id"`
	Secret    string  `json:"secret"`
	Token     string  `json:"token"`
	UserID    string  `json:"user_id"`
	CreatedAt string  `json:"created_at"`
	ExpiresAt *string `json:"expires_at"`
}

// callbackResponse is the full response body for POST /api/v1/auth/callback.
type callbackResponse struct {
	User   callbackUserResponse `json:"user"`
	APIKey callbackKeyResponse  `json:"api_key"`
}

// usernameRegexp validates usernames: alphanumeric and hyphens only, 1-39 chars.
var usernameRegexp = regexp.MustCompile(`^[0-9A-Za-z-]{1,39}$`)

// validExpiresValues is the set of accepted values for the expires field.
var validExpiresValues = map[int]bool{0: true, 30: true, 60: true, 90: true}

// defaultExpiresValue is used when expires is omitted from the request.
const defaultExpiresValue = 90

// oauthTimeout is the context timeout for outbound OAuth provider calls.
const oauthTimeout = 10 * time.Second

// CallbackHandler returns an Echo handler for POST /api/v1/auth/callback.
// This is a public endpoint (no auth middleware) that:
//  1. Validates provider, expires, and redirect_uri.
//  2. Exchanges the authorization code with the provider.
//  3. Fetches and validates user info from the provider.
//  4. Upserts the user, revokes existing active keys, and creates a new key
//     in a single database transaction.
//  5. Returns HTTP 200 with the user object and new API key.
func CallbackHandler(db *sql.DB, registry *Registry, allowlist *Allowlist) echo.HandlerFunc {
	return func(c echo.Context) error {
		var req CallbackRequest
		if err := c.Bind(&req); err != nil {
			return writeError(c, http.StatusBadRequest, "invalid request body")
		}

		// ------------------------------------------------------------------
		// 6.1: Validate provider, expires, redirect_uri
		// ------------------------------------------------------------------

		// Validate provider against registry.
		provider, ok := registry.Lookup(req.Provider)
		if !ok {
			return writeError(c, http.StatusBadRequest,
				"unrecognized provider: "+req.Provider)
		}

		// Validate expires (default to 90 if omitted).
		expiresInDays := defaultExpiresValue
		if req.Expires != nil {
			expiresInDays = *req.Expires
			if !validExpiresValues[expiresInDays] {
				return writeError(c, http.StatusBadRequest,
					"invalid expires value; accepted values are [0, 30, 60, 90]")
			}
		}

		// Validate redirect_uri against allowlist.
		if err := allowlist.IsAllowed(req.RedirectURI); err != nil {
			return writeError(c, http.StatusBadRequest,
				"redirect_uri not allowed: "+err.Error())
		}

		// ------------------------------------------------------------------
		// 6.2: Exchange code and fetch user info with timeout
		// ------------------------------------------------------------------

		ctx, cancel := context.WithTimeout(c.Request().Context(), oauthTimeout)
		defer cancel()

		tokenResp, err := provider.ExchangeCode(ctx, req.Code, req.RedirectURI)
		if err != nil {
			return writeError(c, http.StatusBadGateway,
				"OAuth provider code exchange failed: "+err.Error())
		}

		userInfo, err := provider.GetUserInfo(ctx, tokenResp.AccessToken)
		if err != nil {
			return writeError(c, http.StatusBadGateway,
				"OAuth provider user info retrieval failed: "+err.Error())
		}

		// Validate email is non-empty.
		if userInfo.Email == "" {
			return writeError(c, http.StatusBadRequest,
				"OAuth provider returned empty email; email is required")
		}

		// Validate username (derived from provider login).
		if !usernameRegexp.MatchString(userInfo.Login) {
			return writeError(c, http.StatusBadRequest,
				"invalid username from provider: must contain only alphanumeric characters and hyphens, max 39 characters")
		}

		// ------------------------------------------------------------------
		// 6.3: User upsert + key lifecycle in a single transaction
		// ------------------------------------------------------------------

		tx, err := db.BeginTx(c.Request().Context(), nil)
		if err != nil {
			return writeError(c, http.StatusInternalServerError,
				"failed to start database transaction")
		}
		defer tx.Rollback() //nolint:errcheck

		// Check if user exists for this (provider, provider_id).
		var userID, storedUsername, storedEmail, storedFullName, storedStatus, storedCreatedAt, storedUpdatedAt string
		err = tx.QueryRowContext(c.Request().Context(),
			`SELECT id, username, email, full_name, status, created_at, updated_at
			 FROM users WHERE provider = ? AND provider_id = ?`,
			req.Provider, userInfo.ID,
		).Scan(&userID, &storedUsername, &storedEmail, &storedFullName, &storedStatus, &storedCreatedAt, &storedUpdatedAt)

		var isNewUser bool
		var finalUsername, finalEmail, finalFullName, finalStatus, finalCreatedAt, finalUpdatedAt string

		if err == sql.ErrNoRows {
			// ---------------------------------------------------------------
			// New user: check for case-insensitive username conflict.
			// ---------------------------------------------------------------
			isNewUser = true

			var conflictID string
			err = tx.QueryRowContext(c.Request().Context(),
				`SELECT id FROM users WHERE LOWER(username) = LOWER(?)`,
				userInfo.Login,
			).Scan(&conflictID)
			if err == nil {
				// A user with this username (case-insensitive) already exists
				// but belongs to a different (provider, provider_id) pair.
				return writeError(c, http.StatusConflict,
					"username already taken by another user")
			} else if err != sql.ErrNoRows {
				return writeError(c, http.StatusInternalServerError,
					"failed to check username uniqueness")
			}

			// Create new user.
			userID = uuid.New().String()
			now := time.Now().UTC().Format(time.RFC3339)
			finalUsername = userInfo.Login
			finalEmail = userInfo.Email
			finalFullName = userInfo.Name
			finalStatus = "active"
			finalCreatedAt = now
			finalUpdatedAt = now

			_, err = tx.ExecContext(c.Request().Context(),
				`INSERT INTO users (id, username, email, full_name, status, provider, provider_id, created_at, updated_at)
				 VALUES (?, ?, ?, ?, 'active', ?, ?, ?, ?)`,
				userID, finalUsername, finalEmail, finalFullName,
				req.Provider, userInfo.ID, finalCreatedAt, finalUpdatedAt,
			)
			if err != nil {
				return writeError(c, http.StatusInternalServerError,
					"failed to create user record")
			}

		} else if err != nil {
			return writeError(c, http.StatusInternalServerError,
				"failed to query user record")
		} else {
			// ---------------------------------------------------------------
			// Existing user: check blocked status, then update if needed.
			// ---------------------------------------------------------------
			isNewUser = false

			if storedStatus == "blocked" {
				return writeError(c, http.StatusForbidden,
					"user account is blocked")
			}

			// Check for case-insensitive username conflict with a DIFFERENT
			// (provider, provider_id) pair — i.e. a different user.
			if strings.EqualFold(userInfo.Login, storedUsername) {
				// Same user (case-insensitive match is this user) — no conflict.
			} else {
				// The provider returned a different username; check if it
				// conflicts with another user.
				var conflictID string
				err = tx.QueryRowContext(c.Request().Context(),
					`SELECT id FROM users WHERE LOWER(username) = LOWER(?) AND id != ?`,
					userInfo.Login, userID,
				).Scan(&conflictID)
				if err == nil {
					return writeError(c, http.StatusConflict,
						"username already taken by another user")
				} else if err != sql.ErrNoRows {
					return writeError(c, http.StatusInternalServerError,
						"failed to check username uniqueness")
				}
			}

			// Update username and email from provider response.
			// Do NOT update provider_id (immutable after creation).
			// Only bump updated_at if at least one value changed.
			usernameChanged := storedUsername != userInfo.Login
			emailChanged := storedEmail != userInfo.Email

			finalUsername = userInfo.Login
			finalEmail = userInfo.Email
			finalFullName = storedFullName
			finalStatus = storedStatus
			finalCreatedAt = storedCreatedAt
			finalUpdatedAt = storedUpdatedAt

			if usernameChanged || emailChanged {
				now := time.Now().UTC().Format(time.RFC3339)
				finalUpdatedAt = now
				_, err = tx.ExecContext(c.Request().Context(),
					`UPDATE users SET username = ?, email = ?, updated_at = ? WHERE id = ?`,
					finalUsername, finalEmail, finalUpdatedAt, userID,
				)
				if err != nil {
					return writeError(c, http.StatusInternalServerError,
						"failed to update user record")
				}
			}
		}

		_ = isNewUser // may be used for logging in the future

		// Revoke any existing active keys for the user.
		if err := keys.RevokeActiveKey(c.Request().Context(), tx, userID); err != nil {
			return writeError(c, http.StatusInternalServerError,
				"failed to revoke existing API keys")
		}

		// Create a new API key.
		keyRec, plainSecret, err := keys.CreateKey(c.Request().Context(), tx, userID, expiresInDays)
		if err != nil {
			return writeError(c, http.StatusInternalServerError,
				"failed to create API key")
		}

		// Commit the transaction.
		if err := tx.Commit(); err != nil {
			return writeError(c, http.StatusInternalServerError,
				"failed to commit database transaction")
		}

		token := keys.BuildToken(keyRec.KeyID, plainSecret)

		resp := callbackResponse{
			User: callbackUserResponse{
				ID:        userID,
				Username:  finalUsername,
				Email:     finalEmail,
				FullName:  finalFullName,
				Status:    finalStatus,
				Provider:  req.Provider,
				CreatedAt: finalCreatedAt,
				UpdatedAt: finalUpdatedAt,
			},
			APIKey: callbackKeyResponse{
				KeyID:     keyRec.KeyID,
				Secret:    plainSecret,
				Token:     token,
				UserID:    userID,
				CreatedAt: keyRec.CreatedAt,
				ExpiresAt: keyRec.ExpiresAt,
			},
		}

		return c.JSON(http.StatusOK, resp)
	}
}

// LogUnregisteredProvider logs a warning when an unregistered provider name
// is used in admin user creation. Exported for use by the users package.
func LogUnregisteredProvider(providerName string) {
	log.Printf("WARNING: provider %q is not registered in the provider registry", providerName)
}
