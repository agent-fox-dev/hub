package workspace

import (
	"context"
	"log/slog"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// Package-level audit dependencies for force-close. Set by
// RegisterRoutesWithConfig or SetAuditDependencies; nil when not
// configured (force-close is silently skipped).
var (
	defaultAuditStore   audit.Store
	defaultAuditEmitter audit.Emitter
	defaultAuditMetrics *audit.Metrics
)

// SetAuditDependencies configures the audit store, emitter, and metrics
// used by the force-close logic in workspace archive and delete handlers.
// Call this after MountWorkspaceHandlers when audit dependencies are
// initialised separately from workspace route registration.
func SetAuditDependencies(store audit.Store, emitter audit.Emitter, m *audit.Metrics) {
	defaultAuditStore = store
	defaultAuditEmitter = emitter
	defaultAuditMetrics = m
}

// forceCloseWorkspaceSessions force-closes all active sessions for the
// given workspace. It updates session status to terminated in DuckDB,
// decrements the afhub_agent_sessions_active gauge for each closed
// session, and emits a hub.session.force_closed HubEvent for each.
//
// If the audit store is not configured, this is a no-op. Errors from
// the DuckDB operation are returned so callers can log and continue.
// Individual emitter errors are logged but do not abort the loop.
func forceCloseWorkspaceSessions(ctx context.Context, workspaceSlug, reason, timestamp string) error {
	if defaultAuditStore == nil {
		return nil
	}

	results, err := defaultAuditStore.ForceCloseSessions(ctx, workspaceSlug, reason, timestamp)
	if err != nil {
		return err
	}

	// Decrement gauge for each force-closed session.
	if defaultAuditMetrics != nil {
		for range results {
			defaultAuditMetrics.AgentSessionsActive.WithLabelValues(workspaceSlug).Dec()
		}
	}

	// Emit a hub.session.force_closed event for each closed session.
	if defaultAuditEmitter != nil {
		for _, r := range results {
			event := audit.HubEvent{
				EventType:    "hub.session.force_closed",
				ActorType:    "system",
				ActorID:      "",
				ResourceType: "session",
				ResourceID:   r.SessionID,
				Action:       "terminate",
				Workspace:    workspaceSlug,
				Metadata: map[string]any{
					"session_id": r.SessionID,
					"reason":     reason,
				},
			}
			if emitErr := defaultAuditEmitter.Emit(ctx, event); emitErr != nil {
				// 19-REQ-8.E3: Log emitter error and continue.
				slog.Error("force-close: failed to emit hub.session.force_closed",
					"session_id", r.SessionID,
					"workspace", workspaceSlug,
					"error", emitErr,
				)
			}
		}
	}

	return nil
}
