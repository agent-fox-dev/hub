package cli

import (
	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

// MergeCmd returns the 'merge' parent cobra.Command with subcommands
// for submit, list, status, and cancel merge jobs.
//
// The authenticated CLI client is retrieved from the Cobra context via
// apikit.CLIClientFromCmd — credentials are resolved by apikit's
// PersistentPreRunE from flags, environment variables, and the config file.
func MergeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "merge",
		Short:         "Manage merge jobs",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	cmd.AddCommand(
		newMergeSubmitCmd(),
		newMergeListCmd(),
		newMergeStatusCmd(),
		newMergeCancelCmd(),
	)

	return cmd
}

// newMergeSubmitCmd returns the 'merge submit' subcommand.
// It sends POST /api/v1/workspaces/:slug/merges with the target and source
// branches specified via --target and --source flags.
func newMergeSubmitCmd() *cobra.Command {
	var (
		target string
		source string
	)

	cmd := &cobra.Command{
		Use:           "submit <workspace-slug>",
		Short:         "Submit a merge job",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if target == "" {
				return apikit.CLIHandleError(cmd, apikit.NewCLIError(2, "--target flag is required"))
			}
			if source == "" {
				return apikit.CLIHandleError(cmd, apikit.NewCLIError(2, "--source flag is required"))
			}

			// Stub: implementation will be added in task group 15.
			// Will call POST /api/v1/workspaces/:slug/merges with
			// {target_branch, source_ref} body and print the result.
			return apikit.CLIHandleError(cmd, apikit.NewCLIError(1, "not implemented"))
		},
	}

	cmd.Flags().StringVar(&target, "target", "", "Target branch (required)")
	cmd.Flags().StringVar(&source, "source", "", "Source branch (required)")

	return cmd
}

// newMergeListCmd returns the 'merge list' subcommand.
// It sends GET /api/v1/workspaces/:slug/merges and prints the list of merge jobs.
func newMergeListCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "list <workspace-slug>",
		Short:         "List merge jobs",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Stub: implementation will be added in task group 15.
			// Will call GET /api/v1/workspaces/:slug/merges and print the result.
			return apikit.CLIHandleError(cmd, apikit.NewCLIError(1, "not implemented"))
		},
	}
}

// newMergeStatusCmd returns the 'merge status' subcommand.
// It sends GET /api/v1/workspaces/:slug/merges/:id and prints the merge job status.
func newMergeStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "status <workspace-slug> <merge-id>",
		Short:         "Get merge job status",
		Args:          cobra.ExactArgs(2),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Stub: implementation will be added in task group 15.
			// Will call GET /api/v1/workspaces/:slug/merges/:id and print the result.
			return apikit.CLIHandleError(cmd, apikit.NewCLIError(1, "not implemented"))
		},
	}
}

// newMergeCancelCmd returns the 'merge cancel' subcommand.
// It sends DELETE /api/v1/workspaces/:slug/merges/:id and prints confirmation.
func newMergeCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "cancel <workspace-slug> <merge-id>",
		Short:         "Cancel a merge job",
		Args:          cobra.ExactArgs(2),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Stub: implementation will be added in task group 15.
			// Will call DELETE /api/v1/workspaces/:slug/merges/:id and print confirmation.
			return apikit.CLIHandleError(cmd, apikit.NewCLIError(1, "not implemented"))
		},
	}
}
