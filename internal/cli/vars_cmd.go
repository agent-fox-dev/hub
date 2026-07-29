package cli

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

// VarsCmd returns the 'vars' parent cobra.Command with subcommands
// for create, list, update, delete, and resolve.
func VarsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "vars",
		Short:         "Manage variables",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	cmd.AddCommand(
		newVarsCreateCmd(),
		newVarsListCmd(),
		newVarsUpdateCmd(),
		newVarsDeleteCmd(),
		newVarsResolveCmd(),
	)

	return cmd
}

// newVarsCreateCmd returns the 'vars create' subcommand.
func newVarsCreateCmd() *cobra.Command {
	var (
		userFlag      bool
		orgFlag       string
		workspaceFlag string
	)

	cmd := &cobra.Command{
		Use:           "create <KEY=VALUE[,KEY2=VALUE2,...]>",
		Short:         "Create variables",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return apikit.CLIHandleError(cmd, apikit.NewCLIError(2, "required argument: KEY=VALUE"))
			}

			entries, err := parseKeyValueList(args[0])
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			// Build the entries body.
			body := map[string]any{
				"entries": kvEntriesToMaps(entries),
			}

			scopes := resolveScope(userFlag, orgFlag, workspaceFlag)
			hadError := false

			for _, scope := range scopes {
				result, err := client.DoRequest(cmd.Context(), http.MethodPost, scope.PathPrefix+"/vars", body)
				if err != nil {
					apikit.CLIHandleError(cmd, err) //nolint:errcheck
					hadError = true
					continue
				}
				apikit.CLIPrintResult(cmd, result) //nolint:errcheck
			}

			if hadError {
				return apikit.NewCLIError(1, "")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&userFlag, "user", false, "Target user scope")
	cmd.Flags().StringVar(&orgFlag, "org", "", "Target organization scope (by slug)")
	cmd.Flags().StringVar(&workspaceFlag, "workspace", "", "Target workspace scope (by slug)")

	return cmd
}

// newVarsListCmd returns the 'vars list' subcommand.
func newVarsListCmd() *cobra.Command {
	var (
		userFlag      bool
		orgFlag       string
		workspaceFlag string
	)

	cmd := &cobra.Command{
		Use:           "list",
		Short:         "List variables",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			scope, err := singleScope(userFlag, orgFlag, workspaceFlag)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodGet, scope.PathPrefix+"/vars", nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			return apikit.CLIPrintResult(cmd, result)
		},
	}

	cmd.Flags().BoolVar(&userFlag, "user", false, "Target user scope")
	cmd.Flags().StringVar(&orgFlag, "org", "", "Target organization scope (by slug)")
	cmd.Flags().StringVar(&workspaceFlag, "workspace", "", "Target workspace scope (by slug)")

	return cmd
}

// newVarsUpdateCmd returns the 'vars update' subcommand.
func newVarsUpdateCmd() *cobra.Command {
	var (
		userFlag      bool
		orgFlag       string
		workspaceFlag string
	)

	cmd := &cobra.Command{
		Use:           "update <KEY=VALUE>",
		Short:         "Update a variable",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return apikit.CLIHandleError(cmd, apikit.NewCLIError(2, "required argument: KEY=VALUE"))
			}

			key, value, err := parseKeyValue(args[0])
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			scope, err := singleScope(userFlag, orgFlag, workspaceFlag)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			body := map[string]any{"value": value}
			result, err := client.DoRequest(cmd.Context(), http.MethodPatch, scope.PathPrefix+"/vars/"+key, body)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			return apikit.CLIPrintResult(cmd, result)
		},
	}

	cmd.Flags().BoolVar(&userFlag, "user", false, "Target user scope")
	cmd.Flags().StringVar(&orgFlag, "org", "", "Target organization scope (by slug)")
	cmd.Flags().StringVar(&workspaceFlag, "workspace", "", "Target workspace scope (by slug)")

	return cmd
}

// newVarsDeleteCmd returns the 'vars delete' subcommand.
func newVarsDeleteCmd() *cobra.Command {
	var (
		userFlag      bool
		orgFlag       string
		workspaceFlag string
	)

	cmd := &cobra.Command{
		Use:           "delete <KEY>",
		Short:         "Delete a variable",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return apikit.CLIHandleError(cmd, apikit.NewCLIError(2, "required argument: KEY"))
			}

			key := args[0]

			scope, err := singleScope(userFlag, orgFlag, workspaceFlag)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			_, err = client.DoRequest(cmd.Context(), http.MethodDelete, scope.PathPrefix+"/vars/"+key, nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			fmt.Fprintf(cmd.ErrOrStderr(), "Variable '%s' has been deleted.\n", key)
			return nil
		},
	}

	cmd.Flags().BoolVar(&userFlag, "user", false, "Target user scope")
	cmd.Flags().StringVar(&orgFlag, "org", "", "Target organization scope (by slug)")
	cmd.Flags().StringVar(&workspaceFlag, "workspace", "", "Target workspace scope (by slug)")

	return cmd
}

// newVarsResolveCmd returns the 'vars resolve' subcommand.
// This command does NOT define ownership flags; it always targets a workspace
// by its positional slug argument.
func newVarsResolveCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "resolve <workspace-slug>",
		Short:         "Resolve variables for a workspace",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return apikit.CLIHandleError(cmd, apikit.NewCLIError(2, "required argument: workspace slug"))
			}

			slug := args[0]

			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodGet, "/workspaces/"+slug+"/vars/resolved", nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			return apikit.CLIPrintResult(cmd, result)
		},
	}
}
