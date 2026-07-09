package cli

import (
	"fmt"
	"os"

	"github.com/agent-fox/af-hub/internal/cliconfig"
	"github.com/spf13/cobra"
)

// newKeysDefaultCmd creates the "keys default" subcommand.
// It sets the active workspace slug (api_key) in the config file.
func newKeysDefaultCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "default <workspace-slug>",
		Short: "Set the default API key workspace",
		Long:  "Set which stored API key is used by default by specifying its workspace slug.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("Usage: afc keys default <workspace-slug>\n\nMissing required argument: <workspace-slug>")
			}
			if len(args) > 1 {
				return fmt.Errorf("accepts 1 argument, received %d", len(args))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			workspaceSlug := args[0]

			homeDir, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("failed to determine home directory: %w", err)
			}

			if loadedConfig == nil {
				return fmt.Errorf("config not loaded")
			}

			if err := cliconfig.SetDefaultKey(homeDir, loadedConfig, workspaceSlug); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Default API key set to %s\n", workspaceSlug)
			return nil
		},
	}

	return cmd
}
