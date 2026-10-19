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
	"github.com/agent-fox-dev/agentkit-go/sandbox/podman"
)

// fakeProvider is a sandbox.Provider test double. Its Capabilities are
// configurable, and Reap, the call the service uses as its probe, records the
// deadline of its context and can fail or block. Every method is counted.
type fakeProvider struct {
	mu sync.Mutex

	caps       sandbox.Capabilities
	reapErr    error
	reapBlocks bool
	failing    bool

	reapCalls     int
	reapDeadlines []time.Duration // time left to the context deadline at each call; -1 for none
	createCalls   int
	capsCalls     int
}

// newFakeProvider returns a provider that honours no network but none, every
// resource bound, images and Landlock.
func newFakeProvider() *fakeProvider {
	return &fakeProvider{caps: sandbox.Capabilities{
		Provider:  "fake",
		Network:   []sandbox.NetworkMode{sandbox.NetworkNone},
		Resources: sandbox.ResourceCaps{CPUs: true, MemoryBytes: true, PIDs: true, DiskBytes: true},
		Image:     true,
		Landlock:  true,
	}}
}

func (f *fakeProvider) Capabilities() sandbox.Capabilities {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.capsCalls++
	return f.caps
}

func (f *fakeProvider) Create(context.Context, sandbox.Spec) (sandbox.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	return nil, errors.New("fakeProvider: Create must not be called")
}

func (f *fakeProvider) Reap(ctx context.Context) ([]string, error) {
	f.mu.Lock()
	f.reapCalls++
	left := time.Duration(-1)
	if deadline, ok := ctx.Deadline(); ok {
		left = time.Until(deadline)
	}
	f.reapDeadlines = append(f.reapDeadlines, left)
	blocks, failing, err := f.reapBlocks, f.failing, f.reapErr
	f.mu.Unlock()

	if blocks {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if failing {
		return nil, errors.New("fakeProvider: engine unreachable")
	}
	return nil, err
}

// failAll makes every later call fail.
func (f *fakeProvider) failAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failing = true
}

// reset zeroes the call counters.
func (f *fakeProvider) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reapCalls, f.createCalls, f.capsCalls = 0, 0, 0
	f.reapDeadlines = nil
}

// total returns the number of calls of every method since the last reset.
func (f *fakeProvider) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reapCalls + f.createCalls + f.capsCalls
}

// recordingFactory is a ProviderFactory that hands out provider (or fails with
// err), counts its calls and records the options it received.
type recordingFactory struct {
	provider sandbox.Provider
	err      error
	calls    int
	options  podman.Options
}

func (r *recordingFactory) build(o podman.Options) (sandbox.Provider, error) {
	r.calls++
	r.options = o
	if r.err != nil {
		return nil, r.err
	}
	return r.provider, nil
}

// withProbeTimeout shortens the probe timeout for the length of the test.
func withProbeTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := probeTimeout
	probeTimeout = d
	t.Cleanup(func() { probeTimeout = prev })
}

// TS-25-11: Engine options default and are passed to the provider constructor
// unchanged.
// Requirement: 25-REQ-2.4
func TestBuildProvider_OptionsUnchanged_TS_25_11(t *testing.T) {
	cfg := validConfig()
	cfg.EngineBinary, cfg.Runtime, cfg.UserNS = "docker", "runsc", "keep-id"
	factory := &recordingFactory{provider: newFakeProvider()}
	if _, err := BuildProvider(requireValid(t, cfg), factory.build); err != nil {
		t.Fatalf("BuildProvider returned error: %v", err)
	}
	if want := (podman.Options{Binary: "docker", Runtime: "runsc", UserNS: "keep-id"}); !reflect.DeepEqual(factory.options, want) {
		t.Errorf("options = %+v; want %+v", factory.options, want)
	}

	factory = &recordingFactory{provider: newFakeProvider()}
	if _, err := BuildProvider(requireValid(t, validConfig()), factory.build); err != nil {
		t.Fatalf("BuildProvider returned error: %v", err)
	}
	if want := (podman.Options{Binary: "podman"}); !reflect.DeepEqual(factory.options, want) {
		t.Errorf("default options = %+v; want %+v", factory.options, want)
	}
}

