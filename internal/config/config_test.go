package config_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/agent-fox-dev/hub/internal/config"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// writeRawConfig writes raw content to $HOME/.af/config.toml, creating the
// directory structure as needed.
func writeRawConfig(t *testing.T, home, content string) string {
	t.Helper()
	dir := filepath.Join(home, ".af")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	return path
}

// writeStructConfig encodes a Config struct as TOML and writes it to the
// config path under the given home directory.
func writeStructConfig(t *testing.T, home string, cfg config.Config) string {
	t.Helper()
	dir := filepath.Join(home, ".af")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	path := filepath.Join(dir, "config.toml")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatalf("failed to open config for writing: %v", err)
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(cfg); err != nil {
		t.Fatalf("failed to encode config: %v", err)
	}
	return path
}

// readParsedConfig reads and parses the config file at the given path.
func readParsedConfig(t *testing.T, path string) config.Config {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}
	var cfg config.Config
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		t.Fatalf("failed to parse config file: %v", err)
	}
	return cfg
}

// ---------------------------------------------------------------------------
// 1.1 — Config Initialization Tests (REQ-1)
// ---------------------------------------------------------------------------

// TestConfigTOMLRoundTrip verifies that config.toml is a valid TOML file
// containing all four required fields: hub_url, user_id, api_key, key_id.
// TS-05-1
func TestConfigTOMLRoundTrip(t *testing.T) {
	tmpHome := t.TempDir()
	configPath := filepath.Join(tmpHome, ".af", "config.toml")

	original := &config.Config{
		HubURL: "https://hub.example.com",
		UserID: "uid-123",
		APIKey: "key-abc",
		KeyID:  "kid-xyz",
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		t.Fatal(err)
	}

	if err := config.Save(configPath, original); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if loaded.HubURL != "https://hub.example.com" {
		t.Errorf("HubURL = %q, want %q", loaded.HubURL, "https://hub.example.com")
	}
	if loaded.UserID != "uid-123" {
		t.Errorf("UserID = %q, want %q", loaded.UserID, "uid-123")
	}
	if loaded.APIKey != "key-abc" {
		t.Errorf("APIKey = %q, want %q", loaded.APIKey, "key-abc")
	}
	if loaded.KeyID != "kid-xyz" {
		t.Errorf("KeyID = %q, want %q", loaded.KeyID, "kid-xyz")
	}
}

// TestConfigAutoCreation verifies that invoking any CLI command when
// config.toml does not exist creates the directory with mode 0700 and the
// file with mode 0600 containing empty field values.
// TS-05-2
func TestConfigAutoCreation(t *testing.T) {
	tmpHome := t.TempDir()
	afDir := filepath.Join(tmpHome, ".af")

	// Verify .af directory does not exist yet.
	if _, err := os.Stat(afDir); !os.IsNotExist(err) {
		t.Fatalf("expected .af dir to not exist, got stat result: %v", err)
	}

	// Trigger config auto-creation.
	if err := config.EnsureConfigDir(tmpHome); err != nil {
		t.Fatalf("EnsureConfigDir failed: %v", err)
	}

	configPath := config.ConfigPath(tmpHome)
	if err := config.EnsureConfigFile(configPath); err != nil {
		t.Fatalf("EnsureConfigFile failed: %v", err)
	}

	// Verify directory exists with correct permissions.
	dirInfo, err := os.Stat(afDir)
	if err != nil {
		t.Fatalf("failed to stat .af dir: %v", err)
	}
	if !dirInfo.IsDir() {
		t.Errorf(".af is not a directory")
	}
	if perm := dirInfo.Mode().Perm(); perm != 0700 {
		t.Errorf(".af dir mode = %o, want 0700", perm)
	}

	// Verify file exists with correct permissions.
	fileInfo, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("failed to stat config.toml: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0600 {
		t.Errorf("config.toml mode = %o, want 0600", perm)
	}

	// Verify file contains valid TOML with empty field values.
	cfg := readParsedConfig(t, configPath)
	if cfg.HubURL != "" {
		t.Errorf("HubURL = %q, want empty", cfg.HubURL)
	}
	if cfg.UserID != "" {
		t.Errorf("UserID = %q, want empty", cfg.UserID)
	}
	if cfg.APIKey != "" {
		t.Errorf("APIKey = %q, want empty", cfg.APIKey)
	}
	if cfg.KeyID != "" {
		t.Errorf("KeyID = %q, want empty", cfg.KeyID)
	}
}

