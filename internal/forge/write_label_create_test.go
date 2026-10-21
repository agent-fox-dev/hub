package forge

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/agent-fox-dev/aflib/issuex"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// labelStub is the labels of acme/widgets on GitHub: it holds the labels created through it,
// answers a lookup of one by name and counts the lookups and the creates.
type labelStub struct {
	mu      sync.Mutex
	labels  map[string]issuex.Label
	lookups int
	creates int
	// refuseCreate, when set, is the status the create answers instead of creating.
	refuseCreate int
}

func newLabelStub(existing ...issuex.Label) *labelStub {
	s := &labelStub{labels: map[string]issuex.Label{}}
	for _, l := range existing {
		s.labels[l.Name] = l
	}
	return s
}

func (s *labelStub) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	const prefix = "/repos/acme/widgets/labels"
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, prefix+"/"):
		s.lookups++
		if l, ok := s.labels[strings.TrimPrefix(r.URL.Path, prefix+"/")]; ok {
			respond(http.StatusOK, labelJSON(l))(w, r)
			return
		}
		respond(http.StatusNotFound, `{"message":"Not Found"}`)(w, r)
	case r.Method == http.MethodPost && r.URL.Path == prefix:
		s.creates++
		if s.refuseCreate != 0 {
			respond(s.refuseCreate, `{"message":"Validation Failed"}`)(w, r)
			return
		}
		var l issuex.Label
		_ = json.NewDecoder(r.Body).Decode(&l)
		s.labels[l.Name] = l
		respond(http.StatusCreated, labelJSON(l))(w, r)
	default:
		respond(http.StatusNotFound, `{"message":"Not Found"}`)(w, r)
	}
}

// labelJSON is a label as GitHub answers it.
func labelJSON(l issuex.Label) string {
	b, _ := json.Marshal(map[string]any{"name": l.Name, "color": l.Color, "description": l.Description})
	return string(b)
}

