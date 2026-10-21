package forge

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/agent-fox-dev/aflib/issuex"

	"github.com/agent-fox-dev/hub/internal/audit"
)

const commentBody = `{"id":9,"body":"done","html_url":"https://github.com/acme/widgets/issues/42#issuecomment-9"}`

const commentURL = "https://github.com/acme/widgets/issues/42#issuecomment-9"

// closedIssue is what GitHub answers to the request that closes issue n.
func closedIssue(n int) string {
	return strings.Replace(issueBody(n, "t", ""), `"state":"open"`, `"state":"closed"`, 1)
}

// TS-31-15: A close with no comment, an empty body, {} or a blank comment closes the
// issue and posts nothing, an already closed issue included.
// Requirement: 31-REQ-3.1
func TestCloseIssue_WithoutAUsableComment_TS_31_15(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"PATCH /repos/acme/widgets/issues/7": respond(http.StatusOK, closedIssue(7)),
		"PATCH /repos/acme/widgets/issues/8": respond(http.StatusOK, closedIssue(8)),
	}))

	for _, tc := range []struct {
		path, body string
		n          int
	}{
		{"/workspaces/widgets/issues/7/close", ``, 7},
		{"/workspaces/widgets/issues/7/close", `{}`, 7},
		{"/workspaces/widgets/issues/7/close", `{"comment":"   "}`, 7},
		{"/workspaces/widgets/issues/7/close", `{"comment":null}`, 7},
		{"/workspaces/widgets/issues/8/close", ``, 8},
	} {
		rec := e.send(http.MethodPost, tc.path, apiKeyAuth(), tc.body)
		if rec.Code != http.StatusOK {
			t.Errorf("POST %s %q: status = %d; want 200\n%s", tc.path, tc.body, rec.Code, rec.Body)
			continue
		}
		var issue issuex.Issue
		if err := json.Unmarshal(rec.Body.Bytes(), &issue); err != nil || issue.Number != tc.n || issue.State != "closed" {
			t.Errorf("POST %s %q: body %s; want issue %d, closed (%v)", tc.path, tc.body, rec.Body, tc.n, err)
		}
	}
	for _, r := range e.stub.received() {
		if r.Method != http.MethodPatch || !strings.HasPrefix(r.Path, "/repos/acme/widgets/issues/") || strings.HasSuffix(r.Path, "/comments") {
			t.Errorf("the forge received %s %s; want only the PATCH that closes", r.Method, r.Path)
		}
		if got := jsonOf(t, r.Body); len(got) != 1 || got["state"] != "closed" {
			t.Errorf("the close carried %s; want {\"state\":\"closed\"}", r.Body)
		}
	}
	if n := e.stub.count(); n != 5 {
		t.Errorf("the forge received %d requests; want 5, one close each", n)
	}
	for i, ev := range em.recorded() {
		if _, has := ev.Metadata["comment_url"]; has {
			t.Errorf("event %d holds a comment_url; no comment was posted", i)
		}
	}
	if comment := strings.TrimSpace(`{"comment":5}`); comment != "" {
		if rec := e.send(http.MethodPost, "/workspaces/widgets/issues/7/close", apiKeyAuth(), comment); rec.Code != http.StatusBadRequest ||
			!strings.Contains(decodeError(t, rec).Message, "comment") {
			t.Errorf("a comment that is no string: %d %s; want 400 naming comment", rec.Code, rec.Body)
		}
	}
}

