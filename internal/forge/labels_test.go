package forge

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/agent-fox-dev/aflib/issuex"

	"github.com/agent-fox-dev/hub/internal/testutil"
)

const githubLabelPath = "/repos/acme/widgets/labels"

// Adding labels sends every name and answers 204 without a body; a refusal of the forge
// is mapped. GitHub and GitLab alike.
func TestAddLabels(t *testing.T) {
	e := widgetsEnv(t)
	e.stub.setHandler(githubWrites())
	rec := e.send(http.MethodPost, "/workspaces/widgets/issues/42/labels", apiKeyAuth(), `["af:ready","af:fix"]`)
	if got := e.stub.received(); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 || len(got) != 1 || fmt.Sprint(jsonOf(t, got[0].Body)["labels"]) != "[af:ready af:fix]" {
		t.Errorf("%d %q after %+v; want 204, no body and one request with both names", rec.Code, rec.Body, got)
	}
	e.stub.setHandler(answer(422, "Validation Failed"))
	if rec := e.send(http.MethodPost, "/workspaces/widgets/issues/42/labels", apiKeyAuth(), `["no-such-label"]`); rec.Code != http.StatusUnprocessableEntity || testutil.Error(t, rec).ErrorType != "forge_rejected" {
		t.Errorf("a refused add: %d %s; want 422 forge_rejected", rec.Code, rec.Body)
	}
	gl := gitlabEnv(t)
	gl.stub.setHandler(respond(http.StatusOK, gitlabIssueBody(42, "t", "", "opened", "af:ready")))
	if rec := gl.send(http.MethodPost, "/workspaces/lab/issues/42/labels", apiKeyAuth(), `["af:ready"]`); rec.Code != http.StatusNoContent || jsonOf(t, gl.stub.received()[0].Body)["add_labels"] != "af:ready" {
		t.Errorf("GitLab: %d after %+v; want 204 and add_labels af:ready", rec.Code, gl.stub.received())
	}
}

// The label to remove is the last path segment decoded once, with . and .. sent as %2E so
// that no dot segment is resolved; the removal is 204 also when the issue never had the
// label (GitHub says not found and hub reads the issue once); when the issue is missing
// too it is 404 forge_not_found; any other failure of the removal or the read is its own.
func TestRemoveLabel(t *testing.T) {
	e := widgetsEnv(t)
	e.stub.setHandler(respond(http.StatusOK, labelsAnswer))
	for segment, sent := range map[string]string{"type%2Fbug": "type%2Fbug", "af:fix": "af:fix", "100%2525": "100%2525", "needs%20triage": "needs%20triage", "%2E": "%2E", "%2E%2E": "%2E%2E"} {
		e.stub.forget()
		rec := e.send(http.MethodDelete, "/workspaces/widgets/issues/42/labels/"+segment, apiKeyAuth(), "")
		if got := e.stub.received(); rec.Code != http.StatusNoContent || len(got) != 1 || got[0].Method != http.MethodDelete || got[0].EscapedPath != "/repos/acme/widgets/issues/42/labels/"+sent {
			t.Errorf("DELETE .../labels/%s: %d after %+v; want 204 and DELETE .../labels/%s", segment, rec.Code, got, sent)
		}
	}
	for _, tc := range []struct {
		name       string
		handler    http.HandlerFunc
		status     int
		errorType  string
		requests   int
	}{
		{"an absent label", routes(map[string]http.HandlerFunc{"DELETE /repos/acme/widgets/issues/42/labels/af:fix": answer(404, "Label does not exist"), "GET /repos/acme/widgets/issues/42": respond(http.StatusOK, issueBody(42, "t", ""))}), 204, "", 2},
		{"a missing issue", answer(404, "Not Found"), 404, "forge_not_found", 2},
		{"a refused removal", answer(403, "Forbidden"), 502, "forge_forbidden", 1},
		{"a failed read", routes(map[string]http.HandlerFunc{"DELETE /repos/acme/widgets/issues/42/labels/af:fix": answer(404, "Not Found"), "GET /repos/acme/widgets/issues/42": answer(500, "boom")}), 502, "forge_error", 2},
	} {
		e.stub.setHandler(tc.handler)
		e.stub.forget()
		rec := e.send(http.MethodDelete, "/workspaces/widgets/issues/42/labels/af:fix", apiKeyAuth(), "")
		errorType := ""
		if rec.Code >= 400 {
			errorType = testutil.Error(t, rec).ErrorType
		}
		if rec.Code != tc.status || errorType != tc.errorType || e.stub.count() != tc.requests {
			t.Errorf("%s: %d %s after %d requests; want %d %s after %d", tc.name, rec.Code, errorType, e.stub.count(), tc.status, tc.errorType, tc.requests)
		}
	}

	gl := gitlabEnv(t)
	gl.stub.setHandler(respond(http.StatusOK, gitlabIssueBody(42, "t", "", "opened")))
	if rec := gl.send(http.MethodDelete, "/workspaces/lab/issues/42/labels/af:fix", apiKeyAuth(), ""); rec.Code != http.StatusNoContent || jsonOf(t, gl.stub.received()[0].Body)["remove_labels"] != "af:fix" {
		t.Errorf("GitLab: %d after %+v; want 204 and remove_labels af:fix", rec.Code, gl.stub.received())
	}
	gl.stub.setHandler(answer(404, "404 Not found"))
	if rec := gl.send(http.MethodDelete, "/workspaces/lab/issues/99/labels/af:fix", apiKeyAuth(), ""); rec.Code != http.StatusNotFound || testutil.Error(t, rec).ErrorType != "forge_not_found" {
		t.Errorf("GitLab missing issue: %d %s; want 404 forge_not_found", rec.Code, rec.Body)
	}
}

