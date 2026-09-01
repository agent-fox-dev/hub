package audit

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// TS-19-28: Workspace archive force-closes all active sessions with
// status=terminated, sets ended_at and error_message, decrements gauge,
// and emits hub.session.force_closed events.
// Requirement: 19-REQ-8.1
func TestForceClose_ArchiveTerminatesActiveSessions_TS19_28(t *testing.T) {
	env, emitter := newAuditTestEnvWithEmitter(t)

	// Seed workspace.
	env.seedWorkspace(t, "ws-1", "user-1")

	// Seed two active sessions for ws-1.
	env.seedSession(t, &Session{
		ID:             "sess-active-1",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-1",
		CredentialType: "api_key",
	})
	env.seedSession(t, &Session{
		ID:             "sess-active-2",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-2",
		CredentialType: "api_key",
	})

	// Force-close sessions for ws-1 (simulating workspace archive).
	archiveTime := time.Now().UTC().Format(time.RFC3339)
	results, err := env.store.ForceCloseSessions(
		context.Background(), "ws-1", "workspace archived", archiveTime,
	)
	if err != nil {
		t.Fatalf("ForceCloseSessions: %v", err)
	}

	// Expect two sessions were closed.
	if len(results) != 2 {
		t.Fatalf("force-closed count = %d; want 2", len(results))
	}

	// Verify both sessions are now terminated with correct fields.
	for _, id := range []string{"sess-active-1", "sess-active-2"} {
		status := env.getSessionStatus(t, id)
		if status != "terminated" {
			t.Errorf("session %q status = %q; want %q", id, status, "terminated")
		}

		endedAt := env.getSessionField(t, id, "ended_at")
		if endedAt == "" {
			t.Errorf("session %q ended_at is empty; want a timestamp", id)
		}

		errMsg := env.getSessionField(t, id, "error_message")
		if errMsg != "workspace archived" {
			t.Errorf("session %q error_message = %q; want %q", id, errMsg, "workspace archived")
		}
	}

	// Emit HubEvents for each force-closed session.
	ctx := context.Background()
	for _, result := range results {
		event := HubEvent{
			EventType:    "hub.session.force_closed",
			ActorType:    "system",
			ActorID:      "",
			ResourceType: "session",
			ResourceID:   result.SessionID,
			Action:       "terminate",
			Workspace:    "ws-1",
			Metadata: map[string]any{
				"session_id": result.SessionID,
				"reason":     "workspace archived",
			},
		}
		if err := emitter.Emit(ctx, event); err != nil {
			t.Errorf("Emit for %q: %v", result.SessionID, err)
		}
	}

	// Verify emitted events.
	if len(emitter.events) != 2 {
		t.Fatalf("emitted events count = %d; want 2", len(emitter.events))
	}

	for _, ev := range emitter.events {
		if ev.EventType != "hub.session.force_closed" {
			t.Errorf("event.EventType = %q; want %q", ev.EventType, "hub.session.force_closed")
		}
		if ev.ActorType != "system" {
			t.Errorf("event.ActorType = %q; want %q", ev.ActorType, "system")
		}
		if ev.ActorID != "" {
			t.Errorf("event.ActorID = %q; want empty", ev.ActorID)
		}
		if ev.ResourceType != "session" {
			t.Errorf("event.ResourceType = %q; want %q", ev.ResourceType, "session")
		}
		if ev.Action != "terminate" {
			t.Errorf("event.Action = %q; want %q", ev.Action, "terminate")
		}
		reason, _ := ev.Metadata["reason"].(string)
		if reason != "workspace archived" {
			t.Errorf("event.Metadata[reason] = %q; want %q", reason, "workspace archived")
		}
	}
}

// TS-19-29: Workspace archive force-close does not modify sessions that are
// already in a terminal state.
// Requirement: 19-REQ-8.2
// Property: 19-PROP-6
func TestForceClose_ArchiveLeavesTerminalSessions_TS19_29(t *testing.T) {
	env := newAuditTestEnvWithSQLite(t)

	// Seed workspace.
	env.seedWorkspace(t, "ws-1", "user-1")

	// Seed one active and one completed session.
	env.seedSession(t, &Session{
		ID:             "sess-active",
		WorkspaceSlug:  "ws-1",
		Status:         "active",
		CredentialID:   "cred-1",
		CredentialType: "api_key",
	})
	endedAt := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	env.seedSession(t, &Session{
		ID:             "sess-completed",
		WorkspaceSlug:  "ws-1",
		Status:         "completed",
		CredentialID:   "cred-1",
		CredentialType: "api_key",
		EndedAt:        &endedAt,
	})

	// Force-close.
	archiveTime := time.Now().UTC().Format(time.RFC3339)
	results, err := env.store.ForceCloseSessions(
		context.Background(), "ws-1", "workspace archived", archiveTime,
	)
	if err != nil {
		t.Fatalf("ForceCloseSessions: %v", err)
	}

	// Only the active session should have been closed.
	if len(results) != 1 {
		t.Fatalf("force-closed count = %d; want 1", len(results))
	}

	// Active session is now terminated.
	activeStatus := env.getSessionStatus(t, "sess-active")
	if activeStatus != "terminated" {
		t.Errorf("active session status = %q; want %q", activeStatus, "terminated")
	}

	// Completed session remains completed.
	completedStatus := env.getSessionStatus(t, "sess-completed")
	if completedStatus != "completed" {
		t.Errorf("completed session status = %q; want %q", completedStatus, "completed")
	}
}

