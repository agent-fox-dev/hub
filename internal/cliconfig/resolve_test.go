package cliconfig_test

import (
	"strings"
	"testing"

	"github.com/agent-fox/af-hub/internal/cliconfig"
)

// ---------------------------------------------------------------------------
// TS-05-7: Hub URL resolution precedence — flag > env > config > error
// REQ: 05-REQ-3.1
// ---------------------------------------------------------------------------

func TestResolveHubURL_FlagWinsOverEnvAndConfig(t *testing.T) {
	// Scenario 1: flag is set — it takes highest precedence over env and config.
	cfg := &cliconfig.Config{
		HubURL: "https://config.example.com",
	}

	result, err := cliconfig.ResolveHubURL("https://flag.example.com", "https://env.example.com", cfg)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result != "https://flag.example.com" {
		t.Errorf("expected flag URL %q, got %q", "https://flag.example.com", result)
	}
}

func TestResolveHubURL_EnvWinsOverConfig(t *testing.T) {
	// Scenario 2: flag is empty, env is set — env takes precedence over config.
	cfg := &cliconfig.Config{
		HubURL: "https://config.example.com",
	}

	result, err := cliconfig.ResolveHubURL("", "https://env.example.com", cfg)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result != "https://env.example.com" {
		t.Errorf("expected env URL %q, got %q", "https://env.example.com", result)
	}
}

func TestResolveHubURL_ConfigUsedWhenFlagAndEnvEmpty(t *testing.T) {
	// Scenario 3: both flag and env are empty — config value is used.
	cfg := &cliconfig.Config{
		HubURL: "https://config.example.com",
	}

	result, err := cliconfig.ResolveHubURL("", "", cfg)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result != "https://config.example.com" {
		t.Errorf("expected config URL %q, got %q", "https://config.example.com", result)
	}
}

func TestResolveHubURL_AllEmptyReturnsError(t *testing.T) {
	// Scenario 4: all sources are empty — returns error.
	cfg := &cliconfig.Config{
		HubURL: "",
	}

	result, err := cliconfig.ResolveHubURL("", "", cfg)
	if err == nil {
		t.Fatal("expected non-nil error when all hub URL sources are empty, got nil")
	}
	if result != "" {
		t.Errorf("expected empty result on error, got %q", result)
	}
}

// ---------------------------------------------------------------------------
// TS-05-8: API key token resolution precedence — flag > env > config > error
// REQ: 05-REQ-3.2
// ---------------------------------------------------------------------------

func TestResolveAPIKey_FlagWinsOverEnvAndConfig(t *testing.T) {
	// Scenario 1: flag is set — it takes highest precedence.
	cfg := &cliconfig.Config{
		APIKey: "my-project",
		Keys: map[string]cliconfig.KeyEntry{
			"my-project": {
				KeyID: "abc123",
				Token: "af_abc_secret",
			},
		},
	}

	result, err := cliconfig.ResolveAPIKey("af_flagtoken", "af_envtoken", cfg)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result != "af_flagtoken" {
		t.Errorf("expected flag token %q, got %q", "af_flagtoken", result)
	}
}

func TestResolveAPIKey_EnvWinsOverConfig(t *testing.T) {
	// Scenario 2: flag is empty, env is set — env takes precedence.
	cfg := &cliconfig.Config{
		APIKey: "my-project",
		Keys: map[string]cliconfig.KeyEntry{
			"my-project": {
				KeyID: "abc123",
				Token: "af_abc_secret",
			},
		},
	}

	result, err := cliconfig.ResolveAPIKey("", "af_envtoken", cfg)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result != "af_envtoken" {
		t.Errorf("expected env token %q, got %q", "af_envtoken", result)
	}
}

func TestResolveAPIKey_ConfigLookupUsedWhenFlagAndEnvEmpty(t *testing.T) {
	// Scenario 3: flag and env empty — config api_key + keys lookup.
	cfg := &cliconfig.Config{
		APIKey: "my-project",
		Keys: map[string]cliconfig.KeyEntry{
			"my-project": {
				KeyID: "abc123",
				Token: "af_abc_secret",
			},
		},
	}

	result, err := cliconfig.ResolveAPIKey("", "", cfg)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if result != "af_abc_secret" {
		t.Errorf("expected config token %q, got %q", "af_abc_secret", result)
	}
}

func TestResolveAPIKey_AllEmptyReturnsError(t *testing.T) {
	// Scenario 4: no source provides a key — returns error.
	cfg := &cliconfig.Config{
		APIKey: "",
		Keys:   map[string]cliconfig.KeyEntry{},
	}

	result, err := cliconfig.ResolveAPIKey("", "", cfg)
	if err == nil {
		t.Fatal("expected non-nil error when all API key sources are empty, got nil")
	}
	if result != "" {
		t.Errorf("expected empty result on error, got %q", result)
	}
}

// ---------------------------------------------------------------------------
// TS-05-9: Error message content for unresolved hub URL and API key
// REQ: 05-REQ-3.3
// ---------------------------------------------------------------------------

func TestResolveHubURL_ErrorMessageContent(t *testing.T) {
	// TS-05-9: When hub URL cannot be resolved, the error message must mention
	// config.toml, --hub-url, and AF_HUB_URL.
	cfg := &cliconfig.Config{
		HubURL: "",
	}

	_, err := cliconfig.ResolveHubURL("", "", cfg)
	if err == nil {
		t.Fatal("expected non-nil error, got nil")
	}

	errMsg := err.Error()

	if !strings.Contains(errMsg, "config.toml") {
		t.Errorf("expected error message to contain 'config.toml', got: %q", errMsg)
	}
	if !strings.Contains(errMsg, "--hub-url") {
		t.Errorf("expected error message to contain '--hub-url', got: %q", errMsg)
	}
	if !strings.Contains(errMsg, "AF_HUB_URL") {
		t.Errorf("expected error message to contain 'AF_HUB_URL', got: %q", errMsg)
	}
}

func TestResolveAPIKey_ErrorMessageContent(t *testing.T) {
	// TS-05-9 supplemental: When API key cannot be resolved, the error message
	// must mention config.toml, --api-key, and AF_HUB_API_KEY.
	cfg := &cliconfig.Config{
		APIKey: "",
		Keys:   map[string]cliconfig.KeyEntry{},
	}

	_, err := cliconfig.ResolveAPIKey("", "", cfg)
	if err == nil {
		t.Fatal("expected non-nil error, got nil")
	}

	errMsg := err.Error()

	if !strings.Contains(errMsg, "config.toml") {
		t.Errorf("expected error message to contain 'config.toml', got: %q", errMsg)
	}
	if !strings.Contains(errMsg, "--api-key") {
		t.Errorf("expected error message to contain '--api-key', got: %q", errMsg)
	}
	if !strings.Contains(errMsg, "AF_HUB_API_KEY") {
		t.Errorf("expected error message to contain 'AF_HUB_API_KEY', got: %q", errMsg)
	}
}

func TestResolveAPIKey_NilConfigKeys(t *testing.T) {
	// Supplemental: Config with nil Keys map should not panic.
	cfg := &cliconfig.Config{
		APIKey: "my-project",
		Keys:   nil,
	}

	result, err := cliconfig.ResolveAPIKey("", "", cfg)
	if err == nil {
		t.Fatal("expected non-nil error when Keys map is nil and no flag/env, got nil")
	}
	if result != "" {
		t.Errorf("expected empty result, got %q", result)
	}
}
