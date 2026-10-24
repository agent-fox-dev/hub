package audit

import (
	"net/http"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// gauge reads a GaugeVec series.
func gauge(t *testing.T, g *prometheus.GaugeVec, labels ...string) float64 {
	t.Helper()
	var m prometheus.Metric
	m, err := g.GetMetricWithLabelValues(labels...)
	if err != nil {
		t.Fatal(err)
	}
	return metricValue(t, m)
}

// POST /sessions opens an active session owned by the caller, with every
// optional field stored, and counts it on the active-sessions gauge once:
// replaying the id returns the first session with 200 and no increment.
func TestCreateSession_OpensAndReplaysIdempotently(t *testing.T) {
	env := newEnv(t)
	body := `{"id":"` + uuidN(1) + `","workspace_slug":"w","run_id":"` + runID + `","node_id":"n1","archetype":"coder","model":"m","metadata":{"k":"v"}}`
	s := decode[Session](t, env.must(t, http.StatusCreated, http.MethodPost, sessions, body, owner))
	if s.ID != uuidN(1) || s.Status != "active" || s.CredentialID != "u1" || s.CredentialType != "pat" || s.RunID != runID ||
		s.NodeID != "n1" || s.Archetype != "coder" || s.Model != "m" || s.StartedAt == "" || s.Metadata.(map[string]any)["k"] != "v" {
		t.Errorf("opened session %+v", s)
	}
	again := decode[Session](t, env.must(t, http.StatusOK, http.MethodPost, sessions, `{"id":"`+uuidN(1)+`","workspace_slug":"w","model":"other"}`, owner))
	if again.Model != "m" {
		t.Errorf("replay returned model %q; want the first session's", again.Model)
	}
	if g := gauge(t, env.metrics.AgentSessionsActive, "w"); g != 1 {
		t.Errorf("active gauge %v; want 1", g)
	}
	generated := decode[Session](t, env.must(t, http.StatusCreated, http.MethodPost, sessions, `{"workspace_slug":"w"}`, admin))
	if ValidateUUID(generated.ID) != nil || generated.CredentialType != "admin_token" {
		t.Errorf("session without id: %+v; want a generated UUID", generated)
	}
}

// Only the workspace's owner and admin tokens open sessions: a member is
// refused with 403, a stranger, an unknown workspace and a token bound to
// another workspace with 404; the request itself is validated first.
func TestCreateSession_Refusals(t *testing.T) {
	env := newEnv(t)
	for _, c := range []struct {
		name, body string
		auth       *apikit.AuthInfo
		status     int
		msg        string
	}{
		{"unauthenticated", `{"workspace_slug":"w"}`, nil, http.StatusUnauthorized, "authentication required"},
		{"missing scope", `{"workspace_slug":"w"}`, testutil.PAT("u1", "sessions:read"), http.StatusForbidden, "sessions:write scope required"},
		{"invalid body", `{`, owner, http.StatusBadRequest, "invalid request body"},
		{"blank slug", `{"workspace_slug":"  "}`, owner, http.StatusBadRequest, "workspace_slug is required"},
		{"member", `{"workspace_slug":"w"}`, member, http.StatusForbidden, "org members have read-only access to this workspace"},
		{"stranger", `{"workspace_slug":"w"}`, stranger, http.StatusNotFound, "workspace not found"},
		{"unknown workspace", `{"workspace_slug":"nope"}`, owner, http.StatusNotFound, "workspace not found"},
		{"bound elsewhere", `{"workspace_slug":"w-arch"}`, bound, http.StatusNotFound, "workspace not found"},
		{"archived is writable", `{"workspace_slug":"w-arch"}`, owner, http.StatusCreated, ""},
		{"api key of owner", `{"workspace_slug":"w"}`, key, http.StatusCreated, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			expect(t, env.do(t, http.MethodPost, sessions, c.body, c.auth), c.status, c.msg)
		})
	}
}

