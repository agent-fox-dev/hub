package forge

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/agent-fox-dev/aflib/issuex"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// gitlabIssueBody is a GitLab issue of acme/widgets as the API answers one.
func gitlabIssueBody(iid int, title, description, state string, labels ...string) string {
	names := make([]string, len(labels))
	for i, l := range labels {
		names[i] = fmt.Sprintf("%q", l)
	}
	return fmt.Sprintf(`{"id":%d,"iid":%d,"title":%q,"description":%q,"state":%q,"web_url":"https://gitlab.com/acme/widgets/-/issues/%d",`+
		`"author":{"username":"hub"},"labels":[%s],"created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:05Z"}`,
		1000+iid, iid, title, description, state, iid, strings.Join(names, ","))
}

// newGitLabWriteEnv is newWriteEnv for the workspace lab, a standard workspace on
// gitlab.com/acme/widgets whose API is the forge stub.
func newGitLabWriteEnv(t *testing.T, mods ...func(*Config)) *env {
	t.Helper()
	e := newEnv(t, mods...)
	addWorkspace(t, e.db, "lab", func(r *workspaceRow) { r.gitURL = "https://gitlab.com/acme/widgets" })
	e.setVar("lab", "FORGE_API_URL", e.stub.URL)
	return e
}

// lastBody is the body of the last request the stub received, decoded.
func lastBody(t *testing.T, s *forgeStub) map[string]any {
	t.Helper()
	got := s.received()
	if len(got) == 0 {
		t.Fatal("the forge received no request")
	}
	return jsonOf(t, got[len(got)-1].Body)
}

// TS-31-11: PATCH sends the forge only the fields present and non-null, an empty body
// clears the body and an empty list clears the set, and the answer is the issue as the
// forge returns it.
// Requirement: 31-REQ-2.3
func TestUpdateIssue_SendsOnlyPresentFields_TS_31_11(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"PATCH /repos/acme/widgets/issues/42": respond(http.StatusOK, issueBody(42, "x", "", "af:fix")),
	}))

	rec := e.send(http.MethodPatch, "/workspaces/widgets/issues/42", apiKeyAuth(), `{"body":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200\n%s", rec.Code, rec.Body)
	}
	var issue issuex.Issue
	if err := json.Unmarshal(rec.Body.Bytes(), &issue); err != nil {
		t.Fatal(err)
	}
	again, _ := json.Marshal(issue)
	if strings.TrimSpace(rec.Body.String()) != string(again) || issue.Number != 42 || issue.Title != "x" {
		t.Errorf("body %s; want the issue 42 as issuex encodes it", rec.Body)
	}
	first := e.stub.received()
	if len(first) != 1 || first[0].Method != http.MethodPatch || first[0].Path != "/repos/acme/widgets/issues/42" {
		t.Fatalf("the forge received %+v; want one PATCH /repos/acme/widgets/issues/42", first)
	}
	if token := bearer(first[0]); token != "origin-token" {
		t.Errorf("the update carried the token %q; want the workspace's origin-token", token)
	}
	if ua := first[0].Header.Get("User-Agent"); ua != "af-hub/"+apikit.Version {
		t.Errorf("the update carried the user agent %q; want af-hub/%s", ua, apikit.Version)
	}
	if first[0].Header.Get("Accept") != "application/vnd.github+json" || first[0].Header.Get("Content-Type") != "application/json" {
		t.Errorf("the update carried Accept %q and Content-Type %q", first[0].Header.Get("Accept"), first[0].Header.Get("Content-Type"))
	}
	if got := jsonOf(t, first[0].Body); len(got) != 1 || got["body"] != "" {
		t.Errorf("the forge received %s; want exactly {\"body\":\"\"}", first[0].Body)
	}

	rec = e.send(http.MethodPatch, "/workspaces/widgets/issues/42", apiKeyAuth(),
		`{"title":"x","state":"closed","labels":[],"assignees":["a"],"body":null}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200\n%s", rec.Code, rec.Body)
	}
	sent := lastBody(t, e.stub)
	labels, isList := sent["labels"].([]any)
	if _, hasBody := sent["body"]; hasBody || len(sent) != 4 || sent["title"] != "x" || sent["state"] != "closed" ||
		!isList || len(labels) != 0 || fmt.Sprint(sent["assignees"]) != "[a]" {
		t.Errorf("the forge received %v; want exactly title, state, labels [] and assignees [a]", sent)
	}

	events := em.recorded()
	if len(events) != 2 {
		t.Fatalf("%d events recorded; want 2", len(events))
	}
	for i, fields := range []string{"[body]", "[assignees labels state title]"} {
		ev := events[i]
		if ev.EventType != audit.EventForgeIssueUpdate || ev.ResourceType != "issue" || ev.ResourceID != "42" || ev.Action != "update" {
			t.Errorf("event %d = %+v; want hub.forge.issue.update on issue 42", i, ev)
		}
		if got := fmt.Sprint(ev.Metadata["fields"]); got != fields || ev.Metadata["number"] != 42 || ev.Metadata["ok"] != true {
			t.Errorf("event %d metadata = %v; want fields %s, number 42, ok true", i, ev.Metadata, fields)
		}
	}
}