// TS-31-16: A close with a comment posts the comment on the issue first and closes after.
// Requirement: 31-REQ-3.2
func TestCloseIssue_CommentBeforeClose_TS_31_16(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/issues/42/comments": respond(http.StatusCreated, commentBody),
		"PATCH /repos/acme/widgets/issues/42":         respond(http.StatusOK, closedIssue(42)),
	}))

	rec := e.send(http.MethodPost, "/workspaces/widgets/issues/42/close", apiKeyAuth(), `{"comment":"done"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200\n%s", rec.Code, rec.Body)
	}
	var issue issuex.Issue
	if err := json.Unmarshal(rec.Body.Bytes(), &issue); err != nil || issue.Number != 42 || issue.State != "closed" {
		t.Errorf("body %s; want the closed issue 42 (%v)", rec.Body, err)
	}
	got := e.stub.received()
	if len(got) != 2 || got[0].Method != http.MethodPost || !strings.HasSuffix(got[0].Path, "/issues/42/comments") ||
		got[1].Method != http.MethodPatch || got[1].Path != "/repos/acme/widgets/issues/42" {
		t.Fatalf("the forge received %+v; want the comment, then the close", got)
	}
	if body := jsonOf(t, got[0].Body); body["body"] != "done" || len(body) != 1 {
		t.Errorf("the comment carried %s; want the comment text as given", got[0].Body)
	}

	// One event stands for the request: the close, with the comment's URL.
	events := em.recorded()
	if len(events) != 1 {
		t.Fatalf("%d events recorded; want 1", len(events))
	}
	ev := events[0]
	if ev.EventType != audit.EventForgeIssueClose || ev.ResourceType != "issue" || ev.ResourceID != "42" || ev.Action != "close" {
		t.Errorf("event = %+v; want hub.forge.issue.close on issue 42", ev)
	}
	if ev.Metadata["comment_url"] != commentURL || ev.Metadata["ok"] != true || ev.Metadata["number"] != 42 {
		t.Errorf("metadata = %v; want comment_url %s, ok true, number 42", ev.Metadata, commentURL)
	}
}

// TS-31-17: A close comment the forge refuses is answered with the mapped error, and the
// issue is not closed.
// Requirement: 31-REQ-3.3
func TestCloseIssue_FailedCommentPreventsClose_TS_31_17(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/issues/42/comments": respond(http.StatusForbidden, `{"message":"Resource not accessible by personal access token"}`),
		"PATCH /repos/acme/widgets/issues/42":         respond(http.StatusOK, closedIssue(42)),
	}))

	rec := e.send(http.MethodPost, "/workspaces/widgets/issues/42/close", apiKeyAuth(), `{"comment":"done"}`)
	if got := decodeError(t, rec); rec.Code != http.StatusBadGateway || got.ErrorType != "forge_forbidden" {
		t.Errorf("answer = %d %+v; want 502 forge_forbidden, the mapping of a forge 403", rec.Code, got)
	}
	for _, r := range e.stub.received() {
		if r.Method == http.MethodPatch {
			t.Errorf("the forge received %s %s; the issue must not be closed", r.Method, r.Path)
		}
	}
	events := em.recorded()
	if len(events) != 1 || events[0].EventType != audit.EventForgeIssueClose || events[0].Metadata["ok"] != false ||
		events[0].Metadata["error_type"] != "forge_forbidden" {
		t.Fatalf("events = %+v; want one failed hub.forge.issue.close", events)
	}
	if _, has := events[0].Metadata["comment_url"]; has {
		t.Errorf("the event holds a comment_url, but no comment was posted")
	}
}

// TS-31-18: A close that fails after the comment was posted is answered with the close's
// error, and the event records the comment's URL.
// Requirement: 31-REQ-3.4
func TestCloseIssue_FailedCloseKeepsTheCommentURL_TS_31_18(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/issues/42/comments": respond(http.StatusCreated, commentBody),
		"PATCH /repos/acme/widgets/issues/42":         respond(http.StatusInternalServerError, `{"message":"Server Error"}`),
	}))

	rec := e.send(http.MethodPost, "/workspaces/widgets/issues/42/close", apiKeyAuth(), `{"comment":"done"}`)
	if got := decodeError(t, rec); rec.Code != http.StatusBadGateway || got.ErrorType != "forge_error" {
		t.Errorf("answer = %d %+v; want 502 forge_error, the mapping of the close's 500", rec.Code, got)
	}
	events := em.recorded()
	if len(events) != 1 {
		t.Fatalf("%d events recorded; want 1", len(events))
	}
	ev := events[0]
	if ev.EventType != audit.EventForgeIssueClose || ev.Metadata["ok"] != false || ev.Metadata["error_type"] != "forge_error" ||
		ev.Metadata["comment_url"] != commentURL {
		t.Errorf("event = %+v; want a failed hub.forge.issue.close with comment_url %s", ev, commentURL)
	}
}

// TS-31-19: Commenting on an issue answers 201 with the comment's URL.
// Requirement: 31-REQ-3.5
func TestCommentOnIssue_TS_31_19(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/issues/42/comments": respond(http.StatusCreated, commentBody),
	}))

	rec := e.send(http.MethodPost, "/workspaces/widgets/issues/42/comments", apiKeyAuth(), `{"body":"hello"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201\n%s", rec.Code, rec.Body)
	}
	if want := fmt.Sprintf(`{"url":%q}`, commentURL); strings.TrimSpace(rec.Body.String()) != want {
		t.Errorf("body = %s; want %s", rec.Body, want)
	}
	if got := e.stub.received(); len(got) != 1 || jsonOf(t, got[0].Body)["body"] != "hello" {
		t.Errorf("the forge received %+v; want one comment with the text", got)
	}
	events := em.recorded()
	if len(events) != 1 {
		t.Fatalf("%d events recorded; want 1", len(events))
	}
	ev := events[0]
	if ev.EventType != audit.EventForgeCommentCreate || ev.ResourceType != "comment" || ev.ResourceID != "42" || ev.Action != "create" ||
		ev.Metadata["url"] != commentURL || ev.Metadata["number"] != 42 {
		t.Errorf("event = %+v; want hub.forge.comment.create on 42 with the comment url", ev)
	}
}

