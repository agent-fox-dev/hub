// Package auth — provider registry.
package auth

import (
	"github.com/agent-fox/af-hub/internal/config"
)

// Registry manages the mapping of provider names to Provider implementations.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry creates a Registry populated from the given config.
// Only providers listed in the config are registered.
func NewRegistry(cfg *config.AuthConfig) *Registry {
	panic("not implemented")
}

// Register adds or replaces a provider in the registry.
func (r *Registry) Register(name string, p Provider) {
	panic("not implemented")
}

// GetProvider returns the Provider registered under the given name,
// or an error if no such provider exists.
func (r *Registry) GetProvider(name string) (Provider, error) {
	panic("not implemented")
}

// ListProviders returns a list of registered provider names.
func (r *Registry) ListProviders() []string {
	panic("not implemented")
}
