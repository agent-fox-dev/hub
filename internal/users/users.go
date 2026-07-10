// Package users implements user management endpoints for af-hub, providing
// user CRUD operations with admin and self-service access patterns.
//
// Endpoints:
//   - POST   /api/v1/users          (admin only)
//   - GET    /api/v1/users          (admin only)
//   - GET    /api/v1/users/:id      (admin only)
//   - PUT    /api/v1/users/:id      (mixed auth)
package users

import (
	"database/sql"

	"github.com/labstack/echo/v4"
)

// CredentialType identifies the kind of authentication credential used.
type CredentialType string

const (
	// CredentialTypeAdmin is an admin token credential.
	CredentialTypeAdmin CredentialType = "admin"
	// CredentialTypeAPIKey is a user API key credential.
	CredentialTypeAPIKey CredentialType = "api_key"
	// CredentialTypeWorkspaceToken is a workspace token credential.
	CredentialTypeWorkspaceToken CredentialType = "workspace_token"
)

// ContextKey is a typed key for Echo context values.
type ContextKey string

// AuthContextKey is the Echo context key for AuthContext.
const AuthContextKey ContextKey = "auth_context"

// AuthContext holds the authentication state set by auth middleware.
type AuthContext struct {
	CredentialType CredentialType
	UserID         string
	WorkspaceID    string
	IsAdmin        bool
}

// User represents a user record in the users table.
type User struct {
	ID              string            `json:"id"`
	Username        string            `json:"username"`
	Email           string            `json:"email"`
	FullName        string            `json:"full_name"`
	Status          string            `json:"status"`
	Provider        string            `json:"provider"`
	ProviderID      string            `json:"provider_id,omitempty"`
	CreatedAt       string            `json:"created_at"`
	UpdatedAt       string            `json:"updated_at"`
	TeamMemberships []TeamMembership  `json:"team_memberships,omitempty"`
}

// TeamMembership represents a user's membership in a team.
type TeamMembership struct {
	TeamID   string `json:"team_id"`
	TeamName string `json:"team_name"`
	Role     string `json:"role"`
}

// CreateUserRequest is the request body for POST /api/v1/users.
type CreateUserRequest struct {
	Username   string `json:"username"`
	Email      string `json:"email"`
	FullName   string `json:"full_name"`
	Provider   string `json:"provider"`
	ProviderID string `json:"provider_id"`
}

// UpdateUserRequest is the request body for PUT /api/v1/users/:id.
type UpdateUserRequest struct {
	FullName *string `json:"full_name,omitempty"`
	Status   *string `json:"status,omitempty"`
}

// ProviderRegistry is a minimal interface for checking provider registration.
type ProviderRegistry interface {
	IsRegistered(name string) bool
}

// CreateUserHandler returns an Echo handler for POST /api/v1/users.
func CreateUserHandler(_ *sql.DB, _ ProviderRegistry) echo.HandlerFunc {
	return func(c echo.Context) error {
		return echo.NewHTTPError(501, "not implemented")
	}
}

// ListUsersHandler returns an Echo handler for GET /api/v1/users.
func ListUsersHandler(_ *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return echo.NewHTTPError(501, "not implemented")
	}
}

// GetUserHandler returns an Echo handler for GET /api/v1/users/:id.
func GetUserHandler(_ *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return echo.NewHTTPError(501, "not implemented")
	}
}

// UpdateUserHandler returns an Echo handler for PUT /api/v1/users/:id.
func UpdateUserHandler(_ *sql.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		return echo.NewHTTPError(501, "not implemented")
	}
}

// ValidateUsername checks that a username contains only alphanumeric
// characters and hyphens, and is at most 39 characters long.
func ValidateUsername(username string) error {
	return nil // stub: validation not yet implemented
}
