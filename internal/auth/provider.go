// Package auth provides OAuth provider abstractions, authentication middleware,
// and RBAC enforcement for af-hub.
package auth

import "context"

// TokenResponse represents the response from an OAuth token exchange.
type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
}

// UserInfo represents user information retrieved from an OAuth provider.
type UserInfo struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Name     string `json:"name"`
}

// Provider defines the interface that all OAuth providers must implement.
type Provider interface {
	// AuthorizeURL constructs the authorization URL for the given redirect URI.
	AuthorizeURL(redirectURI string) string

	// ExchangeCode exchanges an authorization code for tokens.
	ExchangeCode(ctx context.Context, code string, redirectURI string) (*TokenResponse, error)

	// GetUserInfo retrieves user information using the given access token.
	GetUserInfo(ctx context.Context, accessToken string) (*UserInfo, error)
}
