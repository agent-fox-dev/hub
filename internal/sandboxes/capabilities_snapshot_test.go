package sandboxes

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/podman"
)

// startBoth starts the same configuration once with provider podman and once with
// provider gateway, both over providers that report the same capabilities: set
// is applied to the capabilities of each. edit changes both configurations. It
// returns the services and errors of the two starts.
func startBoth(t *testing.T, edit func(*Config), set func(*sandbox.Capabilities)) (podmanSvc, gatewaySvc *Service, podmanErr, gatewayErr error) {
	t.Helper()
	podmanCfg, gatewayCfg := validConfig(), remoteConfig("https://gw.example")
	if edit != nil {
		edit(podmanCfg)
		edit(gatewayCfg)
	}
	fake := newFakeProvider()
	remote := newStubRemote()
	if set != nil {
		set(&fake.caps)
		set(&remote.caps)
	}
	podmanSvc, podmanErr = Start(podmanCfg, func(podman.Options) (sandbox.Provider, error) { return fake, nil })
	gatewaySvc, gatewayErr = Start(gatewayCfg, noPodman(t), WithGatewayFactory(stubFactory(remote, nil).dial))
	return podmanSvc, gatewaySvc, podmanErr, gatewayErr
}

// TS-29-4: The same profile set is accepted or rejected identically under podman
// and gateway, with the same message, given the same capabilities.
// Requirement: 29-REQ-1.4
func TestStart_ProfileRulesAreTheSameForBothProviders_TS_29_4(t *testing.T) {
	cases := map[string]struct {
		edit func(*Config)
		set  func(*sandbox.Capabilities)
		ok   bool
	}{
		"valid": {ok: true},
		"valid with two modes": {
			edit: func(c *Config) { c.NetworkModes = []string{"none", "unrestricted"} },
			set:  func(c *sandbox.Capabilities) { c.Network = append(c.Network, sandbox.NetworkUnrestricted) },
			ok:   true,
		},
		"bad max_ttl": {edit: func(c *Config) { c.MaxTTL = strPtr("bogus") }},
		"ttl above max_ttl": {edit: func(c *Config) {
			c.MaxTTL = strPtr("30m")
		}},
		"network_modes entry the provider does not honour": {edit: func(c *Config) { c.NetworkModes = []string{"none", "host"} }},
		"profile rejected by CheckSpec": {
			edit: func(c *Config) { c.Profiles[0].CPUs = 2 },
			set:  func(c *sandbox.Capabilities) { c.Resources.CPUs = false },
		},
		"profile that needs Landlock": {
			edit: func(c *Config) { c.Profiles[0].RequireLandlock = true },
			set:  func(c *sandbox.Capabilities) { c.Landlock = false },
		},
		"no profile": {edit: func(c *Config) { c.Profiles = nil }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			podmanSvc, gatewaySvc, podmanErr, gatewayErr := startBoth(t, tc.edit, tc.set)
			if (podmanErr == nil) != tc.ok || (gatewayErr == nil) != tc.ok {
				t.Fatalf("podman error = %v, gateway error = %v; want success: %t for both", podmanErr, gatewayErr, tc.ok)
			}
			if tc.ok {
				if podmanSvc == nil || gatewaySvc == nil {
					t.Fatal("a successful start returned no service")
				}
				if !reflect.DeepEqual(podmanSvc.Profiles(), gatewaySvc.Profiles()) {
					t.Errorf("the profiles differ: %+v and %+v", podmanSvc.Profiles(), gatewaySvc.Profiles())
				}
				return
			}
			if podmanSvc != nil || gatewaySvc != nil {
				t.Error("a refused start returned a service")
			}
			if podmanErr.Error() != gatewayErr.Error() {
				t.Errorf("the messages differ:\npodman:  %v\ngateway: %v", podmanErr, gatewayErr)
			}
		})
	}
}

