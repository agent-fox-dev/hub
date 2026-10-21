package forge

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/agent-fox-dev/hub/internal/audit"
)

// gitlabPayload is a GitLab delivery of the project acme/widgets from alice, with the fields of
// its kind added.
func gitlabPayload(fields map[string]any) string {
	doc := map[string]any{
		"user":    map[string]any{"username": "alice"},
		"project": map[string]any{"path_with_namespace": "acme/widgets"},
	}
	for k, v := range fields {
		doc[k] = v
	}
	b, _ := json.Marshal(doc)
	return string(b)
}

// titles makes the list of labels GitLab sends, each an object with a title.
func titles(names ...string) []any {
	out := []any{}
	for _, name := range names {
		out = append(out, map[string]any{"id": len(name), "title": name})
	}
	return out
}

// gitlabUpdateHook is an Issue Hook update of issue 7: changes holds what changed and labels the labels
// the issue has now.
func gitlabUpdateHook(changes map[string]any, labels ...string) string {
	return gitlabPayload(map[string]any{
		"object_kind": "issue",
		"object_attributes": map[string]any{
			"iid": 7, "title": "Widgets break", "action": "update", "url": "https://gitlab.example.com/acme/widgets/-/issues/7",
		},
		"changes": changes,
		"labels":  titles(labels...),
	})
}

// labelChange is the changes.labels object of an update from previous to current.
func labelChange(previous, current []string) map[string]any {
	return map[string]any{"previous": titles(previous...), "current": titles(current...)}
}

// TS-32-38: An Issue Hook that opens, closes or reopens an issue is the matching event, numbered
// by its iid.
// Requirement: 32-REQ-7.1
func TestGitLabIssueHook_TS_32_38(t *testing.T) {
	e, em := newIngressEnv(t)
	for _, action := range []string{"open", "close", "reopen"} {
		d := gitlabDelivery(ingressSecret, "d-"+action, "Issue Hook", gitlabIssuePayload(action, "acme/widgets"))
		if outcome := outcomeOf(t, e.deliver("widgets", d)); outcome != "relayed" {
			t.Errorf("%s: outcome = %q; want relayed", action, outcome)
		}
	}
	events := em.recorded()
	want := []string{audit.EventForgeIssueOpened, audit.EventForgeIssueClosed, audit.EventForgeIssueReopened}
	if got := eventTypes(events); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v; want %v", got, want)
	}
	for i, ev := range events {
		wantMeta := map[string]any{
			"forge": "gitlab", "target": "origin", "delivery_id": "d-" + []string{"open", "close", "reopen"}[i], "actor": "alice",
			"number": 7, "title": "Widgets break", "url": "https://gitlab.example.com/acme/widgets/-/issues/7", "labels": []string{"bug"},
		}
		if !reflect.DeepEqual(ev.Metadata, wantMeta) {
			t.Errorf("event %d metadata = %#v\nwant %#v", i, ev.Metadata, wantMeta)
		}
		if ev.ResourceType != "issue" || ev.ResourceID != "7" {
			t.Errorf("event %d = %+v; want issue 7", i, ev)
		}
	}
	// An Issue Hook without an iid is malformed.
	bad := gitlabPayload(map[string]any{"object_attributes": map[string]any{"action": "open"}})
	if rec := e.deliver("widgets", gitlabDelivery(ingressSecret, "d-bad", "Issue Hook", bad)); rec.Code != 400 {
		t.Errorf("an Issue Hook without an iid: status = %d; want 400", rec.Code)
	}
}

