// Package auth — RBAC enforcement layer for af-hub.
package auth

import "github.com/labstack/echo/v4"

// Role constants define the three authorization levels.
const (
	// RoleAdmin has global full access across all workspaces and endpoints.
	RoleAdmin = "admin"

	// RoleEditor has read/write access within their assigned workspace.
	RoleEditor = "editor"

	// RoleReader has read-only access within their assigned workspace.
	RoleReader = "reader"
)

// RequireRole returns middleware that enforces one of the given roles.
// If the authenticated user's role (from request context) is not in the
// allowed set, it returns HTTP 403 with "insufficient permissions".
// Unknown role values also result in HTTP 403 with "unknown role".
// Admin role always has access regardless of the allowed list.
func RequireRole(roles ...string) echo.MiddlewareFunc {
	panic("not implemented")
}

// RequireAdminOrSelf returns middleware for endpoints where:
//   - Admins can perform any operation.
//   - Non-admin users can only modify their own records (matched by :id param).
//   - Non-admin users are limited to updating full_name; attempts to change
//     status are rejected with HTTP 403.
func RequireAdminOrSelf() echo.MiddlewareFunc {
	panic("not implemented")
}
