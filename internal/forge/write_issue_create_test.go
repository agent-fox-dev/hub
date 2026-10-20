package forge

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/aflib/issuex"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
	"github.com/agent-fox-dev/hub/internal/wslock"
)

// newWriteEnv is the forge routes with the workspace widgets, a standard workspace on
// github.com/acme/widgets whose API is the forge stub, and the workspace's credential
// origin-token. The mods change the Config.
func newWriteEnv(t *testing.T, mods ...func(*Config)) *env {
	t.Helper()
	e := newEnv(t, mods...)
	addWorkspace(t, e.db, "widgets")
	e.setVar("widgets", "FORGE_API_URL", e.stub.URL)
	return e
}

// respond is a stub handler that answers every request with status and the JSON body.
func respond(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

// routes is a stub handler that answers by "METHOD path" and answers a request no
// handler is given for 404, as GitHub does.
func routes(handlers map[string]http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h, ok := handlers[r.Method+" "+r.URL.Path]; ok {
			h(w, r)
			return
		}
		respond(http.StatusNotFound, `{"message":"Not Found"}`)(w, r)
	}
}

// issueBody is a GitHub issue of acme/widgets as the API answers one.
func issueBody(n int, title, body string, labels ...string) string {
	names := make([]string, len(labels))
	for i, l := range labels {
		names[i] = fmt.Sprintf(`{"name":%q}`, l)
	}
	return fmt.Sprintf(`{"number":%d,"title":%q,"body":%q,"state":"open","html_url":"https://github.com/acme/widgets/issues/%d",`+
		`"user":{"login":"hub"},"labels":[%s],"created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:05Z"}`,
		n, title, body, n, strings.Join(names, ","))
}

// jsonOf decodes the JSON in s into a map.
func jsonOf(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, s)
	}
	return m
}

// TS-31-1: A request that fails several checks receives the status of the earliest of
// them, in the order no credential, no forge:write, no such workspace, not active, an
// invalid request, an unresolvable client, a body over the limit; the forge sees nothing.
// Requirement: 31-REQ-1.1
func TestWriteChecksOrder_TS_31_1(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	addWorkspace(t, e.db, "retired", func(r *workspaceRow) { r.status = "archived"; r.owner = otherID })
	addWorkspace(t, e.db, "unknown", func(r *workspaceRow) { r.gitURL = "https://git.internal.example/acme/widgets" })
	big := fmt.Sprintf(`{"title":"t","body":%q}`, strings.Repeat("x", 65537))

	for _, tc := range []struct {
		name string
		slug string
		auth *apikit.AuthInfo
		body string
		want int
	}{
		{"no credential and a bad body", "widgets", nil, `{not json`, http.StatusUnauthorized},
		{"a forge:read PAT and a missing workspace", "ghost", patAuth("forge:read"), `{"title":"t"}`, http.StatusForbidden},
		{"a non-owner and an archived workspace", "retired", apiKeyAuth(), `{"title":"t"}`, http.StatusNotFound},
		{"malformed JSON and an unknown forge", "unknown", apiKeyAuth(), `{not json`, http.StatusBadRequest},
		{"an unknown forge and an oversized body", "unknown", apiKeyAuth(), big, http.StatusUnprocessableEntity},
	} {
		rec := e.send(http.MethodPost, "/workspaces/"+tc.slug+"/issues", tc.auth, tc.body)
		if rec.Code != tc.want {
			t.Errorf("%s: status = %d; want %d\n%s", tc.name, rec.Code, tc.want, rec.Body)
		}
	}
	if n := e.stub.count(); n != 0 {
		t.Errorf("the forge stub received %d requests; want none", n)
	}
	if n := len(em.recorded()); n != 0 {
		t.Errorf("%d events recorded; want none", n)
	}

	// The last check is the limit, and it is there: the same oversized body on a forge
	// that resolves answers 413, so the 422 above was the client, not a missing check.
	rec := e.send(http.MethodPost, "/workspaces/widgets/issues", apiKeyAuth(), big)
	if got := decodeError(t, rec); rec.Code != http.StatusRequestEntityTooLarge || got.ErrorType != "body_too_large" {
		t.Errorf("an oversized body on a resolvable forge: %d %+v; want 413 body_too_large", rec.Code, got)
	}
	if n := e.stub.count(); n != 0 {
		t.Errorf("the forge stub received %d requests; want none", n)
	}
}

