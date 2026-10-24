package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/aflib/issuex"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/txsvc/apikit"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

// A write applies its checks in order and stops at the first that fails: no credential,
// no forge:write, no such workspace, archived, an invalid request (the body is not read
// before), an unresolvable client, a body over the forge's limit; none of them reaches the
// forge or records an event, and the valid request does both.
func TestWrites_ChecksInOrderAndRefusalsSendNothing(t *testing.T) {
	em := &recordingEmitter{}
	e := widgetsEnv(t, withEmitter(em))
	e.stub.setHandler(githubWrites())
	addWorkspace(t, e.db, "retired", archived)
	addWorkspace(t, e.db, "unknown", withGitURL("https://git.internal.example/acme/widgets"))
	big := fmt.Sprintf(`{"title":"t","body":%q}`, strings.Repeat("x", 65537))
	for _, tc := range []struct {
		name string
		slug string
		auth *apikit.AuthInfo
		body string
		want int
	}{
		{"no credential and a bad body", "widgets", nil, `{not json`, 401},
		{"a forge:read PAT and a missing workspace", "ghost", patAuth("forge:read"), `{"title":"t"}`, 403},
		{"a stranger's archived workspace", "retired", testutil.User(otherID), `{not json`, 404},
		{"an archived workspace and a bad body", "retired", apiKeyAuth(), `{not json`, 400},
		{"a bad target and a bad body", "widgets?target=upstream", apiKeyAuth(), `{not json`, 400},
		{"malformed JSON and an unknown forge", "unknown", apiKeyAuth(), `{not json`, 400},
		{"an unknown forge and an oversized body", "unknown", apiKeyAuth(), big, 422},
		{"an oversized body", "widgets", apiKeyAuth(), big, 413},
	} {
		slug, query, _ := strings.Cut(tc.slug, "?")
		path := "/workspaces/" + slug + "/issues"
		if query != "" {
			path += "?" + query
		}
		if rec := e.send(http.MethodPost, path, tc.auth, tc.body); rec.Code != tc.want {
			t.Errorf("%s: status = %d; want %d\n%s", tc.name, rec.Code, tc.want, rec.Body)
		}
	}
	broken := widgetsEnv(t, withEmitter(em))
	broken.originErr = errors.New("secrets store is down")
	for _, r := range writeRoutes {
		for _, tc := range []struct {
			name string
			send func() int
			want int
		}{
			{"a target the workspace does not have", func() int { return e.send(r.method, r.url("widgets")+"?target=upstream", apiKeyAuth(), r.body).Code }, 400},
			{"a forge hub cannot tell", func() int { return e.sendRoute(r, "unknown", apiKeyAuth()).Code }, 422},
			{"a credential hub cannot read", func() int { return broken.sendRoute(r, "widgets", apiKeyAuth()).Code }, 500},
		} {
			if got := tc.send(); got != tc.want {
				t.Errorf("%s with %s: status = %d; want %d", r.name, tc.name, got, tc.want)
			}
		}
	}
	if n := e.stub.count() + broken.stub.count() + len(em.recorded()); n != 0 {
		t.Fatalf("%d forge requests and events for refused requests; want none", n)
	}
	for _, r := range writeRoutes {
		if rec := e.sendRoute(r, "widgets", apiKeyAuth()); rec.Code >= 300 {
			t.Errorf("%s: status = %d; want it to succeed\n%s", r.name, rec.Code, rec.Body)
		}
	}
	if e.stub.count() == 0 || len(em.recorded()) != len(writeRoutes) {
		t.Errorf("valid requests sent %d and recorded %d; want requests and %d events", e.stub.count(), len(em.recorded()), len(writeRoutes))
	}
}

// The number in the path of a numbered write route is a positive decimal integer.
func TestWrites_MalformedNumberIs400(t *testing.T) {
	e := widgetsEnv(t)
	for _, name := range []string{"update issue", "close issue", "comment on issue", "add labels", "remove label", "comment on pull request"} {
		r := namedWrite(name)
		for _, n := range []string{"0", "abc", "-1"} {
			if rec := e.send(r.method, strings.Replace(r.url("widgets"), "/42", "/"+n, 1), apiKeyAuth(), r.body); rec.Code != http.StatusBadRequest {
				t.Errorf("%s with n=%s: status = %d; want 400", name, n, rec.Code)
			}
		}
	}
	if n := e.stub.count(); n != 0 {
		t.Errorf("the forge received %d requests; want none", n)
	}
}

