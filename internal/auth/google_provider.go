package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
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
func NewGoogleProvider(cfg ProviderConfig) *GoogleProvider {
	p := &GoogleProvider{
		authorizeURL: DefaultGoogleAuthorizeURL,
		tokenURL:     DefaultGoogleTokenURL,
		userInfoURL:  DefaultGoogleUserInfoURL,
		scopes:       DefaultGoogleScopes,
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
func (p *GoogleProvider) SetHTTPClient(c *http.Client) {
	p.httpClient = c
}

// AuthorizeURL returns the configured authorization URL for Google.
func (p *GoogleProvider) AuthorizeURL() string {
	return p.authorizeURL
}

// Scopes returns the OAuth scopes requested from Google.
func (p *GoogleProvider) Scopes() string {
	return p.scopes
}

// ExchangeCode exchanges an authorization code for a Google access token.
// It POSTs to the token URL with the code, client_id, client_secret,
// redirect_uri, and grant_type=authorization_code. The context deadline
// is respected for timeout control.
func (p *GoogleProvider) ExchangeCode(ctx context.Context, code, redirectURI string) (*TokenResponse, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
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

// usernameAllowed matches characters valid in an af-hub username: [0-9A-Za-z-].
var usernameAllowed = regexp.MustCompile(`[^0-9A-Za-z-]`)

// maxUsernameLen is the maximum length of a derived username.
const maxUsernameLen = 39

// deriveUsername extracts a valid af-hub username from a Google email address.
// It takes the local part (before @), removes all characters not matching
// [0-9A-Za-z-], and truncates to 39 characters. Returns an error if the email
// has no @ sign, an empty local part, or the sanitized result is empty.
func deriveUsername(email string) (string, error) {
	localPart, _, found := strings.Cut(email, "@")
	if !found {
		return "", fmt.Errorf("cannot derive username: email %q has no @ character", email)
	}
	if localPart == "" {
		return "", fmt.Errorf("cannot derive username: email %q has empty local part", email)
	}

	// Remove all characters not in [0-9A-Za-z-].
	sanitized := usernameAllowed.ReplaceAllString(localPart, "")
	if sanitized == "" {
		return "", fmt.Errorf("cannot derive username: email %q yields empty username after sanitization", email)
	}

	// Truncate to maxUsernameLen characters.
	if len(sanitized) > maxUsernameLen {
		sanitized = sanitized[:maxUsernameLen]
	}

	return sanitized, nil
}

// GetUserInfo retrieves the authenticated user's profile from Google.
// It sends a GET request to the userinfo URL with a Bearer token, decodes the
// JSON response, and derives the Login field from the email's local part.
// The context deadline is respected for timeout control.
func (p *GoogleProvider) GetUserInfo(ctx context.Context, token string) (*UserInfo, error) {
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
		return nil, fmt.Errorf("google userinfo endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	var googleUser struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal(body, &googleUser); err != nil {
		return nil, fmt.Errorf("parsing userinfo response: %w", err)
	}

	login, err := deriveUsername(googleUser.Email)
	if err != nil {
		return nil, err
	}

	return &UserInfo{
		ID:    googleUser.ID,
		Login: login,
		Email: googleUser.Email,
		Name:  googleUser.Name,
	}, nil
}
