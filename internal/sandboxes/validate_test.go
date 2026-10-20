package sandboxes

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// strPtr returns a pointer to s, for the optional string keys of Config.
func strPtr(s string) *string { return &s }

// validConfig returns a configuration that passes validation: the podman
// provider and one complete profile.
func validConfig() *Config {
	return &Config{
		Provider: "podman",
		Profiles: []ProfileConfig{validProfile("dev")},
	}
}

// validProfile returns a profile that passes validation under validConfig.
func validProfile(name string) ProfileConfig {
	return ProfileConfig{Name: name, Image: "registry.example/" + name + ":1", TTL: "1h"}
}

// failures splits a validation error into its individual messages.
func failures(err error) []string {
	if err == nil {
		return nil
	}
	return strings.Split(err.Error(), "\n")
}

// requireFailure fails the test unless one message of err contains every one
// of parts: the profile and the field named in the same failure.
func requireFailure(t *testing.T, err error, parts ...string) {
	t.Helper()
	for _, msg := range failures(err) {
		ok := true
		for _, part := range parts {
			if !strings.Contains(msg, part) {
				ok = false
				break
			}
		}
		if ok {
			return
		}
	}
	t.Errorf("no failure contains all of %q in:\n%v", parts, err)
}

// requireValid fails the test when cfg does not validate, and returns the
// validated definition.
func requireValid(t *testing.T, cfg *Config) *Validated {
	t.Helper()
	v, err := Validate(cfg)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	if v == nil {
		t.Fatal("Validate returned a nil definition and a nil error")
	}
	return v
}

// TS-25-9: A [sandbox] table with no provider fails naming sandbox.provider.
// Requirement: 25-REQ-2.1
func TestValidate_ProviderRequired_TS_25_9(t *testing.T) {
	// An absent key and provider = "" decode to the same empty value.
	cfg := validConfig()
	cfg.Provider = ""
	_, err := Validate(cfg)
	requireFailure(t, err, "sandbox.provider", "required")
}

// TS-25-10: provider gateway is accepted since spec 29, which replaced the
// refusal of 25-REQ-2.2 (see docs/errata/29_sandbox_gateway_provider_divergences.md),
// and any other unknown provider lists the accepted values.
// Requirements: 25-REQ-2.2, 25-REQ-2.3
func TestValidate_UnsupportedProviders_TS_25_10(t *testing.T) {
	if _, err := Validate(remoteConfig("https://gw.example")); err != nil {
		t.Errorf("provider gateway with a gateway_url and a gateway_token returned error: %v", err)
	}

	cfg := validConfig()
	cfg.Provider = "kubernetes"
	_, err := Validate(cfg)
	requireFailure(t, err, "sandbox.provider", "kubernetes", "podman")
	if strings.Contains(err.Error(), "not supported by this build") {
		t.Errorf("an unknown provider got the gateway message: %v", err)
	}
}

// TS-25-12: max_ttl rejects invalid or non-positive values and defaults to 4h.
// Requirement: 25-REQ-2.5
func TestValidate_MaxTTL_TS_25_12(t *testing.T) {
	for _, bad := range []string{"soon", "0s", "-1h", ""} {
		cfg := validConfig()
		cfg.MaxTTL = strPtr(bad)
		_, err := Validate(cfg)
		requireFailure(t, err, "sandbox.max_ttl")
	}

	cfg := validConfig()
	cfg.Profiles[0].TTL = "4h"
	v := requireValid(t, cfg)
	if v.MaxTTL != 4*time.Hour {
		t.Errorf("effective max_ttl = %v; want 4h when the key is absent", v.MaxTTL)
	}

	cfg = validConfig()
	cfg.MaxTTL = strPtr("90m")
	if v := requireValid(t, cfg); v.MaxTTL != 90*time.Minute {
		t.Errorf("effective max_ttl = %v; want the configured 90m", v.MaxTTL)
	}
}

// TS-25-13: network_modes rejects empty and duplicate entries and defaults to
// ["none"].
// Requirement: 25-REQ-2.6
func TestValidate_NetworkModes_TS_25_13(t *testing.T) {
	cfg := validConfig()
	cfg.NetworkModes = []string{"none", ""}
	_, err := Validate(cfg)
	requireFailure(t, err, "sandbox.network_modes[1]")

	cfg = validConfig()
	cfg.NetworkModes = []string{"none", "unrestricted", "none"}
	_, err = Validate(cfg)
	requireFailure(t, err, "sandbox.network_modes[2]", "none")

	v := requireValid(t, validConfig())
	if !slices.Equal(v.NetworkModes, []string{"none"}) {
		t.Errorf("effective network_modes = %v; want [none] when the key is absent", v.NetworkModes)
	}

	cfg = validConfig()
	cfg.NetworkModes = []string{"none", "unrestricted"}
	if v := requireValid(t, cfg); !slices.Equal(v.NetworkModes, []string{"none", "unrestricted"}) {
		t.Errorf("effective network_modes = %v; want the configured list", v.NetworkModes)
	}
}

