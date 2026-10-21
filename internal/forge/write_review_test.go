package forge

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The tests of this file pin defects the conformance review of spec 31 found.

// A write that issuex refuses before it sends anything, because the workspace has no
// credential, sent nothing to the forge and records no event (31-REQ-9.1: an event is
// recorded for a write that sent at least one request to the forge). A request hub sends
// itself goes out without a token and is recorded.
// Requirement: 31-REQ-9.1
func TestWriteWithoutACredentialSendsNothingAndRecordsNothing(t *testing.T) {
	for _, route := range []writeRouteCase{
		writeRouteCases[0], // create issue
		{"close issue with a comment", http.MethodPost, "/workspaces/%s/issues/42/close", `{"comment":"done"}`},
		writeRouteCases[3], // comment on issue
		writeRouteCases[4], // add labels
		writeRouteCases[7], // open pull request
		writeRouteCases[8], // comment on pull request
	} {
		t.Run(route.name, func(t *testing.T) {
			em := &recordingEmitter{}
			e := newWriteEnv(t, withEmitter(em))
			e.originAuth = nil
			e.stub.setHandler(githubWrites())
			rec := e.sendRoute(route, "widgets", apiKeyAuth())
			if rec.Code != http.StatusBadGateway {
				t.Errorf("status = %d; want 502\n%s", rec.Code, rec.Body)
			}
			if n := e.stub.count(); n != 0 {
				t.Errorf("the forge received %d requests; want none", n)
			}
			if n := len(em.recorded()); n != 0 {
				t.Errorf("%d events recorded although nothing reached the forge: %+v", n, em.recorded())
			}
		})
	}

	// On GitLab the removal of a label is issuex's and is refused the same way.
	em := &recordingEmitter{}
	e := newGitLabWriteEnv(t, withEmitter(em))
	e.originAuth = nil
	e.send(http.MethodDelete, "/workspaces/lab/issues/42/labels/af:fix", apiKeyAuth(), "")
	if e.stub.count() != 0 || len(em.recorded()) != 0 {
		t.Errorf("GitLab removal without a credential: %d requests, %d events; want none", e.stub.count(), len(em.recorded()))
	}

	// The control: an update is a request hub sends itself, it goes out (the forge refuses
	// it) and it is recorded.
	em = &recordingEmitter{}
	e = newWriteEnv(t, withEmitter(em))
	e.originAuth = nil
	e.stub.setHandler(respond(http.StatusUnauthorized, `{"message":"Requires authentication"}`))
	rec := e.send(http.MethodPatch, "/workspaces/widgets/issues/42", apiKeyAuth(), `{"title":"t"}`)
	if got := decodeError(t, rec); rec.Code != http.StatusBadGateway || got.ErrorType != "forge_unauthenticated" {
		t.Errorf("update without a credential: %d %+v; want the forge's 401 as 502 forge_unauthenticated", rec.Code, got)
	}
	if e.stub.count() != 1 || len(em.recorded()) != 1 {
		t.Errorf("update without a credential: %d requests, %d events; want 1 and 1", e.stub.count(), len(em.recorded()))
	}
	if token := bearer(e.stub.received()[0]); token != "" {
		t.Errorf("the update carried the token %q; want none", token)
	}
}

