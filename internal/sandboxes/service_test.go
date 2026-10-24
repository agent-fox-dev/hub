package sandboxes

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/podman"
	"github.com/agent-fox-dev/hub/internal/scrub"
)

// The engine options go to the provider constructor unchanged, defaults included.
func TestBuildProvider_PassesTheEngineOptions(t *testing.T) {
	for _, tc := range []struct {
		edit func(*Config)
		want podman.Options
	}{
		{func(*Config) {}, podman.Options{Binary: "podman"}},
		{func(c *Config) { c.EngineBinary, c.Runtime, c.UserNS = "docker", "runsc", "keep-id" }, podman.Options{Binary: "docker", Runtime: "runsc", UserNS: "keep-id"}},
	} {
		factory := &recordingFactory{provider: newFakeProvider()}
		if _, err := BuildProvider(requireValid(t, edited(tc.edit)), factory.build); err != nil {
			t.Fatalf("BuildProvider: %v", err)
		}
		if !reflect.DeepEqual(factory.options, tc.want) {
			t.Errorf("options = %+v; want %+v", factory.options, tc.want)
		}
	}
}

// Start validates first, then builds and probes the provider once with a
// deadline within ten seconds, and keeps the provider and the profiles in
// file order without ever calling Create.
func TestStart_ProbesOnceAndKeepsProviderAndProfiles(t *testing.T) {
	fake := newFakeProvider()
	factory := &recordingFactory{provider: fake}
	svc, err := Start(configWith(validProfile("alpha"), validProfile("beta")), factory.build)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if svc.Provider() != sandbox.Provider(fake) || factory.calls != 1 || fake.reapCalls != 1 || fake.createCalls != 0 {
		t.Errorf("provider kept %v, factory calls %d, probes %d, creates %d; want true, 1, 1, 0", svc.Provider() == sandbox.Provider(fake), factory.calls, fake.reapCalls, fake.createCalls)
	}
	if left := fake.reapDeadlines[0]; left <= 0 || left > 10*time.Second {
		t.Errorf("the probe had %v left; want a deadline within 10s", left)
	}
	var names []string
	for _, p := range svc.Profiles() {
		names = append(names, p.Name)
	}
	if !slices.Equal(names, []string{"alpha", "beta"}) || svc.MaxSandboxes() != 16 || svc.LeaseTimeout() != 5*time.Minute || svc.ReapInterval() != 10*time.Minute {
		t.Errorf("profiles %v, limit %d, lease timeout %v, reap interval %v", names, svc.MaxSandboxes(), svc.LeaseTimeout(), svc.ReapInterval())
	}
	if svc.scrubValues() != nil || svc.isRemote() || svc.Close() != nil {
		t.Error("a podman service has scrub values, is remote or fails to close")
	}

	// A static failure reports every fault and never builds or probes.
	fake, factory = newFakeProvider(), &recordingFactory{provider: fake}
	_, err = Start(configWith(validProfile("dup"), validProfile("dup")), factory.build)
	requireFailure(t, err, `"dup"`, "name")
	if factory.calls != 0 || fake.total() != 0 {
		t.Errorf("factory calls = %d, provider calls = %d after a static failure; want none", factory.calls, fake.total())
	}
}

// A provider that cannot be built, a probe that fails and a probe that runs out
// of time each refuse start with a sandbox: error naming the provider keys.
func TestStart_ProviderFailuresRefuseStart(t *testing.T) {
	withProbeTimeout(t, 50*time.Millisecond)
	cause := errors.New("engine binary not found")
	for name, tc := range map[string]struct {
		factory *recordingFactory
		is      error
	}{
		"construction fails": {&recordingFactory{err: cause}, cause},
		"probe fails":        {&recordingFactory{provider: &fakeProvider{caps: newFakeProvider().caps, reapErr: cause}}, cause},
		"probe hangs":        {&recordingFactory{provider: &fakeProvider{caps: newFakeProvider().caps, reapBlocks: true}}, context.DeadlineExceeded},
	} {
		t.Run(name, func(t *testing.T) {
			svc, err := Start(validConfig(), tc.factory.build)
			if svc != nil || err == nil || !strings.HasPrefix(err.Error(), "sandbox:") || !errors.Is(err, tc.is) {
				t.Fatalf("Start = (%v, %v); want a sandbox: error wrapping %v", svc, err, tc.is)
			}
			requireFailure(t, err, "sandbox.provider", "sandbox.engine_binary")
		})
	}
}

