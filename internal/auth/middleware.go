package auth

import (
	"database/sql"

	"github.com/labstack/echo/v4"
)

// Middleware returns Echo middleware that authenticates requests by validating
// Bearer tokens from the Authorization header. It identifies credential type
// by prefix (af_admin_, af_wt_, af_), verifies the hashed secret, resolves
// the associated user and workspace, checks expiry/revocation, and rejects
// blocked users.
//
// Stub: not yet implemented. The real implementation will:
//   - Extract and validate Bearer token from Authorization header
//   - Dispatch by prefix to admin/api_key/workspace_token verification
//   - Check expiry, revocation, and blocked-user status
//   - Attach AuthContext to Echo's context
func Middleware(db *sql.DB) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// Stub: passes all requests through without authentication.
			return next(c)
		}
	}
}
