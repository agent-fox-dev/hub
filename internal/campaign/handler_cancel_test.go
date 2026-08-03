package campaign

import (
	"strings"
	"testing"
)

// TS-12-5: DELETE /campaigns/:id sets campaign status to cancelled and all
// campaign_specs statuses to cancelled, leaving spec branches in place.
func TestCancelCampaign_Returns200WithCancelledStatus(t *testing.T) {
	t.Parallel()
	env := newHandlerTestEnv(t)

	// Seed an active campaign.
	seedCampaign(t, env.db, "camp-1", "ws-slug", "sprint-42", "main", "active",
		`{"specs":["07"],"edges":[]}`, "user")
	seedCampaignSpec(t, env.db, "camp-1", "07", "active", "spec/07-secrets-variables", "abc123")

	rec := env.doRequest(t, "DELETE", "/api/v1/workspaces/ws-slug/campaigns/camp-1", "", adminAuth())

	if rec.Code != 200 {
		t.Fatalf("status code = %d; want 200", rec.Code)
	}

	resp := parseRawJSON(t, rec)
	status, ok := resp["status"].(string)
	if !ok {
		t.Fatal("response missing 'status' field")
	}
	if status != "cancelled" {
		t.Errorf("response status = %q; want %q", status, "cancelled")
	}

	// Verify campaign_specs statuses are cancelled.
	rows, err := env.db.Query("SELECT status FROM campaign_specs WHERE campaign_id = ?", "camp-1")
	if err != nil {
		t.Fatalf("query campaign_specs failed: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var specStatus string
		if err := rows.Scan(&specStatus); err != nil {
			t.Fatalf("scan spec status: %v", err)
		}
		if specStatus != "cancelled" {
			t.Errorf("spec status = %q; want %q", specStatus, "cancelled")
		}
	}
}

// TS-12-1.E1: DELETE request targeting a campaign that is already completed,
// failed, or cancelled returns HTTP 409.
func TestCancelCampaign_409WhenAlreadyTerminal(t *testing.T) {
	t.Parallel()

	terminalStatuses := []string{"completed", "failed", "cancelled"}
	for _, status := range terminalStatuses {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			env := newHandlerTestEnv(t)

			seedCampaign(t, env.db, "camp-"+status, "ws-slug", "camp-"+status, "main", status,
				`{"specs":["07"],"edges":[]}`, "user")

			rec := env.doRequest(t, "DELETE",
				"/api/v1/workspaces/ws-slug/campaigns/camp-"+status, "", adminAuth())

			if rec.Code != 409 {
				t.Fatalf("status code = %d; want 409 for %s campaign", rec.Code, status)
			}

			resp := parseRawJSON(t, rec)
			errMsg, ok := resp["error"].(string)
			if !ok {
				t.Fatal("response missing 'error' field")
			}
			if !strings.Contains(strings.ToLower(errMsg), "cancel") && !strings.Contains(strings.ToLower(errMsg), "not") {
				t.Errorf("error message should indicate non-cancellable state, got: %q", errMsg)
			}
		})
	}
}

// TS-12-2.E3 applied to DELETE: HTTP 403 when caller lacks campaigns:write scope.
func TestCancelCampaign_403WhenLackingWriteScope(t *testing.T) {
	t.Parallel()
	env := newHandlerTestEnv(t)

	seedCampaign(t, env.db, "camp-1", "ws-slug", "sprint-42", "main", "active",
		`{"specs":["07"],"edges":[]}`, "user")

	auth := patAuth("user-1", "campaigns:read")
	rec := env.doRequest(t, "DELETE", "/api/v1/workspaces/ws-slug/campaigns/camp-1", "", auth)

	if rec.Code != 403 {
		t.Fatalf("status code = %d; want 403", rec.Code)
	}
}
