package cli

import (
	"github.com/spf13/cobra"
)

// SecretsCmd returns the 'secrets' parent cobra.Command with subcommands
// for create, list, update, and delete.
//
// Stub: subcommands have no RunE — implementation is added in a later task group.
func SecretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "secrets",
		Short:         "Manage secrets",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	cmd.AddCommand(
		&cobra.Command{Use: "create", Short: "Create secrets", SilenceErrors: true, SilenceUsage: true},
		&cobra.Command{Use: "list", Short: "List secrets", SilenceErrors: true, SilenceUsage: true},
		&cobra.Command{Use: "update", Short: "Update a secret", SilenceErrors: true, SilenceUsage: true},
		&cobra.Command{Use: "delete", Short: "Delete a secret", SilenceErrors: true, SilenceUsage: true},
	)

	return cmd
}
