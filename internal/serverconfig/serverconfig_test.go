package serverconfig_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/serverconfig"
)

// ---------------------------------------------------------------------------
// 1.1 — Config Loading Defaults and Valid TOML Parsing
// ---------------------------------------------------------------------------

// TestSpec01_ConfigLoadDefaults verifies that when no config.toml exists,
// LoadConfig returns all documented defaults: port=8080, bind=0.0.0.0,
// db_path=./data/af-hub.db, log_level=info, external_url="".
// TS-01-1, REQ: 01-REQ-1.1
func TestSpec01_ConfigLoadDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	// No config.toml exists at this path — should use defaults.

	result, err := serverconfig.LoadConfig(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfig with missing file should not return error: %v", err)
	}
	if result == nil || result.Config == nil {
		t.Fatal("LoadConfig should return non-nil result and config")
	}

	cfg := result.Config
	if cfg.Server.Port != 8080 {
		t.Errorf("Server.Port = %d, want 8080", cfg.Server.Port)
	}
	if cfg.Server.Bind != "0.0.0.0" {
		t.Errorf("Server.Bind = %q, want %q", cfg.Server.Bind, "0.0.0.0")
	}
	if cfg.Server.ExternalURL != "" {
		t.Errorf("Server.ExternalURL = %q, want empty string", cfg.Server.ExternalURL)
	}
	if cfg.Database.Path != "./data/af-hub.db" {
		t.Errorf("Database.Path = %q, want %q", cfg.Database.Path, "./data/af-hub.db")
	}
	if cfg.Log.Level != "info" {
		t.Errorf("Log.Level = %q, want %q", cfg.Log.Level, "info")
	}
}

