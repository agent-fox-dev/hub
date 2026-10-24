package forge

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// sseEvents reads an event stream and sends the event types of the audit events it carries.
func sseEvents(t *testing.T, body io.Reader) (heartbeat <-chan struct{}, events <-chan string) {
	t.Helper()
	hb, ev := make(chan struct{}, 1), make(chan string, 16)
	go func() {
		scanner := bufio.NewScanner(body)
		var name string
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				switch name {
				case "heartbeat":
					select {
					case hb <- struct{}{}:
					default:
					}
				case "audit_event":
					var doc struct {
						EventType string `json:"event_type"`
					}
					if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &doc) == nil {
						ev <- doc.EventType
					}
				}
			}
		}
	}()
	return hb, ev
}

// relayHub is the routes and the ingress over a real DuckDB audit store, SSE manager and broadcast
// emitter, served over HTTP.
func newRelayHub(t *testing.T) *httptest.Server {
	t.Helper()
	auditDB, err := audit.OpenDB(filepath.Join(t.TempDir(), "audit.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { auditDB.Close() })
	if err := audit.InitSchema(auditDB); err != nil {
		t.Fatal(err)
	}
	store := audit.NewStore(auditDB)
	sse := audit.NewSSEManager(5)
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go sse.Run(done)

	e := newEnv(t, func(cfg *Config) { cfg.Audit = audit.NewEmitter(store, sse) })
	e.webhookWorkspace("widgets")
	api := e.echo.Group("/api/v1")
	api.Use(testAuthMiddleware())
	audit.RegisterAuditQueryRoutes(api, store, sse, e.db)
	server := httptest.NewServer(e.echo)
	t.Cleanup(server.Close)
	return server
}

// ownerHeader is the credential of the owner of the workspace widgets, for the test middleware.
func ownerHeader() string {
	auth, _ := json.Marshal(apiKeyAuth())
	return string(auth)
}

// openEventStream opens GET /events?workspace=widgets as the owner and returns the types of the
// audit events it carries, once it has sent its first heartbeat.
func openEventStream(t *testing.T, server *httptest.Server) <-chan string {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/events?workspace=widgets", nil)
	req.Header.Set("X-Test-Auth", ownerHeader())
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stream.Body.Close() })
	if stream.StatusCode != http.StatusOK {
		t.Fatalf("the stream: status = %d; want 200", stream.StatusCode)
	}
	heartbeat, events := sseEvents(t, stream.Body)
	select {
	case <-heartbeat:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream did not open")
	}
	return events
}

// auditTypes asks GET /audit?workspace=widgets for the event types, oldest first (the query lists
// the newest first), and checks that every event is a hub event of the workspace.
func auditTypes(t *testing.T, server *httptest.Server) []string {
	t.Helper()
	query, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/audit?workspace=widgets", nil)
	query.Header.Set("X-Test-Auth", ownerHeader())
	res, err := http.DefaultClient.Do(query)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var doc struct {
		Events []struct {
			EventType string `json:"event_type"`
			Workspace string `json:"workspace"`
			Source    string `json:"source"`
		} `json:"events"`
	}
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	var types []string
	for i := len(doc.Events) - 1; i >= 0; i-- {
		if doc.Events[i].Workspace != "widgets" || doc.Events[i].Source != "hub" {
			t.Fatalf("an event of another workspace or source: %+v", doc.Events[i])
		}
		types = append(types, doc.Events[i].EventType)
	}
	return types
}

// TS-32-48: The events of a delivery are stored in the order they were made, and appear on the SSE
// stream and in the audit query, though the request that made them is over.
// Requirement: 32-REQ-8.6
func TestRelayReachesTheStreamAndTheQuery_TS_32_48(t *testing.T) {
	server := newRelayHub(t)
	events := openEventStream(t, server)

	// A GitLab update that takes a label off and puts another on; the client goes away at once,
	// which cancels the request's context while the events are still being stored.
	d := gitlabDelivery(ingressSecret, "d-1", "Issue Hook", gitlabUpdateHook(map[string]any{"labels": labelChange([]string{"b"}, []string{"a"})}, "a"))
	post, _ := http.NewRequest(http.MethodPost, server.URL+"/hooks/forge/widgets", strings.NewReader(d.body))
	post.Header = d.header.Clone()
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Do(post)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted || strings.TrimSpace(string(body)) != `{"outcome":"relayed"}` {
		t.Fatalf("the delivery: %d %s; want 202 relayed", resp.StatusCode, body)
	}

	want := []string{audit.EventForgeIssueUnlabeled, audit.EventForgeIssueLabeled}
	var got []string
	for len(got) < len(want) {
		select {
		case typ := <-events:
			got = append(got, typ)
		case <-time.After(5 * time.Second):
			t.Fatalf("the stream carried %v; want %v", got, want)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the stream carried %v; want %v in this order", got, want)
	}

	// The events are stored after the answer, so wait for them.
	var stored []string
	eventually(t, "both events in the audit query", func() bool {
		stored = auditTypes(t, server)
		return len(stored) >= len(want)
	})
	if !reflect.DeepEqual(stored, want) {
		t.Errorf("the audit query returned %v; want %v in the order they were made", stored, want)
	}
}

// TS-32-49, with a real delivery: an update that makes three events, the first of which the
// emitter fails, still offers all three and answers as it would.
// Requirement: 32-REQ-8.7
func TestEmitErrorWithAnUpdateOfThreeEvents(t *testing.T) {
	logs := captureLog(t)
	em := &failFirstEmitter{}
	e := newEnv(t, func(cfg *Config) { cfg.Audit = em })
	e.webhookWorkspace("widgets")
	body := gitlabUpdateHook(map[string]any{
		"title":  map[string]any{"previous": "a", "current": "b"},
		"labels": labelChange([]string{"x"}, []string{"y"}),
	}, "y")
	if outcome := outcomeOf(t, e.deliver("widgets", gitlabDelivery(ingressSecret, "d-1", "Issue Hook", body))); outcome != "relayed" {
		t.Fatalf("outcome = %q; want relayed", outcome)
	}
	want := []string{audit.EventForgeIssueEdited, audit.EventForgeIssueUnlabeled, audit.EventForgeIssueLabeled}
	if got := em.attempted(); !reflect.DeepEqual(got, want) {
		t.Errorf("offered %v; want %v", got, want)
	}
	if !strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("the log = %q; want an error line", logs)
	}
	if rows := e.deliveryRows(); len(rows) != 1 || rows[0].outcome != "relayed" {
		t.Errorf("rows = %+v; want one relayed row", rows)
	}
}
