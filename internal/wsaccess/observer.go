package wsaccess

import "sync/atomic"

// BoundRefusal describes one request refused because of a token binding:
// which bound token, which workspace it is bound to, what it asked
// for and how it was answered. It never carries the token itself.
type BoundRefusal struct {
	// TokenID is the PAT's token id.
	TokenID string
	// UserID is the token's user, when the refusal point has authenticated
	// the token; empty otherwise (the apikit route guard).
	UserID string
	// BoundSlug is the workspace the token is bound to.
	BoundSlug string
	// RequestedSlug is the workspace the request named, empty when it named
	// none.
	RequestedSlug string
	// Method is the request method.
	Method string
	// Route is the matched route template (the git route for git).
	Route string
	// Status is the status of the refusal (403 or 404).
	Status int
	// Surface is SurfaceAPI or SurfaceGit.
	Surface string
}

// Surfaces of a refusal.
const (
	// SurfaceAPI is a refusal by the REST API.
	SurfaceAPI = "api"
	// SurfaceGit is a refusal by the git server.
	SurfaceGit = "git"
)

// refusalObserver holds the registered observer, nil when none is.
var refusalObserver atomic.Pointer[func(BoundRefusal)]

// SetRefusalObserver registers the process-wide observer that receives one
// BoundRefusal for every request refused because of a binding. A nil
// observer unregisters it. It is safe to call concurrently with
// ReportBoundRefusal.
func SetRefusalObserver(observer func(BoundRefusal)) {
	if observer == nil {
		refusalObserver.Store(nil)
		return
	}
	refusalObserver.Store(&observer)
}

// ReportBoundRefusal hands r to the registered refusal observer, and does
// nothing when none is registered. Every refusal point calls it once per
// refused request; filtering a list is not a refusal.
func ReportBoundRefusal(r BoundRefusal) {
	if observer := refusalObserver.Load(); observer != nil {
		(*observer)(r)
	}
}
