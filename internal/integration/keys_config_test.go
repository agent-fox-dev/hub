package integration_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/agent-fox/af-hub/internal/cliconfig"
)

// ---------------------------------------------------------------------------
// TS-05-15: afc keys create adds [keys.<workspace_slug>] section to config
// REQ: 05-REQ-6.1
// ---------------------------------------------------------------------------

func TestKeysCreateConfig_AddsKeySection(t *testing.T) {
	// After a successful keys create, a [keys.my-project] section should be
	// added to config.toml with key_id, token, and label.

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	initialConfig := `hub_url = "https://hub.example.com"
api_key = "_login"

[keys._login]
key_id = "0011aabb"
token = "af_0011aabb_secret"
label = "login"
`
	configPath := filepath.Join(afDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(initialConfig), 0600); err != nil {
		t.Fatal(err)
	}

	// Load config struct (simulating what CLI does before the API call).
	cfg := &cliconfig.Config{
		HubURL: "https://hub.example.com",
		APIKey: "_login",
		Keys: map[string]cliconfig.KeyEntry{
			"_login": {KeyID: "0011aabb", Token: "af_0011aabb_secret", Label: "login"},
		},
	}

	// Simulate successful API response: new key for workspace "my-project".
	err := cliconfig.AddKeyEntry(homeDir, cfg, "my-project", "a1b2c3d4", "af_a1b2c3d4_newsecret", "dev laptop")
	if err != nil {
		t.Fatalf("AddKeyEntry returned unexpected error: %v", err)
	}

	// Read config back and verify the new key section was added.
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}

	var reloaded cliconfig.Config
	if _, err := toml.Decode(string(content), &reloaded); err != nil {
		t.Fatalf("failed to decode config.toml: %v", err)
	}

	entry, ok := reloaded.Keys["my-project"]
	if !ok {
		t.Fatal("expected [keys.my-project] section to exist after AddKeyEntry")
	}
	if entry.KeyID != "a1b2c3d4" {
		t.Errorf("expected key_id = %q, got %q", "a1b2c3d4", entry.KeyID)
	}
	if entry.Token != "af_a1b2c3d4_newsecret" {
		t.Errorf("expected token = %q, got %q", "af_a1b2c3d4_newsecret", entry.Token)
	}
	if entry.Label != "dev laptop" {
		t.Errorf("expected label = %q, got %q", "dev laptop", entry.Label)
	}

	// Existing keys should still be present.
	if _, ok := reloaded.Keys["_login"]; !ok {
		t.Error("expected [keys._login] section to still exist after AddKeyEntry")
	}
}

// ---------------------------------------------------------------------------
// TS-05-16: afc keys refresh updates token for matching key_id
// REQ: 05-REQ-7.1
// ---------------------------------------------------------------------------

func TestKeysRefreshConfig_UpdatesToken(t *testing.T) {
	// After keys refresh, the token of the matching [keys.*] entry should be
	// updated while key_id and label remain unchanged.

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	initialConfig := `hub_url = "https://hub.example.com"
api_key = "my-project"

[keys.my-project]
key_id = "a1b2c3"
token = "af_a1b2c3_old"
label = "dev laptop"
`
	configPath := filepath.Join(afDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(initialConfig), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := &cliconfig.Config{
		HubURL: "https://hub.example.com",
		APIKey: "my-project",
		Keys: map[string]cliconfig.KeyEntry{
			"my-project": {KeyID: "a1b2c3", Token: "af_a1b2c3_old", Label: "dev laptop"},
		},
	}

	// Simulate successful refresh: new token for key_id "a1b2c3".
	var stderr bytes.Buffer
	err := cliconfig.UpdateKeyToken(homeDir, cfg, "a1b2c3", "af_a1b2c3_new", &stderr)
	if err != nil {
		t.Fatalf("UpdateKeyToken returned unexpected error: %v", err)
	}

	// Read config back and verify the token was updated.
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}

	var reloaded cliconfig.Config
	if _, err := toml.Decode(string(content), &reloaded); err != nil {
		t.Fatalf("failed to decode config.toml: %v", err)
	}

	entry, ok := reloaded.Keys["my-project"]
	if !ok {
		t.Fatal("expected [keys.my-project] to still exist after refresh")
	}
	if entry.Token != "af_a1b2c3_new" {
		t.Errorf("expected token = %q, got %q", "af_a1b2c3_new", entry.Token)
	}
	// key_id and label should be unchanged.
	if entry.KeyID != "a1b2c3" {
		t.Errorf("expected key_id = %q (unchanged), got %q", "a1b2c3", entry.KeyID)
	}
	if entry.Label != "dev laptop" {
		t.Errorf("expected label = %q (unchanged), got %q", "dev laptop", entry.Label)
	}
}

