package sandboxes

import (
	"context"
	"log/slog"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// emit records one of the gateway's events for a lease. The event belongs to the
// lease, whose workspace and id it carries, and the actor is the lease itself:
// the credential of a gateway call is the lease token, and created_by says who
// issued it. The metadata holds names, never a secret value, an environment
// value or the token.
//
// Without an emitter nothing is recorded, and an emit that fails is logged and
// changes nothing about the outcome of the call. The event is recorded even when
// the caller has gone away.
func (b *Backend) emit(ctx context.Context, lease *Lease, eventType, action string, metadata map[string]any) {
	if b.audit == nil {
		return
	}
	event := audit.HubEvent{
		EventType:    eventType,
		ResourceType: "sandbox",
		ResourceID:   lease.ID,
		Action:       action,
		Workspace:    lease.Workspace,
		ActorID:      lease.CreatedBy,
		ActorType:    "lease",
		Metadata:     metadata,
	}
	if err := b.audit.Emit(context.WithoutCancel(ctx), event); err != nil {
		slog.Error("audit: failed to emit "+eventType, "workspace", lease.Workspace, "lease_id", lease.ID, "error", err)
	}
}
