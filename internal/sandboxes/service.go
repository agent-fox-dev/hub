package sandboxes

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/url"
	"slices"
	"sync/atomic"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
)

// probeTimeout bounds the one call that shows the provider is reachable.
var probeTimeout = 10 * time.Second

// bytesPerMB converts the megabytes of memory_mb and disk_mb to bytes. The
// engines read them as mebibytes.
const bytesPerMB = 1 << 20

// specChecker has the shape of sandbox.CheckSpec.
type specChecker func(sandbox.Capabilities, sandbox.Spec) (sandbox.Spec, error)

// Service is the validated sandbox definition and the provider it was checked
// against. It is built once at start and read-only afterwards, except for the
// shutdown mode (see EnterShutdown).
type Service struct {
	provider sandbox.Provider
	// remote is the client of the remote gateway, nil for any other provider.
	remote RemoteClient
	// remoteScrub are the values that stay out of every text derived from the
	// remote's errors: hub's token for it, the gateway_url string and its host.
	remoteScrub []string
	// caps is the snapshot of the remote gateway's capabilities, read once at
	// start; nil for a provider that is not remote, which is asked every time.
	caps         *sandbox.Capabilities
	profiles     []ValidatedProfile
	maxSandboxes int
	networkModes []string
	maxTTL       time.Duration
	leaseTimeout time.Duration
	reapInterval time.Duration
	gateway      gatewaySettings
	// shutdown is set when hub starts to shut down.
	shutdown atomic.Bool
}

// Start validates cfg, builds the provider and checks it. A static failure is
// returned before any factory is called, so neither an engine nor a remote
// gateway is contacted for a configuration that cannot work.
//
// Provider "podman" is built through factory. Provider "gateway" is built through
// the factory of WithGatewayFactory, gateway.Dial by default, and never through
// factory: no engine is run for it.
func Start(cfg *Config, factory ProviderFactory, opts ...StartOption) (*Service, error) {
	v, err := Validate(cfg)
	if err != nil {
		return nil, err
	}
	if v.Provider == providerGateway {
		return startRemote(v, newStartOptions(opts).gateway)
	}
	p, err := BuildProvider(v, factory)
	if err != nil {
		return nil, err
	}
	return New(v, p)
}

// startRemote builds the client of the remote gateway and checks it. The factory
// and the one List that proves the remote answers and accepts hub's token share
// one probeTimeout, and nothing is created on the remote. The client is closed
// when start is refused, since it holds a connection.
func startRemote(v *Validated, factory GatewayFactory) (*Service, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	client, err := factory(ctx, v.GatewayURL, gateway.Options{Token: v.GatewayToken.reveal()})
	if err == nil && client == nil {
		err = errors.New("the gateway factory returned no client")
	}
	if err != nil {
		return nil, remoteStartFailure(ctx, v, err)
	}
	if _, err := client.List(ctx); err != nil {
		closeRemote(client)
		return nil, remoteStartFailure(ctx, v, err)
	}
	// The remote's capabilities are read here, once, and kept for the life of the
	// process: a remote that is down must not make hub's gateway wait or report no
	// modes, and a change on the remote takes effect when hub restarts.
	caps := client.Capabilities()
	svc, err := assemble(v, client, caps, sandbox.CheckSpec)
	if err != nil {
		closeRemote(client)
		return nil, err
	}
	svc.caps, svc.remote, svc.remoteScrub = &caps, client, remoteScrubValues(v)
	return svc, nil
}

// remoteStartFailure is the error of a remote that cannot be used: it names the
// three keys that decide which remote hub talks to, and holds the client's text
// with the token replaced and the user information of URLs removed. The text is
// built from the scrubbed string and wraps nothing, so the token cannot be got
// back from the chain.
func remoteStartFailure(ctx context.Context, v *Validated, err error) error {
	text := scrub(err.Error(), []string{v.GatewayToken.reveal()})
	if ctx.Err() != nil {
		text = fmt.Sprintf("no answer within %s: %s", probeTimeout, text)
	}
	return fmt.Errorf("sandbox: sandbox.provider %q cannot use the remote gateway at sandbox.gateway_url %q with sandbox.gateway_token: %s", v.Provider, v.GatewayURL, text)
}

// closeRemote closes a client that is being given up. Its error is dropped: start
// is being refused already, and says why.
func closeRemote(client RemoteClient) {
	if closer, ok := client.(io.Closer); ok {
		_ = closer.Close()
	}
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
	return assemble(v, p, p.Capabilities(), check)
}

// assemble checks the network modes and the profiles against the capabilities of
// a provider that has passed its probe, and builds the service.
func assemble(v *Validated, p sandbox.Provider, caps sandbox.Capabilities, check specChecker) (*Service, error) {
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
		reapInterval: v.ReapInterval,
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

// EnterShutdown puts hub in shutdown mode, for good: from then on the gateway
// refuses every Create with Unavailable, no sweep starts, and every lease that
// ends does so with the end reason shutdown. It is the one thing about a Service
// that changes after start, and it changes once.
func (s *Service) EnterShutdown() {
	if s != nil {
		s.shutdown.Store(true)
	}
}

// InShutdown reports whether hub is in shutdown mode. A nil Service, the feature
// turned off, never is.
func (s *Service) InShutdown() bool { return s != nil && s.shutdown.Load() }

// endReason is the reason a lease ends with: the given one, or shutdown while hub
// is in shutdown mode.
func (s *Service) endReason(reason string) string {
	if s.InShutdown() {
		return leaseEndReasonShutdown
	}
	return reason
}

// ReapInterval returns how often the reaper sweeps.
func (s *Service) ReapInterval() time.Duration { return s.reapInterval }

// LeaseTimeout returns how long a lease may wait for its Create.
func (s *Service) LeaseTimeout() time.Duration { return s.leaseTimeout }

// NetworkModes returns the names of the network modes the deployment allows. The
// slice is a copy.
func (s *Service) NetworkModes() []string { return slices.Clone(s.networkModes) }

// Capabilities returns what the provider honours. For the remote gateway that is
// the snapshot taken at start, a copy the caller may change, and the remote is
// not asked; for any other provider it is the provider's own answer.
func (s *Service) Capabilities() sandbox.Capabilities {
	if s.caps == nil {
		return s.provider.Capabilities()
	}
	caps := *s.caps
	caps.Network = slices.Clone(caps.Network)
	return caps
}

// scrubValues are the values besides the secrets and the environment of a lease
// that must stay out of any text derived from the provider's errors, which is any
// text stored on a lease, logged, emitted, or returned to a gateway or HTTP
// caller. For the remote gateway they are hub's token, the configured gateway_url
// string and the URL's host, as url.Host gives it; for any other provider there
// are none. The slice is a copy.
func (s *Service) scrubValues() []string {
	if s == nil {
		return nil
	}
	return slices.Clone(s.remoteScrub)
}

// remoteScrubValues are the scrub values of the remote gateway v describes.
func remoteScrubValues(v *Validated) []string {
	values := []string{v.GatewayToken.reveal(), v.GatewayURL}
	if u, err := url.Parse(v.GatewayURL); err == nil && u.Host != "" {
		values = append(values, u.Host)
	}
	return values
}

// Provider returns the provider the service was built with.
func (s *Service) Provider() sandbox.Provider { return s.provider }

// Profiles returns the validated profiles in file order. The slice is a copy;
// the profiles share their lists and maps with the service and must not be
// changed.
func (s *Service) Profiles() []ValidatedProfile { return slices.Clone(s.profiles) }