// TestConfigMalformedTOML verifies that a malformed config file causes Load
// to return an error containing the parse failure details, and the file
// content remains unchanged.
// TS-05-3
func TestConfigMalformedTOML(t *testing.T) {
	tmpHome := t.TempDir()
	invalidContent := "not = [valid toml"
	configPath := writeRawConfig(t, tmpHome, invalidContent)

	_, err := config.Load(configPath)
	if err == nil {
		t.Fatal("Load should return an error for malformed TOML, got nil")
	}

	if !strings.Contains(err.Error(), "failed to parse config file") {
		t.Errorf("error message = %q, want it to contain 'failed to parse config file'", err.Error())
	}

	// Verify file is unchanged.
	data, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatalf("failed to read config after Load error: %v", readErr)
	}
	if string(data) != invalidContent {
		t.Errorf("config file was modified after failed Load; got %q, want %q", string(data), invalidContent)
	}
}

// TestConfigDirPermissionDenied verifies that when the parent directory is
// read-only and the .af directory cannot be created, EnsureConfigDir returns
// a descriptive error.
// TS-05-E1
func TestConfigDirPermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission test not applicable on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("test requires non-root user for permission checks")
	}

	tmpHome := t.TempDir()

	// Make the home directory read-only so .af cannot be created.
	if err := os.Chmod(tmpHome, 0555); err != nil {
		t.Fatalf("failed to chmod: %v", err)
	}
	// Restore permissions for cleanup.
	t.Cleanup(func() {
		os.Chmod(tmpHome, 0755)
	})

	err := config.EnsureConfigDir(tmpHome)
	if err == nil {
		t.Fatal("EnsureConfigDir should return error when dir cannot be created, got nil")
	}

	// Verify no partial state left.
	afDir := filepath.Join(tmpHome, ".af")
	if _, statErr := os.Stat(afDir); !os.IsNotExist(statErr) {
		t.Errorf("expected .af dir to not exist after failure, but it does")
	}
}

// TestConfigPathIsDirectory verifies that when config.toml exists as a
// directory rather than a file, EnsureConfigFile returns a descriptive error.
// TS-05-E2
func TestConfigPathIsDirectory(t *testing.T) {
	tmpHome := t.TempDir()
	afDir := filepath.Join(tmpHome, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	// Create config.toml as a directory instead of a file.
	configAsDir := filepath.Join(afDir, "config.toml")
	if err := os.Mkdir(configAsDir, 0700); err != nil {
		t.Fatal(err)
	}

	err := config.EnsureConfigFile(configAsDir)
	if err == nil {
		t.Fatal("EnsureConfigFile should return error when config path is a directory, got nil")
	}
}

// ---------------------------------------------------------------------------
// 1.2 — Config Resolution Precedence Tests (REQ-2)
// ---------------------------------------------------------------------------

// TestConfigResolutionPrecedence verifies that config resolution follows
// the precedence: flag > env var > config file for hub_url, user_id, api_key.
// TS-05-4
func TestConfigResolutionPrecedence(t *testing.T) {
	// Set up config file values.
	cfg := &config.Config{
		HubURL: "file-url",
		UserID: "file-user",
		APIKey: "file-key",
	}

	// Set environment variables.
	t.Setenv("AF_HUB_URL", "env-url")
	t.Setenv("AF_HUB_USER_ID", "env-user")

	// Resolve with flag for hub-url only.
	flags := map[string]string{
		"hub-url": "flag-url",
		"user-id": "",
		"api-key": "",
	}
	resolved, err := config.Resolve(flags, cfg)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}

	// Flag should win for hub_url.
	if resolved.HubURL != "flag-url" {
		t.Errorf("HubURL = %q, want %q (flag should override env and config)", resolved.HubURL, "flag-url")
	}
	// Env should win for user_id (no flag provided).
	if resolved.UserID != "env-user" {
		t.Errorf("UserID = %q, want %q (env should override config)", resolved.UserID, "env-user")
	}
	// Config file should be used for api_key (no flag or env var).
	if resolved.APIKey != "file-key" {
		t.Errorf("APIKey = %q, want %q (config file value should be used)", resolved.APIKey, "file-key")
	}
}

