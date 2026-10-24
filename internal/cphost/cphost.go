// Package cphost binds the carry-patch module (github.com/agent-fox-dev/carrypatch)
// to the hub: the module declares what it needs from its host in carrypatch.Host,
// carrypatch.Emitter and its git hook types, and this package supplies the hub's
// implementations. It is the one place the hub and the module meet besides
// cmd/af-hub, which calls it.
package cphost

import (
	"context"
	"database/sql"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/carrypatch"
	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/gitserver"
	"github.com/agent-fox-dev/hub/internal/wsaccess"
	"github.com/agent-fox-dev/hub/internal/wslock"
)

// Bind hands the module the hub's per-workspace locks and workspace access
// rule. It runs once at start, before any carry-patch route or job exists.
func Bind() {
	carrypatch.SetHost(carrypatch.Host{
		Lock:         wslock.Lock,
		TryLock:      wslock.TryLock,
		BeginRebuild: wslock.BeginRebuild,
		Authorize:    authorize,
	})
}

// authorize is the module's view of wsaccess.AuthorizeWorkspace: members of
// the workspace's org read, owners and admin tokens also write, a member's
// write is 403 and anyone else gets 404.
func authorize(db *sql.DB, auth *apikit.AuthInfo, slug string, need carrypatch.Need) (int, string) {
	n := wsaccess.NeedRead
	if need == carrypatch.NeedWrite {
		n = wsaccess.NeedWrite
	}
	_, code, msg := wsaccess.AuthorizeWorkspace(db, auth, slug, n)
	return code, msg
}

// Emitter adapts the hub's audit emitter to the module's. A nil emitter gives
// a nil carrypatch.Emitter, which the module treats as emission switched off.
func Emitter(e audit.Emitter) carrypatch.Emitter {
	if e == nil {
		return nil
	}
	return emitter{e}
}

type emitter struct{ audit.Emitter }

func (e emitter) Emit(ctx context.Context, ev carrypatch.Event) error {
	return e.Emitter.Emit(ctx, audit.HubEvent{
		EventType:    ev.EventType,
		ActorID:      ev.ActorID,
		ActorType:    ev.ActorType,
		ResourceType: ev.ResourceType,
		ResourceID:   ev.ResourceID,
		Action:       ev.Action,
		Workspace:    ev.Workspace,
		Metadata:     ev.Metadata,
	})
}

// PreReceive adapts the module's pre-receive hook to the git server's.
func PreReceive(hook carrypatch.PreReceiveHook) gitserver.PreReceiveHookFunc {
	return func(ctx context.Context, db *sql.DB, slug string, actor *apikit.AuthInfo, upd gitserver.RefUpdate) error {
		return hook(ctx, db, slug, actor, carrypatch.RefUpdate(upd))
	}
}

// PostPush adapts the module's post-push hook to the git server's.
func PostPush(hook carrypatch.PostPushHook) gitserver.PostPushHookFunc {
	return gitserver.PostPushHookFunc(hook)
}