// TS-32-39: An Issue Hook update is edited, then one unlabeled for each label taken off, then one
// labeled for each label put on, each group sorted by name.
// Requirement: 32-REQ-7.2
func TestGitLabIssueUpdate_TS_32_39(t *testing.T) {
	e, em := newIngressEnv(t)
	first := gitlabUpdateHook(map[string]any{
		"title":  map[string]any{"previous": "a", "current": "b"},
		"labels": labelChange([]string{"z", "keep", "b"}, []string{"y", "keep", "a"}),
	}, "keep", "y", "a")
	second := gitlabUpdateHook(map[string]any{"labels": labelChange([]string{"B", "keep"}, []string{"A", "keep"})}, "A", "keep")
	only := gitlabUpdateHook(map[string]any{"description": map[string]any{"previous": "x", "current": "y"}}, "keep")

	for i, body := range []string{first, second, only} {
		if outcome := outcomeOf(t, e.deliver("widgets", gitlabDelivery(ingressSecret, fmt.Sprintf("d-%d", i), "Issue Hook", body))); outcome != "relayed" {
			t.Errorf("update %d: outcome = %q; want relayed", i, outcome)
		}
	}
	events := em.recorded()
	wantTypes := []string{
		audit.EventForgeIssueEdited, audit.EventForgeIssueUnlabeled, audit.EventForgeIssueUnlabeled, audit.EventForgeIssueLabeled, audit.EventForgeIssueLabeled,
		audit.EventForgeIssueUnlabeled, audit.EventForgeIssueLabeled,
		audit.EventForgeIssueEdited,
	}
	if got := eventTypes(events); !reflect.DeepEqual(got, wantTypes) {
		t.Fatalf("events = %v\nwant %v", got, wantTypes)
	}
	var labels []string
	for _, ev := range events[1:7] {
		labels = append(labels, ev.Metadata["label"].(string))
	}
	if want := []string{"b", "z", "a", "y", "B", "A"}; !reflect.DeepEqual(labels, want) {
		t.Errorf("labels = %v; want %v", labels, want)
	}
	// The labeled events carry all the labels the issue has now.
	if got := events[3].Metadata["labels"]; !reflect.DeepEqual(got, []string{"keep", "y", "a"}) {
		t.Errorf("labeled metadata labels = %v; want the labels the issue has", got)
	}
	// The edit carries the title and the labels, and the row lists the events in order.
	if events[0].Metadata["title"] != "Widgets break" || events[0].Metadata["number"] != 7 {
		t.Errorf("edited metadata = %v", events[0].Metadata)
	}
	rows := e.deliveryRows()
	if len(rows) != 3 || rows[0].relayed != `["hub.forge.issue.edited","hub.forge.issue.unlabeled","hub.forge.issue.unlabeled","hub.forge.issue.labeled","hub.forge.issue.labeled"]` {
		t.Errorf("rows = %+v; want the first row to list the five events in order", rows)
	}
}

// TS-32-40: An Issue Hook update that changes no title, description or label is ignored.
// Requirement: 32-REQ-7.3
func TestGitLabIssueUpdateWithoutChange_TS_32_40(t *testing.T) {
	e, em := newIngressEnv(t)
	for i, body := range []string{
		gitlabUpdateHook(map[string]any{}),
		gitlabUpdateHook(map[string]any{"labels": labelChange([]string{"a", "b"}, []string{"b", "a"})}),
		gitlabUpdateHook(map[string]any{"assignees": []any{}}),
		gitlabUpdateHook(map[string]any{"labels": labelChange(nil, nil)}),
	} {
		rec := e.deliver("widgets", gitlabDelivery(ingressSecret, fmt.Sprintf("d-%d", i), "Issue Hook", body))
		if outcome := outcomeOf(t, rec); outcome != "ignored" {
			t.Errorf("update %d: outcome = %q; want ignored", i, outcome)
		}
	}
	noChanges := gitlabPayload(map[string]any{"object_attributes": map[string]any{"iid": 7, "action": "update"}})
	if outcome := outcomeOf(t, e.deliver("widgets", gitlabDelivery(ingressSecret, "d-none", "Issue Hook", noChanges))); outcome != "ignored" {
		t.Errorf("an update with no changes object: outcome = %q; want ignored", outcome)
	}
	if got := em.recorded(); len(got) != 0 {
		t.Errorf("events = %v; want none", got)
	}
	for i, row := range e.deliveryRows() {
		if row.outcome != "ignored" || row.relayed != "[]" || row.target == nil || *row.target != "origin" {
			t.Errorf("row %d = %+v; want ignored, bound to the origin, nothing relayed", i, row)
		}
	}
}

// noteHook is a Note Hook of the noteable type with the fields of its kind.
func noteHook(noteable, action string, fields map[string]any) string {
	attrs := map[string]any{"noteable_type": noteable, "note": "text", "url": "https://gitlab.example.com/acme/widgets/-/issues/7#note_1"}
	if action != "" {
		attrs["action"] = action
	}
	doc := map[string]any{"object_kind": "note", "object_attributes": attrs}
	for k, v := range fields {
		doc[k] = v
	}
	return gitlabPayload(doc)
}

