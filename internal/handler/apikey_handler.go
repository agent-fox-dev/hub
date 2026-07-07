package handler

import (
	"github.com/agent-fox/af-hub/internal/store"
	"github.com/labstack/echo/v4"
)

// APIKeyHandler handles API key management endpoints.
type APIKeyHandler struct {
	store store.Store
}

// NewAPIKeyHandler creates a new APIKeyHandler with the given store.
func NewAPIKeyHandler(s store.Store) *APIKeyHandler {
	panic("not implemented")
}

// CreateAPIKey handles POST /api/v1/keys.
func (h *APIKeyHandler) CreateAPIKey(c echo.Context) error {
	panic("not implemented")
}

// ListAPIKeys handles GET /api/v1/keys.
func (h *APIKeyHandler) ListAPIKeys(c echo.Context) error {
	panic("not implemented")
}

// RefreshAPIKey handles POST /api/v1/keys/:key_id/refresh.
func (h *APIKeyHandler) RefreshAPIKey(c echo.Context) error {
	panic("not implemented")
}

// RevokeAPIKey handles DELETE /api/v1/keys/:key_id.
func (h *APIKeyHandler) RevokeAPIKey(c echo.Context) error {
	panic("not implemented")
}
