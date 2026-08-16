package cli

import (
	"github.com/spf13/cobra"
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
// Stub: implementation in later task groups.
func newRerereListCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "list <workspace-slug>",
		Short:         "List recorded rerere resolutions",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Stub: to be implemented in task group 8.
			return nil
		},
	}
}

// newRerereForgetCmd returns the 'rerere forget' subcommand.
// It sends DELETE /api/v1/workspaces/:slug/rerere/*pathspec and prints
// confirmation.
// Stub: implementation in later task groups.
func newRerereForgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "forget <workspace-slug> <pathspec>",
		Short:         "Forget a recorded rerere resolution",
		Args:          cobra.ExactArgs(2),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Stub: to be implemented in task group 8.
			return nil
		},
	}
}
