// Package handler provides HTTP handlers for af-hub REST API endpoints.
package handler

import (
	"github.com/agent-fox/af-hub/internal/store"
	"github.com/labstack/echo/v4"
)

// UserHandler handles user management HTTP endpoints.
type UserHandler struct {
	store store.Store
}

// NewUserHandler creates a new UserHandler.
func NewUserHandler(s store.Store) *UserHandler {
	panic("not implemented")
}

// CreateUser handles POST /api/v1/users.
// Creates a new user with status=active. Requires admin auth.
func (h *UserHandler) CreateUser(c echo.Context) error {
	panic("not implemented")
}

// ListUsers handles GET /api/v1/users.
// Returns all user records. Requires admin auth.
func (h *UserHandler) ListUsers(c echo.Context) error {
	panic("not implemented")
}

// GetUser handles GET /api/v1/users/:id.
// Returns a user with workspace memberships. Requires admin auth.
func (h *UserHandler) GetUser(c echo.Context) error {
	panic("not implemented")
}

// UpdateUser handles PUT /api/v1/users/:id.
// Updates full_name and/or status. Admin can change both; non-admin
// can only change own full_name (enforced by RBAC middleware).
func (h *UserHandler) UpdateUser(c echo.Context) error {
	panic("not implemented")
}