// Ensuring a label looks it up first and answers one that exists 200 as the forge holds it,
// unchanged; creates a missing one with the color without its # (ededed by default) and
// answers 201; after a refused create looks once more, answering the label another call
// made 200 or the create's error; a lookup that fails otherwise is its own failure. The
// name is escaped as a path segment in the lookup and sent as it is in the create; on
// GitLab the project is one segment and the color keeps its #.
func TestEnsureLabel(t *testing.T) {
	e := widgetsEnv(t)
	stub := newLabelStub(githubLabelPath, issuex.Label{Name: "af:triage", Color: "ff0000", Description: "old"})
	e.stub.setHandler(stub.handler)
	rec := e.send(http.MethodPost, "/workspaces/widgets/labels", apiKeyAuth(), `{"name":"af:triage","color":"00ff00","description":"new"}`)
	var label issuex.Label
	testutil.Decode(t, rec, &label)
	if rec.Code != http.StatusOK || label != (issuex.Label{Name: "af:triage", Color: "ff0000", Description: "old"}) || stub.creates != 0 {
		t.Errorf("an existing label: %d %s after %d creates; want 200 as the forge holds it and no create", rec.Code, rec.Body, stub.creates)
	}
	first := e.send(http.MethodPost, "/workspaces/widgets/labels", apiKeyAuth(), `{"name":"type/bug needs triage","description":"d"}`)
	second := e.send(http.MethodPost, "/workspaces/widgets/labels", apiKeyAuth(), `{"name":"b","color":"#AABBCC"}`)
	again := e.send(http.MethodPost, "/workspaces/widgets/labels", apiKeyAuth(), `{"name":"b"}`)
	testutil.Decode(t, first, &label)
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated || again.Code != http.StatusOK || label != (issuex.Label{Name: "type/bug needs triage", Color: "ededed", Description: "d"}) {
		t.Errorf("creates: %d %s, %d, then %d; want 201 with the default color, 201, then 200", first.Code, first.Body, second.Code, again.Code)
	}
	got := e.stub.received()
	if len(got) != 6 || got[1].EscapedPath != githubLabelPath+"/type%2Fbug%20needs%20triage" || jsonOf(t, got[2].Body)["name"] != "type/bug needs triage" || jsonOf(t, got[4].Body)["color"] != "AABBCC" || stub.creates != 2 {
		t.Errorf("the forge received %+v; want the escaped lookups, the names as given and the color without its #", got)
	}
	for _, name := range []string{".", ".."} {
		e.stub.forget()
		e.send(http.MethodPost, "/workspaces/widgets/labels", apiKeyAuth(), `{"name":"`+name+`"}`)
		if want := githubLabelPath + "/" + strings.ReplaceAll(name, ".", "%2E"); e.stub.received()[0].EscapedPath != want {
			t.Errorf("the lookup of %q went to %s; want %s", name, e.stub.received()[0].EscapedPath, want)
		}
	}

	lookups := 0
	e.stub.setHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if lookups++; lookups == 1 {
				answer(404, "Not Found")(w, r)
				return
			}
			respond(http.StatusOK, `{"name":"af:x","color":"123456","description":"theirs"}`)(w, r)
			return
		}
		answer(422, "already_exists")(w, r)
	})
	rec = e.send(http.MethodPost, "/workspaces/widgets/labels", apiKeyAuth(), `{"name":"af:x"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "theirs") || lookups != 2 {
		t.Errorf("a create that lost a race: %d %s after %d lookups; want 200 with the other's label after 2", rec.Code, rec.Body, lookups)
	}
	for _, tc := range []struct {
		name      string
		stub      *labelStub
		status    int
		errorType string
		lookups   int
	}{
		{"a create still refused", &labelStub{prefix: githubLabelPath, labels: map[string]string{}, refuseCreate: 422}, 422, "forge_rejected", 2},
		{"a create refused with 403", &labelStub{prefix: githubLabelPath, labels: map[string]string{}, refuseCreate: 403}, 502, "forge_forbidden", 2},
	} {
		e.stub.setHandler(tc.stub.handler)
		rec := e.send(http.MethodPost, "/workspaces/widgets/labels", apiKeyAuth(), `{"name":"af:triage"}`)
		if rec.Code != tc.status || testutil.Error(t, rec).ErrorType != tc.errorType || tc.stub.lookups != tc.lookups || tc.stub.creates != 1 {
			t.Errorf("%s: %d %s after %d lookups and %d creates; want %d %s after %d and 1", tc.name, rec.Code, rec.Body, tc.stub.lookups, tc.stub.creates, tc.status, tc.errorType, tc.lookups)
		}
	}
	e.stub.setHandler(answer(403, "Forbidden"))
	e.stub.forget()
	if rec := e.send(http.MethodPost, "/workspaces/widgets/labels", apiKeyAuth(), `{"name":"af:triage"}`); rec.Code != http.StatusBadGateway || e.stub.count() != 1 {
		t.Errorf("a refused lookup: %d after %d requests; want 502 after the lookup alone", rec.Code, e.stub.count())
	}

	gl := gitlabEnv(t)
	glStub := newLabelStub(gitlabLabelPath)
	gl.stub.setHandler(glStub.handler)
	first = gl.send(http.MethodPost, "/workspaces/lab/labels", apiKeyAuth(), `{"name":"af:triage","description":"d"}`)
	second = gl.send(http.MethodPost, "/workspaces/lab/labels", apiKeyAuth(), `{"name":"af:triage"}`)
	testutil.Decode(t, second, &label)
	got = gl.stub.received()
	if first.Code != http.StatusCreated || second.Code != http.StatusOK || label != (issuex.Label{Name: "af:triage", Color: "#ededed", Description: "d"}) ||
		len(got) != 3 || got[0].EscapedPath != "/api/v4/projects/acme%2Fwidgets/labels/af:triage" || got[1].Header.Get("PRIVATE-TOKEN") != "origin-token" {
		t.Errorf("GitLab: %d, %d %s after %+v; want 201 then 200 with the # kept, on the escaped project", first.Code, second.Code, second.Body, got)
	}
}
