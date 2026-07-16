package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-fox-dev/hub/internal/serverconfig"
)

// ---------------------------------------------------------------------------
// 1.4 — Startup Sequence Ordering Integration Tests
// ---------------------------------------------------------------------------

// TestSpec01_StartupSequenceConfigLoading verifies that the configuration
// loading step (step 2 of the startup sequence) correctly applies defaults
// when no config.toml is present, as part of the strict sequential
// initialization order.
//
// When the full startup pipeline is implemented (groups 9-14), this test
// should be extended to verify that each step completes before the next:
//   Step 1: parse CLI flags
//   Step 2: load config.toml
//   Step 3: initialize structured logging
//   Step 4: open/initialize SQLite
//   Step 5: run admin bootstrap or token validation
//   Step 6: register HTTP routes and middleware
//   Step 7: log startup info
//   Step 8: start HTTP listener
//   Step 9: arm SIGTERM/SIGINT handler
//
// TS-01-6, REQ: 01-REQ-2.1
func TestSpec01_StartupSequenceConfigLoading(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")
	// No config file — simulates a clean first-boot environment.

	// Step 2: Load configuration.
	result, err := serverconfig.LoadConfig(configPath, false)
	if err != nil {
		t.Fatalf("Step 2 (config loading) should succeed with missing file: %v", err)
	}
	if result == nil || result.Config == nil {
		t.Fatal("Step 2 should return non-nil config with defaults")
	}

	// Verify defaults are applied (prerequisite for subsequent steps).
	cfg := result.Config
	if cfg.Server.Port != 8080 {
		t.Errorf("Default port = %d, want 8080", cfg.Server.Port)
	}
	if cfg.Server.Bind != "0.0.0.0" {
		t.Errorf("Default bind = %q, want %q", cfg.Server.Bind, "0.0.0.0")
	}
	if cfg.Database.Path != "./data/af-hub.db" {
		t.Errorf("Default db_path = %q, want %q", cfg.Database.Path, "./data/af-hub.db")
	}
	if cfg.Log.Level != "info" {
		t.Errorf("Default log_level = %q, want %q", cfg.Log.Level, "info")
	}

	// Verify ConfigDir is set for admin bootstrap (step 5).
	if result.ConfigDir == "" {
		t.Error("ConfigDir should be set after config loading for admin bootstrap to use")
	}
}

// TestSpec01_StartupSequenceWithCustomConfig verifies that the config loading
// step correctly parses a custom config.toml with non-default values.
// This tests step 2 of the startup sequence with a real config file.
// TS-01-6, REQ: 01-REQ-2.1
func TestSpec01_StartupSequenceWithCustomConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[server]
port = 9090
bind = "127.0.0.1"

[database]
path = "./testdata/test.db"

[log]
level = "debug"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	result, err := serverconfig.LoadConfig(configPath, false)
	if err != nil {
		t.Fatalf("Config loading should succeed: %v", err)
	}

	cfg := result.Config
	if cfg.Server.Port != 9090 {
		t.Errorf("Server.Port = %d, want 9090", cfg.Server.Port)
	}
	if cfg.Server.Bind != "127.0.0.1" {
		t.Errorf("Server.Bind = %q, want %q", cfg.Server.Bind, "127.0.0.1")
	}
	if cfg.Database.Path != "./testdata/test.db" {
		t.Errorf("Database.Path = %q, want %q", cfg.Database.Path, "./testdata/test.db")
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("Log.Level = %q, want %q", cfg.Log.Level, "debug")
	}
}

// TestSpec01_FatalInitErrorInvalidTOML verifies that a fatal error during
// initialization step 2 (config loading) causes the config loader to return
// an error. In the full server binary, this would cause exit with code 1
// before the HTTP listener opens.
// TS-01-E3, REQ: 01-REQ-2.E1
func TestSpec01_FatalInitErrorInvalidTOML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	// Write invalid TOML to trigger a fatal config loading error.
	if err := os.WriteFile(configPath, []byte("[server\nport = 8080"), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	_, err := serverconfig.LoadConfig(configPath, false)
	if err == nil {
		t.Fatal("LoadConfig should return error for invalid TOML (fatal init error at step 2); got nil")
	}
}

// TestSpec01_FatalInitErrorUnwritableDBPath verifies that when the
// config specifies a database path whose parent directory cannot be created,
// the initialization sequence should fail. This tests step 4 behavior
// from the config perspective — the actual DB creation failure is tested
// in task group 2 (internal/db).
//
// For now, this test verifies that LoadConfig successfully parses a config
// with a problematic DB path (config loading itself doesn't validate the path).
// The fatal error occurs later in the DB initialization step.
// TS-01-E3, REQ: 01-REQ-2.E1
func TestSpec01_FatalInitErrorUnwritableDBPath(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	content := `[database]
path = "/root/noperm/af-hub.db"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	result, err := serverconfig.LoadConfig(configPath, false)
	if err != nil {
		t.Fatalf("Config loading should succeed (DB path validation is in step 4): %v", err)
	}

	// The path should be parsed as-is from the config file.
	if result.Config.Database.Path != "/root/noperm/af-hub.db" {
		t.Errorf("Database.Path = %q, want %q", result.Config.Database.Path, "/root/noperm/af-hub.db")
	}
}

// TestSpec01_StartupInfoLogFieldsPresent verifies that StartupLogFields
// returns all required fields for the startup info log entry emitted
// at step 7 of the startup sequence.
// TS-01-7 (integration context), REQ: 01-REQ-2.2
func TestSpec01_StartupInfoLogFieldsPresent(t *testing.T) {
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
		t.Fatal("StartupLogFields returned nil; expected a map with bind, port, db_path, log_level, msg")
	}

	requiredKeys := []string{"bind", "port", "db_path", "log_level", "msg"}
	for _, key := range requiredKeys {
		if _, ok := fields[key]; !ok {
			t.Errorf("StartupLogFields missing required key %q", key)
		}
	}

	if msg, ok := fields["msg"]; ok && msg != "server starting" {
		t.Errorf("msg = %v, want %q", msg, "server starting")
	}
}
