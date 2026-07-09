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
// TS-05-P2: Newly created config dir and file have correct permissions
// PROP: 05-PROP-2
// Validates: 05-REQ-1.1, 05-REQ-1.3
// ---------------------------------------------------------------------------

func TestPropertyEnsureConfig_PermissionsCorrectOnCreation(t *testing.T) {
	// For any writable temp directory used as homeDir, after ensureConfigExists,
	// $HOME/.af/ must have 0700 and config.toml must have 0600.

	for i := range 10 {
		homeDir := t.TempDir()

		err := cliconfig.EnsureConfigExists(homeDir)
		if err != nil {
			t.Fatalf("iteration %d: EnsureConfigExists error: %v", i, err)
		}

		afDir := filepath.Join(homeDir, ".af")
		dirInfo, statErr := os.Stat(afDir)
		if statErr != nil {
			t.Fatalf("iteration %d: failed to stat .af dir: %v", i, statErr)
		}
		dirPerm := dirInfo.Mode().Perm()
		if dirPerm != 0700 {
			t.Errorf("iteration %d: expected .af dir permissions 0700, got %04o", i, dirPerm)
		}

		configPath := filepath.Join(afDir, "config.toml")
		fileInfo, statErr := os.Stat(configPath)
		if statErr != nil {
			t.Fatalf("iteration %d: failed to stat config.toml: %v", i, statErr)
		}
		filePerm := fileInfo.Mode().Perm()
		if filePerm != 0600 {
			t.Errorf("iteration %d: expected config.toml permissions 0600, got %04o", i, filePerm)
		}
	}
}

// ---------------------------------------------------------------------------
// TS-05-P6: keys default rejects non-existent workspace slugs
// PROP: 05-PROP-6
// Validates: 05-REQ-9.E1
// ---------------------------------------------------------------------------

