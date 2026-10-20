package forge

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// labelsAnswer is what GitHub answers to a request that adds or removes labels: the
// labels the issue has after it.
const labelsAnswer = `[{"name":"af:ready"}]`

// TS-31-21: Adding labels answers 204 with no body, sends the forge every name, passes a
// forge refusal through the mapping, and records hub.forge.label.add with the names.
// Requirement: 31-REQ-4.1
func TestAddLabels_TS_31_21(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/issues/42/labels": respond(http.StatusOK, labelsAnswer),
	}))

	rec := e.send(http.MethodPost, "/workspaces/widgets/issues/42/labels", apiKeyAuth(), `["af:ready","af:fix"]`)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("answer = %d %q; want 204 and no body", rec.Code, rec.Body)
	}
	got := e.stub.received()
	if len(got) != 1 || fmt.Sprint(jsonOf(t, got[0].Body)["labels"]) != "[af:ready af:fix]" {
		t.Errorf("the forge received %+v; want one request with both names", got)
	}
	events := em.recorded()
	if len(events) != 1 {
		t.Fatalf("%d events recorded; want 1", len(events))
	}
	ev := events[0]
	if ev.EventType != audit.EventForgeLabelAdd || ev.ResourceType != "label" || ev.ResourceID != "42" || ev.Action != "add" ||
		fmt.Sprint(ev.Metadata["labels"]) != "[af:ready af:fix]" || ev.Metadata["number"] != 42 || ev.Metadata["ok"] != true {
		t.Errorf("event = %+v; want hub.forge.label.add on 42 with both names", ev)
	}

	// A forge that refuses answers through the mapping, and that is recorded too.
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/issues/42/labels": respond(http.StatusUnprocessableEntity, `{"message":"Validation Failed"}`),
	}))
	rec = e.send(http.MethodPost, "/workspaces/widgets/issues/42/labels", apiKeyAuth(), `["no-such-label"]`)
	if got := decodeError(t, rec); rec.Code != http.StatusUnprocessableEntity || got.ErrorType != "forge_rejected" {
		t.Errorf("answer = %d %+v; want 422 forge_rejected", rec.Code, got)
	}
	if events := em.recorded(); len(events) != 2 || events[1].Metadata["ok"] != false || events[1].Metadata["error_type"] != "forge_rejected" {
		t.Errorf("events = %+v; want a second, failed one", events)
	}
}

// TS-31-22: A label-add body that is not a list of 1 to 100 names, none blank, answers 400
// and the forge receives nothing.
// Requirement: 31-REQ-4.2
func TestAddLabels_InvalidBodies_TS_31_22(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	names := make([]string, 101)
	for i := range names {
		names[i] = fmt.Sprintf(`"l%d"`, i)
	}

	for _, tc := range []struct{ name, body string }{
		{"an object", `{"labels":["a"]}`},
		{"an empty list", `[]`},
		{"101 names", `[` + strings.Join(names, ",") + `]`},
		{"a number", `[1]`},
		{"a mixed list", `["a",2]`},
		{"a blank name", `["  "]`},
		{"null", `null`},
		{"no body", ``},
		{"a string", `"a"`},
	} {
		rec := e.send(http.MethodPost, "/workspaces/widgets/issues/42/labels", apiKeyAuth(), tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d; want 400\n%s", tc.name, rec.Code, rec.Body)
		}
	}
	if n := e.stub.count(); n != 0 {
		t.Errorf("the forge stub received %d requests; want none", n)
	}
	if n := len(em.recorded()); n != 0 {
		t.Errorf("%d events recorded; want none", n)
	}

	// 100 names are allowed.
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/issues/42/labels": respond(http.StatusOK, labelsAnswer),
	}))
	if rec := e.send(http.MethodPost, "/workspaces/widgets/issues/42/labels", apiKeyAuth(), `[`+strings.Join(names[:100], ",")+`]`); rec.Code != http.StatusNoContent {
		t.Errorf("100 names: status = %d; want 204", rec.Code)
	}
}

