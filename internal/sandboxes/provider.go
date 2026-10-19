package sandboxes

import (
	"fmt"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
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
