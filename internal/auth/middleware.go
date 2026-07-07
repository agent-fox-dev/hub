// Package auth — authentication middleware for af-hub.
package auth

import (
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
	panic("not implemented")
}
