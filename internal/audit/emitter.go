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
