package sandboxes

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"
)

// profileWith returns a valid profile named name, changed by mutate.
func profileWith(name string, mutate func(*ProfileConfig)) ProfileConfig {
	p := validProfile(name)
	mutate(&p)
	return p
}

// configWith returns validConfig with its profiles replaced.
func configWith(profiles ...ProfileConfig) *Config {
	cfg := validConfig()
	cfg.Profiles = profiles
	return cfg
}

// mustFail returns the validation error of cfg and fails the test when cfg
// validates.
func mustFail(t *testing.T, cfg *Config) error {
	t.Helper()
	v, err := Validate(cfg)
	if err == nil {
		t.Fatalf("Validate accepted an invalid configuration: %+v", v)
	}
	return err
}

// TS-25-18: Negative or fractional resource values fail, zero passes, and
// fractional CPUs are allowed.
// Requirement: 25-REQ-3.5
func TestValidate_Resources_TS_25_18(t *testing.T) {
	cfg := configWith(
		profileWith("neg-cpus", func(p *ProfileConfig) { p.CPUs = -1 }),
		profileWith("neg-memory", func(p *ProfileConfig) { p.MemoryMB = -5 }),
		profileWith("frac-pids", func(p *ProfileConfig) { p.PIDs = 1.5 }),
		profileWith("neg-disk", func(p *ProfileConfig) { p.DiskMB = -1 }),
		profileWith("nan-cpus", func(p *ProfileConfig) { p.CPUs = math.NaN() }),
		profileWith("inf-memory", func(p *ProfileConfig) { p.MemoryMB = math.Inf(1) }),
		profileWith("zero", func(p *ProfileConfig) {}),
	)
	err := mustFail(t, cfg)
	requireFailure(t, err, `"neg-cpus"`, "cpus")
	requireFailure(t, err, `"neg-memory"`, "memory_mb")
	requireFailure(t, err, `"frac-pids"`, "pids")
	requireFailure(t, err, `"neg-disk"`, "disk_mb")
	requireFailure(t, err, `"nan-cpus"`, "cpus")
	requireFailure(t, err, `"inf-memory"`, "memory_mb")
	if strings.Contains(err.Error(), `"zero"`) {
		t.Errorf("the all-zero profile was reported: %v", err)
	}

	cfg = configWith(
		profileWith("zero", func(p *ProfileConfig) {}),
		profileWith("sized", func(p *ProfileConfig) {
			p.CPUs, p.MemoryMB, p.PIDs, p.DiskMB = 1.5, 512, 100, 2048
		}),
	)
	v := requireValid(t, cfg)
	if got := v.Profiles[0].Resources; got != (Resources{}) {
		t.Errorf("zero profile resources = %+v; want all zero (the provider's default)", got)
	}
	if got, want := v.Profiles[1].Resources, (Resources{CPUs: 1.5, MemoryMB: 512, PIDs: 100, DiskMB: 2048}); got != want {
		t.Errorf("sized profile resources = %+v; want %+v", got, want)
	}
}

// TS-25-19: A profile network outside network_modes fails; an absent network
// defaults to none.
// Requirement: 25-REQ-3.6
func TestValidate_Network_TS_25_19(t *testing.T) {
	cfg := configWith(profileWith("web", func(p *ProfileConfig) { p.Network = strPtr("bridge") }))
	err := mustFail(t, cfg)
	requireFailure(t, err, `"web"`, "network", "bridge")

	cfg = configWith(profileWith("blank", func(p *ProfileConfig) { p.Network = strPtr("") }))
	err = mustFail(t, cfg)
	requireFailure(t, err, `"blank"`, "network")

	v := requireValid(t, configWith(validProfile("plain")))
	if got := v.Profiles[0].Network; got != "none" {
		t.Errorf("validated network = %q; want none for an absent key", got)
	}

	cfg = configWith(profileWith("open", func(p *ProfileConfig) { p.Network = strPtr("unrestricted") }))
	cfg.NetworkModes = []string{"none", "unrestricted"}
	if v := requireValid(t, cfg); v.Profiles[0].Network != "unrestricted" {
		t.Errorf("validated network = %q; want unrestricted", v.Profiles[0].Network)
	}
}

