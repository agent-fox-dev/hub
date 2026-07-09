package cli

import (
	"fmt"
	"os"

	"github.com/agent-fox/af-hub/internal/cliconfig"
	"github.com/spf13/cobra"
)

var hubURL string

// loadedConfig holds the parsed client configuration loaded during
// PersistentPreRunE. It is nil until the root command's pre-run executes.
var loadedConfig *cliconfig.Config

// NewRootCmd creates and returns the root afc command.
func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "afc",
		Short: "afc is the command-line client for af-hub",
		Long:  "afc is the CLI client for interacting with af-hub. It supports OAuth login and API key management.",
		// SilenceUsage prevents usage from being printed on every error.
		SilenceUsage: true,
		// SilenceErrors prevents Cobra's own error printing so we control stderr output.
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("failed to determine home directory: %w", err)
			}

			// Ensure the config directory and file exist.
			if err := cliconfig.EnsureConfigExists(homeDir); err != nil {
				return err
			}

			// Load the config file.
			cfg, err := cliconfig.LoadConfig(homeDir)
			if err != nil {
				return err
			}

			loadedConfig = cfg
			return nil
		},
	}

	// Register --hub-url as a persistent flag available on all subcommands.
	rootCmd.PersistentFlags().StringVar(&hubURL, "hub-url", "", "Hub server base URL (overrides AF_HUB_URL)")

	// Register subcommands.
	rootCmd.AddCommand(newLoginCmd())
	rootCmd.AddCommand(newKeysCmd())

	return rootCmd
}

// Execute runs the root command. Called from main().
func Execute() {
	rootCmd := NewRootCmd()
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error: "+err.Error())
		os.Exit(1)
	}
}

// resolveHubURL returns the hub URL using the precedence chain:
// --hub-url flag > AF_HUB_URL env var > config file hub_url > error.
func resolveHubURL() (string, error) {
	cfg := loadedConfig
	if cfg == nil {
		cfg = &cliconfig.Config{}
	}
	return cliconfig.ResolveHubURL(hubURL, os.Getenv("AF_HUB_URL"), cfg)
}

// resolveAPIKey returns the API key token using the precedence chain:
// --api-key flag > AF_HUB_API_KEY env var > config file keys lookup > error.
func resolveAPIKey(flagVal string) (string, error) {
	cfg := loadedConfig
	if cfg == nil {
		cfg = &cliconfig.Config{}
	}
	return cliconfig.ResolveAPIKey(flagVal, os.Getenv("AF_HUB_API_KEY"), cfg)
}