// TS-31-12: A PATCH body that breaks a field rule answers 400 naming the field.
// Requirement: 31-REQ-2.4
func TestUpdateIssue_InvalidBodies_TS_31_12(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	many := func(n int) string {
		items := make([]string, n)
		for i := range items {
			items[i] = fmt.Sprintf(`"n%d"`, i)
		}
		return strings.Join(items, ",")
	}

	for _, tc := range []struct{ name, body, field string }{
		{"a blank title", `{"title":"  "}`, "title"},
		{"an empty title", `{"title":""}`, "title"},
		{"a title that is no string", `{"title":["t"]}`, "title"},
		{"a state of merged", `{"state":"merged"}`, "state"},
		{"an empty state", `{"state":""}`, "state"},
		{"a body that is no string", `{"body":1}`, "body"},
		{"101 labels", `{"labels":[` + many(101) + `]}`, "labels"},
		{"a blank label", `{"labels":["a",""]}`, "labels"},
		{"labels that are no list", `{"labels":"a"}`, "labels"},
		{"a blank assignee", `{"assignees":[" "]}`, "assignees"},
		{"101 assignees", `{"assignees":[` + many(101) + `]}`, "assignees"},
		{"a valid field next to an invalid one", `{"body":"fine","state":"merged"}`, "state"},
		{"a body that is no object", `"title"`, "JSON object"},
	} {
		rec := e.send(http.MethodPatch, "/workspaces/widgets/issues/42", apiKeyAuth(), tc.body)
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
}

// TS-31-13: A PATCH body with nothing to change, {} or only nulls, answers 400 nothing to update.
// Requirement: 31-REQ-2.5
func TestUpdateIssue_NothingToUpdate_TS_31_13(t *testing.T) {
	e := newWriteEnv(t)
	for _, body := range []string{`{}`, `{"title":null,"body":null}`, `{"labels":null,"assignees":null,"state":null}`, `{"extra":1}`} {
		rec := e.send(http.MethodPatch, "/workspaces/widgets/issues/42", apiKeyAuth(), body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d; want 400\n%s", body, rec.Code, rec.Body)
			continue
		}
		if got := decodeError(t, rec).Message; got != "nothing to update" {
			t.Errorf("%s: message = %q; want %q", body, got, "nothing to update")
		}
	}
	if n := e.stub.count(); n != 0 {
		t.Errorf("the forge stub received %d requests; want none", n)
	}
}

// The number in the path is a positive decimal integer on every numbered write route; the
// request is refused with 400 before the forge is asked.
// Requirement: 31-REQ-1.1
func TestWriteNumbers_Malformed(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	numbered := map[string]bool{"update issue": true, "close issue": true, "comment on issue": true, "add labels": true, "remove label": true, "comment on pull request": true}
	for _, route := range writeRouteCases {
		if !numbered[route.name] {
			continue
		}
		for _, n := range []string{"0", "abc", "-1", "1.5", "99999999999999999999"} {
			path := strings.Replace(route.url("widgets"), "/42", "/"+n, 1)
			rec := e.send(route.method, path, apiKeyAuth(), route.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s %s: status = %d; want 400", route.method, path, rec.Code)
			}
		}
	}
	if n := e.stub.count(); n != 0 {
		t.Errorf("the forge stub received %d requests; want none", n)
	}
	if n := len(em.recorded()); n != 0 {
		t.Errorf("%d events recorded; want none", n)
	}
}

// TS-31-14: Issues are created and updated on GitHub and on GitLab alike, and :n is the
// issue number on one and the project's iid on the other.
// Requirement: 31-REQ-2.6
func TestIssueCreateAndUpdate_BothForges_TS_31_14(t *testing.T) {
	gh := newWriteEnv(t)
	gh.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/issues":     respond(http.StatusCreated, issueBody(5, "t", "b")),
		"PATCH /repos/acme/widgets/issues/42": respond(http.StatusOK, issueBody(42, "t", "new")),
	}))
	gl := newGitLabWriteEnv(t)
	gl.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /api/v4/projects/acme/widgets/issues":   respond(http.StatusCreated, gitlabIssueBody(5, "t", "b", "opened")),
		"PUT /api/v4/projects/acme/widgets/issues/42": respond(http.StatusOK, gitlabIssueBody(42, "t", "new", "opened")),
	}))

	for _, tc := range []struct {
		forge string
		env   *env
		slug  string
	}{{"github", gh, "widgets"}, {"gitlab", gl, "lab"}} {
		create := tc.env.send(http.MethodPost, "/workspaces/"+tc.slug+"/issues", apiKeyAuth(), `{"title":"t","body":"b"}`)
		if create.Code != http.StatusCreated {
			t.Errorf("%s create: status = %d; want 201\n%s", tc.forge, create.Code, create.Body)
		}
		update := tc.env.send(http.MethodPatch, "/workspaces/"+tc.slug+"/issues/42", apiKeyAuth(), `{"body":"new"}`)
		if update.Code != http.StatusOK {
			t.Fatalf("%s update: status = %d; want 200\n%s", tc.forge, update.Code, update.Body)
		}
		var issue issuex.Issue
		if err := json.Unmarshal(update.Body.Bytes(), &issue); err != nil || issue.Number != 42 || issue.Body != "new" || issue.State != "open" {
			t.Errorf("%s update: body %s; want the open issue 42 with the new body (%v)", tc.forge, update.Body, err)
		}
	}

	if got := gh.stub.received(); len(got) != 2 || got[1].Method != http.MethodPatch || got[1].Path != "/repos/acme/widgets/issues/42" {
		t.Errorf("GitHub received %+v; want the create, then PATCH issue 42", got)
	}
	got := gl.stub.received()
	if len(got) != 2 || got[1].Method != http.MethodPut || got[1].Path != "/api/v4/projects/acme/widgets/issues/42" {
		t.Fatalf("GitLab received %+v; want the create, then PUT iid 42", got)
	}
	for _, r := range got {
		if r.Header.Get("PRIVATE-TOKEN") != "origin-token" {
			t.Errorf("GitLab %s %s carried the token %q; want origin-token", r.Method, r.Path, r.Header.Get("PRIVATE-TOKEN"))
		}
	}
	if got[1].EscapedPath != "/api/v4/projects/acme%2Fwidgets/issues/42" {
		t.Errorf("GitLab was sent %s; want the project as one escaped segment", got[1].EscapedPath)
	}
	if sent := jsonOf(t, got[1].Body); len(sent) != 1 || sent["description"] != "new" {
		t.Errorf("GitLab received %s; want only the description", got[1].Body)
	}

	// GitLab's names for the fields, and for clearing them.
	if rec := gl.send(http.MethodPatch, "/workspaces/lab/issues/42", apiKeyAuth(),
		`{"title":"x","body":"","state":"closed","labels":[]}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200\n%s", rec.Code, rec.Body)
	}
	sent := lastBody(t, gl.stub)
	if len(sent) != 4 || sent["title"] != "x" || sent["description"] != "" || sent["state_event"] != "close" || sent["labels"] != "" {
		t.Errorf("GitLab received %v; want title, an empty description, state_event close and an empty label list", sent)
	}
	if rec := gl.send(http.MethodPatch, "/workspaces/lab/issues/42", apiKeyAuth(), `{"state":"open","labels":["a","b"]}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200\n%s", rec.Code, rec.Body)
	}
	sent = lastBody(t, gl.stub)
	if len(sent) != 2 || sent["state_event"] != "reopen" || sent["labels"] != "a,b" {
		t.Errorf("GitLab received %v; want state_event reopen and labels a,b", sent)
	}
}

