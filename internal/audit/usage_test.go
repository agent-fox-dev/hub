package audit

import (
	"net/http"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// counter reads a CounterVec series.
func counter(t *testing.T, c *prometheus.CounterVec, labels ...string) float64 {
	t.Helper()
	m, err := c.GetMetricWithLabelValues(labels...)
	if err != nil {
		t.Fatal(err)
	}
	return metricValue(t, m)
}

// POST /sessions/:id/usage records one token_usage row per call on an active
// session the caller owns, replays a duplicate id and counts each direction
// on the token counter.
func TestReportUsage_RecordsIncrementsAndCounts(t *testing.T) {
	env := newEnv(t)
	env.seedSession(t, Session{ID: "s1", WorkspaceSlug: "w"})
	path := sessions + "/s1/usage"
	u := decode[TokenUsage](t, env.must(t, http.StatusCreated, http.MethodPost, path, `{"id":"`+uuidN(1)+`","model":"m","input_tokens":10,"output_tokens":5,"cache_read_tokens":2}`, owner))
	if u.ID != uuidN(1) || u.SessionID != "s1" || u.WorkspaceSlug != "w" || u.InputTokens != 10 || u.ReportedAt == "" {
		t.Errorf("recorded usage %+v", u)
	}
	again := decode[TokenUsage](t, env.must(t, http.StatusCreated, http.MethodPost, path, `{"id":"`+uuidN(1)+`","model":"m"}`, owner))
	if again.InputTokens != 10 || env.count(t, "token_usage", "") != 1 {
		t.Errorf("duplicate id recorded again: %+v", again)
	}
	second := decode[TokenUsage](t, env.must(t, http.StatusCreated, http.MethodPost, path, `{"model":"m","output_tokens":1}`, admin))
	if ValidateUUID(second.ID) != nil || env.count(t, "token_usage", "") != 2 {
		t.Errorf("usage without id: %+v", second)
	}
	for direction, want := range map[string]float64{"input": 10, "output": 6, "cache_read": 2} {
		if got := counter(t, env.metrics.AgentTokensTotal, "w", "m", direction); got != want {
			t.Errorf("tokens %s = %v; want %v", direction, got, want)
		}
	}
}

func TestReportUsage_Refusals(t *testing.T) {
	env := newEnv(t)
	env.seedSession(t, Session{ID: "s1", WorkspaceSlug: "w"})
	env.seedSession(t, Session{ID: "done", WorkspaceSlug: "w", Status: "completed"})
	env.seedSession(t, Session{ID: "sx", WorkspaceSlug: "w-x"})
	path := sessions + "/s1/usage"
	ok := `{"model":"m","input_tokens":1}`
	for _, c := range []struct {
		name, path, body string
		auth             *apikit.AuthInfo
		status           int
		msg              string
	}{
		{"unauthenticated", path, ok, nil, http.StatusUnauthorized, "authentication required"},
		{"missing scope", path, ok, testutil.PAT("u1", "sessions:read"), http.StatusForbidden, "sessions:write scope required"},
		{"invalid body", path, `{`, owner, http.StatusBadRequest, "invalid request body"},
		{"missing model", path, `{"input_tokens":1}`, owner, http.StatusBadRequest, "model is required"},
		{"negative tokens", path, `{"model":"m","output_tokens":-1}`, owner, http.StatusUnprocessableEntity, "token counts must be non-negative"},
		{"unknown session", sessions + "/none/usage", ok, owner, http.StatusNotFound, "session not found"},
		{"not the owner", path, ok, member, http.StatusForbidden, "not session owner"},
		{"bound elsewhere", sessions + "/sx/usage", ok, bound, http.StatusForbidden, "not session owner"},
		{"terminal session", sessions + "/done/usage", ok, owner, http.StatusConflict, "session is not active"},
	} {
		t.Run(c.name, func(t *testing.T) {
			expect(t, env.do(t, http.MethodPost, c.path, c.body, c.auth), c.status, c.msg)
		})
	}
}

// GET /sessions/:id/usage pages the records of a session (100 by default,
// 1000 at most) with totals over all of them, for a reader of its workspace.
func TestListSessionUsage_PagesWithTotals(t *testing.T) {
	env := newEnv(t)
	env.seedSession(t, Session{ID: "s1", WorkspaceSlug: "w"})
	env.seedSession(t, Session{ID: "sx", WorkspaceSlug: "w-x"})
	for i := 0; i < 3; i++ {
		env.seedUsage(t, TokenUsage{ID: uuidN(i), SessionID: "s1", WorkspaceSlug: "w", Model: "m", InputTokens: 10, ReportedAt: daysAgo(3 - i)})
	}
	path := sessions + "/s1/usage"
	first := decode[UsageListResponse](t, env.must(t, http.StatusOK, http.MethodGet, path+"?limit=2", "", member))
	if first.SessionID != "s1" || len(first.Records) != 2 || !first.HasMore || first.NextCursor == nil || first.Records[0].ID != uuidN(2) || first.Totals.TotalInputTokens != 30 {
		t.Fatalf("first page: %+v", first)
	}
	second := decode[UsageListResponse](t, env.must(t, http.StatusOK, http.MethodGet, path+"?limit=2&cursor="+*first.NextCursor, "", member))
	if len(second.Records) != 1 || second.HasMore || second.Records[0].ID != uuidN(0) || second.Totals.TotalInputTokens != 30 {
		t.Errorf("second page: %+v", second)
	}
	if asc := decode[UsageListResponse](t, env.must(t, http.StatusOK, http.MethodGet, path+"?order=asc&limit=x", "", member)); asc.Records[0].ID != uuidN(0) || len(asc.Records) != 3 {
		t.Errorf("ascending: %+v", asc)
	}
	for _, c := range []struct {
		name, path string
		auth       *apikit.AuthInfo
		status     int
		msg        string
	}{
		{"malformed cursor", path + "?cursor=***", owner, http.StatusBadRequest, "invalid cursor"},
		{"unknown session", sessions + "/none/usage", owner, http.StatusNotFound, "session not found"},
		{"stranger", path, stranger, http.StatusForbidden, "workspace access denied"},
		{"bound elsewhere", sessions + "/sx/usage", bound, http.StatusForbidden, "workspace access denied"},
		{"admin anywhere", sessions + "/sx/usage", admin, http.StatusOK, ""},
		{"no scope", path, testutil.PAT("u1"), http.StatusForbidden, "sessions:read scope required"},
		{"unauthenticated", path, nil, http.StatusUnauthorized, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			expect(t, env.do(t, http.MethodGet, c.path, "", c.auth), c.status, c.msg)
		})
	}
}
