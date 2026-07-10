// Package serverconfig handles loading and validation of the af-hub server
// configuration from config.toml. This is the server-side configuration,
// separate from the CLI client configuration in internal/config (spec 05).
//
// See docs/errata/01_server_config_package.md for why this is a separate
// package from internal/config.
package serverconfig

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
}

// LoadConfig loads the server configuration from a TOML file at the given path.
//
// Behavior:
//   - If the file does not exist, returns a Config with all defaults applied (non-fatal).
//   - If the file contains invalid TOML syntax, returns an error.
//   - If the file contains unrecognized fields, they are listed in LoadResult.UnrecognizedKeys.
//   - If log.level is not in {trace,debug,info,warn,error,fatal,panic}, a warning is
//     added to LoadResult.Warnings and the level defaults to "info".
//   - ConfigDir is set to the absolute path of the directory containing the config file.
func LoadConfig(path string) (*LoadResult, error) {
	// Stub: returns zero-value config without defaults.
	// Implementation will be added in task group 8.
	return &LoadResult{Config: &Config{}}, nil
}

// StartupLogFields returns the structured fields for the "server starting"
// info-level log entry emitted immediately before the HTTP listener starts.
//
// Returns a map with keys: "bind" (string), "port" (int), "db_path" (string),
// "log_level" (string), "msg" (string = "server starting").
func StartupLogFields(cfg *Config) map[string]any {
	// Stub: returns nil. Implementation in task group 14.
	return nil
}