// TS-25-20: ttl is required, positive, parseable and at most max_ttl; a ttl
// equal to max_ttl passes.
// Requirement: 25-REQ-3.7
func TestValidate_TTL_TS_25_20(t *testing.T) {
	setTTL := func(ttl string) func(*ProfileConfig) { return func(p *ProfileConfig) { p.TTL = ttl } }
	cfg := configWith(
		profileWith("missing", setTTL("")),
		profileWith("garbage", setTTL("abc")),
		profileWith("zero", setTTL("0s")),
		profileWith("negative", setTTL("-5m")),
		profileWith("over", setTTL("61m")),
		profileWith("exact", setTTL("1h")),
		profileWith("short", setTTL("90s")),
	)
	cfg.MaxTTL = strPtr("1h")
	err := mustFail(t, cfg)
	for _, name := range []string{"missing", "garbage", "zero", "negative", "over"} {
		requireFailure(t, err, fmt.Sprintf("%q", name), "ttl")
	}
	requireFailure(t, err, `"over"`, "sandbox.max_ttl")
	for _, name := range []string{"exact", "short"} {
		if strings.Contains(err.Error(), fmt.Sprintf("%q", name)) {
			t.Errorf("profile %s with an acceptable ttl was reported: %v", name, err)
		}
	}

	cfg = configWith(profileWith("exact", setTTL("1h")), profileWith("short", setTTL("90s")))
	cfg.MaxTTL = strPtr("1h")
	v := requireValid(t, cfg)
	if v.Profiles[0].TTL != time.Hour || v.Profiles[1].TTL != 90*time.Second {
		t.Errorf("validated ttls = %v, %v; want 1h, 1m30s", v.Profiles[0].TTL, v.Profiles[1].TTL)
	}

	// With an invalid max_ttl the comparison is skipped: the failure is the
	// max_ttl itself, not every profile below the default.
	cfg = configWith(profileWith("long", setTTL("24h")))
	cfg.MaxTTL = strPtr("soon")
	err = mustFail(t, cfg)
	requireFailure(t, err, "sandbox.max_ttl")
	if strings.Contains(err.Error(), `"long"`) {
		t.Errorf("a ttl was compared with an invalid max_ttl: %v", err)
	}
}

// TS-25-21: require_landlock defaults to false when omitted and is kept when
// set.
// Requirement: 25-REQ-3.8
func TestValidate_RequireLandlock_TS_25_21(t *testing.T) {
	v := requireValid(t, configWith(
		validProfile("lax"),
		profileWith("strict", func(p *ProfileConfig) { p.RequireLandlock = true }),
	))
	if v.Profiles[0].RequireLandlock || !v.Profiles[1].RequireLandlock {
		t.Errorf("require_landlock = %v, %v; want false, true", v.Profiles[0].RequireLandlock, v.Profiles[1].RequireLandlock)
	}
}

// TS-25-22: Invalid secret names are reported with profile, list and index.
// Requirement: 25-REQ-4.1
func TestValidate_InvalidSecretNames_TS_25_22(t *testing.T) {
	cfg := configWith(profileWith("agent", func(p *ProfileConfig) {
		p.Secrets = []string{"OK", "1BAD"}
		p.OptionalSecrets = []string{"has-dash"}
	}))
	err := mustFail(t, cfg)
	requireFailure(t, err, `"agent"`, "secrets[1]", "1BAD")
	requireFailure(t, err, `"agent"`, "optional_secrets[0]", "has-dash")
	if strings.Contains(err.Error(), ": secrets[0]") {
		t.Errorf("the valid entry OK was reported: %v", err)
	}
}

