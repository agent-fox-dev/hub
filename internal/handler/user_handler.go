// Package handler provides HTTP handlers for af-hub REST API endpoints.
package handler

import (
	"errors"
	"net/http"

	"github.com/agent-fox/af-hub/internal/auth"
	"github.com/agent-fox/af-hub/internal/store"
	"github.com/labstack/echo/v4"
)

// UserHandler handles user management HTTP endpoints.
type UserHandler struct {
	store store.Store
}

// NewUserHandler creates a new UserHandler.
func NewUserHandler(s store.Store) *UserHandler {
	return &UserHandler{store: s}
}

// createUserRequest represents the request body for POST /api/v1/users.
type createUserRequest struct {
	Username   string `json:"username"`
	Email      string `json:"email"`
	Provider   string `json:"provider"`
	ProviderID string `json:"provider_id"`
}

// updateUserRequest represents the request body for PUT /api/v1/users/:id.
type updateUserRequest struct {
	FullName *string `json:"full_name,omitempty"`
	Status   *string `json:"status,omitempty"`
}

// CreateUser handles POST /api/v1/users.
// Creates a new user with status=active. Requires admin auth.
func (h *UserHandler) CreateUser(c echo.Context) error {
	var req createUserRequest
	if err := c.Bind(&req); err != nil {
		return NewErrorResponse(c, http.StatusBadRequest, "missing required fields")
	}

	// Validate required fields.
	if req.Username == "" || req.Email == "" || req.Provider == "" || req.ProviderID == "" {
		return NewErrorResponse(c, http.StatusBadRequest, "missing required fields")
	}

	user := &store.User{
		Username:   req.Username,
		Email:      req.Email,
		Provider:   req.Provider,
		ProviderID: req.ProviderID,
		Status:     "active",
	}

	created, err := h.store.CreateUser(user)
	if err != nil {
		if errors.Is(err, store.ErrConstraintViolation) {
			return NewErrorResponse(c, http.StatusConflict, "duplicate username or provider identity")
		}
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	return c.JSON(http.StatusCreated, created)
}

// ListUsers handles GET /api/v1/users.
// Returns all user records. Requires admin auth.
func (h *UserHandler) ListUsers(c echo.Context) error {
	users, err := h.store.ListUsers()
	if err != nil {
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	// Return an empty array instead of null when no users exist.
	if users == nil {
		users = []*store.User{}
	}

	return c.JSON(http.StatusOK, users)
}

// GetUser handles GET /api/v1/users/:id.
// Returns a user with workspace memberships. Requires admin auth.
func (h *UserHandler) GetUser(c echo.Context) error {
	id := c.Param("id")

	user, err := h.store.GetUserByID(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return NewErrorResponse(c, http.StatusNotFound, "user not found")
		}
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	// Fetch workspace memberships for this user.
	memberships, err := h.listMembershipsForUser(user.ID)
	if err != nil {
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	result := &store.UserWithMemberships{
		User:        *user,
		Memberships: memberships,
	}

	return c.JSON(http.StatusOK, result)
}

// listMembershipsForUser retrieves all workspace memberships for a given user.
// The store interface provides ListWorkspaceMembers (by workspace ID), but we
// need memberships by user ID. We query all workspaces to find user memberships.
// This is a pragmatic approach given the current store interface.
func (h *UserHandler) listMembershipsForUser(userID string) ([]*store.WorkspaceMember, error) {
	// Get all workspaces (including archived) to check memberships.
	workspaces, err := h.store.ListWorkspaces(true)
	if err != nil {
		return nil, err
	}

	var memberships []*store.WorkspaceMember
	for _, ws := range workspaces {
		member, err := h.store.GetWorkspaceMember(userID, ws.ID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return nil, err
		}
		memberships = append(memberships, member)
	}

	// Return empty slice instead of nil.
	if memberships == nil {
		memberships = []*store.WorkspaceMember{}
	}

	return memberships, nil
}

// UpdateUser handles PUT /api/v1/users/:id.
// Updates full_name and/or status. Admin can change both; non-admin
// can only change own full_name (enforced by RBAC middleware).
func (h *UserHandler) UpdateUser(c echo.Context) error {
	id := c.Param("id")

	// Look up the existing user first.
	user, err := h.store.GetUserByID(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return NewErrorResponse(c, http.StatusNotFound, "user not found")
		}
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	var req updateUserRequest
	if err := c.Bind(&req); err != nil {
		return NewErrorResponse(c, http.StatusBadRequest, "invalid request body")
	}

	// Apply updates only for fields that are present in the request.
	if req.FullName != nil {
		user.FullName = *req.FullName
	}

	// Status can be changed only by admins. The RBAC middleware
	// (RequireAdminOrSelf) already prevents non-admins from including
	// the status field, so we just apply it here if present.
	if req.Status != nil {
		role := c.Get(auth.ContextKeyRole)
		if role == auth.RoleAdmin {
			user.Status = *req.Status
		}
		// Non-admin reaching here means RBAC let it through, but as a
		// safety net, only admins change status.
	}

	updated, err := h.store.UpdateUser(user)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return NewErrorResponse(c, http.StatusNotFound, "user not found")
		}
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	return c.JSON(http.StatusOK, updated)
}
