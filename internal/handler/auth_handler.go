// Package handler provides HTTP handlers for af-hub REST API endpoints.
package handler

import (
	"github.com/agent-fox/af-hub/internal/auth"
	"github.com/agent-fox/af-hub/internal/store"
	"github.com/labstack/echo/v4"
)

// AuthHandler handles authentication-related HTTP endpoints.
type AuthHandler struct {
	registry *auth.Registry
	store    store.Store
}

// NewAuthHandler creates a new AuthHandler.
func NewAuthHandler(registry *auth.Registry, s store.Store) *AuthHandler {
	panic("not implemented")
}

// ListProviders handles GET /api/v1/auth/providers.
// Returns an array of configured providers with name and authorize_url.
// No secrets or credentials are exposed.
func (h *AuthHandler) ListProviders(c echo.Context) error {
	panic("not implemented")
}

// OAuthCallback handles POST /api/v1/auth/callback.
// Exchanges the authorization code, retrieves user info, and upserts the user.
func (h *AuthHandler) OAuthCallback(c echo.Context) error {
	panic("not implemented")
}

// OAuthCallbackRequest represents the request body for the OAuth callback.
type OAuthCallbackRequest struct {
	Provider    string `json:"provider"`
	Code        string `json:"code"`
	RedirectURI string `json:"redirect_uri"`
}
