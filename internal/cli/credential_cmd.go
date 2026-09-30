package cli

import (
	"bufio"
	"fmt"
	"net"
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

			cfg := loadHelperConfig()
			if cfg.EndpointURL == "" || cfg.APIKey == "" {
				return nil
			}

			hub, err := url.Parse(cfg.EndpointURL)
			if err != nil || hub.Host == "" {
				return nil
			}

			attrs := parseCredentialInput(cmd.InOrStdin())

			// Only answer for the hub's own scheme and host:port. Matching on
			// host alone would hand the full-access API key to a plain-http
			// request for the same host (a downgraded or spoofed remote).
			if !credentialMatchesHub(attrs, hub) {
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

// loadHelperConfig resolves the endpoint URL and API key for the credential
// helper. The ENDPOINT_URL and API_KEY environment variables take precedence,
// matching the precedence apikit applies to every other afc command, so an
// environment-only setup (CI, devcontainers, Codespaces) works for git too.
// The config file fills in whatever the environment does not provide.
func loadHelperConfig() *afConfig {
	cfg := &afConfig{}
	if fileCfg, err := loadAFConfig(); err == nil {
		cfg = fileCfg
	}
	if v := os.Getenv("ENDPOINT_URL"); v != "" {
		cfg.EndpointURL = v
	}
	if v := os.Getenv("API_KEY"); v != "" {
		cfg.APIKey = v
	}
	return cfg
}

// credentialMatchesHub reports whether the git credential request (protocol,
// host, optional port) targets the configured hub URL. Scheme and host:port
// must both match; a missing port on either side means the scheme default.
func credentialMatchesHub(attrs map[string]string, hub *url.URL) bool {
	if attrs["protocol"] != hub.Scheme {
		return false
	}
	reqHost := attrs["host"]
	if reqHost == "" {
		return false
	}
	return normalizeHostPort(reqHost, attrs["protocol"]) == normalizeHostPort(hub.Host, hub.Scheme)
}

// normalizeHostPort lowercases the host and appends the scheme's default
// port when none is present so that "hub.example.com" and
// "hub.example.com:443" compare equal for https.
func normalizeHostPort(hostport, scheme string) string {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
		port = ""
	}
	if port == "" {
		switch scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return strings.ToLower(host) + ":" + port
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
