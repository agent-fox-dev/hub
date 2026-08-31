package cli

import (
	"bufio"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"
	"github.com/txsvc/apikit"
)

// afConfig is a minimal representation of ~/.af/config.toml,
// sufficient for the credential helper to extract the hub URL and API key.
type afConfig struct {
	EndpointURL string `toml:"endpoint_url"`
	APIKey      string `toml:"api_key"`
}

// CredentialHelperCmd returns the 'credential-helper' command that implements
// git's credential helper protocol. Configure it with:
//
//	git config --global credential.<hub-url>.helper '!afc credential-helper'
func CredentialHelperCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "credential-helper <get|store|erase>",
		Short:         "Git credential helper for hub authentication",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		Hidden:        true,
		Annotations: map[string]string{
			"auth": "none",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "get" {
				return nil
			}

			cfg, err := loadAFConfig()
			if err != nil || cfg.EndpointURL == "" || cfg.APIKey == "" {
				return nil
			}

			hubHost, err := hostFromURL(cfg.EndpointURL)
			if err != nil {
				return nil
			}

			attrs := parseCredentialInput(cmd.InOrStdin())

			if attrs["host"] != hubHost {
				return nil
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "protocol=%s\n", attrs["protocol"])
			fmt.Fprintf(out, "host=%s\n", attrs["host"])
			fmt.Fprintln(out, "username=x-token-auth")
			fmt.Fprintf(out, "password=%s\n", cfg.APIKey)

			return nil
		},
	}
}

// CredentialCmd returns the 'credential' parent command with the 'set'
// subcommand for storing upstream git credentials as workspace secrets.
func CredentialCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "credential",
		Short:         "Manage git credentials",
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	cmd.AddCommand(newCredentialSetCmd())

	return cmd
}

// newCredentialSetCmd returns the 'credential set' subcommand that stores
// upstream git credentials as workspace secrets via the secrets API.
// Supports --upstream-git-pat, --upstream-git-username, --upstream-git-password.
// Requirements: 15-REQ-5.2, 15-REQ-5.3
func newCredentialSetCmd() *cobra.Command {
	var (
		upstreamGitPAT      string
		upstreamGitUsername  string
		upstreamGitPassword string
	)

	cmd := &cobra.Command{
		Use:           "set <workspace-slug>",
		Short:         "Set upstream git credentials for a workspace",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := args[0]

			hasPAT := cmd.Flags().Changed("upstream-git-pat")
			hasUsername := cmd.Flags().Changed("upstream-git-username")
			hasPassword := cmd.Flags().Changed("upstream-git-password")

			if !hasPAT && !hasUsername && !hasPassword {
				return apikit.CLIHandleError(cmd, apikit.NewCLIError(2,
					"at least one credential flag is required (--upstream-git-pat, --upstream-git-username/--upstream-git-password)"))
			}

			// Build the list of secret entries to store.
			var entries []map[string]string

			if hasPAT {
				entries = append(entries, map[string]string{
					"key":   "UPSTREAM_GIT_PAT",
					"value": upstreamGitPAT,
				})
			}
			if hasUsername {
				entries = append(entries, map[string]string{
					"key":   "UPSTREAM_GIT_USERNAME",
					"value": upstreamGitUsername,
				})
			}
			if hasPassword {
				entries = append(entries, map[string]string{
					"key":   "UPSTREAM_GIT_PASSWORD",
					"value": upstreamGitPassword,
				})
			}

			client, err := apikit.CLIClientFromCmd(cmd)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			body := map[string]any{
				"entries": entries,
			}

			result, err := client.DoRequest(cmd.Context(), http.MethodPost,
				"/workspaces/"+slug+"/secrets", body)
			if err != nil {
				return apikit.CLIHandleError(cmd, err)
			}

			// Print API response JSON to stdout; human-readable confirmation to stderr.
			if err := apikit.CLIPrintResult(cmd, result); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Upstream credentials stored for workspace '%s'.\n", slug)
			return nil
		},
	}

	cmd.Flags().StringVar(&upstreamGitPAT, "upstream-git-pat", "", "Upstream personal access token")
	cmd.Flags().StringVar(&upstreamGitUsername, "upstream-git-username", "", "Upstream git username")
	cmd.Flags().StringVar(&upstreamGitPassword, "upstream-git-password", "", "Upstream git password")

	return cmd
}

func loadAFConfig() (*afConfig, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(home, ".af", "config.toml"))
	if err != nil {
		return nil, err
	}
	var cfg afConfig
	if _, err := toml.Decode(os.ExpandEnv(string(data)), &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func hostFromURL(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	return u.Host, nil
}

func parseCredentialInput(r interface{ Read([]byte) (int, error) }) map[string]string {
	attrs := make(map[string]string)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			attrs[k] = v
		}
	}
	return attrs
}
