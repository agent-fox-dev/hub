package cli

import (
	"net/http"

	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

// RebuildCmd returns the 'rebuild' parent cobra.Command with subcommands
// for submit, list, and status of rebuild jobs.
//
// The authenticated CLI client is retrieved from the Cobra context via
// apikit.CLIClientFromCmd — credentials are resolved by apikit's
// PersistentPreRunE from flags, environment variables, and the config file.
func RebuildCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "rebuild",
		Short:         "Manage rebuild jobs",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	cmd.AddCommand(
		newRebuildSubmitCmd(),
		newRebuildListCmd(),
		newRebuildStatusCmd(),
	)

	return cmd
}

// newRebuildSubmitCmd returns the 'rebuild submit' subcommand.
// It sends POST /api/v1/workspaces/:slug/rebuild and prints the job record.
// Requirements: 16-REQ-7.1
func newRebuildSubmitCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "submit <workspace-slug>",
		Short:         "Submit a rebuild job",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodPost,
				"/workspaces/"+args[0]+"/rebuild", nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			return apikit.CLIPrintResult(cmd, result)
		},
	}
}

// newRebuildListCmd returns the 'rebuild list' subcommand.
// It sends GET /api/v1/workspaces/:slug/rebuilds and prints the list.
// Requirements: 16-REQ-7.2
func newRebuildListCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "list <workspace-slug>",
		Short:         "List rebuild jobs",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodGet,
				"/workspaces/"+args[0]+"/rebuilds", nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			return apikit.CLIPrintResult(cmd, result)
		},
	}
}

// newRebuildStatusCmd returns the 'rebuild status' subcommand.
// It sends GET /api/v1/workspaces/:slug/rebuilds/:id and prints job details.
// Requirements: 16-REQ-7.3
func newRebuildStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "status <workspace-slug> <rebuild-id>",
		Short:         "Get rebuild job status",
		Args:          cobra.ExactArgs(2),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodGet,
				"/workspaces/"+args[0]+"/rebuilds/"+args[1], nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			return apikit.CLIPrintResult(cmd, result)
		},
	}
}
