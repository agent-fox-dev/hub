package auth

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// ProvidersResponse is the response body for GET /api/v1/auth/providers.
type ProvidersResponse struct {
	Providers []ProviderEntry `json:"providers"`
}

// GetProvidersHandler returns an Echo handler for GET /api/v1/auth/providers.
// This is a public endpoint (no authentication required).
//
// It returns the list of registered OAuth providers with their name,
// authorize_url, and scopes. No client secrets, token URLs, or userinfo
// URLs are included in the response.
//
// Returns HTTP 200 with {"providers": [...]}. If no providers are registered,
// returns HTTP 200 with {"providers": []}.
func GetProvidersHandler(registry *Registry) echo.HandlerFunc {
	return func(c echo.Context) error {
		entries := registry.List()

		// Ensure we always return an empty array, not null.
		if entries == nil {
			entries = []ProviderEntry{}
		}

		return c.JSON(http.StatusOK, ProvidersResponse{
			Providers: entries,
		})
	}
}
