// Package handler provides HTTP handlers for af-hub REST API endpoints.
package handler

import (
	"errors"
	"net/http"

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
	return &AuthHandler{
		registry: registry,
		store:    s,
	}
}

// providerEntry represents a single provider in the list response.
// Only name and authorize_url are exposed; no secrets or credentials.
type providerEntry struct {
	Name         string `json:"name"`
	AuthorizeURL string `json:"authorize_url"`
}

// ListProviders handles GET /api/v1/auth/providers.
// Returns an array of configured providers with name and authorize_url.
// No secrets or credentials are exposed.
func (h *AuthHandler) ListProviders(c echo.Context) error {
	names := h.registry.ListProviders()
	providers := make([]providerEntry, 0, len(names))

	for _, name := range names {
		p, err := h.registry.GetProvider(name)
		if err != nil {
			continue
		}
		providers = append(providers, providerEntry{
			Name:         name,
			AuthorizeURL: p.AuthorizeURL(""),
		})
	}

	return c.JSON(http.StatusOK, providers)
}

// OAuthCallbackRequest represents the request body for the OAuth callback.
type OAuthCallbackRequest struct {
	Provider    string `json:"provider"`
	Code        string `json:"code"`
	RedirectURI string `json:"redirect_uri"`
}

// OAuthCallback handles POST /api/v1/auth/callback.
// Exchanges the authorization code, retrieves user info, and upserts the user.
func (h *AuthHandler) OAuthCallback(c echo.Context) error {
	var req OAuthCallbackRequest
	if err := c.Bind(&req); err != nil {
		return NewErrorResponse(c, http.StatusBadRequest, "missing required fields")
	}

	// Validate required fields.
	if req.Provider == "" || req.Code == "" || req.RedirectURI == "" {
		return NewErrorResponse(c, http.StatusBadRequest, "missing required fields")
	}

	// Look up the provider in the registry.
	provider, err := h.registry.GetProvider(req.Provider)
	if err != nil {
		if errors.Is(err, auth.ErrUnsupportedProvider) {
			return NewErrorResponse(c, http.StatusBadRequest, "unsupported provider")
		}
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	// Exchange the authorization code for tokens.
	tokenResp, err := provider.ExchangeCode(c.Request().Context(), req.Code, req.RedirectURI)
	if err != nil {
		if errors.Is(err, auth.ErrProviderTimeout) {
			return NewErrorResponse(c, http.StatusInternalServerError, "identity provider timeout")
		}
		if errors.Is(err, auth.ErrCodeExchangeFailed) {
			return NewErrorResponse(c, http.StatusBadRequest, "authorization code exchange failed")
		}
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	// Retrieve user info from the provider.
	userInfo, err := provider.GetUserInfo(c.Request().Context(), tokenResp.AccessToken)
	if err != nil {
		if errors.Is(err, auth.ErrProviderTimeout) {
			return NewErrorResponse(c, http.StatusInternalServerError, "identity provider timeout")
		}
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	// Upsert the user by (provider, provider_id).
	user, err := h.upsertUser(req.Provider, userInfo)
	if err != nil {
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	return c.JSON(http.StatusOK, user)
}

// upsertUser creates a new user if not found, or updates an existing user's
// username and email. It never changes a blocked user's status to active.
func (h *AuthHandler) upsertUser(providerName string, info *auth.UserInfo) (*store.User, error) {
	existing, err := h.store.GetUserByProviderID(providerName, info.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	if existing != nil {
		// Update mutable fields but never change status from blocked to active.
		existing.Username = info.Username
		existing.Email = info.Email
		if info.Name != "" {
			existing.FullName = info.Name
		}
		// Status is intentionally NOT changed here — a blocked user stays blocked.
		return h.store.UpdateUser(existing)
	}

	// Create a new user with status active.
	newUser := &store.User{
		Username:   info.Username,
		Email:      info.Email,
		FullName:   info.Name,
		Provider:   providerName,
		ProviderID: info.ID,
		Status:     "active",
	}
	return h.store.CreateUser(newUser)
}
