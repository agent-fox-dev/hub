package cli

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

// WorkspaceCmd returns the 'workspace' parent cobra.Command with subcommands
// for create, list, get, update, archive, reactivate, and delete.
//
// The authenticated CLI client is retrieved from the Cobra context via
// apikit.CLIClientFromCmd — credentials are resolved by apikit's
// PersistentPreRunE from flags, environment variables, and the config file.
func WorkspaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "workspace",
		Short:         "Manage workspaces",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	cmd.AddCommand(
		newCreateCmd(),
		newListCmd(),
		newGetCmd(),
		newUpdateCmd(),
		newArchiveCmd(),
		newReactivateCmd(),
		newDeleteCmd(),
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
		WorkspaceCmd(),
		CredentialHelperCmd(),
		SecretsCmd(),
		VarsCmd(),
		MergeCmd(),
	)

	return root
}

// newCreateCmd returns the 'workspace create' subcommand.
func newCreateCmd() *cobra.Command {
	var (
		gitURL      string
		slug        string
		branch      string
		org         string
		displayName string
		description string
		syncMode    string
		gitPAT      string
		gitUsername  string
		gitPassword string
	)

	cmd := &cobra.Command{
		Use:           "create",
		Short:         "Create a new workspace",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if gitURL == "" {
				return apikit.CLIHandleError(cmd, apikit.NewCLIError(2, "--git-url flag is required"))
			}
			if slug == "" {
				return apikit.CLIHandleError(cmd, apikit.NewCLIError(2, "--slug flag is required"))
			}

			hasPAT := cmd.Flags().Changed("git-pat")
			hasUsername := cmd.Flags().Changed("git-username")
			hasPassword := cmd.Flags().Changed("git-password")
			hasAnyCred := hasPAT || hasUsername || hasPassword

			// Mutual exclusion: --git-pat and --git-username/--git-password
			// cannot be used together.
			if hasPAT && (hasUsername || hasPassword) {
				return apikit.CLIHandleError(cmd, apikit.NewCLIError(2,
					"--git-pat and --git-username/--git-password are mutually exclusive"))
			}

			// Pair completeness: --git-username and --git-password must both
			// be provided together.
			if hasUsername != hasPassword {
				return apikit.CLIHandleError(cmd, apikit.NewCLIError(2,
					"--git-username and --git-password must be provided together"))
			}

			// Empty value validation.
			if hasPAT && gitPAT == "" {
				return apikit.CLIHandleError(cmd, apikit.NewCLIError(2,
					"--git-pat value must not be empty"))
			}
			if hasUsername && gitUsername == "" {
				return apikit.CLIHandleError(cmd, apikit.NewCLIError(2,
					"--git-username value must not be empty"))
			}
			if hasPassword && gitPassword == "" {
				return apikit.CLIHandleError(cmd, apikit.NewCLIError(2,
					"--git-username and --git-password values must not be empty"))
			}

			// HTTPS requirement: credentials require an HTTPS git_url.
			if hasAnyCred && !strings.HasPrefix(gitURL, "https://") {
				return apikit.CLIHandleError(cmd, apikit.NewCLIError(2,
					"git credentials require an HTTPS git_url"))
			}

			// 13-REQ-2.E3: Validate --sync-mode flag before making any API call.
			if cmd.Flags().Changed("sync-mode") {
				validModes := map[string]bool{"pull_only": true, "disabled": true}
				if !validModes[syncMode] {
					return apikit.CLIHandleError(cmd, apikit.NewCLIError(2,
						"invalid --sync-mode value: must be 'pull_only' or 'disabled'"))
				}
			}

			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

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
			if cmd.Flags().Changed("sync-mode") {
				body["sync_mode"] = syncMode
			}

			if org != "" {
				body["org_id"] = org
			}

			// Add credential fields to request body.
			if hasPAT {
				body["git_pat"] = gitPAT
			}
			if hasUsername {
				body["git_username"] = gitUsername
			}
			if hasPassword {
				body["git_password"] = gitPassword
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodPost, "/workspaces", body)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			return apikit.CLIPrintResult(cmd, result)
		},
	}

	cmd.Flags().StringVar(&gitURL, "git-url", "", "Git repository URL (required)")
	cmd.Flags().StringVar(&slug, "slug", "", "Workspace slug (required)")
	cmd.Flags().StringVar(&branch, "branch", "", "Git branch (optional)")
	cmd.Flags().StringVar(&org, "org", "", "Organization slug (optional)")
	cmd.Flags().StringVar(&displayName, "display-name", "", "Workspace display name (optional)")
	cmd.Flags().StringVar(&description, "description", "", "Workspace description (optional)")
	cmd.Flags().StringVar(&syncMode, "sync-mode", "", "Sync mode: 'pull_only' (default) or 'disabled' (optional)")
	cmd.Flags().StringVar(&gitPAT, "git-pat", "", "Personal access token for private repo (optional)")
	cmd.Flags().StringVar(&gitUsername, "git-username", "", "Git username for private repo (optional)")
	cmd.Flags().StringVar(&gitPassword, "git-password", "", "Git password for private repo (optional)")

	return cmd
}

