package workspace

import (
	"context"
	"log/slog"

	"github.com/labstack/echo/v4"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// emitHubAudit emits a hub-internal audit event using the package-level
// defaultAuditEmitter. If the emitter is nil, emission is silently skipped.
// If Emit returns an error, the error is logged and the caller is unaffected.
//
// The caller provides a partially-filled HubEvent; this function populates
// ActorID and ActorType from the request's auth context. When auth info is
// unavailable (unauthenticated context), ActorID is empty and ActorType is
// "system".
func emitHubAudit(c echo.Context, event audit.HubEvent) {
	emitHubAuditContext(c.Request().Context(), c, event)
}

// emitHubAuditContext is emitHubAudit emitting on ctx rather than on the
// request's context, for an event that must be recorded when the caller has
// gone away.
func emitHubAuditContext(ctx context.Context, c echo.Context, event audit.HubEvent) {
	if defaultAuditEmitter == nil {
		return
	}

	auth := apikit.GetAuthInfo(c)
	if auth != nil {
		event.ActorID = auth.UserID
		event.ActorType = auth.CredentialType
	} else {
		event.ActorID = ""
		event.ActorType = "system"
	}

	if err := defaultAuditEmitter.Emit(ctx, event); err != nil {
		slog.Error("audit: failed to emit event",
			"event_type", event.EventType,
			"workspace", event.Workspace,
			"error", err,
		)
	}
}
