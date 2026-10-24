package forge

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/testutil"
)

// orderWriter notes how many events were recorded when the first byte of the answer was written.
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
	o.WriteHeader(0)
	return o.ResponseWriter.Write(b)
}

// A write that reached the forge records exactly one event before the answer is written,
// a success and a failure alike, with the caller as actor, the slug as workspace, and the
// type, resource and action of its route; a create that failed has no resource id.
func TestWriteEvents_OneEventPerWrite(t *testing.T) {
	em := &recordingEmitter{}
	e := widgetsEnv(t, withEmitter(em))
	e.stub.setHandler(githubWrites())
	caller := testutil.PAT(ownerID, "forge:write")
	for _, tc := range []struct {
		route                                   string
		eventType, resource, resourceID, action string
	}{
		{"create issue", audit.EventForgeIssueCreate, "issue", "7", "create"},
		{"update issue", audit.EventForgeIssueUpdate, "issue", "42", "update"},
		{"close issue", audit.EventForgeIssueClose, "issue", "42", "close"},
		{"comment on issue", audit.EventForgeCommentCreate, "comment", "42", "create"},
		{"add labels", audit.EventForgeLabelAdd, "label", "42", "add"},
		{"remove label", audit.EventForgeLabelRemove, "label", "42", "remove"},
		{"ensure label", audit.EventForgeLabelCreate, "label", "af:triage", "create"},
		{"open pull request", audit.EventForgePullRequestCreate, "pull_request", "9", "create"},
		{"comment on pull request", audit.EventForgePullRequestComment, "pull_request", "42", "comment"},
	} {
		r := writeRoute(tc.route)
		before, requests := len(em.recorded()), e.stub.count()
		req := httptest.NewRequest(r.method, "/api/v1"+r.url("widgets"), strings.NewReader(r.body))
		req.Header.Set(testutil.AuthHeader, marshal(caller))
		rec, seen := httptest.NewRecorder(), -1
		e.echo.ServeHTTP(orderWriter{rec, func() int { return len(em.recorded()) - before }, &seen}, req)
		events := em.recorded()[before:]
		if rec.Code >= 300 || len(events) != 1 || seen != 1 || e.stub.count() == requests {
			t.Errorf("%s: %d, %d events, %d when the answer was written; want 1 and 1", tc.route, rec.Code, len(events), seen)
			continue
		}
		ev := events[0]
		if ev.EventType != tc.eventType || ev.ResourceType != tc.resource || ev.ResourceID != tc.resourceID || ev.Action != tc.action ||
			ev.ActorID != ownerID || ev.ActorType != "pat" || ev.Workspace != "widgets" {
			t.Errorf("%s: event = %+v; want %s/%s/%s/%s by the caller in widgets", tc.route, ev, tc.eventType, tc.resource, tc.resourceID, tc.action)
		}
	}
	e.stub.setHandler(answer(500, "boom"))
	for _, name := range []string{"create issue", "open pull request"} {
		before := len(em.recorded())
		e.sendRoute(writeRoute(name), "widgets", apiKeyAuth())
		if events := em.recorded()[before:]; len(events) != 1 || events[0].ResourceID != "" || events[0].Metadata["ok"] != false {
			t.Errorf("%s that failed: events = %+v; want one failure with no resource id", name, events)
		}
	}
}