// Replaying an id that belongs to another workspace discloses the session
// only to a caller who may read that workspace.
func TestCreateSession_ReplayOfForeignSessionNeedsReadAccess(t *testing.T) {
	env := newEnv(t)
	env.seedSession(t, Session{ID: uuidN(7), WorkspaceSlug: "w"})
	body := `{"id":"` + uuidN(7) + `","workspace_slug":"w-own"}`
	if s := decode[Session](t, env.must(t, http.StatusOK, http.MethodPost, sessions, body, member)); s.WorkspaceSlug != "w" {
		t.Errorf("member replay returned %+v; want the w session", s)
	}
	env.seedSession(t, Session{ID: uuidN(8), WorkspaceSlug: "w-x"})
	expect(t, env.do(t, http.MethodPost, sessions, `{"id":"`+uuidN(8)+`","workspace_slug":"w"}`, owner), http.StatusNotFound, "workspace not found")
}

// POST /sessions/:id/complete ends an active session with a client-settable
// terminal status, decrements the gauge once and answers a terminal session
// idempotently.
func TestCompleteSession_EndsActiveSessionOnce(t *testing.T) {
	env := newEnv(t)
	for i, status := range []string{"completed", "failed", "timeout"} {
		env.must(t, http.StatusCreated, http.MethodPost, sessions, `{"id":"`+uuidN(i)+`","workspace_slug":"w"}`, owner)
		s := decode[Session](t, env.must(t, http.StatusOK, http.MethodPost, sessions+"/"+uuidN(i)+"/complete",
			`{"status":"`+status+`","error_message":"e","duration_ms":12}`, owner))
		if s.Status != status || s.EndedAt == nil || s.DurationMs == nil || *s.DurationMs != 12 || s.ErrorMessage != "e" {
			t.Errorf("%s: %+v", status, s)
		}
	}
	again := decode[Session](t, env.must(t, http.StatusOK, http.MethodPost, sessions+"/"+uuidN(0)+"/complete", `{"status":"failed"}`, owner))
	if again.Status != "completed" {
		t.Errorf("replayed completion changed the status to %s", again.Status)
	}
	if g := gauge(t, env.metrics.AgentSessionsActive, "w"); g != 0 {
		t.Errorf("active gauge %v after completing every session; want 0", g)
	}
	env.seedSession(t, Session{ID: "odd", WorkspaceSlug: "w", Status: "paused"})
	expect(t, env.do(t, http.MethodPost, sessions+"/odd/complete", `{"status":"completed"}`, owner), http.StatusConflict, "session is not active")
}

func TestCompleteSession_Refusals(t *testing.T) {
	env := newEnv(t)
	env.seedSession(t, Session{ID: "s1", WorkspaceSlug: "w"})
	env.seedSession(t, Session{ID: "sx", WorkspaceSlug: "w-x"})
	path := sessions + "/s1/complete"
	for _, c := range []struct {
		name, path, body string
		auth             *apikit.AuthInfo
		status           int
		msg              string
	}{
		{"unauthenticated", path, `{"status":"completed"}`, nil, http.StatusUnauthorized, "authentication required"},
		{"missing scope", path, `{"status":"completed"}`, testutil.PAT("u1", "sessions:read"), http.StatusForbidden, "sessions:write scope required"},
		{"invalid body", path, `{`, owner, http.StatusBadRequest, "invalid request body"},
		{"terminated not settable", path, `{"status":"terminated"}`, owner, http.StatusBadRequest, "terminated is not a valid client-settable status"},
		{"unknown status", path, `{"status":"done"}`, owner, http.StatusBadRequest, "invalid status"},
		{"unknown session", sessions + "/none/complete", `{"status":"completed"}`, owner, http.StatusNotFound, "session not found"},
		{"not the owner", path, `{"status":"completed"}`, member, http.StatusForbidden, "not session owner"},
		{"bound elsewhere", sessions + "/sx/complete", `{"status":"completed"}`, bound, http.StatusForbidden, "not session owner"},
		{"admin overrides", path, `{"status":"completed"}`, admin, http.StatusOK, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			expect(t, env.do(t, http.MethodPost, c.path, c.body, c.auth), c.status, c.msg)
		})
	}
}

