package auth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/auth"
)

// ---------------------------------------------------------------------------
// TS-02-1: Provider registry stores GitHub entry with all required fields
// and default scopes of 'user:email'.
// Requirement: 02-REQ-1.1
// ---------------------------------------------------------------------------

func TestProviderRegistry_GitHubDefaults(t *testing.T) {
	registry := auth.NewRegistry()
	cfg := auth.ProviderConfig{
		Name:     "github",
		ClientID: "test-client-id",
	}
	provider := auth.NewGitHubProvider(cfg)
	registry.Register("github", provider, cfg)

	// Verify via the handler response.
	e := echo.New()
	e.GET("/api/v1/auth/providers", auth.GetProvidersHandler(registry))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/providers", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", rec.Code)
	}

	var resp struct {
		Providers []map[string]interface{} `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if len(resp.Providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(resp.Providers))
	}

	gh := resp.Providers[0]
	if gh["name"] != "github" {
		t.Errorf("expected name 'github', got %v", gh["name"])
	}
	if gh["authorize_url"] != "https://github.com/login/oauth/authorize?client_id=test-client-id&scope=user%3Aemail" {
		t.Errorf("expected authorize_url with client_id and scope, got %v", gh["authorize_url"])
	}
	if gh["scopes"] != "user:email" {
		t.Errorf("expected scopes 'user:email', got %v", gh["scopes"])
	}

	// No secrets should be exposed.
	if _, ok := gh["client_secret"]; ok {
		t.Error("client_secret should NOT be in response")
	}
	if _, ok := gh["token_url"]; ok {
		t.Error("token_url should NOT be in response")
	}
}

// ---------------------------------------------------------------------------
// TS-02-2: Provider registry uses configured URL overrides instead of
// defaults when provided.
// Requirement: 02-REQ-1.2
// ---------------------------------------------------------------------------

func TestProviderRegistry_URLOverrides(t *testing.T) {
	registry := auth.NewRegistry()
	cfg := auth.ProviderConfig{
		Name:         "github",
		AuthorizeURL: "https://custom.example.com/authorize",
		TokenURL:     "https://custom.example.com/token",
		UserInfoURL:  "https://custom.example.com/userinfo",
		ClientID:     "test-id",
	}
	provider := auth.NewGitHubProvider(cfg)
	registry.Register("github", provider, cfg)

	e := echo.New()
	e.GET("/api/v1/auth/providers", auth.GetProvidersHandler(registry))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/providers", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", rec.Code)
	}

	var resp struct {
		Providers []map[string]interface{} `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	gh := resp.Providers[0]
	if gh["authorize_url"] != "https://custom.example.com/authorize?client_id=test-id&scope=user%3Aemail" {
		t.Errorf("expected custom authorize_url with client_id and scope, got %v", gh["authorize_url"])
	}
}

// ---------------------------------------------------------------------------
// TS-02-3: A new provider can be added to the registry and appears in the
// providers list without modifying authentication flow code.
// Requirement: 02-REQ-1.3
// ---------------------------------------------------------------------------

func TestProviderRegistry_MultipleProviders(t *testing.T) {
	registry := auth.NewRegistry()

	ghCfg := auth.ProviderConfig{Name: "github", ClientID: "gh-id"}
	registry.Register("github", auth.NewGitHubProvider(ghCfg), ghCfg)

	// Add a second provider (using GitHubProvider as a stand-in for any
	// provider — the point is that the registry supports multiple entries).
	glCfg := auth.ProviderConfig{
		Name:         "gitlab",
		AuthorizeURL: "https://gitlab.com/oauth/authorize",
		Scopes:       "read_user",
		ClientID:     "gl-id",
	}
	registry.Register("gitlab", auth.NewGitHubProvider(glCfg), glCfg)

	e := echo.New()
	e.GET("/api/v1/auth/providers", auth.GetProvidersHandler(registry))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/providers", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", rec.Code)
	}

	var resp struct {
		Providers []map[string]interface{} `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if len(resp.Providers) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(resp.Providers))
	}

	names := make(map[string]bool)
	for _, p := range resp.Providers {
		name, _ := p["name"].(string)
		names[name] = true
	}
	if !names["github"] {
		t.Error("expected 'github' in providers list")
	}
	if !names["gitlab"] {
		t.Error("expected 'gitlab' in providers list")
	}
}

// ---------------------------------------------------------------------------
// TS-02-4: GET /api/v1/auth/providers returns HTTP 200 with providers array
// containing name, authorize_url, and scopes; no secrets exposed.
// Requirement: 02-REQ-1.4
// ---------------------------------------------------------------------------

func TestGetProviders_ResponseShape(t *testing.T) {
	registry := auth.NewRegistry()
	cfg := auth.ProviderConfig{Name: "github", ClientID: "test-id", ClientSecret: "super-secret"}
	registry.Register("github", auth.NewGitHubProvider(cfg), cfg)

	e := echo.New()
	e.GET("/api/v1/auth/providers", auth.GetProvidersHandler(registry))

	// No auth header — this is a public endpoint.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/providers", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", rec.Code)
	}

	var resp struct {
		Providers []map[string]interface{} `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if len(resp.Providers) == 0 {
		t.Fatal("expected at least one provider")
	}

	for _, p := range resp.Providers {
		if _, ok := p["name"]; !ok {
			t.Error("provider entry missing 'name'")
		}
		if _, ok := p["authorize_url"]; !ok {
			t.Error("provider entry missing 'authorize_url'")
		}
		if _, ok := p["scopes"]; !ok {
			t.Error("provider entry missing 'scopes'")
		}
		// Secrets must not be exposed.
		if _, ok := p["client_secret"]; ok {
			t.Error("provider entry should NOT include 'client_secret'")
		}
		if _, ok := p["token_url"]; ok {
			t.Error("provider entry should NOT include 'token_url'")
		}
	}
}