// TS-32-41: A Note Hook on an issue or a merge request is comment.created, numbered by the iid of
// the issue or merge request.
// Requirement: 32-REQ-7.4
func TestGitLabNoteHook_TS_32_41(t *testing.T) {
	e, em := newIngressEnv(t)
	issue := noteHook("Issue", "", map[string]any{"issue": map[string]any{"iid": 7}})
	mr := noteHook("MergeRequest", "create", map[string]any{"merge_request": map[string]any{"iid": 9}})
	for i, body := range []string{issue, mr} {
		if outcome := outcomeOf(t, e.deliver("widgets", gitlabDelivery(ingressSecret, fmt.Sprintf("d-%d", i), "Note Hook", body))); outcome != "relayed" {
			t.Errorf("note %d: outcome = %q; want relayed", i, outcome)
		}
	}
	events := em.recorded()
	if len(events) != 2 {
		t.Fatalf("%d events; want 2", len(events))
	}
	for i, want := range []struct {
		number int
		pull   bool
	}{{7, false}, {9, true}} {
		ev := events[i]
		if ev.EventType != audit.EventForgeCommentCreated || ev.ResourceType != "comment" || ev.ResourceID != fmt.Sprint(want.number) {
			t.Errorf("event %d = %+v; want comment.created on %d", i, ev, want.number)
		}
		if ev.Metadata["number"] != want.number || ev.Metadata["pull_request"] != want.pull ||
			ev.Metadata["comment_url"] != "https://gitlab.example.com/acme/widgets/-/issues/7#note_1" {
			t.Errorf("event %d metadata = %v", i, ev.Metadata)
		}
	}
	// A note that is edited, one on a commit or a snippet, and one without its issue are not relayed.
	for i, tc := range []struct{ body, want string }{
		{noteHook("Issue", "update", map[string]any{"issue": map[string]any{"iid": 7}}), "ignored"},
		{noteHook("Commit", "", map[string]any{"commit": map[string]any{"id": "abc"}}), "ignored"},
		{noteHook("Snippet", "", map[string]any{"snippet": map[string]any{"id": 3}}), "ignored"},
	} {
		d := gitlabDelivery(ingressSecret, fmt.Sprintf("d-x%d", i), "Note Hook", tc.body)
		if outcome := outcomeOf(t, e.deliver("widgets", d)); outcome != tc.want {
			t.Errorf("note %d: outcome = %q; want %s", i, outcome, tc.want)
		}
	}
	if rec := e.deliver("widgets", gitlabDelivery(ingressSecret, "d-noiid", "Note Hook", noteHook("Issue", "", nil))); rec.Code != 400 {
		t.Errorf("a note on an issue without its iid: status = %d; want 400", rec.Code)
	}
}

// mergeRequestHook is a Merge Request Hook of merge request 9 with the action and the fields added.
func mergeRequestHook(action string, extra map[string]any) string {
	attrs := map[string]any{
		"iid": 9, "action": action, "url": "https://gitlab.example.com/acme/widgets/-/merge_requests/9",
		"source_branch": "feature", "target_branch": "main", "state": "opened",
	}
	for k, v := range extra {
		attrs[k] = v
	}
	return gitlabPayload(map[string]any{"object_kind": "merge_request", "object_attributes": attrs})
}

// TS-32-42: A Merge Request Hook is opened, closed, reopened, merged, or synchronize for an update
// that has an oldrev; an update without one is ignored.
// Requirement: 32-REQ-7.5
func TestGitLabMergeRequestHook_TS_32_42(t *testing.T) {
	e, em := newIngressEnv(t)
	bodies := []string{
		mergeRequestHook("open", nil), mergeRequestHook("close", nil), mergeRequestHook("reopen", nil),
		mergeRequestHook("merge", map[string]any{"state": "merged"}),
		mergeRequestHook("update", map[string]any{"oldrev": "abc123"}),
		mergeRequestHook("update", nil),
	}
	want := []string{"relayed", "relayed", "relayed", "relayed", "relayed", "ignored"}
	for i, body := range bodies {
		rec := e.deliver("widgets", gitlabDelivery(ingressSecret, fmt.Sprintf("d-%d", i), "Merge Request Hook", body))
		if outcome := outcomeOf(t, rec); outcome != want[i] {
			t.Errorf("hook %d: outcome = %q; want %s", i, outcome, want[i])
		}
	}
	wantTypes := []string{
		audit.EventForgePullRequestOpened, audit.EventForgePullRequestClosed, audit.EventForgePullRequestReopened,
		audit.EventForgePullRequestMerged, audit.EventForgePullRequestSynchronize,
	}
	events := em.recorded()
	if got := eventTypes(events); !reflect.DeepEqual(got, wantTypes) {
		t.Fatalf("events = %v; want %v", got, wantTypes)
	}
	for i, ev := range events {
		wantMeta := map[string]any{
			"forge": "gitlab", "target": "origin", "delivery_id": fmt.Sprintf("d-%d", i), "actor": "alice",
			"number": 9, "url": "https://gitlab.example.com/acme/widgets/-/merge_requests/9", "head": "feature", "base": "main",
			"merged": i == 3,
		}
		if !reflect.DeepEqual(ev.Metadata, wantMeta) {
			t.Errorf("event %d metadata = %#v\nwant %#v", i, ev.Metadata, wantMeta)
		}
		if ev.ResourceType != "pull_request" || ev.ResourceID != "9" {
			t.Errorf("event %d = %+v; want merge request 9", i, ev)
		}
	}
}

