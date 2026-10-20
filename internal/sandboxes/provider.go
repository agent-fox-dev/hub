package sandboxes

import (
	"context"
	"fmt"

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
// breaks its rules before it connects.
func DialGateway(ctx context.Context, url string, opts gateway.Options) (RemoteClient, error) {
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
