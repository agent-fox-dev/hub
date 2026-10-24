package audit

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// NewSSEManager keeps the connection limit given, falling back to the
// default for a non-positive one, and refuses registrations past it;
// unregistering closes the connection's channel.
func TestSSEManager_ConnectionLimit(t *testing.T) {
	if m := NewSSEManager(0); m.maxConns != DefaultMaxConnections {
		t.Errorf("maxConns %d for 0; want %d", m.maxConns, DefaultMaxConnections)
	}
	if m := NewSSEManager(-5); m.maxConns != DefaultMaxConnections {
		t.Errorf("maxConns %d for -5; want %d", m.maxConns, DefaultMaxConnections)
	}
	m := NewSSEManager(2)
	a, err := m.Register(sseFilters{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Register(sseFilters{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Register(sseFilters{}); err == nil || err.Error() != "too many SSE connections" {
		t.Errorf("third registration: %v; want too many SSE connections", err)
	}
	m.Unregister(a.id)
	if _, ok := <-a.ch; ok {
		t.Error("channel still open after Unregister")
	}
	m.Unregister(a.id)
	if m.ConnCount() != 1 {
		t.Errorf("%d connections; want 1", m.ConnCount())
	}
}

// Broadcast fans an event out to the connections whose filters it passes;
// a full per-client buffer drops its oldest event, a full broadcast channel
// drops the event.
func TestSSEManager_FanOutAndBackpressure(t *testing.T) {
	m := NewSSEManager(10)
	done := make(chan struct{})
	defer close(done)
	go m.Run(done)
	all, _ := m.Register(sseFilters{})
	hubOnly, _ := m.Register(sseFilters{category: "hub"})
	wsW, _ := m.Register(sseFilters{workspace: "w"})
	m.Broadcast(HubEvent{ID: "e1", EventType: "agent.thing", Workspace: "w"})
	m.Broadcast(HubEvent{ID: "e2", EventType: "hub.thing", Workspace: "x"})
	m.Broadcast(HubEvent{ID: "end", EventType: "hub.end", Workspace: "w"})
	drain := func(ch chan HubEvent) []string {
		var got []string
		for {
			select {
			case ev := <-ch:
				if ev.ID == "end" {
					return got
				}
				got = append(got, ev.ID)
			case <-time.After(3 * time.Second):
				t.Fatalf("no end event; got %v", got)
			}
		}
	}
	if got := drain(all.ch); jsonOf(got) != `["e1","e2"]` {
		t.Errorf("unfiltered connection got %v", got)
	}
	if got := drain(hubOnly.ch); jsonOf(got) != `["e2"]` {
		t.Errorf("hub-only connection got %v", got)
	}
	if got := drain(wsW.ch); jsonOf(got) != `["e1"]` {
		t.Errorf("workspace w connection got %v", got)
	}

	full, _ := m.Register(sseFilters{})
	for i := 0; i < PerClientBufferSize+1; i++ {
		m.Broadcast(HubEvent{ID: uuidN(i), EventType: "hub.x"})
	}
	waitFor(t, "the per-client buffer to fill", func() bool { return len(full.ch) == PerClientBufferSize })
	if first := <-full.ch; first.ID != uuidN(1) {
		t.Errorf("oldest buffered event is %s; want %s after dropping %s", first.ID, uuidN(1), uuidN(0))
	}

	idle := NewSSEManager(1)
	for i := 0; i < broadcastChanSize+5; i++ {
		idle.Broadcast(HubEvent{ID: uuidN(i)})
	}
	if len(idle.broadcast) != broadcastChanSize {
		t.Errorf("broadcast channel holds %d; want %d with the rest dropped", len(idle.broadcast), broadcastChanSize)
	}
}

// matchesFilters applies the workspace, the readable set and the category;
// run_id is not applied; heartbeats pass the hub category.
func TestSSEManager_MatchesFilters(t *testing.T) {
	readable := map[string]bool{"w": true}
	for name, c := range map[string]struct {
		ev   HubEvent
		f    sseFilters
		want bool
	}{
		"no filter":                {HubEvent{EventType: "hub.x", Workspace: "w"}, sseFilters{}, true},
		"workspace match":          {HubEvent{EventType: "hub.x", Workspace: "w"}, sseFilters{workspace: "w"}, true},
		"workspace mismatch":       {HubEvent{EventType: "hub.x", Workspace: "x"}, sseFilters{workspace: "w"}, false},
		"readable":                 {HubEvent{EventType: "hub.x", Workspace: "w"}, sseFilters{allowedWorkspaces: readable}, true},
		"not readable":             {HubEvent{EventType: "hub.x", Workspace: "x"}, sseFilters{allowedWorkspaces: readable}, false},
		"workspaceless":            {HubEvent{EventType: "hub.user.x"}, sseFilters{allowedWorkspaces: readable}, false},
		"workspaceless to admin":   {HubEvent{EventType: "hub.user.x"}, sseFilters{}, true},
		"hub category":             {HubEvent{EventType: "hub.x"}, sseFilters{category: "hub"}, true},
		"hub category drops agent": {HubEvent{EventType: "session.start"}, sseFilters{category: "hub"}, false},
		"hub category heartbeat":   {heartbeatEvent(), sseFilters{category: "hub"}, true},
		"agent category":           {HubEvent{EventType: "session.start"}, sseFilters{category: "agent"}, true},
		"agent category drops hub": {HubEvent{EventType: "hub.x"}, sseFilters{category: "agent"}, false},
		"run_id ignored":           {HubEvent{EventType: "hub.x"}, sseFilters{runID: "r"}, true},
	} {
		if got := matchesFilters(c.ev, c.f); got != c.want {
			t.Errorf("%s: %v; want %v", name, got, c.want)
		}
	}
}

// The heartbeat loop ticks on the injected ticker, sends a heartbeat to
// every connection and reaps those that have not read for longer than the
// stale timeout; TouchLastRead keeps a connection alive.
func TestSSEManager_HeartbeatAndStaleReaping(t *testing.T) {
	m := NewSSEManager(10, WithTickerFactory(func(time.Duration) *time.Ticker { return time.NewTicker(time.Millisecond) }), WithTickerFactory(nil))
	done := make(chan struct{})
	defer close(done)
	go m.Run(done)
	live, _ := m.Register(sseFilters{})
	stale, _ := m.Register(sseFilters{})
	select {
	case ev := <-live.ch:
		if ev.EventType != "heartbeat" || ev.Timestamp == "" {
			t.Errorf("first event %+v; want a heartbeat", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no heartbeat")
	}
	m.mu.Lock()
	stale.lastRead = time.Now().Add(-2 * StaleConnectionTimeout)
	m.mu.Unlock()
	m.TouchLastRead(live.id)
	waitFor(t, "the stale connection to be reaped", func() bool { return m.ConnCount() == 1 })
	m.mu.Lock()
	_, liveKept := m.connections[live.id]
	m.mu.Unlock()
	if !liveKept {
		t.Error("the live connection was reaped")
	}
	if HeartbeatInterval != 30*time.Second || StaleConnectionTimeout != 60*time.Second {
		t.Errorf("heartbeat %v, stale timeout %v; want 30s and 60s", HeartbeatInterval, StaleConnectionTimeout)
	}
}

// A panic while fanning out (a connection whose channel was closed under
// the broadcaster) is recovered and the broadcaster keeps delivering.
func TestSSEManager_BroadcasterSurvivesPanic(t *testing.T) {
	m := NewSSEManager(10)
	done := make(chan struct{})
	defer close(done)
	go m.Run(done)
	broken, _ := m.Register(sseFilters{})
	close(broken.ch)
	m.Broadcast(HubEvent{ID: "boom", EventType: "hub.x"})
	waitFor(t, "the broadcaster to take the event", func() bool { return len(m.broadcast) == 0 })
	m.mu.Lock()
	delete(m.connections, broken.id)
	m.mu.Unlock()
	fresh, _ := m.Register(sseFilters{})
	m.Broadcast(HubEvent{ID: "after", EventType: "hub.x"})
	select {
	case ev := <-fresh.ch:
		if ev.ID != "after" {
			t.Errorf("got %s; want after", ev.ID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("broadcaster did not recover")
	}
}

// GET /events streams text/event-stream frames: a heartbeat first, then an
// audit_event frame per event of the caller's filters, to every connected
// client; the connection is unregistered when the client leaves.
func TestSSEStream_DeliversFramesToEveryClient(t *testing.T) {
	env := newEnv(t)
	ts := env.serve(t)
	a := env.connect(t, ts, "", admin, 1)
	b := env.connect(t, ts, "?workspace=w&category=hub&run_id=r", admin, 2)
	if ct := a.resp.Header.Get("Content-Type"); ct != "text/event-stream" || a.resp.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("headers: Content-Type %q, Cache-Control %q", ct, a.resp.Header.Get("Cache-Control"))
	}
	hb := a.next()
	var hbData map[string]string
	if hb.event != "heartbeat" || json.Unmarshal([]byte(hb.data), &hbData) != nil || hbData["timestamp"] == "" {
		t.Errorf("first frame %+v; want a heartbeat with a timestamp", hb)
	}
	env.mgr.Broadcast(HubEvent{ID: "agent", EventType: "session.start", Workspace: "w"})
	env.mgr.Broadcast(liveEvent("other", "w-x"))
	env.mgr.Broadcast(heartbeatEvent())
	env.mgr.Broadcast(liveEvent("end", "w"))
	if got := a.receivedUntil(t, "end"); len(got) != 2 || !got["agent"] || !got["other"] {
		t.Errorf("unfiltered client received %v", got)
	}
	if got := b.receivedUntil(t, "end"); len(got) != 0 {
		t.Errorf("filtered client received %v; want nothing but end", got)
	}
	a.resp.Body.Close()
	waitFor(t, "the closed client to be unregistered", func() bool { return env.mgr.ConnCount() == 1 })
}

// A full manager answers 503; the scope and the readable set are checked
// before any stream header, so a workspace filter outside the set is 404,
// a failed lookup 500, and neither registers a connection.
func TestSSEStream_Refusals(t *testing.T) {
	env := newEnv(t)
	ts := env.serve(t)
	for _, c := range []struct {
		name, query string
		auth        *apikit.AuthInfo
		status      int
		msg         string
	}{
		{"unauthenticated", "", nil, 401, "authentication required"},
		{"missing scope", "", testutil.PAT("u2", "sessions:read"), 403, "insufficient scope: audit:read required"},
		{"filter outside set", "?workspace=w-x", member, 404, "workspace not found"},
		{"bound elsewhere", "?workspace=w-arch", bound, 404, "workspace not found"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := env.refusedStream(t, c.query, c.auth)
			expect(t, rec, c.status, c.msg)
			if strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") || env.mgr.ConnCount() != 0 {
				t.Errorf("a stream was opened: %q, %d connections", rec.Header().Get("Content-Type"), env.mgr.ConnCount())
			}
		})
	}
	for i := 1; i <= 3; i++ {
		env.connect(t, ts, "", admin, i)
	}
	full := openStream(t, ts, "", admin)
	var body map[string]string
	if full.resp.StatusCode != 503 || json.NewDecoder(full.resp.Body).Decode(&body) != nil || body["error"] != "too many SSE connections" {
		t.Errorf("fourth connection: %d %v; want 503 too many SSE connections", full.resp.StatusCode, body)
	}
	testutil.Exec(t, env.sqlite, "DROP TABLE org_members")
	rec := env.refusedStream(t, "", member)
	expect(t, rec, 500, "internal server error")
}

// A member's stream carries the events of owned and member workspaces only,
// as the set stood when the stream opened; events without a workspace reach
// admin streams only.
func TestSSEStream_ReadableSetFixedAtConnect(t *testing.T) {
	env := newEnv(t)
	ts := env.serve(t)
	mem := env.connect(t, ts, "", member, 1)
	adm := env.connect(t, ts, "", admin, 2)
	seedWS(t, env.sqlite, "w-new", "u1", "N", "active")
	testutil.Exec(t, env.sqlite, "DELETE FROM org_members WHERE org_id = 'O' AND user_id = 'u2'")
	testutil.SeedOrg(t, env.sqlite, "N", "n", "u1")
	testutil.SeedOrgMember(t, env.sqlite, "N", "u2")
	for _, ev := range []HubEvent{liveEvent("team", "w"), liveEvent("arch", "w-arch"), liveEvent("own", "w-own"), liveEvent("x", "w-x"),
		liveEvent("new", "w-new"), {ID: "none", EventType: "hub.user.updated", Timestamp: at(1)}, liveEvent("end", "w")} {
		env.mgr.Broadcast(ev)
	}
	if got := mem.receivedUntil(t, "end"); jsonOf(got) != `{"arch":true,"own":true,"team":true}` {
		t.Errorf("member received %v; want team, arch and own", got)
	}
	if got := adm.receivedUntil(t, "end"); len(got) != 6 || !got["none"] {
		t.Errorf("admin received %v; want everything", got)
	}
}

// Without a concrete SSE manager the route writes the stream headers and an
// initial keep-alive and heartbeat, then returns.
func TestSSEStream_WithoutManagerAnswersOnce(t *testing.T) {
	duck := openAuditDB(t)
	e, api := testutil.API()
	RegisterAuditQueryRoutes(api, NewStore(duck), struct{ SSEBroadcaster }{}, nil)
	rec := testutil.Request(t, e, http.MethodGet, "/api/v1/events", "", admin)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/event-stream" || !strings.HasPrefix(rec.Body.String(), ": keep-alive\n\n") || !strings.Contains(rec.Body.String(), "event: heartbeat\n") {
		t.Errorf("(%d, %q, %q)", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}