// A body that breaks a rule of its route is 400 naming the field, before the forge is asked.
func TestWrites_InvalidBodies(t *testing.T) {
	e := widgetsEnv(t)
	names := func(n int) string {
		items := make([]string, n)
		for i := range items {
			items[i] = fmt.Sprintf(`"n%d"`, i)
		}
		return strings.Join(items, ",")
	}
	for _, tc := range []struct{ route, body, field string }{
		{"create issue", `{"body":"b"}`, "title"},
		{"create issue", `{"title":"  "}`, "title"},
		{"create issue", `{"title":"t","body":1}`, "body"},
		{"create issue", `{"title":"t","labels":[` + names(101) + `]}`, "labels"},
		{"create issue", `{"title":"t","labels":["a","  "]}`, "labels"},
		{"create issue", `["t"]`, "JSON object"},
		{"create issue", `{not json`, "JSON object"},
		{"update issue", `{"title":""}`, "title"},
		{"update issue", `{"state":"merged"}`, "state"},
		{"update issue", `{"assignees":[` + names(101) + `]}`, "assignees"},
		{"update issue", `{}`, "nothing to update"},
		{"update issue", `{"title":null,"body":null,"extra":1}`, "nothing to update"},
		{"close issue", `{"comment":5}`, "comment"},
		{"close issue", `[]`, "JSON object"},
		{"comment on issue", `{}`, "body"},
		{"comment on issue", `{"body":"  "}`, "body"},
		{"comment on pull request", `{"body":null}`, "body"},
		{"add labels", `{"labels":["a"]}`, "labels"},
		{"add labels", `[]`, "labels"},
		{"add labels", `[` + names(101) + `]`, "labels"},
		{"add labels", `["  "]`, "labels"},
		{"add labels", ``, "labels"},
		{"ensure label", `{"color":"ff0000"}`, "name"},
		{"ensure label", `{"name":"  "}`, "name"},
		{"ensure label", `{"name":"a","color":"red"}`, "color"},
		{"ensure label", `{"name":"a","color":123456}`, "color"},
		{"open pull request", `{"head":"h","base":"main"}`, "title"},
		{"open pull request", `{"title":"t","head":"  ","base":"main"}`, "head"},
		{"open pull request", `{"title":"t","head":"h","base":"main","draft":"yes"}`, "draft"},
		{"open pull request", `{"title":"t","head":"h","base":"main","body":false}`, "body"},
	} {
		r := namedWrite(tc.route)
		rec := e.send(r.method, r.url("widgets"), apiKeyAuth(), tc.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(testutil.Error(t, rec).Message, tc.field) {
			t.Errorf("%s %s: %d %s; want 400 naming %q", tc.route, tc.body, rec.Code, rec.Body, tc.field)
		}
	}
	if rec := e.send(http.MethodDelete, "/workspaces/widgets/issues/42/labels/%20", apiKeyAuth(), ""); rec.Code != http.StatusBadRequest {
		t.Errorf("a blank label name: status = %d; want 400", rec.Code)
	}
	if n := e.stub.count(); n != 0 {
		t.Errorf("the forge received %d requests; want none", n)
	}
	// The limits are counts, not off by one.
	e.stub.setHandler(githubWrites())
	if rec := e.send(http.MethodPost, "/workspaces/widgets/issues", apiKeyAuth(), `{"title":"t","labels":[`+names(100)+`],"assignees":[`+names(100)+`]}`); rec.Code != http.StatusCreated {
		t.Errorf("100 labels and assignees: status = %d; want 201", rec.Code)
	}
}

// Creating an issue sends the workspace's token and never the caller's, sends the fields
// as given and ignores unknown ones, and answers 201 with the issue as issuex encodes it.
func TestCreateIssue_SendsTheWorkspaceCredential(t *testing.T) {
	e := widgetsEnv(t)
	e.stub.setHandler(githubWrites())
	rec := e.do(http.MethodPost, "/workspaces/widgets/issues", apiKeyAuth(),
		`{"title":"t","body":"b","labels":["af:triage"],"assignees":["octocat"],"extra":1}`, http.Header{"Authorization": {"Bearer caller-bearer"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d\n%s", rec.Code, rec.Body)
	}
	var issue issuex.Issue
	testutil.Decode(t, rec, &issue)
	if again, _ := json.Marshal(issue); strings.TrimSpace(rec.Body.String()) != string(again) || issue.Number != 7 {
		t.Errorf("body %s is not issue 7 as issuex encodes it", rec.Body)
	}
	got := e.stub.received()
	if len(got) != 1 || got[0].Method != http.MethodPost || got[0].Path != "/repos/acme/widgets/issues" || bearer(got[0]) != "origin-token" {
		t.Fatalf("the forge received %+v; want one POST with the workspace's token", got)
	}
	if dump := fmt.Sprint(got[0].Header); strings.Contains(dump, "caller-bearer") {
		t.Errorf("the forge saw the caller's bearer: %s", dump)
	}
	if sent := jsonOf(t, got[0].Body); sent["title"] != "t" || sent["body"] != "b" || fmt.Sprint(sent["labels"]) != "[af:triage]" || fmt.Sprint(sent["assignees"]) != "[octocat]" || len(sent) != 4 {
		t.Errorf("the forge received %s; want title, body, labels and assignees only", got[0].Body)
	}
}

// An update sends only the fields present and not null, an empty body clears the body, an
// empty list clears the set, and the request hub sends itself carries hub's headers; on
// GitLab the fields have their names, a state is a state event, labels are joined, and
// assignees are not sent at all, so an update of nothing else has nothing to update.
func TestUpdateIssue_SendsOnlyPresentFields(t *testing.T) {
	gh := widgetsEnv(t)
	gh.stub.setHandler(respond(http.StatusOK, issueBody(42, "x", "", "af:fix")))
	rec := gh.send(http.MethodPatch, "/workspaces/widgets/issues/42", apiKeyAuth(), `{"body":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d\n%s", rec.Code, rec.Body)
	}
	var issue issuex.Issue
	testutil.Decode(t, rec, &issue)
	if again, _ := json.Marshal(issue); strings.TrimSpace(rec.Body.String()) != string(again) || issue.Number != 42 || issue.Labels[0] != "af:fix" {
		t.Errorf("body %s is not issue 42 as issuex encodes it", rec.Body)
	}
	first := gh.stub.received()[0]
	if first.Method != http.MethodPatch || first.Path != "/repos/acme/widgets/issues/42" || bearer(first) != "origin-token" || first.Header.Get("User-Agent") != "af-hub/"+apikit.Version ||
		first.Header.Get("Accept") != "application/vnd.github+json" || first.Header.Get("Content-Type") != "application/json" || first.Body != `{"body":""}` {
		t.Errorf("the forge received %+v; want PATCH issue 42 with hub's headers and {\"body\":\"\"}", first)
	}
	gh.send(http.MethodPatch, "/workspaces/widgets/issues/42", apiKeyAuth(), `{"title":"x","state":"closed","labels":[],"assignees":["a"],"body":null}`)
	if sent := lastBody(t, gh.stub); len(sent) != 4 || sent["title"] != "x" || sent["state"] != "closed" || fmt.Sprint(sent["labels"]) != "[]" || fmt.Sprint(sent["assignees"]) != "[a]" {
		t.Errorf("the forge received %v; want title, state, labels [] and assignees [a]", sent)
	}

	gl := gitlabEnv(t)
	gl.stub.setHandler(respond(http.StatusOK, gitlabIssueBody(42, "t", "new", "opened")))
	rec = gl.send(http.MethodPatch, "/workspaces/lab/issues/42", apiKeyAuth(), `{"title":"x","body":"","state":"closed","labels":[]}`)
	testutil.Decode(t, rec, &issue)
	if rec.Code != http.StatusOK || issue.Number != 42 || issue.Body != "new" || issue.State != "open" {
		t.Errorf("%d %s; want the open issue 42 with the new body", rec.Code, rec.Body)
	}
	put := gl.stub.received()[0]
	if put.Method != http.MethodPut || put.EscapedPath != "/api/v4/projects/acme%2Fwidgets/issues/42" || put.Header.Get("PRIVATE-TOKEN") != "origin-token" {
		t.Errorf("GitLab received %+v; want PUT on the escaped project with the private token", put)
	}
	if sent := jsonOf(t, put.Body); len(sent) != 4 || sent["title"] != "x" || sent["description"] != "" || sent["state_event"] != "close" || sent["labels"] != "" {
		t.Errorf("GitLab received %v; want title, an empty description, state_event close and empty labels", sent)
	}
	gl.send(http.MethodPatch, "/workspaces/lab/issues/42", apiKeyAuth(), `{"state":"open","labels":["a","b"],"assignees":["octocat"]}`)
	if sent := lastBody(t, gl.stub); len(sent) != 2 || sent["state_event"] != "reopen" || sent["labels"] != "a,b" {
		t.Errorf("GitLab received %v; want state_event reopen and labels a,b, no assignees", sent)
	}
	rec = gl.send(http.MethodPatch, "/workspaces/lab/issues/42", apiKeyAuth(), `{"assignees":["octocat"]}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(testutil.Error(t, rec).Message, "nothing to update") || gl.stub.count() != 2 {
		t.Errorf("only assignees on GitLab: %d %s; want 400 nothing to update and no request", rec.Code, rec.Body)
	}
}

// A close without a usable comment only closes, a closed issue included; a close with a
// comment posts it first and closes after; a comment the forge refuses leaves the issue
// open; a close that fails after the comment is the close's error. The same on GitLab.
func TestCloseIssue(t *testing.T) {
	e := widgetsEnv(t)
	e.stub.setHandler(githubWrites())
	for _, body := range []string{``, `{}`, `{"comment":"   "}`, `{"comment":null}`} {
		rec := e.send(http.MethodPost, "/workspaces/widgets/issues/42/close", apiKeyAuth(), body)
		var issue issuex.Issue
		testutil.Decode(t, rec, &issue)
		if rec.Code != http.StatusOK || issue.Number != 42 {
			t.Errorf("close %q: %d %s; want 200 and issue 42", body, rec.Code, rec.Body)
		}
	}
	for _, r := range e.stub.received() {
		if r.Method != http.MethodPatch || r.Body != `{"state":"closed"}` {
			t.Errorf("the forge received %s %s %s; want only the PATCH that closes", r.Method, r.Path, r.Body)
		}
	}
	e.stub.forget()
	if rec := e.send(http.MethodPost, "/workspaces/widgets/issues/42/close", apiKeyAuth(), `{"comment":"done"}`); rec.Code != http.StatusOK {
		t.Fatalf("a close with a comment: %d %s", rec.Code, rec.Body)
	}
	got := e.stub.received()
	if len(got) != 2 || got[0].Method != http.MethodPost || !strings.HasSuffix(got[0].Path, "/issues/42/comments") || got[0].Body != `{"body":"done"}` || got[1].Method != http.MethodPatch {
		t.Errorf("the forge received %+v; want the comment, then the close", got)
	}

	e.stub.setHandler(routes(map[string]http.HandlerFunc{"POST /repos/acme/widgets/issues/42/comments": answer(403, "no"), "PATCH /repos/acme/widgets/issues/42": respond(http.StatusOK, closedIssue(42))}))
	e.stub.forget()
	rec := e.send(http.MethodPost, "/workspaces/widgets/issues/42/close", apiKeyAuth(), `{"comment":"done"}`)
	if rec.Code != http.StatusBadGateway || testutil.Error(t, rec).ErrorType != "forge_forbidden" || e.stub.count() != 1 {
		t.Errorf("a refused comment: %d %s after %d requests; want 502 forge_forbidden and no close", rec.Code, rec.Body, e.stub.count())
	}
	e.stub.setHandler(routes(map[string]http.HandlerFunc{"POST /repos/acme/widgets/issues/42/comments": respond(http.StatusCreated, commentBody), "PATCH /repos/acme/widgets/issues/42": answer(500, "boom")}))
	if rec := e.send(http.MethodPost, "/workspaces/widgets/issues/42/close", apiKeyAuth(), `{"comment":"done"}`); rec.Code != http.StatusBadGateway || testutil.Error(t, rec).ErrorType != "forge_error" {
		t.Errorf("a failed close: %d %s; want 502 forge_error", rec.Code, rec.Body)
	}

	gl := gitlabEnv(t)
	gl.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /api/v4/projects/acme/widgets/issues/42/notes": respond(http.StatusCreated, `{"id":5,"body":"done","web_url":"https://gitlab.com/acme/widgets/-/issues/42#note_5"}`),
		"PUT /api/v4/projects/acme/widgets/issues/42":        respond(http.StatusOK, gitlabIssueBody(42, "t", "", "closed")),
	}))
	rec = gl.send(http.MethodPost, "/workspaces/lab/issues/42/close", apiKeyAuth(), `{"comment":"done"}`)
	got = gl.stub.received()
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"closed"`) || len(got) != 2 || !strings.HasSuffix(got[0].Path, "/notes") || got[1].Body != `{"state_event":"close"}` {
		t.Errorf("GitLab: %d %s after %+v; want the note, then state_event close", rec.Code, rec.Body, got)
	}
}

// A comment on an issue or a pull request is 201 {"url": ...} with the URL of a comment on
// the conversation; on GitLab it is a note on the merge request, not the issue.
func TestComment_AnswersTheURL(t *testing.T) {
	e := widgetsEnv(t)
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/issues/42/comments": respond(http.StatusCreated, commentBody),
		"POST /repos/acme/widgets/issues/5/comments":  respond(http.StatusCreated, `{"id":9,"html_url":"https://github.com/acme/widgets/pull/5#issuecomment-9"}`),
	}))
	for path, want := range map[string]string{"/issues/42/comments": commentURL, "/pulls/5/comments": "https://github.com/acme/widgets/pull/5#issuecomment-9"} {
		rec := e.send(http.MethodPost, "/workspaces/widgets"+path, apiKeyAuth(), `{"body":"hello"}`)
		if rec.Code != http.StatusCreated || strings.TrimSpace(rec.Body.String()) != fmt.Sprintf(`{"url":%q}`, want) {
			t.Errorf("%s: %d %s; want 201 with %s", path, rec.Code, rec.Body, want)
		}
	}
	if got := e.stub.received(); len(got) != 2 || jsonOf(t, got[0].Body)["body"] != "hello" {
		t.Errorf("the forge received %+v; want two comments with the text", got)
	}
	gl := gitlabEnv(t)
	gl.stub.setHandler(respond(http.StatusCreated, `{"id":3,"body":"lgtm","web_url":"https://gitlab.com/acme/widgets/-/merge_requests/7#note_3"}`))
	rec := gl.send(http.MethodPost, "/workspaces/lab/pulls/7/comments", apiKeyAuth(), `{"body":"lgtm"}`)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), "merge_requests/7#note_3") || !strings.HasSuffix(gl.stub.received()[0].Path, "/merge_requests/7/notes") {
		t.Errorf("GitLab: %d %s to %s; want a note on the merge request", rec.Code, rec.Body, gl.stub.received()[0].Path)
	}
}

// A pull request is opened with the head as given (owner:branch included) and draft false
// unless the request says true, answered 201 as issuex encodes it; with ?target=upstream
// on a carry_patch workspace it is opened on the upstream with the upstream credential; on
// GitLab a draft gets Draft: in its title.
func TestOpenPullRequest(t *testing.T) {
	e := widgetsEnv(t)
	e.stub.setHandler(githubWrites())
	rec := e.send(http.MethodPost, "/workspaces/widgets/pulls", apiKeyAuth(), `{"title":"Add widgets","head":"me:feature","base":"main","draft":null}`)
	var pr issuex.PullRequest
	testutil.Decode(t, rec, &pr)
	if again, _ := json.Marshal(pr); rec.Code != http.StatusCreated || strings.TrimSpace(rec.Body.String()) != string(again) || pr.Number != 9 {
		t.Errorf("%d %s; want 201 and pull request 9 as issuex encodes it", rec.Code, rec.Body)
	}
	if sent := lastBody(t, e.stub); sent["head"] != "me:feature" || sent["base"] != "main" || sent["title"] != "Add widgets" || sent["draft"] != false {
		t.Errorf("the forge received %v; want head me:feature, base main and draft false", sent)
	}
	e.send(http.MethodPost, "/workspaces/widgets/pulls", apiKeyAuth(), `{"title":"t","body":"b","head":"h","base":"main","draft":true}`)
	if sent := lastBody(t, e.stub); sent["draft"] != true || sent["body"] != "b" {
		t.Errorf("the forge received %v; want draft true and the body", sent)
	}

	fork := newEnv(t)
	upstream := newForgeStub(t)
	upstream.setHandler(respond(http.StatusCreated, pullBody(3, "up/widgets", "feature", "main", false)))
	fork.upstreamAuth = &githttp.BasicAuth{Username: patUsername, Password: "upstream-token"}
	addWorkspace(t, fork.db, "fork", withGitURL("https://github.com/me/widgets"), carryPatch("https://github.com/up/widgets"))
	fork.setVar("fork", "FORGE_API_URL", fork.stub.URL)
	fork.setVar("fork", "UPSTREAM_FORGE_API_URL", upstream.URL)
	if rec := fork.send(http.MethodPost, "/workspaces/fork/pulls?target=upstream", apiKeyAuth(), `{"title":"t","head":"me:feature","base":"main"}`); rec.Code != http.StatusCreated {
		t.Fatalf("upstream: %d %s", rec.Code, rec.Body)
	}
	got := upstream.received()
	if len(got) != 1 || got[0].Path != "/repos/up/widgets/pulls" || bearer(got[0]) != "upstream-token" || fork.stub.count() != 0 || fork.originCalls.Load() != 0 {
		t.Errorf("the upstream saw %+v, the origin %d requests and %d resolutions; want the create on up/widgets alone", got, fork.stub.count(), fork.originCalls.Load())
	}

	gl := gitlabEnv(t)
	gl.stub.setHandler(respond(http.StatusCreated, `{"iid":7,"title":"Draft: t","state":"opened","draft":true,"source_branch":"feature","target_branch":"main","web_url":"https://gitlab.com/acme/widgets/-/merge_requests/7","author":{"username":"hub"}}`))
	rec = gl.send(http.MethodPost, "/workspaces/lab/pulls", apiKeyAuth(), `{"title":"t","head":"feature","base":"main","draft":true}`)
	testutil.Decode(t, rec, &pr)
	if sent := lastBody(t, gl.stub); rec.Code != http.StatusCreated || pr.Number != 7 || !pr.Draft || sent["source_branch"] != "feature" || sent["title"] != "Draft: t" {
		t.Errorf("GitLab: %d %s sent %v; want the draft merge request 7", rec.Code, rec.Body, sent)
	}
}

// A write the forge refuses is answered through the shared error mapping, Retry-After
// passed through and the message scrubbed, and GitLab's validation object is forwarded as
// text; hub adds no retry: each step is one call and a failed step ends the request.
func TestWrites_ForgeFailures(t *testing.T) {
	logs := captureLog(t)
	for _, tc := range []struct {
		status             int
		retryAfter         string
		body               string
		wantStatus         int
		wantType, wantText string
	}{
		{401, "", `{"message":"no good for origin-token at https://bot:hunter2@github.com/x"}`, 502, "forge_unauthenticated", "[redacted]"},
		{403, "", `{"message":"no"}`, 502, "forge_forbidden", "no"},
		{404, "", `{"message":"Not Found"}`, 404, "forge_not_found", "Not Found"},
		{422, "", `{"message":"Validation Failed","errors":[]}`, 422, "forge_rejected", "Validation Failed"},
		{429, "300", `{"message":"rate limited"}`, 429, "forge_rate_limited", "rate limited"},
		{500, "", `not json`, 502, "forge_error", "Internal Server Error"},
	} {
		e := widgetsEnv(t)
		e.stub.setHandler(func(w http.ResponseWriter, r *http.Request) {
			if tc.retryAfter != "" {
				w.Header().Set("Retry-After", tc.retryAfter)
			}
			w.WriteHeader(tc.status)
			io.WriteString(w, tc.body)
		})
		for _, path := range []string{"/issues", "/issues/42"} {
			method := http.MethodPost
			if path == "/issues/42" {
				method = http.MethodPatch
			}
			rec := e.send(method, "/workspaces/widgets"+path, apiKeyAuth(), `{"title":"t"}`)
			got := testutil.Error(t, rec)
			if rec.Code != tc.wantStatus || got.ErrorType != tc.wantType || !strings.Contains(got.Message, tc.wantText) || strings.Contains(got.Message, "hunter2") {
				t.Errorf("%d on %s: %d %+v; want %d %s carrying %q", tc.status, path, rec.Code, got, tc.wantStatus, tc.wantType, tc.wantText)
			}
			if want := map[bool]string{true: "301", false: ""}[tc.status == 429]; rec.Header().Get("Retry-After") != want {
				t.Errorf("%d on %s: Retry-After = %q; want %q", tc.status, path, rec.Header().Get("Retry-After"), want)
			}
		}
		if n := e.stub.count(); n != 2 {
			t.Errorf("%d: the forge received %d requests; want 2, one per write", tc.status, n)
		}
	}
	if out := logs.String(); strings.Contains(out, "origin-token") || strings.Contains(out, "hunter2") || strings.Count(out, "forge: request failed") != 8 {
		t.Errorf("the log holds a secret or not the 8 warnings of the 401s, 403s, 429s and 500s:\n%s", out)
	}
	gl := gitlabEnv(t)
	gl.stub.setHandler(respond(http.StatusBadRequest, `{"message":{"title":["is too long"]}}`))
	if rec := gl.send(http.MethodPatch, "/workspaces/lab/issues/42", apiKeyAuth(), `{"title":"x"}`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(testutil.Error(t, rec).Message, "is too long") {
		t.Errorf("GitLab validation: %d %s; want 422 carrying the object", rec.Code, rec.Body)
	}

	want := map[string]string{"create issue": "CreateIssue", "update issue": "ChangeIssue", "close issue": "ChangeIssue", "comment on issue": "AddComment",
		"add labels": "AddLabels", "remove label": "DropLabel", "ensure label": "GetLabel", "open pull request": "CreatePullRequest", "comment on pull request": "AddComment"}
	for _, failure := range []struct {
		err        error
		wantStatus int
		wantType   string
	}{
		{&issuex.HTTPError{Method: "POST", Path: "/x", Status: 503, Message: "unavailable"}, 502, "forge_error"},
		{fmt.Errorf("POST /x: %w", context.DeadlineExceeded), 504, "forge_timeout"},
		{&url.Error{Op: "Post", URL: "https://api.github.com/x", Err: errors.New("connection refused")}, 502, "forge_unreachable"},
	} {
		for _, r := range append(writeRoutes, route{"close with a comment", http.MethodPost, "/workspaces/%s/issues/42/close", `{"comment":"done"}`}) {
			fake := &fakeWriter{behave: func(context.Context, string, int) error { return failure.err }}
			e := widgetsEnv(t, useClient(fake, nil))
			rec := e.sendRoute(r, "widgets", apiKeyAuth())
			wantCall := want[r.name]
			if r.name == "close with a comment" {
				wantCall = "AddComment"
			}
			if got := testutil.Error(t, rec); rec.Code != failure.wantStatus || got.ErrorType != failure.wantType || fmt.Sprint(fake.names()) != "["+wantCall+"]" {
				t.Errorf("%s with %v: %d %+v after %v; want %d %s after one %s", r.name, failure.err, rec.Code, got, fake.names(), failure.wantStatus, failure.wantType, wantCall)
			}
		}
	}
}

// A write the caller walks away from still reaches the forge, is answered and is recorded:
// the requests to the forge, the one issuex sends and the one hub sends itself, and the
// event run on a context the caller's cancellation does not reach.
func TestWrites_ContinueAfterTheCallerLeaves(t *testing.T) {
	em := &recordingEmitter{}
	e := widgetsEnv(t, withEmitter(em))
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var stayed atomic.Bool
	all := githubWrites()
	e.stub.setHandler(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() {
			close(started)
			<-release
			stayed.Store(r.Context().Err() == nil)
		})
		all(w, r)
	})
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/widgets/issues/42/close", strings.NewReader(`{"comment":"done"}`)).WithContext(ctx)
	req.Header.Set(testutil.AuthHeader, marshal(apiKeyAuth()))
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { e.echo.ServeHTTP(rec, req); close(done) }()
	<-started
	cancel()
	close(release)
	<-done
	if ctxs := em.contexts(); !stayed.Load() || rec.Code != http.StatusOK || e.stub.count() != 2 || len(ctxs) != 1 || ctxs[0] != nil {
		t.Errorf("hub stayed %v, answered %d after %d requests, emitted on %v; want it to stay, 200, 2 and one live event", stayed.Load(), rec.Code, e.stub.count(), ctxs)
	}
}

// Every request of a write that takes several gets the whole deadline from its own start.
func TestWrites_EachRequestGetsItsOwnDeadline(t *testing.T) {
	shortDeadline(t, 600*time.Millisecond)
	const used = 200 * time.Millisecond
	for _, tc := range []struct {
		route  route
		behave func(ctx context.Context, method string, n int) error
		calls  string
	}{
		{route{"", http.MethodPost, "/workspaces/%s/issues/42/close", `{"comment":"done"}`},
			func(context.Context, string, int) error { time.Sleep(used); return nil }, "[AddComment ChangeIssue]"},
		{route{"", http.MethodPost, "/workspaces/%s/labels", `{"name":"af:triage"}`},
			func(_ context.Context, method string, n int) error {
				time.Sleep(used)
				if method == "GetLabel" && n == 1 {
					return notFound()
				}
				if method == "MakeLabel" {
					return &issuex.HTTPError{Status: 422, Message: "Validation Failed"}
				}
				return nil
			}, "[GetLabel MakeLabel GetLabel]"},
	} {
		fake := &fakeWriter{behave: tc.behave}
		e := widgetsEnv(t, useClient(fake, nil))
		if rec := e.sendRoute(tc.route, "widgets", apiKeyAuth()); rec.Code >= 300 || fmt.Sprint(fake.names()) != tc.calls {
			t.Fatalf("%s: %d %s after %v; want success after %s", tc.route.path, rec.Code, rec.Body, fake.names(), tc.calls)
		}
		if last := fake.left[len(fake.left)-1]; last < callDeadline-100*time.Millisecond || fake.left[0] > callDeadline {
			t.Errorf("%s: the last request had %s left of %s; want nearly all of it", tc.route.path, last, callDeadline)
		}
	}
}

// A write issuex refuses before it sends anything, for a workspace without a credential,
// sends nothing and records nothing; a request hub sends itself goes out without a token.
func TestWrites_WithoutACredential(t *testing.T) {
	em := &recordingEmitter{}
	e := widgetsEnv(t, withEmitter(em))
	e.originAuth = nil
	e.stub.setHandler(githubWrites())
	for _, name := range []string{"create issue", "comment on issue", "add labels", "open pull request", "comment on pull request"} {
		if rec := e.sendRoute(namedWrite(name), "widgets", apiKeyAuth()); rec.Code != http.StatusBadGateway {
			t.Errorf("%s: status = %d; want 502", name, rec.Code)
		}
	}
	if e.stub.count() != 0 || len(em.recorded()) != 0 {
		t.Errorf("%d requests and %d events although nothing reached the forge", e.stub.count(), len(em.recorded()))
	}
	e.stub.setHandler(answer(401, "Requires authentication"))
	rec := e.send(http.MethodPatch, "/workspaces/widgets/issues/42", apiKeyAuth(), `{"title":"t"}`)
	if got := testutil.Error(t, rec); rec.Code != http.StatusBadGateway || got.ErrorType != "forge_unauthenticated" || e.stub.count() != 1 || len(em.recorded()) != 1 || bearer(e.stub.received()[0]) != "" {
		t.Errorf("an update without a credential: %d %+v, %d requests, %d events; want 502 forge_unauthenticated, one request without a token and one event", rec.Code, got, e.stub.count(), len(em.recorded()))
	}
}

// The requests issuex sends and the ones hub sends itself go to one API root, also when
// hub's environment names another API for the host.
func TestWrites_GoToOneAPIRoot(t *testing.T) {
	var urls []string
	var mu sync.Mutex
	e := newEnv(t, func(cfg *Config) {
		cfg.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			mu.Lock()
			urls = append(urls, r.Method+" "+r.URL.String())
			mu.Unlock()
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(issueBody(1, "t", ""))), Request: r}, nil
		})
	})
	addWorkspace(t, e.db, "ghe", withGitURL("https://github.corp.example/acme/widgets"))
	t.Setenv("GITHUB_API_URL", "https://api.github.corp.example")
	e.send(http.MethodPost, "/workspaces/ghe/issues", apiKeyAuth(), `{"title":"t"}`)
	e.send(http.MethodPatch, "/workspaces/ghe/issues/1", apiKeyAuth(), `{"title":"x"}`)
	want := "[POST https://github.corp.example/api/v3/repos/acme/widgets/issues PATCH https://github.corp.example/api/v3/repos/acme/widgets/issues/1]"
	if fmt.Sprint(urls) != want {
		t.Errorf("the requests went to %v; want %s", urls, want)
	}
}

// A body-like field longer than the forge's limit, counted in code points, is 413
// body_too_large naming the field, its length, the forge and the limit, before the forge
// is asked; the limit itself passes, and the fields hub does not measure reach the forge.
func TestWrites_BodyLimits(t *testing.T) {
	type field struct{ name, method, path, before, after string }
	fields := []field{
		{"body", http.MethodPost, "/issues", `{"title":"t","body":"`, `"}`},
		{"body", http.MethodPatch, "/issues/42", `{"body":"`, `"}`},
		{"comment", http.MethodPost, "/issues/42/close", `{"comment":"`, `"}`},
		{"body", http.MethodPost, "/issues/42/comments", `{"body":"`, `"}`},
		{"body", http.MethodPost, "/pulls", `{"title":"t","head":"h","base":"main","body":"`, `"}`},
		{"body", http.MethodPost, "/pulls/42/comments", `{"body":"`, `"}`},
	}
	gh := widgetsEnv(t)
	gh.stub.setHandler(githubWrites())
	for _, f := range fields {
		for _, char := range []string{"x", "😀"} {
			rec := gh.send(f.method, "/workspaces/widgets"+f.path, apiKeyAuth(), f.before+strings.Repeat(char, 65537)+f.after)
			got := testutil.Error(t, rec)
			if want := f.name + " is 65537 characters; github accepts at most 65536"; rec.Code != http.StatusRequestEntityTooLarge || got.ErrorType != "body_too_large" || got.Message != want {
				t.Errorf("%s %s with %q: %d %+v; want 413 %q", f.method, f.path, char, rec.Code, got, want)
			}
		}
		if gh.stub.count() != 0 {
			t.Fatalf("%s %s: an oversized body reached the forge", f.method, f.path)
		}
		if rec := gh.send(f.method, "/workspaces/widgets"+f.path, apiKeyAuth(), f.before+strings.Repeat("é", 65536)+f.after); rec.Code >= 300 {
			t.Errorf("%s %s at the limit: status = %d; want it sent", f.method, f.path, rec.Code)
		}
		gh.stub.forget()
	}
	long := strings.Repeat("y", 70000)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/issues", `{"title":"` + long + `","labels":["` + long + `"]}`},
		{http.MethodPost, "/labels", `{"name":"af:triage","description":"` + long + `"}`},
	} {
		gh.stub.forget()
		if rec := gh.send(tc.method, "/workspaces/widgets"+tc.path, apiKeyAuth(), tc.body); rec.Code == http.StatusRequestEntityTooLarge || gh.stub.count() == 0 {
			t.Errorf("%s: status = %d after %d requests; want the forge to decide", tc.path, rec.Code, gh.stub.count())
		}
	}

	gl := gitlabEnv(t)
	gl.stub.setHandler(respond(http.StatusCreated, gitlabIssueBody(42, "t", "", "opened")))
	rec := gl.send(http.MethodPost, "/workspaces/lab/issues", apiKeyAuth(), `{"title":"t","body":"`+strings.Repeat("x", 1000001)+`"}`)
	if got := testutil.Error(t, rec); rec.Code != http.StatusRequestEntityTooLarge || got.Message != "body is 1000001 characters; gitlab accepts at most 1000000" {
		t.Errorf("GitLab: %d %+v; want 413 naming gitlab and 1000000", rec.Code, got)
	}
	if rec := gl.send(http.MethodPost, "/workspaces/lab/issues", apiKeyAuth(), `{"title":"t","body":"`+strings.Repeat("😀", 1000000)+`"}`); rec.Code != http.StatusCreated {
		t.Errorf("GitLab at the limit: status = %d; want 201", rec.Code)
	}
	if ref := checkBodyLimits(issuex.ForgeType("other"), []textField{{"body", strings.Repeat("x", 65537)}}); ref == nil || !strings.Contains(ref.message, "65536") {
		t.Errorf("an unknown forge gets GitHub's limit; got %+v", ref)
	}
}
