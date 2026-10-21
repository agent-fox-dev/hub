package forge

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// githubWrites answers the write requests of acme/widgets on GitHub with success.
func githubWrites() http.HandlerFunc {
	return routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/issues":                    respond(http.StatusCreated, issueBody(7, "t", "b")),
		"PATCH /repos/acme/widgets/issues/42":                respond(http.StatusOK, issueBody(42, "t", "b")),
		"POST /repos/acme/widgets/issues/42/comments":        respond(http.StatusCreated, commentBody),
		"POST /repos/acme/widgets/issues/42/labels":          respond(http.StatusOK, labelsAnswer),
		"DELETE /repos/acme/widgets/issues/42/labels/af:fix": respond(http.StatusOK, labelsAnswer),
		"GET /repos/acme/widgets/labels/af:triage":           respond(http.StatusNotFound, `{"message":"Not Found"}`),
		"POST /repos/acme/widgets/labels":                    respond(http.StatusCreated, `{"name":"af:triage","color":"ededed"}`),
		"POST /repos/acme/widgets/pulls":                     respond(http.StatusCreated, pullBody(9, "acme/widgets", "feature", "main", false)),
	})
}

// bodyField is a body-like field of a write route, and the request that sets it to text.
type bodyField struct {
	name   string // as the message names it
	method string
	path   string
	body   func(text string) string
}

func jsonString(text string) string { return fmt.Sprintf("%q", text) }

var bodyFields = []bodyField{
	{"body", http.MethodPost, "/issues", func(s string) string { return `{"title":"t","body":` + jsonString(s) + `}` }},
	{"body", http.MethodPatch, "/issues/42", func(s string) string { return `{"body":` + jsonString(s) + `}` }},
	{"comment", http.MethodPost, "/issues/42/close", func(s string) string { return `{"comment":` + jsonString(s) + `}` }},
	{"body", http.MethodPost, "/issues/42/comments", func(s string) string { return `{"body":` + jsonString(s) + `}` }},
	{"body", http.MethodPost, "/pulls", func(s string) string { return `{"title":"t","head":"h","base":"main","body":` + jsonString(s) + `}` }},
	{"body", http.MethodPost, "/pulls/42/comments", func(s string) string { return `{"body":` + jsonString(s) + `}` }},
}

// writesAnyWhere answers every write request of the six body routes with success, GitHub
// and GitLab alike.
func writesAnyWhere(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.Contains(r.URL.Path, "/merge_requests") && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/merge_requests"):
		respond(http.StatusCreated, `{"iid":9,"title":"t","state":"opened","source_branch":"h","target_branch":"main","web_url":"https://gitlab.com/acme/widgets/-/merge_requests/9"}`)(w, r)
	case strings.HasSuffix(r.URL.Path, "/notes"):
		respond(http.StatusCreated, `{"id":3,"body":"x","web_url":"https://gitlab.com/acme/widgets/-/issues/42#note_3"}`)(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/v4/") && strings.Contains(r.URL.Path, "/issues"):
		respond(http.StatusCreated, gitlabIssueBody(42, "t", "", "opened"))(w, r)
	case strings.HasSuffix(r.URL.Path, "/comments"):
		respond(http.StatusCreated, commentBody)(w, r)
	case strings.HasSuffix(r.URL.Path, "/pulls"):
		respond(http.StatusCreated, pullBody(9, "acme/widgets", "h", "main", false))(w, r)
	default:
		respond(http.StatusCreated, issueBody(42, "t", ""))(w, r)
	}
}

// TS-31-38: On GitHub a body-like field of 65,537 characters is refused with 413
// body_too_large for each of the six fields, and the message names the field, its length,
// the forge and the limit; nothing is sent and nothing is recorded.
// Requirement: 31-REQ-7.1
func TestBodyLimit_GitHub_TS_31_38(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	e.stub.setHandler(githubWrites())

	for _, f := range bodyFields {
		rec := e.send(f.method, "/workspaces/widgets"+f.path, apiKeyAuth(), f.body(strings.Repeat("x", 65537)))
		got := decodeError(t, rec)
		want := f.name + " is 65537 characters; github accepts at most 65536"
		if rec.Code != http.StatusRequestEntityTooLarge || got.ErrorType != "body_too_large" || got.Message != want {
			t.Errorf("%s %s: answer = %d %+v; want 413 body_too_large %q", f.method, f.path, rec.Code, got, want)
		}
	}
	if n := e.stub.count(); n != 0 {
		t.Errorf("the forge received %d requests; want none", n)
	}
	if n := len(em.recorded()); n != 0 {
		t.Errorf("%d events recorded; want none", n)
	}
}

// TS-31-39: On GitLab a body of 1,000,001 characters is refused, naming gitlab and 1000000,
// and one of exactly 1,000,000 is sent.
// Requirement: 31-REQ-7.2
func TestBodyLimit_GitLab_TS_31_39(t *testing.T) {
	e := newGitLabWriteEnv(t)
	e.stub.setHandler(writesAnyWhere)

	rec := e.send(http.MethodPost, "/workspaces/lab/issues", apiKeyAuth(), `{"title":"t","body":`+jsonString(strings.Repeat("x", 1000001))+`}`)
	got := decodeError(t, rec)
	if want := "body is 1000001 characters; gitlab accepts at most 1000000"; rec.Code != http.StatusRequestEntityTooLarge || got.ErrorType != "body_too_large" || got.Message != want {
		t.Errorf("answer = %d %+v; want 413 body_too_large %q", rec.Code, got, want)
	}
	if n := e.stub.count(); n != 0 {
		t.Errorf("the forge received %d requests; want none", n)
	}
	e.stub.forget()
	rec = e.send(http.MethodPost, "/workspaces/lab/issues", apiKeyAuth(), `{"title":"t","body":`+jsonString(strings.Repeat("x", 1000000))+`}`)
	if rec.Code != http.StatusCreated {
		t.Errorf("a body of exactly 1,000,000 characters: status = %d; want 201\n%.200s", rec.Code, rec.Body)
	}
}