// ---------------------------------------------------------------------------
// TS-05-17: afc keys revoke removes matching [keys.*] section
// REQ: 05-REQ-8.1
// ---------------------------------------------------------------------------

func TestKeysRevokeConfig_RemovesKeySection(t *testing.T) {
	// After keys revoke, the [keys.staging] section with matching key_id
	// should be removed. api_key should be unchanged (it was a different slug).

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	initialConfig := `hub_url = "https://hub.example.com"
api_key = "my-project"

[keys.my-project]
key_id = "a1b2c3"
token = "af_a1b2c3_secret"
label = "dev laptop"

[keys.staging]
key_id = "f7e8d9"
token = "af_f7e8d9_secret"
label = "staging env"
`
	configPath := filepath.Join(afDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(initialConfig), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := &cliconfig.Config{
		HubURL: "https://hub.example.com",
		APIKey: "my-project",
		Keys: map[string]cliconfig.KeyEntry{
			"my-project": {KeyID: "a1b2c3", Token: "af_a1b2c3_secret", Label: "dev laptop"},
			"staging":    {KeyID: "f7e8d9", Token: "af_f7e8d9_secret", Label: "staging env"},
		},
	}

	// Revoke the staging key.
	var stderr bytes.Buffer
	err := cliconfig.RemoveKeyEntry(homeDir, cfg, "f7e8d9", &stderr)
	if err != nil {
		t.Fatalf("RemoveKeyEntry returned unexpected error: %v", err)
	}

	// Read config back and verify the staging section was removed.
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}

	var reloaded cliconfig.Config
	if _, err := toml.Decode(string(content), &reloaded); err != nil {
		t.Fatalf("failed to decode config.toml: %v", err)
	}

	if _, ok := reloaded.Keys["staging"]; ok {
		t.Error("expected [keys.staging] to be removed after revoke")
	}

	// api_key should be unchanged (was "my-project", not "staging").
	if reloaded.APIKey != "my-project" {
		t.Errorf("expected api_key = %q (unchanged), got %q", "my-project", reloaded.APIKey)
	}

	// Other keys should still be present.
	if _, ok := reloaded.Keys["my-project"]; !ok {
		t.Error("expected [keys.my-project] to still exist after revoking a different key")
	}
}

// ---------------------------------------------------------------------------
// TS-05-18: afc keys revoke clears api_key when revoked key is the default
// REQ: 05-REQ-8.2
// ---------------------------------------------------------------------------

func TestKeysRevokeConfig_ClearsDefaultAPIKey(t *testing.T) {
	// When the revoked key's workspace slug matches the current api_key,
	// api_key should be cleared to empty and a warning printed to stderr.

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	initialConfig := `hub_url = "https://hub.example.com"
api_key = "my-project"

[keys.my-project]
key_id = "a1b2c3"
token = "af_a1b2c3_secret"
label = "dev laptop"
`
	configPath := filepath.Join(afDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(initialConfig), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := &cliconfig.Config{
		HubURL: "https://hub.example.com",
		APIKey: "my-project",
		Keys: map[string]cliconfig.KeyEntry{
			"my-project": {KeyID: "a1b2c3", Token: "af_a1b2c3_secret", Label: "dev laptop"},
		},
	}

	// Revoke the default key.
	var stderr bytes.Buffer
	err := cliconfig.RemoveKeyEntry(homeDir, cfg, "a1b2c3", &stderr)
	if err != nil {
		t.Fatalf("RemoveKeyEntry returned unexpected error: %v", err)
	}

	// Read config back.
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}

	var reloaded cliconfig.Config
	if _, err := toml.Decode(string(content), &reloaded); err != nil {
		t.Fatalf("failed to decode config.toml: %v", err)
	}

	// api_key should be cleared to empty string.
	if reloaded.APIKey != "" {
		t.Errorf("expected api_key to be cleared to empty string, got %q", reloaded.APIKey)
	}

	// The key section should be removed.
	if _, ok := reloaded.Keys["my-project"]; ok {
		t.Error("expected [keys.my-project] to be removed after revoke")
	}

	// stderr should mention "afc keys default".
	stderrStr := stderr.String()
	if !strings.Contains(stderrStr, "afc keys default") {
		t.Errorf("expected stderr to mention 'afc keys default', got: %q", stderrStr)
	}
}

