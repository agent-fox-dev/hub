package cphost

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/agent-fox-dev/carrypatch"
	"github.com/agent-fox-dev/hub/internal/audit"
)

// A module event becomes a hub event field for field; the hub emitter's
// error comes back; no hub emitter means no module emitter.
func TestEmitter_AdaptsModuleEventsToHubAudit(t *testing.T) {
	if Emitter(nil) != nil {
		t.Error("Emitter(nil) is not nil")
	}
	rec := &hubRecorder{err: errors.New("audit down")}
	meta := map[string]any{"branch_name": "p1"}
	ev := carrypatch.Event{EventType: "hub.patch.create", ActorID: "u1", ActorType: "api_key", ResourceType: "patch", ResourceID: "p1", Action: "create", Workspace: "myws", Metadata: meta}
	if err := Emitter(rec).Emit(context.Background(), ev); !errors.Is(err, rec.err) {
		t.Errorf("Emit error = %v, want the hub emitter's", err)
	}
	want := audit.HubEvent{EventType: "hub.patch.create", ActorID: "u1", ActorType: "api_key", ResourceType: "patch", ResourceID: "p1", Action: "create", Workspace: "myws", Metadata: meta}
	if len(rec.events) != 1 || !reflect.DeepEqual(rec.events[0], want) {
		t.Errorf("hub events = %+v, want [%+v]", rec.events, want)
	}
}