// TS-31-27: A POST /labels body with no name, a blank name or a color that is not six
// hex digits answers 400 naming the field, and the forge receives nothing.
// Requirement: 31-REQ-5.1
func TestEnsureLabel_InvalidBodies_TS_31_27(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	for _, tc := range []struct{ name, body, field string }{
		{"no name", `{"color":"ff0000"}`, "name"},
		{"a blank name", `{"name":"  "}`, "name"},
		{"a name that is no string", `{"name":3}`, "name"},
		{"the color red", `{"name":"a","color":"red"}`, "color"},
		{"five digits", `{"name":"a","color":"12345"}`, "color"},
		{"seven digits", `{"name":"a","color":"1234567"}`, "color"},
		{"a non-hex digit", `{"name":"a","color":"#12345g"}`, "color"},
		{"an empty color", `{"name":"a","color":""}`, "color"},
		{"two hashes", `{"name":"a","color":"##123456"}`, "color"},
		{"a color that is no string", `{"name":"a","color":123456}`, "color"},
		{"a description that is no string", `{"name":"a","description":[]}`, "description"},
	} {
		rec := e.send(http.MethodPost, "/workspaces/widgets/labels", apiKeyAuth(), tc.body)
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

// TS-31-28: A label that exists is answered as the forge holds it, 200, and neither a create
// nor an update is sent, whatever color and description the request carries.
// Requirement: 31-REQ-5.2
func TestEnsureLabel_ExistingIsReturnedUnchanged_TS_31_28(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	stub := newLabelStub(issuex.Label{Name: "af:triage", Color: "ff0000", Description: "old"})
	e.stub.setHandler(stub.handler)

	rec := e.send(http.MethodPost, "/workspaces/widgets/labels", apiKeyAuth(), `{"name":"af:triage","color":"00ff00","description":"new"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200\n%s", rec.Code, rec.Body)
	}
	var label issuex.Label
	if err := json.Unmarshal(rec.Body.Bytes(), &label); err != nil || label != (issuex.Label{Name: "af:triage", Color: "ff0000", Description: "old"}) {
		t.Errorf("body %s; want the label as the forge holds it (ff0000, old) (%v)", rec.Body, err)
	}
	got := e.stub.received()
	if len(got) != 1 || got[0].Method != http.MethodGet || got[0].Path != "/repos/acme/widgets/labels/af:triage" {
		t.Errorf("the forge received %+v; want the lookup and nothing else", got)
	}
	events := em.recorded()
	if len(events) != 1 {
		t.Fatalf("%d events recorded; want 1", len(events))
	}
	ev := events[0]
	if ev.EventType != audit.EventForgeLabelCreate || ev.ResourceType != "label" || ev.ResourceID != "af:triage" || ev.Action != "create" ||
		ev.Metadata["label"] != "af:triage" || ev.Metadata["created"] != false || ev.Metadata["ok"] != true {
		t.Errorf("event = %+v; want hub.forge.label.create of af:triage with created false", ev)
	}
}

// TS-31-29: A label that is missing is created with the given name and description and the
// color without its #, ededed when there is none, and answered 201.
// Requirement: 31-REQ-5.3
func TestEnsureLabel_CreatesWithTheDefaultColor_TS_31_29(t *testing.T) {
	e := newWriteEnv(t)
	e.stub.setHandler(newLabelStub().handler)

	first := e.send(http.MethodPost, "/workspaces/widgets/labels", apiKeyAuth(), `{"name":"af:triage","description":"d"}`)
	second := e.send(http.MethodPost, "/workspaces/widgets/labels", apiKeyAuth(), `{"name":"b","color":"#AABBCC"}`)
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated {
		t.Fatalf("statuses = %d and %d; want 201 and 201\n%s\n%s", first.Code, second.Code, first.Body, second.Body)
	}
	var created issuex.Label
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil || created != (issuex.Label{Name: "af:triage", Color: "ededed", Description: "d"}) {
		t.Errorf("first body %s; want the created label (ededed, d) (%v)", first.Body, err)
	}

	var creates []recordedRequest
	for _, r := range e.stub.received() {
		if r.Method == http.MethodPost {
			creates = append(creates, r)
		}
	}
	if len(creates) != 2 {
		t.Fatalf("%d creates; want 2", len(creates))
	}
	if sent := jsonOf(t, creates[0].Body); sent["name"] != "af:triage" || sent["color"] != "ededed" || sent["description"] != "d" || len(sent) != 3 {
		t.Errorf("the first create carried %s; want name, color ededed and description d", creates[0].Body)
	}
	if sent := jsonOf(t, creates[1].Body); sent["name"] != "b" || sent["color"] != "AABBCC" || len(sent) != 2 {
		t.Errorf("the second create carried %s; want name b and the color without its #", creates[1].Body)
	}
}

// TS-31-30: A create the forge refuses is followed by exactly one more lookup: a label that
// is found now is answered 200, one that is not is the create's error.
// Requirement: 31-REQ-5.4
func TestEnsureLabel_RefusedCreateLooksAgain_TS_31_30(t *testing.T) {
	// The first lookup finds nothing; the label appears before the second.
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	lookups := 0
	e.stub.setHandler(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			lookups++
			if lookups == 1 {
				respond(http.StatusNotFound, `{"message":"Not Found"}`)(w, r)
				return
			}
			respond(http.StatusOK, `{"name":"af:triage","color":"123456","description":"theirs"}`)(w, r)
		default:
			respond(http.StatusUnprocessableEntity, `{"message":"Validation Failed","errors":[{"resource":"Label","code":"already_exists","field":"name"}]}`)(w, r)
		}
	})
	rec := e.send(http.MethodPost, "/workspaces/widgets/labels", apiKeyAuth(), `{"name":"af:triage"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200\n%s", rec.Code, rec.Body)
	}
	var label issuex.Label
	if err := json.Unmarshal(rec.Body.Bytes(), &label); err != nil || label.Description != "theirs" {
		t.Errorf("body %s; want the label the other call created", rec.Body)
	}
	if lookups != 2 || e.stub.count() != 3 {
		t.Errorf("%d lookups and %d requests; want 2 and 3", lookups, e.stub.count())
	}
	if events := em.recorded(); len(events) != 1 || events[0].Metadata["created"] != false || events[0].Metadata["ok"] != true {
		t.Errorf("events = %+v; want one event with created false", events)
	}

	// The label is still not there at the second lookup: the create's own failure.
	em = &recordingEmitter{}
	e = newWriteEnv(t, withEmitter(em))
	stub := newLabelStub()
	stub.refuseCreate = http.StatusUnprocessableEntity
	e.stub.setHandler(stub.handler)
	rec = e.send(http.MethodPost, "/workspaces/widgets/labels", apiKeyAuth(), `{"name":"af:triage"}`)
	if got := decodeError(t, rec); rec.Code != http.StatusUnprocessableEntity || got.ErrorType != "forge_rejected" {
		t.Errorf("answer = %d %+v; want 422 forge_rejected, the create's", rec.Code, got)
	}
	if stub.lookups != 2 || stub.creates != 1 {
		t.Errorf("%d lookups and %d creates; want 2 and 1", stub.lookups, stub.creates)
	}
	if events := em.recorded(); len(events) != 1 || events[0].Metadata["created"] != false || events[0].Metadata["ok"] != false ||
		events[0].Metadata["error_type"] != "forge_rejected" {
		t.Errorf("events = %+v; want one failed event with created false", events)
	}
}

// TS-31-31: Two sequential creates of one label answer 201 and then 200, and the forge sees
// one create across both.
// Requirement: 31-REQ-5.5
func TestEnsureLabel_Twice_TS_31_31(t *testing.T) {
	em := &recordingEmitter{}
	e := newWriteEnv(t, withEmitter(em))
	stub := newLabelStub()
	e.stub.setHandler(stub.handler)

	first := e.send(http.MethodPost, "/workspaces/widgets/labels", apiKeyAuth(), `{"name":"af:triage"}`)
	second := e.send(http.MethodPost, "/workspaces/widgets/labels", apiKeyAuth(), `{"name":"af:triage"}`)
	if first.Code != http.StatusCreated || second.Code != http.StatusOK {
		t.Errorf("statuses = %d and %d; want 201 and 200", first.Code, second.Code)
	}
	if stub.creates != 1 {
		t.Errorf("the forge saw %d creates; want 1", stub.creates)
	}
	events := em.recorded()
	if len(events) != 2 || events[0].Metadata["created"] != true || events[1].Metadata["created"] != false {
		t.Errorf("events = %+v; want created true, then created false", events)
	}
}

// A lookup that fails otherwise than with not found is its own failure, and nothing is
// created; a create that fails with a refusal other than a conflict is looked up again too.
// Requirement: 31-REQ-5.3, 31-REQ-5.4
func TestEnsureLabel_Failures(t *testing.T) {
	e := newWriteEnv(t)
	e.stub.setHandler(respond(http.StatusForbidden, `{"message":"Forbidden"}`))
	rec := e.send(http.MethodPost, "/workspaces/widgets/labels", apiKeyAuth(), `{"name":"af:triage"}`)
	if got := decodeError(t, rec); rec.Code != http.StatusBadGateway || got.ErrorType != "forge_forbidden" {
		t.Errorf("a lookup refused: %d %+v; want 502 forge_forbidden", rec.Code, got)
	}
	if n := e.stub.count(); n != 1 {
		t.Errorf("the forge received %d requests; want only the lookup", n)
	}

	e = newWriteEnv(t)
	stub := newLabelStub()
	stub.refuseCreate = http.StatusForbidden
	e.stub.setHandler(stub.handler)
	rec = e.send(http.MethodPost, "/workspaces/widgets/labels", apiKeyAuth(), `{"name":"af:triage"}`)
	if got := decodeError(t, rec); rec.Code != http.StatusBadGateway || got.ErrorType != "forge_forbidden" {
		t.Errorf("a create refused with 403: %d %+v; want 502 forge_forbidden", rec.Code, got)
	}
	if stub.lookups != 2 {
		t.Errorf("%d lookups; want 2", stub.lookups)
	}
}

// On GitLab the label is looked up and created in the project, and the color goes with its #.
// Requirement: 31-REQ-5.2, 31-REQ-5.3
func TestEnsureLabel_GitLab(t *testing.T) {
	em := &recordingEmitter{}
	e := newGitLabWriteEnv(t, withEmitter(em))
	labels := map[string]string{}
	e.stub.setHandler(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "/api/v4/projects/acme/widgets/labels"
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, prefix+"/"):
			if body, ok := labels[strings.TrimPrefix(r.URL.Path, prefix+"/")]; ok {
				respond(http.StatusOK, body)(w, r)
				return
			}
			respond(http.StatusNotFound, `{"message":"404 Label Not Found"}`)(w, r)
		case r.Method == http.MethodPost && r.URL.Path == prefix:
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			labels[in["name"]] = fmt.Sprintf(`{"id":1,"name":%q,"color":%q,"description":%q}`, in["name"], in["color"], in["description"])
			respond(http.StatusCreated, labels[in["name"]])(w, r)
		default:
			respond(http.StatusNotFound, `{}`)(w, r)
		}
	})

	first := e.send(http.MethodPost, "/workspaces/lab/labels", apiKeyAuth(), `{"name":"af:triage","description":"d"}`)
	second := e.send(http.MethodPost, "/workspaces/lab/labels", apiKeyAuth(), `{"name":"af:triage"}`)
	if first.Code != http.StatusCreated || second.Code != http.StatusOK {
		t.Fatalf("statuses = %d and %d; want 201 and 200\n%s\n%s", first.Code, second.Code, first.Body, second.Body)
	}
	var label issuex.Label
	if err := json.Unmarshal(second.Body.Bytes(), &label); err != nil || label != (issuex.Label{Name: "af:triage", Color: "#ededed", Description: "d"}) {
		t.Errorf("second body %s; want the label as GitLab holds it, with its #", second.Body)
	}
	got := e.stub.received()
	if len(got) != 3 || got[0].Method != http.MethodGet || got[1].Method != http.MethodPost || got[2].Method != http.MethodGet {
		t.Fatalf("GitLab received %+v; want lookup, create, lookup", got)
	}
	for _, r := range got {
		if r.Header.Get("PRIVATE-TOKEN") != "origin-token" {
			t.Errorf("%s %s carried the token %q", r.Method, r.Path, r.Header.Get("PRIVATE-TOKEN"))
		}
	}
	if sent := jsonOf(t, got[1].Body); sent["color"] != "#ededed" || sent["name"] != "af:triage" || sent["description"] != "d" {
		t.Errorf("the create carried %s; want the color with a #", got[1].Body)
	}
	if got[0].EscapedPath != "/api/v4/projects/acme%2Fwidgets/labels/af:triage" {
		t.Errorf("the lookup went to %s; want the project as one escaped segment", got[0].EscapedPath)
	}
}

// A label name is escaped as a path segment in the lookup, and sent as it is in the create.
// Requirement: 31-REQ-5.2
func TestEnsureLabel_NameWithSlashAndSpace(t *testing.T) {
	e := newWriteEnv(t)
	stub := newLabelStub()
	e.stub.setHandler(stub.handler)

	body, _ := json.Marshal(map[string]string{"name": "type/bug needs triage"})
	if rec := e.send(http.MethodPost, "/workspaces/widgets/labels", apiKeyAuth(), string(body)); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; want 201\n%s", rec.Code, rec.Body)
	}
	got := e.stub.received()
	if len(got) != 2 || got[0].EscapedPath != "/repos/acme/widgets/labels/type%2Fbug%20needs%20triage" {
		t.Errorf("the forge received %+v; want the lookup of the escaped name, then the create", got)
	}
	if sent := jsonOf(t, got[1].Body); sent["name"] != "type/bug needs triage" {
		t.Errorf("the create carried %s; want the name as given", got[1].Body)
	}
}
