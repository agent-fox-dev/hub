package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var hubURL string

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

// resolveHubURL returns the hub URL from the --hub-url flag if set, else from
// the AF_HUB_URL environment variable. Returns an error if neither is set.
func resolveHubURL() (string, error) {
	if hubURL != "" {
		return hubURL, nil
	}
	if envURL := os.Getenv("AF_HUB_URL"); envURL != "" {
		return envURL, nil
	}
	return "", fmt.Errorf("hub URL is required: use --hub-url flag or set AF_HUB_URL environment variable")
}

// resolveAPIKey returns the API key from the --api-key flag if set, else from
// the AF_HUB_API_KEY environment variable. Returns an error if neither is set.
func resolveAPIKey(flagVal string) (string, error) {
	if flagVal != "" {
		return flagVal, nil
	}
	if envKey := os.Getenv("AF_HUB_API_KEY"); envKey != "" {
		return envKey, nil
	}
	return "", fmt.Errorf("API key is required: use --api-key flag or set AF_HUB_API_KEY environment variable")
}
