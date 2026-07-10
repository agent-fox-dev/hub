// Package middleware also provides authentication middleware for af-hub.
//
// The canonical auth context types (AuthContext, ContextKey, CredentialType)
// live in internal/authctx to avoid import cycles. This file provides the
// middleware function that validates Bearer tokens and sets AuthContext.
//
// Implementation will be added in task group 12.
package middleware

import (
	"database/sql"

	"github.com/labstack/echo/v4"
)

// AuthMiddleware returns Echo middleware that authenticates requests by
// extracting the Bearer token from the Authorization header and validating
// it against admin_tokens, api_keys, or workspace_tokens tables.
//
// Returns HTTP 401 with {"error": {"code": 401, "message": "missing or
// invalid authentication credentials"}} for any authentication failure.
//
// Token format recognition:
//   - af_admin_<64 hex chars>:           admin token
//   - af_wt_<token_id>_<secret>:         workspace token
//   - af_<key_id>_<secret>:              user API key
//
// All structural validation is performed before any DB query (REQ-14.6).
// Hash comparisons use crypto/subtle.ConstantTimeCompare (REQ-14.7).
// Admin token auth bypasses the blocked-user check (REQ-14.8).
//
// Stub: returns a pass-through middleware. Implementation in task group 12.
func AuthMiddleware(db *sql.DB) echo.MiddlewareFunc {
	// Stub: pass-through. Implementation in task group 12.
	_ = db
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			return next(c)
		}
	}
}