// TestSpec01_ConfigFullParse verifies that when config.toml is fully populated
// with all documented fields including [[oauth.providers]], LoadConfig parses
// all values correctly.
// TS-01-5, REQ: 01-REQ-1.5
func TestSpec01_ConfigFullParse(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[server]
port = 9090
bind = "127.0.0.1"
external_url = "https://example.com"

[database]
path = "./custom/db.db"

[log]
level = "debug"

[[oauth.providers]]
name = "github"
client_id = "abc"
client_secret = "xyz"
authorize_url = "https://github.com/login/oauth/authorize"
token_url = "https://github.com/login/oauth/access_token"
userinfo_url = "https://api.github.com/user"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	result, err := serverconfig.LoadConfig(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if result == nil || result.Config == nil {
		t.Fatal("LoadConfig should return non-nil result and config")
	}

	cfg := result.Config
	if cfg.Server.Port != 9090 {
		t.Errorf("Server.Port = %d, want 9090", cfg.Server.Port)
	}
	if cfg.Server.Bind != "127.0.0.1" {
		t.Errorf("Server.Bind = %q, want %q", cfg.Server.Bind, "127.0.0.1")
	}
	if cfg.Server.ExternalURL != "https://example.com" {
		t.Errorf("Server.ExternalURL = %q, want %q", cfg.Server.ExternalURL, "https://example.com")
	}
	if cfg.Database.Path != "./custom/db.db" {
		t.Errorf("Database.Path = %q, want %q", cfg.Database.Path, "./custom/db.db")
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("Log.Level = %q, want %q", cfg.Log.Level, "debug")
	}
	if len(cfg.OAuth.Providers) != 1 {
		t.Fatalf("OAuth.Providers length = %d, want 1", len(cfg.OAuth.Providers))
	}

	p := cfg.OAuth.Providers[0]
	if p.Name != "github" {
		t.Errorf("Provider.Name = %q, want %q", p.Name, "github")
	}
	if p.ClientID != "abc" {
		t.Errorf("Provider.ClientID = %q, want %q", p.ClientID, "abc")
	}
	if p.ClientSecret != "xyz" {
		t.Errorf("Provider.ClientSecret = %q, want %q", p.ClientSecret, "xyz")
	}
	if p.AuthorizeURL != "https://github.com/login/oauth/authorize" {
		t.Errorf("Provider.AuthorizeURL = %q, want %q", p.AuthorizeURL, "https://github.com/login/oauth/authorize")
	}
	if p.TokenURL != "https://github.com/login/oauth/access_token" {
		t.Errorf("Provider.TokenURL = %q, want %q", p.TokenURL, "https://github.com/login/oauth/access_token")
	}
	if p.UserinfoURL != "https://api.github.com/user" {
		t.Errorf("Provider.UserinfoURL = %q, want %q", p.UserinfoURL, "https://api.github.com/user")
	}
}

// ---------------------------------------------------------------------------
// 1.2 — Config Parse Errors and Unknown Field Warnings
// ---------------------------------------------------------------------------

// TestSpec01_ConfigInvalidTOML verifies that LoadConfig returns an error when
// config.toml exists but contains invalid TOML syntax (e.g., unclosed bracket).
// TS-01-2, REQ: 01-REQ-1.2
func TestSpec01_ConfigInvalidTOML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	// Malformed TOML: unclosed section header.
	if err := os.WriteFile(configPath, []byte("[server\nport = 8080"), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	_, err := serverconfig.LoadConfig(configPath, false)
	if err == nil {
		t.Fatal("LoadConfig should return error for invalid TOML syntax, got nil")
	}
}

// TestSpec01_ConfigUnrecognizedFields verifies that LoadConfig reports
// unrecognized field names in the config.toml without causing a fatal error.
// Each unrecognized field should appear in LoadResult.UnrecognizedKeys.
// TS-01-3, REQ: 01-REQ-1.3
func TestSpec01_ConfigUnrecognizedFields(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	// Config with typo section [servr] and unknown key foo.
	content := `[servr]
port = 9090
foo = "bar"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	result, err := serverconfig.LoadConfig(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfig should not error on unrecognized fields: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}

	// Unrecognized keys should be reported.
	if len(result.UnrecognizedKeys) == 0 {
		t.Error("UnrecognizedKeys should contain entries for unrecognized fields [servr], servr.port, servr.foo")
	}

	// Verify at least the typo section is detected.
	foundServr := false
	for _, key := range result.UnrecognizedKeys {
		if strings.Contains(key, "servr") {
			foundServr = true
			break
		}
	}
	if !foundServr {
		t.Errorf("UnrecognizedKeys = %v, expected to contain an entry referencing 'servr'", result.UnrecognizedKeys)
	}

	// Server defaults should still be applied (unrecognized fields don't
	// affect recognized defaults).
	if result.Config.Server.Port != 8080 {
		t.Errorf("Server.Port = %d, want 8080 (default, since [servr] is unrecognized)", result.Config.Server.Port)
	}
}

// TestSpec01_ConfigInvalidLogLevel verifies that when config.toml specifies a
// log.level value not in {trace,debug,info,warn,error,fatal,panic}, LoadConfig
// adds a warning with the invalid_value field and defaults log.level to "info".
// TS-01-4, REQ: 01-REQ-1.4
func TestSpec01_ConfigInvalidLogLevel(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[log]
level = "verbose"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	result, err := serverconfig.LoadConfig(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfig should not error on invalid log level: %v", err)
	}
	if result == nil || result.Config == nil {
		t.Fatal("result and config should not be nil")
	}

	// Log level should default to "info" when an invalid value is provided.
	if result.Config.Log.Level != "info" {
		t.Errorf("Log.Level = %q, want %q (should default to info for invalid value)", result.Config.Log.Level, "info")
	}

	// A warning about the invalid log level should be present.
	if len(result.Warnings) == 0 {
		t.Error("Warnings should contain an entry for the invalid log.level value")
	}

	foundInvalidValue := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "verbose") {
			foundInvalidValue = true
			break
		}
	}
	if !foundInvalidValue {
		t.Errorf("Warnings = %v, expected to contain a warning mentioning 'verbose'", result.Warnings)
	}
}

// TestSpec01_ConfigMissingFileNonFatal verifies that a missing config.toml
// is treated as non-fatal: all defaults are applied and no error is returned.
// TS-01-E1, REQ: 01-REQ-1.E1
func TestSpec01_ConfigMissingFileNonFatal(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "nonexistent", "config.toml")
	// Path does not exist and parent dir doesn't exist either.

	result, err := serverconfig.LoadConfig(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfig should treat missing config as non-fatal, got error: %v", err)
	}
	if result == nil || result.Config == nil {
		t.Fatal("result and config should not be nil")
	}

	// All defaults should be applied.
	cfg := result.Config
	if cfg.Server.Port != 8080 {
		t.Errorf("Server.Port = %d, want 8080", cfg.Server.Port)
	}
	if cfg.Server.Bind != "0.0.0.0" {
		t.Errorf("Server.Bind = %q, want %q", cfg.Server.Bind, "0.0.0.0")
	}
	if cfg.Database.Path != "./data/af-hub.db" {
		t.Errorf("Database.Path = %q, want %q", cfg.Database.Path, "./data/af-hub.db")
	}
	if cfg.Log.Level != "info" {
		t.Errorf("Log.Level = %q, want %q", cfg.Log.Level, "info")
	}
}

// ---------------------------------------------------------------------------
// 1.3 — Admin Token File Directory Anchoring and Startup Info Log
// ---------------------------------------------------------------------------

// TestSpec01_AdminTokenDirWithoutConfigFlag verifies that when no --config
// flag is provided (i.e., config.toml is loaded from the current working
// directory), LoadResult.ConfigDir is set to the current working directory.
// The admin_token file should be written to this directory.
// TS-01-E2, REQ: 01-REQ-1.E2
func TestSpec01_AdminTokenDirWithoutConfigFlag(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	// No config file created — simulates no --config flag with CWD = tmpDir.

	result, err := serverconfig.LoadConfig(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfig should not error: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}

	// ConfigDir should be the directory containing the config path.
	// When config.toml is at tmpDir/config.toml, ConfigDir should be tmpDir.
	if result.ConfigDir != tmpDir {
		t.Errorf("ConfigDir = %q, want %q (directory of config file path)", result.ConfigDir, tmpDir)
	}
}

// TestSpec01_AdminTokenDirWithConfigFlag verifies that when --config
// /some/path/config.toml is provided, LoadResult.ConfigDir is set to
// /some/path/ (the directory containing the specified config file).
// TS-01-E2, REQ: 01-REQ-1.E2
func TestSpec01_AdminTokenDirWithConfigFlag(t *testing.T) {
	tmpDir := t.TempDir()
	customDir := filepath.Join(tmpDir, "custom", "confdir")
	if err := os.MkdirAll(customDir, 0755); err != nil {
		t.Fatalf("failed to create custom dir: %v", err)
	}

	configPath := filepath.Join(customDir, "config.toml")
	content := `[server]
port = 9191
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	result, err := serverconfig.LoadConfig(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfig should not error: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}

	// ConfigDir should be the directory of the --config file.
	if result.ConfigDir != customDir {
		t.Errorf("ConfigDir = %q, want %q (directory of --config path)", result.ConfigDir, customDir)
	}
}

// TestSpec01_StartupInfoLogEntry verifies that StartupLogFields returns a map
// with all required fields for the "server starting" log entry:
//   - bind (string)
//   - port (int, not string)
//   - db_path (string)
//   - log_level (string)
//   - msg = "server starting"
//
// TS-01-7, REQ: 01-REQ-2.2
func TestSpec01_StartupInfoLogEntry(t *testing.T) {
	cfg := &serverconfig.Config{
		Server: serverconfig.ServerConfig{
			Port: 8080,
			Bind: "0.0.0.0",
		},
		Database: serverconfig.DatabaseConfig{
			Path: "./data/af-hub.db",
		},
		Log: serverconfig.LogConfig{
			Level: "info",
		},
	}

	fields := serverconfig.StartupLogFields(cfg)
	if fields == nil {
		t.Fatal("StartupLogFields returned nil, want non-nil map with startup fields")
	}

	// Check bind field.
	if bind, ok := fields["bind"]; !ok {
		t.Error("StartupLogFields missing 'bind' field")
	} else if bind != "0.0.0.0" {
		t.Errorf("bind = %v, want %q", bind, "0.0.0.0")
	}

	// Check port field — must be integer, not string.
	if port, ok := fields["port"]; !ok {
		t.Error("StartupLogFields missing 'port' field")
	} else {
		switch p := port.(type) {
		case int:
			if p != 8080 {
				t.Errorf("port = %d, want 8080", p)
			}
		default:
			t.Errorf("port should be int, got %T (%v)", port, port)
		}
	}

	// Check db_path field.
	if dbPath, ok := fields["db_path"]; !ok {
		t.Error("StartupLogFields missing 'db_path' field")
	} else if dbPath != "./data/af-hub.db" {
		t.Errorf("db_path = %v, want %q", dbPath, "./data/af-hub.db")
	}

	// Check log_level field.
	if logLevel, ok := fields["log_level"]; !ok {
		t.Error("StartupLogFields missing 'log_level' field")
	} else if logLevel != "info" {
		t.Errorf("log_level = %v, want %q", logLevel, "info")
	}

	// Check msg field.
	if msg, ok := fields["msg"]; !ok {
		t.Error("StartupLogFields missing 'msg' field")
	} else if msg != "server starting" {
		t.Errorf("msg = %v, want %q", msg, "server starting")
	}
}

// TestSpec01_StartupInfoLogFieldTypes verifies that the startup log entry
// emits port as an integer type (not a string), as required by REQ-2.2.
// This is a focused type assertion complementing TestSpec01_StartupInfoLogEntry.
// TS-01-7, REQ: 01-REQ-2.2
func TestSpec01_StartupInfoLogFieldTypes(t *testing.T) {
	cfg := &serverconfig.Config{
		Server: serverconfig.ServerConfig{
			Port: 9090,
			Bind: "127.0.0.1",
		},
		Database: serverconfig.DatabaseConfig{
			Path: "./custom/db.db",
		},
		Log: serverconfig.LogConfig{
			Level: "debug",
		},
	}

	fields := serverconfig.StartupLogFields(cfg)
	if fields == nil {
		t.Fatal("StartupLogFields returned nil")
	}

	// Port must be an integer for proper JSON serialization.
	port := fields["port"]
	if _, ok := port.(int); !ok {
		t.Errorf("port field type = %T, want int (for correct JSON serialization)", port)
	}

	// All string fields must be strings.
	for _, key := range []string{"bind", "db_path", "log_level", "msg"} {
		val := fields[key]
		if _, ok := val.(string); !ok {
			t.Errorf("%s field type = %T, want string", key, val)
		}
	}
}

// ---------------------------------------------------------------------------
// XDG Base Directory support
// ---------------------------------------------------------------------------

func TestDefaultConfigPath_WithXDGEnv(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/config")
	got := serverconfig.DefaultConfigPath()
	want := filepath.Join("/custom/config", "af-hub", "config.toml")
	if got != want {
		t.Errorf("DefaultConfigPath() = %q, want %q", got, want)
	}
}

func TestDefaultConfigPath_FallsBackToHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	got := serverconfig.DefaultConfigPath()
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".config", "af-hub", "config.toml")
	if got != want {
		t.Errorf("DefaultConfigPath() = %q, want %q", got, want)
	}
}

