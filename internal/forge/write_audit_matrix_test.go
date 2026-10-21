package forge

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// eventExpectation is what a route's event holds when it is called with n = 42.
type eventExpectation struct {
	route      string
	eventType  string
	resource   string
	resourceID string
	action     string
}

var eventTable = []eventExpectation{
	{"create issue", audit.EventForgeIssueCreate, "issue", "7", "create"},
	{"update issue", audit.EventForgeIssueUpdate, "issue", "42", "update"},
	{"close issue", audit.EventForgeIssueClose, "issue", "42", "close"},
	{"comment on issue", audit.EventForgeCommentCreate, "comment", "42", "create"},
	{"add labels", audit.EventForgeLabelAdd, "label", "42", "add"},
	{"remove label", audit.EventForgeLabelRemove, "label", "42", "remove"},
	{"ensure label", audit.EventForgeLabelCreate, "label", "af:triage", "create"},
	{"open pull request", audit.EventForgePullRequestCreate, "pull_request", "9", "create"},
	{"comment on pull request", audit.EventForgePullRequestComment, "pull_request", "42", "comment"},
}

// orderWriter is a response writer that notes how many events were recorded when the first
// byte of the answer was written.
type orderWriter struct {
	http.ResponseWriter
	events func() int
	seen   *int
}

func (o orderWriter) WriteHeader(status int) {
	if *o.seen < 0 {
		*o.seen = o.events()
	}
	o.ResponseWriter.WriteHeader(status)
}

func (o orderWriter) Write(b []byte) (int, error) {
	if *o.seen < 0 {
		*o.seen = o.events()
	}
	return o.ResponseWriter.Write(b)
}

// TS-31-48: A write that reached the forge records exactly one event, a success, a forge
// failure and a close with a comment alike, with the caller as actor and the slug as
// workspace, and the event is already recorded when the answer is written.
// Requirement: 31-REQ-9.1
func TestOneEventPerWrite_TS_31_48(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	caller := &apikit.AuthInfo{UserID: "user-4242", CredentialType: "api_key"}
	addWorkspace(t, e.db, "mine", func(r *workspaceRow) { r.owner = "user-4242" })
	e.setVar("mine", "FORGE_API_URL", e.stub.URL)

	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		method  string
		path    string
		body    string
		want    string
	}{
		{"a success", githubWrites(), http.MethodPost, "/workspaces/mine/issues", `{"title":"t"}`, audit.EventForgeIssueCreate},
		{"a forge failure", respond(http.StatusForbidden, `{"message":"no"}`), http.MethodPost, "/workspaces/mine/issues", `{"title":"t"}`, audit.EventForgeIssueCreate},
		{"a close with a comment", githubWrites(), http.MethodPost, "/workspaces/mine/issues/42/close", `{"comment":"done"}`, audit.EventForgeIssueClose},
		{"a label lookup, create and lookup", githubWrites(), http.MethodPost, "/workspaces/mine/labels", `{"name":"af:triage"}`, audit.EventForgeLabelCreate},
		{"a removal and a read of the issue", respond(http.StatusNotFound, `{}`), http.MethodDelete, "/workspaces/mine/issues/42/labels/af:fix", ``, audit.EventForgeLabelRemove},
	} {
		e.stub.setHandler(tc.handler)
		before := len(em.recorded())
		requestsBefore := e.stub.count()

		req := httptest.NewRequest(tc.method, "/api/v1"+tc.path, strings.NewReader(tc.body))
		req.Header.Set("X-Test-Auth", authHeader(t, caller))
		rec := httptest.NewRecorder()
		seen := -1
		e.echo.ServeHTTP(orderWriter{ResponseWriter: rec, events: func() int { return len(em.recorded()) - before }, seen: &seen}, req)

		events := em.recorded()[before:]
		if len(events) != 1 {
			t.Errorf("%s: %d events recorded; want 1", tc.name, len(events))
			continue
		}
		ev := events[0]
		if ev.EventType != tc.want || ev.ActorID != "user-4242" || ev.ActorType != "api_key" || ev.Workspace != "mine" {
			t.Errorf("%s: event = %+v; want %s by user-4242 (api_key) in mine", tc.name, ev, tc.want)
		}
		if seen != 1 {
			t.Errorf("%s: %d events were recorded when the answer was written; want 1", tc.name, seen)
		}
		if e.stub.count() == requestsBefore {
			t.Errorf("%s: the forge was not asked", tc.name)
		}
	}
}

