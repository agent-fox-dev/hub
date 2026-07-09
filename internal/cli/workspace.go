package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"
)

// newWorkspaceCmd creates the "workspace" parent command.
func newWorkspaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspace",
		Short: "Manage workspaces",
		Long:  "Manage workspaces in af-hub. A workspace maps to a git repository.",
	}

	cmd.AddCommand(newWorkspaceCreateCmd())

	return cmd
}

// newWorkspaceCreateCmd creates the "workspace create" subcommand.
func newWorkspaceCreateCmd() *cobra.Command {
	var gitURL string
	var slug string
	var branch string
	var team string
	var wsAPIKey string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new workspace",
		Long:  "Create a new workspace mapped to a git repository.",
		RunE: func(cmd *cobra.Command, args []string) error {
			hub, err := resolveHubURL()
			if err != nil {
				return err
			}
			key, err := resolveAPIKey(wsAPIKey)
			if err != nil {
				return err
			}

			client := newHTTPClient()

			// If --team is provided, resolve the team slug to a UUID.
			var teamID string
			if team != "" {
				resolved, err := resolveTeamSlug(client, hub, key, team)
				if err != nil {
					return err
				}
				teamID = resolved
			}

			// Build the workspace creation request body.
			body := map[string]any{
				"slug":    slug,
				"git_url": gitURL,
			}
			if branch != "" {
				body["branch"] = branch
			}
			if teamID != "" {
				body["team_id"] = teamID
			}

			bodyJSON, err := json.Marshal(body)
			if err != nil {
				return fmt.Errorf("failed to marshal request body: %w", err)
			}

			req, err := http.NewRequest("POST", hub+"/api/v1/workspaces", bytes.NewReader(bodyJSON))
			if err != nil {
				return fmt.Errorf("failed to create request: %w", err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+key)

			resp, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("failed to connect to hub at %s: %w", hub, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return ParseHTTPError(resp)
			}

			// Decode the response body as JSON.
			var result json.RawMessage
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				return fmt.Errorf("unexpected server response: failed to parse JSON: %w", err)
			}

			return PrintJSON(cmd.OutOrStdout(), result)
		},
	}

	cmd.Flags().StringVar(&gitURL, "git-url", "", "Git remote URL (required)")
	cmd.Flags().StringVar(&slug, "slug", "", "Workspace slug (required)")
	cmd.Flags().StringVar(&branch, "branch", "", "Git branch (optional)")
	cmd.Flags().StringVar(&team, "team", "", "Team slug (optional, resolved to team UUID)")
	cmd.Flags().StringVar(&wsAPIKey, "api-key", "", "API key for authentication (overrides AF_HUB_API_KEY)")

	// Mark required flags so Cobra enforces them before RunE.
	_ = cmd.MarkFlagRequired("git-url")
	_ = cmd.MarkFlagRequired("slug")

	return cmd
}

// resolveTeamSlug calls GET /api/v1/teams?slug=<teamSlug> to find the team UUID.
// Returns the team ID or an error if the team cannot be found.
func resolveTeamSlug(client *http.Client, hubURL, apiKey, teamSlug string) (string, error) {
	req, err := http.NewRequest("GET", hubURL+"/api/v1/teams?slug="+teamSlug, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create team lookup request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to resolve team %q: %w", teamSlug, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("failed to resolve team %q: %v", teamSlug, ParseHTTPError(resp))
	}

	// Parse the response as a JSON array of team objects.
	var teams []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&teams); err != nil {
		return "", fmt.Errorf("failed to parse team lookup response: %w", err)
	}

	// Find the matching team by slug.
	for _, t := range teams {
		if t.Slug == teamSlug {
			return t.ID, nil
		}
	}

	return "", fmt.Errorf("team %q not found", teamSlug)
}

// newHTTPClient returns an *http.Client with a configurable timeout.
// The timeout is read from the AFC_HTTP_TIMEOUT environment variable
// (value in seconds). If unset or invalid, defaults to 30 seconds.
func newHTTPClient() *http.Client {
	timeout := 30 * time.Second
	if envTimeout := os.Getenv("AFC_HTTP_TIMEOUT"); envTimeout != "" {
		if secs, err := strconv.Atoi(envTimeout); err == nil && secs > 0 {
			timeout = time.Duration(secs) * time.Second
		}
	}
	return &http.Client{Timeout: timeout}
}
