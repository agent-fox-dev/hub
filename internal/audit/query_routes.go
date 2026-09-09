package audit

import (
	"database/sql"

	"github.com/labstack/echo/v4"
)

// SSEBroadcaster is the interface for the SSE connection manager used by
// audit query route registration. The concrete *SSEManager type (sse.go)
// satisfies this interface.
type SSEBroadcaster interface{}

// RegisterAuditQueryRoutes mounts the unified audit query, transcript, and
// SSE streaming endpoints on the given echo group:
//
//   - GET /audit           — unified query handler with audit:read permission
//   - GET /workspaces/:slug/runs/:run_id/transcript — transcript handler
//   - GET /events          — SSE streaming handler
//
// The store and sseBroadcaster parameters must be non-nil; this function panics
// at startup with a descriptive message if either is nil (18-REQ-10.E1).
// sqliteDB is used to resolve workspace ownership so that non-admin callers
// only see audit data for workspaces they own; when nil the ownership check
// is skipped (unit tests only).
func RegisterAuditQueryRoutes(api *echo.Group, store Store, sseBroadcaster SSEBroadcaster, sqliteDB *sql.DB) {
	if store == nil {
		panic("RegisterAuditQueryRoutes: store must not be nil")
	}
	if sseBroadcaster == nil {
		panic("RegisterAuditQueryRoutes: sseBroadcaster must not be nil")
	}

	// Resolve the SSEManager from the interface if possible; pass nil to the
	// handler if the broadcaster is not a concrete *SSEManager (e.g. mock).
	var mgr *SSEManager
	if m, ok := sseBroadcaster.(*SSEManager); ok {
		mgr = m
	}

	// GET /audit — unified audit event query (18-REQ-6, 18-REQ-10.1).
	api.GET("/audit", handleAuditQuery(store, sqliteDB))

	// GET /workspaces/:slug/runs/:run_id/transcript — conversation transcript
	// reconstruction (18-REQ-7, 18-REQ-10.2).
	api.GET("/workspaces/:slug/runs/:run_id/transcript", handleTranscript(store, sqliteDB))

	// GET /events — SSE streaming endpoint (18-REQ-8, 18-REQ-10.3).
	api.GET("/events", handleSSEStream(store, mgr, sqliteDB))
}