// TS-32-43: A Push Hook and a Tag Push Hook are hub.forge.push, by the ref.
// Requirement: 32-REQ-7.6
func TestGitLabPushHooks_TS_32_43(t *testing.T) {
	e, em := newIngressEnv(t)
	for i, tc := range []struct{ event, ref string }{{"Push Hook", "refs/heads/main"}, {"Tag Push Hook", "refs/tags/v1"}} {
		body := gitlabPayload(map[string]any{"ref": tc.ref, "before": "1111", "after": "2222", "user_username": "bob", "user": nil})
		if outcome := outcomeOf(t, e.deliver("widgets", gitlabDelivery(ingressSecret, fmt.Sprintf("d-%d", i), tc.event, body))); outcome != "relayed" {
			t.Errorf("%s: outcome = %q; want relayed", tc.event, outcome)
		}
	}
	events := em.recorded()
	if len(events) != 2 {
		t.Fatalf("%d events; want 2", len(events))
	}
	for i, ref := range []string{"refs/heads/main", "refs/tags/v1"} {
		ev := events[i]
		wantMeta := map[string]any{"forge": "gitlab", "target": "origin", "delivery_id": fmt.Sprintf("d-%d", i), "actor": "bob",
			"ref": ref, "before": "1111", "after": "2222"}
		if ev.EventType != audit.EventForgePush || ev.ResourceType != "repository" || ev.ResourceID != ref || ev.Action != "push" ||
			!reflect.DeepEqual(ev.Metadata, wantMeta) {
			t.Errorf("event %d = %+v\nwant a push of %s with %v", i, ev, ref, wantMeta)
		}
	}
	for i, body := range []string{gitlabPayload(nil), gitlabPayload(map[string]any{"ref": ""}), gitlabPayload(map[string]any{"ref": 5})} {
		if rec := e.deliver("widgets", gitlabDelivery(ingressSecret, fmt.Sprintf("d-noref-%d", i), "Push Hook", body)); rec.Code != 400 {
			t.Errorf("a push without a usable ref (%s): status = %d; want 400", body, rec.Code)
		}
	}
}

