package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TS-01-6: Verify that the configuration loader reads config.toml and applies
// the correct defaults for all optional fields.
func TestLoadConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	// Minimal config.toml with no optional fields set.
	err := os.WriteFile(cfgPath, []byte("# minimal config\n"), 0644)
	if err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig returned unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("LoadConfig returned nil config")
	}

	if cfg.Server.Port != 8080 {
		t.Errorf("expected default port 8080, got %d", cfg.Server.Port)
	}
	if cfg.Server.BindAddress != "0.0.0.0" {
		t.Errorf("expected default bind_address '0.0.0.0', got %q", cfg.Server.BindAddress)
	}
	if cfg.Database.Path != "./data/af-hub.db" {
		t.Errorf("expected default database.path './data/af-hub.db', got %q", cfg.Database.Path)
	}
	if cfg.Logging.Level != "info" {
		t.Errorf("expected default logging.level 'info', got %q", cfg.Logging.Level)
	}
}

// TS-01-7: Verify that the configuration validator accepts all valid log levels
// and rejects an out-of-range port.
func TestValidateConfig_InvalidPort(t *testing.T) {
	cfg := &Config{
		Server: ServerConfig{
			Port:        70000,
			BindAddress: "0.0.0.0",
		},
		Database: DatabaseConfig{Path: "./data/af-hub.db"},
		Logging:  LoggingConfig{Level: "info"},
	}
	err := ValidateConfig(cfg)
	if err == nil {
		t.Fatal("expected validation error for port 70000, got nil")
	}
	if errMsg := err.Error(); !contains(errMsg, "port") {
		t.Errorf("error should mention 'port', got: %s", errMsg)
	}
}

func TestValidateConfig_InvalidLogLevel(t *testing.T) {
	cfg := &Config{
		Server: ServerConfig{
			Port:        8080,
			BindAddress: "0.0.0.0",
		},
		Database: DatabaseConfig{Path: "./data/af-hub.db"},
		Logging:  LoggingConfig{Level: "verbose"},
	}
	err := ValidateConfig(cfg)
	if err == nil {
		t.Fatal("expected validation error for log level 'verbose', got nil")
	}
	if errMsg := err.Error(); !contains(errMsg, "level") {
		t.Errorf("error should mention 'level', got: %s", errMsg)
	}
}

func TestValidateConfig_ValidLogLevels(t *testing.T) {
	validLevels := []string{"trace", "debug", "info", "warn", "error", "fatal", "panic"}
	for _, level := range validLevels {
		t.Run(level, func(t *testing.T) {
			cfg := &Config{
				Server: ServerConfig{
					Port:        8080,
					BindAddress: "0.0.0.0",
				},
				Database: DatabaseConfig{Path: "./data/af-hub.db"},
				Logging:  LoggingConfig{Level: level},
			}
			err := ValidateConfig(cfg)
			if err != nil {
				t.Errorf("valid log level %q should not produce an error, got: %v", level, err)
			}
		})
	}
}

func TestValidateConfig_PortBoundary(t *testing.T) {
	tests := []struct {
		name    string
		port    int
		wantErr bool
	}{
		{"port 0", 0, true},
		{"port 1", 1, false},
		{"port 65535", 65535, false},
		{"port 65536", 65536, true},
		{"port -1", -1, true},
		{"port 99999", 99999, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Server: ServerConfig{
					Port:        tc.port,
					BindAddress: "0.0.0.0",
				},
				Database: DatabaseConfig{Path: "./data/af-hub.db"},
				Logging:  LoggingConfig{Level: "info"},
			}
			err := ValidateConfig(cfg)
			if tc.wantErr && err == nil {
				t.Errorf("expected error for port %d, got nil", tc.port)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error for port %d: %v", tc.port, err)
			}
		})
	}
}

