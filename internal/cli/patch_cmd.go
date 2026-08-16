package cli

import (
	"github.com/spf13/cobra"
)

// PatchCmd returns the 'patch' parent cobra.Command with subcommands
// for add, list, remove, reorder, and update.
//
// Requirements: 15-REQ-14
func PatchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "patch",
		Short:         "Manage patches",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	cmd.AddCommand(
		newPatchAddCmd(),
		newPatchListCmd(),
		newPatchRemoveCmd(),
		newPatchReorderCmd(),
		newPatchUpdateCmd(),
	)

	return cmd
}

// newPatchAddCmd returns the 'patch add' subcommand stub.
// Requirements: 15-REQ-14.1
func newPatchAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "add <workspace-slug>",
		Short:         "Add a patch to a workspace",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// TODO: implement in task group 11
			return nil
		},
	}

	cmd.Flags().String("branch", "", "Branch name for the patch (required)")
	cmd.Flags().Int("position", 0, "Position in the patch list (optional)")
	cmd.Flags().String("upstream-pr", "", "Upstream PR URL (optional)")
	cmd.Flags().String("description", "", "Patch description (optional)")

	return cmd
}

// newPatchListCmd returns the 'patch list' subcommand stub.
// Requirements: 15-REQ-14.2
func newPatchListCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "list <workspace-slug>",
		Short:         "List patches for a workspace",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// TODO: implement in task group 11
			return nil
		},
	}
}

// newPatchRemoveCmd returns the 'patch remove' subcommand stub.
// Requirements: 15-REQ-14.3
func newPatchRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "remove <workspace-slug> <patch-id>",
		Short:         "Remove a patch from a workspace",
		Args:          cobra.ExactArgs(2),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// TODO: implement in task group 11
			return nil
		},
	}
}

// newPatchReorderCmd returns the 'patch reorder' subcommand stub.
// Requirements: 15-REQ-14.4
func newPatchReorderCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "reorder <workspace-slug> <patch-id-1> [patch-id-2] ...",
		Short:         "Reorder patches for a workspace",
		Args:          cobra.MinimumNArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// TODO: implement in task group 11
			return nil
		},
	}
}

// newPatchUpdateCmd returns the 'patch update' subcommand stub.
// Requirements: 15-REQ-14.5
func newPatchUpdateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "update <workspace-slug> <patch-id>",
		Short:         "Update a patch",
		Args:          cobra.ExactArgs(2),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// TODO: implement in task group 11
			return nil
		},
	}

	cmd.Flags().String("status", "", "Patch status (active, merged_upstream, conflict, disabled)")
	cmd.Flags().String("description", "", "Patch description")
	cmd.Flags().String("upstream-pr", "", "Upstream PR URL")
	cmd.Flags().Int("position", 0, "New position")

	return cmd
}