// TS-31-40: Length is counted in code points: a field of exactly the limit passes and one
// code point more is 413, whether each code point is one, two, three or four bytes.
// Requirement: 31-REQ-7.3
func TestBodyLimit_CountsCodePoints_TS_31_40(t *testing.T) {
	chars := []struct {
		name string
		char string
	}{{"1 byte", "x"}, {"2 bytes", "é"}, {"3 bytes", "€"}, {"4 bytes", "😀"}}

	for _, forge := range []struct {
		name   string
		slug   string
		limit  int
		env    func(t *testing.T) *env
		fields []bodyField
	}{
		{"github", "widgets", 65536, func(t *testing.T) *env { return newWriteEnv(t) }, bodyFields},
		// GitLab's limit is a megabyte of code points, four megabytes of the widest: every
		// field with the narrowest and the widest.
		{"gitlab", "lab", 1000000, func(t *testing.T) *env { return newGitLabWriteEnv(t) }, bodyFields},
	} {
		e := forge.env(t)
		e.stub.setHandler(writesAnyWhere)
		for _, f := range forge.fields {
			for i, c := range chars {
				if forge.name == "gitlab" && i != 0 && i != 3 {
					continue
				}
				at := e.send(f.method, "/workspaces/"+forge.slug+f.path, apiKeyAuth(), f.body(strings.Repeat(c.char, forge.limit)))
				e.stub.forget()
				if at.Code == http.StatusRequestEntityTooLarge || at.Code >= 300 {
					t.Errorf("%s %s %s: %d characters of %s: status = %d; want it sent\n%.200s", forge.name, f.method, f.path, forge.limit, c.name, at.Code, at.Body)
				}
				over := e.send(f.method, "/workspaces/"+forge.slug+f.path, apiKeyAuth(), f.body(strings.Repeat(c.char, forge.limit+1)))
				if over.Code != http.StatusRequestEntityTooLarge {
					t.Errorf("%s %s %s: %d characters of %s: status = %d; want 413", forge.name, f.method, f.path, forge.limit+1, c.name, over.Code)
				}
				if n := e.stub.count(); n != 0 {
					t.Errorf("%s %s %s: %d characters of %s reached the forge", forge.name, f.method, f.path, forge.limit+1, c.name)
					e.stub.forget()
				}
			}
		}
	}
}

// TS-31-41: An oversized body on a workspace whose forge cannot be told is the 422 of the
// client, not a 413: the limit depends on the forge, so it is checked after the client.
// Requirement: 31-REQ-7.4
func TestBodyLimit_UnknownForge_TS_31_41(t *testing.T) {
	e := newWriteEnv(t)
	addWorkspace(t, e.db, "unknown", func(r *workspaceRow) { r.gitURL = "https://git.internal.example/acme/widgets" })
	rec := e.send(http.MethodPost, "/workspaces/unknown/issues", apiKeyAuth(), `{"title":"t","body":`+jsonString(strings.Repeat("x", 70000))+`}`)
	if got := decodeError(t, rec); rec.Code != http.StatusUnprocessableEntity || got.ErrorType != "forge_unknown" {
		t.Errorf("answer = %d %+v; want 422 forge_unknown", rec.Code, got)
	}
	if n := e.stub.count(); n != 0 {
		t.Errorf("the forge received %d requests; want none", n)
	}
}

// TS-31-42: Titles and the other fields hub does not measure get no length check: they
// reach the forge, which decides.
// Requirement: 31-REQ-7.5
func TestBodyLimit_OtherFieldsAreNotChecked_TS_31_42(t *testing.T) {
	e := newWriteEnv(t)
	e.stub.setHandler(githubWrites())
	long := strings.Repeat("y", 70000)

	for _, tc := range []struct{ name, method, path, body string }{
		{"issue title", http.MethodPost, "/issues", `{"title":"` + long + `"}`},
		{"issue assignee", http.MethodPost, "/issues", `{"title":"t","assignees":["` + long + `"]}`},
		{"issue label", http.MethodPost, "/issues", `{"title":"t","labels":["` + long + `"]}`},
		{"update title", http.MethodPatch, "/issues/42", `{"title":"` + long + `"}`},
		{"label names", http.MethodPost, "/issues/42/labels", `["` + long + `"]`},
		{"pull request title", http.MethodPost, "/pulls", `{"title":"` + long + `","head":"h","base":"main"}`},
		{"pull request head", http.MethodPost, "/pulls", `{"title":"t","head":"` + long + `","base":"main"}`},
		{"pull request base", http.MethodPost, "/pulls", `{"title":"t","head":"h","base":"` + long + `"}`},
		{"label name", http.MethodPost, "/labels", `{"name":"` + long + `"}`},
		{"label description", http.MethodPost, "/labels", `{"name":"af:triage","description":"` + long + `"}`},
	} {
		before := e.stub.count()
		rec := e.send(tc.method, "/workspaces/widgets"+tc.path, apiKeyAuth(), tc.body)
		if rec.Code == http.StatusRequestEntityTooLarge {
			t.Errorf("%s: refused with 413", tc.name)
		}
		if e.stub.count() == before {
			t.Errorf("%s: the request did not reach the forge (status %d)", tc.name, rec.Code)
		}
	}
}