// newUpdateCmd returns the 'workspace update' subcommand.
// It sends a PATCH request to /api/v1/workspaces/:slug with only the
// explicitly provided fields in the body.
func newUpdateCmd() *cobra.Command {
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

			hasDisplayName := cmd.Flags().Changed("display-name")
			hasDescription := cmd.Flags().Changed("description")
			hasOrg := cmd.Flags().Changed("org")

			if !hasDisplayName && !hasDescription && !hasOrg &&
				!clearDisplayName && !clearDescription && !clearOrg {
				return apikit.CLIHandleError(cmd, apikit.NewCLIError(2, "at least one update flag must be provided (--display-name, --description, --org, --clear-display-name, --clear-description, --clear-org)"))
			}

			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()

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
				body["org_id"] = org
			}

			result, err := client.DoRequest(ctx, http.MethodPatch, "/workspaces/"+slug, body)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			m, ok := result.(map[string]any)
			if !ok {
				return apikit.CLIHandleError(cmd, apikit.NewCLIError(2, "unexpected response format"))
			}
			if _, ok := m["slug"]; !ok {
				return apikit.CLIHandleError(cmd, apikit.NewCLIError(2, "unexpected response format: missing required field 'slug'"))
			}

			return apikit.CLIPrintResult(cmd, result)
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
func newListCmd() *cobra.Command {
	var includeArchived bool

	cmd := &cobra.Command{
		Use:           "list",
		Short:         "List workspaces",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			path := "/workspaces"
			if includeArchived {
				path += "?include_archived=true"
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodGet, path, nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			return apikit.CLIPrintResult(cmd, result)
		},
	}

	cmd.Flags().BoolVar(&includeArchived, "include-archived", false, "Include archived workspaces")

	return cmd
}

// newGetCmd returns the 'workspace get' subcommand.
func newGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "get <slug>",
		Short:         "Get workspace details",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodGet, "/workspaces/"+args[0], nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			return apikit.CLIPrintResult(cmd, result)
		},
	}
}

// newArchiveCmd returns the 'workspace archive' subcommand.
func newArchiveCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "archive <slug>",
		Short:         "Archive a workspace",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodPost, "/workspaces/"+args[0]+"/archive", nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			return apikit.CLIPrintResult(cmd, result)
		},
	}
}

// newReactivateCmd returns the 'workspace reactivate' subcommand.
func newReactivateCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "reactivate <slug>",
		Short:         "Reactivate an archived workspace",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodPost, "/workspaces/"+args[0]+"/reactivate", nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			return apikit.CLIPrintResult(cmd, result)
		},
	}
}

// newDeleteCmd returns the 'workspace delete' subcommand.
func newDeleteCmd() *cobra.Command {
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
				return apikit.CLIHandleError(cmd, apikit.NewCLIError(2, "--confirm flag is required to permanently delete a workspace"))
			}

			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			_, err = client.DoRequest(cmd.Context(), http.MethodDelete, "/workspaces/"+slug, nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			fmt.Fprintf(cmd.ErrOrStderr(), "Workspace '%s' has been permanently deleted.\n", slug)
			return nil
		},
	}

	cmd.Flags().BoolVar(&confirm, "confirm", false, "Confirm permanent deletion")

	return cmd
}
