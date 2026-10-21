package forge

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/agent-fox-dev/aflib/issuex"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// pullBody is a GitHub pull request of acme/widgets as the API answers one.
func pullBody(n int, repo, head, base string, draft bool) string {
	return fmt.Sprintf(`{"number":%d,"title":"t","body":"","state":"open","html_url":"https://github.com/%s/pull/%d","draft":%v,"merged":false,`+
		`"user":{"login":"hub"},"head":{"ref":%q,"sha":"abc123"},"base":{"ref":%q,"sha":"def456"},`+
		`"created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:05Z"}`, n, repo, n, draft, head, base)
}

// TS-31-32: A pull request is opened with head as given, owner:branch included, and draft
// false unless the request says true; the answer is 201 with the pull request as issuex
// encodes it, and hub.forge.pull_request.create records the branches and the flag.
// Requirement: 31-REQ-6.1
func TestOpenPullRequest_HeadVerbatimDraftDefault_TS_31_32(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/pulls": respond(http.StatusCreated, pullBody(9, "acme/widgets", "feature", "main", false)),
	}))

	rec := e.send(http.MethodPost, "/workspaces/widgets/pulls", apiKeyAuth(), `{"title":"Add widgets support","head":"me:feature","base":"main"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201\n%s", rec.Code, rec.Body)
	}
	var pr issuex.PullRequest
	if err := json.Unmarshal(rec.Body.Bytes(), &pr); err != nil {
		t.Fatal(err)
	}
	again, _ := json.Marshal(pr)
	if strings.TrimSpace(rec.Body.String()) != string(again) || pr.Number != 9 {
		t.Errorf("body %s; want pull request 9 as issuex encodes it", rec.Body)
	}
	got := e.stub.received()
	if len(got) != 1 || bearer(got[0]) != "origin-token" {
		t.Fatalf("the forge received %+v; want one request with the workspace's token", got)
	}
	sent := jsonOf(t, got[0].Body)
	if sent["head"] != "me:feature" || sent["base"] != "main" || sent["title"] != "Add widgets support" || sent["draft"] != false {
		t.Errorf("the forge received %s; want head me:feature, base main and draft false", got[0].Body)
	}

	rec = e.send(http.MethodPost, "/workspaces/widgets/pulls", apiKeyAuth(), `{"title":"t","body":"b","head":"h","base":"main","draft":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201\n%s", rec.Code, rec.Body)
	}
	if sent := lastBody(t, e.stub); sent["draft"] != true || sent["body"] != "b" {
		t.Errorf("the forge received %v; want draft true and the body", sent)
	}
	// A null draft is no draft.
	if rec := e.send(http.MethodPost, "/workspaces/widgets/pulls", apiKeyAuth(), `{"title":"t","head":"h","base":"main","draft":null}`); rec.Code != http.StatusCreated {
		t.Errorf("a null draft: status = %d; want 201", rec.Code)
	}

	events := em.recorded()
	if len(events) != 3 {
		t.Fatalf("%d events recorded; want 3", len(events))
	}
	ev := events[0]
	if ev.EventType != audit.EventForgePullRequestCreate || ev.ResourceType != "pull_request" || ev.ResourceID != "9" || ev.Action != "create" {
		t.Errorf("event = %+v; want hub.forge.pull_request.create of 9", ev)
	}
	if ev.Metadata["head"] != "me:feature" || ev.Metadata["base"] != "main" || ev.Metadata["draft"] != false || ev.Metadata["number"] != 9 ||
		ev.Metadata["url"] != "https://github.com/acme/widgets/pull/9" || ev.Metadata["target"] != "origin" || ev.Metadata["ok"] != true {
		t.Errorf("metadata = %v; want head, base, draft false, number 9 and the url", ev.Metadata)
	}
	if events[1].Metadata["draft"] != true {
		t.Errorf("the second event's draft = %v; want true", events[1].Metadata["draft"])
	}
	for i, ev := range events {
		if strings.Contains(fmt.Sprint(ev.Metadata), "Add widgets support") {
			t.Errorf("event %d holds the title: %v", i, ev.Metadata)
		}
	}
}