// The engine options default to podman, "" and "" and a configured value is
// kept as it is.
// Requirement: 25-REQ-2.4
func TestValidate_EngineOptionDefaults(t *testing.T) {
	v := requireValid(t, validConfig())
	if v.EngineBinary != "podman" || v.Runtime != "" || v.UserNS != "" {
		t.Errorf("defaults = (%q, %q, %q); want (podman, \"\", \"\")", v.EngineBinary, v.Runtime, v.UserNS)
	}

	cfg := validConfig()
	cfg.EngineBinary, cfg.Runtime, cfg.UserNS = "docker", "krun", "keep-id"
	v = requireValid(t, cfg)
	if v.EngineBinary != "docker" || v.Runtime != "krun" || v.UserNS != "keep-id" {
		t.Errorf("options = (%q, %q, %q); want the configured values unchanged", v.EngineBinary, v.Runtime, v.UserNS)
	}
}

// TS-25-14: A sandbox section with no profiles fails.
// Requirement: 25-REQ-3.1
func TestValidate_AtLeastOneProfile_TS_25_14(t *testing.T) {
	cfg := validConfig()
	cfg.Profiles = nil
	_, err := Validate(cfg)
	requireFailure(t, err, "sandbox.profiles", "at least one profile")
}

// TS-25-15: Missing, malformed and duplicate profile names are reported with
// the profile identifier and the name field.
// Requirement: 25-REQ-3.2
func TestValidate_ProfileNames_TS_25_15(t *testing.T) {
	long64 := strings.Repeat("a", 64)
	cfg := validConfig()
	cfg.Profiles = []ProfileConfig{
		validProfile(""),
		validProfile("Upper"),
		validProfile("-lead"),
		validProfile(long64),
		validProfile("dev"),
		validProfile("dev"),
	}
	cfg.Profiles[0].Image = "registry.example/unnamed:1"
	_, err := Validate(cfg)
	if err == nil {
		t.Fatal("Validate accepted invalid profile names")
	}

	requireFailure(t, err, "sandbox.profiles[0]", "name")
	requireFailure(t, err, `"Upper"`, "name")
	requireFailure(t, err, `"-lead"`, "name")
	requireFailure(t, err, long64, "name")
	requireFailure(t, err, `"dev"`, "name", "sandbox.profiles[4]")
	// The first of the two "dev" profiles is not the duplicate.
	for _, msg := range failures(err) {
		if strings.Contains(msg, `"dev"`) && strings.Contains(msg, "sandbox.profiles[5]") {
			t.Errorf("the first profile named dev was reported as a duplicate: %s", msg)
		}
	}

	long63 := strings.Repeat("a", 63)
	cfg = validConfig()
	cfg.Profiles = []ProfileConfig{validProfile(long63), validProfile("a_b-c1"), validProfile("0lead")}
	requireValid(t, cfg)
}

// TS-25-16: Two default profiles fail, one or none pass.
// Requirement: 25-REQ-3.3
func TestValidate_DefaultProfile_TS_25_16(t *testing.T) {
	profiles := func(defaults ...bool) []ProfileConfig {
		out := []ProfileConfig{validProfile("alpha"), validProfile("beta"), validProfile("gamma")}
		for i, d := range defaults {
			out[i].Default = d
		}
		return out
	}

	cfg := validConfig()
	cfg.Profiles = profiles(true, true)
	_, err := Validate(cfg)
	requireFailure(t, err, "default", `"alpha"`, `"beta"`)
	if strings.Contains(err.Error(), `"gamma"`) {
		t.Errorf("a profile that is not the default was named: %v", err)
	}

	cfg.Profiles = profiles(true, true, true)
	_, err = Validate(cfg)
	requireFailure(t, err, "default", `"alpha"`, `"beta"`, `"gamma"`)

	cfg.Profiles = profiles(false, true)
	if v := requireValid(t, cfg); !v.Profiles[1].Default || v.Profiles[0].Default {
		t.Errorf("validated defaults = %v, %v; want only beta", v.Profiles[0].Default, v.Profiles[1].Default)
	}

	cfg.Profiles = profiles()
	requireValid(t, cfg)
}

// TS-25-17: A missing or empty image is reported with profile and field.
// Requirement: 25-REQ-3.4
func TestValidate_Image_TS_25_17(t *testing.T) {
	cfg := validConfig()
	cfg.Profiles = []ProfileConfig{validProfile("a"), validProfile("b"), validProfile("ok")}
	cfg.Profiles[0].Image = ""
	cfg.Profiles[1].Image = ""
	_, err := Validate(cfg)
	requireFailure(t, err, `"a"`, "image")
	requireFailure(t, err, `"b"`, "image")
	if strings.Contains(err.Error(), `"ok"`) {
		t.Errorf("a profile with an image was reported: %v", err)
	}
}