// The request hub sends itself for an update fails like any call to the forge: the status
// and error_type of the mapping, the forge's message, Retry-After passed through, and one
// event that says how it ended. The call is made once.
// Requirement: 31-REQ-8.1, 31-REQ-8.2
func TestUpdateIssue_ForgeFailures(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		header     string
		body       string
		wantStatus int
		wantType   string
		wantText   string
	}{
		{"401", 401, "", `{"message":"Bad credentials"}`, 502, "forge_unauthenticated", "Bad credentials"},
		{"403", 403, "", `{"message":"Resource not accessible"}`, 502, "forge_forbidden", "Resource not accessible"},
		{"404", 404, "", `{"message":"Not Found"}`, 404, "forge_not_found", "Not Found"},
		{"422", 422, "", `{"message":"Validation Failed","errors":[{"resource":"Issue","field":"title","code":"missing_field"}]}`, 422, "forge_rejected", "Validation Failed"},
		{"429", 429, "30", `{"message":"rate limited"}`, 429, "forge_rate_limited", "rate limited"},
		{"500", 500, "", `not json`, 502, "forge_error", "Internal Server Error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			em := &recordingEmitter{}
			e := newWriteEnv(t, withEmitter(em))
			e.stub.setHandler(func(w http.ResponseWriter, r *http.Request) {
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			rec := e.send(http.MethodPatch, "/workspaces/widgets/issues/42", apiKeyAuth(), `{"title":"x"}`)
			got := decodeError(t, rec)
			if rec.Code != tc.wantStatus || got.ErrorType != tc.wantType || !strings.Contains(got.Message, tc.wantText) {
				t.Errorf("answer = %d %+v; want %d %s mentioning %q", rec.Code, got, tc.wantStatus, tc.wantType, tc.wantText)
			}
			if tc.status == 429 && rec.Header().Get("Retry-After") != "31" {
				t.Errorf("Retry-After = %q; want 31, the forge's 30 seconds and the second issuex adds", rec.Header().Get("Retry-After"))
			}
			if n := e.stub.count(); n != 1 {
				t.Errorf("the forge received %d requests; want 1, hub does not retry", n)
			}
			events := em.recorded()
			if len(events) != 1 || events[0].Metadata["ok"] != false || events[0].Metadata["error_type"] != tc.wantType ||
				fmt.Sprint(events[0].Metadata["fields"]) != "[title]" {
				t.Errorf("events = %+v; want one failed update with error_type %s", events, tc.wantType)
			}
		})
	}
}

