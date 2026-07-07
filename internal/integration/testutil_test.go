package integration_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agent-fox/af-hub/internal/auth"
	"github.com/agent-fox/af-hub/internal/config"
	"github.com/agent-fox/af-hub/internal/handler"
	"github.com/agent-fox/af-hub/internal/store"
	"github.com/labstack/echo/v4"
)

// testEnv holds the test infrastructure for integration tests.
type testEnv struct {
	Echo        *echo.Echo
	Store       store.Store
	Registry    *auth.Registry
	AuthHandler *handler.AuthHandler
}

// setupTestEnv creates a fully wired test environment with Echo server,
// in-memory store, provider registry, and registered routes.
func setupTestEnv(t *testing.T, oauthCfg []config.OAuthProviderConfig) *testEnv {
	t.Helper()

	cfg := &config.AuthConfig{
		OAuth:   oauthCfg,
		Timeout: 5,
	}

	registry := auth.NewRegistry(cfg)

	// Create a store (implementation will provide an in-memory SQLite store).
	s := createTestStore(t)

	authHandler := handler.NewAuthHandler(registry, s)

	e := echo.New()
	e.HTTPErrorHandler = handler.CustomHTTPErrorHandler

	// Register auth routes (public, no auth middleware).
	authGroup := e.Group("/api/v1/auth")
	authGroup.GET("/providers", authHandler.ListProviders)
	authGroup.POST("/callback", authHandler.OAuthCallback)

	// Register health probe.
	e.GET("/health", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	return &testEnv{
		Echo:        e,
		Store:       s,
		Registry:    registry,
		AuthHandler: authHandler,
	}
}

// createTestStore creates an in-memory test store.
// This will use an in-memory SQLite database once the store is implemented.
func createTestStore(t *testing.T) store.Store {
	t.Helper()
	// The store implementation from spec 01 will provide this.
	// For now, we rely on the stub NewStore or a mock implementation.
	return store.NewStore(nil)
}

// doRequest performs an HTTP request against the test Echo server
// and returns the response recorder.
func doRequest(e *echo.Echo, method, path string, body string, headers map[string]string) *httptest.ResponseRecorder {
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}

	req := httptest.NewRequest(method, path, bodyReader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// parseJSON parses the response body as JSON into the given target.
func parseJSON(t *testing.T, rec *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), target); err != nil {
		t.Fatalf("failed to parse JSON response: %v\nBody: %s", err, rec.Body.String())
	}
}

// errorResponse is the standard error envelope for assertions.
type errorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// providerListEntry represents a single entry in the provider list response.
type providerListEntry struct {
	Name         string `json:"name"`
	AuthorizeURL string `json:"authorize_url"`
}

// userResponse represents a user object in API responses.
type userResponse struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	Email      string `json:"email"`
	FullName   string `json:"full_name"`
	Provider   string `json:"provider"`
	ProviderID string `json:"provider_id"`
	Status     string `json:"status"`
}

// setupMockGitHubServer creates a mock HTTP server that simulates GitHub's
// OAuth token and userinfo endpoints.
func setupMockGitHubServer(t *testing.T, tokenResp, userInfoResp string, tokenStatus, userInfoStatus int) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(tokenStatus)
		_, _ = w.Write([]byte(tokenResp))
	})
	mux.HandleFunc("/api/user", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(userInfoStatus)
		_, _ = w.Write([]byte(userInfoResp))
	})

	return httptest.NewServer(mux)
}

// newTestServerFromMux creates a test HTTP server from a custom mux.
func newTestServerFromMux(mux *http.ServeMux) *httptest.Server {
	return httptest.NewServer(mux)
}
