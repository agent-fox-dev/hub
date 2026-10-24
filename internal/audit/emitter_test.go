package audit

import (
	"context"
	"testing"
	"time"
)

// Emit stores the event in hub_audit_events with a generated id, the current
// time as timestamp and ingestion time and the metadata as JSON, counts it
// and broadcasts it, with the generated fields filled in, to the SSE
// manager when one is wired.
func TestEmitter_StoresCountsAndBroadcasts(t *testing.T) {
	env := newEnv(t)
	conn, _ := env.mgr.Register(sseFilters{})
	before := time.Now().UTC().Add(-time.Second)
	err := env.emitter.Emit(context.Background(), HubEvent{EventType: EventTokenBind, ActorID: "u1", ActorType: "pat", ResourceType: "token",
		ResourceID: "tok", Action: "bind", Workspace: "w", Metadata: map[string]any{"token_id": "tok"}})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	var id, ts, meta, actorType string
	if err := env.duck.QueryRow("SELECT id, timestamp, metadata, actor_type FROM hub_audit_events WHERE event_type = ?", EventTokenBind).Scan(&id, &ts, &meta, &actorType); err != nil {
		t.Fatal(err)
	}
	when, perr := time.Parse(time.RFC3339Nano, ts)
	if ValidateUUID(id) != nil || perr != nil || when.Before(before) || meta != `{"token_id":"tok"}` || actorType != "pat" {
		t.Errorf("stored row id %q ts %q meta %q actor_type %q", id, ts, meta, actorType)
	}
	select {
	case ev := <-conn.ch:
		if ev.ID != id || ev.Timestamp != ts || ev.Workspace != "w" || ev.Metadata["token_id"] != "tok" {
			t.Errorf("broadcast %+v; want the stored event", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("event not broadcast")
	}
	if got := counter(t, env.metrics.AuditEventsTotal, "hub", EventTokenBind); got != 1 {
		t.Errorf("hub events counted %v; want 1", got)
	}
	if err := NewEmitter(env.store, nil).Emit(context.Background(), HubEvent{EventType: "hub.x"}); err != nil || env.count(t, "hub_audit_events", "event_type = 'hub.x' AND metadata = '{}'") != 1 {
		t.Errorf("emit without manager or metadata: %v", err)
	}
}

// A failed insert is logged and swallowed: Emit returns nil, nothing is
// broadcast and nothing is counted.
func TestEmitter_SwallowsStoreErrors(t *testing.T) {
	env := newEnv(t)
	conn, _ := env.mgr.Register(sseFilters{})
	if err := NewEmitter(failingStore{}, env.mgr).Emit(context.Background(), HubEvent{EventType: "hub.x"}); err != nil {
		t.Errorf("Emit over a failing store: %v; want nil", err)
	}
	env.mgr.Broadcast(liveEvent("end", "w"))
	if ev := <-conn.ch; ev.ID != "end" {
		t.Errorf("a failed event was broadcast: %+v", ev)
	}
	if got := counter(t, env.metrics.AuditEventsTotal, "hub", "hub.x"); got != 0 {
		t.Errorf("failed event counted %v", got)
	}
}
