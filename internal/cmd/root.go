// Package cmd defines the Cobra command tree for the afc CLI.
package cmd

import (
	"fmt"
	"os"

	"github.com/agent-fox-dev/hub/internal/config"
	"github.com/agent-fox-dev/hub/internal/validate"
	"github.com/spf13/cobra"
)

// NewRootCmd creates and returns the root Cobra command for the afc CLI.
// The version parameter is injected at build time via -ldflags.
func NewRootCmd(version string) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "afc",
		Short: "af-hub CLI client",
		Long:  "afc is the CLI client for the af-hub platform.",
		// When invoked with no subcommand, Cobra prints help (default behavior).
	}
	rootCmd.Version = version
	rootCmd.SetVersionTemplate("afc version {{.Version}}\n")

	// Register persistent flags for config override.
	rootCmd.PersistentFlags().String("hub-url", "", "Hub server URL (overrides AF_HUB_URL and config file)")
	rootCmd.PersistentFlags().String("user-id", "", "User ID (overrides AF_HUB_USER_ID and config file)")
	rootCmd.PersistentFlags().String("api-key", "", "API key (overrides AF_HUB_API_KEY and config file)")

	// PersistentPreRunE: ensure config directory and file exist, load config.
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to determine home directory: %w", err)
		}

		if err := config.EnsureConfigDir(home); err != nil {
			return err
		}

		cfgPath := config.ConfigPath(home)
		if err := config.EnsureConfigFile(cfgPath); err != nil {
			return err
		}

		_, err = config.Load(cfgPath)
		if err != nil {
			return err
		}

		return nil
	}

	// Register subcommands.
	rootCmd.AddCommand(newLoginCmd())
	rootCmd.AddCommand(newKeysCmd())
	rootCmd.AddCommand(newWorkspaceCmd())

	return rootCmd
}

// newLoginCmd creates the login subcommand.
func newLoginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with the hub using OAuth",
		RunE: func(cmd *cobra.Command, args []string) error {
			expires, _ := cmd.Flags().GetInt("expires")
			if err := validate.ValidateExpires(expires); err != nil {
				return err
			}
			// Stub: login flow not implemented yet (task group 8).
			return nil
		},
	}
	cmd.Flags().String("provider", "github", "OAuth provider (e.g., 'github')")
	cmd.Flags().Int("expires", 90, "Key expiry in days (0, 30, 60, or 90)")
	return cmd
}

// newKeysCmd creates the keys parent command with list, refresh, and revoke subcommands.
func newKeysCmd() *cobra.Command {
	keysCmd := &cobra.Command{
		Use:   "keys",
		Short: "Manage API keys",
	}

	keysCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List all API keys",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Stub: not implemented yet (task group 9).
			return nil
		},
	})

	keysCmd.AddCommand(&cobra.Command{
		Use:   "refresh",
		Short: "Rotate the current API key",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Stub: not implemented yet (task group 9).
			return nil
		},
	})

	keysCmd.AddCommand(&cobra.Command{
		Use:   "revoke",
		Short: "Revoke the current API key and clear local credentials",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Stub: not implemented yet (task group 9).
			return nil
		},
	})

	return keysCmd
}

// newWorkspaceCmd creates the workspace parent command with create, list, get,
// and token subcommands.
func newWorkspaceCmd() *cobra.Command {
	wsCmd := &cobra.Command{
		Use:   "workspace",
		Short: "Manage workspaces",
	}

	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Register a new workspace",
		RunE: func(cmd *cobra.Command, args []string) error {
			gitURL, _ := cmd.Flags().GetString("git-url")
			if err := validate.ValidateNonEmpty("git-url", gitURL); err != nil {
				return err
			}
			slug, _ := cmd.Flags().GetString("slug")
			if err := validate.ValidateNonEmpty("slug", slug); err != nil {
				return err
			}
			// Stub: not implemented yet (task group 10).
			return nil
		},
	}
	createCmd.Flags().String("git-url", "", "Git repository URL (required)")
	createCmd.Flags().String("slug", "", "Workspace slug (required)")
	createCmd.Flags().String("branch", "", "Git branch or ref (optional)")
	createCmd.Flags().String("team", "", "Team slug (optional, resolved to UUID)")

	wsCmd.AddCommand(createCmd)

	wsCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List all workspaces",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Stub: not implemented yet (task group 10).
			return nil
		},
	})

	wsCmd.AddCommand(&cobra.Command{
		Use:   "get",
		Short: "Get a workspace by slug",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Stub: not implemented yet (task group 10).
			return nil
		},
	})

	wsCmd.AddCommand(newWorkspaceTokenCmd())

	return wsCmd
}

// newWorkspaceTokenCmd creates the workspace token subcommand tree.
func newWorkspaceTokenCmd() *cobra.Command {
	tokenCmd := &cobra.Command{
		Use:   "token",
		Short: "Manage workspace tokens",
	}

	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a workspace token",
		RunE: func(cmd *cobra.Command, args []string) error {
			workspace, _ := cmd.Flags().GetString("workspace")
			if err := validate.ValidateNonEmpty("workspace", workspace); err != nil {
				return err
			}
			expires, _ := cmd.Flags().GetInt("expires")
			if err := validate.ValidateExpires(expires); err != nil {
				return err
			}
			// Stub: not implemented yet (task group 11).
			return nil
		},
	}
	createCmd.Flags().String("workspace", "", "Workspace slug (required)")
	createCmd.Flags().String("label", "", "Human-readable token label (optional)")
	createCmd.Flags().Int("expires", 30, "Token expiry in days (0, 30, 60, or 90)")

	tokenCmd.AddCommand(createCmd)

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List workspace tokens",
		RunE: func(cmd *cobra.Command, args []string) error {
			workspace, _ := cmd.Flags().GetString("workspace")
			if err := validate.ValidateNonEmpty("workspace", workspace); err != nil {
				return err
			}
			// Stub: not implemented yet (task group 11).
			return nil
		},
	}
	listCmd.Flags().String("workspace", "", "Workspace slug (required)")

	tokenCmd.AddCommand(listCmd)

	revokeCmd := &cobra.Command{
		Use:   "revoke",
		Short: "Revoke a workspace token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workspace, _ := cmd.Flags().GetString("workspace")
			if err := validate.ValidateNonEmpty("workspace", workspace); err != nil {
				return err
			}
			// Stub: not implemented yet (task group 11).
			return nil
		},
	}
	revokeCmd.Flags().String("workspace", "", "Workspace slug (required)")

	tokenCmd.AddCommand(revokeCmd)

	return tokenCmd
}
