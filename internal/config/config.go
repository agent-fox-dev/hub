// Package config handles TOML configuration loading and validation for af-hub.
package config

// Config is the top-level configuration struct for af-hub.
type Config struct {
	Server   ServerConfig        `toml:"server"`
	Database DatabaseConfig      `toml:"database"`
	Logging  LoggingConfig       `toml:"logging"`
	Auth     AuthConfig          `toml:"auth"`
}

// ServerConfig holds HTTP server configuration.
type ServerConfig struct {
	Port        int    `toml:"port"`
	BindAddress string `toml:"bind_address"`
	ExternalURL string `toml:"external_url"`
}

// DatabaseConfig holds database configuration.
type DatabaseConfig struct {
	Path string `toml:"path"`
}

// LoggingConfig holds logging configuration.
type LoggingConfig struct {
	Level string `toml:"level"`
}

// AuthConfig holds authentication configuration.
type AuthConfig struct {
	OAuth []OAuthProviderConfig `toml:"oauth"`
}

// OAuthProviderConfig holds configuration for a single OAuth provider.
type OAuthProviderConfig struct {
	Provider     string `toml:"provider"`
	ClientID     string `toml:"client_id"`
	ClientSecret string `toml:"client_secret"`
	AuthorizeURL string `toml:"authorize_url"`
	TokenURL     string `toml:"token_url"`
	UserinfoURL  string `toml:"userinfo_url"`
}

// LoadConfig reads and parses config.toml from the given path, applying defaults.
func LoadConfig(path string) (*Config, error) {
	// Stub — implementation in a later task group.
	return nil, nil
}

// ValidateConfig validates the loaded configuration and returns an error
// describing any invalid fields.
func ValidateConfig(cfg *Config) error {
	// Stub — implementation in a later task group.
	return nil
}

// EnsureDataDir creates the parent directory for the database file if it
// does not exist.
func EnsureDataDir(dbPath string) error {
	// Stub — implementation in a later task group.
	return nil
}