// TS-31-20: An absent or blank comment body answers 400 naming body.
// Requirement: 31-REQ-3.6
func TestCommentOnIssue_BlankBody_TS_31_20(t *testing.T) {
	e := newWriteEnv(t)
	for _, body := range []string{`{}`, `{"body":"  "}`, `{"body":null}`, `{"body":3}`} {
		rec := e.send(http.MethodPost, "/workspaces/widgets/issues/42/comments", apiKeyAuth(), body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d; want 400\n%s", body, rec.Code, rec.Body)
			continue
		}
		if msg := decodeError(t, rec).Message; !strings.Contains(msg, "body") {
			t.Errorf("%s: message = %q; want it to name body", body, msg)
		}
	}
	if n := e.stub.count(); n != 0 {
		t.Errorf("the forge stub received %d requests; want none", n)
	}
}

// A close on GitLab posts the note on iid 42 first, then closes with state_event close,
// and answers the closed issue (31-PATH-2 without the CLI).
// Requirement: 31-REQ-3.2
func TestCloseIssue_GitLab(t *testing.T) {
	em := &recordingEmitter{}
	e := newGitLabWriteEnv(t, withEmitter(em))
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /api/v4/projects/acme/widgets/issues/42/notes": respond(http.StatusCreated, `{"id":5,"body":"done","web_url":"https://gitlab.com/acme/widgets/-/issues/42#note_5"}`),
		"PUT /api/v4/projects/acme/widgets/issues/42":        respond(http.StatusOK, gitlabIssueBody(42, "t", "", "closed")),
	}))

	rec := e.send(http.MethodPost, "/workspaces/lab/issues/42/close", apiKeyAuth(), `{"comment":"done"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200\n%s", rec.Code, rec.Body)
	}
	var issue issuex.Issue
	if err := json.Unmarshal(rec.Body.Bytes(), &issue); err != nil || issue.Number != 42 || issue.State != "closed" {
		t.Errorf("body %s; want the closed issue 42 (%v)", rec.Body, err)
	}
	got := e.stub.received()
	if len(got) != 2 || !strings.HasSuffix(got[0].Path, "/issues/42/notes") || got[1].Method != http.MethodPut {
		t.Fatalf("GitLab received %+v; want the note, then the close", got)
	}
	if body := jsonOf(t, got[1].Body); len(body) != 1 || body["state_event"] != "close" {
		t.Errorf("the close carried %s; want {\"state_event\":\"close\"}", got[1].Body)
	}
	events := em.recorded()
	if len(events) != 1 || events[0].Metadata["comment_url"] != "https://gitlab.com/acme/widgets/-/issues/42#note_5" {
		t.Errorf("events = %+v; want one close event with the note's url", events)
	}
}

// A body-like field over the forge's limit is refused with 413 body_too_large on the issue
// routes, before the forge is asked and without an event; the limit itself is allowed.
// Requirement: 31-REQ-7.1
func TestIssueRoutes_BodyLimit(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"PATCH /repos/acme/widgets/issues/42":         respond(http.StatusOK, closedIssue(42)),
		"POST /repos/acme/widgets/issues/42/comments": respond(http.StatusCreated, commentBody),
	}))
	over, at := strings.Repeat("x", 65537), strings.Repeat("x", 65536)

	for _, tc := range []struct{ name, method, path, field, over, at string }{
		{"update body", http.MethodPatch, "/workspaces/widgets/issues/42", "body", `{"body":"` + over + `"}`, `{"body":"` + at + `"}`},
		{"close comment", http.MethodPost, "/workspaces/widgets/issues/42/close", "comment", `{"comment":"` + over + `"}`, `{"comment":"` + at + `"}`},
		{"issue comment", http.MethodPost, "/workspaces/widgets/issues/42/comments", "body", `{"body":"` + over + `"}`, `{"body":"` + at + `"}`},
	} {
		before := e.stub.count()
		rec := e.send(tc.method, tc.path, apiKeyAuth(), tc.over)
		got := decodeError(t, rec)
		want := tc.field + " is 65537 characters; github accepts at most 65536"
		if rec.Code != http.StatusRequestEntityTooLarge || got.ErrorType != "body_too_large" || got.Message != want {
			t.Errorf("%s: answer = %d %+v; want 413 body_too_large %q", tc.name, rec.Code, got, want)
		}
		if e.stub.count() != before {
			t.Errorf("%s: the forge was asked", tc.name)
		}
		if rec := e.send(tc.method, tc.path, apiKeyAuth(), tc.at); rec.Code >= 300 {
			t.Errorf("%s at the limit: status = %d; want it sent", tc.name, rec.Code)
		}
	}
	if n := len(em.recorded()); n != 3 {
		t.Errorf("%d events recorded; want 3, one for each request that reached the forge", n)
	}
}
