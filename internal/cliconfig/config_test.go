package cliconfig_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox/af-hub/internal/cliconfig"
)

// ---------------------------------------------------------------------------
// TS-05-4: Config file TOML parsing — top-level fields
// REQ: 05-REQ-2.1
// ---------------------------------------------------------------------------

func TestLoadConfig_TopLevelFields(t *testing.T) {
	// TS-05-4: Write config with hub_url and api_key; assert LoadConfig returns
	// struct with matching HubURL and APIKey.

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	content := `hub_url = "https://hub.example.com"
api_key = "my-project"
`
	configPath := filepath.Join(afDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := cliconfig.LoadConfig(homeDir)
	if err != nil {
		t.Fatalf("LoadConfig returned unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("LoadConfig returned nil config")
	}

	if cfg.HubURL != "https://hub.example.com" {
		t.Errorf("expected HubURL = %q, got %q", "https://hub.example.com", cfg.HubURL)
	}
	if cfg.APIKey != "my-project" {
		t.Errorf("expected APIKey = %q, got %q", "my-project", cfg.APIKey)
	}
}

// ---------------------------------------------------------------------------
// TS-05-5: Config file TOML parsing — [keys.*] sections
// REQ: 05-REQ-2.2
// ---------------------------------------------------------------------------

func TestLoadConfig_KeyEntrySections(t *testing.T) {
	// TS-05-5: Write config with [keys.my-project] section containing key_id,
	// token, and label. Assert cfg.Keys["my-project"] has correct fields.

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	content := `hub_url = "https://hub.example.com"
api_key = "my-project"

[keys.my-project]
key_id = "a1b2c3"
token = "af_a1b2c3_secret"
label = "dev laptop"
`
	configPath := filepath.Join(afDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := cliconfig.LoadConfig(homeDir)
	if err != nil {
		t.Fatalf("LoadConfig returned unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("LoadConfig returned nil config")
	}

	entry, ok := cfg.Keys["my-project"]
	if !ok {
		t.Fatal("expected cfg.Keys to contain 'my-project' entry")
	}
	if entry.KeyID != "a1b2c3" {
		t.Errorf("expected KeyID = %q, got %q", "a1b2c3", entry.KeyID)
	}
	if entry.Token != "af_a1b2c3_secret" {
		t.Errorf("expected Token = %q, got %q", "af_a1b2c3_secret", entry.Token)
	}
	if entry.Label != "dev laptop" {
		t.Errorf("expected Label = %q, got %q", "dev laptop", entry.Label)
	}
}

func TestLoadConfig_MultipleKeyEntries(t *testing.T) {
	// Additional coverage: multiple [keys.*] sections are parsed correctly.

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	content := `hub_url = "https://hub.example.com"
api_key = "my-project"

[keys.my-project]
key_id = "a1b2c3"
token = "af_a1b2c3_secret"
label = "dev laptop"

[keys._login]
key_id = "0011aabb"
token = "af_0011aabb_loginsecret"
label = "login"
`
	configPath := filepath.Join(afDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := cliconfig.LoadConfig(homeDir)
	if err != nil {
		t.Fatalf("LoadConfig returned unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("LoadConfig returned nil config")
	}

	if len(cfg.Keys) != 2 {
		t.Errorf("expected 2 key entries, got %d", len(cfg.Keys))
	}

	loginEntry, ok := cfg.Keys["_login"]
	if !ok {
		t.Fatal("expected cfg.Keys to contain '_login' entry")
	}
	if loginEntry.KeyID != "0011aabb" {
		t.Errorf("expected _login KeyID = %q, got %q", "0011aabb", loginEntry.KeyID)
	}
	if loginEntry.Token != "af_0011aabb_loginsecret" {
		t.Errorf("expected _login Token = %q, got %q", "af_0011aabb_loginsecret", loginEntry.Token)
	}
	if loginEntry.Label != "login" {
		t.Errorf("expected _login Label = %q, got %q", "login", loginEntry.Label)
	}
}

func TestLoadConfig_OptionalLabel(t *testing.T) {
	// TS-05-5 supplemental: label is optional; entry without label parses OK.

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	content := `hub_url = ""
api_key = ""

[keys.staging]
key_id = "deadbeef"
token = "af_deadbeef_secretval"
`
	configPath := filepath.Join(afDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := cliconfig.LoadConfig(homeDir)
	if err != nil {
		t.Fatalf("LoadConfig returned unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("LoadConfig returned nil config")
	}

	entry, ok := cfg.Keys["staging"]
	if !ok {
		t.Fatal("expected cfg.Keys to contain 'staging' entry")
	}
	if entry.KeyID != "deadbeef" {
		t.Errorf("expected KeyID = %q, got %q", "deadbeef", entry.KeyID)
	}
	if entry.Label != "" {
		t.Errorf("expected Label to be empty when omitted, got %q", entry.Label)
	}
}

// ---------------------------------------------------------------------------
// TS-05-6: Empty string config values treated as unset
// REQ: 05-REQ-2.3
// ---------------------------------------------------------------------------

func TestResolveHubURL_EmptyConfigTreatedAsUnset(t *testing.T) {
	// TS-05-6: Config has hub_url = "" — resolution should fall through
	// to error, not return an empty string.

	cfg := &cliconfig.Config{
		HubURL: "",
		APIKey: "",
	}

	result, err := cliconfig.ResolveHubURL("", "", cfg)
	if err == nil {
		t.Error("expected non-nil error when hub_url is empty and no flag/env set, got nil")
	}
	if result != "" {
		t.Errorf("expected empty result on error, got %q", result)
	}
}

func TestResolveAPIKey_EmptyConfigTreatedAsUnset(t *testing.T) {
	// TS-05-6: Config has api_key = "" — resolution should fall through
	// to error, not return an empty string.

	cfg := &cliconfig.Config{
		HubURL: "",
		APIKey: "",
	}

	result, err := cliconfig.ResolveAPIKey("", "", cfg)
	if err == nil {
		t.Error("expected non-nil error when api_key is empty and no flag/env set, got nil")
	}
	if result != "" {
		t.Errorf("expected empty result on error, got %q", result)
	}
}

// ---------------------------------------------------------------------------
// TS-05-E4: Invalid TOML parse error
// REQ: 05-REQ-2.E1
// ---------------------------------------------------------------------------

func TestLoadConfig_InvalidTOML(t *testing.T) {
	// TS-05-E4: Config file contains invalid TOML. LoadConfig should return
	// a non-nil error whose message contains the file path and a description
	// of the parse failure.

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	// Write deliberately invalid TOML.
	invalidContent := `hub_url = [not valid`
	configPath := filepath.Join(afDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(invalidContent), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := cliconfig.LoadConfig(homeDir)
	if err == nil {
		t.Fatal("expected non-nil error on invalid TOML, got nil")
	}

	errMsg := err.Error()

	// Error should reference the config file path.
	if !strings.Contains(errMsg, configPath) && !strings.Contains(errMsg, "config.toml") {
		t.Errorf("expected error message to contain config file path, got: %q", errMsg)
	}

	// Error should describe the parse failure.
	hasParseRef := strings.Contains(strings.ToLower(errMsg), "parse") ||
		strings.Contains(strings.ToLower(errMsg), "decode") ||
		strings.Contains(strings.ToLower(errMsg), "toml")
	if !hasParseRef {
		t.Errorf("expected error message to describe parse failure (parse/decode/toml), got: %q", errMsg)
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	// Supplemental: LoadConfig on a directory with no config.toml returns
	// a meaningful error.

	homeDir := t.TempDir()
	// Don't create .af/ or config.toml.

	_, err := cliconfig.LoadConfig(homeDir)
	if err == nil {
		t.Fatal("expected non-nil error when config file does not exist, got nil")
	}
}

// ---------------------------------------------------------------------------
// TS-05-E5: api_key references non-existent workspace_slug
// REQ: 05-REQ-2.E2
// ---------------------------------------------------------------------------

func TestResolveAPIKey_NonExistentWorkspaceSlug(t *testing.T) {
	// TS-05-E5: Config has api_key = "nonexistent-slug" with no matching
	// [keys.nonexistent-slug] section. ResolveAPIKey should treat the value
	// as unset and fall through to return an error.

	cfg := &cliconfig.Config{
		HubURL: "https://hub.example.com",
		APIKey: "nonexistent-slug",
		Keys:   map[string]cliconfig.KeyEntry{}, // empty — no matching entry
	}

	result, err := cliconfig.ResolveAPIKey("", "", cfg)
	if err == nil {
		t.Error("expected non-nil error when api_key references non-existent slug, got nil")
	}
	if result != "" {
		t.Errorf("expected empty result when api_key references non-existent slug, got %q", result)
	}
}

func TestResolveAPIKey_NonExistentSlugWithOtherKeys(t *testing.T) {
	// Supplemental: api_key references a slug that doesn't exist, even though
	// other key entries are present.

	cfg := &cliconfig.Config{
		HubURL: "https://hub.example.com",
		APIKey: "nonexistent-slug",
		Keys: map[string]cliconfig.KeyEntry{
			"my-project": {
				KeyID: "a1b2c3",
				Token: "af_a1b2c3_secret",
				Label: "dev",
			},
		},
	}

	result, err := cliconfig.ResolveAPIKey("", "", cfg)
	if err == nil {
		t.Error("expected non-nil error when api_key references non-existent slug, got nil")
	}
	if result != "" {
		t.Errorf("expected empty result, got %q", result)
	}
}