// TS-31-49: Each route records its event type, resource type, resource id and action; a
// create that failed has an empty resource id.
// Requirement: 31-REQ-9.2
func TestEventTypesAndResources_TS_31_49(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	e.stub.setHandler(githubWrites())

	for _, want := range eventTable {
		var route writeRouteCase
		for _, r := range writeRouteCases {
			if r.name == want.route {
				route = r
			}
		}
		before := len(em.recorded())
		if rec := e.sendRoute(route, "widgets", apiKeyAuth()); rec.Code >= 300 {
			t.Errorf("%s: status = %d\n%s", want.route, rec.Code, rec.Body)
			continue
		}
		events := em.recorded()[before:]
		if len(events) != 1 {
			t.Errorf("%s: %d events recorded; want 1", want.route, len(events))
			continue
		}
		ev := events[0]
		if ev.EventType != want.eventType || ev.ResourceType != want.resource || ev.ResourceID != want.resourceID || ev.Action != want.action {
			t.Errorf("%s: event = %s/%s/%s/%s; want %s/%s/%s/%s", want.route,
				ev.EventType, ev.ResourceType, ev.ResourceID, ev.Action, want.eventType, want.resource, want.resourceID, want.action)
		}
	}

	// A create the forge refused has no resource id.
	e.stub.setHandler(respond(http.StatusInternalServerError, `{"message":"boom"}`))
	for _, name := range []string{"create issue", "open pull request"} {
		var route writeRouteCase
		for _, r := range writeRouteCases {
			if r.name == name {
				route = r
			}
		}
		before := len(em.recorded())
		e.sendRoute(route, "widgets", apiKeyAuth())
		events := em.recorded()[before:]
		if len(events) != 1 || events[0].ResourceID != "" || events[0].Metadata["ok"] != false {
			t.Errorf("%s that failed: events = %+v; want one failure with an empty resource id", name, events)
		}
	}
}

// TS-31-50: The common metadata is the target, ok, the number as a JSON number and the url
// without userinfo; a failure has ok false and the error_type and no url.
// Requirement: 31-REQ-9.3
func TestCommonEventMetadata_TS_31_50(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	withUserinfo := strings.Replace(issueBody(7, "t", "b"), "https://github.com/", "https://bot:secret@github.com/", 1)
	e.stub.setHandler(routes(map[string]http.HandlerFunc{"POST /repos/acme/widgets/issues": respond(http.StatusCreated, withUserinfo)}))

	e.send(http.MethodPost, "/workspaces/widgets/issues", apiKeyAuth(), `{"title":"t"}`)
	e.stub.setHandler(respond(http.StatusForbidden, `{"message":"no"}`))
	e.send(http.MethodPost, "/workspaces/widgets/issues", apiKeyAuth(), `{"title":"t"}`)

	events := em.recorded()
	if len(events) != 2 {
		t.Fatalf("%d events recorded; want 2", len(events))
	}
	encoded, _ := json.Marshal(events[0].Metadata)
	if string(encoded) != `{"number":7,"ok":true,"target":"origin","url":"https://github.com/acme/widgets/issues/7"}` {
		t.Errorf("success metadata = %s; want target, ok true, number 7 as a JSON number and the url without userinfo", encoded)
	}
	encoded, _ = json.Marshal(events[1].Metadata)
	if string(encoded) != `{"error_type":"forge_forbidden","ok":false,"target":"origin"}` {
		t.Errorf("failure metadata = %s; want ok false, error_type forge_forbidden and no url or number", encoded)
	}
}

