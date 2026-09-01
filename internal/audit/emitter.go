package audit

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"
	"github.com/txsvc/apikit"
)

// Emitter defines the interface for emitting hub-internal audit events.
type Emitter interface {
	Emit(ctx context.Context, event HubEvent) error
}

// defaultEmitter writes hub audit events to the Store.
type defaultEmitter struct {
	store Store
	mgr   *SSEManager // nil when SSE broadcast is not wired
}

// NewEmitter creates the default Emitter implementation backed by a Store.
// Emit errors are swallowed and logged (TS-17-12).
func NewEmitter(store Store) Emitter {
	return &defaultEmitter{store: store}
}

// NewEmitterWithBroadcast creates an Emitter that writes to the Store and also
// publishes events to the SSE broadcast channel (18-REQ-9.2). Every successful
// Emit call inserts into hub_audit_events and broadcasts to the SSEManager.
func NewEmitterWithBroadcast(store Store, mgr *SSEManager) Emitter {
	return &defaultEmitter{store: store, mgr: mgr}
}

// Emit generates a UUID for the event id, sets ingested_at to current UTC
// time, inserts into hub_audit_events, and optionally broadcasts to SSE
// subscribers. Insert errors are swallowed and logged (TS-17-12).
func (e *defaultEmitter) Emit(ctx context.Context, event HubEvent) error {
	id := uuid.New().String()
	now := apikit.NowUTC()

	metadataJSON := "{}"
	if event.Metadata != nil {
		if data, err := json.Marshal(event.Metadata); err == nil {
			metadataJSON = string(data)
		}
	}

	row := HubEventRow{
		ID:           id,
		EventType:    event.EventType,
		ActorID:      event.ActorID,
		ActorType:    event.ActorType,
		ResourceType: event.ResourceType,
		ResourceID:   event.ResourceID,
		Action:       event.Action,
		Workspace:    event.Workspace,
		Metadata:     metadataJSON,
		IngestedAt:   now,
	}

	err := e.store.InsertHubEvent(ctx, row)
	if err != nil {
		slog.Error("audit: failed to insert hub event",
			"event_type", event.EventType,
			"error", err,
		)
		return nil // swallow error (TS-17-12)
	}

	// Populate the event with generated fields before broadcasting.
	event.ID = id
	event.Timestamp = now

	// Broadcast to SSE subscribers if manager is wired (18-REQ-9.2).
	if e.mgr != nil {
		e.mgr.Broadcast(event)
	}

	return nil
}
