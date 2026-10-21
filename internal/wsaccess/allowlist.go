package wsaccess

import (
	"sync"

	"github.com/labstack/echo/v4"
)

// boundTokenAllowlist holds the routes, keyed by method and path template,
// that a bound token may call although they are not workspace-scoped.
var boundTokenAllowlist = struct {
	sync.RWMutex
	routes map[string]bool
}{routes: make(map[string]bool)}

// AllowBoundTokens marks routes as callable by a bound token although they are
// not workspace-scoped. Pass the *echo.Route values the registration returned;
// the handler of a marked route must itself confine its result to the
// token's workspace. A route that is not marked is refused to bound tokens.
func AllowBoundTokens(routes ...*echo.Route) {
	boundTokenAllowlist.Lock()
	defer boundTokenAllowlist.Unlock()
	for _, r := range routes {
		boundTokenAllowlist.routes[routeKey(r.Method, r.Path)] = true
	}
}

// IsAllowed reports whether the route with the given method and path
// template (echo's c.Path()) was marked with AllowBoundTokens.
func IsAllowed(method, path string) bool {
	boundTokenAllowlist.RLock()
	defer boundTokenAllowlist.RUnlock()
	return boundTokenAllowlist.routes[routeKey(method, path)]
}

// routeKey is the key of a route in the allowlist and in the apikit route set.
func routeKey(method, path string) string {
	return method + " " + path
}