func TestDefaultDataDir_WithXDGEnv(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/custom/data")
	got := serverconfig.DefaultDataDir()
	want := filepath.Join("/custom/data", "af-hub")
	if got != want {
		t.Errorf("DefaultDataDir() = %q, want %q", got, want)
	}
}

func TestDefaultDataDir_FallsBackToHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	got := serverconfig.DefaultDataDir()
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".local", "share", "af-hub")
	if got != want {
		t.Errorf("DefaultDataDir() = %q, want %q", got, want)
	}
}

func TestLoadConfig_UseXDG_DefaultsDatabasePath(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/xdg/data")
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	// No config file — LoadConfig will apply defaults.

	result, err := serverconfig.LoadConfig(configPath, true)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}

	want := filepath.Join("/xdg/data", "af-hub", "af-hub.db")
	if result.Config.Database.Path != want {
		t.Errorf("Database.Path = %q, want %q", result.Config.Database.Path, want)
	}
}

func TestLoadConfig_NoXDG_DefaultsCWDRelative(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	result, err := serverconfig.LoadConfig(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}

	if result.Config.Database.Path != "./data/af-hub.db" {
		t.Errorf("Database.Path = %q, want %q", result.Config.Database.Path, "./data/af-hub.db")
	}
}

func TestLoadConfig_UseXDG_ConfigOverridesDefault(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/xdg/data")
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	content := `[database]
path = "/explicit/path/db.sqlite"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	result, err := serverconfig.LoadConfig(configPath, true)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}

	if result.Config.Database.Path != "/explicit/path/db.sqlite" {
		t.Errorf("Database.Path = %q, want explicit override", result.Config.Database.Path)
	}
}
