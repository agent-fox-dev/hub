package config

import (
	"fmt"
	"os"
)

// ResolvedConfig holds the final resolved configuration values after applying
// the precedence chain: CLI flags > environment variables > config file.
type ResolvedConfig struct {
	HubURL string
	UserID string
	APIKey string
	KeyID  string
}

// Resolve resolves configuration values using the precedence chain:
// CLI flags > environment variables > config file values.
// Returns an error if a required value (hub_url, user_id, api_key) is not
// available from any source.
//
// The flags map should contain keys "hub-url", "user-id", "api-key" with
// values from CLI flags (empty string if flag not provided).
//
// key_id is resolved exclusively from the config file; no flag or env var
// override exists.
func Resolve(flags map[string]string, cfg *Config) (*ResolvedConfig, error) {
	resolved := &ResolvedConfig{}

	// Resolve hub_url: flag > env > config file.
	resolved.HubURL = resolveValue(flags["hub-url"], os.Getenv("AF_HUB_URL"), cfg.HubURL)
	if resolved.HubURL == "" {
		return nil, fmt.Errorf("hub_url is not set. Provide it via --hub-url flag, AF_HUB_URL environment variable, or hub_url in config file")
	}

	// Resolve user_id: flag > env > config file.
	resolved.UserID = resolveValue(flags["user-id"], os.Getenv("AF_HUB_USER_ID"), cfg.UserID)
	if resolved.UserID == "" {
		return nil, fmt.Errorf("user_id is not set. Provide it via --user-id flag, AF_HUB_USER_ID environment variable, or user_id in config file")
	}

	// Resolve api_key: flag > env > config file.
	resolved.APIKey = resolveValue(flags["api-key"], os.Getenv("AF_HUB_API_KEY"), cfg.APIKey)
	if resolved.APIKey == "" {
		return nil, fmt.Errorf("api_key is not set. Provide it via --api-key flag, AF_HUB_API_KEY environment variable, or api_key in config file")
	}

	// key_id: config file only — no flag or env var override exists.
	resolved.KeyID = cfg.KeyID

	return resolved, nil
}

// resolveValue returns the first non-empty value from the precedence chain:
// flag > env > config.
func resolveValue(flag, env, config string) string {
	if flag != "" {
		return flag
	}
	if env != "" {
		return env
	}
	return config
}
