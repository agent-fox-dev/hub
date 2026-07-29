package cli

import (
	"github.com/spf13/cobra"
)

// VarsCmd returns the 'vars' parent cobra.Command with subcommands
// for create, list, update, delete, and resolve.
//
// Stub: subcommands have no RunE — implementation is added in a later task group.
func VarsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "vars",
		Short:         "Manage variables",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	cmd.AddCommand(
		&cobra.Command{Use: "create", Short: "Create variables", SilenceErrors: true, SilenceUsage: true},
		&cobra.Command{Use: "list", Short: "List variables", SilenceErrors: true, SilenceUsage: true},
		&cobra.Command{Use: "update", Short: "Update a variable", SilenceErrors: true, SilenceUsage: true},
		&cobra.Command{Use: "delete", Short: "Delete a variable", SilenceErrors: true, SilenceUsage: true},
		&cobra.Command{Use: "resolve", Short: "Resolve variables for a workspace", SilenceErrors: true, SilenceUsage: true},
	)

	return cmd
}
