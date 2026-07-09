package cliconfig_test

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/quick"

	"github.com/BurntSushi/toml"
	"github.com/agent-fox/af-hub/internal/cliconfig"
)

// ---------------------------------------------------------------------------
// TS-05-P1: Flag/env always overrides config file for hub URL resolution
// PROP: 05-PROP-1
// Validates: 05-REQ-3.1, 05-REQ-11.2
// ---------------------------------------------------------------------------

func TestPropertyResolveHubURL_FlagOrEnvAlwaysWins(t *testing.T) {
	// For any random non-empty flag or env value, the resolved hub URL must
	// always equal the flag (if set) or env (if flag is empty), never the
	// config file value.

	f := func(flag, env, cfgHubURL string) bool {
		// We only test cases where at least one of flag/env is non-empty.
		if flag == "" && env == "" {
			return true // skip — not in property scope
		}

		cfg := &cliconfig.Config{
			HubURL: cfgHubURL,
		}

		result, err := cliconfig.ResolveHubURL(flag, env, cfg)
		if err != nil {
			return false // should not error when flag/env is set
		}

		if flag != "" {
			return result == flag
		}
		return result == env
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("property violation: flag/env should always win over config: %v", err)
	}
}

func TestPropertyResolveAPIKey_FlagOrEnvAlwaysWins(t *testing.T) {
	// Same property for API key resolution: flag or env always wins.

	f := func(flag, env string) bool {
		if flag == "" && env == "" {
			return true // skip
		}

		cfg := &cliconfig.Config{
			APIKey: "some-project",
			Keys: map[string]cliconfig.KeyEntry{
				"some-project": {
					KeyID: "abc",
					Token: "af_abc_config_token",
				},
			},
		}

		result, err := cliconfig.ResolveAPIKey(flag, env, cfg)
		if err != nil {
			return false
		}

		if flag != "" {
			return result == flag
		}
		return result == env
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("property violation: flag/env should always win for API key: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TS-05-P3: Atomic writes never leave partial config file
// PROP: 05-PROP-3
// Validates: 05-REQ-4.1, 05-REQ-4.E1
// ---------------------------------------------------------------------------

func TestPropertyAtomicWrite_ConfigAlwaysValidTOML(t *testing.T) {
	// For any valid config state and any mutation, the config file on disk is
	// always valid TOML (either old or new content). We test this by writing
	// random configs atomically and verifying the result parses.

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(afDir, "config.toml")
	// Seed with a valid initial config.
	if err := os.WriteFile(configPath, []byte(`hub_url = ""`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	rng := rand.New(rand.NewSource(42))

	for i := range 50 {
		// Generate a random config.
		hubURL := randomURLString(rng)
		apiKey := randomSlugString(rng)
		nKeys := rng.Intn(4) // 0-3 key entries
		keys := make(map[string]cliconfig.KeyEntry, nKeys)
		for range nKeys {
			slug := randomSlugString(rng)
			keys[slug] = cliconfig.KeyEntry{
				KeyID: randomHexString(rng, 8),
				Token: "af_" + randomHexString(rng, 8) + "_secret",
				Label: randomSlugString(rng),
			}
		}

		cfg := &cliconfig.Config{
			HubURL: hubURL,
			APIKey: apiKey,
			Keys:   keys,
		}

		err := cliconfig.WriteConfigAtomic(homeDir, cfg)
		if err != nil {
			t.Fatalf("iteration %d: WriteConfigAtomic error: %v", i, err)
		}

		// Read the config file and assert it is valid TOML.
		content, readErr := os.ReadFile(configPath)
		if readErr != nil {
			t.Fatalf("iteration %d: read error: %v", i, readErr)
		}

		var parsed cliconfig.Config
		if _, decodeErr := toml.Decode(string(content), &parsed); decodeErr != nil {
			t.Fatalf("iteration %d: config file is NOT valid TOML after atomic write: %v\nContent:\n%s",
				i, decodeErr, content)
		}
	}
}

func TestPropertyAtomicWrite_FailedRenamePreservesOriginal(t *testing.T) {
	// For any config mutation where rename fails, the original file must be
	// unchanged — it must still contain the exact same bytes.

	homeDir := t.TempDir()
	afDir := filepath.Join(homeDir, ".af")
	if err := os.MkdirAll(afDir, 0700); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(afDir, "config.toml")

	rng := rand.New(rand.NewSource(99))

	for i := range 30 {
		// Write a random "original" config directly to disk.
		originalContent := fmt.Sprintf("hub_url = %q\napi_key = %q\n",
			randomURLString(rng), randomSlugString(rng))
		if err := os.WriteFile(configPath, []byte(originalContent), 0600); err != nil {
			t.Fatalf("iteration %d: setup write error: %v", i, err)
		}
		originalBytes := []byte(originalContent)

		// Now attempt a mutation with a failing rename.
		newCfg := &cliconfig.Config{
			HubURL: "https://should-not-be-written.example.com",
			APIKey: "should-not-appear",
		}

		_ = cliconfig.WriteConfigAtomicWith(homeDir, newCfg,
			os.CreateTemp,
			func(oldpath, newpath string) error {
				return os.ErrPermission
			},
		)

		afterBytes, readErr := os.ReadFile(configPath)
		if readErr != nil {
			t.Fatalf("iteration %d: read error after failed rename: %v", i, readErr)
		}

		if string(afterBytes) != string(originalBytes) {
			t.Fatalf("iteration %d: original config was modified after failed rename!\nExpected: %q\nGot:     %q",
				i, originalBytes, afterBytes)
		}
	}
}

// ---------------------------------------------------------------------------
// TS-05-P4: Empty string config values fall through to next precedence level
// PROP: 05-PROP-4
// Validates: 05-REQ-2.3, 05-REQ-3.1, 05-REQ-3.2
// ---------------------------------------------------------------------------

func TestPropertyResolveHubURL_EmptyConfigNeverReturnsEmptySuccess(t *testing.T) {
	// For any config with hub_url = "" and any flag/env combination:
	// - If flag != "", result == flag
	// - If flag == "" and env != "", result == env
	// - If flag == "" and env == "", must return error (not empty string)

	f := func(flag, env string) bool {
		cfg := &cliconfig.Config{
			HubURL: "", // always empty
		}

		result, err := cliconfig.ResolveHubURL(flag, env, cfg)

		if flag != "" {
			return err == nil && result == flag
		}
		if env != "" {
			return err == nil && result == env
		}
		// Both empty + config empty → must error
		return err != nil && result == ""
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("property violation: empty config hub_url should fall through: %v", err)
	}
}

func TestPropertyResolveAPIKey_EmptyConfigNeverReturnsEmptySuccess(t *testing.T) {
	// For any config with api_key = "" and any flag/env combination:
	// - If flag != "", result == flag
	// - If flag == "" and env != "", result == env
	// - If flag == "" and env == "", must return error (not empty string)

	f := func(flag, env string) bool {
		cfg := &cliconfig.Config{
			APIKey: "", // always empty
			Keys:   map[string]cliconfig.KeyEntry{},
		}

		result, err := cliconfig.ResolveAPIKey(flag, env, cfg)

		if flag != "" {
			return err == nil && result == flag
		}
		if env != "" {
			return err == nil && result == env
		}
		// Both empty + config empty → must error
		return err != nil && result == ""
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 200}); err != nil {
		t.Errorf("property violation: empty config api_key should fall through: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Helpers for property-based test data generation
// ---------------------------------------------------------------------------

const slugChars = "abcdefghijklmnopqrstuvwxyz0123456789-_"
const hexChars = "0123456789abcdef"

func randomSlugString(rng *rand.Rand) string {
	length := 3 + rng.Intn(12)
	var sb strings.Builder
	for range length {
		sb.WriteByte(slugChars[rng.Intn(len(slugChars))])
	}
	return sb.String()
}

func randomHexString(rng *rand.Rand, length int) string {
	var sb strings.Builder
	for range length {
		sb.WriteByte(hexChars[rng.Intn(len(hexChars))])
	}
	return sb.String()
}

func randomURLString(rng *rand.Rand) string {
	domains := []string{
		"https://hub.example.com",
		"https://staging.example.com",
		"https://prod.example.org",
		"https://local.dev:8080",
		"",
	}
	return domains[rng.Intn(len(domains))]
}
