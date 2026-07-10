// Package cmd defines the Cobra command tree for the afc CLI.
package cmd

import (
	"fmt"
	"os"

	"github.com/agent-fox-dev/hub/internal/apierror"
	"github.com/agent-fox-dev/hub/internal/config"
	"github.com/agent-fox-dev/hub/internal/httpclient"
	"github.com/agent-fox-dev/hub/internal/keys"
	"github.com/agent-fox-dev/hub/internal/login"
	"github.com/agent-fox-dev/hub/internal/output"
	"github.com/agent-fox-dev/hub/internal/validate"
	"github.com/agent-fox-dev/hub/internal/wsclient"
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
		// SilenceUsage prevents Cobra from printing usage text on RunE errors;
		// usage is only printed for help/version/unknown command, not for
		// runtime errors from command handlers.
		SilenceUsage: true,
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
		RunE:  runKeysList,
	})

	keysCmd.AddCommand(&cobra.Command{
		Use:   "refresh",
		Short: "Rotate the current API key",
		RunE:  runKeysRefresh,
	})

	keysCmd.AddCommand(&cobra.Command{
		Use:   "revoke",
		Short: "Revoke the current API key and clear local credentials",
		RunE:  runKeysRevoke,
	})

	return keysCmd
}

// resolveAuthConfig loads the config file and resolves hub_url, user_id, and
// api_key using the standard precedence chain (flag > env > config file).
// Returns the resolved config, the config file path, and the raw config
// (needed for key_id and config mutations).
func resolveAuthConfig(cmd *cobra.Command) (*config.ResolvedConfig, string, *config.Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, "", nil, fmt.Errorf("failed to determine home directory: %w", err)
	}
	cfgPath := config.ConfigPath(home)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, "", nil, err
	}

	flags := map[string]string{
		"hub-url": getFlagString(cmd, "hub-url"),
		"user-id": getFlagString(cmd, "user-id"),
		"api-key": getFlagString(cmd, "api-key"),
	}

	resolved, err := config.Resolve(flags, cfg)
	if err != nil {
		return nil, "", nil, err
	}
	return resolved, cfgPath, cfg, nil
}

// getFlagString retrieves a string flag value, returning "" if the flag
// is not defined on this command (persistent flags may not be directly
// accessible on subcommands via Flags()).
func getFlagString(cmd *cobra.Command, name string) string {
	val, err := cmd.Flags().GetString(name)
	if err != nil {
		// Try inherited persistent flags.
		val, _ = cmd.InheritedFlags().GetString(name)
	}
	return val
}

// runKeysList implements "afc keys list": GET /api/v1/keys with Bearer auth,
// pretty-print JSON response to stdout.
func runKeysList(cmd *cobra.Command, args []string) error {
	resolved, _, _, err := resolveAuthConfig(cmd)
	if err != nil {
		return err
	}

	client := httpclient.NewClient()
	body, statusCode, err := keys.ListKeys(resolved.HubURL, resolved.APIKey, client)
	if err != nil {
		return err
	}

	if !apierror.IsSuccess(statusCode) {
		return fmt.Errorf("%s", apierror.HandleResponseBody(statusCode, body))
	}

	return output.PrintJSON(cmd.OutOrStdout(), body)
}

// runKeysRefresh implements "afc keys refresh": POST /api/v1/keys/:key_id/refresh,
// update config with new credentials, print response JSON to stdout.
func runKeysRefresh(cmd *cobra.Command, args []string) error {
	resolved, cfgPath, cfg, err := resolveAuthConfig(cmd)
	if err != nil {
		return err
	}

	// key_id must be present in config (no flag/env override).
	if err := keys.ValidateKeyID(resolved.KeyID); err != nil {
		return err
	}

	client := httpclient.NewClient()
	refreshResp, rawBody, err := keys.RefreshKey(resolved.HubURL, resolved.APIKey, resolved.KeyID, client)
	if err != nil {
		return err
	}

	// Update config with new credentials from refresh response.
	// Per spec 02: token is the full composite key for Bearer auth,
	// key_id is the new key identifier.
	cfg.APIKey = refreshResp.Token
	cfg.KeyID = refreshResp.KeyID
	if err := config.Save(cfgPath, cfg); err != nil {
		return err
	}

	return output.PrintJSON(cmd.OutOrStdout(), rawBody)
}

