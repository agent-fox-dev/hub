// Package cmd defines the Cobra command tree for the afc CLI.
package cmd

import (
	"fmt"
	"os"

	"github.com/agent-fox-dev/hub/internal/config"
	"github.com/agent-fox-dev/hub/internal/httpclient"
	"github.com/agent-fox-dev/hub/internal/login"
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

// LoginTimeout controls the maximum wait for an OAuth callback.
// It can be overridden in tests to avoid 2-minute waits.
var LoginTimeout = login.DefaultTimeout

// newLoginCmd creates the login subcommand.
func newLoginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with the hub using OAuth",
		Long:  "Authenticate with the hub using OAuth authorization code flow.",
		RunE:  runLogin,
	}
	cmd.Flags().String("provider", "github", "OAuth provider (e.g., 'github')")
	cmd.Flags().Int("expires", 90, "Key expiry in days (0, 30, 60, or 90)")
	return cmd
}

func runLogin(cmd *cobra.Command, args []string) error {
	// Step 1: Validate --expires before any network calls.
	expires, _ := cmd.Flags().GetInt("expires")
	if err := validate.ValidateExpires(expires); err != nil {
		return err
	}

	provider, _ := cmd.Flags().GetString("provider")

	// Step 2: Resolve hub_url. Login doesn't require user_id or api_key.
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to determine home directory: %w", err)
	}
	cfgPath := config.ConfigPath(home)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	// Resolve hub_url from flag > env > config.
	hubURL, _ := cmd.Flags().GetString("hub-url")
	resolvedHubURL := resolveHubURL(hubURL, cfg)
	if resolvedHubURL == "" {
		return fmt.Errorf("hub_url is not set. Provide it via --hub-url flag, AF_HUB_URL environment variable, or hub_url in config file")
	}

	// Step 3: Fetch providers from the hub.
	client := httpclient.NewClient()
	providers, err := login.FetchProviders(resolvedHubURL, "", client)
	if err != nil {
		return err
	}

	// Step 4: Validate --provider against the list.
	if err := login.ValidateProvider(provider, providers); err != nil {
		return err
	}

	// Find the selected provider's authorize URL.
	selectedProvider, err := login.FindProvider(provider, providers)
	if err != nil {
		return err
	}

	// Step 5: Generate CSRF state.
	state, err := login.GenerateState()
	if err != nil {
		return err
	}

	// Step 6: Start callback server.
	cs, err := login.StartCallbackServer(state)
	if err != nil {
		return err
	}
	defer cs.Shutdown()

	// Step 7: Build authorization URL.
	redirectURI := fmt.Sprintf("http://localhost:%d/callback", cs.Port())
	authURL := login.BuildAuthorizationURL(
		selectedProvider.AuthorizeURL, state, redirectURI,
	)

	// Step 8: Open browser (non-fatal on error).
	if err := login.BrowserOpenFunc(authURL); err != nil {
		// Browser failure is non-fatal — URL is printed to stderr for manual use.
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not open browser: %v\n", err)
	}

	// Step 9: Always print auth URL to stderr.
	fmt.Fprintf(cmd.ErrOrStderr(), "Open this URL in your browser to authenticate:\n%s\n", authURL)

	// Step 10: Wait for callback.
	code, receivedState, err := cs.WaitForCode(LoginTimeout)
	if err != nil {
		return err
	}

	// Step 11: Validate state match.
	if receivedState != state {
		return fmt.Errorf("OAuth callback state mismatch: possible CSRF attack")
	}

	// Step 12: Exchange code for credentials.
	cbResp, err := login.ExchangeCode(resolvedHubURL, provider, code, redirectURI, expires, client)
	if err != nil {
		return err
	}

	// Step 13: Save credentials to config via atomic write.
	// Store api_key.token (full composite key) as api_key (for Bearer auth).
	// Store api_key.key_id as key_id.
	cfg.HubURL = resolvedHubURL
	cfg.UserID = cbResp.User.ID
	cfg.APIKey = cbResp.APIKey.Token
	cfg.KeyID = cbResp.APIKey.KeyID

	if err := config.Save(cfgPath, cfg); err != nil {
		return err
	}

	return nil
}

// resolveHubURL resolves hub_url from flag > env > config, without requiring
// user_id or api_key (which are not available pre-login).
func resolveHubURL(flagVal string, cfg *config.Config) string {
	if flagVal != "" {
		return flagVal
	}
	if envVal := os.Getenv("AF_HUB_URL"); envVal != "" {
		return envVal
	}
	return cfg.HubURL
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