// TS-31-23: The label to remove is the last path segment percent-decoded once: type%2Fbug
// is type/bug, af:fix is unchanged, and %2525 is the literal %25.
// Requirement: 31-REQ-4.3
func TestRemoveLabel_NameIsDecodedOnce_TS_31_23(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	e.stub.setHandler(func(w http.ResponseWriter, r *http.Request) {
		respond(http.StatusOK, labelsAnswer)(w, r)
	})

	for _, tc := range []struct{ segment, label, sent string }{
		{"type%2Fbug", "type/bug", "type%2Fbug"},
		{"af:fix", "af:fix", "af:fix"},
		{"100%2525", "100%25", "100%2525"},
		{"needs%20triage", "needs triage", "needs%20triage"},
	} {
		before := len(e.stub.received())
		rec := e.send(http.MethodDelete, "/workspaces/widgets/issues/42/labels/"+tc.segment, apiKeyAuth(), "")
		if rec.Code != http.StatusNoContent {
			t.Errorf("DELETE .../labels/%s: status = %d; want 204\n%s", tc.segment, rec.Code, rec.Body)
			continue
		}
		got := e.stub.received()[before:]
		if len(got) != 1 || got[0].Method != http.MethodDelete || got[0].EscapedPath != "/repos/acme/widgets/issues/42/labels/"+tc.sent {
			t.Errorf("DELETE .../labels/%s: the forge received %+v; want DELETE .../labels/%s", tc.segment, got, tc.sent)
		}
	}
	events := em.recorded()
	if len(events) != 4 {
		t.Fatalf("%d events recorded; want 4", len(events))
	}
	for i, label := range []string{"type/bug", "af:fix", "100%25", "needs triage"} {
		ev := events[i]
		if ev.EventType != audit.EventForgeLabelRemove || ev.ResourceType != "label" || ev.ResourceID != "42" || ev.Action != "remove" || ev.Metadata["label"] != label {
			t.Errorf("event %d = %+v; want hub.forge.label.remove of %q on 42", i, ev, label)
		}
	}
}

// TS-31-24: When the forge says the label removal found nothing and the issue is there, the
// label is already absent: 204 after one read of the issue.
// Requirement: 31-REQ-4.4
func TestRemoveLabel_AbsentLabel_TS_31_24(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"DELETE /repos/acme/widgets/issues/42/labels/af:fix": respond(http.StatusNotFound, `{"message":"Label does not exist"}`),
		"GET /repos/acme/widgets/issues/42":                  respond(http.StatusOK, issueBody(42, "t", "")),
	}))

	rec := e.send(http.MethodDelete, "/workspaces/widgets/issues/42/labels/af:fix", apiKeyAuth(), "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d; want 204\n%s", rec.Code, rec.Body)
	}
	got := e.stub.received()
	if len(got) != 2 || got[0].Method != http.MethodDelete || got[1].Method != http.MethodGet || got[1].Path != "/repos/acme/widgets/issues/42" {
		t.Errorf("the forge received %+v; want the removal, then exactly one read of the issue", got)
	}
	events := em.recorded()
	if len(events) != 1 || events[0].Metadata["ok"] != true || events[0].Metadata["label"] != "af:fix" {
		t.Errorf("events = %+v; want one successful removal", events)
	}
}

// TS-31-25: When the forge says not found to the removal and to the read of the issue,
// the issue is missing: 404 forge_not_found, after one read.
// Requirement: 31-REQ-4.5
func TestRemoveLabel_MissingIssue_TS_31_25(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	e.stub.setHandler(respond(http.StatusNotFound, `{"message":"Not Found"}`))

	rec := e.send(http.MethodDelete, "/workspaces/widgets/issues/99/labels/af:fix", apiKeyAuth(), "")
	if got := decodeError(t, rec); rec.Code != http.StatusNotFound || got.ErrorType != "forge_not_found" {
		t.Errorf("answer = %d %+v; want 404 forge_not_found", rec.Code, got)
	}
	reads := 0
	for _, r := range e.stub.received() {
		if r.Method == http.MethodGet {
			reads++
		}
	}
	if got := e.stub.received(); len(got) != 2 || reads != 1 {
		t.Errorf("the forge received %+v; want the removal and one read", got)
	}
	events := em.recorded()
	if len(events) != 1 || events[0].EventType != audit.EventForgeLabelRemove || events[0].Metadata["ok"] != false ||
		events[0].Metadata["error_type"] != "forge_not_found" {
		t.Errorf("events = %+v; want one failed hub.forge.label.remove", events)
	}
}