// Every profile is turned into a spec of its image, fixed env, network,
// resources in bytes, TTL and landlock, and checked once against the
// provider's capabilities; what CheckSpec refuses names the profile, a network
// the provider does not honour names network_modes once, and a size that does
// not fit an int64 names the field.
func TestNew_ChecksEveryProfileAgainstTheProvider(t *testing.T) {
	cfg := configWith(
		profileWith("first", func(p *ProfileConfig) {
			p.CPUs, p.MemoryMB, p.PIDs, p.DiskMB, p.TTL, p.RequireLandlock = 1.5, 512, 100, 2048, "90m", true
			p.Secrets, p.Env = []string{"GIT_PAT"}, map[string]string{"LANG": "C"}
		}),
		validProfile("second"),
	)
	var specs []sandbox.Spec
	check := func(caps sandbox.Capabilities, spec sandbox.Spec) (sandbox.Spec, error) {
		if caps.Provider != "fake" {
			t.Errorf("checked against %q; want the provider's capabilities", caps.Provider)
		}
		specs = append(specs, spec)
		return spec, nil
	}
	if _, err := newService(requireValid(t, cfg), newFakeProvider(), check); err != nil {
		t.Fatalf("newService: %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("CheckSpec ran %d times; want once per profile", len(specs))
	}
	first := specs[0]
	if first.Image != "registry.example/first:1" || !maps.Equal(first.Env, map[string]string{"LANG": "C"}) || first.Network.Mode != sandbox.NetworkNone ||
		first.Resources != (sandbox.Resources{CPUs: 1.5, MemoryBytes: 512 << 20, PIDs: 100, DiskBytes: 2048 << 20}) || first.TTL != 90*time.Minute || !first.RequireLandlock ||
		len(first.Labels) != 0 || first.Workdir != "" {
		t.Errorf("first spec = %+v", first)
	}
	if specs[1].RequireLandlock || specs[1].Resources != (sandbox.Resources{}) {
		t.Errorf("second spec = %+v; want landlock off and default resources", specs[1])
	}

	fake := newFakeProvider()
	fake.caps.Landlock = false
	_, err := New(requireValid(t, configWith(profileWith("big", func(p *ProfileConfig) { p.RequireLandlock = true }), validProfile("small"))), fake)
	if err == nil || !errors.Is(err, sandbox.ErrUnsupported) || !strings.Contains(err.Error(), `profile "big"`) || strings.Contains(err.Error(), `"small"`) {
		t.Errorf("a refused profile: %v; want ErrUnsupported naming big and not small", err)
	}

	cfg = configWith(profileWith("a", func(p *ProfileConfig) { p.Network = strPtr("host") }), profileWith("b", func(p *ProfileConfig) { p.Network = strPtr("host") }))
	cfg.NetworkModes = []string{"none", "host"}
	err = newRefused(t, requireValid(t, cfg), newFakeProvider())
	requireFailure(t, err, "sandbox.network_modes[1]", `"host"`, "it honours")
	if n := len(failures(err)); n != 1 {
		t.Errorf("%d failures; want only the network_modes one:\n%v", n, err)
	}

	err = newRefused(t, requireValid(t, configWith(profileWith("mem", func(p *ProfileConfig) { p.MemoryMB = 1 << 53 }), profileWith("disk", func(p *ProfileConfig) { p.DiskMB = 1 << 53 }))), newFakeProvider())
	requireFailure(t, err, `"mem"`, "memory_mb")
	requireFailure(t, err, `"disk"`, "disk_mb")
}

// noCloseRemote is a RemoteClient without a Close.
type noCloseRemote struct{ *fakeProvider }

func (noCloseRemote) List(context.Context) ([]gateway.Info, error) { return nil, nil }
func (noCloseRemote) Destroy(context.Context, string) error        { return nil }

// newRefused is New on a definition the provider must refuse.
func newRefused(t *testing.T, v *Validated, p sandbox.Provider) error {
	t.Helper()
	svc, err := New(v, p)
	if err == nil || svc != nil {
		t.Fatalf("New = (%v, nil); want an error", svc)
	}
	return err
}

// Shutdown mode is entered once and for good, and changes every end reason to
// shutdown; a nil service is never in it.
func TestService_ShutdownMode(t *testing.T) {
	svc, err := New(requireValid(t, validConfig()), newFakeProvider())
	if err != nil {
		t.Fatal(err)
	}
	if svc.InShutdown() || svc.endReason("api") != "api" || (*Service)(nil).InShutdown() {
		t.Error("a fresh service is in shutdown mode")
	}
	svc.EnterShutdown()
	(*Service)(nil).EnterShutdown()
	if !svc.InShutdown() || svc.endReason("api") != "shutdown" {
		t.Error("EnterShutdown did not put the service in shutdown mode")
	}
}

// Provider gateway: the factory gets the URL and the token, one List proves the
// remote, nothing is created, and the capabilities are read once and kept as a
// snapshot the backend and the gateway answer from.
func TestStartRemote_ProbesWithOneListAndSnapshotsCapabilities(t *testing.T) {
	stub := newStubRemote()
	stub.caps.Network = []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkUnrestricted}
	stub.caps.Notes = "Container sandboxes. Sandboxes run on the gateway at gw.example:8443."
	factory := stubFactory(stub, nil)
	cfg := remoteConfig("https://gw.example:8443")
	cfg.NetworkModes = []string{"none", "unrestricted"}

	svc, err := Start(cfg, noPodman(t), WithGatewayFactory(factory.dial))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if factory.callCount() != 1 || factory.urls[0] != "https://gw.example:8443" || factory.tokens[0] != testGatewayToken || stub.listCount() != 1 || stub.createCalls != 0 {
		t.Errorf("factory calls %d (%v), lists %d, creates %d; want 1 with the configured URL and token, 1 and 0", factory.callCount(), factory.urls, stub.listCount(), stub.createCalls)
	}
	backend := newBackend(backendConfig{Service: svc})
	for range 5 {
		if got := backend.Capabilities().Network; !slices.Equal(got, stub.caps.Network) {
			t.Fatalf("the backend reports %v; want %v", got, stub.caps.Network)
		}
	}
	if _, err := gatewayNetworkModes(svc); err != nil {
		t.Fatal(err)
	}
	if stub.capsCalls != 1 {
		t.Errorf("the remote's Capabilities was read %d times; want 1", stub.capsCalls)
	}
	svc.Capabilities().Network[0] = sandbox.NetworkUnrestricted
	if svc.Capabilities().Network[0] != sandbox.NetworkNone {
		t.Error("a caller changed the snapshot")
	}
	if notes := svc.Capabilities().Notes; strings.Contains(notes, "gw.example") || !strings.Contains(notes, "Container sandboxes") {
		t.Errorf("the snapshot's notes = %q; want the remote's name scrubbed and the rest kept", notes)
	}
	// The scrub set: the token, the URL, the host and the bare host name, copied.
	if got := svc.scrubValues(); !slices.Equal(got, []string{testGatewayToken, "https://gw.example:8443", "gw.example:8443", "gw.example"}) {
		t.Errorf("scrub set = %q", got)
	}
	svc.scrubValues()[0] = "changed"
	if svc.scrubValues()[0] == "changed" {
		t.Error("the scrub set is not a copy")
	}
	out := scrub.Text("lookup gw.example: no such host; certificate for other.example; "+testGatewayToken, svc.scrubValues()...)
	if strings.Contains(out, "gw.example") || strings.Contains(out, testGatewayToken) || !strings.Contains(out, "other.example") {
		t.Errorf("scrubbed = %q", out)
	}
}

