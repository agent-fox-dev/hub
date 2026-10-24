package cli

import (
	"net/http"

	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

// PatchStatusCmd returns the 'workspace patch-status' subcommand, which the
// hub adds under its workspace command.
// It sends GET /api/v1/workspaces/:slug/patch-status and prints the
// full status dashboard.
func PatchStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "patch-status <workspace-slug>",
		Short:         "View carry-patch status dashboard",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodGet,
				apiPath("workspaces", args[0], "patch-status"), nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			return apikit.CLIPrintResult(cmd, result)
		},
	}
}