// TS-31-4: API keys and admin tokens pass the scope check without holding permissions.
// Requirement: 31-REQ-1.4
func TestCreateIssue_APIKeyAndAdminToken_TS_31_4(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/issues": respond(http.StatusCreated, issueBody(7, "t", "", "")),
	}))

	credentials := []*apikit.AuthInfo{{UserID: ownerID, CredentialType: "api_key"}, adminAuth(), patAuth("forge:write")}
	for _, auth := range credentials {
		before := e.stub.count()
		rec := e.send(http.MethodPost, "/workspaces/widgets/issues", auth, `{"title":"t"}`)
		if rec.Code != http.StatusCreated {
			t.Errorf("%s: status = %d; want 201\n%s", auth.CredentialType, rec.Code, rec.Body)
		}
		if got := e.stub.count() - before; got != 1 {
			t.Errorf("%s: the forge received %d requests; want 1", auth.CredentialType, got)
		}
	}

	// The event names the caller: its user and its kind of credential.
	events := em.recorded()
	if len(events) != len(credentials) {
		t.Fatalf("%d events recorded; want %d", len(events), len(credentials))
	}
	for i, auth := range credentials {
		if events[i].ActorID != auth.UserID || events[i].ActorType != auth.CredentialType {
			t.Errorf("event %d: actor %s (%s); want %s (%s)", i, events[i].ActorID, events[i].ActorType, auth.UserID, auth.CredentialType)
		}
	}
}

// TS-31-8: A write ignores clone_status and the workspace lock.
// Requirement: 31-REQ-1.8
func TestCreateIssue_IgnoresCloneStatusAndLock_TS_31_8(t *testing.T) {
	e := newWriteEnv(t)
	addWorkspace(t, e.db, "pending", func(r *workspaceRow) { r.cloneStatus = "pending" })
	e.setVar("pending", "FORGE_API_URL", e.stub.URL)
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/issues": respond(http.StatusCreated, issueBody(7, "t", "")),
	}))

	unlock := wslock.Lock("pending")
	defer unlock()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- e.send(http.MethodPost, "/workspaces/pending/issues", apiKeyAuth(), `{"title":"t"}`) }()
	select {
	case rec := <-done:
		if rec.Code != http.StatusCreated {
			t.Errorf("status = %d; want 201, as for a ready workspace\n%s", rec.Code, rec.Body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the write waited for the workspace lock")
	}
}

// TS-31-9: Creating an issue sends the workspace's GIT_PAT and never the caller's bearer,
// ignores unknown fields, answers 201 with the issue as issuex encodes it and records one
// hub.forge.issue.create event.
// Requirement: 31-REQ-2.1
func TestCreateIssue_SendsTheWorkspaceCredential_TS_31_9(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/issues": respond(http.StatusCreated, issueBody(7, "t", "b", "af:triage")),
	}))

	rec := e.sendHeader(http.MethodPost, "/workspaces/widgets/issues", apiKeyAuth(),
		`{"title":"t","body":"b","labels":["af:triage"],"assignees":["octocat"],"extra":1}`,
		http.Header{"Authorization": {"Bearer caller-bearer-31"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201\n%s", rec.Code, rec.Body)
	}

	// The body is the issue as issuex encodes it: decoding and encoding it again changes nothing.
	var issue issuex.Issue
	if err := json.Unmarshal(rec.Body.Bytes(), &issue); err != nil {
		t.Fatal(err)
	}
	again, _ := json.Marshal(issue)
	if strings.TrimSpace(rec.Body.String()) != string(again) {
		t.Errorf("body\n%s\nis not an issuex.Issue as encoded\n%s", rec.Body, again)
	}
	if issue.Number != 7 || issue.Title != "t" || issue.Body != "b" || len(issue.Labels) != 1 || issue.Labels[0] != "af:triage" {
		t.Errorf("issue = %+v; want number 7, title t, body b, label af:triage", issue)
	}

	got := e.stub.received()
	if len(got) != 1 || got[0].Method != http.MethodPost || got[0].Path != "/repos/acme/widgets/issues" {
		t.Fatalf("the forge received %+v; want one POST /repos/acme/widgets/issues", got)
	}
	if token := bearer(got[0]); token != "origin-token" {
		t.Errorf("the forge saw the token %q; want the workspace's origin-token", token)
	}
	if dump := fmt.Sprint(got[0].Header, got[0].Body, got[0].Query); strings.Contains(dump, "caller-bearer-31") {
		t.Errorf("the forge saw the caller's bearer: %s", dump)
	}
	sent := jsonOf(t, got[0].Body)
	if sent["title"] != "t" || sent["body"] != "b" || fmt.Sprint(sent["labels"]) != "[af:triage]" || fmt.Sprint(sent["assignees"]) != "[octocat]" || len(sent) != 4 {
		t.Errorf("the forge received %s; want title, body, labels and assignees only", got[0].Body)
	}

	events := em.recorded()
	if len(events) != 1 {
		t.Fatalf("%d events recorded; want 1", len(events))
	}
	ev := events[0]
	if ev.EventType != audit.EventForgeIssueCreate || ev.ResourceType != "issue" || ev.ResourceID != "7" || ev.Action != "create" ||
		ev.Workspace != "widgets" || ev.ActorID != ownerID || ev.ActorType != "api_key" {
		t.Errorf("event = %+v; want hub.forge.issue.create on issue 7 in widgets by the caller", ev)
	}
	if ev.Metadata["target"] != "origin" || ev.Metadata["ok"] != true || ev.Metadata["number"] != 7 ||
		ev.Metadata["url"] != "https://github.com/acme/widgets/issues/7" || len(ev.Metadata) != 4 {
		t.Errorf("metadata = %+v; want target origin, ok true, number 7 and the issue url", ev.Metadata)
	}
}

// TS-31-10: A POST /issues body that breaks a field rule answers 400 naming the field,
// and the forge receives nothing and nothing is recorded.
// Requirement: 31-REQ-2.2
func TestCreateIssue_InvalidBodies_TS_31_10(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	names := func(n int, name string) string {
		items := make([]string, n)
		for i := range items {
			items[i] = fmt.Sprintf("%q", fmt.Sprintf("%s-%d", name, i))
		}
		return strings.Join(items, ",")
	}

	for _, tc := range []struct{ name, body, field string }{
		{"an absent title", `{"body":"b"}`, "title"},
		{"a blank title", `{"title":"  "}`, "title"},
		{"a title that is no string", `{"title":5}`, "title"},
		{"101 labels", `{"title":"t","labels":[` + names(101, "l") + `]}`, "labels"},
		{"a blank label", `{"title":"t","labels":["a","  "]}`, "labels"},
		{"labels that are no list", `{"title":"t","labels":"a"}`, "labels"},
		{"101 assignees", `{"title":"t","assignees":[` + names(101, "a") + `]}`, "assignees"},
		{"a blank assignee", `{"title":"t","assignees":[""]}`, "assignees"},
		{"a body that is no object", `["t"]`, "JSON object"},
		{"a body that is no JSON", `{not json`, "JSON"},
	} {
		rec := e.send(http.MethodPost, "/workspaces/widgets/issues", apiKeyAuth(), tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d; want 400\n%s", tc.name, rec.Code, rec.Body)
			continue
		}
		if msg := decodeError(t, rec).Message; !strings.Contains(msg, tc.field) {
			t.Errorf("%s: message = %q; want it to name %q", tc.name, msg, tc.field)
		}
	}
	if n := e.stub.count(); n != 0 {
		t.Errorf("the forge stub received %d requests; want none", n)
	}
	if n := len(em.recorded()); n != 0 {
		t.Errorf("%d events recorded; want none", n)
	}

	// 100 labels are allowed: the limit is a count, and it is not off by one.
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/issues": respond(http.StatusCreated, issueBody(7, "t", "")),
	}))
	rec := e.send(http.MethodPost, "/workspaces/widgets/issues", apiKeyAuth(), `{"title":"t","labels":[`+names(100, "l")+`],"assignees":[`+names(100, "a")+`]}`)
	if rec.Code != http.StatusCreated {
		t.Errorf("100 labels and 100 assignees: status = %d; want 201\n%s", rec.Code, rec.Body)
	}
}

