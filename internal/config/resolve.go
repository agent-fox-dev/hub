package config

import "os"

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
	// Stub: returns empty config with no error.
	_ = os.Getenv // reference os to avoid unused import
	return &ResolvedConfig{}, nil
}
