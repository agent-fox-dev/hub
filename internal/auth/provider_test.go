package auth_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/agent-fox/af-hub/internal/auth"
	"github.com/agent-fox/af-hub/internal/config"
)

// mockProvider implements auth.Provider for testing purposes.
type mockProvider struct {
	authorizeURL string
	tokenURL     string
	userInfoURL  string
}

func (m *mockProvider) AuthorizeURL(redirectURI string) string {
	return m.authorizeURL + "?redirect_uri=" + redirectURI
}

func (m *mockProvider) ExchangeCode(_ context.Context, _ string, _ string) (*auth.TokenResponse, error) {
	return &auth.TokenResponse{AccessToken: "mock_token"}, nil
}

func (m *mockProvider) GetUserInfo(_ context.Context, _ string) (*auth.UserInfo, error) {
	return &auth.UserInfo{ID: "mock_id", Username: "mockuser", Email: "mock@example.com"}, nil
}

// TS-02-1: Verify that the provider registry defines a common interface with
// authorize URL construction, code exchange, and user info extraction methods.
func TestProviderInterfaceDefinesRequiredMethods(t *testing.T) {
	// The Provider interface must declare at least three methods:
	// 1. AuthorizeURL(redirectURI string) string
	// 2. ExchangeCode(ctx, code, redirectURI) (*TokenResponse, error)
	// 3. GetUserInfo(ctx, accessToken) (*UserInfo, error)

	// Verify that our mock satisfies the Provider interface at compile time.
	var _ auth.Provider = (*mockProvider)(nil)

	mock := &mockProvider{
		authorizeURL: "https://example.com/auth",
		tokenURL:     "https://example.com/token",
		userInfoURL:  "https://example.com/userinfo",
	}

	// Test AuthorizeURL
	url := mock.AuthorizeURL("http://localhost/callback")
	if url == "" {
		t.Error("AuthorizeURL returned empty string")
	}

	// Test ExchangeCode
	token, err := mock.ExchangeCode(context.Background(), "testcode", "http://localhost/callback")
	if err != nil {
		t.Errorf("ExchangeCode returned error: %v", err)
	}
	if token == nil {
		t.Error("ExchangeCode returned nil TokenResponse")
	}
	if token != nil && token.AccessToken == "" {
		t.Error("ExchangeCode returned empty access token")
	}

	// Test GetUserInfo
	info, err := mock.GetUserInfo(context.Background(), "test_token")
	if err != nil {
		t.Errorf("GetUserInfo returned error: %v", err)
	}
	if info == nil {
		t.Error("GetUserInfo returned nil UserInfo")
	}
	if info != nil && info.ID == "" {
		t.Error("GetUserInfo returned empty ID")
	}
}

// TS-02-1: Verify that GitHubProvider also satisfies the Provider interface.
func TestGitHubProviderImplementsProviderInterface(t *testing.T) {
	// Verify compile-time interface satisfaction.
	var _ auth.Provider = (*auth.GitHubProvider)(nil)
}

// TS-02-2: Verify that the GitHub provider ships with built-in default URLs.
func TestGitHubProviderDefaultURLs(t *testing.T) {
	cfg := config.OAuthProviderConfig{
		Provider:     "github",
		ClientID:     "test_client_id",
		ClientSecret: "test_client_secret",
		// No URL overrides — should use built-in defaults.
	}

	provider := auth.NewGitHubProvider(cfg, http.DefaultClient)

	// Verify default authorize URL
	authorizeURL := provider.GetAuthorizeURL()
	if authorizeURL != auth.GitHubDefaultAuthorizeURL {
		t.Errorf("default authorize URL = %q, want %q", authorizeURL, auth.GitHubDefaultAuthorizeURL)
	}

	// Verify default token URL
	tokenURL := provider.GetTokenURL()
	if tokenURL != auth.GitHubDefaultTokenURL {
		t.Errorf("default token URL = %q, want %q", tokenURL, auth.GitHubDefaultTokenURL)
	}

	// Verify default userinfo URL
	userInfoURL := provider.GetUserInfoURL()
	if userInfoURL != auth.GitHubDefaultUserInfoURL {
		t.Errorf("default userinfo URL = %q, want %q", userInfoURL, auth.GitHubDefaultUserInfoURL)
	}
}

// TS-02-2: Verify that config.toml overrides take precedence over built-in defaults.
func TestGitHubProviderConfigOverridesTakePrecedence(t *testing.T) {
	customAuthorize := "https://custom.example.com/oauth/authorize"
	customToken := "https://custom.example.com/oauth/token"
	customUserInfo := "https://custom.example.com/api/user"

	cfg := config.OAuthProviderConfig{
		Provider:     "github",
		ClientID:     "test_client_id",
		ClientSecret: "test_client_secret",
		AuthorizeURL: customAuthorize,
		TokenURL:     customToken,
		UserinfoURL:  customUserInfo,
	}

	provider := auth.NewGitHubProvider(cfg, http.DefaultClient)

	// Verify overridden authorize URL
	if got := provider.GetAuthorizeURL(); got != customAuthorize {
		t.Errorf("overridden authorize URL = %q, want %q", got, customAuthorize)
	}

	// Verify overridden token URL
	if got := provider.GetTokenURL(); got != customToken {
		t.Errorf("overridden token URL = %q, want %q", got, customToken)
	}

	// Verify overridden userinfo URL
	if got := provider.GetUserInfoURL(); got != customUserInfo {
		t.Errorf("overridden userinfo URL = %q, want %q", got, customUserInfo)
	}
}

