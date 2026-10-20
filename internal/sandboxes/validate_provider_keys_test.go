package sandboxes

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/podman"
)

// TS-29-1: An unknown provider value is refused with both accepted values listed,
// and no provider is built.
// Requirement: 29-REQ-1.2
func TestValidate_UnknownProviderListsBoth_TS_29_1(t *testing.T) {
	cfg := validConfig()
	cfg.Provider = "kubernetes"

	_, err := Validate(cfg)
	requireFailure(t, err, "sandbox.provider", "kubernetes", "podman", "gateway")

	var podmanCalls atomic.Int32
	_, err = Start(cfg, func(podman.Options) (sandbox.Provider, error) {
		podmanCalls.Add(1)
		return newFakeProvider(), nil
	})
	requireFailure(t, err, "sandbox.provider", "podman", "gateway")
	if n := podmanCalls.Load(); n != 0 {
		t.Errorf("the podman factory was called %d times for an unknown provider", n)
	}

	// An absent provider lists them as well.
	cfg.Provider = ""
	_, err = Validate(cfg)
	requireFailure(t, err, "sandbox.provider", "required", "podman", "gateway")
}

// TS-29-15: engine_binary, runtime and userns under provider gateway are each
// refused, one failure per key.
// Requirement: 29-REQ-4.1
func TestValidate_PodmanKeysUnderGateway_TS_29_15(t *testing.T) {
	keys := map[string]func(*Config){
		"sandbox.engine_binary": func(c *Config) { c.EngineBinary = "/usr/bin/podman" },
		"sandbox.runtime":       func(c *Config) { c.Runtime = "krun" },
		"sandbox.userns":        func(c *Config) { c.UserNS = "keep-id" },
	}
	for key, set := range keys {
		cfg := remoteConfig("https://gw.example")
		set(cfg)
		_, err := Validate(cfg)
		requireFailure(t, err, key, "podman")
		requireFailure(t, err, key, "applies only to")
		if n := len(failures(err)); n != 1 {
			t.Errorf("%s alone gave %d failures, want 1:\n%v", key, n, err)
		}
	}

	cfg := remoteConfig("https://gw.example")
	for _, set := range keys {
		set(cfg)
	}
	_, err := Validate(cfg)
	for key := range keys {
		requireFailure(t, err, key, "podman")
	}
	if n := len(failures(err)); n != 3 {
		t.Errorf("the three keys gave %d failures, want 3:\n%v", n, err)
	}

	// Under podman the same keys are accepted, and the engine binary defaults.
	cfg = validConfig()
	cfg.Runtime = "krun"
	if v := requireValid(t, cfg); v.EngineBinary != "podman" || v.Runtime != "krun" {
		t.Errorf("podman engine_binary, runtime = %q, %q", v.EngineBinary, v.Runtime)
	}
	// Under gateway there is no engine binary to default.
	if v := requireValid(t, remoteConfig("https://gw.example")); v.EngineBinary != "" {
		t.Errorf("the validated engine_binary under gateway = %q; want none", v.EngineBinary)
	}
}

// TS-29-16: gateway_url and gateway_token under provider podman are each refused,
// saying they apply to provider gateway only, without echoing the token.
// Requirement: 29-REQ-4.2
func TestValidate_GatewayKeysUnderPodman_TS_29_16(t *testing.T) {
	const token = "tok-SECRET-0123456789"
	cfg := validConfig()
	cfg.GatewayURL = "https://gw.example"
	cfg.GatewayToken = token
	_, err := Validate(cfg)
	if err == nil {
		t.Fatal("provider podman with gateway_url and gateway_token was accepted")
	}
	requireFailure(t, err, "sandbox.gateway_url", "gateway", "applies only to")
	requireFailure(t, err, "sandbox.gateway_token", "gateway", "applies only to")
	if n := len(failures(err)); n != 2 {
		t.Errorf("the two keys gave %d failures, want 2:\n%v", n, err)
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("the error repeats the token: %v", err)
	}

	for _, set := range []func(*Config){
		func(c *Config) { c.GatewayURL = "https://gw.example" },
		func(c *Config) { c.GatewayToken = token },
	} {
		cfg := validConfig()
		set(cfg)
		_, err := Validate(cfg)
		if n := len(failures(err)); n != 1 {
			t.Errorf("one gateway key under podman gave %d failures, want 1: %v", n, err)
		}
	}
}

// TS-29-17: The failures of other-provider keys are collected with the other
// static failures in one error.
// Requirement: 29-REQ-4.3
func TestValidate_ProviderKeysJoinOtherFailures_TS_29_17(t *testing.T) {
	cfg := remoteConfig("https://gw.example")
	cfg.Runtime = "krun"
	cfg.MaxTTL = strPtr("bogus")
	_, err := Validate(cfg)
	requireFailure(t, err, "sandbox.runtime")
	requireFailure(t, err, "sandbox.max_ttl")

	cfg = validConfig()
	cfg.GatewayToken = "tok-SECRET-0123456789"
	cfg.MaxTTL = strPtr("bogus")
	_, err = Validate(cfg)
	requireFailure(t, err, "sandbox.gateway_token")
	requireFailure(t, err, "sandbox.max_ttl")
}