// A create the forge refuses is answered with the mapped error and recorded as a failure:
// ok false, the error_type hub answered with, no number and no resource id.
// Requirement: 31-REQ-9.1, 31-REQ-9.3
func TestCreateIssue_ForgeFailureIsRecorded(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/issues": respond(http.StatusForbidden, `{"message":"Resource not accessible by personal access token"}`),
	}))

	rec := e.send(http.MethodPost, "/workspaces/widgets/issues", apiKeyAuth(), `{"title":"secret title","body":"secret body"}`)
	if got := decodeError(t, rec); rec.Code != http.StatusBadGateway || got.ErrorType != "forge_forbidden" {
		t.Fatalf("answer = %d %+v; want 502 forge_forbidden", rec.Code, got)
	}
	events := em.recorded()
	if len(events) != 1 {
		t.Fatalf("%d events recorded; want 1", len(events))
	}
	ev := events[0]
	if ev.EventType != audit.EventForgeIssueCreate || ev.ResourceID != "" {
		t.Errorf("event = %+v; want hub.forge.issue.create with an empty resource id", ev)
	}
	want := map[string]any{"target": "origin", "ok": false, "error_type": "forge_forbidden"}
	if fmt.Sprint(ev.Metadata) != fmt.Sprint(want) {
		t.Errorf("metadata = %v; want %v", ev.Metadata, want)
	}
}

// The url of the event is the forge's, with any userinfo taken out.
// Requirement: 31-REQ-9.3
func TestCreateIssue_EventURLHasNoUserinfo(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	withUserinfo := strings.Replace(issueBody(7, "t", ""), "https://github.com/", "https://bot:hunter2@github.com/", 1)
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/issues": respond(http.StatusCreated, withUserinfo),
	}))

	if rec := e.send(http.MethodPost, "/workspaces/widgets/issues", apiKeyAuth(), `{"title":"t"}`); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201\n%s", rec.Code, rec.Body)
	}
	events := em.recorded()
	if len(events) != 1 {
		t.Fatalf("%d events recorded; want 1", len(events))
	}
	if url := events[0].Metadata["url"]; url != "https://github.com/acme/widgets/issues/7" {
		t.Errorf("url = %v; want the issue url without userinfo", url)
	}
}
