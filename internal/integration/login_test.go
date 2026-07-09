package integration_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/agent-fox/af-hub/internal/cliconfig"
)

// ---------------------------------------------------------------------------
// TS-05-12: afc login stores [keys._login] section with key_id, token,
//           label="login", and sets api_key="_login"
// REQ: 05-REQ-5.1
// ---------------------------------------------------------------------------

func TestLoginConfig_StoresLoginCredentials(t *testing.T) {
	// Simulate the config side-effect of a successful afc login.
	// The login command should call WriteLoginKey which adds a [keys._login]
	// section and sets api_key = "_login".

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	// Pre-existing config with hub_url set.
	initialConfig := `hub_url = "https://hub.example.com"
api_key = ""
`
	configPath := filepath.Join(afDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(initialConfig), 0600); err != nil {
		t.Fatal(err)
	}

	// Load the config into a struct (simulating what the CLI would do).
	cfg := &cliconfig.Config{
		HubURL: "https://hub.example.com",
		APIKey: "",
		Keys:   make(map[string]cliconfig.KeyEntry),
	}

	// Simulate callback response: api_key = {key: "af_0011aabb_secret", key_id: "0011aabb"}
	err := cliconfig.WriteLoginKey(homeDir, cfg, "0011aabb", "af_0011aabb_secret", "https://hub.example.com")
	if err != nil {
		t.Fatalf("WriteLoginKey returned unexpected error: %v", err)
	}

	// Read config back from disk and verify the login credentials were written.
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}

	var reloaded cliconfig.Config
	if _, err := toml.Decode(string(content), &reloaded); err != nil {
		t.Fatalf("failed to decode config.toml: %v", err)
	}

	// Verify api_key is set to "_login".
	if reloaded.APIKey != "_login" {
		t.Errorf("expected api_key = %q, got %q", "_login", reloaded.APIKey)
	}

	// Verify [keys._login] section exists with correct fields.
	loginEntry, ok := reloaded.Keys["_login"]
	if !ok {
		t.Fatal("expected [keys._login] section to exist after WriteLoginKey")
	}
	if loginEntry.KeyID != "0011aabb" {
		t.Errorf("expected _login key_id = %q, got %q", "0011aabb", loginEntry.KeyID)
	}
	if loginEntry.Token != "af_0011aabb_secret" {
		t.Errorf("expected _login token = %q, got %q", "af_0011aabb_secret", loginEntry.Token)
	}
	if loginEntry.Label != "login" {
		t.Errorf("expected _login label = %q, got %q", "login", loginEntry.Label)
	}
}

// ---------------------------------------------------------------------------
// TS-05-13: afc login writes hub_url to config when currently empty
// REQ: 05-REQ-5.2
// ---------------------------------------------------------------------------

func TestLoginConfig_WritesHubURLWhenEmpty(t *testing.T) {
	// When hub_url is empty in the config, login should set it to the login URL.

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	initialConfig := `hub_url = ""
api_key = ""
`
	configPath := filepath.Join(afDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(initialConfig), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := &cliconfig.Config{
		HubURL: "",
		APIKey: "",
		Keys:   make(map[string]cliconfig.KeyEntry),
	}

	err := cliconfig.WriteLoginKey(homeDir, cfg, "0011aabb", "af_0011aabb_secret", "https://hub.example.com")
	if err != nil {
		t.Fatalf("WriteLoginKey returned unexpected error: %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}

	var reloaded cliconfig.Config
	if _, err := toml.Decode(string(content), &reloaded); err != nil {
		t.Fatalf("failed to decode config.toml: %v", err)
	}

	// Verify hub_url was set to the login URL.
	if reloaded.HubURL != "https://hub.example.com" {
		t.Errorf("expected hub_url = %q, got %q", "https://hub.example.com", reloaded.HubURL)
	}
}

// ---------------------------------------------------------------------------
// TS-05-14: afc login does NOT overwrite existing hub_url
// REQ: 05-REQ-5.3
// ---------------------------------------------------------------------------

func TestLoginConfig_DoesNotOverwriteExistingHubURL(t *testing.T) {
	// When hub_url is already set, login should NOT overwrite it.

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	initialConfig := `hub_url = "https://existing.example.com"
api_key = ""
`
	configPath := filepath.Join(afDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(initialConfig), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := &cliconfig.Config{
		HubURL: "https://existing.example.com",
		APIKey: "",
		Keys:   make(map[string]cliconfig.KeyEntry),
	}

	// Login with a DIFFERENT hub URL.
	err := cliconfig.WriteLoginKey(homeDir, cfg, "0011aabb", "af_0011aabb_secret", "https://new.example.com")
	if err != nil {
		t.Fatalf("WriteLoginKey returned unexpected error: %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}

	var reloaded cliconfig.Config
	if _, err := toml.Decode(string(content), &reloaded); err != nil {
		t.Fatalf("failed to decode config.toml: %v", err)
	}

	// hub_url should remain as existing value, not overwritten.
	if reloaded.HubURL != "https://existing.example.com" {
		t.Errorf("expected hub_url = %q (unchanged), got %q", "https://existing.example.com", reloaded.HubURL)
	}

	// But the login credentials should still be written.
	if reloaded.APIKey != "_login" {
		t.Errorf("expected api_key = %q, got %q", "_login", reloaded.APIKey)
	}
	loginEntry, ok := reloaded.Keys["_login"]
	if !ok {
		t.Fatal("expected [keys._login] section to exist")
	}
	if loginEntry.KeyID != "0011aabb" {
		t.Errorf("expected _login key_id = %q, got %q", "0011aabb", loginEntry.KeyID)
	}
}

// ---------------------------------------------------------------------------
// TS-05-E8: Login callback response missing api_key — config unchanged
// REQ: 05-REQ-5.E1
// ---------------------------------------------------------------------------

func TestLoginConfig_MissingAPIKeyInResponse_ConfigUnchanged(t *testing.T) {
	// When the login callback response is missing the api_key field,
	// the CLI should not modify the config file. We test this at the
	// mutation function level: WriteLoginKey should reject empty credentials.
	//
	// Note: In the full CLI flow, the login command should detect the
	// missing field before calling WriteLoginKey. This test verifies the
	// defense-in-depth behavior.

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

	// Simulate missing api_key: empty key_id and token.
	writeErr := cliconfig.WriteLoginKey(homeDir, cfg, "", "", "https://hub.example.com")

	// The function should either return an error for empty credentials,
	// or the config should remain unchanged.
	afterContent, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatalf("failed to read config after WriteLoginKey: %v", readErr)
	}

	if writeErr == nil {
		// If no error was returned, at minimum the config must be unchanged
		// (empty credentials should not be written).
		if string(afterContent) != string(originalContent) {
			t.Error("WriteLoginKey with empty credentials modified the config file; expected it to be unchanged")
		}
	}

	// Regardless of error return, verify the config was not corrupted.
	var reloaded cliconfig.Config
	if _, decErr := toml.Decode(string(afterContent), &reloaded); decErr != nil {
		t.Fatalf("config file is no longer valid TOML: %v", decErr)
	}

	// Empty credentials should never appear in the config.
	if loginEntry, ok := reloaded.Keys["_login"]; ok {
		if loginEntry.KeyID == "" && loginEntry.Token == "" {
			t.Error("expected [keys._login] to not be written with empty key_id and token")
		}
	}
}
