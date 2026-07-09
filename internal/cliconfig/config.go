// Package cliconfig manages the persistent client configuration for the afc CLI.
// Configuration is stored in $HOME/.af/config.toml and provides hub URL, API key,
// and workspace key storage.
package cliconfig

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// Config represents the afc CLI client configuration stored in
// $HOME/.af/config.toml.
type Config struct {
	HubURL string              `toml:"hub_url"`
	APIKey string              `toml:"api_key"`
	Keys   map[string]KeyEntry `toml:"keys"`
}

// KeyEntry represents a single API key entry stored in a
// [keys.<workspace_slug>] TOML table section.
type KeyEntry struct {
	KeyID string `toml:"key_id"`
	Token string `toml:"token"`
	Label string `toml:"label"`
}

// ConfigDir returns the path to the configuration directory for the given
// home directory: $HOME/.af/
func ConfigDir(homeDir string) string {
	return homeDir + string(os.PathSeparator) + ".af"
}

// ConfigFilePath returns the path to the configuration file for the given
// home directory: $HOME/.af/config.toml
func ConfigFilePath(homeDir string) string {
	return ConfigDir(homeDir) + string(os.PathSeparator) + "config.toml"
}

// LoadConfig loads and parses the afc client configuration from
// $HOME/.af/config.toml. Returns a descriptive error if the file cannot
// be read or contains invalid TOML.
func LoadConfig(homeDir string) (*Config, error) {
	configPath := ConfigFilePath(homeDir)

	var cfg Config
	if _, err := toml.DecodeFile(configPath, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file %s: %w", configPath, err)
	}

	// Ensure the Keys map is initialized even if the TOML had no [keys.*] sections.
	if cfg.Keys == nil {
		cfg.Keys = make(map[string]KeyEntry)
	}

	return &cfg, nil
}