// TS-31-33: With ?target=upstream on a carry_patch workspace the pull request is opened on
// the upstream repository with the upstream credential, and the origin sees nothing.
// Requirement: 31-REQ-6.2
func TestOpenPullRequest_UpstreamTarget_TS_31_33(t *testing.T) {
	em := &recordingEmitter{}
	e := newEnv(t, withEmitter(em))
	origin, upstream := e.stub, newForgeStub(t)
	upstream.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/up/widgets/pulls": respond(http.StatusCreated, pullBody(3, "up/widgets", "feature", "main", false)),
	}))
	e.upstreamAuth = &githttp.BasicAuth{Username: "x-token-auth", Password: "upstream-token"}
	// The upstream stub speaks for the upstream host only; the transport of the test
	// environment trusts both stubs' certificates, which are the same test CA.
	addWorkspace(t, e.db, "fork", func(r *workspaceRow) {
		r.gitURL = "https://github.com/me/widgets"
		carryPatch("https://github.com/up/widgets")(r)
	})
	e.setVar("fork", "FORGE_API_URL", origin.URL)
	e.setVar("fork", "UPSTREAM_FORGE_API_URL", upstream.URL)

	rec := e.send(http.MethodPost, "/workspaces/fork/pulls?target=upstream", apiKeyAuth(), `{"title":"t","head":"me:feature","base":"main"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201\n%s", rec.Code, rec.Body)
	}
	got := upstream.received()
	if len(got) != 1 || got[0].Method != http.MethodPost || got[0].Path != "/repos/up/widgets/pulls" || bearer(got[0]) != "upstream-token" {
		t.Errorf("the upstream saw %+v; want the create on up/widgets with the upstream token", got)
	}
	if sent := jsonOf(t, got[0].Body); sent["head"] != "me:feature" {
		t.Errorf("the upstream received %s; want head me:feature verbatim", got[0].Body)
	}
	if n := origin.count(); n != 0 {
		t.Errorf("the origin received %d requests; want none", n)
	}
	if e.originCalls.Load() != 0 {
		t.Error("the origin credential was resolved for the upstream target")
	}
	events := em.recorded()
	if len(events) != 1 || events[0].Metadata["target"] != "upstream" || events[0].EventType != audit.EventForgePullRequestCreate {
		t.Errorf("events = %+v; want one pull request event with target upstream", events)
	}

	// Without ?target the same workspace opens it on its own fork.
	origin.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/me/widgets/pulls": respond(http.StatusCreated, pullBody(4, "me/widgets", "feature", "main", false)),
	}))
	if rec := e.send(http.MethodPost, "/workspaces/fork/pulls", apiKeyAuth(), `{"title":"t","head":"feature","base":"main"}`); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201\n%s", rec.Code, rec.Body)
	}
	if n := upstream.count(); n != 1 {
		t.Errorf("the upstream received %d requests in all; want 1", n)
	}
}

// TS-31-34: ?target=upstream on a standard workspace is 400 workspace_mode_mismatch, with
// no request to the forge and no event.
// Requirement: 31-REQ-6.3
func TestOpenPullRequest_UpstreamOnStandard_TS_31_34(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	for _, target := range []string{"upstream", "elsewhere"} {
		rec := e.send(http.MethodPost, "/workspaces/widgets/pulls?target="+target, apiKeyAuth(), `{"title":"t","head":"h","base":"main"}`)
		got := decodeError(t, rec)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("target=%s: status = %d; want 400", target, rec.Code)
		}
		if target == "upstream" && got.ErrorType != "workspace_mode_mismatch" {
			t.Errorf("target=upstream: error_type = %q; want workspace_mode_mismatch", got.ErrorType)
		}
	}
	if n := e.stub.count(); n != 0 {
		t.Errorf("the forge received %d requests; want none", n)
	}
	if n := len(em.recorded()); n != 0 {
		t.Errorf("%d events recorded; want none", n)
	}
}

// TS-31-35: A POST /pulls body without a title, head or base, with one that is blank, or
// with a draft that is not a boolean answers 400 naming the field.
// Requirement: 31-REQ-6.4
func TestOpenPullRequest_InvalidBodies_TS_31_35(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	for _, tc := range []struct{ name, body, field string }{
		{"no title", `{"head":"h","base":"main"}`, "title"},
		{"a blank title", `{"title":" ","head":"h","base":"main"}`, "title"},
		{"no head", `{"title":"t","base":"main"}`, "head"},
		{"a blank head", `{"title":"t","head":"  ","base":"main"}`, "head"},
		{"no base", `{"title":"t","head":"h"}`, "base"},
		{"a blank base", `{"title":"t","head":"h","base":""}`, "base"},
		{"a draft of yes", `{"title":"t","head":"h","base":"main","draft":"yes"}`, "draft"},
		{"a draft of 1", `{"title":"t","head":"h","base":"main","draft":1}`, "draft"},
		{"a body that is no string", `{"title":"t","head":"h","base":"main","body":false}`, "body"},
	} {
		rec := e.send(http.MethodPost, "/workspaces/widgets/pulls", apiKeyAuth(), tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d; want 400\n%s", tc.name, rec.Code, rec.Body)
			continue
		}
		if msg := decodeError(t, rec).Message; !strings.Contains(msg, tc.field) {
			t.Errorf("%s: message = %q; want it to name %q", tc.name, msg, tc.field)
		}
	}
	if n := e.stub.count(); n != 0 {
		t.Errorf("the forge received %d requests; want none", n)
	}
	if n := len(em.recorded()); n != 0 {
		t.Errorf("%d events recorded; want none", n)
	}
}

// TS-31-36: A pull request the forge refuses, one that exists for head and base, is
// answered through the mapping, and recorded as a failure.
// Requirement: 31-REQ-6.5
func TestOpenPullRequest_ForgeRefusal_TS_31_36(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/pulls": respond(http.StatusUnprocessableEntity, `{"message":"Validation Failed","errors":[{"message":"A pull request already exists for acme:feature."}]}`),
	}))

	rec := e.send(http.MethodPost, "/workspaces/widgets/pulls", apiKeyAuth(), `{"title":"t","head":"feature","base":"main","draft":true}`)
	got := decodeError(t, rec)
	if rec.Code != http.StatusUnprocessableEntity || got.ErrorType != "forge_rejected" || !strings.Contains(got.Message, "Validation Failed") {
		t.Errorf("answer = %d %+v; want 422 forge_rejected with the forge's message", rec.Code, got)
	}
	events := em.recorded()
	if len(events) != 1 {
		t.Fatalf("%d events recorded; want 1", len(events))
	}
	ev := events[0]
	if ev.ResourceID != "" || ev.Metadata["ok"] != false || ev.Metadata["error_type"] != "forge_rejected" || ev.Metadata["head"] != "feature" ||
		ev.Metadata["base"] != "main" || ev.Metadata["draft"] != true {
		t.Errorf("event = %+v; want a failed create without a resource id that still names head, base and draft", ev)
	}
	if _, has := ev.Metadata["number"]; has {
		t.Errorf("a failed create holds a number: %v", ev.Metadata)
	}
}

// TS-31-37: Commenting on a pull request answers 201 {"url": ...} with the URL of a comment
// on the pull request's conversation; a blank body is 400 naming body and sends nothing.
// Requirement: 31-REQ-6.6
func TestCommentOnPullRequest_TS_31_37(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	const url = "https://github.com/acme/widgets/pull/5#issuecomment-9"
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/issues/5/comments": respond(http.StatusCreated, fmt.Sprintf(`{"id":9,"body":"lgtm","html_url":%q}`, url)),
	}))

	rec := e.send(http.MethodPost, "/workspaces/widgets/pulls/5/comments", apiKeyAuth(), `{"body":"lgtm"}`)
	if rec.Code != http.StatusCreated || strings.TrimSpace(rec.Body.String()) != fmt.Sprintf(`{"url":%q}`, url) {
		t.Fatalf("answer = %d %s; want 201 and the comment's url", rec.Code, rec.Body)
	}
	if got := e.stub.received(); len(got) != 1 || jsonOf(t, got[0].Body)["body"] != "lgtm" {
		t.Errorf("the forge received %+v; want one comment", got)
	}

	before := e.stub.count()
	for _, body := range []string{`{"body":" "}`, `{}`} {
		rec := e.send(http.MethodPost, "/workspaces/widgets/pulls/5/comments", apiKeyAuth(), body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(decodeError(t, rec).Message, "body") {
			t.Errorf("%s: answer = %d %s; want 400 naming body", body, rec.Code, rec.Body)
		}
	}
	if e.stub.count() != before {
		t.Error("a blank comment reached the forge")
	}

	events := em.recorded()
	if len(events) != 1 {
		t.Fatalf("%d events recorded; want 1", len(events))
	}
	ev := events[0]
	if ev.EventType != audit.EventForgePullRequestComment || ev.ResourceType != "pull_request" || ev.ResourceID != "5" || ev.Action != "comment" ||
		ev.Metadata["url"] != url || ev.Metadata["number"] != 5 {
		t.Errorf("event = %+v; want hub.forge.pull_request.comment on 5 with the comment url", ev)
	}
}

// On GitLab a merge request is opened with Draft: in its title when it is a draft, and a
// comment goes to the merge request's notes, not an issue's of the same number.
// Requirement: 31-REQ-6.1, 31-REQ-6.6
func TestPullRequests_GitLab(t *testing.T) {
	e := newGitLabWriteEnv(t)
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /api/v4/projects/acme/widgets/merge_requests": respond(http.StatusCreated,
			`{"iid":7,"title":"Draft: t","description":"","state":"opened","draft":true,"source_branch":"feature","target_branch":"main",`+
				`"web_url":"https://gitlab.com/acme/widgets/-/merge_requests/7","author":{"username":"hub"},"created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:05Z"}`),
		"POST /api/v4/projects/acme/widgets/merge_requests/7/notes": respond(http.StatusCreated,
			`{"id":3,"body":"lgtm","web_url":"https://gitlab.com/acme/widgets/-/merge_requests/7#note_3"}`),
	}))

	rec := e.send(http.MethodPost, "/workspaces/lab/pulls", apiKeyAuth(), `{"title":"t","head":"feature","base":"main","draft":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201\n%s", rec.Code, rec.Body)
	}
	var pr issuex.PullRequest
	if err := json.Unmarshal(rec.Body.Bytes(), &pr); err != nil || pr.Number != 7 || !pr.Draft || pr.State != "open" {
		t.Errorf("body %s; want the open draft merge request 7 (%v)", rec.Body, err)
	}
	if sent := lastBody(t, e.stub); sent["source_branch"] != "feature" || sent["target_branch"] != "main" || sent["title"] != "Draft: t" {
		t.Errorf("GitLab received %v; want the branches and a Draft: title", sent)
	}

	rec = e.send(http.MethodPost, "/workspaces/lab/pulls/7/comments", apiKeyAuth(), `{"body":"lgtm"}`)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), "merge_requests/7#note_3") {
		t.Errorf("comment: answer = %d %s; want 201 and the note's url", rec.Code, rec.Body)
	}
}

