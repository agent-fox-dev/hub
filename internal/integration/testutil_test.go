package integration_test

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox/af-hub/internal/auth"
	"github.com/agent-fox/af-hub/internal/config"
	"github.com/agent-fox/af-hub/internal/handler"
	"github.com/agent-fox/af-hub/internal/store"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	_ "modernc.org/sqlite"
)

// testEnv holds the test infrastructure for integration tests.
type testEnv struct {
	Echo             *echo.Echo
	Store            store.Store
	Registry         *auth.Registry
	AuthHandler      *handler.AuthHandler
	UserHandler      *handler.UserHandler
	WorkspaceHandler *handler.WorkspaceHandler
	APIKeyHandler    *handler.APIKeyHandler
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
	e.Use(middleware.BodyLimit("1M"))

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

// createTestStore creates a test store backed by a temporary SQLite database
// with all tables initialized.
func createTestStore(t *testing.T) store.Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("createTestStore: open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("createTestStore: enable WAL: %v", err)
	}

	schema := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			username TEXT UNIQUE NOT NULL,
			email TEXT,
			full_name TEXT,
			provider TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			status TEXT DEFAULT 'active',
			created_at TEXT,
			updated_at TEXT,
			UNIQUE(provider, provider_id)
		)`,
		`CREATE TABLE IF NOT EXISTS workspaces (
			id TEXT PRIMARY KEY,
			name TEXT UNIQUE NOT NULL,
			slug TEXT UNIQUE NOT NULL,
			url TEXT UNIQUE NOT NULL,
			status TEXT DEFAULT 'active',
			created_at TEXT,
			created_by TEXT REFERENCES users(id)
		)`,
		`CREATE TABLE IF NOT EXISTS workspace_members (
			user_id TEXT REFERENCES users(id),
			workspace_id TEXT REFERENCES workspaces(id),
			role TEXT NOT NULL,
			created_at TEXT,
			granted_by TEXT REFERENCES users(id),
			PRIMARY KEY (user_id, workspace_id)
		)`,
		`CREATE TABLE IF NOT EXISTS api_keys (
			id TEXT PRIMARY KEY,
			key_id TEXT UNIQUE,
			key_hash TEXT,
			user_id TEXT REFERENCES users(id),
			workspace_id TEXT REFERENCES workspaces(id),
			role TEXT,
			label TEXT,
			expires_at TEXT,
			revoked_at TEXT,
			created_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS admin_tokens (
			id TEXT PRIMARY KEY,
			token_hash TEXT,
			created_at TEXT
		)`,
	}
	for _, stmt := range schema {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("createTestStore: schema: %v", err)
		}
	}

	return store.NewStore(db)
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

// oauthCallbackTestResponse represents the full response from POST /api/v1/auth/callback.
// It wraps the user object and the login api_key per 05-REQ-10.1.
type oauthCallbackTestResponse struct {
	User   userResponse              `json:"user"`
	APIKey *oauthCallbackAPIKeyEntry `json:"api_key"`
}

// oauthCallbackAPIKeyEntry represents the api_key portion of the callback response.
type oauthCallbackAPIKeyEntry struct {
	Key   string `json:"key"`
	KeyID string `json:"key_id"`
}

// apiKeyResponse represents an API key object in list responses (no plaintext).
type apiKeyResponse struct {
	ID          string  `json:"id"`
	KeyID       string  `json:"key_id"`
	UserID      string  `json:"user_id"`
	WorkspaceID string  `json:"workspace_id"`
	Role        string  `json:"role"`
	Label       string  `json:"label"`
	ExpiresAt   *string `json:"expires_at,omitempty"`
	RevokedAt   *string `json:"revoked_at,omitempty"`
	Key         string  `json:"key,omitempty"`
}

// apiKeyCreateResponse represents the response from creating or refreshing a key.
type apiKeyCreateResponse struct {
	Key       string  `json:"key"`
	KeyID     string  `json:"key_id"`
	Role      string  `json:"role"`
	ExpiresAt *string `json:"expires_at,omitempty"`
}

// setupFullTestEnv creates a fully wired test environment with auth middleware,
// RBAC enforcement, and all handler routes (auth, user, workspace, API keys).
// This is used by user, workspace, and API key handler integration tests.
func setupFullTestEnv(t *testing.T) *testEnv {
	t.Helper()

	cfg := &config.AuthConfig{
		OAuth: []config.OAuthProviderConfig{
			{
				Provider:     "github",
				ClientID:     "test_client_id",
				ClientSecret: "test_client_secret",
			},
		},
		Timeout: 5,
	}

	registry := auth.NewRegistry(cfg)
	s := createTestStore(t)
	authHandler := handler.NewAuthHandler(registry, s)
	userHandler := handler.NewUserHandler(s)
	workspaceHandler := handler.NewWorkspaceHandler(s)
	apiKeyHandler := handler.NewAPIKeyHandler(s)

	e := echo.New()
	e.HTTPErrorHandler = handler.CustomHTTPErrorHandler
	e.Use(middleware.BodyLimit("1M"))

	// Public auth routes (no middleware).
	authGroup := e.Group("/api/v1/auth")
	authGroup.GET("/providers", authHandler.ListProviders)
	authGroup.POST("/callback", authHandler.OAuthCallback)

	// Protected routes with auth middleware.
	apiGroup := e.Group("/api/v1", auth.AuthMiddleware(s))

	// User routes — admin only, except PUT which uses RequireAdminOrSelf.
	adminUserGroup := apiGroup.Group("", auth.RequireRole(auth.RoleAdmin))
	adminUserGroup.POST("/users", userHandler.CreateUser)
	adminUserGroup.GET("/users", userHandler.ListUsers)
	adminUserGroup.GET("/users/:id", userHandler.GetUser)

	// PUT /users/:id — admin or self (for full_name only).
	apiGroup.PUT("/users/:id", userHandler.UpdateUser, auth.RequireAdminOrSelf())

	// Workspace routes — admin only.
	adminWsGroup := apiGroup.Group("", auth.RequireRole(auth.RoleAdmin))
	adminWsGroup.POST("/workspaces", workspaceHandler.CreateWorkspace)
	adminWsGroup.GET("/workspaces", workspaceHandler.ListWorkspaces)
	adminWsGroup.POST("/workspaces/:id/archive", workspaceHandler.ArchiveWorkspace)
	adminWsGroup.POST("/workspaces/:id/reactivate", workspaceHandler.ReactivateWorkspace)
	adminWsGroup.DELETE("/workspaces/:id", workspaceHandler.DeleteWorkspace)
	adminWsGroup.POST("/workspaces/:id/members", workspaceHandler.AddOrUpdateMember)
	adminWsGroup.GET("/workspaces/:id/members", workspaceHandler.ListMembers)

	// API key routes — editor or admin for create/refresh/revoke, any auth for list.
	editorKeyGroup := apiGroup.Group("", auth.RequireRole(auth.RoleAdmin, auth.RoleEditor))
	editorKeyGroup.POST("/keys", apiKeyHandler.CreateAPIKey)
	editorKeyGroup.POST("/keys/:key_id/refresh", apiKeyHandler.RefreshAPIKey)
	editorKeyGroup.DELETE("/keys/:key_id", apiKeyHandler.RevokeAPIKey)
	apiGroup.GET("/keys", apiKeyHandler.ListAPIKeys)

	// Health probe (no middleware).
	e.GET("/health", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	return &testEnv{
		Echo:             e,
		Store:            s,
		Registry:         registry,
		AuthHandler:      authHandler,
		UserHandler:      userHandler,
		WorkspaceHandler: workspaceHandler,
		APIKeyHandler:    apiKeyHandler,
	}
}

// seedAdminTokenFull creates an admin token in the store and returns
// the plaintext token string for use in Authorization headers.
func seedAdminTokenFull(t *testing.T, s store.Store, plaintextToken string) {
	t.Helper()
	hash := sha256HexString(plaintextToken)
	_, err := s.CreateAdminToken(&store.AdminToken{
		TokenHash: hash,
	})
	if err != nil {
		t.Fatalf("failed to seed admin token: %v", err)
	}
}

// adminHeaders returns standard headers for admin-authenticated requests.
func adminHeaders(token string) map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + token,
	}
}

// workspaceResponse represents a workspace object in API responses.
type workspaceResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	URL       string `json:"url"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	CreatedBy string `json:"created_by,omitempty"`
}

// userWithMembershipsResponse represents a user with memberships in API responses.
type userWithMembershipsResponse struct {
	userResponse
	Memberships []membershipResponse `json:"memberships,omitempty"`
}

// membershipResponse represents a membership object in API responses.
type membershipResponse struct {
	UserID      string `json:"user_id"`
	WorkspaceID string `json:"workspace_id"`
	Role        string `json:"role"`
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