// A static failure, a factory error, a failed or hanging List and a factory
// that returns no client each refuse start naming the three keys and holding
// neither the token nor URL user information; a client that was built is closed.
func TestStartRemote_FailuresRefuseStart(t *testing.T) {
	withProbeTimeout(t, 200*time.Millisecond)
	leak := "dial https://user:pw@host.example with " + testGatewayToken + " failed"
	for name, tc := range map[string]struct {
		edit    func(*Config)
		stub    func(*stubRemote)
		factory func(*stubRemote) *remoteFactory
		want    []string
		closes  int
	}{
		"static failure": {edit: func(c *Config) { c.GatewayURL, c.MaxTTL = "https://gw.example/api", strPtr("bogus") },
			want: []string{"sandbox.gateway_url"}},
		"factory error": {factory: func(*stubRemote) *remoteFactory { return stubFactory(nil, errors.New(leak)) }, want: []string{"[redacted]", `sandbox.gateway_url "https://gw.example"`}},
		"no client":     {factory: func(*stubRemote) *remoteFactory { return stubFactory(nil, nil) }, want: []string{"no client"}},
		"List fails":    {stub: func(s *stubRemote) { s.listErr = errors.New(leak) }, want: []string{"[redacted]"}, closes: 1},
		"List hangs":    {stub: func(s *stubRemote) { s.listBlock = true }, want: []string{"no answer within 200ms"}, closes: 1},
		"profile refused": {edit: func(c *Config) { c.NetworkModes = []string{"none", "unrestricted"} }, // the stub honours none only
			want: []string{"sandbox.network_modes[1]"}, closes: 1},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := remoteConfig("https://gw.example")
			if tc.edit != nil {
				tc.edit(cfg)
			}
			stub := newStubRemote()
			if tc.stub != nil {
				tc.stub(stub)
			}
			factory := stubFactory(stub, nil)
			if tc.factory != nil {
				factory = tc.factory(stub)
			}
			svc, err := Start(cfg, noPodman(t), WithGatewayFactory(factory.dial))
			if svc != nil || err == nil {
				t.Fatalf("Start = (%v, %v); want a refusal", svc, err)
			}
			if name != "static failure" && name != "profile refused" {
				if !strings.HasPrefix(err.Error(), "sandbox:") {
					t.Errorf("error %q does not start with sandbox:", err)
				}
				requireFailure(t, err, "sandbox.provider", "sandbox.gateway_url", "sandbox.gateway_token")
			} else if name == "static failure" && factory.callCount() != 0 {
				t.Error("a static failure built a client")
			}
			requireFailure(t, err, tc.want...)
			containsNone(t, "the error", err.Error(), testGatewayToken, "user:pw", "pw@")
			if stub.closes != tc.closes {
				t.Errorf("the client was closed %d times; want %d", stub.closes, tc.closes)
			}
		})
	}
}

