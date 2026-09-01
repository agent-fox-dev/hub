package audit

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// mockEmitter records Emit calls for compile-time interface verification.
type mockEmitter struct {
	calls []HubEvent
}

func (m *mockEmitter) Emit(_ context.Context, event HubEvent) error {
	m.calls = append(m.calls, event)
	return nil
}

// failingStore is a Store that always returns errors on insert.
type failingStore struct{}

func (f *failingStore) InsertHubEvent(_ context.Context, _ HubEventRow) error {
	return fmt.Errorf("simulated database error")
}

func (f *failingStore) CreateSession(_ context.Context, _ *Session) (*Session, bool, error) {
	return nil, false, fmt.Errorf("simulated database error")
}

func (f *failingStore) GetSession(_ context.Context, _ string) (*Session, error) {
	return nil, fmt.Errorf("simulated database error")
}

func (f *failingStore) CompleteSession(_ context.Context, _ string, _ *CompleteSessionRequest) (*Session, error) {
	return nil, fmt.Errorf("simulated database error")
}

func (f *failingStore) InsertTokenUsage(_ context.Context, _ *TokenUsage) (*TokenUsage, error) {
	return nil, fmt.Errorf("simulated database error")
}

func (f *failingStore) ListSessions(_ context.Context, _ SessionListParams, _ []string) (*SessionListResponse, error) {
	return nil, fmt.Errorf("simulated database error")
}

func (f *failingStore) GetSessionWithSummary(_ context.Context, _ string) (*Session, error) {
	return nil, fmt.Errorf("simulated database error")
}

func (f *failingStore) ListTokenUsage(_ context.Context, _ string, _ UsageListParams) (*UsageListResponse, error) {
	return nil, fmt.Errorf("simulated database error")
}

func (f *failingStore) GetWorkspaceCost(_ context.Context, _ CostParams) (*CostResponse, error) {
	return nil, fmt.Errorf("simulated database error")
}

func (f *failingStore) ForceCloseSessions(_ context.Context, _ string, _ string, _ string) ([]ForceCloseResult, error) {
	return nil, fmt.Errorf("simulated database error")
}

// TS-17-10: Emitter interface and HubEvent struct are exported from the
// internal/audit package with the correct fields.
func TestEmitterInterface(t *testing.T) {
	// Compile-time check: mockEmitter satisfies Emitter
	var _ Emitter = (*mockEmitter)(nil)

	// Verify HubEvent has all required fields
	event := HubEvent{
		EventType:    "test.event",
		ActorID:      "actor-1",
		ActorType:    "system",
		ResourceType: "workspace",
		ResourceID:   "ws1",
		Action:       "create",
		Workspace:    "ws1",
		Metadata:     map[string]any{"key": "value"},
	}

	if event.EventType != "test.event" {
		t.Errorf("EventType = %q, want %q", event.EventType, "test.event")
	}
	if event.ActorID != "actor-1" {
		t.Errorf("ActorID = %q, want %q", event.ActorID, "actor-1")
	}
	if event.ActorType != "system" {
		t.Errorf("ActorType = %q, want %q", event.ActorType, "system")
	}
	if event.Metadata == nil {
		t.Error("Metadata should not be nil")
	}
}

// TS-17-11: Default Emitter.Emit generates a UUID for the event id, sets
// ingested_at to current UTC time, and inserts into hub_audit_events.
func TestDefaultEmitter_EmitWritesToHubAuditEvents(t *testing.T) {
	db := openTestAuditDBWithSchema(t)

	store := NewStore(db)
	emitter := NewEmitter(store)

	event := HubEvent{
		EventType:    "workspace.created",
		ActorID:      "user1",
		ActorType:    "pat",
		ResourceType: "workspace",
		ResourceID:   "ws1",
		Action:       "create",
		Workspace:    "ws1",
		Metadata:     map[string]any{},
	}

	err := emitter.Emit(context.Background(), event)
	if err != nil {
		t.Fatalf("Emit returned error: %v", err)
	}

	// Verify the row was inserted into hub_audit_events
	var (
		id         string
		eventType  string
		actorID    string
		actorType  string
		ingestedAt string
	)
	err = db.QueryRow(`SELECT id, event_type, actor_id, actor_type, ingested_at
		FROM hub_audit_events LIMIT 1`).Scan(&id, &eventType, &actorID, &actorType, &ingestedAt)
	if err != nil {
		t.Fatalf("query hub_audit_events: %v", err)
	}

	if id == "" {
		t.Error("event id should not be empty (UUID expected)")
	}
	if eventType != "workspace.created" {
		t.Errorf("event_type = %q, want %q", eventType, "workspace.created")
	}
	if actorID != "user1" {
		t.Errorf("actor_id = %q, want %q", actorID, "user1")
	}
	if actorType != "pat" {
		t.Errorf("actor_type = %q, want %q", actorType, "pat")
	}
	if ingestedAt == "" {
		t.Error("ingested_at should not be empty")
	}
}

// TS-17-12: Default Emitter.Emit returns nil even when the Store insert
// fails, and logs the error via slog.
func TestDefaultEmitter_EmitSwallowsError(t *testing.T) {
	// Capture slog output to verify error logging
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	origLogger := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(origLogger) })

	store := &failingStore{}
	emitter := NewEmitter(store)

	event := HubEvent{
		EventType:    "test.event",
		ActorID:      "actor-1",
		ActorType:    "system",
		ResourceType: "workspace",
		ResourceID:   "ws1",
		Action:       "create",
		Workspace:    "ws1",
		Metadata:     map[string]any{},
	}

	err := emitter.Emit(context.Background(), event)
	if err != nil {
		t.Fatalf("Emit() returned error: %v; want nil (errors should be swallowed)", err)
	}

	// Verify that the error was logged
	logOutput := buf.String()
	if !strings.Contains(strings.ToUpper(logOutput), "ERROR") {
		t.Errorf("expected slog output to contain error-level message, got: %q", logOutput)
	}
}

// TS-17-13: HubEvent.ActorType accepts all four valid values without
// runtime validation at ingestion time.
func TestDefaultEmitter_ActorTypeValues(t *testing.T) {
	db := openTestAuditDBWithSchema(t)

	store := NewStore(db)
	emitter := NewEmitter(store)

	actorTypes := []string{"admin_token", "api_key", "pat", "system"}
	for _, at := range actorTypes {
		t.Run(at, func(t *testing.T) {
			event := HubEvent{
				EventType:    "test.event",
				ActorID:      "actor-1",
				ActorType:    at,
				ResourceType: "workspace",
				ResourceID:   "ws1",
				Action:       "create",
				Workspace:    "ws1",
				Metadata:     map[string]any{},
			}

			err := emitter.Emit(context.Background(), event)
			if err != nil {
				t.Fatalf("Emit with ActorType=%q returned error: %v", at, err)
			}

			// Verify the row exists with correct actor_type
			var count int
			err = db.QueryRow(
				"SELECT COUNT(*) FROM hub_audit_events WHERE actor_type = ?", at,
			).Scan(&count)
			if err != nil {
				t.Fatalf("count query: %v", err)
			}
			if count < 1 {
				t.Errorf("expected at least 1 row with actor_type=%q, got %d", at, count)
			}
		})
	}
}
