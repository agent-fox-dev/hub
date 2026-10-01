package audit

import (
	"net/http"
	"testing"
)

// TS-01-52 (audit side): hub.rebuild.followup is accepted as an event type by
// audit queries, both as an exact filter and by prefix.
// Requirement: 01-REQ-7.7
func TestQueryAudit_RebuildFollowupEventType_TS_01_52(t *testing.T) {
	if EventRebuildFollowup != "hub.rebuild.followup" {
		t.Fatalf("EventRebuildFollowup = %q", EventRebuildFollowup)
	}

	env := newUnifiedQueryTestEnv(t)
	insertTestHubEvent(t, env.db,
		"hub-1", EventRebuildFollowup, "system", "system",
		"patch", "ws-1", "followup", "ws-1", "info",
		"2026-09-01T12:00:00Z")
	insertTestHubEvent(t, env.db,
		"hub-2", "hub.rebuild.complete", "system", "system",
		"patch", "ws-1", "complete", "ws-1", "info",
		"2026-09-01T13:00:00Z")

	for _, query := range []string{"event_type=hub.rebuild.followup", "event_type_prefix=hub.rebuild.follow"} {
		rec := env.doRequest(t, http.MethodGet, auditQueryPath+"?"+query, "", adminAuth())
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", query, rec.Code, rec.Body.String())
		}
		var resp auditQueryResponse
		parseJSON(t, rec, &resp)
		if len(resp.Events) != 1 {
			t.Fatalf("%s: events = %d, want 1", query, len(resp.Events))
		}
		if et, _ := resp.Events[0]["event_type"].(string); et != EventRebuildFollowup {
			t.Errorf("%s: event_type = %q", query, et)
		}
	}
}