// The profile rules and messages are the same under both providers, given the
// same capabilities.
func TestStart_SameRulesForBothProviders(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(*Config)
		set  func(*sandbox.Capabilities)
		ok   bool
	}{
		"valid":             {ok: true},
		"bad max_ttl":       {edit: func(c *Config) { c.MaxTTL = strPtr("bogus") }},
		"ttl above max_ttl": {edit: func(c *Config) { c.MaxTTL = strPtr("30m") }},
		"mode not honoured": {edit: func(c *Config) { c.NetworkModes = []string{"none", "host"} }},
		"CheckSpec refuses": {edit: func(c *Config) { c.Profiles[0].CPUs = 2 }, set: func(c *sandbox.Capabilities) { c.Resources.CPUs = false }},
	} {
		t.Run(name, func(t *testing.T) {
			podmanCfg, gatewayCfg := validConfig(), remoteConfig("https://gw.example")
			fake, remote := newFakeProvider(), newStubRemote()
			if tc.edit != nil {
				tc.edit(podmanCfg)
				tc.edit(gatewayCfg)
			}
			if tc.set != nil {
				tc.set(&fake.caps)
				tc.set(&remote.caps)
			}
			podmanSvc, podmanErr := Start(podmanCfg, func(podman.Options) (sandbox.Provider, error) { return fake, nil })
			gatewaySvc, gatewayErr := Start(gatewayCfg, noPodman(t), WithGatewayFactory(stubFactory(remote, nil).dial))
			if (podmanErr == nil) != tc.ok || (gatewayErr == nil) != tc.ok {
				t.Fatalf("podman %v, gateway %v; want ok %t for both", podmanErr, gatewayErr, tc.ok)
			}
			if tc.ok {
				if !reflect.DeepEqual(podmanSvc.Profiles(), gatewaySvc.Profiles()) {
					t.Errorf("the profiles differ: %+v and %+v", podmanSvc.Profiles(), gatewaySvc.Profiles())
				}
			} else if podmanErr.Error() != gatewayErr.Error() {
				t.Errorf("the messages differ:\npodman:  %v\ngateway: %v", podmanErr, gatewayErr)
			}
		})
	}
}

// Close closes the remote client once, from any number of goroutines, with the
// scrubbed error of the first Close returned by every call; a client without
// Close and a nil service close without an error.
func TestServiceClose_ClosesTheRemoteOnce(t *testing.T) {
	log := &callLog{}
	boom := errors.New("closing the connection to gw.example failed with " + testGatewayToken)
	client := scriptedRemote{recordingProvider: newRecordingProvider(), closeLog: log, closeErr: boom}
	svc, err := Start(remoteConfig("https://gw.example"), noPodman(t), WithGatewayFactory(stubFactory(client, nil).dial))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := svc.Close(); err == nil || strings.Contains(err.Error(), "gw.example") || strings.Contains(err.Error(), testGatewayToken) || !strings.Contains(err.Error(), "[redacted]") {
				t.Errorf("Close = %v; want the scrubbed error", err)
			}
		})
	}
	wg.Wait()
	if n := len(log.all()); n != 1 {
		t.Errorf("the client was closed %d times; want 1", n)
	}
	plain, err := Start(remoteConfig("https://gw.example"), noPodman(t), WithGatewayFactory(stubFactory(noCloseRemote{newFakeProvider()}, nil).dial))
	if err != nil {
		t.Fatal(err)
	}
	if plain.Close() != nil || (*Service)(nil).Close() != nil {
		t.Error("a client without Close or a nil service failed to close")
	}
}