// TS-25-28: After static validation the provider is built and probed once,
// with a deadline within 10 seconds.
// Requirement: 25-REQ-5.1
func TestService_ProbesOnceWithinTenSeconds_TS_25_28(t *testing.T) {
	fake := newFakeProvider()
	if _, err := New(requireValid(t, validConfig()), fake); err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if fake.reapCalls != 1 {
		t.Fatalf("probe calls = %d; want exactly 1", fake.reapCalls)
	}
	if left := fake.reapDeadlines[0]; left <= 0 || left > 10*time.Second {
		t.Errorf("probe context had %v left; want a deadline within 10s", left)
	}
}

// TS-25-29: Static failures are reported without building the provider or
// probing.
// Requirement: 25-REQ-5.2
func TestStart_StaticFailuresSkipTheProvider_TS_25_29(t *testing.T) {
	fake := newFakeProvider()
	factory := &recordingFactory{provider: fake}
	cfg := configWith(validProfile("dup"), validProfile("dup"))

	svc, err := Start(cfg, factory.build)
	if svc != nil || err == nil {
		t.Fatalf("Start = (%v, %v); want a validation error", svc, err)
	}
	requireFailure(t, err, `"dup"`, "name")
	if factory.calls != 0 || fake.total() != 0 {
		t.Errorf("factory calls = %d, provider calls = %d; want none after a static failure", factory.calls, fake.total())
	}
}

// TS-25-30: A provider construction failure, a probe error and a probe
// timeout refuse start with the sandbox: prefix.
// Requirement: 25-REQ-5.3
func TestStart_ProviderFailures_TS_25_30(t *testing.T) {
	assertRefused := func(t *testing.T, err error, causes ...string) {
		t.Helper()
		if err == nil {
			t.Fatal("Start succeeded; want a refusal")
		}
		if !strings.HasPrefix(err.Error(), "sandbox:") {
			t.Errorf("error %q does not start with sandbox:", err)
		}
		for _, want := range append([]string{"sandbox.provider", "sandbox.engine_binary"}, causes...) {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not contain %q", err, want)
			}
		}
	}

	t.Run("construction fails", func(t *testing.T) {
		cause := errors.New("podman: engine binary \"podman\" not found on PATH")
		factory := &recordingFactory{err: cause}
		_, err := Start(validConfig(), factory.build)
		assertRefused(t, err, cause.Error())
		if !errors.Is(err, cause) {
			t.Errorf("error %v does not wrap the provider's error", err)
		}
	})

	t.Run("probe returns an error", func(t *testing.T) {
		fake := newFakeProvider()
		fake.reapErr = errors.New("engine down")
		factory := &recordingFactory{provider: fake}
		_, err := Start(validConfig(), factory.build)
		assertRefused(t, err, "engine down")
		if !errors.Is(err, fake.reapErr) {
			t.Errorf("error %v does not wrap the probe error", err)
		}
		if fake.reapCalls != 1 {
			t.Errorf("probe calls = %d; want 1", fake.reapCalls)
		}
	})

	t.Run("probe exceeds its timeout", func(t *testing.T) {
		withProbeTimeout(t, 50*time.Millisecond)
		fake := newFakeProvider()
		fake.reapBlocks = true
		factory := &recordingFactory{provider: fake}

		started := time.Now()
		_, err := Start(validConfig(), factory.build)
		assertRefused(t, err)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("error %v does not wrap context.DeadlineExceeded", err)
		}
		if elapsed := time.Since(started); elapsed > 5*time.Second {
			t.Errorf("start took %v; the probe must give up at its timeout", elapsed)
		}
	})

	t.Run("the production timeout is ten seconds", func(t *testing.T) {
		if probeTimeout != 10*time.Second {
			t.Errorf("probe timeout = %v; want 10s", probeTimeout)
		}
	})
}

// TS-25-31: A network_modes entry the provider does not honour refuses start.
// Requirement: 25-REQ-5.4
func TestService_UnsupportedNetworkMode_TS_25_31(t *testing.T) {
	fake := newFakeProvider()
	cfg := validConfig()
	cfg.NetworkModes = []string{"none", "host"}
	_, err := New(requireValid(t, cfg), fake)
	requireFailure(t, err, "sandbox.network_modes[1]", `"host"`)
	if strings.Contains(err.Error(), "sandbox.network_modes[0]") {
		t.Errorf("a supported mode was reported: %v", err)
	}
}

