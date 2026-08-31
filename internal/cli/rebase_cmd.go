package cli

import (
	"net/http"
	"strings"

	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

// RebaseCmd returns the 'rebase' parent cobra.Command with subcommands
// for batch rebase operations.
//
// The authenticated CLI client is retrieved from the Cobra context via
// apikit.CLIClientFromCmd — credentials are resolved by apikit's
// PersistentPreRunE from flags, environment variables, and the config file.
func RebaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "rebase",
		Short:         "Manage rebase operations",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	cmd.AddCommand(
		newRebaseSubmitCmd(),
	)

	return cmd
}

// newRebaseSubmitCmd returns the 'rebase submit' subcommand.
// It sends POST /api/v1/workspaces/:slug/rebase with the target ref and
// branches specified via --target-ref and --branches flags.
func newRebaseSubmitCmd() *cobra.Command {
	var (
		targetRef string
		branches  string
	)

	cmd := &cobra.Command{
		Use:           "submit <workspace-slug>",
		Short:         "Submit a batch rebase",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := args[0]

			if targetRef == "" {
				return apikit.CLIHandleError(cmd, apikit.NewCLIError(2, "--target-ref flag is required"))
			}
			if branches == "" {
				return apikit.CLIHandleError(cmd, apikit.NewCLIError(2, "--branches flag is required"))
			}

			branchList := strings.Split(branches, ",")

			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			body := map[string]any{
				"target_ref": targetRef,
				"branches":   branchList,
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodPost, "/workspaces/"+slug+"/rebase", body)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			return apikit.CLIPrintResult(cmd, result)
		},
	}

	cmd.Flags().StringVar(&targetRef, "target-ref", "", "Target ref to rebase onto (required)")
	cmd.Flags().StringVar(&branches, "branches", "", "Comma-separated list of branches to rebase (required)")

	return cmd
}
