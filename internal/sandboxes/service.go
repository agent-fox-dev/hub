package sandboxes

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// probeTimeout bounds the one call that shows the provider is reachable.
var probeTimeout = 10 * time.Second

// bytesPerMB converts the megabytes of memory_mb and disk_mb to bytes. The
// engines read them as mebibytes.
const bytesPerMB = 1 << 20

// specChecker has the shape of sandbox.CheckSpec.
type specChecker func(sandbox.Capabilities, sandbox.Spec) (sandbox.Spec, error)

// Service is the validated sandbox definition and the provider it was checked
// against. It is built once at start and read-only afterwards.
type Service struct {
	provider     sandbox.Provider
	profiles     []ValidatedProfile
	maxSandboxes int
	networkModes []string
	maxTTL       time.Duration
	leaseTimeout time.Duration
	gateway      gatewaySettings
}

// Start validates cfg, builds the provider through factory and checks it. A
// static failure is returned before the factory is called, so the engine is
// never contacted for a configuration that cannot work.
func Start(cfg *Config, factory ProviderFactory) (*Service, error) {
	v, err := Validate(cfg)
	if err != nil {
		return nil, err
	}
	p, err := BuildProvider(v, factory)
	if err != nil {
		return nil, err
	}
	return New(v, p)
}

// New probes p once, checks every network mode of v against its capabilities
// and turns every profile into a sandbox.Spec that sandbox.CheckSpec must
// accept. It never creates or destroys a sandbox. Every failure is collected
// and returned as one joined error.
func New(v *Validated, p sandbox.Provider) (*Service, error) {
	return newService(v, p, sandbox.CheckSpec)
}

// newService is New with the spec check injected.
func newService(v *Validated, p sandbox.Provider, check specChecker) (*Service, error) {
	if err := probe(v, p); err != nil {
		return nil, err
	}

	caps := p.Capabilities()
	errs := checkNetworkModes(v, caps)
	for _, profile := range v.Profiles {
		if err := checkProfile(profile, caps, check); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return &Service{
		provider:     p,
		profiles:     slices.Clone(v.Profiles),
		maxSandboxes: v.MaxSandboxes,
		networkModes: slices.Clone(v.NetworkModes),
		maxTTL:       v.MaxTTL,
		leaseTimeout: v.LeaseTimeout,
		gateway:      gatewaySettings{listen: v.Listen, tlsCert: v.TLSCert, tlsKey: v.TLSKey, externalURL: v.ExternalURL},
	}, nil
}

// probe shows that the provider reaches its engine.
//
// sandbox.Provider has no List: Reap is the one provider method that contacts
// the engine without creating a sandbox. It removes the leftovers, labelled by
// AgentKit, whose own expiry has already passed, and it returns an error when
// the engine cannot be listed and also when one of those removals fails, so
// the failure is worded as a failed check, not as an unreachable engine.
func probe(v *Validated, p sandbox.Provider) error {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	if _, err := p.Reap(ctx); err != nil {
		return fmt.Errorf("sandbox: sandbox.provider %q failed its start-up check through sandbox.engine_binary %q: %w", v.Provider, v.EngineBinary, err)
	}
	return nil
}

// networkModeNames returns the names of the network modes caps honours, which
// are the names an operator writes in network_modes and network.
func networkModeNames(caps sandbox.Capabilities) []string {
	names := make([]string, len(caps.Network))
	for i, mode := range caps.Network {
		names[i] = mode.String()
	}
	return names
}

// checkNetworkModes fails every network_modes entry the provider does not
// honour.
func checkNetworkModes(v *Validated, caps sandbox.Capabilities) []error {
	honoured := networkModeNames(caps)
	var errs []error
	for i, mode := range v.NetworkModes {
		if !slices.Contains(honoured, mode) {
			errs = append(errs, fmt.Errorf("sandbox.network_modes[%d] %q is not a network mode of provider %q (it honours %q)", i, mode, caps.Provider, honoured))
		}
	}
	return errs
}

// checkProfile turns profile into a Spec and has check judge it against caps.
func checkProfile(profile ValidatedProfile, caps sandbox.Capabilities, check specChecker) error {
	mode, ok := networkMode(caps, profile.Network)
	if !ok {
		// The profile's network is one of network_modes, and checkNetworkModes
		// has already failed that entry.
		return nil
	}
	spec, err := profileSpec(profile, mode)
	if err != nil {
		return fmt.Errorf("profile %q: %w", profile.Name, err)
	}
	if _, err := check(caps, spec); err != nil {
		return fmt.Errorf("profile %q: %w", profile.Name, err)
	}
	return nil
}

// networkMode returns the mode caps honours under name.
func networkMode(caps sandbox.Capabilities, name string) (sandbox.NetworkMode, bool) {
	for _, mode := range caps.Network {
		if mode.String() == name {
			return mode, true
		}
	}
	return sandbox.NetworkNone, false
}

// profileSpec is the sandbox.Spec a sandbox of profile gets: the image, the
// profile's fixed env and nothing else in it, the network, the resources, the
// TTL and landlock. Secrets are not part of it; they are resolved when a
// sandbox is created.
func profileSpec(profile ValidatedProfile, mode sandbox.NetworkMode) (sandbox.Spec, error) {
	memory, memoryOK := megabytesToBytes(profile.Resources.MemoryMB)
	disk, diskOK := megabytesToBytes(profile.Resources.DiskMB)
	var errs []error
	if !memoryOK {
		errs = append(errs, fmt.Errorf("memory_mb %d is too large", profile.Resources.MemoryMB))
	}
	if !diskOK {
		errs = append(errs, fmt.Errorf("disk_mb %d is too large", profile.Resources.DiskMB))
	}
	if len(errs) > 0 {
		return sandbox.Spec{}, errors.Join(errs...)
	}

	return sandbox.Spec{
		Image: profile.Image,
		Env:   maps.Clone(profile.Env),
		Resources: sandbox.Resources{
			// AgentKit bounds CPUs as a float64, so a fractional cpus needs no
			// check of its own.
			CPUs:        profile.Resources.CPUs,
			MemoryBytes: memory,
			PIDs:        profile.Resources.PIDs,
			DiskBytes:   disk,
		},
		Network:         sandbox.Network{Mode: mode},
		TTL:             profile.TTL,
		RequireLandlock: profile.RequireLandlock,
	}, nil
}

// megabytesToBytes converts megabytes to bytes, and reports false when the
// result does not fit an int64.
func megabytesToBytes(mb int64) (int64, bool) {
	if mb > math.MaxInt64/bytesPerMB {
		return 0, false
	}
	return mb * bytesPerMB, true
}

// MaxSandboxes returns the effective limit on live leases.
func (s *Service) MaxSandboxes() int { return s.maxSandboxes }

// LeaseTimeout returns how long a lease may wait for its Create.
func (s *Service) LeaseTimeout() time.Duration { return s.leaseTimeout }

// NetworkModes returns the names of the network modes the deployment allows. The
// slice is a copy.
func (s *Service) NetworkModes() []string { return slices.Clone(s.networkModes) }

// Provider returns the provider the service was built with.
func (s *Service) Provider() sandbox.Provider { return s.provider }

// Profiles returns the validated profiles in file order. The slice is a copy;
// the profiles share their lists and maps with the service and must not be
// changed.
func (s *Service) Profiles() []ValidatedProfile { return slices.Clone(s.profiles) }