// The metadata holds the target, ok, the number as a JSON number and the url without
// userinfo; a failure has ok false and the error_type and no url or number; each kind adds
// its own: the fields of an update, the comment's URL of a close, the labels and the label,
// whether a label was created, the branches and the draft flag of a pull request.
func TestWriteEvents_Metadata(t *testing.T) {
	em := &recordingEmitter{}
	e := widgetsEnv(t, withEmitter(em))
	all, labels := githubWrites(), newLabelStub(githubLabelPath)
	e.stub.setHandler(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/acme/widgets/issues" && r.Method == http.MethodPost:
			respond(http.StatusCreated, strings.Replace(issueBody(7, "t", "b"), "https://github.com/", "https://bot:secret@github.com/", 1))(w, r)
		case r.URL.Path == "/repos/acme/widgets/issues/42/comments":
			respond(http.StatusCreated, `{"id":9,"html_url":"https://bot:hunter2@github.com/acme/widgets/issues/42?t=origin-token#issuecomment-9"}`)(w, r)
		case strings.HasPrefix(r.URL.Path, githubLabelPath):
			labels.handler(w, r)
		default:
			all(w, r)
		}
	})
	for _, call := range []struct{ method, path, body string }{
		{http.MethodPost, "/workspaces/widgets/issues", `{"title":"t"}`},
		{http.MethodPatch, "/workspaces/widgets/issues/42", `{"body":"b","labels":["x"]}`},
		{http.MethodPost, "/workspaces/widgets/issues/42/close", `{"comment":"done"}`},
		{http.MethodPost, "/workspaces/widgets/issues/42/labels", `["af:ready","af:fix"]`},
		{http.MethodDelete, "/workspaces/widgets/issues/42/labels/af:fix", ``},
		{http.MethodPost, "/workspaces/widgets/labels", `{"name":"af:triage"}`},
		{http.MethodPost, "/workspaces/widgets/labels", `{"name":"af:triage"}`},
		{http.MethodPost, "/workspaces/widgets/pulls", `{"title":"t","head":"me:feature","base":"main","draft":true}`},
		{http.MethodPost, "/workspaces/widgets/pulls/42/comments", `{"body":"b"}`},
	} {
		if rec := e.send(call.method, call.path, apiKeyAuth(), call.body); rec.Code >= 300 {
			t.Fatalf("%s %s: %d %s", call.method, call.path, rec.Code, rec.Body)
		}
	}
	e.stub.setHandler(answer(403, "no"))
	e.send(http.MethodPost, "/workspaces/widgets/issues", apiKeyAuth(), `{"title":"t"}`)
	events := em.recorded()
	if len(events) != 10 {
		t.Fatalf("%d events; want 10", len(events))
	}
	for i, want := range []string{
		`{"number":7,"ok":true,"target":"origin","url":"https://github.com/acme/widgets/issues/7"}`,
		`{"fields":["body","labels"],"number":42,"ok":true,"target":"origin","url":"https://github.com/acme/widgets/issues/42"}`,
		`{"comment_url":"https://github.com/acme/widgets/issues/42?t=[redacted]#issuecomment-9","number":42,"ok":true,"target":"origin","url":"https://github.com/acme/widgets/issues/42"}`,
		`{"labels":["af:ready","af:fix"],"number":42,"ok":true,"target":"origin"}`,
		`{"label":"af:fix","number":42,"ok":true,"target":"origin"}`,
		`{"created":true,"label":"af:triage","ok":true,"target":"origin"}`,
		`{"created":false,"label":"af:triage","ok":true,"target":"origin"}`,
		`{"base":"main","draft":true,"head":"me:feature","number":9,"ok":true,"target":"origin","url":"https://github.com/acme/widgets/pull/9"}`,
		`{"number":42,"ok":true,"target":"origin","url":"https://github.com/acme/widgets/issues/42?t=[redacted]#issuecomment-9"}`,
		`{"error_type":"forge_forbidden","ok":false,"target":"origin"}`,
	} {
		if got := marshal(events[i].Metadata); got != want {
			t.Errorf("event %d (%s) metadata = %s\nwant %s", i, events[i].EventType, got, want)
		}
	}
}

// No metadata value of any event holds the title, the body, the comment text, the token or
// the forge's message, whether the forge accepted the write or not.
func TestWriteEvents_HoldNoContent(t *testing.T) {
	const title, text, message = "TITLE-0xc0ffee", "TEXT-0xdeadbeef", "MESSAGE-0xfeedface"
	bodies := map[string]string{
		"create issue": `{"title":"` + title + `","body":"` + text + `"}`, "update issue": `{"title":"` + title + `","body":"` + text + `"}`,
		"close issue": `{"comment":"` + text + `"}`, "comment on issue": `{"body":"` + text + `"}`, "ensure label": `{"name":"af:triage","description":"` + text + `"}`,
		"open pull request": `{"title":"` + title + `","body":"` + text + `","head":"feature","base":"main"}`, "comment on pull request": `{"body":"` + text + `"}`,
	}
	em := &recordingEmitter{}
	e := widgetsEnv(t, withEmitter(em))
	for _, handler := range []http.HandlerFunc{githubWrites(), answer(404, message+" origin-token"), answer(500, message+" origin-token")} {
		e.stub.setHandler(handler)
		for _, r := range writeRoutes {
			if body, ok := bodies[r.name]; ok {
				r.body = body
			}
			e.sendRoute(r, "widgets", apiKeyAuth())
		}
	}
	events := em.recorded()
	if len(events) < 3*len(writeRoutes) {
		t.Fatalf("%d events; the run proves nothing", len(events))
	}
	for _, ev := range events {
		if s := fmt.Sprint(ev.Metadata); strings.Contains(s, title) || strings.Contains(s, text) || strings.Contains(s, message) || strings.Contains(s, "origin-token") {
			t.Errorf("%s: metadata %s holds content", ev.EventType, s)
		}
	}
}

// Without an emitter a write behaves as with one; an emitter that fails is logged with the
// event type and the workspace and changes nothing about the answer; the event is emitted
// on a context the caller's cancellation does not reach.
func TestWriteEvents_EmitterIsOptionalAndMayFail(t *testing.T) {
	logs := captureLog(t)
	em := &recordingEmitter{err: errors.New("store is down")}
	with, without := widgetsEnv(t, withEmitter(em)), widgetsEnv(t)
	with.stub.setHandler(githubWrites())
	without.stub.setHandler(githubWrites())
	a, b := with.sendRoute(writeRoutes[0], "widgets", apiKeyAuth()), without.sendRoute(writeRoutes[0], "widgets", apiKeyAuth())
	if a.Code != http.StatusCreated || a.Code != b.Code || a.Body.String() != b.Body.String() {
		t.Errorf("with an emitter %d %s; without %d %s; want the same 201", a.Code, a.Body, b.Code, b.Body)
	}
	if log := logs.String(); strings.Count(log, "audit: failed to emit") != 1 || !strings.Contains(log, audit.EventForgeIssueCreate) || !strings.Contains(log, "widgets") {
		t.Errorf("the log = %q; want one error line naming the event and the workspace", log)
	}
	if ctxs := em.contexts(); len(ctxs) != 1 || ctxs[0] != nil {
		t.Errorf("the event was emitted on the context %v; want a live one", ctxs)
	}
}