// TS-02-2: Verify partial overrides: only the overridden URL changes,
// others keep defaults.
func TestGitHubProviderPartialOverride(t *testing.T) {
	customToken := "https://custom.example.com/oauth/token"

	cfg := config.OAuthProviderConfig{
		Provider:     "github",
		ClientID:     "test_client_id",
		ClientSecret: "test_client_secret",
		TokenURL:     customToken,
		// AuthorizeURL and UserinfoURL left empty — should use defaults.
	}

	provider := auth.NewGitHubProvider(cfg, http.DefaultClient)

	// Authorize URL should be the default
	if got := provider.GetAuthorizeURL(); got != auth.GitHubDefaultAuthorizeURL {
		t.Errorf("authorize URL = %q, want default %q", got, auth.GitHubDefaultAuthorizeURL)
	}

	// Token URL should be the override
	if got := provider.GetTokenURL(); got != customToken {
		t.Errorf("token URL = %q, want override %q", got, customToken)
	}

	// Userinfo URL should be the default
	if got := provider.GetUserInfoURL(); got != auth.GitHubDefaultUserInfoURL {
		t.Errorf("userinfo URL = %q, want default %q", got, auth.GitHubDefaultUserInfoURL)
	}
}

// TS-02-3: Verify that a new provider can be registered in the registry
// without modifying auth middleware or handler code.
func TestRegistryAllowsNewProviderRegistration(t *testing.T) {
	cfg := &config.AuthConfig{
		OAuth: []config.OAuthProviderConfig{},
	}
	registry := auth.NewRegistry(cfg)

	mock := &mockProvider{
		authorizeURL: "https://testprovider.example.com/auth",
		tokenURL:     "https://testprovider.example.com/token",
		userInfoURL:  "https://testprovider.example.com/userinfo",
	}

	// Register a new provider
	registry.Register("testprovider", mock)

	// Retrieve and verify
	retrieved, err := registry.GetProvider("testprovider")
	if err != nil {
		t.Fatalf("GetProvider('testprovider') returned error: %v", err)
	}
	if retrieved == nil {
		t.Fatal("GetProvider('testprovider') returned nil")
	}

	// Verify the retrieved provider is the same as what was registered
	url := retrieved.AuthorizeURL("http://localhost/callback")
	expectedPrefix := "https://testprovider.example.com/auth"
	if len(url) < len(expectedPrefix) || url[:len(expectedPrefix)] != expectedPrefix {
		t.Errorf("retrieved provider AuthorizeURL = %q, want prefix %q", url, expectedPrefix)
	}
}

// TS-02-3: Verify that GetProvider returns an error for unknown providers.
func TestRegistryGetProviderReturnsErrorForUnknown(t *testing.T) {
	cfg := &config.AuthConfig{
		OAuth: []config.OAuthProviderConfig{},
	}
	registry := auth.NewRegistry(cfg)

	_, err := registry.GetProvider("nonexistent")
	if err == nil {
		t.Error("GetProvider('nonexistent') should return error, got nil")
	}
}

// TS-02-3: Verify that registering a provider with the same name replaces it.
func TestRegistryReplaceExistingProvider(t *testing.T) {
	cfg := &config.AuthConfig{
		OAuth: []config.OAuthProviderConfig{},
	}
	registry := auth.NewRegistry(cfg)

	original := &mockProvider{authorizeURL: "https://original.com/auth"}
	replacement := &mockProvider{authorizeURL: "https://replacement.com/auth"}

	registry.Register("myprovider", original)
	registry.Register("myprovider", replacement)

	retrieved, err := registry.GetProvider("myprovider")
	if err != nil {
		t.Fatalf("GetProvider returned error: %v", err)
	}

	url := retrieved.AuthorizeURL("http://localhost/cb")
	if url == "" {
		t.Error("AuthorizeURL returned empty string")
	}
	// After replacement, the URL should contain the replacement provider's URL
	expectedPrefix := "https://replacement.com/auth"
	if len(url) < len(expectedPrefix) || url[:len(expectedPrefix)] != expectedPrefix {
		t.Errorf("after replacement, AuthorizeURL = %q, want prefix %q", url, expectedPrefix)
	}
}

// TS-02-2: Verify that NewRegistry with GitHub config creates a registry
// that contains the GitHub provider.
func TestNewRegistryWithGitHubConfig(t *testing.T) {
	cfg := &config.AuthConfig{
		OAuth: []config.OAuthProviderConfig{
			{
				Provider:     "github",
				ClientID:     "test_id",
				ClientSecret: "test_secret",
			},
		},
	}

	registry := auth.NewRegistry(cfg)
	provider, err := registry.GetProvider("github")
	if err != nil {
		t.Fatalf("GetProvider('github') returned error: %v", err)
	}
	if provider == nil {
		t.Fatal("GetProvider('github') returned nil")
	}
}

// TS-02-3: Verify that ListProviders returns all registered provider names.
func TestRegistryListProviders(t *testing.T) {
	cfg := &config.AuthConfig{
		OAuth: []config.OAuthProviderConfig{
			{
				Provider:     "github",
				ClientID:     "test_id",
				ClientSecret: "test_secret",
			},
		},
	}

	registry := auth.NewRegistry(cfg)

	// Also register a custom provider
	custom := &mockProvider{authorizeURL: "https://custom.com/auth"}
	registry.Register("custom", custom)

	names := registry.ListProviders()
	if len(names) < 2 {
		t.Errorf("ListProviders returned %d providers, want at least 2", len(names))
	}

	// Check that both github and custom are present
	found := map[string]bool{}
	for _, name := range names {
		found[name] = true
	}
	if !found["github"] {
		t.Error("ListProviders did not include 'github'")
	}
	if !found["custom"] {
		t.Error("ListProviders did not include 'custom'")
	}
}
