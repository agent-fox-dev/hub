package cli

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

// RerereCmd returns the 'rerere' parent cobra.Command with subcommands
// for list and forget rerere resolutions.
//
// The authenticated CLI client is retrieved from the Cobra context via
// apikit.CLIClientFromCmd — credentials are resolved by apikit's
// PersistentPreRunE from flags, environment variables, and the config file.
func RerereCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "rerere",
		Short:         "Manage rerere resolutions",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	cmd.AddCommand(
		newRerereListCmd(),
		newRerereForgetCmd(),
	)

	return cmd
}

// newRerereListCmd returns the 'rerere list' subcommand.
// It sends GET /api/v1/workspaces/:slug/rerere and prints the list of
// recorded resolutions.
// Requirements: 16-REQ-8.1
func newRerereListCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "list <workspace-slug>",
		Short:         "List recorded rerere resolutions",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodGet,
				"/workspaces/"+args[0]+"/rerere", nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			return apikit.CLIPrintResult(cmd, result)
		},
	}
}

// newRerereForgetCmd returns the 'rerere forget' subcommand.
// It sends DELETE /api/v1/workspaces/:slug/rerere/<pathspec> and prints
// confirmation.
// 16-REQ-8.E1: The pathspec is appended as a path segment after /rerere/
// without additional URL encoding of slashes.
// Requirements: 16-REQ-8.2
func newRerereForgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "forget <workspace-slug> <pathspec>",
		Short:         "Forget a recorded rerere resolution",
		Args:          cobra.ExactArgs(2),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			// 16-REQ-8.E1: append pathspec as path segment without encoding slashes.
			path := "/workspaces/" + args[0] + "/rerere/" + args[1]

			_, err = client.DoRequest(cmd.Context(), http.MethodDelete, path, nil)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			// Emit synthetic JSON status object to stdout (server returns 204 No Content).
			synthetic := map[string]string{
				"status":   "forgotten",
				"pathspec": args[1],
			}
			data, _ := json.MarshalIndent(synthetic, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(data))

			// Human-readable confirmation to stderr only.
			fmt.Fprintf(cmd.ErrOrStderr(), "Rerere resolution for '%s' has been forgotten.\n", args[1])
			return nil
		},
	}
}