// An update that cannot reach the forge is answered forge_unreachable, once.
// Requirement: 31-REQ-8.2
func TestUpdateIssue_Unreachable(t *testing.T) {
	counter := &countingTransport{}
	e := newWriteEnv(t, func(cfg *Config) { cfg.Transport = counter })
	rec := e.send(http.MethodPatch, "/workspaces/widgets/issues/42", apiKeyAuth(), `{"title":"x"}`)
	if got := decodeError(t, rec); rec.Code != http.StatusBadGateway || got.ErrorType != "forge_unreachable" {
		t.Errorf("answer = %d %+v; want 502 forge_unreachable", rec.Code, got)
	}
	if n := counter.n.Load(); n != 1 {
		t.Errorf("the transport saw %d requests; want 1", n)
	}
}

// GitLab's errors, the message an object for a validation, are forwarded as text.
// Requirement: 31-REQ-8.1
func TestUpdateIssue_GitLabValidationMessage(t *testing.T) {
	e := newGitLabWriteEnv(t)
	e.stub.setHandler(respond(http.StatusBadRequest, `{"message":{"title":["is too long"]}}`))
	rec := e.send(http.MethodPatch, "/workspaces/lab/issues/42", apiKeyAuth(), `{"title":"x"}`)
	if got := decodeError(t, rec); rec.Code != http.StatusUnprocessableEntity || got.ErrorType != "forge_rejected" || !strings.Contains(got.Message, "is too long") {
		t.Errorf("answer = %d %+v; want 422 forge_rejected carrying the forge's message", rec.Code, got)
	}
}

