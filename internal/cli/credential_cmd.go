package cli

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"
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