// keysOf returns the sorted keys of metadata.
func keysOf(metadata map[string]any) []string {
	keys := make([]string, 0, len(metadata))
	for k := range metadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TS-32-44: Every relayed event has the fixed fields of a HubEvent and exactly the metadata keys of
// its kind, and nothing else of the payload.
// Requirement: 32-REQ-8.1, 32-REQ-8.2
func TestEventContents_TS_32_44(t *testing.T) {
	e, em := newIngressEnv(t)
	secretField := "PAYLOAD-ONLY-MARKER"
	extra := func(doc string) string { // a field no event should carry
		return doc[:len(doc)-1] + `,"leaked":"` + secretField + `","sender2":{"email":"` + secretField + `"}}`
	}
	noSender := withoutKey(githubPayload("closed", map[string]any{"issue": map[string]any{"number": 7, "title": "t", "html_url": "https://github.com/acme/widgets/issues/7"}}), "sender")
	noSender = extra(noSender)
	cases := []struct {
		name  string
		d     signedDelivery
		types []string
		extra []string
		res   string
		id    string
		actor string
	}{
		{"a GitHub issue edited", githubDelivery(ingressSecret, "d-1", "issues", extra(issuesPayload("edited", "acme/widgets"))),
			[]string{audit.EventForgeIssueEdited}, []string{"number", "title", "url", "labels"}, "issue", "7", "octocat"},
		{"a GitHub issue labeled", githubDelivery(ingressSecret, "d-2", "issues", extra(issuesPayload("labeled", "acme/widgets"))),
			[]string{audit.EventForgeIssueLabeled}, []string{"number", "label", "labels", "url"}, "issue", "7", "octocat"},
		{"a GitHub issue closed by nobody", githubDelivery(ingressSecret, "d-3", "issues", noSender),
			[]string{audit.EventForgeIssueClosed}, []string{"number", "title", "url", "labels"}, "issue", "7", ""},
		{"a GitHub comment", githubDelivery(ingressSecret, "d-4", "issue_comment", extra(githubPayload("created", map[string]any{"issue": map[string]any{"number": 7}}))),
			[]string{audit.EventForgeCommentCreated}, []string{"number", "comment_url", "pull_request"}, "comment", "7", "octocat"},
		{"a GitHub pull request merged", githubDelivery(ingressSecret, "d-5", "pull_request", extra(pullPayload("closed", true))),
			[]string{audit.EventForgePullRequestMerged}, []string{"number", "url", "head", "base", "merged"}, "pull_request", "12", "octocat"},
		{"a GitHub push", githubDelivery(ingressSecret, "d-6", "push", extra(githubPayload("", map[string]any{"ref": "refs/heads/main"}))),
			[]string{audit.EventForgePush}, []string{"ref", "before", "after"}, "repository", "refs/heads/main", "octocat"},
		{"a GitLab issue update", gitlabDelivery(ingressSecret, "d-7", "Issue Hook", extra(gitlabUpdateHook(map[string]any{
			"title": map[string]any{"previous": "a", "current": "b"}, "labels": labelChange([]string{"x"}, []string{"y"})}, "y"))),
			[]string{audit.EventForgeIssueEdited, audit.EventForgeIssueUnlabeled, audit.EventForgeIssueLabeled}, nil, "issue", "7", "alice"},
	}
	base := []string{"actor", "delivery_id", "forge", "target"}
	for _, tc := range cases {
		before := len(em.recorded())
		if outcome := outcomeOf(t, e.deliver("widgets", tc.d)); outcome != "relayed" {
			t.Errorf("%s: outcome = %q; want relayed", tc.name, outcome)
			continue
		}
		events := em.recorded()[before:]
		if got := eventTypes(events); !reflect.DeepEqual(got, tc.types) {
			t.Errorf("%s: events = %v; want %v", tc.name, got, tc.types)
			continue
		}
		for _, ev := range events {
			if ev.Workspace != "widgets" || ev.ActorType != "forge" || ev.ActorID != "" || ev.ResourceType != tc.res || ev.ResourceID != tc.id {
				t.Errorf("%s: event = %+v; want the fixed fields", tc.name, ev)
			}
			if want := ev.EventType[strings.LastIndex(ev.EventType, ".")+1:]; ev.Action != want {
				t.Errorf("%s: %s has the action %q; want %q", tc.name, ev.EventType, ev.Action, want)
			}
			extraKeys := tc.extra
			if tc.extra == nil { // the GitLab update: the keys depend on the event
				extraKeys = map[string][]string{
					audit.EventForgeIssueEdited:    {"number", "title", "url", "labels"},
					audit.EventForgeIssueUnlabeled: {"number", "label", "labels", "url"},
					audit.EventForgeIssueLabeled:   {"number", "label", "labels", "url"},
				}[ev.EventType]
			}
			want := append(append([]string{}, base...), extraKeys...)
			sort.Strings(want)
			if got := keysOf(ev.Metadata); !reflect.DeepEqual(got, want) {
				t.Errorf("%s: %s metadata keys = %v; want %v", tc.name, ev.EventType, got, want)
			}
			if ev.Metadata["actor"] != tc.actor {
				t.Errorf("%s: actor = %v; want %q", tc.name, ev.Metadata["actor"], tc.actor)
			}
			if b, _ := json.Marshal(ev); strings.Contains(string(b), secretField) {
				t.Errorf("%s: the event holds a field of the payload nothing asked for: %s", tc.name, b)
			}
		}
	}
}

// withoutKey removes a top-level key from a JSON object.
func withoutKey(body, key string) string {
	var doc map[string]any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		panic(err)
	}
	delete(doc, key)
	b, _ := json.Marshal(doc)
	return string(b)
}

