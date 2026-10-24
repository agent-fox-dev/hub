package workspace

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/testutil"
)

// TestAudit_Emit: an event carries the caller as actor, or system without a
// caller; without an emitter nothing is emitted; a failing emitter is logged
// and does not fail the request.
func TestAudit_Emit(t *testing.T) {
	v := newEnv(t)
	logs := captureLog(t)
	c, _ := contextOf(http.MethodGet, "/", "/")
	emitHubAudit(c, audit.HubEvent{EventType: "hub.test", Workspace: "w"})
	apikit.SetAuthInfo(c, testutil.PAT(owner, "workspaces:read"))
	emitHubAudit(c, audit.HubEvent{EventType: "hub.test", Workspace: "w"})
	evs := v.events.ofType("hub.test")
	if len(evs) != 2 || evs[0].ActorType != "system" || evs[0].ActorID != "" || evs[1].ActorType != "pat" || evs[1].ActorID != owner {
		t.Errorf("events %+v", evs)
	}

	v.events.fail = errors.New("sink down")
	v.ready(t, "ws", owner)
	expectStatus(t, v.do(t, http.MethodPost, "/api/v1/workspaces/ws/archive", "", testutil.User(owner)), http.StatusOK)
	if !strings.Contains(logs.String(), "failed to emit event") || !strings.Contains(logs.String(), "hub.workspace.archive") {
		t.Errorf("logs %s", logs)
	}

	swap(t, &defaultAuditEmitter, nil)
	emitHubAudit(c, audit.HubEvent{EventType: "hub.test"})
	v.seed(t, &Workspace{Slug: "quiet", OwnerID: owner, Status: "archived"})
	expectStatus(t, v.do(t, http.MethodPost, "/api/v1/workspaces/quiet/reactivate", "", testutil.User(owner)), http.StatusOK)
	if len(v.events.ofType("hub.workspace.reactivate")) != 0 {
		t.Error("an event reached the removed emitter")
	}
}

// TestAudit_ForceClose: closing a workspace's sessions is skipped without an
// audit store; with one, every closed session is counted down and gets a
// system event, and an emitter failure does not stop the loop.
func TestAudit_ForceClose(t *testing.T) {
	v := newEnv(t)
	if err := forceCloseWorkspaceSessions(context.Background(), "ws", "test", "now"); err != nil || len(v.events.ofType("hub.session.force_closed")) != 0 {
		t.Errorf("without a store: %v", err)
	}
	store := &fakeAuditStore{results: []audit.ForceCloseResult{{SessionID: "a"}, {SessionID: "b"}}}
	metrics := audit.NewMetrics()
	swap(t, &defaultAuditStore, audit.Store(store))
	swap(t, &defaultAuditMetrics, metrics)
	v.events.fail = errors.New("sink down")
	if err := forceCloseWorkspaceSessions(context.Background(), "ws", "workspace archived", "now"); err != nil {
		t.Fatal(err)
	}
	evs := v.events.ofType("hub.session.force_closed")
	if len(evs) != 2 || evs[1].ResourceID != "b" || evs[1].ResourceType != "session" || evs[1].Action != "terminate" || evs[1].Workspace != "ws" ||
		evs[1].Metadata["session_id"] != "b" || evs[1].Metadata["reason"] != "workspace archived" || evs[1].ActorType != "system" {
		t.Errorf("events %+v", evs)
	}
	if store.calls[0] != "ws|workspace archived" {
		t.Errorf("calls %v", store.calls)
	}
}