// TS-31-51: The metadata that belongs to a kind of event is recorded: the fields of an
// update, the comment of a close, the labels of an add and the label of a removal, whether
// a label create created it, the branches and flag of a pull request.
// Requirement: 31-REQ-9.4
func TestPerTypeEventMetadata_TS_31_51(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	stub := newLabelStub()
	githubAll := githubWrites()
	e.stub.setHandler(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/labels") && !strings.Contains(r.URL.Path, "/issues/") {
			stub.handler(w, r)
			return
		}
		githubAll(w, r)
	})

	for _, call := range []struct{ method, path, body string }{
		{http.MethodPatch, "/workspaces/widgets/issues/42", `{"body":"b","labels":["x"]}`},
		{http.MethodPost, "/workspaces/widgets/issues/42/close", `{"comment":"done"}`},
		{http.MethodPost, "/workspaces/widgets/issues/42/labels", `["af:ready","af:fix"]`},
		{http.MethodDelete, "/workspaces/widgets/issues/42/labels/af:fix", ``},
		{http.MethodPost, "/workspaces/widgets/labels", `{"name":"af:triage"}`},
		{http.MethodPost, "/workspaces/widgets/labels", `{"name":"af:triage"}`},
		{http.MethodPost, "/workspaces/widgets/pulls", `{"title":"t","head":"me:feature","base":"main","draft":true}`},
	} {
		if rec := e.send(call.method, call.path, apiKeyAuth(), call.body); rec.Code >= 300 {
			t.Fatalf("%s %s: status = %d\n%s", call.method, call.path, rec.Code, rec.Body)
		}
	}

	events := em.recorded()
	if len(events) != 7 {
		t.Fatalf("%d events recorded; want 7", len(events))
	}
	// The metadata of the kinds, besides the common target, ok, number and url.
	own := func(i int) string {
		m := map[string]any{}
		for _, key := range []string{"fields", "comment_url", "labels", "label", "created", "head", "base", "draft"} {
			if v, ok := events[i].Metadata[key]; ok {
				m[key] = v
			}
		}
		return fmt.Sprint(m)
	}
	for i, want := range []string{
		"map[fields:[body labels]]",
		"map[comment_url:" + commentURL + "]",
		"map[labels:[af:ready af:fix]]",
		"map[label:af:fix]",
		"map[created:true label:af:triage]",
		"map[created:false label:af:triage]",
		"map[base:main draft:true head:me:feature]",
	} {
		if got := own(i); got != want {
			t.Errorf("event %d (%s) holds %s; want %s", i, events[i].EventType, got, want)
		}
	}
}

// TS-31-52: No metadata value of any event holds the title, the body, the comment text, the
// token or the forge's message, for any route, whether the forge accepted the write or not.
// Requirement: 31-REQ-9.5
func TestEventMetadataHoldsNoContent_TS_31_52(t *testing.T) {
	const (
		title   = "TITLE-0xc0ffee"
		text    = "TEXT-0xdeadbeef"
		message = "MESSAGE-0xfeedface"
	)
	secrets := []string{title, text, message, "origin-token"}
	bodies := map[string]string{
		"create issue":            `{"title":"` + title + `","body":"` + text + `"}`,
		"update issue":            `{"title":"` + title + `","body":"` + text + `"}`,
		"close issue":             `{"comment":"` + text + `"}`,
		"comment on issue":        `{"body":"` + text + `"}`,
		"add labels":              `["af:ready"]`,
		"remove label":            ``,
		"ensure label":            `{"name":"af:triage","description":"` + text + `"}`,
		"open pull request":       `{"title":"` + title + `","body":"` + text + `","head":"feature","base":"main"}`,
		"comment on pull request": `{"body":"` + text + `"}`,
	}
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em), noSleep)

	for _, outcome := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"success", githubWrites()},
		{"401", func(w http.ResponseWriter, r *http.Request) { respondMessage(w, 401, message+" origin-token") }},
		{"404", func(w http.ResponseWriter, r *http.Request) { respondMessage(w, 404, message+" origin-token") }},
		{"422", func(w http.ResponseWriter, r *http.Request) { respondMessage(w, 422, message+" origin-token") }},
		{"429", func(w http.ResponseWriter, r *http.Request) { respondMessage(w, 429, message+" origin-token") }},
		{"500", func(w http.ResponseWriter, r *http.Request) { respondMessage(w, 500, message+" origin-token") }},
	} {
		e.stub.setHandler(outcome.handler)
		for _, route := range writeRouteCases {
			route.body = bodies[route.name]
			e.sendRoute(route, "widgets", apiKeyAuth())
		}
	}

	events := em.recorded()
	if len(events) < 6*len(writeRouteCases) {
		t.Fatalf("%d events recorded; the run proves nothing", len(events))
	}
	for _, ev := range events {
		for key, value := range ev.Metadata {
			for _, secret := range secrets {
				if strings.Contains(fmt.Sprint(value), secret) {
					t.Errorf("%s: metadata %s = %v holds %q", ev.EventType, key, value, secret)
				}
			}
		}
	}
}