// runKeysRevoke implements "afc keys revoke": DELETE /api/v1/keys/:key_id,
// clear credentials on success or 404, print status to stderr.
func runKeysRevoke(cmd *cobra.Command, args []string) error {
	resolved, cfgPath, cfg, err := resolveAuthConfig(cmd)
	if err != nil {
		return err
	}

	// key_id must be present in config.
	if err := keys.ValidateKeyID(resolved.KeyID); err != nil {
		return err
	}

	client := httpclient.NewClient()
	statusCode, body, err := keys.RevokeKey(resolved.HubURL, resolved.APIKey, resolved.KeyID, client)
	if err != nil {
		return err
	}

	// Handle the three response categories:
	// 1. 2xx: key revoked successfully
	// 2. 404: key not found (already revoked or deleted)
	// 3. Other non-2xx: server error
	switch {
	case apierror.IsSuccess(statusCode):
		// Clear credentials in config.
		cfg.APIKey = ""
		cfg.KeyID = ""
		cfg.UserID = ""
		if err := config.Save(cfgPath, cfg); err != nil {
			return err
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "API key revoked.")
		return nil

	case statusCode == 404:
		// Treat 404 as success — clear local credentials anyway.
		cfg.APIKey = ""
		cfg.KeyID = ""
		cfg.UserID = ""
		if err := config.Save(cfgPath, cfg); err != nil {
			return err
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "API key not found on server. Local credentials cleared.")
		return nil

	default:
		// Other non-2xx: print error to stderr, do not modify config.
		return fmt.Errorf("%s", apierror.HandleResponseBody(statusCode, body))
	}
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
		RunE:  runWorkspaceCreate,
	}
	createCmd.Flags().String("git-url", "", "Git repository URL (required)")
	createCmd.Flags().String("slug", "", "Workspace slug (required)")
	createCmd.Flags().String("branch", "", "Git branch or ref (optional)")
	createCmd.Flags().String("team", "", "Team slug (optional, resolved to UUID)")

	wsCmd.AddCommand(createCmd)

	wsCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List all workspaces",
		RunE:  runWorkspaceList,
	})

	wsCmd.AddCommand(&cobra.Command{
		Use:   "get",
		Short: "Get a workspace by slug",
		Args:  cobra.ExactArgs(1),
		RunE:  runWorkspaceGet,
	})

	wsCmd.AddCommand(newWorkspaceTokenCmd())

	return wsCmd
}

// runWorkspaceList implements "afc workspace list": GET /api/v1/workspaces
// with Bearer auth, pretty-print JSON response to stdout.
func runWorkspaceList(cmd *cobra.Command, args []string) error {
	resolved, _, _, err := resolveAuthConfig(cmd)
	if err != nil {
		return err
	}

	client := httpclient.NewClient()
	body, statusCode, err := wsclient.ListWorkspaces(resolved.HubURL, resolved.APIKey, client)
	if err != nil {
		return err
	}

	if !apierror.IsSuccess(statusCode) {
		return fmt.Errorf("%s", apierror.HandleResponseBody(statusCode, body))
	}

	return output.PrintJSON(cmd.OutOrStdout(), body)
}

// runWorkspaceGet implements "afc workspace get <slug>": GET /api/v1/workspaces/:slug
// with Bearer auth, pretty-print JSON response to stdout.
func runWorkspaceGet(cmd *cobra.Command, args []string) error {
	resolved, _, _, err := resolveAuthConfig(cmd)
	if err != nil {
		return err
	}

	slug := args[0]

	client := httpclient.NewClient()
	body, statusCode, err := wsclient.GetWorkspace(resolved.HubURL, resolved.APIKey, slug, client)
	if err != nil {
		return err
	}

	if !apierror.IsSuccess(statusCode) {
		return fmt.Errorf("%s", apierror.HandleResponseBody(statusCode, body))
	}

	return output.PrintJSON(cmd.OutOrStdout(), body)
}

// runWorkspaceCreate implements "afc workspace create": validates flags,
// resolves team slug if --team is provided, and POSTs the workspace.
func runWorkspaceCreate(cmd *cobra.Command, args []string) error {
	// Step 1: Validate required flags before any network call.
	gitURL, _ := cmd.Flags().GetString("git-url")
	if err := validate.ValidateNonEmpty("git-url", gitURL); err != nil {
		return err
	}
	slug, _ := cmd.Flags().GetString("slug")
	if err := validate.ValidateNonEmpty("slug", slug); err != nil {
		return err
	}

	// Step 2: Resolve auth config (hub_url, user_id, api_key).
	resolved, _, _, err := resolveAuthConfig(cmd)
	if err != nil {
		return err
	}

	client := httpclient.NewClient()

	// Step 3: Build the POST payload with required fields.
	payload := map[string]any{
		"git_url": gitURL,
		"slug":    slug,
	}

	// Step 4: Add optional branch if provided.
	branch, _ := cmd.Flags().GetString("branch")
	if branch != "" {
		payload["branch"] = branch
	}

	// Step 5: Resolve team slug to UUID if --team is provided.
	team, _ := cmd.Flags().GetString("team")
	if team != "" {
		teams, err := wsclient.ListTeams(resolved.HubURL, resolved.APIKey, client)
		if err != nil {
			return err
		}

		teamID, err := wsclient.ResolveTeamSlug(teams, team)
		if err != nil {
			return err
		}
		payload["team_id"] = teamID
	}

	// Step 6: Create workspace via POST /api/v1/workspaces.
	body, statusCode, err := wsclient.CreateWorkspace(resolved.HubURL, resolved.APIKey, payload, client)
	if err != nil {
		return err
	}

	if !apierror.IsSuccess(statusCode) {
		return fmt.Errorf("%s", apierror.HandleResponseBody(statusCode, body))
	}

	return output.PrintJSON(cmd.OutOrStdout(), body)
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
		RunE:  runWorkspaceTokenCreate,
	}
	createCmd.Flags().String("workspace", "", "Workspace slug (required)")
	createCmd.Flags().String("label", "", "Human-readable token label (optional)")
	createCmd.Flags().Int("expires", 30, "Token expiry in days (0, 30, 60, or 90)")

	tokenCmd.AddCommand(createCmd)

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List workspace tokens",
		RunE:  runWorkspaceTokenList,
	}
	listCmd.Flags().String("workspace", "", "Workspace slug (required)")

	tokenCmd.AddCommand(listCmd)

	revokeCmd := &cobra.Command{
		Use:   "revoke",
		Short: "Revoke a workspace token",
		Args:  cobra.ExactArgs(1),
		RunE:  runWorkspaceTokenRevoke,
	}
	revokeCmd.Flags().String("workspace", "", "Workspace slug (required)")

	tokenCmd.AddCommand(revokeCmd)

	return tokenCmd
}

