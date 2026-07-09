package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/agent-fox/af-hub/internal/cliconfig"
)

// ---------------------------------------------------------------------------
// TS-05-19: afc keys default sets api_key to specified workspace_slug
// REQ: 05-REQ-9.1
// ---------------------------------------------------------------------------

func TestKeysDefault_SetsAPIKeyToWorkspaceSlug(t *testing.T) {
	// When [keys.my-project] exists and user runs 'keys default my-project',
	// api_key should be updated to "my-project".

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	configContent := `hub_url = "https://hub.example.com"
api_key = "_login"

[keys._login]
key_id = "0011aabb"
token = "af_0011aabb_secret"
label = "login"

[keys.my-project]
key_id = "a1b2c3"
token = "af_a1b2c3_secret"
label = "dev laptop"
`
	configPath := filepath.Join(afDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := &cliconfig.Config{
		HubURL: "https://hub.example.com",
		APIKey: "_login",
		Keys: map[string]cliconfig.KeyEntry{
			"_login":     {KeyID: "0011aabb", Token: "af_0011aabb_secret", Label: "login"},
			"my-project": {KeyID: "a1b2c3", Token: "af_a1b2c3_secret", Label: "dev laptop"},
		},
	}

	err := cliconfig.SetDefaultKey(homeDir, cfg, "my-project")
	if err != nil {
		t.Fatalf("SetDefaultKey returned unexpected error: %v", err)
	}

	// Read config back and verify api_key was updated.
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}

	var reloaded cliconfig.Config
	if _, err := toml.Decode(string(content), &reloaded); err != nil {
		t.Fatalf("failed to decode config.toml: %v", err)
	}

	if reloaded.APIKey != "my-project" {
		t.Errorf("expected api_key = %q, got %q", "my-project", reloaded.APIKey)
	}
}

// ---------------------------------------------------------------------------
// TS-05-20: afc keys default _login sets api_key to _login
// REQ: 05-REQ-9.2
// ---------------------------------------------------------------------------