// TS-25-23: Case-insensitive duplicate secret names fail within and across
// lists.
// Requirement: 25-REQ-4.2
func TestValidate_DuplicateSecretNames_TS_25_23(t *testing.T) {
	cfg := configWith(
		profileWith("within", func(p *ProfileConfig) { p.Secrets = []string{"API_KEY", "api_key"} }),
		profileWith("across", func(p *ProfileConfig) {
			p.Secrets = []string{"TOKEN"}
			p.OptionalSecrets = []string{"token"}
		}),
		profileWith("distinct", func(p *ProfileConfig) {
			p.Secrets = []string{"ONE"}
			p.OptionalSecrets = []string{"TWO"}
		}),
	)
	err := mustFail(t, cfg)
	requireFailure(t, err, `"within"`, `"api_key"`)
	requireFailure(t, err, `"across"`, `"token"`)
	if strings.Contains(err.Error(), `"distinct"`) {
		t.Errorf("a profile without duplicates was reported: %v", err)
	}
}

// TS-25-24: Reserved names such as GIT_PAT are accepted as secrets.
// Requirement: 25-REQ-4.3
func TestValidate_ReservedSecretNames_TS_25_24(t *testing.T) {
	v := requireValid(t, configWith(profileWith("agent", func(p *ProfileConfig) {
		p.Secrets = []string{"GIT_PAT"}
		p.OptionalSecrets = []string{"GH_TOKEN"}
	})))
	got := v.Profiles[0]
	if !slices.Equal(got.Secrets, []string{"GIT_PAT"}) || !slices.Equal(got.OptionalSecrets, []string{"GH_TOKEN"}) {
		t.Errorf("validated secrets = %v, optional = %v; want [GIT_PAT], [GH_TOKEN]", got.Secrets, got.OptionalSecrets)
	}
}

// TS-25-25: An invalid env key is reported by key without its value.
// Requirement: 25-REQ-4.4
func TestValidate_InvalidEnvKey_TS_25_25(t *testing.T) {
	cfg := configWith(profileWith("agent", func(p *ProfileConfig) {
		p.Env = map[string]string{"BAD-KEY": "supersecretvalue", "GOOD_KEY": "goodsecretvalue"}
	}))
	err := mustFail(t, cfg)
	requireFailure(t, err, `"agent"`, "env", "BAD-KEY")
	for _, leaked := range []string{"supersecretvalue", "goodsecretvalue"} {
		if strings.Contains(err.Error(), leaked) {
			t.Errorf("error leaks the env value %q: %v", leaked, err)
		}
	}
	if strings.Contains(err.Error(), "GOOD_KEY") {
		t.Errorf("the valid env key was reported: %v", err)
	}

	v := requireValid(t, configWith(profileWith("agent", func(p *ProfileConfig) {
		p.Env = map[string]string{"LANG": "C", "_PRIVATE": "x"}
	})))
	if got := v.Profiles[0].Env; len(got) != 2 || got["LANG"] != "C" || got["_PRIVATE"] != "x" {
		t.Errorf("validated env = %v; want the configured pairs", got)
	}
}

// TS-25-26: An env key that collides with a secret name, ignoring case, fails
// without leaking the value.
// Requirement: 25-REQ-4.5
func TestValidate_EnvSecretCollision_TS_25_26(t *testing.T) {
	cfg := configWith(
		profileWith("required", func(p *ProfileConfig) {
			p.Secrets = []string{"GIT_PAT"}
			p.Env = map[string]string{"git_pat": "leakme"}
		}),
		profileWith("optional", func(p *ProfileConfig) {
			p.OptionalSecrets = []string{"X"}
			p.Env = map[string]string{"x": "leakme"}
		}),
		profileWith("apart", func(p *ProfileConfig) {
			p.Secrets = []string{"A"}
			p.Env = map[string]string{"B": "leakme"}
		}),
	)
	err := mustFail(t, cfg)
	requireFailure(t, err, `"required"`, "env", `"git_pat"`)
	requireFailure(t, err, `"optional"`, "env", `"x"`)
	if strings.Contains(err.Error(), "leakme") {
		t.Errorf("error leaks an env value: %v", err)
	}
	if strings.Contains(err.Error(), `"apart"`) {
		t.Errorf("a profile whose env does not collide was reported: %v", err)
	}
}