// Every request of a write goes to one API root: the requests issuex sends and the ones hub
// sends itself, also when the hub's environment names another API for the host, which
// issuex would honour for a client it is not given a URL.
// Requirement: 31-REQ-8.1
func TestWriteRequests_GoToOneAPIRoot(t *testing.T) {
	var urls []string
	var mu sync.Mutex
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		urls = append(urls, r.Method+" "+r.URL.String())
		mu.Unlock()
		body := issueBody(1, "t", "")
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	e := newEnv(t, func(cfg *Config) { cfg.Transport = transport })
	addWorkspace(t, e.db, "ghe", func(r *workspaceRow) { r.gitURL = "https://github.corp.example/acme/widgets" })
	t.Setenv("GITHUB_API_URL", "https://api.github.corp.example")

	if rec := e.send(http.MethodPost, "/workspaces/ghe/issues", apiKeyAuth(), `{"title":"t"}`); rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("create: status = %d\n%s", rec.Code, rec.Body)
	}
	if rec := e.send(http.MethodPatch, "/workspaces/ghe/issues/1", apiKeyAuth(), `{"title":"x"}`); rec.Code != http.StatusOK {
		t.Fatalf("update: status = %d\n%s", rec.Code, rec.Body)
	}
	want := []string{
		"POST https://github.corp.example/api/v3/repos/acme/widgets/issues",
		"PATCH https://github.corp.example/api/v3/repos/acme/widgets/issues/1",
	}
	if fmt.Sprint(urls) != fmt.Sprint(want) {
		t.Errorf("the requests went to\n%v\nwant\n%v", urls, want)
	}
}

// The issue update as each forge takes it, built directly: a list that is set is a list in
// the payload, an empty one included, also when it is a nil slice.
// Requirement: 31-REQ-2.3
func TestIssueUpdatePayloads(t *testing.T) {
	var nilList []string
	title, state := "t", "open"
	update := issueUpdate{Title: &title, State: &state, Labels: &nilList, Assignees: &nilList}

	if b, _ := json.Marshal(update.githubPayload()); string(b) != `{"assignees":[],"labels":[],"state":"open","title":"t"}` {
		t.Errorf("GitHub payload = %s", b)
	}
	if b, _ := json.Marshal(update.gitlabPayload()); string(b) != `{"labels":"","state_event":"reopen","title":"t"}` {
		t.Errorf("GitLab payload = %s", b)
	}
	if got := fmt.Sprint(update.fields()); got != "[assignees labels state title]" {
		t.Errorf("fields = %s; want them sorted", got)
	}
	if got := (issueUpdate{}).fields(); len(got) != 0 {
		t.Errorf("fields of an empty update = %v; want none", got)
	}
}