func TestKeysDefault_SetsAPIKeyToLogin(t *testing.T) {
	// When [keys._login] exists and user runs 'keys default _login',
	// api_key should be updated to "_login".

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	configContent := `hub_url = "https://hub.example.com"
api_key = "my-project"

[keys._login]
key_id = "0011aabb"
token = "af_0011aabb_secret"
label = "login"

[keys.my-project]
key_id = "a1b2c3"
token = "af_a1b2c3_secret"
label = "dev laptop"
`
	configPath := filepath.Join(afDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := &cliconfig.Config{
		HubURL: "https://hub.example.com",
		APIKey: "my-project",
		Keys: map[string]cliconfig.KeyEntry{
			"_login":     {KeyID: "0011aabb", Token: "af_0011aabb_secret", Label: "login"},
			"my-project": {KeyID: "a1b2c3", Token: "af_a1b2c3_secret", Label: "dev laptop"},
		},
	}

	err := cliconfig.SetDefaultKey(homeDir, cfg, "_login")
	if err != nil {
		t.Fatalf("SetDefaultKey returned unexpected error: %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}

	var reloaded cliconfig.Config
	if _, err := toml.Decode(string(content), &reloaded); err != nil {
		t.Fatalf("failed to decode config.toml: %v", err)
	}

	if reloaded.APIKey != "_login" {
		t.Errorf("expected api_key = %q, got %q", "_login", reloaded.APIKey)
	}
}

// ---------------------------------------------------------------------------
// TS-05-E11: afc keys default with non-existent workspace_slug — error
// REQ: 05-REQ-9.E1
// ---------------------------------------------------------------------------

func TestKeysDefault_NonExistentSlug_ReturnsError(t *testing.T) {
	// When the workspace_slug does not match any [keys.*] section,
	// SetDefaultKey should return an error and not modify api_key.

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	configContent := `hub_url = "https://hub.example.com"
api_key = "_login"

[keys._login]
key_id = "0011aabb"
token = "af_0011aabb_secret"
label = "login"
`
	configPath := filepath.Join(afDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := &cliconfig.Config{
		HubURL: "https://hub.example.com",
		APIKey: "_login",
		Keys: map[string]cliconfig.KeyEntry{
			"_login": {KeyID: "0011aabb", Token: "af_0011aabb_secret", Label: "login"},
		},
	}

	err := cliconfig.SetDefaultKey(homeDir, cfg, "nonexistent")
	if err == nil {
		t.Fatal("expected non-nil error when workspace_slug does not exist, got nil")
	}

	// Verify error message references the slug.
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("expected error message to contain 'nonexistent', got: %q", err.Error())
	}

	// Verify config was not modified.
	content, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatalf("failed to read config file: %v", readErr)
	}

	var reloaded cliconfig.Config
	if _, decErr := toml.Decode(string(content), &reloaded); decErr != nil {
		t.Fatalf("failed to decode config.toml: %v", decErr)
	}

	if reloaded.APIKey != "_login" {
		t.Errorf("expected api_key to remain %q after error, got %q", "_login", reloaded.APIKey)
	}
}

// ---------------------------------------------------------------------------
// TS-05-E12: afc keys default with no argument — usage help and non-zero exit
// REQ: 05-REQ-9.E2
// ---------------------------------------------------------------------------

func TestKeysDefault_NoArgument_ExitsWithUsage(t *testing.T) {
	// Running 'afc keys default' with no workspace-slug argument should
	// print usage help and exit with non-zero status.

	// This tests the CLI binary directly because it validates cobra
	// argument validation behavior.
	_, stderr, exitCode := execAfc(t, []string{"keys", "default"})

	if exitCode == 0 {
		t.Error("expected non-zero exit code when no workspace-slug argument provided, got 0")
	}

	// stderr should contain usage information or mention workspace-slug.
	stderrLower := strings.ToLower(stderr)
	hasUsageRef := strings.Contains(stderrLower, "usage") ||
		strings.Contains(stderrLower, "workspace") ||
		strings.Contains(stderrLower, "slug") ||
		strings.Contains(stderrLower, "argument") ||
		strings.Contains(stderrLower, "default")
	if !hasUsageRef {
		t.Errorf("expected stderr to contain usage info or mention workspace-slug, got: %q", stderr)
	}
}

// ---------------------------------------------------------------------------
// TS-05-P5: Non-mutating commands do not modify config file
// PROP: 05-PROP-5
// Validates: 05-REQ-5.1, 05-REQ-6.1, 05-REQ-7.1, 05-REQ-8.1, 05-REQ-9.1
// ---------------------------------------------------------------------------

func TestPropertyNonMutatingCommands_DoNotModifyConfig(t *testing.T) {
	// For each non-mutating command (keys list, workspace commands without
	// key storage), config.toml content and mtime must be unchanged.

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	configContent := `hub_url = "https://hub.example.com"
api_key = "my-project"

[keys.my-project]
key_id = "a1b2c3"
token = "af_a1b2c3_secret"
label = "dev laptop"
`
	configPath := filepath.Join(afDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
		t.Fatal(err)
	}

	originalContent, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	originalInfo, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}

	// Set up a stub server for API calls.
	stub := newStubServer(t)
	stub.onRoute("GET", "/api/v1/keys", 200, `[]`)

	// Run a non-mutating command: keys list.
	_, _, _ = execAfc(t, []string{
		"keys", "list",
		"--api-key", "af_a1b2c3_secret",
	},
		"HOME="+homeDir,
		"AF_HUB_URL="+stub.Server.URL,
	)

	// Verify config file is unchanged.
	afterContent, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read config after non-mutating command: %v", err)
	}
	afterInfo, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("failed to stat config after non-mutating command: %v", err)
	}

	if string(afterContent) != string(originalContent) {
		t.Error("config.toml content was modified by non-mutating 'keys list' command")
	}
	if afterInfo.ModTime() != originalInfo.ModTime() {
		t.Error("config.toml mtime was changed by non-mutating 'keys list' command")
	}
}
