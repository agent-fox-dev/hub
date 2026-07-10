// Package auth provides authentication types and middleware for af-hub.
// Implements the AuthContext and credential types defined in server_foundation (spec 01).
//
// The canonical type definitions live in internal/authctx to avoid import
// cycles with handler packages. This file re-exports them as type aliases
// so existing code that references auth.AuthContext continues to work.
package auth

import "github.com/agent-fox-dev/hub/internal/authctx"

// ContextKey is a typed key for Echo context values to avoid collisions.
type ContextKey = authctx.ContextKey

// AuthContextKey is the key used to store AuthContext in Echo's context.
const AuthContextKey = authctx.AuthContextKey

// CredentialType identifies the kind of credential used to authenticate.
type CredentialType = authctx.CredentialType

const (
	// CredentialTypeAdmin identifies admin token authentication.
	CredentialTypeAdmin = authctx.CredentialTypeAdmin
	// CredentialTypeAPIKey identifies user API key authentication.
	CredentialTypeAPIKey = authctx.CredentialTypeAPIKey
	// CredentialTypeWorkspaceToken identifies workspace token authentication.
	CredentialTypeWorkspaceToken = authctx.CredentialTypeWorkspaceToken
)

// AuthContext holds the resolved identity for an authenticated request.
// It is stored in Echo's context under AuthContextKey.
// For admin token auth, UserID is empty and IsAdmin is true.
type AuthContext = authctx.AuthContext