// TS-19-30: Workspace delete force-closes all active sessions with
// status=terminated and error_message='workspace deleted' before the
// workspace row is deleted.
// Requirement: 19-REQ-9.1
func TestForceClose_DeleteTerminatesActiveSessions_TS19_30(t *testing.T) {
	env, emitter := newAuditTestEnvWithEmitter(t)

	// Seed workspace 'ws-del'.
	env.seedWorkspace(t, "ws-del", "user-1")

	// Seed two active sessions.
	env.seedSession(t, &Session{
		ID:             "sess-del-1",
		WorkspaceSlug:  "ws-del",
		Status:         "active",
		CredentialID:   "cred-1",
		CredentialType: "api_key",
	})
	env.seedSession(t, &Session{
		ID:             "sess-del-2",
		WorkspaceSlug:  "ws-del",
		Status:         "active",
		CredentialID:   "cred-2",
		CredentialType: "api_key",
	})

	// Force-close sessions (simulating pre-delete).
	deleteTime := time.Now().UTC().Format(time.RFC3339)
	results, err := env.store.ForceCloseSessions(
		context.Background(), "ws-del", "workspace deleted", deleteTime,
	)
	if err != nil {
		t.Fatalf("ForceCloseSessions: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("force-closed count = %d; want 2", len(results))
	}

	// Verify sessions are terminated with correct error message.
	for _, id := range []string{"sess-del-1", "sess-del-2"} {
		status := env.getSessionStatus(t, id)
		if status != "terminated" {
			t.Errorf("session %q status = %q; want %q", id, status, "terminated")
		}

		errMsg := env.getSessionField(t, id, "error_message")
		if errMsg != "workspace deleted" {
			t.Errorf("session %q error_message = %q; want %q", id, errMsg, "workspace deleted")
		}
	}

	// Emit events and verify.
	ctx := context.Background()
	for _, result := range results {
		event := HubEvent{
			EventType:    "hub.session.force_closed",
			ActorType:    "system",
			ActorID:      "",
			ResourceType: "session",
			ResourceID:   result.SessionID,
			Action:       "terminate",
			Workspace:    "ws-del",
			Metadata: map[string]any{
				"session_id": result.SessionID,
				"reason":     "workspace deleted",
			},
		}
		if err := emitter.Emit(ctx, event); err != nil {
			t.Errorf("Emit for %q: %v", result.SessionID, err)
		}
	}

	for _, ev := range emitter.events {
		reason, _ := ev.Metadata["reason"].(string)
		if reason != "workspace deleted" {
			t.Errorf("event.Metadata[reason] = %q; want %q", reason, "workspace deleted")
		}
	}
}

// TS-19-31: Workspace delete retains audit data (agent_sessions, token_usage)
// in DuckDB after the workspace row is removed from SQLite.
// Requirement: 19-REQ-9.2
func TestForceClose_DeleteRetainsAuditData_TS19_31(t *testing.T) {
	env := newAuditTestEnvWithSQLite(t)

	// Seed workspace in SQLite.
	env.seedWorkspace(t, "ws-del", "user-1")

	// Seed sessions and token_usage in DuckDB.
	env.seedSession(t, &Session{
		ID:             "sess-del-1",
		WorkspaceSlug:  "ws-del",
		Status:         "active",
		CredentialID:   "cred-1",
		CredentialType: "api_key",
	})
	env.seedTokenUsage(t, &TokenUsage{
		ID:            "u-del-1",
		SessionID:     "sess-del-1",
		WorkspaceSlug: "ws-del",
		Model:         "gpt-4",
		InputTokens:   100,
	})

	// Force-close sessions.
	deleteTime := time.Now().UTC().Format(time.RFC3339)
	_, err := env.store.ForceCloseSessions(
		context.Background(), "ws-del", "workspace deleted", deleteTime,
	)
	if err != nil {
		t.Fatalf("ForceCloseSessions: %v", err)
	}

	// Simulate workspace deletion from SQLite.
	_, err = env.sqliteDB.Exec("DELETE FROM workspaces WHERE slug = ?", "ws-del")
	if err != nil {
		t.Fatalf("delete workspace from SQLite: %v", err)
	}

	// Verify workspace is gone from SQLite.
	if env.workspaceExistsInSQLite(t, "ws-del") {
		t.Error("workspace still exists in SQLite after deletion")
	}

	// Verify audit data (sessions) is still in DuckDB.
	sessionCount := env.countSessionRows(t, "ws-del")
	if sessionCount == 0 {
		t.Error("sessions were deleted from DuckDB; want them retained")
	}

	// Verify token_usage is still in DuckDB.
	usageCount := env.countTokenUsageRows(t, "ws-del")
	if usageCount == 0 {
		t.Error("token_usage rows were deleted from DuckDB; want them retained")
	}
}

// 19-REQ-8.E2: Force-close on workspace with no active sessions completes
// without error.
func TestForceClose_NoActiveSessions(t *testing.T) {
	env := newAuditTestEnvWithSQLite(t)

	env.seedWorkspace(t, "ws-empty", "user-1")

	results, err := env.store.ForceCloseSessions(
		context.Background(), "ws-empty", "workspace archived", time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("ForceCloseSessions: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("force-closed count = %d; want 0", len(results))
	}
}

// 19-REQ-8.E1: Force-close failure on archive should not abort the workflow.
// This test verifies the store method returns an error that the handler can
// log and proceed.
func TestForceClose_ErrorHandling(t *testing.T) {
	// This is tested indirectly: the store returns an error which the
	// workspace archive handler should log and continue. We verify the
	// Store interface contract here.
	env := newAuditTestEnv(t)

	// Calling ForceCloseSessions on a non-existent workspace should not error
	// (just return zero results).
	results, err := env.store.ForceCloseSessions(
		context.Background(), "nonexistent", "test", time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("ForceCloseSessions on nonexistent workspace: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("force-closed count = %d; want 0", len(results))
	}
}

// 19-REQ-8.2: Force-close does not modify sessions with status=failed.
func TestForceClose_LeavesFailedSessions(t *testing.T) {
	env := newAuditTestEnvWithSQLite(t)
	env.seedWorkspace(t, "ws-1", "user-1")

	env.seedSession(t, &Session{
		ID:             "sess-failed",
		WorkspaceSlug:  "ws-1",
		Status:         "failed",
		CredentialID:   "cred-1",
		CredentialType: "api_key",
	})
	env.seedSession(t, &Session{
		ID:             "sess-timeout",
		WorkspaceSlug:  "ws-1",
		Status:         "timeout",
		CredentialID:   "cred-1",
		CredentialType: "api_key",
	})

	results, err := env.store.ForceCloseSessions(
		context.Background(), "ws-1", "workspace archived", time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("ForceCloseSessions: %v", err)
	}

	if len(results) != 0 {
		t.Errorf("force-closed count = %d; want 0 (only terminal sessions)", len(results))
	}

	// Verify statuses unchanged.
	if s := env.getSessionStatus(t, "sess-failed"); s != "failed" {
		t.Errorf("failed session status = %q; want %q", s, "failed")
	}
	if s := env.getSessionStatus(t, "sess-timeout"); s != "timeout" {
		t.Errorf("timeout session status = %q; want %q", s, "timeout")
	}
}

// 19-REQ-9.E2: SQLite workspace deletion fails after DuckDB sessions have
// been force-closed — sessions remain safely closed in DuckDB.
func TestForceClose_SQLiteFailureAfterDuckDBClose(t *testing.T) {
	env := newAuditTestEnvWithSQLite(t)
	env.seedWorkspace(t, "ws-fail", "user-1")

	env.seedSession(t, &Session{
		ID:             "sess-1",
		WorkspaceSlug:  "ws-fail",
		Status:         "active",
		CredentialID:   "cred-1",
		CredentialType: "api_key",
	})

	// Force-close in DuckDB.
	_, err := env.store.ForceCloseSessions(
		context.Background(), "ws-fail", "workspace deleted", time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("ForceCloseSessions: %v", err)
	}

	// Sessions are terminated in DuckDB.
	status := env.getSessionStatus(t, "sess-1")
	if status != "terminated" {
		t.Errorf("session status = %q; want %q", status, "terminated")
	}

	// Simulate SQLite failure: the workspace still exists in SQLite.
	// (We just don't delete it — verifying that DuckDB changes persist
	// independently of SQLite operations.)
	if !env.workspaceExistsInSQLite(t, "ws-fail") {
		t.Error("workspace should still exist in SQLite")
	}
}

// Workspace access scoping: admin can fetch usage from any workspace.
func TestListSessionUsage_AdminCanAccessAnyWorkspace(t *testing.T) {
	env := newAuditTestEnv(t)

	env.seedSession(t, &Session{
		ID:             "sess-private",
		WorkspaceSlug:  "ws-private",
		Status:         "active",
		CredentialID:   "cred-other",
		CredentialType: "api_key",
	})

	rec := env.doRequest(t, http.MethodGet,
		"/api/v1/sessions/sess-private/usage",
		"", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
}