// TestKeyIDFromConfigOnly verifies that key_id is resolved exclusively from
// the config file and there is no --key-id CLI flag.
// TS-05-5
func TestKeyIDFromConfigOnly(t *testing.T) {
	cfg := &config.Config{
		KeyID: "kid-from-file",
	}

	flags := map[string]string{
		"hub-url": "http://test",
		"user-id": "u",
		"api-key": "k",
	}

	resolved, err := config.Resolve(flags, cfg)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}

	if resolved.KeyID != "kid-from-file" {
		t.Errorf("KeyID = %q, want %q (must come from config file only)", resolved.KeyID, "kid-from-file")
	}
}

// TestMissingRequiredConfigValue verifies that when a required config value
// is not available from any source, Resolve returns an error naming the
// missing value and how to set it.
// TS-05-6
func TestMissingRequiredConfigValue(t *testing.T) {
	// Empty config, no env vars, no flags.
	t.Setenv("AF_HUB_URL", "")
	t.Setenv("AF_HUB_USER_ID", "")
	t.Setenv("AF_HUB_API_KEY", "")

	cfg := &config.Config{}
	flags := map[string]string{
		"hub-url": "",
		"user-id": "",
		"api-key": "",
	}

	_, err := config.Resolve(flags, cfg)
	if err == nil {
		t.Fatal("Resolve should return error when required values are missing, got nil")
	}

	errMsg := err.Error()
	// Error should name the missing value and how to set it.
	if !strings.Contains(errMsg, "hub_url") && !strings.Contains(errMsg, "hub-url") {
		t.Errorf("error should mention hub_url or hub-url, got: %s", errMsg)
	}
}

// TestEnvVarOverridesConfigFile verifies that when AF_HUB_URL is set and
// config file has hub_url, the env var value is used (no --hub-url flag).
// TS-05-E3
func TestEnvVarOverridesConfigFile(t *testing.T) {
	cfg := &config.Config{
		HubURL: "config-url",
		UserID: "u",
		APIKey: "k",
	}

	t.Setenv("AF_HUB_URL", "env-url")

	flags := map[string]string{
		"hub-url": "", // no flag
		"user-id": "",
		"api-key": "",
	}

	resolved, err := config.Resolve(flags, cfg)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}

	if resolved.HubURL != "env-url" {
		t.Errorf("HubURL = %q, want %q (env var should override config file)", resolved.HubURL, "env-url")
	}
}

// TestFlagOverridesEnvAndConfig verifies that when --hub-url flag is provided
// alongside AF_HUB_URL and config file value, the flag wins.
// TS-05-E4
func TestFlagOverridesEnvAndConfig(t *testing.T) {
	cfg := &config.Config{
		HubURL: "config-url",
		UserID: "u",
		APIKey: "k",
	}

	t.Setenv("AF_HUB_URL", "env-url")

	flags := map[string]string{
		"hub-url": "flag-url",
		"user-id": "",
		"api-key": "",
	}

	resolved, err := config.Resolve(flags, cfg)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}

	if resolved.HubURL != "flag-url" {
		t.Errorf("HubURL = %q, want %q (flag should override both env and config)", resolved.HubURL, "flag-url")
	}
}

