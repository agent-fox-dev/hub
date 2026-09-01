package audit

import "context"

// Emitter defines the interface for emitting hub-internal audit events.
type Emitter interface {
	Emit(ctx context.Context, event HubEvent) error
}

// NewEmitter creates the default Emitter implementation backed by a Store.
func NewEmitter(store Store) Emitter {
	panic("not implemented")
}

// NewEmitterWithBroadcast creates an Emitter that writes to the Store and also
// publishes events to the SSE broadcast channel (18-REQ-9.2). Every successful
// Emit call inserts into hub_audit_events and broadcasts to the SSEManager.
func NewEmitterWithBroadcast(store Store, mgr *SSEManager) Emitter {
	panic("not implemented: NewEmitterWithBroadcast")
}
