// Package auth — context key constants for storing authentication
// and authorization information in Echo request contexts.
package auth

// Context key constants used by AuthMiddleware and RBAC enforcement
// to store and retrieve auth information from the request context.
const (
	// ContextKeyUserID stores the authenticated user's ID.
	ContextKeyUserID = "auth_user_id"

	// ContextKeyRole stores the user's role (admin, editor, reader).
	ContextKeyRole = "auth_role"

	// ContextKeyWorkspaceID stores the workspace ID associated with the API key.
	ContextKeyWorkspaceID = "auth_workspace_id"

	// ContextKeyAuthMethod stores how the user authenticated (admin, api_key).
	ContextKeyAuthMethod = "auth_method"

	// ContextKeyUserStatus stores the user's status (active, blocked).
	ContextKeyUserStatus = "auth_user_status"
)

// Auth method constants identifying how a request was authenticated.
const (
	// AuthMethodAdmin indicates authentication via an admin bearer token.
	AuthMethodAdmin = "admin"

	// AuthMethodAPIKey indicates authentication via an API key token.
	AuthMethodAPIKey = "api_key"
)
