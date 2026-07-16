package auth

import (
	"context"
	"net/http"
)

// Default Google OAuth / OpenID Connect URLs.
const (
	DefaultGoogleAuthorizeURL = "https://accounts.google.com/o/oauth2/v2/auth"
	DefaultGoogleTokenURL     = "https://oauth2.googleapis.com/token"
	DefaultGoogleUserInfoURL  = "https://www.googleapis.com/oauth2/v2/userinfo"
	DefaultGoogleScopes       = "openid email profile"
)

// GoogleProvider implements the Provider interface for Google OAuth 2.0 /
// OpenID Connect authentication.
type GoogleProvider struct {
	authorizeURL string
	tokenURL     string
	userInfoURL  string
	scopes       string
	clientID     string
	clientSecret string
	httpClient   *http.Client
}

// NewGoogleProvider creates a new GoogleProvider from the given config.
// URL fields default to Google's well-known OAuth URLs if not overridden.
//
// TODO(group-4): Implement default URL assignment and config override logic.
func NewGoogleProvider(cfg ProviderConfig) *GoogleProvider {
	return &GoogleProvider{}
}

// SetHTTPClient sets a custom HTTP client for outbound requests.
// This is used in tests to inject httptest servers.
//
// TODO(group-4): Store the client for use in HTTP calls.
func (p *GoogleProvider) SetHTTPClient(c *http.Client) {
}

// AuthorizeURL returns the configured authorization URL for Google.
//
// TODO(group-4): Return p.authorizeURL.
func (p *GoogleProvider) AuthorizeURL() string {
	return ""
}

// Scopes returns the OAuth scopes requested from Google.
//
// TODO(group-4): Return p.scopes.
func (p *GoogleProvider) Scopes() string {
	return ""
}

// ExchangeCode exchanges an authorization code for a Google access token.
//
// TODO(group-5): Implement token exchange with grant_type=authorization_code.
func (p *GoogleProvider) ExchangeCode(ctx context.Context, code, redirectURI string) (*TokenResponse, error) {
	return nil, nil
}

// GetUserInfo retrieves the authenticated user's profile from Google.
//
// TODO(group-6): Implement userinfo retrieval and username derivation.
func (p *GoogleProvider) GetUserInfo(ctx context.Context, token string) (*UserInfo, error) {
	return nil, nil
}