// GET /sessions lists the sessions the caller may read with their token
// summaries, newest first, filtered and paged through the query parameters.
func TestListSessions_FiltersAndPages(t *testing.T) {
	env := newEnv(t)
	for i := 0; i < 3; i++ {
		env.seedSession(t, Session{ID: uuidN(i), WorkspaceSlug: "w", StartedAt: daysAgo(3 - i)})
	}
	env.seedSession(t, Session{ID: "done", WorkspaceSlug: "w", Status: "completed", StartedAt: daysAgo(10)})
	env.seedUsage(t, TokenUsage{ID: "u", SessionID: uuidN(2), WorkspaceSlug: "w", Model: "m", InputTokens: 5})
	first := decode[SessionListResponse](t, env.must(t, http.StatusOK, http.MethodGet, sessions+"?limit=2&status=active", owner))
	if len(first.Sessions) != 2 || !first.HasMore || first.NextCursor == nil || first.Sessions[0].ID != uuidN(2) || first.Sessions[0].TokenSummary.TotalInputTokens != 5 {
		t.Fatalf("first page: %+v", first)
	}
	second := decode[SessionListResponse](t, env.must(t, http.StatusOK, http.MethodGet, sessions+"?limit=2&status=active&cursor="+*first.NextCursor, owner))
	if len(second.Sessions) != 1 || second.HasMore || second.Sessions[0].ID != uuidN(0) {
		t.Errorf("second page: %+v", second)
	}
	if got := decode[SessionListResponse](t, env.must(t, http.StatusOK, http.MethodGet, sessions+"?since="+daysAgo(2), owner)); len(got.Sessions) != 2 {
		t.Errorf("since filter: %d sessions; want 2", len(got.Sessions))
	}
	if got := decode[SessionListResponse](t, env.must(t, http.StatusOK, http.MethodGet, sessions+"?order=asc&limit=abc", owner)); got.Sessions[0].ID != "done" {
		t.Errorf("ascending order lists %s first; want done", got.Sessions[0].ID)
	}
	expect(t, env.do(t, http.MethodGet, sessions+"?cursor=***", owner), http.StatusBadRequest, "invalid cursor")
	expect(t, env.do(t, http.MethodGet, sessions, nil), http.StatusUnauthorized, "authentication required")
	expect(t, env.do(t, http.MethodGet, sessions, testutil.PAT("u1", "audit:read")), http.StatusForbidden, "sessions:read scope required")
}

// Without a workspace filter a caller sees the sessions of the active
// workspaces they own or are a member of (a bound token at most its own);
// admin tokens see everything; a workspace filter the caller may not read,
// or an archived one, is 403.
func TestListSessions_ReadableWorkspaces(t *testing.T) {
	env := newEnv(t)
	for _, ws := range []string{"w", "w-arch", "w-own", "w-x"} {
		env.seedSession(t, Session{ID: "s-" + ws, WorkspaceSlug: ws})
	}
	seen := func(auth *apikit.AuthInfo, query string) map[string]bool {
		t.Helper()
		got := map[string]bool{}
		for _, s := range decode[SessionListResponse](t, env.must(t, http.StatusOK, http.MethodGet, sessions+query, auth)).Sessions {
			got[s.WorkspaceSlug] = true
		}
		return got
	}
	for name, c := range map[string]struct {
		auth *apikit.AuthInfo
		want []string
	}{
		"owner":    {owner, []string{"w"}},
		"member":   {member, []string{"w", "w-own"}},
		"stranger": {stranger, []string{"w-x"}},
		"bound":    {bound, []string{"w"}},
		"nobody":   {testutil.PAT("u9", "sessions:read"), nil},
		"admin":    {admin, []string{"w", "w-arch", "w-own", "w-x"}},
	} {
		got := seen(c.auth, "")
		if len(got) != len(c.want) {
			t.Errorf("%s sees %v; want %v", name, got, c.want)
		}
		for _, ws := range c.want {
			if !got[ws] {
				t.Errorf("%s does not see %s", name, ws)
			}
		}
	}
	if got := seen(member, "?workspace_slug=w"); !got["w"] {
		t.Errorf("member filtering on w sees %v", got)
	}
	for name, c := range map[string]struct {
		auth  *apikit.AuthInfo
		query string
	}{
		"stranger on w": {stranger, "?workspace_slug=w"},
		"archived":      {owner, "?workspace_slug=w-arch"},
		"bound on w-x":  {bound, "?workspace_slug=w-x"},
		"unknown":       {owner, "?workspace_slug=nope"},
	} {
		t.Run(name, func(t *testing.T) {
			expect(t, env.do(t, http.MethodGet, sessions+c.query, c.auth), http.StatusForbidden, "workspace access denied")
		})
	}
}

