// Package auth — provider registry.
package auth

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/agent-fox/af-hub/internal/config"
)

// Default HTTP timeout for identity provider calls.
const defaultProviderTimeout = 10 * time.Second

// Registry manages the mapping of provider names to Provider implementations.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry creates a Registry populated from the given config.
// Only providers listed in the config are registered. GitHub is the
// only built-in provider; others must be registered manually via Register.
//
// An HTTP client with the configured timeout (or 10s default) is shared
// across all providers created from this config.
func NewRegistry(cfg *config.AuthConfig) *Registry {
	r := &Registry{
		providers: make(map[string]Provider),
	}

	timeout := defaultProviderTimeout
	if cfg.Timeout > 0 {
		timeout = time.Duration(cfg.Timeout) * time.Second
	}
	httpClient := &http.Client{Timeout: timeout}

	for _, oauthCfg := range cfg.OAuth {
		switch oauthCfg.Provider {
		case "github":
			r.providers["github"] = NewGitHubProvider(oauthCfg, httpClient)
		default:
			// Unknown provider types in config are skipped.
			// Custom providers can be added via Register().
		}
	}

	return r
}

// Register adds or replaces a provider in the registry.
func (r *Registry) Register(name string, p Provider) {
	r.providers[name] = p
}

// GetProvider returns the Provider registered under the given name,
// or an error wrapping ErrUnsupportedProvider if no such provider exists.
func (r *Registry) GetProvider(name string) (Provider, error) {
	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedProvider, name)
	}
	return p, nil
}

// ListProviders returns a sorted list of registered provider names.
func (r *Registry) ListProviders() []string {
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
