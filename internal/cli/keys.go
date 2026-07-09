package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/agent-fox/af-hub/internal/cliconfig"
	"github.com/spf13/cobra"
)

// apiKeyResponse represents the key fields returned by the server on
// key creation and refresh. Used to parse the response for config updates.
type apiKeyResponse struct {
	Key   string `json:"key"`
	KeyID string `json:"key_id"`
}

var apiKey string

// newKeysCmd creates the "keys" parent command for API key management.
func newKeysCmd() *cobra.Command {
	keysCmd := &cobra.Command{
		Use:   "keys",
		Short: "Manage API keys",
		Long:  "Manage API keys for programmatic access to af-hub workspaces.",
	}

	// Register --api-key as a persistent flag on the keys command group.
	keysCmd.PersistentFlags().StringVar(&apiKey, "api-key", "", "API key for authentication (overrides AF_HUB_API_KEY)")

	// Register key subcommands.
	keysCmd.AddCommand(newKeysCreateCmd())
	keysCmd.AddCommand(newKeysListCmd())
	keysCmd.AddCommand(newKeysRefreshCmd())
	keysCmd.AddCommand(newKeysRevokeCmd())
	keysCmd.AddCommand(newKeysDefaultCmd())

	return keysCmd
}

// newKeysCreateCmd creates the "keys create" subcommand.
func newKeysCreateCmd() *cobra.Command {
	var workspace string
	var label string
	var expires int

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new API key",
		Long:  "Create a new API key scoped to a workspace.",
		RunE: func(cmd *cobra.Command, args []string) error {
			hub, err := resolveHubURL()
			if err != nil {
				return err
			}
			key, err := resolveAPIKey(apiKey)
			if err != nil {
				return err
			}

			if workspace == "" {
				return fmt.Errorf("--workspace flag is required")
			}

			// Build request body.
			body := map[string]any{
				"workspace_id": workspace,
				"expires":      expires,
			}
			if label != "" {
				body["label"] = label
			}

			bodyJSON, err := json.Marshal(body)
			if err != nil {
				return fmt.Errorf("failed to marshal request body: %w", err)
			}

			req, err := http.NewRequest("POST", hub+"/api/v1/keys", bytes.NewReader(bodyJSON))
			if err != nil {
				return fmt.Errorf("failed to create request: %w", err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+key)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("failed to connect to hub at %s: %w", hub, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return ParseHTTPError(resp)
			}

			// Decode the response to extract key_id and token for config update.
			var result json.RawMessage
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				return fmt.Errorf("failed to decode response: %w", err)
			}

			// Parse the key response for config storage.
			var keyResp apiKeyResponse
			if err := json.Unmarshal(result, &keyResp); err == nil && keyResp.KeyID != "" && keyResp.Key != "" {
				// Store the key in the config file. Use the --workspace flag
				// value as the section key and --label flag value for the label
				// (the server response does not include these fields).
				if homeDir, err := os.UserHomeDir(); err == nil && loadedConfig != nil {
					if addErr := cliconfig.AddKeyEntry(homeDir, loadedConfig, workspace, keyResp.KeyID, keyResp.Key, label); addErr != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to save key to config: %v\n", addErr)
					}
				}
			}

			return PrintJSON(cmd.OutOrStdout(), result)
		},
	}

	cmd.Flags().StringVar(&workspace, "workspace", "", "Workspace ID (required)")
	cmd.Flags().StringVar(&label, "label", "", "Human-readable label for the key")
	cmd.Flags().IntVar(&expires, "expires", 30, "Key expiry in days (0, 30, 60, or 90)")

	return cmd
}

// newKeysListCmd creates the "keys list" subcommand.
func newKeysListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all API keys",
		Long:  "List all API keys for the authenticated user.",
		RunE: func(cmd *cobra.Command, args []string) error {
			hub, err := resolveHubURL()
			if err != nil {
				return err
			}
			key, err := resolveAPIKey(apiKey)
			if err != nil {
				return err
			}

			req, err := http.NewRequest("GET", hub+"/api/v1/keys", nil)
			if err != nil {
				return fmt.Errorf("failed to create request: %w", err)
			}
			req.Header.Set("Authorization", "Bearer "+key)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("failed to connect to hub at %s: %w", hub, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return ParseHTTPError(resp)
			}

			// Decode and print the keys array to stdout.
			var result json.RawMessage
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				return fmt.Errorf("failed to decode response: %w", err)
			}
			return PrintJSON(cmd.OutOrStdout(), result)
		},
	}

	return cmd
}

// newKeysRefreshCmd creates the "keys refresh" subcommand.
func newKeysRefreshCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "refresh <key-id>",
		Short: "Refresh an API key secret",
		Long:  "Generate a new secret for an existing API key by key ID.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("key ID argument is required")
			}
			if len(args) > 1 {
				return fmt.Errorf("accepts 1 argument, received %d", len(args))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			hub, err := resolveHubURL()
			if err != nil {
				return err
			}
			key, err := resolveAPIKey(apiKey)
			if err != nil {
				return err
			}

			keyID := args[0]

			req, err := http.NewRequest("POST", hub+"/api/v1/keys/"+keyID+"/refresh", nil)
			if err != nil {
				return fmt.Errorf("failed to create request: %w", err)
			}
			req.Header.Set("Authorization", "Bearer "+key)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("failed to connect to hub at %s: %w", hub, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return ParseHTTPError(resp)
			}

			// Decode the response to extract the new token for config update.
			var result json.RawMessage
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				return fmt.Errorf("failed to decode response: %w", err)
			}

			// Parse the key response and update the token in config.
			var keyResp apiKeyResponse
			if err := json.Unmarshal(result, &keyResp); err == nil && keyResp.Key != "" {
				if homeDir, err := os.UserHomeDir(); err == nil && loadedConfig != nil {
					if updateErr := cliconfig.UpdateKeyToken(homeDir, loadedConfig, keyID, keyResp.Key, cmd.ErrOrStderr()); updateErr != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to update key in config: %v\n", updateErr)
					}
				}
			}

			return PrintJSON(cmd.OutOrStdout(), result)
		},
	}

	return cmd
}

// newKeysRevokeCmd creates the "keys revoke" subcommand.
func newKeysRevokeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "revoke <key-id>",
		Short: "Revoke an API key",
		Long:  "Permanently revoke an existing API key by key ID.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("key ID argument is required")
			}
			if len(args) > 1 {
				return fmt.Errorf("accepts 1 argument, received %d", len(args))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			hub, err := resolveHubURL()
			if err != nil {
				return err
			}
			key, err := resolveAPIKey(apiKey)
			if err != nil {
				return err
			}

			keyID := args[0]

			req, err := http.NewRequest("DELETE", hub+"/api/v1/keys/"+keyID, nil)
			if err != nil {
				return fmt.Errorf("failed to create request: %w", err)
			}
			req.Header.Set("Authorization", "Bearer "+key)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("failed to connect to hub at %s: %w", hub, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return ParseHTTPError(resp)
			}

			// Remove the key from the config file.
			if homeDir, err := os.UserHomeDir(); err == nil && loadedConfig != nil {
				if removeErr := cliconfig.RemoveKeyEntry(homeDir, loadedConfig, keyID, cmd.ErrOrStderr()); removeErr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to remove key from config: %v\n", removeErr)
				}
			}

			// Print confirmation to stderr (not stdout).
			fmt.Fprintf(cmd.ErrOrStderr(), "Successfully revoked API key %s\n", keyID)
			return nil
		},
	}

	return cmd
}
