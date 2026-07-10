// Package authctx provides shared authentication context types used by
// multiple packages in af-hub. This package exists to break the import cycle
// between auth (which provides middleware) and keys/users (which provide
// handlers that auth imports).
//
// All packages that need to read or write AuthContext should import this
// package for the canonical type definitions.
package authctx

// ContextKey is a typed key for Echo context values to avoid collisions.
type ContextKey string

// AuthContextKey is the key used to store AuthContext in Echo's context.
const AuthContextKey ContextKey = "auth_context"

// CredentialType identifies the kind of credential used to authenticate.
type CredentialType string

const (
	// CredentialTypeAdmin identifies admin token authentication.
	CredentialTypeAdmin CredentialType = "admin"
	// CredentialTypeAPIKey identifies user API key authentication.
	CredentialTypeAPIKey CredentialType = "api_key"
	// CredentialTypeWorkspaceToken identifies workspace token authentication.
	CredentialTypeWorkspaceToken CredentialType = "workspace_token"
)

// AuthContext holds the resolved identity for an authenticated request.
// It is stored in Echo's context under AuthContextKey.
// For admin token auth, UserID is empty and IsAdmin is true.
type AuthContext struct {
	CredentialType CredentialType // "admin", "api_key", or "workspace_token"
	UserID         string         // UUID of the authenticated user; empty for admin token auth
	WorkspaceID    string         // UUID of the workspace; empty if not a workspace token
	IsAdmin        bool           // true only for admin token credential type
}
