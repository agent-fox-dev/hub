// Package workspace implements workspace management and workspace token
// delegation for af-hub. Covers CRUD endpoints for workspaces and their
// associated access tokens.
package workspace

// Workspace represents the API response object for a workspace.
// All workspace endpoints return this same schema regardless of caller type.
type Workspace struct {
	ID          string  `json:"id"`
	Slug        string  `json:"slug"`
	GitURL      string  `json:"git_url"`
	Branch      *string `json:"branch"`
	TeamID      *string `json:"team_id"`
	OwnerUserID string  `json:"owner_user_id"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// CreateWorkspaceRequest is the JSON request body for POST /api/v1/workspaces.
type CreateWorkspaceRequest struct {
	Slug   string  `json:"slug"`
	GitURL string  `json:"git_url"`
	Branch *string `json:"branch,omitempty"`
	TeamID *string `json:"team_id,omitempty"`
}

// CreateTokenRequest is the JSON request body for POST .../tokens.
type CreateTokenRequest struct {
	Label   *string `json:"label,omitempty"`
	Expires *int    `json:"expires,omitempty"`
}

// TokenCreateResponse is the response for POST .../tokens (HTTP 201).
// The Token field contains the full plaintext token string, returned exactly once.
type TokenCreateResponse struct {
	Token     string  `json:"token"`
	TokenID   string  `json:"token_id"`
	Label     *string `json:"label"`
	ExpiresAt *string `json:"expires_at"`
	CreatedAt string  `json:"created_at"`
}

// TokenListItem is the response item in GET .../tokens arrays.
// The secret is never included in list responses.
type TokenListItem struct {
	TokenID   string  `json:"token_id"`
	Label     *string `json:"label"`
	CreatedAt string  `json:"created_at"`
	ExpiresAt *string `json:"expires_at"`
	RevokedAt *string `json:"revoked_at"`
}
