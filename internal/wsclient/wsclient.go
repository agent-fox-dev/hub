// Package wsclient implements workspace management operations for the afc CLI:
// list, get, and create workspaces, including team slug resolution for
// workspace creation with team association.
package wsclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/agent-fox-dev/hub/internal/httpclient"
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
	resp, err := httpclient.DoRequest(client, "GET", hubURL+"/api/v1/teams", apiKey, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("server returned HTTP %d", resp.StatusCode)
	}

	var teams []Team
	if err := json.Unmarshal(data, &teams); err != nil {
		return nil, fmt.Errorf("failed to parse teams response: %w", err)
	}
	return teams, nil
}

// ResolveTeamSlug resolves a team slug to its UUID by searching the given
// teams list. Returns:
//   - the team UUID if exactly one match is found
//   - error "team not found: <slug>" if zero matches
//   - error "ambiguous team slug: <slug>" if multiple matches
func ResolveTeamSlug(teams []Team, slug string) (string, error) {
	var matches []Team
	for _, t := range teams {
		if t.Slug == slug {
			matches = append(matches, t)
		}
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("team not found: %s", slug)
	case 1:
		return matches[0].ID, nil
	default:
		return "", fmt.Errorf("ambiguous team slug: %s", slug)
	}
}

// ListWorkspaces sends GET /api/v1/workspaces with Bearer authentication.
// Returns the raw response body for pretty-printing, the HTTP status code,
// and any error.
func ListWorkspaces(hubURL, apiKey string, client *http.Client) (body []byte, statusCode int, err error) {
	resp, err := httpclient.DoRequest(client, "GET", hubURL+"/api/v1/workspaces", apiKey, nil)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read response body: %w", err)
	}
	return data, resp.StatusCode, nil
}

// GetWorkspace sends GET /api/v1/workspaces/:slug with Bearer authentication.
// Returns the raw response body for pretty-printing, the HTTP status code,
// and any error.
func GetWorkspace(hubURL, apiKey, slug string, client *http.Client) (body []byte, statusCode int, err error) {
	url := fmt.Sprintf("%s/api/v1/workspaces/%s", hubURL, slug)
	resp, err := httpclient.DoRequest(client, "GET", url, apiKey, nil)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read response body: %w", err)
	}
	return data, resp.StatusCode, nil
}

// CreateWorkspace sends POST /api/v1/workspaces with Bearer authentication
// and a JSON payload. The payload is a map that may contain git_url, slug,
// and optionally branch and team_id. Returns the raw response body, the HTTP
// status code, and any error.
func CreateWorkspace(hubURL, apiKey string, payload map[string]any, client *http.Client) (body []byte, statusCode int, err error) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to encode workspace payload: %w", err)
	}

	resp, err := httpclient.DoRequest(client, "POST", hubURL+"/api/v1/workspaces", apiKey, bytes.NewReader(payloadBytes))
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read response body: %w", err)
	}
	return data, resp.StatusCode, nil
}

// ---------------------------------------------------------------------------
// Workspace Token Client Functions
// ---------------------------------------------------------------------------

// CreateToken sends POST /api/v1/workspaces/:slug/tokens with Bearer
// authentication and a JSON payload containing 'expires' (integer) and
// optionally 'label'. Returns the raw response body for pretty-printing,
// the HTTP status code, and any error.
func CreateToken(hubURL, apiKey, slug string, payload map[string]any, client *http.Client) (body []byte, statusCode int, err error) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to encode token payload: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/workspaces/%s/tokens", hubURL, slug)
	resp, err := httpclient.DoRequest(client, "POST", url, apiKey, bytes.NewReader(payloadBytes))
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read response body: %w", err)
	}
	return data, resp.StatusCode, nil
}

// ListTokens sends GET /api/v1/workspaces/:slug/tokens with Bearer
// authentication. Returns the raw response body (token metadata only,
// no secrets) for pretty-printing, the HTTP status code, and any error.
func ListTokens(hubURL, apiKey, slug string, client *http.Client) (body []byte, statusCode int, err error) {
	url := fmt.Sprintf("%s/api/v1/workspaces/%s/tokens", hubURL, slug)
	resp, err := httpclient.DoRequest(client, "GET", url, apiKey, nil)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read response body: %w", err)
	}
	return data, resp.StatusCode, nil
}

// RevokeToken sends DELETE /api/v1/workspaces/:slug/tokens/:tokenID with
// Bearer authentication. Returns the HTTP status code, any response body,
// and any error.
func RevokeToken(hubURL, apiKey, slug, tokenID string, client *http.Client) (statusCode int, body []byte, err error) {
	url := fmt.Sprintf("%s/api/v1/workspaces/%s/tokens/%s", hubURL, slug, tokenID)
	resp, err := httpclient.DoRequest(client, "DELETE", url, apiKey, nil)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("failed to read response body: %w", err)
	}
	return resp.StatusCode, data, nil
}