// fault is an independent static fault of a sandbox section: it changes a
// valid configuration and names what the error must say about it.
type fault struct {
	name   string
	inject func(*Config)
	expect []string
}

// faults is the catalogue TS-25-27 combines. Each fault touches its own
// deployment key or its own profiles, so no fault hides another.
var faults = []fault{
	{"bad provider", func(c *Config) { c.Provider = "kubernetes" }, []string{"sandbox.provider", "kubernetes"}},
	{"bad max_ttl", func(c *Config) { c.MaxTTL = strPtr("soon") }, []string{"sandbox.max_ttl"}},
	{"bad network_modes", func(c *Config) { c.NetworkModes = []string{"none", ""} }, []string{"sandbox.network_modes[1]"}},
	{"bad name", func(c *Config) { c.Profiles = append(c.Profiles, validProfile("Bad_Name")) }, []string{`"Bad_Name"`, "name"}},
	{"duplicate name", func(c *Config) {
		c.Profiles = append(c.Profiles, validProfile("dup"), validProfile("dup"))
	}, []string{`"dup"`, "already used"}},
	{"missing image", func(c *Config) {
		c.Profiles = append(c.Profiles, profileWith("noimage", func(p *ProfileConfig) { p.Image = "" }))
	}, []string{`"noimage"`, "image"}},
	{"negative resource", func(c *Config) {
		c.Profiles = append(c.Profiles, profileWith("negres", func(p *ProfileConfig) { p.MemoryMB = -1 }))
	}, []string{`"negres"`, "memory_mb"}},
	{"bad ttl", func(c *Config) {
		c.Profiles = append(c.Profiles, profileWith("badttl", func(p *ProfileConfig) { p.TTL = "abc" }))
	}, []string{`"badttl"`, "ttl"}},
	{"bad secret", func(c *Config) {
		c.Profiles = append(c.Profiles, profileWith("badsecret", func(p *ProfileConfig) { p.Secrets = []string{"1BAD"} }))
	}, []string{`"badsecret"`, "secrets[0]"}},
	{"env collision", func(c *Config) {
		c.Profiles = append(c.Profiles, profileWith("envclash", func(p *ProfileConfig) {
			p.Secrets = []string{"TOK"}
			p.Env = map[string]string{"tok": "value"}
		}))
	}, []string{`"envclash"`, `"tok"`}},
}

// TS-25-27: For any section with N independent static faults, one joined error
// contains all N messages. Every non-empty subset of the catalogue is checked.
// Requirement: 25-REQ-4.6
func TestValidate_AllFailuresTogether_TS_25_27(t *testing.T) {
	for mask := 1; mask < 1<<len(faults); mask++ {
		cfg := validConfig()
		var injected []fault
		for i, f := range faults {
			if mask&(1<<i) != 0 {
				f.inject(cfg)
				injected = append(injected, f)
			}
		}

		v, err := Validate(cfg)
		if err == nil || v != nil {
			t.Fatalf("subset %b: Validate = (%v, %v); want a nil definition and an error", mask, v, err)
		}
		joined, ok := err.(interface{ Unwrap() []error })
		if !ok {
			t.Fatalf("subset %b: error %T does not join its failures", mask, err)
		}
		for _, f := range injected {
			found := false
			for _, one := range joined.Unwrap() {
				if containsAll(one.Error(), f.expect) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("subset %b: no failure for %q (want all of %q) in:\n%v", mask, f.name, f.expect, err)
			}
		}
	}
}

// containsAll reports whether s contains every one of parts.
func containsAll(s string, parts []string) bool {
	for _, part := range parts {
		if !strings.Contains(s, part) {
			return false
		}
	}
	return true
}
