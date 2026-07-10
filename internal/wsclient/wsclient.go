// Package wsclient implements workspace management operations for the afc CLI:
// list, get, and create workspaces, including team slug resolution for
// workspace creation with team association.
package wsclient

import (
	"fmt"
	"net/http"
)

// Team represents a team returned by GET /api/v1/teams.
// Used for team slug resolution during workspace creation.
type Team struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// ListTeams sends GET /api/v1/teams with Bearer authentication and returns
// the parsed list of teams. Returns an error for network failures or non-2xx
// responses.
func ListTeams(hubURL, apiKey string, client *http.Client) ([]Team, error) {
	// Stub: not implemented yet.
	return nil, fmt.Errorf("ListTeams not implemented")
}

// ResolveTeamSlug resolves a team slug to its UUID by searching the given
// teams list. Returns:
//   - the team UUID if exactly one match is found
//   - error "team not found: <slug>" if zero matches
//   - error "ambiguous team slug: <slug>" if multiple matches
func ResolveTeamSlug(teams []Team, slug string) (string, error) {
	// Stub: not implemented yet.
	return "", fmt.Errorf("ResolveTeamSlug not implemented")
}

// ListWorkspaces sends GET /api/v1/workspaces with Bearer authentication.
// Returns the raw response body for pretty-printing, the HTTP status code,
// and any error.
func ListWorkspaces(hubURL, apiKey string, client *http.Client) (body []byte, statusCode int, err error) {
	// Stub: not implemented yet.
	return nil, 0, fmt.Errorf("ListWorkspaces not implemented")
}

// GetWorkspace sends GET /api/v1/workspaces/:slug with Bearer authentication.
// Returns the raw response body for pretty-printing, the HTTP status code,
// and any error.
func GetWorkspace(hubURL, apiKey, slug string, client *http.Client) (body []byte, statusCode int, err error) {
	// Stub: not implemented yet.
	return nil, 0, fmt.Errorf("GetWorkspace not implemented")
}

// CreateWorkspace sends POST /api/v1/workspaces with Bearer authentication
// and a JSON payload. The payload is a map that may contain git_url, slug,
// and optionally branch and team_id. Returns the raw response body, the HTTP
// status code, and any error.
func CreateWorkspace(hubURL, apiKey string, payload map[string]any, client *http.Client) (body []byte, statusCode int, err error) {
	// Stub: not implemented yet.
	return nil, 0, fmt.Errorf("CreateWorkspace not implemented")
}

// ---------------------------------------------------------------------------
// Workspace Token Client Functions
// ---------------------------------------------------------------------------

// CreateToken sends POST /api/v1/workspaces/:slug/tokens with Bearer
// authentication and a JSON payload containing 'expires' (integer) and
// optionally 'label'. Returns the raw response body for pretty-printing,
// the HTTP status code, and any error.
func CreateToken(hubURL, apiKey, slug string, payload map[string]any, client *http.Client) (body []byte, statusCode int, err error) {
	// Stub: not implemented yet.
	return nil, 0, fmt.Errorf("CreateToken not implemented")
}

// ListTokens sends GET /api/v1/workspaces/:slug/tokens with Bearer
// authentication. Returns the raw response body (token metadata only,
// no secrets) for pretty-printing, the HTTP status code, and any error.
func ListTokens(hubURL, apiKey, slug string, client *http.Client) (body []byte, statusCode int, err error) {
	// Stub: not implemented yet.
	return nil, 0, fmt.Errorf("ListTokens not implemented")
}

// RevokeToken sends DELETE /api/v1/workspaces/:slug/tokens/:tokenID with
// Bearer authentication. Returns the HTTP status code, any response body,
// and any error.
func RevokeToken(hubURL, apiKey, slug, tokenID string, client *http.Client) (statusCode int, body []byte, err error) {
	// Stub: not implemented yet.
	return 0, nil, fmt.Errorf("RevokeToken not implemented")
}