// checkedSpec is one call of the injected spec check.
type checkedSpec struct {
	caps sandbox.Capabilities
	spec sandbox.Spec
}

// TS-25-32: Each profile is converted to a Spec with the image, the fixed env
// only, the network, the resources, the TTL and landlock, and checked once.
// Requirement: 25-REQ-5.5
func TestService_ProfileSpecsAreChecked_TS_25_32(t *testing.T) {
	cfg := configWith(
		profileWith("first", func(p *ProfileConfig) {
			p.Image = "registry.example/first:1"
			p.CPUs, p.MemoryMB, p.PIDs, p.DiskMB = 1.5, 512, 100, 2048
			p.TTL = "90m"
			p.RequireLandlock = true
			p.Secrets = []string{"GIT_PAT"}
			p.OptionalSecrets = []string{"X"}
			p.Env = map[string]string{"LANG": "C"}
		}),
		profileWith("second", func(p *ProfileConfig) {
			p.Secrets = []string{"GIT_PAT"}
			p.OptionalSecrets = []string{"X"}
			p.Env = map[string]string{"LANG": "C"}
		}),
	)
	fake := newFakeProvider()
	var checks []checkedSpec
	check := func(caps sandbox.Capabilities, spec sandbox.Spec) (sandbox.Spec, error) {
		checks = append(checks, checkedSpec{caps, spec})
		return spec, nil
	}

	if _, err := newService(requireValid(t, cfg), fake, check); err != nil {
		t.Fatalf("newService returned error: %v", err)
	}
	if len(checks) != 2 {
		t.Fatalf("CheckSpec ran %d times; want once per profile (2)", len(checks))
	}
	for i, c := range checks {
		if c.caps.Provider != "fake" {
			t.Errorf("check %d ran against capabilities of %q; want the provider's", i, c.caps.Provider)
		}
		if !maps.Equal(c.spec.Env, map[string]string{"LANG": "C"}) {
			t.Errorf("check %d: Spec.Env = %v; want only the fixed env {LANG: C}", i, c.spec.Env)
		}
		if c.spec.Network.Mode != sandbox.NetworkNone || len(c.spec.Network.Allow) != 0 {
			t.Errorf("check %d: Spec.Network = %+v; want mode none", i, c.spec.Network)
		}
		if len(c.spec.Labels) != 0 || c.spec.Workdir != "" || len(c.spec.Provider) != 0 {
			t.Errorf("check %d: Spec carries labels, a workdir or provider options: %+v", i, c.spec)
		}
	}

	first := checks[0].spec
	if first.Image != "registry.example/first:1" || first.TTL != 90*time.Minute || !first.RequireLandlock {
		t.Errorf("first spec = %+v; want its image, a 90m TTL and RequireLandlock", first)
	}
	if want := (sandbox.Resources{CPUs: 1.5, MemoryBytes: 512 << 20, PIDs: 100, DiskBytes: 2048 << 20}); first.Resources != want {
		t.Errorf("first spec resources = %+v; want %+v (megabytes converted to bytes)", first.Resources, want)
	}
	if second := checks[1].spec; second.RequireLandlock || second.Resources != (sandbox.Resources{}) {
		t.Errorf("second spec = %+v; want landlock off and the provider's default resources", second)
	}
}

// TS-25-33: A profile refused by CheckSpec fails start naming the profile and
// the error text.
// Requirement: 25-REQ-5.6
func TestService_RefusedProfile_TS_25_33(t *testing.T) {
	fake := newFakeProvider()
	fake.caps.Landlock = false
	cfg := configWith(
		profileWith("big", func(p *ProfileConfig) { p.RequireLandlock = true }),
		validProfile("small"),
		profileWith("huge", func(p *ProfileConfig) { p.RequireLandlock = true }),
	)
	v := requireValid(t, cfg)

	_, err := New(v, fake)
	if err == nil {
		t.Fatal("New accepted profiles the provider cannot honour")
	}
	_, refusal := sandbox.CheckSpec(fake.caps, sandbox.Spec{Image: v.Profiles[0].Image, TTL: v.Profiles[0].TTL, RequireLandlock: true})
	if refusal == nil {
		t.Fatal("test setup: CheckSpec accepted RequireLandlock without Landlock")
	}
	requireFailure(t, err, `"big"`, refusal.Error())
	requireFailure(t, err, `"huge"`, refusal.Error())
	if strings.Contains(err.Error(), `"small"`) {
		t.Errorf("an acceptable profile was reported: %v", err)
	}
	if !errors.Is(err, sandbox.ErrUnsupported) {
		t.Errorf("error %v does not wrap sandbox.ErrUnsupported", err)
	}
}

