package cli

import (
	"github.com/spf13/cobra"
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
// Stub: implementation in later task groups.
func newRebuildSubmitCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "submit <workspace-slug>",
		Short:         "Submit a rebuild job",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Stub: to be implemented in task group 8.
			return nil
		},
	}
}

// newRebuildListCmd returns the 'rebuild list' subcommand.
// It sends GET /api/v1/workspaces/:slug/rebuilds and prints the list.
// Stub: implementation in later task groups.
func newRebuildListCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "list <workspace-slug>",
		Short:         "List rebuild jobs",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Stub: to be implemented in task group 8.
			return nil
		},
	}
}

// newRebuildStatusCmd returns the 'rebuild status' subcommand.
// It sends GET /api/v1/workspaces/:slug/rebuilds/:id and prints job details.
// Stub: implementation in later task groups.
func newRebuildStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "status <workspace-slug> <rebuild-id>",
		Short:         "Get rebuild job status",
		Args:          cobra.ExactArgs(2),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Stub: to be implemented in task group 8.
			return nil
		},
	}
}
