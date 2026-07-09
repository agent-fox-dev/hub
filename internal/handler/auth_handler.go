// Package handler provides HTTP handlers for af-hub REST API endpoints.
package handler

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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

	// Generate a login API key for the user.
	loginKey, err := h.createLoginKey(user.ID)
	if err != nil {
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	// Return the user object and api_key in the response per 05-REQ-10.1.
	return c.JSON(http.StatusOK, oauthCallbackResponse{
		User:   user,
		APIKey: loginKey,
	})
}

// oauthCallbackResponse is the response structure for POST /api/v1/auth/callback.
// It wraps the user object and the generated login API key per 05-REQ-10.1.
type oauthCallbackResponse struct {
	User   *store.User         `json:"user"`
	APIKey *loginAPIKeyResponse `json:"api_key"`
}

// loginAPIKeyResponse represents the api_key portion of the OAuth callback response.
type loginAPIKeyResponse struct {
	Key   string `json:"key"`
	KeyID string `json:"key_id"`
}

// createLoginKey generates a new API key for the user after a successful login.
// The key is stored in the database and the plaintext token is returned exactly once.
// Login keys have no workspace scope, no expiry, and are labeled "login".
func (h *AuthHandler) createLoginKey(userID string) (*loginAPIKeyResponse, error) {
	// Generate key_id (random hex).
	keyIDBytes := make([]byte, 16)
	if _, err := rand.Read(keyIDBytes); err != nil {
		return nil, fmt.Errorf("generate key_id: %w", err)
	}
	keyID := hex.EncodeToString(keyIDBytes)

	// Generate secret.
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return nil, fmt.Errorf("generate secret: %w", err)
	}
	secret := hex.EncodeToString(secretBytes)

	// Hash the secret for storage.
	h256 := sha256.Sum256([]byte(secret))
	keyHash := hex.EncodeToString(h256[:])

	// Build the plaintext token: af_<key_id>_<secret>.
	plaintextKey := fmt.Sprintf("af_%s_%s", keyID, secret)

	apiKey := &store.APIKey{
		KeyID:   keyID,
		KeyHash: keyHash,
		UserID:  userID,
		Role:    "member",
		Label:   "login",
	}

	if _, err := h.store.CreateAPIKey(apiKey); err != nil {
		return nil, fmt.Errorf("store login key: %w", err)
	}

	return &loginAPIKeyResponse{
		Key:   plaintextKey,
		KeyID: keyID,
	}, nil
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