// A label that GitLab lists twice is one event, and the labels of an update without the top-level
// list are what the change says.
// Requirement: 32-REQ-7.2
func TestGitLabIssueUpdateLabelDetails(t *testing.T) {
	e, em := newIngressEnv(t)
	twice := gitlabUpdateHook(map[string]any{"labels": map[string]any{
		"previous": titles("old", "old"), "current": titles("a", "a", "b"),
	}}, "a", "b")
	if outcome := outcomeOf(t, e.deliver("widgets", gitlabDelivery(ingressSecret, "d-1", "Issue Hook", twice))); outcome != "relayed" {
		t.Fatalf("outcome = %q; want relayed", outcome)
	}
	if got := eventTypes(em.recorded()); !reflect.DeepEqual(got, []string{audit.EventForgeIssueUnlabeled, audit.EventForgeIssueLabeled, audit.EventForgeIssueLabeled}) {
		t.Errorf("events = %v; want one unlabeled and two labeled, each label once", got)
	}

	// No top-level labels: the labels the issue has are the current ones of the change.
	noList := withoutKey(gitlabUpdateHook(map[string]any{"labels": labelChange([]string{"x"}, []string{"y", "z"})}), "labels")
	if outcome := outcomeOf(t, e.deliver("widgets", gitlabDelivery(ingressSecret, "d-2", "Issue Hook", noList))); outcome != "relayed" {
		t.Fatalf("outcome = %q; want relayed", outcome)
	}
	last := em.recorded()[len(em.recorded())-1]
	if !reflect.DeepEqual(last.Metadata["labels"], []string{"y", "z"}) {
		t.Errorf("labels = %v; want the current labels of the change", last.Metadata["labels"])
	}

	// The URLs of the label events lose their user information too.
	secret := strings.Replace(gitlabUpdateHook(map[string]any{"labels": labelChange([]string{"x"}, []string{"y"})}, "y"),
		"https://gitlab.example.com", "https://user:secret@gitlab.example.com", 1)
	if outcome := outcomeOf(t, e.deliver("widgets", gitlabDelivery(ingressSecret, "d-3", "Issue Hook", secret))); outcome != "relayed" {
		t.Fatalf("outcome = %q; want relayed", outcome)
	}
	for _, ev := range em.recorded()[len(em.recorded())-2:] {
		if url, _ := ev.Metadata["url"].(string); strings.Contains(url, "secret") || !strings.HasPrefix(url, "https://gitlab.example.com/") {
			t.Errorf("%s url = %q; want it without user information", ev.EventType, url)
		}
	}
}

// An Issue Hook update without the iid of the issue is malformed, whatever it changes.
// Requirement: 32-REQ-5.1
func TestGitLabIssueUpdateWithoutAnIID(t *testing.T) {
	e, _ := newIngressEnv(t)
	body := gitlabPayload(map[string]any{
		"object_attributes": map[string]any{"action": "update"},
		"changes":           map[string]any{"labels": labelChange([]string{"x"}, []string{"y"})},
	})
	if rec := e.deliver("widgets", gitlabDelivery(ingressSecret, "d-1", "Issue Hook", body)); rec.Code != 400 {
		t.Errorf("status = %d; want 400", rec.Code)
	}
}

// merged says whether the merge request is merged: a merge action is, and so is a hook whose
// state is merged.
// Requirement: 32-REQ-8.2
func TestGitLabMergeRequestMergedFlag(t *testing.T) {
	e, em := newIngressEnv(t)
	for i, body := range []string{
		mergeRequestHook("merge", map[string]any{"state": "opened"}),
		mergeRequestHook("update", map[string]any{"oldrev": "abc", "state": "merged"}),
		mergeRequestHook("update", map[string]any{"oldrev": "abc", "state": "opened"}),
	} {
		if outcome := outcomeOf(t, e.deliver("widgets", gitlabDelivery(ingressSecret, fmt.Sprintf("d-%d", i), "Merge Request Hook", body))); outcome != "relayed" {
			t.Fatalf("hook %d: outcome = %q; want relayed", i, outcome)
		}
	}
	var merged []any
	for _, ev := range em.recorded() {
		merged = append(merged, ev.Metadata["merged"])
	}
	if !reflect.DeepEqual(merged, []any{true, true, false}) {
		t.Errorf("merged = %v; want true, true, false", merged)
	}
}
