// Package auth — GitHub OAuth provider implementation.
package auth

import (
	"context"
	"net/http"

	"github.com/agent-fox/af-hub/internal/config"
)

// GitHub provider default URLs.
const (
	GitHubDefaultAuthorizeURL = "https://github.com/login/oauth/authorize"
	GitHubDefaultTokenURL     = "https://github.com/login/oauth/access_token"
	GitHubDefaultUserInfoURL  = "https://api.github.com/user"
)

// GitHubProvider implements the Provider interface for GitHub OAuth.
type GitHubProvider struct {
	clientID     string
	clientSecret string
	authorizeURL string
	tokenURL     string
	userInfoURL  string
	httpClient   *http.Client
}

// NewGitHubProvider creates a GitHubProvider with built-in defaults.
// Config overrides take precedence over defaults.
func NewGitHubProvider(cfg config.OAuthProviderConfig, httpClient *http.Client) *GitHubProvider {
	panic("not implemented")
}

// AuthorizeURL constructs the GitHub authorization URL.
func (g *GitHubProvider) AuthorizeURL(redirectURI string) string {
	panic("not implemented")
}

// ExchangeCode exchanges an authorization code for a GitHub access token.
func (g *GitHubProvider) ExchangeCode(ctx context.Context, code string, redirectURI string) (*TokenResponse, error) {
	panic("not implemented")
}

// GetUserInfo retrieves the authenticated user's info from GitHub.
func (g *GitHubProvider) GetUserInfo(ctx context.Context, accessToken string) (*UserInfo, error) {
	panic("not implemented")
}

// GetAuthorizeURL returns the configured authorize URL for inspection.
func (g *GitHubProvider) GetAuthorizeURL() string {
	panic("not implemented")
}

// GetTokenURL returns the configured token URL for inspection.
func (g *GitHubProvider) GetTokenURL() string {
	panic("not implemented")
}

// GetUserInfoURL returns the configured user info URL for inspection.
func (g *GitHubProvider) GetUserInfoURL() string {
	panic("not implemented")
}
