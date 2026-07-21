package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

// wsClient is a lightweight HTTP client for workspace API calls.
// It wraps a base URL and API key for authenticated requests.
type wsClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// doRequest performs an authenticated HTTP request and returns the raw
// response body and status code. The path is appended to baseURL as-is.
func (c *wsClient) doRequest(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	fullURL := strings.TrimRight(c.baseURL, "/") + path

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("failed to marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	httpClient := c.httpClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read response: %w", err)
	}

	return respBody, resp.StatusCode, nil
}

// doAPI performs an authenticated workspace API request (under /api/v1)
// and returns the raw response body. On 4xx/5xx, it decodes the error
// envelope and returns a descriptive error.
func (c *wsClient) doAPI(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	respBody, status, err := c.doRequest(ctx, method, "/api/v1"+path, body)
	if err != nil {
		return nil, 0, err
	}

	if status >= 400 {
		// Try to extract error message from the server's error envelope.
		var errEnv struct {
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(respBody, &errEnv) == nil && errEnv.Error.Message != "" {
			return nil, status, fmt.Errorf("%s", errEnv.Error.Message)
		}
		return nil, status, fmt.Errorf("HTTP %d: %s", status, http.StatusText(status))
	}

	return respBody, status, nil
}

// WorkspaceCmd returns the 'workspace' parent cobra.Command with subcommands
// for create, list, get, archive, reactivate, and delete.
//
// baseURL is the hub API base URL (e.g. "http://localhost:8080").
// apiKey is the authentication credential for API calls.
func WorkspaceCmd(baseURL, apiKey string) *cobra.Command {
	client := &wsClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}

	cmd := &cobra.Command{
		Use:           "workspace",
		Short:         "Manage workspaces",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	cmd.AddCommand(
		newCreateCmd(client),
		newListCmd(client),
		newGetCmd(client),
		newUpdateCmd(client),
		newArchiveCmd(client),
		newReactivateCmd(client),
		newDeleteCmd(client),
	)

	return cmd
}

// BuildRootCommand creates the full afc CLI command tree by calling
// apikit.RootCommand() and adding workspace subcommands alongside the
// standard apikit commands (login, user, keys, tokens, orgs, admin).
func BuildRootCommand() *cobra.Command {
	root := apikit.RootCommand()
	root.Use = "afc"
	root.Short = "agent-fox CLI"

	root.AddCommand(
		apikit.LoginCmd(),
		apikit.UserCmd(),
		apikit.KeysCmd(),
		apikit.TokensCmd(),
		apikit.OrgsCmd(),
		apikit.AdminCmd(),
		WorkspaceCmd("", ""),
	)

	return root
}

// newCreateCmd returns the 'workspace create' subcommand.
func newCreateCmd(client *wsClient) *cobra.Command {
	var (
		gitURL      string
		slug        string
		branch      string
		org         string
		displayName string
		description string
	)

	cmd := &cobra.Command{
		Use:           "create",
		Short:         "Create a new workspace",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate required flags.
			if gitURL == "" {
				fmt.Fprintln(cmd.ErrOrStderr(), "Error: --git-url flag is required")
				return fmt.Errorf("--git-url flag is required")
			}
			if slug == "" {
				fmt.Fprintln(cmd.ErrOrStderr(), "Error: --slug flag is required")
				return fmt.Errorf("--slug flag is required")
			}

			// Build request body.
			body := map[string]any{
				"slug":    slug,
				"git_url": gitURL,
			}
			if branch != "" {
				body["branch"] = branch
			}
			if cmd.Flags().Changed("display-name") {
				body["display_name"] = displayName
			}
			if cmd.Flags().Changed("description") {
				body["description"] = description
			}

			// Resolve org slug if provided.
			if org != "" {
				orgID, err := resolveOrgSlug(cmd.Context(), client, org)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Error: %v\n", err)
					return err
				}
				body["org_id"] = orgID
			}

			// Create workspace.
			respBody, _, err := client.doAPI(cmd.Context(), http.MethodPost, "/workspaces", body)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: %v\n", err)
				return err
			}

			fmt.Fprint(cmd.OutOrStdout(), string(respBody))
			return nil
		},
	}

	cmd.Flags().StringVar(&gitURL, "git-url", "", "Git repository URL (required)")
	cmd.Flags().StringVar(&slug, "slug", "", "Workspace slug (required)")
	cmd.Flags().StringVar(&branch, "branch", "", "Git branch (optional)")
	cmd.Flags().StringVar(&org, "org", "", "Organization slug (optional)")
	cmd.Flags().StringVar(&displayName, "display-name", "", "Workspace display name (optional)")
	cmd.Flags().StringVar(&description, "description", "", "Workspace description (optional)")

	return cmd
}

