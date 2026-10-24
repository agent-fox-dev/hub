package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
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

// NewEmitter creates an Emitter that writes to the Store and, when mgr is not nil,
// publishes events to the SSE broadcast channel. Every successful
// Emit call inserts into hub_audit_events and broadcasts to the SSEManager.
func NewEmitter(store Store, mgr *SSEManager) Emitter {
	return &defaultEmitter{store: store, mgr: mgr}
}

// Emit generates a UUID for the event id, sets timestamp and ingested_at to
// the current UTC time (microsecond precision so that hub and agent events
// interleave correctly in the unified query), inserts into hub_audit_events,
// and optionally broadcasts to SSE subscribers. Insert errors are swallowed
// and logged (TS-17-12).
func (e *defaultEmitter) Emit(ctx context.Context, event HubEvent) error {
	id := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339Nano)

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
		Timestamp:    now,
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

	recordAuditEvents("hub", event.EventType, 1)

	// Populate the event with generated fields before broadcasting.
	event.ID = id
	event.Timestamp = now

	// Broadcast to SSE subscribers if manager is wired (18-REQ-9.2).
	if e.mgr != nil {
		e.mgr.Broadcast(event)
	}

	return nil
}