// GET /sessions/:id returns the session with its token summary to a reader
// of its active workspace; otherwise 404 for an unknown id and 403.
func TestGetSession_WithSummaryAndAccess(t *testing.T) {
	env := newEnv(t)
	env.seedSession(t, Session{ID: "s1", WorkspaceSlug: "w"})
	env.seedSession(t, Session{ID: "sa", WorkspaceSlug: "w-arch"})
	env.seedUsage(t, TokenUsage{ID: "u1", SessionID: "s1", WorkspaceSlug: "w", Model: "m2", InputTokens: 3})
	env.seedUsage(t, TokenUsage{ID: "u2", SessionID: "s1", WorkspaceSlug: "w", Model: "m1", OutputTokens: 4})
	s := decode[Session](t, env.must(t, http.StatusOK, http.MethodGet, sessions+"/s1", member))
	if s.TokenSummary == nil || s.TokenSummary.TotalInputTokens != 3 || s.TokenSummary.TotalOutputTokens != 4 || len(s.TokenSummary.ModelsUsed) != 2 || s.TokenSummary.ModelsUsed[0] != "m1" {
		t.Errorf("summary %+v", s.TokenSummary)
	}
	for _, c := range []struct {
		name, path string
		auth       *apikit.AuthInfo
		status     int
		msg        string
	}{
		{"unknown", sessions + "/none", owner, http.StatusNotFound, "session not found"},
		{"stranger", sessions + "/s1", stranger, http.StatusForbidden, "workspace access denied"},
		{"archived", sessions + "/sa", owner, http.StatusForbidden, "workspace access denied"},
		{"admin on archived", sessions + "/sa", admin, http.StatusOK, ""},
		{"no scope", sessions + "/s1", testutil.PAT("u1"), http.StatusForbidden, "sessions:read scope required"},
		{"unauthenticated", sessions + "/s1", nil, http.StatusUnauthorized, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			expect(t, env.do(t, http.MethodGet, c.path, "", c.auth), c.status, c.msg)
		})
	}
}

// Without SQLite the session routes skip the workspace checks: any caller
// with the scope opens, lists and reads sessions anywhere.
func TestSessionRoutes_WithoutSQLiteSkipWorkspaceChecks(t *testing.T) {
	env := newBareEnv(t)
	s := decode[Session](t, env.must(t, http.StatusCreated, http.MethodPost, sessions, `{"workspace_slug":"anything"}`, stranger))
	env.must(t, http.StatusOK, http.MethodGet, sessions+"/"+s.ID, member)
	env.must(t, http.StatusOK, http.MethodGet, sessions+"/"+s.ID+"/usage", member)
	if got := decode[SessionListResponse](t, env.must(t, http.StatusOK, http.MethodGet, sessions+"?workspace_slug=anything", member)); len(got.Sessions) != 1 {
		t.Errorf("list without SQLite: %+v", got)
	}
	env.must(t, http.StatusOK, http.MethodPost, sessions+"/"+s.ID+"/complete", `{"status":"completed"}`, stranger)
	expect(t, env.do(t, http.MethodGet, "/api/v1/workspaces/anything/cost", "", stranger), http.StatusForbidden, "workspace access denied")
	env.must(t, http.StatusOK, http.MethodGet, "/api/v1/workspaces/anything/cost", "", admin)
}

// A failed lookup in SQLite is 500 on the session writes and 403 on the
// reads, never access.
func TestSessionRoutes_DatabaseFailure(t *testing.T) {
	env := newEnv(t)
	env.seedSession(t, Session{ID: "s1", WorkspaceSlug: "w"})
	testutil.Exec(t, env.sqlite, "DROP TABLE token_workspace_bindings")
	expect(t, env.do(t, http.MethodPost, sessions, `{"workspace_slug":"w"}`, bound), http.StatusInternalServerError, "internal server error")
	expect(t, env.do(t, http.MethodPost, sessions+"/s1/complete", `{"status":"completed"}`, bound), http.StatusInternalServerError, "internal server error")
	expect(t, env.do(t, http.MethodPost, sessions+"/s1/usage", `{"model":"m"}`, bound), http.StatusInternalServerError, "internal server error")
	expect(t, env.do(t, http.MethodGet, sessions+"/s1", "", bound), http.StatusForbidden, "workspace access denied")
	expect(t, env.do(t, http.MethodGet, sessions, "", bound), http.StatusInternalServerError, "internal server error")
}