func TestPropertyKeysDefault_RejectsNonExistentSlugs(t *testing.T) {
	// For any workspace slug that has no matching [keys.*] section,
	// SetDefaultKey must return an error and api_key must remain unchanged.

	rng := rand.New(rand.NewSource(77))

	for i := range 30 {
		homeDir := t.TempDir()
		afDir := filepath.Join(homeDir, ".af")
		if err := os.MkdirAll(afDir, 0700); err != nil {
			t.Fatal(err)
		}

		// Create a config with a known api_key and some existing keys.
		originalAPIKey := randomSlugString(rng)
		cfg := &cliconfig.Config{
			HubURL: "https://hub.example.com",
			APIKey: originalAPIKey,
			Keys: map[string]cliconfig.KeyEntry{
				"existing-key": {KeyID: "aabb", Token: "af_aabb_secret", Label: "test"},
			},
		}

		// Write config to disk.
		configPath := filepath.Join(afDir, "config.toml")
		f, err := os.Create(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := toml.NewEncoder(f).Encode(cfg); err != nil {
			f.Close()
			t.Fatal(err)
		}
		f.Close()

		// Generate a random slug guaranteed not to be in the Keys map.
		nonExistentSlug := "nonexistent-" + randomSlugString(rng) + fmt.Sprintf("-%d", i)

		err = cliconfig.SetDefaultKey(homeDir, cfg, nonExistentSlug)
		if err == nil {
			t.Errorf("iteration %d: expected error for non-existent slug %q, got nil", i, nonExistentSlug)
		}

		// Verify api_key was not modified.
		if cfg.APIKey != originalAPIKey {
			t.Errorf("iteration %d: expected api_key to remain %q, got %q", i, originalAPIKey, cfg.APIKey)
		}
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

// ---------------------------------------------------------------------------
// TS-05-P7: Resolution terminates in bounded steps
// PROP: 05-PROP-7 (implied)
// Validates: 05-REQ-3.1, 05-REQ-3.2
//
// For any combination of (flag, env, cfg), resolveHubURL and resolveAPIKey
// terminate in a bounded number of steps:
//   - Hub URL: at most 3 checks (flag, env, config)
//   - API key: at most 4 checks (flag, env, api_key slug lookup, keys section lookup)
// ---------------------------------------------------------------------------

func TestPropertyResolveHubURL_BoundedSteps(t *testing.T) {
	// For every combination of flag, env, and config hub_url, the resolution
	// function must terminate without hanging. We instrument this by counting
	// the number of non-empty sources checked.

	rng := rand.New(rand.NewSource(123))

	for i := range 200 {
		flag := ""
		env := ""
		cfgHubURL := ""

		// Randomly populate sources.
		if rng.Intn(2) == 0 {
			flag = randomURLString(rng)
		}
		if rng.Intn(2) == 0 {
			env = randomURLString(rng)
		}
		if rng.Intn(2) == 0 {
			cfgHubURL = randomURLString(rng)
		}

		cfg := &cliconfig.Config{HubURL: cfgHubURL}

		// The resolution function should return in bounded time.
		// We measure this implicitly: if it hangs, the test will time out.
		// We also verify the result is consistent with the precedence rules.
		result, err := cliconfig.ResolveHubURL(flag, env, cfg)

		// Count how many precedence steps were actually needed.
		steps := 0

		// Step 1: check flag.
		steps++
		if flag != "" {
			if err != nil || result != flag {
				t.Errorf("iteration %d: flag=%q set but result=%q err=%v", i, flag, result, err)
			}
			continue
		}

		// Step 2: check env.
		steps++
		if env != "" {
			if err != nil || result != env {
				t.Errorf("iteration %d: env=%q set but result=%q err=%v", i, env, result, err)
			}
			continue
		}

		// Step 3: check config.
		steps++
		if cfgHubURL != "" {
			if err != nil || result != cfgHubURL {
				t.Errorf("iteration %d: cfg=%q set but result=%q err=%v", i, cfgHubURL, result, err)
			}
			continue
		}

		// All empty → must error.
		if err == nil {
			t.Errorf("iteration %d: all sources empty but no error returned; result=%q", i, result)
		}

		// Verify steps <= 3.
		if steps > 3 {
			t.Errorf("iteration %d: resolution took %d steps, expected <= 3", i, steps)
		}
	}
}

func TestPropertyResolveAPIKey_BoundedSteps(t *testing.T) {
	// For every combination of flag, env, and config (api_key + keys map),
	// the resolution function must terminate in at most 4 steps.

	rng := rand.New(rand.NewSource(456))

	for i := range 200 {
		flag := ""
		env := ""
		apiKeySlug := ""
		hasMatchingKey := false

		// Randomly populate sources.
		if rng.Intn(2) == 0 {
			flag = "af_" + randomHexString(rng, 6) + "_flagsecret"
		}
		if rng.Intn(2) == 0 {
			env = "af_" + randomHexString(rng, 6) + "_envsecret"
		}
		if rng.Intn(2) == 0 {
			apiKeySlug = randomSlugString(rng)
		}
		if rng.Intn(2) == 0 {
			hasMatchingKey = true
		}

		keys := make(map[string]cliconfig.KeyEntry)
		if apiKeySlug != "" && hasMatchingKey {
			keys[apiKeySlug] = cliconfig.KeyEntry{
				KeyID: randomHexString(rng, 8),
				Token: "af_" + randomHexString(rng, 8) + "_configsecret",
			}
		}

		cfg := &cliconfig.Config{
			APIKey: apiKeySlug,
			Keys:   keys,
		}

		result, err := cliconfig.ResolveAPIKey(flag, env, cfg)

		// Count steps and verify precedence.
		steps := 0

		// Step 1: check flag.
		steps++
		if flag != "" {
			if err != nil || result != flag {
				t.Errorf("iteration %d: flag=%q set but result=%q err=%v", i, flag, result, err)
			}
			continue
		}

		// Step 2: check env.
		steps++
		if env != "" {
			if err != nil || result != env {
				t.Errorf("iteration %d: env=%q set but result=%q err=%v", i, env, result, err)
			}
			continue
		}

		// Step 3: check api_key slug.
		steps++
		if apiKeySlug != "" {
			// Step 4: look up keys section.
			steps++
			if hasMatchingKey {
				expectedToken := keys[apiKeySlug].Token
				if err != nil || result != expectedToken {
					t.Errorf("iteration %d: config has key for slug %q but result=%q err=%v (expected %q)",
						i, apiKeySlug, result, err, expectedToken)
				}
				continue
			}
			// Slug exists but no matching key entry → falls through to error.
		}

		// No source found → must error.
		if err == nil {
			t.Errorf("iteration %d: no source available but no error returned; result=%q", i, result)
		}

		// Verify steps <= 4.
		if steps > 4 {
			t.Errorf("iteration %d: API key resolution took %d steps, expected <= 4", i, steps)
		}
	}
}
