// Package config handles TOML configuration loading and validation for af-hub.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config is the top-level configuration struct for af-hub.
type Config struct {
	Server   ServerConfig   `toml:"server"`
	Database DatabaseConfig `toml:"database"`
	Logging  LoggingConfig  `toml:"logging"`
	Auth     AuthConfig     `toml:"auth"`
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
	OAuth   []OAuthProviderConfig `toml:"oauth"`
	Timeout int                   `toml:"timeout"` // HTTP timeout in seconds for identity provider calls
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

// validLogLevels is the set of accepted logging level values.
var validLogLevels = map[string]bool{
	"trace": true,
	"debug": true,
	"info":  true,
	"warn":  true,
	"error": true,
	"fatal": true,
	"panic": true,
}

// LoadConfig reads and parses config.toml from the given path, applying defaults.
// Returns a descriptive error if the file cannot be read or parsed.
func LoadConfig(path string) (*Config, error) {
	cfg := &Config{}

	// Apply defaults before decoding so TOML values override them.
	cfg.Server.Port = 8080
	cfg.Server.BindAddress = "0.0.0.0"
	cfg.Database.Path = "./data/af-hub.db"
	cfg.Logging.Level = "info"

	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, fmt.Errorf("failed to load config from %s: %w", path, err)
	}

	return cfg, nil
}

// ValidateConfig validates the loaded configuration and returns an error
// describing any invalid fields.
func ValidateConfig(cfg *Config) error {
	// Validate port range: 1–65535.
	if cfg.Server.Port < 1 || cfg.Server.Port > 65535 {
		return fmt.Errorf("invalid config: port must be in range 1-65535, got %d", cfg.Server.Port)
	}

	// Validate log level.
	if !validLogLevels[cfg.Logging.Level] {
		return fmt.Errorf("invalid config: level must be one of trace/debug/info/warn/error/fatal/panic, got %q", cfg.Logging.Level)
	}

	// Validate each OAuth provider entry.
	for i, oauth := range cfg.Auth.OAuth {
		if oauth.Provider == "" {
			return fmt.Errorf("invalid config: auth.oauth[%d].provider must not be empty", i)
		}
		if oauth.ClientID == "" {
			return fmt.Errorf("invalid config: auth.oauth[%d].client_id must not be empty", i)
		}
		if oauth.ClientSecret == "" {
			return fmt.Errorf("invalid config: auth.oauth[%d].client_secret must not be empty", i)
		}
	}

	return nil
}

// EnsureDataDir creates the parent directory for the database file if it
// does not exist. Returns the underlying OS error on failure.
func EnsureDataDir(dbPath string) error {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create data directory %s: %w", dir, err)
	}
	return nil
}