// ---------------------------------------------------------------------------
// TS-05-E9: afc keys refresh with key_id not in config — warning, no error
// REQ: 05-REQ-7.E1
// ---------------------------------------------------------------------------

func TestKeysRefreshConfig_KeyNotFound_WarnsButNoError(t *testing.T) {
	// When refreshing a key_id that's not in config, the function should
	// write a warning to stderr but not return an error.

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	initialConfig := `hub_url = "https://hub.example.com"
api_key = ""
`
	configPath := filepath.Join(afDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(initialConfig), 0600); err != nil {
		t.Fatal(err)
	}
	originalContent, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	cfg := &cliconfig.Config{
		HubURL: "https://hub.example.com",
		APIKey: "",
		Keys:   make(map[string]cliconfig.KeyEntry),
	}

	// Refresh a key_id not in config.
	var stderr bytes.Buffer
	updateErr := cliconfig.UpdateKeyToken(homeDir, cfg, "deadbeef", "af_deadbeef_new", &stderr)
	if updateErr != nil {
		t.Fatalf("expected nil error for non-existent key refresh, got: %v", updateErr)
	}

	// Config should be unchanged.
	afterContent, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config after UpdateKeyToken: %v", err)
	}
	if string(afterContent) != string(originalContent) {
		t.Error("config file was modified when key_id was not found; expected unchanged")
	}

	// stderr should contain a warning about the missing key.
	stderrStr := stderr.String()
	if !strings.Contains(stderrStr, "deadbeef") && !strings.Contains(strings.ToLower(stderrStr), "not found") {
		t.Errorf("expected stderr to warn about missing key 'deadbeef', got: %q", stderrStr)
	}
}

// ---------------------------------------------------------------------------
// TS-05-E10: afc keys revoke with key_id not in config — warning, no error
// REQ: 05-REQ-8.E1
// ---------------------------------------------------------------------------

func TestKeysRevokeConfig_KeyNotFound_WarnsButNoError(t *testing.T) {
	// When revoking a key_id that's not in config, the function should
	// write a warning to stderr but not return an error.

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	initialConfig := `hub_url = "https://hub.example.com"
api_key = ""
`
	configPath := filepath.Join(afDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(initialConfig), 0600); err != nil {
		t.Fatal(err)
	}
	originalContent, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	cfg := &cliconfig.Config{
		HubURL: "https://hub.example.com",
		APIKey: "",
		Keys:   make(map[string]cliconfig.KeyEntry),
	}

	// Revoke a key_id not in config.
	var stderr bytes.Buffer
	revokeErr := cliconfig.RemoveKeyEntry(homeDir, cfg, "cafebabe", &stderr)
	if revokeErr != nil {
		t.Fatalf("expected nil error for non-existent key revoke, got: %v", revokeErr)
	}

	// Config should be unchanged.
	afterContent, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config after RemoveKeyEntry: %v", err)
	}
	if string(afterContent) != string(originalContent) {
		t.Error("config file was modified when key_id was not found; expected unchanged")
	}

	// stderr should contain a warning about the missing key.
	stderrStr := stderr.String()
	if !strings.Contains(stderrStr, "cafebabe") && !strings.Contains(strings.ToLower(stderrStr), "not found") {
		t.Errorf("expected stderr to warn about missing key 'cafebabe', got: %q", stderrStr)
	}
}