// TS-25-34: AgentKit's CPU field is a float64, so a fractional cpus value is
// accepted and reaches the provider; the whole-number refusal of 25-REQ-5.7
// does not apply.
// Requirement: 25-REQ-5.7
func TestService_FractionalCPUs_TS_25_34(t *testing.T) {
	var _ float64 = sandbox.Resources{}.CPUs // fails to compile if AgentKit's CPU field becomes a whole number

	cfg := configWith(
		profileWith("half", func(p *ProfileConfig) { p.CPUs = 0.5 }),
		profileWith("one-and-a-half", func(p *ProfileConfig) { p.CPUs = 1.5 }),
		profileWith("two", func(p *ProfileConfig) { p.CPUs = 2 }),
	)
	var seen []float64
	check := func(_ sandbox.Capabilities, spec sandbox.Spec) (sandbox.Spec, error) {
		seen = append(seen, spec.Resources.CPUs)
		return spec, nil
	}
	if _, err := newService(requireValid(t, cfg), newFakeProvider(), check); err != nil {
		t.Fatalf("newService returned error: %v", err)
	}
	if !slices.Equal(seen, []float64{0.5, 1.5, 2}) {
		t.Errorf("CPUs seen by CheckSpec = %v; want [0.5 1.5 2]", seen)
	}
}

// A memory or disk size whose byte count does not fit an int64 is refused,
// naming the profile and the field, instead of wrapping around.
// Requirement: 25-REQ-5.6
func TestService_ResourceOverflow(t *testing.T) {
	cfg := configWith(
		profileWith("memory", func(p *ProfileConfig) { p.MemoryMB = 1 << 53 }),
		profileWith("disk", func(p *ProfileConfig) { p.DiskMB = 1 << 53 }),
	)
	_, err := New(requireValid(t, cfg), newFakeProvider())
	requireFailure(t, err, `"memory"`, "memory_mb")
	requireFailure(t, err, `"disk"`, "disk_mb")
}

// A network the provider does not honour is reported once, as a
// sandbox.network_modes failure, not again for every profile that uses it.
// Requirement: 25-REQ-5.4
func TestService_UnsupportedNetworkReportedOnce(t *testing.T) {
	cfg := configWith(
		profileWith("a", func(p *ProfileConfig) { p.Network = strPtr("host") }),
		profileWith("b", func(p *ProfileConfig) { p.Network = strPtr("host") }),
	)
	cfg.NetworkModes = []string{"none", "host"}
	_, err := New(requireValid(t, cfg), newFakeProvider())
	if err == nil {
		t.Fatal("New accepted a network the provider does not honour")
	}
	if n := len(failures(err)); n != 1 {
		t.Errorf("%d failures; want only the sandbox.network_modes one:\n%v", n, err)
	}
	requireFailure(t, err, "sandbox.network_modes[1]", `"host"`)
}

// TS-25-35: The service keeps the provider and the profiles and never calls
// Create.
// Requirement: 25-REQ-5.8
func TestService_KeepsProviderAndProfiles_TS_25_35(t *testing.T) {
	fake := newFakeProvider()
	factory := &recordingFactory{provider: fake}
	cfg := configWith(validProfile("alpha"), validProfile("beta"))

	svc, err := Start(cfg, factory.build)
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if svc.Provider() != sandbox.Provider(fake) {
		t.Error("Provider() is not the provider the service was built with")
	}
	names := make([]string, 0, 2)
	for _, p := range svc.Profiles() {
		names = append(names, p.Name)
	}
	if !slices.Equal(names, []string{"alpha", "beta"}) {
		t.Errorf("Profiles() = %v; want [alpha beta]", names)
	}
	if factory.calls != 1 {
		t.Errorf("factory calls = %d; want 1", factory.calls)
	}
	if fake.createCalls != 0 || fake.reapCalls != 1 {
		t.Errorf("Create calls = %d, probe calls = %d; want 0 and 1", fake.createCalls, fake.reapCalls)
	}
}