// newUpdateCmd returns the 'workspace update' subcommand.
// It sends a PATCH request to /api/v1/workspaces/:slug with only the
// explicitly provided fields in the body.
func newUpdateCmd(client *wsClient) *cobra.Command {
	var (
		displayName      string
		description      string
		org              string
		clearDisplayName bool
		clearDescription bool
		clearOrg         bool
	)

	cmd := &cobra.Command{
		Use:           "update <slug>",
		Short:         "Update workspace properties",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := args[0]

			// Check if at least one update flag was provided.
			hasDisplayName := cmd.Flags().Changed("display-name")
			hasDescription := cmd.Flags().Changed("description")
			hasOrg := cmd.Flags().Changed("org")

			if !hasDisplayName && !hasDescription && !hasOrg &&
				!clearDisplayName && !clearDescription && !clearOrg {
				fmt.Fprintln(cmd.ErrOrStderr(), "Error: at least one update flag must be provided (--display-name, --description, --org, --clear-display-name, --clear-description, --clear-org)")
				return fmt.Errorf("at least one update flag is required")
			}

			// Use a context with timeout to avoid hanging indefinitely.
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()

			// Build PATCH body with only explicitly-set fields.
			// --clear-* flags send null; value flags send the provided string.
			body := make(map[string]any)

			if clearDisplayName {
				body["display_name"] = nil
			} else if hasDisplayName {
				body["display_name"] = displayName
			}

			if clearDescription {
				body["description"] = nil
			} else if hasDescription {
				body["description"] = description
			}

			if clearOrg {
				body["org_id"] = nil
			} else if hasOrg {
				orgID, err := resolveOrgSlug(ctx, client, org)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Error: %v\n", err)
					return err
				}
				body["org_id"] = orgID
			}

			// Send PATCH request.
			respBody, _, err := client.doAPI(ctx, http.MethodPatch, "/workspaces/"+slug, body)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: %v\n", err)
				return err
			}

			// Validate response is parseable JSON with expected shape.
			var ws map[string]any
			if err := json.Unmarshal(respBody, &ws); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: unexpected response format: %v\n", err)
				return fmt.Errorf("unexpected response format: %w", err)
			}
			if _, ok := ws["slug"]; !ok {
				fmt.Fprintln(cmd.ErrOrStderr(), "Error: unexpected response format: missing required field 'slug'")
				return fmt.Errorf("unexpected response format: missing required field 'slug'")
			}

			fmt.Fprint(cmd.OutOrStdout(), string(respBody))
			return nil
		},
	}

	cmd.Flags().StringVar(&displayName, "display-name", "", "Set workspace display name")
	cmd.Flags().StringVar(&description, "description", "", "Set workspace description")
	cmd.Flags().StringVar(&org, "org", "", "Set organization (by slug)")
	cmd.Flags().BoolVar(&clearDisplayName, "clear-display-name", false, "Clear display name to default (slug)")
	cmd.Flags().BoolVar(&clearDescription, "clear-description", false, "Clear description to default (empty)")
	cmd.Flags().BoolVar(&clearOrg, "clear-org", false, "Remove organization association")

	return cmd
}

// newListCmd returns the 'workspace list' subcommand.
func newListCmd(client *wsClient) *cobra.Command {
	var includeArchived bool

	cmd := &cobra.Command{
		Use:           "list",
		Short:         "List workspaces",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "/workspaces"
			if includeArchived {
				path += "?include_archived=true"
			}

			respBody, _, err := client.doAPI(cmd.Context(), http.MethodGet, path, nil)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: %v\n", err)
				return err
			}

			fmt.Fprint(cmd.OutOrStdout(), string(respBody))
			return nil
		},
	}

	cmd.Flags().BoolVar(&includeArchived, "include-archived", false, "Include archived workspaces")

	return cmd
}

// newGetCmd returns the 'workspace get' subcommand.
func newGetCmd(client *wsClient) *cobra.Command {
	return &cobra.Command{
		Use:           "get <slug>",
		Short:         "Get workspace details",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := args[0]

			respBody, _, err := client.doAPI(cmd.Context(), http.MethodGet, "/workspaces/"+slug, nil)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: %v\n", err)
				return err
			}

			fmt.Fprint(cmd.OutOrStdout(), string(respBody))
			return nil
		},
	}
}

// newArchiveCmd returns the 'workspace archive' subcommand.
func newArchiveCmd(client *wsClient) *cobra.Command {
	return &cobra.Command{
		Use:           "archive <slug>",
		Short:         "Archive a workspace",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := args[0]

			respBody, _, err := client.doAPI(cmd.Context(), http.MethodPost, "/workspaces/"+slug+"/archive", nil)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: %v\n", err)
				return err
			}

			fmt.Fprint(cmd.OutOrStdout(), string(respBody))
			return nil
		},
	}
}

// newReactivateCmd returns the 'workspace reactivate' subcommand.
func newReactivateCmd(client *wsClient) *cobra.Command {
	return &cobra.Command{
		Use:           "reactivate <slug>",
		Short:         "Reactivate an archived workspace",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := args[0]

			respBody, _, err := client.doAPI(cmd.Context(), http.MethodPost, "/workspaces/"+slug+"/reactivate", nil)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: %v\n", err)
				return err
			}

			fmt.Fprint(cmd.OutOrStdout(), string(respBody))
			return nil
		},
	}
}

// newDeleteCmd returns the 'workspace delete' subcommand.
func newDeleteCmd(client *wsClient) *cobra.Command {
	var confirm bool

	cmd := &cobra.Command{
		Use:           "delete <slug>",
		Short:         "Permanently delete an archived workspace",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := args[0]

			if !confirm {
				fmt.Fprintln(cmd.ErrOrStderr(), "Error: --confirm flag is required to permanently delete a workspace")
				return fmt.Errorf("--confirm flag is required")
			}

			_, _, err := client.doAPI(cmd.Context(), http.MethodDelete, "/workspaces/"+slug, nil)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: %v\n", err)
				return err
			}

			fmt.Fprintf(cmd.ErrOrStderr(), "Workspace '%s' has been permanently deleted.\n", slug)
			return nil
		},
	}

	cmd.Flags().BoolVar(&confirm, "confirm", false, "Confirm permanent deletion")

	return cmd
}

// Ensure apikit is used (for go vet / import validation).
var _ = apikit.RootCommand