// TS-29-23: The remote client's Capabilities is read exactly once across start-up
// and every later use of the hub's backend and gateway.
// Requirement: 29-REQ-6.1
func TestStart_CapabilitiesAreReadOnce_TS_29_23(t *testing.T) {
	stub := newStubRemote()
	cfg := remoteConfig("https://gw.example")
	cfg.NetworkModes = []string{"none", "unrestricted"}
	stub.caps.Network = []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkUnrestricted}

	svc, err := Start(cfg, noPodman(t), WithGatewayFactory(stubFactory(stub, nil).dial))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	backend := newBackend(backendConfig{Service: svc})
	for range 20 {
		if got := backend.Capabilities().Network; !slices.Equal(got, stub.caps.Network) {
			t.Fatalf("the backend reports the modes %v; want %v", got, stub.caps.Network)
		}
	}
	if _, err := gatewayNetworkModes(svc); err != nil {
		t.Fatalf("gatewayNetworkModes: %v", err)
	}
	// A Create builds the spec of the box from the snapshot too; the stub refuses
	// to make the box, which ends the lease and is no matter here.
	env := newBackendEnv(t, leaseEnvOptions{service: svc})
	_, _ = env.create(env.issue(`{"profile":"dev"}`), sandbox.Spec{})
	stub.mu.Lock()
	reads := stub.capsCalls
	stub.mu.Unlock()
	if reads != 1 {
		t.Errorf("the remote client's Capabilities was called %d times; want 1", reads)
	}

	// The snapshot is the service's own copy: changing what a caller got does not
	// change it.
	svc.Capabilities().Network[0] = sandbox.NetworkUnrestricted
	if got := svc.Capabilities().Network[0]; got != sandbox.NetworkNone {
		t.Errorf("a caller changed the snapshot: the first mode is now %v", got)
	}
}

// TS-29-24: A network_modes entry the remote does not offer refuses start with the
// message it has under podman.
// Requirement: 29-REQ-6.2
func TestStart_NetworkModeTheRemoteDoesNotOffer_TS_29_24(t *testing.T) {
	podmanSvc, gatewaySvc, podmanErr, gatewayErr := startBoth(t, func(c *Config) {
		c.NetworkModes = []string{"none", "host"}
	}, func(c *sandbox.Capabilities) { c.Network = []sandbox.NetworkMode{sandbox.NetworkNone} })

	if gatewayErr == nil || podmanErr == nil {
		t.Fatalf("start was not refused: podman %v, gateway %v", podmanErr, gatewayErr)
	}
	if gatewaySvc != nil || podmanSvc != nil {
		t.Error("a refused start returned a service")
	}
	requireFailure(t, gatewayErr, "sandbox.network_modes[1]", `"host"`, "is not a network mode of provider")
	if gatewayErr.Error() != podmanErr.Error() {
		t.Errorf("the messages differ:\npodman:  %v\ngateway: %v", podmanErr, gatewayErr)
	}
}

// TS-29-25: A profile that sandbox.CheckSpec rejects against the snapshot refuses
// start with the per-profile message, unchanged.
// Requirement: 29-REQ-6.3
func TestStart_ProfileRejectedByTheSnapshot_TS_29_25(t *testing.T) {
	stub := newStubRemote()
	stub.caps.Resources.CPUs = false
	cfg := remoteConfig("https://gw.example")
	cfg.Profiles[0].CPUs = 2

	svc, err := Start(cfg, noPodman(t), WithGatewayFactory(stubFactory(stub, nil).dial))
	if err == nil || svc != nil {
		t.Fatalf("Start returned %v, %v; want an error and no service", svc, err)
	}
	v := requireValid(t, cfg)
	mode, _ := networkMode(stub.caps, "none")
	spec, specErr := profileSpec(v.Profiles[0], mode)
	if specErr != nil {
		t.Fatal(specErr)
	}
	_, want := sandbox.CheckSpec(stub.caps, spec)
	if want == nil {
		t.Fatal("CheckSpec accepted a profile that needs CPUs from a provider with none")
	}
	if err.Error() != `profile "dev": `+want.Error() {
		t.Errorf("the error is %q; want the per-profile message %q", err, `profile "dev": `+want.Error())
	}
}

// TS-29-26: The hub backend answers Capabilities from the snapshot, narrowed to
// the modes network_modes allows, while the remote is stopped and without any call
// to it.
// Requirement: 29-REQ-6.4
func TestBackendCapabilities_ComeFromTheSnapshotWhileTheRemoteIsDown_TS_29_26(t *testing.T) {
	env := newRemoteEnv(t, leaseEnvOptions{})
	if remoteModes := env.profile.Provider().Capabilities().Network; len(remoteModes) < 2 {
		t.Fatalf("the remote offers %v; the test needs it to offer more than hub allows", remoteModes)
	}
	env.remote.stop()
	rpcs := env.remote.rpcs()

	began := time.Now()
	got := env.backend.Capabilities()
	if elapsed := time.Since(began); elapsed > time.Second {
		t.Errorf("Capabilities took %v with the remote down", elapsed)
	}
	if want := []sandbox.NetworkMode{sandbox.NetworkNone}; !slices.Equal(got.Network, want) {
		t.Errorf("Network = %v; want %v: the snapshot narrowed to network_modes", got.Network, want)
	}
	if env.remote.rpcs() != rpcs {
		t.Errorf("Capabilities reached the remote")
	}
	if _, err := env.profile.Provider().(RemoteClient).List(context.Background()); err == nil {
		t.Error("the stopped remote still answers a List; the test does not test a down remote")
	}
}