// runWorkspaceTokenCreate implements "afc workspace token create":
// validates flags, POSTs /api/v1/workspaces/:slug/tokens, prints the full
// token JSON (including secret) to stdout. Does NOT persist the token to config.
func runWorkspaceTokenCreate(cmd *cobra.Command, args []string) error {
	// Step 1: Validate --workspace non-empty before any network call.
	workspace, _ := cmd.Flags().GetString("workspace")
	if err := validate.ValidateNonEmpty("workspace", workspace); err != nil {
		return err
	}

	// Step 2: Validate --expires before any network call.
	expires, _ := cmd.Flags().GetInt("expires")
	if err := validate.ValidateExpires(expires); err != nil {
		return err
	}

	// Step 3: Resolve auth config (hub_url, user_id, api_key).
	resolved, _, _, err := resolveAuthConfig(cmd)
	if err != nil {
		return err
	}

	// Step 4: Build payload. Always include 'expires' as integer.
	// Include 'label' only if provided.
	payload := map[string]any{
		"expires": expires,
	}
	label, _ := cmd.Flags().GetString("label")
	if label != "" {
		payload["label"] = label
	}

	// Step 5: POST /api/v1/workspaces/:slug/tokens with Bearer auth.
	client := httpclient.NewClient()
	body, statusCode, err := wsclient.CreateToken(resolved.HubURL, resolved.APIKey, workspace, payload, client)
	if err != nil {
		return err
	}

	if !apierror.IsSuccess(statusCode) {
		return fmt.Errorf("%s", apierror.HandleResponseBody(statusCode, body))
	}

	// Step 6: Print token JSON to stdout (do NOT write to config).
	return output.PrintJSON(cmd.OutOrStdout(), body)
}

// runWorkspaceTokenList implements "afc workspace token list":
// GET /api/v1/workspaces/:slug/tokens with Bearer auth, prints metadata
// JSON to stdout.
func runWorkspaceTokenList(cmd *cobra.Command, args []string) error {
	// Step 1: Validate --workspace non-empty.
	workspace, _ := cmd.Flags().GetString("workspace")
	if err := validate.ValidateNonEmpty("workspace", workspace); err != nil {
		return err
	}

	// Step 2: Resolve auth config.
	resolved, _, _, err := resolveAuthConfig(cmd)
	if err != nil {
		return err
	}

	// Step 3: GET /api/v1/workspaces/:slug/tokens with Bearer auth.
	client := httpclient.NewClient()
	body, statusCode, err := wsclient.ListTokens(resolved.HubURL, resolved.APIKey, workspace, client)
	if err != nil {
		return err
	}

	if !apierror.IsSuccess(statusCode) {
		return fmt.Errorf("%s", apierror.HandleResponseBody(statusCode, body))
	}

	return output.PrintJSON(cmd.OutOrStdout(), body)
}

// runWorkspaceTokenRevoke implements "afc workspace token revoke":
// DELETE /api/v1/workspaces/:slug/tokens/:token_id with Bearer auth,
// prints "Token <token-id> revoked." to stderr on success.
func runWorkspaceTokenRevoke(cmd *cobra.Command, args []string) error {
	// Step 1: Validate --workspace non-empty.
	workspace, _ := cmd.Flags().GetString("workspace")
	if err := validate.ValidateNonEmpty("workspace", workspace); err != nil {
		return err
	}

	// Step 2: Resolve auth config.
	resolved, _, _, err := resolveAuthConfig(cmd)
	if err != nil {
		return err
	}

	// Step 3: The token-id is the required positional argument (ExactArgs(1)).
	tokenID := args[0]

	// Step 4: DELETE /api/v1/workspaces/:slug/tokens/:token_id with Bearer auth.
	client := httpclient.NewClient()
	statusCode, body, err := wsclient.RevokeToken(resolved.HubURL, resolved.APIKey, workspace, tokenID, client)
	if err != nil {
		return err
	}

	if !apierror.IsSuccess(statusCode) {
		return fmt.Errorf("%s", apierror.HandleResponseBody(statusCode, body))
	}

	// Step 5: Print confirmation to stderr.
	fmt.Fprintf(cmd.ErrOrStderr(), "Token %s revoked.\n", tokenID)
	return nil
}
