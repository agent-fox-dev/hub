package wsaccess

import (
	"context"
	"sync"
)

// requestScopeKey is the context key under which a request scope is stored.
// It is unexported so only WithRequestScope can install one.
type requestScopeKey struct{}

// requestScope memoises string values for the lifetime of one HTTP request.
type requestScope struct {
	mu     sync.Mutex
	values map[string]string
}

// WithRequestScope returns a context that carries a per-request memo for
// RequestScoped. A request handler installs it once, before any hook runs, so
// that values read through RequestScoped are read exactly once for the whole
// request.
//
// It is idempotent: if ctx already carries a scope, ctx is returned unchanged
// so a nested caller cannot reset the memo of the request that contains it.
func WithRequestScope(ctx context.Context) context.Context {
	if _, ok := ctx.Value(requestScopeKey{}).(*requestScope); ok {
		return ctx
	}
	return context.WithValue(ctx, requestScopeKey{}, &requestScope{values: make(map[string]string)})
}

// RequestScoped returns the value memoised under key for the request scope
// carried by ctx. On first use within a scope it calls load and remembers the
// result; later calls with the same key return that result without calling
// load again.
//
// If ctx carries no request scope, load is called on every invocation, so
// callers outside a request (direct callers, unit tests) see the unmemoised
// behaviour.
//
// load is called with the scope's lock held, so it must not call RequestScoped
// on the same context.
func RequestScoped(ctx context.Context, key string, load func() string) string {
	scope, ok := ctx.Value(requestScopeKey{}).(*requestScope)
	if !ok {
		return load()
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	if v, found := scope.values[key]; found {
		return v
	}
	v := load()
	scope.values[key] = v
	return v
}