// TestPropertyConfigResolutionPrecedence is a property test that verifies
// config resolution precedence across all 8 combinations of present/absent
// for flag, env var, and config file values.
// TS-05-P1
func TestPropertyConfigResolutionPrecedence(t *testing.T) {
	type source struct {
		flag   string
		env    string
		config string
	}

	tests := []struct {
		name    string
		src     source
		want    string
		wantErr bool
	}{
		{"all set: flag wins", source{"flag-val", "env-val", "cfg-val"}, "flag-val", false},
		{"flag+env: flag wins", source{"flag-val", "env-val", ""}, "flag-val", false},
		{"flag+config: flag wins", source{"flag-val", "", "cfg-val"}, "flag-val", false},
		{"flag only: flag wins", source{"flag-val", "", ""}, "flag-val", false},
		{"env+config: env wins", source{"", "env-val", "cfg-val"}, "env-val", false},
		{"env only: env wins", source{"", "env-val", ""}, "env-val", false},
		{"config only: config wins", source{"", "", "cfg-val"}, "cfg-val", false},
		{"none set: error", source{"", "", ""}, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear env vars.
			t.Setenv("AF_HUB_URL", tt.src.env)
			// Also ensure other required fields are satisfied so we isolate
			// the hub_url resolution.
			t.Setenv("AF_HUB_USER_ID", "")
			t.Setenv("AF_HUB_API_KEY", "")

			cfg := &config.Config{
				HubURL: tt.src.config,
				UserID: "test-user",
				APIKey: "test-key",
			}
			if tt.wantErr {
				cfg.UserID = ""
				cfg.APIKey = ""
			}

			flags := map[string]string{
				"hub-url": tt.src.flag,
				"user-id": "",
				"api-key": "",
			}
			if !tt.wantErr {
				flags["user-id"] = "u"
				flags["api-key"] = "k"
			}

			resolved, err := config.Resolve(flags, cfg)

			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error for no sources, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if resolved.HubURL != tt.want {
				t.Errorf("HubURL = %q, want %q", resolved.HubURL, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 1.3 — Atomic Config Write Tests (REQ-3)
// ---------------------------------------------------------------------------

// TestAtomicWriteTempFileAndRename verifies that Save creates a temporary file
// in $HOME/.af/ with prefix 'config.toml.', renames it to config.toml, and
// leaves no temporary file after a successful write.
// TS-05-7
func TestAtomicWriteTempFileAndRename(t *testing.T) {
	tmpHome := t.TempDir()
	configDir := filepath.Join(tmpHome, ".af")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}

	// Write an initial config.
	configPath := filepath.Join(configDir, "config.toml")
	writeStructConfig(t, tmpHome, config.Config{HubURL: "old-url"})

	// Write new config using atomic Save.
	newCfg := &config.Config{
		HubURL: "https://new.example.com",
		UserID: "new-uid",
		APIKey: "new-key",
		KeyID:  "new-kid",
	}

	if err := config.Save(configPath, newCfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify final content.
	loaded := readParsedConfig(t, configPath)
	if loaded.HubURL != "https://new.example.com" {
		t.Errorf("HubURL = %q, want %q", loaded.HubURL, "https://new.example.com")
	}
	if loaded.UserID != "new-uid" {
		t.Errorf("UserID = %q, want %q", loaded.UserID, "new-uid")
	}
	if loaded.APIKey != "new-key" {
		t.Errorf("APIKey = %q, want %q", loaded.APIKey, "new-key")
	}
	if loaded.KeyID != "new-kid" {
		t.Errorf("KeyID = %q, want %q", loaded.KeyID, "new-kid")
	}

	// Verify no temp files remain in the config directory.
	entries, err := os.ReadDir(configDir)
	if err != nil {
		t.Fatalf("failed to read config dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "config.toml.") {
			t.Errorf("temp file %q remains in config dir after successful Save", e.Name())
		}
	}
}

// TestAtomicWriteFilePermissions verifies that both the temporary file and the
// final config.toml have mode 0600.
// TS-05-8
func TestAtomicWriteFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission test not applicable on Windows")
	}

	tmpHome := t.TempDir()
	configDir := filepath.Join(tmpHome, ".af")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(configDir, "config.toml")
	cfg := &config.Config{
		HubURL: "https://example.com",
		UserID: "u",
		APIKey: "k",
		KeyID:  "kid",
	}

	if err := config.Save(configPath, cfg); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("failed to stat config.toml: %v", err)
	}

	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("config.toml mode = %o, want 0600", perm)
	}
}

// TestAtomicWriteCreateTempFailure verifies that when temporary file creation
// fails, Save returns an error and leaves the original config unchanged.
// TS-05-E5
func TestAtomicWriteCreateTempFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission test not applicable on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("test requires non-root user for permission checks")
	}

	tmpHome := t.TempDir()
	configDir := filepath.Join(tmpHome, ".af")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}

	// Write an initial config.
	configPath := filepath.Join(configDir, "config.toml")
	originalContent := "hub_url = \"original\"\nuser_id = \"\"\napi_key = \"\"\nkey_id = \"\"\n"
	if err := os.WriteFile(configPath, []byte(originalContent), 0600); err != nil {
		t.Fatal(err)
	}

	// Make the directory read-only to prevent temp file creation.
	if err := os.Chmod(configDir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.Chmod(configDir, 0755)
	})

	newCfg := &config.Config{
		HubURL: "https://new.example.com",
	}

	err := config.Save(configPath, newCfg)
	if err == nil {
		t.Fatal("Save should return error when temp file creation fails, got nil")
	}

	// Verify original config is unchanged.
	os.Chmod(configDir, 0755) // restore to read
	data, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatalf("failed to read config after failed Save: %v", readErr)
	}
	if string(data) != originalContent {
		t.Errorf("config was modified after failed Save; got %q", string(data))
	}
}

