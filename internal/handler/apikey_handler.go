// Package handler provides HTTP handlers for af-hub REST API endpoints.
package handler

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/agent-fox/af-hub/internal/auth"
	"github.com/agent-fox/af-hub/internal/store"
	"github.com/labstack/echo/v4"
)

// APIKeyHandler handles API key management endpoints.
type APIKeyHandler struct {
	store store.Store
}

// NewAPIKeyHandler creates a new APIKeyHandler with the given store.
func NewAPIKeyHandler(s store.Store) *APIKeyHandler {
	return &APIKeyHandler{store: s}
}

// createAPIKeyRequest represents the request body for POST /api/v1/keys.
type createAPIKeyRequest struct {
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	Expires     *int   `json:"expires"`
}

// apiKeyCreateResp is returned on key creation or refresh.
// The plaintext Key is included exactly once; it is never stored.
type apiKeyCreateResp struct {
	Key       string  `json:"key"`
	KeyID     string  `json:"key_id"`
	Role      string  `json:"role,omitempty"`
	ExpiresAt *string `json:"expires_at,omitempty"`
}

// validExpires holds the set of valid expires values (in days).
var validExpires = map[int]bool{0: true, 30: true, 60: true, 90: true}

// generateSecret creates a cryptographically random hex secret (32 bytes → 64 hex chars).
func generateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// sha256HexHash computes the hex-encoded SHA-256 hash of the input string.
func sha256HexHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// CreateAPIKey handles POST /api/v1/keys.
// Creates a new API key scoped to a workspace. The user must be a member of
// the workspace. The plaintext key is returned exactly once in the response.
func (h *APIKeyHandler) CreateAPIKey(c echo.Context) error {
	var req createAPIKeyRequest
	if err := c.Bind(&req); err != nil {
		return NewErrorResponse(c, http.StatusBadRequest, "invalid request body")
	}

	// Validate required fields.
	if req.WorkspaceID == "" || req.Label == "" || req.Expires == nil {
		return NewErrorResponse(c, http.StatusBadRequest, "missing required fields")
	}

	// Validate expires value.
	if !validExpires[*req.Expires] {
		return NewErrorResponse(c, http.StatusBadRequest, "expires must be 0, 30, 60, or 90")
	}

	// Verify workspace exists.
	_, err := h.store.GetWorkspaceByID(req.WorkspaceID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return NewErrorResponse(c, http.StatusNotFound, "workspace not found")
		}
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	// Get the authenticated user's ID.
	userID, _ := c.Get(auth.ContextKeyUserID).(string)

	// Verify the user is a member of the workspace.
	member, err := h.store.GetWorkspaceMember(userID, req.WorkspaceID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return NewErrorResponse(c, http.StatusForbidden, "not a member of this workspace")
		}
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	// Generate key_id (random hex, no hyphens) and secret.
	keyIDBytes := make([]byte, 16)
	if _, err := rand.Read(keyIDBytes); err != nil {
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}
	keyID := hex.EncodeToString(keyIDBytes)
	secret, err := generateSecret()
	if err != nil {
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	// Compute the hash of the secret for storage.
	keyHash := sha256HexHash(secret)

	// Compute expires_at from the expires value (in days).
	var expiresAt *time.Time
	if *req.Expires > 0 {
		t := time.Now().UTC().Add(time.Duration(*req.Expires) * 24 * time.Hour)
		expiresAt = &t
	}

	// The key inherits the user's role in the workspace.
	role := member.Role

	apiKey := &store.APIKey{
		KeyID:       keyID,
		KeyHash:     keyHash,
		UserID:      userID,
		WorkspaceID: req.WorkspaceID,
		Role:        role,
		Label:       req.Label,
		ExpiresAt:   expiresAt,
	}

	created, err := h.store.CreateAPIKey(apiKey)
	if err != nil {
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	// Build the plaintext token: af_<key_id>_<secret>.
	plaintextKey := fmt.Sprintf("af_%s_%s", created.KeyID, secret)

	// Format expires_at for the response.
	var expiresAtStr *string
	if created.ExpiresAt != nil {
		s := created.ExpiresAt.UTC().Format(time.RFC3339)
		expiresAtStr = &s
	}

	return c.JSON(http.StatusCreated, apiKeyCreateResp{
		Key:       plaintextKey,
		KeyID:     created.KeyID,
		Role:      created.Role,
		ExpiresAt: expiresAtStr,
	})
}

// ListAPIKeys handles GET /api/v1/keys.
// Admin token: returns all keys across all users (including expired).
// API key token: returns only the authenticated user's own keys.
// Plaintext secrets are never included in the response.
func (h *APIKeyHandler) ListAPIKeys(c echo.Context) error {
	authMethod, _ := c.Get(auth.ContextKeyAuthMethod).(string)
	userID, _ := c.Get(auth.ContextKeyUserID).(string)

	var keys []*store.APIKey
	var err error

	if authMethod == auth.AuthMethodAdmin {
		keys, err = h.store.ListAPIKeys()
	} else {
		keys, err = h.store.ListAPIKeysByUserID(userID)
	}

	if err != nil {
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	// Return empty array instead of null.
	if keys == nil {
		keys = []*store.APIKey{}
	}

	return c.JSON(http.StatusOK, keys)
}

// RefreshAPIKey handles POST /api/v1/keys/:key_id/refresh.
// Generates a new secret for an existing key, updates the stored hash,
// and returns the new plaintext key exactly once.
func (h *APIKeyHandler) RefreshAPIKey(c echo.Context) error {
	keyIDParam := c.Param("key_id")
	userID, _ := c.Get(auth.ContextKeyUserID).(string)
	authMethod, _ := c.Get(auth.ContextKeyAuthMethod).(string)

	// Look up the key record.
	apiKey, err := h.store.GetAPIKeyByKeyID(keyIDParam)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return NewErrorResponse(c, http.StatusNotFound, "API key not found")
		}
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	// Ownership check: non-admin users can only refresh their own keys.
	if authMethod != auth.AuthMethodAdmin && apiKey.UserID != userID {
		return NewErrorResponse(c, http.StatusNotFound, "API key not found")
	}

	// Generate a new secret.
	newSecret, err := generateSecret()
	if err != nil {
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	// Update the stored hash.
	newHash := sha256HexHash(newSecret)
	if err := h.store.UpdateAPIKeyHash(keyIDParam, newHash); err != nil {
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	// Build the new plaintext token.
	plaintextKey := fmt.Sprintf("af_%s_%s", keyIDParam, newSecret)

	return c.JSON(http.StatusOK, apiKeyCreateResp{
		Key:   plaintextKey,
		KeyID: keyIDParam,
	})
}

// RevokeAPIKey handles DELETE /api/v1/keys/:key_id.
// Sets revoked_at on the key, permanently revoking it.
func (h *APIKeyHandler) RevokeAPIKey(c echo.Context) error {
	keyIDParam := c.Param("key_id")
	userID, _ := c.Get(auth.ContextKeyUserID).(string)
	authMethod, _ := c.Get(auth.ContextKeyAuthMethod).(string)

	// Look up the key record to verify ownership.
	apiKey, err := h.store.GetAPIKeyByKeyID(keyIDParam)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return NewErrorResponse(c, http.StatusNotFound, "API key not found")
		}
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	// Ownership check: non-admin users can only revoke their own keys.
	if authMethod != auth.AuthMethodAdmin && apiKey.UserID != userID {
		return NewErrorResponse(c, http.StatusNotFound, "API key not found")
	}

	// Revoke the key (sets revoked_at to now).
	if err := h.store.RevokeAPIKey(keyIDParam); err != nil {
		return NewErrorResponse(c, http.StatusInternalServerError, "internal server error")
	}

	return c.JSON(http.StatusOK, map[string]string{
		"message": "key revoked",
	})
}
