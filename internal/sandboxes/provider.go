package sandboxes

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/podman"
)

// ProviderFactory builds a sandbox provider from the Podman options. It has
// the shape of podman.New; tests inject their own.
type ProviderFactory func(podman.Options) (sandbox.Provider, error)

// PodmanFactory builds the real Podman (or Docker) provider. It is the only
// factory production code uses: the unisolated local provider is never
// selectable from configuration.
func PodmanFactory(o podman.Options) (sandbox.Provider, error) {
	return podman.New(o)
}

// BuildProvider builds the provider for v through factory. The engine binary,
// the runtime and the user namespace go to the provider unchanged, so an
// invalid value is the provider's to refuse.
func BuildProvider(v *Validated, factory ProviderFactory) (sandbox.Provider, error) {
	p, err := factory(podman.Options{
		Binary:  v.EngineBinary,
		Runtime: v.Runtime,
		UserNS:  v.UserNS,
	})
	if err != nil {
		return nil, fmt.Errorf("sandbox: cannot build sandbox.provider %q from sandbox.engine_binary %q: %w", v.Provider, v.EngineBinary, err)
	}
	return p, nil
}

// RemoteClient is the client of a remote agentkit-gateway: a sandbox.Provider
// that also lists the sandboxes its token holds, which is what the start-up probe
// calls, and destroys one by id, which answers OK for a sandbox the remote does
// not have and so can be repeated. *gateway.Provider is one.
type RemoteClient interface {
	sandbox.Provider
	List(ctx context.Context) ([]gateway.Info, error)
	Destroy(ctx context.Context, id string) error
}

// GatewayFactory builds the client of the remote gateway at url under opts. It
// has the shape of gateway.Dial; tests inject their own.
type GatewayFactory func(ctx context.Context, url string, opts gateway.Options) (RemoteClient, error)

// DialGateway is the factory production uses: gateway.Dial, which connects to the
// gateway under ctx, reads its capabilities and refuses a URL or a token that
// breaks its rules before it connects. Unless opts brings a Dialer, the client dials
// with dialWithoutAddress.
func DialGateway(ctx context.Context, url string, opts gateway.Options) (RemoteClient, error) {
	if opts.Dialer == nil {
		opts.Dialer = dialWithoutAddress
	}
	client, err := gateway.Dial(ctx, url, opts)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// StartOption changes how Start and Init build the service.
type StartOption func(*startOptions)

type startOptions struct{ gateway GatewayFactory }

// newStartOptions applies opts to the defaults.
func newStartOptions(opts []StartOption) startOptions {
	o := startOptions{gateway: DialGateway}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// WithGatewayFactory sets the factory that builds the client of the remote
// gateway, for provider "gateway". The default is DialGateway.
func WithGatewayFactory(f GatewayFactory) StartOption {
	return func(o *startOptions) { o.gateway = f }
}

// dialWithoutAddress dials the remote gateway as the client does by default, over
// TCP, and returns a failure that says why and not where. gRPC puts the text of a
// failed dial into the status of every call that fails because of it, and gatewayd
// relays the status of a call to the sandbox's agent to the lease holder as it is,
// which hub cannot scrub: "dial tcp 10.0.0.5:8443: connect: connection refused"
// would hand the remote's address to the holder.
func dialWithoutAddress(ctx context.Context, addr string) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, errors.New("cannot connect to the remote gateway: " + dialReason(err))
	}
	return conn, nil
}

// dialReason is the part of a failed dial that does not name an address: "connection
// refused", "no such host", "i/o timeout".
func dialReason(err error) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.Err
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Err != nil {
		return opErr.Err.Error()
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	return "failed"
}