// A pull request body and a pull-request comment over the forge's limit are 413
// body_too_large before the forge is asked; a title of any length is not checked.
// Requirement: 31-REQ-7.1, 31-REQ-7.5
func TestPullRequestRoutes_BodyLimit(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/pulls":             respond(http.StatusCreated, pullBody(9, "acme/widgets", "h", "main", false)),
		"POST /repos/acme/widgets/issues/5/comments": respond(http.StatusCreated, commentBody),
	}))
	over, at := strings.Repeat("x", 65537), strings.Repeat("x", 65536)

	for _, tc := range []struct{ name, path, over, at string }{
		{"pull request body", "/workspaces/widgets/pulls", `{"title":"t","head":"h","base":"main","body":"` + over + `"}`, `{"title":"t","head":"h","base":"main","body":"` + at + `"}`},
		{"pull-request comment", "/workspaces/widgets/pulls/5/comments", `{"body":"` + over + `"}`, `{"body":"` + at + `"}`},
	} {
		before := e.stub.count()
		rec := e.send(http.MethodPost, tc.path, apiKeyAuth(), tc.over)
		got := decodeError(t, rec)
		if rec.Code != http.StatusRequestEntityTooLarge || got.ErrorType != "body_too_large" || got.Message != "body is 65537 characters; github accepts at most 65536" {
			t.Errorf("%s: answer = %d %+v; want 413 body_too_large", tc.name, rec.Code, got)
		}
		if e.stub.count() != before {
			t.Errorf("%s: the forge was asked", tc.name)
		}
		if rec := e.send(http.MethodPost, tc.path, apiKeyAuth(), tc.at); rec.Code != http.StatusCreated {
			t.Errorf("%s at the limit: status = %d; want 201", tc.name, rec.Code)
		}
	}
	title := strings.Repeat("t", 70000)
	if rec := e.send(http.MethodPost, "/workspaces/widgets/pulls", apiKeyAuth(), `{"title":"`+title+`","head":"h","base":"main"}`); rec.Code != http.StatusCreated {
		t.Errorf("a 70,000-character title: status = %d; want 201, the forge decides", rec.Code)
	}
}
