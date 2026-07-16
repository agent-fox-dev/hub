// Package serverconfig handles loading and validation of the af-hub server
// configuration from config.toml. This is the server-side configuration,
// separate from the CLI client configuration in internal/config (spec 05).
//
// See docs/errata/01_server_config_package.md for why this is a separate
// package from internal/config.
package serverconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// DefaultConfigPath returns the XDG-compliant default config file path.
// It uses $XDG_CONFIG_HOME/af-hub/config.toml, falling back to
// ~/.config/af-hub/config.toml when the env var is unset or empty.
func DefaultConfigPath() string {
	return filepath.Join(xdgConfigDir(), "af-hub", "config.toml")
}

// DefaultDataDir returns the XDG-compliant default data directory.
// It uses $XDG_DATA_HOME/af-hub, falling back to
// ~/.local/share/af-hub when the env var is unset or empty.
func DefaultDataDir() string {
	return filepath.Join(xdgDataDir(), "af-hub")
}

func xdgConfigDir() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".config")
	}
	return filepath.Join(home, ".config")
}

func xdgDataDir() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".local", "share")
	}
	return filepath.Join(home, ".local", "share")
}

// Config represents the af-hub server configuration loaded from config.toml.
//
// Default values when fields are omitted:
//   - Server.Port:    8080
//   - Server.Bind:    "0.0.0.0"
//   - Database.Path:  "./data/af-hub.db"
//   - Log.Level:      "info"
type Config struct {
	Server   ServerConfig   `toml:"server"`
	Database DatabaseConfig `toml:"database"`
	Log      LogConfig      `toml:"log"`
	OAuth    OAuthConfig    `toml:"oauth"`
}

// ServerConfig holds HTTP server settings from the [server] TOML section.
type ServerConfig struct {
	Port        int    `toml:"port"`
	Bind        string `toml:"bind"`
	ExternalURL string `toml:"external_url"`
}

// DatabaseConfig holds SQLite database settings from the [database] TOML section.
type DatabaseConfig struct {
	Path string `toml:"path"`
}

// LogConfig holds structured logging settings from the [log] TOML section.
type LogConfig struct {
	Level string `toml:"level"`
}

// OAuthConfig holds OAuth provider configurations from the [oauth] TOML section.
type OAuthConfig struct {
	Providers []OAuthProvider `toml:"providers"`
}

// OAuthProvider defines a single OAuth provider from a [[oauth.providers]] block.
type OAuthProvider struct {
	Name         string `toml:"name"`
	ClientID     string `toml:"client_id"`
	ClientSecret string `toml:"client_secret"`
	AuthorizeURL string `toml:"authorize_url"`
	TokenURL     string `toml:"token_url"`
	UserinfoURL  string `toml:"userinfo_url"`
}

// LoadResult contains the parsed server configuration along with metadata
// about the loading process.
type LoadResult struct {
	// Config is the parsed configuration with defaults applied for missing fields.
	Config *Config

	// ConfigDir is the absolute path of the directory containing the resolved
	// config file. This determines where the admin_token file will be written.
	// When no --config flag is provided, this is the current working directory.
	ConfigDir string

	// UnrecognizedKeys lists TOML keys present in the file that do not map to
	// any config struct field. Each entry is the dotted key path (e.g., "servr.port").
	// The caller should emit a warn-level log entry for each.
	UnrecognizedKeys []string

	// Warnings collects non-fatal warning messages produced during config loading.
	// For example, an invalid log.level value generates a warning with the
	// invalid_value field name.
	Warnings []string

	// InvalidLogLevel is set to the original invalid log level value when
	// log.level is not in the recognized set. The caller should include this
	// in the warn-level log entry as the "invalid_value" field.
	InvalidLogLevel string
}

// validLogLevels is the set of recognized log level strings.
var validLogLevels = map[string]bool{
	"trace": true,
	"debug": true,
	"info":  true,
	"warn":  true,
	"error": true,
	"fatal": true,
	"panic": true,
}

// applyDefaults sets default values for any Config fields that are zero-valued.
// When useXDG is true, the database path defaults to the XDG data directory
// instead of a CWD-relative path.
func applyDefaults(cfg *Config, useXDG bool) {
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Server.Bind == "" {
		cfg.Server.Bind = "0.0.0.0"
	}
	if cfg.Database.Path == "" {
		if useXDG {
			cfg.Database.Path = filepath.Join(DefaultDataDir(), "af-hub.db")
		} else {
			cfg.Database.Path = "./data/af-hub.db"
		}
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}
}

// resolveConfigDir returns the absolute path of the directory containing path.
func resolveConfigDir(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("failed to resolve config path: %w", err)
	}
	return filepath.Dir(absPath), nil
}

// LoadConfig loads the server configuration from a TOML file at the given path.
//
// When useXDG is true, omitted database.path defaults to the XDG data
// directory ($XDG_DATA_HOME/af-hub/af-hub.db) instead of ./data/af-hub.db.
//
// Behavior:
//   - If the file does not exist, returns a Config with all defaults applied (non-fatal).
//   - If the file contains invalid TOML syntax, returns an error.
//   - If the file contains unrecognized fields, they are listed in LoadResult.UnrecognizedKeys.
//   - If log.level is not in {trace,debug,info,warn,error,fatal,panic}, a warning is
//     added to LoadResult.Warnings and the level defaults to "info".
//   - ConfigDir is set to the absolute path of the directory containing the config file.
func LoadConfig(path string, useXDG bool) (*LoadResult, error) {
	configDir, err := resolveConfigDir(path)
	if err != nil {
		return nil, err
	}

	result := &LoadResult{
		Config:    &Config{},
		ConfigDir: configDir,
	}

	// Attempt to decode the TOML file.
	md, err := toml.DecodeFile(path, result.Config)
	if err != nil {
		// Check if the file simply doesn't exist — that's non-fatal.
		var pathErr *os.PathError
		if errors.As(err, &pathErr) && errors.Is(pathErr.Err, os.ErrNotExist) {
			// File not found is non-fatal; apply all defaults.
			applyDefaults(result.Config, useXDG)
			return result, nil
		}
		// Also handle the case where os.IsNotExist returns true.
		if os.IsNotExist(err) {
			applyDefaults(result.Config, useXDG)
			return result, nil
		}
		// Any other error (parse failure, permission error) is fatal.
		return nil, fmt.Errorf("failed to parse config file %s: %w", path, err)
	}

	// Collect unrecognized keys from TOML metadata.
	for _, key := range md.Undecoded() {
		result.UnrecognizedKeys = append(result.UnrecognizedKeys, key.String())
	}

	// Validate log level.
	if result.Config.Log.Level != "" && !validLogLevels[result.Config.Log.Level] {
		result.InvalidLogLevel = result.Config.Log.Level
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("unrecognized log level %q; defaulting to \"info\"", result.Config.Log.Level))
		result.Config.Log.Level = ""
	}

	// Apply defaults for any omitted fields.
	applyDefaults(result.Config, useXDG)

	return result, nil
}

// StartupLogFields returns the structured fields for the "server starting"
// info-level log entry emitted immediately before the HTTP listener starts.
//
// Returns a map with keys: "bind" (string), "port" (int), "db_path" (string),
// "log_level" (string), "msg" (string = "server starting").
func StartupLogFields(cfg *Config) map[string]any {
	return map[string]any{
		"bind":      cfg.Server.Bind,
		"port":      cfg.Server.Port,
		"db_path":   cfg.Database.Path,
		"log_level": cfg.Log.Level,
		"msg":       "server starting",
	}
}
