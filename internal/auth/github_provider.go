package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Default GitHub OAuth URLs.
const (
	DefaultGitHubAuthorizeURL = "https://github.com/login/oauth/authorize"
	DefaultGitHubTokenURL     = "https://github.com/login/oauth/access_token"
	DefaultGitHubUserInfoURL  = "https://api.github.com/user"
	DefaultGitHubScopes       = "user:email"
)

// GitHubProvider implements the Provider interface for GitHub OAuth.
type GitHubProvider struct {
	authorizeURL string
	tokenURL     string
	userInfoURL  string
	scopes       string
	clientID     string
	clientSecret string
	httpClient   *http.Client
}

// NewGitHubProvider creates a new GitHubProvider from the given config.
// URL fields default to GitHub's well-known OAuth URLs if not overridden.
func NewGitHubProvider(cfg ProviderConfig) *GitHubProvider {
	p := &GitHubProvider{
		authorizeURL: DefaultGitHubAuthorizeURL,
		tokenURL:     DefaultGitHubTokenURL,
		userInfoURL:  DefaultGitHubUserInfoURL,
		scopes:       DefaultGitHubScopes,
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		httpClient:   http.DefaultClient,
	}

	if cfg.AuthorizeURL != "" {
		p.authorizeURL = cfg.AuthorizeURL
	}
	if cfg.TokenURL != "" {
		p.tokenURL = cfg.TokenURL
	}
	if cfg.UserInfoURL != "" {
		p.userInfoURL = cfg.UserInfoURL
	}
	if cfg.Scopes != "" {
		p.scopes = cfg.Scopes
	}

	return p
}

// SetHTTPClient sets a custom HTTP client for outbound requests.
// This is used in tests to inject httptest servers.
func (p *GitHubProvider) SetHTTPClient(c *http.Client) {
	p.httpClient = c
}

// AuthorizeURL returns the configured authorization URL for GitHub.
func (p *GitHubProvider) AuthorizeURL() string {
	return p.authorizeURL
}

// Scopes returns the OAuth scopes requested from GitHub.
func (p *GitHubProvider) Scopes() string {
	return p.scopes
}

// ExchangeCode exchanges an authorization code for a GitHub access token.
// It POSTs to the token URL with the code, client_id, client_secret, and
// redirect_uri. The context deadline is respected for timeout control.
func (p *GitHubProvider) ExchangeCode(ctx context.Context, code, redirectURI string) (*TokenResponse, error) {
	data := url.Values{
		"client_id":     {p.clientID},
		"client_secret": {p.clientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("building token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading token response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("parsing token response: %w", err)
	}
	if tokenResp.Error != "" {
		return nil, fmt.Errorf("token exchange error: %s", tokenResp.Error)
	}
	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("token exchange returned empty access_token")
	}

	return &TokenResponse{AccessToken: tokenResp.AccessToken}, nil
}

// GetUserInfo fetches the authenticated user's profile from GitHub.
// It calls the user info URL with a Bearer token. The context deadline
// is respected for timeout control.
func (p *GitHubProvider) GetUserInfo(ctx context.Context, token string) (*UserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.userInfoURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building userinfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("userinfo request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading userinfo response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var ghUser struct {
		ID    json.Number `json:"id"`
		Login string      `json:"login"`
		Email string      `json:"email"`
		Name  string      `json:"name"`
	}
	if err := json.Unmarshal(body, &ghUser); err != nil {
		return nil, fmt.Errorf("parsing userinfo response: %w", err)
	}

	return &UserInfo{
		ID:    ghUser.ID.String(),
		Login: ghUser.Login,
		Email: ghUser.Email,
		Name:  ghUser.Name,
	}, nil
}
