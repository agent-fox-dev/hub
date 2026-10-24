package cli

import (
	"net/http"

	"github.com/spf13/cobra"
)

// LabelCmd returns the 'label' parent cobra.Command, with the subcommand that makes sure a
// label exists in a workspace's repository on its forge.
//
// The authenticated CLI client is retrieved from the Cobra context via
// apikit.CLIClientFromCmd — credentials are resolved by apikit's PersistentPreRunE
// from flags, environment variables, and the config file.
func LabelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "label",
		Short:         "Manage the labels of a workspace's forge repository",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	cmd.AddCommand(newLabelCreateCmd())
	return cmd
}

// newLabelCreateCmd returns the 'label create' subcommand.
// It sends POST /api/v1/workspaces/:slug/labels with the name and the flags that are set,
// and prints the label the hub answers: the label as the forge holds it, 200, when it
// exists already, which the hub never changes, and the label it created, 201.
func newLabelCreateCmd() *cobra.Command {
	var color, description, target string
	cmd := &cobra.Command{
		Use:           "create <workspace-slug> <name>",
		Short:         "Make sure a label exists in a workspace's repository",
		Args:          cobra.ExactArgs(2),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[1] == "" {
				return usageError(cmd, "the label name must not be empty")
			}
			payload := map[string]any{"name": args[1]}
			if cmd.Flags().Changed("color") {
				payload["color"] = color
			}
			if cmd.Flags().Changed("description") {
				payload["description"] = description
			}
			return forgeSend(cmd, http.MethodPost, withTarget(cmd, apiPath("workspaces", args[0], "labels"), target), payload, nil)
		},
	}
	cmd.Flags().StringVar(&color, "color", "", "Color as six hex digits, with or without a leading # (default: ededed)")
	cmd.Flags().StringVar(&description, "description", "", "Description of the label")
	cmd.Flags().StringVar(&target, "target", "", targetWriteUsage)
	return cmd
}