// The URL of the comment in the event of a close is the forge's with userinfo and the
// token taken out, like the url of every event.
// Requirement: 31-REQ-8.5, 31-REQ-9.5
func TestCloseEventCommentURLIsScrubbed(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/issues/42/comments": respond(http.StatusCreated, dirtyCommentBody),
		"PATCH /repos/acme/widgets/issues/42":         respond(http.StatusOK, closedIssue(42)),
	}))
	if rec := e.send(http.MethodPost, "/workspaces/widgets/issues/42/close", apiKeyAuth(), `{"comment":"done"}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d\n%s", rec.Code, rec.Body)
	}
	events := em.recorded()
	if len(events) != 1 {
		t.Fatalf("%d events recorded; want 1", len(events))
	}
	if got := events[0].Metadata["comment_url"]; got != "https://github.com/acme/widgets/issues/42?t=[redacted]#issuecomment-9" {
		t.Errorf("comment_url = %v; want the url without userinfo and with the token redacted", got)
	}
}

// A comment URL, an issue URL and a pull-request URL that carry userinfo and the token reach
// no event, on any route: the metadata holds the hub's own record of them, scrubbed.
// Requirement: 31-REQ-8.5, 31-REQ-9.5
func TestEventURLsNeverHoldUserinfoOrTheToken(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	dirty := func(body string) string {
		return strings.Replace(body, "https://github.com/", "https://bot:hunter2@github.com/", 1)
	}
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/issues":             respond(http.StatusCreated, dirty(issueBody(7, "t", ""))),
		"PATCH /repos/acme/widgets/issues/42":         respond(http.StatusOK, dirty(closedIssue(42))),
		"POST /repos/acme/widgets/issues/42/comments": respond(http.StatusCreated, dirtyCommentBody),
		"POST /repos/acme/widgets/pulls":              respond(http.StatusCreated, dirty(pullBody(9, "acme/widgets", "h", "main", false))),
	}))
	for _, call := range []struct{ method, path, body string }{
		{http.MethodPost, "/workspaces/widgets/issues", `{"title":"t"}`},
		{http.MethodPatch, "/workspaces/widgets/issues/42", `{"title":"t"}`},
		{http.MethodPost, "/workspaces/widgets/issues/42/close", `{"comment":"done"}`},
		{http.MethodPost, "/workspaces/widgets/issues/42/comments", `{"body":"done"}`},
		{http.MethodPost, "/workspaces/widgets/pulls/42/comments", `{"body":"done"}`},
		{http.MethodPost, "/workspaces/widgets/pulls", `{"title":"t","head":"h","base":"main"}`},
	} {
		e.send(call.method, call.path, apiKeyAuth(), call.body)
	}
	events := em.recorded()
	if len(events) != 6 {
		t.Fatalf("%d events recorded; want 6", len(events))
	}
	for _, ev := range events {
		for key, value := range ev.Metadata {
			if text := fmt.Sprint(value); strings.Contains(text, "hunter2") || strings.Contains(text, "origin-token") {
				t.Errorf("%s: metadata %s = %q holds userinfo or the token", ev.EventType, key, text)
			}
		}
	}
}

// dirtyCommentBody is a comment whose URL carries userinfo and, in its query, the token.
const dirtyCommentBody = `{"id":9,"body":"done","html_url":"https://bot:hunter2@github.com/acme/widgets/issues/42?t=origin-token#issuecomment-9"}`

// A label named . or .. is sent to the forge as %2E or %2E%2E: a dot segment in the path of
// a request is resolved by a forge or a proxy, and the removal of a label named . would
// remove every label of the issue.
// Requirement: 31-REQ-4.3
func TestDotSegmentLabelNamesAreEncoded(t *testing.T) {
	e := newWriteEnv(t)
	e.stub.setHandler(respond(http.StatusOK, labelsAnswer))
	for _, tc := range []struct{ segment, sent string }{{"%2E", "%2E"}, {"%2E%2E", "%2E%2E"}} {
		before := e.stub.count()
		if rec := e.send(http.MethodDelete, "/workspaces/widgets/issues/42/labels/"+tc.segment, apiKeyAuth(), ""); rec.Code != http.StatusNoContent {
			t.Errorf("DELETE .../labels/%s: status = %d; want 204\n%s", tc.segment, rec.Code, rec.Body)
		}
		got := e.stub.received()[before:]
		if len(got) != 1 || got[0].EscapedPath != "/repos/acme/widgets/issues/42/labels/"+tc.sent {
			t.Errorf("DELETE .../labels/%s: the forge received %+v; want .../labels/%s", tc.segment, got, tc.sent)
		}
	}

	stub := newLabelStub()
	e.stub.setHandler(stub.handler)
	for _, name := range []string{".", ".."} {
		before := e.stub.count()
		e.send(http.MethodPost, "/workspaces/widgets/labels", apiKeyAuth(), `{"name":"`+name+`"}`)
		lookup := e.stub.received()[before]
		if want := "/repos/acme/widgets/labels/" + strings.ReplaceAll(name, ".", "%2E"); lookup.EscapedPath != want {
			t.Errorf("the lookup of %q went to %s; want %s", name, lookup.EscapedPath, want)
		}
	}
}

// GitLab does not take assignees by name, so an update that sets nothing else sets
// nothing: it is refused as having nothing to update, and an update with other fields
// records only those as changed.
// Requirement: 31-REQ-2.3, 31-REQ-2.5
func TestGitLabUpdateWithOnlyAssignees(t *testing.T) {
	em := &recordingEmitter{}
	e := newGitLabWriteEnv(t, withEmitter(em))
	e.stub.setHandler(respond(http.StatusOK, gitlabIssueBody(42, "t", "", "opened")))

	rec := e.send(http.MethodPatch, "/workspaces/lab/issues/42", apiKeyAuth(), `{"assignees":["octocat"]}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(decodeError(t, rec).Message, "nothing to update") {
		t.Errorf("answer = %d %s; want 400 nothing to update", rec.Code, rec.Body)
	}
	if e.stub.count() != 0 || len(em.recorded()) != 0 {
		t.Errorf("%d requests and %d events; want none", e.stub.count(), len(em.recorded()))
	}

	if rec := e.send(http.MethodPatch, "/workspaces/lab/issues/42", apiKeyAuth(), `{"title":"x","assignees":["octocat"]}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200\n%s", rec.Code, rec.Body)
	}
	events := em.recorded()
	if len(events) != 1 || fmt.Sprint(events[0].Metadata["fields"]) != "[title]" {
		t.Errorf("events = %+v; want one with fields [title]: the assignees were not sent", events)
	}

	// On GitHub the assignees are a field like any other.
	em = &recordingEmitter{}
	gh := newWriteEnv(t, withEmitter(em))
	gh.stub.setHandler(respond(http.StatusOK, issueBody(42, "t", "")))
	if rec := gh.send(http.MethodPatch, "/workspaces/widgets/issues/42", apiKeyAuth(), `{"assignees":["octocat"]}`); rec.Code != http.StatusOK {
		t.Errorf("GitHub: status = %d; want 200", rec.Code)
	}
	if events := em.recorded(); len(events) != 1 || fmt.Sprint(events[0].Metadata["fields"]) != "[assignees]" {
		t.Errorf("GitHub events = %+v; want fields [assignees]", events)
	}
}