// Anything but not found is the removal's own answer, with no read of the issue; and a read
// that fails otherwise than with not found is that failure.
// Requirement: 31-REQ-4.4
func TestRemoveLabel_OtherFailures(t *testing.T) {
	e := newWriteEnv(t)
	e.stub.setHandler(respond(http.StatusForbidden, `{"message":"Forbidden"}`))
	rec := e.send(http.MethodDelete, "/workspaces/widgets/issues/42/labels/af:fix", apiKeyAuth(), "")
	if got := decodeError(t, rec); rec.Code != http.StatusBadGateway || got.ErrorType != "forge_forbidden" {
		t.Errorf("answer = %d %+v; want 502 forge_forbidden", rec.Code, got)
	}
	if n := e.stub.count(); n != 1 {
		t.Errorf("the forge received %d requests; want only the removal", n)
	}

	e = newWriteEnv(t)
	e.stub.setHandler(routes(map[string]http.HandlerFunc{
		"DELETE /repos/acme/widgets/issues/42/labels/af:fix": respond(http.StatusNotFound, `{"message":"Not Found"}`),
		"GET /repos/acme/widgets/issues/42":                  respond(http.StatusInternalServerError, `{"message":"boom"}`),
	}))
	rec = e.send(http.MethodDelete, "/workspaces/widgets/issues/42/labels/af:fix", apiKeyAuth(), "")
	if got := decodeError(t, rec); rec.Code != http.StatusBadGateway || got.ErrorType != "forge_error" {
		t.Errorf("answer = %d %+v; want 502 forge_error, the read's failure", rec.Code, got)
	}

	rec = e.send(http.MethodDelete, "/workspaces/widgets/issues/42/labels/%20", apiKeyAuth(), "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a blank label: status = %d; want 400", rec.Code)
	}
}

// TS-31-26: Label add and remove answer 204 on GitHub and on GitLab alike, and removing a
// label from a GitLab issue that is not there is not found, as on GitHub.
// Requirement: 31-REQ-4.6
func TestLabels_BothForges_TS_31_26(t *testing.T) {
	gh := newWriteEnv(t)
	gh.stub.setHandler(routes(map[string]http.HandlerFunc{
		"POST /repos/acme/widgets/issues/42/labels":          respond(http.StatusOK, labelsAnswer),
		"DELETE /repos/acme/widgets/issues/42/labels/af:fix": respond(http.StatusOK, labelsAnswer),
	}))
	gl := newGitLabWriteEnv(t)
	gl.stub.setHandler(routes(map[string]http.HandlerFunc{
		"PUT /api/v4/projects/acme/widgets/issues/42": respond(http.StatusOK, gitlabIssueBody(42, "t", "", "opened", "af:ready")),
	}))

	for _, tc := range []struct {
		forge string
		env   *env
		slug  string
	}{{"github", gh, "widgets"}, {"gitlab", gl, "lab"}} {
		add := tc.env.send(http.MethodPost, "/workspaces/"+tc.slug+"/issues/42/labels", apiKeyAuth(), `["af:ready"]`)
		remove := tc.env.send(http.MethodDelete, "/workspaces/"+tc.slug+"/issues/42/labels/af:fix", apiKeyAuth(), "")
		if add.Code != http.StatusNoContent || remove.Code != http.StatusNoContent {
			t.Errorf("%s: add %d, remove %d; want 204 and 204", tc.forge, add.Code, remove.Code)
		}
	}
	got := gl.stub.received()
	if len(got) != 2 || jsonOf(t, got[0].Body)["add_labels"] != "af:ready" || jsonOf(t, got[1].Body)["remove_labels"] != "af:fix" {
		t.Errorf("GitLab received %+v; want add_labels af:ready, then remove_labels af:fix", got)
	}

	// A GitLab issue that is not there: the removal's 404 is followed by one read, and it is 404 too.
	gl.stub.setHandler(respond(http.StatusNotFound, `{"message":"404 Not found"}`))
	rec := gl.send(http.MethodDelete, "/workspaces/lab/issues/99/labels/af:fix", apiKeyAuth(), "")
	if got := decodeError(t, rec); rec.Code != http.StatusNotFound || got.ErrorType != "forge_not_found" {
		t.Errorf("GitLab missing issue: %d %+v; want 404 forge_not_found", rec.Code, got)
	}
	// ... and one that is there loses a label it never had without complaint.
	gl.stub.setHandler(routes(map[string]http.HandlerFunc{
		"PUT /api/v4/projects/acme/widgets/issues/42": respond(http.StatusNotFound, `{"message":"404 Not found"}`),
		"GET /api/v4/projects/acme/widgets/issues/42": respond(http.StatusOK, gitlabIssueBody(42, "t", "", "opened")),
	}))
	if rec := gl.send(http.MethodDelete, "/workspaces/lab/issues/42/labels/af:fix", apiKeyAuth(), ""); rec.Code != http.StatusNoContent {
		t.Errorf("GitLab absent label: status = %d; want 204", rec.Code)
	}
}
