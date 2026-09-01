package audit

import (
	"net/http"
	"testing"
)

// ---------------------------------------------------------------------------
// 3.5 — Workspace attribution and access control
// Requirements: 17-REQ-20
// Test Spec: TS-17-50, TS-17-51, TS-17-52, TS-17-53, TS-17-54
// ---------------------------------------------------------------------------

// TS-17-50: All audit ingestion endpoints store the workspace slug from the
// URL :slug parameter in the workspace column.
func TestWorkspaceAttribution_StoredInDB(t *testing.T) {
	env := newAuditTestEnv(t)

	// Use the my-workspace slug.
	myWorkspacePath := "/api/v1/workspaces/my-workspace/runs/" + testRunID + "/events"
	body := `{"event_type":"session.start","payload":{}}`
	rec := env.doJSON(t, http.MethodPost, myWorkspacePath, body, apiKeyAuth())

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d\nbody: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	resp := parseJSONMap(t, rec)
	id, _ := resp["id"].(string)

	// Query the stored row and check workspace.
	var workspace string
	err := env.db.QueryRow(
		"SELECT workspace FROM agent_audit_events WHERE id = ?", id,
	).Scan(&workspace)
	if err != nil {
		t.Fatalf("query workspace: %v", err)
	}

	if workspace != "my-workspace" {
		t.Errorf("workspace = %q, want %q", workspace, "my-workspace")
	}
}

// TS-17-51: Audit ingestion endpoint returns HTTP 403 with error type
// workspace_mismatch when a workspace-scoped PAT's workspace does not match
// the URL :slug.
func TestWorkspaceAuth_ScopedTokenMismatch403(t *testing.T) {
	env := newAuditTestEnv(t)

	// PAT scoped to workspace-B, but posting to workspace-A.
	patForB := workspacePatAuth("user-1", "workspace-B", "audit:write")
	wsAPath := "/api/v1/workspaces/workspace-A/runs/" + testRunID + "/events"
	body := `{"event_type":"session.start"}`

	rec := env.doJSON(t, http.MethodPost, wsAPath, body, patForB)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d\nbody: %s", rec.Code, http.StatusForbidden, rec.Body.String())
	}

	var errResp apiErrorEnvelope
	parseJSON(t, rec, &errResp)
	if errResp.Error.ErrorType != "workspace_mismatch" {
		t.Errorf("error_type = %q, want %q", errResp.Error.ErrorType, "workspace_mismatch")
	}
}

// TS-17-52: Audit ingestion endpoint returns HTTP 403 with error type
// workspace_access_denied when the token owner lacks write access.
func TestWorkspaceAuth_AccessDenied403(t *testing.T) {
	env := newAuditTestEnv(t)

	// Generic PAT without workspace access.
	noAccessPat := patAuth("user-no-access", "audit:write")
	body := `{"event_type":"session.start"}`

	rec := env.doJSON(t, http.MethodPost, eventsPath, body, noAccessPat)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d\nbody: %s", rec.Code, http.StatusForbidden, rec.Body.String())
	}

	var errResp apiErrorEnvelope
	parseJSON(t, rec, &errResp)
	if errResp.Error.ErrorType != "workspace_access_denied" {
		t.Errorf("error_type = %q, want %q", errResp.Error.ErrorType, "workspace_access_denied")
	}
}

// TS-17-53: Audit ingestion endpoint returns HTTP 409 with error type
// workspace_archived when the target workspace is archived.
func TestWorkspaceAuth_ArchivedWorkspace409(t *testing.T) {
	env := newAuditTestEnvWithSQLite(t)
	env.seedWorkspaceWithStatus(t, testSlug, "owner-1", "archived")

	body := `{"event_type":"session.start"}`
	rec := env.doJSON(t, http.MethodPost, eventsPath, body, apiKeyAuth())

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d\nbody: %s", rec.Code, http.StatusConflict, rec.Body.String())
	}

	var errResp apiErrorEnvelope
	parseJSON(t, rec, &errResp)
	if errResp.Error.ErrorType != "workspace_archived" {
		t.Errorf("error_type = %q, want %q", errResp.Error.ErrorType, "workspace_archived")
	}
}

// TS-17-54: Audit ingestion endpoint returns HTTP 404 when the target
// workspace does not exist.
func TestWorkspaceAuth_NotFound404(t *testing.T) {
	env := newAuditTestEnvWithSQLite(t)
	// Do NOT seed any workspace — it should not exist.

	nonexistentPath := "/api/v1/workspaces/nonexistent-ws/runs/" + testRunID + "/events"
	body := `{"event_type":"session.start"}`
	rec := env.doJSON(t, http.MethodPost, nonexistentPath, body, apiKeyAuth())

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d\nbody: %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

// TS-17-51: Audit ingestion with PAT lacking audit:write scope returns 403.
func TestWorkspaceAuth_InsufficientScope403(t *testing.T) {
	env := newAuditTestEnv(t)

	// PAT with only audit:read (no write).
	readOnlyPat := patAuth("user-1", "audit:read")
	body := `{"event_type":"session.start"}`

	rec := env.doJSON(t, http.MethodPost, eventsPath, body, readOnlyPat)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d\nbody: %s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

// No auth header at all returns 401.
func TestWorkspaceAuth_Unauthenticated401(t *testing.T) {
	env := newAuditTestEnv(t)

	body := `{"event_type":"session.start"}`
	rec := env.doJSON(t, http.MethodPost, eventsPath, body, nil)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d\nbody: %s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
}

// 17-REQ-20.E1: Admin token bypasses workspace restriction checks.
func TestWorkspaceAuth_AdminTokenFullAccess(t *testing.T) {
	env := newAuditTestEnv(t)

	body := `{"event_type":"session.start"}`
	rec := env.doJSON(t, http.MethodPost, eventsPath, body, adminAuth())

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d\nbody: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
}

// 17-REQ-20.6: GET query endpoints include workspace_slug filter regardless
// of caller credentials — verify events from another workspace are not leaked.
func TestWorkspaceQuery_WorkspaceIsolation(t *testing.T) {
	env := newAuditTestEnv(t)

	// Insert events for ws1 and ws2.
	env.seedAuditEvent(t, "550e8400-e29b-41d4-a716-446655440001",
		testRunID, "ws1", "session.start", "2026-09-01T13:00:00Z")
	env.seedAuditEvent(t, "550e8400-e29b-41d4-a716-446655440002",
		testRunID, "ws2", "session.start", "2026-09-01T13:01:00Z")

	// Query for ws1 only.
	rec := env.doJSON(t, http.MethodGet, eventsPath+"?limit=10", "", adminAuth())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d\nbody: %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp eventsResponse
	parseJSON(t, rec, &resp)

	// Should only see events for ws1.
	for _, e := range resp.Events {
		ws, _ := e["workspace"].(string)
		if ws != "" && ws != testSlug {
			t.Errorf("event from wrong workspace: %q, want %q", ws, testSlug)
		}
	}
}
