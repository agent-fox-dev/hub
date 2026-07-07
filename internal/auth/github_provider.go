// Package auth — GitHub OAuth provider implementation.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

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
	p := &GitHubProvider{
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		authorizeURL: GitHubDefaultAuthorizeURL,
		tokenURL:     GitHubDefaultTokenURL,
		userInfoURL:  GitHubDefaultUserInfoURL,
		httpClient:   httpClient,
	}

	if cfg.AuthorizeURL != "" {
		p.authorizeURL = cfg.AuthorizeURL
	}
	if cfg.TokenURL != "" {
		p.tokenURL = cfg.TokenURL
	}
	if cfg.UserinfoURL != "" {
		p.userInfoURL = cfg.UserinfoURL
	}

	if p.httpClient == nil {
		p.httpClient = http.DefaultClient
	}

	return p
}

// AuthorizeURL constructs the GitHub authorization URL with client_id and
// redirect_uri query parameters.
func (g *GitHubProvider) AuthorizeURL(redirectURI string) string {
	u, err := url.Parse(g.authorizeURL)
	if err != nil {
		// Fallback: append query parameters manually.
		return g.authorizeURL + "?client_id=" + url.QueryEscape(g.clientID) +
			"&redirect_uri=" + url.QueryEscape(redirectURI)
	}
	q := u.Query()
	q.Set("client_id", g.clientID)
	q.Set("redirect_uri", redirectURI)
	u.RawQuery = q.Encode()
	return u.String()
}

// ExchangeCode exchanges an authorization code for a GitHub access token.
// Returns ErrProviderTimeout if the token endpoint does not respond or
// returns HTTP 504. Returns ErrCodeExchangeFailed if the code is rejected.
func (g *GitHubProvider) ExchangeCode(ctx context.Context, code string, redirectURI string) (*TokenResponse, error) {
	data := url.Values{
		"client_id":     {g.clientID},
		"client_secret": {g.clientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		// Context deadline exceeded or HTTP timeout → provider timeout.
		return nil, fmt.Errorf("%w: %v", ErrProviderTimeout, err)
	}
	defer resp.Body.Close()

	// HTTP 504 Gateway Timeout → provider timeout.
	if resp.StatusCode == http.StatusGatewayTimeout {
		return nil, fmt.Errorf("%w: token endpoint returned %d", ErrProviderTimeout, resp.StatusCode)
	}

	// Any other non-2xx response → exchange failed.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: token endpoint returned HTTP %d", ErrCodeExchangeFailed, resp.StatusCode)
	}

	// GitHub returns 200 with error JSON when the code is rejected.
	var result struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("%w: decode token response: %v", ErrCodeExchangeFailed, err)
	}

	if result.Error != "" {
		return nil, fmt.Errorf("%w: %s", ErrCodeExchangeFailed, result.ErrorDesc)
	}

	if result.AccessToken == "" {
		return nil, fmt.Errorf("%w: empty access token in response", ErrCodeExchangeFailed)
	}

	return &TokenResponse{
		AccessToken: result.AccessToken,
		TokenType:   result.TokenType,
	}, nil
}

// GetUserInfo retrieves the authenticated user's info from GitHub.
// Returns ErrProviderTimeout if the userinfo endpoint does not respond
// or returns HTTP 504.
func (g *GitHubProvider) GetUserInfo(ctx context.Context, accessToken string) (*UserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.userInfoURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create userinfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProviderTimeout, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusGatewayTimeout {
		return nil, fmt.Errorf("%w: userinfo endpoint returned %d", ErrProviderTimeout, resp.StatusCode)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("userinfo request failed: HTTP %d", resp.StatusCode)
	}

	// GitHub returns id as a number, login as the username.
	var result struct {
		ID    json.Number `json:"id"`
		Login string      `json:"login"`
		Email string      `json:"email"`
		Name  string      `json:"name"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode userinfo response: %w", err)
	}

	return &UserInfo{
		ID:       result.ID.String(),
		Username: result.Login,
		Email:    result.Email,
		Name:     result.Name,
	}, nil
}

// GetAuthorizeURL returns the configured authorize URL for inspection.
func (g *GitHubProvider) GetAuthorizeURL() string {
	return g.authorizeURL
}

// GetTokenURL returns the configured token URL for inspection.
func (g *GitHubProvider) GetTokenURL() string {
	return g.tokenURL
}

// GetUserInfoURL returns the configured user info URL for inspection.
func (g *GitHubProvider) GetUserInfoURL() string {
	return g.userInfoURL
}
