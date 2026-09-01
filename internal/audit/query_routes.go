package audit

import "github.com/labstack/echo/v4"

// SSEManager is the interface for the SSE connection manager used by audit
// query route registration. This is a placeholder for the concrete type
// defined in internal/audit/sse.go (spec 18, group 5).
type SSEManager interface{}

// RegisterAuditQueryRoutes mounts the unified audit query, transcript, and
// SSE streaming endpoints on the given echo group:
//
//   - GET /audit           — unified query handler with audit:read permission
//   - GET /workspaces/:slug/runs/:run_id/transcript — transcript handler
//   - GET /events          — SSE streaming handler
//
// The store and sseManager parameters must be non-nil; this function panics
// at startup with a descriptive message if either is nil (18-REQ-10.E1).
func RegisterAuditQueryRoutes(api *echo.Group, store Store, sseManager SSEManager) {
	// TODO(spec-18): Register audit query, transcript, and SSE routes.
}