// ---------------------------------------------------------------------------
// TS-02-E1: GET /api/v1/auth/providers returns HTTP 200 with empty providers
// array when no providers are registered.
// Requirement: 02-REQ-1.E1
// ---------------------------------------------------------------------------

func TestGetProviders_EmptyRegistry(t *testing.T) {
	registry := auth.NewRegistry()

	e := echo.New()
	e.GET("/api/v1/auth/providers", auth.GetProvidersHandler(registry))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/providers", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d", rec.Code)
	}

	var resp struct {
		Providers []map[string]interface{} `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if resp.Providers == nil {
		t.Error("providers should be an empty array, not null")
	}
	if len(resp.Providers) != 0 {
		t.Errorf("expected 0 providers, got %d", len(resp.Providers))
	}
}

// ---------------------------------------------------------------------------
// Registry.Lookup and IsRegistered
// ---------------------------------------------------------------------------

func TestRegistry_LookupAndIsRegistered(t *testing.T) {
	registry := auth.NewRegistry()

	// Before registration.
	if registry.IsRegistered("github") {
		t.Error("github should not be registered yet")
	}
	if _, ok := registry.Lookup("github"); ok {
		t.Error("Lookup should return false for unregistered provider")
	}

	// After registration.
	cfg := auth.ProviderConfig{Name: "github"}
	registry.Register("github", auth.NewGitHubProvider(cfg), cfg)

	if !registry.IsRegistered("github") {
		t.Error("github should be registered")
	}
	p, ok := registry.Lookup("github")
	if !ok || p == nil {
		t.Error("Lookup should return the registered provider")
	}

	// Unregistered provider still returns false.
	if registry.IsRegistered("gitlab") {
		t.Error("gitlab should not be registered")
	}
}

// ---------------------------------------------------------------------------
// GitHub Provider — ExchangeCode and GetUserInfo
// ---------------------------------------------------------------------------

func TestGitHubProvider_ExchangeCode(t *testing.T) {
	// Mock GitHub token endpoint.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"access_token": "gho_test_token_12345",
		})
	}))
	defer ts.Close()

	cfg := auth.ProviderConfig{
		Name:         "github",
		TokenURL:     ts.URL,
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
	}
	p := auth.NewGitHubProvider(cfg)

	resp, err := p.ExchangeCode(context.Background(), "test-code", "http://localhost:3000/cb")
	if err != nil {
		t.Fatalf("ExchangeCode failed: %v", err)
	}
	if resp.AccessToken != "gho_test_token_12345" {
		t.Errorf("expected access_token 'gho_test_token_12345', got %q", resp.AccessToken)
	}
}

func TestGitHubProvider_ExchangeCode_Error(t *testing.T) {
	// Mock GitHub token endpoint that returns an error.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server error"))
	}))
	defer ts.Close()

	cfg := auth.ProviderConfig{
		Name:     "github",
		TokenURL: ts.URL,
	}
	p := auth.NewGitHubProvider(cfg)

	_, err := p.ExchangeCode(context.Background(), "bad-code", "http://localhost:3000/cb")
	if err == nil {
		t.Fatal("ExchangeCode should fail on HTTP 500")
	}
}

func TestGitHubProvider_GetUserInfo(t *testing.T) {
	// Mock GitHub user info endpoint.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer gho_test_token" {
			t.Errorf("expected Bearer auth, got %q", authHeader)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":    12345,
			"login": "testuser",
			"email": "test@example.com",
			"name":  "Test User",
		})
	}))
	defer ts.Close()

	cfg := auth.ProviderConfig{
		Name:        "github",
		UserInfoURL: ts.URL,
	}
	p := auth.NewGitHubProvider(cfg)

	info, err := p.GetUserInfo(context.Background(), "gho_test_token")
	if err != nil {
		t.Fatalf("GetUserInfo failed: %v", err)
	}
	if info.ID != "12345" {
		t.Errorf("expected ID '12345', got %q", info.ID)
	}
	if info.Login != "testuser" {
		t.Errorf("expected Login 'testuser', got %q", info.Login)
	}
	if info.Email != "test@example.com" {
		t.Errorf("expected Email 'test@example.com', got %q", info.Email)
	}
	if info.Name != "Test User" {
		t.Errorf("expected Name 'Test User', got %q", info.Name)
	}
}

func TestGitHubProvider_GetUserInfo_Error(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))
	}))
	defer ts.Close()

	cfg := auth.ProviderConfig{
		Name:        "github",
		UserInfoURL: ts.URL,
	}
	p := auth.NewGitHubProvider(cfg)

	_, err := p.GetUserInfo(context.Background(), "bad-token")
	if err == nil {
		t.Fatal("GetUserInfo should fail on HTTP 401")
	}
}

func TestGitHubProvider_ExchangeCode_ContextTimeout(t *testing.T) {
	// Mock GitHub token endpoint that never responds.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until context is cancelled.
		<-r.Context().Done()
	}))
	defer ts.Close()

	cfg := auth.ProviderConfig{
		Name:     "github",
		TokenURL: ts.URL,
	}
	p := auth.NewGitHubProvider(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err := p.ExchangeCode(ctx, "test-code", "http://localhost:3000/cb")
	if err == nil {
		t.Fatal("ExchangeCode should fail with cancelled context")
	}
}
