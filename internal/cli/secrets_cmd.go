package cli

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

// SecretsCmd returns the 'secrets' parent cobra.Command with subcommands
// for create, list, update, and delete.
func SecretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "secrets",
		Short:         "Manage secrets",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	cmd.AddCommand(
		newSecretsCreateCmd(),
		newSecretsListCmd(),
		newSecretsUpdateCmd(),
		newSecretsDeleteCmd(),
	)

	return cmd
}

// newSecretsCreateCmd returns the 'secrets create' subcommand.
func newSecretsCreateCmd() *cobra.Command {
	var (
		userFlag      bool
		orgFlag       string
		workspaceFlag string
	)

	cmd := &cobra.Command{
		Use:           "create <KEY=VALUE[,KEY2=VALUE2,...]>",
		Short:         "Create secrets",
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
				result, err := client.DoRequest(cmd.Context(), http.MethodPost, scope.PathPrefix+"/secrets", body)
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

// newSecretsListCmd returns the 'secrets list' subcommand.
func newSecretsListCmd() *cobra.Command {
	var (
		userFlag      bool
		orgFlag       string
		workspaceFlag string
	)

	cmd := &cobra.Command{
		Use:           "list",
		Short:         "List secrets",
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

			result, err := client.DoRequest(cmd.Context(), http.MethodGet, scope.PathPrefix+"/secrets", nil)
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

// newSecretsUpdateCmd returns the 'secrets update' subcommand.
func newSecretsUpdateCmd() *cobra.Command {
	var (
		userFlag      bool
		orgFlag       string
		workspaceFlag string
	)

	cmd := &cobra.Command{
		Use:           "update <KEY=VALUE>",
		Short:         "Update a secret",
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
			result, err := client.DoRequest(cmd.Context(), http.MethodPatch, scope.PathPrefix+"/secrets/"+key, body)
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

// newSecretsDeleteCmd returns the 'secrets delete' subcommand.
func newSecretsDeleteCmd() *cobra.Command {
	var (
		userFlag      bool
		orgFlag       string
		workspaceFlag string
	)

	cmd := &cobra.Command{
		Use:           "delete <KEY>",
		Short:         "Delete a secret",
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

			_, err = client.DoRequest(cmd.Context(), http.MethodDelete, scope.PathPrefix+"/secrets/"+key, nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			fmt.Fprintf(cmd.ErrOrStderr(), "Secret '%s' has been deleted.\n", key)
			return nil
		},
	}

	cmd.Flags().BoolVar(&userFlag, "user", false, "Target user scope")
	cmd.Flags().StringVar(&orgFlag, "org", "", "Target organization scope (by slug)")
	cmd.Flags().StringVar(&workspaceFlag, "workspace", "", "Target workspace scope (by slug)")

	return cmd
}

// kvEntriesToMaps converts a slice of kvEntry to a slice of maps for JSON
// serialization in the request body.
func kvEntriesToMaps(entries []kvEntry) []map[string]string {
	result := make([]map[string]string, len(entries))
	for i, e := range entries {
		result[i] = map[string]string{
			"key":   e.Key,
			"value": e.Value,
		}
	}
	return result
}
