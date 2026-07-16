package auth

import (
	"context"
	"net/url"
)

// TokenResponse holds the result of exchanging an authorization code with an
// OAuth provider. The AccessToken is used to fetch user information.
type TokenResponse struct {
	AccessToken string
}

// UserInfo holds user profile data retrieved from an OAuth provider.
type UserInfo struct {
	ID    string // provider_id (opaque string from the provider, e.g. GitHub integer user ID as string)
	Login string // username (e.g. GitHub login)
	Email string // user's email address
	Name  string // display name (full_name)
}

// Provider defines the interface for an OAuth identity provider.
// Each provider implementation handles URL construction, code exchange,
// and user info retrieval for a specific OAuth service.
type Provider interface {
	// AuthorizeURL returns the base authorization URL for this provider.
	// The returned URL does not include query parameters like state or client_id.
	AuthorizeURL() string

	// Scopes returns the OAuth scopes requested by this provider.
	Scopes() string

	// ExchangeCode exchanges an authorization code for an access token.
	// The redirectURI must match the one used in the authorization request.
	ExchangeCode(ctx context.Context, code, redirectURI string) (*TokenResponse, error)

	// GetUserInfo retrieves the authenticated user's profile from the provider.
	GetUserInfo(ctx context.Context, token string) (*UserInfo, error)
}

// ProviderConfig holds the configuration for registering an OAuth provider.
type ProviderConfig struct {
	Name         string // Provider name (e.g. "github")
	AuthorizeURL string // OAuth authorization endpoint URL
	TokenURL     string // OAuth token exchange endpoint URL
	UserInfoURL  string // User info/profile endpoint URL
	Scopes       string // OAuth scopes to request
	ClientID     string // OAuth client ID
	ClientSecret string // OAuth client secret
}

// Registry holds registered OAuth providers keyed by name.
type Registry struct {
	providers map[string]Provider
	// configs stores the original config for each provider, used for
	// listing provider metadata.
	configs map[string]ProviderConfig
}

// NewRegistry creates a new empty provider registry.
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[string]Provider),
		configs:   make(map[string]ProviderConfig),
	}
}

// Register adds a provider to the registry under the given name.
// If a provider with the same name already exists, it is replaced.
func (r *Registry) Register(name string, p Provider, cfg ProviderConfig) {
	r.providers[name] = p
	r.configs[name] = cfg
}

// Lookup returns the provider registered under the given name and true,
// or nil and false if no provider is registered with that name.
func (r *Registry) Lookup(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// IsRegistered returns true if a provider with the given name exists in the
// registry. This satisfies the users.ProviderRegistry interface.
func (r *Registry) IsRegistered(name string) bool {
	_, ok := r.providers[name]
	return ok
}

// List returns all registered provider names and their public metadata.
// The result is a slice of ProviderEntry values suitable for the
// GET /api/v1/auth/providers response.
func (r *Registry) List() []ProviderEntry {
	entries := make([]ProviderEntry, 0, len(r.providers))
	for name, p := range r.providers {
		authorizeURL := p.AuthorizeURL()
		if cfg, ok := r.configs[name]; ok {
			if u, err := url.Parse(authorizeURL); err == nil {
				q := u.Query()
				q.Set("client_id", cfg.ClientID)
				q.Set("scope", p.Scopes())
				u.RawQuery = q.Encode()
				authorizeURL = u.String()
			}
		}
		entries = append(entries, ProviderEntry{
			Name:         name,
			AuthorizeURL: authorizeURL,
			Scopes:       p.Scopes(),
		})
	}
	return entries
}

// ProviderEntry holds the public metadata for a registered provider,
// suitable for inclusion in the GET /api/v1/auth/providers response.
// No secrets (client_secret, token_url, userinfo_url) are included.
type ProviderEntry struct {
	Name         string `json:"name"`
	AuthorizeURL string `json:"authorize_url"`
	Scopes       string `json:"scopes"`
}
