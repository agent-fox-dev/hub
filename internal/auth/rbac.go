// Package auth — RBAC enforcement layer for af-hub.
package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/labstack/echo/v4"
)

// Role constants define the three authorization levels.
const (
	// RoleAdmin has global full access across all workspaces and endpoints.
	RoleAdmin = "admin"

	// RoleEditor has read/write access within their assigned workspace.
	RoleEditor = "editor"

	// RoleReader has read-only access within their assigned workspace.
	RoleReader = "reader"
)

// validRoles is the set of known role values.
var validRoles = map[string]bool{
	RoleAdmin:  true,
	RoleEditor: true,
	RoleReader: true,
}

// RequireRole returns middleware that enforces one of the given roles.
// If the authenticated user's role (from request context) is not in the
// allowed set, it returns HTTP 403 with "insufficient permissions".
// Unknown role values also result in HTTP 403 with "unknown role".
// Admin role always has access regardless of the allowed list.
func RequireRole(roles ...string) echo.MiddlewareFunc {
	// Build a set for O(1) lookup.
	allowed := make(map[string]bool, len(roles))
	for _, r := range roles {
		allowed[r] = true
	}

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			role := getRoleFromContext(c)

			// Empty or nil role → unknown.
			if role == "" {
				return rbacError(c, http.StatusForbidden, "unknown role")
			}

			// Admin always has access.
			if role == RoleAdmin {
				return next(c)
			}

			// Unknown role.
			if !validRoles[role] {
				return rbacError(c, http.StatusForbidden, "unknown role")
			}

			// Check if the role is in the allowed set.
			if allowed[role] {
				return next(c)
			}

			return rbacError(c, http.StatusForbidden, "insufficient permissions")
		}
	}
}

// RequireAdminOrSelf returns middleware for endpoints where:
//   - Admins can perform any operation.
//   - Non-admin users can only modify their own records (matched by :id param).
//   - Non-admin users are limited to updating full_name; attempts to change
//     status are rejected with HTTP 403.
func RequireAdminOrSelf() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			role := getRoleFromContext(c)
			userID := getUserIDFromContext(c)

			// Admin can do anything.
			if role == RoleAdmin {
				return next(c)
			}

			// Non-admin: the :id param must match the authenticated user's ID.
			paramID := c.Param("id")
			if paramID != userID {
				return rbacError(c, http.StatusForbidden, "insufficient permissions")
			}

			// Non-admin self-update: read the body to check for status changes.
			bodyBytes, err := io.ReadAll(c.Request().Body)
			if err != nil {
				return rbacError(c, http.StatusForbidden, "insufficient permissions")
			}
			// Restore the body so the handler can read it.
			c.Request().Body = io.NopCloser(bytes.NewReader(bodyBytes))

			// Parse the body to check for "status" field.
			if len(bodyBytes) > 0 {
				var body map[string]json.RawMessage
				if err := json.Unmarshal(bodyBytes, &body); err == nil {
					if _, hasStatus := body["status"]; hasStatus {
						return rbacError(c, http.StatusForbidden, "insufficient permissions")
					}
				}
			}

			return next(c)
		}
	}
}

// getRoleFromContext safely extracts the role from the Echo context.
// Returns an empty string if the role is not set or not a string.
func getRoleFromContext(c echo.Context) string {
	v := c.Get(ContextKeyRole)
	if v == nil {
		return ""
	}
	role, ok := v.(string)
	if !ok {
		return ""
	}
	return role
}

// getUserIDFromContext safely extracts the user ID from the Echo context.
func getUserIDFromContext(c echo.Context) string {
	v := c.Get(ContextKeyUserID)
	if v == nil {
		return ""
	}
	userID, ok := v.(string)
	if !ok {
		return ""
	}
	return userID
}

// rbacError creates a standard error response for RBAC failures.
func rbacError(c echo.Context, status int, message string) error {
	return c.JSON(status, map[string]any{
		"error": map[string]any{
			"code":    fmt.Sprintf("%d", status),
			"message": message,
		},
	})
}
