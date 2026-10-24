package audit

import (
	"net/http"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

const auditPath = "/api/v1/audit"

// hub seeds a hub event of workspace ws by actor at ts (minute i).
func (env *env) hub(t *testing.T, id, ws, eventType, actor, actorType, resourceType, action string, i int) {
	t.Helper()
	env.seedHubEvent(t, HubEventRow{ID: id, EventType: eventType, ActorID: actor, ActorType: actorType, ResourceType: resourceType,
		ResourceID: "r", Action: action, Workspace: ws, Timestamp: at(i), IngestedAt: at(i)})
}

// GET /audit merges hub and agent events newest first, with the fields of
// the other source null, filters on every parameter (hub-only filters drop
// agent events and run_id drops hub events) and pages by cursor with the
// limit clamped to 1000.
func TestAuditQuery_MergesSourcesFiltersAndPages(t *testing.T) {
	env := newEnv(t)
	env.hub(t, "h0", "w", "hub.workspace.updated", "u1", "pat", "workspace", "update", 0)
	env.hub(t, "h2", "w", "hub.token.bind", "u2", "api_key", "token", "bind", 2)
	env.hub(t, "h4", "w-own", "hub.workspace.updated", "u1", "pat", "workspace", "update", 4)
	env.seedAgentEvent(t, agentEvent{id: "a1", ts: at(1), eventType: "session.start", nodeID: "n1", sessionID: "s1"})
	env.seedAgentEvent(t, agentEvent{id: "a3", ts: at(3), eventType: "session.fail", severity: "error"})
	env.seedAgentEvent(t, agentEvent{id: "a5", ts: at(5), ws: "w-own", run: "20260704_143022_ffffff"})
	get := func(query string, auth *apikit.AuthInfo) []UnifiedAuditEvent {
		t.Helper()
		return decode[unifiedQueryResponse](t, env.must(t, http.StatusOK, http.MethodGet, auditPath+query, "", auth)).Events
	}
	all := get("", admin)
	if len(all) != 6 || all[0].ID != "a5" || all[5].ID != "h0" {
		t.Fatalf("merged listing: %v", eventIDs(all))
	}
	h := all[3]
	if h.Source != "hub" || h.ActorID == nil || *h.ActorID != "u2" || *h.ActorType != "api_key" || *h.ResourceType != "token" || *h.ResourceID != "r" || *h.Action != "bind" || h.RunID != nil || h.NodeID != nil || h.Severity != "info" || h.Timestamp != at(2) {
		t.Errorf("hub event %+v", h)
	}
	a := all[4]
	if a.Source != "agent" || a.RunID == nil || *a.RunID != runID || *a.NodeID != "n1" || *a.SessionID != "s1" || a.Archetype == nil || a.ActorID != nil || a.Action != nil || a.Workspace != "w" {
		t.Errorf("agent event %+v", a)
	}
	for query, want := range map[string][]string{
		"?source=hub":                         {"h4", "h2", "h0"},
		"?source=agent":                       {"a5", "a3", "a1"},
		"?workspace=w-own":                    {"a5", "h4"},
		"?event_type=session.fail":            {"a3"},
		"?event_type_prefix=hub.token":        {"h2"},
		"?severity=error":                     {"a3"},
		"?actor_id=u2":                        {"h2"},
		"?actor_type=api_key":                 {"h2"},
		"?resource_type=token":                {"h2"},
		"?action=bind":                        {"h2"},
		"?run_id=" + runID:                    {"a3", "a1"},
		"?since=" + at(3):                     {"a5", "h4", "a3"},
		"?until=" + at(2):                     {"a1", "h0"},
		"?since=" + at(1) + "&until=" + at(3): {"h2", "a1"},
		"?source=hub&run_id=" + runID:         {},
		"?source=agent&actor_id=u1":           {},
		"?limit=0":                            {"a5", "h4", "a3", "h2", "a1", "h0"},
		"?limit=99999":                        {"a5", "h4", "a3", "h2", "a1", "h0"},
	} {
		if got := eventIDs(get(query, admin)); jsonOf(got) != jsonOf(want) {
			t.Errorf("%s: %v; want %v", query, got, want)
		}
	}
	first := decode[unifiedQueryResponse](t, env.must(t, http.StatusOK, http.MethodGet, auditPath+"?limit=4", "", admin))
	if len(first.Events) != 4 || !first.HasMore || first.NextCursor == nil {
		t.Fatalf("first page: %+v", first)
	}
	second := decode[unifiedQueryResponse](t, env.must(t, http.StatusOK, http.MethodGet, auditPath+"?limit=4&cursor="+*first.NextCursor, "", admin))
	if got := eventIDs(second.Events); jsonOf(got) != `["a1","h0"]` || second.HasMore || second.NextCursor != nil {
		t.Errorf("second page: %v has_more %v", got, second.HasMore)
	}
}

func eventIDs(events []UnifiedAuditEvent) []string {
	out := []string{}
	for _, e := range events {
		out = append(out, e.ID)
	}
	return out
}

// Hub events written without a timestamp sort by their ingestion time, so
// they interleave with agent events.
func TestAuditQuery_LegacyHubEventsSortByIngestion(t *testing.T) {
	env := newEnv(t)
	testutil.Exec(t, env.duck, `INSERT INTO hub_audit_events (id, event_type, timestamp, ingested_at) VALUES ('legacy', 'hub.x', '', ?)`, at(2))
	env.seedAgentEvent(t, agentEvent{id: "a1", ts: at(1)})
	env.seedAgentEvent(t, agentEvent{id: "a3", ts: at(3)})
	got := decode[unifiedQueryResponse](t, env.must(t, http.StatusOK, http.MethodGet, auditPath, "", admin)).Events
	if ids := eventIDs(got); jsonOf(ids) != `["a3","legacy","a1"]` || got[1].Timestamp != at(2) {
		t.Errorf("ordering %v, legacy timestamp %s", ids, got[1].Timestamp)
	}
}

func TestAuditQuery_InvalidParameters(t *testing.T) {
	env := newEnv(t)
	for _, c := range []struct {
		name, query string
		auth        *apikit.AuthInfo
		status      int
		msg         string
	}{
		{"unauthenticated", "", nil, 401, "authentication required"},
		{"missing scope", "", testutil.PAT("u1", "sessions:read"), 403, "insufficient scope: audit:read required"},
		{"malformed cursor", "?cursor=***", owner, 400, "invalid cursor"},
		{"cursor without time", "?cursor=" + encodeCursor("x", "y"), owner, 400, "invalid cursor"},
		{"bad since", "?since=yesterday", owner, 400, "invalid query parameter: since/until must be RFC 3339"},
		{"bad until", "?until=tomorrow", owner, 400, "invalid query parameter: since/until must be RFC 3339"},
		{"since after until", "?since=" + at(2) + "&until=" + at(1), owner, 400, "since must be before until"},
	} {
		t.Run(c.name, func(t *testing.T) {
			expect(t, env.do(t, http.MethodGet, auditPath+c.query, "", c.auth), c.status, c.msg)
		})
	}
	testutil.Exec(t, env.duck, "DROP TABLE hub_audit_events")
	expect(t, env.do(t, http.MethodGet, auditPath, "", admin), 500, "internal server error")
}

// A non-admin caller sees the events of the workspaces they may read
// (owned and member ones of any status; for a bound token only its own),
// never events without a workspace; a workspace filter outside that set is
// 404; admin tokens see everything; a failed lookup is 500.
func TestAuditQuery_ReadableWorkspaces(t *testing.T) {
	env := newEnv(t)
	for i, ws := range []string{"w", "w-arch", "w-own", "w-x"} {
		env.hub(t, "h-"+ws, ws, "hub.workspace.updated", "u1", "pat", "workspace", "update", i)
		env.seedAgentEvent(t, agentEvent{id: "a-" + ws, ws: ws, ts: at(i)})
	}
	env.hub(t, "h-none", "", "hub.user.updated", "u1", "pat", "user", "update", 9)
	seen := func(auth *apikit.AuthInfo, query string) map[string]int {
		t.Helper()
		got := map[string]int{}
		for _, e := range decode[unifiedQueryResponse](t, env.must(t, http.StatusOK, http.MethodGet, auditPath+query, "", auth)).Events {
			got[e.Workspace]++
		}
		return got
	}
	for name, c := range map[string]struct {
		auth *apikit.AuthInfo
		want map[string]int
	}{
		"owner":    {owner, map[string]int{"w": 2, "w-arch": 2}},
		"member":   {member, map[string]int{"w": 2, "w-arch": 2, "w-own": 2}},
		"stranger": {stranger, map[string]int{"w-x": 2}},
		"bound":    {bound, map[string]int{"w": 2}},
		"nobody":   {testutil.PAT("u9", "audit:read"), map[string]int{}},
		"admin":    {admin, map[string]int{"w": 2, "w-arch": 2, "w-own": 2, "w-x": 2, "": 1}},
	} {
		if got := seen(c.auth, ""); jsonOf(got) != jsonOf(c.want) {
			t.Errorf("%s sees %v; want %v", name, got, c.want)
		}
	}
	if got := seen(member, "?workspace=w-own"); len(got) != 1 || got["w-own"] != 2 {
		t.Errorf("member filtering on w-own sees %v", got)
	}
	for name, c := range map[string]struct {
		auth  *apikit.AuthInfo
		query string
	}{
		"stranger on w":   {stranger, "?workspace=w"},
		"bound on w-arch": {bound, "?workspace=w-arch"},
		"unknown":         {owner, "?workspace=nope"},
	} {
		t.Run(name, func(t *testing.T) {
			expect(t, env.do(t, http.MethodGet, auditPath+c.query, "", c.auth), http.StatusNotFound, "workspace not found")
		})
	}
	testutil.Exec(t, env.sqlite, "DROP TABLE org_members")
	expect(t, env.do(t, http.MethodGet, auditPath, "", member), http.StatusInternalServerError, "internal server error")
}

// Without SQLite the unified query is unrestricted; the route registration
// refuses a nil store or broadcaster.
func TestAuditQuery_RegistrationAndNoSQLite(t *testing.T) {
	env := newBareEnv(t)
	env.hub(t, "h", "anything", "hub.x", "u", "pat", "x", "x", 1)
	if got := decode[unifiedQueryResponse](t, env.must(t, http.StatusOK, http.MethodGet, auditPath, "", stranger)).Events; len(got) != 1 {
		t.Errorf("without SQLite a stranger sees %v", eventIDs(got))
	}
	for name, register := range map[string]func(){
		"nil store":       func() { _, api := testutil.API(); RegisterAuditQueryRoutes(api, nil, env.mgr, nil) },
		"nil broadcaster": func() { _, api := testutil.API(); RegisterAuditQueryRoutes(api, env.store, nil, nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("registering with a %s did not panic", name)
				}
			}()
			register()
		}()
	}
}