func TestValidateConfig_MissingOAuthFields(t *testing.T) {
	tests := []struct {
		name    string
		oauth   OAuthProviderConfig
		wantErr bool
	}{
		{
			"all required fields present",
			OAuthProviderConfig{
				Provider:     "github",
				ClientID:     "client123",
				ClientSecret: "secret456",
			},
			false,
		},
		{
			"missing provider",
			OAuthProviderConfig{
				ClientID:     "client123",
				ClientSecret: "secret456",
			},
			true,
		},
		{
			"missing client_id",
			OAuthProviderConfig{
				Provider:     "github",
				ClientSecret: "secret456",
			},
			true,
		},
		{
			"missing client_secret",
			OAuthProviderConfig{
				Provider: "github",
				ClientID: "client123",
			},
			true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Server: ServerConfig{
					Port:        8080,
					BindAddress: "0.0.0.0",
				},
				Database: DatabaseConfig{Path: "./data/af-hub.db"},
				Logging:  LoggingConfig{Level: "info"},
				Auth:     AuthConfig{OAuth: []OAuthProviderConfig{tc.oauth}},
			}
			err := ValidateConfig(cfg)
			if tc.wantErr && err == nil {
				t.Errorf("expected validation error for %s, got nil", tc.name)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error for %s: %v", tc.name, err)
			}
		})
	}
}

// TS-01-8: Verify that the server creates the database parent directory
// automatically when it does not exist.
func TestEnsureDataDir_CreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "nonexistent", "nested", "af-hub.db")

	err := EnsureDataDir(dbPath)
	if err != nil {
		t.Fatalf("EnsureDataDir returned error: %v", err)
	}

	parentDir := filepath.Dir(dbPath)
	info, err := os.Stat(parentDir)
	if err != nil {
		t.Fatalf("parent directory was not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("parent path is not a directory")
	}
}

// TS-01-9: Verify that when external_url is set in config.toml, the value is
// loaded and accessible.
func TestLoadConfig_ExternalURL(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	content := `[server]
external_url = "https://example.com"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg == nil {
		t.Fatal("LoadConfig returned nil config")
	}
	if cfg.Server.ExternalURL != "https://example.com" {
		t.Errorf("expected external_url 'https://example.com', got %q", cfg.Server.ExternalURL)
	}

	// Validate should pass — external_url has no strict validation.
	if err := ValidateConfig(cfg); err != nil {
		t.Errorf("ValidateConfig should accept config with external_url, got: %v", err)
	}
}

// TS-01-E2: Verify that the server logs a fatal error and exits with non-zero
// code when config.toml is absent.
func TestLoadConfig_MissingFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml") // Does not exist.

	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error when config.toml is missing, got nil")
	}
}

// TS-01-E3: Verify that the server logs a descriptive fatal error identifying
// the invalid field when config.toml contains an out-of-range port.
func TestValidateConfig_OutOfRangePort(t *testing.T) {
	cfg := &Config{
		Server: ServerConfig{
			Port:        99999,
			BindAddress: "0.0.0.0",
		},
		Database: DatabaseConfig{Path: "./data/af-hub.db"},
		Logging:  LoggingConfig{Level: "info"},
	}
	err := ValidateConfig(cfg)
	if err == nil {
		t.Fatal("expected validation error for port 99999, got nil")
	}
	if errMsg := err.Error(); !contains(errMsg, "port") {
		t.Errorf("error message should mention 'port', got: %s", errMsg)
	}
}

// TS-01-E4: Verify that the server logs a fatal error with the OS error and
// exits non-zero when the data directory cannot be created due to permission.
func TestEnsureDataDir_PermissionError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}

	// Create a read-only directory to prevent subdirectory creation.
	dir := t.TempDir()
	readOnlyDir := filepath.Join(dir, "readonly")
	if err := os.Mkdir(readOnlyDir, 0555); err != nil {
		t.Fatalf("failed to create read-only dir: %v", err)
	}
	// Ensure cleanup can remove it.
	t.Cleanup(func() { os.Chmod(readOnlyDir, 0755) })

	dbPath := filepath.Join(readOnlyDir, "subdir", "af-hub.db")
	err := EnsureDataDir(dbPath)
	if err == nil {
		t.Fatal("expected permission error from EnsureDataDir, got nil")
	}
}

// contains is a helper to check for case-insensitive substring match.
func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 &&
		(len(s) >= len(substr)) &&
		containsCI(s, substr)
}

func containsCI(s, substr string) bool {
	s = lower(s)
	substr = lower(substr)
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func lower(s string) string {
	b := make([]byte, len(s))
	for i := range s {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}
