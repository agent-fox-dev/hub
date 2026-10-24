package audit

import (
	"net/http"
	"testing"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

const costPath = "/api/v1/workspaces/w/cost"

// GET /workspaces/:slug/cost sums the usage of the window (the last 24 hours
// by default) per day, session or model for a reader of the active
// workspace.
func TestWorkspaceCost_SumsPerDimensionAndDefaultsTheWindow(t *testing.T) {
	env := newEnv(t)
	env.seedUsage(t, TokenUsage{ID: "a", SessionID: "s1", WorkspaceSlug: "w", Model: "m1", InputTokens: 10, ReportedAt: "2026-09-01T10:00:00Z"})
	env.seedUsage(t, TokenUsage{ID: "b", SessionID: "s2", WorkspaceSlug: "w", Model: "m2", OutputTokens: 5, ReportedAt: "2026-09-02T10:00:00Z"})
	env.seedUsage(t, TokenUsage{ID: "c", SessionID: "s3", WorkspaceSlug: "w", Model: "m1", CacheReadTokens: 7, ReportedAt: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)})
	window := "since=2026-09-01T00:00:00Z&until=2026-09-03T00:00:00Z"
	for _, c := range []struct {
		query string
		want  CostBreakdownEntry
	}{
		{"?" + window, CostBreakdownEntry{Date: "2026-09-01", InputTokens: 10, Sessions: 1}},
		{"?group_by=day&" + window, CostBreakdownEntry{Date: "2026-09-01", InputTokens: 10, Sessions: 1}},
		{"?group_by=session&" + window, CostBreakdownEntry{SessionID: "s1", InputTokens: 10, Sessions: 1}},
		{"?group_by=model&" + window, CostBreakdownEntry{Model: "m1", InputTokens: 10, Sessions: 1}},
	} {
		resp := decode[CostResponse](t, env.must(t, http.StatusOK, http.MethodGet, costPath+c.query, "", member))
		if resp.Workspace != "w" || resp.Totals != (CostTotals{InputTokens: 10, OutputTokens: 5, Sessions: 2}) || len(resp.Breakdown) != 2 || resp.Breakdown[0] != c.want {
			t.Errorf("%s: %+v", c.query, resp)
		}
	}
	recent := decode[CostResponse](t, env.must(t, http.StatusOK, http.MethodGet, costPath, "", owner))
	if recent.Totals != (CostTotals{CacheReadTokens: 7, Sessions: 1}) || recent.Period.Since == "" || recent.Period.Until == "" {
		t.Errorf("default window: %+v", recent)
	}
	if empty := decode[CostResponse](t, env.must(t, http.StatusOK, http.MethodGet, costPath+"?since=2020-01-01T00:00:00Z&until=2020-01-02T00:00:00Z", "", admin)); len(empty.Breakdown) != 0 || empty.Totals.Sessions != 0 {
		t.Errorf("empty window: %+v", empty)
	}
}

func TestWorkspaceCost_Refusals(t *testing.T) {
	env := newEnv(t)
	for _, c := range []struct {
		name, path string
		auth       *apikit.AuthInfo
		status     int
		msg        string
	}{
		{"unauthenticated", costPath, nil, http.StatusUnauthorized, "authentication required"},
		{"missing scope", costPath, testutil.PAT("u1", "audit:read"), http.StatusForbidden, "sessions:read scope required"},
		{"stranger", costPath, stranger, http.StatusForbidden, "workspace access denied"},
		{"archived", "/api/v1/workspaces/w-arch/cost", owner, http.StatusForbidden, "workspace access denied"},
		{"bound elsewhere", "/api/v1/workspaces/w-own/cost", bound, http.StatusForbidden, "workspace access denied"},
		{"unknown workspace", "/api/v1/workspaces/nope/cost", owner, http.StatusForbidden, "workspace access denied"},
		{"admin on unknown", "/api/v1/workspaces/nope/cost", admin, http.StatusOK, ""},
		{"bad group_by", costPath + "?group_by=hour", owner, http.StatusBadRequest, "invalid group_by value"},
		{"bad since", costPath + "?since=yesterday", owner, http.StatusBadRequest, "invalid since timestamp"},
		{"bad until", costPath + "?until=tomorrow", owner, http.StatusBadRequest, "invalid until timestamp"},
		{"since after until", costPath + "?since=2026-09-02T00:00:00Z&until=2026-09-01T00:00:00Z", owner, http.StatusBadRequest, "since must be before until"},
		{"since equals until", costPath + "?since=2026-09-01T00:00:00Z&until=2026-09-01T00:00:00Z", owner, http.StatusBadRequest, "since must be before until"},
	} {
		t.Run(c.name, func(t *testing.T) {
			expect(t, env.do(t, http.MethodGet, c.path, "", c.auth), c.status, c.msg)
		})
	}
}