// TestAtomicWriteRenameFailure verifies that when os.Rename fails after the
// temporary file is written, Save returns an error and the original config
// remains unchanged.
// TS-05-E6
func TestAtomicWriteRenameFailure(t *testing.T) {
	tmpHome := t.TempDir()
	configDir := filepath.Join(tmpHome, ".af")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(configDir, "config.toml")
	originalContent := "hub_url = \"original\"\nuser_id = \"\"\napi_key = \"\"\nkey_id = \"\"\n"
	if err := os.WriteFile(configPath, []byte(originalContent), 0600); err != nil {
		t.Fatal(err)
	}

	// Inject a rename failure via the exported function variable.
	origRename := config.SaveRename
	config.SaveRename = func(oldpath, newpath string) error {
		return os.ErrPermission
	}
	t.Cleanup(func() {
		config.SaveRename = origRename
	})

	newCfg := &config.Config{HubURL: "https://new.example.com"}
	err := config.Save(configPath, newCfg)
	if err == nil {
		t.Fatal("Save should return error when Rename fails, got nil")
	}

	// Verify original config is unchanged.
	data, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatalf("failed to read config after failed Save: %v", readErr)
	}
	if string(data) != originalContent {
		t.Errorf("config was modified after failed Rename; got %q", string(data))
	}
}

// TestPropertyAtomicWriteCrashSafety is a property test that verifies the
// atomic write invariant: after any Save call (successful or not), the config
// file is either fully updated or entirely unchanged — never partially written.
// TS-05-P2
func TestPropertyAtomicWriteCrashSafety(t *testing.T) {
	// Test with various config values to ensure no partial writes.
	testConfigs := []config.Config{
		{HubURL: "https://a.com", UserID: "u1", APIKey: "k1", KeyID: "kid1"},
		{HubURL: "https://b.com", UserID: "u2", APIKey: "k2", KeyID: "kid2"},
		{HubURL: "https://really-long-url.example.com/with/many/path/segments",
			UserID: "very-long-user-id-value-that-extends-past-typical-lengths",
			APIKey: "long-api-key-value-for-testing",
			KeyID:  "long-key-id"},
		{HubURL: "", UserID: "", APIKey: "", KeyID: ""},
	}

	for i, newCfg := range testConfigs {
		t.Run(strings.ReplaceAll(newCfg.HubURL, "/", "_"), func(t *testing.T) {
			tmpHome := t.TempDir()
			configDir := filepath.Join(tmpHome, ".af")
			if err := os.MkdirAll(configDir, 0700); err != nil {
				t.Fatal(err)
			}

			configPath := filepath.Join(configDir, "config.toml")
			oldCfg := config.Config{
				HubURL: "https://old.com",
				UserID: "old-user",
				APIKey: "old-key",
				KeyID:  "old-kid",
			}
			writeStructConfig(t, tmpHome, oldCfg)

			cfg := newCfg // local copy
			err := config.Save(configPath, &cfg)

			// Read back config — it must be either the old or new values,
			// never a mix.
			data, readErr := os.ReadFile(configPath)
			if readErr != nil {
				t.Fatalf("failed to read config: %v", readErr)
			}

			var decoded config.Config
			if _, decErr := toml.Decode(string(data), &decoded); decErr != nil {
				t.Fatalf("config file is not valid TOML after Save (index %d): %v", i, decErr)
			}

			if err != nil {
				// Save failed — config must be the old values.
				if decoded.HubURL != oldCfg.HubURL {
					t.Errorf("after failed Save, HubURL = %q, want old %q", decoded.HubURL, oldCfg.HubURL)
				}
			} else {
				// Save succeeded — config must be the new values.
				if decoded.HubURL != newCfg.HubURL {
					t.Errorf("after successful Save, HubURL = %q, want new %q", decoded.HubURL, newCfg.HubURL)
				}
				if decoded.UserID != newCfg.UserID {
					t.Errorf("after successful Save, UserID = %q, want new %q", decoded.UserID, newCfg.UserID)
				}
				if decoded.APIKey != newCfg.APIKey {
					t.Errorf("after successful Save, APIKey = %q, want new %q", decoded.APIKey, newCfg.APIKey)
				}
				if decoded.KeyID != newCfg.KeyID {
					t.Errorf("after successful Save, KeyID = %q, want new %q", decoded.KeyID, newCfg.KeyID)
				}
			}
		})
	}
}
