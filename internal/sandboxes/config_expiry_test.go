package sandboxes

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// expiryConfig returns a configuration that passes validation, changed by edit.
func expiryConfig(edit func(*Config)) *Config {
	cfg := validConfig()
	edit(cfg)
	return cfg
}

// TS-28-1: A [sandbox] section without lease_timeout and reap_interval validates
// to the defaults of 5 minutes and 10 minutes.
// Requirement: 28-REQ-1.1
func TestExpiryKeys_Defaults_TS_28_1(t *testing.T) {
	v := requireValid(t, validConfig())
	if v.LeaseTimeout != 5*time.Minute {
		t.Errorf("LeaseTimeout = %v; want 5m", v.LeaseTimeout)
	}
	if v.ReapInterval != 10*time.Minute {
		t.Errorf("ReapInterval = %v; want 10m", v.ReapInterval)
	}

	// The default is not held to max_ttl: only a value the operator wrote is, so
	// a deployment with a short max_ttl and no lease_timeout still starts. Its
	// leases are bounded by their expires_at all the same.
	short := requireValid(t, expiryConfig(func(c *Config) {
		c.MaxTTL = strPtr("2m")
		c.Profiles[0].TTL = "1m"
	}))
	if short.LeaseTimeout != 5*time.Minute {
		t.Errorf("LeaseTimeout under max_ttl 2m = %v; want the default 5m", short.LeaseTimeout)
	}
}

// TS-28-2: Valid lease_timeout and reap_interval values, every boundary
// included, are decoded, known keys and stored as the durations written.
// Requirement: 28-REQ-1.2
func TestExpiryKeys_ValidAndBoundaries_TS_28_2(t *testing.T) {
	for _, tc := range []struct{ leaseTimeout, reapInterval string }{
		{"1s", "10s"},
		{"1h", "1h"}, // lease_timeout = max_ttl
		{"90s", "30s"},
	} {
		t.Run(tc.leaseTimeout+"/"+tc.reapInterval, func(t *testing.T) {
			path := writeConfigFile(t, t.TempDir(), `[sandbox]
provider      = "podman"
max_ttl       = "1h"
lease_timeout = "`+tc.leaseTimeout+`"
reap_interval = "`+tc.reapInterval+`"

[[sandbox.profiles]]
name  = "dev"
image = "registry.example/dev:1"
ttl   = "1h"
`)
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("LoadConfig reported %v; want lease_timeout and reap_interval to be known keys", err)
			}
			v := requireValid(t, cfg)
			wantLease, _ := time.ParseDuration(tc.leaseTimeout)
			wantReap, _ := time.ParseDuration(tc.reapInterval)
			if v.LeaseTimeout != wantLease || v.ReapInterval != wantReap {
				t.Errorf("validated = %v, %v; want %v, %v", v.LeaseTimeout, v.ReapInterval, wantLease, wantReap)
			}
		})
	}

	t.Run("${NAME} is expanded", func(t *testing.T) {
		t.Setenv("LEASE_TIMEOUT_DISTINCT", "2m")
		path := writeConfigFile(t, t.TempDir(), `[sandbox]
provider      = "podman"
lease_timeout = "${LEASE_TIMEOUT_DISTINCT}"

[[sandbox.profiles]]
name  = "dev"
image = "registry.example/dev:1"
ttl   = "1h"
`)
		if v := requireValid(t, mustLoad(t, path)); v.LeaseTimeout != 2*time.Minute {
			t.Errorf("LeaseTimeout = %v; want 2m", v.LeaseTimeout)
		}
	})
}

// TS-28-3: An unparseable or out-of-range lease_timeout is reported by key,
// together with every other failure of the section. An empty string cannot be
// parsed either (it is what ${NAME} gives when the variable is unset), so it is
// refused and is not the default. The second failure is a bad listen address: the
// spec's example, an unknown runtime, is not something Validate checks.
// Requirement: 28-REQ-1.3
func TestLeaseTimeout_Invalid_TS_28_3(t *testing.T) {
	for _, bad := range []string{"abc", "999ms", "2h", "0s", "-5m", "1h1ns", ""} {
		t.Run(bad, func(t *testing.T) {
			cfg := expiryConfig(func(c *Config) {
				c.MaxTTL = strPtr("1h")
				c.LeaseTimeout = strPtr(bad)
				c.Listen = "nonsense"
			})
			_, err := Validate(cfg)
			if err == nil {
				t.Fatalf("Validate accepted lease_timeout %q", bad)
			}
			requireFailure(t, err, "sandbox.lease_timeout")
			requireFailure(t, err, "sandbox.listen")
		})
	}

	t.Run("a max_ttl that does not parse does not hide the failure", func(t *testing.T) {
		_, err := Validate(expiryConfig(func(c *Config) {
			c.MaxTTL = strPtr("bogus")
			c.LeaseTimeout = strPtr("abc")
		}))
		requireFailure(t, err, "sandbox.max_ttl")
		requireFailure(t, err, "sandbox.lease_timeout")
	})

	t.Run("a max_ttl that does not parse leaves a parseable value alone", func(t *testing.T) {
		_, err := Validate(expiryConfig(func(c *Config) {
			c.MaxTTL = strPtr("bogus")
			c.LeaseTimeout = strPtr("5h") // above the default max_ttl of 4h, which is not what was written
		}))
		requireFailure(t, err, "sandbox.max_ttl")
		for _, msg := range failures(err) {
			if strings.Contains(msg, "sandbox.lease_timeout") {
				t.Errorf("lease_timeout was compared with an unusable max_ttl: %q", msg)
			}
		}
	})
}

// TS-28-4: An unparseable or out-of-range reap_interval is reported by key,
// together with every other failure of the section.
// Requirement: 28-REQ-1.4
func TestReapInterval_Invalid_TS_28_4(t *testing.T) {
	for _, bad := range []string{"xyz", "9s", "61m", "0s", "-1m", "1h1ns", ""} {
		t.Run(bad, func(t *testing.T) {
			cfg := expiryConfig(func(c *Config) {
				c.ReapInterval = strPtr(bad)
				c.Listen = "nonsense"
			})
			_, err := Validate(cfg)
			if err == nil {
				t.Fatalf("Validate accepted reap_interval %q", bad)
			}
			requireFailure(t, err, "sandbox.reap_interval")
			requireFailure(t, err, "sandbox.listen")
		})
	}
}

// TS-28-5: A configuration file without a [sandbox] section reads neither key:
// the feature is off, whatever the file says elsewhere.
// Requirement: 28-REQ-1.5
func TestInit_NoSandboxSectionReadsNeitherKey_TS_28_5(t *testing.T) {
	dir := t.TempDir()
	for name, path := range map[string]string{
		"no file":      filepath.Join(dir, "missing.toml"),
		"no [sandbox]": writeConfigFile(t, dir, "[server]\nport = 9090\n"),
		"keys elsewhere": writeConfigFile(t, t.TempDir(),
			"lease_timeout = \"abc\"\nreap_interval = \"xyz\"\n\n[server]\nlease_timeout = \"abc\"\n"),
	} {
		t.Run(name, func(t *testing.T) {
			logger, _ := newCapturedLogger()
			factory := &recordingFactory{provider: newFakeProvider()}
			svc, err := Init(logger, path, factory.build)
			if svc != nil || err != nil {
				t.Fatalf("Init = (%v, %v); want (nil, nil)", svc, err)
			}
			if factory.calls != 0 {
				t.Errorf("the provider factory was called %d times; want none", factory.calls)
			}
		})
	}
}
