package forge

import (
	"context"
	"log/slog"

	"github.com/labstack/echo/v4"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/scrub"
)

// emit records the event of a write that sent at least one request to the forge: one
// for the request, whether the write succeeded or not, and before the answer is
// written. errorType is the error_type hub answered with, "" for a success. Without an
// emitter nothing is recorded, and an emitter that fails is logged and changes nothing
// about the answer. The event is recorded when the caller has gone away.
//
// Its metadata holds names, numbers and URLs and never a credential, a title, a body, a
// comment or a message from the forge, so that none of them can reach the audit store.
func (h *handlers) emit(c echo.Context, req *request, rt writeRoute, w *writeRun, errorType string) {
	if h.cfg.Audit == nil || !w.sent {
		return
	}
	metadata := map[string]any{"target": string(w.fc.target), "ok": errorType == ""}
	if errorType != "" {
		metadata["error_type"] = errorType
	}
	if w.number > 0 {
		metadata["number"] = w.number
	}
	if w.url != "" {
		metadata["url"] = scrub.Text(w.url, w.fc.token)
	}
	for key, value := range w.extra {
		metadata[key] = value
	}
	event := audit.HubEvent{
		EventType:    rt.event.eventType,
		ActorID:      req.auth.UserID,
		ActorType:    req.auth.CredentialType,
		ResourceType: rt.event.resource,
		ResourceID:   w.resourceID,
		Action:       rt.event.action,
		Workspace:    req.slug,
		Metadata:     metadata,
	}
	if err := h.cfg.Audit.Emit(context.WithoutCancel(c.Request().Context()), event); err != nil {
		slog.Error("audit: failed to emit "+event.EventType, "workspace", req.slug, "error", err)
	}
}
